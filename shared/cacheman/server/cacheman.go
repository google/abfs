// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"bytes"
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"runtime"
	runtimedebug "runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	rc "github.com/bazelbuild/remote-apis-sdks/go/pkg/client"
	"github.com/bazelbuild/remote-apis-sdks/go/pkg/rexec"
	"github.com/gofrs/flock"
	"github.com/iafan/cwalk"
	"github.com/prometheus/procfs"
	"go.uber.org/multierr"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
	"google.golang.org/grpc/status"

	"github.com/google/abfs/remoteconfig"
	"github.com/google/abfs/shared/ari"
	"github.com/google/abfs/shared/cacheman/protocol"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/dagsync"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/hashpath"
	"github.com/google/abfs/shared/instance"
	"github.com/google/abfs/shared/metrics"
	"github.com/google/abfs/shared/objects"
	"github.com/google/abfs/shared/objects/gitobjects"
	"github.com/google/abfs/shared/rbe"
	"github.com/google/abfs/shared/remote"
	"github.com/google/abfs/shared/remote/grpc/client"
	"github.com/google/abfs/shared/store/localcache"
	"github.com/google/abfs/shared/version"
)

// this can be enabled at runtime through the http socket with a command like:
//
//	`curl --unix-socket ~/.abfs/default/cache/http.socket -X PUT http://localhost/debug -d true`
var debug = false

var (
	relFetchMaxDepth = 5
	relFetchMaxCount = 200
)

type Printer interface {
	Printf(format string, a ...any)
	Println(a ...any)

	Fatalf(format string, a ...any)
	Fatal(a ...any)

	Close() error
}

const (
	defaultFetchPriority = 1.0
	gitPrefetchPriority  = 0.8
)

const (
	maintenanceDelay        = 3 * time.Hour
	localGCThreshold        = -1 * time.Hour
	remoteHealthCheckPeriod = 10 * time.Second
	HealthBypassFileName    = "health_bypass"
	StatusDirName           = "status"
	RemoteErrFileName       = "remote.err"
	CredsErrFileName        = "creds.err"
)

func SetDebug(d bool) {
	debug = d
}

const (
	ResultWait     = protocol.ResultWait
	ResultSuccess  = protocol.ResultSuccess
	ResultNotFound = protocol.ResultNotFound
	ResultFailed   = protocol.ResultFailed
	ResultTimeout  = protocol.ResultTimeout
)

type ABFSClient interface {
	Close() error
	AddMapResult(ctx context.Context, from, to entities.Hash) error
	GetObject(ctx context.Context, key remote.Hash) (remote.ContentReadCloser, error)
	GetRefs(ctx context.Context, server string, project string, refs []string) ([]entities.Hash, error)
	Headers(ctx context.Context, keys []remote.Hash) ([]entities.ObjectHeader, error)
	SHA256(ctx context.Context, keys []remote.Hash) ([]entities.SHA256, error)
	MapResults(ctx context.Context, id entities.Hash) ([]entities.Hash, error)
	PutObject(ctx context.Context, key remote.Hash, reader remote.ContentReadCloser) error
	PutRefs(ctx context.Context, server string, project string, refs []entities.RefHash) error
	SearchRefs(ctx context.Context, server string, project string, refPrefix string, limit int) ([]entities.ProjectRefHash, error)
	Mark(ctx context.Context, hash entities.Hash, key, value string) (int64, error)
	Version(ctx context.Context) (string, error)
	WhoAmI(ctx context.Context) (string, error)
}

type NewARESFunc func(address string, authInfo instance.AuthInfo) (remote.ARESService, error)
type CheckCredsFunc func(ctx context.Context) error

type ARESClient interface {
	remote.ARESService
	// Support returns whether remote execution is available
	Support() bool
}

type StatStore interface {
	Put(key entities.Hash, stat entities.StatInfo) error
	Get(key entities.Hash) (entities.StatInfo, bool)
}

type memStatStore struct {
	storage sync.Map
	// TODO: add cleanup to minimize memory usage
}

func (s *memStatStore) Put(key entities.Hash, stat entities.StatInfo) error {
	s.storage.Store(key, stat)
	return nil
}

func (s *memStatStore) Get(key entities.Hash) (entities.StatInfo, bool) {
	value, ok := s.storage.Load(key)
	if !ok {
		return entities.StatInfo{}, false
	}
	stat, ok := value.(entities.StatInfo)
	return stat, ok
}

type CacheMan struct {
	instance             *instance.Instance
	config               *instance.Config
	appliedRemoteConfig  *instance.Config
	applyErr             error
	incomingConfig       atomic.Pointer[instance.Config]
	newConfigC           chan struct{}
	newARES              NewARESFunc
	checkCredsFn         CheckCredsFunc
	healthStatusCode     atomic.Int32
	healthCheckGroup     singleflight.Group
	initDeeptreeGroup    singleflight.Group // see (*CacheMan).resolveDeeptree
	healthCheckTrigger   chan struct{}
	abfsClient           ABFSClient
	ariClient            ari.BlobClient
	aresClient           ARESClient
	cancelOverridesWatch func()
	cancelSync           func()
	serverCtx            context.Context // outlives any one connection; see resolveDeeptree
	cancelServerCtx      context.CancelFunc
	tunnels              []*tunnel
	identityStore        rbe.IdentityStore
	store                *localcache.LocalCache
	index                Index
	maps                 *Maps
	socket               net.Listener
	httpSocket           net.Listener
	httpServer           *http.Server
	socketLock           *flock.Flock
	fetchObjReqs         *FetchReqQueue
	fetchBlobsStat       *protocol.QueueStat
	fetchTreesStat       *protocol.QueueStat
	fetchObjectsStat     *protocol.QueueStat
	fetchARIsStat        *protocol.QueueStat
	fetchCASBlobsStat    *protocol.QueueStat
	fetchMapsReqs        *FetchReqQueue
	fetchMapsStat        *protocol.QueueStat
	indexObjReqs         *IndexReqQueue
	newObjReqs           *NewObjectReqQueue
	pendingAdd           *PendingAddTracker
	notifyUseAgg         *NotifyUseAggregator
	activeWorkers        sync.WaitGroup

	relFetchObjectReqs chan RelativeFetchReq
	relFetchObjectStat *protocol.QueueStat
	// closeChan is closed when cacheman is stopped
	closeChan       chan struct{}
	sync            atomic.Pointer[dagsync.DAGSync]
	periodicLogging bool
	pushBlobsStat   *protocol.PushStat
	pushObjectsStat *protocol.PushStat
	pushTreesStat   *protocol.PushStat
	pushRefsStat    *protocol.PushStat
	pushMapsStat    *protocol.PushStat
	phonyStat       *protocol.PushStat
	executeStat     *protocol.ExecutionStat

	statStore StatStore

	printer              Printer
	cacheBlockSize       int64
	requestCallbacksLock sync.Mutex
	requestCallbacks     map[entities.Hash][]func(error)

	// Serializes full-cache reconciliation (maintenanceWork), explicit cache cleanup (clean),
	// and shutdown teardown (Stop). Lock order: maintenanceLock -> index.evictCh.
	maintenanceLock sync.Mutex
	remoteSetupLock sync.Mutex

	provider       metrics.Provider
	memGauge       metrics.Gauge
	cacheSizeGauge metrics.Gauge
	evictionGauge  metrics.Gauge

	buildDurationHist      metrics.Histogram
	buildCacheHitRatioHist metrics.Histogram
	buildPhaseDurationHist metrics.Histogram
	buildCacheHitsTotal    metrics.Counter
	buildCacheMissesTotal  metrics.Counter
}

func New(inst *instance.Instance, newARES NewARESFunc) (*CacheMan, error) {
	provider := metrics.GetProvider()
	serverCtx, cancelServerCtx := context.WithCancel(context.Background())
	c := &CacheMan{
		instance:           inst,
		config:             new(instance.Config),
		newConfigC:         make(chan struct{}, 1),
		healthCheckTrigger: make(chan struct{}, 1),
		newARES:            newARES,
		printer:            new(DefaultPrinter),
		serverCtx:          serverCtx,
		cancelServerCtx:    cancelServerCtx,
		fetchObjReqs:       NewFetchReqQueue(),
		fetchBlobsStat:     new(protocol.QueueStat),
		fetchTreesStat:     new(protocol.QueueStat),
		fetchObjectsStat:   new(protocol.QueueStat),
		fetchARIsStat:      new(protocol.QueueStat),
		fetchCASBlobsStat:  new(protocol.QueueStat),
		fetchMapsReqs:      NewFetchReqQueue(),
		fetchMapsStat:      new(protocol.QueueStat),
		indexObjReqs:       NewIndexReqQueue(),
		newObjReqs:         NewNewObjectReqQueue(),
		pendingAdd:         NewPendingAddTracker(),
		notifyUseAgg:       NewNotifyUseAggregator(),
		relFetchObjectReqs: make(chan RelativeFetchReq, channelBufferMax),
		relFetchObjectStat: new(protocol.QueueStat),
		statStore: &memStatStore{
			storage: sync.Map{},
		},
		index:            &EmptyIndex{},
		closeChan:        make(chan struct{}),
		pushBlobsStat:    new(protocol.PushStat),
		pushObjectsStat:  new(protocol.PushStat),
		pushTreesStat:    new(protocol.PushStat),
		pushRefsStat:     new(protocol.PushStat),
		pushMapsStat:     new(protocol.PushStat),
		phonyStat:        new(protocol.PushStat),
		executeStat:      new(protocol.ExecutionStat),
		requestCallbacks: make(map[entities.Hash][]func(error)),
		provider:         provider,
		memGauge: provider.NewGauge(
			"abfs_cacheman_memory_bytes",
			"Resident Set Size (RSS) memory used by the cacheman daemon.",
			nil,
		),
		cacheSizeGauge: provider.NewGauge(
			"abfs_cacheman_local_cache_size_bytes",
			"Total disk space used by the ABFS local cache.",
			nil,
		),
		evictionGauge: provider.NewGauge(
			"abfs_cacheman_eviction_total",
			"Total number of objects evicted from the local cache.",
			nil,
		),
		buildDurationHist: provider.NewHistogram(
			"abfs_build_duration_seconds",
			"Total wall-clock duration of an ABFS build.",
			[]float64{30, 60, 120, 180, 240, 300, 600, 900, 1200, 1800, 3600, 7200},
			[]string{"build_goal", "status", "android_target"},
		),
		buildCacheHitRatioHist: provider.NewHistogram(
			"abfs_build_cache_hit_ratio",
			"Ratio of cache hits (0.0 to 1.0) for the build.",
			[]float64{0.0, 0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 0.95, 0.99, 1.0},
			[]string{"build_goal", "status", "android_target"},
		),
		buildPhaseDurationHist: provider.NewHistogram(
			"abfs_build_phase_duration_seconds",
			"Time spent in various ninja build phases.",
			[]float64{0.1, 1, 10, 30, 60, 120, 300, 600, 1200, 1800, 3600, 7200},
			[]string{"build_goal", "status", "android_target", "phase"},
		),
		buildCacheHitsTotal: provider.NewCounter(
			"abfs_build_cache_hits_total",
			"Total number of cache hits in builds.",
			[]string{"build_goal", "status", "android_target"},
		),
		buildCacheMissesTotal: provider.NewCounter(
			"abfs_build_cache_misses_total",
			"Total number of cache misses in builds.",
			[]string{"build_goal", "status", "android_target"},
		),
	}
	inst.RegisterConfigChange(func(config *instance.Config) {
		c.incomingConfig.Store(config)
		select {
		case c.newConfigC <- struct{}{}:
		default:
		}
	})
	if err := inst.StartWatchingConfigFile(); err != nil {
		return nil, err
	}

	c.httpServer = &http.Server{
		Handler: c.httpHandler(),
	}
	// Initialize to local only clients and let setupRemote overwrite.
	c.abfsClient = &localOnlyClient{}
	c.aresClient = &emptyARES{}

	if err := c.applyLocalConfig(inst.Config, false); err != nil {
		return nil, err
	}

	// Assume unconnected until we have confirmed.
	if !c.config.LocalOnly {
		c.healthStatusCode.Store(int32(syscall.ENOTCONN))
	}

	return c, nil
}

func (c *CacheMan) noRemotePush() bool {
	return c.config.LocalOnly || c.config.RemoteReadOnly
}

func (c *CacheMan) isDAGSyncChanged(config *instance.Config) bool {
	if c.isRemoteChanged(config) {
		return true
	}
	if c.appliedRemoteConfig == nil {
		return true
	}
	if c.appliedRemoteConfig.ConcurrentUploads != config.ConcurrentUploads {
		return true
	}
	if c.appliedRemoteConfig.OfflineThreshold != config.OfflineThreshold {
		return true
	}

	return false
}

func (c *CacheMan) setupDAGSync(ctx context.Context, config *instance.Config, force bool) {
	if !force && !c.isDAGSyncChanged(config) {
		return
	}
	if c.cancelSync != nil {
		c.cancelSync()
		c.cancelSync = nil
	}
	c.sync.Store(nil)
	if c.noRemotePush() {
		return
	}
	onPush := func(hash entities.Hash, reqType string) {
		// IndexEntries won't be present for ref, map, or phony
		// request types.
		if reqType == "ref" || reqType == "map" || reqType == "phony" {
			return
		}

		if err := c.markObjectRemote(hash, reqType); err != nil {
			c.printer.Printf("Unable to mark hash: %s remote, err: %v\n", hash, err)
		}
	}
	peekOffline := func(hash entities.Hash) (bool, bool) {
		entry := c.index.Get(hash)
		if entry == nil {
			return false, false
		}

		return entry.Offline, true
	}
	sync := dagsync.New(&dagsync.DagSyncOpts{
		Updater:          c.abfsClient,
		Accessor:         &dagSyncObjectAccessor{cache: c.store},
		OnPush:           onPush,
		WorkersCount:     config.ConcurrentUploads,
		OfflineThreshold: config.OfflineThreshold,
		PeekOffline:      peekOffline,
		Logger:           log.New(&printerLogWriter{c: c}, "", 0),
	})

	syncCtx, syncCancel := context.WithCancel(ctx)
	c.cancelSync = syncCancel
	c.sync.Store(sync)
	go sync.Start(syncCtx)
}

func (c *CacheMan) getSync() *dagsync.DAGSync {
	return c.sync.Load()
}

type printerLogWriter struct {
	c *CacheMan
}

func (w *printerLogWriter) Write(p []byte) (n int, err error) {
	w.c.printer.Printf("%s", p)
	return len(p), nil
}

func (c *CacheMan) Printer() Printer {
	return c.printer
}

func (c *CacheMan) insertNextRelativeFetchReq(ctx context.Context, req *RelativeFetchReq, t entities.ObjectType) error {
	var nextFetch []entities.Hash
	var nextFetchType []entities.ObjectType
	var nextReqs []RelativeFetchReq

	if stat, err := c.store.Stat(ctx, req.Hash); err != nil {
		return err
	} else {
		if err := c.statStore.Put(req.Hash, stat); err != nil {
			return err
		}
	}

	switch t {
	case gitobjects.ObjectTypeDir:
		hash := req.Hash
		treeb, meta, err := c.store.GetTree(ctx, hash)
		if err != nil {
			return err
		}
		tree, err := entities.DecodeDir(treeb, meta)
		if err != nil {
			return err
		}
		for _, ent := range tree {
			t := entities.ObjectTypeBlob
			if ent.Mode == entities.FileModeDir {
				t = gitobjects.ObjectTypeDir
				nextFetch = append(nextFetch, ent.Hash)
				nextFetchType = append(nextFetchType, gitobjects.ObjectTypeDir)
			}
			if ent.Mode == entities.FileModeRegular || ent.Mode == entities.FileModeExecutable {
				err := c.statStore.Put(ent.Hash, entities.StatInfo{
					Size:    ent.Size,
					Mode:    ent.Mode,
					GitType: t,
				})
				if err != nil {
					return err
				}
			}
		}

		// TODO: add relativeObject of commit object
	}

	for i, h := range nextFetch {
		t := nextFetchType[i]
		if stat, ok := req.FetchStat.Next(); ok {
			nextReq := RelativeFetchReq{
				FetchReq: FetchReq{
					Hash:    h,
					ObjType: protocol.TypeObject,
				},
				FetchStat:   &stat,
				RequestType: t,
			}
			cb := func(err error) {
				if err != nil {
					c.printer.Printf("Failed to fetch (type=%s) for %s: %v\n", t, req.Hash, err)
				} else {
					if err := c.insertNextRelativeFetchReq(ctx, &nextReq, t); err != nil {
						c.printer.Printf("Failed to insert next relative fetch req for %s: %v\n", req.Hash, err)
					}
				}
			}
			nextReq.callback = cb
			nextReqs = append(nextReqs, nextReq)
		}
	}

	for _, req := range nextReqs {
		c.queueFetchReq(req.FetchReq)
	}
	return nil
}

func (f *FetchReq) Callback(err error) {
	if f.callback == nil {
		return
	}
	if st, ok := status.FromError(err); ok && st != nil {
		f.callback(st.Err())
	} else {
		f.callback(err)
	}
}

const (
	channelBufferMax = 1000000
)

type restoreState struct {
	index Index
	maps  *Maps
}

func restoreStateWithClean(printer Printer, rootDir string, logDir string, enableIndex bool, cache *localcache.LocalCache, evictThreshold uint64) (restoreState, error) {
	var (
		index Index
		maps  *Maps
		err   error
	)

	restore := func() error {
		if enableIndex {
			t := time.Now()
			printer.Println("Restoring Index...")
			index, err = setupIndex(rootDir, logDir, printer, cache, evictThreshold)
			if err != nil {
				return err
			}
			printer.Println("Restoring Index took", time.Since(t))
		} else {
			index = &EmptyIndex{}
		}
		t := time.Now()
		printer.Println("Restoring Maps...")
		maps, err = setupMaps(rootDir, printer)
		if err != nil {
			return err
		}
		printer.Println("Restoring Maps took", time.Since(t))

		return nil
	}
	if err := restore(); err != nil {
		// Clean and try again
		printer.Printf("Restoring state failed with err: %v, cleaning cache...\n", err)
		if err := clean(rootDir); err != nil {
			return restoreState{}, err
		}
		if err := restore(); err != nil {
			return restoreState{}, clierr.CLIErr("Failed to restore state: %v. Try `abfs cacheman clean -af`", err)
		}
	}

	return restoreState{index: index, maps: maps}, nil
}

func setupIndex(rootDir string, logDir string, printer Printer, cache *localcache.LocalCache, evictThreshold uint64) (Index, error) {
	trackUse := evictThreshold > 0
	opts := &IndexOptions{
		IndexStore:     NewBadgerIndexStore(path.Join(rootDir, "index"), printer, MaxEvictBatchSize, trackUse),
		Cache:          cache,
		EvictThreshold: evictThreshold,
		Printer:        printer,
		LogDir:         logDir,
	}
	index, err := NewIndex(opts)
	if err != nil {
		return nil, err
	}

	return index, nil
}

func setupMaps(rootDir string, printer Printer) (*Maps, error) {
	mapStore := NewBadgerMapStore(path.Join(rootDir, "map"), printer, DefaultTTLHours)
	m, err := NewMaps(mapStore)
	if err != nil {
		return nil, err
	}

	return m, nil
}

func (c *CacheMan) setupTunnels(config *instance.Config, force bool) error {
	if !force && !c.isRemoteChanged(config) {
		return nil
	}

	c.closeTunnels()

	if len(config.TunnelPorts) > 0 {
		if len(config.TunnelPorts) != len(config.RemoteServers) {
			return fmt.Errorf("tunnel ports and remote servers must have the same length")
		}
	}

	c.tunnels = make([]*tunnel, len(config.TunnelPorts))
	for i, port := range config.TunnelPorts {
		if port > 0 {
			c.printer.Printf("Setting up port forward on %d to %s...\n", port, config.RemoteServers[i])
			conn, err := PortForward(c.printer, config.RemoteServers[i], port)
			if err != nil {
				c.printer.Printf("Warning: failed to setup tunnel on port %d: %v\n", port, err)
				continue
			}
			c.tunnels[i] = conn
		}
	}
	return nil
}

func (c *CacheMan) closeTunnels() {
	for _, c := range c.tunnels {
		if c != nil {
			c.Close()
		}
	}
	c.tunnels = nil
}

func (c *CacheMan) isCacheChanged(config *instance.Config) bool {
	if c.config.CacheDir != config.CacheDir {
		return true
	}
	if c.config.EvictThreshold != config.EvictThreshold {
		return true
	}
	if c.config.FsyncObjects != config.FsyncObjects {
		return true
	}
	if c.config.NoObjectCompression != config.NoObjectCompression {
		return true
	}
	if c.config.EnableIndex != config.EnableIndex {
		return true
	}
	return false
}

func (c *CacheMan) openStore(config *instance.Config, force bool) error {
	if !force && !c.isCacheChanged(config) {
		return nil
	}

	if c.store != nil {
		if err := c.store.Close(); err != nil {
			return err
		}
	}

	cacheOpts := []localcache.CacheOpt{localcache.FsyncOpt(config.FsyncObjects)}
	if config.NoObjectCompression {
		cacheOpts = append(cacheOpts, localcache.NoObjectCompressionOpt())
	}
	cache, err := localcache.NewCache(config.CacheDir, cacheOpts...)
	exclusive := false
	if err != nil {
		if errors.Is(err, localcache.ErrIncompatibleExisting) {
			exclusiveOpts := append(cacheOpts, localcache.BypassCompatCheckOpt(), localcache.ExclusiveLockOpt())
			cache, err = localcache.NewCache(config.CacheDir, exclusiveOpts...)
			exclusive = true
		}
	}
	if err != nil {
		return err
	}
	defer func() {
		if exclusive {
			// The flock package we use does not ideally update internal state on
			// lock state transitions.
			// e.g. If we want to go from exclusive -> shared -> exclusive, the
			// final exclusive attempt is short circuited and ignored. Inserting an
			// Unlock() between the transition will clear state.
			if err := cache.Unlock(); err != nil {
				c.printer.Printf("cache.Unlock failed: %v\n", err)
			}
			if err := cache.RLock(); err != nil {
				c.printer.Printf("cache.RLock failed: %v\n", err)
			}
		}

		if err := cache.Close(); err != nil {
			c.printer.Printf("cache.Close failed: %v\n", err)
		}
	}()

	if !cache.CompatibleExistingCache() {
		c.printer.Println("Uncorrectable cache incompatability detected. Cleaning cache...")
		err := uprevCacheUncorrectable(config.CacheDir)
		if err != nil {
			return err
		}
	}

	c.maintenanceLock.Lock()
	defer c.maintenanceLock.Unlock()

	if err := c.index.Close(); err != nil {
		return fmt.Errorf("failed to close index: %w", err)
	}

	if c.maps != nil {
		if err := c.maps.Close(); err != nil {
			return fmt.Errorf("failed to close maps: %w", err)
		}
	}

	restoreState, err := restoreStateWithClean(c.printer, config.CacheDir, config.LogDir, config.EnableIndex, cache, config.EvictThreshold)
	if err != nil {
		return err
	}

	c.index = restoreState.index
	c.maps = restoreState.maps
	c.store = cache

	return nil
}

func (c *CacheMan) applyLocalConfig(config *instance.Config, force bool) (applyErr error) {
	defer func() {
		c.applyErr = applyErr
	}()

	if !force && c.applyErr != nil {
		force = true
	}

	if err := c.setupLogPrinter(config, force); err != nil {
		return err
	}

	if err := c.listenUDSes(config, force); err != nil {
		return err
	}

	if err := c.openStore(config, force); err != nil {
		return err
	}

	if err := c.populateBlockSize(config.CacheDir); err != nil {
		return err
	}

	c.config = config
	return nil
}

func (c *CacheMan) setupRemote(ctx context.Context, config *instance.Config, force bool) error {
	c.remoteSetupLock.Lock()
	defer c.remoteSetupLock.Unlock()

	if !force && !c.isRemoteChanged(config) {
		return nil
	}

	if err := c.setupTunnels(config, force); err != nil {
		c.printer.Printf("Warning: failed to setup tunnels: %v\n", err)
	}

	if err := c.setupABFSClient(config, force); err != nil {
		c.printer.Printf("Warning: failed to setup ABFS client: %v\n", err)
	}

	if err := c.setupRemoteConfig(config, force); err != nil {
		c.printer.Printf("Warning: failed to setup remote config: %v\n", err)
	}
	// Re-read config after remote config is applied
	config = c.instance.Config

	if err := c.setupARESClient(config, c.newARES, force); err != nil {
		c.printer.Printf("Warning: failed to setup ARES client: %v\n", err)
	}

	if err := c.setupRBE(config, force); err != nil {
		c.printer.Printf("Warning: failed to setup RBE: %v\n", err)
	}

	if err := c.setupARI(config, force); err != nil {
		c.printer.Printf("Warning: failed to setup ARI: %v\n", err)
	}

	c.setupDAGSync(ctx, config, force)

	c.appliedRemoteConfig = config
	return nil
}

func (c *CacheMan) setupRBE(config *instance.Config, force bool) error {
	if !force && !c.isRemoteChanged(config) {
		return nil
	}

	if !config.RBEAsCAS {
		return nil
	}

	if c.identityStore != nil {
		if err := c.identityStore.Close(); err != nil {
			return fmt.Errorf("failed to close identity store: %w", err)
		}
	}

	idStore, err := rbe.NewBadgerIdentityStore(path.Join(config.CacheDir, "identity"))
	if err != nil {
		return fmt.Errorf("failed to open identity store: %w", err)
	}
	if c.identityStore != nil {
		if err := c.identityStore.Close(); err != nil {
			return fmt.Errorf("failed to close identity store: %w", err)
		}
	}
	c.identityStore = idStore

	rbeRemote := config.RBERemote

	ctx := context.Background()
	// Initialize RBE SDK client with timeouts suitable for large repositories (e.g. Android Superproject).
	rbeClient, err := rc.NewClient(ctx, config.RBEInstance, rc.DialParams{
		Service:               rbeRemote,
		NoSecurity:            false,
		UseApplicationDefault: true,
	}, rc.RPCTimeouts{
		"GetTree":          15 * time.Minute,
		"BatchReadBlobs":   5 * time.Minute,
		"BatchUpdateBlobs": 5 * time.Minute,
	})
	if err != nil {
		if err := idStore.Close(); err != nil {
			c.printer.Printf("Error closing identity store: %v\n", err)
		}
		return fmt.Errorf("failed to initialize RBE client: %w", err)
	}

	// Wrap the original abfsClient with RBEClient.
	// TODO: Rename abfsClient to something more generic.
	c.abfsClient = rbe.NewRBEClient(&rexec.Client{GrpcClient: rbeClient}, idStore)
	c.ariClient = ari.NewClient(&rexec.Client{GrpcClient: rbeClient})
	return nil
}

func (c *CacheMan) setupARI(config *instance.Config, force bool) error {
	if !force && !c.isRemoteChanged(config) {
		return nil
	}

	if config.RBEAsCAS {
		// ariClient is already initialized from shared rbeClient in setupRBE.
		return nil
	}

	if config.RBEInstance == "" || config.RBERemote == "" {
		if config.LocalOnly {
			return nil
		}
		c.ariClient = nil
		return nil
	}

	ctx := context.Background()
	rbeClient, err := rc.NewClient(ctx, config.RBEInstance, rc.DialParams{
		Service:               config.RBERemote,
		NoSecurity:            false,
		UseApplicationDefault: true,
	})
	if err != nil {
		c.printer.Printf("Warning: failed to initialize RBE client for ARI: %v\n", err)
		c.ariClient = nil
		return nil
	}

	c.ariClient = ari.NewClient(&rexec.Client{GrpcClient: rbeClient})
	return nil
}

func (c *CacheMan) listenUDSes(config *instance.Config, force bool) error {
	if !force && c.config.CacheDir == config.CacheDir {
		return nil
	}

	c.closeUDSes()

	socketPath := path.Join(config.CacheDir, "socket")
	httpSocketPath := path.Join(config.CacheDir, "http.socket")
	lock := flock.New(socketPath + ".lock")
	locked, err := lock.TryLock()
	if err != nil {
		return err
	}
	if !locked {
		return fmt.Errorf("the cacheman socket is locked")
	}

	_ = os.Remove(socketPath)
	binSocket, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}

	_ = os.Remove(httpSocketPath)
	httpSocket, err := net.Listen("unix", httpSocketPath)
	if err != nil {
		return err
	}

	c.socketLock = lock
	c.socket = binSocket
	c.httpSocket = httpSocket

	return nil
}

func (c *CacheMan) closeUDSes() {
	if c.socket != nil {
		if err := c.socket.Close(); err != nil {
			c.printer.Printf("Error closing socket: %v\n", err)
		}
		c.socket = nil
	}
	if c.httpSocket != nil {
		err := c.httpSocket.Close()
		if err != nil {
			c.printer.Printf("Error closing http socket: %v\n", err)
		}
		c.httpSocket = nil
	}
	if c.socketLock != nil {
		if err := c.socketLock.Unlock(); err != nil {
			c.printer.Printf("Error unlocking socket: %v\n", err)
		}
		c.socketLock = nil
	}
}

func (c *CacheMan) setupLogPrinter(config *instance.Config, force bool) error {
	if !force && c.config.LogDir == config.LogDir {
		return nil
	}

	logPath := filepath.Join(config.LogDir, "cacheman")

	if err := os.MkdirAll(path.Dir(logPath), 0755); err != nil {
		return err
	}

	printer, err := NewResilientPrinter(logPath)
	if err != nil {
		return fmt.Errorf("failed to create resilient printer: %w", err)
	}
	if err := c.printer.Close(); err != nil {
		log.Printf("Error closing printer: %v\n", err)
	}
	c.printer = printer
	return nil
}

func (c *CacheMan) populateBlockSize(cacheDir string) error {
	var statfs syscall.Statfs_t
	if err := syscall.Statfs(cacheDir, &statfs); err != nil {
		return err
	}

	c.cacheBlockSize = statfs.Bsize

	return nil
}

func (c *CacheMan) isRemoteChanged(config *instance.Config) bool {
	if c.appliedRemoteConfig == nil {
		return true
	}
	if c.appliedRemoteConfig.RBEAsCAS != config.RBEAsCAS {
		return true
	}
	if c.appliedRemoteConfig.RBERemote != config.RBERemote {
		return true
	}
	if c.appliedRemoteConfig.RBEInstance != config.RBEInstance {
		return true
	}
	if c.appliedRemoteConfig.LocalOnly != config.LocalOnly {
		return true
	}
	if c.appliedRemoteConfig.RemoteReadOnly != config.RemoteReadOnly {
		return true
	}
	if !reflect.DeepEqual(c.appliedRemoteConfig.RemoteServers, config.RemoteServers) {
		return true
	}
	if !reflect.DeepEqual(c.appliedRemoteConfig.TunnelPorts, config.TunnelPorts) {
		return true
	}
	if !reflect.DeepEqual(c.appliedRemoteConfig.GetAuthInfo(), config.GetAuthInfo()) {
		return true
	}
	return false
}

func (c *CacheMan) setupABFSClient(config *instance.Config, force bool) error {
	if !force && !c.isRemoteChanged(config) {
		return nil
	}

	if c.abfsClient != nil {
		if err := c.abfsClient.Close(); err != nil {
			c.printer.Printf("Error closing abfs client: %v\n", err)
		}
	}
	c.abfsClient = &localOnlyClient{}
	if c.ariClient != nil {
		if err := c.ariClient.Close(); err != nil {
			c.printer.Printf("Error closing ari client: %v\n", err)
		}
		c.ariClient = nil
	}

	if config.LocalOnly || config.RBEAsCAS {
		c.abfsClient = &localOnlyClient{}
		return nil
	}

	abfsServers := config.EffectiveRemoteServers()
	if len(abfsServers) <= 0 {
		c.printer.Println("No remote servers configured, running with local-only fallback")
		c.abfsClient = &localOnlyClient{}
		return nil
	}

	abfsClient, err := client.NewABFSClient(&client.ABFSClientOpts{
		Servers:         abfsServers,
		AuthInfo:        config.GetAuthInfo(),
		SkipHealthCheck: true,
	})
	if err != nil {
		return err
	}

	c.abfsClient = abfsClient

	return nil
}

func (c *CacheMan) setupRemoteConfig(config *instance.Config, force bool) error {
	if c.cancelOverridesWatch != nil {
		c.cancelOverridesWatch()
		c.cancelOverridesWatch = nil
	}

	if config.LocalOnly || config.RBEAsCAS {
		return nil
	}

	if !force && !c.isRemoteChanged(config) {
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	c.cancelOverridesWatch = cancel
	if err := c.watchClientsOverrides(ctx); err != nil {
		return err
	}

	return nil
}

func (c *CacheMan) setupARESClient(config *instance.Config, newARES NewARESFunc, force bool) error {
	if !force && !c.isRemoteChanged(config) {
		return nil
	}

	if config.LocalOnly {
		c.aresClient = &emptyARES{}
		return nil
	}

	if newARES == nil {
		c.printer.Println("Remote execution is not set")
		c.aresClient = &emptyARES{}
		return nil
	}

	abfsServers := config.EffectiveRemoteServers()
	if len(abfsServers) <= 0 {
		c.printer.Println("Remote execution is not set")
		c.aresClient = &emptyARES{}
		return nil
	}
	client, err := newARES(abfsServers[0], config.GetAuthInfo())
	if err != nil {
		c.printer.Printf("Remote execution not supported in current configuration. Only local execution will be used: %v\n", err)
		c.aresClient = &emptyARES{}
		return nil
	}

	c.printer.Println("Remote execution is supported")
	c.aresClient = &aresSupported{client}
	return nil
}

func (c *CacheMan) watchClientsOverrides(ctx context.Context) error {
	if _, ok := c.abfsClient.(*localOnlyClient); ok {
		return nil
	}
	hp, err := hashpath.Parse(instance.ClientsConfigsHashPath)
	if err != nil {
		return err
	}

	go remoteconfig.WatchConfig(ctx, hp, remote.NewRemoteStore(c.abfsClient), func(overrides *instance.ConfigRemoteOverrides, hash entities.Hash) {
		c.instance.SetNewRemoteConfig(c.resolveRemoteConfig(overrides))
	})

	return nil
}

func (c *CacheMan) resolveRemoteConfig(overrides *instance.ConfigRemoteOverrides) *instance.ConfigOptional {
	clientInfo := instance.ClientInfo{
		User:    c.instance.User,
		Class:   c.instance.Config.Class,
		Version: version.Version(),
	}

	return instance.ResolveRemoteConfig(clientInfo, overrides)
}

func clean(rootDir string) error {
	return errors.Join(
		entities.IgnoreNotExistErr(cleanDir(path.Join(rootDir, "objects"), nil)),
		entities.IgnoreNotExistErr(cleanDir(path.Join(rootDir, "blobs"), nil)),
		entities.IgnoreNotExistErr(cleanDirAll(path.Join(rootDir, "map"))),
		entities.IgnoreNotExistErr(cleanDir(path.Join(rootDir, "tmp"), nil)),
		entities.IgnoreNotExistErr(cleanDir(path.Join(rootDir, "index"), nil)),
	)
}

// uprevCacheUncorrectable wipes the entire cache and should be used when
// a version uprev is not possible or too expensive to correct in place.
func uprevCacheUncorrectable(rootDir string) error {
	if err := clean(rootDir); err != nil {
		return err
	}
	return localcache.WriteCacheVersion(rootDir, localcache.CacheVersion)
}

func timeoutFromStrOrDefault(timeoutStr string, defaultTimeout time.Duration) (time.Duration, error) {
	timeout := defaultTimeout
	if timeoutStr != "" {
		n, err := strconv.ParseInt(timeoutStr, 10, 32)
		if err != nil {
			return timeout, fmt.Errorf("cannot parse int timeout: %s", err)
		}
		timeout = time.Duration(n) * time.Second
	}

	return timeout, nil
}

func (c *CacheMan) logPanic() {
	if p := recover(); p != nil {
		c.printer.Printf("panic %v\n", p)
		runtimedebug.PrintStack()
	}
}

func (c *CacheMan) registerCallback(req FetchReq) (isFirst bool) {
	c.requestCallbacksLock.Lock()
	defer c.requestCallbacksLock.Unlock()

	callbacks, exists := c.requestCallbacks[req.Hash]
	callbacks = append(callbacks, req.Callback)
	c.requestCallbacks[req.Hash] = callbacks
	return !exists
}

func (c *CacheMan) performCallbacks(hash entities.Hash, err error) {
	c.requestCallbacksLock.Lock()
	callbacks := c.requestCallbacks[hash]
	delete(c.requestCallbacks, hash)
	c.requestCallbacksLock.Unlock()

	for _, cb := range callbacks {
		cb(err)
	}
}

func (c *CacheMan) fetchBlob(ctx context.Context, hash entities.Hash) (int64, error) {
	var retErr error
	reader, err := c.abfsClient.GetObject(ctx, hash)
	if err != nil {
		return 0, err
	}
	defer entities.CloseErrCheck(reader, &retErr)

	blobSize, err := objects.ReadBlobHeader(reader)
	if err != nil {
		return 0, err
	}

	if err := c.store.WriteBlob(ctx, hash, reader); err != nil {
		return 0, err
	}
	// Return the disk block size used for this blob so callers avoid re-reading disk.
	return bytesUsed(blobSize, c.cacheBlockSize), nil
}

type countingReader struct {
	reader io.Reader
	count  int64
}

func (cr *countingReader) Read(p []byte) (int, error) {
	n, err := cr.reader.Read(p)
	cr.count += int64(n)
	return n, err
}

func (c *CacheMan) fetchARI(ctx context.Context, ari string, hash entities.Hash) (int64, error) {
	var retErr error
	if c.ariClient == nil {
		return 0, errors.New("no client configured to fetch ARI")
	}
	reader, err := c.ariClient.FetchBlob(ctx, ari)
	if err != nil {
		return 0, err
	}
	defer entities.CloseErrCheck(reader, &retErr)

	if hash.IsZero() {
		hash = entities.Hash(sha1.Sum([]byte(ari)))
	}

	cr := &countingReader{reader: reader}
	if err := c.store.WriteARI(ctx, hash, cr); err != nil {
		return 0, err
	}
	// Return the disk block size computed from measured bytes written.
	return bytesUsed(cr.count, c.cacheBlockSize), nil
}

func (c *CacheMan) fetchCASBlob(ctx context.Context, sha256Hex string, size int64) (int64, error) {
	if c.store != nil {
		has, err := c.store.HasCASBlob(ctx, sha256Hex)
		if err == nil && has {
			return bytesUsed(size, c.cacheBlockSize), nil
		}
	}
	if c.ariClient == nil {
		return 0, errors.New("no client configured to fetch CAS blob from RBE")
	}
	ariStr := fmt.Sprintf("rbe:%s:%d", sha256Hex, size)
	reader, err := c.ariClient.FetchBlob(ctx, ariStr)
	if err != nil {
		return 0, err
	}
	var retErr error
	defer entities.CloseErrCheck(reader, &retErr)

	cr := &countingReader{reader: reader}
	if err := c.store.WriteCASBlob(ctx, sha256Hex, cr); err != nil {
		return 0, err
	}
	return bytesUsed(cr.count, c.cacheBlockSize), nil
}

func (c *CacheMan) isStopped(ctx context.Context) bool {
	if ctx != nil && ctx.Err() != nil {
		return true
	}
	select {
	case <-c.closeChan:
		return true
	default:
		return false
	}
}

func (c *CacheMan) fetchObjectsWorker(ctx context.Context) {
	defer c.logPanic()
	c.activeWorkers.Add(1)
	defer c.activeWorkers.Done()

	for {
		if c.isStopped(ctx) {
			break
		}
		req, ok := c.fetchObjReqs.Pop()
		if !ok || c.isStopped(ctx) {
			break
		}

		c.handleFetchReq(ctx, req)
	}
}

func (c *CacheMan) indexObjectsWorker(ctx context.Context) {
	defer c.logPanic()
	c.activeWorkers.Add(1)
	defer c.activeWorkers.Done()

	for {
		if c.isStopped(ctx) {
			break
		}
		req, ok := c.indexObjReqs.Pop()
		if !ok || c.isStopped(ctx) {
			break
		}

		objType := protocol.ParseRequestType(req.RawType)
		if objType != req.ObjType {
			actualStat := c.fetchQueueStat(objType)
			actualStat.Output.Add(1)
			actualStat.Input.Add(1)
			req.ReqStat.Output.Add(-1)
			req.ReqStat.Input.Add(-1)
		}
		if _, err := c.addFetchedObjectToIndexWithSize(req.Hash, req.EntryType, req.Size); err != nil {
			c.printer.Printf("Failed to index fetched object %s: %v\n", req.Hash, err)
			req.ReqStat.Errors.Add(1)
		}
		if c.pendingAdd != nil {
			c.pendingAdd.Remove(req.Hash)
		}
	}
}

// EnqueueNewObject pushes locally created object notifications to an asynchronous
// queue for background indexing and eager-push without blocking the socket reader.
func (c *CacheMan) EnqueueNewObject(hash entities.Hash, objType protocol.RequestObjectType) {
	c.newObjReqs.Push(NewObjectReq{Hash: hash, ObjType: objType})
}

// NotifyUse records a touched hash into an in-memory deduplicated aggregator,
// which is flushed periodically in batches by notifyUseWorker.
func (c *CacheMan) NotifyUse(hash entities.Hash) {
	c.notifyUseAgg.Add(hash)
}

// newObjectsWorker drains the newObjReqs queue and processes local object
// metadata calculation, BadgerDB index insertion, and remote eager-push in the background.
func (c *CacheMan) newObjectsWorker(ctx context.Context) {
	defer c.logPanic()
	c.activeWorkers.Add(1)
	defer c.activeWorkers.Done()

	for {
		if c.isStopped(ctx) {
			break
		}
		req, ok := c.newObjReqs.Pop()
		if !ok || c.isStopped(ctx) {
			break
		}
		c.NewObject(req.Hash, req.ObjType)
	}
}

// notifyUseWorker periodically flushes coalesced LRU touch notifications to
// the index store using UseMany in a single concurrent batch.
func (c *CacheMan) notifyUseWorker(ctx context.Context) {
	defer c.logPanic()
	c.activeWorkers.Add(1)
	defer c.activeWorkers.Done()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	flush := func() {
		hashes := c.notifyUseAgg.Flush()
		if len(hashes) == 0 {
			return
		}
		if err := c.index.UseMany(hashes); err != nil {
			c.printer.Printf("notifyUseWorker failed to UseMany: %v\n", err)
		}
	}

	for {
		select {
		case <-c.closeChan:
			flush()
			return
		case <-ticker.C:
			flush()
		}
	}
}

func (c *CacheMan) fetchQueueStat(t protocol.RequestObjectType) *protocol.QueueStat {
	switch t {
	case protocol.TypeBlob:
		return c.fetchBlobsStat
	case protocol.TypeTree:
		return c.fetchTreesStat
	case protocol.TypeARI:
		return c.fetchARIsStat
	case protocol.TypeCASBlob:
		return c.fetchCASBlobsStat
	default:
		return c.fetchObjectsStat
	}
}

func (c *CacheMan) handleFetchReq(ctx context.Context, req FetchReq) {
	reqStat := c.fetchQueueStat(req.ObjType)

	reqStat.Active.Add(1)
	defer reqStat.Active.Add(-1)
	reqStat.Output.Add(1)

	// Synchronously track this hash in pendingAdd before writing the file to disk or
	// unblocking CASFS callbacks. This ensures neither maintenanceWalkDir nor NewObject
	// can ever observe the fetched file on disk before it is tracked as remote.
	//
	// This is low cost on the blocking path because:
	// 1. Purely in-memory: pendingAdd simply acquires a lightweight Mutex and increments
	//    an in-memory map entry.
	// 2. Zero disk I/O: no file stating (os.Stat) or disk reads are performed.
	// 3. Zero DB contention: no BadgerDB transaction commits, WAL writes, or lock retry loops.
	if c.pendingAdd != nil {
		c.pendingAdd.Add(req.Hash)
	}

	t, sz, err := c.doFetch(ctx, req)
	if err != nil {
		if c.pendingAdd != nil {
			c.pendingAdd.Remove(req.Hash)
		}
		c.printer.Printf("error while fetching %s %s: %v\n", req.ObjType, req.Hash, err)
		reqStat.Errors.Add(1)
		c.performCallbacks(req.Hash, err)
		return
	}

	// Unblock CASFS immediately now that the object is written to disk.
	// This eliminates DB commit latency from the user-facing read path.
	c.performCallbacks(req.Hash, nil)

	// Offload index registration to a dedicated dynamic queue to avoid blocking
	// the fetch worker or stalling subsequent requests in the fetch queue.
	// Carry the precomputed on-disk size to avoid redundant disk I/O in the index worker.
	c.indexObjReqs.Push(IndexReq{
		Hash:      req.Hash,
		EntryType: ObjectTypeToEntryType(t),
		ObjType:   req.ObjType,
		ReqStat:   reqStat,
		RawType:   t,
		Size:      sz,
	})
}

func (c *CacheMan) doFetch(ctx context.Context, req FetchReq) (string, int64, error) {
	if req.ObjType == protocol.TypeBlob {
		sz, err := c.fetchBlob(ctx, req.Hash)
		return "blob", sz, err
	}
	if req.ObjType == protocol.TypeARI {
		sz, err := c.fetchARI(ctx, req.ARI, req.Hash)
		return "ari", sz, err
	}
	if req.ObjType == protocol.TypeCASBlob {
		sz, err := c.fetchCASBlob(ctx, req.SHA256Hex, req.Size)
		return "cas_blob", sz, err
	}
	return c.fetchGenericObject(ctx, req.Hash)
}

// entrySize returns the size used on disk, not the bytes of data.
func (c *CacheMan) entrySize(hash entities.Hash, entryType EntryType) (int64, error) {
	ctx := context.Background()

	switch entryType {
	case EntryTypeBlob, EntryTypeObject, EntryTypeARI:
		hdr, err := c.store.ObjectHeader(ctx, hash)
		if err != nil {
			return 0, err
		}
		return bytesUsed(hdr.FileSize(), c.cacheBlockSize), nil
	case EntryTypeTree:
		hdr, err := c.store.ObjectHeader(ctx, hash)
		if err != nil {
			return 0, err
		}

		rawMetaSz, err := c.store.MetaSize(ctx, hash)
		if err != nil {
			if !entities.IsNotExist(err) {
				return 0, err
			}
			if err := c.ensureMeta(ctx, hash); err != nil {
				return 0, err
			}
			rawMetaSz, err = c.store.MetaSize(ctx, hash)
			if err != nil {
				return 0, err
			}
		}

		return bytesUsed(hdr.FileSize(), c.cacheBlockSize) + bytesUsed(rawMetaSz, c.cacheBlockSize), nil
	default:
		return 0, fmt.Errorf("unknown entry type %v", entryType)
	}
}

func (c *CacheMan) makeIndexEntry(hash entities.Hash, entryType EntryType, remote bool) (*IndexKeyEntry, error) {
	return c.makeIndexEntryWithSize(hash, entryType, remote, 0)
}

func (c *CacheMan) makeIndexEntryWithSize(hash entities.Hash, entryType EntryType, remote bool, sz int64) (*IndexKeyEntry, error) {
	if sz <= 0 {
		var err error
		sz, err = c.entrySize(hash, entryType)
		if err != nil {
			return nil, err
		}
	}

	offline := !remote && c.noRemotePush()

	return &IndexKeyEntry{
		Key: hash,
		IndexEntry: &IndexEntry{
			EntryType:     entryType,
			Loose:         true,
			Local:         true,
			Remote:        remote,
			Offline:       offline,
			Size:          uint64(sz),
			LastAliveTime: time.Now(),
		},
	}, nil
}

func (c *CacheMan) makeFetchedIndexEntry(hash entities.Hash, entryType EntryType) (*IndexKeyEntry, error) {
	return c.makeIndexEntryWithSize(hash, entryType, true, 0)
}

func (c *CacheMan) makeFetchedIndexEntryWithSize(hash entities.Hash, entryType EntryType, sz int64) (*IndexKeyEntry, error) {
	return c.makeIndexEntryWithSize(hash, entryType, true, sz)
}

func (c *CacheMan) addFetchedObjectsToIndex(entries []*IndexKeyEntry) error {
	if !c.config.EnableIndex || len(entries) == 0 {
		return nil
	}
	_, _, err := c.index.PutMany(entries)
	return err
}

func (c *CacheMan) addObjectToIndex(hash entities.Hash, entryType EntryType, remote bool) (notifyRemote bool, err error) {
	return c.addObjectToIndexWithSize(hash, entryType, remote, 0)
}

// addObjectToIndexWithSize adds an object to the index with a precomputed size, falling back to
// entrySize() only if sz <= 0. This allows asynchronous index workers to bypass redundant disk reads.
func (c *CacheMan) addObjectToIndexWithSize(hash entities.Hash, entryType EntryType, remote bool, sz int64) (notifyRemote bool, err error) {
	if !c.config.EnableIndex {
		return false, nil
	}

	// If this object came from a fetch and is still pending asynchronous index registration,
	// we know that it already exists on the remote server.
	// Promote it to remote here so that we skip re-notifying remote of an object it already has.
	if !remote && c.pendingAdd != nil && c.pendingAdd.Has(hash) {
		remote = true
	}

	ke, err := c.makeIndexEntryWithSize(hash, entryType, remote, sz)
	if err != nil {
		return false, err
	}

	_, _, prevEnt, _, err := c.index.Put(ke.Key, ke.IndexEntry)
	if err != nil {
		return false, err
	}
	return !remote && !ke.Offline && (prevEnt == nil || !prevEnt.Remote), nil
}

func (c *CacheMan) addLocalObjectToIndex(hash entities.Hash, entryType EntryType) (notifyRemote bool, err error) {
	return c.addObjectToIndex(hash, entryType, false)
}

func (c *CacheMan) addFetchedObjectToIndex(hash entities.Hash, entryType EntryType) (notifyRemote bool, err error) {
	return c.addObjectToIndex(hash, entryType, true)
}

func (c *CacheMan) addFetchedObjectToIndexWithSize(hash entities.Hash, entryType EntryType, sz int64) (notifyRemote bool, err error) {
	return c.addObjectToIndexWithSize(hash, entryType, true, sz)
}

// markObjectRemote marks an existing index entry as remote. If the entry is missing
// from the index (e.g. pushed early by dagsync before newObjectsWorker processed it),
// it falls back to inserting the entry directly via addObjectToIndex with Remote=true
// to guarantee immediate LRU tracking.
func (c *CacheMan) markObjectRemote(key entities.Hash, reqType string) error {
	if !c.config.EnableIndex {
		return nil
	}

	err := c.index.markObjectRemote(key)
	if err == nil || !errors.Is(err, ErrMissingEntry) {
		return err
	}

	// Fallback: object was pushed before newObjectsWorker indexed it.
	// Index it directly with Remote=true to ensure immediate LRU tracking.
	entryType := ObjectTypeToEntryType(reqType)
	_, err = c.addObjectToIndex(key, entryType, true)
	return err
}

func (c *CacheMan) genTreeMeta(ctx context.Context, content []byte) (entities.TreeMeta, error) {
	entries, err := entities.DecodeDirPlain(content)
	if err != nil {
		return entities.TreeMeta{}, err
	}

	ids := make([]int, 0, len(entries))
	blobHashes := make([]entities.Hash, 0, len(entries))

	for i, ent := range entries {
		if ent.Mode.IsFileOrSymlink() {
			blobHashes = append(blobHashes, ent.Hash)
			ids = append(ids, i)
		}
	}
	sizes := make([]int64, len(entries))
	if len(blobHashes) > 0 {
		hdrs, err := c.abfsClient.Headers(ctx, blobHashes)
		if err != nil {
			return entities.TreeMeta{}, err
		}
		for i, hdr := range hdrs {
			if hdr.IsEmpty() {
				return entities.TreeMeta{}, fmt.Errorf("server does not know about %s", blobHashes[i])
			}
			sizes[ids[i]] = hdr.Size
		}
	}
	return entities.TreeMeta{Sizes: sizes}, nil
}
func (c *CacheMan) ensureMeta(ctx context.Context, hash entities.Hash) error {
	content, err := c.store.GetObject(ctx, hash)
	if err != nil {
		return err
	}

	meta, err := c.genTreeMeta(ctx, content)
	if err != nil {
		return err
	}

	return c.store.PutTree(ctx, hash, content, meta)
}

// resolveDeeptree returns the DeepTree hash for tree, compiling it via a remote
// fetch if it is not already mapped and present in the local CAS.
//
// Concurrent callers for the same tree are coalesced through a server-wide
// singleflight group. That is load-bearing: InitDeeptree is served off the
// connection read loop, so without it several in-flight requests for one tree
// would each perform the same (potentially minutes-long) fetch and compile.
//
// This is deliberately a CacheMan method rather than a Connection one. The work
// is server-wide and its result is handed to every coalesced caller, so it must
// not be scoped to whichever connection happened to arrive first.
func (c *CacheMan) resolveDeeptree(tree entities.Hash) (entities.Hash, error) {
	if c.maps == nil {
		return entities.Hash{}, errors.New("maps store not enabled")
	}

	reqDT := &objects.DeepTreeRequest{
		Version: objects.CurrentDeepTreeRequestVersion,
		Tree:    tree,
	}
	if err := reqDT.Encode(); err != nil {
		return entities.Hash{}, fmt.Errorf("encoding deeptree request: %w", err)
	}

	inHash := reqDT.Hash
	c.printer.Printf("InitDeeptree: treeHash=%s, inHash=%s\n", tree, inHash)

	v, err, shared := c.initDeeptreeGroup.Do(inHash.String(), func() (any, error) {
		return c.compileDeeptree(inHash, tree)
	})
	if shared && debug {
		c.printer.Printf("InitDeeptree: request for [%s] coalesced into an in-flight compile\n", tree)
	}
	if err != nil {
		return entities.Hash{}, err
	}
	return v.(entities.Hash), nil
}

// compileDeeptree looks up the DeepTree mapping for tree and, if it is absent
// or its object has been evicted from the local CAS, triggers the remote fetch
// that compiles it.
//
// Must only be called through c.initDeeptreeGroup.
func (c *CacheMan) compileDeeptree(inHash entities.Hash, tree entities.Hash) (entities.Hash, error) {
	// Scoped to the server, not to a caller: this result is shared with every
	// request coalesced into the same singleflight, so cancelation must not
	// follow any one client's connection.
	ctx := c.serverCtx

	hashes, err := c.maps.Get(inHash)
	c.printer.Printf("InitDeeptree: query returned err=%v, len(hashes)=%d\n", err, len(hashes))
	if err == nil && len(hashes) > 0 {
		// Gap 1 Fix: Verify physical file presence in CAS
		if hasObj, _ := c.store.HasObject(ctx, hashes[0]); hasObj {
			return hashes[0], nil
		}
	}

	c.printer.Printf("[%s] Deeptree mapping missing or expired in Cacheman. Triggering remote fetch & compilation...\n", tree)
	if _, _, err := c.fetchGenericObject(ctx, tree); err != nil {
		return entities.Hash{}, fmt.Errorf("resolving and compiling deeptree via remote fetch: %w", err)
	}

	// Query maps again to retrieve the generated deeptree hash.
	hashes, err = c.maps.Get(inHash)
	c.printer.Printf("InitDeeptree after fetch: query returned err=%v, len(hashes)=%d\n", err, len(hashes))
	if err != nil {
		return entities.Hash{}, fmt.Errorf("retrieving generated deeptree mapping after fetch: %w", err)
	}
	if len(hashes) == 0 {
		return entities.Hash{}, fmt.Errorf("no deeptree mapping generated for tree [%s] after fetch", tree)
	}
	return hashes[0], nil
}

func (c *CacheMan) fetchGenericObject(ctx context.Context, hash entities.Hash) (s string, sz int64, retErr error) {
	reader, err := c.abfsClient.GetObject(ctx, hash)
	if err != nil {
		return "", 0, err
	}
	defer entities.CloseErrCheck(reader, &retErr)

	if bundled, ok := reader.(remote.BundledReadCloser); ok {
		// Process any side-car objects (e.g., trees and symlink targets from RBE)
		// and eagerly populate the local store to avoid extra round-trips.
		var indexEntries []*IndexKeyEntry
		var indexMu sync.Mutex

		eg, egCtx := errgroup.WithContext(ctx)
		eg.SetLimit(runtime.NumCPU())
		for _, obj := range bundled.Blobs() {
			eg.Go(func() error {
				if err := c.store.WriteBlob(egCtx, obj.Hash, bytes.NewReader(obj.Data)); err == nil {
					if c.config.EnableIndex {
						diskBytesSz := bytesUsed(int64(len(obj.Data)), c.cacheBlockSize)
						ke, err := c.makeFetchedIndexEntryWithSize(obj.Hash, EntryTypeBlob, diskBytesSz)
						if err != nil {
							return err
						}
						indexMu.Lock()
						indexEntries = append(indexEntries, ke)
						indexMu.Unlock()
					}
				}
				return nil
			})
		}
		for _, obj := range bundled.Objects() {
			eg.Go(func() error {
				objType, _, err := entities.DecodeObject(obj.Data)
				if err == nil && entities.ObjectType(objType) == gitobjects.ObjectTypeDeepTree {
					if err := c.store.PutObject(egCtx, obj.Hash, obj.Data); err == nil {
						if c.config.EnableIndex {
							diskBytesSz := bytesUsed(int64(len(obj.Data)), c.cacheBlockSize)
							ke, err := c.makeFetchedIndexEntryWithSize(obj.Hash, EntryTypeObject, diskBytesSz)
							if err != nil {
								return err
							}
							indexMu.Lock()
							indexEntries = append(indexEntries, ke)
							indexMu.Unlock()
						}
					}
					return nil
				}

				// If it is not a DeepTree, it is a standard Tree.
				meta, err := c.genTreeMeta(egCtx, obj.Data)
				if err != nil {
					return err
				}
				if err := c.store.PutTree(egCtx, obj.Hash, obj.Data, meta); err == nil {
					if c.config.EnableIndex {
						rawMetaSz := int64(8 * len(meta.Sizes))
						diskBytesSz := bytesUsed(int64(len(obj.Data)), c.cacheBlockSize) + bytesUsed(rawMetaSz, c.cacheBlockSize)
						ke, err := c.makeFetchedIndexEntryWithSize(obj.Hash, EntryTypeTree, diskBytesSz)
						if err != nil {
							return err
						}
						indexMu.Lock()
						indexEntries = append(indexEntries, ke)
						indexMu.Unlock()
					}
				}
				return nil
			})
		}
		for _, m := range bundled.MapResults() {
			eg.Go(func() error {
				return c.maps.Put(m.From, m.To)
			})
		}
		if err := eg.Wait(); err != nil {
			return "", 0, err
		}

		if err := c.addFetchedObjectsToIndex(indexEntries); err != nil {
			return "", 0, err
		}
	}

	t, n, err := objects.ReadHeader(reader)
	if err != nil {
		return "", 0, err
	}

	if entities.ObjectType(t) == entities.ObjectTypeBlob {
		if err := c.store.WriteBlob(ctx, hash, reader); err != nil {
			return t, 0, err
		}
		return t, bytesUsed(n, c.cacheBlockSize), nil
	}

	content, err := io.ReadAll(objects.PrependHeaderReader(reader, t, n))
	if err != nil {
		return t, 0, err
	}

	if entities.ObjectType(t) == gitobjects.ObjectTypeDir {
		meta, err := c.genTreeMeta(ctx, content)
		if err != nil {
			return t, 0, err
		}
		if err := c.store.PutTree(ctx, hash, content, meta); err != nil {
			return t, 0, err
		}
		rawMetaSz := int64(8 * len(meta.Sizes))
		treeSz := bytesUsed(int64(len(content)), c.cacheBlockSize) + bytesUsed(rawMetaSz, c.cacheBlockSize)
		return t, treeSz, nil
	}

	if err := c.store.PutObject(ctx, hash, content); err != nil {
		return t, 0, err
	}
	return t, bytesUsed(int64(len(content)), c.cacheBlockSize), nil
}

func (c *CacheMan) fetchMaps(ctx context.Context) {
	defer c.logPanic()
	c.activeWorkers.Add(1)
	defer c.activeWorkers.Done()

	for {
		if c.isStopped(ctx) {
			break
		}
		req, ok := c.fetchMapsReqs.Pop()
		if !ok || c.isStopped(ctx) {
			break
		}
		c.fetchMapsStat.Output.Add(1)
		func() {
			var err error
			defer func() {
				if err != nil {
					c.fetchMapsStat.Errors.Add(1)
				}
				req.Callback(err)
			}()

			remotes, err := c.abfsClient.MapResults(ctx, req.Hash)
			if err != nil {
				return
			}
			locals, err := c.maps.Get(req.Hash)
			if err != nil {
				return
			}
			localSet := make(map[entities.Hash]struct{}, len(locals))
			for _, h := range locals {
				localSet[h] = struct{}{}
			}
			for _, h := range remotes {
				if _, ok := localSet[h]; !ok {
					if err := c.maps.Put(req.Hash, h); err != nil {
						c.printer.Printf("failed to put map for %v: %v\n", req.Hash.String(), err)
					}
				}
			}
		}()
	}
}

func (c *CacheMan) periodicPushMetrics(ctx context.Context) {
	if c.config == nil || !c.config.RecordClientMetrics {
		return
	}

	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			var rssBytes uint64
			if p, err := procfs.Self(); err == nil {
				if stat, err := p.Stat(); err == nil {
					rssBytes = uint64(stat.ResidentMemory())
				}
			}

			c.memGauge.Set(float64(rssBytes))
			c.cacheSizeGauge.Set(float64(c.index.TotalSize()))
			c.evictionGauge.Set(float64(c.index.EvictCount()))

			pushCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			if err := c.provider.Push(pushCtx); err != nil {
				c.printer.Printf("Failed to push cacheman metrics: %v\n", err)
			}
			cancel()
		}
	}
}

func (c *CacheMan) remoteHealthWorker(ctx context.Context) {
	defer c.logPanic()
	c.activeWorkers.Add(1)
	defer c.activeWorkers.Done()

	timer := time.NewTimer(remoteHealthCheckPeriod)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.closeChan:
			return
		case <-c.healthCheckTrigger:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			_ = c.checkRemoteHealth(ctx)
			timer.Reset(remoteHealthCheckPeriod)
		case <-timer.C:
			_ = c.checkRemoteHealth(ctx)
			timer.Reset(remoteHealthCheckPeriod)
		}
	}
}

func (c *CacheMan) triggerHealthCheck() {
	if c.healthCheckTrigger == nil {
		return
	}
	select {
	case c.healthCheckTrigger <- struct{}{}:
	default:
	}
}

func (c *CacheMan) checkRemoteHealth(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	ch := c.healthCheckGroup.DoChan("checkRemoteHealth", func() (any, error) {
		return nil, c.doCheckRemoteHealth()
	})

	select {
	case <-ctx.Done():
		return ctx.Err()
	case res := <-ch:
		return res.Err
	}
}

func (c *CacheMan) doCheckRemoteHealth() error {
	const emptyRBEDigestARI = "rbe:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855:0"

	// Sample these each time because the config can change dynamically.
	statusDir := filepath.Join(c.config.CacheDir, StatusDirName)
	errFile := filepath.Join(statusDir, RemoteErrFileName)
	errTmpFile := filepath.Join(statusDir, RemoteErrFileName+".tmp")
	credsErrFile := filepath.Join(statusDir, CredsErrFileName)
	credsErrTmpFile := filepath.Join(statusDir, CredsErrFileName+".tmp")
	bypassFile := filepath.Join(statusDir, HealthBypassFileName)

	removeErrFiles := func() {
		_ = os.Remove(errFile)
		_ = os.Remove(errTmpFile)
		_ = os.Remove(credsErrFile)
		_ = os.Remove(credsErrTmpFile)
	}

	if _, err := os.Stat(bypassFile); err == nil || c.config.LocalOnly {
		c.setHealthStatusCode(0, "")
		removeErrFiles()
		return nil
	}

	checkCtx, cancel := context.WithTimeout(context.Background(), remoteHealthCheckPeriod)
	defer cancel()

	if c.closeChan != nil {
		stopChan := c.closeChan
		go func() {
			select {
			case <-stopChan:
				cancel()
			case <-checkCtx.Done():
			}
		}()
	}

	var errs error
	if c.abfsClient == nil {
		errs = multierr.Append(errs, errors.New("remote client not initialized"))
	} else if _, ok := c.abfsClient.(*localOnlyClient); ok {
		errs = multierr.Append(errs, errors.New("remote client not initialized (running with local-only fallback)"))
	} else {
		if _, err := c.abfsClient.Version(checkCtx); err != nil {
			errs = multierr.Append(errs, err)
		}
	}

	if c.config.RBEInstance != "" && c.config.RBERemote != "" && !c.config.RBEAsCAS {
		if c.ariClient == nil {
			errs = multierr.Append(errs, errors.New("rbe: client not initialized"))
		} else {
			rc, err := c.ariClient.FetchBlob(checkCtx, emptyRBEDigestARI)
			if err != nil {
				errs = multierr.Append(errs, fmt.Errorf("rbe: %w", err))
			} else {
				_ = rc.Close()
			}
		}
	}

	if errs == nil {
		c.setHealthStatusCode(0, "")
		removeErrFiles()
		return nil
	}

	if err := os.MkdirAll(statusDir, 0755); err != nil {
		c.printer.Printf("failed to create status dir %s: %v\n", statusDir, err)
	}

	// Write remote disconnected error
	content := fmt.Sprintf("timestamp: %s\nerror: %v\n", time.Now().Format(time.RFC3339), errs)
	if writeErr := os.WriteFile(errTmpFile, []byte(content), 0644); writeErr == nil {
		_ = os.Rename(errTmpFile, errFile)
	}

	// Only check credentials if checkCtx has not timed out.
	// A timeout or cancellation must not be reported as a credential failure (ENOKEY).
	var credsErr error
	if checkCtx.Err() == nil {
		credsErr = c.checkCreds(checkCtx)
	}
	if credsErr != nil && !errors.Is(credsErr, context.DeadlineExceeded) && !errors.Is(credsErr, context.Canceled) {
		c.setHealthStatusCode(int32(syscall.ENOKEY), credsErr.Error())
		credsMsg := fmt.Sprintf("timestamp: %s\nerror: %v\n", time.Now().Format(time.RFC3339), credsErr)
		if writeErr := os.WriteFile(credsErrTmpFile, []byte(credsMsg), 0644); writeErr == nil {
			_ = os.Rename(credsErrTmpFile, credsErrFile)
		}
		return multierr.Append(errs, credsErr)
	} else {
		c.setHealthStatusCode(int32(syscall.ENOTCONN), errs.Error())
		_ = os.Remove(credsErrFile)
		_ = os.Remove(credsErrTmpFile)
		return errs
	}
}

func (c *CacheMan) HealthStatus() int32 {
	return c.healthStatusCode.Load()
}

func (c *CacheMan) setHealthStatusCode(code int32, msg string) {
	if old := c.healthStatusCode.Swap(code); old != code {
		if code != 0 && msg != "" {
			c.printer.Printf("health status changed from %d to %d: %s\n", old, code, msg)
		} else {
			c.printer.Printf("health status changed from %d to %d\n", old, code)
		}
	}
}

func (c *CacheMan) setCheckCreds(f CheckCredsFunc) {
	c.checkCredsFn = f
}

// checkCreds checks the validity of user credentials using the configured checker.
func (c *CacheMan) checkCreds(ctx context.Context) error {
	if c.checkCredsFn == nil {
		return nil
	}
	return c.checkCredsFn(ctx)
}

func (c *CacheMan) Serve(ctx context.Context) error {
	c.printer.Printf("Listening on %s...\n", c.socket.Addr())

	go c.handleNewConfig(ctx)

	go func() {
		err := c.httpServer.Serve(c.httpSocket)
		if err != nil && err != http.ErrServerClosed {
			c.printer.Printf("Error while listening on %s: %v\n", c.httpSocket.Addr(), err)
		}
	}()

	// setupRemote in the background to allow Serve()ing in the event of unhealthy connection.
	go func() {
		if err := c.setupRemote(ctx, c.config, true); err != nil {
			c.printer.Printf("Error during remote setup: %v\n", err)
		}
		c.triggerHealthCheck()
	}()

	go func() {
		if err := c.maintenance(ctx); err != nil {
			c.printer.Printf("Error while running maintenance: %v\n", err)
		}
	}()

	workers := runtime.NumCPU()
	for range workers {
		go c.newObjectsWorker(ctx)
	}
	go c.notifyUseWorker(ctx)

	if !c.config.LocalOnly {
		for range workers {
			go c.fetchObjectsWorker(ctx)
			go c.indexObjectsWorker(ctx)
			go c.fetchMaps(ctx)
			go c.prefetchGitObject(ctx)
		}
		go c.periodicLogStats()
	} else {
		c.printer.Println("Cacheman is running in localOnly mode...")
	}

	go c.periodicPushMetrics(ctx)
	go c.remoteHealthWorker(ctx)

	// FIXME: Fix the context use and exit process from cacheman
	for {
		conn, err := c.socket.Accept()
		if err != nil {
			return err
		}
		connection := newConnection(c, conn)

		go func() {
			if debug {
				defer c.printer.Printf("Connection Closed %s\n", conn.RemoteAddr())
			}
			defer func() {
				if err := connection.Close(); err != nil {
					c.printer.Printf("error closing connection %s: %v\n", conn.RemoteAddr(), err)
				}
			}()

			if debug {
				c.printer.Printf("New connection created %s\n", conn.RemoteAddr())
			}
			if err := connection.Serve(); err != nil {
				c.printer.Printf("error handling connection %s: %v\n", conn.RemoteAddr(), err)
			}
		}()
	}
}

func (c *CacheMan) handleNewConfig(ctx context.Context) {
	for {
		select {
		case <-c.newConfigC:
		case <-ctx.Done():
			return
		}

		config := c.incomingConfig.Load()
		c.printer.Printf("Applying new config...\n")
		err := c.applyLocalConfig(config, false)
		if err == nil {
			go func() {
				if err := c.setupRemote(ctx, config, false); err != nil {
					c.printer.Printf("Error while applying new remote config: %v\n", err)
				}
				c.triggerHealthCheck()
			}()
			c.printer.Printf("Successfully applied new config.\n")
			continue
		}
		c.printer.Printf("Error while applying new config: %v\n", err)
		time.Sleep(2 * time.Second)

		select {
		case c.newConfigC <- struct{}{}:
		default:
		}
	}
}

func (c *CacheMan) periodicLogStats() {
	if !c.periodicLogging {
		c.printer.Printf("Periodic logging is disabled. You can enable it by running `curl --unix-socket %s -X PUT http://localhost/log -d true`\n", c.httpSocket.Addr())
	}
	// unconditonally starting the ticker because it didn't seem possible to restart it after stopping it if we did start/stop in the http handlers
	for range time.NewTicker(10 * time.Second).C {
		if c.periodicLogging {
			sb := new(strings.Builder)
			sb.WriteString("\n")
			// read retries aren't implemented, but we print 0 for consistent formatting
			fmt.Fprintf(sb, "Fetch Blob:\t Input=%-15d Output=%-15d Errors=%-15d Retries=%-15d Length=%-15d\n",
				c.fetchBlobsStat.Input.Load(), c.fetchBlobsStat.Output.Load(), c.fetchBlobsStat.Errors.Load(), 0, c.fetchBlobsStat.Input.Load()-c.fetchBlobsStat.Output.Load())
			fmt.Fprintf(sb, "Fetch Tree:\t Input=%-15d Output=%-15d Errors=%-15d Retries=%-15d Length=%-15d\n",
				c.fetchTreesStat.Input.Load(), c.fetchTreesStat.Output.Load(), c.fetchTreesStat.Errors.Load(), 0, c.fetchTreesStat.Input.Load()-c.fetchTreesStat.Output.Load())
			fmt.Fprintf(sb, "Fetch Object:\t Input=%-15d Output=%-15d Errors=%-15d Retries=%-15d Length=%-15d\n",
				c.fetchObjectsStat.Input.Load(), c.fetchObjectsStat.Output.Load(), c.fetchObjectsStat.Errors.Load(), 0, c.fetchObjectsStat.Input.Load()-c.fetchObjectsStat.Output.Load())
			fmt.Fprintf(sb, "Fetch ARI:\t Input=%-15d Output=%-15d Errors=%-15d Retries=%-15d Length=%-15d\n",
				c.fetchARIsStat.Input.Load(), c.fetchARIsStat.Output.Load(), c.fetchARIsStat.Errors.Load(), 0, c.fetchARIsStat.Input.Load()-c.fetchARIsStat.Output.Load())
			fmt.Fprintf(sb, "Fetch Map:\t Input=%-15d Output=%-15d Errors=%-15d Retries=%-15d Length=%-15d\n",
				c.fetchMapsStat.Input.Load(), c.fetchMapsStat.Output.Load(), c.fetchMapsStat.Errors.Load(), 0, c.fetchMapsStat.Input.Load()-c.fetchMapsStat.Output.Load())
			fmt.Fprintf(sb, "Git Fetch:\t Input=%-15d Output=%-15d Errors=%-15d Retries=%-15d Length=%-15d\n",
				c.relFetchObjectStat.Input.Load(), c.relFetchObjectStat.Output.Load(), c.relFetchObjectStat.Errors.Load(), 0, c.relFetchObjectStat.Input.Load()-c.relFetchObjectStat.Output.Load())
			if sync := c.getSync(); sync != nil {
				stats := sync.Stats()
				fmt.Fprintf(sb, "Push stats\t Broken:%-14d Missing:%-15d\n", stats.BrokenLinks, stats.MissingObjects)

				printPush := func(req string, pushStat *protocol.PushStat) {
					fmt.Fprintf(sb, "Requests=%-12s ", pushStat.Diff())
					fmt.Fprintf(sb, "Queued=%-15d ", stats.States["queued"][req])
					fmt.Fprintf(sb, "Blocked=%-14d ", stats.States["blocked"][req])
					fmt.Fprintf(sb, "Pushing=%-15d ", stats.States["running"][req])
					fmt.Fprintf(sb, "Failed=%-15d\n", stats.States["failed"][req])
				}
				fmt.Fprintf(sb, "Push Blob:\t ")
				printPush("blob", c.pushBlobsStat)
				fmt.Fprintf(sb, "Push Object:\t ")
				printPush("object", c.pushObjectsStat)
				fmt.Fprintf(sb, "Push Tree:\t ")
				printPush("tree", c.pushTreesStat)
				fmt.Fprintf(sb, "Push Map:\t ")
				printPush("map", c.pushMapsStat)
				fmt.Fprintf(sb, "Push Ref:\t ")
				printPush("ref", c.pushRefsStat)
				fmt.Fprintf(sb, "Phony:\t ")
				printPush("phony", c.phonyStat)
			}
			fmt.Fprintf(sb, "Execute:\t ")
			fmt.Fprintf(sb, "Requests=%-12d \n", c.executeStat.Request.Load())
			fmt.Fprintf(sb, "Success=%-14d \n", c.executeStat.Success.Load())
			fmt.Fprintf(sb, "Failed=%-15d \n", c.executeStat.Failed.Load())

			c.printer.Println(sb.String())
		}
	}
}

func (c *CacheMan) Stats() protocol.CacheManStats {
	_, err := c.abfsClient.Version(context.Background())
	connected := err == nil
	var stats dagsync.Stats
	if sync := c.getSync(); sync != nil {
		stats = sync.Stats()
	}

	stat := protocol.CacheManStats{
		Connected:     connected,
		TotalSize:     int64(c.index.TotalSize()),
		LRUSize:       c.index.LRUSize(),
		LocalCount:    c.index.LocalCount(),
		IndexCount:    c.index.IndexCount(),
		GCStart:       c.index.GCStart(),
		GCEnd:         c.index.GCEnd(),
		EvictStart:    c.index.EvictStart(),
		EvictEnd:      c.index.EvictEnd(),
		EvictCount:    c.index.EvictCount(),
		OldestObject:  c.index.OldestUsed(),
		FetchBlobs:    c.fetchBlobsStat,
		FetchTrees:    c.fetchTreesStat,
		FetchObjects:  c.fetchObjectsStat,
		FetchARIs:     c.fetchARIsStat,
		FetchCASBlobs: c.fetchCASBlobsStat,
		FetchMaps:     c.fetchMapsStat,
		OverallPushStat: protocol.OverallPushStat{
			BrokenLinks:    stats.BrokenLinks,
			MissingObjects: stats.MissingObjects,
		},
		PushBlobs:   pushGroup("blob", c.pushBlobsStat, stats),
		PushObjects: pushGroup("object", c.pushObjectsStat, stats),
		PushTrees:   pushGroup("tree", c.pushTreesStat, stats),
		PushMaps:    pushGroup("map", c.pushMapsStat, stats),
		PushRefs:    pushGroup("ref", c.pushRefsStat, stats),
		Phony:       pushGroup("phony", c.phonyStat, stats),
		Execute: protocol.ExecStat{
			Request: c.executeStat.Request.Load(),
			Success: c.executeStat.Success.Load(),
			Failed:  c.executeStat.Failed.Load(),
		},
	}

	if cli, ok := c.abfsClient.(*client.ABFSClient); ok {
		stat.ClientStat = cli.Stat()
	}

	return stat
}

func pushGroup(req string, ps *protocol.PushStat, ds dagsync.Stats) protocol.PushGroupStat {
	var queued, blocked, pushing, failed int64
	if ds.States != nil {
		queued = ds.States["queued"][req]
		blocked = ds.States["blocked"][req]
		pushing = ds.States["running"][req]
		failed = ds.States["failed"][req]
	}
	return protocol.PushGroupStat{
		Requests: ps.Request.Load(),
		Queued:   queued,
		Blocked:  blocked,
		Pushing:  pushing,
		Failed:   failed,
	}
}

func (c *CacheMan) RunLocalGC() error {
	if !c.config.EnableIndex {
		return nil
	}
	c.printer.Println("Starting local object garbage collection.")
	// Evict objects not touched in the last 1 hours.
	threshold := time.Now().Add(localGCThreshold).Unix()
	evicted, err := c.index.Sweep(threshold)
	if err != nil {
		c.printer.Printf("Local object GC sweep failed : %v\n", err)
	} else {
		c.printer.Printf("Local object GC sweep complete. Evicted %d objects.\n", evicted)
	}
	return err
}

func (c *CacheMan) Stop() error {
	if c.cancelOverridesWatch != nil {
		c.cancelOverridesWatch()
	}
	if c.cancelSync != nil {
		c.cancelSync()
	}
	if c.cancelServerCtx != nil {
		c.cancelServerCtx()
	}
	c.closeTunnels()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	err := c.httpServer.Shutdown(shutdownCtx)
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}

	close(c.closeChan)
	c.fetchObjReqs.Close()
	c.fetchMapsReqs.Close()
	c.indexObjReqs.Close()
	c.newObjReqs.Close()
	close(c.relFetchObjectReqs)
	c.activeWorkers.Wait()

	// Wait for any in-flight maintenance run to finish before closing index/store.
	c.maintenanceLock.Lock()
	defer c.maintenanceLock.Unlock()

	if c.socket != nil {
		multierr.AppendInto(&err, c.socket.Close())
	}
	if c.socketLock != nil {
		multierr.AppendInto(&err, c.socketLock.Close())
	}
	if c.abfsClient != nil {
		multierr.AppendInto(&err, c.abfsClient.Close())
	}
	if c.ariClient != nil {
		multierr.AppendInto(&err, c.ariClient.Close())
	}
	if c.aresClient != nil {
		multierr.AppendInto(&err, c.aresClient.Close())
	}

	// Close index first (which waits for any active eviction to finish)
	// before closing the underlying localcache store and printer.
	multierr.AppendInto(&err, c.index.Close())
	if c.maps != nil {
		multierr.AppendInto(&err, c.maps.Close())
	}
	if c.identityStore != nil {
		multierr.AppendInto(&err, c.identityStore.Close())
	}
	if c.store != nil {
		multierr.AppendInto(&err, c.store.Close())
	}

	return entities.FirstError(err, c.printer.Close())
}

func (c *CacheMan) FetchObject(hash entities.Hash, objType protocol.RequestObjectType, priority float64, callback func(error)) {
	c.queueFetchReq(FetchReq{Hash: hash, Priority: priority, ObjType: objType, callback: callback})
}

func (c *CacheMan) FetchARI(ari string, priority float64, callback func(error)) {
	hash := entities.Hash(sha1.Sum([]byte(ari)))
	c.queueFetchReq(FetchReq{Hash: hash, ARI: ari, Priority: priority, ObjType: protocol.TypeARI, callback: callback})
}

func (c *CacheMan) FetchCASBlob(sha256Hex string, size int64, priority float64, callback func(error)) {
	key := fmt.Sprintf("cas:%s:%d", sha256Hex, size)
	hash := entities.Hash(sha1.Sum([]byte(key)))
	c.queueFetchReq(FetchReq{
		Hash:      hash,
		SHA256Hex: sha256Hex,
		Size:      size,
		Priority:  priority,
		ObjType:   protocol.TypeCASBlob,
		callback:  callback,
	})
}

func (c *CacheMan) queueFetchReq(req FetchReq) {
	switch req.ObjType {
	case protocol.TypeBlob, protocol.TypeTree, protocol.TypeObject, protocol.TypeARI, protocol.TypeCASBlob:
	default:
		req.Callback(fmt.Errorf("unknown object type %02x", byte(req.ObjType)))
		return
	}

	// Ingress deduplication: if this hash is already queued or actively being fetched,
	// registerCallback coalesces this callback into the existing in-flight request
	// and returns isFirst = false. We avoid pushing redundant requests into the heap.
	if isFirst := c.registerCallback(req); !isFirst {
		return
	}

	c.fetchQueueStat(req.ObjType).Input.Add(1)
	c.fetchObjReqs.Push(req)
}

func (c *CacheMan) NewObject(hash entities.Hash, objType protocol.RequestObjectType) {
	var addErr error
	var notifyRemote bool

	switch objType {
	case protocol.TypeBlob:
		notifyRemote, addErr = c.addLocalObjectToIndex(hash, EntryTypeBlob)
		if sync := c.getSync(); sync != nil && c.config.EagerPush && !c.noRemotePush() && notifyRemote {
			c.pushBlobsStat.Request.Add(1)
			go sync.PutBlob(hash)
		}
	case protocol.TypeTree:
		notifyRemote, addErr = c.addLocalObjectToIndex(hash, EntryTypeTree)
		if sync := c.getSync(); sync != nil && c.config.EagerPush && !c.noRemotePush() && notifyRemote {
			c.pushTreesStat.Request.Add(1)
			go sync.PutTree(hash)
		}
	case protocol.TypeObject:
		notifyRemote, addErr = c.addLocalObjectToIndex(hash, EntryTypeObject)
		if sync := c.getSync(); sync != nil && c.config.EagerPush && !c.noRemotePush() && notifyRemote {
			c.pushObjectsStat.Request.Add(1)
			go sync.PutObject(hash)
		}
	case protocol.TypeARI: // This only happens on maintenance runs. CASFS does not notify any ARI.
		_, addErr = c.addLocalObjectToIndex(hash, EntryTypeARI)
	default:
		c.printer.Printf("NewObject(%s): Unknown object type %02x\n", hash, byte(objType))
		return
	}

	if addErr != nil {
		// The object can be missing from disk if it was evicted concurrently while receiving NewObject message.
		// This is self-correcting because the next time it is read, it will be re-fetched.
		if !entities.IsNotExist(addErr) {
			c.printer.Printf("NewObject(%s): Unable to add to index err: %v\n", hash, addErr)
		}
	}
}

// Phony - callback could be called multiple times with errors before a successful call because of retries
func (c *CacheMan) Phony(priority dagsync.RequestPriority, objects []entities.Hash, callback dagsync.Callback) {
	c.phonyStat.Request.Add(1)

	// if all of our objects are already marked remote in the index, we can skip dagsync
	misses := make([]entities.Hash, 0, len(objects))

	for _, h := range objects {
		ent := c.index.Get(h)
		if ent == nil || !ent.Remote {
			misses = append(misses, h)
		}
	}

	if len(misses) == 0 {
		callback(nil)
		return
	}

	if sync := c.getSync(); sync != nil && !c.noRemotePush() { // calling this function with localOnly=true doesn't make much sense, but we check just in case
		go sync.Phony(priority, misses, callback)
	} else {
		callback(nil)
	}
}

func (c *CacheMan) FetchMap(hash entities.Hash, priority float64, callback func(error)) {
	req := FetchReq{Hash: hash, Priority: priority, callback: callback}

	c.fetchMapsStat.Input.Add(1)
	c.fetchMapsReqs.Push(req)
}

func (c *CacheMan) MapResults(hash entities.Hash) []entities.Hash {
	hashes, err := c.maps.Get(hash)
	if err != nil {
		c.printer.Printf("failed to look maps for %v: %v\n", hash.String(), err)
		return nil
	}
	return hashes
}

func (c *CacheMan) NewMap(from, to entities.Hash, localOnly bool) {
	c.pushMapsStat.Request.Add(1)
	if err := c.maps.Put(from, to); err != nil {
		c.printer.Printf("failed to put map for %v: %v\n", from.String(), err)
	}
	if sync := c.getSync(); sync != nil && !localOnly && !c.config.RemoteReadOnly {
		go sync.AddMapResult(from, to)
	}
}

func (c *CacheMan) clean(req protocol.CleanReq) error {
	c.maintenanceLock.Lock()
	defer c.maintenanceLock.Unlock()

	if c.isStopped(nil) {
		return nil
	}

	if c.config.EnableIndex {
		unlockEviction := c.index.LockEviction()
		defer unlockEviction()
	}

	cacheDir := c.config.CacheDir
	if req.Blobs {
		c.printer.Println("Cleaning blobs...")
		if err := c.cleanDir(path.Join(cacheDir, "blobs")); err != nil {
			return err
		}
	}

	if req.Objects {
		c.printer.Println("Cleaning objects...")
		if err := c.cleanDir(path.Join(cacheDir, "objects")); err != nil {
			return err
		}
	}

	c.printer.Println("Cleaning hashes...")
	// Hash cleanup is best effort and does not throw errors.
	for _, h := range req.Hashes {
		if !entities.IsHash(h) {
			continue
		}
		postfix := h[:3] + "/" + h[3:]

		// Maps don't get tracked by index and can be simply removed
		if err := os.Remove(filepath.Join(c.config.CacheDir, "map", postfix)); err != nil {
			log.Printf("Failed to remove map %q: %v", h, err)
		}

		if _, err := os.Stat(filepath.Join(cacheDir, "blobs", postfix)); err == nil { // if NO error
			if c.config.EnableIndex {
				if err := c.index.Delete(entities.NewHash(h)); err != nil {
					log.Printf("Failed to evict %q: %v", h, err)
				}
			}
			if err := os.Remove(filepath.Join(cacheDir, "blobs", postfix)); err != nil && !entities.IsNotExist(err) {
				log.Printf("Failed to remove blob %q: %v", h, err)
			}
		}

		if _, err := os.Stat(filepath.Join(cacheDir, "objects", postfix)); err == nil { // if NO error
			if c.config.EnableIndex {
				if err := c.index.Delete(entities.NewHash(h)); err != nil {
					log.Printf("Failed to evict %q: %v", h, err)
				}
			}
			if err := os.Remove(filepath.Join(cacheDir, "objects", postfix)); err != nil && !entities.IsNotExist(err) {
				log.Printf("Failed to remove object %q: %v", h, err)
			}
		}

		if _, err := os.Stat(filepath.Join(cacheDir, "aris", postfix)); err == nil { // if NO error
			if c.config.EnableIndex {
				if err := c.index.Delete(entities.NewHash(h)); err != nil {
					log.Printf("Failed to evict %q: %v", h, err)
				}
			}
			if err := os.Remove(filepath.Join(cacheDir, "aris", postfix)); err != nil && !entities.IsNotExist(err) {
				log.Printf("Failed to remove ari %q: %v", h, err)
			}
		}
	}
	c.printer.Println("Done cleaning")

	return nil
}

func cleanDir(directory string, beforeRemove func(path string) error) error {
	if _, err := os.Stat(directory); err != nil {
		if entities.IsNotExist(err) {
			return fmt.Errorf("%s does not exist %w", directory, err)
		} else {
			return fmt.Errorf("error while cleaning %s: %v", directory, err)
		}
	}

	err := cwalk.Walk(directory, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			if entities.IsNotExist(err) {
				// The passed in error is from an lstat, so if the file is
				// already removed we can gracefully ignore.
				return nil
			}
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}

		if beforeRemove != nil {
			if err := beforeRemove(path); err != nil {
				return err
			}
		}

		if err := os.Remove(filepath.Join(directory, path)); err != nil {
			// beforeRemove() may have removed the file
			if !entities.IsNotExist(err) {
				return err
			}
		}
		return nil
	})

	// The cwalk library returns "Broken symlink" whenever lstat returns
	// a nil info. Ignore this error since it indicates the file was already
	// removed.
	if err != nil {
		realErrors := []string{}

		allErrs := strings.Split(err.Error(), "\n")
		for _, e := range allErrs {
			if !strings.Contains(e, "Broken symlink") {
				realErrors = append(realErrors, e)
			}
		}

		if len(realErrors) > 0 {
			return fmt.Errorf("failed to walk %s: %s", directory, strings.Join(realErrors, "\n"))
		}
	}

	return nil
}

func cleanDirAll(directory string) error {
	if _, err := os.Stat(directory); err != nil {
		if entities.IsNotExist(err) {
			return fmt.Errorf("%s does not exist %w", directory, err)
		} else {
			return fmt.Errorf("error while cleaning %s: %v", directory, err)
		}
	}

	ents, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	dirs := make(chan string)
	go func() {
		for _, ent := range ents {
			dirs <- ent.Name()
		}
		close(dirs)
	}()

	eg := new(errgroup.Group)
	eg.SetLimit(runtime.NumCPU())
	for dir := range dirs {
		eg.Go(func() error {
			return os.RemoveAll(filepath.Join(directory, dir))
		})
	}

	return eg.Wait()
}

func bytesUsed(rawSize, blockSize int64) int64 {
	return int64(math.Ceil(float64(rawSize)/float64(blockSize)) * float64(blockSize))
}

func (c *CacheMan) cleanDir(directory string) error {
	beforeRemove := func(path string) error {
		if !c.config.EnableIndex {
			return nil
		}
		s := strings.ReplaceAll(path, "/", "")
		if entities.IsHash(s) {
			if err := c.index.Delete(entities.NewHash(s)); err != nil {
				if !entities.IsNotExist(err) {
					c.printer.Printf("Failed to evict %q err: %v\n", s, err)
				}
			}
		}

		return nil
	}

	return cleanDir(directory, beforeRemove)
}

func (c *CacheMan) putRefs(server, project string, refHashes []entities.RefHash, blocking bool) error {
	if c.config.LocalOnly {
		return errors.New("cannot putRefs in localOnly mode")
	}
	if c.config.RemoteReadOnly {
		c.printer.Printf("skipping putRefs for %s@%s in remoteReadOnly mode\n", project, server)
		return nil
	}
	sync := c.getSync()
	if sync == nil {
		return errors.New("sync client not initialized")
	}
	if debug {
		c.printer.Printf("PutRefs(server=%q, project=%q, refs=%v)\n", server, project, refHashes)
	}

	if !blocking {
		sync.PutRefs(server, project, refHashes)
		return nil
	}

	done := make(chan error, 1)
	sync.PutRefs(server, project, refHashes, func(err error) {
		select {
		case done <- err:
		default:
		}
	})

	csRefs := func() string {
		refs := new(strings.Builder)
		for i, r := range refHashes {
			if i > 0 {
				refs.WriteByte(',')
			}
			fmt.Fprintf(refs, "%s=%s", r.Ref, r.Hash)
		}
		return refs.String()
	}

	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf(
				"failed to update refs %s for %s@%s: %w", csRefs(),
				project, server, err,
			)
		}
		return nil
	case <-time.After(c.config.RefUpdateTimeout):
		return fmt.Errorf(
			"failed to update refs %s for %s@%s: timed out after %s", csRefs(),
			project, server, c.config.RefUpdateTimeout,
		)
	}
}

func (c *CacheMan) reconcileDiskObject(hash entities.Hash, objType protocol.RequestObjectType, trackedInLRU map[entities.Hash]struct{}) {
	// Sentinel empty hashes are never indexed or evicted.
	if hash == entities.EmptyFileHash || hash == entities.EmptyDirHash {
		return
	}
	// In-flight fetches are already queued for asynchronous index registration.
	if c.pendingAdd != nil && c.pendingAdd.Has(hash) {
		return
	}

	entry := c.index.Get(hash)
	resolveObjType := func() protocol.RequestObjectType {
		if objType != protocol.TypeObject {
			return objType
		}
		if entry != nil {
			if entry.EntryType == EntryTypeTree {
				return protocol.TypeTree
			}
			return objType
		}
		if hdr, err := c.store.ObjectHeader(context.Background(), hash); err == nil && hdr.Type == gitobjects.ObjectTypeDir {
			return protocol.TypeTree
		}
		return objType
	}

	if entry == nil {
		// 1. Object is on disk, but not in the index.
		// This will add to the index, push to the remote if EagerPush is enabled,
		// and receive the dagsync callbacks to update 'remote' tracking.
		c.NewObject(hash, resolveObjType())
	} else if !entry.Local {
		// 2. Object is on disk and in the index, but index says local=false.
		// Preserve known remote status: if it was already remote (e.g. an evicted object or
		// re-fetched download), re-index with remote=true so LRU tracking is restored without
		// risk of pinning. If it was local-only, delegate to NewObject to initiate remote push.
		if entry.Remote {
			if _, err := c.addFetchedObjectToIndex(hash, entry.EntryType); err != nil && !entities.IsNotExist(err) {
				c.printer.Printf("maintenance: failed to restore local entry for %q: %v\n", hash.String(), err)
			}
		} else {
			c.NewObject(hash, resolveObjType())
		}
	} else if !entry.Remote {
		// 3. If object is on disk and in the index, but index says remote=false:
		// This will add to the index, push to the remote,
		// and receive the dagsync callbacks to update 'remote' tracking.
		// The "add" to the index will be a NOP in the common case, but we want
		// to go through this code path for the callbacks.
		c.NewObject(hash, resolveObjType())
	} else if entry.Local && entry.Remote {
		// 4. Object is fully synced but might be missing from LRU tracking.
		if _, tracked := trackedInLRU[hash]; !tracked {
			if err := c.index.Use(hash); err != nil {
				c.printer.Printf("Failed to restore LRU tracking for %q: %v\n", hash.String(), err)
			}
		}
	}
}

// In our cwalk fork (v1.1.0-abfs), returning filepath.SkipDir on a file skips only that file
// (not the parent dir) and avoids cwalk's "Broken symlink" error when info is nil.
var cwalkSkipFile = filepath.SkipDir

func (c *CacheMan) maintenanceWalkDir(dir string, objType protocol.RequestObjectType, trackedInLRU map[entities.Hash]struct{}) (map[entities.Hash]struct{}, error) {
	switch objType {
	case protocol.TypeBlob, protocol.TypeObject, protocol.TypeARI:
	default:
		return nil, fmt.Errorf("unexpected objType: %v", objType.String())
	}

	var mu sync.Mutex
	seen := make(map[entities.Hash]struct{})

	if err := cwalk.Walk(path.Join(c.config.CacheDir, dir), func(relPath string, info fs.FileInfo, err error) error {
		if err != nil {
			if entities.IsNotExist(err) {
				// Return cwalkSkipFile (not nil) when info is nil, since cwalk treats
				// (err == nil && info == nil) as "Broken symlink".
				return cwalkSkipFile
			}
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		replaced := strings.Replace(relPath, "/", "", 1)
		if !entities.IsHash(replaced) {
			return nil
		}

		hash := entities.NewHash(replaced)
		mu.Lock()
		seen[hash] = struct{}{}
		mu.Unlock()

		c.reconcileDiskObject(hash, objType, trackedInLRU)
		return nil
	}); err != nil {
		return nil, err
	}

	return seen, nil
}

// maintenance updates the index and/or localcache
// to a consistent state.
//  1. If object is on disk, but not in the index:
//     Add to the index and push to ABFS remote
//  2. If object is on disk and in the index, but index says local=false:
//     Restore local=true (or re-index via NewObject)
//  3. If object is on disk and in the index, but index says remote=false:
//     Push to remote
//  4. If object is missing on disk, but is in the index:
//     Remove from the index
func (c *CacheMan) maintenance(ctx context.Context) error {
	ticker := time.NewTicker(maintenanceDelay)
	defer ticker.Stop()

	for {
		if err := c.maintenanceWork(); err != nil {
			c.printer.Printf("Maintenance failed with error: %v\n", err)
		}

		select {
		case <-ticker.C:
		case <-c.closeChan:
			return nil
		case <-ctx.Done():
			return nil
		}
	}
}

func (c *CacheMan) maintenanceWork() error {
	c.maintenanceLock.Lock()
	defer c.maintenanceLock.Unlock()

	if !c.config.EnableIndex || c.isStopped(nil) {
		return nil
	}

	// Serialize maintenance against background LRU eviction so that eviction does not
	// unlink files on disk while maintenance reconciles the cache directory.
	unlockEviction := c.index.LockEviction()
	defer unlockEviction()

	t := time.Now()

	c.printer.Println("Starting maintenance...")
	defer func() {
		c.printer.Println("Maintenance completed in", time.Since(t))
	}()

	// Read the full LRU list to find what is tracked
	lruList, err := c.index.ReadLRU()
	if err != nil {
		return err
	}
	trackedInLRU := make(map[entities.Hash]struct{}, len(lruList))
	for _, item := range lruList {
		trackedInLRU[item.Key] = struct{}{}
	}

	// First pass - reconcile on-disk objects inline during the directory walk and
	// record seen hashes so Pass 2 does not re-stat files already seen on disk.
	blobs, err := c.maintenanceWalkDir("blobs", protocol.TypeBlob, trackedInLRU)
	if err != nil {
		return err
	}
	objects, err := c.maintenanceWalkDir("objects", protocol.TypeObject, trackedInLRU)
	if err != nil {
		return err
	}
	aris, err := c.maintenanceWalkDir("aris", protocol.TypeARI, trackedInLRU)
	if err != nil {
		return err
	}

	// Second pass - iterate through index objects.
	// Entries aren't deleted during iteration, but a bulk delete request is issued
	// after.
	// We only delete items that aren't already set with a TTL. If the item has a TTL,
	// we let the storage layer handle the cleanup. This prevents duplicate decrements to
	// the indexCount.
	// If key was not seen during Pass 1 (!ok), re-check disk via Has* before deleting:
	// this deletes entries whose files are genuinely missing on disk, while preserving
	// objects that were written and indexed after Pass 1 already walked their directory.
	ctx := context.Background()
	var toDelete []entities.Hash
	if err := c.index.ForEach(func(key entities.Hash, entry *IndexEntry, expiresAt uint64) error {
		// Evicted tombstones have a TTL; let BadgerDB expire them to avoid double-decrementing indexCount.
		if expiresAt != 0 {
			return nil
		}
		switch entry.EntryType {
		case EntryTypeBlob:
			if _, ok := blobs[key]; !ok {
				if has, err := c.store.HasBlob(ctx, key); err == nil && !has {
					toDelete = append(toDelete, key)
				}
			}
		case EntryTypeARI:
			if _, ok := aris[key]; !ok {
				if has, err := c.store.HasARI(ctx, key); err == nil && !has {
					toDelete = append(toDelete, key)
				}
			}
		default:
			if _, ok := objects[key]; !ok {
				if has, err := c.store.HasObject(ctx, key); err == nil && !has {
					toDelete = append(toDelete, key)
				}
			}
		}

		return nil
	}); err != nil {
		return err
	}

	if _, _, err := c.index.DeleteMany(toDelete); err != nil {
		return err
	}

	return nil
}

func (c *CacheMan) PrefetchGitObject(hash entities.Hash, callback func(error)) {
	cnt := new(atomic.Int32)
	cnt.Add(int32(relFetchMaxCount))
	req := RelativeFetchReq{
		FetchReq: FetchReq{
			Hash:     hash,
			Priority: gitPrefetchPriority,
			callback: callback,
		},
		FetchStat: &relativeFetchStat{
			RemainDepth:  relFetchMaxDepth,
			RemainCntItr: cnt,
		},
		RequestType: entities.ObjectTypeUnknown,
	}
	c.relFetchObjectStat.Input.Add(1)
	c.relFetchObjectReqs <- req
}

func (c *CacheMan) prefetchGitObject(ctx context.Context) {
	defer c.logPanic()
	c.activeWorkers.Add(1)
	defer c.activeWorkers.Done()

	for req := range c.relFetchObjectReqs {
		c.relFetchObjectStat.Output.Add(1)

		isFirst := c.registerCallback(req.FetchReq)
		if !isFirst {
			continue
		}

		var err error
		var t string
		var sz int64
		var stat entities.StatInfo
		if stat, err = c.store.Stat(ctx, req.Hash); err != nil {
			if entities.IsNotExist(err) {
				if c.pendingAdd != nil {
					c.pendingAdd.Add(req.Hash)
				}
				t, sz, err = c.fetchGenericObject(ctx, req.Hash)
				if err != nil {
					c.printer.Printf("error while fetching git object %s: %v\n", req.Hash, err)
					c.relFetchObjectStat.Errors.Add(1)
				} else {
					_, err = c.addFetchedObjectToIndexWithSize(req.Hash, ObjectTypeToEntryType(t), sz)
				}
				if c.pendingAdd != nil {
					c.pendingAdd.Remove(req.Hash)
				}

				if err == nil {
					stat, err = c.store.Stat(ctx, req.Hash)
				}
			}
		}

		if err == nil {
			err = c.statStore.Put(req.Hash, stat)
		} else {
			c.printer.Printf("error while getting stat of git object %s: %v\n", req.Hash, err)
		}

		c.performCallbacks(req.Hash, err)

		if err != nil {
			continue
		}

		hdr, err := c.store.ObjectHeader(ctx, req.Hash)
		if err != nil {
			c.printer.Printf("error while getting header of git object %s: %v\n", req.Hash, err)
			continue
		} else {
			err := c.insertNextRelativeFetchReq(ctx, &req, entities.ObjectType(hdr.Type))
			if err != nil {
				c.printer.Printf("error while making relative git requests for %s: %v\n", req.Hash, err)
			}
		}
	}
}

func (c *CacheMan) GetStat(h entities.Hash) (entities.StatInfo, error) {
	if !c.config.GitPrefetching {
		return c.store.Stat(context.Background(), h)
	}

	if st, ok := c.statStore.Get(h); ok {
		return st, nil
	} else {
		return entities.StatInfo{}, fmt.Errorf("stat for hash %s not found", h)
	}
}

func (c *CacheMan) Prefetch(ctx context.Context, blobs, objects []entities.Hash, priority float64) error {
	if debug {
		c.printer.Printf("Prefetching %d blobs and %d objects with priority %f\n", len(blobs), len(objects), priority)
	}

	var grp errgroup.Group
	grp.SetLimit(runtime.NumCPU())
	for _, h := range blobs {
		grp.Go(func() error {
			has, err := c.store.HasBlob(ctx, h)
			if err != nil || has {
				return err
			}
			c.FetchObject(h, protocol.TypeBlob, priority, nil)
			return nil
		})
	}
	for _, h := range objects {
		grp.Go(func() error {
			has, err := c.store.HasObject(ctx, h)
			if err != nil || has {
				return err
			}
			c.FetchObject(h, protocol.TypeObject, priority, nil)
			return nil
		})
	}

	return grp.Wait()
}

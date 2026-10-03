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

package ninjago

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/ninja-go/backends"
	"github.com/google/ninja-go/build"
	"github.com/google/ninja-go/graph"
	"github.com/google/ninja-go/ninja"
	"github.com/google/ninja-go/parse"
	ninjaruntime "github.com/google/ninja-go/runtime"
	"github.com/google/ninja-go/xxmap"
	"golang.org/x/sync/errgroup"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/binding/run"
	"github.com/google/abfs/binding/tap"
	"github.com/google/abfs/ninjago/ninjalog"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/cacheman/client"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
)

const (
	recheckCacheMinDuration = 30 * time.Second
	concurrentStatUpdates   = 8000 // less than 10000 max system threads
)

type HashStat = entities.HashStat

type HashFetchReq struct {
	file     string
	callback func(*HashStat, error)
}

type ABFSBackend struct {
	startTime       time.Time
	params          *ninjaruntime.RunParameters
	dryRun          bool
	outDir          string
	remoteBasePatch *binding.PatchObject
	remotePatches   sync.Map
	useRemote       bool
	remotableRules  map[string]struct{}
	abfs            *binding.Client
	cacheman        *client.Client
	graph           *graph.Graph
	log             ninjaruntime.Logger
	mismatchFile    *os.File
	engine          *build.Engine
	envVarsHash     entities.Hash
	buildStart      *binding.BuildStart
	chroot          string
	config          *Configuration
	logFile         *ninjalog.NinjaLog
	logEntries      xxmap.ObjectsMap[graph.Node, *ninjalog.LogEntry]
	hashes          xxmap.StringMap[*HashStat]
	evals           xxmap.ObjectsMap[graph.Node, *Eval]
	failedsLock     sync.Mutex
	faileds         []*FailedStep
	ninjaEntryFile  string
	ninjaFiles      []string
	hashFetchC      chan HashFetchReq
	pendingJobs     sync.WaitGroup
	matchFiles      *matchFiles
}

func NewABFSBackend() *ABFSBackend {
	return &ABFSBackend{
		logEntries:     xxmap.NewObjectsMap[graph.Node, *ninjalog.LogEntry](),
		remotableRules: make(map[string]struct{}),
		hashes:         xxmap.NewStringMap[*HashStat](),
		evals:          xxmap.NewObjectsMap[graph.Node, *Eval](),
		hashFetchC:     make(chan HashFetchReq),
	}
}

var _ backends.Backend = (*ABFSBackend)(nil)

var notExistsStat = new(HashStat)

func toPatchEntry(hs *HashStat, fname string) entities.PatchEntry {
	return entities.PatchEntry{
		Path: fname,
		Mode: hs.Mode,
		Hash: hs.Hash,
		Size: hs.Size,
	}
}

type Eval struct {
	step          *build.Step
	req           *binding.EvalRequest
	triedResps    map[entities.Hash]struct{}
	triedRemote   bool
	lastRefresh   time.Time
	cacheStart    time.Time
	cacheEnd      time.Time
	start         time.Time
	end           time.Time
	remotePatches []entities.Hash
}

type FailedStep struct {
	step *build.Step
	req  *binding.EvalRequest
	resp *binding.EvalResponse
	err  error
}

// Init implements backends.Backend.
func (c *ABFSBackend) Init(params *ninjaruntime.RunParameters, cmd *string, args *[]string) error {
	c.startTime = time.Now()
	c.config = LoadConfig()
	c.chroot = c.config.Chroot

	c.params = params
	c.dryRun = params.CliFlags.DryRun

	if c.params.CliFlags.OptNoRemote || c.config.NoARES {
		c.params.CliFlags.OptNoRemote = true
		c.config.NoARES = true
	}

	if c.params.CliFlags.OptNoLocalFallback || c.config.NoFallbackIfRemoteFails {
		c.params.CliFlags.OptNoLocalFallback = true
		c.config.NoFallbackIfRemoteFails = true
	}

	c.cacheman = client.NewClient(c.config.CacheDir, c.config.FetchTimeout)

	abfs, err := binding.NewClient(".", entities.ABFSSocketFileName, c.config.CacheDir, c.config.LocalOnly, c.config.FetchTimeout)
	if err != nil {
		return fmt.Errorf("could not create ABFS client: %w", err)
	}
	c.abfs = abfs

	envVars := NormalizedEnvVars()
	envVarsHash, err := abfs.StoreEnvVars(envVars)
	if err != nil {
		return fmt.Errorf("failed to store env-vars: %w", err)
	}
	c.envVarsHash = envVarsHash

	if c.config.PSICPU > 0 {
		params.CliFlags.PSICPU = c.config.PSICPU
	}

	if c.config.PSIIO > 0 {
		params.CliFlags.PSIIO = c.config.PSIIO
	}

	if c.config.PSIMem > 0 {
		params.CliFlags.PSIMem = c.config.PSIMem
	}

	concurrency := int(float64(params.CliFlags.ConcurrencyLevel) * c.config.ConcurrencyMultiplier)
	if concurrency < 1 {
		concurrency = 1
	}
	params.CliFlags.ConcurrencyLevel = concurrency

	return nil
}

func (c *ABFSBackend) fetchHashes(filesC <-chan string) error {
	var wg sync.WaitGroup
	var errLock sync.Mutex
	var errs []error
	for file := range filesC {
		wg.Add(1)
		c.hashFetchC <- HashFetchReq{
			file: file,
			callback: func(_ *HashStat, err error) {
				if err != nil {
					errLock.Lock()
					errs = append(errs, err)
					errLock.Unlock()
				}
				wg.Done()
			},
		}
	}

	wg.Wait()
	return errors.Join(errs...)
}

// GetDeps implements backends.Backend.
func (c *ABFSBackend) GetDeps(n *graph.Node) ([]string, error) {
	// ABFS ninja does not use deps in graph.
	return nil, nil
}

// Parsed implements backends.Backend.
func (c *ABFSBackend) Parsed(ninjas *ninja.ParsedFiles) error {
	c.log = c.params.Log
	c.ninjaEntryFile = ninjas.EntryFile
	c.ninjaFiles = make([]string, 0, len(ninjas.Files))
	for f := range ninjas.Files {
		c.ninjaFiles = append(c.ninjaFiles, f)
	}
	return nil
}

// GraphLoading implements backends.Backend.
func (c *ABFSBackend) GraphLoading() error {
	return nil
}

var ninjaBuildRegex = regexp.MustCompile(`/soong/build[^/]*\.ninja$`)

// GraphLoaded implements backends.Backend.
func (c *ABFSBackend) GraphLoaded(g *graph.Graph) error {
	c.outDir = g.BuildDir
	c.matchFiles = newMatchFiles(c.outDir)

	for s := range skipTargets {
		delete(skipTargets, s)
		skipTargets[strings.ReplaceAll(s, "$OUT_DIR", c.outDir)] = struct{}{}
	}

	g.Targets.GoRange(func(key string, n *graph.Node) bool {
		switch {

		// soong does not specify these dependencies
		case n.TargetsMatch(ninjaBuildRegex):
			files, err := readFileLines(filepath.Join(c.outDir, ".module_paths/Android.bp.list"))
			if err != nil {
				c.log.Errorf("failed to read file: %s", err)
			}
			for _, file := range files {
				g.Files.Put(file)
			}
			n.Build.ImplicitInputs = append(n.Build.ImplicitInputs, files...)

			// TODO: Add soong/dexpreopt-*.config and soong/soong.*.variables to soong_build deps
			c.addFileGlobs("soong/dexpreopt-*.config", g, n)
			c.addFileGlobs("soong/soong.*.variables", g, n)

		// fsgen images hidden dep on out/build_date.txt
		case strings.HasSuffix(n.Build.RuleName, ".add_avb_footer"):
			n.Build.OrderOnlyInputs = append(n.Build.OrderOnlyInputs,
				filepath.Join(c.outDir, "build_date.txt"),
				filepath.Join(c.outDir, "soong/build_number.txt"),
			)
		}

		return true
	})

	c.graph = g
	return nil
}

func (c *ABFSBackend) addFileGlobs(glob string, g *graph.Graph, n *graph.Node) {
	ents, err := filepath.Glob(filepath.Join(c.outDir, glob))
	if err != nil {
		c.log.Errorf("failed to glob: %s", err)
		return
	}
	for _, file := range ents {
		g.Files.Put(file)
	}
	n.Build.ImplicitInputs = append(n.Build.ImplicitInputs, ents...)
}

func (c *ABFSBackend) addFailedStep(step *build.Step, req *binding.EvalRequest, resp *binding.EvalResponse, err error) {
	c.failedsLock.Lock()
	defer c.failedsLock.Unlock()

	if err == nil && resp != nil {
		err = fmt.Errorf("command failed with %d", resp.ExitCode)
	}

	c.faileds = append(c.faileds, &FailedStep{
		step: step,
		req:  req,
		resp: resp,
		err:  err,
	})
}

func readFileLines(file string) (lines []string, retErr error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer entities.CloseErrCheck(f, &retErr)

	lines = make([]string, 0)
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		lines = append(lines, scan.Text())
	}
	if err := scan.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

func (c *ABFSBackend) loadNinjaLog() error {
	entries, err := c.logFile.Parse()
	if err != nil {
		return fmt.Errorf("failed to read ninja-log file %q: %w", c.logFile.Name(), err)
	}

	for _, ents := range entries {
		for _, ent := range ents.Entries {
			n := c.graph.Targets.Get(ent.Target)
			if n != nil {
				c.logEntries.Put(n, ent)
			}
		}
	}
	return nil
}

// BuildInit implements backends.Backend.
func (c *ABFSBackend) BuildInit(engine *build.Engine) error {
	c.engine = engine
	buildStart, err := binding.ReadBuildStart("", c.outDir)
	if err != nil {
		return err
	}
	c.buildStart = buildStart

	c.logFile = ninjalog.New(path.Join(c.outDir, ninjalog.DefaultLogFileName))
	if err := c.loadNinjaLog(); err != nil {
		return err
	}

	c.startHashFetchers()

	if !c.config.NoARES {
		if supp, err := c.abfs.ExecuteSupport(); supp && (err == nil) {
			if err := c.loadRemote(); err != nil {
				c.log.Warnf("cannot use remote execution: %v", err)
			}
		}
	}

	if err := c.collectInitialHashes(); err != nil {
		return err
	}

	if !c.dryRun {
		if err := c.logFile.OpenForWrite(); err != nil {
			return err
		}
		if c.config.LogMismatches {
			f, err := os.OpenFile(path.Join(entities.OutDir, entities.MismatchesLogFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
			if err != nil {
				return fmt.Errorf("failed to open mismatch file: %v", err)
			}
			c.mismatchFile = f
		}
	}

	c.log.Infof("Starting ABFS build with ninjago to build %s...", strings.Join(c.graph.GoalTargets, ","))
	return nil
}

func (c *ABFSBackend) evalReqResp(reqHash, respHash entities.Hash) (req *binding.EvalRequest, resp *binding.EvalResponse, err error) {
	ctx := context.Background()
	var grp errgroup.Group

	grp.Go(func() error {
		var err error
		req, err = binding.FetchObject[binding.EvalRequest](ctx, c.abfs.Store(), reqHash)
		return err
	})
	grp.Go(func() error {
		var err error
		resp, err = binding.FetchObject[binding.EvalResponse](ctx, c.abfs.Store(), respHash)
		return err
	})

	if err := grp.Wait(); err != nil {
		return nil, nil, err
	}
	return req, resp, err
}

// IsClean implements backends.Backend.
func (c *ABFSBackend) IsClean(step *build.Step) (bool, error) {
	if step.IsPhonyOutput() { // phony-output is always dirty
		return false, nil
	}
	if SkipNode(step.Node) {
		return true, nil
	}

	ent, found := c.logEntries.Get(step.Node)
	if !found {
		return false, nil
	}

	req, resp, err := c.evalReqResp(ent.ReqHash, ent.RespHash)
	if err != nil {
		return false, nil
	}

	if req.EnvVars != c.envVarsHash {
		return false, nil
	}

	if req.Command != step.Command() {
		return false, nil
	}

	var grp errgroup.Group
	grp.SetLimit(runtime.NumCPU())

	writesMap := make(map[string]struct{}, len(resp.WritePatch))
	for _, ent := range resp.WritePatch {
		writesMap[ent.Path] = struct{}{}
		grp.Go(func() error {
			return c.cleanMatch(ent)
		})
	}

	for _, ent := range resp.ReadPatch {
		grp.Go(func() error {
			if _, exists := writesMap[ent.Path]; exists {
				return nil
			}
			if ent.State.IsOrderOnly() {
				return nil
			}
			return c.cleanMatch(ent)
		})
	}

	if step.RSPFile() != "" {
		grp.Go(func() error {
			ent := rspPatchEntry(step.RSPFile(), step.RSPContent())
			return c.cleanMatch(ent)
		})
	}

	err = grp.Wait()
	if binding.IsMatchError(err) {
		if c.config.LogMismatches {
			if err := c.logMismatch("clean mismatch for %s req=%s resp=%s: %v\n", step, req.Hash, resp.Hash, err); err != nil {
				return false, err
			}
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}

	return true, nil
}

// ApplyIfCached implements backends.Backend.
func (c *ABFSBackend) ApplyIfCached(step *build.Step) (bool, error) {
	if c.dryRun {
		return false, nil
	}

	req, err := c.toEvalRequest(step)
	if err != nil {
		return false, err
	}
	eval := &Eval{
		step:       step,
		req:        req,
		triedResps: make(map[entities.Hash]struct{}),
	}

	ctx := context.Background()

	cached, err := c.applyIfCached(ctx, eval, false)
	if err != nil || cached {
		return cached, err
	}
	c.evals.Put(step.Node, eval)
	return false, nil
}

func (c *ABFSBackend) applyIfCached(ctx context.Context, eval *Eval, delayed bool) (bool, error) {
	eval.cacheStart = time.Now()
	defer func() {
		eval.cacheEnd = time.Now()
	}()

	if c.config.EvalNoCache {
		return false, nil
	}

	runType := ninjalog.RunTypeCached
	if delayed {
		runType = ninjalog.RunTypeDelayedCached
	}
	for i := range 2 {
		if i > 0 {
			eval.lastRefresh = time.Now()
			if err := c.abfs.Store().RefreshMapResults(ctx, eval.req.Hash); err != nil {
				return false, err
			}
		}
		respHashes, err := c.abfs.Store().MapResults(context.Background(), eval.req.Hash)
		if err != nil {
			return false, err
		}
		slices.Reverse(respHashes) // More recent responses have higher chance of a match

		for _, hash := range respHashes {
			if _, done := eval.triedResps[hash]; done {
				continue
			}
			resp, err := binding.FetchObject[binding.EvalResponse](ctx, c.abfs.Store(), hash)
			if err != nil {
				return false, err
			}

			targetsMap := make(map[string]struct{}, len(eval.step.Node.Targets))
			for _, t := range eval.step.Node.Targets {
				targetsMap[t] = struct{}{}
			}
			for _, ent := range resp.WritePatch {
				delete(targetsMap, ent.Path)
			}
			if len(targetsMap) > 0 {
				for _, ent := range resp.ReadPatch {
					delete(targetsMap, ent.Path)
					if len(targetsMap) == 0 {
						break
					}
				}
			}
			if len(targetsMap) > 0 {
				c.log.Tracef("Skipping response %s for %s because WritePatch does not contain %v (chroot is %q)", hash, eval.req.Hash, targetsMap, c.chroot)
				eval.triedResps[resp.Hash] = struct{}{}
				continue
			}
			err = c.cacheMatch(resp, eval)
			if err == nil { // On a successful match
				c.updateCacheWithResp(resp)
				eval.cacheEnd = time.Now()
				if err := c.addToNinjaLog(eval, resp, runType); err != nil {
					return false, err
				}
				return true, nil
			}
			if !binding.IsMatchError(err) {
				c.log.Warnf("failed to match/apply req=%s resp=%s: %v", eval.req.Hash, resp.Hash, err)
			}
			if c.config.LogMismatches {
				if err := c.logMismatch("%s\n", err.Error()); err != nil {
					return false, fmt.Errorf("cannot log mismatch: %w", err)
				}
			}
			eval.triedResps[resp.Hash] = struct{}{}
		}
	}
	return false, nil
}

func (c *ABFSBackend) updateCacheWithResp(resp *binding.EvalResponse) {
	for _, f := range resp.Deletes {
		c.hashes.Put(f, notExistsStat)
	}

	for _, ent := range resp.WritePatch {
		c.hashes.Put(ent.Path, &HashStat{
			Hash: ent.Hash,
			Size: ent.Size,
			Mode: ent.Mode,
		})
	}
}

func (c *ABFSBackend) addToNinjaLog(eval *Eval, resp *binding.EvalResponse, runType ninjalog.RunType) error {
	entryTime := eval.cacheStart
	if entryTime.IsZero() {
		entryTime = eval.start
	}

	var duration time.Duration
	if !eval.cacheStart.IsZero() && !eval.cacheEnd.IsZero() {
		duration += eval.cacheEnd.Sub(eval.cacheStart)
	}
	if !eval.start.IsZero() && !eval.end.IsZero() {
		duration += eval.end.Sub(eval.start)
	}
	ent := &ninjalog.LogEntry{
		Time:       entryTime,
		Duration:   duration,
		RunType:    runType,
		ReqHash:    resp.EvalRequest,
		RespHash:   resp.Hash,
		Mismatches: len(eval.triedResps),
	}
	for _, t := range eval.step.Node.Targets {
		ent.Target = t
		if err := c.logFile.WriteEntry(ent); err != nil {
			return err
		}
	}
	return nil
}

// fileHashStat reads the file's hash-stat using extended attributes.
// It is similar to bindcas.Hash(), but for directories it does not read the actual hash of the directory.
func fileHashStat(f string) (*HashStat, error) {
	stat, err := os.Lstat(f)
	if entities.IsNotExist(err) {
		return notExistsStat, nil
	}
	if err != nil {
		return nil, err
	}

	if stat.IsDir() {
		return &HashStat{
			Hash: entities.EmptyDirHash,
			Mode: entities.FileModeDir,
		}, nil
	}

	hash, size, err := bindcas.HashSize(f)
	if entities.IsNotExist(err) {
		return notExistsStat, nil
	}
	if err != nil {
		return nil, err
	}

	return &HashStat{
		Hash: hash,
		Mode: entities.ModeToFileMode(stat.Mode()),
		Size: size,
	}, nil
}

func (c *ABFSBackend) ensureDirCached(dirName string) error {
	for dirName != "" && dirName != "." {
		if c.hashes.Get(dirName) != nil {
			return nil
		}
		hs, err := fileHashStat(dirName)
		if err != nil {
			return err
		}
		c.hashes.Put(dirName, hs)
		dirName = path.Dir(dirName)
	}
	return nil
}

func (c *ABFSBackend) updateStat(fname string) (*HashStat, error) {
	hashStat, err := fileHashStat(fname)
	if err != nil {
		return nil, err
	}
	c.hashes.Put(fname, hashStat)
	if hashStat != notExistsStat {
		if err := c.ensureDirCached(path.Dir(fname)); err != nil {
			return nil, err
		}
	}
	return hashStat, nil
}

func (c *ABFSBackend) startHashFetchers() {
	for range concurrentStatUpdates {
		c.pendingJobs.Add(1)
		go func() {
			defer c.pendingJobs.Done()
			for req := range c.hashFetchC {
				hs, err := c.updateStat(req.file)
				req.callback(hs, err)
			}
		}()
	}
}

func (c *ABFSBackend) collectInitialHashes() error {
	c.hashes.Put("", &HashStat{Mode: entities.FileModeDir})
	c.hashes.Put(".", &HashStat{Mode: entities.FileModeDir})

	filesC := make(chan string)

	go func() {
		c.graph.Nodes.GoRange(func(n *graph.Node) bool {
			for _, t := range n.Targets {
				filesC <- t
			}
			return true
		})
		c.graph.Files.GoRange(func(fname string) bool {
			filesC <- fname
			return true
		})
		close(filesC)
	}()

	return c.fetchHashes(filesC)
}

// IsRemotable implements backends.Backend.
func (c *ABFSBackend) IsRemotable(step *build.Step) bool {
	if c.dryRun {
		return false
	}
	return c.isRemotable(step)
}

// ExecRemote implements backends.Backend.
func (c *ABFSBackend) ExecRemote(ctx context.Context, step *build.Step) build.ExecResult {
	return c.eval(ctx, step, ninjalog.RunTypeRemote)
}

// ExecLocal implements backends.Backend.
func (c *ABFSBackend) ExecLocal(ctx context.Context, step *build.Step) build.ExecResult {
	var r build.ExecResult
	if !c.dryRun {
		r = c.eval(ctx, step, ninjalog.RunTypeLocal)
	}
	return r
}

func (c *ABFSBackend) shouldTap(step *build.Step) bool {
	for _, t := range step.Node.Targets {
		if strings.HasSuffix(t, "buildko.timestamp") {
			return false
		}
	}
	if c.config.SkipTapSoongBuild && step.Node.TargetsMatch(ninjaBuildRegex) {
		return false
	}
	return true
}

func (c *ABFSBackend) eval(ctx context.Context, step *build.Step, runType ninjalog.RunType) build.ExecResult {
	eval, found := c.evals.Get(step.Node)
	if !found {
		return build.ExecResult{Err: fmt.Errorf("failed to find Eval object for %s %p", step, step.Node)}
	}

	if time.Since(eval.lastRefresh) > recheckCacheMinDuration {
		cached, err := c.applyIfCached(context.Background(), eval, true)
		if err != nil || cached {
			return build.ExecResult{Err: err}
		}
	}

	switch runType {
	case ninjalog.RunTypeLocal:
		if !c.shouldTap(step) {
			runType = ninjalog.RunTypeLocalNoTap
		} else if eval.triedRemote {
			runType = ninjalog.RunTypeFallbackErr
		}
	case ninjalog.RunTypeRemote:
		eval.triedRemote = true
	}

	eval.start = time.Now()
	resp, err := c.exec(ctx, eval, runType)
	eval.end = time.Now()

	err = c.postExec(eval, resp, runType, err)

	execResult := build.ExecResult{
		Err: err,
	}
	if resp != nil {
		execResult.Stdout = resp.Stdout
		execResult.Stderr = resp.Stderr
		execResult.ExitCode = resp.ExitCode
	}

	return execResult
}

func (c *ABFSBackend) exec(ctx context.Context, eval *Eval, runType ninjalog.RunType) (*binding.EvalResponse, error) {
	switch runType {
	case ninjalog.RunTypeLocalNoTap:
		return c.execNoTapLocal(ctx, eval)

	case ninjalog.RunTypeRemote:
		return c.execRemote(ctx, eval)

	default:
		return c.execTapLocal(ctx, eval)
	}
}

func (c *ABFSBackend) postExec(eval *Eval, resp *binding.EvalResponse, runType ninjalog.RunType, err error) error {
	if err != nil || resp.ExitCode != 0 { // if `err == nil` we assume `resp != nil`
		c.addFailedStep(eval.step, eval.req, resp, err)

		var msg string
		if err == nil {
			msg = "resp=" + resp.Hash.String()
		} else {
			msg = err.Error()
		}
		fmt.Fprintf(os.Stderr, "Exec (%s) %s failed req=%s %s\n", runType, eval.step, eval.req.Hash, msg)
		if resp != nil && runType == ninjalog.RunTypeRemote && !c.config.NoFallbackIfRemoteFails { // ninja will not log stdout and stderr because it falls back to local
			fmt.Fprintf(os.Stderr, "Command: %s\nOutput:\n%s%s\n", eval.req.Command, resp.Stdout, resp.Stderr)
		}
	}

	if resp == nil {
		return fmt.Errorf("failed to execute: %w", err)
	}

	c.updateCacheWithResp(resp)

	if err == nil && resp.ExitCode == 0 {
		if err := c.addToNinjaLog(eval, resp, runType); err != nil {
			return err
		}
	}

	return err
}

func (c *ABFSBackend) prefetchInputs(eval *Eval) error {
	if !c.config.PrefetchInputs {
		return nil
	}
	return binding.PrefetchInputPatch(eval.req.InputPatch, c.cacheman)
}

func (c *ABFSBackend) execNoTapLocal(ctx context.Context, eval *Eval) (*binding.EvalResponse, error) {
	if err := c.prefetchInputs(eval); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to prefetch inputs for %s req=%s: %v\n", eval.step, eval.req.Hash, err)
	}
	cmd := run.BuildCommand(eval.step.Command())
	r := run.RunCommand(cmd)
	if r.Err != nil {
		return nil, r.Err
	}

	writePatch := make(entities.Patch, 0, len(eval.req.Targets))
	for _, t := range eval.req.Targets {
		s, err := c.updateStat(t)
		if err != nil {
			return nil, err
		}

		if s == notExistsStat {
			continue
		}

		writePatch = append(writePatch, &entities.PatchEntry{
			Path:  t,
			Mode:  s.Mode,
			Size:  s.Size,
			Hash:  s.Hash,
			State: entities.PatchEntryStateOut,
		})
	}

	resp := binding.ResponseFromRun(r)
	resp.EvalRequest = eval.req.Hash
	resp.RunInfo.BuildStartHash = eval.req.Hash
	resp.ReadPatch = eval.req.InputPatch
	resp.WritePatch = writePatch

	if err := c.StoreResults(ctx, eval, resp); err != nil {
		return resp, err
	}

	return resp, nil
}

func (c *ABFSBackend) execTapLocal(ctx context.Context, eval *Eval) (*binding.EvalResponse, error) {
	if err := c.prefetchInputs(eval); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to prefetch inputs for %s req=%s: %v\n", eval.step, eval.req.Hash, err)
	}
	snapshotConcurrency := 4
	if eval.step.Node.TargetsMatch(ninjaBuildRegex) {
		snapshotConcurrency = -1
	}
	tapResult, err := tap.TapCommand(eval.step.Command(), c.chroot, c.outDir, c.config.StrictTappingOff, snapshotConcurrency)
	if err != nil {
		return nil, fmt.Errorf("failed to execute command: %w", err)
	}

	resp := binding.EvalRespFromTapResult(tapResult)
	if c.buildStart != nil {
		resp.RunInfo.BuildStartHash = c.buildStart.Hash
	}

	resp.EvalRequest = eval.req.Hash

	binding.ProcessResp(binding.EvalPostProcess{
		Resp:          resp,
		Req:           eval.req,
		ChrootToStrip: c.chroot,
	})

	if err := c.StoreResults(ctx, eval, resp); err != nil {
		return resp, err
	}

	if tapResult.ExitCode == 0 {
		if err := c.abfs.Store().AddMapResult(ctx, eval.req.Hash, resp.Hash); err != nil {
			fmt.Fprintf(os.Stderr, "failed to add map entry for %s req=%s resp=%s: %v\n", eval.step, eval.req.Hash, resp.Hash, err)
		}
	}

	return resp, nil
}

func (c *ABFSBackend) lookup(fname string) (*HashStat, error) {
	ent := c.hashes.Get(fname)
	if ent != nil {
		return ent, nil
	}

	hs, err := fileHashStat(fname)
	if err != nil {
		return nil, err
	}
	c.hashes.Put(fname, hs)
	return hs, nil
}

func (c *ABFSBackend) StoreResults(ctx context.Context, eval *Eval, resp *binding.EvalResponse) error {
	if err := c.abfs.Store().PutObject(ctx, eval.req.Hash, eval.req.Encoded); err != nil {
		return fmt.Errorf("failed to write eval-req %s for %s: %w", eval.req.Hash, eval.step, err)
	}

	if err := resp.Encode(); err != nil {
		return fmt.Errorf("failed to encode eval-resp (req=%s) for %s: %w", eval.req.Hash, eval.step, err)
	}

	if err := c.abfs.Store().PutObject(ctx, resp.Hash, resp.Encoded); err != nil {
		return fmt.Errorf("failed to write eval-resp %s (req=%s) for %s: %w", resp.Hash, eval.req.Hash, eval.step, err)
	}
	return nil
}

// shouldIgnoreInput will hide the input from the eval request (which will break remote execution if it is actually required).
// Care should be taken before adding something here. See ignoreMatch() in match.go for a lighter weight check on the eval response
// matching part of the flow.
func shouldIgnoreInput(fname string) bool {
	// handle out/.path_interposer_origpath
	if strings.HasSuffix(fname, ".path_interposer_origpath") {
		return true
	}
	return false
}

// addInput uses the inputSet to efficiently look recursively through phony rules to see if the input needs to be added.
func (c *ABFSBackend) addInput(fname string, isOrderOnly bool, inputs *[]entities.PatchEntry, inputSet map[string]struct{}) error {
	if shouldIgnoreInput(fname) {
		return nil
	}
	if n := c.graph.Targets.Get(fname); n != nil && n.Build.Rule.IsPhony() {
		// Traverse phony targets recursively to add to the input list
		for i, ins := range n.Build.AllInputs() {
			for _, in := range ins {
				if _, exists := inputSet[in]; !exists {
					if err := c.addInput(in, isOrderOnly || ninja.InputsType(i) == ninja.InputsTypeOrderOnly, inputs, inputSet); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}

	if isOrderOnly && !c.orderOnlyAsRegularInput(fname) {
		*inputs = append(*inputs, entities.PatchEntry{
			Path:  fname,
			Mode:  entities.FileModeRegular,
			State: entities.PatchEntryStateOrderOnly,
		})
	} else {
		hs, err := c.lookup(fname)
		if err != nil {
			return err
		}
		if hs == notExistsStat {
			return fmt.Errorf("cannot find %s", fname)
		}
		ent := toPatchEntry(hs, fname)
		if isOrderOnly {
			ent.SetState(entities.PatchEntryStateOrderOnly)
		}
		*inputs = append(*inputs, ent)
	}
	inputSet[fname] = struct{}{}
	return nil
}

func (c *ABFSBackend) toEvalRequest(step *build.Step) (*binding.EvalRequest, error) {
	b := step.Node.Build

	if err := c.reqHashForInput(b); err != nil {
		return nil, err
	}

	sizeGuess := 0
	if step.RSPFile() != "" {
		sizeGuess = 1
	}
	for _, a := range b.AllInputs() {
		sizeGuess += len(a)
	}

	inputs := make([]entities.PatchEntry, 0, sizeGuess)
	inputSet := make(map[string]struct{}, sizeGuess)

	for i, ins := range b.AllInputs() {
		for _, in := range ins {
			if err := c.addInput(in, ninja.InputsType(i) == ninja.InputsTypeOrderOnly, &inputs, inputSet); err != nil {
				return nil, err
			}
		}
	}

	// BUG: target_files.zip step does not have the directy entries as input
	if strings.HasSuffix(step.Node.Build.RuleName, ".target_files") {
		inputDir := path.Join(path.Dir(step.Node.Targets[0]), "target_files_dir")
		hash, err := bindcas.Hash(inputDir)
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, entities.PatchEntry{
			Path: inputDir,
			Mode: entities.FileModeDir,
			Hash: hash,
		})
	}

	if step.RSPFile() != "" {
		if err := c.addInput(step.RSPFile(), false, &inputs, inputSet); err != nil {
			return nil, err
		}
	}

	inputPatch := make(entities.Patch, len(inputs))
	for i := range inputs {
		inputPatch[i] = &inputs[i]
	}

	req := &binding.EvalRequest{
		Version:    binding.CurrentEvalRequestVersion,
		Salt:       c.config.EvalSalt,
		Targets:    step.Node.Targets,
		DepFile:    step.Node.DepFile(),
		Command:    step.Command(),
		InputPatch: inputPatch,
		RSP:        step.RSPFile(),
		EnvVars:    c.envVarsHash,
	}

	return req, req.Encode()
}

func (c *ABFSBackend) reqHashForInput(b *ninja.Build) error {
	filesC := make(chan string)

	go func() {
		for _, a := range [][]string{b.Inputs, b.ImplicitInputs} {
			for _, i := range a {
				if c.hashes.Get(i) != nil {
					filesC <- i
				}
			}
		}
		close(filesC)
	}()

	return c.fetchHashes(filesC)
}

func (c *ABFSBackend) MkdirAll(dir string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return c.ensureDirCached(dir)
}

// CreateDirs implements backends.Backend.
func (c *ABFSBackend) CreateDirs(step *build.Step) error {
	if c.dryRun {
		return nil
	}

	for _, t := range step.Node.Targets {
		if err := c.MkdirAll(path.Dir(t)); err != nil {
			return err
		}
	}

	depFile := step.Node.DepFile()
	if depFile != "" {
		if err := c.MkdirAll(path.Dir(depFile)); err != nil {
			return err
		}
	}
	return nil
}

// CreateRSP implements backends.Backend.
func (c *ABFSBackend) CreateRSP(step *build.Step) error {
	if c.dryRun {
		return nil
	}

	rspFile := step.RSPFile()
	if rspFile == "" {
		return nil
	}

	if err := c.MkdirAll(path.Dir(rspFile)); err != nil {
		return err
	}

	content := []byte(step.RSPContent())
	err := os.WriteFile(rspFile, content, 0644)
	if err != nil {
		return err
	}

	hs, err := fileHashStat(rspFile)
	if err != nil {
		return err
	}
	c.hashes.Put(rspFile, hs)

	return nil
}

// ReadNinjaFile implements backends.Backend.
func (c *ABFSBackend) ReadNinjaFile(fileName string) ([]byte, parse.ReleaseFileFunc, error) {
	return ninjaruntime.ReadMMapFile(fileName)
}

// FinalizeStep implements backends.Backend.
func (c *ABFSBackend) FinalizeStep(step *build.Step) {
	c.evals.Remove(step.Node)
}

// FinalizeBuild implements backends.Backend.
func (c *ABFSBackend) FinalizeBuild(err error) error {
	close(c.hashFetchC)
	c.pendingJobs.Wait()

	states := c.engine.StateCounts()

	failed := states[build.StateFailed]
	cached := states[build.StateCached]
	done := states[build.StateDone]
	hitRate := 0.0
	if cached+done != 0 {
		hitRate = float64(cached) / float64(cached+done) * 100
	}
	msg := fmt.Sprintf(
		"Evaluation took %s, fail: %d, cache: hits: %d, misses: %d, ratio: %.3f",
		time.Since(c.startTime), failed, cached, done, hitRate,
	)
	c.log.Warnf("%s", msg)

	return writeStats(c, err)
}

func rspPatchEntry(rspFile string, rspContent string) *entities.PatchEntry {
	content := []byte(rspContent)
	return &entities.PatchEntry{
		Path: rspFile,
		Hash: objects.HashBlobBytes(content),
		Size: int64(len(content)),
		Mode: entities.FileModeRegular,
	}
}

func (c *ABFSBackend) logMismatch(format string, args ...any) error {
	withTimestamp := []any{time.Now().UnixMilli()}
	withTimestamp = append(withTimestamp, args...)

	_, err := fmt.Fprintf(c.mismatchFile, "%-13d "+format, withTimestamp...)
	return err
}

func (c *ABFSBackend) Close() error {
	var err error
	if c.mismatchFile != nil {
		err = c.mismatchFile.Close()
		c.mismatchFile = nil
	}

	return err
}

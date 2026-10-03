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

package dagsync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects/gitobjects"
	"github.com/google/abfs/shared/remote"
)

var Debug = false

func debugLog(logger *log.Logger, format string, v ...any) {
	if Debug {
		logger.Printf(format, v...)
	}
}

const (
	maintenanceRunInterval = 5 * time.Second
	placeholderNodeTimeout = 1 * time.Hour
	maxFailCooldown        = 10 * time.Minute
	failCooldown           = 5 * time.Second
	waitTickerCooldown     = 10 * time.Millisecond
	maxSkipSetSize         = 1000
)

type NodeState int

const (
	HolderState               NodeState = iota // The node is only a placeholder for the object
	InitialLinkBuildingState                   // The node is going through the initial link generation
	FailedLinkBuildState                       // The node has failed to build the links, it will be retried
	QueuedState                                // The node is ready to be executed and is added to the queue
	BlockedState                               // The node is blocked by one of its dependencies
	RunningState                               // The node is being executed
	FixingLinkValidationState                  // The node failed to execute due to a link validation error, and it is being fixed before re-activating
	FailedState                                // The execution of the node has failed, it will be retried
	maxState                                   // Not a valid state just the last state to have the count
)

func (s NodeState) String() string {
	switch s {
	case HolderState:
		return "placeholder"
	case InitialLinkBuildingState:
		return "link-building"
	case FailedLinkBuildState:
		return "failed-link-building"
	case QueuedState:
		return "queued"
	case BlockedState:
		return "blocked"
	case RunningState:
		return "running"
	case FixingLinkValidationState:
		return "fixing-link-validation"
	case FailedState:
		return "failed"
	default:
		return fmt.Sprintf("unknown-state-%d", s)
	}
}

func (s NodeState) IsLinked() bool {
	switch s {
	case HolderState, InitialLinkBuildingState, FailedLinkBuildState:
		return false
	default:
		return true
	}
}

type node struct {
	tag     string
	touch   time.Time
	retries int
	state   NodeState
	// each object can potentially have multiple links to another object (imagine a dir with two files with the same content)
	deps      map[*node]int
	revDeps   map[*node]int
	degree    int
	queuedRef int
	request   Request
	priority  QueuePriority
	callbacks []Callback
}

func (n *node) String() string {
	return n.tag
}

type DAGSync struct {
	lock                   sync.Mutex
	stopC                  chan struct{}
	stopped                atomic.Bool
	mailbox                *Mailbox
	mailboxLen             atomic.Int32
	updater                ABFSUpdater
	accessor               ObjectAccessor
	onPush                 func(entities.Hash, string)
	workersCount           int
	runningCount           int
	nodes                  map[string]*node
	stateCounts            []map[string]int64
	brokenLinks            int64
	missingObjects         int64
	queue                  *PriorityQueue
	MaintenanceRunInterval time.Duration
	FailCooldown           time.Duration
	PlaceholderNodeTimeout time.Duration
	logger                 *log.Logger

	skipMu           sync.Mutex
	skipSet          map[entities.Hash]bool
	offlineThreshold uint64
	peekOffline      func(entities.Hash) (bool, bool)
	sizer            SizeProvider

	pendingJobs sync.WaitGroup

	MailboxDelay time.Duration
}

type ABFSUpdater interface {
	Headers(ctx context.Context, keys []remote.Hash) ([]entities.ObjectHeader, error)
	PutObject(ctx context.Context, key entities.Hash, reader remote.ContentReadCloser) error
	AddMapResult(ctx context.Context, from, to entities.Hash) error
	PutRefs(ctx context.Context, server string, project string, refs []entities.RefHash) error
}

type ReadSizeCloser interface {
	io.ReadCloser
	entities.Sizer
}

type ObjectAccessor interface {
	GetObject(ctx context.Context, key entities.Hash) ([]byte, error)
	// OpenBlob is required to return a ReadSizeCloser because the size is needed as a header
	// to the object. If the size isn't provided, all the bytes must be read into memory, which
	// is an issue for concurrent large blobs.
	OpenBlob(ctx context.Context, key entities.Hash) (ReadSizeCloser, error)
}

type SizeProvider interface {
	ObjectSize(ctx context.Context, objectHash entities.Hash) (int64, error)
	BlobSize(ctx context.Context, objectHash entities.Hash) (int64, error)
}

type DagSyncOpts struct {
	Updater      ABFSUpdater
	Accessor     ObjectAccessor
	WorkersCount int

	PeekOffline func(entities.Hash) (bool, bool)
	OnPush      func(entities.Hash, string)

	// If = 0, skipping is disabled.
	// If enabled, the ObjectAccessor must also be a SizeProvider.
	OfflineThreshold uint64
	Logger           *log.Logger
}

func New(opts *DagSyncOpts) *DAGSync {
	counts := make([]map[string]int64, maxState)
	for i := range counts {
		counts[i] = make(map[string]int64, len(requestsTypes))
		for _, r := range requestsTypes {
			counts[i][r.Type()] = 0
		}
	}

	var sizer SizeProvider
	if opts.OfflineThreshold > 0 {
		sizer = opts.Accessor.(SizeProvider)
	}

	logger := opts.Logger
	if logger == nil {
		logger = log.Default()
	}

	return &DAGSync{
		mailbox:                NewMailbox(),
		updater:                opts.Updater,
		accessor:               opts.Accessor,
		onPush:                 opts.OnPush,
		nodes:                  make(map[string]*node),
		queue:                  NewPriorityQueue(),
		stateCounts:            counts,
		workersCount:           opts.WorkersCount,
		FailCooldown:           failCooldown,
		MaintenanceRunInterval: maintenanceRunInterval,
		PlaceholderNodeTimeout: placeholderNodeTimeout,
		logger:                 logger,

		skipSet:          make(map[entities.Hash]bool),
		offlineThreshold: opts.OfflineThreshold,
		peekOffline:      opts.PeekOffline,
		sizer:            sizer,
	}
}

func (d *DAGSync) UpdateLogger(logger *log.Logger) {
	// Don't need a mutex here since we are performing a pointer assignment, which
	// is already atomic
	d.logger = logger
}

func (d *DAGSync) State(tag string) (NodeState, bool) {
	d.lock.Lock()
	defer d.lock.Unlock()

	n := d.nodes[tag]
	if n == nil {
		return 0, false
	}
	return n.state, true
}

// Dump writes the information of all nodes to the given writer (used for debugging purposes).
// It locks the global lock so it should not be used lightly.
func (d *DAGSync) Dump(ctx context.Context, w io.Writer) {
	d.lock.Lock()
	defer d.lock.Unlock()

	_, _ = fmt.Fprintf(w, "Nodes=%d broken=%d missing=%d queue=%d\n", len(d.nodes), d.brokenLinks, d.missingObjects, d.queue.Len())
	i := 0
	for t, n := range d.nodes {
		if ctx.Err() != nil {
			return
		}
		i++
		state := n.state.String()
		if n.state == FailedState {
			state = fmt.Sprintf("%s retries=%d", state, n.retries)
		}
		_, _ = fmt.Fprintf(w, "%d %10f %s %s %s deg=%d rev-dep=", i, time.Since(n.touch).Seconds(), n.request.Type(), state, t, n.degree)
		j := 0
		for m := range n.revDeps {
			j++
			if j > 1 {
				_, _ = fmt.Fprintf(w, ",")
			}
			_, _ = fmt.Fprintf(w, "%s", m)
		}
		_, _ = fmt.Fprintf(w, "\n")
	}
}

// Validate checks the internal state and prints any inconsistentcies to the given writer (used for debugging purposes).
// It locks the global lock so it should not be used lightly.
func (d *DAGSync) Validate(ctx context.Context, w io.Writer, nodeStaleTimeout time.Duration) {
	d.lock.Lock()
	defer d.lock.Unlock()

	errCount := 0
	writeErr := func(format string, a ...any) {
		errCount++
		_, _ = fmt.Fprintf(w, format+"\n", a...)
	}
	degrees := make(map[*node]int)
	for _, n := range d.nodes {
		if ctx.Err() != nil {
			return
		}
		if n.state != QueuedState && n.queuedRef != -1 {
			writeErr("%s is not in queued state but has queuedRef", n)
		}
		switch n.state {
		case QueuedState:
			if n.queuedRef == -1 {
				writeErr("%s is in queued state but queuedRef is nil", n)
			}
			if n.degree != 0 {
				writeErr("%s is in queued state but its degree is %d", n, n.degree)
			}
		case BlockedState:
			// from FixingLinkValidationState, we move directly to BlockedState but the degree might still be 0 until
			// the new dependencies are processed
		case RunningState:
			if time.Since(n.touch) > nodeStaleTimeout {
				writeErr("Running %s takes too long (%s)", n, time.Since(n.touch))
			}
		case HolderState:
			if n.degree != 0 {
				writeErr("%s is a placeholder but has a non-zero degree (%d)", n, n.degree)
			}
		case InitialLinkBuildingState:
			if time.Since(n.touch) > nodeStaleTimeout {
				writeErr("Building links for %s took too long (%s)", n, time.Since(n.touch))
			}
		case FailedLinkBuildState:
			if time.Since(n.touch) > d.failedNodeCooldown(n) {
				writeErr("%s in %s for too long (%s)", n, n.state, time.Since(n.touch))
			}
		case FixingLinkValidationState:
			if time.Since(n.touch) > nodeStaleTimeout {
				writeErr("Fixing link validating for %s takes too long (%s)", n, time.Since(n.touch))
			}
		case FailedState:
			if time.Since(n.touch) > d.failedNodeCooldown(n) {
				writeErr("%s in Failed for too long (%s)", n, time.Since(n.touch))
			}
		default:
			writeErr("%s has invalid state %s", n, n.state)
			continue
		}
		for m, c := range n.revDeps {
			if d.nodes[m.tag] == nil {
				writeErr("%s has a rev-deps link to %s which is not part of the graph", n, m)
			}
			if m.deps[n] != c {
				writeErr("%s has a deps link to %s but revs do not match (%d != %d)", m, n, m.deps[n], c)
			}
			if n.state.IsLinked() {
				degrees[m] += c
			}
		}
	}
	for _, n := range d.nodes {
		if ctx.Err() != nil {
			return
		}
		if n.degree != degrees[n] {
			writeErr("%s has a degree of %d, but has observed links of %d", n, n.degree, degrees[n])
		}
	}

	if d.runningCount > d.workersCount {
		writeErr("More jobs (%d) are in running states than expected (%d)", d.runningCount, d.workersCount)
	}
	if errCount == 0 {
		_, _ = fmt.Fprintf(w, "all good!\n")
	}
}

type Stats struct {
	States         map[string]map[string]int64
	BrokenLinks    int64
	MissingObjects int64
}

func (d *DAGSync) Stats() Stats {
	d.lock.Lock()
	defer d.lock.Unlock()

	counts := make(map[string]map[string]int64, maxState)
	for i := 0; i < int(maxState); i++ {
		cc := make(map[string]int64, len(requestsTypes))
		for _, r := range requestsTypes {
			cc[r.Type()] = d.stateCounts[i][r.Type()]
		}
		counts[NodeState(i).String()] = cc
	}
	return Stats{
		States:         counts,
		BrokenLinks:    d.brokenLinks,
		MissingObjects: d.missingObjects,
	}
}

func (d *DAGSync) Send(m Message) {
	debugLog(d.logger, "Sending message %T %+v", m, m)
	d.mailboxLen.Add(1)
	d.mailbox.Send(m)
}

func (d *DAGSync) transit(n *node, state NodeState) {
	debugLog(d.logger, "transit node state %q: %s -> %s", n, n.state, state)
	switch state {
	case QueuedState:
		if n.degree != 0 {
			d.logger.Printf("BUG: dagsync changing state to queued for %s while it has non-zero degree (%d)", n, n.degree)
		}
	case BlockedState:
		// from FixingLinkValidationState, we move directly to BlockedState but the degree might still be 0 until
		// the new dependencies are processed
	case RunningState:
		if n.state != QueuedState {
			d.logger.Printf("BUG: dagsync changing state to running for %s from %s", n, n.state)
		}
		d.runningCount++
	case InitialLinkBuildingState:
		if n.state != HolderState {
			d.logger.Printf("BUG: dagsync changing state to Updating for %s from %s", n, n.state)
		}
	case FixingLinkValidationState:
		if n.state != RunningState {
			d.logger.Printf("BUG: dagsync changing state to running for %s from %s", n, n.state)
		}
	}
	switch n.state {
	case RunningState:
		d.runningCount--
		if state != FailedState && state != FixingLinkValidationState {
			d.logger.Printf("BUG: dagsync changing state from Running for %s to %s", n, state)
		}
	}
	n.touch = time.Now()
	d.stateCounts[n.state][n.request.Type()]--
	if n.state == QueuedState {
		debugLog(d.logger, "transit %q - removing node from queue because old state was queued", n)
		d.queue.Remove(n)
		n.queuedRef = -1
	}
	n.state = state
	d.stateCounts[n.state][n.request.Type()]++
	if state == QueuedState {
		debugLog(d.logger, "transit %q - enqueing node because new state is queued state", n)
		d.queue.Push(n)
	}
}

func (d *DAGSync) node(request Request) *node {
	tag := request.String()
	n := d.nodes[tag]
	if n == nil {
		debugLog(d.logger, "node - creating new node for %q", request)
		d.stateCounts[HolderState][request.Type()]++
		n = &node{
			tag:       tag,
			state:     HolderState,
			request:   request,
			deps:      nil,
			revDeps:   nil,
			queuedRef: -1,
			touch:     time.Now(),
			priority: QueuePriority{
				// priority is set afterwards via setPriorities()
				time: time.Now(),
			},
		}
		d.nodes[tag] = n
	}
	return n
}

// Run the function, in case of panic, recover and return it as error
func try(f func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic occurred: %v", r)
		}
	}()
	return f()
}

func (d *DAGSync) failedNodeCooldown(n *node) time.Duration {
	cooldown := d.FailCooldown * time.Duration(n.retries)
	if cooldown > maxFailCooldown {
		cooldown = maxFailCooldown
	}
	return cooldown
}

func (d *DAGSync) executedCallback(n *node, err error) {
	for i, cb := range n.callbacks {
		debugLog(d.logger, "calling exec-callback #%d for %q", i, n)
		go cb(err)
	}
}

// runNext gets the next highest-priority node from the queue and executes it.
// It returns false if there are no more jobs in the queue or if the maximum number of concurrent runs has been reached.
func (d *DAGSync) runNext(ctx context.Context) bool {
	defer func() {
		if r := recover(); r != nil {
			d.logger.Printf("panic occurred runNext(): %v\n%s", r, string(debug.Stack()))
		}
	}()

	d.lock.Lock()
	defer d.lock.Unlock()

	if d.queue.Len() == 0 {
		return false
	}
	if d.runningCount >= d.workersCount {
		return false
	}

	n := d.queue.Peek()

	if n.state != QueuedState {
		d.logger.Printf("BUG: runNext %q - node is at head of queue but isn't in queued state", n)
		return true
	}
	if n.degree != 0 {
		d.logger.Printf("BUG: runNext %q - expected %s to have zero deps but has %d", n, n, n.degree)
		return true
	}

	d.transit(n, RunningState)
	go d.execute(ctx, n)
	return true
}

func (d *DAGSync) execute(ctx context.Context, n *node) {
	debugLog(d.logger, "execute node %q", n)

	skipped := d.shouldSkip(n)
	var err error
	if !skipped {
		err = try(func() error { return n.request.Execute(ctx, d.updater, d.accessor) })
	}
	d.Send(&executed{node: n, skipped: skipped, err: err})
}

// maintain iterates over the nodes and removes stale holder nodes or retry failed jobs
func (d *DAGSync) maintain() {
	for _, n := range d.nodes {
		switch n.state {
		case HolderState:
			if len(n.deps) == 0 && len(n.revDeps) == 0 && time.Since(n.touch) > d.PlaceholderNodeTimeout {
				// moved to debug log because we haven't seen missing node issues recently, and this is now a frequent case with blobs
				// successfully uploading and then having placeholders created when they are later referenced
				debugLog(d.logger, "maintain %q - deleting node because it's a placeholder and has no deps or rev deps and is untouched longer than placeholder node timeout, %s", n, d.PlaceholderNodeTimeout)
				d.delete(n)
			}
		}
	}
}

func (d *DAGSync) delete(n *node) {
	debugLog(d.logger, "deleting node %s in %s state", n, n.state)
	d.stateCounts[n.state][n.request.Type()]--
	if n.state == RunningState {
		d.runningCount--
	}
	delete(d.nodes, n.tag)
}

func (d *DAGSync) schedule(delay time.Duration, n *node, f func()) {
	d.pendingJobs.Add(1)
	initTouch := n.touch
	initState := n.state

	go func() {
		defer d.pendingJobs.Done()

		if delay > 0 {
			select {
			case <-time.NewTimer(delay).C:
			case <-d.stopC:
				return
			}
		}
		if d.stopped.Load() {
			return
		}

		// The node's state has changed since it was scheduled, skip the task...
		if initTouch != n.touch {
			d.logger.Printf("Scheduled task for %s will be ignored because it is touched since it was scheduled (current-state=%s, init-state=%s)", n, n.state, initState)
			return
		}

		f()
	}()

}

func (d *DAGSync) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			d.mailbox.Close()
			// Drain all messages in the mailbox
			for range d.mailbox.Receive() {
			}
			return
		case m := <-d.mailbox.Receive():
			if d.MailboxDelay > 0 {
				time.Sleep(d.MailboxDelay)
			}
			debugLog(d.logger, "Received message %T %+v", m, m)
			d.mailboxLen.Add(-1)
			d.handle(m)
			for d.runNext(ctx) {
			}
		}
	}
}

func (d *DAGSync) handle(m Message) {
	defer func() {
		if r := recover(); r != nil {
			d.logger.Printf("panic occurred handle(): %v\n%s", r, string(debug.Stack()))
		}
	}()

	d.lock.Lock()
	defer d.lock.Unlock()

	switch r := m.(type) {
	case *newRequest:
		d.handleNewRequest(r)
	case *requestLinked:
		d.handleRequestLinked(r)
	case *executed:
		d.handleExecuted(r)
	case *retryFailedNode:
		d.handleRetryFailedNode(r)
	case *runMaintenance:
		d.maintain()
	default:
		log.Printf("unknown message type %T: %v", m, m)
	}
}

// setPriorities propogates the priorities recursively to all dependencies
func (d *DAGSync) setPriorities(n *node, priority RequestPriority) {
	if n.priority.priority >= priority {
		return
	}
	n.priority.priority = priority
	if n.queuedRef != -1 {
		d.queue.Update(n, priority)
	}
	for m := range n.deps {
		d.setPriorities(m, priority)
	}
}

func (d *DAGSync) handleNewRequest(r *newRequest) {
	n := d.node(r.request)
	d.setPriorities(n, r.priority)
	n.callbacks = append(n.callbacks, r.callbacks...)

	switch n.state {
	case FailedState:
		n.retries = 0
		d.activate(n)
		return
	case HolderState:
		// continue linking
	default:
		return
	}

	n.retries = 0
	d.transit(n, InitialLinkBuildingState)

	if putObj, ok := n.request.(*PutObjectRequest); ok && !putObj.IsBlob() {
		// only schedule links for objects that need I/O to calculate their links
		d.schedule(0, n, func() { d.linkRequest(n) })
	} else {
		// Shortcut links if the request does not do I/O in Links()
		oldType := n.request.Type()
		links, err := n.request.Links(d.accessor)
		d.handleRequestLinked(&requestLinked{node: n, links: links, err: err, oldType: oldType})
	}
}

func (d *DAGSync) linkRequest(n *node) {
	oldType := n.request.Type()
	links, err := n.request.Links(d.accessor)
	d.Send(&requestLinked{node: n, links: links, err: err, oldType: oldType})
}

func (d *DAGSync) handleRequestLinked(r *requestLinked) {
	n, links, err := r.node, r.links, r.err

	if err != nil {
		d.logger.Printf("failed to load links node %q, request %s (retried=%d), err: %s", n, n.request, n.retries, err)
		n.retries++
		d.transit(n, FailedLinkBuildState)
		d.schedule(d.failedNodeCooldown(n), n, func() { d.linkRequest(n) })
		return
	}

	// If the type changed during link resolution, update the metrics for the current state.
	oldType := r.oldType
	newType := n.request.Type()
	if newType != oldType {
		d.stateCounts[n.state][oldType]--
		d.stateCounts[n.state][newType]++
	}

	n.retries = 0
	if len(links) > 0 {
		n.deps = make(map[*node]int, len(links))
	}

	for _, link := range links {
		if link.IsZero() {
			continue
		}
		m := d.node(NewObjectRequest(link))
		d.addDependency(n, m)
	}
	for m, c := range n.revDeps {
		m.degree += c
		if m.degree == c && (m.state == QueuedState || m.state == FixingLinkValidationState) {
			d.transit(m, BlockedState)
		}
	}
	d.activate(n)
}

func (d *DAGSync) handleExecuted(e *executed) {
	n, skipped, err := e.node, e.skipped, e.err

	// If we are skipped, then all of our reverse dependencies also need to be
	// skipped.
	if skipped {
		for m := range n.revDeps {
			d.skip(m.request.Key())
		}
	}

	if n.state != RunningState {
		d.logger.Printf("Expected %s be in %s but it is in %s", n, RunningState, n.state)
		return
	}

	if err != nil {
		d.handleFailedExec(n, err)
		return
	}

	// reaching here means success
	if d.onPush != nil {
		if !skipped {
			d.onPush(n.request.Key(), n.request.Type())
		}
	}

	debugLog(d.logger, "execute for %q, going to lower degree of rev deps", n)
	for m, c := range n.revDeps {
		if m.deps[n] != c {
			d.logger.Printf("BUG: execute - %s has a deps link to %s but revs do not match (%d != %d)", m, n, m.deps[n], c)
		}
		m.degree -= c
		debugLog(d.logger, "execute lowering %q degree by %d because %q executed. degree after: %d", m, c, n, m.degree)
		m.deps[n] -= c
		if m.deps[n] == 0 {
			delete(m.deps, n)
		}
		if m.degree == 0 && m.state == BlockedState {
			d.transit(m, QueuedState)
		}
	}
	for m, c := range n.deps {
		if m.revDeps[n] != c {
			d.logger.Printf("BUG: execute - %s has a revDeps link to %s but revs do not match (%d != %d)", m, n, m.revDeps[n], c)
		}
		m.revDeps[n] -= c
		debugLog(d.logger, "execute lowering (%s).revDeps[%s] by %d because %q executed. m.revDeps[n] after: %d", m, n, c, n, m.revDeps[n])
		if m.revDeps[n] == 0 {
			delete(m.revDeps, n)
		}
	}
	d.delete(n)
	d.executedCallback(n, nil)
}

func (d *DAGSync) handleFailedExec(n *node, err error) {
	n.retries++
	var linkErr *remote.ErrObjectLinkValidation
	if errors.As(err, &linkErr) && len(linkErr.MissingLinks) > 0 {
		d.handleMissingLinks(n, linkErr)
	} else {
		d.transit(n, FailedState)
		d.schedule(d.failedNodeCooldown(n), n, func() { d.Send(&retryFailedNode{n}) })
	}
	d.executedCallback(n, err)
}

func (d *DAGSync) handleRetryFailedNode(r *retryFailedNode) {
	n := r.node
	d.activate(n)
}

// addDependency sets up the dependency between parent n and child m.
// It updates deps/revDeps and the degree of the parent if the child is already linked.
// It's ok for nodes to be linked multiple times because we count links. Sometimes this is
// a real use case like a dir containing 2 files with the same content.
func (d *DAGSync) addDependency(n, m *node) {
	d.setPriorities(m, n.priority.priority)

	if n.deps == nil {
		n.deps = make(map[*node]int)
	}
	if m.revDeps == nil {
		m.revDeps = make(map[*node]int)
	}
	m.revDeps[n]++
	n.deps[m]++
	if m.state.IsLinked() {
		n.degree++
		if n.degree > 0 && n.state == QueuedState {
			d.transit(n, BlockedState)
		}
	}
}

func (d *DAGSync) handleMissingLinks(n *node, linkErr *remote.ErrObjectLinkValidation) {
	d.transit(n, FixingLinkValidationState)
	for _, link := range linkErr.MissingLinks {
		debugLog(d.logger, "server rejected %s for broken link %s", n.tag, link)
		d.brokenLinks++
		newReq := NewObjectRequest(link)
		m := d.node(newReq)
		// it's ok to call this even if there is already a dependency link between the nodes
		d.addDependency(n, m)

		// if node m isn't in holder state, it is already in the graph but probably just wasn't properly
		// linked to the current node
		if m.state == HolderState {
			d.Send(&newRequest{request: newReq, priority: n.priority.priority})
		}
	}
	// we want to send node to blocked state instead of calling d.activate() because the dependencies might need time to get out
	// of the placeholder state. activate() would try to retry the node immediately since the degree hasn't yet incremented
	d.transit(n, BlockedState)
}

func (d *DAGSync) Start(ctx context.Context) {
	d.stopped.Store(false)
	d.stopC = make(chan struct{})

	d.pendingJobs.Add(1)
	go func() {
		defer d.pendingJobs.Done()
		ticker := time.NewTicker(d.MaintenanceRunInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if d.stopped.Load() {
					return
				}
				d.Send(&runMaintenance{})
			case <-d.stopC:
				return
			}
		}
	}()

	d.run(ctx)

	d.stopped.Store(true)
	close(d.stopC)
	d.pendingJobs.Wait()
}

// activate tells dagsync that we want to try uploading the node. It moves a node to a Queued or Blocked state depending on if there are unmet dependencies.
//
// This state transition is done regardless of the current state. Therefore, checks should be performed before calling it and likely only 1 calling location per source state
// should exist to prevent race conditions.
//
// The lock should be acquired before this function is called.
func (d *DAGSync) activate(n *node) {
	if n.degree == 0 {
		debugLog(d.logger, "activate sending node %q to queued state becuase it has degree 0", n)
		d.transit(n, QueuedState)
	} else {
		debugLog(d.logger, "activate sending node %q to blocked state becuase it has degree non-zero", n)
		d.transit(n, BlockedState)
	}
}

func (d *DAGSync) PutBlob(key entities.Hash) fmt.Stringer {
	r := &PutObjectRequest{hash: key, objType: entities.ObjectTypeBlob}
	d.Send(&newRequest{request: r})
	return r
}

func (d *DAGSync) PutTree(key entities.Hash) fmt.Stringer {
	r := &PutObjectRequest{hash: key, objType: gitobjects.ObjectTypeDir}
	d.Send(&newRequest{request: r})
	return r
}

func (d *DAGSync) PutObject(key entities.Hash) fmt.Stringer {
	r := &PutObjectRequest{hash: key}
	d.Send(&newRequest{request: r})
	return r
}

func (d *DAGSync) AddMapResult(from, to entities.Hash) fmt.Stringer {
	r := &AddMapRequest{from: from, to: to}
	d.Send(&newRequest{request: r})
	return r
}

func (d *DAGSync) PutRefs(server string, project string, refs []entities.RefHash, callbacks ...Callback) fmt.Stringer {
	r := &PutRefsRequest{server: server, project: project, refs: refs}
	d.Send(&newRequest{request: r, callbacks: callbacks})
	return r
}

func (d *DAGSync) Phony(priority RequestPriority, objects []entities.Hash, callback Callback) fmt.Stringer {
	r := &PhonyRequest{links: objects}
	d.Send(&newRequest{request: r, priority: priority, callbacks: []Callback{callback}})
	return r
}

// idle() returns true iff every node is in a placeholder
func (d *DAGSync) idle() bool {
	d.lock.Lock()
	defer d.lock.Unlock()

	if d.stopped.Load() {
		return true
	}

	if d.mailboxLen.Load() > 0 {
		return false
	}

	for _, s := range []NodeState{RunningState, QueuedState, InitialLinkBuildingState, FixingLinkValidationState} {
		if d.countNodesInState(s) > 0 {
			return false
		}
	}

	return true
}

func (d *DAGSync) countNodesInState(s NodeState) int64 {
	sum := int64(0)
	for _, n := range d.stateCounts[s] {
		sum += n
	}
	return sum
}

// Wait() returns when dagsync is idle or the timeoutDuration is reached.
// It returns error if there are any broken entries after the dagsync is idle.
func (d *DAGSync) Wait(timeoutDuration time.Duration) error {
	if err := d.waitForIdle(timeoutDuration); err != nil {
		return err
	}

	d.lock.Lock()
	fails := d.countNodesInState(FailedState)
	blockeds := d.countNodesInState(BlockedState)
	d.lock.Unlock()

	if fails > 0 || blockeds > 0 {
		return fmt.Errorf("dagsync is idle but there are %d failed and %d blocked nodes", fails, blockeds)
	}
	return nil
}

func (d *DAGSync) waitForIdle(timeoutDuration time.Duration) error {
	if d.idle() {
		return nil
	}

	timeoutTime := time.Now().Add(timeoutDuration)
	ticker := time.NewTicker(waitTickerCooldown)
	defer ticker.Stop()

	for {
		t := <-ticker.C

		if d.idle() {
			return nil
		}

		if t.After(timeoutTime) {
			return errors.New("timeout waiting for dagsync to be idle")
		}
	}
}

func (d *DAGSync) skipSetPut(hash entities.Hash) {
	d.skipMu.Lock()
	defer d.skipMu.Unlock()

	// We can safely clear the map at any time. Too-Big objects will
	// get their size queried again.
	if len(d.skipSet) >= maxSkipSetSize {
		clear(d.skipSet)
	}
	d.skipSet[hash] = true
}

func (d *DAGSync) skipSetGet(hash entities.Hash) bool {
	d.skipMu.Lock()
	defer d.skipMu.Unlock()

	return d.skipSet[hash]
}

func (d *DAGSync) skip(hash entities.Hash) {
	d.skipSetPut(hash)
}

func (d *DAGSync) objectSize(n *node) (int64, error) {
	objReq, isObjReq := n.request.(*PutObjectRequest)
	if !isObjReq {
		// ignore size for anything else
		return 0, nil
	}

	hash := n.request.Key()
	if objReq.IsBlob() {
		sz, err := d.sizer.BlobSize(context.Background(), hash)
		if err != nil {
			return 0, fmt.Errorf("could not get blob size for %v", hash)
		}
		return sz, err
	}

	// objReq is an object
	sz, err := d.sizer.ObjectSize(context.Background(), hash)
	if err != nil {
		return 0, fmt.Errorf("could not get object size for %v", hash)
	}
	return sz, err
}

func (d *DAGSync) shouldSkip(n *node) bool {
	// Bail early if we have an unconfigured size threshold.
	if d.offlineThreshold == 0 {
		return false
	}

	hash := n.request.Key()

	// Do we already have it marked skipped?
	if d.skipSetGet(hash) {
		return true
	}

	// Was this object's offline status determined elsewhere?
	// We do not need to notify in this case.
	if d.peekOffline != nil {
		offline, found := d.peekOffline(hash)
		if found && offline {
			d.skipSetPut(hash)
			return true
		}
	}

	// Does this object violate size constraints?
	sz, err := d.objectSize(n)
	if err != nil {
		d.logger.Printf("shouldSkip err: %v\n", err)
		return false
	}
	tooBig := uint64(sz) > d.offlineThreshold

	if tooBig {
		d.skip(hash)
	}

	return tooBig
}

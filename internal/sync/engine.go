// Package sync runs the background indexing engine. A single worker goroutine
// processes sync requests one at a time (SQLite has a single writer), fed by
// three inputs: per-library interval jobs (gocron), folder file-system events
// (fsnotify), and explicit UI/API triggers. It also reclaims storage for
// libraries whose deletion grace period has elapsed.
package sync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	stdsync "sync"
	"time"

	"github.com/Toshik1978/folio/internal/db"
	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/events"
	"github.com/Toshik1978/folio/internal/ingest"
	"github.com/Toshik1978/folio/internal/logging"
	"github.com/Toshik1978/folio/internal/notify"
)

// Library status values. A healthy library is "active"; "syncing" is reported
// in-memory via Status rather than persisted (the worker leaves the DB status
// untouched while running and writes "error" only on failure).
const (
	statusError             = "error"
	statusPendingPurge      = "pending_purge"
	statusPurged            = "purged"
	purgeCheckInterval      = time.Minute
	defaultWatchDebounce    = 10 * time.Second
	defaultProgressInterval = 250 * time.Millisecond
)

// reqKind says what a queued request runs against its library.
type reqKind uint8

const (
	kindSync      reqKind = iota // read the library source and reconcile the catalog
	kindDeepIndex                // fill missing descriptions from the books' own files
)

// String returns the kind's wire name, as carried by Status.Kind.
func (k reqKind) String() string {
	if k == kindDeepIndex {
		return "deep_index"
	}

	return "sync"
}

// runKind maps the request kind to the kind a run outcome reports.
func (k reqKind) runKind() notify.RunKind {
	if k == kindDeepIndex {
		return notify.RunDeepIndex
	}

	return notify.RunSync
}

// syncReq is a queued unit of work: a library, what to run against it, and
// whether a sync is forced (bypassing checkpoint gating; sync only).
type syncReq struct {
	id    int64
	kind  reqKind
	force bool
}

// reqKey is the queue dedup key: one pending request per library and kind.
type reqKey struct {
	id   int64
	kind reqKind
}

func (r syncReq) key() reqKey { return reqKey{id: r.id, kind: r.kind} }

// Status is a snapshot of the engine's live work, surfaced by GET /api/sync/status.
type Status struct {
	Running bool    `json:"running"`           // a run is in progress
	Current int64   `json:"current,omitempty"` // library ID being worked on (0 if idle)
	Kind    string  `json:"kind,omitempty"`    // "sync" | "deep_index" while running, else empty
	Queued  []int64 `json:"queued"`            // library IDs waiting their turn
}

// DeepIndexer fills a library's missing book descriptions from the books' own
// files. *ingest.DeepIndexer satisfies it. It takes the write guard per batch
// itself, so the engine does not hold the guard across a run.
type DeepIndexer interface {
	Run(
		ctx context.Context, library dbq.Library, scope ingest.DeepIndexScope, rep ingest.Reporter,
	) (ingest.DeepIndexResult, error)
}

// Publisher is the minimal surface the sync engine depends on. *Broker satisfies
// it. A nil Publisher is treated as a no-op by callers.
type Publisher interface {
	Publish(ev events.Event)
}

// StatsObserver is notified by the sync engine after catalog stats change
// (e.g. a successful sync or purge), so observers can react however they need.
type StatsObserver interface {
	StatsChanged()
}

// Notifier receives crash reports and run outcomes for the owner's
// notifications. *notify.Service satisfies it. Its methods never block. A nil
// Notifier disables reporting.
type Notifier interface {
	ReportCrash(place, detail string, recovered any, stack []byte) bool
	RunFinished(o notify.RunOutcome)
	LibraryRemoved(id int64)
}

// Engine owns the worker goroutine, the gocron scheduler, and the optional
// file-system watcher. Construct it with New and call Start once.
type Engine struct {
	log     *slog.Logger
	db      *sql.DB
	parsers map[string]Parser
	covers  ingest.CoverStore

	now      func() time.Time // injectable clock (tests)
	debounce time.Duration    // fsnotify quiet period before a folder re-sync

	wake chan struct{} // capacity 1: nudges the worker that the queue is non-empty
	stop chan struct{} // closed by Stop
	done chan struct{} // closed when the worker goroutine exits

	// writeGuard serializes the engine's heavy writers — a library sync and a
	// library purge — so a teardown never deletes rows while an indexing run is
	// mid-write. It is the process-wide single-writer guard (shared with the API
	// write handlers), so an indexing run also serializes against a concurrent
	// manual edit / Fix Match / cover upload at the SQLite layer. It is distinct
	// from mu, which only guards the in-memory queue state below.
	writeGuard *db.WriteGuard

	// bg tracks detached background goroutines (currently async purges started by
	// RequestPurge) so Stop can wait for an in-flight teardown to finish cleanly.
	bg stdsync.WaitGroup

	events      Publisher   // optional; nil disables event emission
	deepIndexer DeepIndexer // optional; nil makes deep index triggers no-ops

	mu          stdsync.Mutex
	queue       []syncReq       // pending work, FIFO
	queued      map[reqKey]bool // membership set for dedupe, per (library, kind)
	current     int64           // library ID being worked on, 0 when idle
	currentKind reqKind         // kind of the current run; meaningful only when current != 0

	scheduler *scheduler
	watcher   *watcher

	statsObserver StatsObserver
	notifier      Notifier // optional; nil disables owner notifications
}

// New builds an engine over the given database, parser registry, and cover
// store. parsers is keyed by library type (built by the composition root).
// writeGuard is the process-wide single-writer guard, shared with the API write
// handlers so an indexing run serializes against concurrent API writes.
func New(
	log *slog.Logger,
	database *sql.DB,
	writeGuard *db.WriteGuard,
	parsers map[string]Parser,
	covers ingest.CoverStore,
	opts ...Option,
) (*Engine, error) {
	e := &Engine{
		db:         database,
		writeGuard: writeGuard,
		parsers:    parsers,
		covers:     covers,
		log:        log,
		now:        time.Now,
		debounce:   defaultWatchDebounce,
		wake:       make(chan struct{}, 1),
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
		queued:     make(map[reqKey]bool),
	}

	sched, err := newScheduler(log, e.TriggerLibrary)
	if err != nil {
		return nil, err
	}
	e.scheduler = sched

	for _, opt := range opts {
		opt(e)
	}

	return e, nil
}

// Option customizes the Engine at construction.
type Option func(*Engine)

// WithEvents wires a publisher the Engine emits status/library/progress events to.
func WithEvents(p Publisher) Option {
	return func(e *Engine) { e.events = p }
}

// WithDeepIndexer wires the indexer that Deep Index runs use. Without one,
// TriggerDeepIndex and TriggerDeepIndexAll are no-ops.
func WithDeepIndexer(d DeepIndexer) Option {
	return func(e *Engine) { e.deepIndexer = d }
}

// WithStatsObserver wires an observer the Engine notifies when catalog stats change.
func WithStatsObserver(obs StatsObserver) Option {
	return func(e *Engine) { e.statsObserver = obs }
}

// WithNotifier wires the notifier the Engine reports crashes, run outcomes,
// and removed libraries to.
func WithNotifier(n Notifier) Option {
	return func(e *Engine) { e.notifier = n }
}

// libraryEvent is the payload of a TypeLibrary event: a single library row
// settled (sync success or error) or was reclaimed (purged). The client refetches
// the list on receipt.
type libraryEvent struct {
	ID     int64  `json:"id"`
	Status string `json:"status"` // "active" | "error" | "purged"
}

// completedSync is a sync run whose parser succeeded: the library it ran
// against, the parser's result, and the run's outcome with its book counts,
// not yet reported.
type completedSync struct {
	library dbq.Library
	result  ingest.Result
	outcome notify.RunOutcome
}

// Start launches the worker, scheduler, and folder watchers, then kicks off an
// initial sync of every library and an immediate purge sweep. It never blocks.
func (e *Engine) Start() {
	go e.worker()
	e.scheduler.start()

	ctx := context.Background()
	// gocron recovers a panicking job silently, so the sweep reports its own.
	sweep := func() {
		defer e.recoverCrash("purge sweep", nil)
		e.checkPurge(context.Background())
	}
	if err := e.scheduler.every(purgeCheckInterval, sweep); err != nil {
		e.log.Error("schedule purge checker", slog.Any("error", err))
	}
	// Create the watcher before Reschedule so the initial Reschedule attaches the
	// folder-library watches (and later Reschedules keep them in sync at runtime).
	if err := e.initWatcher(); err != nil {
		e.log.Error("init watcher", slog.Any("error", err))
	}
	if err := e.Reschedule(ctx); err != nil {
		e.log.Error("initial schedule", slog.Any("error", err))
	}

	e.checkPurge(ctx)
	e.TriggerAll()
}

// Stop shuts the engine down: it stops scheduling and watching, then waits for
// any in-flight sync and any background purge to finish.
//
// Ordering matters. close(e.stop) must precede scheduler.shutdown(): a purge
// sweep running on the scheduler can be blocked on the write guard held by an
// in-flight sync, and scheduler.shutdown() waits for running jobs to return.
// Closing e.stop first cancels the in-flight sync (its context is derived from
// e.stop), which releases the guard, lets the blocked sweep acquire it and
// return, and so lets scheduler.shutdown() complete instead of waiting out
// gocron's stop timeout. The worker observes e.stop independently and exits on
// its own, so the subsequent <-e.done cannot deadlock against the scheduler.
func (e *Engine) Stop() {
	if e.watcher != nil {
		e.watcher.Close()
	}
	close(e.stop)              // cancel in-flight work FIRST
	_ = e.scheduler.shutdown() // now a guard-blocked sweep can drain and return
	<-e.done
	e.bg.Wait()
}

// TriggerAll enqueues every library that is not awaiting deletion, respecting
// checkpoint gating (the startup pass).
func (e *Engine) TriggerAll() { e.triggerAll(false) }

// TriggerAllForced enqueues every non-purging library with checkpoint gating
// bypassed — the "Re-index All" action. Manual triggers always force;
// automatic ones (scheduler, watcher, startup) respect gating.
func (e *Engine) TriggerAllForced() { e.triggerAll(true) }

// TriggerLibrary enqueues a single library for syncing, respecting checkpoint
// gating (used by interval jobs, folder file-system events, and the manual
// "Sync Now" action).
func (e *Engine) TriggerLibrary(libraryID int64) {
	e.enqueue(syncReq{id: libraryID})
}

// TriggerLibraryForced enqueues a single library for an unconditional sync,
// bypassing checkpoint gating (used by the manual per-library "Re-index" action).
func (e *Engine) TriggerLibraryForced(libraryID int64) {
	e.enqueue(syncReq{id: libraryID, force: true})
}

// TriggerDeepIndex enqueues a Deep Index run over every unchecked book of one
// library. A library already queued for Deep Index is not queued twice; a
// pending_purge library is accepted here and skipped at run time.
func (e *Engine) TriggerDeepIndex(libraryID int64) {
	if !e.deepIndexEnabled() {
		return
	}
	e.enqueue(syncReq{id: libraryID, kind: kindDeepIndex})
}

// TriggerDeepIndexAll enqueues a Deep Index run for every library that is not
// awaiting deletion.
func (e *Engine) TriggerDeepIndexAll() {
	if !e.deepIndexEnabled() {
		return
	}
	e.enqueueAll(kindDeepIndex, false)
}

// Status reports the engine's current work.
func (e *Engine) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.statusLocked()
}

// deepIndexEnabled reports whether a deep indexer is wired, logging a skipped
// trigger when it is not.
func (e *Engine) deepIndexEnabled() bool {
	if e.deepIndexer == nil {
		e.log.Debug("deep index trigger ignored: no deep indexer configured")
		return false
	}

	return true
}

// statusLocked builds the status snapshot; the caller must hold e.mu.
func (e *Engine) statusLocked() Status {
	queued := make([]int64, len(e.queue))
	for i := range e.queue {
		queued[i] = e.queue[i].id
	}

	st := Status{
		Running: e.current != 0,
		Current: e.current,
		Queued:  queued,
	}
	if st.Running {
		st.Kind = e.currentKind.String()
	}

	return st
}

// emitStatus publishes the current state snapshot. It holds e.mu across Publish
// (which is non-blocking and does no IO) so status events are linearized with the
// state mutations; lock order e.mu -> broker -> subscription has no reverse path.
func (e *Engine) emitStatus() {
	if e.events == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events.Publish(events.Event{
		Type:        events.TypeStatus,
		CoalesceKey: "status",
		Data:        e.statusLocked(),
	})
}

// emitLibrary publishes that one library row settled. Reliable (no coalesce key).
func (e *Engine) emitLibrary(id int64, status string) {
	if e.events == nil {
		return
	}
	e.events.Publish(events.Event{
		Type: events.TypeLibrary,
		Data: libraryEvent{ID: id, Status: status},
	})
}

func (e *Engine) triggerAll(force bool) { e.enqueueAll(kindSync, force) }

// enqueueAll queues a request of the given kind for every library that is not
// awaiting deletion.
func (e *Engine) enqueueAll(kind reqKind, force bool) {
	srcs, err := dbq.New(e.db).ListLibraries(context.Background())
	if err != nil {
		e.log.Error("list libraries for trigger", slog.Any("error", err))
		return
	}
	reqs := make([]syncReq, 0, len(srcs))
	for i := range srcs {
		if srcs[i].Status == statusPendingPurge {
			continue
		}
		reqs = append(reqs, syncReq{id: srcs[i].ID, kind: kind, force: force})
	}
	e.enqueue(reqs...)
}

// enqueue appends unseen requests to the queue and wakes the worker. A library
// already queued for the same kind is not duplicated, but a forced sync request
// upgrades the pending sync to forced. It never blocks, so it is safe to call
// from HTTP handlers and scheduler callbacks.
func (e *Engine) enqueue(reqs ...syncReq) {
	e.mu.Lock()
	for _, req := range reqs {
		if e.queued[req.key()] {
			if req.force && req.kind == kindSync {
				for i := range e.queue {
					if e.queue[i].key() == req.key() {
						e.queue[i].force = true
					}
				}
			}

			continue
		}
		e.queued[req.key()] = true
		e.queue = append(e.queue, req)
	}
	e.mu.Unlock()

	select {
	case e.wake <- struct{}{}:
	default: // a wake-up is already pending
	}
	e.emitStatus()
}

// dequeue removes the next request and marks its library current. ok is false
// when the queue is empty.
func (e *Engine) dequeue() (syncReq, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.queue) == 0 {
		return syncReq{}, false
	}
	req := e.queue[0]
	e.queue = e.queue[1:]
	delete(e.queued, req.key())
	e.current = req.id
	e.currentKind = req.kind

	return req, true
}

// worker is the single sequential consumer. It drains the queue on each wake-up
// and exits when stop is closed.
func (e *Engine) worker() {
	defer close(e.done)
	for {
		select {
		case <-e.stop:
			return
		case <-e.wake:
			for {
				req, ok := e.dequeue()
				if !ok {
					break
				}
				e.emitStatus() // a run started / advanced (current set)
				e.safeSync(req)
				e.mu.Lock()
				e.current = 0
				e.mu.Unlock()
				e.emitStatus() // back to idle / next

				select {
				case <-e.stop:
					return
				default:
				}
			}
		}
	}
}

// safeSync runs one library sync under a panic guard so a parser/IO panic on the
// worker goroutine can never crash the process. On a panic it marks the library
// errored, reports the crash and the failed run, and the worker proceeds to the
// next item. The write guard is released correctly because syncLibrary defers
// its unlock.
func (e *Engine) safeSync(req syncReq) {
	defer func() {
		if r := recover(); r != nil {
			stack := debug.Stack()
			ctx := context.Background()
			// Marked: the crash report below stands in for the tapped log record.
			e.log.ErrorContext(logging.MarkReported(ctx), "sync panicked",
				slog.Int64("library", req.id), slog.Any("panic", r), slog.String("stack", string(stack)))
			name := e.libraryName(ctx, req.id)
			// A crash folded into an open de-dup window (or dropped) sent no
			// message, so the run failure must then speak for itself.
			crashSent := e.notifyCrash("sync engine",
				fmt.Sprintf("library %q %s", name, req.kind.runKind()), r, stack)
			e.notifyRun(notify.RunOutcome{
				LibraryID:     req.id,
				Library:       name,
				Kind:          req.kind.runKind(),
				Status:        notify.RunFailed,
				Reason:        fmt.Sprint(r),
				CrashReported: crashSent,
			})
			e.markError(ctx, req.id, fmt.Errorf("sync panicked: %v", r))
		}
	}()
	e.syncLibrary(req)
}

// libraryName is the library's name for a crash report, best-effort: "#<id>"
// when the row cannot be read.
func (e *Engine) libraryName(ctx context.Context, id int64) string {
	src, err := dbq.New(e.db).GetLibrary(ctx, id)
	if err != nil {
		return fmt.Sprintf("#%d", id)
	}

	return src.Name
}

// notifyCrash reports a recovered panic and whether it was queued as a
// message; a no-op (false) without a notifier.
func (e *Engine) notifyCrash(place, detail string, recovered any, stack []byte) bool {
	return e.notifier != nil && e.notifier.ReportCrash(place, detail, recovered, stack)
}

// notifyRun reports how a library run ended; a no-op without a notifier.
func (e *Engine) notifyRun(o notify.RunOutcome) {
	if e.notifier != nil {
		e.notifier.RunFinished(o)
	}
}

// notifyLibraryRemoved reports a purged library; a no-op without a notifier.
func (e *Engine) notifyLibraryRemoved(id int64) {
	if e.notifier != nil {
		e.notifier.LibraryRemoved(id)
	}
}

// runContext returns the context one queued run executes under: it is
// cancelled when the engine stops or when the returned cancel is called.
func (e *Engine) runContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-e.stop:
			cancel()
		case <-ctx.Done():
		}
	}()

	return ctx, cancel
}

// syncLibrary runs one queued request. A sync runs the parser for the library
// and records the outcome; when the parser is a Checkpointer and the run is not
// forced, an unchanged checkpoint skips the read+reconcile entirely. A sync
// that inserted books then fills descriptions for just those books, in the
// same queue turn. A deep index run is delegated to deepIndexLibrary.
func (e *Engine) syncLibrary(req syncReq) {
	ctx, cancel := e.runContext()
	defer cancel()

	if req.kind == kindDeepIndex {
		e.deepIndexLibrary(ctx, req.id)
		return
	}

	// The fill runs only once guardedSync has returned and so released the
	// whole-run write guard: the indexer takes the guard per batch itself.
	run, ok := e.guardedSync(ctx, req)
	if !ok {
		return
	}
	// The fill's counts join the sync's outcome: one sync gives one summary.
	out := run.outcome
	if run.result.Added > 0 {
		fill, err := e.safeFill(ctx, run.library, run.result.ImportedAt)
		addDeepIndexCounts(&out, fill)
		if err != nil && !errors.Is(err, context.Canceled) {
			out.FillFailed = err.Error()
		}
	}
	e.notifyRun(out)
}

// safeFill runs fillImported under its own panic guard. The sync has already
// committed by then, so a panic in the fill must not unwind into safeSync,
// which would report the sync as failed and lose its outcome; it is reported
// as a crash and becomes the fill's error instead.
func (e *Engine) safeFill(ctx context.Context, src dbq.Library, stamp int64) (res ingest.DeepIndexResult, err error) {
	defer e.recoverCrash(fmt.Sprintf("library %q post-sync fill", src.Name), func(r any) {
		err = fmt.Errorf("post-sync fill panicked: %v", r)
	})

	return e.fillImported(ctx, src, stamp)
}

// recoverCrash guards engine work that runs outside safeSync: it must be
// deferred directly (recover only works there). A panic is logged (marked
// reported), sent as a "sync engine" crash with detail, then handed to onPanic
// when non-nil.
func (e *Engine) recoverCrash(detail string, onPanic func(r any)) {
	r := recover() //nolint:revive // recoverCrash is itself always the deferred call, so recover works here
	if r == nil {
		return
	}
	e.reportPanic(detail, r, debug.Stack())
	if onPanic != nil {
		onPanic(r)
	}
}

// reportPanic logs a recovered panic as reported and sends it as a crash.
func (e *Engine) reportPanic(detail string, r any, stack []byte) {
	e.log.ErrorContext(logging.MarkReported(context.Background()), "engine task panicked",
		slog.String("task", detail), slog.Any("panic", r), slog.String("stack", string(stack)))
	e.notifyCrash("sync engine", detail, r, stack)
}

// guardedSync runs one sync request under the whole-run write guard. ok is true
// only when the parser ran and succeeded; a skipped, refused, or failed sync
// reports false, having reported its own outcome if it has one.
func (e *Engine) guardedSync(ctx context.Context, req syncReq) (completedSync, bool) {
	// Hold the single-writer guard for the whole run so a concurrent library
	// purge (HTTP "Purge Now" or the deadline sweep) — and any API write — waits
	// rather than racing the sync at the SQLite layer. The acquire honors ctx, so a
	// shutdown signalled while we are still queued behind another writer cancels the
	// run cleanly instead of blocking teardown.
	if err := e.writeGuard.Lock(ctx); err != nil {
		return completedSync{}, false
	}
	defer e.writeGuard.Unlock()

	q := dbq.New(e.db)

	src, err := q.GetLibrary(ctx, req.id)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) { // a missing library was deleted before its turn
			e.log.Error("load library", slog.Int64("library", req.id), slog.Any("error", err))
		}
		return completedSync{}, false
	}
	if src.Status == statusPendingPurge {
		return completedSync{}, false // do not sync libraries awaiting deletion
	}

	parser, found := e.parsers[src.Type]
	if !found {
		cause := fmt.Errorf("no parser for library type %q", src.Type)
		e.markError(ctx, req.id, cause)
		e.notifyRun(runOutcome(src, notify.RunSync, notify.RunFailed, cause.Error()))
		return completedSync{}, false
	}

	// Fingerprint the artifact once, before reading it. Storing this pre-read
	// value (rather than recomputing after the run) means an artifact modified
	// mid-sync yields a stored checkpoint that mismatches on the next pass, so
	// the concurrent change is picked up instead of skipped until the artifact
	// changes again.
	fp := e.fingerprint(parser, src)
	if e.shouldSkip(req, src, fp) {
		// Stamp the check time, but skip the change notifications: nothing in the
		// catalog moved, so busting the stats cache or emitting a library event
		// would make every connected client refetch on each unchanged cycle.
		e.stampLastSync(ctx, req.id)
		e.recheckSkipped(ctx, parser, src)
		// A skip is a successful check: it ends a failing streak like a full run.
		e.notifyRun(runOutcome(src, notify.RunSync, notify.RunCompleted, ""))
		return completedSync{}, false // unchanged artifact: skip read + reconcile
	}

	// Counted under the guard, before the parser runs, so the mass-removal
	// check compares against the catalog this run started from.
	before, err := q.CountBooksByLibrary(ctx, src.ID)
	if err != nil {
		e.log.Warn("count books before sync", slog.Int64("library", src.ID), slog.Any("error", err))
	}

	res, out, ok := e.runSync(ctx, parser, src, req, fp, int(before))

	return completedSync{library: src, result: res, outcome: out}, ok
}

// runSync executes the parser for one library, records the outcome, and stores
// the pre-read checkpoint fingerprint on success. ok reports whether the sync
// succeeded; out is then its completed outcome, carrying booksBefore and the
// books the run removed.
func (e *Engine) runSync(
	ctx context.Context, parser Parser, src dbq.Library, req syncReq, fp string, booksBefore int,
) (ingest.Result, notify.RunOutcome, bool) {
	e.log.Info(
		"sync start",
		slog.Int64("library", req.id),
		slog.String("type", src.Type),
		slog.String("path", src.Path),
	)
	reporter, finish := e.beginProgress(req.id)
	res, err := parser.Sync(ctx, src, e.db, e.covers, reporter)
	finish() // emit the final frame even if the run errored
	if err != nil {
		if errors.Is(err, context.Canceled) {
			e.log.Info("sync interrupted by shutdown", slog.Int64("library", req.id))
			return res, notify.RunOutcome{}, false
		}
		// Marked: the run-failed notification below stands in for the tapped log record.
		e.log.ErrorContext(logging.MarkReported(ctx), "sync failed",
			slog.Int64("library", req.id), slog.Any("error", err))
		e.markError(context.Background(), req.id, err)
		e.notifyRun(runOutcome(src, notify.RunSync, notify.RunFailed, err.Error()))

		return res, notify.RunOutcome{}, false
	}

	persistCtx := context.WithoutCancel(ctx) // keep values, drop cancellation
	e.recordLastSync(persistCtx, req.id)
	e.storeCheckpoint(persistCtx, src.ID, fp)
	if res.Added > 0 || res.Removed > 0 {
		e.optimize(persistCtx)
	}
	e.log.Info("sync done",
		slog.Int64("library", req.id), slog.Int("added", res.Added), slog.Int("removed", res.Removed))

	out := runOutcome(src, notify.RunSync, notify.RunCompleted, "")
	out.BooksBefore = booksBefore
	out.BooksRemoved = res.RemovedBooks

	return res, out, true
}

// fillImported fills descriptions for the books one sync run inserted (those
// stamped with its imported_at), never the library's backlog. It runs in the
// sync's queue turn but after the sync's write guard is released, and reports
// progress as a deep index run. A failure is logged and leaves the library's
// status alone: the sync itself succeeded, and the books stay unchecked for a
// later Deep Index. Without a deep indexer it does nothing. It returns the
// fill's result and error so the caller can fold them into the sync's outcome.
func (e *Engine) fillImported(ctx context.Context, src dbq.Library, stamp int64) (ingest.DeepIndexResult, error) {
	if e.deepIndexer == nil {
		return ingest.DeepIndexResult{}, nil
	}
	e.setCurrentKind(kindDeepIndex)

	reporter, finish := e.beginProgress(src.ID)
	res, err := e.runDeepIndex(ctx, src, ingest.DeepIndexImportedAt(stamp), reporter)
	finish() // emit the final frame even if the run errored
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			e.log.Warn("post-sync fill failed; the sync result stands",
				slog.Int64("library", src.ID), slog.Any("error", err))
		}
		return res, err
	}
	e.emitLibrary(src.ID, "active") // descriptions changed: clients refetch

	return res, nil
}

// setCurrentKind relabels the current run (a sync's post-sync fill reports as
// a deep index) and publishes the new status.
func (e *Engine) setCurrentKind(kind reqKind) {
	e.mu.Lock()
	e.currentKind = kind
	e.mu.Unlock()
	e.emitStatus()
}

// deepIndexLibrary runs the "Deep Index" action for one library: every
// unchecked book, with progress streamed like a sync. It is never
// checkpoint-gated and does not hold the whole-run write guard, because the
// indexer takes the guard per batch. On success it emits library{id,"active"}
// so clients refetch; on failure, including a library root that cannot be
// reached (see ingest.CheckReadRoot), it marks the library errored and reports
// a failed run.
func (e *Engine) deepIndexLibrary(ctx context.Context, id int64) {
	src, err := dbq.New(e.db).GetLibrary(ctx, id)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			e.log.Error("load library", slog.Int64("library", id), slog.Any("error", err))
		}
		return // a missing library was deleted before its turn
	}
	if src.Status == statusPendingPurge {
		return // do not index libraries awaiting deletion
	}

	// An unreachable root fails the run as a whole, rather than as one
	// retryable read error per book.
	if err = ingest.CheckReadRoot(src); err != nil {
		e.failDeepIndex(ctx, src, fmt.Errorf("deep index: %w", err))
		return
	}

	reporter, finish := e.beginProgress(id)
	res, err := e.runDeepIndex(ctx, src, ingest.DeepIndexAll(), reporter)
	finish() // emit the final frame even if the run errored
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			e.failDeepIndex(ctx, src, err)
		}

		return
	}
	e.emitLibrary(id, "active")
	out := runOutcome(src, notify.RunDeepIndex, notify.RunCompleted, "")
	addDeepIndexCounts(&out, res)
	e.notifyRun(out)
}

// failDeepIndex logs a failed Deep Index run, marks the library errored, and
// reports the failed run.
func (e *Engine) failDeepIndex(ctx context.Context, src dbq.Library, err error) {
	// Marked: the run-failed notification below stands in for the tapped log record.
	e.log.ErrorContext(logging.MarkReported(ctx), "deep index failed",
		slog.Int64("library", src.ID), slog.Any("error", err))
	e.markError(context.Background(), src.ID, err)
	e.notifyRun(runOutcome(src, notify.RunDeepIndex, notify.RunFailed, err.Error()))
}

// runDeepIndex runs the deep indexer over one library with the given scope and
// logs its start, completion, or shutdown interruption, returning the run's
// result; the caller logs a failure and decides what it means for the library.
// The caller must not hold the write guard: the indexer takes it per batch.
func (e *Engine) runDeepIndex(
	ctx context.Context, src dbq.Library, scope ingest.DeepIndexScope, rep ingest.Reporter,
) (ingest.DeepIndexResult, error) {
	e.log.Info("deep index start", slog.Int64("library", src.ID), slog.String("type", src.Type))
	res, err := e.deepIndexer.Run(ctx, src, scope, rep)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			e.log.Info("deep index interrupted by shutdown", slog.Int64("library", src.ID))
		}
		return res, fmt.Errorf("deep index: %w", err)
	}
	e.log.Info("deep index done", slog.Int64("library", src.ID),
		slog.Int("checked", res.Checked), slog.Int("filled", res.Filled),
		slog.Int("skipped", res.Skipped), slog.Int("errors", res.Errors))

	return res, nil
}

// shouldSkip reports whether a sync pass can be skipped because the artifact
// fingerprint is unchanged (non-forced run with a valid, matching checkpoint).
func (e *Engine) shouldSkip(req syncReq, src dbq.Library, fp string) bool {
	// A library whose last run failed (a sync or a Deep Index) is read in full:
	// a skip would report it recovered, and flip it back to active, without
	// having read anything.
	return !req.force && src.Status != statusError && fp != "" &&
		src.Checkpoint.Valid && src.Checkpoint.String == fp
}

// fingerprint returns the parser's current artifact fingerprint, or "" when the
// parser is not a Checkpointer. If Checkpoint returns an error the error is
// logged, checkpoint gating is disabled for this run, and "" is returned.
func (e *Engine) fingerprint(parser Parser, src dbq.Library) string {
	cp, ok := parser.(Checkpointer)
	if !ok {
		return ""
	}
	fp, err := cp.Checkpoint(src)
	if err != nil {
		e.log.Warn("fingerprint failed, checkpoint gating disabled",
			slog.Int64("library", src.ID), slog.Any("error", err))
		return ""
	}

	return fp
}

// stampLastSync persists the library's last sync time without emitting any
// change notification. Used on a no-op checkpoint skip, where the run touched
// nothing in the catalog and only the "last checked" timestamp advances.
func (e *Engine) stampLastSync(ctx context.Context, id int64) {
	err := dbq.New(e.db).UpdateLibraryLastSync(ctx, dbq.UpdateLibraryLastSyncParams{
		LastSyncAt: sql.NullInt64{Int64: e.now().Unix(), Valid: true}, ID: id,
	})
	if err != nil {
		e.log.Error("record last sync", slog.Int64("library", id), slog.Any("error", err))
	}
}

// recheckSkipped re-checks the broken-read records of a library whose sync was
// skipped on an unchanged checkpoint, so a book file replaced behind its index
// stops counting as unreadable. Only books with a record are looked at, by
// stat or zip directory, with no entry read. Lifted records change the
// unreadable count, so observers are told; otherwise the skip stays silent. A
// failure is logged and the skip stands.
func (e *Engine) recheckSkipped(ctx context.Context, parser Parser, src dbq.Library) {
	rc, ok := parser.(ReadFailureRechecker)
	if !ok {
		return
	}
	lifted, err := rc.RecheckReadFailures(ctx, src, e.db)
	if err != nil && !errors.Is(err, context.Canceled) {
		e.log.Warn("read failure re-check failed; the skip stands",
			slog.Int64("library", src.ID), slog.Any("error", err))
	}
	if lifted == 0 {
		return
	}
	e.log.Info("lifted stale read failure records",
		slog.Int64("library", src.ID), slog.Int("lifted", lifted))
	if e.statsObserver != nil {
		e.statsObserver.StatsChanged()
	}
	e.emitLibrary(src.ID, "active")
}

// recordLastSync stamps the library's last successful sync time and notifies
// observers that catalog state changed (stats cache + libraries list).
func (e *Engine) recordLastSync(ctx context.Context, id int64) {
	e.stampLastSync(ctx, id)
	// Notify before emitting so SSE clients that immediately refetch stats
	// see a fresh value. This also converges the libraries count after a new
	// library is created: the create path does not notify on its own, so
	// the first successful sync of that library is what closes the gap.
	if e.statsObserver != nil {
		e.statsObserver.StatsChanged()
	}
	e.emitLibrary(id, "active")
}

// storeCheckpoint records the pre-read fingerprint for the library; "" (no
// fingerprint available) is not stored.
func (e *Engine) storeCheckpoint(ctx context.Context, id int64, fp string) {
	if fp == "" {
		return
	}
	err := dbq.New(e.db).UpdateLibraryCheckpoint(ctx, dbq.UpdateLibraryCheckpointParams{
		Checkpoint: sql.NullString{String: fp, Valid: true}, ID: id,
	})
	if err != nil {
		e.log.Error("record checkpoint", slog.Int64("library", id), slog.Any("error", err))
	}
}

// optimize refreshes the planner statistics after a run changed the catalog.
// Callers hold the write guard, as the statistics are a write. A failure is
// only logged: stale statistics cost query speed, never correctness.
func (e *Engine) optimize(ctx context.Context) {
	if err := db.Optimize(ctx, e.db); err != nil {
		e.log.Warn("refresh planner statistics", slog.Any("error", err))
	}
}

// markError flags a library as failed with the given cause.
func (e *Engine) markError(ctx context.Context, id int64, cause error) {
	err := dbq.New(e.db).UpdateLibrarySyncError(ctx, dbq.UpdateLibrarySyncErrorParams{
		LastSyncError: sql.NullString{String: cause.Error(), Valid: true},
		ID:            id,
	})
	if err != nil {
		e.log.Error("record sync error", slog.Int64("library", id), slog.Any("error", err))
	}
	e.emitLibrary(id, "error")
}

// runOutcome is the outcome of one run of kind against src, with no counts.
func runOutcome(src dbq.Library, kind notify.RunKind, status notify.RunStatus, reason string) notify.RunOutcome {
	return notify.RunOutcome{LibraryID: src.ID, Library: src.Name, Kind: kind, Status: status, Reason: reason}
}

// addDeepIndexCounts records a deep index run's per-book retries and newly
// unreadable books (total and sample) on o.
func addDeepIndexCounts(o *notify.RunOutcome, res ingest.DeepIndexResult) {
	o.PerBookErrors = res.Errors
	o.NewlyUnreadable = res.Broken
	if len(res.NewlyBroken) == 0 {
		return
	}
	o.UnreadableSample = make([]notify.UnreadableBook, len(res.NewlyBroken))
	for i, b := range res.NewlyBroken {
		o.UnreadableSample[i] = notify.UnreadableBook{BookID: b.BookID, Reason: string(b.Reason)}
	}
}

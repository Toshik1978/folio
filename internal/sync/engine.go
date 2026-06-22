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
	stdsync "sync"
	"time"

	"github.com/Toshik1978/folio/internal/db"
	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/events"
	"github.com/Toshik1978/folio/internal/ingest"
	"github.com/Toshik1978/folio/internal/libtype"
)

// Library status values. A healthy library is "active"; "syncing" is reported
// in-memory via Status rather than persisted (the worker leaves the DB status
// untouched while running and writes "error" only on failure).
const (
	statusPendingPurge      = "pending_purge"
	statusPurged            = "purged"
	purgeCheckInterval      = time.Minute
	defaultWatchDebounce    = 10 * time.Second
	defaultWarmDelay        = 50 * time.Millisecond
	defaultProgressInterval = 250 * time.Millisecond
)

// syncReq is a queued unit of work: a library to sync and whether the run is
// forced (bypassing checkpoint gating).
type syncReq struct {
	id    int64
	force bool
}

// Status is a snapshot of the engine's live work, surfaced by GET /api/sync/status.
type Status struct {
	Running bool    `json:"running"`           // a sync is in progress
	Current int64   `json:"current,omitempty"` // library ID being synced (0 if idle)
	Queued  []int64 `json:"queued"`            // library IDs waiting their turn
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

	events Publisher // optional; nil disables event emission

	mu      stdsync.Mutex
	queue   []syncReq      // pending work, FIFO
	queued  map[int64]bool // membership set for dedupe
	current int64          // library ID being synced, 0 when idle

	scheduler *scheduler
	watcher   *watcher

	warmer *warmer // optional; nil disables INPX cover-warming

	statsObserver StatsObserver
}

// New builds an engine over the given database, parser registry, and cover
// store. parsers is keyed by library type (built by the composition root).
// extractor enables the INPX cover-warming pass; pass nil to disable warming.
// writeGuard is the process-wide single-writer guard, shared with the API write
// handlers so an indexing run serializes against concurrent API writes.
func New(
	log *slog.Logger,
	database *sql.DB,
	writeGuard *db.WriteGuard,
	parsers map[string]Parser,
	covers ingest.CoverStore,
	extractor CoverExtractor,
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
		queued:     make(map[int64]bool),
	}

	sched, err := newScheduler(log, e.TriggerLibrary)
	if err != nil {
		return nil, err
	}
	e.scheduler = sched

	if extractor != nil {
		e.warmer = newWarmer(log, database, covers, extractor, e.stop, defaultWarmDelay)
	}
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

// WithStatsObserver wires an observer the Engine notifies when catalog stats change.
func WithStatsObserver(obs StatsObserver) Option {
	return func(e *Engine) { e.statsObserver = obs }
}

// Start launches the worker, scheduler, and folder watchers, then kicks off an
// initial sync of every library and an immediate purge sweep. It never blocks.
func (e *Engine) Start() {
	go e.worker()
	if e.warmer != nil {
		go e.warmer.run()
	}
	e.scheduler.start()

	ctx := context.Background()
	if err := e.scheduler.every(purgeCheckInterval, func() { e.checkPurge(context.Background()) }); err != nil {
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
	if e.warmer != nil {
		<-e.warmer.done
	}
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

// Status reports the engine's current work.
func (e *Engine) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.statusLocked()
}

// statusLocked builds the status snapshot; the caller must hold e.mu.
func (e *Engine) statusLocked() Status {
	queued := make([]int64, len(e.queue))
	for i := range e.queue {
		queued[i] = e.queue[i].id
	}

	return Status{
		Running: e.current != 0,
		Current: e.current,
		Queued:  queued,
	}
}

// libraryEvent is the payload of a TypeLibrary event: a single library row
// settled (sync success or error) or was reclaimed (purged). The client refetches
// the list on receipt.
type libraryEvent struct {
	ID     int64  `json:"id"`
	Status string `json:"status"` // "active" | "error" | "purged"
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

func (e *Engine) triggerAll(force bool) {
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
		reqs = append(reqs, syncReq{id: srcs[i].ID, force: force})
	}
	e.enqueue(reqs...)
}

// enqueue appends unseen requests to the queue and wakes the worker. A library
// already queued is not duplicated, but a forced request upgrades the pending
// one to forced. It never blocks, so it is safe to call from HTTP handlers and
// scheduler callbacks.
func (e *Engine) enqueue(reqs ...syncReq) {
	e.mu.Lock()
	for _, req := range reqs {
		if e.queued[req.id] {
			if req.force {
				for i := range e.queue {
					if e.queue[i].id == req.id {
						e.queue[i].force = true
					}
				}
			}

			continue
		}
		e.queued[req.id] = true
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
	delete(e.queued, req.id)
	e.current = req.id

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
// errored and the worker proceeds to the next item. The write guard is released
// correctly because syncLibrary defers its unlock.
func (e *Engine) safeSync(req syncReq) {
	defer func() {
		if r := recover(); r != nil {
			e.log.Error("sync panicked", slog.Int64("library", req.id), slog.Any("panic", r))
			e.markError(context.Background(), req.id, fmt.Errorf("sync panicked: %v", r))
		}
	}()
	e.syncLibrary(req)
}

// syncLibrary runs the parser for one library and records the outcome. When the
// parser is a Checkpointer and the run is not forced, an unchanged checkpoint
// skips the read+reconcile entirely.
func (e *Engine) syncLibrary(req syncReq) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-e.stop:
			cancel()
		case <-ctx.Done():
		}
	}()

	// Hold the single-writer guard for the whole run so a concurrent library
	// purge (HTTP "Purge Now" or the deadline sweep) — and any API write — waits
	// rather than racing the sync at the SQLite layer.
	e.writeGuard.Lock()
	defer e.writeGuard.Unlock()

	q := dbq.New(e.db)

	src, err := q.GetLibrary(ctx, req.id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return // library was deleted before its turn
		}
		e.log.Error("load library", slog.Int64("library", req.id), slog.Any("error", err))
		return
	}
	if src.Status == statusPendingPurge {
		return // do not sync libraries awaiting deletion
	}

	parser, ok := e.parsers[src.Type]
	if !ok {
		e.markError(ctx, req.id, fmt.Errorf("no parser for library type %q", src.Type))
		return
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
		return // unchanged artifact: skip read + reconcile
	}

	e.runSync(ctx, parser, src, req, fp)
}

// runSync executes the parser for one library, records the outcome, and stores
// the pre-read checkpoint fingerprint on success.
func (e *Engine) runSync(ctx context.Context, parser Parser, src dbq.Library, req syncReq, fp string) {
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
			return
		}
		e.log.Error("sync failed", slog.Int64("library", req.id), slog.Any("error", err))
		e.markError(context.Background(), req.id, err)

		return
	}

	persistCtx := context.WithoutCancel(ctx) // keep values, drop cancellation
	e.recordLastSync(persistCtx, req.id)
	e.storeCheckpoint(persistCtx, src.ID, fp)
	e.log.Info("sync done",
		slog.Int64("library", req.id), slog.Int("added", res.Added), slog.Int("removed", res.Removed))

	if src.Type == libtype.INPX && e.warmer != nil {
		e.warmer.enqueue(req.id) // low-priority background cover extraction
	}
}

// shouldSkip reports whether a sync pass can be skipped because the artifact
// fingerprint is unchanged (non-forced run with a valid, matching checkpoint).
func (e *Engine) shouldSkip(req syncReq, src dbq.Library, fp string) bool {
	return !req.force && fp != "" && src.Checkpoint.Valid && src.Checkpoint.String == fp
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
	if err := dbq.New(e.db).UpdateLibraryLastSync(ctx, dbq.UpdateLibraryLastSyncParams{
		LastSyncAt: sql.NullInt64{Int64: e.now().Unix(), Valid: true}, ID: id,
	}); err != nil {
		e.log.Error("record last sync", slog.Int64("library", id), slog.Any("error", err))
	}
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
	if err := dbq.New(e.db).UpdateLibraryCheckpoint(ctx, dbq.UpdateLibraryCheckpointParams{
		Checkpoint: sql.NullString{String: fp, Valid: true}, ID: id,
	}); err != nil {
		e.log.Error("record checkpoint", slog.Int64("library", id), slog.Any("error", err))
	}
}

// markError flags a library as failed with the given cause.
func (e *Engine) markError(ctx context.Context, id int64, cause error) {
	if err := dbq.New(e.db).UpdateLibrarySyncError(ctx, dbq.UpdateLibrarySyncErrorParams{
		LastSyncError: sql.NullString{String: cause.Error(), Valid: true},
		ID:            id,
	}); err != nil {
		e.log.Error("record sync error", slog.Int64("library", id), slog.Any("error", err))
	}
	e.emitLibrary(id, "error")
}

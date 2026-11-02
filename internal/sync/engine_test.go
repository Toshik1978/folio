package sync

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	stdsync "sync"
	"time"

	"github.com/Toshik1978/folio/internal/db"
	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ingest"
)

type engineSuite struct {
	baseSuite
}

// startWorker runs just the worker goroutine (no scheduler/watcher/initial
// trigger) so tests drive the queue explicitly, and stops it on cleanup.
func (s *engineSuite) startWorker() {
	go s.engine.worker()
	s.T().Cleanup(func() {
		close(s.engine.stop)
		<-s.engine.done
	})
}

func (s *engineSuite) TestTriggerLibraryRecordsLastSync() {
	src := s.insertLibrary("stub", "/lib/a")
	s.startWorker()

	s.engine.TriggerLibrary(src.ID)

	s.Require().Eventually(func() bool {
		return s.getLibrary(src.ID).LastSyncAt.Valid
	}, time.Second, 5*time.Millisecond)

	got := s.getLibrary(src.ID)
	s.True(got.LastSyncAt.Valid)
	s.False(got.LastSyncError.Valid)
	s.Equal(1, s.parser.callCount())
}

func (s *engineSuite) TestCheckpointGatingSkipsUnchanged() {
	s.parser.checkpoint = "cp-1"
	src := s.insertLibrary("stub", "/lib/cp")

	// First run: no stored checkpoint → runs and records "cp-1".
	s.engine.syncLibrary(syncReq{id: src.ID})
	s.Equal(1, s.parser.callCount())
	s.Equal("cp-1", s.getLibrary(src.ID).Checkpoint.String)

	// Unchanged checkpoint → skipped.
	s.engine.syncLibrary(syncReq{id: src.ID})
	s.Equal(1, s.parser.callCount())

	// Forced run → bypasses the gate.
	s.engine.syncLibrary(syncReq{id: src.ID, force: true})
	s.Equal(2, s.parser.callCount())

	// Changed checkpoint → runs again.
	s.parser.checkpoint = "cp-2"
	s.engine.syncLibrary(syncReq{id: src.ID})
	s.Equal(3, s.parser.callCount())
}

// recordingStats counts StatsChanged calls so tests can assert whether a sync
// pass invalidated the stats cache.
type recordingStats struct {
	mu    stdsync.Mutex
	count int
}

func (r *recordingStats) StatsChanged() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.count++
}

func (r *recordingStats) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}

// TestCheckpointSkipDoesNotBustCaches guards 2.5b: a no-op checkpoint skip must
// still stamp last_sync_at, but must NOT invalidate the stats cache or emit a
// library event — otherwise every connected client needlessly refetches stats
// and the libraries list on every unchanged cycle.
func (s *engineSuite) TestCheckpointSkipDoesNotBustCaches() {
	s.parser.checkpoint = "cp-1"
	rec := &recordingPublisher{}
	stats := &recordingStats{}
	eng, err := New(slog.New(slog.DiscardHandler), s.db, db.NewWriteGuard(),
		map[string]Parser{"stub": s.parser}, s.store,
		WithEvents(rec), WithStatsObserver(stats))
	s.Require().NoError(err)

	src := s.insertLibrary("stub", "/lib/skip-cache")

	// First run: executes, stamps, busts the stats cache, emits a library event.
	eng.now = func() time.Time { return time.Unix(1000, 0) }
	eng.syncLibrary(syncReq{id: src.ID})
	s.Require().Equal(1, s.parser.callCount())
	s.Require().Equal(1, stats.calls())
	s.Require().Equal([]int64{src.ID}, rec.libraryIDs())
	s.Require().Equal(int64(1000), s.getLibrary(src.ID).LastSyncAt.Int64)

	// Second run at a later time: unchanged checkpoint → skip.
	eng.now = func() time.Time { return time.Unix(2000, 0) }
	eng.syncLibrary(syncReq{id: src.ID})

	s.Equal(1, s.parser.callCount(), "skip must not re-run the parser")
	s.Equal(int64(2000), s.getLibrary(src.ID).LastSyncAt.Int64,
		"skip must still stamp last_sync_at with the current time")
	s.Equal(1, stats.calls(), "skip must not invalidate the stats cache")
	s.Equal([]int64{src.ID}, rec.libraryIDs(), "skip must not emit another library event")
}

func (s *engineSuite) TestParserErrorMarksLibraryFailed() {
	s.parser.err = errors.New("boom")
	src := s.insertLibrary("stub", "/lib/b")
	s.startWorker()

	s.engine.TriggerLibrary(src.ID)

	s.Require().Eventually(func() bool {
		return s.getLibrary(src.ID).LastSyncError.Valid
	}, time.Second, 5*time.Millisecond)

	got := s.getLibrary(src.ID)
	s.Equal("error", got.Status)
	s.Equal("boom", got.LastSyncError.String)
	s.False(got.LastSyncAt.Valid)
}

func (s *engineSuite) TestUnknownLibraryTypeIsAnError() {
	src := s.insertLibrary("mystery", "/lib/c")
	s.startWorker()

	s.engine.TriggerLibrary(src.ID)

	s.Require().Eventually(func() bool {
		return s.getLibrary(src.ID).LastSyncError.Valid
	}, time.Second, 5*time.Millisecond)

	s.Equal("error", s.getLibrary(src.ID).Status)
	s.Zero(s.parser.callCount())
}

func (s *engineSuite) TestPendingPurgeLibraryIsSkipped() {
	src := s.insertLibrary("stub", "/lib/d")
	s.markPendingPurge(src.ID, sql.NullInt64{})
	s.startWorker()

	s.engine.TriggerLibrary(src.ID)
	// give the worker a chance to (not) run it
	time.Sleep(50 * time.Millisecond)

	s.Zero(s.parser.callCount())
}

// H2: a sync finishing after the user deleted the library must not resurrect it
// to 'active' (which would silently cancel the purge while leaving purge_at set).
func (s *engineSuite) TestRecordLastSyncKeepsPendingPurge() {
	src := s.insertLibrary("stub", "/lib/h2")
	s.markPendingPurge(src.ID, sql.NullInt64{Int64: 99, Valid: true})

	s.engine.recordLastSync(context.Background(), src.ID)

	got := s.getLibrary(src.ID)
	s.Equal(statusPendingPurge, got.Status, "completed sync must not resurrect a deleted library")
	s.True(got.PurgeAt.Valid, "purge_at must remain set")
}

// M3: RequestPurge tears the library down on a background goroutine; Stop waits
// for it to finish.
func (s *engineSuite) TestRequestPurgeRunsInBackground() {
	src := s.insertLibrary("stub", "/lib/m3")

	s.engine.Start()
	s.engine.RequestPurge(src.ID)
	s.engine.Stop() // must wait for the background purge

	_, err := dbq.New(s.db).GetLibrary(context.Background(), src.ID)
	s.ErrorIs(err, sql.ErrNoRows, "the library row must be gone after a background purge")
}

func (s *engineSuite) TestSequentialSingleWriter() {
	s.parser.delay = 30 * time.Millisecond
	a := s.insertLibrary("stub", "/lib/seq-a")
	b := s.insertLibrary("stub", "/lib/seq-b")
	s.startWorker()

	s.engine.TriggerLibrary(a.ID)
	s.engine.TriggerLibrary(b.ID)

	s.Require().Eventually(func() bool {
		return s.parser.callCount() == 2
	}, time.Second, 5*time.Millisecond)

	s.Equal(1, s.parser.peakConcurrency(), "only one sync may run at a time")
}

func (s *engineSuite) TestDuplicateTriggersCoalesce() {
	s.parser.delay = 40 * time.Millisecond
	src := s.insertLibrary("stub", "/lib/dup")
	s.startWorker()

	// First trigger starts running; the burst that follows must collapse into
	// at most one more queued run.
	s.engine.TriggerLibrary(src.ID)
	time.Sleep(10 * time.Millisecond)
	for range 5 {
		s.engine.TriggerLibrary(src.ID)
	}

	s.Require().Eventually(func() bool {
		return !s.engine.Status().Running && len(s.engine.Status().Queued) == 0
	}, time.Second, 5*time.Millisecond)

	s.LessOrEqual(s.parser.callCount(), 2)
}

func (s *engineSuite) TestSuccessfulSyncClearsErrorStatus() {
	src := s.insertLibrary("stub", "/lib/recover")

	// First run fails → status sticks at 'error'.
	s.parser.err = errors.New("nas offline")
	s.engine.syncLibrary(syncReq{id: src.ID})
	s.Equal("error", s.getLibrary(src.ID).Status)

	// The next successful run must recover the status and clear the error.
	s.parser.err = nil
	s.engine.syncLibrary(syncReq{id: src.ID})

	got := s.getLibrary(src.ID)
	s.Equal("active", got.Status)
	s.False(got.LastSyncError.Valid)
	s.True(got.LastSyncAt.Valid)
}

func (s *engineSuite) TestSkippedUnchangedSyncClearsErrorStatus() {
	s.parser.checkpoint = "cp-1"
	src := s.insertLibrary("stub", "/lib/skip-recover")

	// Successful run stores the checkpoint; a later transient failure marks error.
	s.engine.syncLibrary(syncReq{id: src.ID})
	s.parser.err = errors.New("transient busy")
	s.engine.syncLibrary(syncReq{id: src.ID, force: true})
	s.Equal("error", s.getLibrary(src.ID).Status)

	// The next scheduled run skips (unchanged artifact) but must still recover.
	s.parser.err = nil
	s.engine.syncLibrary(syncReq{id: src.ID})

	s.Equal("active", s.getLibrary(src.ID).Status)
}

func (s *engineSuite) TestTriggerLibraryForced() {
	s.parser.checkpoint = "cp-1"
	src := s.insertLibrary("stub", "/lib/forced")
	s.startWorker()

	// Run initially to record cp-1
	s.engine.TriggerLibrary(src.ID)
	s.Require().Eventually(func() bool {
		return s.parser.callCount() == 1
	}, time.Second, 5*time.Millisecond)

	// Triggering normally should skip (because checkpoint cp-1 is unchanged)
	s.engine.TriggerLibrary(src.ID)
	time.Sleep(50 * time.Millisecond)
	s.Equal(1, s.parser.callCount())

	// Triggering forced should run it regardless
	s.engine.TriggerLibraryForced(src.ID)
	s.Require().Eventually(func() bool {
		return s.parser.callCount() == 2
	}, time.Second, 5*time.Millisecond)
}

func (s *engineSuite) TestWorkerSurvivesSyncPanic() {
	s.parser.panicMsg = "kaboom"
	src := s.insertLibrary("stub", "/lib/panic")

	s.NotPanics(func() { s.engine.safeSync(syncReq{id: src.ID}) })

	got := s.getLibrary(src.ID)
	s.Equal("error", got.Status)
	s.Contains(got.LastSyncError.String, "panicked")
}

// M1: purging is idempotent — a retry after the rows are gone is a clean no-op,
// and a successful purge removes books, covers, and the library row together.
func (s *engineSuite) TestPurgeIsAtomicAndIdempotent() {
	ctx := context.Background()
	src := s.insertLibrary("stub", "/lib/purge")

	q := dbq.New(s.db)
	bookID, err := q.InsertBook(ctx, dbq.InsertBookParams{
		LibraryID: src.ID, LibraryKey: "k", Title: "T",
		Language: "en", ContentHash: "h", AddedAt: time.Now().Unix(),
	})
	s.Require().NoError(err)
	s.Require().NoError(s.store.Save(bookID, s.jpegFixture(1, 2, 3)))

	s.Require().NoError(s.engine.purgeLibrary(ctx, src.ID))

	_, err = q.GetLibrary(ctx, src.ID)
	s.Require().ErrorIs(err, sql.ErrNoRows, "library row gone")
	s.False(s.store.Has(bookID), "cover evicted")

	// Retry on the now-missing library must not error.
	s.NoError(s.engine.purgeLibrary(ctx, src.ID), "purge is idempotent")
}

func (s *engineSuite) TestCheckpointStoredFromPreSyncFingerprint() {
	s.parser.checkpoint = "cp-1"
	src := s.insertLibrary("stub", "/lib/toctou")

	// The artifact changes while the sync runs: the post-run fingerprint is
	// "cp-2", but the data that was read corresponds to "cp-1".
	s.parser.onSync = func(context.Context, dbq.Library, *sql.DB, ingest.CoverStore) {
		s.parser.checkpoint = "cp-2"
	}
	s.engine.syncLibrary(syncReq{id: src.ID})
	s.Equal("cp-1", s.getLibrary(src.ID).Checkpoint.String)

	// The next pass must see the mismatch and re-sync instead of skipping.
	s.parser.onSync = nil
	s.engine.syncLibrary(syncReq{id: src.ID})
	s.Equal(2, s.parser.callCount())
}

// TestCheckpointPersistsWhenRunContextCancelled guards against the race where
// the sync completes just as the server shuts down: the run context is already
// cancelled at the point recordLastSync/storeCheckpoint are called, so both
// writes must use a detached context that ignores cancellation.
func (s *engineSuite) TestCheckpointPersistsWhenRunContextCancelled() {
	const fp = "cp-persist"
	s.parser.checkpoint = fp
	src := s.insertLibrary("stub", "/lib/persist-on-cancel")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // simulate shutdown cancelling the run ctx at completion

	// Call runSync directly so we can inject the already-cancelled context.
	// The stubParser ignores ctx cancellation and returns success, exercising
	// exactly the window where the context is cancelled between Sync returning
	// and the persist calls.
	s.engine.runSync(ctx, s.parser, src, syncReq{id: src.ID}, fp)

	// Both the last-sync timestamp and the checkpoint must be written despite
	// the cancelled context.
	got := s.getLibrary(src.ID)
	s.True(got.LastSyncAt.Valid, "last-sync stamp must persist despite cancelled context")
	s.Equal(fp, got.Checkpoint.String, "checkpoint must persist despite cancelled context")
}

// newDeepIndexEngine builds an engine over the suite's database and stub parser
// with the given deep indexer wired, plus any extra options.
func (s *engineSuite) newDeepIndexEngine(indexer DeepIndexer, opts ...Option) *Engine {
	opts = append(opts, WithDeepIndexer(indexer))
	eng, err := New(slog.New(slog.DiscardHandler), s.db, db.NewWriteGuard(),
		map[string]Parser{"stub": s.parser}, s.store, opts...)
	s.Require().NoError(err)

	return eng
}

// runWorker starts eng's worker goroutine and stops it on cleanup.
func (s *engineSuite) runWorker(eng *Engine) {
	go eng.worker()
	s.T().Cleanup(func() {
		close(eng.stop)
		<-eng.done
	})
}

func (s *engineSuite) TestDeepIndexRunsIndexerWithAllScope() {
	idx := &fakeDeepIndexer{}
	rec := &recordingPublisher{}
	eng := s.newDeepIndexEngine(idx, WithEvents(rec))
	src := s.insertLibrary("stub", "/lib/deep")

	eng.syncLibrary(syncReq{id: src.ID, kind: kindDeepIndex})

	s.Equal([]int64{src.ID}, idx.runs())
	s.Equal([]ingest.DeepIndexScope{ingest.DeepIndexAll()}, idx.scopes)
	s.Zero(s.parser.callCount(), "a deep index run must not run the parser")
	s.Equal([]int64{src.ID}, rec.libraryIDs(), "success emits a library event")
	s.Equal("active", s.getLibrary(src.ID).Status)
}

func (s *engineSuite) TestDeepIndexIgnoresCheckpoint() {
	s.parser.checkpoint = "cp-1"
	s.parser.result = ingest.Result{} // no new books: keep the post-sync fill out of the count
	idx := &fakeDeepIndexer{}
	eng := s.newDeepIndexEngine(idx)
	src := s.insertLibrary("stub", "/lib/deep-cp")

	// A sync stores the checkpoint; an unchanged artifact would gate the next sync.
	eng.syncLibrary(syncReq{id: src.ID})
	s.Require().Equal("cp-1", s.getLibrary(src.ID).Checkpoint.String)

	eng.syncLibrary(syncReq{id: src.ID, kind: kindDeepIndex})
	eng.syncLibrary(syncReq{id: src.ID, kind: kindDeepIndex})

	s.Equal([]int64{src.ID, src.ID}, idx.runs(), "deep index is never checkpoint-gated")
	s.Equal(1, s.parser.callCount())
}

func (s *engineSuite) TestDeepIndexDoesNotHoldWriteGuard() {
	guard := db.NewWriteGuard()
	var lockedDuringRun bool
	idx := &fakeDeepIndexer{onRun: func(context.Context, dbq.Library, ingest.Reporter) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		if guard.Lock(ctx) == nil {
			lockedDuringRun = true
			guard.Unlock()
		}
	}}
	eng, err := New(slog.New(slog.DiscardHandler), s.db, guard,
		map[string]Parser{"stub": s.parser}, s.store, WithDeepIndexer(idx))
	s.Require().NoError(err)
	src := s.insertLibrary("stub", "/lib/deep-guard")

	eng.syncLibrary(syncReq{id: src.ID, kind: kindDeepIndex})

	s.True(lockedDuringRun, "the engine must not hold the write guard across a deep index run")
}

func (s *engineSuite) TestDeepIndexErrorMarksLibraryFailed() {
	idx := &fakeDeepIndexer{err: errors.New("disk gone")}
	eng := s.newDeepIndexEngine(idx)
	src := s.insertLibrary("stub", "/lib/deep-err")

	eng.syncLibrary(syncReq{id: src.ID, kind: kindDeepIndex})

	got := s.getLibrary(src.ID)
	s.Equal("error", got.Status)
	s.Equal("deep index: disk gone", got.LastSyncError.String)
}

func (s *engineSuite) TestDeepIndexReportsProgress() {
	idx := &fakeDeepIndexer{onRun: func(_ context.Context, _ dbq.Library, rep ingest.Reporter) {
		rep.SetTotal(3)
		rep.Add(3)
	}}
	rec := &recordingPublisher{}
	eng := s.newDeepIndexEngine(idx, WithEvents(rec))
	src := s.insertLibrary("stub", "/lib/deep-progress")

	eng.syncLibrary(syncReq{id: src.ID, kind: kindDeepIndex})

	var last progressEvent
	for _, ev := range rec.events() {
		if pe, ok := ev.Data.(progressEvent); ok {
			last = pe
		}
	}
	s.Equal(progressEvent{Library: src.ID, Processed: 3, Total: 3}, last)
}

func (s *engineSuite) TestDeepIndexSkipsPendingPurgeAtRunTime() {
	idx := &fakeDeepIndexer{}
	eng := s.newDeepIndexEngine(idx)
	src := s.insertLibrary("stub", "/lib/deep-purge")
	s.markPendingPurge(src.ID, sql.NullInt64{})

	eng.syncLibrary(syncReq{id: src.ID, kind: kindDeepIndex})

	s.Empty(idx.runs())
}

func (s *engineSuite) TestTriggerDeepIndexAllSkipsPendingPurge() {
	idx := &fakeDeepIndexer{}
	eng := s.newDeepIndexEngine(idx)
	a := s.insertLibrary("stub", "/lib/deep-all-a")
	b := s.insertLibrary("stub", "/lib/deep-all-b")
	s.markPendingPurge(b.ID, sql.NullInt64{})

	eng.TriggerDeepIndexAll()

	s.Equal([]syncReq{{id: a.ID, kind: kindDeepIndex}}, eng.queue)
}

func (s *engineSuite) TestDeepIndexTriggersAreNoOpsWithoutIndexer() {
	src := s.insertLibrary("stub", "/lib/deep-none")

	s.engine.TriggerDeepIndex(src.ID)
	s.engine.TriggerDeepIndexAll()

	s.Empty(s.engine.Status().Queued)
}

func (s *engineSuite) TestDeepIndexAndSyncSameLibraryRunSequentially() {
	var active, peak int
	var mu stdsync.Mutex
	enter := func() {
		mu.Lock()
		active++
		peak = max(peak, active)
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		active--
		mu.Unlock()
	}
	s.parser.onSync = func(context.Context, dbq.Library, *sql.DB, ingest.CoverStore) { enter() }
	s.parser.result = ingest.Result{} // no new books: only the queued Deep Index runs the indexer
	idx := &fakeDeepIndexer{onRun: func(context.Context, dbq.Library, ingest.Reporter) { enter() }}
	eng := s.newDeepIndexEngine(idx)
	src := s.insertLibrary("stub", "/lib/deep-seq")

	// Queue both before the worker starts so they are pending side by side.
	eng.TriggerLibrary(src.ID)
	eng.TriggerDeepIndex(src.ID)
	s.Equal([]int64{src.ID, src.ID}, eng.Status().Queued, "both kinds are queued for one library")
	s.runWorker(eng)

	s.Require().Eventually(func() bool {
		return s.parser.callCount() == 1 && len(idx.runs()) == 1 && !eng.Status().Running
	}, time.Second, 5*time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	s.Equal(1, peak, "sync and deep index must not overlap")
}

func (s *engineSuite) TestStatusCarriesRunningKind() {
	release := make(chan struct{})
	idx := &fakeDeepIndexer{onRun: func(context.Context, dbq.Library, ingest.Reporter) { <-release }}
	rec := &recordingPublisher{}
	eng := s.newDeepIndexEngine(idx, WithEvents(rec))
	src := s.insertLibrary("stub", "/lib/deep-kind")
	s.runWorker(eng)

	eng.TriggerDeepIndex(src.ID)
	s.Require().Eventually(func() bool {
		return eng.Status().Running
	}, time.Second, 5*time.Millisecond)
	s.Equal("deep_index", eng.Status().Kind)
	close(release)

	s.Require().Eventually(func() bool {
		return !eng.Status().Running
	}, time.Second, 5*time.Millisecond)
	s.Empty(eng.Status().Kind)

	eng.TriggerLibrary(src.ID)
	s.Require().Eventually(func() bool {
		return s.parser.callCount() == 1 && !eng.Status().Running
	}, time.Second, 5*time.Millisecond)

	var kinds []string
	for _, ev := range rec.events() {
		if st, ok := ev.Data.(Status); ok && st.Running {
			kinds = append(kinds, st.Kind)
		}
	}
	s.Contains(kinds, "deep_index")
	s.Contains(kinds, "sync")
}

func (s *engineSuite) TestSyncFillsOnlyNewlyImportedBooks() {
	s.parser.result = ingest.Result{Added: 2, ImportedAt: 1_700_000_000}
	idx := &fakeDeepIndexer{}
	eng := s.newDeepIndexEngine(idx)
	src := s.insertLibrary("stub", "/lib/fill")

	eng.syncLibrary(syncReq{id: src.ID})

	s.Equal(1, s.parser.callCount())
	s.Equal([]int64{src.ID}, idx.runs(), "the fill runs in the sync's queue turn")
	s.Equal([]ingest.DeepIndexScope{ingest.DeepIndexImportedAt(1_700_000_000)}, idx.scopes,
		"the fill visits only this run's books, never the backlog")
	s.NotContains(idx.scopes, ingest.DeepIndexAll())
}

func (s *engineSuite) TestSyncWithoutAdditionsRunsNoFill() {
	s.parser.result = ingest.Result{Removed: 3, ImportedAt: 1_700_000_000}
	idx := &fakeDeepIndexer{}
	eng := s.newDeepIndexEngine(idx)
	src := s.insertLibrary("stub", "/lib/fill-none")

	eng.syncLibrary(syncReq{id: src.ID})

	s.Equal(1, s.parser.callCount())
	s.Empty(idx.runs())
}

func (s *engineSuite) TestSkippedSyncRunsNoFill() {
	s.parser.checkpoint = "cp-1"
	s.parser.result = ingest.Result{Added: 1, ImportedAt: 1_700_000_000}
	idx := &fakeDeepIndexer{}
	eng := s.newDeepIndexEngine(idx)
	src := s.insertLibrary("stub", "/lib/fill-skip")

	eng.syncLibrary(syncReq{id: src.ID}) // runs, adds, fills
	s.Require().Equal([]int64{src.ID}, idx.runs())

	eng.syncLibrary(syncReq{id: src.ID}) // unchanged checkpoint: skipped

	s.Equal(1, s.parser.callCount())
	s.Equal([]int64{src.ID}, idx.runs(), "a checkpoint-skipped sync runs no fill")
}

func (s *engineSuite) TestFailedSyncRunsNoFill() {
	s.parser.result = ingest.Result{Added: 1, ImportedAt: 1_700_000_000}
	s.parser.err = errors.New("boom")
	idx := &fakeDeepIndexer{}
	eng := s.newDeepIndexEngine(idx)
	src := s.insertLibrary("stub", "/lib/fill-failed")

	eng.syncLibrary(syncReq{id: src.ID})

	s.Empty(idx.runs())
}

func (s *engineSuite) TestSyncWithoutIndexerRunsNoFill() {
	s.parser.result = ingest.Result{Added: 1, ImportedAt: 1_700_000_000}
	src := s.insertLibrary("stub", "/lib/fill-noindexer")

	s.NotPanics(func() { s.engine.syncLibrary(syncReq{id: src.ID}) })

	s.True(s.getLibrary(src.ID).LastSyncAt.Valid)
}

func (s *engineSuite) TestPostSyncFillRunsAfterSyncGuardReleased() {
	s.parser.result = ingest.Result{Added: 1, ImportedAt: 1_700_000_000}
	guard := db.NewWriteGuard()
	var lockedDuringFill bool
	idx := &fakeDeepIndexer{onRun: func(context.Context, dbq.Library, ingest.Reporter) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		if guard.Lock(ctx) == nil {
			lockedDuringFill = true
			guard.Unlock()
		}
	}}
	eng, err := New(slog.New(slog.DiscardHandler), s.db, guard,
		map[string]Parser{"stub": s.parser}, s.store, WithDeepIndexer(idx))
	s.Require().NoError(err)
	src := s.insertLibrary("stub", "/lib/fill-guard")

	eng.syncLibrary(syncReq{id: src.ID})

	s.Require().Equal([]int64{src.ID}, idx.runs())
	s.True(lockedDuringFill, "the sync's write guard must be released before the fill runs")
}

func (s *engineSuite) TestPostSyncFillErrorKeepsSyncSuccess() {
	s.parser.checkpoint = "cp-1"
	s.parser.result = ingest.Result{Added: 1, ImportedAt: 1_700_000_000}
	idx := &fakeDeepIndexer{err: errors.New("disk gone")}
	eng := s.newDeepIndexEngine(idx)
	src := s.insertLibrary("stub", "/lib/fill-err")

	eng.syncLibrary(syncReq{id: src.ID})

	got := s.getLibrary(src.ID)
	s.Equal("active", got.Status)
	s.False(got.LastSyncError.Valid, "a failed fill must not mark the library errored")
	s.True(got.LastSyncAt.Valid)
	s.Equal("cp-1", got.Checkpoint.String, "the sync's checkpoint still stands")
}

func (s *engineSuite) TestPostSyncFillReportsProgressAsDeepIndex() {
	s.parser.result = ingest.Result{Added: 2, ImportedAt: 1_700_000_000}
	rec := &recordingPublisher{}
	var kindDuringFill string
	var eng *Engine
	idx := &fakeDeepIndexer{onRun: func(_ context.Context, _ dbq.Library, rep ingest.Reporter) {
		kindDuringFill = eng.Status().Kind
		rep.SetTotal(2)
		rep.Add(2)
	}}
	eng = s.newDeepIndexEngine(idx, WithEvents(rec))
	src := s.insertLibrary("stub", "/lib/fill-progress")
	s.runWorker(eng)

	eng.TriggerLibrary(src.ID)
	s.Require().Eventually(func() bool {
		return len(idx.runs()) == 1 && !eng.Status().Running
	}, time.Second, 5*time.Millisecond)

	s.Equal("deep_index", kindDuringFill, "the status names the fill while it runs")
	var last progressEvent
	var kinds []string
	for _, ev := range rec.events() {
		switch data := ev.Data.(type) {
		case progressEvent:
			last = data
		case Status:
			if data.Running {
				kinds = append(kinds, data.Kind)
			}
		}
	}
	s.Equal(progressEvent{Library: src.ID, Processed: 2, Total: 2}, last)
	s.Equal([]string{"sync", "deep_index"}, kinds, "status events announce the sync, then its fill")
}

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
		map[string]Parser{"stub": s.parser}, s.store, nil,
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

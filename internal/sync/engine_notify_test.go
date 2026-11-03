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
	"github.com/Toshik1978/folio/internal/ebook"
	"github.com/Toshik1978/folio/internal/ingest"
	"github.com/Toshik1978/folio/internal/notify"
)

// recordedCrash is one ReportCrash call seen by recordingNotifier.
type recordedCrash struct {
	place     string
	detail    string
	recovered any
	stack     []byte
}

// recordingNotifier is a Notifier that records every call.
type recordingNotifier struct {
	mu            stdsync.Mutex
	crashesDedupe bool // ReportCrash reports the crash as not queued (de-duplicated)
	crashes       []recordedCrash
	outcomes      []notify.RunOutcome
	removed       []int64
}

func (n *recordingNotifier) ReportCrash(place, detail string, recovered any, stack []byte) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.crashes = append(n.crashes, recordedCrash{place: place, detail: detail, recovered: recovered, stack: stack})

	return !n.crashesDedupe
}

func (n *recordingNotifier) RunFinished(o notify.RunOutcome) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.outcomes = append(n.outcomes, o)
}

func (n *recordingNotifier) LibraryRemoved(id int64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.removed = append(n.removed, id)
}

func (n *recordingNotifier) runs() []notify.RunOutcome {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]notify.RunOutcome(nil), n.outcomes...)
}

func (n *recordingNotifier) crashCalls() []recordedCrash {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]recordedCrash(nil), n.crashes...)
}

func (n *recordingNotifier) removedIDs() []int64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]int64(nil), n.removed...)
}

// newNotifyEngine builds an engine over the suite's database and stub parser
// that reports to a fresh recording notifier.
func (s *engineSuite) newNotifyEngine(opts ...Option) (*Engine, *recordingNotifier) {
	rec := &recordingNotifier{}
	opts = append(opts, WithNotifier(rec))
	eng, err := New(slog.New(slog.DiscardHandler), s.db, db.NewWriteGuard(),
		map[string]Parser{"stub": s.parser}, s.store, opts...)
	s.Require().NoError(err)

	return eng, rec
}

// insertNamedLibrary inserts a library with a name, so outcomes can be
// checked for it.
func (s *engineSuite) insertNamedLibrary(name, typ, path string) dbq.Library {
	q := dbq.New(s.db)
	id, err := q.InsertLibrary(context.Background(), dbq.InsertLibraryParams{
		Name: name, Type: typ, Path: path, SyncIntervalSeconds: 3600, CreatedAt: time.Now().Unix(),
	})
	s.Require().NoError(err)

	return s.getLibrary(id)
}

func (s *engineSuite) TestNotifySyncParserErrorReportsFailed() {
	s.parser.err = errors.New("boom")
	eng, rec := s.newNotifyEngine()
	src := s.insertNamedLibrary("Shelf", "stub", "/lib/n-err")

	eng.syncLibrary(syncReq{id: src.ID})

	s.Equal([]notify.RunOutcome{{
		LibraryID: src.ID, Library: "Shelf", Kind: notify.RunSync, Status: notify.RunFailed, Reason: "boom",
	}}, rec.runs())
	s.Empty(rec.crashCalls())
}

func (s *engineSuite) TestNotifySyncCancelReportsNothing() {
	s.parser.err = fmt.Errorf("read: %w", context.Canceled)
	eng, rec := s.newNotifyEngine()
	src := s.insertNamedLibrary("Shelf", "stub", "/lib/n-cancel")

	eng.syncLibrary(syncReq{id: src.ID})

	s.Empty(rec.runs())
}

func (s *engineSuite) TestNotifySyncSuccessReportsCompleted() {
	eng, rec := s.newNotifyEngine()
	src := s.insertNamedLibrary("Shelf", "stub", "/lib/n-ok")

	eng.syncLibrary(syncReq{id: src.ID})

	s.Equal([]notify.RunOutcome{{
		LibraryID: src.ID, Library: "Shelf", Kind: notify.RunSync, Status: notify.RunCompleted,
	}}, rec.runs())
}

func (s *engineSuite) TestNotifyCheckpointSkipReportsCompleted() {
	s.parser.checkpoint = "cp-1"
	eng, rec := s.newNotifyEngine()
	src := s.insertNamedLibrary("Shelf", "stub", "/lib/n-skip")

	eng.syncLibrary(syncReq{id: src.ID})
	eng.syncLibrary(syncReq{id: src.ID}) // unchanged checkpoint: skipped

	s.Require().Equal(1, s.parser.callCount())
	runs := rec.runs()
	s.Require().Len(runs, 2)
	s.Equal(notify.RunOutcome{
		LibraryID: src.ID, Library: "Shelf", Kind: notify.RunSync, Status: notify.RunCompleted,
	}, runs[1], "a skip is a completed run with zero counts")
}

func (s *engineSuite) TestNotifyErroredLibraryIsNotSkippedIntoRecovery() {
	s.parser.checkpoint = "cp-1"
	eng, rec := s.newNotifyEngine()
	src := s.insertNamedLibrary("Shelf", "stub", "/lib/n-skip-err")

	eng.syncLibrary(syncReq{id: src.ID}) // stores the checkpoint
	eng.markError(s.T().Context(), src.ID, errors.New("deep index: root unreachable"))
	eng.syncLibrary(syncReq{id: src.ID}) // unchanged checkpoint, but last run failed

	s.Equal(2, s.parser.callCount(), "an errored library is re-read, not skipped")
	s.Len(rec.runs(), 2)
}

func (s *engineSuite) TestNotifyUnknownLibraryTypeReportsFailed() {
	eng, rec := s.newNotifyEngine()
	src := s.insertNamedLibrary("Shelf", "mystery", "/lib/n-type")

	eng.syncLibrary(syncReq{id: src.ID})

	s.Equal([]notify.RunOutcome{{
		LibraryID: src.ID, Library: "Shelf", Kind: notify.RunSync, Status: notify.RunFailed,
		Reason: `no parser for library type "mystery"`,
	}}, rec.runs())
}

func (s *engineSuite) TestNotifyMissingOrPendingPurgeLibraryReportsNothing() {
	idx := &fakeDeepIndexer{}
	eng, rec := s.newNotifyEngine(WithDeepIndexer(idx))
	src := s.insertNamedLibrary("Shelf", "stub", "/lib/n-purge")
	s.markPendingPurge(src.ID, sql.NullInt64{})

	eng.syncLibrary(syncReq{id: src.ID})
	eng.syncLibrary(syncReq{id: src.ID, kind: kindDeepIndex})
	eng.syncLibrary(syncReq{id: src.ID + 1000})
	eng.syncLibrary(syncReq{id: src.ID + 1000, kind: kindDeepIndex})

	s.Empty(idx.runs())
	s.Empty(rec.runs())
	s.Empty(rec.crashCalls())
}

func (s *engineSuite) TestNotifySyncPanicReportsCrashThenFailed() {
	s.parser.panicMsg = "kaboom"
	eng, rec := s.newNotifyEngine()
	src := s.insertNamedLibrary("Shelf", "stub", "/lib/n-panic")

	s.NotPanics(func() { eng.safeSync(syncReq{id: src.ID}) })

	crashes := rec.crashCalls()
	s.Require().Len(crashes, 1)
	s.Equal("sync engine", crashes[0].place)
	s.Equal(`library "Shelf" sync`, crashes[0].detail)
	s.Equal("kaboom", crashes[0].recovered)
	s.NotEmpty(crashes[0].stack)
	s.Equal([]notify.RunOutcome{{
		LibraryID: src.ID, Library: "Shelf", Kind: notify.RunSync, Status: notify.RunFailed,
		Reason: "kaboom", CrashReported: true,
	}}, rec.runs())
	s.Equal("error", s.getLibrary(src.ID).Status, "the library is still marked errored")
}

// TestNotifySyncPanicDedupedCrashStillFails: a crash folded into an open
// de-dup window sends no message, so the run failure must not be suppressed.
func (s *engineSuite) TestNotifySyncPanicDedupedCrashStillFails() {
	s.parser.panicMsg = "kaboom"
	eng, rec := s.newNotifyEngine()
	rec.crashesDedupe = true
	src := s.insertNamedLibrary("Shelf", "stub", "/lib/n-panic-dedupe")

	s.NotPanics(func() { eng.safeSync(syncReq{id: src.ID}) })

	runs := rec.runs()
	s.Require().Len(runs, 1)
	s.Equal(notify.RunFailed, runs[0].Status)
	s.False(runs[0].CrashReported)
}

func (s *engineSuite) TestNotifyDeepIndexPanicNamesDeepIndex() {
	idx := &fakeDeepIndexer{onRun: func(context.Context, dbq.Library, ingest.Reporter) { panic("deep kaboom") }}
	eng, rec := s.newNotifyEngine(WithDeepIndexer(idx))
	src := s.insertNamedLibrary("Shelf", "stub", "/lib/n-deep-panic")

	s.NotPanics(func() { eng.safeSync(syncReq{id: src.ID, kind: kindDeepIndex}) })

	crashes := rec.crashCalls()
	s.Require().Len(crashes, 1)
	s.Equal(`library "Shelf" deep index`, crashes[0].detail)
	runs := rec.runs()
	s.Require().Len(runs, 1)
	s.Equal(notify.RunDeepIndex, runs[0].Kind)
	s.True(runs[0].CrashReported)
}

func (s *engineSuite) TestNotifyDeepIndexOutcomes() {
	idx := &fakeDeepIndexer{err: errors.New("disk gone")}
	eng, rec := s.newNotifyEngine(WithDeepIndexer(idx))
	src := s.insertNamedLibrary("Shelf", "stub", "/lib/n-deep")

	eng.syncLibrary(syncReq{id: src.ID, kind: kindDeepIndex})
	idx.err = nil
	eng.syncLibrary(syncReq{id: src.ID, kind: kindDeepIndex})

	s.Equal([]notify.RunOutcome{
		{
			LibraryID: src.ID, Library: "Shelf", Kind: notify.RunDeepIndex, Status: notify.RunFailed,
			Reason: "deep index: disk gone",
		},
		{LibraryID: src.ID, Library: "Shelf", Kind: notify.RunDeepIndex, Status: notify.RunCompleted},
	}, rec.runs())
}

func (s *engineSuite) TestNotifyDeepIndexCancelReportsNothing() {
	idx := &fakeDeepIndexer{err: context.Canceled}
	eng, rec := s.newNotifyEngine(WithDeepIndexer(idx))
	src := s.insertNamedLibrary("Shelf", "stub", "/lib/n-deep-cancel")

	eng.syncLibrary(syncReq{id: src.ID, kind: kindDeepIndex})

	s.Empty(rec.runs())
}

func (s *engineSuite) TestNotifyPurgeReportsLibraryRemoved() {
	eng, rec := s.newNotifyEngine()
	a := s.insertNamedLibrary("A", "stub", "/lib/n-purge-a")
	b := s.insertNamedLibrary("B", "stub", "/lib/n-purge-b")

	s.Require().NoError(eng.PurgeLibrary(context.Background(), a.ID))
	s.markPendingPurge(b.ID, sql.NullInt64{Int64: 1, Valid: true})
	eng.now = func() time.Time { return time.Unix(100, 0) }
	eng.checkPurge(context.Background())

	s.Equal([]int64{a.ID, b.ID}, rec.removedIDs())
}

// insertBooks adds n book rows to a library.
func (s *engineSuite) insertBooks(libraryID int64, n int) {
	q := dbq.New(s.db)
	for i := range n {
		_, err := q.InsertBook(context.Background(), dbq.InsertBookParams{
			LibraryID: libraryID, LibraryKey: fmt.Sprintf("k%d", i), Title: "T",
			Language: "en", ContentHash: fmt.Sprintf("h%d", i), AddedAt: time.Now().Unix(),
		})
		s.Require().NoError(err)
	}
}

func (s *engineSuite) TestNotifySyncReportsBooksBeforeAndRemoved() {
	s.parser.result = ingest.Result{Removed: 2, RemovedBooks: 2}
	eng, rec := s.newNotifyEngine()
	src := s.insertNamedLibrary("Shelf", "stub", "/lib/n-removed")
	s.insertBooks(src.ID, 3)
	// The run itself changes the catalog: BooksBefore must be counted before it.
	s.parser.onSync = func(ctx context.Context, _ dbq.Library, _ *sql.DB, _ ingest.CoverStore) {
		_, err := s.db.ExecContext(ctx, "DELETE FROM books WHERE library_id = ?", src.ID)
		s.Require().NoError(err)
	}

	eng.syncLibrary(syncReq{id: src.ID})

	s.Equal([]notify.RunOutcome{{
		LibraryID: src.ID, Library: "Shelf", Kind: notify.RunSync, Status: notify.RunCompleted,
		BooksBefore: 3, BooksRemoved: 2,
	}}, rec.runs())
}

func (s *engineSuite) TestNotifySyncMergesFillCountsIntoOneOutcome() {
	s.parser.result = ingest.Result{Added: 9, ImportedAt: 1_700_000_000}
	idx := &fakeDeepIndexer{result: ingest.DeepIndexResult{
		Broken: 7, Errors: 2,
		NewlyBroken: []ingest.BrokenBook{
			{BookID: 11, Reason: ebook.ReasonCorrupt},
			{BookID: 12, Reason: ebook.ReasonMissingEntry},
			{BookID: 13, Reason: ebook.ReasonTooLarge},
			{BookID: 14, Reason: ebook.ReasonCrashed},
			{BookID: 15, Reason: ebook.ReasonCorrupt},
		},
	}}
	eng, rec := s.newNotifyEngine(WithDeepIndexer(idx))
	src := s.insertNamedLibrary("Shelf", "stub", "/lib/n-fill")

	eng.syncLibrary(syncReq{id: src.ID})

	s.Equal([]notify.RunOutcome{{
		LibraryID: src.ID, Library: "Shelf", Kind: notify.RunSync, Status: notify.RunCompleted,
		PerBookErrors: 2, NewlyUnreadable: 7,
		UnreadableSample: []notify.UnreadableBook{
			{BookID: 11, Reason: "corrupt"},
			{BookID: 12, Reason: "missing_entry"},
			{BookID: 13, Reason: "too_large"},
			{BookID: 14, Reason: "crashed"},
			{BookID: 15, Reason: "corrupt"},
		},
	}}, rec.runs(), "one sync gives one summary")
}

func (s *engineSuite) TestNotifySyncFillFailureStaysCompleted() {
	s.parser.result = ingest.Result{Added: 1, ImportedAt: 1_700_000_000}
	idx := &fakeDeepIndexer{err: errors.New("disk gone")}
	eng, rec := s.newNotifyEngine(WithDeepIndexer(idx))
	src := s.insertNamedLibrary("Shelf", "stub", "/lib/n-fill-err")

	eng.syncLibrary(syncReq{id: src.ID})

	s.Equal([]notify.RunOutcome{{
		LibraryID: src.ID, Library: "Shelf", Kind: notify.RunSync, Status: notify.RunCompleted,
		FillFailed: "deep index: disk gone",
	}}, rec.runs())
}

// TestNotifySyncFillPanicKeepsSyncOutcome: the sync has committed before the
// fill runs, so a fill panic is a crash plus FillFailed, not a failed sync.
// TestRecoverCrashReportsAndContinues: engine work outside safeSync (the purge
// sweep gocron would swallow, a background purge) reports its panic as a crash.
func (s *engineSuite) TestRecoverCrashReportsAndContinues() {
	eng, rec := s.newNotifyEngine()
	var got any

	s.NotPanics(func() {
		defer eng.recoverCrash("purge sweep", func(r any) { got = r })
		panic("sweep kaboom")
	})

	s.Equal("sweep kaboom", got)
	crashes := rec.crashCalls()
	s.Require().Len(crashes, 1)
	s.Equal("sync engine", crashes[0].place)
	s.Equal("purge sweep", crashes[0].detail)
	s.Contains(string(crashes[0].stack), "TestRecoverCrashReportsAndContinues")
}

func (s *engineSuite) TestNotifySyncFillPanicKeepsSyncOutcome() {
	s.parser.result = ingest.Result{Added: 1, ImportedAt: 1_700_000_000}
	idx := &fakeDeepIndexer{onRun: func(context.Context, dbq.Library, ingest.Reporter) { panic("fill kaboom") }}
	eng, rec := s.newNotifyEngine(WithDeepIndexer(idx))
	src := s.insertNamedLibrary("Shelf", "stub", "/lib/n-fill-panic")

	s.NotPanics(func() { eng.safeSync(syncReq{id: src.ID}) })

	crashes := rec.crashCalls()
	s.Require().Len(crashes, 1)
	s.Equal(`library "Shelf" post-sync fill`, crashes[0].detail)
	runs := rec.runs()
	s.Require().Len(runs, 1)
	s.Equal(notify.RunCompleted, runs[0].Status)
	s.Equal("post-sync fill panicked: fill kaboom", runs[0].FillFailed)
	s.NotEqual("error", s.getLibrary(src.ID).Status, "the sync itself succeeded")
}

func (s *engineSuite) TestNotifySyncFillCancelLeavesFillFailedEmpty() {
	s.parser.result = ingest.Result{Added: 1, ImportedAt: 1_700_000_000}
	idx := &fakeDeepIndexer{err: context.Canceled}
	eng, rec := s.newNotifyEngine(WithDeepIndexer(idx))
	src := s.insertNamedLibrary("Shelf", "stub", "/lib/n-fill-cancel")

	eng.syncLibrary(syncReq{id: src.ID})

	s.Equal([]notify.RunOutcome{{
		LibraryID: src.ID, Library: "Shelf", Kind: notify.RunSync, Status: notify.RunCompleted,
	}}, rec.runs())
}

func (s *engineSuite) TestNotifyDeepIndexReportsCounts() {
	idx := &fakeDeepIndexer{result: ingest.DeepIndexResult{
		Errors: 3, Broken: 1,
		NewlyBroken: []ingest.BrokenBook{{BookID: 21, Reason: ebook.ReasonTooLarge}},
	}}
	eng, rec := s.newNotifyEngine(WithDeepIndexer(idx))
	src := s.insertNamedLibrary("Shelf", "stub", "/lib/n-deep-counts")

	eng.syncLibrary(syncReq{id: src.ID, kind: kindDeepIndex})

	s.Equal([]notify.RunOutcome{{
		LibraryID: src.ID, Library: "Shelf", Kind: notify.RunDeepIndex, Status: notify.RunCompleted,
		PerBookErrors: 3, NewlyUnreadable: 1,
		UnreadableSample: []notify.UnreadableBook{{BookID: 21, Reason: "too_large"}},
	}}, rec.runs())
}

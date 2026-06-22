package sync

import (
	"context"
	"database/sql"
	"os"
	"time"

	"github.com/Toshik1978/folio/internal/db/dbq"
)

type schedulerSuite struct {
	baseSuite
}

// insertBook adds one book row for a library and returns its ID.
func (s *schedulerSuite) insertBook(libraryID int64, title string) int64 {
	q := dbq.New(s.db)
	id, err := q.InsertBook(context.Background(), dbq.InsertBookParams{
		LibraryID: libraryID, LibraryKey: title, Title: title,
		Language: "en", ContentHash: title, AddedAt: time.Now().Unix(),
	})
	s.Require().NoError(err)
	_, err = q.InsertBookFile(context.Background(), dbq.InsertBookFileParams{
		BookID: id, FileFormat: "epub", FileSize: 1, SourcePath: title + ".epub",
	})
	s.Require().NoError(err)

	return id
}

func (s *schedulerSuite) TestCheckPurgeDeletesExpiredSource() {
	src := s.insertLibrary("stub", "/lib/purge")
	bookID := s.insertBook(src.ID, "doomed")
	s.Require().NoError(s.store.Save(bookID, s.jpegFixture(10, 20, 30)))

	past := s.engine.now().Add(-time.Hour).Unix()
	s.markPendingPurge(src.ID, sql.NullInt64{Int64: past, Valid: true})

	s.engine.checkPurge(context.Background())

	// Source gone.
	_, err := dbq.New(s.db).GetLibrary(context.Background(), src.ID)
	s.Require().ErrorIs(err, sql.ErrNoRows)

	// Books gone.
	count, err := dbq.New(s.db).CountBooksByLibrary(context.Background(), src.ID)
	s.Require().NoError(err)
	s.Zero(count)

	// Cover file gone.
	_, statErr := os.Stat(s.store.Path(bookID))
	s.True(os.IsNotExist(statErr))
}

func (s *schedulerSuite) TestPurgeSourceReclaimsImmediately() {
	src := s.insertLibrary("stub", "/lib/now")
	bookID := s.insertBook(src.ID, "doomed-now")
	s.Require().NoError(s.store.Save(bookID, s.jpegFixture(10, 20, 30)))

	// No deadline set — PurgeLibrary skips the grace period entirely.
	s.Require().NoError(s.engine.PurgeLibrary(context.Background(), src.ID))

	_, err := dbq.New(s.db).GetLibrary(context.Background(), src.ID)
	s.Require().ErrorIs(err, sql.ErrNoRows)

	count, err := dbq.New(s.db).CountBooksByLibrary(context.Background(), src.ID)
	s.Require().NoError(err)
	s.Zero(count)

	_, statErr := os.Stat(s.store.Path(bookID))
	s.True(os.IsNotExist(statErr))
}

// TestPurgeEmitsLibraryEvent asserts a completed purge publishes a library event
// so SSE-connected clients refetch and drop the deleted row instead of leaving it
// stuck on "Pending Purge". Regression test for the purge completing silently.
func (s *schedulerSuite) TestPurgeEmitsLibraryEvent() {
	rec := &recordingPublisher{}
	s.engine.events = rec

	src := s.insertLibrary("stub", "/lib/notify")

	s.Require().NoError(s.engine.PurgeLibrary(context.Background(), src.ID))

	s.Equal([]int64{src.ID}, rec.libraryIDs())
}

// TestCheckPurgeEmitsLibraryEvent asserts the deadline sweep also notifies clients
// when it reclaims an expired library.
func (s *schedulerSuite) TestCheckPurgeEmitsLibraryEvent() {
	rec := &recordingPublisher{}
	s.engine.events = rec

	src := s.insertLibrary("stub", "/lib/sweep-notify")
	past := s.engine.now().Add(-time.Hour).Unix()
	s.markPendingPurge(src.ID, sql.NullInt64{Int64: past, Valid: true})

	s.engine.checkPurge(context.Background())

	s.Equal([]int64{src.ID}, rec.libraryIDs())
}

// TestPurgeWaitsForWriteLock asserts a purge does not delete rows while the
// single-writer lock is held (standing in for an in-flight sync): it blocks until
// the lock is released, so teardown never races an indexing transaction.
func (s *schedulerSuite) TestPurgeWaitsForWriteLock() {
	src := s.insertLibrary("stub", "/lib/locked")
	_ = s.insertBook(src.ID, "doomed")

	s.engine.writeGuard.Lock() // stand in for an in-flight sync holding the guard

	done := make(chan error, 1)
	go func() { done <- s.engine.PurgeLibrary(context.Background(), src.ID) }()

	select {
	case <-done:
		s.engine.writeGuard.Unlock()
		s.Fail("purge proceeded while the single-writer guard was held")
		return
	case <-time.After(50 * time.Millisecond):
		// still blocked, as required
	}

	s.engine.writeGuard.Unlock() // release; the purge may now run
	s.Require().NoError(<-done)

	_, err := dbq.New(s.db).GetLibrary(context.Background(), src.ID)
	s.Require().ErrorIs(err, sql.ErrNoRows)
}

func (s *schedulerSuite) TestCheckPurgeKeepsSourcesBeforeDeadline() {
	src := s.insertLibrary("stub", "/lib/keep")
	future := s.engine.now().Add(time.Hour).Unix()
	s.markPendingPurge(src.ID, sql.NullInt64{Int64: future, Valid: true})

	s.engine.checkPurge(context.Background())

	_, err := dbq.New(s.db).GetLibrary(context.Background(), src.ID)
	s.Require().NoError(err, "source with a future deadline must survive")
}

func (s *schedulerSuite) TestRescheduleTracksSources() {
	s.engine.scheduler.start()
	s.T().Cleanup(func() { _ = s.engine.scheduler.shutdown() })

	active := s.insertLibrary("stub", "/lib/active")
	purging := s.insertLibrary("stub", "/lib/purging")
	s.markPendingPurge(purging.ID, sql.NullInt64{})

	s.Require().NoError(s.engine.Reschedule(context.Background()))

	s.Contains(s.engine.scheduler.jobs, active.ID, "active source gets a job")
	s.NotContains(s.engine.scheduler.jobs, purging.ID, "pending-purge source gets no job")

	// Removing the source should drop its job on the next reconcile.
	s.Require().NoError(dbq.New(s.db).DeleteLibrary(context.Background(), active.ID))
	s.Require().NoError(s.engine.Reschedule(context.Background()))
	s.NotContains(s.engine.scheduler.jobs, active.ID)
}

// TestSchedulerShutdownReturnsNilOnCleanShutdown guards against shutdown wrapping
// a nil gocron error: fmt.Errorf("...: %w", nil) yields a non-nil error, which
// would make every clean shutdown look like a failure to any caller that checks.
func (s *schedulerSuite) TestSchedulerShutdownReturnsNilOnCleanShutdown() {
	s.engine.scheduler.start()
	s.Require().NoError(s.engine.scheduler.shutdown(), "clean shutdown must report no error")
}

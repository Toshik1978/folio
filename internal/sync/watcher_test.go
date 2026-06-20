package sync

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"time"

	"github.com/Toshik1978/folio/internal/libtype"
)

type watcherSuite struct {
	baseSuite
}

// startEngineWatcher starts the worker plus the watcher and attaches watches via
// an initial Reschedule, mirroring what Engine.Start does.
func (s *watcherSuite) startEngineWatcher() {
	go s.engine.worker()
	s.Require().NoError(s.engine.initWatcher())
	s.Require().NoError(s.engine.Reschedule(context.Background()))
	s.T().Cleanup(func() { s.engine.Stop() })
}

func (s *watcherSuite) TestFolderChangeTriggersSync() {
	root := s.T().TempDir()
	src := s.insertLibrary(libtype.Folder, root)

	s.startEngineWatcher()

	// Writing a file should, after the debounce window, drive exactly one sync.
	s.Require().NoError(os.WriteFile(filepath.Join(root, "new.epub"), []byte("data"), 0o600))

	s.Require().Eventually(func() bool {
		return s.parser.callCount() >= 1
	}, 2*time.Second, 10*time.Millisecond)

	s.Equal(src.ID, s.parser.synced[0])
}

func (s *watcherSuite) TestNoFolderSourcesIsNoop() {
	s.insertLibrary("stub", "/lib/not-a-folder")

	s.startEngineWatcher()

	s.Zero(s.engine.watcher.watchedLibraryCount(), "a non-folder source is not watched")
}

// L8: a watch that fails partway must leave nothing registered, so a later
// reconcile re-adds cleanly rather than leaking watches. A missing root is the
// portable way to force the WalkDir error.
func (s *watcherSuite) TestWatchErrorLeavesNothingRegistered() {
	s.startEngineWatcher()

	missing := filepath.Join(s.T().TempDir(), "does-not-exist")
	err := s.engine.watcher.watch(99, missing)

	s.Require().Error(err)
	s.Zero(s.engine.watcher.watchedLibraryCount(), "a failed watch records no root")
}

// TestReschedulePicksUpRuntimeFolderLibrary is the M1 regression guard: a folder
// library added (and later removed) after startup must be watched/unwatched
// without a restart, driven by Reschedule — exactly what the library CRUD
// handlers call.
func (s *watcherSuite) TestReschedulePicksUpRuntimeFolderLibrary() {
	s.startEngineWatcher() // no libraries yet
	s.Zero(s.engine.watcher.watchedLibraryCount())

	// Add a folder library at runtime, then Reschedule as createLibrary does.
	root := s.T().TempDir()
	src := s.insertLibrary(libtype.Folder, root)
	s.Require().NoError(s.engine.Reschedule(context.Background()))
	s.Equal(1, s.engine.watcher.watchedLibraryCount())

	// The newly watched folder now drives a sync on change.
	s.Require().NoError(os.WriteFile(filepath.Join(root, "new.epub"), []byte("data"), 0o600))
	s.Require().Eventually(func() bool {
		return s.parser.callCount() >= 1
	}, 2*time.Second, 10*time.Millisecond)
	s.Equal(src.ID, s.parser.synced[0])

	// Marking it pending-purge (as DELETE does) unwatches it on the next reconcile.
	s.markPendingPurge(src.ID, sql.NullInt64{})
	s.Require().NoError(s.engine.Reschedule(context.Background()))
	s.Zero(s.engine.watcher.watchedLibraryCount())
}

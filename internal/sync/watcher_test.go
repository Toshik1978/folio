package sync

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/Toshik1978/folio/internal/libtype"
	"github.com/Toshik1978/folio/internal/logging"
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

// captureSink records the Error records that reach the error tap, i.e. those
// not marked as already reported.
type captureSink struct{ msgs []string }

func (c *captureSink) LogError(_ context.Context, r slog.Record) { c.msgs = append(c.msgs, r.Message) }

func (s *watcherSuite) reconcileOne(root string) []string {
	sink := &captureSink{}
	base := slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError}))
	log := logging.WithErrorSink(base, sink)
	w, err := newWatcher(log, time.Millisecond, func(int64) {})
	s.Require().NoError(err)
	s.T().Cleanup(func() { _ = w.fsw.Close() })

	w.reconcile(map[int64]string{1: root})

	return sink.msgs
}

func (s *watcherSuite) TestMissingRootWatchErrorIsMarkedReported() {
	missing := filepath.Join(s.T().TempDir(), "gone")

	s.Empty(s.reconcileOne(missing), "a missing root is reported by the run's failed message")
}

func (s *watcherSuite) TestOtherWatchErrorIsStillForwarded() {
	file := filepath.Join(s.T().TempDir(), "file")
	s.Require().NoError(os.WriteFile(file, []byte("x"), 0o600))

	// A path below a regular file fails with ENOTDIR, neither not-exist nor permission.
	s.Equal([]string{"watch library"}, s.reconcileOne(filepath.Join(file, "sub")))
}

// TestHandlePanicIsReported: a panic while handling one event is reported and
// dropped; it must not escape the event loop's goroutine and kill the process.
func (s *watcherSuite) TestHandlePanicIsReported() {
	dir := s.T().TempDir()
	sub := filepath.Join(dir, "new")
	s.Require().NoError(os.Mkdir(sub, 0o700))
	var crashes []any
	w := &watcher{
		log:     slog.New(slog.DiscardHandler),
		dirs:    map[string]int64{dir: 1},
		timers:  map[int64]*time.Timer{},
		crashed: func(r any, _ []byte) { crashes = append(crashes, r) },
		// fsw is nil, so watching the new subdirectory panics.
	}

	s.NotPanics(func() { w.handleSafely(fsnotify.Event{Name: sub, Op: fsnotify.Create}) })
	s.Len(crashes, 1)
}

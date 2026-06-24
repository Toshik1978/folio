package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/Toshik1978/folio/internal/libtype"
	"github.com/Toshik1978/folio/internal/sync"
)

type librariesSuite struct {
	baseSuite
}

func (s *librariesSuite) TestListLibrariesWithBookCount() {
	src := s.seedLibrary("folder", "/lib")
	s.seedBook(src, bookSeed{Title: "A"})
	s.seedBook(src, bookSeed{Title: "B"})

	w := s.do(http.MethodGet, "/libraries", nil)
	s.Require().Equal(http.StatusOK, w.Code)
	var views []libraryView
	s.decode(w, &views)
	s.Require().Len(views, 1)
	s.Equal(int64(2), views[0].BookCount)
	s.Equal("active", views[0].Status)
}

func (s *librariesSuite) TestCreateLibraryValidates() {
	bad := s.do(http.MethodPost, "/libraries", map[string]any{"name": "X", "type": "bogus", "path": "/x"})
	s.Equal(http.StatusBadRequest, bad.Code)

	missingName := s.do(http.MethodPost, "/libraries", map[string]any{"type": "folder", "path": "/x"})
	s.Equal(http.StatusBadRequest, missingName.Code)

	missingPath := s.do(http.MethodPost, "/libraries", map[string]any{"name": "X", "type": "folder"})
	s.Equal(http.StatusBadRequest, missingPath.Code)
}

func (s *librariesSuite) TestCreateLibraryTriggersSyncAndReschedule() {
	w := s.do(http.MethodPost, "/libraries", map[string]any{
		"name": "My Library", "type": "folder", "path": "/library/books", "sync_interval_seconds": 1800,
	})
	s.Require().Equal(http.StatusCreated, w.Code)

	var view libraryView
	s.decode(w, &view)
	s.Equal("My Library", view.Name)
	s.Equal("folder", view.Type)
	s.Equal(int64(1800), view.SyncIntervalSeconds)

	s.Equal(1, s.sync.rescheduled)
	s.Equal([]int64{view.ID}, s.sync.triggered)
}

func (s *librariesSuite) TestGetAndUpdateLibrary() {
	id := s.seedLibrary("folder", "/lib")

	w := s.do(http.MethodGet, "/libraries/"+itoa(id), nil)
	s.Require().Equal(http.StatusOK, w.Code)

	upd := s.do(http.MethodPut, "/libraries/"+itoa(id), map[string]any{
		"name": "Renamed", "path": "/new/path", "sync_interval_seconds": 7200,
	})
	s.Require().Equal(http.StatusOK, upd.Code)
	var view libraryView
	s.decode(upd, &view)
	s.Equal("Renamed", view.Name)
	s.Equal("/new/path", view.Path)
	s.Equal(int64(7200), view.SyncIntervalSeconds)
	s.GreaterOrEqual(s.sync.rescheduled, 1)

	s.Equal(http.StatusNotFound, s.do(http.MethodGet, "/libraries/9999", nil).Code)
}

func (s *librariesSuite) TestUpdateLibraryTriggersSync() {
	id := s.seedLibrary("calibre", "/wrong/path")

	upd := s.do(http.MethodPut, "/libraries/"+itoa(id), map[string]any{
		"name": "Calibre", "path": "/right/path", "sync_interval_seconds": 3600,
	})
	s.Require().Equal(http.StatusOK, upd.Code)

	// Editing a library (e.g. correcting a bad path that left it in 'error')
	// must enqueue a sync so the error clears and re-index starts promptly,
	// rather than waiting up to a full interval for the scheduled job.
	s.Equal([]int64{id}, s.sync.triggered)
}

// TestWriteReturns503WhileIndexing asserts a user-facing library write fails fast
// with 503 — rather than blocking past the HTTP WriteTimeout — when an indexing
// run (modeled here by holding the shared single-writer guard) owns the guard, and
// that the same write succeeds once the guard is released.
func (s *librariesSuite) TestWriteReturns503WhileIndexing() {
	id := s.seedLibrary("folder", "/lib")

	// Shrink the acquire budget so the test does not wait the production 2s.
	restore := writeAcquireBudget
	writeAcquireBudget = 20 * time.Millisecond
	defer func() { writeAcquireBudget = restore }()

	// Model an in-flight sync holding the guard for the whole write. Keep the path
	// unchanged so the request exercises the main update guard, not the checkpoint.
	s.Require().NoError(s.guard.Lock(context.Background()))
	body := map[string]any{"name": "Renamed", "path": "/lib", "sync_interval_seconds": 3600}

	start := time.Now()
	w := s.do(http.MethodPut, "/libraries/"+itoa(id), body)
	s.Equal(http.StatusServiceUnavailable, w.Code)
	s.Less(time.Since(start), time.Second) // failed fast; did not block on the guard

	// Once the sync releases the guard, the same write succeeds.
	s.guard.Unlock()
	s.Equal(http.StatusOK, s.do(http.MethodPut, "/libraries/"+itoa(id), body).Code)
}

func (s *librariesSuite) TestDeleteStartsPurgeAndReactivateCancels() {
	id := s.seedLibrary("folder", "/lib")

	del := s.do(http.MethodDelete, "/libraries/"+itoa(id), nil)
	s.Require().Equal(http.StatusOK, del.Code)

	got := s.do(http.MethodGet, "/libraries/"+itoa(id), nil)
	var pending libraryView
	s.decode(got, &pending)
	s.Equal("pending_purge", pending.Status)
	s.Require().NotNil(pending.PurgeAt)
	s.Positive(*pending.PurgeAt)

	react := s.do(http.MethodPost, "/libraries/"+itoa(id)+"/reactivate", nil)
	s.Require().Equal(http.StatusOK, react.Code)
	var active libraryView
	s.decode(react, &active)
	s.Equal("active", active.Status)
	s.Nil(active.PurgeAt)
}

// M7: a DELETE/reactivate against a missing id is 404, not a fabricated 200.
func (s *librariesSuite) TestDeleteMissingLibraryIs404() {
	s.Equal(http.StatusNotFound, s.do(http.MethodDelete, "/libraries/9999", nil).Code)
}

func (s *librariesSuite) TestReactivateMissingLibraryIs404() {
	s.Equal(http.StatusNotFound, s.do(http.MethodPost, "/libraries/9999/reactivate", nil).Code)
}

// M7: a duplicate path is a 409, not a generic 500.
func (s *librariesSuite) TestCreateDuplicatePathIs409() {
	s.seedLibrary("folder", "/dup")
	w := s.do(http.MethodPost, "/libraries", map[string]any{"name": "X", "type": "folder", "path": "/dup"})
	s.Equal(http.StatusConflict, w.Code)
}

func (s *librariesSuite) TestUpdateToDuplicatePathIs409() {
	s.seedLibrary("folder", "/a")
	id := s.seedLibrary("folder", "/b")
	w := s.do(http.MethodPut, "/libraries/"+itoa(id), map[string]any{
		"name": "B", "path": "/a", "sync_interval_seconds": 3600,
	})
	s.Equal(http.StatusConflict, w.Code)
}

// M7: a PUT must not silently cancel a pending purge or wipe error state.
func (s *librariesSuite) TestUpdateKeepsPendingPurge() {
	id := s.seedLibrary("folder", "/lib")
	s.Require().Equal(http.StatusOK, s.do(http.MethodDelete, "/libraries/"+itoa(id), nil).Code)

	w := s.do(http.MethodPut, "/libraries/"+itoa(id), map[string]any{
		"name": "Renamed", "path": "/lib", "sync_interval_seconds": 3600,
	})
	s.Require().Equal(http.StatusOK, w.Code)
	var view libraryView
	s.decode(w, &view)
	s.Equal("pending_purge", view.Status, "a PUT must not silently cancel a pending purge")
	s.Require().NotNil(view.PurgeAt)
}

func (s *librariesSuite) TestForcePurgeLibrary() {
	id := s.seedLibrary("folder", "/lib")

	// Purge is asynchronous: the handler enqueues the teardown and returns 202.
	w := s.do(http.MethodPost, "/libraries/"+itoa(id)+"/purge", nil)
	s.Require().Equal(http.StatusAccepted, w.Code)
	s.Equal([]int64{id}, s.sync.purged)

	s.Equal(http.StatusNotFound, s.do(http.MethodPost, "/libraries/9999/purge", nil).Code)
}

func (s *librariesSuite) TestForcePurgeStampsPendingPurge() {
	id := s.seedLibrary("folder", "/lib/fp") // starts active

	w := s.do(http.MethodPost, fmt.Sprintf("/libraries/%d/purge", id), nil)
	s.Require().Equal(http.StatusAccepted, w.Code)
	s.Equal([]int64{id}, s.sync.purged, "teardown is delegated to the engine")

	got, err := s.q.GetLibrary(context.Background(), id)
	s.Require().NoError(err)
	s.Equal("pending_purge", got.Status, "Purge Now stamps pending_purge so the sweep can retry")
	s.True(got.PurgeAt.Valid, "purge_at is set so the deadline sweep picks it up")
}

func (s *librariesSuite) TestSyncLibraryNow() {
	id := s.seedLibrary("folder", "/lib")

	w := s.do(http.MethodPost, "/libraries/"+itoa(id)+"/sync", nil)
	s.Require().Equal(http.StatusAccepted, w.Code)
	s.Equal([]int64{id}, s.sync.triggered) // "Sync Now" is now incremental (non-forced)
	s.Empty(s.sync.triggeredForced)

	s.Equal(http.StatusNotFound, s.do(http.MethodPost, "/libraries/9999/sync", nil).Code)
}

func (s *librariesSuite) TestReindexLibrary() {
	id := s.seedLibrary("folder", "/lib")

	w := s.do(http.MethodPost, "/libraries/"+itoa(id)+"/reindex", nil)
	s.Require().Equal(http.StatusAccepted, w.Code)
	s.Equal([]int64{id}, s.sync.triggeredForced) // Re-index forces a full re-read
	s.Empty(s.sync.triggered)

	s.Equal(http.StatusNotFound, s.do(http.MethodPost, "/libraries/9999/reindex", nil).Code)
	s.Equal(http.StatusBadRequest, s.do(http.MethodPost, "/libraries/abc/reindex", nil).Code)
}

func (s *librariesSuite) TestSyncingStatusOverlay() {
	id := s.seedLibrary("folder", "/lib")
	s.sync.status.Running = true
	s.sync.status.Current = id

	w := s.do(http.MethodGet, "/libraries/"+itoa(id), nil)
	var view libraryView
	s.decode(w, &view)
	s.Equal("syncing", view.Status)
}

func (s *librariesSuite) TestQueuedLibraryReportsQueuedStatus() {
	id := s.seedLibrary("folder", "/lib/q")
	s.sync.status = sync.Status{Queued: []int64{id}}

	w := s.do(http.MethodGet, fmt.Sprintf("/libraries/%d", id), nil)
	s.Require().Equal(http.StatusOK, w.Code)

	var v libraryView
	s.decode(w, &v)
	s.Equal("queued", v.Status)
}

func (s *librariesSuite) TestLibrariesNegativeAndEdgeCases() {
	// 1. Invalid ID formats (Bad Request)
	s.Equal(http.StatusBadRequest, s.do(http.MethodGet, "/libraries/abc", nil).Code)
	s.Equal(http.StatusBadRequest, s.do(http.MethodGet, "/libraries/0", nil).Code)
	s.Equal(http.StatusBadRequest, s.do(http.MethodGet, "/libraries/-5", nil).Code)
	s.Equal(http.StatusBadRequest, s.do(http.MethodDelete, "/libraries/abc", nil).Code)
	s.Equal(http.StatusBadRequest, s.do(http.MethodPost, "/libraries/abc/reactivate", nil).Code)
	s.Equal(http.StatusBadRequest, s.do(http.MethodPost, "/libraries/abc/purge", nil).Code)
	s.Equal(http.StatusBadRequest, s.do(http.MethodPost, "/libraries/abc/sync", nil).Code)

	// 2. Reactivating a non-existent library is a clean 404 (existence is checked
	// before the update, per M7).
	s.Equal(http.StatusNotFound, s.do(http.MethodPost, "/libraries/99999/reactivate", nil).Code)
}

func (s *librariesSuite) TestUpdateLibraryValidationAndEdgeCases() {
	id := s.seedLibrary("folder", "/lib")

	// Invalid JSON body on update
	w := s.do(http.MethodPut, "/libraries/"+itoa(id), "invalid-json")
	s.Equal(http.StatusBadRequest, w.Code)

	// Missing/empty name on update
	w = s.do(http.MethodPut, "/libraries/"+itoa(id), map[string]any{"path": "/new/path"})
	s.Equal(http.StatusBadRequest, w.Code)

	// Missing/empty path on update
	w = s.do(http.MethodPut, "/libraries/"+itoa(id), map[string]any{"name": "NameOnly"})
	s.Equal(http.StatusBadRequest, w.Code)

	// Non-existent library ID on update
	w = s.do(http.MethodPut, "/libraries/99999", map[string]any{"name": "Renamed", "path": "/new/path"})
	s.Equal(http.StatusNotFound, w.Code)

	// Invalid JSON body on creation
	w = s.do(http.MethodPost, "/libraries", "invalid-json")
	s.Equal(http.StatusBadRequest, w.Code)
}

func (s *librariesSuite) TestUpdateLibraryLoadFailureIs500() {
	id := s.seedLibrary("folder", "/lib")
	s.Require().NoError(s.db.Close()) // force a non-ErrNoRows load failure
	s.db = nil                        // prevent double-close in TearDownTest

	w := s.do(http.MethodPut, fmt.Sprintf("/libraries/%d", id), map[string]any{
		"name": "n", "path": "/lib", "sync_interval_seconds": 60,
	})
	s.Equal(http.StatusInternalServerError, w.Code)
}

func (s *librariesSuite) TestSyncNowLoadFailureIs500() {
	id := s.seedLibrary("folder", "/lib")
	s.Require().NoError(s.db.Close())
	s.db = nil // prevent double-close in TearDownTest

	w := s.do(http.MethodPost, fmt.Sprintf("/libraries/%d/sync", id), nil)
	s.Equal(http.StatusInternalServerError, w.Code)
}

// useLibraryRoot rebuilds the libraries handler confined to root and re-registers
// it, so a test can exercise the LIBRARY_ROOT constraint (off by default).
func (s *librariesSuite) useLibraryRoot(root string) {
	s.libraries = NewLibraries(slog.New(slog.DiscardHandler), s.db, s.guard, s.sync, root)
	s.rebuildRouter()
}

func (s *librariesSuite) TestCreateRejectsPathOutsideLibraryRoot() {
	s.useLibraryRoot("/library")

	out := s.do(http.MethodPost, "/libraries", map[string]any{"name": "X", "type": "folder", "path": "/etc"})
	s.Equal(http.StatusBadRequest, out.Code, "a path outside LIBRARY_ROOT must be refused")

	in := s.do(http.MethodPost, "/libraries", map[string]any{"name": "X", "type": "folder", "path": "/library/books"})
	s.Equal(http.StatusCreated, in.Code, "a path inside LIBRARY_ROOT is accepted")
}

func (s *librariesSuite) TestUpdateRejectsPathOutsideLibraryRoot() {
	id := s.seedLibrary("folder", "/library/books")
	s.useLibraryRoot("/library")

	bad := s.do(http.MethodPut, "/libraries/"+itoa(id), map[string]any{
		"name": "X", "path": "/etc/passwd", "sync_interval_seconds": 3600,
	})
	s.Equal(http.StatusBadRequest, bad.Code, "editing a path outside LIBRARY_ROOT must be refused")

	ok := s.do(http.MethodPut, "/libraries/"+itoa(id), map[string]any{
		"name": "X", "path": "/library/other", "sync_interval_seconds": 3600,
	})
	s.Equal(http.StatusOK, ok.Code, "editing to a path inside LIBRARY_ROOT is accepted")
}

// The constraint applies to every library type, not just folder libraries.
func (s *librariesSuite) TestLibraryRootConstrainsAllTypes() {
	s.useLibraryRoot("/library")
	for _, typ := range []string{libtype.Folder, libtype.Calibre, libtype.INPX} {
		w := s.do(http.MethodPost, "/libraries", map[string]any{"name": typ, "type": typ, "path": "/etc/" + typ})
		s.Equal(http.StatusBadRequest, w.Code, typ)
	}
}

func (s *librariesSuite) TestWithinLibraryRoot() {
	// An empty root means the constraint is disabled: any path is accepted.
	s.True(withinLibraryRoot("", "/anything"))
	// The root itself and any descendant are inside.
	s.True(withinLibraryRoot("/library", "/library"))
	s.True(withinLibraryRoot("/library", "/library/books"))
	// A sibling, an unrelated path, and a traversal out are all rejected.
	s.False(withinLibraryRoot("/library", "/etc"))
	s.False(withinLibraryRoot("/library", "/librarian")) // prefix-only, not a child
	s.False(withinLibraryRoot("/library", "/library/../etc"))
}

func (s *librariesSuite) TestUpdateLibrarySucceedsWhenRescheduleFails() {
	id := s.seedLibrary("folder", "/lib")
	s.sync.rescheduleErr = errors.New("scheduler down")

	w := s.do(http.MethodPut, fmt.Sprintf("/libraries/%d", id), map[string]any{
		"name": "n", "path": "/lib", "sync_interval_seconds": 60,
	})

	s.Equal(http.StatusOK, w.Code)
	s.Equal(1, s.sync.rescheduled)
}

package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Toshik1978/folio/internal/db"
	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/libtype"
	"github.com/Toshik1978/folio/internal/sync"
)

// purgeGracePeriod is how long a deleted library's data survives before the
// sync engine reclaims it.
const purgeGracePeriod = 7 * 24 * time.Hour

// statusKey is the JSON field carrying a library action's resulting state.
const statusKey = "status"

// Library statuses.
const (
	pendingPurgeStatus = "pending_purge"
	syncingStatus      = "syncing"
	queuedStatus       = "queued"
)

type libraryView struct {
	ID                  int64   `json:"id"`
	Name                string  `json:"name"`
	Type                string  `json:"type"`
	Path                string  `json:"path"`
	SyncIntervalSeconds int64   `json:"sync_interval_seconds"`
	Status              string  `json:"status"`
	PurgeAt             *int64  `json:"purge_at"`
	LastSyncAt          *int64  `json:"last_sync_at"`
	LastSyncError       *string `json:"last_sync_error"`
	BookCount           int64   `json:"book_count"`
}

type createLibraryRequest struct {
	Name                string `json:"name"`
	Type                string `json:"type"`
	Path                string `json:"path"`
	SyncIntervalSeconds int64  `json:"sync_interval_seconds"`
}

type updateLibraryRequest struct {
	Name                string `json:"name"`
	Path                string `json:"path"`
	SyncIntervalSeconds int64  `json:"sync_interval_seconds"`
}

// listLibraries handles GET /api/libraries.
func (h *LibrariesHandler) listLibraries(w http.ResponseWriter, r *http.Request) {
	rows, err := h.q.ListLibrariesWithBookCount(r.Context())
	if err != nil {
		h.log.Error("list libraries", slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to list libraries")
		return
	}
	// Snapshot the engine's live work once for the whole list rather than locking
	// the engine per row inside effectiveStatus.
	st := h.sync.Status()
	views := make([]libraryView, 0, len(rows))
	for i := range rows {
		views = append(views, h.libraryCountRowView(st, rows[i]))
	}
	h.writeJSON(w, http.StatusOK, views)
}

// getLibrary handles GET /api/libraries/{id}.
func (h *LibrariesHandler) getLibrary(w http.ResponseWriter, r *http.Request) {
	id, ok := intParam(r, "id")
	if !ok {
		h.writeError(w, http.StatusBadRequest, "invalid library id")
		return
	}
	src, err := h.q.GetLibrary(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		h.writeError(w, http.StatusNotFound, "library not found")
		return
	}
	if err != nil {
		h.log.Error("get library", slog.Int64("library", id), slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to load library")
		return
	}
	count, err := h.q.CountBooksByLibrary(r.Context(), id)
	if err != nil {
		h.log.Error("count library books", slog.Int64("library", id), slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to load library")
		return
	}
	h.writeJSON(w, http.StatusOK, h.libraryView(h.sync.Status(), src, count))
}

// createLibrary handles POST /api/libraries.
func (h *LibrariesHandler) createLibrary(w http.ResponseWriter, r *http.Request) {
	validLibraryTypes := map[string]bool{libtype.Calibre: true, libtype.INPX: true, libtype.Folder: true}

	var req createLibraryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		h.writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if !validLibraryTypes[req.Type] {
		h.writeError(w, http.StatusBadRequest, "invalid library type")
		return
	}
	if req.Path == "" {
		h.writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	if req.SyncIntervalSeconds <= 0 {
		req.SyncIntervalSeconds = 3600
	}

	id, err := h.q.InsertLibrary(r.Context(), dbq.InsertLibraryParams{
		Name:                req.Name,
		Type:                req.Type,
		Path:                req.Path,
		SyncIntervalSeconds: req.SyncIntervalSeconds,
		CreatedAt:           time.Now().Unix(),
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			h.writeError(w, http.StatusConflict, "a library with this path already exists")
			return
		}
		h.log.Error("insert library", slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to create library")

		return
	}

	h.reschedule(r)
	h.sync.TriggerLibrary(id)

	src, err := h.q.GetLibrary(r.Context(), id)
	if err != nil {
		h.log.Error("get created library", slog.Int64("library", id), slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to load library")
		return
	}
	h.writeJSON(w, http.StatusCreated, h.libraryView(h.sync.Status(), src, 0))
}

// updateLibrary handles PUT /api/libraries/{id}.
func (h *LibrariesHandler) updateLibrary(w http.ResponseWriter, r *http.Request) {
	id, ok := intParam(r, "id")
	if !ok {
		h.writeError(w, http.StatusBadRequest, "invalid library id")
		return
	}
	prev, err := h.q.GetLibrary(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		h.writeError(w, http.StatusNotFound, "library not found")
		return
	}
	if err != nil {
		h.log.Error("get library for update", slog.Int64("library", id), slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to load library")
		return
	}
	req, ok := h.decodeUpdateLibraryRequest(w, r)
	if !ok {
		return
	}

	if err := h.q.UpdateLibrary(r.Context(), dbq.UpdateLibraryParams{
		Name: req.Name, Path: req.Path, SyncIntervalSeconds: req.SyncIntervalSeconds, ID: id,
	}); err != nil {
		if db.IsUniqueViolation(err) {
			h.writeError(w, http.StatusConflict, "a library with this path already exists")
			return
		}
		h.log.Error("update library", slog.Int64("library", id), slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to update library")

		return
	}
	// A PUT edits name/path/interval only; it deliberately does NOT reset
	// status/purge_at/last_sync_error directly. Doing so would silently cancel a
	// pending purge or clear an error badge without re-checking the artifact.
	// Instead the edit triggers a sync below: an 'error' library recovers via
	// UpdateLibraryLastSync once that sync succeeds, or re-asserts the error if
	// the path is still bad.
	//
	// When the path changes the old checkpoint fingerprint is stale; clear it
	// so the triggered sync does not skip based on a mismatched artifact.
	if req.Path != prev.Path {
		if err := h.q.UpdateLibraryCheckpoint(r.Context(), dbq.UpdateLibraryCheckpointParams{
			Checkpoint: sql.NullString{}, ID: id,
		}); err != nil {
			h.log.Error("clear library checkpoint", slog.Int64("library", id), slog.Any("error", err))
		}
	}
	h.reschedule(r)
	// Enqueue a sync so an edit (e.g. correcting a bad path) clears the error and
	// re-indexes promptly instead of waiting for the next scheduled interval.
	// Checkpoint gating still skips the read+reconcile when nothing relevant changed.
	h.sync.TriggerLibrary(id)
	h.respondLibrary(w, r, id)
}

// decodeUpdateLibraryRequest decodes and validates the body of an updateLibrary
// request. It writes an error response and returns ok=false on failure.
func (h *LibrariesHandler) decodeUpdateLibraryRequest(
	w http.ResponseWriter,
	r *http.Request,
) (updateLibraryRequest, bool) {
	var req updateLibraryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid JSON body")
		return req, false
	}
	if strings.TrimSpace(req.Name) == "" {
		h.writeError(w, http.StatusBadRequest, "name is required")
		return req, false
	}
	if req.Path == "" {
		h.writeError(w, http.StatusBadRequest, "path is required")
		return req, false
	}
	if req.SyncIntervalSeconds <= 0 {
		req.SyncIntervalSeconds = 3600
	}

	return req, true
}

// requireLibrary parses the {id} path param and confirms the library exists,
// writing the matching 400/404/500 and returning ok=false otherwise. It is the
// shared guard for the id-only library actions (delete, reactivate, purge, sync).
func (h *LibrariesHandler) requireLibrary(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, ok := intParam(r, "id")
	if !ok {
		h.writeError(w, http.StatusBadRequest, "invalid library id")
		return 0, false
	}
	if _, err := h.q.GetLibrary(r.Context(), id); errors.Is(err, sql.ErrNoRows) {
		h.writeError(w, http.StatusNotFound, "library not found")
		return 0, false
	} else if err != nil {
		h.log.Error("get library", slog.Int64("library", id), slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to load library")

		return 0, false
	}

	return id, true
}

// deleteLibrary handles DELETE /api/libraries/{id}: it begins the purge countdown
// rather than deleting immediately.
func (h *LibrariesHandler) deleteLibrary(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireLibrary(w, r)
	if !ok {
		return
	}
	purgeAt := time.Now().Add(purgeGracePeriod).Unix()
	if err := h.q.UpdateLibraryStatus(r.Context(), dbq.UpdateLibraryStatusParams{
		Status:  pendingPurgeStatus,
		PurgeAt: sql.NullInt64{Int64: purgeAt, Valid: true},
		ID:      id,
	}); err != nil {
		h.log.Error("delete library", slog.Int64("library", id), slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to delete library")
		return
	}
	h.reschedule(r)
	h.writeJSON(w, http.StatusOK, map[string]any{statusKey: pendingPurgeStatus, "purge_at": purgeAt})
}

// reactivateLibrary handles POST /api/libraries/{id}/reactivate: it cancels a
// pending purge.
func (h *LibrariesHandler) reactivateLibrary(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireLibrary(w, r)
	if !ok {
		return
	}
	if err := h.q.UpdateLibraryStatus(r.Context(), dbq.UpdateLibraryStatusParams{
		Status: "active", PurgeAt: sql.NullInt64{}, ID: id,
	}); err != nil {
		h.log.Error("reactivate library", slog.Int64("library", id), slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to reactivate library")
		return
	}
	h.reschedule(r)
	h.respondLibrary(w, r, id)
}

// forcePurgeLibrary handles POST /api/libraries/{id}/purge: it reclaims the
// library's books, covers, and row immediately, skipping the 7-day grace period.
func (h *LibrariesHandler) forcePurgeLibrary(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireLibrary(w, r)
	if !ok {
		return
	}
	// Stamp pending_purge + purge_at=now so the teardown is consistent regardless
	// of the library's prior state, and so the minute-interval deadline sweep
	// retries promptly if the async purge fails. The async purge almost always
	// wins; the sweep is the universal fallback.
	if err := h.q.UpdateLibraryStatus(r.Context(), dbq.UpdateLibraryStatusParams{
		Status:  pendingPurgeStatus,
		PurgeAt: sql.NullInt64{Int64: time.Now().Unix(), Valid: true},
		ID:      id,
	}); err != nil {
		h.log.Error("stamp purge", slog.Int64("library", id), slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to purge library")
		return
	}
	// Purge runs asynchronously on a background context; answer 202 and let the
	// engine tear down (see Engine.RequestPurge).
	h.sync.RequestPurge(id)
	h.writeJSON(w, http.StatusAccepted, map[string]string{statusKey: "purging"})
}

// syncLibraryNow handles POST /api/libraries/{id}/sync — a lightweight,
// incremental sync that respects checkpoint gating (an unchanged source is
// skipped). The forced "re-read from scratch" variant is reindexLibrary.
func (h *LibrariesHandler) syncLibraryNow(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireLibrary(w, r)
	if !ok {
		return
	}
	h.sync.TriggerLibrary(id)
	h.writeJSON(w, http.StatusAccepted, map[string]string{statusKey: queuedStatus})
}

// reindexLibrary handles POST /api/libraries/{id}/reindex — a forced full
// re-read that bypasses checkpoint gating, even when the source fingerprint is
// unchanged. The cheap, checkpoint-respecting variant is syncLibraryNow.
func (h *LibrariesHandler) reindexLibrary(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireLibrary(w, r)
	if !ok {
		return
	}
	h.sync.TriggerLibraryForced(id)
	h.writeJSON(w, http.StatusAccepted, map[string]string{statusKey: queuedStatus})
}

// respondLibrary loads a library with its book count and writes it as JSON.
func (h *LibrariesHandler) respondLibrary(w http.ResponseWriter, r *http.Request, id int64) {
	src, err := h.q.GetLibrary(r.Context(), id)
	if err != nil {
		h.log.Error("get library", slog.Int64("library", id), slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to load library")
		return
	}
	count, err := h.q.CountBooksByLibrary(r.Context(), id)
	if err != nil {
		h.log.Error("count library books", slog.Int64("library", id), slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to load library")
		return
	}

	h.writeJSON(w, http.StatusOK, h.libraryView(h.sync.Status(), src, count))
}

// reschedule re-syncs the engine's interval jobs after a library change. A
// failure means the scheduled job set silently drifts from the DB until the
// next change or restart, so it is logged rather than ignored.
func (h *LibrariesHandler) reschedule(r *http.Request) {
	if err := h.sync.Reschedule(r.Context()); err != nil {
		h.log.Error("reschedule sync jobs", slog.Any("error", err))
	}
}

func (h *LibrariesHandler) libraryView(st sync.Status, s dbq.Library, bookCount int64) libraryView {
	return libraryView{
		ID:                  s.ID,
		Name:                s.Name,
		Type:                s.Type,
		Path:                s.Path,
		SyncIntervalSeconds: s.SyncIntervalSeconds,
		Status:              effectiveStatus(st, s.Status, s.ID),
		PurgeAt:             nullInt(s.PurgeAt),
		LastSyncAt:          nullInt(s.LastSyncAt),
		LastSyncError:       nullStr(s.LastSyncError),
		BookCount:           bookCount,
	}
}

func (h *LibrariesHandler) libraryCountRowView(st sync.Status, s dbq.ListLibrariesWithBookCountRow) libraryView {
	return libraryView{
		ID:                  s.ID,
		Name:                s.Name,
		Type:                s.Type,
		Path:                s.Path,
		SyncIntervalSeconds: s.SyncIntervalSeconds,
		Status:              effectiveStatus(st, s.Status, s.ID),
		PurgeAt:             nullInt(s.PurgeAt),
		LastSyncAt:          nullInt(s.LastSyncAt),
		LastSyncError:       nullStr(s.LastSyncError),
		BookCount:           s.BookCount,
	}
}

// effectiveStatus overlays a snapshot of the engine's live work onto the persisted
// status: the library currently being indexed reports "syncing", a library waiting
// its turn reports "queued", otherwise the persisted status is returned. The caller
// snapshots sync.Status once so a list render does not lock the engine per row.
func effectiveStatus(st sync.Status, dbStatus string, id int64) string {
	if st.Running && st.Current == id {
		return syncingStatus
	}
	if slices.Contains(st.Queued, id) {
		return queuedStatus
	}

	return dbStatus
}

func nullInt(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	return &n.Int64
}

func nullStr(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	return &s.String
}

package opds

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/Toshik1978/folio/internal/bookfile"
)

// downloadBook handles GET /opds/books/{id}/files/{fileID} (Basic Auth required).
func (h *Handler) downloadBook(w http.ResponseWriter, r *http.Request) {
	id, ok := bookID(r)
	if !ok {
		http.Error(w, "Invalid Book ID", http.StatusBadRequest)
		return
	}
	fileID, ok := parseID(chi.URLParam(r, "fileID"))
	if !ok {
		http.Error(w, "Invalid File ID", http.StatusBadRequest)
		return
	}
	file, err := h.q.GetBookFile(r.Context(), fileID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && file.BookID != id) {
		http.Error(w, "File Not Found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.log.Error("opds: failed to get book file", slog.Any("error", err))
		http.Error(w, "Failed to Get Book File", http.StatusInternalServerError)
		return
	}
	book, err := h.q.GetBook(r.Context(), id)
	if err != nil {
		h.log.Error("opds: failed to get book", slog.Any("error", err))
		http.Error(w, "Failed to Get Book", http.StatusInternalServerError)
		return
	}
	source, err := h.q.GetLibrary(r.Context(), book.LibraryID)
	if err != nil {
		h.log.Error("opds: failed to get book library", slog.Any("error", err))
		http.Error(w, "Failed to Get Book Library", http.StatusInternalServerError)
		return
	}
	if err := bookfile.Serve(w, r, source, file); err != nil {
		h.log.Error("opds: serve book file", slog.Int64("file", fileID), slog.Any("error", err))
	}
}

// serveCover handles GET /opds/books/{id}/cover (public — no Basic Auth). The
// {id} is validated to a positive integer, eliminating any path-traversal vector.
func (h *Handler) serveCover(w http.ResponseWriter, r *http.Request) {
	id, ok := bookID(r)
	if !ok {
		http.Error(w, "Invalid Book ID", http.StatusBadRequest)
		return
	}
	h.covers.ServeCover(w, r, id)
}

// serveThumbnail handles GET /opds/books/{id}/cover/thumbnail (public — no Basic
// Auth). Returns an aspect-preserving thumbnail of the cover image.
func (h *Handler) serveThumbnail(w http.ResponseWriter, r *http.Request) {
	id, ok := bookID(r)
	if !ok {
		http.Error(w, "Invalid Book ID", http.StatusBadRequest)
		return
	}
	h.covers.ServeThumbnail(w, r, id)
}

func bookID(r *http.Request) (int64, bool) {
	return parseID(chi.URLParam(r, "id"))
}

func parseID(s string) (int64, bool) {
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

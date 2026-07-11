package api

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ebook"
	"github.com/Toshik1978/folio/internal/metasearch"
)

// matchCandidate is one Fix Match search result presented to the user.
type matchCandidate struct {
	Source    string   `json:"source"`
	VolumeID  string   `json:"volume_id"`
	Title     string   `json:"title"`
	Authors   []string `json:"authors"`
	Year      int      `json:"year"`
	Thumbnail string   `json:"thumbnail"`
}

// searchMatch handles GET /api/books/{id}/match?q= — metadata candidates the
// user can pick from to correct a book's metadata.
func (h *BooksHandler) searchMatch(w http.ResponseWriter, r *http.Request) {
	if h.enricher == nil {
		h.writeError(w, http.StatusNotImplemented, "enrichment disabled")
		return
	}
	id, ok := intParam(r, "id")
	if !ok {
		h.writeError(w, http.StatusBadRequest, "invalid book id")
		return
	}
	_, err := h.q.GetBook(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		h.writeError(w, http.StatusNotFound, "book not found")
		return
	}
	if err != nil {
		h.log.Error("get book", slog.Int64("book", id), slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to load book")
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		h.writeError(w, http.StatusBadRequest, "missing q")
		return
	}
	vols, err := h.enricher.Search(r.Context(), q)
	if err != nil {
		h.log.Error("match search", slog.Any("error", err))
		h.writeError(w, http.StatusBadGateway, "search failed")
		return
	}
	h.writeJSON(w, http.StatusOK, matchCandidates(vols))
}

// matchCandidates maps neutral metasearch volumes to the slim candidate view.
func matchCandidates(vols []metasearch.Volume) []matchCandidate {
	out := make([]matchCandidate, 0, len(vols))
	for i := range vols {
		out = append(out, matchCandidate{
			Source:    vols[i].Source,
			VolumeID:  vols[i].ID,
			Title:     vols[i].Title,
			Authors:   vols[i].Authors,
			Year:      vols[i].Year,
			Thumbnail: vols[i].ThumbnailURL,
		})
	}

	return out
}

// applyMatch handles POST /api/books/{id}/match — overwrite a book's metadata
// from a user-chosen candidate and return the updated book.
func (h *BooksHandler) applyMatch(w http.ResponseWriter, r *http.Request) {
	if h.enricher == nil {
		h.writeError(w, http.StatusNotImplemented, "enrichment disabled")
		return
	}
	id, ok := intParam(r, "id")
	if !ok {
		h.writeError(w, http.StatusBadRequest, "invalid book id")
		return
	}
	// Serialize with the lazy write-on-read tiers: a Fix Match racing a
	// first-view enrichment of the same book would produce an order-dependent
	// outcome. The claim is per-book; the client simply retries on 409.
	if !h.claimLazy(id) {
		h.writeError(w, http.StatusConflict, "book is busy; retry shortly")
		return
	}
	defer h.releaseLazy(id)

	source, volumeID, ok := h.decodeMatch(w, r)
	if !ok {
		return
	}

	book, err := h.q.GetBook(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		h.writeError(w, http.StatusNotFound, "book not found")
		return
	}
	if err != nil {
		h.log.Error("get book", slog.Int64("book", id), slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to load book")
		return
	}

	meta, err := h.enricher.ApplyMatch(r.Context(), source, volumeID)
	if err != nil {
		h.log.Error("apply match", slog.Int64("book", id), slog.Any("error", err))
		h.writeError(w, http.StatusBadGateway, "apply failed")
		return
	}
	if !h.persistMatch(w, r, &book, meta) {
		return
	}

	view, err := h.toSingleBookView(r.Context(), book)
	if err != nil {
		h.log.Error("render book", slog.Int64("book", id), slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to render book")
		return
	}
	h.writeJSON(w, http.StatusOK, view)
}

// persistMatch saves the matched online cover (gap-fill only; never downgrades a
// real local cover) then overwrites book's metadata under the single-writer guard
// with a bounded wait, so it returns 503 promptly during a long indexing run
// rather than blocking past the WriteTimeout. It writes the error response and
// returns false on failure; book is mutated only on a committed change.
func (h *BooksHandler) persistMatch(
	w http.ResponseWriter, r *http.Request, book *dbq.Book, meta ebook.Metadata,
) bool {
	coverSaved := h.saveEnrichedCover(r.Context(), book.ID, meta.Cover)
	wctx, cancel := context.WithTimeout(r.Context(), writeAcquireBudget)
	defer cancel()
	if _, err := h.applyEnrichment(wctx, book, meta, true, coverSaved); err != nil {
		if h.handleGuardErr(w, err) {
			return false
		}
		h.log.Error("apply match", slog.Int64("book", book.ID), slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to save match")

		return false
	}

	return true
}

// decodeMatch reads the {"source":"…","volume_id":"…"} body of an applyMatch
// request. Both source and volume_id are required; the frontend always sends the
// candidate's source, and the coordinator rejects an empty one.
func (h *BooksHandler) decodeMatch(w http.ResponseWriter, r *http.Request) (source, id string, ok bool) {
	var body struct {
		Source   string `json:"source"`
		VolumeID string `json:"volume_id"`
	}
	if err := decodeJSON(w, r, &body); err != nil || strings.TrimSpace(body.VolumeID) == "" {
		h.writeError(w, http.StatusBadRequest, "missing volume_id")
		return "", "", false
	}

	return body.Source, body.VolumeID, true
}

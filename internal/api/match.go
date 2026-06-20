package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Toshik1978/folio/internal/ebook"
	"github.com/Toshik1978/folio/internal/googlebooks"
)

// matchCandidate is one Fix Match search result presented to the user.
type matchCandidate struct {
	VolumeID  string   `json:"volume_id"`
	Title     string   `json:"title"`
	Authors   []string `json:"authors"`
	Year      int      `json:"year"`
	Thumbnail string   `json:"thumbnail"`
}

// searchMatch handles GET /api/books/{id}/match?q= — Google Books candidates the
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
	if _, err := h.q.GetBook(r.Context(), id); errors.Is(err, sql.ErrNoRows) {
		h.writeError(w, http.StatusNotFound, "book not found")
		return
	} else if err != nil {
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

// matchCandidates maps Google Books volumes to the slim candidate view.
func matchCandidates(vols []googlebooks.Volume) []matchCandidate {
	out := make([]matchCandidate, 0, len(vols))
	for i := range vols {
		out = append(out, matchCandidate{
			VolumeID:  vols[i].ID,
			Title:     vols[i].VolumeInfo.Title,
			Authors:   vols[i].VolumeInfo.Authors,
			Year:      ebook.ParseYear(vols[i].VolumeInfo.PublishedDate),
			Thumbnail: httpsURL(vols[i].VolumeInfo.ImageLinks.Thumbnail),
		})
	}

	return out
}

// httpsURL upgrades a Google Books "http://" image link to https so the candidate
// thumbnail isn't blocked as mixed content on an HTTPS-served Folio.
func httpsURL(u string) string {
	if rest, ok := strings.CutPrefix(u, "http://"); ok {
		return "https://" + rest
	}

	return u
}

// applyMatch handles POST /api/books/{id}/match — overwrite a book's metadata
// from a user-chosen Google Books volume and return the updated book.
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

	volumeID, ok := h.decodeVolumeID(w, r)
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

	meta, err := h.enricher.ApplyMatch(r.Context(), volumeID)
	if err != nil {
		h.log.Error("apply match", slog.Int64("book", id), slog.Any("error", err))
		h.writeError(w, http.StatusBadGateway, "apply failed")
		return
	}
	// Save the online cover first (only fills a gap; never downgrades a real
	// local cover), then persist the overwrite. Manual choice overwrites.
	coverSaved := h.saveEnrichedCover(r.Context(), id, meta.Cover)
	if _, err = h.applyEnrichment(r.Context(), &book, meta, true, coverSaved); err != nil {
		h.log.Error("apply match", slog.Int64("book", id), slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to save match")
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

// decodeVolumeID reads the {"volume_id":"…"} body of an applyMatch request,
// writing a 400 and returning ok=false when it is missing or blank.
func (h *BooksHandler) decodeVolumeID(w http.ResponseWriter, r *http.Request) (string, bool) {
	var body struct {
		VolumeID string `json:"volume_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.VolumeID) == "" {
		h.writeError(w, http.StatusBadRequest, "missing volume_id")
		return "", false
	}

	return body.VolumeID, true
}

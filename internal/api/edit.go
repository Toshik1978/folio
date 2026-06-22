package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ebook"
	"github.com/Toshik1978/folio/internal/ingest"
)

// manualCoverPrio pins a user-supplied cover in books.cover_prio. It sits far
// above every filePriority (max 4, EPUB), so the importer's saveCoverIfBetter
// refuses to downgrade it on any later sync, and the on-disk file beats lazy
// extraction at serve time. A manual cover is therefore sticky everywhere.
const manualCoverPrio int64 = 1000

// maxCoverBytes caps an uploaded or fetched cover so a hostile or accidental
// huge body cannot exhaust memory on the low-spec target hosts.
const maxCoverBytes int64 = 10 << 20 // 10 MiB

// saveManualCover writes user-supplied cover bytes for bookID and pins
// cover_prio to the manual sentinel. Unlike enrichment's saveEnrichedCover it
// does NOT defer to an existing local cover: the user explicitly chose this
// image, so it overwrites a PDF page-1 render or any prior cover. covers.Store
// validates and transcodes to JPEG inside Save, returning an error for
// non-image bytes — the caller maps that to a 400.
func (h *BooksHandler) saveManualCover(ctx context.Context, bookID int64, data []byte) error {
	if err := h.coverSaver.Save(bookID, data); err != nil {
		return fmt.Errorf("save cover: %w", err)
	}

	params := dbq.UpdateBookCoverPrioParams{CoverPrio: manualCoverPrio, ID: bookID}
	if err := h.q.UpdateBookCoverPrio(ctx, params); err != nil {
		return fmt.Errorf("pin cover prio: %w", err)
	}

	return nil
}

// uploadCover handles PUT /api/books/{id}/cover — set a book's cover from raw
// image bytes in the request body (file upload, clipboard paste, or drag-drop;
// all deliver bytes). It serializes against the lazy write-on-read tiers like
// applyMatch (409 on contention) and returns the updated book view.
func (h *BooksHandler) uploadCover(w http.ResponseWriter, r *http.Request) {
	if h.coverSaver == nil {
		h.writeError(w, http.StatusNotImplemented, "covers disabled")
		return
	}
	id, ok := intParam(r, "id")
	if !ok {
		h.writeError(w, http.StatusBadRequest, "invalid book id")
		return
	}
	if !h.claimLazy(id) {
		h.writeError(w, http.StatusConflict, "book is busy; retry shortly")
		return
	}
	defer h.releaseLazy(id)

	book, ok := h.loadBook(r.Context(), w, id)
	if !ok {
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxCoverBytes+1))
	if err != nil || int64(len(data)) > maxCoverBytes {
		h.writeError(w, http.StatusBadRequest, "cover too large or unreadable")
		return
	}
	h.applyManualCover(r.Context(), w, book, data)
}

// applyManualCover saves data as book's cover and writes the refreshed view, or
// a 400 when the bytes are not a decodable image. Shared by uploadCover and
// setCoverFromURL.
func (h *BooksHandler) applyManualCover(ctx context.Context, w http.ResponseWriter, book dbq.Book, data []byte) {
	if len(data) == 0 {
		h.writeError(w, http.StatusBadRequest, "empty cover")
		return
	}
	if err := h.saveManualCover(ctx, book.ID, data); err != nil {
		// On these endpoints the body is user input the cover store just tried
		// to decode; the dominant failure is an invalid image, so report 400.
		h.log.Warn("save manual cover", slog.Int64("book", book.ID), slog.Any("error", err))
		h.writeError(w, http.StatusBadRequest, "invalid image")
		return
	}
	view, err := h.toSingleBookView(ctx, book)
	if err != nil {
		h.log.Error("render book", slog.Int64("book", book.ID), slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to render book")
		return
	}
	h.writeJSON(w, http.StatusOK, view)
}

// loadBook fetches a book by id, writing a 404/500 and returning ok=false when
// it cannot. Shared by the manual edit/cover handlers.
func (h *BooksHandler) loadBook(ctx context.Context, w http.ResponseWriter, id int64) (dbq.Book, bool) {
	book, err := h.q.GetBook(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		h.writeError(w, http.StatusNotFound, "book not found")
		return dbq.Book{}, false
	}
	if err != nil {
		h.log.Error("get book", slog.Int64("book", id), slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to load book")
		return dbq.Book{}, false
	}

	return book, true
}

// isBlockedIP reports whether ip is a private, loopback, link-local, or
// unspecified address — any of which must not be reachable via a user-supplied URL.
func isBlockedIP(ip net.IP) bool {
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified()
}

// isBlockedHost reports whether host resolves to any private, loopback,
// link-local, or unspecified address. It is the canonical SSRF guard used for
// both the initial URL and every redirect hop. ctx is used for the DNS lookup.
func isBlockedHost(ctx context.Context, host string) bool {
	// Strip port if present so LookupHost gets a bare hostname or IP.
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		// No port — host is already bare.
		h = host
	}

	addrs, err := net.DefaultResolver.LookupHost(ctx, h)
	if err != nil || len(addrs) == 0 {
		// Cannot resolve → treat as blocked to be safe.
		return true
	}

	for _, a := range addrs {
		ip := net.ParseIP(a)
		if ip == nil || isBlockedIP(ip) {
			return true
		}
	}

	return false
}

// parseCoverURL decodes the JSON body of a cover-URL request and validates the
// scheme and host; it returns the raw URL string and true, or writes a 400 and
// returns false. Only http/https are accepted and the host must not resolve to a
// private/loopback/link-local address to prevent SSRF.
func (h *BooksHandler) parseCoverURL(w http.ResponseWriter, r *http.Request) (string, bool) {
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.URL) == "" {
		h.writeError(w, http.StatusBadRequest, "missing url")
		return "", false
	}
	u, err := url.Parse(body.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		h.writeError(w, http.StatusBadRequest, "url must be http(s)")
		return "", false
	}
	if h.blockedHost(r.Context(), u.Host) {
		h.writeError(w, http.StatusBadRequest, "url host not allowed")
		return "", false
	}

	return body.URL, true
}

// setCoverFromURL handles POST /api/books/{id}/cover with body {"url":"…"} —
// the server downloads the image (http/https only, size-capped) and saves it as
// the book's cover. Used by the Phase 2 cover-search grid and by manual URL
// entry.
func (h *BooksHandler) setCoverFromURL(w http.ResponseWriter, r *http.Request) {
	if h.coverSaver == nil {
		h.writeError(w, http.StatusNotImplemented, "covers disabled")
		return
	}
	id, ok := intParam(r, "id")
	if !ok {
		h.writeError(w, http.StatusBadRequest, "invalid book id")
		return
	}
	rawURL, ok := h.parseCoverURL(w, r)
	if !ok {
		return
	}
	if !h.claimLazy(id) {
		h.writeError(w, http.StatusConflict, "book is busy; retry shortly")
		return
	}
	defer h.releaseLazy(id)

	book, ok := h.loadBook(r.Context(), w, id)
	if !ok {
		return
	}
	data, ok := h.fetchCover(r.Context(), w, rawURL)
	if !ok {
		return
	}
	h.applyManualCover(r.Context(), w, book, data)
}

// editRequest is the PUT /api/books/{id} body. Empty fields are left unchanged
// (overwrite semantics fill only non-empty values); empty author/genre lists do
// not clear existing links. Title is required.
type editRequest struct {
	Title        string   `json:"title"`
	Authors      []string `json:"authors"`
	Genres       []string `json:"genres"`
	Series       string   `json:"series"`
	SeriesNumber float64  `json:"series_number"`
	Year         int      `json:"year"`
	Publisher    string   `json:"publisher"`
	Language     string   `json:"language"`
	Annotation   string   `json:"annotation"`
	// Identifiers is the full desired set, used as an authoritative replacement
	// (add/change/delete in one save). A nil pointer means the field was omitted
	// and identifiers are left untouched; a non-nil (even empty) slice replaces
	// them — so an older client that never sends the field cannot wipe them.
	Identifiers *[]identifierInput `json:"identifiers"`
}

// identifierInput is one (type, value) pair from the edit form.
type identifierInput struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// updateBook handles PUT /api/books/{id} — a manual metadata edit. It feeds the
// user's fields into the shared applyEnrichment(overwrite=true) engine, which
// relinks authors/series/genres, updates FTS, rebumps content_hash, and marks
// the book manually_matched so a later sync never reverts it.
func (h *BooksHandler) updateBook(w http.ResponseWriter, r *http.Request) {
	id, ok := intParam(r, "id")
	if !ok {
		h.writeError(w, http.StatusBadRequest, "invalid book id")
		return
	}
	var req editRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		h.writeError(w, http.StatusBadRequest, "title is required")
		return
	}
	if !h.claimLazy(id) {
		h.writeError(w, http.StatusConflict, "book is busy; retry shortly")
		return
	}
	defer h.releaseLazy(id)

	book, ok := h.loadBook(r.Context(), w, id)
	if !ok {
		return
	}
	meta := ebook.Metadata{
		Title:        strings.TrimSpace(req.Title),
		Authors:      req.Authors,
		Genres:       req.Genres,
		Series:       req.Series,
		SeriesNumber: req.SeriesNumber,
		Year:         req.Year,
		Publisher:    req.Publisher,
		Language:     strings.TrimSpace(req.Language),
		Annotation:   req.Annotation,
	}
	if _, err := h.applyEnrichment(r.Context(), &book, meta, true, false); err != nil {
		h.log.Error("manual edit", slog.Int64("book", id), slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to save edit")
		return
	}
	// Identifiers are reconciled outside applyEnrichment: the manual edit is the
	// only authoritative-replacement path (it may delete), whereas the shared
	// engine — also used by Fix Match — only ever upserts.
	if req.Identifiers != nil {
		if err := h.reconcileBookIdentifiers(r.Context(), id, *req.Identifiers); err != nil {
			h.log.Error("manual edit identifiers", slog.Int64("book", id), slog.Any("error", err))
			h.writeError(w, http.StatusInternalServerError, "failed to save identifiers")
			return
		}
	}
	view, err := h.toSingleBookView(r.Context(), book)
	if err != nil {
		h.log.Error("render book", slog.Int64("book", id), slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to render book")
		return
	}
	h.writeJSON(w, http.StatusOK, view)
}

// reconcileBookIdentifiers replaces a book's entire identifier set with the
// user's submitted one. It cleans the input through the importer's rules (so a
// manually-typed ISBN lands canonical and useless schemes are dropped), then in a
// single transaction clears the existing rows and re-inserts the cleaned set —
// the only path allowed to delete identifiers. An empty input therefore clears
// them all, which is what a user who removed every row intends.
func (h *BooksHandler) reconcileBookIdentifiers(ctx context.Context, bookID int64, in []identifierInput) error {
	raw := make([]ebook.Identifier, 0, len(in))
	for _, id := range in {
		raw = append(raw, ebook.Identifier{Type: id.Type, Value: id.Value})
	}
	cleaned := ingest.CleanIdentifiers(raw)

	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin identifier tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit
	q := dbq.New(tx)

	if err := q.DeleteBookIdentifiers(ctx, bookID); err != nil {
		return fmt.Errorf("clear identifiers: %w", err)
	}
	for _, id := range cleaned {
		if err := q.InsertBookIdentifier(ctx, dbq.InsertBookIdentifierParams{
			BookID: bookID, Type: id.Type, Value: id.Value,
		}); err != nil {
			return fmt.Errorf("insert identifier %s: %w", id.Type, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit identifiers: %w", err)
	}

	return nil
}

// fetchCover downloads an image URL within the cover budget and size cap,
// writing a 502/400 and returning ok=false on failure.
func (h *BooksHandler) fetchCover(ctx context.Context, w http.ResponseWriter, rawURL string) ([]byte, bool) {
	ctx, cancel := context.WithTimeout(ctx, coverFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid url")
		return nil, false
	}
	resp, err := h.coverFetchClient.Do(req)
	if err != nil {
		h.writeError(w, http.StatusBadGateway, "failed to fetch cover")
		return nil, false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		h.writeError(w, http.StatusBadGateway, "cover host returned an error")
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxCoverBytes+1))
	if err != nil || int64(len(data)) > maxCoverBytes {
		h.writeError(w, http.StatusBadGateway, "cover too large or unreadable")
		return nil, false
	}

	return data, true
}

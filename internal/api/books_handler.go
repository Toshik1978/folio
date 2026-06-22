package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	stdsync "sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/microcosm-cc/bluemonday"

	"github.com/Toshik1978/folio/internal/db"
	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ebook"
	"github.com/Toshik1978/folio/internal/metasearch"
)

// coverFetchTimeout bounds the server-side fetch of a cover URL.
const coverFetchTimeout = 15 * time.Second

// CoverServer serves a book cover image (cache hit, lazy extraction, or
// placeholder fallback) and reports the cover-file component of the ?v= cache
// buster. *covers.Store satisfies it.
type CoverServer interface {
	ServeHTTP(w http.ResponseWriter, r *http.Request, bookID int64)
	Version(bookID int64) string
}

// MetadataExtractor lazily recovers metadata that wasn't captured at index time
// (notably INPX annotations and identifiers, which live inside the book file
// rather than the index). It is optional: a nil extractor simply means no
// backfill. The concrete implementation lives in the ingest package (which may
// parse ebooks).
type MetadataExtractor interface {
	// Backfill returns the metadata recovered from the book's source file, with
	// identifiers already cleaned. ok is false when nothing parseable was found
	// (missing book, unsupported/skipped format). The caller persists the fields
	// it needs.
	Backfill(ctx context.Context, bookID int64) (ebook.Metadata, bool, error)
}

// MetadataEnricher recovers metadata from online sources for books the local
// tiers can't fill — notably PDFs. It is optional: a nil enricher disables
// online enrichment. *metasearch.Coordinator satisfies it.
type MetadataEnricher interface {
	// Enrich looks the book up online and returns mapped metadata (with a cover).
	// ok is false when nothing matched.
	Enrich(ctx context.Context, bookID int64) (ebook.Metadata, bool, error)
	// Search returns candidates for a free-text Fix Match query.
	Search(ctx context.Context, query string) ([]metasearch.Volume, error)
	// ApplyMatch maps a specific candidate the user picked (with its cover),
	// routed by its source.
	ApplyMatch(ctx context.Context, source, id string) (ebook.Metadata, error)
}

// CoverSaver caches a freshly-acquired cover (e.g. from online enrichment) and
// reports whether the book already has a real local cover of its own.
// *covers.Store satisfies it.
type CoverSaver interface {
	Save(bookID int64, data []byte) error
	// HasLocalCover reports whether the book has a real (non-placeholder) cover
	// from its own files — cached or extractable. An online cover must never
	// replace one, so even a manual Fix Match leaves a good local cover intact.
	HasLocalCover(ctx context.Context, bookID int64) bool
}

// BooksHandler serves /books and the per-book match endpoints. It owns the lazy
// write-on-read state (backfill + online enrichment claims).
type BooksHandler struct {
	base

	db          *sql.DB
	q           *dbq.Queries
	writeGuard  *db.WriteGuard // process-wide single-writer guard, shared with the sync engine
	covers      CoverServer
	extractor   MetadataExtractor // optional; nil disables lazy backfill
	enricher    MetadataEnricher  // optional; nil disables online enrichment
	coverSaver  CoverSaver        // optional; caches online-fetched covers
	coverSearch CoverSearcher     // optional; nil disables online cover search
	// annotationPolicy sanitizes stored annotation HTML before it is served, so the
	// frontend can render it via v-html without an XSS risk. UGCPolicy permits
	// common formatting tags (p, em, strong, lists, links, …) and strips scripts,
	// event handlers, and other dangerous markup. Sanitizing here, at the serve
	// boundary, covers every library and any already-stored data.
	annotationPolicy *bluemonday.Policy

	// blockedHost is the SSRF guard used in production. Tests may replace it with a
	// stub that allows loopback httptest servers while still exercising fetch logic.
	// Production code always uses isBlockedHost; this indirection is the only seam.
	blockedHost func(ctx context.Context, host string) bool
	// coverFetchClient fetches cover URLs the user picked. A dedicated client keeps
	// the timeout off the shared default transport and rejects redirects to internal
	// addresses (SSRF guard). CheckRedirect calls isBlockedHost directly (not via
	// the blockedHost var) so the real guard is always active, even in tests that
	// override blockedHost to allow loopback httptest servers for the initial URL.
	coverFetchClient *http.Client

	lazyMu       stdsync.Mutex
	lazyInflight map[int64]bool // book ids whose lazy write-on-read tiers are running
}

// NewBooks builds the books handler. extractor, enricher, coverSaver, and coverSearch may be nil.
// writeGuard is the process-wide single-writer guard shared with the sync engine.
func NewBooks(
	log *slog.Logger,
	database *sql.DB,
	writeGuard *db.WriteGuard,
	covers CoverServer,
	extractor MetadataExtractor,
	enricher MetadataEnricher,
	coverSaver CoverSaver,
	coverSearch CoverSearcher,
) *BooksHandler {
	return &BooksHandler{
		base:             base{log: log},
		db:               database,
		q:                dbq.New(database),
		writeGuard:       writeGuard,
		covers:           covers,
		extractor:        extractor,
		enricher:         enricher,
		coverSaver:       coverSaver,
		coverSearch:      coverSearch,
		annotationPolicy: bluemonday.UGCPolicy(),
		blockedHost:      isBlockedHost,
		coverFetchClient: &http.Client{
			Timeout: coverFetchTimeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return errors.New("too many redirects")
				}

				if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
					return fmt.Errorf("redirect to non-http(s) scheme %q", req.URL.Scheme)
				}

				if isBlockedHost(req.Context(), req.URL.Host) {
					return errors.New("redirect to internal address blocked")
				}

				return nil
			},
		},
		lazyInflight: map[int64]bool{},
	}
}

func (h *BooksHandler) Register(r chi.Router) {
	r.Route("/books", func(r chi.Router) {
		r.Get("/", h.listBooks)
		r.Get("/{id}", h.getBook)
		r.Get("/{id}/files/{fileID}", h.downloadBook)
		r.Get("/{id}/cover", h.serveCover)
		r.Get("/{id}/cover/search", h.searchCovers)
		r.Get("/{id}/match", h.searchMatch)
		r.Post("/{id}/match", h.applyMatch)
		r.Put("/{id}", h.updateBook)
		r.Put("/{id}/cover", h.uploadCover)
		r.Post("/{id}/cover", h.setCoverFromURL)
	})
}

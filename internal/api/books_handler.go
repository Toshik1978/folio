package api

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	stdsync "sync"

	"github.com/go-chi/chi/v5"

	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ebook"
	"github.com/Toshik1978/folio/internal/googlebooks"
)

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

// MetadataEnricher recovers metadata from an online source (Google Books) for
// books the local tiers can't fill — notably PDFs. It is optional: a nil
// enricher disables online enrichment. The concrete implementation lives in the
// ingest package.
type MetadataEnricher interface {
	// Enrich looks the book up online and returns mapped metadata (with a cover).
	// ok is false when nothing matched.
	Enrich(ctx context.Context, bookID int64) (ebook.Metadata, bool, error)
	// Search returns Google Books candidates for a free-text Fix Match query.
	Search(ctx context.Context, query string) ([]googlebooks.Volume, error)
	// ApplyMatch maps a specific volume the user picked (with its cover).
	ApplyMatch(ctx context.Context, volumeID string) (ebook.Metadata, error)
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

	db         *sql.DB
	q          *dbq.Queries
	covers     CoverServer
	extractor  MetadataExtractor // optional; nil disables lazy backfill
	enricher   MetadataEnricher  // optional; nil disables online enrichment
	coverSaver CoverSaver        // optional; caches online-fetched covers

	lazyMu       stdsync.Mutex
	lazyInflight map[int64]bool // book ids whose lazy write-on-read tiers are running
}

// NewBooks builds the books handler. extractor, enricher, and coverSaver may be nil.
func NewBooks(
	log *slog.Logger,
	database *sql.DB,
	covers CoverServer,
	extractor MetadataExtractor,
	enricher MetadataEnricher,
	coverSaver CoverSaver,
) *BooksHandler {
	return &BooksHandler{
		base:         base{log: log},
		db:           database,
		q:            dbq.New(database),
		covers:       covers,
		extractor:    extractor,
		enricher:     enricher,
		coverSaver:   coverSaver,
		lazyInflight: map[int64]bool{},
	}
}

func (h *BooksHandler) Register(r chi.Router) { //nolint:dupl // chi route groups share structural shape, not logic
	r.Route("/books", func(r chi.Router) {
		r.Get("/", h.listBooks)
		r.Get("/{id}", h.getBook)
		r.Get("/{id}/files/{fileID}", h.downloadBook)
		r.Get("/{id}/cover", h.serveCover)
		r.Get("/{id}/match", h.searchMatch)
		r.Post("/{id}/match", h.applyMatch)
		r.Put("/{id}", h.updateBook)
		r.Put("/{id}/cover", h.uploadCover)
		r.Post("/{id}/cover", h.setCoverFromURL)
	})
}

package api

import (
	"database/sql"
	"log/slog"
	"sync"

	"github.com/go-chi/chi/v5"

	"github.com/Toshik1978/folio/internal/db/dbq"
)

// CatalogHandler serves the read-only browse endpoints (authors, series, tags,
// publishers, their letter indexes), stats, and facets. It needs only the database.
type CatalogHandler struct {
	base

	q *dbq.Queries

	cacheMutex  sync.Mutex
	cacheValid  bool
	cachedStats statsView

	// alphabet is the fixed superset of buckets the UI renders, in display order:
	// Cyrillic, then Latin, then '#'. The frontend mirrors this order so it can
	// pick the first available letter as the default.
	alphabet []string
	genres   []string

	// computeHook, when non-nil, is called each time computeStats executes.
	// It is nil in production and set only by tests to count invocations.
	computeHook func()
}

// NewCatalog builds the catalog handler over the folio database.
func NewCatalog(log *slog.Logger, database *sql.DB, genres []string) *CatalogHandler {
	return &CatalogHandler{
		base:     base{log: log},
		q:        dbq.New(database),
		alphabet: buildAlphabet(),
		genres:   genres,
	}
}

// StatsChanged marks the cached stats as stale so the next request recomputes them.
// It satisfies sync.StatsObserver.
func (h *CatalogHandler) StatsChanged() {
	h.cacheMutex.Lock()
	h.cacheValid = false
	h.cacheMutex.Unlock()
}

func (h *CatalogHandler) Register(r chi.Router) { //nolint:dupl // structurally similar but distinct routes
	r.Get("/authors", h.listAuthors)
	r.Get("/authors/letters", h.authorLetters)
	r.Get("/series", h.listSeries)
	r.Get("/series/letters", h.seriesLetters)
	r.Get("/tags", h.listTags)
	r.Get("/tags/letters", h.tagLetters)
	r.Get("/publishers", h.listPublishers)
	r.Get("/publishers/letters", h.publisherLetters)
	r.Get("/genres", h.listGenres)
	r.Get("/stats", h.stats)
	r.Get("/facets", h.facets)
}

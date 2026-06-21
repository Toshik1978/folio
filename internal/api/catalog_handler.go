package api

import (
	"database/sql"
	"log/slog"
	"net/http"
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

	genres []string
}

// NewCatalog builds the catalog handler over the folio database.
func NewCatalog(log *slog.Logger, database *sql.DB, genres []string) *CatalogHandler {
	return &CatalogHandler{base: base{log: log}, q: dbq.New(database), genres: genres}
}

// StatsChanged marks the cached stats as stale so the next request recomputes them.
// It satisfies sync.StatsObserver.
func (h *CatalogHandler) StatsChanged() {
	h.cacheMutex.Lock()
	h.cacheValid = false
	h.cacheMutex.Unlock()
}

func (h *CatalogHandler) Register(r chi.Router) {
	r.Get("/authors", h.listAuthors)
	r.Get("/authors/letters", h.authorLetters)
	r.Get("/series", h.listSeries)
	r.Get("/series/letters", h.seriesLetters)
	r.Get("/tags", h.listTags)
	r.Get("/tags/letters", h.tagLetters)
	r.Get("/publishers", h.listPublishers)
	r.Get("/publishers/letters", h.publisherLetters)
	r.Get("/stats", h.stats)
	r.Get("/facets", h.facets)
	r.Get("/genres", h.listGenres)
}

// listGenres returns the canonical genre taxonomy for the edit autocomplete.
func (h *CatalogHandler) listGenres(w http.ResponseWriter, _ *http.Request) {
	h.writeJSON(w, http.StatusOK, h.genres)
}

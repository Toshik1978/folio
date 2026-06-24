package api

import (
	"database/sql"
	"log/slog"

	"github.com/go-chi/chi/v5"

	"github.com/Toshik1978/folio/internal/db"
	"github.com/Toshik1978/folio/internal/db/dbq"
)

// LibrariesHandler serves /libraries CRUD plus the sync/reindex/purge actions.
type LibrariesHandler struct {
	base

	q          *dbq.Queries
	writeGuard *db.WriteGuard // process-wide single-writer guard, shared with the sync engine
	sync       SyncEngine
	// libraryRoot optionally confines library paths to a base directory (the
	// LIBRARY_ROOT env var). Empty disables the constraint; see withinLibraryRoot.
	libraryRoot string
}

// NewLibraries builds the libraries handler over the folio database and sync engine.
// writeGuard is the process-wide single-writer guard shared with the sync engine.
// libraryRoot, when non-empty, confines accepted library paths to that base dir.
func NewLibraries(
	log *slog.Logger,
	database *sql.DB,
	writeGuard *db.WriteGuard,
	syncEngine SyncEngine,
	libraryRoot string,
) *LibrariesHandler {
	return &LibrariesHandler{
		base:        base{log: log},
		q:           dbq.New(database),
		writeGuard:  writeGuard,
		sync:        syncEngine,
		libraryRoot: libraryRoot,
	}
}

func (h *LibrariesHandler) Register(r chi.Router) {
	r.Route("/libraries", func(r chi.Router) {
		r.Get("/", h.listLibraries)
		r.Post("/", h.createLibrary)
		r.Get("/{id}", h.getLibrary)
		r.Put("/{id}", h.updateLibrary)
		r.Delete("/{id}", h.deleteLibrary)
		r.Post("/{id}/reactivate", h.reactivateLibrary)
		r.Post("/{id}/purge", h.forcePurgeLibrary)
		r.Post("/{id}/sync", h.syncLibraryNow)
		r.Post("/{id}/reindex", h.reindexLibrary)
	})
}

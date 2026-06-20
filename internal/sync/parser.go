package sync

import (
	"context"
	"database/sql"

	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ingest"
)

// Parser imports books from one library type into the folio database. The engine
// dispatches to one Parser per library type; implementations live in the ingest
// package. Defined here, where it is consumed, so ingest depends on no interface
// to satisfy this contract.
type Parser interface {
	// Sync imports the given library into db, caching covers in covers and
	// reporting progress to r.
	Sync(
		ctx context.Context, library dbq.Library, db *sql.DB,
		covers ingest.CoverStore, r ingest.Reporter,
	) (ingest.Result, error)
}

// Checkpointer lets a library report a cheap fingerprint of its backing artifact
// so the engine can skip an unchanged library without reading it. Libraries without
// a single artifact (Folder) do not implement it. It is an optional capability the
// engine probes for via type assertion on a Parser.
type Checkpointer interface {
	Checkpoint(library dbq.Library) (string, error)
}

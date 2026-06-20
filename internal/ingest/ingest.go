// Package ingest acquires book metadata for the folio database across three
// sub-areas over one shared normalization core (identifier cleaning in
// identifier.go, genre normalization in genres.go, the ebook.Metadata mapping):
//
//   - Bulk sync (write path): a parser per library type (CalibreParser, INPXParser,
//     FolderParser) enumerates records that the reconciler diffs and the importer
//     merges/persists. The shared lifecycle lives in runReconcile (driver.go); each
//     parser keeps its own diffing strategy (folder size+mtime skip, Calibre/INPX
//     content-hash merge). The Parser interface they satisfy is defined by their
//     consumer, the sync engine.
//   - File extraction (Extractor): on-demand cover/metadata recovery from a book's
//     source files, used by the API and the sync engine's cover-warming pass.
//   - Online enrichment (Enricher): on-demand metadata recovery from Google Books.
//
// The sync engine decides when to call a parser's Sync for a given library.
package ingest

import (
	"fmt"
	"os"
)

// CoverStore is the subset of covers.Store the parsers and cover-warming need
// to cache, evict, and probe cover images. *covers.Store satisfies it.
type CoverStore interface {
	Save(bookID int64, data []byte) error
	Delete(bookID int64) error
	Has(bookID int64) bool
}

// Result summarises what a single Sync run changed.
type Result struct {
	Added   int
	Removed int
}

// Reporter receives indexing progress during a Sync. Implementations are supplied
// by the caller (the sync engine, which streams them to the UI); parsers call Add
// per record and SetTotal when an exact total is cheaply known.
type Reporter interface {
	// SetTotal declares the expected number of records (enables a determinate bar).
	// Leaving it unset (total 0) keeps the bar indeterminate.
	SetTotal(n int)
	// Add increments the processed count by n.
	Add(n int)
}

// fileCheckpoint fingerprints a single file by modification time and size.
func fileCheckpoint(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", path, err)
	}
	return fmt.Sprintf("%d:%d", info.ModTime().UnixNano(), info.Size()), nil
}

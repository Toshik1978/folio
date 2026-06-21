package ingest

import "database/sql"

// bookRecord is the library-agnostic representation of one book, assembled by a
// parser and handed to the importer for insertion.
type bookRecord struct {
	LibraryID  int64
	LibraryKey string
	// DeriveIdentity is true for sources without a native book id (Folder, INPX):
	// the importer may group a record onto an existing book that shares a strong
	// identifier before falling back to LibraryKey. Calibre leaves it false so its
	// authoritative calibre:<id> grouping is never overridden.
	DeriveIdentity bool
	Title          string
	Authors        []string
	Genres         []string
	Annotation     string
	Series         string
	SeriesNumber   sql.NullFloat64
	Language       string
	Publisher      string
	Year           int
	Pages          int
	Rating         sql.NullInt64 // 1..5 stars, invalid = unrated
	AddedAt        int64         // source add-timestamp (unix); 0 = unknown → use run time
	Identifiers    []identifier
	SourcePath     string // path relative to the source (or "{archive}.zip/{inner}")
	FileFormat     string
	FileSize       int64
	Mtime          int64 // file mod time (unix); the folder diff signal, 0 elsewhere
	Cover          []byte
}

// identifier is a typed external book identifier (isbn, amazon, ...).
type identifier struct {
	Type  string
	Value string
}

func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// nullInt treats 0 as "unknown" → NULL.
func nullInt(n int) sql.NullInt64 {
	if n == 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(n), Valid: true}
}

func nullFloat(f float64, ok bool) sql.NullFloat64 {
	if !ok {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: f, Valid: true}
}

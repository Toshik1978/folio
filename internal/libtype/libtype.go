// Package libtype defines the canonical library-type identifiers stored in the
// Library.Type column. They are referenced across layers that cannot share a
// home any other way: the API validates them, the composition root maps them to
// parsers, the sync engine dispatches on them, and ingest/bookfile branch on
// them. A dependency-free leaf package is the only spot all of those can import
// without a cycle.
package libtype

// Library types. Persisted in the Library.Type column; values must stay stable.
const (
	Calibre = "calibre"
	INPX    = "inpx"
	Folder  = "folder"
)

package api

import (
	"fmt"
	"os"
)

// lettersBoundsSuite locks the char() literals in the SQL query files to the
// Go rune constants in letters.go. If a constant is changed without updating
// the SQL (or vice versa) this test will fail.
//
// See also: internal/api/letters.go — the "keep bucketOf and those queries in
// sync" comment; internal/db/queries/{authors,series,genres,books}.sql.
type lettersBoundsSuite struct {
	utilSuite // inherits suite.Suite; no DB needed
}

// TestSQLBucketBoundsMatchGoConstants reads the four SQL query files and
// asserts that each one contains the four char() codepoint expressions derived
// from the Go constants cyrLo, cyrHi, latLo, latHi.  The SQL uses half-open
// [lo, hi) ranges so the upper bound is +1 of the constant.
func (s *lettersBoundsSuite) TestSQLBucketBoundsMatchGoConstants() {
	queryFiles := []string{
		"../db/queries/authors.sql",
		"../db/queries/series.sql",
		"../db/queries/genres.sql",
		"../db/queries/books.sql",
	}

	// Derive the expected char() expressions from the Go constants — not from
	// hardcoded numbers — so this test fails on drift in either direction.
	wantCyrLo := fmt.Sprintf("char(%d)", cyrLo)    // char(1040)
	wantCyrHi1 := fmt.Sprintf("char(%d)", cyrHi+1) // char(1072) — exclusive upper
	wantLatLo := fmt.Sprintf("char(%d)", latLo)    // char(65)
	wantLatHi1 := fmt.Sprintf("char(%d)", latHi+1) // char(91) — exclusive upper

	for _, f := range queryFiles {
		src, err := os.ReadFile(f)
		s.Require().NoError(err, "reading %s", f)
		content := string(src)

		s.Contains(content, wantCyrLo, "file %s: missing Cyrillic lower bound", f)
		s.Contains(content, wantCyrHi1, "file %s: missing Cyrillic upper bound (exclusive)", f)
		s.Contains(content, wantLatLo, "file %s: missing Latin lower bound", f)
		s.Contains(content, wantLatHi1, "file %s: missing Latin upper bound (exclusive)", f)
	}
}

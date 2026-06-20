package db

import (
	"database/sql"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Fold returns the canonical key stored in the authors/series/genres name_fold
// columns. The UNIQUE constraint on that column dedupes case variants of a name
// across records, and browse sort/seek run on it for case-insensitive ordering.
//
// It folds to UPPERCASE (not lowercase) so a folded name's first character lands
// in the uppercase alphabet ranges that api/letters.go and the *NonLetter
// queries use. strings.ToUpper is Unicode-aware, so Cyrillic case variants merge
// too — unlike SQLite's ASCII-only NOCASE collation. Any code writing these
// columns (ingest, tests) must use this function so the stored values agree.
//
// Beyond uppercasing it strips combining marks that follow a Latin base letter
// (É → E) and folds Ё→Е, so accented Latin and Ё names file under a real letter
// bucket instead of '#'. Non-Latin combining sequences are preserved (Cyrillic Й
// = И + breve stays Й — its breve is a distinct letter, not an accent). This is a
// browse-bucket / dedup key, NOT the FTS tokenizer: the FTS index uses
// remove_diacritics 1, which also strips Cyrillic combining marks, so the two are
// deliberately not identical. Changing this function invalidates every stored
// fold: a fresh re-import is required.
func Fold(s string) string {
	up := strings.ToUpper(strings.TrimSpace(s))
	up = norm.NFC.String(stripLatinMarks(norm.NFD.String(up)))

	return strings.ReplaceAll(up, "Ё", "Е")
}

// stripLatinMarks removes combining marks (Mn) that follow a Latin base rune.
// The input must be NFD; the caller recomposes with NFC so untouched sequences
// (e.g. Cyrillic Й = И + breve) survive intact.
func stripLatinMarks(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	latinBase := false
	for _, r := range s {
		if unicode.Is(unicode.Mn, r) {
			if latinBase {
				continue
			}
		} else {
			latinBase = unicode.Is(unicode.Latin, r)
		}
		b.WriteRune(r)
	}

	return b.String()
}

// FoldNull returns the publisher_fold value for a nullable publisher: the
// folded string, or NULL when the publisher is NULL/blank. Every books write
// must set publisher_fold through this so the stored fold always agrees with
// the publisher column.
func FoldNull(s sql.NullString) sql.NullString {
	if !s.Valid || strings.TrimSpace(s.String) == "" {
		return sql.NullString{}
	}

	return sql.NullString{String: Fold(s.String), Valid: true}
}

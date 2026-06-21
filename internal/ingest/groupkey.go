package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/samber/lo"
)

// normalizeAuthorKey canonicalizes one author name for grouping: lowercased,
// split on commas and whitespace, tokens sorted and rejoined. This makes the
// key insensitive to ordering, so "Liu, Cixin", "Cixin Liu" and "Liu Cixin" are
// identical. Display names are unaffected — only the grouping key uses this.
func normalizeAuthorKey(a string) string {
	tokens := strings.FieldsFunc(strings.ToLower(a), func(r rune) bool {
		return r == ',' || unicode.IsSpace(r)
	})
	slices.Sort(tokens)
	return strings.Join(tokens, " ")
}

// groupKey is the within-library identity for libraries without a native book id
// (INPX, Folder): normalized title + sorted authors + language. Formats of the
// same work share it; a different language is a different book.
func groupKey(title string, authors []string, language string) string {
	norm := make([]string, 0, len(authors))
	for _, a := range authors {
		if k := normalizeAuthorKey(a); k != "" {
			norm = append(norm, k)
		}
	}
	slices.Sort(norm)
	lang := lo.CoalesceOrEmpty(strings.ToLower(strings.TrimSpace(language)), "und")

	return strings.Join([]string{
		strings.ToLower(strings.TrimSpace(title)),
		strings.Join(norm, ","),
		lang,
	}, "\x1f")
}

// contentHash fingerprints a record's effective (post-normalization) metadata so
// a re-sync can tell whether a book actually changed. Genres and identifiers run
// through the same taxonomy/cleaning the importer persists, so junk-only
// differences (dropped tags, calibre UUIDs, ISBN punctuation) don't trigger a
// spurious refresh and the fingerprint matches what is stored. Files are
// intentionally excluded (they are diffed separately by source_path/size).
func contentHash(rec bookRecord) string {
	authors := deduplicate(rec.Authors)
	genres := deduplicate(normalizeGenres(rec.Genres))
	clean := cleanIdentifiers(rec.Identifiers)
	idents := make([]string, 0, len(clean))
	for typ, val := range clean {
		idents = append(idents, typ+"="+val)
	}
	slices.Sort(authors)
	slices.Sort(genres)
	slices.Sort(idents)

	var sn string
	if rec.SeriesNumber.Valid {
		sn = fmt.Sprintf("%g", rec.SeriesNumber.Float64)
	}
	var rating string
	if rec.Rating.Valid {
		rating = strconv.FormatInt(rec.Rating.Int64, 10)
	}
	parts := []string{
		rec.Title, strings.Join(authors, "|"), rec.Series, sn, rec.Language,
		rec.Annotation, rec.Publisher, strconv.Itoa(rec.Year), rating,
		strings.Join(genres, "|"), strings.Join(idents, "|"),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))

	return hex.EncodeToString(sum[:])
}

package ingest

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/Toshik1978/folio/internal/ebook"
)

func TestNormalizeLang(t *testing.T) {
	cases := map[string]string{
		"eng":        "en",
		"rus":        "ru",
		"ger":        "de", // 639-2/B variant
		"deu":        "de", // 639-2/T variant
		"ENG":        "en", // case-insensitive
		" eng ":      "en", //nolint:gocritic // trimmed value
		"en":         "en",
		"en-US":      "en", // region subtag stripped
		"EN-us":      "en", // mixed case + region
		"pt-BR":      "pt", // region subtag stripped
		"en-Latn-US": "en", // script + region stripped
		"en_US":      "en", // underscore separator tolerated → recovered
		// Anything we can't resolve to a real base language collapses to "" so the
		// storage layer coalesces it to the "und" sentinel instead of persisting a
		// junk string that would split facets.
		"":        "", // absent
		"und":     "", // explicit undefined → absent (storage re-adds "und")
		"xyz":     "", // well-formed but unknown
		"english": "", // not a code at all
	}
	for in, want := range cases {
		assert.Equalf(t, want, normalizeLang(in), "normalizeLang(%q)", in)
	}
}

// recordFromMeta must normalize the language so editions of the same work that
// declare "en" and "en-US" land in one group instead of splitting facets.
func TestRecordFromMetaNormalizesLanguage(t *testing.T) {
	mk := func(lang string) bookRecord {
		return recordFromMeta(1, "rel", "/x/Book.epub", 10, 20, "epub", ebook.Metadata{
			Title:    "Same Title",
			Authors:  []string{"Author One"},
			Language: lang,
		})
	}
	enUS := mk("en-US")
	en := mk("en")

	assert.Equal(t, "en", enUS.Language, "en-US normalized to en")
	assert.Equal(t, en.LibraryKey, enUS.LibraryKey, "en and en-US share a group key")
}

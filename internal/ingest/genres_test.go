package ingest

import (
	"slices"

	"github.com/stretchr/testify/suite"
)

type genresSuite struct {
	suite.Suite
}

func (s *genresSuite) TestNormalizeGenre() {
	// 1. Left-side keys map to right-side values
	val, ok := normalizeGenre("sf_history")
	s.True(ok)
	s.Equal("Alternative History", val)

	val, ok = normalizeGenre("adventure")
	s.True(ok)
	s.Equal("Action & Adventure", val)

	// 2. Right-side values are preserved in their correct casing
	val, ok = normalizeGenre("science fiction")
	s.True(ok)
	s.Equal("Science Fiction", val)

	val, ok = normalizeGenre("SCIENCE FICTION")
	s.True(ok)
	s.Equal("Science Fiction", val)

	// 3. Unmapped tags are discarded
	val, ok = normalizeGenre("some random tag")
	s.False(ok)
	s.Empty(val)

	val, ok = normalizeGenre("   ")
	s.False(ok)
	s.Empty(val)
}

func (s *genresSuite) TestNormalizeGenres() {
	raw := []string{"sf_history", "unknown_tag", "adventure", "science fiction"}
	expected := []string{"Alternative History", "Action & Adventure", "Science Fiction"}
	s.Equal(expected, normalizeGenres(raw))
}

func (s *genresSuite) TestNormalizeGenresBISACPaths() {
	// Google Books returns slash-delimited BISAC paths. The whole string isn't a
	// taxonomy term, but each recognized segment is mapped.
	s.Equal([]string{"Science Fiction"},
		normalizeGenres([]string{"Fiction / Science Fiction / Space Opera"}),
		"the recognized segment of a BISAC path is mapped")

	// Two distinct whitelist segments in one path both come through.
	s.Equal([]string{"Computer Science", "Programming"},
		normalizeGenres([]string{"Computers / Programming"}),
		"each recognized segment of a path contributes")

	// A path with no recognized segment yields nothing.
	s.Empty(normalizeGenres([]string{"Cooking / Regional & Ethnic"}),
		"a path with no taxonomy segment is discarded")

	// A whole-string match still wins (no splitting applied).
	s.Equal([]string{"Science Fiction"}, normalizeGenres([]string{"science fiction"}))

	// Bare terms and BISAC paths combine across the list.
	s.Equal([]string{"History", "Fantasy"},
		normalizeGenres([]string{"History", "Fiction / Fantasy / Epic"}),
		"bare terms and BISAC paths combine across the list")
}

func (s *genresSuite) TestCanonicalGenres() {
	got := CanonicalGenres()
	s.NotEmpty(got)
	s.True(slices.IsSorted(got), "CanonicalGenres must return a sorted slice")
	s.Contains(got, "Science Fiction")
	s.NotContains(got, "", "taxonomy must not contain an empty label")
}

func (s *genresSuite) TestDeduplicate() {
	// Test case-insensitive, Cyrillic case-folded, space-trimmed deduplication
	input := []string{
		"  Leo Tolstoy ",
		"leo tolstoy",
		"Лев Толстой",
		"лев толстой", // Cyrillic case folding
		"  ",
		"Another Author",
	}
	expected := []string{
		"Leo Tolstoy",
		"Лев Толстой",
		"Another Author",
	}
	s.Equal(expected, deduplicate(input))
}

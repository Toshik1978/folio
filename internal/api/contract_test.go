package api

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// contractSuite pins the manually-mirrored backend/frontend contract: both
// sides assert against the same JSON fixtures under web/src/test/fixtures, so
// a rename, addition, or removal on either side fails exactly one suite and
// names the drift the "keep in sync" comments can only document.
type contractSuite struct {
	baseSuite
}

// fixturesDir points at the frontend's test fixtures, the single source both
// sides assert against.
func fixturesDir() string {
	return filepath.Join("..", "..", "web", "src", "test", "fixtures")
}

// fullBookView is a bookView with every field populated, so the fixture
// exercises the complete serialized surface (names and shapes alike).
func fullBookView() bookView {
	series := "Foundation"
	idx := 1.5
	publisher := "Gnome Press"
	year := 1951
	pages := 255
	rating := 5
	lang := "en"
	ann := "<p>Classic.</p>"
	isbnURL := "https://isbnsearch.org/isbn/9780553293357"
	cover := "/api/books/7/cover?v=abc-0"

	return bookView{
		ID:          7,
		Title:       "Foundation",
		Authors:     []bookAuthorView{{ID: 1, Name: "Isaac Asimov"}},
		Series:      &series,
		SeriesIndex: &idx,
		Tags:        []string{"sf"},
		Publisher:   &publisher,
		Year:        &year,
		Pages:       &pages,
		Rating:      &rating,
		Language:    &lang,
		Annotation:  &ann,
		Formats:     []formatView{{Type: "epub", SizeBytes: 1024, DownloadURL: "/api/books/7/files/11"}},
		Identifiers: []identifierView{{Type: "isbn", Value: "9780553293357", URL: &isbnURL}},
		CoverURL:    &cover,
	}
}

// TestBookViewMatchesSharedFixture fails when bookView's serialized shape
// drifts from the fixture web/src/test/contract.spec.ts also asserts against.
func (s *contractSuite) TestBookViewMatchesSharedFixture() {
	fixture, err := os.ReadFile(filepath.Join(fixturesDir(), "book-contract.json"))
	s.Require().NoError(err)

	got, err := json.Marshal(fullBookView())
	s.Require().NoError(err)

	s.JSONEq(string(fixture), string(got))
}

// TestAlphabetMatchesSharedFixture pins letters.go's bucket order to the same
// fixture web/src/alphabet.ts is checked against.
func (s *contractSuite) TestAlphabetMatchesSharedFixture() {
	fixture, err := os.ReadFile(filepath.Join(fixturesDir(), "alphabet.json"))
	s.Require().NoError(err)

	var want []string
	s.Require().NoError(json.Unmarshal(fixture, &want))
	s.Equal(want, alphabet)
}

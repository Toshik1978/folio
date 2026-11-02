package mcpserver

import (
	"strings"
	"unicode/utf8"

	"github.com/Toshik1978/folio/internal/htmltext"
)

// excerptMaxRunes is the maximum length of a BookSummary excerpt, in characters.
const excerptMaxRunes = 300

// BookSummary is the compact book shape returned by search tools. Fields
// without omitempty are required in the generated JSON Schema.
type BookSummary struct {
	ID             int64    `json:"id"`
	Title          string   `json:"title"`
	Authors        []string `json:"authors"`
	Series         *string  `json:"series,omitempty"`
	SeriesNumber   *float64 `json:"series_number,omitempty"`
	Genres         []string `json:"genres"`
	Language       string   `json:"language"`
	Year           *int     `json:"year,omitempty"`
	Excerpt        string   `json:"excerpt,omitempty"`
	HasDescription bool     `json:"has_description"`
	Link           string   `json:"link"`
}

// Identifier is an external book identifier such as an ISBN.
type Identifier struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// LibraryRef names the library a book belongs to.
type LibraryRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// BookDetail is BookSummary minus the excerpt, plus the full description and
// the remaining metadata.
type BookDetail struct {
	ID             int64        `json:"id"`
	Title          string       `json:"title"`
	Authors        []string     `json:"authors"`
	Series         *string      `json:"series,omitempty"`
	SeriesNumber   *float64     `json:"series_number,omitempty"`
	Genres         []string     `json:"genres"`
	Language       string       `json:"language"`
	Year           *int         `json:"year,omitempty"`
	HasDescription bool         `json:"has_description"`
	Link           string       `json:"link"`
	Description    *string      `json:"description,omitempty"`
	Publisher      *string      `json:"publisher,omitempty"`
	Rating         *int         `json:"rating,omitempty"`
	Identifiers    []Identifier `json:"identifiers"`
	Formats        []string     `json:"formats"`
	Library        LibraryRef   `json:"library"`
}

// bookFields is the plain input shared by both mappings; tool code fills it
// from database rows. Annotation is the raw (possibly HTML) description.
type bookFields struct {
	ID           int64
	Title        string
	Authors      []string
	Series       string
	SeriesNumber *float64
	Genres       []string
	Language     string
	Year         *int
	Annotation   string
}

// detailExtra carries the fields only BookDetail has.
type detailExtra struct {
	Publisher   string
	Rating      *int
	Identifiers []Identifier
	Formats     []string
	LibraryID   int64
	LibraryName string
}

func newSummary(publicURL string, f bookFields) BookSummary {
	ex := excerpt(f.Annotation)

	return BookSummary{
		ID:             f.ID,
		Title:          f.Title,
		Authors:        nonNil(f.Authors),
		Series:         optString(f.Series),
		SeriesNumber:   f.SeriesNumber,
		Genres:         nonNil(f.Genres),
		Language:       f.Language,
		Year:           f.Year,
		Excerpt:        ex,
		HasDescription: ex != "",
		Link:           bookLink(publicURL, f.ID),
	}
}

func newDetail(publicURL string, f bookFields, x detailExtra) BookDetail {
	desc := htmltext.StripMarkup(f.Annotation)

	return BookDetail{
		ID:             f.ID,
		Title:          f.Title,
		Authors:        nonNil(f.Authors),
		Series:         optString(f.Series),
		SeriesNumber:   f.SeriesNumber,
		Genres:         nonNil(f.Genres),
		Language:       f.Language,
		Year:           f.Year,
		HasDescription: desc != "",
		Link:           bookLink(publicURL, f.ID),
		Description:    optString(desc),
		Publisher:      optString(x.Publisher),
		Rating:         x.Rating,
		Identifiers:    nonNil(x.Identifiers),
		Formats:        nonNil(x.Formats),
		Library:        LibraryRef{ID: x.LibraryID, Name: x.LibraryName},
	}
}

// excerpt strips markup from annotation and shortens it to at most
// excerptMaxRunes characters, cutting at a word boundary and appending "…"
// when anything was cut.
func excerpt(annotation string) string {
	text := htmltext.StripMarkup(annotation)
	if utf8.RuneCountInString(text) <= excerptMaxRunes {
		return text
	}

	cut := []rune(text)[:excerptMaxRunes]
	// Cut mid-word unless the next rune is already a space.
	if next := []rune(text)[excerptMaxRunes]; next != ' ' {
		if i := strings.LastIndex(string(cut), " "); i > 0 {
			return strings.TrimRight(string(cut)[:i], " ") + "…"
		}
	}

	return strings.TrimRight(string(cut), " ") + "…"
}

func optString(s string) *string {
	if s == "" {
		return nil
	}

	return &s
}

// nonNil keeps empty slices serializing as [] rather than null.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}

	return s
}

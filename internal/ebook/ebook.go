package ebook

import (
	"regexp"
	"strconv"
	"strings"
)

type Metadata struct {
	Title        string
	Authors      []string
	Genres       []string
	Annotation   string
	Series       string
	SeriesNumber float64 // series index, 0 if unknown
	Language     string
	Publisher    string
	Year         int // publication year, 0 if unknown
	Pages        int // page count, 0 if unknown
	Identifiers  []Identifier
	Cover        []byte
}

// Identifier is a typed external book identifier (e.g. {"isbn", "978-..."}).
type Identifier struct {
	Type  string
	Value string
}

// identifierISBN is the canonical type label for ISBN identifiers.
const identifierISBN = "isbn"

var yearPattern = regexp.MustCompile(`\d{4}`)

// minPlausibleYear is the floor below which a parsed "year" is treated as
// garbage rather than a real publication date. It rejects leading-zero
// sentinels like Calibre's "0101-01-01" (→ 101) and other malformed dates while
// staying well below any realistic book year.
const minPlausibleYear = 1000

// ParseYear extracts the first four-digit year from a date string such as
// "2003", "2003-06-01", or the PDF "D:20030601...". Returns 0 when none is found
// or when the value is implausible (below minPlausibleYear or above 9999).
func ParseYear(s string) int {
	m := yearPattern.FindString(s)
	if m == "" {
		return 0
	}
	y, err := strconv.Atoi(m)
	if err != nil || y < minPlausibleYear || y > 9999 {
		return 0
	}

	return y
}

// parseSeriesIndex parses a series position such as "2" or "1.5"; returns 0 when
// absent or unparseable.
func parseSeriesIndex(s string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return f
}

// Format labels are the normalized, lower-case format identifiers returned by
// Dispatcher.Format and stored in book_files.file_format. They are the shared
// vocabulary across packages (cover content-type, ingest priority, browse filters).
const (
	FormatEPUB = "epub"
	FormatFB2  = "fb2"
	FormatMOBI = "mobi"
	FormatAZW  = "azw"
	FormatAZW3 = "azw3"
	FormatPDF  = "pdf"
)

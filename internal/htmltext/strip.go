// Package htmltext converts the HTML/XML fragments that ebook formats store in
// annotations into the two plain-text forms the rest of the app needs:
//
//   - StripMarkup reduces a fragment to plain text for FTS indexing (consumed by
//     ingest at sync time and by api on lazy backfill — keeping both on one
//     implementation so a given annotation yields identical books_fts tokens
//     regardless of which path indexed it).
//   - NewDisplayDecoder returns an xml.Decoder for parsers that keep the decoded
//     text for display/storage (consumed by ebook's FB2 parser).
//
// Both share one base entity table; they differ only in how the single curly
// quotes are resolved (see ftsEntities / displayEntities).
package htmltext

import (
	"encoding/xml"
	"errors"
	"html"
	"io"
	"maps"
	"regexp"
	"strings"
)

// baseEntities maps the named HTML entities (beyond the five XML built-ins) that
// the standard-library XML decoder cannot resolve on its own. The single curly
// quotes (lsquo/rsquo) are intentionally absent here: each variant below sets
// them, because that is the sole point where the FTS and display tables differ.
var baseEntities = map[string]string{ //nolint:gochecknoglobals // read-only lookup table
	"nbsp": " ", "ndash": "–", "mdash": "—",
	"ldquo": "“", "rdquo": "”",
	"copy": "©", "reg": "®", "trade": "™",
	"hellip": "…", "bull": "•", "middot": "·",
	"laquo": "«", "raquo": "»",
	"euro": "€", "pound": "£", "yen": "¥", "cent": "¢",
}

// displayEntities resolves entities for text kept for display/storage, preserving
// the typographic single quotes ' '. Exposed through NewDisplayDecoder.
var displayEntities = withSingleQuotes("‘", "’") //nolint:gochecknoglobals // read-only lookup table

// ftsEntities normalizes the single curly quotes to the ASCII apostrophe so that
// search matches regardless of the quote style used in the source.
var ftsEntities = withSingleQuotes("'", "'") //nolint:gochecknoglobals // read-only lookup table

// withSingleQuotes returns a copy of baseEntities with lsquo/rsquo set.
func withSingleQuotes(lsquo, rsquo string) map[string]string {
	m := make(map[string]string, len(baseEntities)+2)
	maps.Copy(m, baseEntities)
	m["lsquo"] = lsquo
	m["rsquo"] = rsquo

	return m
}

// NewDisplayDecoder returns an xml.Decoder whose entity table resolves named
// HTML entities for display/storage, preserving the typographic single quotes
// ‘ ’. Each decoder receives its own copy of the table, so the package's entity
// map never escapes by reference.
func NewDisplayDecoder(r io.Reader) *xml.Decoder {
	dec := xml.NewDecoder(r)
	dec.Entity = maps.Clone(displayEntities)

	return dec
}

// StripMarkup reduces an HTML/XML annotation to plain text for FTS indexing.
// The rich original is stored in books.annotation; this is used only for the
// books_fts table.
//
// It uses an XML token decoder to properly resolve named HTML entities
// (e.g. &mdash;, &nbsp;, &ldquo;). If the input is malformed and the decoder
// fails, it falls back to a regex strip with basic entity unescaping.
func StripMarkup(s string) string {
	if s == "" {
		return ""
	}
	if result, ok := stripMarkupXML(s); ok {
		return result
	}

	return stripMarkupRegex(s)
}

// stripMarkupXML walks XML tokens and collects only CharData. ok is false on
// any decode error so the caller's regex fallback sees the whole input — a
// partial-prefix return would silently drop the tail of malformed annotations
// from FTS.
func stripMarkupXML(s string) (string, bool) {
	dec := xml.NewDecoder(strings.NewReader("<root>" + s + "</root>"))
	dec.Entity = ftsEntities
	var b strings.Builder
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", false
		}
		if cd, ok := tok.(xml.CharData); ok {
			text := strings.TrimSpace(string(cd))
			if text == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(text)
		}
	}

	return b.String(), true
}

var markupTag = regexp.MustCompile(`<[^>]*>`)

// stripMarkupRegex is the fallback for malformed HTML that breaks the XML
// decoder. It handles only the five standard HTML entities + numeric refs.
func stripMarkupRegex(s string) string {
	s = markupTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	return strings.Join(strings.Fields(s), " ")
}

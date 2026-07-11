package ebook

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"strings"

	"golang.org/x/text/encoding/ianaindex"

	"github.com/Toshik1978/folio/internal/htmltext"
)

const extFB2Zip = ".fb2.zip"

type fb2Book struct {
	XMLName     xml.Name    `xml:"FictionBook"`
	Description fb2Desc     `xml:"description"`
	Binaries    []fb2Binary `xml:"binary"`
}

type fb2Desc struct {
	TitleInfo   fb2TitleInfo `xml:"title-info"`
	PublishInfo *fb2PubInfo  `xml:"publish-info"`
}

type fb2PubInfo struct {
	Publisher string `xml:"publisher"`
	Year      string `xml:"year"`
	ISBN      string `xml:"isbn"`
}

type fb2TitleInfo struct {
	BookTitle  string      `xml:"book-title"`
	Genres     []string    `xml:"genre"`
	Authors    []fb2Author `xml:"author"`
	Annotation *fb2Markup  `xml:"annotation"`
	Sequence   *fb2Seq     `xml:"sequence"`
	Lang       string      `xml:"lang"`
	Coverpage  *fb2Cover   `xml:"coverpage"`
}

type fb2Author struct {
	FirstName  string `xml:"first-name"`
	MiddleName string `xml:"middle-name"`
	LastName   string `xml:"last-name"`
	Nickname   string `xml:"nickname"`
}

type fb2Markup struct {
	InnerXML string `xml:",innerxml"`
}

type fb2Seq struct {
	Name   string `xml:"name,attr"`
	Number string `xml:"number,attr"`
}

type fb2Cover struct {
	Images []fb2Image `xml:"image"`
}

type fb2Image struct {
	Href string `xml:"href,attr"`
}

type fb2Binary struct {
	ID          string `xml:"id,attr"`
	ContentType string `xml:"content-type,attr"`
	Data        string `xml:",chardata"`
}

func parseFB2(_ context.Context, path string) (Metadata, error) {
	f, err := os.Open(path)
	if err != nil {
		return Metadata{}, fmt.Errorf("open fb2: %w", err)
	}
	defer func() { _ = f.Close() }()

	// Stream the file through the XML decoder instead of reading it whole: the
	// metadata lives in the leading <description>, and the (potentially large)
	// <body> text carries no metadata, so the decoder discards it as it scans.
	// Cap the stream at the same ceiling as the .fb2.zip path (readZipEntry): an
	// FB2 embeds its cover as inline base64 in a <binary>, which the decoder
	// buffers as one large token, so an oversized file is rejected rather than
	// read wholesale into memory on the low-spec target hosts.
	return parseFB2Reader(bufio.NewReader(io.LimitReader(f, maxArchiveTextBytes)))
}

func parseFB2Zip(_ context.Context, path string) (Metadata, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return Metadata{}, fmt.Errorf("open fb2.zip: %w", err)
	}
	defer zr.Close()

	for _, f := range zr.File {
		if !strings.HasSuffix(strings.ToLower(f.Name), ".fb2") {
			continue
		}

		data, err := readZipEntry(f, maxArchiveTextBytes)
		if err != nil {
			return Metadata{}, fmt.Errorf("read fb2 in zip: %w", err)
		}

		return parseFB2XML(data)
	}

	return Metadata{}, errors.New("no .fb2 file found in archive")
}

func parseFB2XML(data []byte) (Metadata, error) {
	return parseFB2Reader(bytes.NewReader(data))
}

func parseFB2Reader(r io.Reader) (Metadata, error) {
	var book fb2Book
	dec := htmltext.NewDisplayDecoder(r)
	dec.CharsetReader = charsetReader
	if err := dec.Decode(&book); err != nil {
		return Metadata{}, fmt.Errorf("parse fb2 xml: %w", err)
	}

	ti := book.Description.TitleInfo

	m := Metadata{
		Title:    strings.TrimSpace(ti.BookTitle),
		Authors:  extractFB2Authors(ti.Authors),
		Genres:   ti.Genres,
		Language: strings.TrimSpace(ti.Lang),
	}

	if ti.Annotation != nil {
		m.Annotation = normalizeFB2Annotation(ti.Annotation.InnerXML)
	}

	if ti.Sequence != nil {
		m.Series = strings.TrimSpace(ti.Sequence.Name)
		m.SeriesNumber = parseSeriesIndex(ti.Sequence.Number)
	}

	if pi := book.Description.PublishInfo; pi != nil {
		m.Publisher = strings.TrimSpace(pi.Publisher)
		m.Year = ParseYear(pi.Year)
		if isbn := strings.TrimSpace(pi.ISBN); isbn != "" {
			m.Identifiers = append(m.Identifiers, Identifier{Type: IdentifierISBN, Value: isbn})
		}
	}

	m.Cover = extractFB2Cover(ti.Coverpage, book.Binaries)

	return m, nil
}

// fb2AnnotationTags maps the FB2 annotation markup vocabulary onto the HTML
// subset that survives the serve-time sanitizer (bluemonday UGCPolicy). FB2 uses
// its own tag names (e.g. <emphasis>, <strikethrough>) which the browser and the
// sanitizer would otherwise drop, flattening the annotation to a wall of text.
var fb2AnnotationTags = map[string]string{ //nolint:gochecknoglobals // read-only lookup table
	"p":             "p",
	"emphasis":      "em",
	"strong":        "strong",
	"strikethrough": "s",
	"sub":           "sub",
	"sup":           "sup",
	"code":          "code",
	"a":             "a",
}

// normalizeFB2Annotation rewrites an FB2 <annotation> body (raw inner XML) into a
// small, safe HTML subset so it renders consistently with annotations from other
// sources: FB2 tags become their HTML equivalents, <empty-line/> becomes <br/>,
// unknown tags are dropped (their text is kept), and character data / entities are
// resolved and re-escaped as HTML. Malformed input is returned unchanged — the
// serve-time sanitizer still guards rendering.
func normalizeFB2Annotation(inner string) string {
	if strings.TrimSpace(inner) == "" {
		return ""
	}

	dec := htmltext.NewDisplayDecoder(strings.NewReader("<root>" + inner + "</root>"))

	var b strings.Builder
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return inner // malformed; leave as-is for the serve-time sanitizer
		}
		writeFB2AnnotationToken(&b, tok)
	}

	return strings.TrimSpace(b.String())
}

// writeFB2AnnotationToken appends the HTML rendering of one FB2 annotation token.
func writeFB2AnnotationToken(b *strings.Builder, tok xml.Token) {
	switch t := tok.(type) {
	case xml.StartElement:
		writeFB2StartTag(b, t)
	case xml.EndElement:
		if t.Name.Local == "root" || t.Name.Local == "empty-line" {
			return
		}
		if tag, ok := fb2AnnotationTags[t.Name.Local]; ok {
			b.WriteString("</" + tag + ">")
		}
	case xml.CharData:
		b.WriteString(html.EscapeString(string(t)))
	}
}

func writeFB2StartTag(b *strings.Builder, t xml.StartElement) {
	switch t.Name.Local {
	case "root":
		return
	case "empty-line":
		b.WriteString("<br/>")
	case "a":
		b.WriteString("<a")
		if href := fb2Href(t.Attr); href != "" {
			b.WriteString(` href="` + html.EscapeString(href) + `"`)
		}
		b.WriteByte('>')
	default:
		if tag, ok := fb2AnnotationTags[t.Name.Local]; ok {
			b.WriteString("<" + tag + ">")
		}
	}
}

// fb2Href returns the link target from an FB2 <a> element, accepting any
// namespace prefix (FB2 uses xlink:href / l:href).
func fb2Href(attrs []xml.Attr) string {
	for _, a := range attrs {
		if a.Name.Local == "href" {
			return strings.TrimSpace(a.Value)
		}
	}

	return ""
}

func extractFB2Authors(authors []fb2Author) []string {
	var out []string
	for _, a := range authors {
		name := buildFB2AuthorName(a)
		if name != "" {
			out = append(out, name)
		}
	}

	return out
}

func buildFB2AuthorName(a fb2Author) string {
	if a.Nickname != "" && a.FirstName == "" && a.LastName == "" {
		return strings.TrimSpace(a.Nickname)
	}

	var parts []string
	if v := strings.TrimSpace(a.FirstName); v != "" {
		parts = append(parts, v)
	}
	if v := strings.TrimSpace(a.MiddleName); v != "" {
		parts = append(parts, v)
	}
	if v := strings.TrimSpace(a.LastName); v != "" {
		parts = append(parts, v)
	}

	return strings.Join(parts, " ")
}

func extractFB2Cover(coverpage *fb2Cover, binaries []fb2Binary) []byte {
	if coverpage == nil || len(coverpage.Images) == 0 {
		return nil
	}

	href := strings.TrimPrefix(coverpage.Images[0].Href, "#")
	if href == "" {
		return nil
	}

	for _, b := range binaries {
		if b.ID == href && strings.HasPrefix(b.ContentType, "image/") {
			return decodeBase64Binary(b.Data)
		}
	}

	return nil
}

func decodeBase64Binary(s string) []byte {
	cleaned := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
			return -1
		}
		return r
	}, s)

	data, err := base64.StdEncoding.DecodeString(cleaned)
	if err != nil {
		return nil
	}

	return data
}

func charsetReader(charset string, input io.Reader) (io.Reader, error) {
	enc, err := ianaindex.IANA.Encoding(charset)
	if err != nil {
		return nil, fmt.Errorf("unsupported charset %q: %w", charset, err)
	}
	if enc == nil {
		return input, nil
	}

	return enc.NewDecoder().Reader(input), nil
}

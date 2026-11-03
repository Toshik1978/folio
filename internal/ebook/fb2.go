package ebook

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"strings"

	"golang.org/x/text/encoding/ianaindex"

	"github.com/Toshik1978/folio/internal/htmltext"
)

const extFB2Zip = ".fb2.zip"

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

// parseFB2Header decodes only the <description> element and stops at its closing
// tag. The input is capped at maxArchiveTextBytes. Metadata.Cover stays nil.
// A well-formed document without a <description> — the header ends at the first
// <body> or <binary> — is readable with empty metadata, not malformed, so the
// caller falls back to the file name as before; a stream with no XML element at
// all is still an error.
func parseFB2Header(r io.Reader) (Metadata, error) {
	dec := newFB2Decoder(newCapReader(r, maxArchiveTextBytes))

	sawRoot := false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			if sawRoot {
				return Metadata{}, nil
			}

			return Metadata{}, errors.New("parse fb2 xml: no root element")
		}
		if err != nil {
			return Metadata{}, fmt.Errorf("parse fb2 xml: %w", err)
		}

		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		sawRoot = true
		switch start.Name.Local {
		case "body", "binary":
			return Metadata{}, nil
		case "description":
		default:
			continue
		}

		var desc fb2Desc
		if err := dec.DecodeElement(&desc, &start); err != nil {
			return Metadata{}, fmt.Errorf("parse fb2 description: %w", err)
		}

		return fb2DescMetadata(desc), nil
	}
}

func newFB2Decoder(r io.Reader) *xml.Decoder {
	dec := htmltext.NewDisplayDecoder(r)
	dec.CharsetReader = charsetReader

	return dec
}

// fb2DescMetadata maps a decoded <description> onto Metadata (without the cover,
// which lives in a <binary> outside the description).
func fb2DescMetadata(desc fb2Desc) Metadata {
	ti := desc.TitleInfo

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

	if pi := desc.PublishInfo; pi != nil {
		m.Publisher = strings.TrimSpace(pi.Publisher)
		m.Year = ParseYear(pi.Year)
		if isbn := strings.TrimSpace(pi.ISBN); isbn != "" {
			m.Identifiers = append(m.Identifiers, Identifier{Type: IdentifierISBN, Value: isbn})
		}
	}

	return m
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

// decodeFB2Binary decodes the base64 text of a <binary>, ignoring whitespace.
// Invalid or truncated base64 is an error.
func decodeFB2Binary(s string) ([]byte, error) {
	cleaned := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
			return -1
		}
		return r
	}, s)

	data, err := base64.StdEncoding.DecodeString(cleaned)
	if err != nil {
		return nil, fmt.Errorf("decode fb2 binary: %w", err)
	}

	return data, nil
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

// fb2Open returns the stream holding the FB2 XML for src: the source itself, or
// its first .fb2 entry when src is a .fb2.zip. The closer releases the lazy
// entry, if any.
func fb2Open(src Source) (io.Reader, func(), error) {
	if !strings.HasSuffix(strings.ToLower(src.Name), extFB2Zip) {
		return bufio.NewReader(io.NewSectionReader(src.R, 0, src.Size)), func() {}, nil
	}

	zr, err := zip.NewReader(src.R, src.Size)
	if err != nil {
		return nil, nil, fmt.Errorf("open fb2.zip: %w", err)
	}
	for _, f := range zr.File {
		if !strings.HasSuffix(strings.ToLower(f.Name), ".fb2") {
			continue
		}
		entry, closer := OpenZipEntry(f, MaxEntryBytes)

		return bufio.NewReader(io.NewSectionReader(entry.R, 0, entry.Size)), func() { _ = closer.Close() }, nil
	}

	return nil, nil, errors.New("no .fb2 file found in archive")
}

// fb2ReadMetadata reads the <description> header of src without touching the body.
func fb2ReadMetadata(ctx context.Context, src Source) (Metadata, error) {
	if err := ctx.Err(); err != nil {
		return Metadata{}, fmt.Errorf("fb2 metadata: %w", err)
	}
	r, release, err := fb2Open(src)
	if err != nil {
		return Metadata{}, err
	}
	defer release()

	return parseFB2Header(r)
}

// fb2ReadCover token-scans src for the coverpage reference in <description>, then
// decodes only the <binary> carrying that id.
func fb2ReadCover(ctx context.Context, src Source) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("fb2 cover: %w", err)
	}
	r, release, err := fb2Open(src)
	if err != nil {
		return nil, err
	}
	defer release()

	return fb2ScanCover(r)
}

// fb2ScanCover walks the token stream of r for the cover reference, then for
// the binary it names.
func fb2ScanCover(r io.Reader) ([]byte, error) {
	dec := newFB2Decoder(newCapReader(r, maxArchiveTextBytes))

	href, err := fb2FindCoverRef(dec)
	if err != nil || href == "" {
		return nil, err
	}

	return fb2FindBinary(dec, href)
}

// fb2FindCoverRef returns the first coverpage image id, or "" if there is none.
func fb2FindCoverRef(dec *xml.Decoder) (string, error) {
	for {
		start, ok, err := nextFB2Start(dec)
		if err != nil || !ok {
			return "", err
		}
		if start.Name.Local == "coverpage" {
			return decodeFB2Coverpage(dec, &start)
		}
		if start.Name.Local == "body" {
			return "", nil
		}
	}
}

// fb2FindBinary decodes the <binary> with the given id, or returns nil.
func fb2FindBinary(dec *xml.Decoder, id string) ([]byte, error) {
	for {
		start, ok, err := nextFB2Start(dec)
		if err != nil || !ok {
			return nil, err
		}
		if start.Name.Local == "binary" && attrValue(start.Attr, "id") == id {
			return decodeFB2BinaryElement(dec, &start)
		}
	}
}

// nextFB2Start returns the next start element; ok is false at a clean end of input.
func nextFB2Start(dec *xml.Decoder) (start xml.StartElement, ok bool, err error) {
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return xml.StartElement{}, false, nil
		}
		if err != nil {
			return xml.StartElement{}, false, fmt.Errorf("parse fb2 xml: %w", err)
		}
		if start, ok := tok.(xml.StartElement); ok {
			return start, true, nil
		}
	}
}

// decodeFB2BinaryElement decodes the <binary> element at start as a cover.
func decodeFB2BinaryElement(dec *xml.Decoder, start *xml.StartElement) ([]byte, error) {
	var b fb2Binary
	if err := dec.DecodeElement(&b, start); err != nil {
		return nil, fmt.Errorf("parse fb2 binary: %w", err)
	}

	return decodeFB2Cover(b.Data)
}

// decodeFB2Coverpage returns the first image reference (without '#') of a
// <coverpage> element.
func decodeFB2Coverpage(dec *xml.Decoder, start *xml.StartElement) (string, error) {
	var cp fb2Cover
	if err := dec.DecodeElement(&cp, start); err != nil {
		return "", fmt.Errorf("parse fb2 coverpage: %w", err)
	}
	if len(cp.Images) == 0 {
		return "", nil
	}

	return strings.TrimPrefix(strings.TrimSpace(cp.Images[0].Href), "#"), nil
}

// decodeFB2Cover decodes cover base64, rejecting a payload over maxCoverBytes.
func decodeFB2Cover(text string) ([]byte, error) {
	// Base64 text is 4/3 of the payload; allow slack for line breaks.
	if int64(len(text)) > maxCoverBytes*2 {
		return nil, fmt.Errorf("%w: fb2 cover is larger than %d bytes", ErrTooLarge, maxCoverBytes)
	}
	data, err := decodeFB2Binary(text)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxCoverBytes {
		return nil, fmt.Errorf("%w: fb2 cover is larger than %d bytes", ErrTooLarge, maxCoverBytes)
	}
	if len(data) == 0 {
		return nil, nil
	}

	return data, nil
}

func attrValue(attrs []xml.Attr, name string) string {
	for _, a := range attrs {
		if a.Name.Local == name {
			return a.Value
		}
	}

	return ""
}

// capReader passes reads through until more than limit bytes have been read,
// then fails with ErrTooLarge. Unlike io.LimitReader's silent EOF — which the
// XML decoder would report as a truncated, i.e. corrupt, file — hitting the
// cap classifies the same way as every other format's size cap.
type capReader struct {
	r     io.Reader
	limit int64
	read  int64
}

func newCapReader(r io.Reader, limit int64) *capReader {
	return &capReader{r: r, limit: limit}
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.read > c.limit {
		return 0, fmt.Errorf("%w: fb2 exceeds %d bytes", ErrTooLarge, c.limit)
	}
	if room := c.limit + 1 - c.read; int64(len(p)) > room {
		p = p[:room]
	}
	n, err := c.r.Read(p)
	c.read += int64(n)
	if c.read > c.limit {
		return n, fmt.Errorf("%w: fb2 exceeds %d bytes", ErrTooLarge, c.limit)
	}

	return n, err //nolint:wrapcheck // a pass-through reader: io.EOF must reach the decoder unwrapped
}

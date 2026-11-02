package ebook

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"strings"
)

type opfPackage struct {
	XMLName  xml.Name    `xml:"package"`
	Metadata opfMetadata `xml:"metadata"`
	Manifest opfManifest `xml:"manifest"`
}

type opfMetadata struct {
	Title       []string        `xml:"title"`
	Creator     []opfCreator    `xml:"creator"`
	Description []string        `xml:"description"`
	Subject     []string        `xml:"subject"`
	Language    []string        `xml:"language"`
	Publisher   []string        `xml:"publisher"`
	Date        []string        `xml:"date"`
	Identifier  []opfIdentifier `xml:"identifier"`
	Meta        []opfMeta       `xml:"meta"`
}

type opfCreator struct {
	Value string `xml:",chardata"`
}

type opfIdentifier struct {
	Scheme string `xml:"scheme,attr"`
	Value  string `xml:",chardata"`
}

type opfMeta struct {
	Name    string `xml:"name,attr"`
	Content string `xml:"content,attr"`
}

type opfManifest struct {
	Items []opfItem `xml:"item"`
}

type opfItem struct {
	ID        string `xml:"id,attr"`
	Href      string `xml:"href,attr"`
	MediaType string `xml:"media-type,attr"`
}

type epubContainer struct {
	XMLName  xml.Name       `xml:"container"`
	Rootfile []rootfileItem `xml:"rootfiles>rootfile"`
}

type rootfileItem struct {
	FullPath  string `xml:"full-path,attr"`
	MediaType string `xml:"media-type,attr"`
}

// readEPUBPackage locates and parses the OPF package document of zr, returning
// it with its path inside the archive.
func readEPUBPackage(zr *zip.Reader) (opfPackage, string, error) {
	opfPath, err := findOPFPath(zr)
	if err != nil {
		return opfPackage{}, "", err
	}

	opfData, err := readZipFile(zr, opfPath, maxArchiveTextBytes)
	if err != nil {
		return opfPackage{}, "", fmt.Errorf("read OPF: %w", err)
	}

	var pkg opfPackage
	if err := xml.Unmarshal(opfData, &pkg); err != nil {
		return opfPackage{}, "", fmt.Errorf("parse OPF: %w", err)
	}

	return pkg, opfPath, nil
}

// epubMetadata maps an OPF package to Metadata, without a cover.
func epubMetadata(pkg opfPackage) Metadata {
	m := Metadata{
		Title:   firstNonEmpty(pkg.Metadata.Title),
		Authors: extractCreators(pkg.Metadata.Creator),
		Genres:  pkg.Metadata.Subject,
	}

	if len(pkg.Metadata.Description) > 0 {
		m.Annotation = firstNonEmpty(pkg.Metadata.Description)
	}
	if len(pkg.Metadata.Language) > 0 {
		m.Language = firstNonEmpty(pkg.Metadata.Language)
	}
	m.Publisher = firstNonEmpty(pkg.Metadata.Publisher)
	m.Year = ParseYear(firstNonEmpty(pkg.Metadata.Date))
	m.Identifiers = extractEPUBIdentifiers(pkg.Metadata.Identifier)

	applySeriesMeta(pkg.Metadata.Meta, &m)

	// Calibre stores "{series} - {index} - {title}" in dc:title; recover the
	// clean title so editions group with their siblings (which keep it clean).
	m.Title = stripSeriesPrefix(m.Title, m.Series)

	return m
}

// epubReadMetadata reads the OPF package of src and maps it to Metadata.
func epubReadMetadata(ctx context.Context, src Source) (Metadata, error) {
	if err := ctx.Err(); err != nil {
		return Metadata{}, fmt.Errorf("epub metadata: %w", err)
	}
	zr, err := zip.NewReader(src.R, src.Size)
	if err != nil {
		return Metadata{}, fmt.Errorf("open epub: %w", err)
	}
	pkg, _, err := readEPUBPackage(zr)
	if err != nil {
		return Metadata{}, err
	}

	return epubMetadata(pkg), nil
}

// epubReadCover reads the declared cover image of src, or (nil, nil) when the
// package declares none.
func epubReadCover(ctx context.Context, src Source) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("epub cover: %w", err)
	}
	zr, err := zip.NewReader(src.R, src.Size)
	if err != nil {
		return nil, fmt.Errorf("open epub: %w", err)
	}
	pkg, opfPath, err := readEPUBPackage(zr)
	if err != nil {
		return nil, err
	}

	return epubCover(zr, pkg, opfPath)
}

// applySeriesMeta fills series name/index from Calibre's OPF meta elements.
func applySeriesMeta(metas []opfMeta, m *Metadata) {
	for _, meta := range metas {
		if meta.Name == "calibre:series" && m.Series == "" {
			m.Series = meta.Content
		}
		if meta.Name == "calibre:series_index" && m.SeriesNumber == 0 {
			m.SeriesNumber = parseSeriesIndex(meta.Content)
		}
	}
}

func findOPFPath(zr *zip.Reader) (string, error) {
	containerData, err := readZipFile(zr, "META-INF/container.xml", maxArchiveTextBytes)
	if err != nil {
		return "", fmt.Errorf("read container.xml: %w", err)
	}

	var c epubContainer
	if err := xml.Unmarshal(containerData, &c); err != nil {
		return "", fmt.Errorf("parse container.xml: %w", err)
	}

	for _, rf := range c.Rootfile {
		if rf.FullPath != "" {
			return rf.FullPath, nil
		}
	}

	return "", errors.New("no rootfile in container.xml")
}

func extractCreators(creators []opfCreator) []string {
	var out []string
	for _, c := range creators {
		v := strings.TrimSpace(c.Value)
		if v != "" {
			out = append(out, v)
		}
	}

	return out
}

// extractEPUBIdentifiers maps dc:identifier elements to typed identifiers. The
// scheme (opf:scheme attribute) becomes the type; for unscheme'd "urn:isbn:"
// values the ISBN type is inferred.
func extractEPUBIdentifiers(ids []opfIdentifier) []Identifier {
	var out []Identifier
	for _, id := range ids {
		value := strings.TrimSpace(id.Value)
		if value == "" {
			continue
		}
		typ := strings.ToLower(strings.TrimSpace(id.Scheme))
		if typ == "" {
			lower := strings.ToLower(value)
			switch {
			case strings.HasPrefix(lower, "urn:isbn:"):
				typ = IdentifierISBN
			case strings.HasPrefix(lower, "isbn:"):
				typ = IdentifierISBN
			case LooksLikeISBN(value):
				typ = IdentifierISBN
			default:
				continue // unknown, untyped identifier — skip
			}
		}
		out = append(out, Identifier{Type: typ, Value: value})
	}

	return out
}

// epubCover returns the bytes of the manifest cover image, capped at
// maxCoverBytes. It returns (nil, nil) when the package declares no cover, and
// an error when the declared cover entry is missing or unreadable.
func epubCover(zr *zip.Reader, pkg opfPackage, opfPath string) ([]byte, error) {
	coverID := findCoverID(pkg)
	if coverID == "" {
		return nil, nil
	}

	for _, item := range pkg.Manifest.Items {
		if item.ID == coverID && strings.HasPrefix(item.MediaType, "image/") {
			return readZipFile(zr, resolveOPFRelative(opfPath, item.Href), maxCoverBytes)
		}
	}

	return nil, nil
}

func findCoverID(pkg opfPackage) string {
	for _, meta := range pkg.Metadata.Meta {
		if meta.Name == "cover" {
			return meta.Content
		}
	}
	for _, item := range pkg.Manifest.Items {
		if strings.Contains(item.ID, "cover") && strings.HasPrefix(item.MediaType, "image/") {
			return item.ID
		}
	}

	return ""
}

func resolveOPFRelative(opfPath, href string) string {
	if decoded, err := url.PathUnescape(href); err == nil {
		href = decoded
	}
	// Clean folds "./" and "../" segments, which zip entry names never carry.
	return path.Join(path.Dir(opfPath), href)
}

func readZipFile(zr *zip.Reader, name string, limit int64) ([]byte, error) {
	for _, f := range zr.File {
		if f.Name == name {
			return readZipEntry(f, limit)
		}
	}

	return nil, fmt.Errorf("file not found in archive: %s", name)
}

// readZipEntry reads a zip entry's decompressed bytes, capped at limit. The cap
// is the truth (a malicious archive can understate UncompressedSize64), so we
// read limit+1 through a LimitReader and reject anything that reaches it — this
// is what bounds zip-bomb expansion.
func readZipEntry(f *zip.File, limit int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("open zip entry %s: %w", f.Name, err)
	}
	defer rc.Close()

	data, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read zip entry %s: %w", f.Name, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("zip entry %s exceeds %d byte limit", f.Name, limit)
	}

	return data, nil
}

// seriesPrefixRe matches Calibre's "{series} - {index} - {title}" decoration,
// where {index} may be a (possibly zero-padded or fractional) number. Group 1 is
// the leading series name and group 2 the clean title; stripSeriesPrefix only
// trusts the match when group 1 equals the known series, so unrelated titles that
// merely contain a " - N - " segment are left untouched.
var seriesPrefixRe = regexp.MustCompile(`^(.+?)\s*-\s*\d+(?:\.\d+)?\s*-\s*(.+)$`)

// stripSeriesPrefix removes a "{series} - {index} - " prefix that Calibre embeds
// in an EPUB's dc:title, returning the clean title. It is a no-op when series is
// empty or the title is not decorated with that exact pattern for this series.
func stripSeriesPrefix(title, series string) string {
	t := strings.TrimSpace(title)
	if series == "" {
		return t
	}
	if m := seriesPrefixRe.FindStringSubmatch(t); len(m) == 3 && strings.TrimSpace(m[1]) == series {
		return strings.TrimSpace(m[2])
	}

	return t
}

func firstNonEmpty(ss []string) string {
	for _, s := range ss {
		v := strings.TrimSpace(s)
		if v != "" {
			return v
		}
	}

	return ""
}

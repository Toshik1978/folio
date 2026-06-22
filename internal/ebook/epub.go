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
	"sync"
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

func parseEPUB(_ context.Context, filePath string) (Metadata, error) {
	zr, err := zip.OpenReader(filePath)
	if err != nil {
		return Metadata{}, fmt.Errorf("open epub: %w", err)
	}
	defer zr.Close()

	opfPath, err := findOPFPath(&zr.Reader)
	if err != nil {
		return Metadata{}, err
	}

	opfData, err := readZipFile(&zr.Reader, opfPath)
	if err != nil {
		return Metadata{}, fmt.Errorf("read OPF: %w", err)
	}

	var pkg opfPackage
	if err := xml.Unmarshal(opfData, &pkg); err != nil {
		return Metadata{}, fmt.Errorf("parse OPF: %w", err)
	}

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

	m.Cover = extractEPUBCover(&zr.Reader, pkg, opfPath)

	return m, nil
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
	containerData, err := readZipFile(zr, "META-INF/container.xml")
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
				typ = identifierISBN
			case strings.HasPrefix(lower, "isbn:"):
				typ = identifierISBN
			case LooksLikeISBN(value):
				typ = identifierISBN
			default:
				continue // unknown, untyped identifier — skip
			}
		}
		out = append(out, Identifier{Type: typ, Value: value})
	}

	return out
}

func extractEPUBCover(zr *zip.Reader, pkg opfPackage, opfPath string) []byte {
	coverID := findCoverID(pkg)
	if coverID == "" {
		return nil
	}

	for _, item := range pkg.Manifest.Items {
		if item.ID == coverID && strings.HasPrefix(item.MediaType, "image/") {
			data, _ := readZipFile(zr, resolveOPFRelative(opfPath, item.Href))
			return data
		}
	}

	return nil
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
	dir := path.Dir(opfPath)
	if dir == "." {
		return href
	}

	return dir + "/" + href
}

func readZipFile(zr *zip.Reader, name string) ([]byte, error) {
	for _, f := range zr.File {
		if f.Name == name {
			return readZipEntry(f)
		}
	}

	return nil, fmt.Errorf("file not found in archive: %s", name)
}

func readZipEntry(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("open zip entry %s: %w", f.Name, err)
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("read zip entry %s: %w", f.Name, err)
	}

	return data, nil
}

// seriesPrefixPattern matches a leading "{series} - {index} - " decoration,
// where {index} may be a (possibly zero-padded or fractional) number. The series
// name is injected as a quoted literal by stripSeriesPrefix.
var seriesPrefixCache = struct { //nolint:gochecknoglobals
	mu    sync.Mutex
	cache map[string]*regexp.Regexp
}{
	cache: map[string]*regexp.Regexp{},
}

// stripSeriesPrefix removes a "{series} - {index} - " prefix that Calibre embeds
// in an EPUB's dc:title, returning the clean title. It is a no-op when series is
// empty or the title is not decorated with that exact pattern.
func stripSeriesPrefix(title, series string) string {
	t := strings.TrimSpace(title)
	if series == "" {
		return t
	}
	re := seriesPrefixRegexp(series)
	if m := re.FindStringSubmatch(t); m != nil {
		return strings.TrimSpace(m[1])
	}

	return t
}

// seriesPrefixCacheLimit caps the per-series regex cache; on overflow the map
// is reset wholesale rather than evicted entry-by-entry — recompiling is cheap
// and the cache only short-circuits repeats within a sync run.
const seriesPrefixCacheLimit = 1024

func seriesPrefixRegexp(series string) *regexp.Regexp {
	seriesPrefixCache.mu.Lock()
	defer seriesPrefixCache.mu.Unlock()
	if re, ok := seriesPrefixCache.cache[series]; ok {
		return re
	}
	if len(seriesPrefixCache.cache) >= seriesPrefixCacheLimit {
		seriesPrefixCache.cache = map[string]*regexp.Regexp{}
	}
	re := regexp.MustCompile(`^` + regexp.QuoteMeta(series) + `\s*-\s*\d+(?:\.\d+)?\s*-\s*(.+)$`)
	seriesPrefixCache.cache[series] = re

	return re
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

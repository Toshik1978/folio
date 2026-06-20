package ebook

import (
	"archive/zip"
	"context"
	"fmt"
	"os"
	"path/filepath"
)

type epubSuite struct {
	baseSuite
}

func (s *epubSuite) TestParse() {
	m, err := s.d.Parse(context.Background(), s.log, s.fixture("test.epub"))
	s.Require().NoError(err)

	s.Equal("Test EPUB Book", m.Title)
	s.Equal([]string{"Jane Author", "John Coauthor"}, m.Authors)
	s.Equal([]string{"Science Fiction", "Adventure"}, m.Genres)
	s.Equal("A test book for unit testing.", m.Annotation)
	s.Equal("en", m.Language)
	s.Equal("Test Series", m.Series)
	s.NotEmpty(m.Cover, "expected cover bytes")
}

func (s *epubSuite) TestStripSeriesPrefix() {
	// Calibre EPUBs embed "{series} - {index} - {title}" in dc:title; the clean
	// title must be recovered so editions group with their azw3/fb2 siblings.
	s.Equal("Периферийные устройства",
		stripSeriesPrefix("Периферийные устройства - 01 - Периферийные устройства", "Периферийные устройства"))
	s.Equal("Волчья Луна", stripSeriesPrefix("Луна - 02 - Волчья Луна", "Луна"))
	s.Equal("Foundation", stripSeriesPrefix("Foundation - 1 - Foundation", "Foundation"))
	// No series, no prefix match, and series-as-substring-but-not-decoration are untouched.
	s.Equal("Clean Title", stripSeriesPrefix("Clean Title", ""))
	s.Equal("Clean Title", stripSeriesPrefix("Clean Title", "Other"))
	s.Equal("Dune Messiah", stripSeriesPrefix("Dune Messiah", "Dune"))
}

func (s *epubSuite) TestSeriesPrefixCacheIsBounded() {
	for i := range seriesPrefixCacheLimit + 10 {
		_ = seriesPrefixRegexp(fmt.Sprintf("Bound Check Series %d", i))
	}

	seriesPrefixCache.mu.Lock()
	defer seriesPrefixCache.mu.Unlock()
	s.LessOrEqual(len(seriesPrefixCache.cache), seriesPrefixCacheLimit)
}

func (s *epubSuite) TestSeriesIndex() {
	path := filepath.Join(s.T().TempDir(), "book.epub")
	f, err := os.Create(path)
	s.Require().NoError(err)
	zw := zip.NewWriter(f)
	write := func(name, content string) {
		w, werr := zw.Create(name)
		s.Require().NoError(werr)
		_, werr = w.Write([]byte(content))
		s.Require().NoError(werr)
	}
	write("META-INF/container.xml", `<?xml version="1.0"?>
<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container" version="1.0">
<rootfiles><rootfile full-path="content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`)
	write("content.opf", `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
<metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
<dc:title>Dune</dc:title><dc:language>en</dc:language>
<meta name="calibre:series" content="Dune Saga"/>
<meta name="calibre:series_index" content="3"/>
</metadata><manifest/></package>`)
	s.Require().NoError(zw.Close())
	s.Require().NoError(f.Close())

	m, err := s.d.Parse(context.Background(), s.log, path)
	s.Require().NoError(err)
	s.Equal("Dune Saga", m.Series)
	s.InDelta(3.0, m.SeriesNumber, 0.0001)
}

package ebook

import (
	"archive/zip"
	"context"
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
	// A title decorated for a different series than the one we know must be left
	// alone — the captured prefix is compared against series, not blindly stripped.
	s.Equal("Other Series - 02 - Real Title", stripSeriesPrefix("Other Series - 02 - Real Title", "My Series"))
}

func (s *epubSuite) TestOversizedCoverIsBounded() {
	// A pathological EPUB can hold a tiny compressed entry that inflates to
	// hundreds of MB. Cover extraction must cap the decompressed read so a zip
	// bomb can't spike memory on the low-spec target hosts (Pi/NAS); the parse
	// still succeeds with the oversized cover dropped rather than loaded.
	const coverSize = 128 << 20 // inflates from a few KB on disk

	path := filepath.Join(s.T().TempDir(), "bomb.epub")
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
<dc:title>Bomb</dc:title>
<meta name="cover" content="cover-img"/>
</metadata>
<manifest><item id="cover-img" href="cover.jpg" media-type="image/jpeg"/></manifest>
</package>`)

	cw, err := zw.Create("cover.jpg")
	s.Require().NoError(err)
	chunk := make([]byte, 1<<20)
	for written := 0; written < coverSize; written += len(chunk) {
		_, werr := cw.Write(chunk)
		s.Require().NoError(werr)
	}
	s.Require().NoError(zw.Close())
	s.Require().NoError(f.Close())

	m, peak := s.parsePeakHeapBytes(path)
	s.Equal("Bomb", m.Title)
	s.Empty(m.Cover, "oversized cover must be dropped, not loaded into memory")
	s.Less(peak, uint64(32<<20), "cover read must stay bounded well below the inflated size")
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

package ebook

import (
	"archive/zip"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type fb2Suite struct {
	baseSuite
}

// TestMetadataBoundedMemory guards against loading the whole file into memory:
// a large FB2 (here a ~32 MB body, comfortably under the maxArchiveTextBytes
// read cap) must be streamed, allocating far less than its size. The body text
// is not part of the metadata, so the header-only read never reaches it.
func (s *fb2Suite) TestMetadataBoundedMemory() {
	const bodySize = 32 << 20

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<FictionBook><description><title-info>`)
	b.WriteString(`<book-title>Huge FB2</book-title>`)
	b.WriteString(`<author><first-name>Big</first-name><last-name>Book</last-name></author>`)
	b.WriteString(`<lang>en</lang></title-info></description><body>`)
	// Many small elements so no single CharData token is large — this isolates
	// the whole-file buffering of the file from XML token buffering.
	const line = "<p>The quick brown fox jumps over the lazy dog.</p>\n"
	for range bodySize/len(line) + 1 {
		b.WriteString(line)
	}
	b.WriteString(`</body></FictionBook>`)

	path := filepath.Join(s.T().TempDir(), "huge.fb2")
	s.Require().NoError(os.WriteFile(path, []byte(b.String()), 0o600))

	var m Metadata
	peak := s.peakHeapBytes(func() { m = s.readMetadata(path) })
	s.Equal("Huge FB2", m.Title)
	s.Lessf(peak, uint64(bodySize/2),
		"parsing a ~%d-byte FB2 held %d peak heap bytes; it must stream, not buffer the whole file",
		bodySize, peak)
}

// TestMetadataRejectsOversizedHeader guards the maxArchiveTextBytes read cap: a
// <description> that does not close within the cap (e.g. a pathological
// annotation) is rejected rather than buffered without bound, and classified
// as too large (like every other format's cap), not as a corrupt file.
func (s *fb2Suite) TestMetadataRejectsOversizedHeader() {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<FictionBook><description><title-info>`)
	b.WriteString(`<book-title>Oversized</book-title><annotation>`)
	const line = "<p>The quick brown fox jumps over the lazy dog.</p>\n"
	for range int(maxArchiveTextBytes)/len(line) + 1 {
		b.WriteString(line)
	}
	b.WriteString(`</annotation></title-info></description><body/></FictionBook>`)

	path := filepath.Join(s.T().TempDir(), "oversized.fb2")
	s.Require().NoError(os.WriteFile(path, []byte(b.String()), 0o600))

	_, err := s.d.Metadata(context.Background(), s.log, s.open(path))
	s.Require().ErrorIs(err, ErrTooLarge)
	reason, ok := ReasonOf(err)
	s.True(ok)
	s.Equal(ReasonTooLarge, reason)
}

func (s *fb2Suite) TestMetadata() {
	m := s.readMetadata(s.fixture("test.fb2"))

	s.Equal("Test FB2 Book", m.Title)
	s.Equal([]string{"Ivan P. Testov"}, m.Authors)
	s.Equal([]string{"sf_history", "adventure"}, m.Genres) // raw flibusta codes, like INPX
	s.Equal("<p>An annotation for testing.</p>", m.Annotation)
	s.Equal("ru", m.Language)
	s.Equal("FB2 Series", m.Series)
	s.Nil(m.Cover, "Metadata never decodes the cover")
	s.NotEmpty(s.readCover(s.fixture("test.fb2")), "expected cover bytes")
}

func (s *fb2Suite) TestMetadataZip() {
	m := s.readMetadata(s.fixture("test.fb2.zip"))

	s.Equal("Test FB2 Book", m.Title)
	s.Equal([]string{"Ivan P. Testov"}, m.Authors)
	s.Equal("ru", m.Language)
	s.Equal("FB2 Series", m.Series)
}

func (s *fb2Suite) TestMetadataWin1251() {
	m := s.readMetadata(s.fixture("test_win1251.fb2"))

	s.Equal("Проверка", m.Title)
	s.Equal([]string{"Иван Тестов"}, m.Authors)
	s.Equal("ru", m.Language)
}

func (s *fb2Suite) TestMetadataEntities() {
	m := s.readMetadata(s.fixture("test_entities.fb2"))

	s.Equal("Entity Test", m.Title)
	s.Equal([]string{"Jack O'Brien"}, m.Authors)
	// NB: "A\u00a0word" uses a non-breaking space, from the &nbsp; entity.
	s.Equal(
		"<p>Jack &amp; Jill went &#34;up&#34; the hill.</p>"+
			"<p>Score: 5 &lt; 10 &gt; 3.</p>"+
			"<p>A\u00a0word \u2014 another \u201cquoted\u201d.</p>",
		m.Annotation,
	)
	s.Equal("en", m.Language)
}

// TestNormalizeAnnotationMapsFB2Tags covers M1b: FB2 markup tags are rewritten to
// the HTML subset the serve-time sanitizer keeps, <empty-line/> becomes <br/>,
// links keep their href, and unknown tags are dropped while their text survives.
func (s *fb2Suite) TestNormalizeAnnotationMapsFB2Tags() {
	in := `<p>An <emphasis>emphasised</emphasis> and <strong>bold</strong> intro.</p>` +
		`<empty-line/>` +
		`<p>A <strikethrough>struck</strikethrough> word, H<sub>2</sub>O, and ` +
		`a <a xlink:href="https://example.com">link</a>.</p>` +
		`<p>Edge<unknown> kept</unknown> text.</p>`

	got := normalizeFB2Annotation(in)

	s.Equal(
		`<p>An <em>emphasised</em> and <strong>bold</strong> intro.</p>`+
			`<br/>`+
			`<p>A <s>struck</s> word, H<sub>2</sub>O, and `+
			`a <a href="https://example.com">link</a>.</p>`+
			`<p>Edge kept text.</p>`,
		got,
	)
}

// TestNormalizeAnnotationFallsBackOnMalformed returns the input unchanged when it
// can't be parsed, so the serve-time sanitizer remains the safety net.
func (s *fb2Suite) TestNormalizeAnnotationFallsBackOnMalformed() {
	in := `<p>unterminated`
	s.Equal(in, normalizeFB2Annotation(in))
	s.Empty(normalizeFB2Annotation("   "))
}

func (s *fb2Suite) TestSeriesNumber() {
	m, err := parseFB2Header(strings.NewReader(`<?xml version="1.0" encoding="UTF-8"?>
<FictionBook><description><title-info>
<book-title>Dune</book-title>
<author><first-name>Frank</first-name><last-name>Herbert</last-name></author>
<sequence name="Dune Saga" number="3"/>
<lang>en</lang>
</title-info></description></FictionBook>`))
	s.Require().NoError(err)
	s.Equal("Dune Saga", m.Series)
	s.InDelta(3.0, m.SeriesNumber, 0.0001)
}

func (s *fb2Suite) TestNoCover() {
	m := s.readMetadata(s.fixture("test_nocover.fb2"))

	s.Equal("No Cover Book", m.Title)
	s.Equal([]string{"TestNick"}, m.Authors)
	s.Equal("en", m.Language)
	s.Empty(m.Annotation)
	s.Empty(m.Series)
	s.Nil(s.readCover(s.fixture("test_nocover.fb2")))
}

// countingReader counts the bytes handed to the consumer.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)

	return n, err //nolint:wrapcheck // transparent passthrough
}

func (s *fb2Suite) TestHeaderIdentifiers() {
	const doc = `<FictionBook><description><title-info><book-title>T</book-title></title-info>` +
		`<publish-info><isbn> 978-1 </isbn><year>1999</year></publish-info></description></FictionBook>`

	m, err := parseFB2Header(strings.NewReader(doc))
	s.Require().NoError(err)
	s.Equal([]Identifier{{Type: IdentifierISBN, Value: "978-1"}}, m.Identifiers)
	s.Equal(1999, m.Year)
}

func (s *fb2Suite) TestHeaderNoAnnotation() {
	m, err := parseFB2Header(strings.NewReader(
		`<FictionBook><description><title-info><book-title>T</book-title></title-info></description></FictionBook>`,
	))
	s.Require().NoError(err)
	s.Equal("T", m.Title)
	s.Empty(m.Annotation)
}

func (s *fb2Suite) TestHeaderMalformed() {
	_, err := parseFB2Header(strings.NewReader(
		`<FictionBook><description><title-info><book-title>T</title-info></description></FictionBook>`,
	))
	s.Require().Error(err)
}

func (s *fb2Suite) TestHeaderMissing() {
	_, err := parseFB2Header(strings.NewReader(`<FictionBook><body/></FictionBook>`))
	s.Require().Error(err)
}

func (s *fb2Suite) TestHeaderStopsAtEndOfDescription() {
	const binSize = 8 << 20
	doc := `<?xml version="1.0" encoding="UTF-8"?><FictionBook><description><title-info>` +
		`<book-title>Big</book-title></title-info></description>` +
		`<binary id="c" content-type="image/png">` + strings.Repeat("A", binSize) + `</binary></FictionBook>`

	cr := &countingReader{r: strings.NewReader(doc)}
	m, err := parseFB2Header(cr)
	s.Require().NoError(err)
	s.Equal("Big", m.Title)
	s.Less(cr.n, int64(binSize/8), "must not read the <binary> block (read %d of %d)", cr.n, len(doc))
}

func (s *fb2Suite) TestTraits() {
	s.Equal(Traits{Rank: 3, Describes: true}, fb2Parser{}.Traits())
}

// TestMetadataZipMatchesPlain proves the .fb2.zip wrapper yields the same
// metadata as the bare FB2 it wraps.
func (s *fb2Suite) TestMetadataZipMatchesPlain() {
	s.Equal(s.readMetadata(s.fixture("test.fb2")), s.readMetadata(s.fixture("test.fb2.zip")))
}

func (s *fb2Suite) TestSourceMetadataLazyZipEntry() {
	zr, err := zip.OpenReader(s.fixture("entries.zip"))
	s.Require().NoError(err)
	defer func() { _ = zr.Close() }()

	var entry *zip.File
	for _, f := range zr.File {
		if f.Name == "big.fb2" {
			entry = f
		}
	}
	s.Require().NotNil(entry)

	src, closer := OpenZipEntry(entry, MaxEntryBytes)
	defer func() { _ = closer.Close() }()

	m, err := fb2Parser{}.Metadata(context.Background(), src)
	s.Require().NoError(err)
	s.NotEmpty(m.Title)
	entryReader, ok := src.R.(*lazyEntry)
	s.Require().True(ok)
	s.Less(entryReader.inflated(), int64(64<<10))
}

// TestCoverZipMatchesPlain proves the .fb2.zip wrapper yields the same cover as
// the bare FB2 it wraps, and that the cover is a decodable image.
func (s *fb2Suite) TestCoverZipMatchesPlain() {
	plain := s.readCover(s.fixture("test.fb2"))
	s.Require().NotEmpty(plain)
	s.True(strings.HasPrefix(http.DetectContentType(plain), "image/"))
	s.Equal(plain, s.readCover(s.fixture("test.fb2.zip")))
}

func (s *fb2Suite) TestSourceCoverNone() {
	src := s.open(s.fixture("test_nocover.fb2"))

	got, err := fb2Parser{}.Cover(context.Background(), src)
	s.Require().NoError(err)
	s.Nil(got)
}

func (s *fb2Suite) TestSourceCoverBadBase64() {
	src := s.open(s.fixture("badcover.fb2"))

	got, err := fb2Parser{}.Cover(context.Background(), src)
	s.Require().Error(err)
	s.Nil(got)
}

func (s *fb2Suite) TestSourceCoverNotAnImage() {
	src := s.open(s.fixture("oddcover.fb2"))

	got, err := fb2Parser{}.Cover(context.Background(), src)
	s.Require().NoError(err)
	s.NotEmpty(got)
	s.False(strings.HasPrefix(http.DetectContentType(got), "image/"))
}

func (s *fb2Suite) TestSourceMetadataCorrupt() {
	src := s.open(s.fixture("corrupt.fb2"))

	_, err := fb2Parser{}.Metadata(context.Background(), src)
	s.Require().Error(err)
}

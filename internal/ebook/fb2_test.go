package ebook

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

type fb2Suite struct {
	baseSuite
}

// TestParseBoundedMemory guards against loading the whole file into memory: a
// large FB2 (here a ~32 MB body, comfortably under the maxArchiveTextBytes read
// cap) must be streamed, allocating far less than its size. The body text is not
// part of the metadata, so a streaming parser discards it as it scans for
// <description>.
func (s *fb2Suite) TestParseBoundedMemory() {
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

	m, peak := s.parsePeakHeapBytes(path)
	s.Equal("Huge FB2", m.Title)
	s.Lessf(peak, uint64(bodySize/2),
		"parsing a ~%d-byte FB2 held %d peak heap bytes; it must stream, not buffer the whole file",
		bodySize, peak)
}

// TestParseRejectsOversizedFile guards the maxArchiveTextBytes read cap on the
// plain .fb2 path: a file larger than the cap (e.g. a pathological embedded
// base64 cover) is rejected rather than buffered whole, matching the .fb2.zip
// path. The oversized file is truncated by the LimitReader before its closing
// tag, so the decode fails.
func (s *fb2Suite) TestParseRejectsOversizedFile() {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<FictionBook><description><title-info>`)
	b.WriteString(`<book-title>Oversized</book-title></title-info></description><body>`)
	const line = "<p>The quick brown fox jumps over the lazy dog.</p>\n"
	for range int(maxArchiveTextBytes)/len(line) + 1 {
		b.WriteString(line)
	}
	b.WriteString(`</body></FictionBook>`)

	path := filepath.Join(s.T().TempDir(), "oversized.fb2")
	s.Require().NoError(os.WriteFile(path, []byte(b.String()), 0o600))

	_, err := s.d.Parse(context.Background(), s.log, path)
	s.Require().Error(err)
}

func (s *fb2Suite) TestParse() {
	m, err := s.d.Parse(context.Background(), s.log, s.fixture("test.fb2"))
	s.Require().NoError(err)

	s.Equal("Test FB2 Book", m.Title)
	s.Equal([]string{"Ivan P. Testov"}, m.Authors)
	s.Equal([]string{"sf_history", "adventure"}, m.Genres) // raw flibusta codes, like INPX
	s.Equal("<p>An annotation for testing.</p>", m.Annotation)
	s.Equal("ru", m.Language)
	s.Equal("FB2 Series", m.Series)
	s.NotEmpty(m.Cover, "expected cover bytes")
}

func (s *fb2Suite) TestParseZip() {
	m, err := s.d.Parse(context.Background(), s.log, s.fixture("test.fb2.zip"))
	s.Require().NoError(err)

	s.Equal("Test FB2 Book", m.Title)
	s.Equal([]string{"Ivan P. Testov"}, m.Authors)
	s.Equal("ru", m.Language)
	s.Equal("FB2 Series", m.Series)
}

func (s *fb2Suite) TestParseWin1251() {
	m, err := s.d.Parse(context.Background(), s.log, s.fixture("test_win1251.fb2"))
	s.Require().NoError(err)

	s.Equal("Проверка", m.Title)
	s.Equal([]string{"Иван Тестов"}, m.Authors)
	s.Equal("ru", m.Language)
}

func (s *fb2Suite) TestParseEntities() {
	m, err := s.d.Parse(context.Background(), s.log, s.fixture("test_entities.fb2"))
	s.Require().NoError(err)

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
	m, err := parseFB2XML([]byte(`<?xml version="1.0" encoding="UTF-8"?>
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

func (s *fb2Suite) TestParseNoCover() {
	m, err := s.d.Parse(context.Background(), s.log, s.fixture("test_nocover.fb2"))
	s.Require().NoError(err)

	s.Equal("No Cover Book", m.Title)
	s.Equal([]string{"TestNick"}, m.Authors)
	s.Equal("en", m.Language)
	s.Empty(m.Cover)
	s.Empty(m.Annotation)
	s.Empty(m.Series)
}

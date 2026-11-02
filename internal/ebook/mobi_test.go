package ebook

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
)

type mobiSuite struct {
	baseSuite
}

// TestCoverViaFirstImageIndex covers M6: EXTH 201 (CoverOffset) is relative to
// the MOBI header's FirstImageIndex. When non-image resources (fonts, indexes)
// precede images, FirstImageIndex skips them and the offset selects the correct
// image. Also covers M5: entity-encoded titles are unescaped.
func (s *mobiSuite) TestCoverViaFirstImageIndex() {
	rec0 := buildRec0("KF8&#x2019;Book", []exthRecord{
		{typ: exthAuthor, data: []byte("Kay Eff")},
		{typ: exthCoverOffset, data: u32(1)}, // cover is the second image
	}, withFirstImageIndex(2)) // images start at record 2
	font := append([]byte("FONT"), make([]byte, 36)...)                   // non-image at record 1
	thumbnail := append([]byte{0x89, 'P', 'N', 'G'}, make([]byte, 40)...) // image at record 2
	cover := append([]byte{0xFF, 0xD8, 0xFF}, make([]byte, 60)...)        // image at record 3
	path := s.writeMOBIFixture("azw3", rec0, font, thumbnail, cover)

	got := s.readCover(path)
	s.NotEmpty(got, "must select cover via FirstImageIndex + CoverOffset")
	s.Len(got, len(cover), "must return record 3 (JPEG cover), not record 2 (PNG thumbnail)")
	s.Equal("KF8\u2019Book", s.readMetadata(path).Title, "title entities must be unescaped")
}

// TestCoverIsFirstImage validates the simple case: CoverOffset=0 → the first
// image record is the cover.
func (s *mobiSuite) TestCoverIsFirstImage() {
	rec0 := buildRec0("Simple Cover", []exthRecord{
		{typ: exthCoverOffset, data: u32(0)},
	}, withFirstImageIndex(1)) // image is record 1
	jpeg := append([]byte{0xFF, 0xD8, 0xFF}, make([]byte, 40)...)
	path := s.writeMOBIFixture("mobi", rec0, jpeg)

	s.NotEmpty(s.readCover(path))
}

// TestCoverFallbackNoFirstImageIndex validates that when FirstImageIndex is
// 0xFFFFFFFF (unset), the parser falls back to scanning for the first image.
func (s *mobiSuite) TestCoverFallbackNoFirstImageIndex() {
	rec0 := buildRec0("Fallback", []exthRecord{
		{typ: exthCoverOffset, data: u32(0)},
	}) // no withFirstImageIndex → defaults to 0xFFFFFFFF
	jpeg := append([]byte{0xFF, 0xD8, 0xFF}, make([]byte, 40)...)
	path := s.writeMOBIFixture("mobi", rec0, jpeg)

	s.NotEmpty(s.readCover(path), "fallback scan must find the image")
}

// TestUpdatedTitlePreferredOverHeader covers M5: EXTH 503 (the clean, full title)
// wins over an entity-encoded / truncated record-0 header title.
func (s *mobiSuite) TestUpdatedTitlePreferredOverHeader() {
	rec0 := buildRec0("Header&#x2019;Bad", []exthRecord{
		{typ: exthAuthor, data: []byte("Auth")},
		{typ: exthUpdatedTitle, data: []byte("Clean Updated Title")},
	})
	path := s.writeMOBIFixture("mobi", rec0)

	m := s.readMetadata(path)
	s.Equal("Clean Updated Title", m.Title)
}

// TestHeaderTitleEntitiesDecodedWithoutEXTH covers the EXTH-absent half of M5: a
// MOBI with no EXTH block must still have its record-0 header title entity-decoded.
// Regression — the decode previously sat after an early return taken when EXTH was
// missing, so an entity-encoded header title was served raw (e.g. "Death&#x2019;s
// End" instead of "Death's End").
func (s *mobiSuite) TestHeaderTitleEntitiesDecodedWithoutEXTH() {
	rec0 := buildRec0("Death&#x2019;s End", nil, withoutEXTH())
	path := s.writeMOBIFixture("mobi", rec0)

	m := s.readMetadata(path)
	s.Equal("Death’s End", m.Title,
		"header title entities must be decoded even when the file has no EXTH block")
}

type exthRecord struct {
	typ  int
	data []byte
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

// rec0Option configures optional MOBI header fields in buildRec0.
type rec0Option func(rec0 []byte)

// withFirstImageIndex sets the MOBI header's FirstImageIndex field.
// Without this option, the field defaults to 0xFFFFFFFF (not set).
func withFirstImageIndex(idx uint32) rec0Option {
	return func(rec0 []byte) {
		// MOBI header offset 92 = PalmDoc(16) + 92 = rec0[108:112]
		binary.BigEndian.PutUint32(rec0[108:112], idx)
	}
}

// withoutEXTH clears the MOBI header's EXTH-present flag so the parser takes the
// EXTH-absent path: it keeps the record-0 header title without consulting an EXTH
// block. buildRec0 still appends an (inert) EXTH body, but the cleared flag makes
// readEXTH return before it is read.
func withoutEXTH() rec0Option {
	return func(rec0 []byte) {
		binary.BigEndian.PutUint32(rec0[128:132], 0)
	}
}

// buildRec0 assembles a MOBI record-0 (PalmDoc + MOBI header + EXTH + title)
// with the given EXTH records, UTF-8 encoded. FirstImageIndex defaults to
// 0xFFFFFFFF (unset); use withFirstImageIndex to override.
func buildRec0(title string, exth []exthRecord, opts ...rec0Option) []byte {
	const mobiHeaderLen = 116
	rec0 := make([]byte, 16+mobiHeaderLen)
	copy(rec0[16:20], "MOBI")
	binary.BigEndian.PutUint32(rec0[20:24], mobiHeaderLen)
	binary.BigEndian.PutUint32(rec0[28:32], 65001)  // UTF-8
	binary.BigEndian.PutUint32(rec0[128:132], 0x40) // EXTH-present flag

	// Default FirstImageIndex to 0xFFFFFFFF (not set).
	binary.BigEndian.PutUint32(rec0[108:112], 0xFFFFFFFF)

	for _, opt := range opts {
		opt(rec0)
	}

	body := buildEXTHBody(exth)
	titleOffset := len(rec0) + len(body)
	binary.BigEndian.PutUint32(rec0[84:88], uint32(titleOffset))
	binary.BigEndian.PutUint32(rec0[88:92], uint32(len(title)))

	rec0 = append(rec0, body...)

	return append(rec0, []byte(title)...)
}

func buildEXTHBody(records []exthRecord) []byte {
	var body []byte
	for _, r := range records {
		rec := make([]byte, 8)
		binary.BigEndian.PutUint32(rec[0:4], uint32(r.typ))
		binary.BigEndian.PutUint32(rec[4:8], uint32(8+len(r.data)))
		body = append(body, rec...)
		body = append(body, r.data...)
	}
	header := make([]byte, 12, 12+len(body))
	copy(header[0:4], "EXTH")
	binary.BigEndian.PutUint32(header[4:8], uint32(12+len(body)))
	binary.BigEndian.PutUint32(header[8:12], uint32(len(records)))

	return append(header, body...)
}

// writeMOBIFixture writes a PDB (header + record table + records) to a temp file
// with the given extension and returns its path.
func (s *mobiSuite) writeMOBIFixture(ext string, records ...[]byte) string {
	header := make([]byte, 78)
	binary.BigEndian.PutUint16(header[76:78], uint16(len(records)))

	recTable := make([]byte, len(records)*8)
	offset := 78 + len(recTable)
	for i, r := range records {
		binary.BigEndian.PutUint32(recTable[i*8:i*8+4], uint32(offset))
		offset += len(r)
	}

	buf := make([]byte, 0, offset)
	buf = append(buf, header...)
	buf = append(buf, recTable...)
	for _, r := range records {
		buf = append(buf, r...)
	}
	path := filepath.Join(s.T().TempDir(), "fixture."+ext)
	s.Require().NoError(os.WriteFile(path, buf, 0o600))

	return path
}

// TestBoundedMemory guards against the OOM that killed the indexer in
// memory-limited containers (Docker): reading a large MOBI must not load the
// whole file into memory. Metadata only needs record 0 and the cover image
// record, so a ~64 MB book-text record must not be allocated.
func (s *mobiSuite) TestBoundedMemory() {
	const fillerSize = 64 << 20 // stand-in for the book's text records
	rec0 := buildRec0("Huge Book", []exthRecord{
		{typ: exthCoverOffset, data: u32(0)},
	}, withFirstImageIndex(2)) // cover is record 2
	filler := make([]byte, fillerSize)                             // non-image "book text" at record 1
	cover := append([]byte{0xFF, 0xD8, 0xFF}, make([]byte, 60)...) // image at record 2
	path := s.writeMOBIFixture("mobi", rec0, filler, cover)

	var got []byte
	peak := s.peakHeapBytes(func() {
		_ = s.readMetadata(path)
		got = s.readCover(path)
	})
	s.NotEmpty(got, "must still extract the cover")
	s.Lessf(peak, uint64(fillerSize/2),
		"reading a %d-byte MOBI held %d peak heap bytes; it must not read the whole file",
		fillerSize, peak)
}

func (s *mobiSuite) TestMetadataMOBI() {
	m := s.readMetadata(s.fixture("test.mobi"))

	s.Equal("Test MOBI Book", m.Title)
	s.Equal([]string{"Alex Writer"}, m.Authors)
	s.Equal("A MOBI description.", m.Annotation)
	s.Nil(m.Cover, "Metadata never decodes the cover")
	s.NotEmpty(s.readCover(s.fixture("test.mobi")), "expected cover bytes")
}

func (s *mobiSuite) TestMetadataAZW3() {
	m := s.readMetadata(s.fixture("test.azw3"))

	s.Equal("Test AZW3 Book", m.Title)
	s.Equal([]string{"Kate Kindler"}, m.Authors)
	s.Equal("An AZW3 description.", m.Annotation)
	s.Nil(m.Cover, "Metadata never decodes the cover")
	cover := s.readCover(s.fixture("test.azw3"))
	s.NotEmpty(cover, "expected cover bytes")
	// The fixture has a decoy image before the real cover; resolving the cover
	// via FirstImageIndex + CoverOffset must pick the cover, not the decoy a
	// fallback scan would return first.
	s.Contains(string(cover), "COVERIMG", "must select the cover via the header offset")
	s.NotContains(string(cover), "DECOY", "must not fall back to the first image")
}

func (s *mobiSuite) TestMetadataCP1252() {
	m := s.readMetadata(s.fixture("test_cp1252.mobi"))

	s.Equal("Résumé", m.Title)
	s.Equal([]string{"François Müller"}, m.Authors)
	s.Equal("Découverte café.", m.Annotation)
}

// TestReadTitleRejectsOutOfRangeOffset guards against a panic when the title
// offset stored in rec0[84:88] has its high bit set (0x80000000). On a 32-bit
// int build, casting that uint32 to int yields a negative value that defeats
// the bounds guard and panics on the slice expression. The fix uses uint64 math
// so the comparison is always unsigned regardless of int width.
func (s *mobiSuite) TestReadTitleRejectsOutOfRangeOffset() {
	// On 64-bit hosts int(uint32(0x80000000)) is positive (2147483648), so the
	// bounds guard alone would pass vacuously; it is the uint64 arithmetic in
	// readTitle that makes this test meaningful on 32-bit where that cast wraps negative.
	rec0 := craftMOBIRecord0WithTitleOffset(0x80000000)
	mf := &mobiFile{rec0: rec0}
	s.NotPanics(func() { _ = mf.readTitle() })
}

// craftMOBIRecord0WithTitleOffset returns the smallest valid rec0 whose
// titleOffset field (bytes 84:88) is set to the given value. The MOBI magic
// and a minimum-size buffer are included so openMOBI preconditions are met if
// readTitle is ever called through the full parse path.
func craftMOBIRecord0WithTitleOffset(offset uint32) []byte {
	// rec0 must be at least mobiRec0MinSize (132) bytes for readEXTH's field
	// accesses not to panic; use exactly that minimum.
	rec0 := make([]byte, mobiRec0MinSize)
	copy(rec0[16:20], mobiMagic)
	binary.BigEndian.PutUint32(rec0[84:88], offset)
	binary.BigEndian.PutUint32(rec0[88:92], 10) // non-zero titleLength
	return rec0
}

func (s *mobiSuite) TestApplyEXTHPublishing() {
	rec0 := buildRec0("EXTH Publishing Book", []exthRecord{
		{typ: exthAuthor, data: []byte("Kay Eff")},
		{typ: exthPublisher, data: []byte("  Super Publisher  ")},
		{typ: exthPublishDate, data: []byte("2026-06-08")},
		{typ: exthISBN, data: []byte("978-3-16-148410-0")},
		{typ: exthLanguage, data: []byte("en")},
	})
	path := s.writeMOBIFixture("mobi", rec0)

	m := s.readMetadata(path)
	s.Equal("Super Publisher", m.Publisher)
	s.Equal(2026, m.Year)
	s.Len(m.Identifiers, 1)
	s.Equal(Identifier{Type: IdentifierISBN, Value: "978-3-16-148410-0"}, m.Identifiers[0])
	s.Equal("en", m.Language)
}

func (s *mobiSuite) TestTraits() {
	s.Equal(Traits{Rank: 2, Describes: true}, mobiParser{}.Traits())
}

func (s *mobiSuite) TestSourceMetadataZipEntry() {
	zr, err := zip.OpenReader(s.fixture("entries.zip"))
	s.Require().NoError(err)
	defer func() { _ = zr.Close() }()

	var entry *zip.File
	for _, f := range zr.File {
		if f.Name == "book.mobi" {
			entry = f
		}
	}
	s.Require().NotNil(entry)

	src, closer := OpenZipEntry(entry, MaxEntryBytes)
	defer func() { _ = closer.Close() }()

	m, err := mobiParser{}.Metadata(context.Background(), src)
	s.Require().NoError(err)
	s.NotEmpty(m.Annotation)
}

func (s *mobiSuite) TestSourceCoverNone() {
	rec0 := buildRec0("No Cover", nil)
	path := s.writeMOBIFixture("mobi", rec0)
	src := s.open(path)

	got, err := mobiParser{}.Cover(context.Background(), src)
	s.Require().NoError(err)
	s.Nil(got)
}

func (s *mobiSuite) TestSourceCoverOffsetOutsideRecords() {
	rec0 := buildRec0("Far", []exthRecord{{typ: exthCoverOffset, data: []byte{0, 0, 0xFF, 0xFF}}})
	path := s.writeMOBIFixture("mobi", rec0)
	src := s.open(path)

	got, err := mobiParser{}.Cover(context.Background(), src)
	s.Require().NoError(err)
	s.Nil(got)
}

func (s *mobiSuite) TestSourceMalformed() {
	src := Source{Name: "bad.mobi", R: bytes.NewReader([]byte("short")), Size: 5}

	_, err := mobiParser{}.Metadata(context.Background(), src)
	s.Require().ErrorIs(err, ErrMalformed)
	_, err = mobiParser{}.Cover(context.Background(), src)
	s.Require().ErrorIs(err, ErrMalformed)
}

func (s *mobiSuite) TestSourceCanceled() {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	src := s.open(s.fixture("test.mobi"))

	_, err := mobiParser{}.Metadata(ctx, src)
	s.Require().ErrorIs(err, context.Canceled)
	_, err = mobiParser{}.Cover(ctx, src)
	s.Require().ErrorIs(err, context.Canceled)
}

// TestSourceCoverOffsetOutsideRecordsWithImages: an EXTH 201 that points past
// the records yields no cover even though an image record exists to fall back on.
func (s *mobiSuite) TestSourceCoverOffsetOutsideRecordsWithImages() {
	rec0 := buildRec0("Far", []exthRecord{{typ: exthCoverOffset, data: u32(0xFFFF)}}, withFirstImageIndex(1))
	image := append([]byte{0xFF, 0xD8, 0xFF}, make([]byte, 20)...)
	path := s.writeMOBIFixture("mobi", rec0, image)
	src := s.open(path)

	got, err := mobiParser{}.Cover(context.Background(), src)
	s.Require().NoError(err)
	s.Nil(got)
}

// TestSourceCoverJunkFirstImageIndexScans: a FirstImageIndex outside the
// records is a broken base, not a broken cover offset, so the first-image scan
// still finds the cover.
func (s *mobiSuite) TestSourceCoverJunkFirstImageIndexScans() {
	rec0 := buildRec0("Junk Base", []exthRecord{{typ: exthCoverOffset, data: u32(0)}}, withFirstImageIndex(0x7FFFFFFF))
	image := append([]byte{0xFF, 0xD8, 0xFF}, make([]byte, 20)...)
	path := s.writeMOBIFixture("mobi", rec0, image)

	got, err := mobiParser{}.Cover(context.Background(), s.open(path))
	s.Require().NoError(err)
	s.Equal(image, got)
}

// TestRecord0ReadIsCapped: a one-record MOBI sizes record 0 to the end of the
// source, and an archive entry's size is whatever the archive declares, so a
// lying 8 GiB declaration must not become an 8 GiB allocation.
func (s *mobiSuite) TestRecord0ReadIsCapped() {
	data, err := os.ReadFile(s.writeMOBIFixture("mobi", buildRec0("One Record", nil)))
	s.Require().NoError(err)
	src := Source{Name: "liar.mobi", R: bytes.NewReader(data), Size: 8 << 30}

	peak := s.peakHeapBytes(func() {
		_, err = mobiParser{}.Metadata(context.Background(), src)
	})
	s.Require().ErrorIs(err, ErrMalformed)
	s.Less(peak, uint64(4*mobiMaxRec0))
}

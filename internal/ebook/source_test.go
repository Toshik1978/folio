package ebook

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"errors"
	"hash/crc32"
	"io"
	"io/fs"
	"path/filepath"
	"sync"
)

type sourceSuite struct {
	baseSuite

	zr *zip.ReadCloser
}

func (s *sourceSuite) SetupTest() {
	s.baseSuite.SetupTest()
	zr, err := zip.OpenReader(s.fixture("entries.zip"))
	s.Require().NoError(err)
	s.zr = zr
}

func (s *sourceSuite) TearDownTest() {
	s.Require().NoError(s.zr.Close())
}

// entry returns the named file of testdata/entries.zip.
func (s *sourceSuite) entry(name string) *zip.File {
	for _, f := range s.zr.File {
		if f.Name == name {
			return f
		}
	}
	s.FailNow("entry not found", name)

	return nil
}

// lazy opens a zip entry and returns its Source plus the concrete reader.
func (s *sourceSuite) lazy(f *zip.File, limit int64) (Source, *lazyEntry) {
	src, closer := OpenZipEntry(f, limit)
	s.T().Cleanup(func() { s.NoError(closer.Close()) })
	le, ok := src.R.(*lazyEntry)
	s.Require().True(ok)

	return src, le
}

func (s *sourceSuite) TestZipEntryMetadata() {
	f := s.entry("big.fb2")
	src, _ := s.lazy(f, MaxEntryBytes)
	s.Equal("big.fb2", src.Name)
	s.Equal(int64(f.UncompressedSize64), src.Size)
}

func (s *sourceSuite) TestHeaderReadInflatesOnlyPrefix() {
	src, le := s.lazy(s.entry("big.fb2"), MaxEntryBytes)
	s.Zero(le.inflated(), "nothing is inflated before the first read")

	p := make([]byte, 4<<10)
	n, err := src.R.ReadAt(p, 0)
	s.Require().NoError(err)
	s.Equal(len(p), n)
	s.Contains(string(p), "Big FB2 Book")
	s.Less(le.inflated(), int64(64<<10))
}

func (s *sourceSuite) TestBackwardReadServedFromBuffer() {
	f := s.entry("big.fb2")
	src, le := s.lazy(f, MaxEntryBytes)

	whole := make([]byte, 8<<10)
	_, err := src.R.ReadAt(whole, 0)
	s.Require().NoError(err)
	s.Equal(1, le.opens)
	before := le.inflated()

	p := make([]byte, 100)
	n, err := src.R.ReadAt(p, 200)
	s.Require().NoError(err)
	s.Equal(100, n)
	s.Equal(whole[200:300], p)
	s.Equal(1, le.opens, "a backward read must not reopen the decompressor")
	s.Equal(before, le.inflated())
}

func (s *sourceSuite) TestReadToEndMatchesEntry() {
	for _, name := range []string{"big.fb2", "book.epub", "book.mobi", "book.pdf"} {
		f := s.entry(name)
		rc, err := f.Open()
		s.Require().NoError(err)
		want, err := io.ReadAll(rc)
		s.Require().NoError(err)
		s.Require().NoError(rc.Close())

		src, _ := s.lazy(f, MaxEntryBytes)
		got, err := io.ReadAll(io.NewSectionReader(src.R, 0, src.Size))
		s.Require().NoError(err, name)
		s.Equal(want, got, name)
	}
}

func (s *sourceSuite) TestOffsetsPastSizeAreEOF() {
	src, _ := s.lazy(s.entry("book.pdf"), MaxEntryBytes)
	p := make([]byte, 10)

	n, err := src.R.ReadAt(p, src.Size)
	s.Zero(n)
	s.Require().ErrorIs(err, io.EOF)

	n, err = src.R.ReadAt(p, src.Size-4)
	s.Equal(4, n)
	s.ErrorIs(err, io.EOF)
}

func (s *sourceSuite) TestConcurrentReadAt() {
	f := s.entry("big.fb2")
	src, _ := s.lazy(f, MaxEntryBytes)
	rc, err := f.Open()
	s.Require().NoError(err)
	want, err := io.ReadAll(rc)
	s.Require().NoError(err)
	s.Require().NoError(rc.Close())

	const workers = 8
	var wg sync.WaitGroup
	errs := make([]error, workers)
	for w := range workers {
		wg.Go(func() {
			p := make([]byte, 4096)
			for off := int64(w * 1000); off < 256<<10; off += 7919 {
				n, err := src.R.ReadAt(p, off)
				if err != nil {
					errs[w] = err
					return
				}
				if !bytes.Equal(p[:n], want[off:off+int64(n)]) {
					errs[w] = errors.New("data mismatch")
					return
				}
			}
		})
	}
	wg.Wait()
	for _, err := range errs {
		s.NoError(err)
	}
}

func (s *sourceSuite) TestLimitExceededIsTooLarge() {
	const limit = 1024
	src, le := s.lazy(s.entry("book.epub"), limit)

	_, err := io.ReadAll(io.NewSectionReader(src.R, 0, src.Size))
	s.Require().ErrorIs(err, ErrTooLarge)
	s.LessOrEqual(le.inflated(), int64(limit+inflateChunk))
}

func (s *sourceSuite) TestTruncatedEntryIsMalformed() {
	content := bytes.Repeat([]byte("truncated entry content "), 200)
	f := rawZipEntry(s, content, uint64(len(content))+500, crc32.ChecksumIEEE(content))
	src, _ := s.lazy(f, MaxEntryBytes)

	_, err := io.ReadAll(io.NewSectionReader(src.R, 0, src.Size))
	s.Require().ErrorIs(err, ErrMalformed)
	s.NotErrorIs(err, ErrSourceIO)
}

func (s *sourceSuite) TestChecksumMismatchIsMalformed() {
	content := bytes.Repeat([]byte("checksum entry content "), 200)
	f := rawZipEntry(s, content, uint64(len(content)), crc32.ChecksumIEEE(content)+1)
	src, _ := s.lazy(f, MaxEntryBytes)

	_, err := io.ReadAll(io.NewSectionReader(src.R, 0, src.Size))
	s.ErrorIs(err, ErrMalformed)
}

func (s *sourceSuite) TestUnderlyingIOErrorStaysSourceIO() {
	// Incompressible content keeps the entry body well clear of the zip
	// directory, which flakyReaderAt still serves.
	content := make([]byte, 4096)
	x := uint32(13579)
	for i := range content {
		x = x*1664525 + 1013904223
		content[i] = byte(x >> 24)
	}
	data := rawZipBytes(s, content, uint64(len(content)), crc32.ChecksumIEEE(content))
	zr, err := zip.NewReader(TagIO(&flakyReaderAt{data: data}), int64(len(data)))
	s.Require().NoError(err)
	src, _ := s.lazy(zr.File[0], MaxEntryBytes)

	_, err = src.R.ReadAt(make([]byte, 16), 0)
	s.Require().ErrorIs(err, ErrSourceIO)
	s.NotErrorIs(err, ErrMalformed)
}

func (s *sourceSuite) TestReadAfterCloseFails() {
	src, closer := OpenZipEntry(s.entry("book.pdf"), MaxEntryBytes)
	_, err := src.R.ReadAt(make([]byte, 8), 0)
	s.Require().NoError(err)
	s.Require().NoError(closer.Close())

	le, ok := src.R.(*lazyEntry)
	s.Require().True(ok)
	s.Zero(le.inflated())
	_, err = src.R.ReadAt(make([]byte, 8), 0)
	s.ErrorIs(err, fs.ErrClosed)
}

func (s *sourceSuite) TestTagIOMarksReadErrors() {
	cause := errors.New("disk on fire")
	r := TagIO(failingReaderAt{err: cause})

	_, err := r.ReadAt(make([]byte, 4), 0)
	s.Require().ErrorIs(err, ErrSourceIO)
	s.Require().ErrorIs(err, cause)

	_, err = TagIO(failingReaderAt{err: io.EOF}).ReadAt(make([]byte, 4), 0)
	s.Equal(io.EOF, err, "EOF passes through unwrapped")
}

func (s *sourceSuite) TestOpenFile() {
	src, closer, err := OpenFile(s.fixture("test.pdf"))
	s.Require().NoError(err)
	defer func() { s.NoError(closer.Close()) }()

	s.Equal("test.pdf", src.Name)
	s.Positive(src.Size)
	p := make([]byte, 5)
	_, err = src.R.ReadAt(p, 0)
	s.Require().NoError(err)
	s.Equal("%PDF-", string(p))
}

func (s *sourceSuite) TestOpenFileMissing() {
	_, closer, err := OpenFile(filepath.Join(s.T().TempDir(), "missing.epub"))
	s.Nil(closer)
	s.Require().ErrorIs(err, fs.ErrNotExist)
	s.NotErrorIs(err, ErrSourceIO)
}

// failingReaderAt fails every read with err.
type failingReaderAt struct{ err error }

func (r failingReaderAt) ReadAt([]byte, int64) (int, error) { return 0, r.err }

// flakyReaderAt serves the zip directory at the end of the archive but fails
// reads of the entry body near its start.
type flakyReaderAt struct{ data []byte }

func (r *flakyReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 1024 {
		return 0, errors.New("sector unreadable")
	}
	if off >= int64(len(r.data)) {
		return 0, io.EOF
	}
	n := copy(p, r.data[off:])
	if n < len(p) {
		return n, io.EOF
	}

	return n, nil
}

// rawZipBytes builds a one-entry deflated archive whose header declares size
// and crc regardless of content, so tests can fake truncation and corruption.
func rawZipBytes(s *sourceSuite, content []byte, size uint64, crc uint32) []byte {
	var comp bytes.Buffer
	fw, err := flate.NewWriter(&comp, flate.DefaultCompression)
	s.Require().NoError(err)
	_, err = fw.Write(content)
	s.Require().NoError(err)
	s.Require().NoError(fw.Close())

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateRaw(&zip.FileHeader{
		Name:               "dir/entry.fb2",
		Method:             zip.Deflate,
		CRC32:              crc,
		CompressedSize64:   uint64(comp.Len()),
		UncompressedSize64: size,
	})
	s.Require().NoError(err)
	_, err = w.Write(comp.Bytes())
	s.Require().NoError(err)
	s.Require().NoError(zw.Close())

	return buf.Bytes()
}

func rawZipEntry(s *sourceSuite, content []byte, size uint64, crc uint32) *zip.File {
	data := rawZipBytes(s, content, size, crc)
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	s.Require().NoError(err)
	s.Require().Len(zr.File, 1)

	return zr.File[0]
}

package ingest

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ebook"
	"github.com/Toshik1978/folio/internal/libtype"
)

type outcomeSuite struct{ suite.Suite }

// TestClassify covers every row of research.md R6.
func (s *outcomeSuite) TestClassify() {
	tests := []struct {
		name   string
		err    error
		status ReadStatus
		reason ebook.Reason
	}{
		{"malformed", fmt.Errorf("read: %w", ebook.ErrMalformed), ReadBroken, ebook.ReasonCorrupt},
		{"too large", fmt.Errorf("read: %w", ebook.ErrTooLarge), ReadBroken, ebook.ReasonTooLarge},
		{"panic", fmt.Errorf("read: %w", &ebook.PanicError{Value: "boom"}), ReadBroken, ebook.ReasonCrashed},
		{"missing entry", fmt.Errorf("x: %w", errMissingEntry), ReadBroken, ebook.ReasonMissingEntry},
		{"open error", fmt.Errorf("open: %w", fs.ErrNotExist), ReadRetryable, ""},
		{"permission", fmt.Errorf("open: %w", fs.ErrPermission), ReadRetryable, ""},
		{"source io", fmt.Errorf("%w: %w", ebook.ErrSourceIO, errors.New("EIO")), ReadRetryable, ""},
		{"source io beats malformed", fmt.Errorf("%w: %w", ebook.ErrSourceIO, ebook.ErrMalformed), ReadRetryable, ""},
		{"canceled", context.Canceled, ReadRetryable, ""},
		{"deadline", fmt.Errorf("lock: %w", context.DeadlineExceeded), ReadRetryable, ""},
		{"unknown", errors.New("mystery"), ReadRetryable, ""},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			got := classify(tt.err)
			s.Equal(tt.status, got.Status)
			s.Equal(tt.reason, got.Reason)
			s.Equal(tt.err, got.Err)
			s.Equal(tt.err.Error(), got.Detail)
		})
	}
}

func (s *outcomeSuite) TestClassifyTruncatesDetail() {
	got := classify(errors.New(strings.Repeat("я", 200))) // 400 bytes
	s.LessOrEqual(len(got.Detail), 256)
	s.Greater(len(got.Detail), 250)
	s.True(strings.HasPrefix(strings.Repeat("я", 200), got.Detail), "cut on a rune boundary")
}

func (s *outcomeSuite) TestCombine() {
	done := ReadResult{Status: ReadDone, Detail: "done"}
	retry := ReadResult{Status: ReadRetryable, Detail: "retry"}
	top := ReadResult{Status: ReadBroken, Reason: ebook.ReasonCorrupt}
	low := ReadResult{Status: ReadBroken, Reason: ebook.ReasonTooLarge}

	s.Equal(ReadDone, combine(nil).Status)
	s.Equal(done, combine([]ReadResult{top, done, retry}), "a later done beats an earlier failure")
	s.Equal(retry, combine([]ReadResult{top, retry, low}), "retryable beats broken")
	s.Equal(top, combine([]ReadResult{top, low}), "else the top candidate's reason")
}

// countingReaderAt counts the bytes read through it.
type countingReaderAt struct {
	f *os.File
	n atomic.Int64
}

func (c *countingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	c.n.Add(int64(len(p)))

	return c.f.ReadAt(p, off) //nolint:wrapcheck // test pass-through
}

// writeArchive writes a zip of name→content at path, every entry stamped mtime.
func (s *outcomeSuite) writeArchive(path string, mtime time.Time, entries map[string]string) {
	f, err := os.Create(path)
	s.Require().NoError(err)
	zw := zip.NewWriter(f)
	for name, content := range entries {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: mtime})
		s.Require().NoError(err)
		_, err = w.Write([]byte(content))
		s.Require().NoError(err)
	}
	s.Require().NoError(zw.Close())
	s.Require().NoError(f.Close())
}

func (s *outcomeSuite) TestFingerprintPlainFiles() {
	dir := s.T().TempDir()
	lib := dbq.Library{Type: libtype.Folder, Path: dir}
	a := filepath.Join(dir, "a.x")
	s.Require().NoError(os.WriteFile(a, []byte("one"), 0o600))
	s.Require().NoError(os.WriteFile(filepath.Join(dir, "b.x"), []byte("two"), 0o600))
	fa, fb := dbq.BookFile{SourcePath: "a.x"}, dbq.BookFile{SourcePath: "b.x"}
	r := newResolver()
	ac := newArchiveCache()
	defer ac.close()

	base, err := r.fingerprint(lib, []dbq.BookFile{fa}, ac)
	s.Require().NoError(err)
	s.Len(base, 32)
	again, err := r.fingerprint(lib, []dbq.BookFile{fa}, ac)
	s.Require().NoError(err)
	s.Equal(base, again, "stable")

	s.Require().NoError(os.Chtimes(a, time.Now(), time.Now().Add(-time.Hour)))
	moved, err := r.fingerprint(lib, []dbq.BookFile{fa}, ac)
	s.Require().NoError(err)
	s.NotEqual(base, moved, "mtime change")

	added, err := r.fingerprint(lib, []dbq.BookFile{fa, fb}, ac)
	s.Require().NoError(err)
	s.NotEqual(moved, added, "file added to the book")

	_, err = r.fingerprint(lib, []dbq.BookFile{{SourcePath: "gone.x"}}, ac)
	s.Require().ErrorIs(err, fs.ErrNotExist)
	_, err = r.fingerprint(lib, []dbq.BookFile{{SourcePath: "../x"}}, ac)
	s.Require().ErrorIs(err, errPathEscape)
}

func (s *outcomeSuite) TestFingerprintArchiveEntries() {
	dir := s.T().TempDir()
	lib := dbq.Library{Type: libtype.INPX, Path: filepath.Join(dir, "lib.inpx")}
	arch := filepath.Join(dir, "a.zip")
	t0 := time.Date(2024, 1, 2, 3, 4, 6, 0, time.UTC)
	s.writeArchive(arch, t0, map[string]string{"b.x": "hello", "c.x": "other"})
	r := newResolver()
	file := dbq.BookFile{SourcePath: "a.zip/b.x"}

	fp := func(files ...dbq.BookFile) (string, error) {
		ac := newArchiveCache()
		defer ac.close()

		return r.fingerprint(lib, files, ac)
	}
	base, err := fp(file)
	s.Require().NoError(err)
	again, err := fp(file)
	s.Require().NoError(err)
	s.Equal(base, again, "stable")

	s.writeArchive(arch, t0, map[string]string{"b.x": "hello, replaced", "c.x": "other"})
	replaced, err := fp(file)
	s.Require().NoError(err)
	s.NotEqual(base, replaced, "replaced entry")

	s.writeArchive(arch, t0.Add(time.Hour), map[string]string{"b.x": "hello, replaced", "c.x": "other"})
	touched, err := fp(file)
	s.Require().NoError(err)
	s.NotEqual(replaced, touched, "entry mtime change")

	added, err := fp(file, dbq.BookFile{SourcePath: "a.zip/c.x"})
	s.Require().NoError(err)
	s.NotEqual(touched, added, "file added to the book")

	// A repaired copy of the same release (identical entry headers) still
	// changes the fingerprint through the archive's own stat, so a corrupt
	// record is lifted once the archive is replaced.
	s.Require().NoError(os.Chtimes(arch, time.Now(), time.Now().Add(-3*time.Hour)))
	repaired, err := fp(file)
	s.Require().NoError(err)
	s.NotEqual(touched, repaired, "archive replaced, entry headers unchanged")

	// A missing entry falls back to the archive's stat, so replacing the
	// archive changes the fingerprint.
	missing := dbq.BookFile{SourcePath: "a.zip/none.x"}
	m1, err := fp(missing)
	s.Require().NoError(err)
	s.Require().NoError(os.Chtimes(arch, time.Now(), time.Now().Add(-2*time.Hour)))
	m2, err := fp(missing)
	s.Require().NoError(err)
	s.NotEqual(m1, m2, "archive replaced under a missing entry")

	_, err = fp(dbq.BookFile{SourcePath: "nope.zip/b.x"})
	s.Require().ErrorIs(err, fs.ErrNotExist)
}

// TestFingerprintReadsNoEntryData proves a fingerprint touches only the central
// directory: with the archive already indexed it reads no byte at all.
func (s *outcomeSuite) TestFingerprintReadsNoEntryData() {
	dir := s.T().TempDir()
	lib := dbq.Library{Type: libtype.INPX, Path: filepath.Join(dir, "lib.inpx")}
	arch := filepath.Join(dir, "a.zip")
	s.writeArchive(arch, time.Now(), map[string]string{"b.x": strings.Repeat("payload ", 4096)})

	f, err := os.Open(arch)
	s.Require().NoError(err)
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	s.Require().NoError(err)
	cr := &countingReaderAt{f: f}
	zr, err := zip.NewReader(cr, fi.Size())
	s.Require().NoError(err)
	indexed := cr.n.Load()
	s.Positive(indexed)

	ac := newArchiveCache()
	ac.archives[arch] = &cachedArchive{info: fi, entries: map[string]*zip.File{zr.File[0].Name: zr.File[0]}}
	_, err = newResolver().fingerprint(lib, []dbq.BookFile{{SourcePath: "a.zip/b.x"}}, ac)
	s.Require().NoError(err)
	s.Equal(indexed, cr.n.Load(), "fingerprint read entry data")
}

package bookfile

import (
	"archive/zip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/Toshik1978/folio/internal/db/dbq"
)

func TestBookfile(t *testing.T) {
	suite.Run(t, new(zipSuite))
}

type zipSuite struct {
	suite.Suite
}

// writeZip creates archive under dir containing a single entry, returning nothing
// (the path is implied by dir/archive).
func (s *zipSuite) writeZip(dir, archive, inner string, data []byte) {
	f, err := os.Create(filepath.Join(dir, archive))
	s.Require().NoError(err)
	defer func() { _ = f.Close() }()

	zw := zip.NewWriter(f)
	w, err := zw.Create(inner)
	s.Require().NoError(err)
	_, err = w.Write(data)
	s.Require().NoError(err)
	s.Require().NoError(zw.Close())
}

// TestServeFromZip is the happy path: an INPX book streams out of its sibling
// archive with the right body, Content-Type, Content-Disposition, and an accurate
// Content-Length (L9).
func (s *zipSuite) TestServeFromZip() {
	dir := s.T().TempDir()
	body := []byte("FICTIONBOOK CONTENT")
	s.writeZip(dir, "books.zip", "123.fb2", body)

	lib := dbq.Library{Type: "inpx", Path: filepath.Join(dir, "catalog.inpx")}
	file := dbq.BookFile{SourcePath: "books.zip/123.fb2", FileFormat: "fb2"}

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/download", http.NoBody)
	s.Require().NoError(Serve(rec, req, lib, file))

	res := rec.Result()
	s.Equal(http.StatusOK, res.StatusCode)
	s.Equal(body, rec.Body.Bytes())
	s.Equal(strconv.Itoa(len(body)), res.Header.Get("Content-Length"), "Content-Length must match the body")
	s.Equal("application/x-fictionbook+xml", res.Header.Get("Content-Type"))
	s.Contains(res.Header.Get("Content-Disposition"), "123.fb2")
}

// TestServeFromZipRejectsPathEscape proves the withinRoot guard (L9): a stored
// source_path that traverses out of the library root is refused before any
// archive is opened, so no bytes leak.
func (s *zipSuite) TestServeFromZipRejectsPathEscape() {
	dir := s.T().TempDir()
	root := filepath.Join(dir, "lib")
	s.Require().NoError(os.MkdirAll(root, 0o755))
	// A zip sitting outside the library root that the traversal aims at.
	s.writeZip(dir, "secret.zip", "x.fb2", []byte("SECRET-BYTES"))

	lib := dbq.Library{Type: "inpx", Path: filepath.Join(root, "catalog.inpx")}
	file := dbq.BookFile{SourcePath: "../secret.zip/x.fb2", FileFormat: "fb2"}

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/download", http.NoBody)
	err := Serve(rec, req, lib, file)

	s.Require().Error(err)
	s.Equal(http.StatusBadRequest, rec.Result().StatusCode)
	s.NotContains(rec.Body.String(), "SECRET-BYTES", "must not leak archive bytes on an escaping path")
}

func (s *zipSuite) TestContentType() {
	s.Equal("application/epub+zip", ContentType("epub"))
	s.Equal("application/x-fictionbook+xml", ContentType("fb2"))
	s.Equal("application/x-mobipocket-ebook", ContentType("mobi"))
	s.Equal("application/x-mobipocket-ebook", ContentType("azw"))
	s.Equal("application/x-mobipocket-ebook", ContentType("azw3"))
	s.Equal("application/pdf", ContentType("pdf"))
	s.Equal("application/octet-stream", ContentType("unknown"))
}

func (s *zipSuite) TestWithinRootError() {
	s.False(withinRoot("relative/path", "/absolute/path"))
}

func (s *zipSuite) TestContentDispositionStripsBackslashes() {
	// A trailing '\' would escape the closing quote of filename="...", producing
	// a malformed header for legacy clients.
	got := contentDisposition(`evil\name\`)
	s.Contains(got, `filename="evilname"`)
}

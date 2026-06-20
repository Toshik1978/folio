package opds

import (
	"archive/zip"
	"net/http"
	"os"
	"path/filepath"
)

type downloadSuite struct {
	baseSuite
}

func (s *downloadSuite) TestDownloadFromFolder() {
	s.setCreds()
	root := s.T().TempDir()
	s.Require().NoError(os.WriteFile(filepath.Join(root, "book.epub"), []byte("EPUBDATA"), 0o600))

	src := s.seedSource("folder", root)
	id := s.seedBook(src, bookSeed{Title: "Book", Format: "epub", SourcePath: "book.epub"})

	w := s.getAuth("/books/"+itoa(id)+"/files/"+itoa(s.firstFileID(id)), testUser, testPass)
	s.Require().Equal(http.StatusOK, w.Code)
	s.Equal("EPUBDATA", w.Body.String())
	s.Equal("application/epub+zip", w.Header().Get("Content-Type"))
}

func (s *downloadSuite) TestDownloadFromINPXZip() {
	s.setCreds()
	dir := s.T().TempDir()
	s.writeZip(filepath.Join(dir, "fb.zip"), "book1.fb2", "FB2DATA")

	src := s.seedSource("inpx", filepath.Join(dir, "lib.inpx"))
	id := s.seedBook(src, bookSeed{Title: "Zip Book", Format: "fb2", SourcePath: "fb.zip/book1.fb2"})

	w := s.getAuth("/books/"+itoa(id)+"/files/"+itoa(s.firstFileID(id)), testUser, testPass)
	s.Require().Equal(http.StatusOK, w.Code)
	s.Equal("FB2DATA", w.Body.String())
	s.Equal("application/x-fictionbook+xml", w.Header().Get("Content-Type"))
}

func (s *downloadSuite) TestCoverFallsBackToPlaceholder() {
	s.setCreds()
	src := s.seedSource("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "No Cover"})

	w := s.getAuth("/books/"+itoa(id)+"/cover", testUser, testPass)
	s.Require().Equal(http.StatusOK, w.Code)
	s.Equal("image/jpeg", w.Header().Get("Content-Type"))
}

func (s *downloadSuite) TestInvalidBookID() {
	s.setCreds()
	s.Equal(http.StatusBadRequest, s.getAuth("/books/abc/cover", testUser, testPass).Code)
}

func (s *downloadSuite) writeZip(path, name, content string) {
	f, err := os.Create(path)
	s.Require().NoError(err)
	defer func() { _ = f.Close() }()

	zw := zip.NewWriter(f)
	entry, err := zw.Create(name)
	s.Require().NoError(err)
	_, err = entry.Write([]byte(content))
	s.Require().NoError(err)
	s.Require().NoError(zw.Close())
}

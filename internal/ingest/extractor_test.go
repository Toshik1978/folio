package ingest

import (
	"archive/zip"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ebook"
)

type extractorSuite struct {
	baseSuite
}

// fb2WithCover builds a minimal FB2 document carrying an annotation and an
// embedded JPEG cover.
func fb2WithCover(title, annotation string, cover []byte) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<FictionBook>
  <description>
    <title-info>
      <book-title>%s</book-title>
      <author><first-name>A</first-name><last-name>B</last-name></author>
      <annotation>%s</annotation>
      <lang>en</lang>
      <coverpage><image href="#c.bin"/></coverpage>
    </title-info>
  </description>
  <binary id="c.bin" content-type="image/jpeg">%s</binary>
</FictionBook>`, title, annotation, base64.StdEncoding.EncodeToString(cover))
}

func (s *extractorSuite) insertBook(libraryID int64, sourcePath, format string) int64 {
	q := dbq.New(s.db)
	id, err := q.InsertBook(context.Background(), dbq.InsertBookParams{
		Title: "T", LibraryID: libraryID, LibraryKey: sourcePath,
		Language: "en", ContentHash: sourcePath, AddedAt: time.Now().Unix(),
	})
	s.Require().NoError(err)
	_, err = q.InsertBookFile(context.Background(), dbq.InsertBookFileParams{
		BookID: id, FileFormat: format, FileSize: 1, SourcePath: sourcePath,
	})
	s.Require().NoError(err)

	return id
}

func (s *extractorSuite) TestCoverAndAnnotationFromFolder() {
	dir := s.T().TempDir()
	s.Require().NoError(os.WriteFile(
		filepath.Join(dir, "a.fb2"),
		[]byte(fb2WithCover("Folder Book", "<p>Folder annotation</p>", []byte("FOLDER-COVER"))),
		0o600,
	))
	src := s.insertLibrary("folder", dir)
	id := s.insertBook(src.ID, "a.fb2", "fb2")

	ext := NewExtractor(s.db, s.log, s.T().TempDir(), newTestDispatcher())

	cover, ok, err := ext.Cover(context.Background(), id)
	s.Require().NoError(err)
	s.True(ok)
	s.Equal("FOLDER-COVER", string(cover))

	meta, ok, err := ext.Backfill(context.Background(), id)
	s.Require().NoError(err)
	s.True(ok)
	s.Contains(meta.Annotation, "Folder annotation")
}

func (s *extractorSuite) TestAnnotationSkipsPDF() {
	dir := s.T().TempDir()
	src := s.insertLibrary("folder", dir)
	// The PDF is never written to disk: opening it would fail. The annotation
	// extractor must skip PDFs entirely, so it returns cleanly without an error
	// (whereas Cover, which does accept PDFs, would try to parse and fail).
	id := s.insertBook(src.ID, "missing.pdf", "pdf")

	ext := NewExtractor(s.db, s.log, s.T().TempDir(), newTestDispatcher())

	meta, ok, err := ext.Backfill(context.Background(), id)
	s.Require().NoError(err, "PDF must be skipped, not parsed")
	s.False(ok)
	s.Empty(meta.Annotation)

	// Sanity: Cover does accept the PDF, so it actually attempts the parse.
	_, _, coverErr := ext.Cover(context.Background(), id)
	s.Error(coverErr, "Cover should attempt to parse the (missing) PDF")
}

func (s *extractorSuite) TestCoverFromINPXZip() {
	dir := s.T().TempDir()
	archive := filepath.Join(dir, "arc.zip")
	s.writeZip(archive, "inner.fb2", fb2WithCover("Zip Book", "<p>Zip annotation</p>", []byte("ZIP-COVER")))

	// library.Path points at the .inpx; the archive is its sibling.
	src := s.insertLibrary("inpx", filepath.Join(dir, "lib.inpx"))
	id := s.insertBook(src.ID, "arc.zip/inner.fb2", "fb2")

	ext := NewExtractor(s.db, s.log, s.T().TempDir(), newTestDispatcher())

	cover, ok, err := ext.Cover(context.Background(), id)
	s.Require().NoError(err)
	s.True(ok)
	s.Equal("ZIP-COVER", string(cover))

	meta, ok, err := ext.Backfill(context.Background(), id)
	s.Require().NoError(err)
	s.True(ok)
	s.Contains(meta.Annotation, "Zip annotation")
}

// TestBackfillReturnsCleanedIdentifiers proves the widened backfill recovers
// identifiers from the source file and normalizes them like sync does (a messy
// ISBN-13 with hyphens collapses to the canonical, hyphen-free form).
func (s *extractorSuite) TestBackfillReturnsCleanedIdentifiers() {
	dir := s.T().TempDir()
	doc := `<?xml version="1.0" encoding="utf-8"?>
<FictionBook>
  <description>
    <title-info>
      <book-title>Ided</book-title>
      <author><first-name>A</first-name><last-name>B</last-name></author>
      <lang>en</lang>
    </title-info>
    <publish-info><isbn>978-0-441-01359-3</isbn></publish-info>
  </description>
</FictionBook>`
	s.Require().NoError(os.WriteFile(filepath.Join(dir, "a.fb2"), []byte(doc), 0o600))
	src := s.insertLibrary("folder", dir)
	id := s.insertBook(src.ID, "a.fb2", "fb2")

	ext := NewExtractor(s.db, s.log, s.T().TempDir(), newTestDispatcher())
	meta, ok, err := ext.Backfill(context.Background(), id)
	s.Require().NoError(err)
	s.Require().True(ok)
	s.Equal([]ebook.Identifier{{Type: "isbn", Value: "9780441013593"}}, meta.Identifiers)
}

func (s *extractorSuite) TestINPXMaterializesUnderDataDir() {
	dir := s.T().TempDir()
	archive := filepath.Join(dir, "arc.zip")
	s.writeZip(archive, "inner.fb2", fb2WithCover("Zip Book", "<p>a</p>", []byte("ZIP-COVER")))

	src := s.insertLibrary("inpx", filepath.Join(dir, "lib.inpx"))
	id := s.insertBook(src.ID, "arc.zip/inner.fb2", "fb2")

	dataDir := s.T().TempDir()
	ext := NewExtractor(s.db, s.log, dataDir, newTestDispatcher())

	_, ok, err := ext.Cover(context.Background(), id)
	s.Require().NoError(err)
	s.True(ok)

	// Inner-file materialization must happen under dataDir/tmp, not system temp.
	_, err = os.Stat(filepath.Join(dataDir, "tmp"))
	s.Require().NoError(err)
}

func (s *extractorSuite) TestMissingBookIsNotOK() {
	ext := NewExtractor(s.db, s.log, s.T().TempDir(), newTestDispatcher())
	_, ok, err := ext.Cover(context.Background(), 9999)
	s.Require().NoError(err)
	s.False(ok)
}

func (s *extractorSuite) writeZip(path, name, content string) {
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

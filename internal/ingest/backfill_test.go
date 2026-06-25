package ingest

import (
	"context"
	"path/filepath"

	"github.com/Toshik1978/folio/internal/db"
	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ebook"
)

type backfillSuite struct {
	baseSuite
}

// fakeFileExtractor returns fixed backfill metadata and counts parses.
type fakeFileExtractor struct {
	meta   ebook.Metadata
	ok     bool
	called int
}

func (f *fakeFileExtractor) Backfill(context.Context, int64) (ebook.Metadata, bool, error) {
	f.called++
	return f.meta, f.ok, nil
}

func (s *backfillSuite) seedBook(libraryID int64, title, format string) int64 {
	q := dbq.New(s.db)
	id, err := q.InsertBook(context.Background(), dbq.InsertBookParams{
		Title: title, LibraryID: libraryID, LibraryKey: title,
		Language: "en", ContentHash: title, AddedAt: 1,
	})
	s.Require().NoError(err)
	_, err = q.InsertBookFile(context.Background(), dbq.InsertBookFileParams{
		BookID: id, FileFormat: format, FileSize: 1, SourcePath: title + "." + format,
	})
	s.Require().NoError(err)

	return id
}

func (s *backfillSuite) TestFillPersistsAnnotationAndIdentifiers() {
	ext := &fakeFileExtractor{
		meta: ebook.Metadata{
			Annotation:  "Recovered annotation",
			Identifiers: []ebook.Identifier{{Type: "isbn", Value: "9780441013593"}},
		},
		ok: true,
	}
	src := s.insertLibrary("inpx", filepath.Join(s.T().TempDir(), "lib.inpx"))
	id := s.seedBook(src.ID, "No Annotation", "fb2")

	bf := NewLocalBackfiller(s.log, s.db, db.NewWriteGuard(), ext)
	s.Require().NoError(bf.Fill(context.Background(), id))

	stored, err := dbq.New(s.db).GetBook(context.Background(), id)
	s.Require().NoError(err)
	s.True(stored.Annotation.Valid)
	s.Equal("Recovered annotation", stored.Annotation.String)
	s.Equal(int64(1), stored.MetadataChecked)

	ids, err := dbq.New(s.db).ListIdentifiersForBook(context.Background(), id)
	s.Require().NoError(err)
	s.Require().Len(ids, 1)
	s.Equal("9780441013593", ids[0].Value)
}

func (s *backfillSuite) TestFillMarksCheckedOnEmptyAndSkipsSecondParse() {
	ext := &fakeFileExtractor{ok: false} // nothing parseable
	src := s.insertLibrary("inpx", filepath.Join(s.T().TempDir(), "lib.inpx"))
	id := s.seedBook(src.ID, "Empty", "fb2")

	bf := NewLocalBackfiller(s.log, s.db, db.NewWriteGuard(), ext)
	s.Require().NoError(bf.Fill(context.Background(), id))

	stored, err := dbq.New(s.db).GetBook(context.Background(), id)
	s.Require().NoError(err)
	s.False(stored.Annotation.Valid, "still no annotation")
	s.Equal(int64(1), stored.MetadataChecked, "marked checked (negative cache)")

	// A checked book is never re-parsed.
	s.Require().NoError(bf.Fill(context.Background(), id))
	s.Equal(1, ext.called, "checked book must not be re-extracted")
}

func (s *backfillSuite) TestFillDoesNotClobberExistingAnnotation() {
	ext := &fakeFileExtractor{meta: ebook.Metadata{Annotation: "from-file"}, ok: true}
	src := s.insertLibrary("folder", "/lib")
	id := s.seedBook(src.ID, "Annotated", "fb2")
	// Seed the annotation WITHOUT touching metadata_checked so Fill proceeds past
	// the early-return gate and reaches the no-clobber guard.
	_, err := s.db.ExecContext(context.Background(), "UPDATE books SET annotation = ? WHERE id = ?", "keep me", id)
	s.Require().NoError(err)

	bf := NewLocalBackfiller(s.log, s.db, db.NewWriteGuard(), ext)
	s.Require().NoError(bf.Fill(context.Background(), id))

	// The extractor must have been called — proving we passed the metadata_checked gate.
	s.Equal(1, ext.called, "extractor must run to reach the no-clobber guard")

	stored, err := dbq.New(s.db).GetBook(context.Background(), id)
	s.Require().NoError(err)
	s.Equal("keep me", stored.Annotation.String, "backfill must not replace an existing annotation")
}

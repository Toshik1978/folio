package ingest

import (
	"context"
	"database/sql"
	"os"
	"time"

	"github.com/Toshik1978/folio/internal/db/dbq"
)

type importerSuite struct {
	baseSuite
}

// rec returns a minimal "en" bookRecord for merge tests. All editions share one
// LibraryKey so add() groups them onto a single logical book; tests that need a
// different language set rec.Language on the returned value.
func (s *importerSuite) rec(lib dbq.Library, format, path string) bookRecord {
	return bookRecord{
		LibraryID: lib.ID, LibraryKey: "k1", Title: "T", Authors: []string{"A"},
		Language: "en", SourcePath: path, FileFormat: format, FileSize: 10,
	}
}

// H5: an in-place language edit of the owning edition must propagate.
func (s *importerSuite) TestLanguageEditPropagates() {
	ctx := context.Background()
	lib := s.insertLibrary("folder", "/lib")
	im := newImporter(s.log, s.db, s.store, 1)
	defer im.rollback()

	r := s.rec(lib, "epub", "a.epub")
	id, err := im.add(ctx, r, 1)
	s.Require().NoError(err)
	s.Require().NoError(im.commit())

	r.Language = "ru" // retagged at the source
	_, err = im.add(ctx, r, 1)
	s.Require().NoError(err)
	s.Require().NoError(im.commit())

	book, err := dbq.New(s.db).GetBook(ctx, id)
	s.Require().NoError(err)
	s.Equal("ru", book.Language)
}

// H1: a manually matched book is never overwritten by sync, only gap-filled.
func (s *importerSuite) TestManualMatchSurvivesSync() {
	ctx := context.Background()
	lib := s.insertLibrary("folder", "/lib")
	q := dbq.New(s.db)
	im := newImporter(s.log, s.db, s.store, 1)
	defer im.rollback()

	r := s.rec(lib, "epub", "a.epub")
	id, err := im.add(ctx, r, 1)
	s.Require().NoError(err)
	s.Require().NoError(im.commit())

	// Simulate Fix Match: user-chosen title + the manual marker + drifted hash.
	s.Require().NoError(q.UpdateBookMatch(ctx, dbq.UpdateBookMatchParams{
		Title: "User Title", ContentHash: "match-hash", ID: id,
	}))

	r.Title = "Source Title" // source still carries the old title
	_, err = im.add(ctx, r, 1)
	s.Require().NoError(err)
	s.Require().NoError(im.commit())

	book, err := q.GetBook(ctx, id)
	s.Require().NoError(err)
	s.Equal("User Title", book.Title, "sync must not revert a Fix Match")
}

// M8: two same-format files must not ping-pong ownership every run.
func (s *importerSuite) TestSameFormatSiblingsDoNotPingPong() {
	ctx := context.Background()
	lib := s.insertLibrary("folder", "/lib")
	q := dbq.New(s.db)
	im := newImporter(s.log, s.db, s.store, 1)
	defer im.rollback()

	a := s.rec(lib, "epub", "a.epub")
	b := s.rec(lib, "epub", "b.epub")
	b.Title = "T-variant" // same key (rec pins LibraryKey), differing metadata

	id, err := im.add(ctx, a, 1)
	s.Require().NoError(err)
	_, err = im.add(ctx, b, 1)
	s.Require().NoError(err)
	s.Require().NoError(im.commit())

	before, err := q.GetBook(ctx, id)
	s.Require().NoError(err)

	// Re-run both editions: with the sibling guard neither may flip the row.
	_, err = im.add(ctx, a, 1)
	s.Require().NoError(err)
	_, err = im.add(ctx, b, 1)
	s.Require().NoError(err)
	s.Require().NoError(im.commit())

	after, err := q.GetBook(ctx, id)
	s.Require().NoError(err)
	s.Equal(before.Title, after.Title)
	s.Equal(before.ContentHash, after.ContentHash, "hash churn = ping-pong overwrite")
}

// M5: a sibling edition's identifiers gap-fill but never replace.
func (s *importerSuite) TestSiblingIdentifierNeverReplaces() {
	ctx := context.Background()
	lib := s.insertLibrary("folder", "/lib")
	q := dbq.New(s.db)
	im := newImporter(s.log, s.db, s.store, 1)
	defer im.rollback()

	a := s.rec(lib, "epub", "a.epub")
	a.Identifiers = []identifier{{Type: "isbn", Value: "9780306406157"}} // ISBN-13
	id, err := im.add(ctx, a, 1)
	s.Require().NoError(err)

	m := s.rec(lib, "mobi", "a.mobi")
	m.Identifiers = []identifier{{Type: "isbn", Value: "0306406152"}} // ISBN-10
	_, err = im.add(ctx, m, 1)
	s.Require().NoError(err)
	s.Require().NoError(im.commit())

	ids, err := q.ListIdentifiersForBook(ctx, id)
	s.Require().NoError(err)
	s.Require().Len(ids, 1)
	s.Equal("9780306406157", ids[0].Value, "lower-priority edition replaced the ISBN-13")
}

// L4: a pages-only change (same size/mtime) must still be stored.
func (s *importerSuite) TestPagesOnlyChangeIsStored() {
	ctx := context.Background()
	lib := s.insertLibrary("folder", "/lib")
	q := dbq.New(s.db)
	im := newImporter(s.log, s.db, s.store, 1)
	defer im.rollback()

	r := s.rec(lib, "epub", "a.epub")
	r.Pages = 100
	id, err := im.add(ctx, r, 1)
	s.Require().NoError(err)

	r.Pages = 250 // e.g. Calibre custom-column edit; size and mtime unchanged
	_, err = im.add(ctx, r, 1)
	s.Require().NoError(err)
	s.Require().NoError(im.commit())

	files, err := q.ListFilesForBook(ctx, id)
	s.Require().NoError(err)
	s.Require().Len(files, 1)
	s.Equal(sql.NullInt64{Int64: 250, Valid: true}, files[0].Pages)
}

// M2: a low-priority edition re-parsed alone must not replace a richer
// edition's cover saved in an earlier run.
func (s *importerSuite) TestCoverNotDowngradedAcrossRuns() {
	ctx := context.Background()
	lib := s.insertLibrary("folder", "/lib")
	im := newImporter(s.log, s.db, s.store, 1)
	defer im.rollback()

	epub := s.rec(lib, "epub", "a.epub")
	epub.Cover = s.coverFixture()
	id, err := im.add(ctx, epub, 1)
	s.Require().NoError(err)
	s.Require().NoError(im.commit())
	saved, err := os.ReadFile(s.store.Path(id))
	s.Require().NoError(err)

	// Fresh importer = fresh run: only the PDF is re-parsed (folder diff).
	im2 := newImporter(s.log, s.db, s.store, 1)
	defer im2.rollback()
	pdf := s.rec(lib, "pdf", "a.pdf")
	pdf.Cover = s.coverFixtureAlt() // distinguishable bytes
	_, err = im2.add(ctx, pdf, 1)
	s.Require().NoError(err)
	s.Require().NoError(im2.commit())

	after, err := os.ReadFile(s.store.Path(id))
	s.Require().NoError(err)
	s.Equal(saved, after, "PDF page-1 render replaced the EPUB cover")
}

// L1: a book with no parseable language is stored as the 'und' sentinel, not 'en'.
func (s *importerSuite) TestInsertStoresUndForUnknownLanguage() {
	lib := s.insertLibrary("folder", "/lib/lang1")
	im := newImporter(s.log, s.db, s.store, 1)
	id, err := im.add(context.Background(), bookRecord{
		LibraryID: lib.ID, LibraryKey: "k1", Title: "T",
		FileFormat: "epub", SourcePath: "a.epub", // no Language
	}, time.Now().Unix())
	s.Require().NoError(err)
	s.Require().NoError(im.commit())

	book, err := dbq.New(s.db).GetBook(context.Background(), id)
	s.Require().NoError(err)
	s.Equal("und", book.Language)
}

// L1: a real language from any edition upgrades an 'und' book.
func (s *importerSuite) TestGapFillUpgradesUndLanguage() {
	ctx := context.Background()
	lib := s.insertLibrary("folder", "/lib/lang2")
	im := newImporter(s.log, s.db, s.store, 1)

	id, err := im.add(ctx, bookRecord{
		LibraryID: lib.ID, LibraryKey: "k", Title: "T",
		FileFormat: "epub", SourcePath: "a.epub", // owner, unknown language → 'und'
	}, time.Now().Unix())
	s.Require().NoError(err)

	_, err = im.add(ctx, bookRecord{
		LibraryID: lib.ID, LibraryKey: "k", Title: "T", Language: "de",
		FileFormat: "pdf", SourcePath: "a.pdf", // lower-priority edition carries a real language
	}, time.Now().Unix())
	s.Require().NoError(err)
	s.Require().NoError(im.commit())

	book, err := dbq.New(s.db).GetBook(ctx, id)
	s.Require().NoError(err)
	s.Equal("de", book.Language)
}

// L1: a real language is never overwritten by a sibling edition's gap-fill.
func (s *importerSuite) TestGapFillKeepsRealLanguage() {
	ctx := context.Background()
	lib := s.insertLibrary("folder", "/lib/lang3")
	im := newImporter(s.log, s.db, s.store, 1)

	id, err := im.add(ctx, bookRecord{
		LibraryID: lib.ID, LibraryKey: "k", Title: "T", Language: "en",
		FileFormat: "epub", SourcePath: "a.epub",
	}, time.Now().Unix())
	s.Require().NoError(err)

	_, err = im.add(ctx, bookRecord{
		LibraryID: lib.ID, LibraryKey: "k", Title: "T", Language: "de",
		FileFormat: "pdf", SourcePath: "a.pdf",
	}, time.Now().Unix())
	s.Require().NoError(err)
	s.Require().NoError(im.commit())

	book, err := dbq.New(s.db).GetBook(ctx, id)
	s.Require().NoError(err)
	s.Equal("en", book.Language, "a real language is never clobbered by a sibling")
}

// L3: 'added' counts genuinely new files only; a changed existing file is not an add.
func (s *importerSuite) TestReconcilerCountsOnlyNewFiles() {
	ctx := context.Background()
	lib := s.insertLibrary("folder", "/lib/added")
	rec := bookRecord{
		LibraryID: lib.ID, LibraryKey: "k", Title: "T",
		FileFormat: "epub", SourcePath: "a.epub", FileSize: 1, Mtime: 1,
	}

	im := newImporter(s.log, s.db, s.store, 1)
	rc, err := newReconciler(ctx, im, lib.ID, time.Now().Unix(), nopReporter{})
	s.Require().NoError(err)
	s.Require().NoError(rc.upsert(ctx, rec))
	s.Require().NoError(im.commit())
	s.Equal(1, rc.added, "a brand-new file is an add")

	im2 := newImporter(s.log, s.db, s.store, 1)
	rc2, err := newReconciler(ctx, im2, lib.ID, time.Now().Unix(), nopReporter{})
	s.Require().NoError(err)
	changed := rec
	changed.FileSize, changed.Mtime = 2, 2 // same path, new bytes
	s.Require().NoError(rc2.upsert(ctx, changed))
	s.Require().NoError(im2.commit())
	s.Equal(0, rc2.added, "a changed existing file is not an add")
}

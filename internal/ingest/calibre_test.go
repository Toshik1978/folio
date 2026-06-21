package ingest

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"

	"github.com/Toshik1978/folio/internal/db/dbq"
)

type calibreSuite struct {
	baseSuite
}

func (s *calibreSuite) buildCalibreDB(libDir string) {
	metaPath := filepath.Join(libDir, "metadata.db")
	cdb, err := sql.Open("sqlite", metaPath)
	s.Require().NoError(err)
	defer func() { _ = cdb.Close() }()

	schema := []string{
		`CREATE TABLE books (id INTEGER PRIMARY KEY, title TEXT, path TEXT, series_index REAL DEFAULT 1.0, has_cover INTEGER DEFAULT 0, pubdate TEXT, timestamp TEXT)`,
		`CREATE TABLE authors (id INTEGER PRIMARY KEY, name TEXT)`,
		`CREATE TABLE books_authors_link (id INTEGER PRIMARY KEY, book INTEGER, author INTEGER)`,
		`CREATE TABLE series (id INTEGER PRIMARY KEY, name TEXT)`,
		`CREATE TABLE books_series_link (id INTEGER PRIMARY KEY, book INTEGER, series INTEGER)`,
		`CREATE TABLE tags (id INTEGER PRIMARY KEY, name TEXT)`,
		`CREATE TABLE books_tags_link (id INTEGER PRIMARY KEY, book INTEGER, tag INTEGER)`,
		`CREATE TABLE publishers (id INTEGER PRIMARY KEY, name TEXT)`,
		`CREATE TABLE books_publishers_link (id INTEGER PRIMARY KEY, book INTEGER, publisher INTEGER)`,
		`CREATE TABLE identifiers (id INTEGER PRIMARY KEY, book INTEGER, type TEXT, val TEXT)`,
		`CREATE TABLE comments (id INTEGER PRIMARY KEY, book INTEGER, text TEXT)`,
		`CREATE TABLE data (id INTEGER PRIMARY KEY, book INTEGER, format TEXT, uncompressed_size INTEGER, name TEXT)`,
		`CREATE TABLE languages (id INTEGER PRIMARY KEY, lang_code TEXT)`,
		`CREATE TABLE books_languages_link (id INTEGER PRIMARY KEY, book INTEGER, lang_code INTEGER, item_order INTEGER DEFAULT 0)`,
		`CREATE TABLE ratings (id INTEGER PRIMARY KEY, rating INTEGER)`,
		`CREATE TABLE books_ratings_link (id INTEGER PRIMARY KEY, book INTEGER, rating INTEGER)`,
		`CREATE TABLE custom_columns (id INTEGER PRIMARY KEY, label TEXT, name TEXT, datatype TEXT)`,
		`CREATE TABLE custom_column_1 (id INTEGER PRIMARY KEY, book INTEGER, value INTEGER)`,
	}
	for _, q := range schema {
		_, err := cdb.ExecContext(context.Background(), q)
		s.Require().NoError(err)
	}

	stmts := []string{
		`INSERT INTO books (id, title, path, series_index, has_cover, pubdate, timestamp) VALUES (1, 'War and Peace', 'Leo Tolstoy/War and Peace (1)', 2.0, 1, '1869-01-01 00:00:00+00:00', '2014-06-28 22:02:03+00:00')`,
		`INSERT INTO books (id, title, path, series_index, has_cover, pubdate) VALUES (2, 'Short Stories', 'Anton Chekhov/Short Stories (2)', 1.0, 0, '0101-01-01 00:00:00+00:00')`,
		`INSERT INTO authors (id, name) VALUES (1, 'Leo Tolstoy'), (2, 'Anton Chekhov')`,
		`INSERT INTO books_authors_link (book, author) VALUES (1, 1), (2, 2)`,
		`INSERT INTO series (id, name) VALUES (1, 'Russian Classics')`,
		`INSERT INTO books_series_link (book, series) VALUES (1, 1)`,
		`INSERT INTO tags (id, name) VALUES (1, 'Literary'), (2, 'History')`,
		`INSERT INTO books_tags_link (book, tag) VALUES (1, 1), (1, 2)`,
		`INSERT INTO publishers (id, name) VALUES (1, 'Penguin Classics')`,
		`INSERT INTO books_publishers_link (book, publisher) VALUES (1, 1)`,
		`INSERT INTO identifiers (book, type, val) VALUES (1, 'isbn', '9780140447934'), (1, 'amazon', 'B000XYZ123')`,
		`INSERT INTO comments (book, text) VALUES (1, '<p>An epic &amp; sweeping novel.</p>')`,
		`INSERT INTO data (book, format, uncompressed_size, name) VALUES (1, 'EPUB', 12345, 'War and Peace - Leo Tolstoy')`,
		`INSERT INTO data (book, format, uncompressed_size, name) VALUES (2, 'FB2', 222, 'Short Stories - Anton Chekhov')`,
		`INSERT INTO languages (id, lang_code) VALUES (1, 'eng')`,
		`INSERT INTO books_languages_link (book, lang_code) VALUES (1, 1)`,
		`INSERT INTO ratings (id, rating) VALUES (1, 8), (2, 10), (3, 1)`,
		`INSERT INTO books_ratings_link (book, rating) VALUES (1, 1), (2, 3)`,
		`INSERT INTO custom_columns (id, label, name, datatype) VALUES (1, 'pages', 'Pages', 'int')`,
		`INSERT INTO custom_column_1 (book, value) VALUES (1, 350)`,
	}
	for _, q := range stmts {
		_, err := cdb.ExecContext(context.Background(), q)
		s.Require().NoError(err)
	}

	coverDir := filepath.Join(libDir, "Leo Tolstoy", "War and Peace (1)")
	s.Require().NoError(os.MkdirAll(coverDir, 0o755))
	s.Require().NoError(os.WriteFile(filepath.Join(coverDir, "cover.jpg"), s.coverFixture(), 0o600))
}

// openCalibreDB builds a fixture metadata.db and returns an open read connection.
func (s *calibreSuite) openCalibreDB() *sql.DB {
	libDir := s.T().TempDir()
	s.buildCalibreDB(libDir)
	cdb, err := sql.Open("sqlite", filepath.Join(libDir, "metadata.db"))
	s.Require().NoError(err)
	s.T().Cleanup(func() { _ = cdb.Close() })

	return cdb
}

func (s *calibreSuite) TestLoadCalibreRatings() {
	got, err := loadCalibreRatings(context.Background(), s.openCalibreDB())
	s.Require().NoError(err)
	s.Equal(4, got[1], "calibre 8 → 4 stars")
	_, ok := got[2]
	s.False(ok, "a half-star rating (raw 1 = 0.5★) is omitted, not stored as a valid 0")
}

func (s *calibreSuite) TestLoadCalibrePages() {
	got, err := loadCalibrePages(context.Background(), s.openCalibreDB())
	s.Require().NoError(err)
	s.Equal(350, got[1])
}

func (s *calibreSuite) TestLoadCalibrePagesAbsent() {
	cdb, err := sql.Open("sqlite", filepath.Join(s.T().TempDir(), "empty.db"))
	s.Require().NoError(err)
	s.T().Cleanup(func() { _ = cdb.Close() })
	_, err = cdb.ExecContext(context.Background(), `CREATE TABLE custom_columns (id INTEGER PRIMARY KEY, label TEXT)`)
	s.Require().NoError(err)

	got, err := loadCalibrePages(context.Background(), cdb)
	s.Require().NoError(err)
	s.Empty(got, "no 'pages' custom column → empty map")
}

func (s *calibreSuite) TestParseCalibreTimestamp() {
	t, err := parseCalibreTimestamp("2014-06-28 22:02:03.123456+00:00")
	s.Require().NoError(err)
	s.Equal(int64(1403992923), t.Unix())

	_, err = parseCalibreTimestamp("")
	s.Error(err)
}

func (s *calibreSuite) TestResyncKeepsBookIDs() {
	ctx := context.Background()
	libDir := s.T().TempDir()
	s.buildCalibreDB(libDir)
	src := s.insertLibrary("calibre", libDir)

	parser := CalibreParser{s.log}
	_, err := parser.Sync(ctx, src, s.db, s.store, nopReporter{})
	s.Require().NoError(err)
	before := s.booksByLibrary(src.ID)
	s.Require().Len(before, 2)
	wpID := before["War and Peace"].ID
	s.Require().True(s.store.Has(wpID), "cover cached after first sync")

	_, err = parser.Sync(ctx, src, s.db, s.store, nopReporter{})
	s.Require().NoError(err)
	after := s.booksByLibrary(src.ID)
	s.Require().Len(after, 2, "re-sync must not duplicate books")
	s.Equal(wpID, after["War and Peace"].ID, "book id must be stable across re-sync")
	s.True(s.store.Has(wpID), "cover must survive re-sync")
}

// editCalibreBook1 mutates book 1's metadata in place the way Calibre does when
// a user edits a book — which rewrites metadata.db and so flips the checkpoint.
// The file bytes (data.uncompressed_size) are left unchanged on purpose, to prove
// the metadata refresh happens off the back of content_hash, not a file diff.
func (s *calibreSuite) editCalibreBook1(libDir string) {
	cdb, err := sql.Open("sqlite", filepath.Join(libDir, "metadata.db"))
	s.Require().NoError(err)
	defer func() { _ = cdb.Close() }()

	stmts := []string{
		`UPDATE comments SET text = '<p>A revised epic.</p>' WHERE book = 1`,
		`UPDATE publishers SET name = 'Vintage' WHERE id = 1`,
		`UPDATE books SET pubdate = '1865-01-01 00:00:00+00:00' WHERE id = 1`,
		`UPDATE authors SET name = 'Lev Tolstoy' WHERE id = 1`,
	}
	for _, q := range stmts {
		_, err := cdb.ExecContext(context.Background(), q)
		s.Require().NoError(err)
	}
}

// TestResyncAppliesInPlaceEdits guards L4: an in-place metadata edit of the
// owning edition must propagate on re-sync, even though the file bytes are
// unchanged. content_hash is what detects it.
func (s *calibreSuite) TestResyncAppliesInPlaceEdits() {
	ctx := context.Background()
	libDir := s.T().TempDir()
	s.buildCalibreDB(libDir)
	src := s.insertLibrary("calibre", libDir)

	parser := CalibreParser{s.log}
	_, err := parser.Sync(ctx, src, s.db, s.store, nopReporter{})
	s.Require().NoError(err)
	before := s.booksByLibrary(src.ID)["War and Peace"]
	s.Require().Equal("<p>An epic &amp; sweeping novel.</p>", before.Annotation.String)
	s.Require().Equal("Penguin Classics", before.Publisher.String)

	s.editCalibreBook1(libDir)

	_, err = parser.Sync(ctx, src, s.db, s.store, nopReporter{})
	s.Require().NoError(err)

	after := s.booksByLibrary(src.ID)["War and Peace"]
	s.Equal(before.ID, after.ID, "in-place edit must not recreate the book")
	s.Equal("<p>A revised epic.</p>", after.Annotation.String, "edited annotation must propagate")
	s.Equal("Vintage", after.Publisher.String, "edited publisher must propagate")
	s.Require().True(after.Year.Valid)
	s.Equal(int64(1865), after.Year.Int64, "edited year must propagate")
	s.Contains(s.authorsForBook(after.ID), "Lev Tolstoy", "edited author must re-link")

	_, ftsAuthors := s.ftsRow(after.ID)
	s.Contains(ftsAuthors, "Lev Tolstoy", "FTS authors must reflect the edit")

	// The hash was restamped, so a subsequent unchanged sync is a clean no-op.
	res, err := parser.Sync(ctx, src, s.db, s.store, nopReporter{})
	s.Require().NoError(err)
	s.Equal(0, res.Added, "unchanged re-sync after edit adds nothing")
	s.Equal("<p>A revised epic.</p>", s.booksByLibrary(src.ID)["War and Peace"].Annotation.String)
}

func (s *calibreSuite) TestSyncSetsDeterminateTotal() {
	ctx := context.Background()
	libDir := s.T().TempDir()
	s.buildCalibreDB(libDir)
	src := s.insertLibrary("calibre", libDir)

	rep := &countingReporter{}
	parser := CalibreParser{s.log}
	_, err := parser.Sync(ctx, src, s.db, s.store, rep)
	s.Require().NoError(err)
	s.Positive(rep.total, "Calibre must set a determinate total up front")
	s.Equal(rep.total, rep.processed, "processed must reach the declared total on a clean full sync")
}

func (s *calibreSuite) TestSync() {
	ctx := context.Background()
	libDir := s.T().TempDir()
	s.buildCalibreDB(libDir)

	src := s.insertLibrary("calibre", libDir)
	parser := CalibreParser{s.log}
	res, err := parser.Sync(ctx, src, s.db, s.store, nopReporter{})
	s.Require().NoError(err)
	s.Equal(2, res.Added)

	books := s.booksByLibrary(src.ID)
	s.Require().Len(books, 2)

	wp := books["War and Peace"]
	wpFile := s.fileFor(wp.ID)
	s.Equal("Leo Tolstoy/War and Peace (1)/War and Peace - Leo Tolstoy.epub", wpFile.SourcePath)
	s.Equal("epub", wpFile.FileFormat)
	s.Equal(int64(12345), wpFile.FileSize)
	s.Equal("en", wp.Language, "calibre 'eng' normalized to ISO 639-1")
	s.Equal("<p>An epic &amp; sweeping novel.</p>", wp.Annotation.String)
	s.Require().True(wp.SeriesID.Valid)
	s.Require().True(wp.SeriesNumber.Valid)
	s.InDelta(2.0, wp.SeriesNumber.Float64, 0.0001)
	s.Equal("Penguin Classics", wp.Publisher.String)
	s.Require().True(wp.Year.Valid)
	s.Equal(int64(1869), wp.Year.Int64)
	s.Require().True(wp.Rating.Valid)
	s.Equal(int64(4), wp.Rating.Int64, "calibre rating 8 → 4 stars")
	s.Equal(int64(1403992923), wp.AddedAt, "added_at from calibre books.timestamp")
	s.Require().True(wpFile.Pages.Valid)
	s.Equal(int64(350), wpFile.Pages.Int64, "page count from custom_column")

	// Identifiers landed (typed, lower-cased).
	ids, err := dbq.New(s.db).ListIdentifiersForBook(ctx, wp.ID)
	s.Require().NoError(err)
	idMap := make(map[string]string, len(ids))
	for _, id := range ids {
		idMap[id.Type] = id.Value
	}
	s.Equal("9780140447934", idMap["isbn"])
	s.Equal("B000XYZ123", idMap["amazon"])

	ss := books["Short Stories"]
	s.Equal("fb2", s.fileFor(ss.ID).FileFormat)
	s.False(ss.SeriesID.Valid)
	s.False(ss.Annotation.Valid)
	s.False(ss.Year.Valid)   // Calibre "unknown" pubdate (0101) → NULL
	s.False(ss.Rating.Valid) // unrated book → NULL
	s.NotZero(ss.AddedAt)    // no timestamp → falls back to the sync run time

	// Cover cached only for the book that has one (JPEG stored as-is).
	data, err := os.ReadFile(s.store.Path(wp.ID))
	s.Require().NoError(err)
	s.Equal(s.coverFixture(), data)
	_, err = os.Stat(s.store.Path(ss.ID))
	s.True(os.IsNotExist(err))

	// Authors and genres landed.
	authors, err := dbq.New(s.db).ListAuthors(ctx, dbq.ListAuthorsParams{Lim: 1000})
	s.Require().NoError(err)
	s.Len(authors, 2)
	genres, err := dbq.New(s.db).ListGenres(ctx, dbq.ListGenresParams{LibraryID: 0, Lim: 1000, Off: 0})
	s.Require().NoError(err)
	s.Len(genres, 2)

	// Re-sync reconciles: unchanged books are not re-added or duplicated.
	res, err = parser.Sync(ctx, src, s.db, s.store, nopReporter{})
	s.Require().NoError(err)
	s.Equal(0, res.Added, "unchanged re-sync adds nothing")
	stats, err := dbq.New(s.db).GlobalStats(ctx)
	s.Require().NoError(err)
	s.Equal(int64(2), stats.TotalBooks)
}

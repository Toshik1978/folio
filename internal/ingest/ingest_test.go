package ingest

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"image"
	"image/color"
	"image/jpeg"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/Toshik1978/folio/internal/covers"
	"github.com/Toshik1978/folio/internal/db"
	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ebook"
)

// newTestDispatcher builds the production parser set for tests that exercise
// real on-disk parsing through an injected Extractor/FolderParser.
func newTestDispatcher() *ebook.Dispatcher {
	return ebook.NewStandardDispatcher()
}

// testRank is the production format rank (EPUB 4, FB2 3, MOBI family 2, PDF 1)
// injected into every importer and source parser the tests build.
func testRank(format string) int {
	return newTestDispatcher().Rank(format)
}

// TestIngest is the package's single entry point; every suite is registered here.
func TestIngest(t *testing.T) {
	suite.Run(t, new(folderSuite))
	suite.Run(t, new(calibreSuite))
	suite.Run(t, new(inpxSuite))
	suite.Run(t, new(extractorSuite))
	suite.Run(t, new(helpersSuite))
	suite.Run(t, new(identifierSuite))
	suite.Run(t, new(genresSuite))
	suite.Run(t, new(enrichSuite))
	suite.Run(t, new(importerSuite))
	suite.Run(t, new(mergeSuite))
	suite.Run(t, new(backfillSuite))
	suite.Run(t, new(groupKeySuite))
	suite.Run(t, new(langSuite))
	suite.Run(t, new(reconcileReportSuite))
	suite.Run(t, new(idMatchSuite))
	suite.Run(t, new(identifierLookupSuite))
	suite.Run(t, new(coverStateSuite))
	suite.Run(t, new(deepIndexFixturesSuite))
	suite.Run(t, new(deepIndexSuite))
	suite.Run(t, new(deepIndexFolderSuite))
	suite.Run(t, new(ingestSuite))
}

// nopReporter discards progress. The tests use it to drive a Sync without caring
// about progress; production callers supply a real Reporter.
type nopReporter struct{}

func (nopReporter) SetTotal(int) {}
func (nopReporter) Add(int)      {}

// baseSuite gives each test a fresh folio database and cover store in a temp dir.
type baseSuite struct {
	suite.Suite

	log   *slog.Logger
	db    *sql.DB
	store *covers.Store
}

func (s *baseSuite) SetupTest() {
	s.log = slog.New(slog.DiscardHandler)
	dir := s.T().TempDir()
	database, err := db.Open(s.log, dir)
	s.Require().NoError(err)

	store, err := covers.NewStore(dir, nil, NewCoverState(database))
	s.Require().NoError(err)

	s.db = database
	s.store = store
}

func (s *baseSuite) TearDownTest() {
	if s.db != nil {
		_ = s.db.Close()
	}
}

// coverFixture returns a tiny real JPEG, since the cover store now transcodes
// covers on save and rejects non-image bytes.
func (s *baseSuite) coverFixture() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 120, G: 200, B: 80, A: 255})
	var buf bytes.Buffer
	s.Require().NoError(jpeg.Encode(&buf, img, nil))
	return buf.Bytes()
}

// coverFixtureAlt returns a JPEG with different pixels than coverFixture, so a
// test can tell one cached cover from another (e.g. EPUB vs PDF cover).
func (s *baseSuite) coverFixtureAlt() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 3, 3))
	img.Set(0, 0, color.RGBA{R: 10, G: 20, B: 200, A: 255})
	img.Set(2, 2, color.RGBA{R: 200, G: 10, B: 20, A: 255})
	var buf bytes.Buffer
	s.Require().NoError(jpeg.Encode(&buf, img, nil))

	return buf.Bytes()
}

func (s *baseSuite) insertLibrary(typ, path string) dbq.Library {
	q := dbq.New(s.db)
	id, err := q.InsertLibrary(context.Background(), dbq.InsertLibraryParams{
		Type: typ, Path: path, SyncIntervalSeconds: 3600, CreatedAt: time.Now().Unix(),
	})
	s.Require().NoError(err)
	src, err := q.GetLibrary(context.Background(), id)
	s.Require().NoError(err)

	return src
}

// booksByLibrary returns the library's books keyed by title for easy assertions.
func (s *baseSuite) booksByLibrary(libraryID int64) map[string]dbq.Book {
	rows, err := dbq.New(s.db).ListBooks(context.Background(), dbq.ListBooksParams{
		Limit: 1000, Offset: 0,
	})
	s.Require().NoError(err)
	out := make(map[string]dbq.Book, len(rows))
	for i := range rows {
		if rows[i].LibraryID == libraryID {
			out[rows[i].Title] = rows[i]
		}
	}

	return out
}

// fileFor returns the (single, in these tests) book_file for a book.
func (s *baseSuite) fileFor(bookID int64) dbq.BookFile {
	files := s.filesFor(bookID)
	s.Require().NotEmpty(files)
	return files[0]
}

// filesFor returns all book_files for a book.
func (s *baseSuite) filesFor(bookID int64) []dbq.BookFile {
	files, err := dbq.New(s.db).ListFilesForBook(context.Background(), bookID)
	s.Require().NoError(err)
	return files
}

// countBooks returns the total number of logical books.
func (s *baseSuite) countBooks() int64 {
	row, err := dbq.New(s.db).GlobalStats(context.Background())
	s.Require().NoError(err)
	return row.TotalBooks
}

// countRows returns the row count of a small lookup table (authors/series/genres).
func (s *baseSuite) countRows(table string) int {
	var n int
	err := s.db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM "+table).Scan(&n)
	s.Require().NoError(err)
	return n
}

// seriesName resolves a series id to its name.
func (s *baseSuite) seriesName(id int64) string {
	series, err := dbq.New(s.db).GetSeries(context.Background(), id)
	s.Require().NoError(err)
	return series.Name
}

// authorsForBook returns a book's author names (ordered by name).
func (s *baseSuite) authorsForBook(bookID int64) []string {
	authors, err := dbq.New(s.db).ListAuthorsForBook(context.Background(), bookID)
	s.Require().NoError(err)
	names := make([]string, len(authors))
	for i := range authors {
		names[i] = authors[i].Name
	}

	return names
}

// ftsHas reports whether the book's books_fts row matches an FTS5 phrase in one
// column. books_fts is contentless, so stored text cannot be read back; a MATCH
// restricted to the book's rowid is the only way to observe its indexed terms.
func (s *baseSuite) ftsHas(bookID int64, column, phrase string) bool {
	var n int
	err := s.db.QueryRowContext(
		context.Background(),
		"SELECT count(*) FROM books_fts WHERE books_fts MATCH ? AND rowid = ?",
		column+`:"`+phrase+`"`, bookID,
	).Scan(&n)
	s.Require().NoError(err)

	return n > 0
}

// ingestSuite holds package-wide structural checks that need no database.
type ingestSuite struct {
	suite.Suite
}

// ebookImportPath is the import path whose format-specific selectors the
// format-name guard rejects.
const ebookImportPath = "github.com/Toshik1978/folio/internal/ebook"

// formatLabels returns every registered format label and extension, lower-cased,
// with and without the leading dot (epub, .epub, fb2, .fb2.zip, fb2.zip, ...).
func formatLabels() []string {
	d := ebook.NewStandardDispatcher()
	set := map[string]struct{}{}
	for _, ext := range d.Extensions() {
		for _, l := range []string{ext, d.Format(ext)} {
			l = strings.TrimPrefix(strings.ToLower(l), ".")
			if l != "" {
				set[l] = struct{}{}
			}
		}
	}
	labels := make([]string, 0, len(set))
	for l := range set {
		labels = append(labels, l)
	}

	return labels
}

// formatNameViolations reports every place in file that names a book format:
// a string literal equal to a label or containing its dotted form, or a selector
// into package ebook whose name is a format constant (Format*) or embeds a
// format label (NewEPUB, FB2Parser, ParseFB2Description, ...). Identifiers of
// the file itself and comments are not checked.
func formatNameViolations(fset *token.FileSet, file *ast.File, labels []string) []string {
	ebookName := ebookImportName(file)

	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.BasicLit:
			if isFormatLiteral(n, labels) {
				out = append(out, fset.Position(n.Pos()).String()+": format literal "+n.Value)
			}
		case *ast.SelectorExpr:
			if isFormatSelector(n, ebookName, labels) {
				out = append(out, fset.Position(n.Pos()).String()+": format identifier ebook."+n.Sel.Name)
			}
		}

		return true
	})

	return out
}

// ebookImportName returns the name file uses for package ebook, or "" when it
// does not import it.
func ebookImportName(file *ast.File) string {
	for _, imp := range file.Imports {
		if path, _ := strconv.Unquote(imp.Path.Value); path != ebookImportPath {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}

		return "ebook"
	}

	return ""
}

// isFormatLiteral reports whether lit is a string whose lower-cased value equals
// a format label or contains its dotted form (".pdf", ".fb2.zip"). Values such
// as "mobi-asin" pass: they name an identifier scheme, not a format.
func isFormatLiteral(lit *ast.BasicLit, labels []string) bool {
	if lit.Kind != token.STRING {
		return false
	}
	v, err := strconv.Unquote(lit.Value)
	if err != nil {
		return false
	}
	v = strings.ToLower(v)

	return slices.ContainsFunc(labels, func(l string) bool {
		return v == l || strings.Contains(v, "."+l)
	})
}

// isFormatSelector reports whether sel selects a format-specific symbol from
// package ebook: a Format* constant, or a name embedding a format label.
func isFormatSelector(sel *ast.SelectorExpr, ebookName string, labels []string) bool {
	x, ok := sel.X.(*ast.Ident)
	if !ok || ebookName == "" || x.Name != ebookName {
		return false
	}
	if strings.HasPrefix(sel.Sel.Name, "Format") {
		return true
	}
	lower := strings.ToLower(sel.Sel.Name)

	return slices.ContainsFunc(labels, func(l string) bool {
		return !strings.Contains(l, ".") && strings.Contains(lower, l)
	})
}

// TestNoFormatNames guards that ingest and covers stay format-agnostic: format
// knowledge lives in package ebook and reaches them only through the injected
// reader and rank, never as a format literal or a format-specific ebook symbol.
func (s *ingestSuite) TestNoFormatNames() {
	labels := formatLabels()
	s.Require().Contains(labels, "azw3")
	s.Require().Contains(labels, "fb2.zip")

	fset := token.NewFileSet()
	var violations []string
	for _, root := range []string{".", filepath.Join("..", "covers")} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return fmt.Errorf("parse %s: %w", path, err)
			}
			violations = append(violations, formatNameViolations(fset, file, labels)...)

			return nil
		})
		s.Require().NoError(err)
	}

	s.Empty(violations, "format names outside package ebook")
}

// TestNoFormatNamesSelfTest proves the guard reports dotted extensions, compared
// labels and format-specific ebook symbols, and passes shared vocabulary in
// identifiers and identifier-scheme literals.
func (s *ingestSuite) TestNoFormatNamesSelfTest() {
	const src = `package x

import "github.com/Toshik1978/folio/internal/ebook"

// a comment naming epub and .pdf is not checked
var fb2GenreMap = map[string]string{}

const calibrePublishersQuery = "SELECT 1"

var scheme = "mobi-asin"

func f(format, name string) bool {
	_ = ebook.FormatPDF
	_ = strings.HasSuffix(name, ".azw3")
	return format == "pdf"
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "x.go", src, parser.ParseComments|parser.SkipObjectResolution)
	s.Require().NoError(err)

	got := strings.Join(formatNameViolations(fset, file, formatLabels()), "\n")
	s.Contains(got, "ebook.FormatPDF")
	s.Contains(got, `".azw3"`)
	s.Contains(got, `"pdf"`)
	s.NotContains(got, "fb2GenreMap")
	s.NotContains(got, "calibrePublishersQuery")
	s.NotContains(got, "mobi-asin")
	s.NotContains(got, "epub")
	s.Len(strings.Split(got, "\n"), 3)
}

type helpersSuite struct {
	baseSuite
}

func (s *helpersSuite) TestIngestHelpers() {
	// 1. Exported constructors return the concrete Parser each map key in
	// main.go's composition root expects (IsType inspects the interface's
	// dynamic type, so this fails loudly if a constructor is ever rewired).
	s.IsType(&CalibreParser{}, NewCalibreParser(s.log, testRank))
	s.IsType(&INPXParser{}, NewINPXParser(s.log, testRank))
	s.IsType(&FolderParser{}, NewFolderParser(s.log, newTestDispatcher()))

	// 2. fileCheckpoint
	tmp := s.T().TempDir()
	filePath := filepath.Join(tmp, "test.txt")
	s.Require().NoError(os.WriteFile(filePath, []byte("some-content"), 0o600))

	cp, err := fileCheckpoint(filePath)
	s.Require().NoError(err)
	s.NotEmpty(cp)

	_, err = fileCheckpoint(filepath.Join(tmp, "non-existent.txt"))
	s.Require().Error(err)

	// 3. Calibre Checkpoint
	cal := CalibreParser{log: s.log, rank: testRank}
	_, err = cal.Checkpoint(dbq.Library{Path: tmp})
	s.Require().Error(err)

	s.Require().NoError(os.WriteFile(filepath.Join(tmp, "metadata.db"), []byte("calibre db"), 0o600))
	calCP, err := cal.Checkpoint(dbq.Library{Path: tmp})
	s.Require().NoError(err)
	s.NotEmpty(calCP)

	// 4. INPX Checkpoint
	inpx := INPXParser{log: s.log, rank: testRank}
	_, err = inpx.Checkpoint(dbq.Library{Path: filepath.Join(tmp, "non-existent.inpx")})
	s.Require().Error(err)

	inpxPath := filepath.Join(tmp, "collection.inpx")
	s.Require().NoError(os.WriteFile(inpxPath, []byte("inpx data"), 0o600))
	inpxCP, err := inpx.Checkpoint(dbq.Library{Path: inpxPath})
	s.Require().NoError(err)
	s.NotEmpty(inpxCP)
}

func (s *helpersSuite) TestRollbackLeavesNoCoverOnDisk() {
	// With cover writes deferred until commit, a rollback must leave no cover on
	// disk — even for new books whose DB row was never persisted.
	lib := s.insertLibrary("folder", "/lib/rollback")
	im := newImporter(s.log, s.db, s.store, 1000, testRank) // large batch; auto-commit disabled

	bookID, err := im.add(context.Background(), bookRecord{
		LibraryID: lib.ID, LibraryKey: "k1", Title: "T",
		FileFormat: "epub", SourcePath: "a.epub", Cover: s.coverFixture(),
	}, time.Now().Unix(), true)
	s.Require().NoError(err)
	s.False(s.store.Has(bookID), "cover must not be written before commit")

	im.rollback()

	s.False(s.store.Has(bookID), "rollback must leave no cover on disk")
}

func (s *helpersSuite) TestRollbackDiscardsPendingCoversWhenTxNil() {
	// A rollback with tx == nil (e.g. after a failed commit) must still discard
	// queued cover ops so the caller never finds a cover that disagrees with the DB.
	im := newImporter(s.log, s.db, s.store, 1, testRank)
	const bookID = int64(9999)
	// Manually queue a cover save (simulates what saveCoverIfBetter would enqueue).
	im.pendingCovers = []coverOp{{bookID: bookID, data: s.coverFixture()}}

	im.rollback() // tx is nil; pending ops must be discarded, not flushed

	s.False(s.store.Has(bookID), "pending cover ops must be discarded on rollback")
}

func (s *helpersSuite) TestInsertBookDeduplicatesFTSAuthors() {
	lib := s.insertLibrary("folder", "/lib/dedupe")
	im := newImporter(s.log, s.db, s.store, 1, testRank)

	bookID, err := im.add(context.Background(), bookRecord{
		LibraryID: lib.ID, LibraryKey: "kd", Title: "T",
		Authors:    []string{"Asimov", "Asimov"},
		FileFormat: "epub", SourcePath: "d.epub",
	}, time.Now().Unix(), true)
	s.Require().NoError(err)

	s.True(s.ftsHas(bookID, "authors", "asimov"))
	s.False(s.ftsHas(bookID, "authors", "asimov asimov"), "duplicate author strings must not repeat in FTS")
}

// TestContentHashIncludesRating guards that rating participates in the content
// hash, so an edited rating re-triggers the overwrite path on re-sync (and the
// cover cache-buster changes). AddedAt deliberately does not.
func (s *helpersSuite) TestContentHashIncludesRating() {
	base := bookRecord{Title: "T", Language: "en"}
	rated := base
	rated.Rating = sql.NullInt64{Int64: 5, Valid: true}
	s.NotEqual(contentHash(base), contentHash(rated))

	added := base
	added.AddedAt = 1403992923
	s.Equal(contentHash(base), contentHash(added), "AddedAt must not affect the content hash")
}

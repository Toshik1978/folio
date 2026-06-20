package db

import (
	"context"
	"database/sql"
	"log/slog"
	"strconv"
	"time"

	"github.com/samber/lo"
	"github.com/stretchr/testify/suite"

	"github.com/Toshik1978/folio/internal/db/dbq"
)

type booksFilterSuite struct {
	suite.Suite

	db *sql.DB
	q  *dbq.Queries
}

func (s *booksFilterSuite) SetupTest() {
	database, err := Open(slog.New(slog.DiscardHandler), s.T().TempDir())
	s.Require().NoError(err)
	s.db = database
	s.q = dbq.New(database)
}

func (s *booksFilterSuite) TearDownTest() {
	if s.db != nil {
		_ = s.db.Close()
	}
}

func (s *booksFilterSuite) library() int64 {
	return s.libraryAt("Main", "/lib")
}

func (s *booksFilterSuite) libraryAt(name, path string) int64 {
	id, err := s.q.InsertLibrary(context.Background(), dbq.InsertLibraryParams{
		Name: name, Type: "folder", Path: path, SyncIntervalSeconds: 3600, CreatedAt: time.Now().Unix(),
	})
	s.Require().NoError(err)
	return id
}

// bookSeed describes a book to seed for filter tests; empty fields are omitted.
type bookSeed struct {
	key, title, lang, publisher, author, series string
	rating                                      int   // 0 = unrated
	addedAt, importedAt                         int64 // 0 = default to now
}

// seed inserts a book (with optional author/series links and an FTS row) and
// returns its id, so the filter tests can assert which books each filter selects.
func (s *booksFilterSuite) seed(srcID int64, bs bookSeed) int64 {
	ctx := context.Background()

	var seriesID sql.NullInt64
	if bs.series != "" {
		id, err := s.q.InsertSeries(ctx, dbq.InsertSeriesParams{Name: bs.series, NameFold: Fold(bs.series)})
		s.Require().NoError(err)
		seriesID = sql.NullInt64{Int64: id, Valid: true}
	}
	lang := lo.CoalesceOrEmpty(bs.lang, "en")

	added := bs.addedAt
	if added == 0 {
		added = time.Now().UnixNano()
	}
	imported := bs.importedAt
	if imported == 0 {
		imported = time.Now().UnixNano()
	}
	id, err := s.q.InsertBook(ctx, dbq.InsertBookParams{
		LibraryID: srcID, LibraryKey: bs.key, Title: bs.title, SeriesID: seriesID, Language: lang,
		Publisher:     sql.NullString{String: bs.publisher, Valid: bs.publisher != ""},
		PublisherFold: FoldNull(sql.NullString{String: bs.publisher, Valid: bs.publisher != ""}),
		Rating:        sql.NullInt64{Int64: int64(bs.rating), Valid: bs.rating != 0},
		ContentHash:   bs.key, AddedAt: added, ImportedAt: imported,
	})
	s.Require().NoError(err)

	if bs.author != "" {
		aid, aerr := s.q.InsertAuthor(ctx, dbq.InsertAuthorParams{Name: bs.author, NameFold: Fold(bs.author)})
		s.Require().NoError(aerr)
		s.Require().NoError(s.q.InsertBookAuthor(ctx, dbq.InsertBookAuthorParams{BookID: id, AuthorID: aid}))
	}
	s.Require().NoError(s.q.InsertBookFTS(ctx, dbq.InsertBookFTSParams{
		BookID: strconv.FormatInt(id, 10), Title: bs.title, Authors: bs.author, Series: bs.series,
	}))

	return id
}

// titles returns the titles of the given books, for set assertions.
func titles(books []dbq.Book) []string {
	out := make([]string, len(books))
	for i := range books {
		out[i] = books[i].Title
	}
	return out
}

// TestScalarFiltersSelectMatchingBooks exercises the author / series / language /
// publisher filters (each via its own join or WHERE) and confirms FilterBooks and
// CountFilteredBooks agree.
func (s *booksFilterSuite) TestScalarFiltersSelectMatchingBooks() {
	ctx := context.Background()
	src := s.library()
	dune := s.seed(src, bookSeed{
		key: "k1", title: "Dune", author: "Frank Herbert", series: "Dune Saga",
		lang: "en", publisher: "Ace",
	})
	s.seed(src, bookSeed{
		key: "k2", title: "Solaris", author: "Stanislaw Lem", series: "Standalone",
		lang: "pl", publisher: "MON",
	})

	cases := []struct {
		name   string
		filter BookFilter
	}{
		{"author", BookFilter{Author: FieldFilter{Value: "Frank Herbert", Exact: true}, Limit: 10}},
		{"series", BookFilter{Series: FieldFilter{Value: "Dune Saga", Exact: true}, Limit: 10}},
		{"lang", BookFilter{Lang: "en", Limit: 10}},
		{"publisher", BookFilter{Publisher: "Ace", Limit: 10}},
	}
	for i := range cases {
		tc := &cases[i]
		books, err := FilterBooks(ctx, s.db, tc.filter)
		s.Require().NoError(err, tc.name)
		s.Require().Len(books, 1, tc.name)
		s.Equal(dune, books[0].ID, tc.name)

		count, cerr := CountFilteredBooks(ctx, s.db, tc.filter)
		s.Require().NoError(cerr, tc.name)
		s.Equal(int64(1), count, tc.name)
	}
}

// TestSortByRating confirms Sort:"rating" orders rated books descending with
// unrated (NULL) books last.
func (s *booksFilterSuite) TestSortByRating() {
	ctx := context.Background()
	src := s.library()
	s.seed(src, bookSeed{key: "k1", title: "Low", rating: 2})
	s.seed(src, bookSeed{key: "k2", title: "High", rating: 5})
	s.seed(src, bookSeed{key: "k3", title: "Unrated"})

	books, err := FilterBooks(ctx, s.db, BookFilter{Sort: "rating", Limit: 10})
	s.Require().NoError(err)
	s.Equal([]string{"High", "Low", "Unrated"}, titles(books), "rated desc, unrated last")
}

// TestFilterBooksByLibraryID confirms the LibraryID filter restricts results to
// one library, and that a zero LibraryID leaves the listing unscoped.
func (s *booksFilterSuite) TestFilterBooksByLibraryID() {
	ctx := context.Background()
	lib1 := s.libraryAt("Alpha", "/a")
	lib2 := s.libraryAt("Beta", "/b")
	alpha := s.seed(lib1, bookSeed{key: "k1", title: "Alpha"})
	s.seed(lib2, bookSeed{key: "k2", title: "Beta"})

	scoped, err := FilterBooks(ctx, s.db, BookFilter{LibraryID: lib1, Limit: 10})
	s.Require().NoError(err)
	s.Require().Len(scoped, 1)
	s.Equal(alpha, scoped[0].ID)

	count, err := CountFilteredBooks(ctx, s.db, BookFilter{LibraryID: lib1})
	s.Require().NoError(err)
	s.Equal(int64(1), count)

	all, err := FilterBooks(ctx, s.db, BookFilter{Limit: 10})
	s.Require().NoError(err)
	s.Len(all, 2) // LibraryID == 0 means unscoped
}

// TestQuerySearchMatchesFTS confirms the FTS5 MATCH path returns only matching
// books (and excludes non-matches), ordered by relevance.
func (s *booksFilterSuite) TestQuerySearchMatchesFTS() {
	ctx := context.Background()
	src := s.library()
	s.seed(src, bookSeed{key: "k1", title: "Dune", author: "Frank Herbert"})
	s.seed(src, bookSeed{key: "k2", title: "Solaris", author: "Stanislaw Lem"})

	books, err := FilterBooks(ctx, s.db, BookFilter{Query: "Dune", Limit: 10})
	s.Require().NoError(err)
	s.Equal([]string{"Dune"}, titles(books))

	count, err := CountFilteredBooks(ctx, s.db, BookFilter{Query: "Dune"})
	s.Require().NoError(err)
	s.Equal(int64(1), count)

	none, err := FilterBooks(ctx, s.db, BookFilter{Query: "Hyperion", Limit: 10})
	s.Require().NoError(err)
	s.Empty(none)
}

// TestCombinedFiltersAndPagination checks that several filters AND together and
// that Limit/Offset page the result.
func (s *booksFilterSuite) TestCombinedFiltersAndPagination() {
	ctx := context.Background()
	src := s.library()
	s.seed(src, bookSeed{key: "k1", title: "Dune", author: "Frank Herbert", lang: "en"})
	s.seed(src, bookSeed{key: "k2", title: "Dune Messiah", author: "Frank Herbert", lang: "en"})
	s.seed(src, bookSeed{key: "k3", title: "Solaris", author: "Stanislaw Lem", lang: "pl"})

	hf := FieldFilter{Value: "Frank Herbert", Exact: true}

	all, err := FilterBooks(ctx, s.db, BookFilter{Author: hf, Lang: "en", Limit: 10})
	s.Require().NoError(err)
	s.Len(all, 2)

	count, err := CountFilteredBooks(ctx, s.db, BookFilter{Author: hf, Lang: "en"})
	s.Require().NoError(err)
	s.Equal(int64(2), count)

	page, err := FilterBooks(ctx, s.db, BookFilter{Author: hf, Lang: "en", Limit: 1, Offset: 1})
	s.Require().NoError(err)
	s.Len(page, 1, "Limit/Offset must page the filtered result")
}

// TestFormatFilterReturnsMultiFormatBookOnce guards the EXISTS-based format
// filter: a book with several formats must appear exactly once (no JOIN fan-out)
// and CountFilteredBooks must agree.
func (s *booksFilterSuite) TestFormatFilterReturnsMultiFormatBookOnce() {
	ctx := context.Background()
	srcID := s.library()
	bookID, err := s.q.InsertBook(ctx, dbq.InsertBookParams{
		LibraryID: srcID, LibraryKey: "k1", Title: "Dune", Language: "en",
		ContentHash: "h1", AddedAt: time.Now().UnixNano(),
	})
	s.Require().NoError(err)
	for _, f := range []struct{ format, path string }{{"epub", "dune.epub"}, {"fb2", "dune.fb2"}} {
		_, fileErr := s.q.InsertBookFile(ctx, dbq.InsertBookFileParams{
			BookID: bookID, FileFormat: f.format, FileSize: 1, SourcePath: f.path,
		})
		s.Require().NoError(fileErr)
	}

	books, err := FilterBooks(ctx, s.db, BookFilter{Format: "epub", Limit: 10})
	s.Require().NoError(err)
	s.Require().Len(books, 1, "multi-format book must be returned once when filtered by one format")
	s.Equal(bookID, books[0].ID)

	count, err := CountFilteredBooks(ctx, s.db, BookFilter{Format: "epub"})
	s.Require().NoError(err)
	s.Equal(int64(1), count)

	fb2, err := FilterBooks(ctx, s.db, BookFilter{Format: "fb2", Limit: 10})
	s.Require().NoError(err)
	s.Len(fb2, 1, "the same book matches its other format too")

	none, err := FilterBooks(ctx, s.db, BookFilter{Format: "mobi", Limit: 10})
	s.Require().NoError(err)
	s.Empty(none, "a format the book lacks matches nothing")
}

// TestPartialFacetSearchMatchesTokens confirms a non-exact facet matches on a
// single token within the field (e.g. one author name), column-scoped.
func (s *booksFilterSuite) TestPartialFacetSearchMatchesTokens() {
	ctx := context.Background()
	src := s.library()
	gg := s.seed(src, bookSeed{key: "k1", title: "Guards! Guards!", author: "Terry Pratchett", series: "Discworld"})
	s.seed(src, bookSeed{key: "k2", title: "Dune", author: "Frank Herbert", series: "Dune Saga"})

	// Partial author: one token of the full name matches.
	byAuthor, err := FilterBooks(ctx, s.db, BookFilter{Author: FieldFilter{Value: "Pratchett"}, Limit: 10})
	s.Require().NoError(err)
	s.Equal([]string{"Guards! Guards!"}, titles(byAuthor))

	// Column scoping: "Dune" as a series filter must not match the title "Dune".
	bySeries, err := FilterBooks(ctx, s.db, BookFilter{Series: FieldFilter{Value: "Discworld"}, Limit: 10})
	s.Require().NoError(err)
	s.Equal([]string{"Guards! Guards!"}, titles(bySeries))

	count, err := CountFilteredBooks(ctx, s.db, BookFilter{Author: FieldFilter{Value: "Pratchett"}})
	s.Require().NoError(err)
	s.Equal(int64(1), count)
	_ = gg
}

// TestExactFacetSearchRequiresFullValue confirms an exact facet matches only the
// whole stored value, not a single token.
func (s *booksFilterSuite) TestExactFacetSearchRequiresFullValue() {
	ctx := context.Background()
	src := s.library()
	s.seed(src, bookSeed{key: "k1", title: "Guards! Guards!", author: "Terry Pratchett"})

	full, err := FilterBooks(
		ctx,
		s.db,
		BookFilter{Author: FieldFilter{Value: "Terry Pratchett", Exact: true}, Limit: 10},
	)
	s.Require().NoError(err)
	s.Len(full, 1)

	partialName, err := FilterBooks(
		ctx,
		s.db,
		BookFilter{Author: FieldFilter{Value: "Pratchett", Exact: true}, Limit: 10},
	)
	s.Require().NoError(err)
	s.Empty(partialName, "exact match must require the full author name")
}

// TestCombinedQueryAndFacetSearch confirms free-text q AND a partial facet
// combine into a single MATCH.
func (s *booksFilterSuite) TestCombinedQueryAndFacetSearch() {
	ctx := context.Background()
	src := s.library()
	s.seed(src, bookSeed{key: "k1", title: "Dune", author: "Frank Herbert"})
	s.seed(src, bookSeed{key: "k2", title: "Dune Messiah", author: "Frank Herbert"})

	books, err := FilterBooks(ctx, s.db, BookFilter{
		Query: "Messiah", Author: FieldFilter{Value: "Herbert"}, Limit: 10,
	})
	s.Require().NoError(err)
	s.Equal([]string{"Dune Messiah"}, titles(books))
}

// TestFTSValueWithSpecialCharsIsEscaped confirms a value containing FTS5 syntax
// characters is treated as literal text, not query operators.
func (s *booksFilterSuite) TestFTSValueWithSpecialCharsIsEscaped() {
	ctx := context.Background()
	src := s.library()
	s.seed(src, bookSeed{key: "k1", title: `The "Quoted" Title`, author: "Anon"})

	books, err := FilterBooks(ctx, s.db, BookFilter{Title: FieldFilter{Value: `"Quoted"`}, Limit: 10})
	s.Require().NoError(err)
	s.Len(books, 1, "embedded quotes must be escaped, not parsed as FTS syntax")
}

// TestFilterBooksScanMatchesGetBook guards the hand-written column list / Scan in
// FilterBooks against drift from the sqlc-generated GetBook (the A2 risk): both
// must produce an identical Book.
func (s *booksFilterSuite) TestFilterBooksScanMatchesGetBook() {
	ctx := context.Background()
	srcID := s.library()
	seriesID, err := s.q.InsertSeries(ctx, dbq.InsertSeriesParams{Name: "Dune Saga", NameFold: Fold("Dune Saga")})
	s.Require().NoError(err)
	bookID, err := s.q.InsertBook(ctx, dbq.InsertBookParams{
		LibraryID:     srcID,
		LibraryKey:    "k1",
		Title:         "Dune",
		SeriesID:      sql.NullInt64{Int64: seriesID, Valid: true},
		SeriesNumber:  sql.NullFloat64{Float64: 1, Valid: true},
		Language:      "en",
		Annotation:    sql.NullString{String: "desc", Valid: true},
		Publisher:     sql.NullString{String: "Ace", Valid: true},
		PublisherFold: sql.NullString{String: "ACE", Valid: true},
		Year:          sql.NullInt64{Int64: 1965, Valid: true},
		Rating:        sql.NullInt64{Int64: 4, Valid: true},
		ContentHash:   "h1",
		AddedAt:       time.Now().UnixNano(),
	})
	s.Require().NoError(err)

	got, err := s.q.GetBook(ctx, bookID)
	s.Require().NoError(err)

	books, err := FilterBooks(ctx, s.db, BookFilter{Limit: 10})
	s.Require().NoError(err)
	s.Require().Len(books, 1)
	s.Equal(got, books[0], "FilterBooks scan must match GetBook column-for-column")
}

// TestSortByImportedAtAndSource proves the default sort orders by imported_at
// (when added to Folio) while Sort:"source" orders by added_at (source date).
// The two books have crossed timestamps so each sort yields the opposite order.
func (s *booksFilterSuite) TestSortByImportedAtAndSource() {
	ctx := context.Background()
	src := s.library()
	s.seed(src, bookSeed{key: "k1", title: "OldImport", addedAt: 2000, importedAt: 100})
	s.seed(src, bookSeed{key: "k2", title: "NewImport", addedAt: 1000, importedAt: 200})

	def, err := FilterBooks(ctx, s.db, BookFilter{Limit: 10})
	s.Require().NoError(err)
	s.Equal([]string{"NewImport", "OldImport"}, titles(def), "default sorts by imported_at DESC")

	bySource, err := FilterBooks(ctx, s.db, BookFilter{Sort: "source", Limit: 10})
	s.Require().NoError(err)
	s.Equal([]string{"OldImport", "NewImport"}, titles(bySource), "source sorts by added_at DESC")
}

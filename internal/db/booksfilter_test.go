package db

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"time"

	"github.com/samber/lo"
	"github.com/stephenafamo/bob/dialect/sqlite"
	"github.com/stephenafamo/bob/dialect/sqlite/sm"
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
	year                                        int   // 0 = unset
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
		Year:          sql.NullInt64{Int64: int64(bs.year), Valid: bs.year != 0},
		ContentHash:   bs.key, AddedAt: added, ImportedAt: imported,
	})
	s.Require().NoError(err)

	if bs.author != "" {
		aid, aerr := s.q.InsertAuthor(ctx, dbq.InsertAuthorParams{Name: bs.author, NameFold: Fold(bs.author)})
		s.Require().NoError(aerr)
		s.Require().NoError(s.q.InsertBookAuthor(ctx, dbq.InsertBookAuthorParams{BookID: id, AuthorID: aid}))
	}
	s.Require().NoError(InsertBookFTS(ctx, s.db, BookFTSRow{
		BookID: id, Title: bs.title, Authors: bs.author, Series: bs.series,
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

// TestDeletedBookRemovedFromFTS guards the FTS delete trigger (books_ad). The
// trigger's `book_id = old.id` match depends on SQLite column affinity coercing
// the TEXT book_id against the INTEGER old.id (see the comment in
// 002_create_fts5.sql). A future refactor that binds an integer book_id, or
// rewrites the trigger to a bare literal, would silently leave orphaned rows in
// the FTS index. The assertion queries books_fts directly (not FilterBooks,
// whose INNER JOIN back to books would hide an orphaned FTS row once the books
// row is gone) so it actually exercises the trigger.
func (s *booksFilterSuite) TestDeletedBookRemovedFromFTS() {
	ctx := context.Background()
	src := s.library()
	dune := s.seed(src, bookSeed{key: "k1", title: "Dune", author: "Frank Herbert"})
	s.seed(src, bookSeed{key: "k2", title: "Solaris", author: "Stanislaw Lem"})

	// Sanity: the title is in the FTS index before deletion.
	s.Require().Equal(1, s.ftsMatchCount(ctx, "Dune"), "seeded book must be in the FTS index")

	s.Require().NoError(s.q.DeleteBook(ctx, dune))

	s.Equal(0, s.ftsMatchCount(ctx, "Dune"),
		"deleted book must be purged from books_fts by the books_ad trigger")
	s.Equal(1, s.ftsMatchCount(ctx, "Solaris"), "surviving book must remain in the FTS index")
}

// ftsMatchCount returns how many rows in books_fts match the given query,
// reading the FTS table directly so trigger-driven cleanup can be asserted
// independently of the books join.
func (s *booksFilterSuite) ftsMatchCount(ctx context.Context, query string) int {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM books_fts WHERE books_fts MATCH ?", query).Scan(&n)
	s.Require().NoError(err)
	return n
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

// TestExactFacetsStayCaseSensitive confirms the fold term added for the index
// does not loosen an exact facet: a case variant of a stored name still finds
// nothing, and publisher case variants (stored side by side) stay apart.
func (s *booksFilterSuite) TestExactFacetsStayCaseSensitive() {
	ctx := context.Background()
	src := s.library()
	s.seed(src, bookSeed{
		key: "k1", title: "Западня", author: "Кевин Джеймс Андерсон", series: "Тёмные дуэты", publisher: "Ace",
	})
	s.seed(src, bookSeed{key: "k2", title: "Dune", publisher: "ACE"})

	variants := []BookFilter{
		{Author: FieldFilter{Value: "кевин джеймс андерсон", Exact: true}, Limit: 10},
		{Series: FieldFilter{Value: "Темные дуэты", Exact: true}, Limit: 10},
	}
	for i := range variants {
		books, err := FilterBooks(ctx, s.db, variants[i])
		s.Require().NoError(err)
		s.Empty(books, "%+v", variants[i])
	}

	books, err := FilterBooks(ctx, s.db, BookFilter{Publisher: "Ace", Limit: 10})
	s.Require().NoError(err)
	s.Equal([]string{"Западня"}, titles(books))
}

// TestExactTitleStaysExact confirms the FTS narrowing of an exact title keeps
// the comparison exact (a longer title sharing the tokens, or a case variant,
// is excluded), leaves the requested sort in charge, and still works for a
// title with no searchable token.
func (s *booksFilterSuite) TestExactTitleStaysExact() {
	ctx := context.Background()
	src := s.library()
	s.seed(src, bookSeed{key: "k1", title: "Dune", importedAt: 1})
	s.seed(src, bookSeed{key: "k2", title: "Dune", importedAt: 3})
	s.seed(src, bookSeed{key: "k3", title: "Dune Messiah", importedAt: 2})
	s.seed(src, bookSeed{key: "k4", title: "dune", importedAt: 4})
	s.seed(src, bookSeed{key: "k5", title: "?!", importedAt: 5})

	f := BookFilter{Title: FieldFilter{Value: "Dune", Exact: true}, Limit: 10}
	books, err := FilterBooks(ctx, s.db, f)
	s.Require().NoError(err)
	s.Equal([]string{"k2", "k1"}, lo.Map(books, func(b dbq.Book, _ int) string { return b.LibraryKey }),
		"newest import first, as with no title filter")
	n, err := CountFilteredBooks(ctx, s.db, f)
	s.Require().NoError(err)
	s.EqualValues(2, n)

	books, err = FilterBooks(ctx, s.db, BookFilter{Title: FieldFilter{Value: "?!", Exact: true}, Limit: 10})
	s.Require().NoError(err)
	s.Equal([]string{"?!"}, titles(books))

	books, err = FilterBooks(ctx, s.db, BookFilter{
		Query: "Messiah", Title: FieldFilter{Value: "Dune", Exact: true}, Limit: 10,
	})
	s.Require().NoError(err)
	s.Empty(books, "the free-text query and the exact title combine with AND")
}

// TestExactFacetsUseFoldIndex guards the plan of the exact facet filters on a
// database without ANALYZE statistics: each must look its name up through the
// fold index (a title, which has none, through the FTS index) rather than
// scan. Before the fold term, the count of an author's books walked every
// book_authors row.
func (s *booksFilterSuite) TestExactFacetsUseFoldIndex() {
	cases := []struct {
		filter BookFilter
		want   string
	}{
		{BookFilter{Author: FieldFilter{Value: "Кевин Джеймс Андерсон", Exact: true}}, "authors_1 (name_fold=?)"},
		{BookFilter{Series: FieldFilter{Value: "Dune Saga", Exact: true}}, "series_1 (name_fold=?)"},
		{BookFilter{Genre: "sf"}, "genres_1 (name_fold=?)"},
		{BookFilter{Publisher: "Ace"}, "idx_books_publisher_fold (publisher_fold=?)"},
		{BookFilter{Title: FieldFilter{Value: "Guards! Guards!", Exact: true}}, "VIRTUAL TABLE INDEX"},
	}
	for i := range cases {
		s.Contains(s.countPlan(cases[i].filter), cases[i].want, "%+v", cases[i].filter)
	}
}

// countPlan returns the EXPLAIN QUERY PLAN details of CountFilteredBooks's
// query for f, one step per line.
func (s *booksFilterSuite) countPlan(f BookFilter) string {
	ctx := context.Background()
	query, args, err := sqlite.Select(append(f.filterMods(), sm.Columns("COUNT(*)"))...).Build(ctx)
	s.Require().NoError(err)

	rows, err := s.db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query, args...)
	s.Require().NoError(err)
	defer func() { _ = rows.Close() }()

	var steps []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		s.Require().NoError(rows.Scan(&id, &parent, &unused, &detail))
		steps = append(steps, detail)
	}
	s.Require().NoError(rows.Err())

	return strings.Join(steps, "\n")
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

// TestPrefixTokenMatchesWordForms confirms a trailing * is an FTS5 prefix match
// (Cyrillic has no stemming), while a lone * and stray quotes stay inert.
func (s *booksFilterSuite) TestPrefixTokenMatchesWordForms() {
	ctx := context.Background()
	src := s.library()
	s.seed(src, bookSeed{key: "k1", title: "Убийца в поезде"})
	s.seed(src, bookSeed{key: "k2", title: "Убийцы по соседству"})
	s.seed(src, bookSeed{key: "k3", title: "Тихий дом"})

	books, err := FilterBooks(ctx, s.db, BookFilter{Query: "убийц*", Limit: 10})
	s.Require().NoError(err)
	s.ElementsMatch([]string{"Убийца в поезде", "Убийцы по соседству"}, titles(books))

	// Without the star the exact token matches nothing.
	books, err = FilterBooks(ctx, s.db, BookFilter{Query: "убийц", Limit: 10})
	s.Require().NoError(err)
	s.Empty(books)

	// Lone stars are dropped (no bare *), leaving no constraint.
	for _, q := range []string{"*", "***", "* *"} {
		n, cerr := CountFilteredBooks(ctx, s.db, BookFilter{Query: q})
		s.Require().NoError(cerr, q)
		s.EqualValues(3, n, q)
	}

	// Quotes next to a star cannot break out of the literal.
	for _, q := range []string{`"*`, `a" OR "b*`, `убийц"*`, `x") OR (title:"*`} {
		_, err = FilterBooks(ctx, s.db, BookFilter{Query: q, Limit: 10})
		s.Require().NoError(err, q)
	}
	s.Equal(`(("a""" AND "OR" AND """b"*))`, ftsClause("", `a" OR "b*`))
	s.Empty(ftsClause("", "* ***"))
}

// TestAnyTermsOrGroup confirms AnyTerms is an OR group ANDed with Query.
func (s *booksFilterSuite) TestAnyTermsOrGroup() {
	ctx := context.Background()
	src := s.library()
	s.seed(src, bookSeed{key: "k1", title: "Murder at dawn", author: "Smith"})
	s.seed(src, bookSeed{key: "k2", title: "Killer in the house", author: "Smith"})
	s.seed(src, bookSeed{key: "k3", title: "Quiet garden", author: "Smith"})
	s.seed(src, bookSeed{key: "k4", title: "Murder abroad", author: "Jones"})

	books, err := FilterBooks(ctx, s.db, BookFilter{AnyTerms: []string{"murder", "killer"}, Limit: 10})
	s.Require().NoError(err)
	s.ElementsMatch([]string{"Murder at dawn", "Killer in the house", "Murder abroad"}, titles(books))

	books, err = FilterBooks(ctx, s.db, BookFilter{Query: "smith", AnyTerms: []string{"murder", "killer"}, Limit: 10})
	s.Require().NoError(err)
	s.ElementsMatch([]string{"Murder at dawn", "Killer in the house"}, titles(books))

	// A multi-word term is an AND of its words; prefix works inside the group.
	books, err = FilterBooks(ctx, s.db, BookFilter{AnyTerms: []string{"killer house", "murd*", " ", "*"}, Limit: 10})
	s.Require().NoError(err)
	s.ElementsMatch([]string{"Murder at dawn", "Killer in the house", "Murder abroad"}, titles(books))

	books, err = FilterBooks(ctx, s.db, BookFilter{AnyTerms: []string{"killer garden"}, Limit: 10})
	s.Require().NoError(err)
	s.Empty(books)

	// Only blank terms: no constraint.
	n, err := CountFilteredBooks(ctx, s.db, BookFilter{AnyTerms: []string{"", "*"}})
	s.Require().NoError(err)
	s.EqualValues(4, n)

	// Operators inside a term stay literal.
	_, err = FilterBooks(ctx, s.db, BookFilter{AnyTerms: []string{`a" OR "b`, "NOT", "title:x"}, Limit: 10})
	s.NoError(err)
}

// TestYearRangeInclusive confirms YearFrom/YearTo bound books.year inclusively.
func (s *booksFilterSuite) TestYearRangeInclusive() {
	ctx := context.Background()
	src := s.library()
	s.seed(src, bookSeed{key: "k1", title: "Y1990", year: 1990})
	s.seed(src, bookSeed{key: "k2", title: "Y2000", year: 2000})
	s.seed(src, bookSeed{key: "k3", title: "Y2010", year: 2010})
	s.seed(src, bookSeed{key: "k4", title: "NoYear"})

	cases := []struct {
		name     string
		from, to int
		want     []string
	}{
		{"from", 2000, 0, []string{"Y2000", "Y2010"}},
		{"to", 0, 2000, []string{"Y1990", "Y2000"}},
		{"both", 2000, 2000, []string{"Y2000"}},
		{"range", 1991, 2009, []string{"Y2000"}},
	}
	for _, c := range cases {
		books, err := FilterBooks(ctx, s.db, BookFilter{YearFrom: c.from, YearTo: c.to, Limit: 10})
		s.Require().NoError(err, c.name)
		s.ElementsMatch(c.want, titles(books), c.name)
		n, err := CountFilteredBooks(ctx, s.db, BookFilter{YearFrom: c.from, YearTo: c.to})
		s.Require().NoError(err, c.name)
		s.EqualValues(len(c.want), n, c.name)
	}
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

func (s *booksFilterSuite) TestListLanguagesWithCounts() {
	ctx := context.Background()
	libA := s.libraryAt("A", "/a")
	libB := s.libraryAt("B", "/b")
	s.seed(libA, bookSeed{key: "a1", title: "A1", lang: "en"})
	s.seed(libA, bookSeed{key: "a2", title: "A2", lang: "en"})
	s.seed(libA, bookSeed{key: "a3", title: "A3", lang: "ru"})
	s.seed(libB, bookSeed{key: "b1", title: "B1", lang: "ru"})
	s.seed(libB, bookSeed{key: "b2", title: "B2", lang: "ru"})
	s.seed(libB, bookSeed{key: "b3", title: "B3", lang: "de"})
	s.seed(libB, bookSeed{key: "b4", title: "B4", lang: "fr"})

	all, err := s.q.ListLanguagesWithCounts(ctx, 0)
	s.Require().NoError(err)
	s.Equal([]dbq.ListLanguagesWithCountsRow{
		{Language: "ru", BookCount: 3},
		{Language: "en", BookCount: 2},
		{Language: "de", BookCount: 1},
		{Language: "fr", BookCount: 1},
	}, all)

	a, err := s.q.ListLanguagesWithCounts(ctx, libA)
	s.Require().NoError(err)
	s.Equal([]dbq.ListLanguagesWithCountsRow{
		{Language: "en", BookCount: 2},
		{Language: "ru", BookCount: 1},
	}, a)
}

// TestPunctuationOnlyTokensAreDropped confirms a token with no letters or digits
// (a spaced dash, "-*") never becomes an empty FTS phrase that zeroes the search.
func (s *booksFilterSuite) TestPunctuationOnlyTokensAreDropped() {
	ctx := context.Background()
	src := s.library()
	s.seed(src, bookSeed{key: "k1", title: "Умный убийца и глупый сыщик"})
	s.seed(src, bookSeed{key: "k2", title: "Тихий дом"})

	for _, q := range []string{"убийца — сыщик", "убийца - сыщик", "убийца & сыщик", "убийца -* сыщик", "убийца …"} {
		books, err := FilterBooks(ctx, s.db, BookFilter{Query: q, Limit: 10})
		s.Require().NoError(err, q)
		s.Equal([]string{"Умный убийца и глупый сыщик"}, titles(books), q)
	}

	// Field filters and any_terms alternatives behave the same way.
	books, err := FilterBooks(ctx, s.db, BookFilter{Title: FieldFilter{Value: "умный — убийца"}, Limit: 10})
	s.Require().NoError(err)
	s.Len(books, 1)
	books, err = FilterBooks(ctx, s.db, BookFilter{AnyTerms: []string{"убийца — сыщик"}, Limit: 10})
	s.Require().NoError(err)
	s.Len(books, 1)

	// A value of only punctuation adds no constraint.
	for _, q := range []string{"—", "-*", "- — &"} {
		n, err := CountFilteredBooks(ctx, s.db, BookFilter{Query: q})
		s.Require().NoError(err, q)
		s.EqualValues(2, n, q)
		s.False(HasSearchTokens(q), q)
	}
	s.True(HasSearchTokens("— убийца"))
	s.Empty(ftsClause("", "— -*"))
}

func (s *booksFilterSuite) TestSearchTerms() {
	s.Equal([]SearchTerm{
		{},
		{Prefix: true, PrefixRunes: 6},
		{Prefix: true, PrefixRunes: 2},
	}, SearchTerms(`убийца murder* co-op.* — -*`))
	s.Empty(SearchTerms("— -* *"))
}

func (s *booksFilterSuite) failMetadata(bookID int64, flow string, version int64) {
	_, err := s.db.ExecContext(context.Background(),
		`INSERT INTO book_read_failures (book_id, flow, reason, fingerprint, reader_version, recorded_at)
		 VALUES (?, ?, 'corrupt', 'fp', ?, 1)`, bookID, flow, version)
	s.Require().NoError(err)
}

// TestUnreadableFilterSelectsCurrentMetadataFailures confirms the filter keeps
// only current-version metadata failures, scopes by library, combines with FTS,
// and that Count agrees with the listing.
func (s *booksFilterSuite) TestUnreadableFilterSelectsCurrentMetadataFailures() {
	ctx := context.Background()
	a := s.libraryAt("A", "/a")
	b := s.libraryAt("B", "/b")
	broken := s.seed(a, bookSeed{key: "k1", title: "Broken Dune"})
	brokenOther := s.seed(a, bookSeed{key: "k2", title: "Broken Emma"})
	coverOnly := s.seed(a, bookSeed{key: "k3", title: "Cover Dune"})
	stale := s.seed(a, bookSeed{key: "k4", title: "Stale Dune"})
	s.seed(a, bookSeed{key: "k5", title: "Fine Dune"})
	inB := s.seed(b, bookSeed{key: "k6", title: "Other Dune"})
	s.failMetadata(broken, "metadata", 2)
	s.failMetadata(brokenOther, "metadata", 2)
	s.failMetadata(coverOnly, "cover", 2)
	s.failMetadata(stale, "metadata", 1)
	s.failMetadata(inB, "metadata", 2)

	check := func(f BookFilter, want ...int64) {
		f.Unreadable, f.ReaderVersion, f.Limit = true, 2, 50
		got, err := FilterBooks(ctx, s.db, f)
		s.Require().NoError(err)
		ids := lo.Map(got, func(b dbq.Book, _ int) int64 { return b.ID })
		s.ElementsMatch(want, ids)
		n, err := CountFilteredBooks(ctx, s.db, f)
		s.Require().NoError(err)
		s.Equal(int64(len(want)), n)
	}
	check(BookFilter{LibraryID: a}, broken, brokenOther)
	check(BookFilter{}, broken, brokenOther, inB)
	check(BookFilter{LibraryID: a, Query: "dune"}, broken)

	// Off by default: the zero value does not restrict.
	all, err := CountFilteredBooks(ctx, s.db, BookFilter{LibraryID: a})
	s.Require().NoError(err)
	s.Equal(int64(5), all)
}

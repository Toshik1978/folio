package mcpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/suite"

	"github.com/Toshik1978/folio/internal/db"
	"github.com/Toshik1978/folio/internal/db/dbq"
)

const testPublicURL = "https://folio.example.com"

// toolsSuite drives the four discovery tools through an in-memory MCP client
// against a freshly migrated fixture database.
type toolsSuite struct {
	suite.Suite

	db  *sql.DB
	q   *dbq.Queries
	cs  *mcp.ClientSession
	ss  *mcp.ServerSession
	ids map[string]int64
	lib map[string]int64
}

type fixtureBook struct {
	key, title, lang, annotation, author, series, genre, format, lib string
	seriesNum                                                        float64
	year                                                             int
	isbn                                                             string
	publisher                                                        string
	rating                                                           int
}

func (s *toolsSuite) SetupTest() {
	ctx := context.Background()
	database, err := db.Open(slog.New(slog.DiscardHandler), s.T().TempDir())
	s.Require().NoError(err)
	s.db = database
	s.q = dbq.New(database)
	s.ids = map[string]int64{}
	s.lib = map[string]int64{}

	s.lib["inpx"] = s.addLibrary("INPX Archive", "inpx", "/library/inpx")
	s.lib["calibre"] = s.addLibrary("Calibre", "calibre", "/calibre")

	base := time.Now().UnixNano()
	books := []fixtureBook{
		{
			key: "orient", title: "Убийство в Восточном экспрессе", lang: "ru", lib: "inpx",
			annotation: "<p>Знаменитый сыщик Эркюль Пуаро расследует <b>убийство</b> в поезде.</p>",
			author:     "Агата Кристи", series: "Эркюль Пуаро", seriesNum: 10, genre: "Детектив",
			format: "fb2", year: 1934, isbn: "9785041234567", publisher: "Эксмо", rating: 4,
		},
		{
			key: "killer", title: "Убийца поневоле", lang: "ru", lib: "inpx",
			annotation: "Наёмный убийца уходит на покой.", author: "Иван Петров",
			genre: "Детектив", format: "fb2", year: 2001,
		},
		{
			key: "master", title: "Мастер и Маргарита", lang: "ru", lib: "inpx",
			annotation: "Дьявол посещает Москву.", author: "Михаил Булгаков",
			genre: "Классика", format: "fb2", year: 1967,
		},
		{
			key: "picnic", title: "Пикник на обочине", lang: "ru", lib: "inpx",
			annotation: "Сталкеры ходят в Зону за артефактами.", author: "Аркадий Стругацкий",
			genre: "Фантастика", format: "fb2", year: 1972,
		},
		{
			key: "dune", title: "Dune", lang: "en", lib: "calibre",
			annotation: "A desert planet.", author: "Frank Herbert",
			genre: "Science Fiction", format: "epub", year: 1965,
		},
	}
	for i := range books {
		s.ids[books[i].key] = s.addBook(&books[i], base+int64(i))
	}

	srv := newMCPServer(slog.New(slog.DiscardHandler), database, testPublicURL, "test")
	st, ct := mcp.NewInMemoryTransports()
	s.ss, err = srv.Connect(ctx, st, nil)
	s.Require().NoError(err)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	s.cs, err = client.Connect(ctx, ct, nil)
	s.Require().NoError(err)
}

func (s *toolsSuite) TearDownTest() {
	if s.cs != nil {
		_ = s.cs.Close()
	}
	if s.ss != nil {
		_ = s.ss.Close()
	}
	if s.db != nil {
		_ = s.db.Close()
	}
}

func (s *toolsSuite) addLibrary(name, typ, path string) int64 {
	id, err := s.q.InsertLibrary(context.Background(), dbq.InsertLibraryParams{
		Name: name, Type: typ, Path: path, SyncIntervalSeconds: 3600, CreatedAt: time.Now().UnixNano(),
	})
	s.Require().NoError(err)

	return id
}

func (s *toolsSuite) addBook(b *fixtureBook, imported int64) int64 {
	ctx := context.Background()
	var seriesID sql.NullInt64
	if b.series != "" {
		sid, err := s.q.InsertSeries(ctx, dbq.InsertSeriesParams{Name: b.series, NameFold: db.Fold(b.series)})
		s.Require().NoError(err)
		seriesID = sql.NullInt64{Int64: sid, Valid: true}
	}
	id, err := s.q.InsertBook(ctx, dbq.InsertBookParams{
		LibraryID: s.lib[b.lib], LibraryKey: b.key, Title: b.title, Language: b.lang,
		SeriesID:     seriesID,
		SeriesNumber: sql.NullFloat64{Float64: b.seriesNum, Valid: b.seriesNum != 0},
		Annotation:   sql.NullString{String: b.annotation, Valid: b.annotation != ""},
		Publisher:    sql.NullString{String: b.publisher, Valid: b.publisher != ""},
		Year:         sql.NullInt64{Int64: int64(b.year), Valid: b.year != 0},
		Rating:       sql.NullInt64{Int64: int64(b.rating), Valid: b.rating != 0},
		ContentHash:  b.key, AddedAt: imported, ImportedAt: imported,
	})
	s.Require().NoError(err)

	aid, err := s.q.InsertAuthor(ctx, dbq.InsertAuthorParams{Name: b.author, NameFold: db.Fold(b.author)})
	s.Require().NoError(err)
	s.Require().NoError(s.q.InsertBookAuthor(ctx, dbq.InsertBookAuthorParams{BookID: id, AuthorID: aid}))
	gid, err := s.q.InsertGenre(ctx, dbq.InsertGenreParams{Name: b.genre, NameFold: db.Fold(b.genre)})
	s.Require().NoError(err)
	s.Require().NoError(s.q.InsertBookGenre(ctx, dbq.InsertBookGenreParams{BookID: id, GenreID: gid}))
	_, err = s.q.InsertBookFile(ctx, dbq.InsertBookFileParams{
		BookID: id, FileFormat: b.format, FileSize: 100, SourcePath: b.key + "." + b.format, Mtime: 1,
	})
	s.Require().NoError(err)
	if b.isbn != "" {
		s.Require().NoError(s.q.InsertBookIdentifier(ctx, dbq.InsertBookIdentifierParams{
			BookID: id, Type: "isbn", Value: b.isbn,
		}))
	}
	s.Require().NoError(db.RefreshBookFTS(ctx, s.db, id))

	return id
}

// call invokes a tool and decodes its structured output into out (when the call
// succeeded). It returns the raw result for error assertions.
func (s *toolsSuite) call(name string, args, out any) *mcp.CallToolResult {
	res, err := s.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	s.Require().NoError(err, "tool errors must not surface as protocol errors")
	if !res.IsError && out != nil {
		raw, mErr := json.Marshal(res.StructuredContent)
		s.Require().NoError(mErr)
		s.Require().NoError(json.Unmarshal(raw, out))
	}

	return res
}

// toolError asserts res is an isError result and returns its text.
func (s *toolsSuite) toolError(res *mcp.CallToolResult) string {
	s.Require().True(res.IsError, "expected a tool error")
	s.Require().NotEmpty(res.Content)
	tc, ok := res.Content[0].(*mcp.TextContent)
	s.Require().True(ok)

	return tc.Text
}

func (s *toolsSuite) search(args map[string]any) searchBooksOutput {
	var out searchBooksOutput
	res := s.call("search_books", args, &out)
	s.Require().False(res.IsError, "unexpected tool error: %v", res.Content)

	return out
}

func bookIDs(books []BookSummary) []int64 {
	out := make([]int64, len(books))
	for i := range books {
		out[i] = books[i].ID
	}

	return out
}

func (s *toolsSuite) TestToolsListedReadOnly() {
	res, err := s.cs.ListTools(context.Background(), nil)
	s.Require().NoError(err)
	names := map[string]bool{}
	for _, t := range res.Tools {
		names[t.Name] = true
		s.Require().NotNil(t.Annotations, t.Name)
		s.True(t.Annotations.ReadOnlyHint, t.Name)
		s.NotEmpty(t.Description, t.Name)
	}
	for _, n := range []string{"list_libraries", "list_facets", "search_books", "get_book", "similar_books"} {
		s.True(names[n], "tool %s registered", n)
	}
}

func (s *toolsSuite) TestListLibraries() {
	var out listLibrariesOutput
	res := s.call("list_libraries", map[string]any{}, &out)
	s.Require().False(res.IsError)
	s.Equal([]LibraryInfo{
		{ID: s.lib["inpx"], Name: "INPX Archive", Type: "inpx", BookCount: 4},
		{ID: s.lib["calibre"], Name: "Calibre", Type: "calibre", BookCount: 1},
	}, out.Libraries)
}

func (s *toolsSuite) TestListFacetsAll() {
	var out listFacetsOutput
	res := s.call("list_facets", map[string]any{}, &out)
	s.Require().False(res.IsError)
	s.Require().NotEmpty(out.Genres)
	s.Equal(GenreCount{Name: "Детектив", BookCount: 2}, out.Genres[0], "sorted by count desc")
	s.Len(out.Genres, 4)
	s.Equal([]LanguageCount{{Code: "ru", BookCount: 4}, {Code: "en", BookCount: 1}}, out.Languages)
}

func (s *toolsSuite) TestListFacetsByLibrary() {
	var out listFacetsOutput
	res := s.call("list_facets", map[string]any{"library_id": s.lib["calibre"]}, &out)
	s.Require().False(res.IsError)
	s.Equal([]GenreCount{{Name: "Science Fiction", BookCount: 1}}, out.Genres)
	s.Equal([]LanguageCount{{Code: "en", BookCount: 1}}, out.Languages)
}

func (s *toolsSuite) TestSearchPrefixCyrillic() {
	out := s.search(map[string]any{"query": "убийц*"})
	s.Equal(int64(1), out.Total)
	s.Equal([]int64{s.ids["killer"]}, bookIDs(out.Books))

	wider := s.search(map[string]any{"query": "убий*"})
	s.Equal(int64(2), wider.Total)
	s.ElementsMatch([]int64{s.ids["killer"], s.ids["orient"]}, bookIDs(wider.Books))

	exact := s.search(map[string]any{"query": "убийц"})
	s.Equal(int64(0), exact.Total, "without * the whole word must match")
	s.NotNil(exact.Books, "books must serialize as [] not null")
}

func (s *toolsSuite) TestSearchAnyTermsCyrillic() {
	out := s.search(map[string]any{"any_terms": []string{"сыщик", "сталкер*"}})
	s.Equal(int64(2), out.Total)
	s.ElementsMatch([]int64{s.ids["orient"], s.ids["picnic"]}, bookIDs(out.Books))

	anded := s.search(map[string]any{"query": "поезде", "any_terms": []string{"сыщик", "сталкер*"}})
	s.Equal([]int64{s.ids["orient"]}, bookIDs(anded.Books), "any_terms is ANDed with query")
}

func (s *toolsSuite) TestSearchSummaryShape() {
	out := s.search(map[string]any{"author": "Кристи"})
	s.Require().Len(out.Books, 1)
	b := out.Books[0]
	s.Equal(s.ids["orient"], b.ID)
	s.Equal("Убийство в Восточном экспрессе", b.Title)
	s.Equal([]string{"Агата Кристи"}, b.Authors)
	s.Require().NotNil(b.Series)
	s.Equal("Эркюль Пуаро", *b.Series)
	s.Require().NotNil(b.SeriesNumber)
	s.InDelta(10.0, *b.SeriesNumber, 0)
	s.Equal([]string{"Детектив"}, b.Genres)
	s.Equal("ru", b.Language)
	s.Require().NotNil(b.Year)
	s.Equal(1934, *b.Year)
	s.Equal("Знаменитый сыщик Эркюль Пуаро расследует убийство в поезде.", b.Excerpt)
	s.True(b.HasDescription)
	s.Equal(fmt.Sprintf("%s/books/%d", testPublicURL, b.ID), b.Link)
}

func (s *toolsSuite) TestSearchFieldsAndFacets() {
	s.Equal([]int64{s.ids["master"]}, bookIDs(s.search(map[string]any{"title": "маргарита"}).Books))
	s.Equal([]int64{s.ids["orient"]}, bookIDs(s.search(map[string]any{"series": "пуаро"}).Books))
	s.Equal([]int64{s.ids["dune"]},
		bookIDs(s.search(map[string]any{"genre": "Science Fiction", "format": "epub", "language": "en"}).Books))
	s.Empty(s.search(map[string]any{"genre": "Science Fiction", "library_id": s.lib["inpx"]}).Books)

	years := s.search(map[string]any{"genre": "Детектив", "year_from": 1930, "year_to": 1940})
	s.Equal([]int64{s.ids["orient"]}, bookIDs(years.Books))
}

func (s *toolsSuite) TestSearchPagination() {
	first := s.search(map[string]any{"genre": "Детектив", "limit": 1})
	s.Equal(int64(2), first.Total)
	s.Equal(0, first.Offset)
	s.Require().Len(first.Books, 1)
	// No text terms: most recently added first ("killer" was imported after "orient").
	s.Equal(s.ids["killer"], first.Books[0].ID)

	second := s.search(map[string]any{"genre": "Детектив", "limit": 1, "offset": 1})
	s.Equal(int64(2), second.Total)
	s.Equal(1, second.Offset)
	s.Equal([]int64{s.ids["orient"]}, bookIDs(second.Books))

	neg := s.search(map[string]any{"genre": "Детектив", "offset": -5, "limit": 500})
	s.Equal(0, neg.Offset, "negative offset becomes 0")
	s.Len(neg.Books, 2)
}

func (s *toolsSuite) TestPageBounds() {
	limit, offset := pageBounds(0, 0)
	s.Equal(20, limit)
	s.Equal(0, offset)
	limit, offset = pageBounds(500, -3)
	s.Equal(50, limit)
	s.Equal(0, offset)
	limit, offset = pageBounds(-1, 7)
	s.Equal(20, limit)
	s.Equal(7, offset)
	limit, _ = pageBounds(33, 0)
	s.Equal(33, limit)
}

func (s *toolsSuite) TestSearchEmptyCriteria() {
	for _, args := range []map[string]any{
		{},
		{"language": "ru", "library_id": s.lib["inpx"]},
		{"query": "  ", "any_terms": []string{"", " "}},
		{"query": "*"},
	} {
		msg := s.toolError(s.call("search_books", args, nil))
		s.Contains(msg, "query")
		s.Contains(msg, "any_terms")
		s.Contains(msg, "genre")
	}
}

func (s *toolsSuite) TestSearchRejectsOversizedInput() {
	terms := make([]string, maxAnyTerms+1)
	for i := range terms {
		terms[i] = "a*"
	}
	msg := s.toolError(s.call("search_books", map[string]any{"any_terms": terms}, nil))
	s.Equal("any_terms takes at most 20 terms", msg)

	msg = s.toolError(s.call("search_books", map[string]any{"title": strings.Repeat("я", maxTextRunes+1)}, nil))
	s.Contains(msg, "at most 500 characters")

	var out searchBooksOutput
	res := s.call("search_books", map[string]any{"query": strings.Repeat("я", maxTextRunes)}, &out)
	s.False(res.IsError, "the bound is inclusive")
}

func (s *toolsSuite) TestSearchBoundsPrefixAndWordCounts() {
	msg := s.toolError(s.call("search_books", map[string]any{"query": "убийца по*"}, nil))
	s.Equal("a prefix term needs at least 3 letters before the *", msg)

	msg = s.toolError(s.call("search_books", map[string]any{
		"query": strings.Repeat("при* ", maxPrefixTerms+1),
	}, nil))
	s.Equal("a search takes at most 10 prefix (*) terms", msg)

	msg = s.toolError(s.call("search_books", map[string]any{
		"query": strings.Repeat("слово ", maxSearchTerms/2), "title": strings.Repeat("книга ", maxSearchTerms/2+1),
	}, nil))
	s.Equal("a search takes at most 40 words across all fields", msg)

	var out searchBooksOutput
	res := s.call("search_books", map[string]any{
		"query": strings.Repeat("при* ", maxPrefixTerms) + strings.Repeat("слово ", maxSearchTerms-maxPrefixTerms),
	}, &out)
	s.False(res.IsError, "the bounds are inclusive")
}

func (s *toolsSuite) TestSearchUnknownGenre() {
	msg := s.toolError(s.call("search_books", map[string]any{"genre": "Нет такого"}, nil))
	s.Equal("genre not found; call list_facets", msg)
}

func (s *toolsSuite) TestSearchErrorClassification() {
	err := searchError(fmt.Errorf("query books: %w", errors.New(`SQL logic error: fts5: syntax error near "AND" (1)`)))
	s.Equal(`invalid query: syntax error near "AND"`, err.Error())

	other := searchError(errors.New("disk I/O error"))
	s.Equal("search failed", other.Error())
}

func (s *toolsSuite) TestGetBook() {
	var out BookDetail
	res := s.call("get_book", map[string]any{"id": s.ids["orient"]}, &out)
	s.Require().False(res.IsError, "unexpected tool error: %v", res.Content)
	s.Equal(s.ids["orient"], out.ID)
	s.Equal([]string{"Агата Кристи"}, out.Authors)
	s.Require().NotNil(out.Description)
	s.Equal("Знаменитый сыщик Эркюль Пуаро расследует убийство в поезде.", *out.Description)
	s.Require().NotNil(out.Publisher)
	s.Equal("Эксмо", *out.Publisher)
	s.Require().NotNil(out.Rating)
	s.Equal(4, *out.Rating)
	s.Equal([]Identifier{{Type: "isbn", Value: "9785041234567"}}, out.Identifiers)
	s.Equal([]string{"fb2"}, out.Formats)
	s.Equal(LibraryRef{ID: s.lib["inpx"], Name: "INPX Archive"}, out.Library)
	s.Equal(fmt.Sprintf("%s/books/%d", testPublicURL, out.ID), out.Link)
	s.Equal([]string{"Детектив"}, out.Genres)

	var plain BookDetail
	s.call("get_book", map[string]any{"id": s.ids["killer"]}, &plain)
	s.NotNil(plain.Identifiers)
	s.Empty(plain.Identifiers)
	s.Nil(plain.Series)
}

func (s *toolsSuite) TestGetBookNotFound() {
	msg := s.toolError(s.call("get_book", map[string]any{"id": 999999}, nil))
	s.Equal("book not found", msg)
}

// fingerprint captures every table the tools read, so a test can prove the
// calls changed nothing.
func (s *toolsSuite) fingerprint() string {
	var fp string
	err := s.db.QueryRowContext(context.Background(), `
SELECT (SELECT COUNT(*) FROM libraries) || '|' ||
       (SELECT COUNT(*) FROM books_fts) || '|' ||
       (SELECT COUNT(*) FROM authors) || '|' || (SELECT COUNT(*) FROM book_authors) || '|' ||
       (SELECT COUNT(*) FROM genres) || '|' || (SELECT COUNT(*) FROM book_genres) || '|' ||
       (SELECT COUNT(*) FROM series) || '|' || (SELECT COUNT(*) FROM book_files) || '|' ||
       (SELECT COUNT(*) FROM book_identifiers) || '|' ||
       (SELECT group_concat(id || ':' || title || ':' || COALESCE(annotation, '') || ':' ||
                            metadata_checked || ':' || enrichment_checked || ':' || cover_state || ':' ||
                            COALESCE(publisher, '') || ':' || COALESCE(year, '') || ':' || content_hash, ';')
        FROM (SELECT * FROM books ORDER BY id))`).Scan(&fp)
	s.Require().NoError(err)

	return fp
}

func (s *toolsSuite) TestNoWrites() {
	before := s.fingerprint()

	s.call("list_libraries", map[string]any{}, nil)
	s.call("list_facets", map[string]any{}, nil)
	s.call("search_books", map[string]any{"query": "убий*"}, nil)
	s.call("search_books", map[string]any{"genre": "Нет такого"}, nil)
	for _, id := range s.ids {
		s.call("get_book", map[string]any{"id": id}, nil)
	}
	s.call("get_book", map[string]any{"id": 999999}, nil)

	s.Equal(before, s.fingerprint())
}

func (s *toolsSuite) TestSimilarBooks() {
	mate := s.addBook(&fixtureBook{
		key: "mate", title: "Смерть на Ниле", lang: "ru", lib: "inpx",
		annotation: "<p>Пуаро снова в деле.</p>", author: "Агата Кристи", series: "Эркюль Пуаро",
		seriesNum: 17, genre: "Детектив", format: "fb2", year: 1937,
	}, time.Now().UnixNano())

	var out similarBooksOutput
	res := s.call("similar_books", map[string]any{"id": s.ids["orient"]}, &out)
	s.Require().False(res.IsError, "unexpected tool error: %v", res.Content)

	s.Equal(s.ids["orient"], out.Source.ID)
	s.Equal("Убийство в Восточном экспрессе", out.Source.Title)
	s.Equal(fmt.Sprintf("%s/books/%d", testPublicURL, s.ids["orient"]), out.Source.Link)
	s.Require().NotEmpty(out.Candidates)

	var first *SimilarCandidate
	reasons := map[int64]string{}
	for i := range out.Candidates {
		c := &out.Candidates[i]
		s.NotEqual(s.ids["orient"], c.ID, "source must not be a candidate")
		s.NotEmpty(c.Link)
		reasons[c.ID] = c.Reason
		if c.ID == mate {
			first = c
		}
		if c.Reason == "distinctive_terms" {
			s.NotEmpty(c.MatchedTerms, "distinctive_terms carries matched_terms")
		} else {
			s.Empty(c.MatchedTerms)
		}
	}
	s.Require().NotNil(first)
	s.Equal("Смерть на Ниле", first.Title)
	s.Equal([]string{"Агата Кристи"}, first.Authors)
	s.Equal(fmt.Sprintf("%s/books/%d", testPublicURL, mate), first.Link)
	s.Contains([]string{"distinctive_terms", "same_series"}, first.Reason)
	s.Contains(reasons, s.ids["killer"])
	s.Contains([]string{"distinctive_terms", "shared_genres"}, reasons[s.ids["killer"]])
}

func (s *toolsSuite) TestSimilarBooksOmitsEmptyMatchedTerms() {
	res := s.call("similar_books", map[string]any{"id": s.ids["orient"], "limit": 5}, nil)
	s.Require().False(res.IsError)
	raw, err := json.Marshal(res.StructuredContent)
	s.Require().NoError(err)
	s.NotContains(string(raw), `"matched_terms":null`)
	s.Contains(string(raw), `"candidates"`)
}

func (s *toolsSuite) TestSimilarBooksUnknownID() {
	res := s.call("similar_books", map[string]any{"id": 999999}, nil)
	s.Equal(errBookNotFound, s.toolError(res))
}

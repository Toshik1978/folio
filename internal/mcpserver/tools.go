package mcpserver

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Toshik1978/folio/internal/db"
	"github.com/Toshik1978/folio/internal/db/dbq"
)

const (
	defaultLimit = 20
	maxLimit     = 50
	// maxFacetGenres caps list_facets genres to the most common ones.
	maxFacetGenres = 200
	// allGenres is the SQLite LIMIT meaning "no limit", for ListGenres.
	allGenres = -1
	// maxAnyTerms, maxTextRunes, maxSearchTerms, maxPrefixTerms and
	// minPrefixRunes bound a search_books request: every prefix term expands
	// across the FTS vocabulary and the match runs twice (page and count), so
	// an unbounded request could hold a reader for seconds. On a 580k-book
	// catalog ten 2-letter prefixes take over a second; ten 3-letter ones a
	// tenth of that.
	maxAnyTerms    = 20
	maxTextRunes   = 500
	maxSearchTerms = 40
	maxPrefixTerms = 10
	minPrefixRunes = 3
)

// Tool error messages, written for the model calling the tool.
const (
	errNoCriteria    = "give at least one of query, any_terms, title, author, series or genre"
	errGenreNotFound = "genre not found; call list_facets"
	errBookNotFound  = "book not found"
	errSearchFailed  = "search failed"
)

// LibraryInfo is one catalog source in list_libraries.
type LibraryInfo struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	BookCount int64  `json:"book_count"`
}

// GenreCount is a genre name with its book count.
type GenreCount struct {
	Name      string `json:"name"`
	BookCount int64  `json:"book_count"`
}

// LanguageCount is a language code with its book count.
type LanguageCount struct {
	Code      string `json:"code"`
	BookCount int64  `json:"book_count"`
}

type listLibrariesInput struct{}

type listLibrariesOutput struct {
	Libraries []LibraryInfo `json:"libraries"`
}

//nolint:lll,revive // jsonschema descriptions are model-facing prose and cannot be wrapped.
type listFacetsInput struct {
	LibraryID int64 `json:"library_id,omitempty" jsonschema:"Restrict counts to one library id from list_libraries; 0 or absent means all libraries."`
}

type listFacetsOutput struct {
	Genres    []GenreCount    `json:"genres"`
	Languages []LanguageCount `json:"languages"`
}

//nolint:lll,revive // jsonschema descriptions are model-facing prose and cannot be wrapped.
type searchBooksInput struct {
	Query     string   `json:"query,omitempty"      jsonschema:"Words that must all match, across title, authors, series and description. Whole words, no stemming: end a word with * to match it as a prefix (e.g. murder* finds murder, murderer); a prefix needs at least 3 letters, and a search takes at most 10 prefixes and 40 words."`
	AnyTerms  []string `json:"any_terms,omitempty"  jsonschema:"Alternatives of which at least one must match, ANDed with query. Use it for synonyms and word forms (e.g. [\"sleuth*\", \"detective*\"]). Each term may end with *; a multi-word term needs all its words."`
	Title     string   `json:"title,omitempty"      jsonschema:"Words that must appear in the title. A trailing * means prefix."`
	Author    string   `json:"author,omitempty"     jsonschema:"Words that must appear in an author name. A trailing * means prefix."`
	Series    string   `json:"series,omitempty"     jsonschema:"Words that must appear in the series name. A trailing * means prefix."`
	Genre     string   `json:"genre,omitempty"      jsonschema:"An exact genre name as returned by list_facets."`
	Language  string   `json:"language,omitempty"   jsonschema:"An ISO language code as returned by list_facets, e.g. ru or en."`
	LibraryID int64    `json:"library_id,omitempty" jsonschema:"Restrict to one library id from list_libraries."`
	Format    string   `json:"format,omitempty"     jsonschema:"A file format, e.g. fb2 or epub."`
	YearFrom  int      `json:"year_from,omitempty"  jsonschema:"Earliest publication year, inclusive."`
	YearTo    int      `json:"year_to,omitempty"    jsonschema:"Latest publication year, inclusive."`
	Limit     int      `json:"limit,omitempty"      jsonschema:"Page size; default 20, at most 50."`
	Offset    int      `json:"offset,omitempty"     jsonschema:"Number of results to skip, for paging; default 0."`
}

type searchBooksOutput struct {
	Total  int64         `json:"total"`
	Offset int           `json:"offset"`
	Books  []BookSummary `json:"books"`
}

type getBookInput struct {
	ID int64 `json:"id" jsonschema:"A book id from search_books."`
}

type similarBooksInput struct {
	ID    int64 `json:"id"              jsonschema:"The source book id from search_books."`
	Limit int   `json:"limit,omitempty" jsonschema:"Number of candidates; default 20, at most 50."`
}

// SimilarCandidate is a BookSummary plus why the book was offered. MatchedTerms
// is set only for the distinctive_terms reason.
type SimilarCandidate struct {
	BookSummary

	Reason       string   `json:"reason"`
	MatchedTerms []string `json:"matched_terms,omitempty"`
}

type similarBooksOutput struct {
	Source     BookSummary        `json:"source"`
	Candidates []SimilarCandidate `json:"candidates"`
}

// readOnly is the annotation every Folio tool carries.
func readOnly() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{ReadOnlyHint: true}
}

// registerTools adds every tool to srv.
func (s *service) registerTools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_libraries",
		Description: "List the catalog's libraries (sources) with their ids, types and book counts.",
		Annotations: readOnly(),
	}, s.listLibraries)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "list_facets",
		Description: "List the genres (top 200 by book count) and languages present in the catalog, " +
			"for use as exact genre/language filters in search_books.",
		Annotations: readOnly(),
	}, s.listFacets)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "search_books",
		Description: "Search the catalog. Give at least one of query, any_terms, title, author, series or genre; " +
			"narrow with language, library_id, format and years. Results are ranked by relevance when there " +
			"are text terms, otherwise most recently added first. Each book has a link to give the user.",
		Annotations: readOnly(),
	}, s.searchBooks)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_book",
		Description: "Get one book's full description and metadata by id.",
		Annotations: readOnly(),
	}, s.getBook)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "similar_books",
		Description: "Find books like a given one, in rank order: shared distinctive description terms, " +
			"same series, same author, shared genres. Each candidate has a reason and a link to give the user.",
		Annotations: readOnly(),
	}, s.similarBooks)
}

func (s *service) listLibraries(
	ctx context.Context, _ *mcp.CallToolRequest, _ listLibrariesInput,
) (*mcp.CallToolResult, listLibrariesOutput, error) {
	rows, err := dbq.New(s.db).ListLibrariesWithBookCount(ctx)
	if err != nil {
		return nil, listLibrariesOutput{}, s.internal("list libraries", err)
	}
	out := listLibrariesOutput{Libraries: make([]LibraryInfo, 0, len(rows))}
	for i := range rows {
		out.Libraries = append(out.Libraries, LibraryInfo{
			ID: rows[i].ID, Name: rows[i].Name, Type: rows[i].Type, BookCount: rows[i].BookCount,
		})
	}

	return nil, out, nil
}

func (s *service) listFacets(
	ctx context.Context, _ *mcp.CallToolRequest, in listFacetsInput,
) (*mcp.CallToolResult, listFacetsOutput, error) {
	q := dbq.New(s.db)
	genres, err := q.ListGenres(ctx, dbq.ListGenresParams{LibraryID: in.LibraryID, Lim: allGenres})
	if err != nil {
		return nil, listFacetsOutput{}, s.internal("list genres", err)
	}
	// ListGenres is name-ordered; a stable sort keeps that as the tie-break.
	slices.SortStableFunc(genres, func(a, b dbq.ListGenresRow) int { return cmp.Compare(b.BookCount, a.BookCount) })
	genres = genres[:min(len(genres), maxFacetGenres)]

	langs, err := q.ListLanguagesWithCounts(ctx, in.LibraryID)
	if err != nil {
		return nil, listFacetsOutput{}, s.internal("list languages", err)
	}

	out := listFacetsOutput{
		Genres:    make([]GenreCount, 0, len(genres)),
		Languages: make([]LanguageCount, 0, len(langs)),
	}
	for i := range genres {
		out.Genres = append(out.Genres, GenreCount{Name: genres[i].Name, BookCount: genres[i].BookCount})
	}
	for i := range langs {
		out.Languages = append(out.Languages, LanguageCount{Code: langs[i].Language, BookCount: langs[i].BookCount})
	}

	return nil, out, nil
}

func (s *service) searchBooks(
	ctx context.Context, _ *mcp.CallToolRequest, in searchBooksInput,
) (*mcp.CallToolResult, searchBooksOutput, error) {
	if !in.hasCriteria() {
		return nil, searchBooksOutput{}, errors.New(errNoCriteria)
	}
	if err := in.checkBounds(); err != nil {
		return nil, searchBooksOutput{}, err
	}
	q := dbq.New(s.db)
	genre := strings.TrimSpace(in.Genre)
	if genre != "" {
		found, err := q.GenreExists(ctx, genre)
		if err != nil {
			return nil, searchBooksOutput{}, s.internal("look up genre", err)
		}
		if !found {
			return nil, searchBooksOutput{}, errors.New(errGenreNotFound)
		}
	}

	limit, offset := pageBounds(in.Limit, in.Offset)
	filter := db.BookFilter{
		Query:     strings.TrimSpace(in.Query),
		AnyTerms:  in.AnyTerms,
		Title:     db.FieldFilter{Value: strings.TrimSpace(in.Title)},
		Author:    db.FieldFilter{Value: strings.TrimSpace(in.Author)},
		Series:    db.FieldFilter{Value: strings.TrimSpace(in.Series)},
		Genre:     genre,
		Format:    strings.TrimSpace(in.Format),
		LibraryID: in.LibraryID,
		Lang:      strings.TrimSpace(in.Language),
		YearFrom:  in.YearFrom,
		YearTo:    in.YearTo,
		Limit:     int64(limit),
		Offset:    int64(offset),
	}

	books, err := db.FilterBooks(ctx, s.db, filter)
	if err != nil {
		return nil, searchBooksOutput{}, s.searchFailure(err)
	}
	total, err := db.CountFilteredBooks(ctx, s.db, filter)
	if err != nil {
		return nil, searchBooksOutput{}, s.searchFailure(err)
	}
	summaries, err := s.summarize(ctx, q, books)
	if err != nil {
		return nil, searchBooksOutput{}, s.internal("load book relations", err)
	}

	return nil, searchBooksOutput{Total: total, Offset: offset, Books: summaries}, nil
}

func (s *service) searchFailure(err error) error {
	out := searchError(err)
	if out.Error() == errSearchFailed {
		s.log.Error("mcp search books", slog.Any("error", err))
	}

	return out
}

// internal logs an unexpected failure and returns a generic tool error.
func (s *service) internal(op string, err error) error {
	s.log.Error("mcp "+op, slog.Any("error", err))

	return fmt.Errorf("failed to %s", op)
}

// summarize maps a page of books to summaries.
func (s *service) summarize(ctx context.Context, q *dbq.Queries, books []dbq.Book) ([]BookSummary, error) {
	fields, err := loadFields(ctx, q, books)
	if err != nil {
		return nil, err
	}
	out := make([]BookSummary, 0, len(fields))
	for i := range fields {
		out = append(out, newSummary(s.publicURL, fields[i]))
	}

	return out, nil
}

func (s *service) getBook(
	ctx context.Context, _ *mcp.CallToolRequest, in getBookInput,
) (*mcp.CallToolResult, BookDetail, error) {
	q := dbq.New(s.db)
	book, err := q.GetBook(ctx, in.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, BookDetail{}, errors.New(errBookNotFound)
	}
	if err != nil {
		return nil, BookDetail{}, s.internal("load book", err)
	}

	detail, err := s.detail(ctx, q, book)
	if err != nil {
		return nil, BookDetail{}, s.internal("load book", err)
	}

	return nil, detail, nil
}

// detail loads a single book's relations and maps it to BookDetail.
func (s *service) detail(ctx context.Context, q *dbq.Queries, book dbq.Book) (BookDetail, error) {
	fields, err := loadFields(ctx, q, []dbq.Book{book})
	if err != nil {
		return BookDetail{}, err
	}
	extra, err := loadDetailExtra(ctx, q, book)
	if err != nil {
		return BookDetail{}, err
	}

	return newDetail(s.publicURL, fields[0], extra), nil
}

func (s *service) similarBooks(
	ctx context.Context, _ *mcp.CallToolRequest, in similarBooksInput,
) (*mcp.CallToolResult, similarBooksOutput, error) {
	q := dbq.New(s.db)
	source, err := q.GetBook(ctx, in.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, similarBooksOutput{}, errors.New(errBookNotFound)
	}
	if err != nil {
		return nil, similarBooksOutput{}, s.internal("load book", err)
	}

	cands, err := db.SimilarBooks(ctx, s.db, in.ID, in.Limit)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, similarBooksOutput{}, errors.New(errBookNotFound)
	}
	if err != nil {
		return nil, similarBooksOutput{}, s.internal("find similar books", err)
	}

	// One batched relation load for the source and every candidate; GetBook per
	// candidate is at most maxLimit cheap primary-key reads. A candidate removed
	// by a concurrent sync since SimilarBooks ran is dropped, not fatal.
	books := make([]dbq.Book, 0, len(cands)+1)
	books = append(books, source)
	kept := make([]db.SimilarCandidate, 0, len(cands))
	for i := range cands {
		b, gErr := q.GetBook(ctx, cands[i].BookID)
		if errors.Is(gErr, sql.ErrNoRows) {
			continue
		}
		if gErr != nil {
			return nil, similarBooksOutput{}, s.internal("load similar book", gErr)
		}
		books = append(books, b)
		kept = append(kept, cands[i])
	}
	cands = kept
	fields, err := loadFields(ctx, q, books)
	if err != nil {
		return nil, similarBooksOutput{}, s.internal("load book relations", err)
	}

	out := similarBooksOutput{
		Source:     newSummary(s.publicURL, fields[0]),
		Candidates: make([]SimilarCandidate, 0, len(cands)),
	}
	for i := range cands {
		out.Candidates = append(out.Candidates, SimilarCandidate{
			BookSummary:  newSummary(s.publicURL, fields[i+1]),
			Reason:       string(cands[i].Reason),
			MatchedTerms: cands[i].MatchedTerms,
		})
	}

	return nil, out, nil
}

// checkBounds rejects a request too large to run cheaply.
func (in searchBooksInput) checkBounds() error {
	if len(in.AnyTerms) > maxAnyTerms {
		return fmt.Errorf("any_terms takes at most %d terms", maxAnyTerms)
	}
	texts := append([]string{in.Query, in.Title, in.Author, in.Series}, in.AnyTerms...)
	terms, prefixes := 0, 0
	for _, t := range texts {
		if utf8.RuneCountInString(t) > maxTextRunes {
			return fmt.Errorf("query, any_terms, title, author and series take at most %d characters each",
				maxTextRunes)
		}
		for _, term := range db.SearchTerms(t) {
			terms++
			if !term.Prefix {
				continue
			}
			prefixes++
			if term.PrefixRunes < minPrefixRunes {
				return fmt.Errorf("a prefix term needs at least %d letters before the *", minPrefixRunes)
			}
		}
	}
	if terms > maxSearchTerms {
		return fmt.Errorf("a search takes at most %d words across all fields", maxSearchTerms)
	}
	if prefixes > maxPrefixTerms {
		return fmt.Errorf("a search takes at most %d prefix (*) terms", maxPrefixTerms)
	}

	return nil
}

// hasCriteria reports whether any text criterion or a genre was given. A value
// with no word in it (only whitespace, * or punctuation) adds no constraint, so it does not count.
func (in searchBooksInput) hasCriteria() bool {
	if hasText(in.Query) || hasText(in.Title) || hasText(in.Author) || hasText(in.Series) ||
		strings.TrimSpace(in.Genre) != "" {
		return true
	}

	return slices.ContainsFunc(in.AnyTerms, hasText)
}

func hasText(v string) bool {
	return db.HasSearchTokens(v)
}

// pageBounds applies the limit default and cap and floors offset at 0.
func pageBounds(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = defaultLimit
	}

	return min(limit, maxLimit), max(offset, 0)
}

// searchError turns a filter error into the message the model sees: SQLite FTS
// syntax errors become "invalid query: …", anything else a generic failure.
func searchError(err error) error {
	msg := err.Error()
	if _, after, ok := strings.Cut(msg, "fts5: "); ok {
		// modernc reports e.g. `SQL logic error: fts5: syntax error near "x" (1)`.
		detail := after
		if j := strings.LastIndex(detail, " ("); j > 0 && strings.HasSuffix(detail, ")") {
			detail = detail[:j]
		}

		return errors.New("invalid query: " + detail)
	}

	return errors.New(errSearchFailed)
}

// loadFields resolves books' authors and genres in one batched statement each
// and series names once per distinct series, in input order.
func loadFields(ctx context.Context, q *dbq.Queries, books []dbq.Book) ([]bookFields, error) {
	out := make([]bookFields, 0, len(books))
	if len(books) == 0 {
		return out, nil
	}
	ids := make([]int64, len(books))
	for i := range books {
		ids[i] = books[i].ID
	}

	authorRows, err := q.ListAuthorsForBooks(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("authors for books: %w", err)
	}
	authors := make(map[int64][]string, len(books))
	for i := range authorRows {
		authors[authorRows[i].BookID] = append(authors[authorRows[i].BookID], authorRows[i].Name)
	}
	genreRows, err := q.ListGenresForBooks(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("genres for books: %w", err)
	}
	genres := make(map[int64][]string, len(books))
	for i := range genreRows {
		genres[genreRows[i].BookID] = append(genres[genreRows[i].BookID], genreRows[i].Name)
	}

	seriesNames := map[int64]string{}
	for i := range books {
		series, err := seriesName(ctx, q, books[i], seriesNames)
		if err != nil {
			return nil, err
		}
		out = append(out, fieldsOf(books[i], authors[books[i].ID], genres[books[i].ID], series))
	}

	return out, nil
}

// seriesName resolves a book's series name, memoised in cache.
func seriesName(ctx context.Context, q *dbq.Queries, b dbq.Book, cache map[int64]string) (string, error) {
	if !b.SeriesID.Valid {
		return "", nil
	}
	if name, ok := cache[b.SeriesID.Int64]; ok {
		return name, nil
	}
	series, err := q.GetSeries(ctx, b.SeriesID.Int64)
	if err != nil {
		return "", fmt.Errorf("get series %d: %w", b.SeriesID.Int64, err)
	}
	cache[b.SeriesID.Int64] = series.Name

	return series.Name, nil
}

func fieldsOf(b dbq.Book, authors, genres []string, series string) bookFields {
	f := bookFields{
		ID:         b.ID,
		Title:      b.Title,
		Authors:    authors,
		Series:     series,
		Genres:     genres,
		Language:   b.Language,
		Annotation: b.Annotation.String,
	}
	if b.SeriesNumber.Valid {
		n := b.SeriesNumber.Float64
		f.SeriesNumber = &n
	}
	f.Year = optInt(b.Year)

	return f
}

func optInt(n sql.NullInt64) *int {
	if !n.Valid {
		return nil
	}
	v := int(n.Int64)

	return &v
}

// loadDetailExtra gathers the fields only BookDetail carries.
func loadDetailExtra(ctx context.Context, q *dbq.Queries, book dbq.Book) (detailExtra, error) {
	identRows, err := q.ListIdentifiersForBook(ctx, book.ID)
	if err != nil {
		return detailExtra{}, fmt.Errorf("identifiers: %w", err)
	}
	idents := make([]Identifier, 0, len(identRows))
	for i := range identRows {
		idents = append(idents, Identifier{Type: identRows[i].Type, Value: identRows[i].Value})
	}
	files, err := q.ListFilesForBook(ctx, book.ID)
	if err != nil {
		return detailExtra{}, fmt.Errorf("files: %w", err)
	}
	formats := make([]string, 0, len(files))
	for i := range files {
		// Files are ordered by format, so duplicates are adjacent.
		if n := len(formats); n == 0 || formats[n-1] != files[i].FileFormat {
			formats = append(formats, files[i].FileFormat)
		}
	}
	lib, err := q.GetLibrary(ctx, book.LibraryID)
	if err != nil {
		return detailExtra{}, fmt.Errorf("library: %w", err)
	}

	return detailExtra{
		Publisher:   book.Publisher.String,
		Rating:      optInt(book.Rating),
		Identifiers: idents,
		Formats:     formats,
		LibraryID:   lib.ID,
		LibraryName: lib.Name,
	}, nil
}

package db

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode"

	"github.com/stephenafamo/bob/dialect/sqlite"
	"github.com/stephenafamo/bob/dialect/sqlite/sm"
	"golang.org/x/text/unicode/norm"

	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/htmltext"
)

// SimilarReason says why a book was offered as similar to another.
type SimilarReason string

// The candidate sources of SimilarBooks, in merge order.
const (
	ReasonDistinctiveTerms SimilarReason = "distinctive_terms"
	ReasonSameSeries       SimilarReason = "same_series"
	ReasonSameAuthor       SimilarReason = "same_author"
	ReasonSharedGenres     SimilarReason = "shared_genres"
)

// SimilarCandidate is one "books like this" candidate. MatchedTerms is set only
// for ReasonDistinctiveTerms: the source's distinctive terms the book contains.
type SimilarCandidate struct {
	BookID       int64
	Reason       SimilarReason
	MatchedTerms []string
}

const (
	// similarTermCount is how many distinctive terms of the source description
	// are OR-matched against the catalog.
	similarTermCount = 8
	// SimilarDefaultLimit and SimilarMaxLimit bound SimilarBooks' result.
	SimilarDefaultLimit = 20
	SimilarMaxLimit     = 50
)

// DistinctiveTerms returns the top k terms of text by tf·idf against the
// descriptions in books_fts, where idf = ln(N/df), N is the number of books with
// a description and df the number of descriptions containing the term. Only the
// annotation column counts: candidates are matched on descriptions alone (see
// addTermMatches), so a word common in titles but rare in descriptions keeps
// its weight. Terms found in only one description (df <= 1) and terms found in
// every one (idf 0) are dropped. Ties are broken by term, so the result is
// deterministic.
//
// text must already be stripped of markup. It is tokenized to match the index
// spelling (see ftsTerms), so df is read straight from books_fts_vocab_col.
func DistinctiveTerms(ctx context.Context, x dbq.DBTX, text string, k int) ([]string, error) {
	tf := map[string]int{}
	for _, t := range ftsTerms(text) {
		tf[t]++
	}
	if len(tf) == 0 || k <= 0 {
		return nil, nil
	}

	// Served by the partial index idx_books_described (migration 009); keep the
	// WHERE clause identical to the index's.
	var n int64
	err := x.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM books WHERE annotation IS NOT NULL AND annotation <> ''`).Scan(&n)
	if err != nil {
		return nil, fmt.Errorf("count described books: %w", err)
	}

	df, err := termDocFreq(ctx, x, tf)
	if err != nil {
		return nil, err
	}

	return rankTerms(tf, df, n, k), nil
}

// rankTerms scores each term by tf·ln(n/df), keeps the positive scores of terms
// with df > 1, and returns the top k (score desc, then term asc).
func rankTerms(tf map[string]int, df map[string]int64, n int64, k int) []string {
	type scored struct {
		term  string
		score float64
	}
	var cands []scored
	for term, d := range df {
		if d <= 1 {
			continue
		}
		if score := float64(tf[term]) * math.Log(float64(n)/float64(d)); score > 0 {
			cands = append(cands, scored{term: term, score: score})
		}
	}
	slices.SortFunc(cands, func(a, b scored) int {
		if c := cmp.Compare(b.score, a.score); c != 0 {
			return c
		}

		return strings.Compare(a.term, b.term)
	})

	out := make([]string, 0, min(k, len(cands)))
	for _, c := range cands[:min(k, len(cands))] {
		out = append(out, c.term)
	}

	return out
}

// termDocFreq reads the annotation-column document frequency of every term in
// tf from books_fts_vocab_col in one query. Terms absent from every description
// are omitted.
func termDocFreq(ctx context.Context, x dbq.DBTX, tf map[string]int) (map[string]int64, error) {
	terms := make([]any, 0, len(tf))
	for t := range tf {
		terms = append(terms, t)
	}
	query, args, err := sqlite.Select(
		sm.Columns("term", "doc"),
		sm.From("books_fts_vocab_col"),
		sm.Where(sqlite.Quote("col").EQ(sqlite.Arg(annotationColumn))),
		sm.Where(sqlite.Quote("term").In(sqlite.Arg(terms...))),
	).Build(ctx)
	if err != nil {
		return nil, fmt.Errorf("build vocab query: %w", err)
	}

	rows, err := x.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query vocab: %w", err)
	}
	defer func() { _ = rows.Close() }()

	df := make(map[string]int64, len(tf))
	for rows.Next() {
		var (
			term string
			doc  int64
		)
		if err := rows.Scan(&term, &doc); err != nil {
			return nil, fmt.Errorf("scan vocab: %w", err)
		}
		df[term] = doc
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate vocab: %w", err)
	}

	return df, nil
}

// ftsTerms splits text into terms spelled the way books_fts stores them
// (tokenizer unicode61 remove_diacritics 1): lowercased runs of letters, digits
// and private-use characters; diacritics on Latin letters removed (é → e);
// stray combining marks dropped without splitting the word. Precomposed
// non-Latin letters keep their marks, as the index does: ё and й stay ё and й.
// A rare letter the SQLite fold table treats differently (ł → l) merely misses
// its df lookup and is skipped.
func ftsTerms(text string) []string {
	s := norm.NFC.String(stripLatinMarks(norm.NFD.String(strings.ToLower(text))))

	var (
		out []string
		cur strings.Builder
	)
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Mn, r):
		case unicode.IsLetter(r), unicode.IsNumber(r), unicode.Is(unicode.Co, r):
			cur.WriteRune(r)
		default:
			flush()
		}
	}
	flush()

	return out
}

// candidateMerger accumulates candidates in source order, deduplicated (the
// source book is pre-seeded in seen) and capped at limit. Each source fetches
// limit rows, so a source with enough books always fills the remainder: at most
// len(out) of its rows can be duplicates.
type candidateMerger struct {
	limit int
	seen  map[int64]bool
	out   []SimilarCandidate
}

func (m *candidateMerger) full() bool { return len(m.out) >= m.limit }

// add appends the not-yet-seen ids under reason and reports the ones it kept.
func (m *candidateMerger) add(ids []int64, reason SimilarReason) []int64 {
	var kept []int64
	for _, id := range ids {
		if m.full() {
			break
		}
		if m.seen[id] {
			continue
		}
		m.seen[id] = true
		m.out = append(m.out, SimilarCandidate{BookID: id, Reason: reason})
		kept = append(kept, id)
	}

	return kept
}

// SimilarBooks returns up to limit books like bookID (limit <= 0 means
// SimilarDefaultLimit; it is capped at SimilarMaxLimit). Candidates come from
// four sources, merged in this order, each book reported once under the first
// source that found it:
//
//  1. distinctive_terms: a bm25-ranked OR match on the distinctive terms of the
//     book's description (skipped when it has none);
//  2. same_series: the series' other books, in series-number order;
//  3. same_author: books sharing an author, most shared authors first;
//  4. shared_genres: books sharing a genre, most shared genres first.
//
// The book itself is never a candidate. An unknown bookID yields an error
// wrapping sql.ErrNoRows.
func SimilarBooks(ctx context.Context, x dbq.DBTX, bookID int64, limit int) ([]SimilarCandidate, error) {
	if limit <= 0 {
		limit = SimilarDefaultLimit
	}
	limit = min(limit, SimilarMaxLimit)

	q := dbq.New(x)
	src, err := q.GetBook(ctx, bookID)
	if err != nil {
		return nil, fmt.Errorf("load book %d: %w", bookID, err)
	}

	m := candidateMerger{limit: limit, seen: map[int64]bool{bookID: true}}

	if text := htmltext.StripMarkup(src.Annotation.String); strings.TrimSpace(text) != "" {
		terms, err := DistinctiveTerms(ctx, x, text, similarTermCount)
		if err != nil {
			return nil, err
		}
		if err := m.addTermMatches(ctx, x, bookID, terms); err != nil {
			return nil, err
		}
	}

	if err := m.addLinkedBooks(ctx, q, src); err != nil {
		return nil, err
	}

	return m.out, nil
}

// addLinkedBooks adds, in order, the series, author and genre mates of src,
// skipping the remaining sources once the result is full.
func (m *candidateMerger) addLinkedBooks(ctx context.Context, q *dbq.Queries, src dbq.Book) error {
	lim := int64(m.limit)
	sources := []struct {
		reason SimilarReason
		list   func() ([]int64, error)
	}{
		{ReasonSameSeries, func() ([]int64, error) {
			if !src.SeriesID.Valid {
				return nil, nil
			}

			return q.ListSeriesMateIDs(ctx, dbq.ListSeriesMateIDsParams{
				SeriesID: src.SeriesID, BookID: src.ID, Lim: lim,
			})
		}},
		{ReasonSameAuthor, func() ([]int64, error) {
			return q.ListAuthorMateIDs(ctx, dbq.ListAuthorMateIDsParams{BookID: src.ID, Lim: lim})
		}},
		{ReasonSharedGenres, func() ([]int64, error) {
			return q.ListGenreMateIDs(ctx, dbq.ListGenreMateIDsParams{BookID: src.ID, Lim: lim})
		}},
	}
	for _, source := range sources {
		if m.full() {
			return nil
		}
		found, err := source.list()
		if err != nil {
			return fmt.Errorf("list %s candidates: %w", source.reason, err)
		}
		m.add(found, source.reason)
	}

	return nil
}

// addTermMatches adds the bm25-ranked OR match on terms against other books'
// descriptions, then fills each kept candidate's MatchedTerms. The match is
// scoped to the annotation column: a word in a title, author or series name is
// not a shared description word (FR-016), and bm25 would rank such short-field
// hits above real description overlap. Name links come from the same_series and
// same_author sources instead.
func (m *candidateMerger) addTermMatches(ctx context.Context, x dbq.DBTX, bookID int64, terms []string) error {
	expr := annotationScope(anyClause(terms))
	if expr == "" {
		return nil
	}
	query, args, err := sqlite.Select(
		sm.Columns("rowid"),
		sm.From("books_fts"),
		sm.Where(sqlite.Raw("books_fts MATCH ?", expr)),
		sm.Where(sqlite.Quote("rowid").NE(sqlite.Arg(bookID))),
		sm.OrderBy("bm25(books_fts)"), sm.OrderBy("rowid"),
		sm.Limit(sqlite.Arg(m.limit)),
	).Build(ctx)
	if err != nil {
		return fmt.Errorf("build similar query: %w", err)
	}
	found, err := queryIDs(ctx, x, query, args)
	if err != nil {
		return fmt.Errorf("query similar by terms: %w", err)
	}

	kept := m.add(found, ReasonDistinctiveTerms)
	if len(kept) == 0 {
		return nil
	}

	return m.fillMatchedTerms(ctx, x, kept, terms)
}

// fillMatchedTerms records which of terms each kept candidate contains: one
// MATCH per term, restricted to the kept rowids, so at most similarTermCount
// small queries.
func (m *candidateMerger) fillMatchedTerms(ctx context.Context, x dbq.DBTX, kept []int64, terms []string) error {
	index := make(map[int64]int, len(kept))
	for i, c := range m.out {
		if c.Reason == ReasonDistinctiveTerms {
			index[c.BookID] = i
		}
	}
	keptArgs := make([]any, 0, len(kept))
	for _, id := range kept {
		keptArgs = append(keptArgs, id)
	}

	for _, term := range terms {
		query, args, err := sqlite.Select(
			sm.Columns("rowid"),
			sm.From("books_fts"),
			sm.Where(sqlite.Raw("books_fts MATCH ?", annotationScope(quoteToken(term)))),
			sm.Where(sqlite.Quote("rowid").In(sqlite.Arg(keptArgs...))),
		).Build(ctx)
		if err != nil {
			return fmt.Errorf("build matched-term query: %w", err)
		}
		hits, err := queryIDs(ctx, x, query, args)
		if err != nil {
			return fmt.Errorf("query matched term: %w", err)
		}
		for _, id := range hits {
			i := index[id]
			m.out[i].MatchedTerms = append(m.out[i].MatchedTerms, term)
		}
	}

	return nil
}

// annotationColumn is the books_fts column holding the book's description.
const annotationColumn = "annotation"

// annotationScope restricts the FTS5 expression expr to the annotation column,
// or returns "" for an empty expression.
func annotationScope(expr string) string {
	if expr == "" {
		return ""
	}

	return annotationColumn + " : (" + expr + ")"
}

// queryIDs runs a single-column integer query.
func queryIDs(ctx context.Context, x dbq.DBTX, query string, args []any) ([]int64, error) {
	rows, err := x.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan id: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate ids: %w", err)
	}

	return out, nil
}

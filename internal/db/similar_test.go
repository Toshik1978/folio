package db

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/stretchr/testify/suite"

	"github.com/Toshik1978/folio/internal/db/dbq"
)

type similarSuite struct {
	suite.Suite

	db  *sql.DB
	q   *dbq.Queries
	lib int64
	seq int
}

func (s *similarSuite) SetupTest() {
	database, err := Open(slog.New(slog.DiscardHandler), s.T().TempDir())
	s.Require().NoError(err)
	s.db = database
	s.q = dbq.New(database)
	s.seq = 0

	s.lib, err = s.q.InsertLibrary(context.Background(), dbq.InsertLibraryParams{
		Name: "Main", Type: "folder", Path: "/lib", SyncIntervalSeconds: 3600, CreatedAt: 1,
	})
	s.Require().NoError(err)
}

func (s *similarSuite) TearDownTest() {
	if s.db != nil {
		_ = s.db.Close()
	}
}

// fixtureBook describes one seeded book. The title defaults to "Book", so the
// title token is common to the whole corpus and never distinctive.
type fixtureBook struct {
	title      string
	annotation string
	series     int64
	seriesNum  float64
	authors    []string
	genres     []string
}

// add seeds a book with its links and indexes it in books_fts.
func (s *similarSuite) add(b fixtureBook) int64 {
	ctx := context.Background()
	s.seq++
	key := fmt.Sprintf("k%d", s.seq)
	p := dbq.InsertBookParams{
		LibraryID: s.lib, LibraryKey: key, Title: cmp.Or(b.title, "Book"), Language: "en",
		ContentHash: key, AddedAt: 1, ImportedAt: 1,
	}
	if b.annotation != "" {
		p.Annotation = sql.NullString{String: b.annotation, Valid: true}
	}
	if b.series != 0 {
		p.SeriesID = sql.NullInt64{Int64: b.series, Valid: true}
		p.SeriesNumber = sql.NullFloat64{Float64: b.seriesNum, Valid: true}
	}
	id, err := s.q.InsertBook(ctx, p)
	s.Require().NoError(err)

	for _, name := range b.authors {
		aid, err := s.q.InsertAuthor(ctx, dbq.InsertAuthorParams{Name: name, NameFold: Fold(name)})
		s.Require().NoError(err)
		s.Require().NoError(s.q.InsertBookAuthor(ctx, dbq.InsertBookAuthorParams{BookID: id, AuthorID: aid}))
	}
	for _, name := range b.genres {
		gid, err := s.q.InsertGenre(ctx, dbq.InsertGenreParams{Name: name, NameFold: Fold(name)})
		s.Require().NoError(err)
		s.Require().NoError(s.q.InsertBookGenre(ctx, dbq.InsertBookGenreParams{BookID: id, GenreID: gid}))
	}
	s.Require().NoError(RefreshBookFTS(ctx, s.db, id))

	return id
}

func (s *similarSuite) series(name string) int64 {
	id, err := s.q.InsertSeries(context.Background(), dbq.InsertSeriesParams{Name: name, NameFold: Fold(name)})
	s.Require().NoError(err)

	return id
}

func ids(cands []SimilarCandidate) []int64 {
	out := make([]int64, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.BookID)
	}

	return out
}

func (s *similarSuite) TestDescribedCountUsesPartialIndex() {
	var id, parent, unused int
	var detail string
	err := s.db.QueryRowContext(context.Background(),
		`EXPLAIN QUERY PLAN SELECT COUNT(*) FROM books WHERE annotation IS NOT NULL AND annotation <> ''`,
	).Scan(&id, &parent, &unused, &detail)
	s.Require().NoError(err)
	s.Contains(detail, "idx_books_described", "the IDF count must not scan the books table")
}

func (s *similarSuite) TestDistinctiveTermsExcludeCommonWords() {
	// N = 5. "story" is in every book (idf 0), "wizard" in 3, "dragon" in 2,
	// "xyzzy" in 1 (df must exceed 1).
	s.add(fixtureBook{annotation: "story dragon wizard"})
	s.add(fixtureBook{annotation: "story dragon wizard"})
	s.add(fixtureBook{annotation: "story wizard"})
	s.add(fixtureBook{annotation: "story"})
	s.add(fixtureBook{annotation: "story xyzzy"})

	terms, err := DistinctiveTerms(context.Background(), s.db, "Story story, DRAGON dragon wizard xyzzy unseen", 10)
	s.Require().NoError(err)
	s.Equal([]string{"dragon", "wizard"}, terms)

	terms, err = DistinctiveTerms(context.Background(), s.db, "story dragon dragon wizard", 1)
	s.Require().NoError(err)
	s.Equal([]string{"dragon"}, terms)
}

// TestDistinctiveTermsCountOnlyDescriptions: document frequency counts only
// descriptions, so a word common in titles but rare in descriptions stays
// distinctive, and N is the number of described books.
func (s *similarSuite) TestDistinctiveTermsCountOnlyDescriptions() {
	// Described books: N = 4; "moscow" in 2 of them, "tale" in all 4 (idf 0).
	s.add(fixtureBook{annotation: "moscow tale"})
	s.add(fixtureBook{annotation: "moscow tale"})
	s.add(fixtureBook{annotation: "tale"})
	s.add(fixtureBook{annotation: "tale"})
	for range 6 {
		s.add(fixtureBook{title: "Moscow"}) // titles must not dilute "moscow"
	}

	terms, err := DistinctiveTerms(context.Background(), s.db, "moscow tale", 10)
	s.Require().NoError(err)
	s.Equal([]string{"moscow"}, terms)
}

// TestTitleHitIsNotADescriptionMatch: a book whose title (not description)
// holds a distinctive word is no distinctive_terms candidate.
func (s *similarSuite) TestTitleHitIsNotADescriptionMatch() {
	src := s.add(fixtureBook{annotation: "dragon wizard quest"})
	twin := s.add(fixtureBook{annotation: "a dragon and a wizard"})
	s.add(fixtureBook{annotation: "unrelated text"})
	s.add(fixtureBook{annotation: "unrelated text"})
	s.add(fixtureBook{title: "Dragon Wizard"})

	got, err := SimilarBooks(context.Background(), s.db, src, 10)
	s.Require().NoError(err)
	s.Equal([]SimilarCandidate{{
		BookID: twin, Reason: ReasonDistinctiveTerms, MatchedTerms: []string{"dragon", "wizard"},
	}}, got)
}

func (s *similarSuite) TestDistinctiveTermsTokenizeLikeIndex() {
	// The terms must be spelled exactly as unicode61 remove_diacritics 1 stores
	// them, or their df lookup in books_fts_vocab_col misses.
	for range 2 {
		s.add(fixtureBook{annotation: "Café Ёлка зо́лото Йод"})
	}
	s.add(fixtureBook{annotation: "other"})
	s.add(fixtureBook{annotation: "other"})

	terms, err := DistinctiveTerms(context.Background(), s.db, "CAFÉ, ёлка! зо́лото — йод", 10)
	s.Require().NoError(err)
	s.ElementsMatch([]string{"cafe", "ёлка", "золото", "йод"}, terms)
}

func (s *similarSuite) TestDistinctiveTermsEmptyText() {
	s.add(fixtureBook{annotation: "story"})
	terms, err := DistinctiveTerms(context.Background(), s.db, "  ,.! ", 10)
	s.Require().NoError(err)
	s.Empty(terms)
}

// corpus seeds a source book plus books related to it in each way.
type corpus struct {
	src, close, weak, seriesMate, bothMate, authorMate, genreMate, unrelated int64
}

func (s *similarSuite) seedCorpus(srcAnnotation string) corpus {
	sid := s.series("Saga")
	var c corpus
	c.src = s.add(fixtureBook{
		annotation: srcAnnotation,
		series:     sid, seriesNum: 2, authors: []string{"Alice"}, genres: []string{"fantasy", "epic"},
	})
	c.close = s.add(fixtureBook{annotation: "<p>The <b>dragon</b> hoards gold under the mountain story</p>"})
	c.weak = s.add(fixtureBook{annotation: "gold story of merchants"})
	// In the same series and sharing a term: reported once, as distinctive_terms.
	c.bothMate = s.add(fixtureBook{annotation: "mountain story", series: sid, seriesNum: 1})
	c.seriesMate = s.add(fixtureBook{annotation: "story", series: sid, seriesNum: 3})
	c.authorMate = s.add(fixtureBook{annotation: "story", authors: []string{"Alice"}})
	c.genreMate = s.add(fixtureBook{annotation: "story", genres: []string{"epic", "fantasy"}})
	s.add(fixtureBook{annotation: "story", genres: []string{"fantasy"}})
	c.unrelated = s.add(fixtureBook{annotation: "story of cooking", genres: []string{"food"}})

	return c
}

func (s *similarSuite) TestSimilarBooksRanksCloseBookFirst() {
	c := s.seedCorpus("<p>A lonely <i>dragon</i> guards ancient gold in a mountain story</p>")

	cands, err := SimilarBooks(context.Background(), s.db, c.src, 0)
	s.Require().NoError(err)
	s.Require().NotEmpty(cands)

	s.Equal(c.close, cands[0].BookID)
	s.Equal(ReasonDistinctiveTerms, cands[0].Reason)
	s.ElementsMatch([]string{"dragon", "gold", "mountain"}, cands[0].MatchedTerms)

	byID := map[int64]SimilarCandidate{}
	for _, cand := range cands {
		byID[cand.BookID] = cand
	}
	s.Equal(ReasonDistinctiveTerms, byID[c.weak].Reason)
	s.Equal([]string{"gold"}, byID[c.weak].MatchedTerms)
	s.Equal(ReasonDistinctiveTerms, byID[c.bothMate].Reason)
	s.Equal([]string{"mountain"}, byID[c.bothMate].MatchedTerms)
	s.Equal(ReasonSameSeries, byID[c.seriesMate].Reason)
	s.Nil(byID[c.seriesMate].MatchedTerms)
	s.Equal(ReasonSameAuthor, byID[c.authorMate].Reason)
	s.Equal(ReasonSharedGenres, byID[c.genreMate].Reason)
	s.NotContains(ids(cands), c.unrelated)

	// Source order: terms, then series, then author, then genres (most shared first).
	reasons := make([]SimilarReason, 0, len(cands))
	for _, cand := range cands {
		reasons = append(reasons, cand.Reason)
	}
	s.Equal([]SimilarReason{
		ReasonDistinctiveTerms, ReasonDistinctiveTerms, ReasonDistinctiveTerms,
		ReasonSameSeries, ReasonSameAuthor, ReasonSharedGenres, ReasonSharedGenres,
	}, reasons)
	s.Equal(c.genreMate, cands[5].BookID, "two shared genres rank above one")
}

func (s *similarSuite) TestSimilarBooksExcludesSource() {
	c := s.seedCorpus("dragon gold mountain story")

	cands, err := SimilarBooks(context.Background(), s.db, c.src, 50)
	s.Require().NoError(err)
	s.Require().NotEmpty(cands)
	s.NotContains(ids(cands), c.src)

	seen := map[int64]bool{}
	for _, id := range ids(cands) {
		s.False(seen[id], "candidate %d listed twice", id)
		seen[id] = true
	}
}

func (s *similarSuite) TestSimilarBooksWithoutDescription() {
	c := s.seedCorpus("")

	cands, err := SimilarBooks(context.Background(), s.db, c.src, 0)
	s.Require().NoError(err)
	s.Require().NotEmpty(cands)
	for _, cand := range cands {
		s.NotEqual(ReasonDistinctiveTerms, cand.Reason)
		s.Nil(cand.MatchedTerms)
	}
	s.Equal([]int64{c.bothMate, c.seriesMate}, ids(cands[:2]), "series order by number")
	s.Equal(ReasonSameSeries, cands[0].Reason)
	s.Contains(ids(cands), c.authorMate)
	s.Contains(ids(cands), c.genreMate)
	s.NotContains(ids(cands), c.close)
}

func (s *similarSuite) TestSimilarBooksLimit() {
	src := s.add(fixtureBook{genres: []string{"sf"}})
	for range 55 {
		s.add(fixtureBook{genres: []string{"sf"}})
	}
	ctx := context.Background()

	cands, err := SimilarBooks(ctx, s.db, src, 5)
	s.Require().NoError(err)
	s.Len(cands, 5)

	cands, err = SimilarBooks(ctx, s.db, src, 0)
	s.Require().NoError(err)
	s.Len(cands, 20, "default limit")

	cands, err = SimilarBooks(ctx, s.db, src, 500)
	s.Require().NoError(err)
	s.Len(cands, 50, "max limit")
}

func (s *similarSuite) TestSimilarBooksUnknownBook() {
	_, err := SimilarBooks(context.Background(), s.db, 9999, 10)
	s.Require().ErrorIs(err, sql.ErrNoRows)
}

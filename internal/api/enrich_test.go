package api

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Toshik1978/folio/internal/ebook"
)

// enrichSuite unit-tests applyEnrichment directly (the shared persist path behind
// both online enrichment and manual Fix Match).
type enrichSuite struct {
	baseSuite
}

func (s *enrichSuite) TestClaimLazyIsExclusivePerBook() {
	s.True(s.books.claimLazy(7))
	s.False(s.books.claimLazy(7), "second concurrent claim must lose")
	s.True(s.books.claimLazy(8), "claims are per book")
	s.books.releaseLazy(7)
	s.True(s.books.claimLazy(7), "released claim can be retaken")
}

func (s *enrichSuite) TestViewSkipsLazyTiersWhileAnotherRequestRunsThem() {
	// Wire an enricher so a normal first view would mark the book checked.
	bh := NewBooks(slog.New(slog.DiscardHandler), s.db, s.guard, s.covers, nil, &fakeEnricher{}, s.covers, nil)
	r := chi.NewRouter()
	bh.Register(r)
	s.books = bh
	s.router = r

	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "Sparse"}) // no annotation → lazy tiers apply

	s.Require().True(s.books.claimLazy(id), "simulate another request mid-enrichment")
	w := s.do(http.MethodGet, fmt.Sprintf("/books/%d", id), nil)
	s.Require().Equal(http.StatusOK, w.Code)

	book, err := s.q.GetBook(s.T().Context(), id)
	s.Require().NoError(err)
	s.Zero(book.EnrichmentChecked, "the losing view must not run enrichment")

	s.books.releaseLazy(id)
	w = s.do(http.MethodGet, fmt.Sprintf("/books/%d", id), nil)
	s.Require().Equal(http.StatusOK, w.Code)
	book, err = s.q.GetBook(s.T().Context(), id)
	s.Require().NoError(err)
	s.EqualValues(1, book.EnrichmentChecked, "the next view runs the tiers normally")
}

// TestApplyEnrichmentPersistsIdentifierOnly guards the regression where a match
// whose only new data is identifiers persisted nothing: the early "nothing
// changed" return ran before the identifier write. Identifiers must survive even
// when no displayable field changed.
func (s *enrichSuite) TestApplyEnrichmentPersistsIdentifierOnly() {
	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "x"})
	book, err := s.q.GetBook(s.T().Context(), id)
	s.Require().NoError(err)

	meta := ebook.Metadata{Identifiers: []ebook.Identifier{{Type: "isbn", Value: "9780441013593"}}}
	persisted, err := s.books.applyEnrichment(s.T().Context(), &book, meta, false, false)
	s.Require().NoError(err)
	s.False(persisted, "no displayable field changed")

	ids, err := s.q.ListIdentifiersForBook(s.T().Context(), id)
	s.Require().NoError(err)
	s.Require().Len(ids, 1)
	s.Equal("9780441013593", ids[0].Value, "an identifier-only match still persists the identifier")
}

// TestApplyEnrichmentGapFillsGenres covers the online-path genre persistence the
// docs promise: a book with no genres receives the volume's genres on enrichment.
func (s *enrichSuite) TestApplyEnrichmentGapFillsGenres() {
	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "x"}) // no genres
	book, err := s.q.GetBook(s.T().Context(), id)
	s.Require().NoError(err)

	meta := ebook.Metadata{Annotation: "desc", Genres: []string{"Science Fiction"}}
	persisted, err := s.books.applyEnrichment(s.T().Context(), &book, meta, false, false)
	s.Require().NoError(err)
	s.True(persisted)

	genres, err := s.q.ListGenresForBook(s.T().Context(), id)
	s.Require().NoError(err)
	s.Require().Len(genres, 1)
	s.Equal("Science Fiction", genres[0].Name, "online enrichment gap-fills genres")
}

func (s *enrichSuite) TestAutoEnrichmentGapFillsIdentifiers() {
	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "x", Identifiers: map[string]string{"isbn": "9780441013593"}}) // ISBN-13
	book, err := s.q.GetBook(s.T().Context(), id)
	s.Require().NoError(err)

	// Auto enrichment (overwrite=false) offering an ISBN-10 must not replace the ISBN-13.
	meta := ebook.Metadata{Annotation: "d", Identifiers: []ebook.Identifier{{Type: "isbn", Value: "0441013597"}}}
	_, err = s.books.applyEnrichment(s.T().Context(), &book, meta, false, false)
	s.Require().NoError(err)

	ids, err := s.q.ListIdentifiersForBook(s.T().Context(), id)
	s.Require().NoError(err)
	s.Require().Len(ids, 1)
	s.Equal("9780441013593", ids[0].Value, "auto enrichment must not downgrade an existing ISBN")
}

func (s *enrichSuite) TestFixMatchOverwritesIdentifiers() {
	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "x", Identifiers: map[string]string{"isbn": "9780441013593"}})
	book, err := s.q.GetBook(s.T().Context(), id)
	s.Require().NoError(err)

	meta := ebook.Metadata{Identifiers: []ebook.Identifier{{Type: "isbn", Value: "0441013597"}}}
	_, err = s.books.applyEnrichment(s.T().Context(), &book, meta, true, false) // overwrite = manual Fix Match
	s.Require().NoError(err)

	ids, err := s.q.ListIdentifiersForBook(s.T().Context(), id)
	s.Require().NoError(err)
	s.Require().Len(ids, 1)
	s.Equal("0441013597", ids[0].Value, "a manual Fix Match overwrites the identifier")
}

// TestApplyEnrichmentKeepsExistingGenres asserts the gap-fill never clobbers
// genres a book already has (only overwrite/Fix Match replaces them).
func (s *enrichSuite) TestApplyEnrichmentKeepsExistingGenres() {
	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "x", Genres: []string{"Existing"}})
	book, err := s.q.GetBook(s.T().Context(), id)
	s.Require().NoError(err)

	meta := ebook.Metadata{Annotation: "desc", Genres: []string{"Science Fiction"}}
	_, err = s.books.applyEnrichment(s.T().Context(), &book, meta, false, false)
	s.Require().NoError(err)

	genres, err := s.q.ListGenresForBook(s.T().Context(), id)
	s.Require().NoError(err)
	s.Require().Len(genres, 1, "gap-fill leaves existing genres untouched")
	s.Equal("Existing", genres[0].Name)
}

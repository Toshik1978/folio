package api

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"

	"github.com/Toshik1978/folio/internal/ebook"
	"github.com/Toshik1978/folio/internal/googlebooks"
)

type matchSuite struct {
	baseSuite
}

// handlerWith rebuilds the suite's books handler with the given enricher.
func (s *matchSuite) handlerWith(enr MetadataEnricher) {
	s.books = NewBooks(slog.New(slog.DiscardHandler), s.db, s.covers, nil, enr, s.covers)
	r := chi.NewRouter()
	s.books.Register(r)
	s.router = r
}

func (s *matchSuite) TestSearchMatchReturnsCandidates() {
	var v googlebooks.Volume
	v.ID = "vol1"
	v.VolumeInfo.Title = "Dune"
	v.VolumeInfo.Authors = []string{"Frank Herbert"}
	v.VolumeInfo.PublishedDate = "1965"
	v.VolumeInfo.ImageLinks.Thumbnail = "http://img/t.jpg"
	enr := &fakeEnricher{candidates: []googlebooks.Volume{v}}
	s.handlerWith(enr)

	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "x"})

	w := s.do(http.MethodGet, "/books/"+itoa(id)+"/match?q=dune", nil)
	s.Require().Equal(http.StatusOK, w.Code)
	s.Equal("dune", enr.lastQuery)

	var got []matchCandidate
	s.decode(w, &got)
	s.Require().Len(got, 1)
	s.Equal("vol1", got[0].VolumeID)
	s.Equal("Dune", got[0].Title)
	s.Equal([]string{"Frank Herbert"}, got[0].Authors)
	s.Equal(1965, got[0].Year)
	s.Equal(
		"https://img/t.jpg",
		got[0].Thumbnail,
		"http thumbnails are upgraded to https to avoid mixed-content blocking",
	)
}

func (s *matchSuite) TestSearchMatchUnknownBook() {
	s.handlerWith(&fakeEnricher{})

	w := s.do(http.MethodGet, "/books/999999/match?q=dune", nil)
	s.Equal(http.StatusNotFound, w.Code, "search against a nonexistent book is 404, not a 200 search")
}

func (s *matchSuite) TestSearchMatchMissingQuery() {
	s.handlerWith(&fakeEnricher{})
	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "x"})

	w := s.do(http.MethodGet, "/books/"+itoa(id)+"/match", nil)
	s.Equal(http.StatusBadRequest, w.Code)
}

func (s *matchSuite) TestApplyMatchOverwrites() {
	enr := &fakeEnricher{applyMeta: ebook.Metadata{
		Title: "Dune", Authors: []string{"Frank Herbert"}, Series: "Dune Chronicles",
		Genres: []string{"Science Fiction"}, Annotation: "Chosen.", Publisher: "Ace",
	}}
	s.handlerWith(enr)

	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{
		Title: "wrong title", Authors: []string{"Wrong Author"}, Genres: []string{"Wrong Genre"},
		Annotation: "old", Publisher: "Old Pub",
	})
	before, err := s.q.GetBook(s.T().Context(), id)
	s.Require().NoError(err)

	w := s.do(http.MethodPost, "/books/"+itoa(id)+"/match", map[string]string{"volume_id": "vol1"})
	s.Require().Equal(http.StatusOK, w.Code)
	s.Equal("vol1", enr.lastVolume)

	after, err := s.q.GetBook(s.T().Context(), id)
	s.Require().NoError(err)
	s.Equal("Chosen.", after.Annotation.String, "manual match overwrites the annotation")
	s.Equal("Ace", after.Publisher.String, "manual match overwrites the publisher")
	s.Equal("Dune", after.Title, "manual match overwrites the title")
	s.NotEqual(before.ContentHash, after.ContentHash, "content_hash restamped")

	authors, err := s.q.ListAuthorsForBook(s.T().Context(), id)
	s.Require().NoError(err)
	s.Require().Len(authors, 1, "authors are replaced, not appended")
	s.Equal("Frank Herbert", authors[0].Name, "manual match overwrites the authors")

	genres, err := s.q.ListGenresForBook(s.T().Context(), id)
	s.Require().NoError(err)
	s.Require().Len(genres, 1, "genres are replaced, not appended")
	s.Equal("Science Fiction", genres[0].Name, "manual match overwrites the genres")
}

// TestApplyMatchKeepsLocalCover guards the Fix Match cover regression: a manual
// match overwrites the text metadata but must NOT replace a real local cover
// (e.g. a page-1 render extracted from a PDF) with Google's low-res thumbnail.
func (s *matchSuite) TestApplyMatchKeepsLocalCover() {
	google := s.jpegFixtureSized(6) // the worse online thumbnail
	enr := &fakeEnricher{applyMeta: ebook.Metadata{Annotation: "Chosen.", Cover: google}}
	s.handlerWith(enr)

	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "x", Format: "pdf"})

	local := s.jpegFixture() // the good cover already on disk
	s.Require().NoError(s.covers.Save(id, local))
	want, err := os.ReadFile(s.covers.Path(id))
	s.Require().NoError(err)

	w := s.do(http.MethodPost, "/books/"+itoa(id)+"/match", map[string]string{"volume_id": "vol1"})
	s.Require().Equal(http.StatusOK, w.Code)

	after, err := s.q.GetBook(s.T().Context(), id)
	s.Require().NoError(err)
	s.Equal("Chosen.", after.Annotation.String, "text metadata is still overwritten")

	got, err := os.ReadFile(s.covers.Path(id))
	s.Require().NoError(err)
	s.Equal(want, got, "Fix Match must not overwrite a real local cover with the online thumbnail")
}

// TestApplyMatchSetsCoverWhenNoLocalCover is the complement: with no local cover
// to protect, Fix Match fills in the chosen volume's cover.
func (s *matchSuite) TestApplyMatchSetsCoverWhenNoLocalCover() {
	google := s.jpegFixtureSized(6)
	enr := &fakeEnricher{applyMeta: ebook.Metadata{Annotation: "Chosen.", Cover: google}}
	s.handlerWith(enr)

	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "x", Format: "pdf"})
	s.Require().False(s.covers.Has(id), "no local cover to begin with")

	w := s.do(http.MethodPost, "/books/"+itoa(id)+"/match", map[string]string{"volume_id": "vol1"})
	s.Require().Equal(http.StatusOK, w.Code)

	got, err := os.ReadFile(s.covers.Path(id))
	s.Require().NoError(err)
	s.Equal(google, got, "with no local cover, Fix Match fills in the chosen volume's cover")
}

// H1: applying a match marks the book manually_matched so a later sync never
// reverts it.
func (s *matchSuite) TestApplyMatchSetsManuallyMatched() {
	enr := &fakeEnricher{applyMeta: ebook.Metadata{Title: "Chosen", Annotation: "From Google"}}
	s.handlerWith(enr)

	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "Original"})

	w := s.do(http.MethodPost, "/books/"+itoa(id)+"/match", map[string]string{"volume_id": "v1"})
	s.Require().Equal(http.StatusOK, w.Code)

	book, err := s.q.GetBook(s.T().Context(), id)
	s.Require().NoError(err)
	s.EqualValues(1, book.ManuallyMatched)
}

// H1 edge: even a match that changes nothing displayable must still lock the
// book (the marker, not the field diff, is what makes sync gap-fill-only).
func (s *matchSuite) TestApplyMatchMarksEvenWhenUnchanged() {
	enr := &fakeEnricher{applyMeta: ebook.Metadata{}} // chosen volume adds nothing
	s.handlerWith(enr)

	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "Original"})

	w := s.do(http.MethodPost, "/books/"+itoa(id)+"/match", map[string]string{"volume_id": "v1"})
	s.Require().Equal(http.StatusOK, w.Code)

	book, err := s.q.GetBook(s.T().Context(), id)
	s.Require().NoError(err)
	s.EqualValues(1, book.ManuallyMatched, "Fix Match locks the book regardless of the field diff")
}

// 3.9: a Fix Match records enrichment_checked even when the chosen volume carries
// no annotation, so a later view must NOT re-trigger online enrichment on the
// still-empty annotation. needsEnrichment alone (which inspects only the
// annotation, not manually_matched) would still say the book qualifies — the
// enrichment_checked guard in runLazyTiers is what suppresses the re-query, and
// this locks that the Fix Match sets it.
func (s *matchSuite) TestApplyMatchSuppressesLaterEnrichment() {
	enr := &fakeEnricher{
		applyMeta: ebook.Metadata{Title: "Chosen"},      // the match leaves the annotation empty
		meta:      ebook.Metadata{Annotation: "online"}, // a later Enrich would have data to add
		ok:        true,
	}
	s.handlerWith(enr)

	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "Original"}) // no annotation

	w := s.do(http.MethodPost, "/books/"+itoa(id)+"/match", map[string]string{"volume_id": "v1"})
	s.Require().Equal(http.StatusOK, w.Code)

	book, err := s.q.GetBook(s.T().Context(), id)
	s.Require().NoError(err)
	s.Require().False(book.Annotation.Valid, "the chosen volume carried no annotation")
	s.Require().EqualValues(1, book.EnrichmentChecked, "the match records enrichment as attempted")

	// A later view of the still-annotation-less book must not consult the online tier.
	w = s.do(http.MethodGet, "/books/"+itoa(id), nil)
	s.Require().Equal(http.StatusOK, w.Code)
	s.Zero(enr.called, "a manually-matched book must not re-trigger online enrichment despite an empty annotation")
}

// L6: a Fix Match must not interleave with an in-flight lazy tier on the book.
func (s *matchSuite) TestApplyMatchBusyIs409() {
	enr := &fakeEnricher{applyMeta: ebook.Metadata{Title: "Chosen"}}
	s.handlerWith(enr)

	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "Original"})

	s.Require().True(s.books.claimLazy(id)) // simulate a concurrent first-view enrichment
	defer s.books.releaseLazy(id)

	w := s.do(http.MethodPost, "/books/"+itoa(id)+"/match", map[string]string{"volume_id": "v1"})
	s.Equal(http.StatusConflict, w.Code)
}

func (s *matchSuite) TestApplyMatchMissingVolumeID() {
	s.handlerWith(&fakeEnricher{})
	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "x"})

	w := s.do(http.MethodPost, "/books/"+itoa(id)+"/match", map[string]string{})
	s.Equal(http.StatusBadRequest, w.Code)
}

func (s *matchSuite) TestMatchDisabledWithoutEnricher() {
	s.handlerWith(nil)
	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "x"})

	w := s.do(http.MethodGet, "/books/"+itoa(id)+"/match?q=dune", nil)
	s.Equal(http.StatusNotImplemented, w.Code)
}

package opds

import (
	"context"
	"log/slog"
	"net/http"
	"sync"

	"github.com/go-chi/chi/v5"

	"github.com/Toshik1978/folio/internal/db/dbq"
)

// fakeFiller records which book ids were backfilled and lets a case simulate
// the offline tier populating annotation.
type fakeFiller struct {
	mu    sync.Mutex
	calls []int64
	fill  func(id int64) // optional side effect (e.g. write annotation to the test DB)
}

func (f *fakeFiller) Fill(_ context.Context, id int64) error {
	f.mu.Lock()
	f.calls = append(f.calls, id)
	f.mu.Unlock()
	if f.fill != nil {
		f.fill(id)
	}

	return nil
}

func (f *fakeFiller) ids() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]int64(nil), f.calls...)
	return out
}

// enrichSuite reuses baseSuite's seeding/get helpers but rebuilds the handler
// with a fake (or nil) filler before each request.
type enrichSuite struct {
	baseSuite
}

// rebuild swaps in a handler/router backed by the given filler.
func (s *enrichSuite) rebuild(filler MetadataFiller) {
	s.handler = New(slog.New(slog.DiscardHandler), s.db, s.covers, filler, s.authn, "")
	r := chi.NewRouter()
	s.handler.Register(r)
	s.router = r
}

// setAnnotation persists an annotation (and marks the book checked), mirroring
// the offline tier writing back what it parsed from the file.
func (s *enrichSuite) setAnnotation(id int64, text string) {
	s.Require().NoError(s.q.UpdateBookAnnotation(context.Background(), dbq.UpdateBookAnnotationParams{
		Annotation: nullStr(text), ID: id,
	}))
}

func (s *enrichSuite) TestSearchBackfillsUncheckedBooksOnly() {
	s.setCreds()
	src := s.seedSource("folder", "/lib")
	a := s.seedBook(src, bookSeed{Title: "Foundation"}) // stays unchecked
	b := s.seedBook(src, bookSeed{Title: "Dune"})       // marked checked below
	s.Require().NoError(s.q.MarkMetadataChecked(context.Background(), b))

	ff := &fakeFiller{}
	s.rebuild(ff)

	w := s.getAuth("/search", testUser, testPass)
	s.Require().Equal(http.StatusOK, w.Code)

	s.Contains(ff.ids(), a)    // unchecked book backfilled
	s.NotContains(ff.ids(), b) // already-checked book skipped
}

func (s *enrichSuite) TestSearchRendersBackfilledAnnotation() {
	s.setCreds()
	src := s.seedSource("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "Foundation"}) // no annotation yet

	ff := &fakeFiller{fill: func(i int64) {
		// simulate the offline tier: write annotation + mark checked in the test DB
		s.setAnnotation(i, "Backfilled summary.")
	}}
	s.rebuild(ff)

	w := s.getAuth("/search", testUser, testPass)
	s.Require().Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), "Backfilled summary.") // re-read row reflects the fill
	s.Equal([]int64{id}, ff.ids())
}

func (s *enrichSuite) TestNavigationFeedsDoNotBackfill() {
	s.setCreds()
	src := s.seedSource("folder", "/lib")
	s.seedBook(src, bookSeed{Title: "Foundation", Authors: []string{"Asimov"}})

	ff := &fakeFiller{}
	s.rebuild(ff)

	w := s.getAuth("/authors", testUser, testPass) // navigation feed, not acquisition
	s.Require().Equal(http.StatusOK, w.Code)
	s.Empty(ff.ids())
}

func (s *enrichSuite) TestNilFillerSkipsBackfill() {
	s.setCreds()
	src := s.seedSource("folder", "/lib")
	s.seedBook(src, bookSeed{Title: "Foundation"})

	s.rebuild(nil)

	w := s.getAuth("/search", testUser, testPass)
	s.Require().Equal(http.StatusOK, w.Code) // renders from DB, no panic
}

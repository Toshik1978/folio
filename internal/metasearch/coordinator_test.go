package metasearch

import (
	"context"
	"errors"
	"log/slog"

	"github.com/Toshik1978/folio/internal/ebook"
)

// fakeMeta is a MetadataSource backed by canned results.
type fakeMeta struct {
	name      string
	searchOut []Volume
	searchErr error
	getOut    ebook.Metadata
	getErr    error
	lastQuery Query
}

func (f *fakeMeta) Name() string               { return f.name }
func (f *fakeMeta) Capabilities() []Capability { return []Capability{CapIdentify} }
func (f *fakeMeta) Search(_ context.Context, q Query) ([]Volume, error) {
	f.lastQuery = q

	return f.searchOut, f.searchErr
}

func (f *fakeMeta) Get(context.Context, string) (ebook.Metadata, error) {
	return f.getOut, f.getErr
}

// fakeLookup returns a fixed query for a known book.
type fakeLookup struct {
	q  Query
	ok bool
}

func (l fakeLookup) Lookup(context.Context, int64) (Query, bool, error) { return l.q, l.ok, nil }

func coord(reg *Registry, lookup BookLookup) *Coordinator {
	return NewCoordinator(slog.New(slog.DiscardHandler), reg, lookup)
}

func (s *coreSuite) TestCoordinatorSearchMerges() {
	gb := &fakeMeta{name: SourceGoogleBooks, searchOut: []Volume{{Source: SourceGoogleBooks, ID: "1", Title: "Dune"}}}
	c := coord(NewRegistry(gb), fakeLookup{})

	got, err := c.Search(context.Background(), "Dune")
	s.Require().NoError(err)
	s.Require().Len(got, 1)
	s.Equal("Dune", gb.lastQuery.Title, "free-text search seeds the Title")
}

func (s *coreSuite) TestCoordinatorSearchPropagatesSoleError() {
	gb := &fakeMeta{name: SourceGoogleBooks, searchErr: errors.New("boom")}
	c := coord(NewRegistry(gb), fakeLookup{})

	_, err := c.Search(context.Background(), "Dune")
	s.Error(err, "a sole failing source surfaces as an error (preserves 502)")
}

func (s *coreSuite) TestCoordinatorApplyMatchRoutesBySource() {
	gb := &fakeMeta{name: SourceGoogleBooks, getOut: ebook.Metadata{Title: "Dune"}}
	c := coord(NewRegistry(gb), fakeLookup{})

	meta, err := c.ApplyMatch(context.Background(), SourceGoogleBooks, "1")
	s.Require().NoError(err)
	s.Equal("Dune", meta.Title)

	_, err = c.ApplyMatch(context.Background(), "nope", "1")
	s.Error(err, "unknown source is an error")
}

func (s *coreSuite) TestCoordinatorApplyMatchEmptySourceFallsBack() {
	gb := &fakeMeta{name: SourceGoogleBooks, getOut: ebook.Metadata{Title: "Dune"}}
	c := coord(NewRegistry(gb), fakeLookup{})

	meta, err := c.ApplyMatch(context.Background(), "", "1")
	s.Require().NoError(err, "an empty source (legacy body) tries each metadata source")
	s.Equal("Dune", meta.Title)
}

func (s *coreSuite) TestCoordinatorEnrichUsesLookupAndGets() {
	gb := &fakeMeta{
		name:      SourceGoogleBooks,
		searchOut: []Volume{{Source: SourceGoogleBooks, ID: "1"}},
		getOut:    ebook.Metadata{Title: "Dune"},
	}
	c := coord(NewRegistry(gb), fakeLookup{q: Query{ISBN: "9780441013593"}, ok: true})

	meta, ok, err := c.Enrich(context.Background(), 42)
	s.Require().NoError(err)
	s.Require().True(ok)
	s.Equal("Dune", meta.Title)
	s.Equal("9780441013593", gb.lastQuery.ISBN, "Enrich searches with the looked-up ISBN")
}

func (s *coreSuite) TestCoordinatorEnrichNoMatch() {
	gb := &fakeMeta{name: SourceGoogleBooks, searchOut: nil}
	c := coord(NewRegistry(gb), fakeLookup{q: Query{Title: "x"}, ok: true})

	_, ok, err := c.Enrich(context.Background(), 42)
	s.Require().NoError(err)
	s.False(ok)
}

func (s *coreSuite) TestCoordinatorEnrichUnknownBook() {
	c := coord(NewRegistry(&fakeMeta{name: SourceGoogleBooks}), fakeLookup{ok: false})
	_, ok, err := c.Enrich(context.Background(), 42)
	s.Require().NoError(err)
	s.False(ok)
}

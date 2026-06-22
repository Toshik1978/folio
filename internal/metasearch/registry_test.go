package metasearch

import (
	"context"

	"github.com/Toshik1978/folio/internal/ebook"
)

// coverOnly advertises only CapCover.
type coverOnly struct{ name string }

func (c coverOnly) Name() string                                                  { return c.name }
func (c coverOnly) Capabilities() []Capability                                    { return []Capability{CapCover} }
func (c coverOnly) SearchCovers(context.Context, Query) ([]CoverCandidate, error) { return nil, nil }

// dual advertises both but, when its caps say cover-only, must not surface as a
// MetadataSource even though it implements the interface.
type dual struct {
	name string
	caps []Capability
}

func (d dual) Name() string                                                  { return d.name }
func (d dual) Capabilities() []Capability                                    { return d.caps }
func (d dual) SearchCovers(context.Context, Query) ([]CoverCandidate, error) { return nil, nil }
func (d dual) Search(context.Context, Query) ([]Volume, error)               { return nil, nil }
func (d dual) Get(context.Context, string) (ebook.Metadata, error)           { return ebook.Metadata{}, nil }

func (s *coreSuite) TestRegistryFiltersByCapability() {
	d := dual{name: "gb", caps: []Capability{CapCover}} // cover-only for now
	reg := NewRegistry(coverOnly{name: "ol"}, d)

	s.Len(reg.CoverSources(), 2, "both advertise CapCover")
	s.Empty(reg.MetadataSources(), "neither advertises CapIdentify in Phase 2")

	_, ok := reg.MetadataSourceByName("gb")
	s.False(ok, "a cover-only source is not a metadata source")
}

func (s *coreSuite) TestRegistryPromotesDualSource() {
	d := dual{name: "gb", caps: []Capability{CapCover, CapIdentify}}
	reg := NewRegistry(d)

	s.Len(reg.CoverSources(), 1)
	s.Len(reg.MetadataSources(), 1)
	got, ok := reg.MetadataSourceByName("gb")
	s.Require().True(ok)
	s.Equal("gb", got.Name())
}

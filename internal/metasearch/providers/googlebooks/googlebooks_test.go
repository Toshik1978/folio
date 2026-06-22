package googlebooks

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/Toshik1978/folio/internal/ebook"
	gb "github.com/Toshik1978/folio/internal/googlebooks"
	"github.com/Toshik1978/folio/internal/metasearch"
)

// TestGoogleBooks is the package's single entry point; every suite is registered here.
func TestGoogleBooks(t *testing.T) {
	suite.Run(t, new(coversSuite))
	suite.Run(t, new(metadataSuite))
}

// stubClient implements the full Client interface for the adapter tests.
type stubClient struct {
	searchVols []gb.Volume
	queryVols  []gb.Volume
	isbnVol    gb.Volume
	isbnOK     bool
	getVol     gb.Volume
	image      []byte
	err        error
	imageErr   error
}

func (s stubClient) Search(context.Context, string, string) ([]gb.Volume, error) {
	return s.searchVols, s.err
}

func (s stubClient) SearchQuery(context.Context, string) ([]gb.Volume, error) {
	return s.queryVols, s.err
}

func (s stubClient) SearchISBN(context.Context, string) (gb.Volume, bool, error) {
	return s.isbnVol, s.isbnOK, s.err
}

func (s stubClient) GetVolume(context.Context, string) (gb.Volume, error) {
	return s.getVol, s.err
}

func (s stubClient) FetchImage(context.Context, string) ([]byte, error) {
	return s.image, s.imageErr
}

func newVolume(id, title, thumb string) gb.Volume {
	var v gb.Volume
	v.ID = id
	v.VolumeInfo.Title = title
	v.VolumeInfo.ImageLinks.Thumbnail = thumb

	return v
}

// identity mapper for tests: avoids depending on ingest here.
func mapTitle(v gb.Volume) ebook.Metadata {
	return ebook.Metadata{Title: v.VolumeInfo.Title}
}

// coversSuite covers capability reporting and the CoverSource path.
type coversSuite struct {
	suite.Suite
}

func (s *coversSuite) TestDualCapability() {
	src := New(stubClient{}, mapTitle)
	s.True(metasearch.HasCapability(src.Capabilities(), metasearch.CapCover))
	s.True(metasearch.HasCapability(src.Capabilities(), metasearch.CapIdentify))
}

func (s *coversSuite) TestSearchCoversMapsThumbnails() {
	src := New(stubClient{searchVols: []gb.Volume{
		newVolume("1", "Dune", "http://books.google.com/x.jpg"),
		newVolume("2", "No Image", ""),
	}}, mapTitle)

	got, err := src.SearchCovers(context.Background(), metasearch.Query{Title: "Dune"})
	s.Require().NoError(err)
	s.Require().Len(got, 1)
	s.Equal("https://books.google.com/x.jpg", got[0].FullURL)
}

func (s *coversSuite) TestSearchCoversStripsEdgeCurl() {
	src := New(stubClient{searchVols: []gb.Volume{
		newVolume("1", "Dune", "http://books.google.com/x.jpg?zoom=1&edge=curl&source=gbs_api"),
	}}, mapTitle)

	got, err := src.SearchCovers(context.Background(), metasearch.Query{Title: "Dune"})
	s.Require().NoError(err)
	s.Require().Len(got, 1)
	// ThumbURL keeps the original (small, with curl) for the grid preview.
	s.Contains(got[0].ThumbURL, "edge=curl", "ThumbURL must retain edge=curl")
	// FullURL has edge=curl stripped.
	s.NotContains(got[0].FullURL, "edge=curl", "FullURL must not contain edge=curl")
	s.Equal("https://books.google.com/x.jpg?zoom=1&source=gbs_api", got[0].FullURL)
}

// metadataSuite covers the MetadataSource path (Search/Get).
type metadataSuite struct {
	suite.Suite
}

func (s *metadataSuite) TestSearchByISBNUsesISBNLookup() {
	src := New(stubClient{isbnVol: newVolume("isbn1", "Dune", ""), isbnOK: true}, mapTitle)

	got, err := src.Search(context.Background(), metasearch.Query{ISBN: "9780441013593"})
	s.Require().NoError(err)
	s.Require().Len(got, 1)
	s.Equal("isbn1", got[0].ID)
	s.Equal(metasearch.SourceGoogleBooks, got[0].Source)
}

func (s *metadataSuite) TestSearchByTitleAuthorVsRaw() {
	// With an author, uses the structured Search.
	withAuthor := New(stubClient{searchVols: []gb.Volume{newVolume("a", "Dune", "")}}, mapTitle)
	got, err := withAuthor.Search(context.Background(), metasearch.Query{Title: "Dune", Author: "Herbert"})
	s.Require().NoError(err)
	s.Equal("a", got[0].ID)

	// Title only (Fix-Match free text) uses the raw query path.
	rawOnly := New(stubClient{queryVols: []gb.Volume{newVolume("q", "Dune", "")}}, mapTitle)
	got, err = rawOnly.Search(context.Background(), metasearch.Query{Title: "Dune"})
	s.Require().NoError(err)
	s.Equal("q", got[0].ID)
}

func (s *metadataSuite) TestGetMapsAndDownloadsCover() {
	src := New(stubClient{
		getVol: newVolume("g", "Dune", "https://books.google.com/c.jpg"),
		image:  []byte("JPEGBYTES"),
	}, mapTitle)

	meta, err := src.Get(context.Background(), "g")
	s.Require().NoError(err)
	s.Equal("Dune", meta.Title)
	s.Equal([]byte("JPEGBYTES"), meta.Cover, "Get downloads the thumbnail into Cover")
}

// TestGetCoverFetchFailureIsNonFatal verifies the actual non-fatal path:
// GetVolume succeeds (returns a volume with a thumbnail) but FetchImage fails —
// Get must still return metadata with an empty Cover, not an error.
func (s *metadataSuite) TestGetCoverFetchFailureIsNonFatal() {
	src := New(stubClient{
		getVol:   newVolume("g", "Dune", "https://books.google.com/c.jpg"),
		imageErr: errors.New("net"),
	}, mapTitle)

	meta, err := src.Get(context.Background(), "g")
	s.Require().NoError(err, "a failing image fetch is non-fatal")
	s.Equal("Dune", meta.Title)
	s.Empty(meta.Cover, "cover is empty when fetch fails")
}

// TestGetVolumeErrorPropagates verifies that an error from GetVolume itself
// (the shared err field) surfaces as an error from Get.
func (s *metadataSuite) TestGetVolumeErrorPropagates() {
	src := New(stubClient{
		err: errors.New("rpc unavailable"),
	}, mapTitle)

	_, err := src.Get(context.Background(), "g")
	s.Require().Error(err, "GetVolume failing is a real error")
}

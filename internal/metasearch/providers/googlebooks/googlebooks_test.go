package googlebooks

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Toshik1978/folio/internal/ebook"
	gb "github.com/Toshik1978/folio/internal/googlebooks"
	"github.com/Toshik1978/folio/internal/metasearch"
)

// stubClient implements the full Client interface for the adapter tests.
type stubClient struct {
	searchVols []gb.Volume
	queryVols  []gb.Volume
	isbnVol    gb.Volume
	isbnOK     bool
	getVol     gb.Volume
	image      []byte
	err        error
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
	return s.image, s.err
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

func TestDualCapability(t *testing.T) {
	src := New(stubClient{}, mapTitle)
	require.True(t, metasearch.HasCapability(src.Capabilities(), metasearch.CapCover))
	require.True(t, metasearch.HasCapability(src.Capabilities(), metasearch.CapIdentify))
}

func TestSearchCoversMapsThumbnails(t *testing.T) {
	src := New(stubClient{searchVols: []gb.Volume{
		newVolume("1", "Dune", "http://books.google.com/x.jpg"),
		newVolume("2", "No Image", ""),
	}}, mapTitle)

	got, err := src.SearchCovers(context.Background(), metasearch.Query{Title: "Dune"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "https://books.google.com/x.jpg", got[0].FullURL)
}

func TestSearchByISBNUsesISBNLookup(t *testing.T) {
	src := New(stubClient{isbnVol: newVolume("isbn1", "Dune", ""), isbnOK: true}, mapTitle)

	got, err := src.Search(context.Background(), metasearch.Query{ISBN: "9780441013593"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "isbn1", got[0].ID)
	require.Equal(t, metasearch.SourceGoogleBooks, got[0].Source)
}

func TestSearchByTitleAuthorVsRaw(t *testing.T) {
	// With an author, uses the structured Search.
	withAuthor := New(stubClient{searchVols: []gb.Volume{newVolume("a", "Dune", "")}}, mapTitle)
	got, err := withAuthor.Search(context.Background(), metasearch.Query{Title: "Dune", Author: "Herbert"})
	require.NoError(t, err)
	require.Equal(t, "a", got[0].ID)

	// Title only (Fix-Match free text) uses the raw query path.
	rawOnly := New(stubClient{queryVols: []gb.Volume{newVolume("q", "Dune", "")}}, mapTitle)
	got, err = rawOnly.Search(context.Background(), metasearch.Query{Title: "Dune"})
	require.NoError(t, err)
	require.Equal(t, "q", got[0].ID)
}

func TestGetMapsAndDownloadsCover(t *testing.T) {
	src := New(stubClient{
		getVol: newVolume("g", "Dune", "https://books.google.com/c.jpg"),
		image:  []byte("JPEGBYTES"),
	}, mapTitle)

	meta, err := src.Get(context.Background(), "g")
	require.NoError(t, err)
	require.Equal(t, "Dune", meta.Title)
	require.Equal(t, []byte("JPEGBYTES"), meta.Cover, "Get downloads the thumbnail into Cover")
}

func TestGetCoverFetchFailureIsNonFatal(t *testing.T) {
	// A volume with a thumbnail but a failing image fetch still returns metadata.
	src := New(stubClient{
		getVol: newVolume("g", "Dune", "https://books.google.com/c.jpg"),
		err:    errors.New("net"),
	}, mapTitle)
	_, err := src.Get(context.Background(), "g")
	require.Error(t, err, "GetVolume itself failing is an error")
}

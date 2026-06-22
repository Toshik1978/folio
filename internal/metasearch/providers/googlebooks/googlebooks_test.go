package googlebooks

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	gb "github.com/Toshik1978/folio/internal/googlebooks"
	"github.com/Toshik1978/folio/internal/metasearch"
)

type stubClient struct {
	vols []gb.Volume
	err  error
}

func (s stubClient) Search(context.Context, string, string) ([]gb.Volume, error) {
	return s.vols, s.err
}

func newVolume(id, title, thumb string) gb.Volume {
	var v gb.Volume
	v.ID = id
	v.VolumeInfo.Title = title
	v.VolumeInfo.ImageLinks.Thumbnail = thumb

	return v
}

func TestSearchCoversMapsThumbnailsAndUpgradesHTTP(t *testing.T) {
	src := New(stubClient{vols: []gb.Volume{
		newVolume("1", "Dune", "http://books.google.com/x.jpg"), // http upgraded to https
		newVolume("2", "No Image", ""),                          // skipped (no thumbnail)
	}})

	got, err := src.SearchCovers(context.Background(), metasearch.Query{Title: "Dune"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, metasearch.SourceGoogleBooks, got[0].Source)
	require.Equal(t, "https://books.google.com/x.jpg", got[0].FullURL)
}

func TestCoverOnlyInPhase2(t *testing.T) {
	src := New(stubClient{})
	require.True(t, metasearch.HasCapability(src.Capabilities(), metasearch.CapCover))
	require.False(t, metasearch.HasCapability(src.Capabilities(), metasearch.CapIdentify),
		"Phase 2 keeps Google Books cover-only; CapIdentify is promoted in Phase 3")
}

package goodreads

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Toshik1978/folio/internal/metasearch"
)

func TestParseCoversFromFixture(t *testing.T) {
	f, err := os.Open("testdata/search.html")
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	got, err := parseCovers(f)
	require.NoError(t, err)
	require.Len(t, got, 2, "two bookCover images; the site logo is ignored")
	for _, c := range got {
		require.Equal(t, metasearch.SourceGoodreads, c.Source)
		require.NotEmpty(t, c.FullURL)
	}
	// Goodreads serves small thumbnails (_SX50_); upgrade to a larger size for FullURL.
	require.Equal(t, "https://images-na.ssl-images-amazon.com/images/S/aaa._SX320_.jpg", got[0].FullURL)
}

func TestCapabilities(t *testing.T) {
	src := New(time.Second)
	require.Equal(t, metasearch.SourceGoodreads, src.Name())
	require.True(t, metasearch.HasCapability(src.Capabilities(), metasearch.CapCover))
}

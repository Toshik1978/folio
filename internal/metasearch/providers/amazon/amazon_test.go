package amazon

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

	// Two s-image results; the sprite (other-image) is ignored.
	require.Len(t, got, 2)
	for _, c := range got {
		require.Equal(t, metasearch.SourceAmazon, c.Source)
		require.Contains(t, c.FullURL, "https://m.media-amazon.com/images/I/")
	}
	// The highest-density srcset entry is chosen as FullURL.
	require.Equal(t, "https://m.media-amazon.com/images/I/aaa._AC_UL320_.jpg", got[0].FullURL)
	require.Equal(t, "https://m.media-amazon.com/images/I/bbb._AC_UL640_.jpg", got[1].FullURL)
}

func TestCapabilities(t *testing.T) {
	src := New(time.Second)
	require.Equal(t, metasearch.SourceAmazon, src.Name())
	require.True(t, metasearch.HasCapability(src.Capabilities(), metasearch.CapCover))
}

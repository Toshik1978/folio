package amazon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
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
	// The highest-density srcset entry is chosen and the size modifier is stripped to get the original.
	require.Equal(t, "https://m.media-amazon.com/images/I/aaa.jpg", got[0].FullURL)
	require.Equal(t, "https://m.media-amazon.com/images/I/bbb.jpg", got[1].FullURL)
}

func TestCapabilities(t *testing.T) {
	src := New(time.Second)
	require.Equal(t, metasearch.SourceAmazon, src.Name())
	require.True(t, metasearch.HasCapability(src.Capabilities(), metasearch.CapCover))
}

func TestSearchCovers(t *testing.T) {
	data, err := os.ReadFile("testdata/search.html")
	require.NoError(t, err)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	src := New(5 * time.Second)
	src.BaseURL = srv.URL

	got, err := src.SearchCovers(context.Background(), metasearch.Query{Title: "Dune"})
	require.NoError(t, err)
	require.NotEmpty(t, got)
	for _, c := range got {
		require.Equal(t, metasearch.SourceAmazon, c.Source)
	}
}

func TestSearchCoversNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	src := New(5 * time.Second)
	src.BaseURL = srv.URL

	_, err := src.SearchCovers(context.Background(), metasearch.Query{Title: "Dune"})
	require.Error(t, err)
}

func TestSearchCoversRetriesOnTransientBlock(t *testing.T) {
	data, err := os.ReadFile("testdata/search.html")
	require.NoError(t, err)

	var reqCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := reqCount.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	// Lower backoff so the test doesn't wait 400 ms.
	orig := retryBackoff
	retryBackoff = time.Millisecond
	defer func() { retryBackoff = orig }()

	src := New(5 * time.Second)
	src.BaseURL = srv.URL

	got, err := src.SearchCovers(context.Background(), metasearch.Query{Title: "Dune"})
	require.NoError(t, err)
	require.NotEmpty(t, got)
	require.GreaterOrEqual(t, int(reqCount.Load()), 2, "server should have been hit at least twice")
}

package goodreads

import (
	"context"
	"net/http"
	"net/http/httptest"
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
		require.Equal(t, metasearch.SourceGoodreads, c.Source)
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

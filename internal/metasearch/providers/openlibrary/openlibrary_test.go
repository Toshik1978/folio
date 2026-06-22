package openlibrary

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Toshik1978/folio/internal/metasearch"
)

func TestSearchCovers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal( //nolint:testifylint // require in handler: brief mandates verbatim
			t,
			"/search.json",
			r.URL.Path,
		)
		require.Equal( //nolint:testifylint // require in handler: brief mandates verbatim
			t,
			"Dune",
			r.URL.Query().Get("title"),
		)
		_, _ = w.Write([]byte(`{"docs":[
			{"title":"Dune","cover_i":123},
			{"title":"No Cover"}
		]}`))
	}))
	defer srv.Close()

	src := New(5 * time.Second)
	src.BaseURL = srv.URL

	got, err := src.SearchCovers(context.Background(), metasearch.Query{Title: "Dune"})
	require.NoError(t, err)
	require.Len(t, got, 1, "only docs with cover_i yield candidates")
	require.Equal(t, metasearch.SourceOpenLibrary, got[0].Source)
	require.Equal(t, "https://covers.openlibrary.org/b/id/123-L.jpg", got[0].FullURL)
	require.Equal(t, "https://covers.openlibrary.org/b/id/123-M.jpg", got[0].ThumbURL)
}

func TestCapabilities(t *testing.T) {
	src := New(time.Second)
	require.Equal(t, metasearch.SourceOpenLibrary, src.Name())
	require.True(t, metasearch.HasCapability(src.Capabilities(), metasearch.CapCover))
}

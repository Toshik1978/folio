package goodreads

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/Toshik1978/folio/internal/metasearch"
)

func TestGoodreads(t *testing.T) {
	suite.Run(t, new(parseSuite))
	suite.Run(t, new(searchSuite))
}

type baseSuite struct {
	suite.Suite
}

// fixture returns the golden Goodreads autocomplete JSON.
func (s *baseSuite) fixture() []byte {
	data, err := os.ReadFile("testdata/autocomplete.json")
	s.Require().NoError(err)

	return data
}

// sourceForHandler starts an httptest server with h and returns a Source wired
// to it with a near-zero retry backoff.
func (s *baseSuite) sourceForHandler(h http.HandlerFunc) *Source {
	srv := httptest.NewServer(h)
	s.T().Cleanup(srv.Close)

	src := New(5 * time.Second)
	src.baseURL = srv.URL
	src.backoff = time.Millisecond

	return src
}

type parseSuite struct {
	baseSuite
}

func (s *parseSuite) TestParseCoversMapsAllTitledItems() {
	f, err := os.Open("testdata/autocomplete.json")
	s.Require().NoError(err)
	defer func() { _ = f.Close() }()

	got, err := parseCovers(f)
	s.Require().NoError(err)
	// All three image-bearing items are mapped (the empty-image item is skipped);
	// relevance filtering of the boxed set now happens in the aggregator.
	s.Require().Len(got, 3)
	for _, c := range got {
		s.Equal(metasearch.SourceGoodreads, c.Source)
		s.NotEmpty(c.FullURL)
		s.NotEmpty(c.Title, "each candidate carries its title for aggregator filtering")
	}
}

func (s *parseSuite) TestCapabilities() {
	src := New(time.Second)
	s.Equal(metasearch.SourceGoodreads, src.Name())
	s.True(metasearch.HasCapability(src.Capabilities(), metasearch.CapCover))
}

type searchSuite struct {
	baseSuite
}

func (s *searchSuite) TestSearchCovers() {
	src := s.sourceForHandler(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(s.fixture())
	})

	got, err := src.SearchCovers(context.Background(), metasearch.Query{Title: "Dune"})
	s.Require().NoError(err)
	s.Require().Len(got, 3)
	for _, c := range got {
		s.Equal(metasearch.SourceGoodreads, c.Source)
	}
}

func (s *searchSuite) TestSearchCovers202IsTerminalBlock() {
	var reqCount atomic.Int32
	src := s.sourceForHandler(func(w http.ResponseWriter, _ *http.Request) {
		reqCount.Add(1)
		w.WriteHeader(http.StatusAccepted) // 202 = Cloudflare challenge
	})

	_, err := src.SearchCovers(context.Background(), metasearch.Query{Title: "Dune"})
	s.Require().Error(err)
	s.Require().ErrorIs(err, metasearch.ErrBlocked)
	s.Require().ErrorIs(err, metasearch.ErrNoRetry)
	s.Equal(int32(1), reqCount.Load(), "a Cloudflare 202 won't clear in the retry budget: no retry")
}

func (s *searchSuite) TestSearchCoversRetriesOnTransientBlock() {
	var reqCount atomic.Int32
	src := s.sourceForHandler(func(w http.ResponseWriter, _ *http.Request) {
		if reqCount.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)

			return
		}
		_, _ = w.Write(s.fixture())
	})

	got, err := src.SearchCovers(context.Background(), metasearch.Query{Title: "Dune"})
	s.Require().NoError(err)
	s.Require().NotEmpty(got)
	s.GreaterOrEqual(int(reqCount.Load()), 2)
}

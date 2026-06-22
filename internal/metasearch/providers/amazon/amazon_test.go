package amazon

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

// TestAmazon is the package's single entry point; every suite is registered here.
func TestAmazon(t *testing.T) {
	suite.Run(t, new(parseSuite))
	suite.Run(t, new(searchSuite))
}

// baseSuite holds helpers shared by the parser and HTTP suites.
type baseSuite struct {
	suite.Suite
}

// fixture returns the golden Amazon search-result HTML.
func (s *baseSuite) fixture() []byte {
	data, err := os.ReadFile("testdata/search.html")
	s.Require().NoError(err)

	return data
}

// sourceForHandler starts an httptest server with h (cleaned up automatically)
// and returns a Source wired to it with a near-zero retry backoff so retry tests
// don't wait the full 400 ms.
func (s *baseSuite) sourceForHandler(h http.HandlerFunc) *Source {
	srv := httptest.NewServer(h)
	s.T().Cleanup(srv.Close)

	src := New(5 * time.Second)
	src.baseURL = srv.URL
	src.backoff = time.Millisecond

	return src
}

// parseSuite covers the offline HTML parser and capability reporting.
type parseSuite struct {
	baseSuite
}

func (s *parseSuite) TestParseCoversFromFixture() {
	f, err := os.Open("testdata/search.html")
	s.Require().NoError(err)
	defer func() { _ = f.Close() }()

	got, err := parseCovers(f)
	s.Require().NoError(err)

	// Two s-image results; the sprite (other-image) is ignored.
	s.Require().Len(got, 2)
	for _, c := range got {
		s.Equal(metasearch.SourceAmazon, c.Source)
		s.Contains(c.FullURL, "https://m.media-amazon.com/images/I/")
	}
	// The highest-density srcset entry is chosen and the size modifier is stripped to get the original.
	s.Equal("https://m.media-amazon.com/images/I/aaa.jpg", got[0].FullURL)
	s.Equal("https://m.media-amazon.com/images/I/bbb.jpg", got[1].FullURL)
}

func (s *parseSuite) TestCapabilities() {
	src := New(time.Second)
	s.Equal(metasearch.SourceAmazon, src.Name())
	s.True(metasearch.HasCapability(src.Capabilities(), metasearch.CapCover))
}

// searchSuite drives SearchCovers end-to-end over HTTP.
type searchSuite struct {
	baseSuite
}

func (s *searchSuite) TestSearchCovers() {
	src := s.sourceForHandler(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(s.fixture())
	})

	got, err := src.SearchCovers(context.Background(), metasearch.Query{Title: "Dune"})
	s.Require().NoError(err)
	s.Require().NotEmpty(got)
	for _, c := range got {
		s.Equal(metasearch.SourceAmazon, c.Source)
	}
}

func (s *searchSuite) TestSearchCoversNon200() {
	src := s.sourceForHandler(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	_, err := src.SearchCovers(context.Background(), metasearch.Query{Title: "Dune"})
	s.Require().Error(err)
}

func (s *searchSuite) TestSearchCoversRetriesOnTransientBlock() {
	var reqCount atomic.Int32
	src := s.sourceForHandler(func(w http.ResponseWriter, _ *http.Request) {
		n := reqCount.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)

			return
		}
		_, _ = w.Write(s.fixture())
	})

	got, err := src.SearchCovers(context.Background(), metasearch.Query{Title: "Dune"})
	s.Require().NoError(err)
	s.Require().NotEmpty(got)
	s.GreaterOrEqual(int(reqCount.Load()), 2, "server should have been hit at least twice")
}

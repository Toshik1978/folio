package amazon

import (
	"bytes"
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
	src.limiter = newRateLimiter(0)

	return src
}

// parseSuite covers the offline HTML parser and capability reporting.
type parseSuite struct {
	baseSuite
}

func (s *parseSuite) TestParseCandidatesFromFixture() {
	f, err := os.Open("testdata/search.html")
	s.Require().NoError(err)
	defer func() { _ = f.Close() }()

	got, err := parseCandidates(f)
	s.Require().NoError(err)

	// Three s-image results; the sprite (other-image) is ignored.
	s.Require().Len(got, 3)
	s.Equal("Dune", got[0].title)
	s.Equal("https://m.media-amazon.com/images/I/aaa.jpg", got[0].cover.FullURL)
	s.Equal("https://m.media-amazon.com/images/I/bbb.jpg", got[1].cover.FullURL)
}

func (s *parseSuite) TestParseThenFilterDropsUnrelated() {
	f, err := os.Open("testdata/search.html")
	s.Require().NoError(err)
	defer func() { _ = f.Close() }()

	cands, err := parseCandidates(f)
	s.Require().NoError(err)

	got := filterByTitle(cands, "Dune", maxCandidates)
	// "The Notebook" is dropped; the two Dune editions remain.
	s.Require().Len(got, 2)
	s.Equal("https://m.media-amazon.com/images/I/aaa.jpg", got[0].FullURL)
	s.Equal("https://m.media-amazon.com/images/I/bbb.jpg", got[1].FullURL)
}

func (s *parseSuite) TestPickSrcsetSkipsMalformedAndPicksHighest() {
	// Malformed descriptor before any valid entry must be skipped; highest valid wins.
	srcset := "https://img/bad.jpg garbage, https://img/lo.jpg 1x, https://img/hi.jpg 3x"
	s.Equal("https://img/hi.jpg", highestDensity(srcset))

	// A srcset that contains only a malformed entry should return "".
	s.Empty(highestDensity("https://img/bad.jpg garbage"))

	// Equal densities: first entry wins (strict > keeps the earlier one).
	s.Equal("https://img/first.jpg", highestDensity("https://img/first.jpg 2x, https://img/second.jpg 2x"))
}

// TestBenignPhraseNotBlocked verifies a real results page that merely contains
// the generic phrase "something went wrong" in body text (e.g. a review) is not
// treated as an anti-bot interstitial.
func (s *parseSuite) TestBenignPhraseNotBlocked() {
	body := []byte(`<!doctype html><html><body>
<p>A gripping tale where something went wrong for the hero.</p>
<img class="s-image" src="https://m.media-amazon.com/images/I/aaa._AC_UY218_.jpg">
</body></html>`)
	s.False(isInterstitial(body), "generic phrase in body text must not be a block")

	out, err := parseCandidates(bytes.NewReader(body))
	s.Require().NoError(err)
	s.Require().NotEmpty(out)
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

func (s *searchSuite) TestSearchCoversNon200IsBlocked() {
	src := s.sourceForHandler(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	_, err := src.SearchCovers(context.Background(), metasearch.Query{Title: "Dune"})
	s.Require().Error(err)
	s.Require().ErrorIs(err, metasearch.ErrBlocked)
}

func (s *searchSuite) TestSearchCoversCaptchaIsBlocked() {
	captcha, err := os.ReadFile("testdata/captcha.html")
	s.Require().NoError(err)
	src := s.sourceForHandler(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(captcha) // HTTP 200 with a CAPTCHA body
	})

	_, err = src.SearchCovers(context.Background(), metasearch.Query{Title: "Dune"})
	s.Require().Error(err)
	s.Require().ErrorIs(err, metasearch.ErrBlocked)
}

func (s *searchSuite) TestSearchCoversInterstitialNotRetried() {
	var reqCount atomic.Int32
	src := s.sourceForHandler(func(w http.ResponseWriter, _ *http.Request) {
		reqCount.Add(1)
		// Minimal Akamai bot-manager interstitial stub (HTTP 200).
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>&nbsp;</title>` +
			`<meta http-equiv="refresh" content="5; URL='/s?bm-verify=abc'"></head>` +
			`<body><script>function triggerInterstitialChallenge(){}</script></body></html>`))
	})

	_, err := src.SearchCovers(context.Background(), metasearch.Query{Title: "Dune"})
	s.Require().Error(err)
	s.Require().ErrorIs(err, metasearch.ErrBlocked)
	s.Equal(int32(1), reqCount.Load(), "a hard interstitial block must not be retried")
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

func (s *searchSuite) TestDirectSearchIsThrottled() {
	src := s.sourceForHandler(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(s.fixture())
	})
	// Give the limiter a real interval and confirm two back-to-back searches
	// are spaced by it (the helper otherwise sets a zero interval for speed).
	src.limiter = newRateLimiter(60 * time.Millisecond)

	start := time.Now()
	_, err := src.SearchCovers(context.Background(), metasearch.Query{Title: "Dune"})
	s.Require().NoError(err)
	_, err = src.SearchCovers(context.Background(), metasearch.Query{Title: "Dune"})
	s.Require().NoError(err)
	s.GreaterOrEqual(time.Since(start), 60*time.Millisecond)
}

func (s *searchSuite) TestRateLimiterEnforcesInterval() {
	rl := newRateLimiter(40 * time.Millisecond)
	start := time.Now()
	s.Require().NoError(rl.wait(context.Background())) // first: no wait
	s.Require().NoError(rl.wait(context.Background())) // second: waits ~interval
	s.GreaterOrEqual(time.Since(start), 40*time.Millisecond)
}

func (s *searchSuite) TestCheckRedirectBoundsDepth() {
	src := New(time.Second)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		"https://www.amazon.com/x/dp/B01", http.NoBody)
	s.Require().NoError(err)

	// Within the limit: allowed. At the limit: stopped.
	s.Require().NoError(src.checkRedirect(req, nil))
	via := make([]*http.Request, maxRedirects)
	s.Require().Error(src.checkRedirect(req, via))
}

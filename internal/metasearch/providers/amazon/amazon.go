// Package amazon is a metasearch CoverSource that scrapes Amazon search-result
// thumbnails. Scraping is accepted here: a private personal server, a handful
// of one-off lookups, and a maintainer who fixes the parser when markup drifts.
package amazon

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/Toshik1978/folio/internal/metasearch"
)

const (
	defaultBaseURL = "https://www.amazon.com"
	maxHTMLBytes   = 4 << 20
	maxAttempts    = 3
	retryBackoff   = 400 * time.Millisecond
	politeInterval = time.Second
	maxRedirects   = 5
)

// Source scrapes Amazon for cover candidates.
type Source struct {
	baseURL string
	client  *http.Client
	backoff time.Duration
	limiter *rateLimiter
}

// New builds an Amazon cover source with the given per-request timeout.
func New(timeout time.Duration) *Source {
	s := &Source{
		baseURL: defaultBaseURL,
		backoff: retryBackoff,
		limiter: newRateLimiter(politeInterval),
	}
	s.client = &http.Client{Timeout: timeout, CheckRedirect: s.checkRedirect}

	return s
}

// Name identifies the source.
func (s *Source) Name() string { return metasearch.SourceAmazon }

// Capabilities reports cover support.
func (s *Source) Capabilities() []metasearch.Capability {
	return []metasearch.Capability{metasearch.CapCover}
}

// SearchCovers scrapes Amazon's search-results page for cover candidates. It is
// best-effort: when Amazon serves a bot interstitial the error wraps
// ErrBlocked and the aggregator simply drops this source for that query.
func (s *Source) SearchCovers(ctx context.Context, q metasearch.Query) ([]metasearch.CoverCandidate, error) {
	out, err := s.searchDirect(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("amazon search: %w", err)
	}

	return out, nil
}

// checkRedirect bounds redirect depth on the direct Amazon fetch. The host
// allowlist that previously guarded externally-sourced URLs went away with the
// DuckDuckGo fallback; the only request target now is Amazon itself.
func (s *Source) checkRedirect(_ *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("amazon: stopped after %d redirects", maxRedirects)
	}

	return nil
}

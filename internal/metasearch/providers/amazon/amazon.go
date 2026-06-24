// Package amazon is a metasearch CoverSource that scrapes Amazon search-result
// thumbnails, with a DuckDuckGo fallback when Amazon serves an anti-bot
// interstitial. Scraping is accepted here: a private personal server, a handful
// of one-off lookups, and a maintainer who fixes the parser when markup drifts.
package amazon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Toshik1978/folio/internal/metasearch"
)

const (
	defaultBaseURL = "https://www.amazon.com"
	defaultDDGURL  = "https://html.duckduckgo.com"
	maxHTMLBytes   = 4 << 20
	maxAttempts    = 3
	retryBackoff   = 400 * time.Millisecond
	politeInterval = time.Second
	maxRedirects   = 5
)

// Source scrapes Amazon for cover candidates, with a DuckDuckGo fallback.
type Source struct {
	baseURL string
	ddgURL  string
	client  *http.Client
	backoff time.Duration
	limiter *rateLimiter
	// allowProductHost gates which DDG result hosts may be fetched (defense in
	// depth, since URLs come from an external source). Tests override it.
	allowProductHost func(rawURL string) bool
}

// New builds an Amazon cover source with the given per-request timeout.
func New(timeout time.Duration) *Source {
	s := &Source{
		baseURL:          defaultBaseURL,
		ddgURL:           defaultDDGURL,
		backoff:          retryBackoff,
		limiter:          newRateLimiter(politeInterval),
		allowProductHost: isAmazonHost,
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

// SearchCovers tries the direct Amazon scrape and, when Amazon blocks it, falls
// back to a DuckDuckGo product-page lookup.
func (s *Source) SearchCovers(ctx context.Context, q metasearch.Query) ([]metasearch.CoverCandidate, error) {
	out, err := s.searchDirect(ctx, q)
	if err == nil && len(out) > 0 {
		return out, nil
	}
	if !errors.Is(err, metasearch.ErrBlocked) {
		// A non-block error (or genuine nil/empty): nothing the fallback fixes.
		if err != nil {
			return nil, fmt.Errorf("amazon search: %w", err)
		}

		return out, nil
	}

	fb, ferr := s.searchFallback(ctx, q)
	if ferr == nil && len(fb) > 0 {
		return fb, nil
	}
	if ferr != nil {
		// Surface the real fallback failure (a DDG block re-wraps ErrBlocked, so
		// errors.Is still classifies it; a timeout/network error is reported as-is).
		return nil, fmt.Errorf("amazon search: %w", ferr)
	}
	// Fallback ran cleanly but found nothing usable: report the original block.
	return nil, fmt.Errorf("amazon search: %w", metasearch.ErrBlocked)
}

// checkRedirect bounds redirect depth and blocks redirects to hosts that are
// neither an allowed product host (Amazon) nor DuckDuckGo, so an external DDG
// result cannot bounce the client onto an arbitrary internal host.
func (s *Source) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("amazon: stopped after %d redirects", maxRedirects)
	}
	host := strings.ToLower(req.URL.Hostname())
	if s.allowProductHost(req.URL.String()) || host == "duckduckgo.com" || strings.HasSuffix(host, ".duckduckgo.com") {
		return nil
	}

	return fmt.Errorf("amazon: blocked redirect to disallowed host %q", host)
}

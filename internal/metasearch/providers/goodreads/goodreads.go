// Package goodreads is a metasearch CoverSource backed by the Goodreads
// autocomplete JSON endpoint (book/auto_complete). The public /search HTML page
// is fronted by Cloudflare and answers bots with HTTP 202, so the JSON API —
// which returns cover thumbnails directly — is the reliable path.
package goodreads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/Toshik1978/folio/internal/metasearch"
)

const (
	defaultBaseURL = "https://www.goodreads.com"
	maxJSONBytes   = 1 << 20
	maxAttempts    = 3
	retryBackoff   = 400 * time.Millisecond
	// thumbHeight is the uniform pixel height for picker thumbnails; the
	// autocomplete API returns tiny _SY75_ images that render blurry.
	thumbHeight = 450
)

// Source queries Goodreads for cover candidates.
type Source struct {
	baseURL string
	client  *http.Client
	backoff time.Duration
}

// New builds a Goodreads cover source with the given per-request timeout.
func New(timeout time.Duration) *Source {
	return &Source{
		baseURL: defaultBaseURL,
		client:  &http.Client{Timeout: timeout},
		backoff: retryBackoff,
	}
}

// Name identifies the source.
func (s *Source) Name() string { return metasearch.SourceGoodreads }

// Capabilities reports cover support.
func (s *Source) Capabilities() []metasearch.Capability {
	return []metasearch.Capability{metasearch.CapCover}
}

// SearchCovers queries the autocomplete API and maps results to candidates,
// retrying transient anti-bot responses.
func (s *Source) SearchCovers(ctx context.Context, q metasearch.Query) ([]metasearch.CoverCandidate, error) {
	out, err := metasearch.RetryCovers(
		ctx, maxAttempts, s.backoff,
		func(c context.Context) ([]metasearch.CoverCandidate, error) {
			return s.fetchOnce(c, q)
		},
	)
	if err != nil {
		return nil, fmt.Errorf("goodreads search: %w", err)
	}

	return out, nil
}

// autocompleteItem is one element of the Goodreads auto_complete JSON array.
// Title drives relevance filtering: the endpoint freely returns box sets and
// omnibuses (e.g. "… The Three-Body Trilogy (…, Death's End)") whose cover shows
// the whole series rather than the requested book.
type autocompleteItem struct {
	ImageURL      string `json:"imageUrl"`
	Title         string `json:"title"`
	BookTitleBare string `json:"bookTitleBare"`
}

// title returns the best available title for relevance filtering.
func (it autocompleteItem) title() string {
	if it.Title != "" {
		return it.Title
	}

	return it.BookTitleBare
}

// fetchOnce performs a single request to the autocomplete API.
func (s *Source) fetchOnce(ctx context.Context, q metasearch.Query) ([]metasearch.CoverCandidate, error) {
	params := url.Values{}
	params.Set("format", "json")
	params.Set("q", q.SearchTerm())
	req, err := http.NewRequestWithContext(
		ctx, http.MethodGet, s.baseURL+"/book/auto_complete?"+params.Encode(), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", metasearch.RandomUserAgent())
	req.Header.Set("Accept", "application/json,text/plain,*/*")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("goodreads request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusAccepted {
		// 202 is the Cloudflare bot challenge; it will not clear within the retry
		// budget, so stop immediately instead of burning all attempts on it.
		return nil, fmt.Errorf("goodreads cloudflare challenge: %w",
			errors.Join(metasearch.ErrBlocked, metasearch.ErrNoRetry))
	}
	if resp.StatusCode != http.StatusOK {
		// Any other non-200 (e.g. a transient 503) is a retryable block.
		return nil, fmt.Errorf("goodreads status %d: %w", resp.StatusCode, metasearch.ErrBlocked)
	}

	return parseCovers(io.LimitReader(resp.Body, maxJSONBytes))
}

// parseCovers decodes the autocomplete JSON array into cover candidates,
// upgrading each small thumbnail to its full-resolution URL. Items with an
// empty ImageURL are skipped; relevance filtering (e.g. box-set rejection)
// is the aggregator's responsibility.
func parseCovers(r io.Reader) ([]metasearch.CoverCandidate, error) {
	var items []autocompleteItem
	if err := json.NewDecoder(r).Decode(&items); err != nil {
		return nil, fmt.Errorf("decode json: %w", err)
	}
	var out []metasearch.CoverCandidate
	for _, it := range items {
		if it.ImageURL == "" {
			continue
		}
		out = append(out, metasearch.CoverCandidate{
			Source:   metasearch.SourceGoodreads,
			Title:    it.title(),
			ThumbURL: metasearch.ThumbCDNImage(it.ImageURL, thumbHeight),
			FullURL:  metasearch.OriginalCDNImage(it.ImageURL),
		})
	}

	return out, nil
}

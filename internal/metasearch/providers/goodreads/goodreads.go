// Package goodreads is a metasearch CoverSource that scrapes Goodreads
// search-result book covers. Goodreads has had no public API since 2020, so a
// defensive scrape with a golden-HTML parser test is the maintainable option.
package goodreads

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/Toshik1978/folio/internal/metasearch"
)

const (
	defaultBaseURL = "https://www.goodreads.com"
	// userAgent mimics a real browser so Goodreads serves the standard result markup.
	userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36" +
		" (KHTML, like Gecko) Chrome/124.0 Safari/537.36"
	maxHTMLBytes = 4 << 20
	imageClass   = "bookCover"
)

// Source scrapes Goodreads for cover candidates.
type Source struct {
	BaseURL string
	client  *http.Client
}

// New builds a Goodreads cover source with the given per-request timeout.
func New(timeout time.Duration) *Source {
	return &Source{BaseURL: defaultBaseURL, client: &http.Client{Timeout: timeout}}
}

// Name identifies the source.
func (s *Source) Name() string { return metasearch.SourceGoodreads }

// Capabilities reports cover support.
func (s *Source) Capabilities() []metasearch.Capability {
	return []metasearch.Capability{metasearch.CapCover}
}

// SearchCovers fetches the Goodreads search page and parses book-cover thumbnails.
func (s *Source) SearchCovers(ctx context.Context, q metasearch.Query) ([]metasearch.CoverCandidate, error) {
	params := url.Values{}
	params.Set("q", strings.TrimSpace(q.Title+" "+q.Author))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.BaseURL+"/search?"+params.Encode(), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("goodreads request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("goodreads status %d", resp.StatusCode)
	}

	return parseCovers(io.LimitReader(resp.Body, maxHTMLBytes))
}

// parseCovers extracts cover candidates from a Goodreads search document,
// upgrading the small search thumbnail to a larger render for the full URL.
func parseCovers(r io.Reader) ([]metasearch.CoverCandidate, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("parse html: %w", err)
	}
	var out []metasearch.CoverCandidate
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "img" && hasClass(n, imageClass) {
			thumb := attr(n, "src")
			if thumb != "" {
				out = append(out, metasearch.CoverCandidate{
					Source:   metasearch.SourceGoodreads,
					ThumbURL: thumb,
					FullURL:  metasearch.OriginalAmazonImage(thumb),
				})
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	return out, nil
}

// hasClass reports whether n's class attribute contains the given class token.
func hasClass(n *html.Node, class string) bool {
	return slices.Contains(strings.Fields(attr(n, "class")), class)
}

// attr returns n's value for the named attribute, or "".
func attr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}

	return ""
}

package amazon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/html"

	"github.com/Toshik1978/folio/internal/metasearch"
)

const imageClass = "s-image"

// interstitialMarkers are substrings that appear on Amazon's CAPTCHA/robot
// pages (served with HTTP 200), used to distinguish a block from real results.
var interstitialMarkers = []string{ //nolint:gochecknoglobals // immutable lookup table
	"automated access",
	"enter the characters",
	"api-services-support",
	"something went wrong on our end",
	"bm-verify",
	"triggerinterstitialchallenge",
}

// searchDirect fetches Amazon's books search page and parses cover thumbnails,
// retrying transient blocks. It returns ErrBlocked when the response is a
// non-200, a CAPTCHA interstitial, or parses to zero candidates.
func (s *Source) searchDirect(ctx context.Context, q metasearch.Query) ([]metasearch.CoverCandidate, error) {
	out, err := metasearch.RetryCovers(
		ctx, maxAttempts, s.backoff,
		func(c context.Context) ([]metasearch.CoverCandidate, error) {
			return s.fetchDirectOnce(c, q)
		},
	)
	if err != nil {
		return nil, fmt.Errorf("direct search: %w", err)
	}

	return out, nil
}

// fetchDirectOnce performs one direct search request.
func (s *Source) fetchDirectOnce(ctx context.Context, q metasearch.Query) ([]metasearch.CoverCandidate, error) {
	if err := s.limiter.wait(ctx); err != nil {
		return nil, err
	}

	params := url.Values{}
	params.Set("k", q.SearchTerm())
	params.Set("i", "stripbooks")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+"/s?"+params.Encode(), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", metasearch.RandomUserAgent())
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Accept", acceptHTML)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("amazon request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("amazon status %d: %w", resp.StatusCode, metasearch.ErrBlocked)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHTMLBytes))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if isInterstitial(body) {
		// A bot interstitial is a deterministic block; retrying it immediately
		// cannot succeed and only burns IP reputation, so mark it terminal.
		return nil, fmt.Errorf("amazon interstitial: %w",
			errors.Join(metasearch.ErrBlocked, metasearch.ErrNoRetry))
	}

	cands, err := parseCandidates(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if len(cands) == 0 {
		// No s-image nodes on a 200 page is, in practice, a soft block.
		return nil, fmt.Errorf("amazon empty results: %w", metasearch.ErrBlocked)
	}

	return filterByTitle(cands, q.Title, maxCandidates), nil
}

// isInterstitial reports whether body looks like an Amazon anti-bot page.
func isInterstitial(body []byte) bool {
	low := bytes.ToLower(body)
	for _, m := range interstitialMarkers {
		if bytes.Contains(low, []byte(m)) {
			return true
		}
	}

	return false
}

// parseCandidates extracts cover candidates and their result titles (the
// s-image alt) from an Amazon search-results document, in page order.
func parseCandidates(r io.Reader) ([]rawCandidate, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("parse html: %w", err)
	}
	var out []rawCandidate
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "img" && hasClass(n, imageClass) {
			if full := bestImage(n); full != "" {
				out = append(out, rawCandidate{
					cover: metasearch.CoverCandidate{
						Source:   metasearch.SourceAmazon,
						ThumbURL: attr(n, "src"),
						FullURL:  metasearch.OriginalAmazonImage(full),
					},
					title: attr(n, "alt"),
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

// bestImage returns the highest-density srcset URL, or src when there is no srcset.
func bestImage(n *html.Node) string {
	if best := highestDensity(attr(n, "srcset")); best != "" {
		return best
	}

	return attr(n, "src")
}

// highestDensity parses a srcset and returns the URL with the largest density
// descriptor. Entries with an unparsable descriptor are skipped; on a tie the
// first entry wins.
func highestDensity(srcset string) string {
	var bestURL string
	var bestD float64
	for part := range strings.SplitSeq(srcset, ",") {
		fields := strings.Fields(strings.TrimSpace(part))
		if len(fields) == 0 {
			continue
		}
		d := 1.0
		if len(fields) > 1 {
			var err error
			d, err = strconv.ParseFloat(strings.TrimSuffix(fields[1], "x"), 64)
			if err != nil {
				continue
			}
		}
		if d > bestD {
			bestD, bestURL = d, fields[0]
		}
	}

	return bestURL
}

package amazon

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/html"

	"github.com/Toshik1978/folio/internal/metasearch"
)

// searchFallback queries DuckDuckGo for a site:amazon.com match, then fetches
// the top allowed product page and reads its cover image. Returns an empty
// slice (not an error) when nothing usable is found.
func (s *Source) searchFallback(ctx context.Context, q metasearch.Query) ([]metasearch.CoverCandidate, error) {
	urls, err := s.ddgSearch(ctx, q)
	if err != nil {
		return nil, err
	}
	for _, u := range urls {
		if !s.allowProductHost(u) {
			continue
		}
		cand, ok, err := s.productCover(ctx, u)
		if err != nil {
			return nil, err
		}
		if ok {
			return []metasearch.CoverCandidate{cand}, nil
		}

		break // politeness: only the top allowed result is fetched
	}

	return nil, nil
}

// ddgSearch runs the DDG HTML query and returns candidate /dp/ URLs.
func (s *Source) ddgSearch(ctx context.Context, q metasearch.Query) ([]string, error) {
	if err := s.limiter.wait(ctx); err != nil {
		return nil, err
	}
	params := url.Values{}
	params.Set("q", "site:amazon.com "+q.SearchTerm())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.ddgURL+"/html/?"+params.Encode(), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("build ddg request: %w", err)
	}
	req.Header.Set("User-Agent", metasearch.RandomUserAgent())
	req.Header.Set("Accept", acceptHTML)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ddg request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ddg status %d: %w", resp.StatusCode, metasearch.ErrBlocked)
	}

	return parseDDGResults(io.LimitReader(resp.Body, maxHTMLBytes)), nil
}

// parseDDGResults walks DDG result anchors and returns target URLs whose path
// contains "/dp/". It unwraps DDG's /l/?uddg= redirect when present.
func parseDDGResults(r io.Reader) []string {
	doc, err := html.Parse(r)
	if err != nil {
		return nil
	}
	var out []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			if target := ddgTarget(attr(n, "href")); target != "" {
				out = append(out, target)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	return out
}

// ddgTarget resolves a DDG anchor href to a real target URL, unwrapping the
// /l/?uddg= redirect, and returns it only if its path contains "/dp/".
func ddgTarget(href string) string {
	if href == "" {
		return ""
	}
	target := href
	// DDG wraps external links as //duckduckgo.com/l/?uddg=<encoded>.
	if strings.Contains(href, "uddg=") {
		probe := href
		if strings.HasPrefix(probe, "//") {
			probe = "https:" + probe
		}
		if u, err := url.Parse(probe); err == nil {
			if dec := u.Query().Get("uddg"); dec != "" {
				target = dec
			}
		}
	}
	pu, err := url.Parse(target)
	if err != nil || !strings.Contains(pu.Path, "/dp/") {
		return ""
	}

	return target
}

// multiLabelTLDs are the public suffixes Amazon uses that span two labels, so
// the registrable domain sits one label deeper than a plain ".com".
var multiLabelTLDs = map[string]bool{ //nolint:gochecknoglobals // immutable lookup table
	"co.uk": true, "com.au": true, "co.jp": true, "com.br": true,
	"com.mx": true, "co.za": true, "com.tr": true, "com.be": true,
}

// isAmazonHost reports whether rawURL's registrable domain is "amazon" (e.g.
// amazon.com, www.amazon.com, amazon.co.uk), rejecting look-alikes such as
// amazon.evil.com. The URL comes from an external source (DDG), so this gate is
// defense in depth, not the only control.
func isAmazonHost(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	labels := strings.Split(strings.ToLower(u.Hostname()), ".")
	if len(labels) < 2 {
		return false
	}
	suffixLen := 1
	if len(labels) >= 3 && multiLabelTLDs[labels[len(labels)-2]+"."+labels[len(labels)-1]] {
		suffixLen = 2
	}
	domainIdx := len(labels) - suffixLen - 1
	if domainIdx < 0 {
		return false
	}

	return labels[domainIdx] == "amazon"
}

// productCover fetches an Amazon product page and extracts its cover image,
// preferring og:image and falling back to #landingImage. ok is false when no
// image is found.
func (s *Source) productCover(ctx context.Context, rawURL string) (metasearch.CoverCandidate, bool, error) {
	if err := s.limiter.wait(ctx); err != nil {
		return metasearch.CoverCandidate{}, false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return metasearch.CoverCandidate{}, false, fmt.Errorf("build product request: %w", err)
	}
	req.Header.Set("User-Agent", metasearch.RandomUserAgent())
	req.Header.Set("Accept", acceptHTML)

	resp, err := s.client.Do(req)
	if err != nil {
		return metasearch.CoverCandidate{}, false, fmt.Errorf("product request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return metasearch.CoverCandidate{}, false, nil
	}

	img := productImage(io.LimitReader(resp.Body, maxHTMLBytes))
	if img == "" {
		return metasearch.CoverCandidate{}, false, nil
	}

	return metasearch.CoverCandidate{
		Source:   metasearch.SourceAmazon,
		ThumbURL: img,
		FullURL:  metasearch.OriginalAmazonImage(img),
	}, true, nil
}

// productImage returns the product cover URL using three-level precedence:
//  1. og:image meta content (highest priority)
//  2. #landingImage data-old-hires attribute (higher-resolution variant)
//  3. #landingImage src attribute (fallback)
func productImage(r io.Reader) string {
	doc, err := html.Parse(r)
	if err != nil {
		return ""
	}
	og, hires, landing := collectImageAttrs(doc)
	if og != "" {
		return og
	}
	if hires != "" {
		return hires
	}

	return landing
}

// collectImageAttrs walks the HTML tree and extracts the og:image meta content,
// the #landingImage data-old-hires, and the #landingImage src.
func collectImageAttrs(doc *html.Node) (og, hires, landing string) {
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "meta":
				if og == "" && attr(n, "property") == "og:image" {
					og = attr(n, "content")
				}
			case "img":
				if attr(n, "id") == "landingImage" && landing == "" {
					hires = attr(n, "data-old-hires")
					landing = attr(n, "src")
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	return og, hires, landing
}

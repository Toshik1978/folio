// Package googlebooks is a minimal, dependency-free client for the Google Books
// volumes API. It decodes only the fields Folio consumes, keeping the binary
// free of the official google.golang.org/api client and its transitive graph.
package googlebooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const (
	defaultBaseURL    = "https://www.googleapis.com/books/v1"
	requestTimeout    = 8 * time.Second
	maxImageBytes     = 5 << 20         // 5 MiB cap on a downloaded cover
	rateLimitCooldown = 5 * time.Minute // back off this long after a 429
)

// ErrRateLimited is returned when Google answered 429 recently and the client is
// within its cooldown window. Requests short-circuit (no HTTP call) so a quota
// wall doesn't amplify into per-view hammering. Callers treat it as transient.
var ErrRateLimited = errors.New("google books rate limited")

// Client queries the Google Books volumes API. The zero value is not usable;
// build one with NewClient.
type Client struct {
	log     *slog.Logger
	apiKey  string
	http    *http.Client
	baseURL string

	mu            sync.Mutex
	cooldownUntil time.Time
}

// NewClient returns a client using the given API key. An empty key still works
// against Google's anonymous quota.
func NewClient(log *slog.Logger, apiKey string) *Client {
	return &Client{
		log:    log,
		apiKey: apiKey,
		http: &http.Client{
			Timeout:   requestTimeout,
			Transport: newLoggingTransport(log, http.DefaultTransport),
		},
		baseURL: defaultBaseURL,
	}
}

// Volume is the subset of a Google Books volume Folio maps onto its own metadata.
type Volume struct {
	ID         string `json:"id"`
	VolumeInfo struct {
		Title         string   `json:"title"`
		Authors       []string `json:"authors"`
		Description   string   `json:"description"`
		Publisher     string   `json:"publisher"`
		PublishedDate string   `json:"publishedDate"`
		Categories    []string `json:"categories"`
		ImageLinks    struct {
			Thumbnail string `json:"thumbnail"`
		} `json:"imageLinks"`
		IndustryIdentifiers []struct {
			Type       string `json:"type"`
			Identifier string `json:"identifier"`
		} `json:"industryIdentifiers"`
	} `json:"volumeInfo"`
}

type volumesResponse struct {
	Items []Volume `json:"items"`
}

// SearchISBN looks a volume up by ISBN (the highest-accuracy query). ok is false
// when nothing matched.
func (c *Client) SearchISBN(ctx context.Context, isbn string) (Volume, bool, error) {
	vols, err := c.query(ctx, "isbn:"+isbn)
	if err != nil || len(vols) == 0 {
		return Volume{}, false, err
	}

	return vols[0], true, nil
}

// Search runs a fuzzy title (+ optional author) query, returning the candidate
// volumes in Google's relevance order.
func (c *Client) Search(ctx context.Context, title, author string) ([]Volume, error) {
	q := "intitle:" + title
	if author != "" {
		q += " inauthor:" + author
	}

	return c.query(ctx, q)
}

// SearchQuery runs a raw free-text query (the Fix Match UI passes whatever the
// user typed, unwrapped by intitle:/inauthor: qualifiers).
func (c *Client) SearchQuery(ctx context.Context, q string) ([]Volume, error) {
	return c.query(ctx, q)
}

// GetVolume fetches a single volume by its Google Books id.
func (c *Client) GetVolume(ctx context.Context, id string) (Volume, error) {
	params := url.Values{}
	if c.apiKey != "" {
		params.Set("key", c.apiKey)
	}
	u := c.baseURL + "/volumes/" + url.PathEscape(id)
	if enc := params.Encode(); enc != "" {
		u += "?" + enc
	}

	var v Volume
	if err := c.getJSON(ctx, u, &v); err != nil {
		return Volume{}, err
	}

	return v, nil
}

// FetchImage downloads cover bytes from a Google Books image URL (capped at
// maxImageBytes).
func (c *Client) FetchImage(ctx context.Context, imageURL string) ([]byte, error) {
	if c.inCooldown() {
		return nil, ErrRateLimited
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("build image request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch image: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusTooManyRequests {
		c.tripCooldown()
		return nil, fmt.Errorf("fetch image: %w", ErrRateLimited)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch image status %d", resp.StatusCode)
	}

	// Read one byte past the cap so an oversized image is rejected rather than
	// silently truncated: a truncated JPEG passes convertToJPEG's header-only
	// check and would be cached and served as a corrupt cover.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read image: %w", err)
	}
	if len(data) > maxImageBytes {
		return nil, fmt.Errorf("image exceeds %d byte limit", maxImageBytes)
	}

	return data, nil
}

// inCooldown reports whether the client is still backing off from a recent 429.
func (c *Client) inCooldown() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return time.Now().Before(c.cooldownUntil)
}

// tripCooldown starts (or extends) the post-429 backoff window.
func (c *Client) tripCooldown() {
	c.mu.Lock()
	c.cooldownUntil = time.Now().Add(rateLimitCooldown)
	c.mu.Unlock()
}

// query runs a /volumes search for q and returns the decoded items.
func (c *Client) query(ctx context.Context, q string) ([]Volume, error) {
	params := url.Values{}
	params.Set("q", q)
	if c.apiKey != "" {
		params.Set("key", c.apiKey)
	}

	var out volumesResponse
	if err := c.getJSON(ctx, c.baseURL+"/volumes?"+params.Encode(), &out); err != nil {
		return nil, err
	}

	return out.Items, nil
}

// getJSON performs a GET and decodes a 200 JSON body into dst.
func (c *Client) getJSON(ctx context.Context, u string, dst any) error {
	if c.inCooldown() {
		return ErrRateLimited
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("google books request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusTooManyRequests {
		c.tripCooldown()
		return fmt.Errorf("google books: %w", ErrRateLimited)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("google books status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	return nil
}

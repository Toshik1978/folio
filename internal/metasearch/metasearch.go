// Package metasearch federates online book-metadata and cover providers behind
// a capability-based registry. A Source advertises what it can do (identify
// metadata, supply covers); the Registry fans a query out to the sources that
// can serve it. This package owns no provider logic — adapters live under
// providers/ and are wired together in cmd.
package metasearch //nolint:revive // max-public-structs: all types are intentional public API

import (
	"context"
	"slices"
	"strings"

	"github.com/Toshik1978/folio/internal/ebook"
)

// Capability is a thing a Source can do. A Source may advertise several.
type Capability int

const (
	// CapIdentify means the Source returns structured metadata for a query.
	CapIdentify Capability = iota
	// CapCover means the Source returns cover-image candidates for a query.
	CapCover
)

// Source name constants. Adapters return these from Name(); the aggregator keys
// its priority table on them. Keeping them here is the single source of truth.
const (
	SourceAmazon      = "amazon"
	SourceGoodreads   = "goodreads"
	SourceOpenLibrary = "openlibrary"
	SourceGoogleBooks = "googlebooks"
)

// Query is the normalized lookup a Source receives. Fields are best-effort: a
// Source uses whatever it can (ISBN is the highest-accuracy key).
type Query struct {
	Title  string
	Author string
	ISBN   string
	// ASIN is the Amazon product id, when known. It lets the Amazon source fetch
	// the product page directly for the exact edition's high-resolution cover,
	// instead of scraping a square search-result thumbnail.
	ASIN string
}

// SearchTerm is the normalized free-text query a provider sends: the title and
// author joined and trimmed.
func (q Query) SearchTerm() string {
	return strings.TrimSpace(q.Title + " " + q.Author)
}

// Source is the common base every provider satisfies.
type Source interface {
	Name() string
	Capabilities() []Capability
}

// Volume is a lightweight metadata search candidate. Source+ID is the routable
// handle used to fetch the full record (see MetadataSource.Get) when the user
// applies a pick. It is the provider-neutral replacement for googlebooks.Volume
// leaking through the API. (Consumed in Phase 3; defined here so the surface is
// built once.)
type Volume struct {
	Source       string   `json:"source"`
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Authors      []string `json:"authors"`
	Year         int      `json:"year"`
	ThumbnailURL string   `json:"thumbnail"`
}

// CoverCandidate is one cover-image option for the picker grid.
type CoverCandidate struct {
	Source   string `json:"source"`
	ThumbURL string `json:"thumb_url"`
	FullURL  string `json:"full_url"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

// MetadataSource returns structured metadata. Search is light (grid candidates);
// Get fetches the full record by the provider-local id; Resolve does an auto-enrich
// lookup-and-map in one provider-optimal operation (no Search+Get round-trip).
type MetadataSource interface {
	Source
	Search(ctx context.Context, q Query) ([]Volume, error)
	Get(ctx context.Context, id string) (ebook.Metadata, error)
	Resolve(ctx context.Context, q Query) (ebook.Metadata, bool, error)
}

// CoverSource returns cover-image candidates for a query.
type CoverSource interface {
	Source
	SearchCovers(ctx context.Context, q Query) ([]CoverCandidate, error)
}

// BookLookup resolves a book id to the query used to enrich it (title, first
// author, ISBN). It is the seam that lets the Coordinator build an auto-enrich
// query without importing the database layer.
type BookLookup interface {
	Lookup(ctx context.Context, bookID int64) (Query, bool, error)
}

// HasCapability reports whether caps contains c.
func HasCapability(caps []Capability, c Capability) bool {
	return slices.Contains(caps, c)
}

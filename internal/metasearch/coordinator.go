package metasearch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Toshik1978/folio/internal/ebook"
)

// Coordinator reimplements the metadata-enrichment facade over the registry's
// metadata sources, so the API depends on this neutral object instead of a
// single hard-coded provider. With one registered source it behaves exactly as
// the legacy Google Books enricher.
type Coordinator struct {
	log      *slog.Logger
	registry *Registry
	lookup   BookLookup
}

// NewCoordinator builds a coordinator over the registry's metadata sources.
func NewCoordinator(log *slog.Logger, reg *Registry, lookup BookLookup) *Coordinator {
	return &Coordinator{log: log, registry: reg, lookup: lookup}
}

// Search runs a free-text query across every metadata source and merges the
// candidates. If every source errors and none returns results, the first error
// is surfaced (so the Fix-Match UI still reports an upstream failure).
func (c *Coordinator) Search(ctx context.Context, query string) ([]Volume, error) {
	var out []Volume
	var firstErr error
	for _, ms := range c.registry.MetadataSources() {
		vols, err := ms.Search(ctx, Query{Title: query})
		if err != nil {
			c.log.Warn("metadata search failed", slog.String("source", ms.Name()), slog.Any("error", err))
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		out = append(out, vols...)
	}
	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}

	return out, nil
}

// ApplyMatch fetches the full metadata for a chosen candidate from the named
// source. The source is required: a candidate id is only meaningful to the source
// that produced it, so guessing across sources could apply a foreign record.
func (c *Coordinator) ApplyMatch(ctx context.Context, source, id string) (ebook.Metadata, error) {
	if source == "" {
		return ebook.Metadata{}, errors.New("apply match: source is required")
	}
	ms, ok := c.registry.MetadataSourceByName(source)
	if !ok {
		return ebook.Metadata{}, fmt.Errorf("unknown metadata source %q", source)
	}

	meta, err := ms.Get(ctx, id)
	if err != nil {
		return ebook.Metadata{}, fmt.Errorf("get metadata from %q: %w", source, err)
	}

	return meta, nil
}

// Enrich auto-enriches a book: it builds the lookup query (ISBN-first, else
// title + first author), searches each source in registry order, and returns the
// full record for the first source that matches. ok is false when nothing
// matched. Behavior parity with the legacy single-provider enricher.
func (c *Coordinator) Enrich(ctx context.Context, bookID int64) (ebook.Metadata, bool, error) {
	q, ok, err := c.lookup.Lookup(ctx, bookID)
	if err != nil {
		return ebook.Metadata{}, false, fmt.Errorf("lookup book %d: %w", bookID, err)
	}
	if !ok {
		return ebook.Metadata{}, false, nil
	}

	var lastErr error
	for _, ms := range c.registry.MetadataSources() {
		meta, found, rerr := ms.Resolve(ctx, q)
		if rerr != nil {
			lastErr = rerr
			continue
		}
		if !found {
			continue
		}

		return meta, true, nil
	}

	return ebook.Metadata{}, false, lastErr
}

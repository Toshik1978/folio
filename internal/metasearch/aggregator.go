package metasearch

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"
)

// defaultCoverTimeout bounds each provider's contribution to a cover search, so
// one slow source can't stall the grid.
const defaultCoverTimeout = 8 * time.Second

// coverPriority ranks providers highest-quality first. Unknown sources sort
// last. ISBN-keyed REST sources (Open Library, Google Books) lead: they return
// the exact edition's portrait cover. Amazon ranks last — it is scraped, its
// search thumbnails are often square audiobook/crop art rather than the print
// cover, and results vary by region and anti-bot state.
var coverPriority = map[string]int{ //nolint:gochecknoglobals // package-level lookup table, not mutable state
	SourceOpenLibrary: 4,
	SourceGoogleBooks: 3,
	SourceGoodreads:   2,
	SourceAmazon:      1,
}

// Aggregator fans a cover query out to every CoverSource in its registry,
// concurrently and best-effort: a provider error or timeout is logged and its
// results dropped, never failing the whole search.
type Aggregator struct {
	log      *slog.Logger
	registry *Registry
	timeout  time.Duration
}

// NewAggregator builds an aggregator over the registry's cover sources.
func NewAggregator(log *slog.Logger, reg *Registry) *Aggregator {
	return &Aggregator{log: log, registry: reg, timeout: defaultCoverTimeout}
}

// SearchCovers queries all cover sources concurrently and returns the merged,
// deduped, ranked candidates. It never returns an error: partial results are the
// contract (the caller always has manual upload/URL as the floor).
func (a *Aggregator) SearchCovers(ctx context.Context, q Query) []CoverCandidate {
	sources := a.registry.CoverSources()
	results := make([][]CoverCandidate, len(sources))

	var wg sync.WaitGroup
	for i, src := range sources {
		wg.Go(func() {
			cctx, cancel := context.WithTimeout(ctx, a.timeout)
			defer cancel()

			start := time.Now()
			out, err := src.SearchCovers(cctx, q)
			a.logOutcome(src.Name(), out, err, time.Since(start))
			if err != nil {
				return
			}
			results[i] = out
		})
	}
	wg.Wait()

	return rankCovers(filterRelevant(q.Title, flatten(results)))
}

// logOutcome emits one structured log line per source describing how its cover
// query resolved: ok, empty, blocked (anti-bot), or error.
func (a *Aggregator) logOutcome(name string, out []CoverCandidate, err error, dur time.Duration) {
	status := "ok"
	warn := false
	switch {
	case errors.Is(err, ErrBlocked):
		status, warn = "blocked", true
	case err != nil:
		status, warn = "error", true
	case len(out) == 0:
		status = "empty"
	}

	attrs := []any{
		slog.String("source", name),
		slog.String("status", status),
		slog.Int("count", len(out)),
		slog.Int64("duration_ms", dur.Milliseconds()),
	}
	if err != nil {
		attrs = append(attrs, slog.Any("error", err))
	}

	if warn {
		a.log.Warn("cover source outcome", attrs...)

		return
	}
	a.log.Info("cover source outcome", attrs...)
}

// flatten concatenates per-source results in source order.
func flatten(groups [][]CoverCandidate) []CoverCandidate {
	var out []CoverCandidate
	for _, g := range groups {
		out = append(out, g...)
	}

	return out
}

// filterRelevant drops candidates whose own title is not an acceptable match for
// the query title (box sets, wrong editions, foreign-script). A candidate with an
// empty title fails open — exact-key sources (ASIN/ISBN) set no title because the
// id already pins the edition. This is the one place cover relevance is applied,
// so every provider is filtered consistently.
func filterRelevant(queryTitle string, in []CoverCandidate) []CoverCandidate {
	out := make([]CoverCandidate, 0, len(in))
	for _, c := range in {
		if TitleAcceptable(queryTitle, c.Title) {
			out = append(out, c)
		}
	}

	return out
}

// rankCovers dedupes by FullURL (keeping the higher-priority/higher-res copy)
// then sorts by (provider priority desc, resolution desc).
func rankCovers(in []CoverCandidate) []CoverCandidate {
	best := make(map[string]CoverCandidate, len(in))
	for _, c := range in {
		if existing, ok := best[c.FullURL]; ok && !better(c, existing) {
			continue
		}
		best[c.FullURL] = c
	}
	out := make([]CoverCandidate, 0, len(best))
	for _, c := range best {
		out = append(out, c)
	}
	slices.SortStableFunc(out, func(a, b CoverCandidate) int {
		if better(a, b) {
			return -1
		}
		if better(b, a) {
			return 1
		}

		return 0
	})

	return out
}

// better reports whether a should rank ahead of b: higher provider priority
// first, then larger pixel area.
func better(a, b CoverCandidate) bool {
	pa, pb := coverPriority[a.Source], coverPriority[b.Source]
	if pa != pb {
		return pa > pb
	}

	return a.Width*a.Height > b.Width*b.Height
}

package metasearch

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"time"
)

// fakeCover is a CoverSource returning canned candidates after an optional delay
// or an error, to exercise concurrency and partial-failure handling.
type fakeCover struct {
	name  string
	out   []CoverCandidate
	err   error
	delay time.Duration
}

func (f fakeCover) Name() string               { return f.name }
func (f fakeCover) Capabilities() []Capability { return []Capability{CapCover} }
func (f fakeCover) SearchCovers(ctx context.Context, _ Query) ([]CoverCandidate, error) {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err() //nolint:wrapcheck // test helper: propagate context error as-is
		}
	}

	return f.out, f.err
}

func (s *coreSuite) TestAggregatorMergesRanksAndDedupes() {
	reg := NewRegistry(
		fakeCover{name: SourceGoogleBooks, out: []CoverCandidate{
			{Source: SourceGoogleBooks, FullURL: "https://gb/x.jpg", Width: 100, Height: 150},
			{Source: SourceGoogleBooks, FullURL: "https://dup/c.jpg", Width: 10, Height: 10}, // dup URL, lower res
		}},
		fakeCover{name: SourceAmazon, out: []CoverCandidate{
			{Source: SourceAmazon, FullURL: "https://amz/big.jpg", Width: 500, Height: 750},
			{Source: SourceAmazon, FullURL: "https://dup/c.jpg", Width: 400, Height: 600}, // dup URL, higher res — kept
		}},
		fakeCover{name: SourceGoodreads, err: errors.New("boom")}, // dropped, not fatal
		fakeCover{name: SourceOpenLibrary, delay: time.Hour, out: []CoverCandidate{ // times out, dropped
			{Source: SourceOpenLibrary, FullURL: "https://ol/slow.jpg"},
		}},
	)
	agg := NewAggregator(slog.New(slog.DiscardHandler), reg)
	agg.timeout = 50 * time.Millisecond

	got := agg.SearchCovers(context.Background(), Query{Title: "Dune"})

	// dup/c.jpg deduped to one entry; ol timed out; goodreads errored.
	s.Len(got, 3)
	// Amazon outranks Google Books; the deduped dup kept the Amazon (higher-res) copy.
	s.Equal(SourceAmazon, got[0].Source)
	for _, c := range got {
		if c.FullURL == "https://dup/c.jpg" {
			s.Equal(SourceAmazon, c.Source, "dedupe keeps the higher-priority/res copy")
		}
	}
}

func (s *coreSuite) TestAggregatorNoSources() {
	agg := NewAggregator(slog.New(slog.DiscardHandler), NewRegistry())
	s.Empty(agg.SearchCovers(context.Background(), Query{}))
}

func (s *coreSuite) TestAggregatorLogsPerSourceOutcome() {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	reg := NewRegistry(
		fakeCover{name: SourceGoogleBooks, out: []CoverCandidate{
			{Source: SourceGoogleBooks, FullURL: "https://gb/x.jpg"},
		}},
		fakeCover{name: SourceAmazon, err: ErrBlocked},
		fakeCover{name: SourceGoodreads}, // nil out, nil err -> empty
	)
	agg := NewAggregator(log, reg)

	agg.SearchCovers(context.Background(), Query{Title: "Dune"})

	logs := buf.String()
	s.Contains(logs, `"status":"ok"`)
	s.Contains(logs, `"status":"blocked"`)
	s.Contains(logs, `"status":"empty"`)
	s.Contains(logs, `"source":"amazon"`)
	s.Contains(logs, `"duration_ms":`)
}

// TestAggregatorLogsErrorStatus verifies that a plain (non-ErrBlocked) source
// error is logged with status "error" and the correct source name, and that
// sources surfacing other status values also log their names.
func (s *coreSuite) TestAggregatorLogsErrorStatus() {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	reg := NewRegistry(
		fakeCover{name: SourceGoodreads, out: []CoverCandidate{
			{Source: SourceGoodreads, FullURL: "https://gr/x.jpg"},
		}},
		fakeCover{name: SourceOpenLibrary, err: errors.New("boom")}, // plain error → "error" status
		fakeCover{name: SourceGoogleBooks, err: ErrBlocked},
	)
	agg := NewAggregator(log, reg)

	agg.SearchCovers(context.Background(), Query{Title: "Dune"})

	logs := buf.String()
	s.Contains(logs, `"status":"error"`)
	s.Contains(logs, `"source":"openlibrary"`)
	s.Contains(logs, `"source":"goodreads"`)
	s.Contains(logs, `"source":"googlebooks"`)
	s.Contains(logs, `"duration_ms":`)
}

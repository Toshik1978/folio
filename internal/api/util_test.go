package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
)

// TestPaginationClampsHugePage guards against a hostile ?page= overflowing
// (pageNo-1)*limit into a negative SQL OFFSET. SQLite treats a negative OFFSET
// as 0, but the arithmetic must never wrap in the first place.
func TestPaginationClampsHugePage(t *testing.T) {
	t.Parallel()

	cases := []string{
		strconv.FormatInt(1<<62, 10), // huge but valid int64
		"9223372036854775807",        // math.MaxInt64
	}
	// An offset past this is implausible for any catalog and signals the
	// multiply overflowed (or was left unclamped).
	const sane = int64(1) << 40
	for _, page := range cases {
		req := httptest.NewRequestWithContext(
			context.Background(), http.MethodGet, "/?"+url.Values{"page": {page}}.Encode(), http.NoBody)
		pageNo, limit, offset := pagination(req)
		if offset < 0 {
			t.Fatalf("page=%s: offset %d must not be negative", page, offset)
		}
		if offset > sane {
			t.Fatalf("page=%s: offset %d must be clamped to a sane bound", page, offset)
		}
		if pageNo < 1 || limit < 1 {
			t.Fatalf("page=%s: pageNo=%d limit=%d must stay positive", page, pageNo, limit)
		}
	}
}

func TestPaginationDefaults(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	pageNo, limit, offset := pagination(req)
	if pageNo != 1 || limit != defaultLimit || offset != 0 {
		t.Fatalf("defaults: got page=%d limit=%d offset=%d", pageNo, limit, offset)
	}
}

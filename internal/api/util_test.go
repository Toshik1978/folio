package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"

	"github.com/stretchr/testify/suite"
)

type utilSuite struct {
	suite.Suite
}

// TestPaginationClampsHugePage guards against a hostile ?page= overflowing
// (pageNo-1)*limit into a negative SQL OFFSET. SQLite treats a negative OFFSET
// as 0, but the arithmetic must never wrap in the first place.
func (s *utilSuite) TestPaginationClampsHugePage() {
	cases := []string{
		strconv.FormatInt(1<<62, 10), // huge but valid int64
		"9223372036854775807",        // math.MaxInt64
	}
	// An offset past this is implausible for any catalog and signals the
	// multiply overflowed (or was left unclamped).
	const sane = int64(1) << 40
	for _, page := range cases {
		req := httptest.NewRequestWithContext(
			context.Background(), http.MethodGet, "/?"+url.Values{"page": {page}}.Encode(), http.NoBody,
		)
		pageNo, limit, offset := pagination(req)

		s.GreaterOrEqual(offset, int64(0))
		s.LessOrEqual(offset, sane)
		s.Positive(pageNo)
		s.Positive(limit)
	}
}

func (s *utilSuite) TestPaginationDefaults() {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	pageNo, limit, offset := pagination(req)

	s.Equal(int64(1), pageNo)
	s.Equal(int64(defaultLimit), limit)
	s.Equal(int64(0), offset)
}

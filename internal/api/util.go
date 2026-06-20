package api

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// page is the envelope for paginated list responses.
type page[T any] struct {
	Items []T   `json:"items"`
	Total int64 `json:"total"`
	Page  int64 `json:"page"`
	Limit int64 `json:"limit"`
}

const (
	defaultLimit = 24
	maxLimit     = 100
)

// pagination reads page/limit query params, applying defaults and bounds, and
// returns the 1-indexed page, the per-page limit, and the SQL offset.
func pagination(r *http.Request) (pageNo, limit, offset int64) {
	pageNo = parseIntDefault(r.URL.Query().Get("page"), 1)
	pageNo = max(pageNo, 1)
	limit = parseIntDefault(r.URL.Query().Get("limit"), defaultLimit)
	if limit < 1 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}

	return pageNo, limit, (pageNo - 1) * limit
}

func parseIntDefault(s string, def int64) int64 {
	if s == "" {
		return def
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return def
	}

	return n
}

// intParam parses the named URL parameter as a positive integer.
func intParam(r *http.Request, name string) (int64, bool) {
	v, err := strconv.ParseInt(chi.URLParam(r, name), 10, 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	return v, true
}

// intQueryParam reads the optional ?<name>=<value> filter shared by the browse
// endpoints. Absent or invalid yields 0.
func intQueryParam(r *http.Request, name string) int64 { //nolint:unparam // It's for library only, but who knows
	v, err := strconv.ParseInt(r.URL.Query().Get(name), 10, 64)
	if err != nil || v < 0 {
		return 0
	}
	return v
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}

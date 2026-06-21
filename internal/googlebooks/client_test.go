package googlebooks

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/suite"
)

func TestGoogleBooks(t *testing.T) {
	suite.Run(t, new(clientSuite))
}

type clientSuite struct {
	suite.Suite
}

// server spins up an httptest server running handler and returns a client
// pointed at it.
func (s *clientSuite) server(handler http.HandlerFunc) (*Client, func()) {
	srv := httptest.NewServer(handler)
	c := NewClient(slog.New(slog.DiscardHandler), "test-key")
	c.baseURL = srv.URL

	return c, srv.Close
}

func (s *clientSuite) TestSearchISBN() {
	c, closeFn := s.server(func(w http.ResponseWriter, r *http.Request) {
		s.Equal("/volumes", r.URL.Path)
		s.Equal("isbn:9780441013593", r.URL.Query().Get("q"))
		s.Equal("test-key", r.URL.Query().Get("key"))
		_, _ = w.Write([]byte(`{"items":[{"id":"abc","volumeInfo":{"title":"Dune","description":"Desert."}}]}`))
	})
	defer closeFn()

	v, ok, err := c.SearchISBN(s.T().Context(), "9780441013593")
	s.Require().NoError(err)
	s.Require().True(ok)
	s.Equal("abc", v.ID)
	s.Equal("Dune", v.VolumeInfo.Title)
	s.Equal("Desert.", v.VolumeInfo.Description)
}

func (s *clientSuite) TestSearchISBNNoMatch() {
	c, closeFn := s.server(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`)) // no items
	})
	defer closeFn()

	_, ok, err := c.SearchISBN(s.T().Context(), "0000000000")
	s.Require().NoError(err)
	s.False(ok)
}

func (s *clientSuite) TestNon200IsError() {
	c, closeFn := s.server(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer closeFn()

	_, _, err := c.SearchISBN(s.T().Context(), "9780441013593")
	s.Require().Error(err)
	s.Contains(err.Error(), "500")
}

// TestRateLimitCooldownSuppressesCalls verifies that after a 429 the client backs
// off: a second request within the cooldown window short-circuits to ErrRateLimited
// without issuing another HTTP call, so a quota wall doesn't get re-hit per view.
func (s *clientSuite) TestRateLimitCooldownSuppressesCalls() {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	c := NewClient(slog.New(slog.DiscardHandler), "")
	c.baseURL = srv.URL

	_, err := c.SearchQuery(s.T().Context(), "x")
	s.Require().ErrorIs(err, ErrRateLimited, "first 429 surfaces ErrRateLimited and trips the cooldown")

	_, err = c.SearchQuery(s.T().Context(), "y")
	s.Require().ErrorIs(err, ErrRateLimited, "second call stays rate-limited during cooldown")
	s.Equal(int32(1), hits.Load(), "second call short-circuited by the cooldown")
}

// A hostile or runaway upstream body must not be buffered unbounded into memory:
// the decode is capped, so an over-cap body is rejected (truncated decode) rather
// than fully read.
func (s *clientSuite) TestGetJSONRejectsOversizedBody() {
	c, closeFn := s.server(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"kind":"`)
		_, _ = w.Write(bytes.Repeat([]byte("a"), maxJSONBytes+1))
		_, _ = io.WriteString(w, `"}`)
	})
	defer closeFn()

	_, _, err := c.SearchISBN(s.T().Context(), "9780441013593")
	s.Require().Error(err, "a body beyond the cap must be rejected, not buffered unbounded")
}

func (s *clientSuite) TestSearchBuildsTitleAuthorQuery() {
	c, closeFn := s.server(func(w http.ResponseWriter, r *http.Request) {
		s.Equal("intitle:Dune inauthor:Herbert", r.URL.Query().Get("q"))
		_, _ = w.Write([]byte(`{"items":[{"id":"a"},{"id":"b"}]}`))
	})
	defer closeFn()

	vols, err := c.Search(s.T().Context(), "Dune", "Herbert")
	s.Require().NoError(err)
	s.Len(vols, 2)
}

func (s *clientSuite) TestGetVolume() {
	c, closeFn := s.server(func(w http.ResponseWriter, r *http.Request) {
		s.Equal("/volumes/xyz", r.URL.Path)
		_, _ = w.Write([]byte(`{"id":"xyz","volumeInfo":{"title":"Picked"}}`))
	})
	defer closeFn()

	v, err := c.GetVolume(s.T().Context(), "xyz")
	s.Require().NoError(err)
	s.Equal("Picked", v.VolumeInfo.Title)
}

func (s *clientSuite) TestFetchImage() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("JPEGBYTES"))
	}))
	defer srv.Close()

	data, err := NewClient(slog.New(slog.DiscardHandler), "").FetchImage(s.T().Context(), srv.URL+"/cover.jpg")
	s.Require().NoError(err)
	s.Equal("JPEGBYTES", string(data))
}

// M9: an image larger than the cap must be rejected, not truncated and cached
// as a corrupt cover.
func (s *clientSuite) TestFetchImageRejectsOversized() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(make([]byte, maxImageBytes+1))
	}))
	defer srv.Close()

	_, err := NewClient(slog.New(slog.DiscardHandler), "").FetchImage(s.T().Context(), srv.URL+"/cover.jpg")
	s.Require().Error(err, "a truncated image must be rejected, not cached corrupt")
}

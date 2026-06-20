package googlebooks

import (
	"errors"
	"log/slog"
	"net/http"
	"testing"

	"github.com/stretchr/testify/suite"
)

func TestLoggingTransport(t *testing.T) {
	suite.Run(t, new(transportSuite))
}

type transportSuite struct {
	suite.Suite
}

// roundTripFunc adapts a plain function to http.RoundTripper so tests can stub
// the underlying transport.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// TestRoundTripWrapsUnderlyingError verifies a transport-level failure surfaces
// as a wrapped error with a nil response, so callers can't accidentally read a
// response that doesn't exist. The success path is covered by clientSuite, which
// routes every request through this transport against an httptest server.
func (s *transportSuite) TestRoundTripWrapsUnderlyingError() {
	underlying := errors.New("dial tcp: connection refused")
	tr := newLoggingTransport(slog.New(slog.DiscardHandler), roundTripFunc(
		func(*http.Request) (*http.Response, error) {
			return nil, underlying
		},
	))

	req, err := http.NewRequestWithContext(s.T().Context(), http.MethodGet, "https://example.test/v1", http.NoBody)
	s.Require().NoError(err)

	resp, err := tr.RoundTrip(req) //nolint:bodyclose // This test is always nil body
	s.Require().Error(err)
	s.Nil(resp, "no response is returned on a transport failure")
	s.Require().ErrorIs(err, underlying, "the underlying error is wrapped, not swallowed")
	s.Contains(err.Error(), "google books")
}

// TestNewLoggingTransportDefaultsUnderlying verifies a nil underlying falls back
// to http.DefaultTransport rather than leaving a nil round-tripper to panic on.
func (s *transportSuite) TestNewLoggingTransportDefaultsUnderlying() {
	tr := newLoggingTransport(slog.New(slog.DiscardHandler), nil)
	lt, ok := tr.(*loggingTransport)
	s.Require().True(ok)
	s.Equal(http.DefaultTransport, lt.underlying)
}

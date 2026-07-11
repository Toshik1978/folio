package googlebooks

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/stretchr/testify/suite"
)

type transportSuite struct {
	suite.Suite
}

// roundTripFunc adapts a plain function to http.RoundTripper so tests can stub
// the underlying transport.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// levelCapture is a minimal slog.Handler that records the level of every emitted
// record, so a test can assert which severity a round-trip outcome logged at.
type levelCapture struct {
	levels []slog.Level
}

func (h *levelCapture) Enabled(context.Context, slog.Level) bool { return true }

func (h *levelCapture) Handle(_ context.Context, r slog.Record) error {
	h.levels = append(h.levels, r.Level)
	return nil
}
func (h *levelCapture) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *levelCapture) WithGroup(string) slog.Handler      { return h }

// roundTripLoggingAt drives one failing round trip whose underlying transport
// returns underErr, and returns the levels the transport logged at.
func (s *transportSuite) roundTripLoggingAt(underErr error) []slog.Level {
	h := &levelCapture{}
	tr := newLoggingTransport(slog.New(h), roundTripFunc(
		func(*http.Request) (*http.Response, error) {
			return nil, underErr
		},
	))

	req, err := http.NewRequestWithContext(s.T().Context(), http.MethodGet, "https://example.test/v1", http.NoBody)
	s.Require().NoError(err)

	_, err = tr.RoundTrip(req) //nolint:bodyclose // always a nil body on error
	s.Require().Error(err)

	return h.levels
}

// TestCancelledRoundTripLogsAtDebug verifies an expected cancellation/timeout —
// routine when the aggregator cancels the losing per-source requests — is logged
// at Debug, not Error, so it does not flood the logs.
func (s *transportSuite) TestCancelledRoundTripLogsAtDebug() {
	for _, underErr := range []error{context.Canceled, context.DeadlineExceeded} {
		levels := s.roundTripLoggingAt(underErr)
		s.Require().Len(levels, 1)
		s.Equalf(slog.LevelDebug, levels[0], "%v must log at Debug", underErr)
	}
}

// TestFailedRoundTripLogsAtError verifies a genuine transport failure still logs
// at Error.
func (s *transportSuite) TestFailedRoundTripLogsAtError() {
	levels := s.roundTripLoggingAt(errors.New("dial tcp: connection refused"))
	s.Require().Len(levels, 1)
	s.Equal(slog.LevelError, levels[0])
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

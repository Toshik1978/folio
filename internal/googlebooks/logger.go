package googlebooks

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

const (
	bold         = "\x1b[1m"
	reset        = "\x1b[0m"
	colorGreen   = "\x1b[32m"
	colorMagenta = "\x1b[35m"
	colorCyan    = "\x1b[36m"
)

// Both request log lines open with "google books" so they read distinctly from
// Folio's inbound request logs: these are the client's own upstream round trips.
const (
	// logHead renders "google books METHOD URL" — method in bold magenta, URL in
	// cyan. Shared by the success and failure lines.
	logHead = "google books " +
		bold + colorMagenta + "%s" + reset + " " +
		colorCyan + "%s" + reset

	// logTail closes a line with the round-trip duration in green.
	logTail = " in " + colorGreen + "%v" + reset

	// successFormat slots the HTTP status (bold green) between head and tail.
	successFormat = logHead + " - " + bold + colorGreen + "%d" + reset + logTail

	// failureFormat mirrors successFormat without the status (there is no
	// response); the error itself rides along as a slog "error" attribute.
	failureFormat = logHead + logTail
)

// loggingTransport is a custom http.RoundTripper for proper logging.
type loggingTransport struct {
	log        *slog.Logger
	underlying http.RoundTripper
}

func newLoggingTransport(log *slog.Logger, underlying http.RoundTripper) http.RoundTripper {
	if underlying == nil {
		underlying = http.DefaultTransport
	}
	return &loggingTransport{log: log, underlying: underlying}
}

func (t *loggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := t.underlying.RoundTrip(req)
	duration := time.Since(start)

	if err != nil {
		t.log.Error(
			fmt.Sprintf(failureFormat, req.Method, req.URL.String(), duration),
			"error", err,
		)
		return nil, fmt.Errorf("google books failed: %w", err)
	}

	t.log.Debug(fmt.Sprintf(
		successFormat,
		req.Method, req.URL.String(), resp.StatusCode, duration,
	))

	return resp, nil
}

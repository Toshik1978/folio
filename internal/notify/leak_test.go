package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/Toshik1978/folio/internal/db"
)

// Delivery modes of the leak sweep's fake endpoint.
const (
	modeOK int32 = iota
	modeTransportError
	modeEchoPath
)

// syncBuffer is a bytes.Buffer safe for the sender goroutine and the test to
// share.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p) //nolint:wrapcheck // in-memory buffer
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

// modeTransport fails every request with an error that quotes the full request
// URL (token included) while the mode is modeTransportError.
type modeTransport struct {
	mode *atomic.Int32
	next http.RoundTripper
	sent func(text string)
}

func (t modeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("read request body: %w", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var body struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(raw, &body)
	t.sent(body.Text)

	if t.mode.Load() == modeTransportError {
		return nil, fmt.Errorf("dial %s: connection refused", r.URL)
	}

	return t.next.RoundTrip(r) //nolint:wrapcheck // pass-through test transport
}

// leakSuite sweeps every message kind and every failure path for the bot
// token (SC-006).
type leakSuite struct {
	suite.Suite

	mode  atomic.Int32
	clock time.Time
	mu    sync.Mutex
	logs  *syncBuffer
	fake  *fakeTelegram
	svc   *Service

	sent []string // every text handed to the transport, delivered or not
	seen []string // every text that may be shown to a person, to be swept
}

func (s *leakSuite) now() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.clock
}

func (s *leakSuite) advance(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clock = s.clock.Add(d)
}

func (s *leakSuite) SetupTest() {
	s.clock = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	s.mode.Store(modeOK)
	s.logs = &syncBuffer{}
	s.seen = nil
	s.sent = nil

	s.fake = newFakeTelegram(func(r *http.Request, _ int) (int, string) {
		if s.mode.Load() == modeEchoPath {
			return 400, fmt.Sprintf(`{"ok":false,"error_code":400,"description":%q}`,
				"Bad Request: cannot POST "+r.URL.Path)
		}

		return 200, okBody
	})
	s.T().Cleanup(s.fake.srv.Close)

	database, err := db.Open(slog.New(slog.DiscardHandler), s.T().TempDir())
	s.Require().NoError(err)
	s.T().Cleanup(func() { _ = database.Close() })
	store := NewStore(database, db.NewWriteGuard())
	s.Require().NoError(store.Save(context.Background(),
		Config{Enabled: true, BotToken: testToken, ChatID: testChat}))

	httpClient := &http.Client{
		Transport: modeTransport{mode: &s.mode, next: http.DefaultTransport, sent: func(text string) {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.sent = append(s.sent, text)
		}},
	}
	timing := fastTiming()
	timing.QueueSize, timing.UrgentSize = 16, 8
	timing.Spacing = time.Millisecond
	log := slog.New(slog.NewTextHandler(s.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	s.svc, err = New(log, store, NewClient(s.fake.srv.URL, httpClient),
		WithPublicURL("https://folio.example.com/"), WithTiming(timing), WithClock(s.now))
	s.Require().NoError(err)
	s.T().Cleanup(s.svc.Stop)
}

// observe records everything the settings surface and the delivery status
// show right now.
func (s *leakSuite) observe() {
	got, err := s.svc.TelegramSettings(context.Background())
	s.Require().NoError(err)
	raw, err := json.Marshal(got) //nolint:musttag // the sweep covers the untagged view too
	s.Require().NoError(err)
	st := s.svc.DeliveryStatus()
	s.seen = append(s.seen, fmt.Sprintf("%+v", got), string(raw), st.LastFailureError)
}

// waitDeliveryFailures waits until the log shows n failed deliveries.
func (s *leakSuite) waitDeliveryFailures(n int) {
	s.Require().Eventually(func() bool {
		return strings.Count(s.logs.String(), "telegram delivery failed") >= n
	}, waitFor, pollTick)
	s.observe()
}

func (s *leakSuite) TestTokenNeverLeaks() {
	ctx := context.Background()
	s.observe()

	// SendTest over a transport error that quotes the URL.
	s.mode.Store(modeTransportError)
	err := s.svc.SendTest(ctx)
	s.Require().Error(err)
	s.seen = append(s.seen, err.Error())
	s.observe()
	s.NotEmpty(s.svc.DeliveryStatus().LastFailureError)

	// SendTest over a 400 that echoes the request path.
	s.mode.Store(modeEchoPath)
	err = s.svc.SendTest(ctx)
	var api *APIError
	s.Require().ErrorAs(err, &api)
	s.seen = append(s.seen, err.Error())
	s.observe()
	s.Contains(s.svc.DeliveryStatus().LastFailureError, "cannot POST")

	s.svc.Start()

	// Queued crash (and a repeat to count) over a transport error.
	s.mode.Store(modeTransportError)
	s.svc.ReportCrash("web request handling", "GET /api/books/{id}", "boom", booksStack())
	s.svc.ReportCrash("web request handling", "GET /api/books/{id}", "boom", booksStack())
	s.waitDeliveryFailures(1)

	// Queued run failure over the echoing 400.
	s.mode.Store(modeEchoPath)
	s.svc.RunFinished(failedRun(1, RunSync, "source unavailable"))
	s.waitDeliveryFailures(2)

	// Every remaining kind, delivered.
	s.mode.Store(modeOK)
	s.Require().NoError(s.svc.SendTest(ctx))
	o := unreadableRun(3, sampleBooks(3)...)
	o.BooksBefore, o.BooksRemoved = 100, 20
	o.PerBookErrors = 2
	s.svc.RunFinished(o) // recovered, unreadable, mass removal, error window
	s.svc.LibraryRemoved(1)
	s.svc.LogError(ctx, slog.NewRecord(s.now(), slog.LevelError, "scan failed", 0))
	s.advance(2 * time.Hour)
	s.svc.tick(s.now()) // crash repeat, error summary
	s.svc.Stop()
	s.observe()

	s.mu.Lock()
	sent := append([]string(nil), s.sent...)
	s.mu.Unlock()
	s.seen = append(s.seen, sent...)
	all := strings.Join(sent, "\n---\n")
	for _, want := range []string{
		"Crash recovered", "repeated 1 more", "Test message: Telegram",
		"sync failed\nReason:", "recovered\nThe last",
		"newly unreadable", "removed 20 of 100", "error(s) in the last 5 minutes", "https://folio.example.com",
	} {
		s.Contains(all, want)
	}

	for _, v := range s.seen {
		s.NotContains(v, testToken)
	}
	logs := s.logs.String()
	s.Contains(logs, "telegram delivery failed")
	for line := range strings.SplitSeq(logs, "\n") {
		s.NotContains(line, testToken)
	}
}

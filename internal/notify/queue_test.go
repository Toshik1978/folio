package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/Toshik1978/folio/internal/db"
)

const (
	waitFor  = 2 * time.Second
	pollTick = 2 * time.Millisecond
)

// fakeRequest is one sendMessage call seen by the fake Telegram endpoint.
type fakeRequest struct {
	at   time.Time
	text string
}

// fakeTelegram is an httptest Bot API that records every request and answers
// through reply, which gets the zero-based request number.
type fakeTelegram struct {
	mu    sync.Mutex
	reqs  []fakeRequest
	reply func(r *http.Request, n int) (int, string)
	srv   *httptest.Server
}

func newFakeTelegram(reply func(r *http.Request, n int) (int, string)) *fakeTelegram {
	f := &fakeTelegram{reply: reply}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		n := len(f.reqs)
		f.reqs = append(f.reqs, fakeRequest{at: time.Now(), text: body.Text})
		f.mu.Unlock()
		status, resp := f.reply(r, n)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, resp)
	}))

	return f
}

func (f *fakeTelegram) requests() []fakeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]fakeRequest(nil), f.reqs...)
}

func (f *fakeTelegram) count() int { return len(f.requests()) }

func reply(status int, body string) func(*http.Request, int) (int, string) {
	return func(*http.Request, int) (int, string) { return status, body }
}

const okBody = `{"ok":true}`

type queueSuite struct {
	suite.Suite

	clock time.Time
}

func (s *queueSuite) SetupTest() {
	s.clock = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
}

// fastTiming scales every delay down to milliseconds.
func fastTiming() Timing {
	return Timing{
		Spacing:       5 * time.Millisecond,
		Backoff:       []time.Duration{5 * time.Millisecond, 10 * time.Millisecond},
		MaxRetryAfter: time.Second,
		MessageBudget: time.Second,
		TickEvery:     time.Hour,
		StopDrain:     time.Second,
		QueueSize:     4,
		UrgentSize:    2,
	}
}

// newService builds a Service over a fresh DB holding an enabled, ready
// Config and a client pointed at f.
func (s *queueSuite) newService(f *fakeTelegram, t Timing, log *slog.Logger) *Service {
	database, err := db.Open(slog.New(slog.DiscardHandler), s.T().TempDir())
	s.Require().NoError(err)
	s.T().Cleanup(func() { _ = database.Close() })
	store := NewStore(database, db.NewWriteGuard())
	s.Require().NoError(store.Save(context.Background(),
		Config{Enabled: true, BotToken: testToken, ChatID: testChat}))
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	svc, err := New(log, store, NewClient(f.srv.URL, f.srv.Client()),
		WithTiming(t), WithClock(func() time.Time { return s.clock }))
	s.Require().NoError(err)
	s.T().Cleanup(svc.Stop)

	return svc
}

func (s *queueSuite) waitStatus(svc *Service, done func(DeliveryStatus) bool) DeliveryStatus {
	s.Require().Eventually(func() bool { return done(svc.status.snapshot()) }, waitFor, pollTick)

	return svc.status.snapshot()
}

func failed(st DeliveryStatus) bool    { return st.LastFailureAt != nil }
func succeeded(st DeliveryStatus) bool { return st.LastSuccessAt != nil }

func (s *queueSuite) TestDefaultsFillZeroFields() {
	f := newFakeTelegram(reply(200, okBody))
	defer f.srv.Close()
	svc := s.newService(f, Timing{Spacing: time.Millisecond}, nil)
	s.Equal(Timing{
		Spacing:       time.Millisecond,
		Backoff:       []time.Duration{2 * time.Second, 4 * time.Second},
		MaxRetryAfter: 10 * time.Second,
		MessageBudget: 30 * time.Second,
		ErrorWindow:   5 * time.Minute,
		CrashWindow:   time.Hour,
		TickEvery:     30 * time.Second,
		StopDrain:     5 * time.Second,
		QueueSize:     32,
		UrgentSize:    8,
	}, svc.timing)
	s.Equal(32, cap(svc.batch))
	s.Equal(8, cap(svc.urgent))
	s.Equal(Config{Enabled: true, BotToken: testToken, ChatID: testChat}, *svc.cfg.Load())
}

func (s *queueSuite) TestRateLimitedTwiceThenOK() {
	f := newFakeTelegram(func(_ *http.Request, n int) (int, string) {
		if n < 2 {
			return 429, `{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":0}}`
		}
		return 200, okBody
	})
	defer f.srv.Close()
	svc := s.newService(f, fastTiming(), nil)
	svc.Start()
	svc.enqueue(message{kind: KindTest, text: "hello"})

	st := s.waitStatus(svc, succeeded)
	s.Equal(3, f.count())
	s.Equal(s.clock, *st.LastSuccessAt)
	s.Nil(st.LastFailureAt)
}

func (s *queueSuite) TestServerErrorGetsThreeAttempts() {
	f := newFakeTelegram(reply(500, `{"ok":false,"error_code":500,"description":"Internal Server Error"}`))
	defer f.srv.Close()
	svc := s.newService(f, fastTiming(), nil)
	svc.Start()
	svc.enqueue(message{kind: KindTest, text: "hello"})

	st := s.waitStatus(svc, failed)
	svc.Stop()
	s.Equal(3, f.count())
	s.Equal("Internal Server Error", st.LastFailureError)
	s.Equal(s.clock, *st.LastFailureAt)
	s.Nil(st.LastSuccessAt)
}

func (s *queueSuite) TestClientErrorIsNotRetried() {
	f := newFakeTelegram(reply(400, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`))
	defer f.srv.Close()
	svc := s.newService(f, fastTiming(), nil)
	svc.Start()
	svc.enqueue(message{kind: KindTest, text: "hello"})

	st := s.waitStatus(svc, failed)
	svc.Stop()
	s.Equal(1, f.count())
	s.Equal("Bad Request: chat not found", st.LastFailureError)
}

func (s *queueSuite) TestRetryAfterAboveCapDrops() {
	f := newFakeTelegram(reply(
		429,
		`{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 5","parameters":{"retry_after":5}}`,
	))
	defer f.srv.Close()
	svc := s.newService(f, fastTiming(), nil) // MaxRetryAfter 1s
	svc.Start()
	svc.enqueue(message{kind: KindTest, text: "hello"})

	st := s.waitStatus(svc, failed)
	svc.Stop()
	s.Equal(1, f.count())
	s.Equal("Too Many Requests: retry after 5", st.LastFailureError)
}

// TestDroppedRateLimitPausesTheSender: a message dropped on a long 429 pauses
// the next send for retry_after instead of sending it into the flood ban.
func (s *queueSuite) TestDroppedRateLimitPausesTheSender() {
	f := newFakeTelegram(func(_ *http.Request, n int) (int, string) {
		if n == 0 {
			return 429, `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 1",` +
				`"parameters":{"retry_after":1}}`
		}

		return 200, okBody
	})
	defer f.srv.Close()
	t := fastTiming()
	t.MaxRetryAfter = 100 * time.Millisecond // retry_after 1s is above the cap: the first message drops
	svc := s.newService(f, t, nil)
	svc.Start()
	svc.enqueue(message{kind: KindTest, text: "one"})
	svc.enqueue(message{kind: KindTest, text: "two"})

	s.Require().Eventually(func() bool { return f.count() == 2 }, waitFor, pollTick)
	reqs := f.requests()
	s.GreaterOrEqual(reqs[1].at.Sub(reqs[0].at), time.Second, "the second send waits out retry_after")
	s.Contains(reqs[1].text, "two")
}

func (s *queueSuite) TestGapAfter() {
	svc := &Service{timing: Timing{Spacing: 3 * time.Second}}
	s.Equal(3*time.Second, svc.gapAfter(nil))
	s.Equal(3*time.Second, svc.gapAfter(errors.New("boom")))
	s.Equal(3*time.Second, svc.gapAfter(&APIError{Code: 400}))
	s.Equal(3*time.Second, svc.gapAfter(&APIError{Code: 429, RetryAfter: time.Second}))
	s.Equal(30*time.Second, svc.gapAfter(fmt.Errorf("send: %w", &APIError{Code: 429, RetryAfter: 30 * time.Second})))
	s.Equal(maxFloodPause, svc.gapAfter(&APIError{Code: 429, RetryAfter: 24 * time.Hour}))
}

func (s *queueSuite) TestWaitOverrunningBudgetDrops() {
	f := newFakeTelegram(reply(
		429,
		`{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 1","parameters":{"retry_after":1}}`,
	))
	defer f.srv.Close()
	t := fastTiming()
	t.MaxRetryAfter = 10 * time.Second
	t.MessageBudget = 300 * time.Millisecond
	svc := s.newService(f, t, nil)
	svc.Start()

	start := time.Now()
	svc.enqueue(message{kind: KindTest, text: "hello"})
	st := s.waitStatus(svc, failed)
	elapsed := time.Since(start)
	svc.Stop()

	s.Equal(1, f.count())
	s.Equal("Too Many Requests: retry after 1", st.LastFailureError)
	s.Less(elapsed, t.MessageBudget)
}

func (s *queueSuite) TestCrashJumpsTheBatchLane() {
	release := make(chan struct{})
	inFlight := make(chan struct{})
	f := newFakeTelegram(func(r *http.Request, n int) (int, string) {
		if n == 0 {
			close(inFlight)
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}

		return 200, okBody
	})
	defer f.srv.Close()
	t := fastTiming()
	t.Spacing = 20 * time.Millisecond
	t.MessageBudget = 500 * time.Millisecond
	svc := s.newService(f, t, nil)
	svc.Start()

	svc.enqueue(message{kind: KindRunFailed, text: "batch-0"})
	<-inFlight
	for i := 1; i <= t.QueueSize; i++ {
		svc.enqueue(message{kind: KindRunFailed, text: "batch-" + string(rune('0'+i))})
	}
	s.Len(svc.batch, t.QueueSize)
	enqueuedAt := time.Now()
	svc.enqueue(message{kind: KindCrash, text: "crash"})
	close(release)

	s.Require().Eventually(func() bool { return f.count() == t.QueueSize+2 }, waitFor, pollTick)
	reqs := f.requests()
	s.Equal("batch-0", reqs[0].text)
	s.Equal("crash", reqs[1].text)
	for i, r := range reqs[2:] {
		s.Equal("batch-"+string(rune('1'+i)), r.text)
	}
	s.LessOrEqual(reqs[1].at.Sub(enqueuedAt), t.MessageBudget+t.Spacing)
}

func (s *queueSuite) TestMessagesAreSpaced() {
	f := newFakeTelegram(reply(200, okBody))
	defer f.srv.Close()
	t := fastTiming()
	t.Spacing = 50 * time.Millisecond
	svc := s.newService(f, t, nil)
	svc.Start()
	svc.enqueue(message{kind: KindTest, text: "one"})
	svc.enqueue(message{kind: KindTest, text: "two"})

	s.Require().Eventually(func() bool { return f.count() == 2 }, waitFor, pollTick)
	reqs := f.requests()
	s.GreaterOrEqual(reqs[1].at.Sub(reqs[0].at), t.Spacing)
}

func (s *queueSuite) TestFullLanesDropWithoutBlocking() {
	f := newFakeTelegram(reply(200, okBody))
	defer f.srv.Close()
	t := fastTiming()
	svc := s.newService(f, t, nil) // sender not started

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range t.QueueSize + 3 {
			svc.enqueue(message{kind: KindRunFailed, text: "batch"})
		}
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		s.Require().Fail("enqueue blocked on a full batch lane")
	}
	s.Len(svc.batch, t.QueueSize)
	st := svc.status.snapshot()
	s.Equal("queue full: 3 message(s) dropped", st.LastFailureError)
	s.Equal(s.clock, *st.LastFailureAt)

	done = make(chan struct{})
	go func() {
		defer close(done)
		for range t.UrgentSize + 1 {
			svc.enqueue(message{kind: KindCrash, text: "crash"})
		}
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		s.Require().Fail("enqueue blocked on a full urgent lane")
	}
	s.Len(svc.urgent, t.UrgentSize)
	s.Equal("queue full: 4 message(s) dropped", svc.status.snapshot().LastFailureError)
	s.Zero(f.count())
}

func (s *queueSuite) TestSuccessResetsDropCount() {
	f := newFakeTelegram(reply(200, okBody))
	defer f.srv.Close()
	t := fastTiming()
	t.QueueSize = 1
	svc := s.newService(f, t, nil)
	svc.enqueue(message{kind: KindTest, text: "kept"})
	svc.enqueue(message{kind: KindTest, text: "dropped"})
	s.Equal("queue full: 1 message(s) dropped", svc.status.snapshot().LastFailureError)

	svc.Start()
	s.waitStatus(svc, succeeded)
	svc.enqueue(message{kind: KindTest, text: "kept"})
	svc.enqueue(message{kind: KindTest, text: "kept"})
	svc.enqueue(message{kind: KindTest, text: "kept"})
	// The sender may take one off the lane at any moment, so at least one of
	// the three fits; at most two are dropped, counted afresh from zero.
	st := svc.status.snapshot()
	s.Contains([]string{
		"queue full: 1 message(s) dropped", "queue full: 2 message(s) dropped",
	}, st.LastFailureError)
}

func (s *queueSuite) TestSwitchedOffBeforeSendDrops() {
	f := newFakeTelegram(reply(200, okBody))
	defer f.srv.Close()
	svc := s.newService(f, fastTiming(), nil)
	svc.enqueue(message{kind: KindTest, text: "hello"})
	off := *svc.cfg.Load()
	off.Enabled = false
	svc.cfg.Store(&off)

	svc.Start()
	svc.Stop() // drains the lanes before returning
	s.Zero(f.count())
	s.Equal(DeliveryStatus{}, svc.status.snapshot())
}

func (s *queueSuite) TestStopSkipsTheGapAfterTheLastMessage() {
	f := newFakeTelegram(reply(200, okBody))
	defer f.srv.Close()
	t := fastTiming()
	t.Spacing = 5 * time.Second // a gap after the last send would outlast StopDrain
	t.StopDrain = 2 * time.Second
	svc := s.newService(f, t, nil)
	svc.enqueue(message{kind: KindTest, text: "last words"})

	start := time.Now()
	svc.Start()
	svc.Stop()
	s.Less(time.Since(start), time.Second, "Stop returns once the queue is drained")
	s.Equal(1, f.count())
}

func (s *queueSuite) TestStopBoundedWhileEndpointHangs() {
	inFlight := make(chan struct{})
	var once sync.Once
	f := newFakeTelegram(func(r *http.Request, _ int) (int, string) {
		once.Do(func() { close(inFlight) })
		<-r.Context().Done()
		return 200, okBody
	})
	defer f.srv.Close()
	t := fastTiming()
	t.MessageBudget = 10 * time.Second
	t.StopDrain = 200 * time.Millisecond
	svc := s.newService(f, t, nil)
	svc.Start()
	svc.enqueue(message{kind: KindTest, text: "hello"})
	svc.enqueue(message{kind: KindTest, text: "never sent"})
	<-inFlight

	start := time.Now()
	svc.Stop()
	s.Less(time.Since(start), t.StopDrain+100*time.Millisecond)

	start = time.Now()
	svc.Stop() // idempotent
	s.Less(time.Since(start), 10*time.Millisecond)
	s.Equal(1, f.count())
	s.Nil(svc.status.snapshot().LastFailureAt) // shutdown is not a delivery failure

	left := len(svc.batch) // "never sent" is dropped, not delivered
	svc.enqueue(message{kind: KindTest, text: "after stop"})
	s.Len(svc.batch, left)
}

func (s *queueSuite) TestFailureLoggedOnceAtWarnWithoutToken() {
	f := newFakeTelegram(reply(400,
		`{"ok":false,"error_code":400,"description":"Bad Request: /bot`+testToken+`/sendMessage rejected"}`))
	defer f.srv.Close()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	svc := s.newService(f, fastTiming(), log)
	svc.Start()
	svc.enqueue(message{kind: KindRunFailed, text: "hello"})

	s.waitStatus(svc, failed)
	svc.Stop()
	out := buf.String()
	s.Equal(1, strings.Count(out, "telegram delivery failed"))
	s.Contains(out, "level=WARN")
	s.NotContains(out, "level=ERROR")
	s.Contains(out, "kind=")
	s.Contains(out, "Bad Request: /bot<redacted>/sendMessage rejected")
	s.NotContains(out, testToken)
	s.NotContains(svc.status.snapshot().LastFailureError, testToken)
}

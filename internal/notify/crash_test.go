package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/Toshik1978/folio/internal/db"
)

const (
	webPlace  = "web request handling"
	webDetail = "GET /api/books/{id}"
	nilDeref  = "runtime error: invalid memory address or nil pointer dereference"
)

type crashSuite struct {
	suite.Suite

	clock time.Time
	svc   *Service
}

func (s *crashSuite) SetupTest() {
	s.clock = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	s.svc = s.newService(Config{Enabled: true, BotToken: testToken, ChatID: testChat})
}

// newService builds an unstarted Service (so the lanes can be inspected) over
// a fresh DB holding cfg, with the suite's clock and a roomy crash lane.
func (s *crashSuite) newService(cfg Config) *Service {
	database, err := db.Open(slog.New(slog.DiscardHandler), s.T().TempDir())
	s.Require().NoError(err)
	s.T().Cleanup(func() { _ = database.Close() })
	store := NewStore(database, db.NewWriteGuard())
	s.Require().NoError(store.Save(context.Background(), cfg))
	svc, err := New(slog.New(slog.DiscardHandler), store, NewClient("http://127.0.0.1:0", nil),
		WithPublicURL("https://folio.example.com/"),
		WithTiming(Timing{UrgentSize: 512, QueueSize: 64}),
		WithClock(func() time.Time { return s.clock }))
	s.Require().NoError(err)
	s.T().Cleanup(svc.Stop)

	return svc
}

// drain empties both lanes, returning the urgent messages first.
func drain(svc *Service) (urgent, batch []message) {
	for {
		select {
		case m := <-svc.urgent:
			urgent = append(urgent, m)
		case m := <-svc.batch:
			batch = append(batch, m)
		default:
			return urgent, batch
		}
	}
}

// fakeStack is a debug.Stack-shaped trace whose first app frame is fn in
// internal/<dir>/<file>:<line>, beneath a recover helper and runtime frames.
func fakeStack(fn, dir, file string, line int) []byte {
	return fmt.Appendf(nil, `goroutine 7 [running]:
runtime/debug.Stack()
	/usr/local/go/src/runtime/debug/stack.go:26 +0x5e
%[1]s/internal/server.recoverer.func1.1()
	/src/folio/internal/server/recover.go:20 +0x45
panic({0x1a2b3c0?, 0xc000123456?})
	/usr/local/go/src/runtime/panic.go:787 +0x132
%[1]s/internal/%[2]s.%[3]s(0xc000123456, {0x1, 0x2})
	/src/folio/internal/%[2]s/%[4]s:%[5]d +0x1f
net/http.HandlerFunc.ServeHTTP(0xc0001, {0x2, 0x3})
	/usr/local/go/src/net/http/server.go:2294 +0x29
`, modulePath, dir, fn, file, line)
}

func booksStack() []byte { return fakeStack("(*Books).get", "api", "books_handler.go", 88) }

// txPanicError stands in for db.TxPanic: a panic re-raised from the write path
// carrying its own place and stack.
type txPanicError struct {
	value any
	stack []byte
}

func (p txPanicError) Error() string      { return fmt.Sprint(p.value) }
func (p txPanicError) CrashPlace() string { return "database write path" }
func (p txPanicError) CrashStack() []byte { return p.stack }

// report records a crash of v from the books handler.
func (s *crashSuite) report(v any) { s.svc.ReportCrash(webPlace, webDetail, v, booksStack()) }

// panicky panics while being formatted.
type panicky struct{}

func (panicky) Error() string { panic("formatting exploded") }

func (s *crashSuite) TestFirstCrashMessage() {
	s.report(errors.New(nilDeref))

	urgent, batch := drain(s.svc)
	s.Empty(batch)
	s.Require().Len(urgent, 1)
	s.Equal(KindCrash, urgent[0].kind)
	s.Equal(`https://folio.example.com
Crash recovered in web request handling
Where: GET /api/books/{id}
At: api.(*Books).get (internal/api/books_handler.go:88)
Error: `+nilDeref+`
Identical crashes in the next hour will be counted, not sent.`, urgent[0].text)
}

func (s *crashSuite) TestReportCrashReportsWhetherQueued() {
	s.True(s.svc.ReportCrash(webPlace, webDetail, errors.New(nilDeref), booksStack()))
	s.False(s.svc.ReportCrash(webPlace, webDetail, errors.New(nilDeref), booksStack()),
		"an identical crash inside the window is only counted")
}

func (s *crashSuite) TestRepeatsWithinHourGiveAtMostTwoMessages() {
	for range 50 {
		s.report(errors.New(nilDeref))
		s.clock = s.clock.Add(time.Minute)
	}
	urgent, batch := drain(s.svc)
	s.Len(urgent, 1)
	s.Empty(batch)

	s.clock = time.Date(2026, 11, 3, 13, 0, 0, 0, time.UTC)
	s.svc.tick(s.clock)
	urgent, batch = drain(s.svc)
	s.Empty(urgent)
	s.Require().Len(batch, 1)
	s.Equal(KindCrashRepeat, batch[0].kind)
	s.Equal(`https://folio.example.com
Crash in web request handling repeated 49 more times in the last hour
Where: GET /api/books/{id}
Error: `+nilDeref, batch[0].text)

	s.svc.tick(s.clock.Add(time.Hour))
	urgent, batch = drain(s.svc)
	s.Empty(urgent)
	s.Empty(batch)
	s.Empty(s.svc.crashes)
}

func (s *crashSuite) TestTickBeforeWindowKeepsEntry() {
	s.report(errors.New(nilDeref))
	s.report(errors.New(nilDeref))
	drain(s.svc)

	s.svc.tick(s.clock.Add(59 * time.Minute))
	urgent, batch := drain(s.svc)
	s.Empty(urgent)
	s.Empty(batch)
	s.Len(s.svc.crashes, 1)
}

func (s *crashSuite) TestSingleCrashHasNoFollowUp() {
	s.report(errors.New(nilDeref))
	drain(s.svc)

	s.svc.tick(s.clock.Add(time.Hour))
	urgent, batch := drain(s.svc)
	s.Empty(urgent)
	s.Empty(batch)
	s.Empty(s.svc.crashes)
}

func (s *crashSuite) TestCrashAfterWindowIsSentAgain() {
	s.report(errors.New(nilDeref))
	s.clock = s.clock.Add(time.Hour)
	s.svc.tick(s.clock)
	s.report(errors.New(nilDeref))

	urgent, _ := drain(s.svc)
	s.Len(urgent, 2)
}

func (s *crashSuite) TestExpiredWindowBeforeTickStillReportsRepeats() {
	s.report(errors.New(nilDeref))
	s.report(errors.New(nilDeref))
	drain(s.svc)

	s.clock = s.clock.Add(time.Hour)
	s.report(errors.New(nilDeref))
	urgent, batch := drain(s.svc)
	s.Len(urgent, 1)
	s.Require().Len(batch, 1)
	s.Contains(batch[0].text, "repeated 1 more times")
	s.Len(s.svc.crashes, 1)
}

func (s *crashSuite) TestNumbersAndAddressesShareKey() {
	s.report(errors.New("runtime error: index out of range [7] with length 3"))
	s.report(errors.New("runtime error: index out of range [9] with length 5"))
	s.report(errors.New("bad pointer 0xc000123abc"))
	s.report(errors.New("bad pointer 0xc000fff000"))

	urgent, _ := drain(s.svc)
	s.Require().Len(urgent, 2)
	s.Contains(urgent[0].text, "Error: runtime error: index out of range [7] with length 3")
	s.Len(s.svc.crashes, 2)
}

func (s *crashSuite) TestSameTextDifferentFramesDiffer() {
	s.svc.ReportCrash(webPlace, webDetail, errors.New(nilDeref), booksStack())
	s.svc.ReportCrash(webPlace, webDetail, errors.New(nilDeref),
		fakeStack("(*Authors).list", "api", "authors_handler.go", 41))

	urgent, _ := drain(s.svc)
	s.Require().Len(urgent, 2)
	s.Contains(urgent[1].text, "At: api.(*Authors).list (internal/api/authors_handler.go:41)")
}

func (s *crashSuite) TestNormalize() {
	s.Equal("index out of range [#] with length #", normalize("index out of range [7] with length 3"))
	s.Equal("addr # at line #", normalize("addr 0xc000a1b2f0 at line 1234"))
	s.Equal("no digits", normalize("no digits"))
}

func (s *crashSuite) TestTxPanicUsesInnerPlaceAndStack() {
	inner := fakeStack("(*importer).flush", "ingest", "importer.go", 120)
	s.report(txPanicError{value: errors.New(nilDeref), stack: inner})

	urgent, _ := drain(s.svc)
	s.Require().Len(urgent, 1)
	s.Equal(`https://folio.example.com
Crash recovered in database write path
Where: via web request handling (GET /api/books/{id})
At: ingest.(*importer).flush (internal/ingest/importer.go:120)
Error: `+nilDeref+`
Identical crashes in the next hour will be counted, not sent.`, urgent[0].text)

	s.svc.tick(s.clock.Add(time.Hour))
	s.report(txPanicError{value: errors.New(nilDeref), stack: inner})
	s.report(txPanicError{value: errors.New(nilDeref), stack: inner})
	s.svc.tick(s.clock.Add(2 * time.Hour))
	_, batch := drain(s.svc)
	s.Require().Len(batch, 1)
	s.Contains(batch[0].text, "Crash in database write path repeated 1 more times in the last hour\n"+
		"Where: via web request handling (GET /api/books/{id})\n")
}

func (s *crashSuite) TestNoFrameOmitsAt() {
	s.svc.ReportCrash("sync engine", `library "Test" sync`, "boom", []byte("goroutine 1 [running]:\n"))

	urgent, _ := drain(s.svc)
	s.Require().Len(urgent, 1)
	s.Equal(`https://folio.example.com
Crash recovered in sync engine
Where: library "Test" sync
Error: boom
Identical crashes in the next hour will be counted, not sent.`, urgent[0].text)
}

func (s *crashSuite) TestPanicTextCollapsedAndCapped() {
	s.report("line one\nline two " + strings.Repeat("x", 400))

	urgent, _ := drain(s.svc)
	s.Require().Len(urgent, 1)
	errLine := strings.Split(urgent[0].text, "\n")[4]
	s.True(strings.HasPrefix(errLine, "Error: line one line two x"))
	s.Len([]rune(errLine), len("Error: ")+maxItemChars)
	s.True(strings.HasSuffix(errLine, "…"))
}

func (s *crashSuite) TestFullMapSendsWithoutStoring() {
	for i := range maxCrashEntries {
		s.report(errors.New("crash " + letters(i)))
	}
	s.Len(s.svc.crashes, maxCrashEntries)
	s.Nil(s.svc.DeliveryStatus().LastFailureAt)

	s.report(errors.New("crash overflow"))
	s.report(errors.New("crash overflow"))
	urgent, _ := drain(s.svc)
	s.Len(urgent, maxCrashEntries+2)
	s.Len(s.svc.crashes, maxCrashEntries)
	st := s.svc.DeliveryStatus()
	s.Require().NotNil(st.LastFailureAt)
	s.Equal(s.clock, *st.LastFailureAt)
	s.Equal("crash de-duplication table full: crash sent without de-duplication", st.LastFailureError)

	// A key already stored is still de-duplicated while the map is full.
	s.report(errors.New("crash " + letters(0)))
	urgent, _ = drain(s.svc)
	s.Empty(urgent)
}

// letters spells i in base 26 with letters only, so normalize keeps it apart.
func letters(i int) string {
	return string([]byte{byte('a' + i/26/26%26), byte('a' + i/26%26), byte('a' + i%26)})
}

func (s *crashSuite) TestOffCollectsNothing() {
	svc := s.newService(Config{BotToken: testToken, ChatID: testChat})
	svc.ReportCrash(webPlace, webDetail, errors.New(nilDeref), booksStack())
	urgent, batch := drain(svc)
	s.Empty(urgent)
	s.Empty(batch)
	s.Empty(svc.crashes)
}

func (s *crashSuite) TestNotReadyCollectsNothing() {
	svc := s.newService(Config{Enabled: true})
	svc.ReportCrash(webPlace, webDetail, errors.New(nilDeref), booksStack())
	urgent, _ := drain(svc)
	s.Empty(urgent)
	s.Empty(svc.crashes)
}

func (s *crashSuite) TestSwitchingOffClearsMap() {
	s.report(errors.New(nilDeref))
	s.Len(s.svc.crashes, 1)

	_, err := s.svc.UpdateTelegram(context.Background(), Update{ChatID: testChat})
	s.Require().NoError(err)
	s.Empty(s.svc.crashes)
}

func (s *crashSuite) TestNeverPanics() {
	s.NotPanics(func() { s.report(panicky{}) })
	s.NotPanics(func() { s.svc.ReportCrash(webPlace, webDetail, nil, nil) })
}

func (s *crashSuite) TestLanes() {
	s.report(errors.New(nilDeref))
	s.report(errors.New(nilDeref))
	s.Len(s.svc.urgent, 1)
	s.Empty(s.svc.batch)

	s.svc.tick(s.clock.Add(time.Hour))
	s.Len(s.svc.urgent, 1)
	s.Len(s.svc.batch, 1)
}

// captureStack is the app frame firstAppFrame must find in a real trace.
func captureStack() []byte { return debug.Stack() }

func (s *crashSuite) TestFirstAppFrameRealStack() {
	frame := firstAppFrame(captureStack())
	s.Regexp(`^notify\.captureStack \(internal/notify/crash_test\.go:\d+\)$`, frame)
}

func (s *crashSuite) TestFirstAppFrameSkipsHelpers() {
	s.Equal("api.(*Books).get (internal/api/books_handler.go:88)", firstAppFrame(booksStack()))
	for _, helper := range []string{
		"server.recoverer.func1",
		"sync.(*Engine).safeSync.func1",
		"sync.(*Engine).recoverCrash",
		"sync.(*watcher).handleSafely.func1",
		"ingest.(*DeepIndexer).process.func2.1",
		"ebook.(*Dispatcher).run.func2",
		"db.(*WriteGuard).WithTx.func1",
		"notify.(*Service).ReportCrash",
	} {
		pkg, fn, _ := strings.Cut(helper, ".")
		s.Empty(firstAppFrame(fakeStack(fn, pkg, "x.go", 1)), helper)
	}
	s.Empty(firstAppFrame(nil))
	s.Empty(firstAppFrame([]byte("goroutine 1 [running]:\nmain.main()\n\t/x/main.go:3 +0x1\n")))
}

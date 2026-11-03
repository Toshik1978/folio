package notify

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/suite"

	"github.com/Toshik1978/folio/internal/db"
)

const summaryMore = "…and 7 more. See the server log for details."

type errWindowSuite struct {
	suite.Suite

	clock time.Time
	svc   *Service
}

func (s *errWindowSuite) SetupTest() {
	s.clock = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	s.svc = s.newService(Config{Enabled: true, BotToken: testToken, ChatID: testChat})
}

// newService builds an unstarted Service over a fresh DB holding cfg, on the
// suite's clock.
func (s *errWindowSuite) newService(cfg Config) *Service {
	database, err := db.Open(slog.New(slog.DiscardHandler), s.T().TempDir())
	s.Require().NoError(err)
	s.T().Cleanup(func() { _ = database.Close() })
	store := NewStore(database, db.NewWriteGuard())
	s.Require().NoError(store.Save(context.Background(), cfg))
	svc, err := New(slog.New(slog.DiscardHandler), store, NewClient("http://127.0.0.1:0", nil),
		WithPublicURL("https://folio.example.com/"),
		WithTiming(Timing{UrgentSize: 8, QueueSize: 64}),
		WithClock(func() time.Time { return s.clock }))
	s.Require().NoError(err)
	s.T().Cleanup(svc.Stop)

	return svc
}

// logError hands the Service an Error record stamped with the suite clock.
func (s *errWindowSuite) logError(msg string, attrs ...slog.Attr) {
	r := slog.NewRecord(s.clock, slog.LevelError, msg, 0)
	r.AddAttrs(attrs...)
	s.svc.LogError(context.Background(), r)
}

// stamp is the "HH:MM:SS" of the suite clock, formatted as the log output
// formats a record's time.
func (s *errWindowSuite) stamp() string { return s.clock.Format("15:04:05") }

// summaries drains the lanes and returns the batch messages, asserting each is
// an error summary.
func (s *errWindowSuite) summaries() []string {
	urgent, batch := drain(s.svc)
	s.Empty(urgent)
	texts := make([]string, 0, len(batch))
	for _, m := range batch {
		s.Equal(KindErrorSummary, m.kind)
		texts = append(texts, m.text)
	}

	return texts
}

func (s *errWindowSuite) sampleLines(text string) []string {
	var out []string
	for l := range strings.SplitSeq(text, "\n") {
		if strings.HasPrefix(l, "- ") {
			out = append(out, l)
		}
	}

	return out
}

func (s *errWindowSuite) TestTwelveRecordsGiveOneSummaryAtDeadline() {
	start := s.clock
	for i := range 12 {
		s.clock = start.Add(time.Duration(i) * 20 * time.Second)
		s.logError("download failed", slog.Any("error", errors.New("boom")))
	}

	s.svc.tick(start.Add(5*time.Minute - time.Second))
	s.Empty(s.summaries())

	s.svc.tick(start.Add(5 * time.Minute))
	got := s.summaries()
	s.Require().Len(got, 1)
	lines := strings.Split(got[0], "\n")
	s.Equal("https://folio.example.com", lines[0])
	s.Equal("12 error(s) in the last 5 minutes", lines[1])
	s.Len(s.sampleLines(got[0]), 5)
	s.Equal("- "+start.Format("15:04:05")+" download failed: boom", lines[2])
	s.Equal(summaryMore, lines[len(lines)-1])
	s.Len(lines, 8)
}

func (s *errWindowSuite) TestEmptyWindowSendsNothing() {
	s.svc.tick(s.clock.Add(time.Hour))
	s.Empty(s.summaries())
}

func (s *errWindowSuite) TestSecondWindowIsIndependent() {
	s.logError("first")
	s.logError("first again")
	s.svc.tick(s.clock.Add(5 * time.Minute))
	s.Require().Len(s.summaries(), 1)

	s.svc.tick(s.clock.Add(20 * time.Minute))
	s.Empty(s.summaries())

	s.clock = s.clock.Add(30 * time.Minute)
	s.logError("second")
	s.svc.tick(s.clock.Add(5*time.Minute - time.Second))
	s.Empty(s.summaries())
	s.svc.tick(s.clock.Add(5 * time.Minute))
	got := s.summaries()
	s.Require().Len(got, 1)
	s.Equal("https://folio.example.com\n1 error(s) in the last 5 minutes\n- "+s.stamp()+" second", got[0])
}

func (s *errWindowSuite) TestOnlyMessageAndErrorAttrAreUsed() {
	s.logError("read failed",
		slog.String("path", "/secret/elsewhere.epub"),
		slog.Any("error", errors.New("bad header")),
		slog.Int("book_id", 42))
	s.svc.tick(s.clock.Add(5 * time.Minute))
	got := s.summaries()
	s.Require().Len(got, 1)
	s.Equal([]string{"- " + s.stamp() + " read failed: bad header"}, s.sampleLines(got[0]))
	s.NotContains(got[0], "/secret/elsewhere.epub")
	s.NotContains(got[0], "42")
}

func (s *errWindowSuite) TestStringErrorAttrAndNoErrorAttr() {
	s.logError("with text", slog.String("error", "plain text"))
	s.logError("bare")
	s.svc.tick(s.clock.Add(5 * time.Minute))
	got := s.summaries()
	s.Require().Len(got, 1)
	s.Equal([]string{
		"- " + s.stamp() + " with text: plain text",
		"- " + s.stamp() + " bare",
	}, s.sampleLines(got[0]))
}

func (s *errWindowSuite) TestPathErrorKeepsPath() {
	err := &fs.PathError{Op: "open", Path: "/books/Main/x.epub", Err: fs.ErrPermission}
	s.logError("open book", slog.Any("error", err))
	s.svc.tick(s.clock.Add(5 * time.Minute))
	got := s.summaries()
	s.Require().Len(got, 1)
	s.Contains(got[0], "- "+s.stamp()+" open book: open /books/Main/x.epub: permission denied")
}

func (s *errWindowSuite) TestLongErrorIsCapped() {
	s.logError("long", slog.String("error", strings.Repeat("x", 400)))
	s.svc.tick(s.clock.Add(5 * time.Minute))
	got := s.summaries()
	s.Require().Len(got, 1)
	samples := s.sampleLines(got[0])
	s.Require().Len(samples, 1)
	line := strings.TrimPrefix(samples[0], "- ")
	s.Equal(maxItemChars, utf8.RuneCountInString(line))
	s.True(strings.HasSuffix(line, "…"))
}

func (s *errWindowSuite) TestMultiLineMessageIsOneLine() {
	s.logError("first\nsecond", slog.String("error", "a\n\tb"))
	s.svc.tick(s.clock.Add(5 * time.Minute))
	got := s.summaries()
	s.Require().Len(got, 1)
	s.Equal([]string{"- " + s.stamp() + " first second: a b"}, s.sampleLines(got[0]))
}

func (s *errWindowSuite) TestRunLinesCountTowardN() {
	retry := completedRun(1, RunDeepIndex)
	retry.PerBookErrors = 3
	s.svc.RunFinished(retry)
	fill := completedRun(2, RunSync)
	fill.FillFailed = "context deadline exceeded"
	s.svc.RunFinished(fill)
	s.svc.RunFinished(completedRun(3, RunSync))
	s.logError("other")

	s.svc.tick(s.clock.Add(5 * time.Minute))
	got := s.summaries()
	s.Require().Len(got, 1)
	s.Equal("https://folio.example.com\n"+
		"3 error(s) in the last 5 minutes\n"+
		"- Library \"Main\": deep index completed with 3 book(s) left for retry\n"+
		"- Library \"Main\": sync completed, but filling descriptions for new books failed: context deadline exceeded\n"+
		"- "+s.stamp()+" other", got[0])
}

func (s *errWindowSuite) TestRunWithBothAddsTwoLines() {
	o := completedRun(1, RunSync)
	o.PerBookErrors = 2
	o.FillFailed = "fill broke"
	s.svc.RunFinished(o)
	s.svc.tick(s.clock.Add(5 * time.Minute))
	got := s.summaries()
	s.Require().Len(got, 1)
	s.Contains(got[0], "\n2 error(s) in the last 5 minutes\n")
	s.Len(s.sampleLines(got[0]), 2)
}

func (s *errWindowSuite) TestCleanRunAddsNothing() {
	s.svc.RunFinished(completedRun(1, RunSync))
	s.svc.RunFinished(completedRun(1, RunDeepIndex))
	s.svc.tick(s.clock.Add(5 * time.Minute))
	s.Empty(s.summaries())
}

func (s *errWindowSuite) TestFailedRunAddsNothing() {
	o := failedRun(1, RunSync, "disk gone")
	o.PerBookErrors = 4
	s.svc.RunFinished(o)
	_, batch := drain(s.svc)
	s.Require().Len(batch, 1)
	s.Equal(KindRunFailed, batch[0].kind)

	s.svc.tick(s.clock.Add(5 * time.Minute))
	s.Empty(s.summaries())
}

// A book-file reading crash is reported as a crash and then as an unreadable
// book; neither puts anything into the error window.
func (s *errWindowSuite) TestReadCrashAndUnreadableAddNothing() {
	s.svc.ReportCrash("book-file reading", "book 5", "boom", nil)
	o := completedRun(1, RunSync)
	o.NewlyUnreadable = 1
	o.UnreadableSample = []UnreadableBook{{BookID: 5, Reason: "crashed"}}
	s.svc.RunFinished(o)
	drain(s.svc)

	s.svc.tick(s.clock.Add(5 * time.Minute))
	s.Empty(s.summaries())
}

func (s *errWindowSuite) TestStopFlushesOpenWindow() {
	s.logError("pending", slog.String("error", "e"))
	s.svc.Stop()
	got := s.summaries()
	s.Require().Len(got, 1)
	s.Equal("https://folio.example.com\n1 error(s) in the last 5 minutes\n- "+s.stamp()+" pending: e", got[0])
}

func (s *errWindowSuite) TestStopWithEmptyWindowSendsNothing() {
	s.svc.Stop()
	s.Empty(s.summaries())
}

func (s *errWindowSuite) TestOffStoresNothing() {
	for _, cfg := range []Config{
		{BotToken: testToken, ChatID: testChat},
		{Enabled: true, ChatID: testChat},
	} {
		s.svc = s.newService(cfg)
		s.logError("ignored")
		retry := completedRun(1, RunSync)
		retry.PerBookErrors = 3
		s.svc.RunFinished(retry)
		s.svc.tick(s.clock.Add(5 * time.Minute))
		s.svc.Stop()
		s.Empty(s.summaries())
	}
}

func (s *errWindowSuite) TestSwitchingOffDiscardsOpenWindow() {
	s.logError("before off")
	_, err := s.svc.UpdateTelegram(context.Background(), Update{ChatID: testChat})
	s.Require().NoError(err)
	_, err = s.svc.UpdateTelegram(context.Background(), Update{Enabled: true, ChatID: testChat})
	s.Require().NoError(err)

	s.svc.tick(s.clock.Add(5 * time.Minute))
	s.Empty(s.summaries())
}

func (s *errWindowSuite) TestLongSamplesStayWithinLimit() {
	for i := range 5 {
		s.logError(strings.Repeat("m", 200), slog.String("error", strings.Repeat(string(rune('a'+i)), 400)))
	}
	s.svc.tick(s.clock.Add(5 * time.Minute))
	got := s.summaries()
	s.Require().Len(got, 1)
	s.LessOrEqual(utf8.RuneCountInString(got[0]), maxMessageChars)
	s.Len(s.sampleLines(got[0]), 5)
}

func (s *errWindowSuite) TestSummaryTextIsTruncated() {
	w := errorWindow{count: 9}
	for range 5 {
		w.samples = append(w.samples, strings.Repeat("y", 1000))
	}
	text := errorSummaryText("https://folio.example.com", &w)
	s.LessOrEqual(utf8.RuneCountInString(text), maxMessageChars)
	s.True(strings.HasPrefix(text, "https://folio.example.com\n9 error(s) in the last 5 minutes\n"))
	s.True(strings.HasSuffix(text, truncMark))
}

func (s *errWindowSuite) TestTimeKeepsRecordZone() {
	zone := time.FixedZone("TEST", 3*60*60)
	r := slog.NewRecord(time.Date(2026, 11, 3, 9, 4, 5, 0, zone), slog.LevelError, "zoned", 0)
	s.Equal("09:04:05 zoned", logLine(r))
}

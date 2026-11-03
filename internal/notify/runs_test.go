package notify

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/Toshik1978/folio/internal/db"
)

type runsSuite struct {
	suite.Suite

	svc *Service
}

func (s *runsSuite) SetupTest() {
	s.svc = s.newService(Config{Enabled: true, BotToken: testToken, ChatID: testChat})
}

// newService builds an unstarted Service over a fresh DB holding cfg.
func (s *runsSuite) newService(cfg Config, extra ...Option) *Service {
	database, err := db.Open(slog.New(slog.DiscardHandler), s.T().TempDir())
	s.Require().NoError(err)
	s.T().Cleanup(func() { _ = database.Close() })
	store := NewStore(database, db.NewWriteGuard())
	s.Require().NoError(store.Save(context.Background(), cfg))
	opts := append([]Option{
		WithPublicURL("https://folio.example.com/"),
		WithTiming(Timing{UrgentSize: 8, QueueSize: 64}),
		WithClock(func() time.Time { return time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC) }),
	}, extra...)
	svc, err := New(slog.New(slog.DiscardHandler), store, NewClient("http://127.0.0.1:0", nil), opts...)
	s.Require().NoError(err)
	s.T().Cleanup(svc.Stop)

	return svc
}

func failedRun(id int64, kind RunKind, reason string) RunOutcome {
	return RunOutcome{LibraryID: id, Library: "Main", Kind: kind, Status: RunFailed, Reason: reason}
}

func completedRun(id int64, kind RunKind) RunOutcome {
	return RunOutcome{LibraryID: id, Library: "Main", Kind: kind, Status: RunCompleted}
}

func (s *runsSuite) batchTexts() []string {
	_, batch := drain(s.svc)
	texts := make([]string, 0, len(batch))
	for _, m := range batch {
		texts = append(texts, m.text)
	}

	return texts
}

func (s *runsSuite) TestKindString() {
	s.Equal("sync", RunSync.String())
	s.Equal("deep index", RunDeepIndex.String())
}

func (s *runsSuite) TestFailuresThenSuccessSendsTwoMessages() {
	for _, n := range []int{1, 3, 10} {
		s.Run("", func() {
			s.SetupTest()
			for range n {
				s.svc.RunFinished(failedRun(1, RunSync, "boom\nsecond line"))
			}
			s.svc.RunFinished(completedRun(1, RunSync))

			_, batch := drain(s.svc)
			s.Require().Len(batch, 2)
			s.Equal(KindRunFailed, batch[0].kind)
			s.Equal("https://folio.example.com\n"+
				"Library \"Main\": sync failed\n"+
				"Reason: boom second line\n"+
				"No further messages for this library until a run succeeds.", batch[0].text)
			s.Equal(KindRunRecovered, batch[1].kind)
			s.Equal("https://folio.example.com\n"+
				"Library \"Main\": recovered\n"+
				"The last sync completed successfully.", batch[1].text)
		})
	}
}

func (s *runsSuite) TestReasonIsCapped() {
	s.svc.RunFinished(failedRun(1, RunSync, strings.Repeat("x", 500)))
	texts := s.batchTexts()
	s.Require().Len(texts, 1)
	s.Contains(texts[0], "Reason: "+strings.Repeat("x", 299)+"…\n")
}

func (s *runsSuite) TestCrashReportedFailureThenSuccess() {
	o := failedRun(1, RunSync, "panic")
	o.CrashReported = true
	s.svc.RunFinished(o)
	s.svc.RunFinished(completedRun(1, RunSync))

	texts := s.batchTexts()
	s.Require().Len(texts, 1)
	s.Contains(texts[0], "recovered")
}

func (s *runsSuite) TestLibraryRemovedForgetsFailure() {
	s.svc.RunFinished(failedRun(1, RunSync, "boom"))
	s.svc.LibraryRemoved(1)
	s.svc.RunFinished(completedRun(1, RunSync))

	texts := s.batchTexts()
	s.Require().Len(texts, 1)
	s.Contains(texts[0], "sync failed")
}

func (s *runsSuite) TestStateIsPerLibraryNotPerKind() {
	s.svc.RunFinished(failedRun(1, RunSync, "boom"))
	s.svc.RunFinished(completedRun(1, RunDeepIndex))

	texts := s.batchTexts()
	s.Require().Len(texts, 2)
	s.Contains(texts[1], "The last deep index completed successfully.")
}

func (s *runsSuite) TestPerBookErrorsStillCountAsSuccess() {
	ok := completedRun(1, RunDeepIndex)
	ok.PerBookErrors = 4
	s.svc.RunFinished(ok)
	s.Empty(s.batchTexts())

	s.svc.RunFinished(failedRun(1, RunSync, "boom"))
	s.svc.RunFinished(ok)
	texts := s.batchTexts()
	s.Require().Len(texts, 2)
	s.Contains(texts[1], "recovered")
}

func (s *runsSuite) TestOffKeepsNoState() {
	off := s.newService(Config{})
	off.RunFinished(failedRun(1, RunSync, "boom"))
	_, batch := drain(off)
	s.Empty(batch)
	s.Empty(off.failing)
}

func (s *runsSuite) TestSwitchingOffClearsState() {
	s.svc.RunFinished(failedRun(1, RunSync, "boom"))
	s.Require().Len(s.batchTexts(), 1)

	_, err := s.svc.UpdateTelegram(context.Background(), Update{Enabled: false, ChatID: testChat})
	s.Require().NoError(err)
	s.Empty(s.svc.failing)
}

func (s *runsSuite) TestLibrariesAreIndependent() {
	s.svc.RunFinished(failedRun(1, RunSync, "boom"))
	s.svc.RunFinished(failedRun(2, RunSync, "boom"))
	s.svc.RunFinished(completedRun(2, RunSync))

	texts := s.batchTexts()
	s.Require().Len(texts, 3)
	s.Contains(texts[2], "recovered")

	s.svc.RunFinished(failedRun(1, RunSync, "again"))
	s.Empty(s.batchTexts())
}

func unreadableRun(total int, books ...UnreadableBook) RunOutcome {
	o := completedRun(1, RunSync)
	o.NewlyUnreadable = total
	o.UnreadableSample = books
	return o
}

func sampleBooks(n int) []UnreadableBook {
	books := make([]UnreadableBook, 0, n)
	for i := range n {
		books = append(books, UnreadableBook{BookID: int64(i + 1), Reason: "corrupt"})
	}

	return books
}

func (s *runsSuite) TestUnreadableSummaryCapsAtFiveItems() {
	s.svc.RunFinished(unreadableRun(7, sampleBooks(5)...))

	_, batch := drain(s.svc)
	s.Require().Len(batch, 1)
	s.Equal(KindUnreadable, batch[0].kind)
	s.Equal("https://folio.example.com\n"+
		"Library \"Main\": sync found 7 newly unreadable book file(s)\n"+
		"- https://folio.example.com/books/1: file is damaged or not in the expected format\n"+
		"- https://folio.example.com/books/2: file is damaged or not in the expected format\n"+
		"- https://folio.example.com/books/3: file is damaged or not in the expected format\n"+
		"- https://folio.example.com/books/4: file is damaged or not in the expected format\n"+
		"- https://folio.example.com/books/5: file is damaged or not in the expected format\n"+
		"…and 2 more. They will not be reported again unless the files change.", batch[0].text)
}

func (s *runsSuite) TestUnreadableSummaryWithoutOverflowLine() {
	s.svc.RunFinished(unreadableRun(3, sampleBooks(3)...))

	texts := s.batchTexts()
	s.Require().Len(texts, 1)
	s.Equal(3, strings.Count(texts[0], "\n- "))
	s.NotContains(texts[0], "…and")
}

func (s *runsSuite) TestNoUnreadableNoMessage() {
	s.svc.RunFinished(unreadableRun(0))
	s.Empty(s.batchTexts())
}

func (s *runsSuite) TestUnreadableDeepIndexKind() {
	o := unreadableRun(1, sampleBooks(1)...)
	o.Kind = RunDeepIndex
	s.svc.RunFinished(o)

	texts := s.batchTexts()
	s.Require().Len(texts, 1)
	s.Contains(texts[0], "Library \"Main\": deep index found 1 newly unreadable book file(s)")
}

func (s *runsSuite) TestBookRefWithoutPublicURL() {
	svc := s.newService(Config{Enabled: true, BotToken: testToken, ChatID: testChat}, WithPublicURL(""))
	svc.RunFinished(unreadableRun(1, UnreadableBook{BookID: 42, Reason: "corrupt"}))

	_, batch := drain(svc)
	s.Require().Len(batch, 1)
	s.Contains(batch[0].text, "\n- book #42: file is damaged")
	s.True(strings.HasPrefix(batch[0].text, "Folio\n"))
}

func (s *runsSuite) TestReasonLabels() {
	cases := map[string]string{
		"corrupt":       "file is damaged or not in the expected format",
		"missing_entry": "file is missing from its archive",
		"too_large":     "file is too large to read",
		"crashed":       "reading the file crashed the reader",
		"weird":         "weird",
	}
	for reason, label := range cases {
		s.Run(reason, func() {
			s.SetupTest()
			s.svc.RunFinished(unreadableRun(1, UnreadableBook{BookID: 9, Reason: reason}))
			texts := s.batchTexts()
			s.Require().Len(texts, 1)
			s.True(strings.HasSuffix(texts[0], ": "+label))
		})
	}
}

func (s *runsSuite) TestMassRemovalThresholds() {
	cases := []struct {
		name            string
		before, removed int
		deep, want      bool
		pct             string
	}{
		{"19 of 100", 100, 19, false, false, ""},
		{"20 of 200", 200, 20, false, true, "10%"},
		{"20 of 201", 201, 20, false, false, ""},
		{"25 of 100", 100, 25, false, true, "25%"},
		{"25 of 300", 300, 25, false, false, ""},
		{"no books before", 0, 25, false, false, ""},
		{"deep index", 100, 25, true, false, ""},
	}
	for _, c := range cases {
		s.Run(c.name, func() {
			s.SetupTest()
			o := completedRun(1, RunSync)
			if c.deep {
				o.Kind = RunDeepIndex
			}
			o.BooksBefore, o.BooksRemoved = c.before, c.removed
			s.svc.RunFinished(o)

			_, batch := drain(s.svc)
			if !c.want {
				s.Empty(batch)
				return
			}
			s.Require().Len(batch, 1)
			s.Equal(KindMassRemoval, batch[0].kind)
			s.Equal("https://folio.example.com\n"+
				fmt.Sprintf("Library \"Main\": sync removed %d of %d books (%s)\n", c.removed, c.before, c.pct)+
				"The removal has been applied. If this was not expected, check that the library source is complete and mounted.",
				batch[0].text)
		})
	}
}

func (s *runsSuite) TestMassRemovalPercentRoundsDown() {
	o := completedRun(1, RunSync)
	o.BooksBefore, o.BooksRemoved = 150, 20
	s.svc.RunFinished(o)

	texts := s.batchTexts()
	s.Require().Len(texts, 1)
	s.Contains(texts[0], "removed 20 of 150 books (13%)")
}

func (s *runsSuite) TestRecoveryThenUnreadableSummary() {
	s.svc.RunFinished(failedRun(1, RunSync, "boom"))
	s.svc.RunFinished(unreadableRun(1, sampleBooks(1)...))

	_, batch := drain(s.svc)
	s.Require().Len(batch, 3)
	s.Equal(KindRunFailed, batch[0].kind)
	s.Equal(KindRunRecovered, batch[1].kind)
	s.Equal(KindUnreadable, batch[2].kind)
}

func (s *runsSuite) TestCrashedBookListedOnceBesideCrashMessage() {
	s.svc.ReportCrash("book-file reading", "book 5", "boom", nil)
	s.svc.RunFinished(unreadableRun(1, UnreadableBook{BookID: 5, Reason: "crashed"}))

	urgent, batch := drain(s.svc)
	s.Require().Len(urgent, 1)
	s.Equal(KindCrash, urgent[0].kind)
	s.Require().Len(batch, 1)
	s.Equal(KindUnreadable, batch[0].kind)
	s.Equal(1, strings.Count(batch[0].text, "/books/5"))
	s.Contains(batch[0].text, "reading the file crashed the reader")
}

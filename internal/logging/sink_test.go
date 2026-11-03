package logging

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/stretchr/testify/suite"
)

type sinkSuite struct {
	suite.Suite
}

type capturedRecord struct {
	rec slog.Record
}

type captureSink struct {
	mu   sync.Mutex
	recs []capturedRecord
}

func (c *captureSink) LogError(_ context.Context, r slog.Record) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recs = append(c.recs, capturedRecord{rec: r})
}

func (c *captureSink) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.recs)
}

func recordAttrs(r slog.Record) map[string]string {
	out := map[string]string{}
	r.Attrs(func(a slog.Attr) bool {
		out[a.Key] = a.Value.String()
		return true
	})

	return out
}

func (s *sinkSuite) tapped() (*slog.Logger, *bytes.Buffer, *captureSink) {
	var buf bytes.Buffer
	sink := &captureSink{}
	base := slog.New(newCustomHandler(&buf, true, "production"))
	return WithErrorSink(base, sink), &buf, sink
}

func (s *sinkSuite) TestErrorIsForwardedWarnAndInfoAreNot() {
	log, buf, sink := s.tapped()

	log.Info("info")
	log.Warn("warn")
	s.Equal(0, sink.count())

	log.Error("boom", "k", "v")
	s.Require().Equal(1, sink.count())
	s.Equal("boom", sink.recs[0].rec.Message)
	s.Equal(slog.LevelError, sink.recs[0].rec.Level)
	s.Equal("v", recordAttrs(sink.recs[0].rec)["k"])
	s.Contains(buf.String(), "boom")
}

func (s *sinkSuite) TestMarkedContextIsNotForwarded() {
	log, buf, sink := s.tapped()

	log.ErrorContext(MarkReported(context.Background()), "already reported")

	s.Equal(0, sink.count())
	s.Contains(buf.String(), "already reported")
}

func (s *sinkSuite) TestWithAttrsAreCarriedOnForwardedRecord() {
	log, _, sink := s.tapped()

	log.With("error", errors.New("bad")).Error("x", "extra", 1)

	s.Require().Equal(1, sink.count())
	attrs := recordAttrs(sink.recs[0].rec)
	s.Equal("bad", attrs["error"])
	s.Equal("1", attrs["extra"])
}

func (s *sinkSuite) TestGroupsQualifyForwardedAttrs() {
	log, _, sink := s.tapped()

	log.WithGroup("g").With("a", 1).Error("x", "b", 2)

	s.Require().Equal(1, sink.count())
	attrs := recordAttrs(sink.recs[0].rec)
	s.Equal("1", attrs["g.a"])
	s.Equal("2", attrs["g.b"])
}

func (s *sinkSuite) TestOutputIdenticalWithAndWithoutSink() {
	emit := func(log *slog.Logger) {
		log.Info("one", "a", 1)
		log.With("error", errors.New("e")).Error("two", "b", 2)
		log.WithGroup("g").With("c", 3).Warn("three", "d", 4)
		log.ErrorContext(MarkReported(context.Background()), "four")
	}

	var plain bytes.Buffer
	plainLog := slog.New(newCustomHandler(&plain, true, "production"))
	emit(plainLog)

	tapped, buf, _ := s.tapped()
	emit(tapped)

	// Timestamps have one-second resolution; strip them by comparing from the level on.
	s.Equal(stripTime(plain.String()), stripTime(buf.String()))
}

func stripTime(out string) string {
	var res bytes.Buffer
	for line := range bytes.SplitSeq([]byte(out), []byte("\n")) {
		if len(line) > 20 {
			line = line[20:]
		}
		res.Write(line)
		res.WriteByte('\n')
	}

	return res.String()
}

func (s *sinkSuite) TestConcurrentLogging() {
	log, _, sink := s.tapped()

	const n = 50
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			log.With("i", i).Error("c")
			log.Info("noise")
		})
	}
	wg.Wait()

	s.Equal(n, sink.count())
}

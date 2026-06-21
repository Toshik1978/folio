package sync

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

type reporterSuite struct {
	suite.Suite
}

func TestReporterSuite(t *testing.T) {
	suite.Run(t, new(reporterSuite))
}

func (s *reporterSuite) TestThrottlesAddButFlushesFinal() {
	rec := &recordingPublisher{}
	now := time.Unix(1000, 0)
	clock := func() time.Time { return now }
	rep := newProgressReporter(rec, 7, clock, 250*time.Millisecond)

	// Rapid adds within the throttle window emit at most the first.
	rep.Add(1)
	rep.Add(1)
	rep.Add(1)
	s.Len(rec.events(), 1, "first Add emits, the rest are throttled")

	// Advancing past the interval lets the next Add emit.
	now = now.Add(300 * time.Millisecond)
	rep.Add(1)
	s.Len(rec.events(), 2)

	// Flush always emits the final frame regardless of the throttle.
	rep.emit()
	got := rec.events()
	s.Len(got, 3)
	last, ok := got[len(got)-1].Data.(progressEvent)
	s.True(ok)
	s.Equal(int64(7), last.Library)
	s.Equal(4, last.Processed)
}

func (s *reporterSuite) TestSetTotalEmitsImmediately() {
	rec := &recordingPublisher{}
	now := time.Unix(1000, 0)
	rep := newProgressReporter(rec, 7, func() time.Time { return now }, 250*time.Millisecond)

	rep.SetTotal(5000)
	got := rec.events()
	s.Require().Len(got, 1)
	ev, ok := got[0].Data.(progressEvent)
	s.True(ok)
	s.Equal(5000, ev.Total)
	s.Equal("progress:7", got[0].CoalesceKey)
}

func (s *reporterSuite) TestTotalOmittedWhenZero() {
	data, err := json.Marshal(progressEvent{Library: 7, Processed: 3})
	s.Require().NoError(err)
	s.NotContains(string(data), "total", "total must be omitted when zero so the UI shows an indeterminate bar")
	s.Contains(string(data), `"processed":3`)
}

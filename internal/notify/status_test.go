package notify

import (
	"time"

	"github.com/stretchr/testify/suite"
)

type statusSuite struct{ suite.Suite }

func (s *statusSuite) TestZeroSnapshot() {
	var b statusBox
	s.Equal(DeliveryStatus{}, b.snapshot())
}

func (s *statusSuite) TestSuccessThenFailureKeepsBoth() {
	var b statusBox
	ok := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	bad := ok.Add(time.Hour)
	b.recordSuccess(ok)
	b.recordFailure(bad, "boom")

	got := b.snapshot()
	s.Require().NotNil(got.LastSuccessAt)
	s.Require().NotNil(got.LastFailureAt)
	s.True(ok.Equal(*got.LastSuccessAt))
	s.True(bad.Equal(*got.LastFailureAt))
	s.Equal("boom", got.LastFailureError)
}

func (s *statusSuite) TestFailureThenSuccessKeepsBoth() {
	var b statusBox
	bad := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	ok := bad.Add(time.Hour)
	b.recordFailure(bad, "boom")
	b.recordSuccess(ok)

	got := b.snapshot()
	s.Require().NotNil(got.LastSuccessAt)
	s.Require().NotNil(got.LastFailureAt)
	s.True(ok.Equal(*got.LastSuccessAt))
	s.True(bad.Equal(*got.LastFailureAt))
	s.Equal("boom", got.LastFailureError)
}

func (s *statusSuite) TestSnapshotIsACopy() {
	var b statusBox
	ok := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	b.recordSuccess(ok)
	b.recordFailure(ok, "x")

	got := b.snapshot()
	*got.LastSuccessAt = ok.Add(24 * time.Hour)
	*got.LastFailureAt = ok.Add(24 * time.Hour)

	again := b.snapshot()
	s.True(ok.Equal(*again.LastSuccessAt))
	s.True(ok.Equal(*again.LastFailureAt))
}

package logging

import (
	"bytes"
	"context"
	"log/slog"
	"time"

	"github.com/stretchr/testify/suite"
)

type reportedSuite struct {
	suite.Suite
}

type otherKey struct{}

func (s *reportedSuite) TestUnmarkedIsNotReported() {
	s.False(IsReported(context.Background()))
}

func (s *reportedSuite) TestMarkedIsReported() {
	s.True(IsReported(MarkReported(context.Background())))
}

func (s *reportedSuite) TestChildOfMarkedStaysMarked() {
	child := context.WithValue(MarkReported(context.Background()), otherKey{}, "v")
	s.True(IsReported(child))
}

func (s *reportedSuite) TestMarkDoesNotChangeRenderedOutput() {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	render := func(ctx context.Context) string {
		var buf bytes.Buffer
		h := newCustomHandler(&buf, false, "")
		rec := slog.NewRecord(now, slog.LevelError, "boom", 0)
		rec.AddAttrs(slog.String("k", "v"))
		s.Require().NoError(h.Handle(ctx, rec))

		return buf.String()
	}
	s.Equal(render(context.Background()), render(MarkReported(context.Background())))
}

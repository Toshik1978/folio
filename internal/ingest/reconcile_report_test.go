package ingest

import (
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/Toshik1978/folio/internal/db/dbq"
)

type countingReporter struct {
	total     int
	processed int
}

func (c *countingReporter) SetTotal(n int) { c.total = n }
func (c *countingReporter) Add(n int)      { c.processed += n }

type reconcileReportSuite struct {
	suite.Suite
}

func TestReconcileReportSuite(t *testing.T) {
	suite.Run(t, new(reconcileReportSuite))
}

func (s *reconcileReportSuite) TestMarkSeenCountsProgress() {
	rep := &countingReporter{}
	rc := &reconciler{
		seen: map[string]struct{}{},
		prev: map[string]dbq.ListBookFilesByLibraryRow{},
		r:    rep,
	}
	rc.markSeen("/a/book.epub")
	rc.markSeen("/a/other.epub")
	s.Equal(2, rep.processed)
}

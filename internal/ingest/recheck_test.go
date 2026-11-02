package ingest

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ebook"
)

// unreadable returns the library's CountUnreadableBooks.
func (s *deepIndexSuite) unreadable() int64 {
	n, err := dbq.New(s.db).CountUnreadableBooks(context.Background(), dbq.CountUnreadableBooksParams{
		LibraryID: s.lib.ID, ReaderVersion: ebook.ReaderVersion,
	})
	s.Require().NoError(err)

	return n
}

// resync runs the library's INPX sync again.
func (s *deepIndexSuite) resync() {
	_, err := NewINPXParser(s.log, newTestDispatcher()).Sync(context.Background(), s.lib, s.db, s.store, nopReporter{})
	s.Require().NoError(err)
}

// TestSyncLiftsRecordsOfReplacedEntries replaces broken archive entries under
// an unchanged .inpx, so sync's size/mtime signal does not move. The sync's
// fingerprint re-check still lifts each replaced book's record, while an
// unchanged broken book stays counted until it too is replaced.
func (s *deepIndexSuite) TestSyncLiftsRecordsOfReplacedEntries() {
	_, err := s.indexer().Run(context.Background(), s.lib, DeepIndexAll(), nopReporter{})
	s.Require().NoError(err)
	s.Require().Equal(int64(2), s.unreadable())

	s.fixArchiveA()
	s.resync()

	s.Equal(int64(1), s.unreadable())
	_, ok := s.failure(diCorrupt)
	s.False(ok, "the replaced book's record is lifted by the sync")
	_, ok = s.failure(diBadEPUB)
	s.True(ok, "an unchanged broken book stays counted")

	s.replaceEntry("c.fb2-000007-000010.zip", "9.epub", "7.epub")
	s.resync()

	s.Equal(int64(0), s.unreadable())
}

// TestRecheckLeavesRecordsWhenLibraryIsUnreachable removes every archive, so no
// fingerprint can be computed: the re-check must not delete on uncertainty.
func (s *deepIndexSuite) TestRecheckLeavesRecordsWhenLibraryIsUnreachable() {
	_, err := s.indexer().Run(context.Background(), s.lib, DeepIndexAll(), nopReporter{})
	s.Require().NoError(err)
	for _, name := range diArchives()[1:] {
		s.Require().NoError(os.Remove(filepath.Join(s.dir, name)))
	}

	lifted, err := recheckReadFailures(context.Background(), s.log, s.db, newTestDispatcher(), s.lib)
	s.Require().NoError(err)

	s.Zero(lifted)
	s.Equal(int64(2), s.unreadable())
	s.Equal(2, s.failureCount())
}

// TestRecheckTouchesOnlyCurrentVersionMetadataRows stores stale-fingerprint
// rows that the re-check must leave alone: a metadata row of another reader
// version, and a cover-flow row.
func (s *deepIndexSuite) TestRecheckTouchesOnlyCurrentVersionMetadataRows() {
	q := dbq.New(s.db)
	now := time.Now().Unix()
	s.Require().NoError(q.UpsertReadFailure(context.Background(), dbq.UpsertReadFailureParams{
		BookID: s.book(diUTF8).ID, Flow: string(flowMetadata), Reason: string(ebook.ReasonCorrupt),
		Fingerprint: "stale", ReaderVersion: ebook.ReaderVersion + 1, RecordedAt: now,
	}))
	s.Require().NoError(q.UpsertReadFailure(context.Background(), dbq.UpsertReadFailureParams{
		BookID: s.book(diCP1251).ID, Flow: string(flowCover), Reason: string(ebook.ReasonCorrupt),
		Fingerprint: "stale", ReaderVersion: ebook.ReaderVersion, RecordedAt: now,
	}))

	lifted, err := recheckReadFailures(context.Background(), s.log, s.db, newTestDispatcher(), s.lib)
	s.Require().NoError(err)

	s.Zero(lifted)
	s.Equal(2, s.failureCount())
}

// TestSkippedSyncRecheckLiftsReplacedEntries runs the parser's standalone
// re-check, the one the engine calls when it skips an unchanged .inpx: the
// replaced book's record is lifted and the unchanged broken book stays.
func (s *deepIndexSuite) TestSkippedSyncRecheckLiftsReplacedEntries() {
	_, err := s.indexer().Run(context.Background(), s.lib, DeepIndexAll(), nopReporter{})
	s.Require().NoError(err)
	s.Require().Equal(int64(2), s.unreadable())
	s.fixArchiveA()

	lifted, err := NewINPXParser(s.log, newTestDispatcher()).RecheckReadFailures(context.Background(), s.lib, s.db)
	s.Require().NoError(err)

	s.Equal(1, lifted)
	s.Equal(int64(1), s.unreadable())
	_, ok := s.failure(diBadEPUB)
	s.True(ok, "an unchanged broken book stays counted")
}

// TestRecheckLiftsOnlyStaleRows lifts the record whose stored fingerprint no
// longer matches its book's files, and keeps the one that still matches.
func (s *deepIndexSuite) TestRecheckLiftsOnlyStaleRows() {
	_, err := s.indexer().Run(context.Background(), s.lib, DeepIndexAll(), nopReporter{})
	s.Require().NoError(err)
	s.fixArchiveA()

	lifted, err := recheckReadFailures(context.Background(), s.log, s.db, newTestDispatcher(), s.lib)
	s.Require().NoError(err)

	s.Equal(1, lifted)
	_, ok := s.failure(diCorrupt)
	s.False(ok)
	_, ok = s.failure(diBadEPUB)
	s.True(ok)
}

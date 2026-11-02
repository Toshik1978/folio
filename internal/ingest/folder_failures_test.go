package ingest

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ebook"
)

// seedReadFailures records a metadata and a cover failure for bookID under
// the current fingerprint of its files, as a real read would, so the post-sync
// re-check keeps them while the files are untouched.
func (s *folderSuite) seedReadFailures(bookID int64) {
	ctx := context.Background()
	q := dbq.New(s.db)
	book, err := q.GetBook(ctx, bookID)
	s.Require().NoError(err)
	library, err := q.GetLibrary(ctx, book.LibraryID)
	s.Require().NoError(err)
	ac := newArchiveCache()
	defer ac.close()
	for _, flow := range []readFlow{flowMetadata, flowCover} {
		cands := rankCandidates(newTestDispatcher(), flow, s.filesFor(bookID))
		fp, fpErr := newResolver().fingerprint(library, cands, ac)
		s.Require().NoError(fpErr)
		s.Require().NoError(q.UpsertReadFailure(ctx, dbq.UpsertReadFailureParams{
			BookID: bookID, Flow: string(flow), Reason: "corrupt", Fingerprint: fp,
			ReaderVersion: ebook.ReaderVersion, RecordedAt: 1,
		}))
	}
}

// readFailureFlows returns the flows bookID has a remembered failure for.
func (s *folderSuite) readFailureFlows(bookID int64) []string {
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT flow FROM book_read_failures WHERE book_id = ? ORDER BY flow`, bookID)
	s.Require().NoError(err)
	defer func() { s.Require().NoError(rows.Close()) }()

	var flows []string
	for rows.Next() {
		var flow string
		s.Require().NoError(rows.Scan(&flow))
		flows = append(flows, flow)
	}
	s.Require().NoError(rows.Err())

	return flows
}

// TestSyncChangedFileDropsReadFailures proves a real file change (mtime moved)
// re-synced through the folder parser drops the book's remembered metadata and
// cover read failures.
func (s *folderSuite) TestSyncChangedFileDropsReadFailures() {
	id := s.syncTouched(s.store, nil, func(bookID int64) {
		s.seedReadFailures(bookID)
		s.Equal([]string{"cover", "metadata"}, s.readFailureFlows(bookID))
	})

	s.Empty(s.readFailureFlows(id), "a changed file drops both failure records")
}

// TestSyncUnchangedKeepsReadFailures proves re-syncing an untouched file
// leaves the book's remembered read failures alone.
func (s *folderSuite) TestSyncUnchangedKeepsReadFailures() {
	ctx := context.Background()
	dir := s.T().TempDir()
	path := filepath.Join(dir, "a.fb2")
	s.writeFB2(path, "Alpha", "Ann", "Author", "", nil)
	old := time.Unix(1_000_000, 0)
	s.Require().NoError(os.Chtimes(path, old, old))

	src := s.insertLibrary("folder", dir)
	parser := FolderParser{log: s.log, parser: newTestDispatcher()}
	_, err := parser.Sync(ctx, src, s.db, s.store, nopReporter{})
	s.Require().NoError(err)
	id := s.booksByLibrary(src.ID)["Alpha"].ID
	s.seedReadFailures(id)

	_, err = parser.Sync(ctx, src, s.db, s.store, nopReporter{})
	s.Require().NoError(err)

	s.Equal([]string{"cover", "metadata"}, s.readFailureFlows(id), "an untouched re-sync keeps both failure records")
}

// TestSyncRetryableReadKeepsBook proves a known file that changed but cannot be
// opened this time (a permission or I/O blip) keeps its book instead of being
// pruned with its covers and edits.
func (s *folderSuite) TestSyncRetryableReadKeepsBook() {
	if os.Geteuid() == 0 {
		s.T().Skip("root ignores file permissions")
	}
	ctx := context.Background()
	dir := s.T().TempDir()
	path := filepath.Join(dir, "a.fb2")
	s.writeFB2(path, "Alpha", "Ann", "Author", "", nil)

	src := s.insertLibrary("folder", dir)
	parser := FolderParser{log: s.log, parser: newTestDispatcher()}
	_, err := parser.Sync(ctx, src, s.db, s.store, nopReporter{})
	s.Require().NoError(err)
	id := s.booksByLibrary(src.ID)["Alpha"].ID

	later := time.Now().Add(time.Hour)
	s.Require().NoError(os.Chtimes(path, later, later))
	s.Require().NoError(os.Chmod(path, 0o000))
	s.T().Cleanup(func() { _ = os.Chmod(path, 0o600) })

	_, err = parser.Sync(ctx, src, s.db, s.store, nopReporter{})
	s.Require().NoError(err)
	s.Equal(id, s.booksByLibrary(src.ID)["Alpha"].ID, "an unopenable known file keeps its book")
}

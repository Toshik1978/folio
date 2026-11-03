package ingest

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"sync"

	"github.com/Toshik1978/folio/internal/db"
	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ebook"
)

type backfillSuite struct {
	baseSuite
}

// fakeFileExtractor returns fixed backfill metadata and counts parses.
type fakeFileExtractor struct {
	meta   ebook.Metadata
	res    ReadResult // the zero value is ReadDone
	called int
}

func (f *fakeFileExtractor) Description(context.Context, int64) (ebook.Metadata, ReadResult) {
	f.called++
	return f.meta, f.res
}

// racingExtractor stores an annotation (as a concurrent enrich or manual edit
// would) while the file read is in flight, then returns the file's metadata.
type racingExtractor struct {
	fakeFileExtractor

	race func()
}

func (r *racingExtractor) Description(ctx context.Context, id int64) (ebook.Metadata, ReadResult) {
	r.race()
	return r.fakeFileExtractor.Description(ctx, id)
}

func (s *backfillSuite) seedBook(libraryID int64, title, format string) int64 {
	q := dbq.New(s.db)
	id, err := q.InsertBook(context.Background(), dbq.InsertBookParams{
		Title: title, LibraryID: libraryID, LibraryKey: title,
		Language: "en", ContentHash: title, AddedAt: 1,
	})
	s.Require().NoError(err)
	_, err = q.InsertBookFile(context.Background(), dbq.InsertBookFileParams{
		BookID: id, FileFormat: format, FileSize: 1, SourcePath: title + "." + format,
	})
	s.Require().NoError(err)

	return id
}

func (s *backfillSuite) TestFillPersistsAnnotationAndIdentifiers() {
	ext := &fakeFileExtractor{
		meta: ebook.Metadata{
			Annotation:  "Recovered annotation",
			Identifiers: []ebook.Identifier{{Type: "isbn", Value: "9780441013593"}},
		},
	}
	src := s.insertLibrary("inpx", filepath.Join(s.T().TempDir(), "lib.inpx"))
	id := s.seedBook(src.ID, "No Annotation", "fb2")

	bf := NewLocalBackfiller(s.log, s.db, db.NewWriteGuard(), ext)
	s.Require().NoError(bf.Fill(context.Background(), id))

	stored, err := dbq.New(s.db).GetBook(context.Background(), id)
	s.Require().NoError(err)
	s.True(stored.Annotation.Valid)
	s.Equal("Recovered annotation", stored.Annotation.String)
	s.Equal(int64(1), stored.MetadataChecked)

	ids, err := dbq.New(s.db).ListIdentifiersForBook(context.Background(), id)
	s.Require().NoError(err)
	s.Require().Len(ids, 1)
	s.Equal("9780441013593", ids[0].Value)
}

func (s *backfillSuite) TestFillKeepsAnnotationStoredDuringTheRead() {
	src := s.insertLibrary("inpx", filepath.Join(s.T().TempDir(), "lib.inpx"))
	id := s.seedBook(src.ID, "Raced", "fb2")
	ext := &racingExtractor{
		race: func() {
			_, err := s.db.ExecContext(context.Background(),
				`UPDATE books SET annotation = 'Enriched' WHERE id = ?`, id)
			s.Require().NoError(err)
		},
	}

	ext.meta = ebook.Metadata{Annotation: "From the file"}

	bf := NewLocalBackfiller(s.log, s.db, db.NewWriteGuard(), ext)
	s.Require().NoError(bf.Fill(context.Background(), id))

	stored, err := dbq.New(s.db).GetBook(context.Background(), id)
	s.Require().NoError(err)
	s.Equal("Enriched", stored.Annotation.String, "the file read must not overwrite a newer annotation")
	s.Equal(int64(1), stored.MetadataChecked)
}

func (s *backfillSuite) TestFillMarksCheckedOnEmptyAndSkipsSecondParse() {
	ext := &fakeFileExtractor{} // done, nothing parseable
	src := s.insertLibrary("inpx", filepath.Join(s.T().TempDir(), "lib.inpx"))
	id := s.seedBook(src.ID, "Empty", "fb2")

	bf := NewLocalBackfiller(s.log, s.db, db.NewWriteGuard(), ext)
	s.Require().NoError(bf.Fill(context.Background(), id))

	stored, err := dbq.New(s.db).GetBook(context.Background(), id)
	s.Require().NoError(err)
	s.False(stored.Annotation.Valid, "still no annotation")
	s.Equal(int64(1), stored.MetadataChecked, "marked checked (negative cache)")

	// A checked book is never re-parsed.
	s.Require().NoError(bf.Fill(context.Background(), id))
	s.Equal(1, ext.called, "checked book must not be re-extracted")
}

func (s *backfillSuite) TestFillDoesNotClobberExistingAnnotation() {
	ext := &fakeFileExtractor{meta: ebook.Metadata{Annotation: "from-file"}}
	src := s.insertLibrary("folder", "/lib")
	id := s.seedBook(src.ID, "Annotated", "fb2")
	// Seed the annotation WITHOUT touching metadata_checked so Fill proceeds past
	// the early-return gate and reaches the no-clobber guard.
	_, err := s.db.ExecContext(context.Background(), "UPDATE books SET annotation = ? WHERE id = ?", "keep me", id)
	s.Require().NoError(err)

	bf := NewLocalBackfiller(s.log, s.db, db.NewWriteGuard(), ext)
	s.Require().NoError(bf.Fill(context.Background(), id))

	// The extractor must have been called — proving we passed the metadata_checked gate.
	s.Equal(1, ext.called, "extractor must run to reach the no-clobber guard")

	stored, err := dbq.New(s.db).GetBook(context.Background(), id)
	s.Require().NoError(err)
	s.Equal("keep me", stored.Annotation.String, "backfill must not replace an existing annotation")
}

// capturedLog returns a logger recording every record, and the record slice.
func capturedLog() (*slog.Logger, *[]slog.Record) {
	recs := &[]slog.Record{}
	return slog.New(captureHandler{mu: &sync.Mutex{}, records: recs}), recs
}

func countMessages(recs []slog.Record, level slog.Level, msg string) int {
	n := 0
	for i := range recs {
		if recs[i].Level == level && recs[i].Message == msg {
			n++
		}
	}

	return n
}

func (s *backfillSuite) TestFillRecordsBrokenBookOnce() {
	ext := &fakeFileExtractor{res: ReadResult{
		Status: ReadBroken, Reason: ebook.ReasonCorrupt, Detail: "bad zip", Fingerprint: "fp1",
	}}
	src := s.insertLibrary("folder", "/lib")
	id := s.seedBook(src.ID, "Corrupt", "epub")
	log, recs := capturedLog()

	bf := NewLocalBackfiller(log, s.db, db.NewWriteGuard(), ext)
	s.Require().NoError(bf.Fill(context.Background(), id))

	row, err := dbq.New(s.db).
		GetReadFailure(context.Background(), dbq.GetReadFailureParams{BookID: id, Flow: "metadata"})
	s.Require().NoError(err)
	s.Equal("corrupt", row.Reason)
	s.Equal("bad zip", row.Detail)
	s.Equal("fp1", row.Fingerprint)
	s.Equal(int64(ebook.ReaderVersion), row.ReaderVersion)
	s.Positive(row.RecordedAt)
	s.Equal(1, countMessages(*recs, slog.LevelWarn, "unreadable book file"))

	stored, err := dbq.New(s.db).GetBook(context.Background(), id)
	s.Require().NoError(err)
	s.Zero(stored.MetadataChecked, "a broken book stays unchecked")

	// The extractor now reports the recorded failure: no write, no log.
	ext.res.Known = true
	s.Require().NoError(bf.Fill(context.Background(), id))
	s.Equal(1, countMessages(*recs, slog.LevelWarn, "unreadable book file"), "a known failure is not logged again")
	s.Equal(0, countMessages(*recs, slog.LevelWarn, "backfill: extract"))
}

func (s *backfillSuite) TestFillFixedFileClearsRecord() {
	ext := &fakeFileExtractor{res: ReadResult{Status: ReadBroken, Reason: ebook.ReasonCorrupt, Fingerprint: "fp1"}}
	src := s.insertLibrary("folder", "/lib")
	id := s.seedBook(src.ID, "Fixed", "epub")
	bf := NewLocalBackfiller(s.log, s.db, db.NewWriteGuard(), ext)
	s.Require().NoError(bf.Fill(context.Background(), id))

	ext.res = ReadResult{Status: ReadDone}
	ext.meta = ebook.Metadata{Annotation: "now readable"}
	s.Require().NoError(bf.Fill(context.Background(), id))

	_, err := dbq.New(s.db).GetReadFailure(context.Background(), dbq.GetReadFailureParams{BookID: id, Flow: "metadata"})
	s.Require().ErrorIs(err, sql.ErrNoRows)
	stored, err := dbq.New(s.db).GetBook(context.Background(), id)
	s.Require().NoError(err)
	s.Equal(int64(1), stored.MetadataChecked)
	s.Equal("now readable", stored.Annotation.String)
}

func (s *backfillSuite) TestFillRetryableWritesNothing() {
	ext := &fakeFileExtractor{res: ReadResult{Status: ReadRetryable, Err: errors.New("io"), Fingerprint: "fp"}}
	src := s.insertLibrary("folder", "/lib")
	id := s.seedBook(src.ID, "Flaky", "epub")
	log, recs := capturedLog()

	bf := NewLocalBackfiller(log, s.db, db.NewWriteGuard(), ext)
	s.Require().NoError(bf.Fill(context.Background(), id))

	_, err := dbq.New(s.db).GetReadFailure(context.Background(), dbq.GetReadFailureParams{BookID: id, Flow: "metadata"})
	s.Require().ErrorIs(err, sql.ErrNoRows)
	stored, err := dbq.New(s.db).GetBook(context.Background(), id)
	s.Require().NoError(err)
	s.Zero(stored.MetadataChecked)
	s.Equal(0, countMessages(*recs, slog.LevelWarn, "unreadable book file"))
	for i := range *recs {
		s.Less((*recs)[i].Level, slog.LevelWarn, "a retryable read logs at debug only")
	}
}

// warnAttr returns the string attribute key of the single Warn record with msg,
// failing the test when there is not exactly one such record.
func (s *baseSuite) warnAttr(recs []slog.Record, msg, key string) string {
	var out []string
	for i := range recs {
		if recs[i].Level != slog.LevelWarn || recs[i].Message != msg {
			continue
		}
		recs[i].Attrs(func(a slog.Attr) bool {
			if a.Key == key {
				out = append(out, a.Value.String())
			}

			return true
		})
	}
	s.Require().Len(out, 1, "one %q record carrying %q", msg, key)

	return out[0]
}

func (s *backfillSuite) TestFillBrokenLogCarriesDetail() {
	ext := &fakeFileExtractor{res: ReadResult{
		Status: ReadBroken, Reason: ebook.ReasonCorrupt, Detail: "bad zip entry", Fingerprint: "fp1",
	}}
	src := s.insertLibrary("folder", "/lib")
	id := s.seedBook(src.ID, "Corrupt", "epub")
	log, recs := capturedLog()

	s.Require().NoError(NewLocalBackfiller(log, s.db, db.NewWriteGuard(), ext).Fill(context.Background(), id))

	s.Equal("bad zip entry", s.warnAttr(*recs, "unreadable book file", "detail"))
}

func (s *backfillSuite) TestFillKeepsBookPendingWhenRecordDeleteFails() {
	ext := &fakeFileExtractor{res: ReadResult{Status: ReadBroken, Reason: ebook.ReasonCorrupt, Fingerprint: "fp1"}}
	src := s.insertLibrary("folder", "/lib")
	id := s.seedBook(src.ID, "Fixed", "epub")
	bf := NewLocalBackfiller(s.log, s.db, db.NewWriteGuard(), ext)
	s.Require().NoError(bf.Fill(context.Background(), id))

	_, err := s.db.ExecContext(context.Background(), `CREATE TRIGGER block_failure_delete
		BEFORE DELETE ON book_read_failures BEGIN SELECT RAISE(ABORT, 'blocked'); END`)
	s.Require().NoError(err)

	ext.res = ReadResult{Status: ReadDone}
	ext.meta = ebook.Metadata{Annotation: "now readable"}
	s.Require().NoError(bf.Fill(context.Background(), id))

	stored, err := dbq.New(s.db).GetBook(context.Background(), id)
	s.Require().NoError(err)
	s.Zero(stored.MetadataChecked, "a stale record must not be sealed by metadata_checked")
}

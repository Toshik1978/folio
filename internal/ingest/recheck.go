package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ebook"
)

// staleCheckBook is one book whose metadata-flow broken-read record the
// post-sync re-check compares against its files.
type staleCheckBook struct {
	id          int64
	fingerprint string
	files       []dbq.BookFile
}

// recheckReadFailures lifts the library's metadata-flow broken-read records
// whose file fingerprint no longer matches, so the unreadable count and filter
// stop counting books whose file changed behind sync's size/mtime signal (an
// archive entry replaced under an unchanged index). Only records of
// ebook.ReaderVersion are considered; older ones are not counted anyway.
//
// Each book's fingerprint is recomputed over its ranked metadata candidates,
// exactly as the read gate does: a stat or zip central-directory lookup, with
// no entry inflated and no file parsed. A record whose fingerprint differs is
// deleted. One whose fingerprint cannot be computed (a stat or open failure,
// e.g. the library is unreachable) is left in place: it is never deleted on
// uncertainty. Sources are only read.
//
// It returns the number of records deleted. The caller holds the write guard.
func recheckReadFailures(
	ctx context.Context, log *slog.Logger, database *sql.DB, reader bookReader, library dbq.Library,
) (int, error) {
	q := dbq.New(database)
	rows, err := q.ListMetadataFailureFiles(ctx, dbq.ListMetadataFailureFilesParams{
		LibraryID: library.ID, ReaderVersion: ebook.ReaderVersion,
	})
	if err != nil {
		return 0, fmt.Errorf("list metadata failure files: %w", err)
	}

	res := newResolver()
	ac := newArchiveCache()
	defer ac.close()
	group := ""
	lifted := 0
	for _, book := range groupStaleCheckBooks(rows) {
		if err := ctx.Err(); err != nil {
			return lifted, fmt.Errorf("recheck read failures: %w", err)
		}
		cands := rankCandidates(reader, flowMetadata, book.files)
		// Keep one archive open at a time: rows are ordered by source_path, so
		// the books of one archive are contiguous.
		if len(cands) > 0 {
			if g := res.group(library, cands[0]); g != group {
				ac.close()
				group = g
			}
		}
		fp, fpErr := res.fingerprint(library, cands, ac)
		if fpErr != nil {
			log.Debug("read failure re-check skipped",
				slog.Int64("book_id", book.id), slog.Any("error", fpErr))
			continue
		}
		if fp == book.fingerprint {
			continue
		}
		del := dbq.DeleteReadFailureParams{BookID: book.id, Flow: string(flowMetadata)}
		if err := q.DeleteReadFailure(ctx, del); err != nil {
			return lifted, fmt.Errorf("delete stale read failure of book %d: %w", book.id, err)
		}
		lifted++
	}

	return lifted, nil
}

// groupStaleCheckBooks collects the rows into books, in order of each book's
// first row. Every row of a book carries the same fingerprint.
func groupStaleCheckBooks(rows []dbq.ListMetadataFailureFilesRow) []staleCheckBook {
	var books []staleCheckBook
	byID := map[int64]int{}
	for _, row := range rows {
		i, ok := byID[row.BookID]
		if !ok {
			i = len(books)
			byID[row.BookID] = i
			books = append(books, staleCheckBook{id: row.BookID, fingerprint: row.Fingerprint})
		}
		books[i].files = append(books[i].files, dbq.BookFile{
			ID: row.FileID, BookID: row.BookID, FileFormat: row.FileFormat, SourcePath: row.SourcePath,
		})
	}

	return books
}

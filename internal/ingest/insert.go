package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/samber/lo"

	dbf "github.com/Toshik1978/folio/internal/db"
	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/htmltext"
)

// resolveAddedAt prefers the source's own add-timestamp (Calibre books.timestamp,
// INPX date) over the sync-run time, so a fresh import preserves the library's
// original chronology. Used on insert only — re-sync never restamps added_at.
func resolveAddedAt(rec bookRecord, runTime int64) int64 {
	if rec.AddedAt > 0 {
		return rec.AddedAt
	}

	return runTime
}

func insertBook(ctx context.Context, q *dbq.Queries, rec bookRecord, addedAt, importedAt int64) (int64, error) {
	var seriesID sql.NullInt64
	if rec.Series != "" {
		id, err := q.InsertSeries(ctx, dbq.InsertSeriesParams{Name: rec.Series, NameFold: dbf.Fold(rec.Series)})
		if err != nil {
			return 0, fmt.Errorf("upsert series: %w", err)
		}
		seriesID = sql.NullInt64{Int64: id, Valid: true}
	}

	lang := lo.CoalesceOrEmpty(rec.Language, undefinedLanguage)

	bookID, err := q.InsertBook(ctx, dbq.InsertBookParams{
		LibraryID:      rec.LibraryID,
		LibraryKey:     rec.LibraryKey,
		Title:          rec.Title,
		SeriesID:       seriesID,
		SeriesNumber:   rec.SeriesNumber,
		Language:       lang,
		Annotation:     nullString(rec.Annotation),
		Publisher:      nullString(rec.Publisher),
		PublisherFold:  dbf.FoldNull(nullString(rec.Publisher)),
		Year:           nullInt(rec.Year),
		Rating:         rec.Rating,
		ContentHash:    contentHash(rec),
		AddedAt:        addedAt,
		ImportedAt:     importedAt,
		MetadataFormat: nullString(strings.ToLower(rec.FileFormat)),
	})
	if err != nil {
		return 0, fmt.Errorf("insert book: %w", err)
	}

	if err := insertFileRow(ctx, q, bookID, rec); err != nil {
		return 0, err
	}

	authors := deduplicate(rec.Authors)
	if err := linkAuthors(ctx, q, bookID, authors); err != nil {
		return 0, err
	}
	if err := linkGenres(ctx, q, bookID, rec.Genres); err != nil {
		return 0, err
	}
	// First write of a fresh book: upsert is fine (no existing values to guard).
	if err := linkIdentifiers(ctx, q, bookID, rec.Identifiers, true); err != nil {
		return 0, err
	}

	if err := q.InsertBookFTS(ctx, dbq.InsertBookFTSParams{
		BookID:     strconv.FormatInt(bookID, 10),
		Title:      rec.Title,
		Authors:    strings.Join(authors, " "),
		Series:     rec.Series,
		Annotation: htmltext.StripMarkup(rec.Annotation),
	}); err != nil {
		return 0, fmt.Errorf("insert fts: %w", err)
	}

	return bookID, nil
}

// insertFileRow persists one physical file for a book.
func insertFileRow(ctx context.Context, q *dbq.Queries, bookID int64, rec bookRecord) error {
	if _, err := q.InsertBookFile(ctx, dbq.InsertBookFileParams{
		BookID:     bookID,
		FileFormat: rec.FileFormat,
		FileSize:   rec.FileSize,
		SourcePath: rec.SourcePath,
		Pages:      nullInt(rec.Pages),
		Mtime:      rec.Mtime,
	}); err != nil {
		return fmt.Errorf("insert book file: %w", err)
	}

	return nil
}

func linkAuthors(ctx context.Context, q *dbq.Queries, bookID int64, names []string) error {
	for _, name := range names {
		authorID, err := q.InsertAuthor(ctx, dbq.InsertAuthorParams{Name: name, NameFold: dbf.Fold(name)})
		if err != nil {
			return fmt.Errorf("upsert author: %w", err)
		}
		if err := q.InsertBookAuthor(ctx, dbq.InsertBookAuthorParams{BookID: bookID, AuthorID: authorID}); err != nil {
			return fmt.Errorf("link author: %w", err)
		}
	}

	return nil
}

func linkGenres(ctx context.Context, q *dbq.Queries, bookID int64, names []string) error {
	for _, name := range deduplicate(normalizeGenres(names)) {
		genreID, err := q.InsertGenre(ctx, dbq.InsertGenreParams{Name: name, NameFold: dbf.Fold(name)})
		if err != nil {
			return fmt.Errorf("upsert genre: %w", err)
		}
		if err := q.InsertBookGenre(ctx, dbq.InsertBookGenreParams{BookID: bookID, GenreID: genreID}); err != nil {
			return fmt.Errorf("link genre: %w", err)
		}
	}

	return nil
}

// linkIdentifiers persists a book's typed identifiers after cleaning and
// de-duping them by type (see cleanIdentifiers). overwrite selects upsert (the
// owner/overwrite path, where values may refresh) vs insert-if-absent (the
// gap-fill path, where an existing value always wins so a lower-priority edition
// can't replace it). Types are written in sorted order so the write sequence is
// deterministic regardless of map iteration.
func linkIdentifiers(ctx context.Context, q *dbq.Queries, bookID int64, ids []identifier, overwrite bool) error {
	clean := cleanIdentifiers(ids)
	types := make([]string, 0, len(clean))
	for typ := range clean {
		types = append(types, typ)
	}
	slices.Sort(types)
	for _, typ := range types {
		var err error
		if overwrite {
			err = q.InsertBookIdentifier(ctx, dbq.InsertBookIdentifierParams{
				BookID: bookID, Type: typ, Value: clean[typ],
			})
		} else {
			err = q.InsertBookIdentifierIfAbsent(ctx, dbq.InsertBookIdentifierIfAbsentParams{
				BookID: bookID, Type: typ, Value: clean[typ],
			})
		}
		if err != nil {
			return fmt.Errorf("link identifier: %w", err)
		}
	}

	return nil
}

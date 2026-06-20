package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	dbf "github.com/Toshik1978/folio/internal/db"
	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ebook"
	"github.com/Toshik1978/folio/internal/htmltext"
)

// bookView is the merged book as the reconcile decision sees it: every books
// column plus the current series name (joined in FindBookByLibraryKey), so series
// changes are decided by name without a mid-decision series upsert.
type bookView = dbq.FindBookByLibraryKeyRow

// mergeExisting reconciles rec onto an already-stored book: it loads the book's
// files once, ensures rec's file row exists/updates, then merges rec's metadata
// onto the book (gap-fill or, for the owning edition, overwrite). The preloaded
// file list also tells the merge whether a same-format sibling exists (M8).
func mergeExisting(ctx context.Context, q *dbq.Queries, existing bookView, rec bookRecord) error {
	files, err := q.ListFilesForBook(ctx, existing.ID)
	if err != nil {
		return fmt.Errorf("list files: %w", err)
	}
	if err := ensureFile(ctx, q, existing.ID, rec, files); err != nil {
		return err
	}

	return gapFill(ctx, q, existing, rec, files)
}

// ensureFile makes the book's preloaded set of files contain rec's file: it
// inserts a new row for an unseen source_path, or updates the row when its bytes
// — or the source's page count, which changes neither size nor mtime (e.g. a
// Calibre 'pages' custom-column edit) — changed.
func ensureFile(ctx context.Context, q *dbq.Queries, bookID int64, rec bookRecord, files []dbq.BookFile) error {
	for i := range files {
		if files[i].SourcePath != rec.SourcePath {
			continue
		}
		if files[i].FileSize == rec.FileSize && files[i].Mtime == rec.Mtime &&
			(rec.Pages == 0 || files[i].Pages == nullInt(rec.Pages)) {
			return nil // unchanged
		}

		if err := q.UpdateBookFile(ctx, dbq.UpdateBookFileParams{
			FileSize: rec.FileSize, Pages: nullInt(rec.Pages), Mtime: rec.Mtime, ID: files[i].ID,
		}); err != nil {
			return fmt.Errorf("update book file: %w", err)
		}

		return nil
	}

	return insertFileRow(ctx, q, bookID, rec)
}

// mergeMode is the reconcile decision: gap-fill empty fields, or let a
// higher-priority / edited-in-place edition overwrite owned fields.
type mergeMode int

const (
	modeGapFill mergeMode = iota
	modeOverwrite
)

// mergePlan is the complete, side-effect-free result of reconciling one record
// against a stored book. applyPlan turns it into writes. Series is carried by
// NAME (+ number); applyPlan upserts it to an id. upd holds the final scalar
// column values except series_id/series_number, which applyPlan fills.
type mergePlan struct {
	upd          dbq.UpdateBookParams // final scalar values (series fields filled at apply)
	seriesName   string               // "" = leave the book's series unchanged
	seriesNumber sql.NullFloat64

	bookChanged bool // any books-row scalar (incl. series) changed → UpdateBook
	titleFTS    bool // refresh books_fts.title
	seriesFTS   bool // refresh books_fts.series
	annFTS      bool // refresh books_fts.annotation
	relations   bool // relink authors/genres + identifier overwrite (owner path)
}

// planMerge reconciles rec against the stored book, producing a side-effect-free
// mergePlan. Pure: a function of (book, rec, files) only — unit-testable without a
// database. Mirrors the previous mergedBook decision exactly.
func planMerge(book bookView, rec bookRecord, files []dbq.BookFile) mergePlan {
	plan := mergePlan{
		upd: dbq.UpdateBookParams{
			Title: book.Title, SeriesID: book.SeriesID, SeriesNumber: book.SeriesNumber,
			Language: book.Language, Annotation: book.Annotation, Publisher: book.Publisher,
			Year: book.Year, Rating: book.Rating, ContentHash: book.ContentHash, ID: book.ID,
			MetadataFormat: book.MetadataFormat,
		},
	}

	// A manually matched book (Fix Match) is owned by the user: gap-fill only.
	if book.ManuallyMatched != 0 {
		planGapFill(book, rec, &plan)
		return plan
	}

	mode, recHash := resolveMode(book, rec, files)
	if mode == modeOverwrite {
		plan.upd.ContentHash = recHash
		planOverwrite(book, rec, &plan)
		return plan
	}

	planGapFill(book, rec, &plan)

	return plan
}

// resolveMode holds the entire subtle overwrite-vs-gap-fill decision: a strictly
// higher-priority format overwrites; an in-place edit of the metadata-owning
// edition (content-hash drift, no same-format sibling) overwrites; else gap-fill.
// It also returns the record's content hash whenever it had to compute one, so the
// caller can reuse it for the overwrite stamp instead of hashing the record twice
// (an empty string means the decision short-circuited before any hash was needed).
func resolveMode(book bookView, rec bookRecord, files []dbq.BookFile) (mergeMode, string) {
	if filePriority(rec.FileFormat) > filePriority(book.MetadataFormat.String) {
		return modeOverwrite, contentHash(rec)
	}
	if isMetadataOwner(book, rec) {
		recHash := contentHash(rec)
		if recHash != book.ContentHash && !hasSameFormatSibling(files, rec) {
			return modeOverwrite, recHash // edited in place
		}
		return modeGapFill, recHash
	}

	return modeGapFill, ""
}

func planOverwrite(book bookView, rec bookRecord, plan *mergePlan) {
	titleChanged, annChanged := overwriteBasicFields(book, rec, &plan.upd)
	seriesChanged := overwriteSeriesPlan(book, rec, plan)
	plan.upd.MetadataFormat = nullString(strings.ToLower(rec.FileFormat))

	plan.bookChanged = true
	plan.titleFTS = titleChanged
	plan.seriesFTS = seriesChanged
	plan.annFTS = annChanged
	plan.relations = true
}

// overwriteSeriesPlan stages rec's series (by name) when it differs from the
// book's current series name/number. Pure — applyPlan does the upsert. Equivalent
// to the old overwriteSeries, comparing names instead of ids.
func overwriteSeriesPlan(book bookView, rec bookRecord, plan *mergePlan) bool {
	if rec.Series == "" {
		return false
	}
	// Compare by name (not the upserted id). A fold-equivalent name (case/accent/space
	// differs from the stored canonical) is staged here but InsertSeries folds it back to
	// the same series id, so the only effect is one idempotent re-write — behavior is unchanged.
	if rec.Series != book.SeriesName || rec.SeriesNumber != book.SeriesNumber {
		plan.seriesName = rec.Series
		plan.seriesNumber = rec.SeriesNumber
		return true
	}

	return false
}

func planGapFill(book bookView, rec bookRecord, plan *mergePlan) {
	if book.Language == undefinedLanguage && rec.Language != "" && rec.Language != undefinedLanguage {
		plan.upd.Language = rec.Language
		plan.bookChanged = true
	}
	if !book.Annotation.Valid && strings.TrimSpace(rec.Annotation) != "" {
		plan.upd.Annotation = nullString(rec.Annotation)
		plan.annFTS, plan.bookChanged = true, true
	}
	if gapFillScalarFields(book, rec, &plan.upd) {
		plan.bookChanged = true
	}
	if fillSeriesPlan(book, rec, plan) {
		plan.seriesFTS, plan.bookChanged = true, true
	}
}

// fillSeriesPlan stages rec's series (by name) only when the book has none.
// Pure equivalent of the old fillSeries.
func fillSeriesPlan(book bookView, rec bookRecord, plan *mergePlan) bool {
	if book.SeriesID.Valid || rec.Series == "" {
		return false
	}
	plan.seriesName = rec.Series
	plan.seriesNumber = rec.SeriesNumber

	return true
}

// gapFill reconciles a grouped book against one of its editions by computing a
// pure plan and applying it.
func gapFill(ctx context.Context, q *dbq.Queries, book bookView, rec bookRecord, files []dbq.BookFile) error {
	return applyPlan(ctx, q, book, rec, planMerge(book, rec, files))
}

// applyPlan turns a mergePlan into writes: upsert the staged series to an id,
// update the books row (with the maintained publisher fold), mirror changed
// scalars into books_fts, and relink authors/genres/identifiers on the owner path.
func applyPlan(ctx context.Context, q *dbq.Queries, book bookView, rec bookRecord, plan mergePlan) error {
	upd := plan.upd
	if plan.seriesName != "" {
		sid, err := q.InsertSeries(ctx, dbq.InsertSeriesParams{
			Name: plan.seriesName, NameFold: dbf.Fold(plan.seriesName),
		})
		if err != nil {
			return fmt.Errorf("upsert series: %w", err)
		}
		upd.SeriesID = sql.NullInt64{Int64: sid, Valid: true}
		upd.SeriesNumber = plan.seriesNumber
	}
	if plan.bookChanged {
		upd.PublisherFold = dbf.FoldNull(upd.Publisher)
		if err := q.UpdateBook(ctx, upd); err != nil {
			return fmt.Errorf("update book: %w", err)
		}
	}
	if err := applyFTS(ctx, q, book.ID, upd, rec, plan); err != nil {
		return err
	}
	if plan.relations {
		if err := relinkAuthors(ctx, q, book.ID, rec); err != nil {
			return err
		}
		if err := relinkGenres(ctx, q, book.ID, rec); err != nil {
			return err
		}
	}
	// Owner/overwrite records may refresh identifier values; gap-fill records must
	// never replace one (a MOBI sibling's ISBN-10 must not clobber the EPUB's ISBN-13).
	return linkIdentifiers(ctx, q, book.ID, rec.Identifiers, plan.relations)
}

// applyFTS mirrors the changed scalar fields into the book's books_fts row so
// search matches what was just written.
func applyFTS(
	ctx context.Context, q *dbq.Queries, bookID int64, upd dbq.UpdateBookParams, rec bookRecord, plan mergePlan,
) error {
	id := strconv.FormatInt(bookID, 10)
	if plan.titleFTS {
		if err := q.UpdateBookFTSTitle(ctx, dbq.UpdateBookFTSTitleParams{Title: upd.Title, BookID: id}); err != nil {
			return fmt.Errorf("update fts title: %w", err)
		}
	}
	if plan.seriesFTS {
		if err := q.UpdateBookFTSSeries(
			ctx,
			dbq.UpdateBookFTSSeriesParams{Series: rec.Series, BookID: id},
		); err != nil {
			return fmt.Errorf("update fts series: %w", err)
		}
	}
	if plan.annFTS {
		if err := q.UpdateBookFTSAnnotation(ctx, dbq.UpdateBookFTSAnnotationParams{
			Annotation: htmltext.StripMarkup(rec.Annotation), BookID: id,
		}); err != nil {
			return fmt.Errorf("gap-fill annotation fts: %w", err)
		}
	}

	return nil
}

// isMetadataOwner reports whether rec's format is the edition that currently
// owns the book's scalar metadata (the format last written by insert/overwrite).
func isMetadataOwner(book bookView, rec bookRecord) bool {
	return book.MetadataFormat.Valid &&
		strings.EqualFold(rec.FileFormat, book.MetadataFormat.String)
}

// hasSameFormatSibling reports whether the book has another file (a different
// source_path) of rec's format.
func hasSameFormatSibling(files []dbq.BookFile, rec bookRecord) bool {
	for i := range files {
		if files[i].SourcePath != rec.SourcePath && strings.EqualFold(files[i].FileFormat, rec.FileFormat) {
			return true
		}
	}

	return false
}

func overwriteBasicFields(book bookView, rec bookRecord, upd *dbq.UpdateBookParams) (titleChanged, annChanged bool) {
	if rec.Title != "" && rec.Title != book.Title {
		upd.Title = rec.Title
		titleChanged = true
	}
	if strings.TrimSpace(rec.Annotation) != "" {
		if newAnn := nullString(rec.Annotation); newAnn != book.Annotation {
			upd.Annotation = newAnn
			annChanged = true
		}
	}
	overwriteScalarFields(book, rec, upd)

	return titleChanged, annChanged
}

// overwriteScalarFields replaces language/publisher/year/rating when rec carries
// a value that differs from the book's. Empty values never clobber an existing
// one. The caller's overwrite path always sets ch.book, so these need no change
// flag. Language is here (not in books_fts) because it is a scalar column, but it
// is part of contentHash, so a source-side language edit reaches this path via
// the editedInPlace branch.
func overwriteScalarFields(book bookView, rec bookRecord, upd *dbq.UpdateBookParams) {
	if rec.Language != "" && rec.Language != book.Language {
		upd.Language = rec.Language
	}
	if rec.Publisher != "" {
		if newPub := nullString(rec.Publisher); newPub != book.Publisher {
			upd.Publisher = newPub
		}
	}
	if rec.Year != 0 {
		if newYear := nullInt(rec.Year); newYear != book.Year {
			upd.Year = newYear
		}
	}
	if rec.Rating.Valid && rec.Rating != book.Rating {
		upd.Rating = rec.Rating
	}
}

// gapFillScalarFields fills publisher/year/rating on upd only where the book is
// missing them and rec supplies a value. Reports whether anything was filled.
func gapFillScalarFields(book bookView, rec bookRecord, upd *dbq.UpdateBookParams) bool {
	changed := false
	if !book.Publisher.Valid && rec.Publisher != "" {
		upd.Publisher = nullString(rec.Publisher)
		changed = true
	}
	if !book.Year.Valid && rec.Year != 0 {
		upd.Year = nullInt(rec.Year)
		changed = true
	}
	if !book.Rating.Valid && rec.Rating.Valid {
		upd.Rating = rec.Rating
		changed = true
	}

	return changed
}

// relinkAuthors replaces the book's author links with rec's authors and mirrors
// them into books_fts. It is a no-op when rec carries no authors, so a
// higher-priority edition that simply omits authors never erases the existing set.
func relinkAuthors(ctx context.Context, q *dbq.Queries, bookID int64, rec bookRecord) error {
	authors := deduplicate(rec.Authors)
	if len(authors) == 0 {
		return nil
	}
	if err := q.DeleteBookAuthors(ctx, bookID); err != nil {
		return fmt.Errorf("clear authors: %w", err)
	}
	if err := linkAuthors(ctx, q, bookID, authors); err != nil {
		return err
	}
	if err := q.UpdateBookFTSAuthors(ctx, dbq.UpdateBookFTSAuthorsParams{
		Authors: strings.Join(authors, " "), BookID: strconv.FormatInt(bookID, 10),
	}); err != nil {
		return fmt.Errorf("update fts authors: %w", err)
	}

	return nil
}

// relinkGenres replaces the book's genre links with rec's genres, normalized to
// the taxonomy. Like relinkAuthors it is a no-op when nothing remains after
// normalization, so a higher-priority edition whose genres don't map to the
// taxonomy never erases the book's existing set.
func relinkGenres(ctx context.Context, q *dbq.Queries, bookID int64, rec bookRecord) error {
	genres := deduplicate(normalizeGenres(rec.Genres))
	if len(genres) == 0 {
		return nil
	}
	if err := q.DeleteBookGenres(ctx, bookID); err != nil {
		return fmt.Errorf("clear genres: %w", err)
	}

	return linkGenres(ctx, q, bookID, genres)
}

// filePriority ranks formats by how good/reliable their metadata and cover are.
func filePriority(format string) int {
	switch strings.ToLower(format) {
	case ebook.FormatEPUB:
		return 4
	case ebook.FormatFB2:
		return 3
	case ebook.FormatMOBI, ebook.FormatAZW3, ebook.FormatAZW:
		return 2
	case ebook.FormatPDF:
		return 1
	default:
		return 0
	}
}

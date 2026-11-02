-- name: GetBook :one
SELECT *
FROM books
WHERE id = ?;

-- name: FindBookByLibraryKey :one
SELECT b.*, COALESCE(s.name, '') AS series_name
FROM books b
         LEFT JOIN series s ON s.id = b.series_id
WHERE b.library_id = ?
  AND b.library_key = ?;

-- ListBooks is a plain paginated listing. Production serves the books grid via
-- the dynamic Bob builder (db.FilterBooks); this static form currently backs
-- test helpers. Kept deliberately, not orphaned.
-- name: ListBooks :many
SELECT *
FROM books
ORDER BY added_at DESC, id DESC
LIMIT ? OFFSET ?;

-- name: ListBookIDsByLibrary :many
SELECT id
FROM books
WHERE library_id = ?;

-- name: CountBooksByLibrary :one
SELECT COUNT(*)
FROM books
WHERE library_id = ?;

-- PublisherFirstChars feeds the browse alphabet selector (see AuthorFirstChars).
-- publisher_fold is the app-maintained uppercase Unicode fold (db.FoldNull), so
-- a lowercase "penguin" lands under 'P' like the name_fold facets, not in '#',
-- and idx_books_publisher_fold supplies the values without scanning publishers.
-- name: PublisherFirstChars :many
SELECT DISTINCT substr(publisher_fold, 1, 1) AS first_char
FROM books
WHERE publisher_fold IS NOT NULL
  AND publisher_fold != ''
  AND (CAST(@library_id AS INTEGER) = 0 OR library_id = @library_id);

-- ListPublishersByLetter lists one alphabet bucket, paginated, range-seeking
-- idx_books_publisher_fold (matching the @lo/@hi the caller computes from the
-- folded letter) but GROUPing BY the raw publisher, so case variants ("Penguin"
-- vs "PENGUIN") stay distinct entries that each match the exact-publisher book
-- filter, while both still appear under the same letter.
-- name: ListPublishersByLetter :many
SELECT COALESCE(publisher, '') AS name, COUNT(*) AS book_count
FROM books
WHERE publisher_fold IS NOT NULL
  AND publisher_fold != ''
  AND publisher_fold >= CAST(@lo AS TEXT)
  AND publisher_fold < CAST(@hi AS TEXT)
  AND (CAST(@library_id AS INTEGER) = 0 OR library_id = @library_id)
GROUP BY publisher
ORDER BY publisher_fold, publisher
LIMIT @lim OFFSET @off;

-- ListPublishersNonLetter is the '#' bucket (see ListAuthorsNonLetter).
-- name: ListPublishersNonLetter :many
SELECT COALESCE(publisher, '') AS name, COUNT(*) AS book_count
FROM books
WHERE publisher_fold IS NOT NULL
  AND publisher_fold != ''
  -- char() bounds mirror cyrLo/cyrHi/latLo/latHi in internal/api/letters.go;
  -- drift guard: internal/api/letters_bounds_test.go TestSQLBucketBoundsMatchGoConstants
  AND NOT ((publisher_fold >= char(1040) AND publisher_fold < char(1072)) OR
           (publisher_fold >= char(65) AND publisher_fold < char(91)))
  AND (CAST(@library_id AS INTEGER) = 0 OR library_id = @library_id)
GROUP BY publisher
ORDER BY publisher_fold, publisher
LIMIT @lim OFFSET @off;

-- name: InsertBook :one
INSERT INTO books (library_id, library_key, title, series_id, series_number, language, annotation, publisher,
                   publisher_fold, year, rating, content_hash, added_at, imported_at, metadata_format)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id;

-- name: UpdateBook :exec
UPDATE books
SET title           = ?,
    series_id       = ?,
    series_number   = ?,
    language        = ?,
    annotation      = ?,
    publisher       = ?,
    publisher_fold  = ?,
    year            = ?,
    rating          = ?,
    content_hash    = ?,
    metadata_format = ?
WHERE id = ?;

-- UpdateBookAnnotation records a lazily-backfilled annotation and marks the book
-- as checked so the detail view never re-parses the source for it.
-- name: UpdateBookAnnotation :exec
UPDATE books
SET annotation       = ?,
    metadata_checked = 1
WHERE id = ?;

-- SetBookAnnotationIfNull stores a file-recovered annotation only when the book
-- has none, so manual, enriched or concurrently backfilled text is never
-- overwritten. Zero affected rows means the book already had one (or is gone).
-- name: SetBookAnnotationIfNull :execrows
UPDATE books
SET annotation = ?
WHERE id = ?
  AND annotation IS NULL;

-- BookHasAnnotation reports whether the book already carries an annotation.
-- name: BookHasAnnotation :one
SELECT annotation IS NOT NULL AS has_annotation
FROM books
WHERE id = ?;

-- MarkMetadataChecked records that the lazy local backfill ran (and found
-- nothing, or skipped the source, e.g. a PDF), so it isn't re-attempted.
-- name: MarkMetadataChecked :exec
UPDATE books
SET metadata_checked = 1
WHERE id = ?;

-- MarkEnrichmentChecked records that online enrichment was attempted (even on a
-- no-match), so the API isn't re-queried on every view.
-- name: MarkEnrichmentChecked :exec
UPDATE books
SET enrichment_checked = 1
WHERE id = ?;

-- UpdateBookEnrichment persists online-enrichment gap-fills (annotation,
-- publisher, year), restamps content_hash so the cover cache-buster changes,
-- and marks enrichment attempted.
-- name: UpdateBookEnrichment :exec
UPDATE books
SET annotation         = ?,
    publisher          = ?,
    publisher_fold     = ?,
    year               = ?,
    content_hash       = ?,
    enrichment_checked = 1
WHERE id = ?;

-- UpdateBookMatch overwrites a book's identity fields from a user-chosen volume
-- (manual Fix Match): title and series in addition to the enrichment scalars,
-- restamps content_hash, marks enrichment attempted, and sets manually_matched
-- so the sync merge gap-fills but never overwrites this book again. Author/genre
-- links are relinked separately.
-- name: UpdateBookMatch :exec
UPDATE books
SET title              = ?,
    series_id          = ?,
    series_number      = ?,
    language           = ?,
    annotation         = ?,
    publisher          = ?,
    publisher_fold     = ?,
    year               = ?,
    content_hash       = ?,
    enrichment_checked = 1,
    manually_matched   = 1
WHERE id = ?;

-- UpdateBookCoverPrio records the format priority of the cover the importer
-- just cached, so later runs can refuse to downgrade it.
-- name: UpdateBookCoverPrio :exec
UPDATE books
SET cover_prio = ?
WHERE id = ?;

-- MarkManuallyMatched flags a book as user-corrected (Fix Match) so the sync
-- merge gap-fills but never overwrites it, and records enrichment as attempted.
-- Used on the Fix-Match path even when the chosen volume changed nothing
-- displayable, so the lock is durable regardless of the field diff.
-- name: MarkManuallyMatched :exec
UPDATE books
SET manually_matched   = 1,
    enrichment_checked = 1
WHERE id = ?;

-- name: DeleteBook :exec
DELETE
FROM books
WHERE id = ?;

-- name: DeleteBooksByLibrary :exec
DELETE
FROM books
WHERE library_id = ?;

-- name: InsertBookFile :one
INSERT INTO book_files (book_id, file_format, file_size, source_path, pages, mtime)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING id;

-- name: GetBookFile :one
SELECT *
FROM book_files
WHERE id = ?;

-- name: ListFilesForBook :many
SELECT *
FROM book_files
WHERE book_id = ?
ORDER BY file_format;

-- ListFilesForBooks is the page-batched form of ListFilesForBook: one statement
-- per page of books instead of one per book.
-- name: ListFilesForBooks :many
SELECT *
FROM book_files
WHERE book_id IN (sqlc.slice('book_ids'))
ORDER BY book_id, file_format;

-- ListEmptyBooksByLibrary finds books left with no files after pruning, in one
-- pass; prune previously ran ListFilesForBook per book, O(books) statements
-- per sync.
-- name: ListEmptyBooksByLibrary :many
SELECT b.id
FROM books b
WHERE b.library_id = ?
  AND NOT EXISTS (SELECT 1 FROM book_files bf WHERE bf.book_id = b.id);

-- name: UpdateBookFile :exec
UPDATE book_files
SET file_size = ?,
    pages     = ?,
    mtime     = ?
WHERE id = ?;

-- name: DeleteBookFile :exec
DELETE
FROM book_files
WHERE id = ?;

-- name: ListBookFilesByLibrary :many
SELECT bf.id, bf.book_id, bf.source_path, bf.file_size, bf.mtime
FROM book_files bf
         JOIN books b ON b.id = bf.book_id
WHERE b.library_id = ?;

-- name: GlobalStats :one
-- GlobalStats returns catalog totals in one query. The authors/series counts use
-- COUNT(DISTINCT child_fk) rather than a correlated EXISTS over the parent table:
-- book_authors.author_id and books.series_id are FK-backed (ON DELETE CASCADE /
-- SET NULL), so a parent row is referenced iff it appears here. This counts only
-- non-orphan parents while scanning the child tables (index-assisted) instead of
-- every parent row.
SELECT (SELECT COUNT(*) FROM books)                                          AS total_books,
       (SELECT CAST(COALESCE(SUM(file_size), 0) AS INTEGER) FROM book_files) AS total_size_bytes,
       (SELECT COUNT(DISTINCT author_id) FROM book_authors)                  AS authors,
       (SELECT COUNT(DISTINCT series_id) FROM books)                         AS series,
       (SELECT COUNT(*) FROM libraries)                                      AS libraries;

-- name: GlobalBooksByFormat :many
SELECT file_format, COUNT(DISTINCT book_id) AS book_count
FROM book_files
GROUP BY file_format
ORDER BY book_count DESC;

-- name: GlobalBooksByLanguage :many
SELECT language, COUNT(*) AS book_count
FROM books
GROUP BY language
ORDER BY book_count DESC;

-- name: ListDistinctFormats :many
SELECT DISTINCT bf.file_format
FROM book_files bf
WHERE CAST(@library_id AS INTEGER) = 0
   OR EXISTS (SELECT 1 FROM books b WHERE b.id = bf.book_id AND b.library_id = @library_id)
ORDER BY bf.file_format;

-- name: ListDistinctLanguages :many
SELECT DISTINCT b.language
FROM books b
WHERE (CAST(@library_id AS INTEGER) = 0 OR b.library_id = @library_id)
ORDER BY b.language;

-- GetCoverState returns the lazy cover-extraction marker for a book
-- (0=unknown, 1=has real cover, 2=parsed/none). Drives the cover serve path.
-- name: GetCoverState :one
SELECT cover_state
FROM books
WHERE id = ?;

-- SetCoverState records the result of a cover-extraction attempt so the serve
-- path never re-parses a known book (real cover cached, or known cover-less).
-- name: SetCoverState :exec
UPDATE books
SET cover_state = ?
WHERE id = ?;

-- CountDeepIndexWork counts the books ListDeepIndexWork pages through: the
-- library's not-yet-checked books that have a file, in the same imported_at
-- scope. The deep indexer reports it as its progress total.
-- name: CountDeepIndexWork :one
SELECT COUNT(*)
FROM books b
WHERE b.library_id = sqlc.arg('library_id')
  AND b.metadata_checked = 0
  AND (CAST(sqlc.narg('imported_at') AS INTEGER) IS NULL OR b.imported_at = sqlc.narg('imported_at'))
  AND EXISTS (SELECT 1 FROM book_files f WHERE f.book_id = b.id);

-- ListDeepIndexWork returns one page of the deep indexer's work: every file of
-- the next lim not-yet-checked books of a library with an id above after_id,
-- taken in id order (pass 0 for the first page, then the page's largest
-- book_id). Paging by book keeps all of a book's files on one page, and
-- moving forward by id means a book the run left unchecked is not picked up
-- again by a later page. Within the page, rows are ordered by source_path so
-- the entries of one archive are contiguous. A NULL imported_at means every
-- book; otherwise only books stamped by that sync run. Each row carries the
-- book's metadata-flow broken-read record (NULL when there is none), joined on
-- its (book_id, flow) primary key, so the deep indexer's read gate needs no
-- query per book.
-- name: ListDeepIndexWork :many
SELECT b.id              AS book_id,
       f.id              AS file_id,
       f.file_format     AS file_format,
       f.source_path     AS source_path,
       rf.fingerprint    AS failure_fingerprint,
       rf.reader_version AS failure_reader_version
FROM (SELECT p.id
      FROM books p
      WHERE p.library_id = sqlc.arg('library_id')
        AND p.metadata_checked = 0
        AND (CAST(sqlc.narg('imported_at') AS INTEGER) IS NULL OR p.imported_at = sqlc.narg('imported_at'))
        AND p.id > sqlc.arg('after_id')
        AND EXISTS (SELECT 1 FROM book_files pf WHERE pf.book_id = p.id)
      ORDER BY p.id
      LIMIT sqlc.arg('lim')) page
         JOIN books b ON b.id = page.id
         JOIN book_files f ON f.book_id = b.id
         LEFT JOIN book_read_failures rf ON rf.book_id = b.id AND rf.flow = 'metadata'
ORDER BY f.source_path, b.id;

-- ResetCoverState forgets a book's imported or lazily extracted cover after its
-- file changed, so the next view re-extracts it: cover_state back to unknown and
-- cover_prio back to 0. A manual cover (cover_prio 1000) is never touched.
-- name: ResetCoverState :exec
UPDATE books
SET cover_state = 0,
    cover_prio  = 0
WHERE id = ?
  AND cover_prio < 1000;

-- MarkFileCover records that the lazy cover flow cached a cover extracted from
-- the book's own file: cover_state = has and cover_prio = 1, the file-cover
-- priority. The prio is what lets a later change to the file invalidate the
-- cover, while an online-enrichment cover (prio 0) is kept. A manual cover
-- (cover_prio 1000) is never touched.
-- name: MarkFileCover :exec
UPDATE books
SET cover_state = 1,
    cover_prio  = 1
WHERE id = ?
  AND cover_prio < 1000;

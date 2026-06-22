-- name: ListIdentifiersForBook :many
SELECT type, value
FROM book_identifiers
WHERE book_id = ?
ORDER BY type;

-- ListIdentifiersForBooks is the page-batched form of ListIdentifiersForBook.
-- name: ListIdentifiersForBooks :many
SELECT book_id, type, value
FROM book_identifiers
WHERE book_id IN (sqlc.slice('book_ids'))
ORDER BY book_id, type;

-- name: InsertBookIdentifier :exec
INSERT INTO book_identifiers (book_id, type, value)
VALUES (?, ?, ?)
ON CONFLICT (book_id, type) DO UPDATE SET value = excluded.value;

-- InsertBookIdentifierIfAbsent is the gap-fill variant of InsertBookIdentifier:
-- it never replaces an existing value, so a lower-priority edition cannot
-- overwrite e.g. the EPUB's ISBN-13 with its own ISBN-10.
-- name: InsertBookIdentifierIfAbsent :exec
INSERT INTO book_identifiers (book_id, type, value)
VALUES (?, ?, ?)
ON CONFLICT (book_id, type) DO NOTHING;

-- name: CountBookIdentifiers :one
SELECT COUNT(*)
FROM book_identifiers
WHERE book_id = ?;

-- DeleteBookIdentifiers removes every identifier of a book. Used only by the
-- manual edit, which then re-inserts the user's full set: the one path allowed
-- to drop identifiers (the shared enrichment engine only ever upserts).
-- name: DeleteBookIdentifiers :exec
DELETE FROM book_identifiers
WHERE book_id = ?;

-- FindBookByIdentifier resolves a cleaned (type, value) identifier to the lowest
-- matching book id in a library and that book's library_key. Used by the
-- importer to group same-book files that share a strong identifier (ISBN/ASIN/
-- Google/Goodreads) but differ in title/author metadata. Lowest id keeps the
-- choice deterministic when several already-split books share the identifier.
-- name: FindBookByIdentifier :one
SELECT bi.book_id, b.library_key
FROM book_identifiers bi
         JOIN books b ON b.id = bi.book_id
WHERE b.library_id = ?
  AND bi.type = ?
  AND bi.value = ?
ORDER BY bi.book_id
LIMIT 1;

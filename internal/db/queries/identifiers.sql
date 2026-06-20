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

-- ListAuthors lists every author (library-scoped), paginated. OPDS index feeds
-- page through it with @lim/@off; pass a large @lim with @off 0 for "all".
-- name: ListAuthors :many
SELECT a.*, COUNT(b.id) AS book_count
FROM authors a
         JOIN book_authors ba ON ba.author_id = a.id
         JOIN books b ON b.id = ba.book_id
WHERE (CAST(@library_id AS INTEGER) = 0 OR b.library_id = @library_id)
GROUP BY a.id
ORDER BY a.name_fold
LIMIT @lim OFFSET @off;

-- AuthorFirstChars returns the distinct first character of every author name
-- (library-scoped), feeding the browse page's alphabet selector. The caller
-- buckets these into the Cyrillic / Latin / '#' letters it renders. name_fold is
-- uppercase, so its first char lands in the uppercase buckets bucketOf expects.
-- name: AuthorFirstChars :many
SELECT DISTINCT substr(a.name_fold, 1, 1) AS first_char
FROM authors a
WHERE EXISTS (SELECT 1
              FROM book_authors ba
                       JOIN books b ON b.id = ba.book_id
              WHERE ba.author_id = a.id
                AND (CAST(@library_id AS INTEGER) = 0 OR b.library_id = @library_id));

-- ListAuthorsByLetter lists one alphabet bucket, paginated. It seeks the
-- authors.name_fold index with the [@lo, @hi) range the caller computes for the
-- selected letter, so it stays fast even for late letters in a huge library.
-- name: ListAuthorsByLetter :many
SELECT a.id,
       a.name,
       (SELECT COUNT(*)
        FROM book_authors ba
        WHERE ba.author_id = a.id
          AND (CAST(@library_id AS INTEGER) = 0
            OR EXISTS (SELECT 1 FROM books b WHERE b.id = ba.book_id AND b.library_id = @library_id))) AS book_count
FROM authors a
WHERE a.name_fold >= @lo
  AND a.name_fold < @hi
  AND EXISTS (SELECT 1
              FROM book_authors ba2
                       JOIN books b ON b.id = ba2.book_id
              WHERE ba2.author_id = a.id
                AND (CAST(@library_id AS INTEGER) = 0 OR b.library_id = @library_id))
ORDER BY a.name_fold
LIMIT @lim OFFSET @off;

-- ListAuthorsNonLetter is the '#' bucket: authors whose name starts outside the
-- Cyrillic (char 1040..1071) and Latin A-Z (char 65..90) ranges: digits,
-- punctuation, lowercase, and other scripts. See bucketOf in letters.go.
-- name: ListAuthorsNonLetter :many
SELECT a.id,
       a.name,
       (SELECT COUNT(*)
        FROM book_authors ba
        WHERE ba.author_id = a.id
          AND (CAST(@library_id AS INTEGER) = 0
            OR EXISTS (SELECT 1 FROM books b WHERE b.id = ba.book_id AND b.library_id = @library_id))) AS book_count
FROM authors a
-- char() bounds mirror cyrLo/cyrHi/latLo/latHi in internal/api/letters.go;
-- drift guard: internal/api/letters_bounds_test.go TestSQLBucketBoundsMatchGoConstants
WHERE NOT ((a.name_fold >= char(1040) AND a.name_fold < char(1072)) OR
           (a.name_fold >= char(65) AND a.name_fold < char(91)))
  AND EXISTS (SELECT 1
              FROM book_authors ba2
                       JOIN books b ON b.id = ba2.book_id
              WHERE ba2.author_id = a.id
                AND (CAST(@library_id AS INTEGER) = 0 OR b.library_id = @library_id))
ORDER BY a.name_fold
LIMIT @lim OFFSET @off;

-- name: ListAuthorsForBook :many
SELECT a.*
FROM authors a
         JOIN book_authors ba ON ba.author_id = a.id
WHERE ba.book_id = ?
ORDER BY a.name_fold;

-- ListAuthorsForBooks returns the author links for a whole page of books in
-- one statement (instead of one ListAuthorsForBook per book), keyed by book_id.
-- name: ListAuthorsForBooks :many
SELECT ba.book_id, a.id, a.name
FROM authors a
         JOIN book_authors ba ON ba.author_id = a.id
WHERE ba.book_id IN (sqlc.slice('book_ids'))
ORDER BY ba.book_id, a.name_fold;

-- InsertAuthor upserts by name_fold (the uppercase case-fold key), so case
-- variants from different records collapse to one row. The existing display
-- name is kept (first writer wins); DO UPDATE is only there so RETURNING yields
-- the id on conflict.
-- name: InsertAuthor :one
INSERT INTO authors (name, name_fold)
VALUES (?, ?)
ON CONFLICT (name_fold) DO UPDATE SET name = name
RETURNING id;

-- name: InsertBookAuthor :exec
INSERT INTO book_authors (book_id, author_id)
VALUES (?, ?)
ON CONFLICT DO NOTHING;

-- name: DeleteBookAuthors :exec
DELETE
FROM book_authors
WHERE book_id = ?;

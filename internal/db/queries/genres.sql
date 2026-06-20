-- ListGenres lists every genre/tag (library-scoped), paginated, mirroring
-- ListAuthors/ListSeries. Backs the OPDS genres index feed; page through it with
-- @lim/@off.
-- name: ListGenres :many
SELECT g.*, COUNT(bg.book_id) AS book_count
FROM genres g
         JOIN book_genres bg ON bg.genre_id = g.id
         JOIN books b ON b.id = bg.book_id
WHERE (CAST(@library_id AS INTEGER) = 0 OR b.library_id = @library_id)
GROUP BY g.id
ORDER BY g.name_fold
LIMIT @lim OFFSET @off;

-- GenreFirstChars feeds the browse alphabet selector (see AuthorFirstChars).
-- name: GenreFirstChars :many
SELECT DISTINCT substr(g.name_fold, 1, 1) AS first_char
FROM genres g
WHERE EXISTS (SELECT 1
              FROM book_genres bg
                       JOIN books b ON b.id = bg.book_id
              WHERE bg.genre_id = g.id
                AND (CAST(@library_id AS INTEGER) = 0 OR b.library_id = @library_id));

-- ListGenresByLetter lists one alphabet bucket, paginated, seeking genres.name_fold.
-- name: ListGenresByLetter :many
SELECT g.name,
       (SELECT COUNT(*)
        FROM book_genres bg
        WHERE bg.genre_id = g.id
          AND (CAST(@library_id AS INTEGER) = 0
            OR EXISTS (SELECT 1 FROM books b WHERE b.id = bg.book_id AND b.library_id = @library_id))) AS book_count
FROM genres g
WHERE g.name_fold >= @lo
  AND g.name_fold < @hi
  AND EXISTS (SELECT 1
              FROM book_genres bg2
                       JOIN books b ON b.id = bg2.book_id
              WHERE bg2.genre_id = g.id
                AND (CAST(@library_id AS INTEGER) = 0 OR b.library_id = @library_id))
ORDER BY g.name_fold
LIMIT @lim OFFSET @off;

-- ListGenresNonLetter is the '#' bucket (see ListAuthorsNonLetter).
-- name: ListGenresNonLetter :many
SELECT g.name,
       (SELECT COUNT(*)
        FROM book_genres bg
        WHERE bg.genre_id = g.id
          AND (CAST(@library_id AS INTEGER) = 0
            OR EXISTS (SELECT 1 FROM books b WHERE b.id = bg.book_id AND b.library_id = @library_id))) AS book_count
FROM genres g
WHERE NOT ((g.name_fold >= char(1040) AND g.name_fold < char(1072)) OR
           (g.name_fold >= char(65) AND g.name_fold < char(91)))
  AND EXISTS (SELECT 1
              FROM book_genres bg2
                       JOIN books b ON b.id = bg2.book_id
              WHERE bg2.genre_id = g.id
                AND (CAST(@library_id AS INTEGER) = 0 OR b.library_id = @library_id))
ORDER BY g.name_fold
LIMIT @lim OFFSET @off;

-- name: ListGenresForBook :many
SELECT g.*
FROM genres g
         JOIN book_genres bg ON bg.genre_id = g.id
WHERE bg.book_id = ?
ORDER BY g.name_fold;

-- ListGenresForBooks is the page-batched form of ListGenresForBook.
-- name: ListGenresForBooks :many
SELECT bg.book_id, g.id, g.name
FROM genres g
         JOIN book_genres bg ON bg.genre_id = g.id
WHERE bg.book_id IN (sqlc.slice('book_ids'))
ORDER BY bg.book_id, g.name_fold;

-- InsertGenre upserts by name_fold; see InsertAuthor.
-- name: InsertGenre :one
INSERT INTO genres (name, name_fold)
VALUES (?, ?)
ON CONFLICT (name_fold) DO UPDATE SET name = name
RETURNING id;

-- name: InsertBookGenre :exec
INSERT INTO book_genres (book_id, genre_id)
VALUES (?, ?)
ON CONFLICT DO NOTHING;

-- name: DeleteBookGenres :exec
DELETE
FROM book_genres
WHERE book_id = ?;

-- CountBookGenres reports how many genres a book has. The online enrichment tier
-- only gap-fills genres for a book that has none, so it never clobbers existing ones.
-- name: CountBookGenres :one
SELECT COUNT(*)
FROM book_genres
WHERE book_id = ?;

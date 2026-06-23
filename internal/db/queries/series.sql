-- name: GetSeries :one
SELECT *
FROM series
WHERE id = ?;

-- ListSeries lists every series (library-scoped), paginated, mirroring
-- ListAuthors for OPDS index feeds.
-- name: ListSeries :many
SELECT s.*, COUNT(b.id) AS book_count
FROM series s
         JOIN books b ON b.series_id = s.id
WHERE (CAST(@library_id AS INTEGER) = 0 OR b.library_id = @library_id)
GROUP BY s.id
ORDER BY s.name_fold
LIMIT @lim OFFSET @off;

-- SeriesFirstChars feeds the browse alphabet selector (see AuthorFirstChars).
-- name: SeriesFirstChars :many
SELECT DISTINCT substr(s.name_fold, 1, 1) AS first_char
FROM series s
WHERE EXISTS (SELECT 1
              FROM books b
              WHERE b.series_id = s.id
                AND (CAST(@library_id AS INTEGER) = 0 OR b.library_id = @library_id));

-- ListSeriesByLetter lists one alphabet bucket, paginated, seeking series.name_fold.
-- name: ListSeriesByLetter :many
SELECT s.id,
       s.name,
       (SELECT COUNT(*)
        FROM books b
        WHERE b.series_id = s.id
          AND (CAST(@library_id AS INTEGER) = 0 OR b.library_id = @library_id)) AS book_count
FROM series s
WHERE s.name_fold >= @lo
  AND s.name_fold < @hi
  AND EXISTS (SELECT 1
              FROM books b
              WHERE b.series_id = s.id
                AND (CAST(@library_id AS INTEGER) = 0 OR b.library_id = @library_id))
ORDER BY s.name_fold
LIMIT @lim OFFSET @off;

-- ListSeriesNonLetter is the '#' bucket (see ListAuthorsNonLetter).
-- name: ListSeriesNonLetter :many
SELECT s.id,
       s.name,
       (SELECT COUNT(*)
        FROM books b
        WHERE b.series_id = s.id
          AND (CAST(@library_id AS INTEGER) = 0 OR b.library_id = @library_id)) AS book_count
FROM series s
-- char() bounds mirror cyrLo/cyrHi/latLo/latHi in internal/api/letters.go;
-- drift guard: internal/api/letters_bounds_test.go TestSQLBucketBoundsMatchGoConstants
WHERE NOT ((s.name_fold >= char(1040) AND s.name_fold < char(1072)) OR
           (s.name_fold >= char(65) AND s.name_fold < char(91)))
  AND EXISTS (SELECT 1
              FROM books b
              WHERE b.series_id = s.id
                AND (CAST(@library_id AS INTEGER) = 0 OR b.library_id = @library_id))
ORDER BY s.name_fold
LIMIT @lim OFFSET @off;

-- InsertSeries upserts by name_fold; see InsertAuthor.
-- name: InsertSeries :one
INSERT INTO series (name, name_fold)
VALUES (?, ?)
ON CONFLICT (name_fold) DO UPDATE SET name = name
RETURNING id;

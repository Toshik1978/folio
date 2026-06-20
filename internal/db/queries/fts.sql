-- name: InsertBookFTS :exec
INSERT INTO books_fts (book_id, title, authors, series, annotation)
VALUES (?, ?, ?, ?, ?);

-- name: UpdateBookFTSAnnotation :exec
UPDATE books_fts
SET annotation = ?
WHERE book_id = ?;

-- name: UpdateBookFTSTitle :exec
UPDATE books_fts
SET title = ?
WHERE book_id = ?;

-- name: UpdateBookFTSAuthors :exec
UPDATE books_fts
SET authors = ?
WHERE book_id = ?;

-- name: UpdateBookFTSSeries :exec
UPDATE books_fts
SET series = ?
WHERE book_id = ?;

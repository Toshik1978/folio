-- +goose Up
-- Partial index over the books that carry a description.
--
-- "Similar books" weighs a book's words by inverse document frequency, which
-- needs N, the number of described books, on every call. Without an index that
-- count reads every book row, annotation overflow pages included; this index
-- holds only the ids of described books, so the count scans it instead. Its
-- WHERE clause must stay identical to the count in internal/db/similar.go or
-- the planner will not use it.
CREATE INDEX idx_books_described ON books (id) WHERE annotation IS NOT NULL AND annotation <> '';

-- +goose Down
DROP INDEX idx_books_described;

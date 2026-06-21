-- +goose Up
CREATE VIRTUAL TABLE books_fts USING fts5
(
    book_id UNINDEXED,
    title,
    authors,
    series,
    annotation,
    tokenize = 'unicode61 remove_diacritics 1'
);

-- +goose StatementBegin
CREATE TRIGGER books_ad
    AFTER DELETE
    ON books
BEGIN
    -- book_id is stored as TEXT (inserts bind strconv.FormatInt; see
    -- internal/ingest/insert.go) but old.id is an INTEGER column. This match
    -- works only because the comparison relies on SQLite column affinity:
    -- old.id carries INTEGER affinity, so SQLite coerces the no-affinity TEXT
    -- book_id ('42' -> 42) before comparing. A bare-literal form (book_id = 42)
    -- would NOT match ('42' = 42 is false). Do not "simplify" this to a literal,
    -- and keep binding book_id as a string elsewhere, or FTS cleanup silently
    -- breaks (deleted books linger in search). Guarded by the regression test
    -- TestDeletedBookRemovedFromFTS in internal/db/booksfilter_test.go.
    DELETE FROM books_fts WHERE book_id = old.id;
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER IF EXISTS books_ad;
DROP TABLE IF EXISTS books_fts;

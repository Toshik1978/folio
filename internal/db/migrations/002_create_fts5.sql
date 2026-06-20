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
    DELETE FROM books_fts WHERE book_id = old.id;
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER IF EXISTS books_ad;
DROP TABLE IF EXISTS books_fts;

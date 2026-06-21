-- +goose Up
CREATE INDEX idx_book_identifiers_lookup ON book_identifiers (type, value);

-- +goose Down
DROP INDEX IF EXISTS idx_book_identifiers_lookup;

-- +goose Up
-- Remembers books whose source could not be read, so an unchanged broken file is
-- not re-read on every sync, cover request or detail view. One row per
-- (book, flow); a row is superseded when the file's fingerprint or the reader
-- version changes. Rows follow their book: deleting a book (or purging its
-- library) removes them.
CREATE TABLE book_read_failures
(
    book_id        INTEGER NOT NULL,
    flow           TEXT    NOT NULL CHECK (flow IN ('metadata', 'cover')),
    reason         TEXT    NOT NULL CHECK (reason IN ('corrupt', 'missing_entry', 'too_large',
    'crashed')),
    detail         TEXT    NOT NULL DEFAULT '',
    fingerprint    TEXT    NOT NULL,
    reader_version INTEGER NOT NULL,
    recorded_at    INTEGER NOT NULL,
    PRIMARY KEY (book_id, flow),
    FOREIGN KEY (book_id) REFERENCES books (id) ON DELETE CASCADE
) WITHOUT ROWID;
CREATE INDEX idx_book_read_failures_flow_version
    ON book_read_failures (flow, reader_version, book_id);

-- +goose Down
DROP TABLE book_read_failures;

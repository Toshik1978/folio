-- +goose Up
-- Re-key books_fts by rowid.
--
-- FTS5 can seek only by rowid or MATCH: a predicate on an ordinary column has no
-- index to use, not even an UNINDEXED one. The previous "WHERE book_id = ?" form
-- therefore scanned the whole index for every single-row update and every fire of
-- the books_ad delete trigger, so the cost of touching one book grew with the size
-- of the catalog. Measured on a 573k-book library: 281ms per FTS update and 335ms
-- per book delete, against 0.23ms and 0.37ms once keyed by rowid. A sync that
-- pruned ~2000 books spent 11 of its 31 minutes inside that trigger alone.
--
-- Dropping the book_id column entirely (rather than keeping it alongside rowid)
-- also retires the TEXT-vs-INTEGER affinity coercion the old trigger relied on,
-- and lets the full-text join in booksfilter.go drop its per-row CAST.
--
-- The rebuild re-tokenizes every row: about 9s for 573k books, once, during the
-- first startup that carries this migration.

DROP TRIGGER books_ad;

CREATE VIRTUAL TABLE books_fts_new USING fts5
(
    title,
    authors,
    series,
    annotation,
    tokenize = 'unicode61 remove_diacritics 1'
);

INSERT INTO books_fts_new (rowid, title, authors, series, annotation)
SELECT CAST(book_id AS INTEGER), title, authors, series, annotation
FROM books_fts;

DROP TABLE books_fts;

ALTER TABLE books_fts_new RENAME TO books_fts;

-- +goose StatementBegin
CREATE TRIGGER books_ad
    AFTER DELETE
    ON books
BEGIN
    DELETE FROM books_fts WHERE rowid = old.id;
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER books_ad;

CREATE VIRTUAL TABLE books_fts_old USING fts5
(
    book_id UNINDEXED,
    title,
    authors,
    series,
    annotation,
    tokenize = 'unicode61 remove_diacritics 1'
);

-- book_id goes back to TEXT (CAST via printf) because the restored trigger below
-- matches it against an INTEGER old.id and depends on that affinity coercion.
INSERT INTO books_fts_old (rowid, book_id, title, authors, series, annotation)
SELECT rowid, printf('%d', rowid), title, authors, series, annotation
FROM books_fts;

DROP TABLE books_fts;

ALTER TABLE books_fts_old RENAME TO books_fts;

-- +goose StatementBegin
CREATE TRIGGER books_ad
    AFTER DELETE
    ON books
BEGIN
    DELETE FROM books_fts WHERE book_id = old.id;
END;
-- +goose StatementEnd

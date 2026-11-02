-- +goose Up
-- Make books_fts a contentless-delete FTS5 table.
--
-- A regular FTS5 table stores a second copy of every column in its shadow
-- books_fts_content table. The text is already in books / authors / series, so
-- keeping it in the index doubles the footprint for nothing: queries only need
-- the rowid (= books.id) that MATCH returns. content='' with contentless_delete=1
-- keeps the inverted index and drops the stored text, while still allowing
-- DELETE by rowid (the books_ad trigger) and full-row UPDATE.
--
-- Write rules this imposes: a partial-column UPDATE is rejected by SQLite
-- ("cannot UPDATE a subset of columns on fts5 contentless-delete table"), so the
-- row must be rewritten whole. Column values cannot be read back from the table.
--
-- books_fts_vocab is an fts5vocab 'row' view over the index; it exposes the
-- document frequency of each term (used for "distinctive terms").
--
-- The up leg copies from the old table while it still stores its content, and
-- re-tokenizes every row once, during the first startup that carries it.

DROP TRIGGER books_ad;

CREATE VIRTUAL TABLE books_fts_new USING fts5
(
    title,
    authors,
    series,
    annotation,
    content = '',
    contentless_delete = 1,
    tokenize = 'unicode61 remove_diacritics 1'
);

INSERT INTO books_fts_new (rowid, title, authors, series, annotation)
SELECT rowid, title, authors, series, annotation
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

CREATE VIRTUAL TABLE books_fts_vocab USING fts5vocab (books_fts, 'row');

-- +goose Down
-- Lossy: the stored text is gone from the contentless index, so the rows are
-- rebuilt from books and its joins. The annotation is copied raw (with its
-- markup), not stripped as the application indexes it; the next refresh or
-- re-index of a book restores the stripped text. Authors follow book_authors
-- link (rowid) order, joined with a space.
DROP TABLE books_fts_vocab;

DROP TRIGGER books_ad;

CREATE VIRTUAL TABLE books_fts_old USING fts5
(
    title,
    authors,
    series,
    annotation,
    tokenize = 'unicode61 remove_diacritics 1'
);

INSERT INTO books_fts_old (rowid, title, authors, series, annotation)
SELECT b.id,
       b.title,
       COALESCE((SELECT group_concat(name, ' ')
                 FROM (SELECT a.name
                       FROM book_authors ba
                                JOIN authors a ON a.id = ba.author_id
                       WHERE ba.book_id = b.id
                       ORDER BY ba.rowid)), ''),
       COALESCE(s.name, ''),
       COALESCE(b.annotation, '')
FROM books b
         LEFT JOIN series s ON s.id = b.series_id;

DROP TABLE books_fts;

ALTER TABLE books_fts_old RENAME TO books_fts;

-- +goose StatementBegin
CREATE TRIGGER books_ad
    AFTER DELETE
    ON books
BEGIN
    DELETE FROM books_fts WHERE rowid = old.id;
END;
-- +goose StatementEnd

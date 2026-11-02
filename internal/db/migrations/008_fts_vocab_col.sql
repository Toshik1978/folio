-- +goose Up
-- Replace the books_fts_vocab 'row' view with an fts5vocab 'col' view.
--
-- "Similar books" matches distinctive words of a book's description against
-- other books' descriptions only, so their document frequency must count only
-- the annotation column: a word common in titles but rare in descriptions
-- (a city, a character name) must keep its weight. The 'row' view counts a
-- book once for a match in any column; the 'col' view has one row per
-- (term, column), so the annotation document frequency is the doc value where
-- col = 'annotation'. Both are views over the existing index: no data is
-- copied or rebuilt.
--
-- IF EXISTS only for sqlc: it skips CREATE VIRTUAL TABLE, so it does not know
-- books_fts_vocab and rejects a plain DROP. The view always exists here.
DROP TABLE IF EXISTS books_fts_vocab;
CREATE VIRTUAL TABLE books_fts_vocab_col USING fts5vocab (books_fts, 'col');

-- +goose Down
DROP TABLE books_fts_vocab_col;
CREATE VIRTUAL TABLE books_fts_vocab USING fts5vocab (books_fts, 'row');

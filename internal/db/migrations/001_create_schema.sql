-- +goose Up
CREATE TABLE libraries
(
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    name                  TEXT    NOT NULL,
    type                  TEXT    NOT NULL,
    path                  TEXT    NOT NULL UNIQUE,
    sync_interval_seconds INTEGER NOT NULL DEFAULT 3600,
    status                TEXT    NOT NULL DEFAULT 'active',
    purge_at              INTEGER,
    last_sync_at          INTEGER,
    last_sync_error       TEXT,
    checkpoint            TEXT,
    created_at            INTEGER NOT NULL
);

CREATE TABLE settings
(
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE series
(
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    name      TEXT NOT NULL,
    -- Uppercase, Unicode case-folded form of name (see ingest.fold). The UNIQUE
    -- constraint dedupes case variants across records ("Foo"/"foo", Cyrillic
    -- included), and browse sort/seek run on it so ordering is case-insensitive.
    -- Uppercase to match the uppercase alphabet ranges in api/letters.go.
    name_fold TEXT NOT NULL UNIQUE
);

CREATE TABLE books
(
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    library_id         INTEGER NOT NULL,
    library_key        TEXT    NOT NULL,
    title              TEXT    NOT NULL,
    series_id          INTEGER,
    series_number      REAL,
    language           TEXT    NOT NULL DEFAULT 'en',
    annotation         TEXT,
    -- 1 once the lazy local-file metadata backfill has run for this book (whether
    -- or not it found anything), so a book sync didn't fully populate isn't
    -- re-parsed on every detail view. Sync writes metadata directly and never
    -- consults this flag.
    metadata_checked   INTEGER NOT NULL DEFAULT 0,
    -- 1 once online (Google Books) enrichment has been attempted for this book,
    -- set even on a no-match so the API isn't re-queried on every view.
    enrichment_checked INTEGER NOT NULL DEFAULT 0,
    publisher          TEXT,
    -- Uppercase Unicode case-fold of publisher (see series.name_fold), written
    -- by the application (db.FoldNull) on every books write so the publisher
    -- browse can range-seek an index instead of scanning books through a
    -- custom fold() SQL function (which cannot be indexed).
    publisher_fold     TEXT,
    year               INTEGER,
    rating             INTEGER, -- 1..5 stars, NULL = unrated
    content_hash       TEXT    NOT NULL,
    metadata_format    TEXT,
    added_at           INTEGER NOT NULL,
    imported_at        INTEGER NOT NULL,
    -- 1 once the user corrected this book via Fix Match. The sync merge then
    -- gap-fills missing fields but never overwrites: manual data outranks any
    -- source edition (see ingest.mergedBook).
    manually_matched   INTEGER NOT NULL DEFAULT 0,
    -- ingest.filePriority of the format whose cover is currently cached on
    -- disk; 0 = none/unknown. Persisted so a partial re-sync (e.g. only the
    -- PDF changed) can never downgrade a richer edition's cover across runs.
    cover_prio         INTEGER NOT NULL DEFAULT 0,
    UNIQUE (library_id, library_key),
    FOREIGN KEY (library_id) REFERENCES libraries (id) ON DELETE CASCADE,
    FOREIGN KEY (series_id) REFERENCES series (id) ON DELETE SET NULL
);

CREATE TABLE book_files
(
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    book_id     INTEGER NOT NULL,
    file_format TEXT    NOT NULL,
    file_size   INTEGER NOT NULL,
    source_path TEXT    NOT NULL,
    pages       INTEGER,
    mtime       INTEGER NOT NULL DEFAULT 0, -- file mod time (unix); folder diff signal
    UNIQUE (book_id, source_path),
    FOREIGN KEY (book_id) REFERENCES books (id) ON DELETE CASCADE
);

CREATE TABLE book_identifiers
(
    book_id INTEGER NOT NULL,
    type    TEXT    NOT NULL, -- 'isbn', 'amazon', 'goodreads', ...
    value   TEXT    NOT NULL,
    PRIMARY KEY (book_id, type),
    FOREIGN KEY (book_id) REFERENCES books (id) ON DELETE CASCADE
);

CREATE TABLE authors
(
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    name      TEXT NOT NULL,
    name_fold TEXT NOT NULL UNIQUE -- see series.name_fold
);

CREATE TABLE book_authors
(
    book_id   INTEGER NOT NULL,
    author_id INTEGER NOT NULL,
    PRIMARY KEY (book_id, author_id),
    FOREIGN KEY (book_id) REFERENCES books (id) ON DELETE CASCADE,
    FOREIGN KEY (author_id) REFERENCES authors (id) ON DELETE CASCADE
);

CREATE TABLE genres
(
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    name      TEXT NOT NULL,
    name_fold TEXT NOT NULL UNIQUE -- see series.name_fold
);

CREATE TABLE book_genres
(
    book_id  INTEGER NOT NULL,
    genre_id INTEGER NOT NULL,
    PRIMARY KEY (book_id, genre_id),
    FOREIGN KEY (book_id) REFERENCES books (id) ON DELETE CASCADE,
    FOREIGN KEY (genre_id) REFERENCES genres (id) ON DELETE CASCADE
);

CREATE INDEX idx_books_library ON books (library_id);
CREATE INDEX idx_books_publisher_fold ON books (publisher_fold);
CREATE INDEX idx_books_series ON books (series_id);
CREATE INDEX idx_books_added_at_id ON books (added_at DESC, id DESC);
CREATE INDEX idx_books_library_added_at_id ON books (library_id, added_at DESC, id DESC);
CREATE INDEX idx_book_files_book ON book_files (book_id);
CREATE INDEX idx_book_authors_author ON book_authors (author_id);
CREATE INDEX idx_book_genres_genre ON book_genres (genre_id);
CREATE INDEX idx_book_identifiers_book ON book_identifiers (book_id);
CREATE INDEX idx_books_imported_at ON books (imported_at DESC, added_at DESC, id DESC);
CREATE INDEX idx_book_files_format ON book_files (file_format, book_id);
CREATE INDEX idx_books_language ON books (language);

-- +goose Down
DROP INDEX IF EXISTS idx_books_language;
DROP INDEX IF EXISTS idx_book_files_format;
DROP INDEX IF EXISTS idx_books_imported_at;
DROP INDEX IF EXISTS idx_book_identifiers_book;
DROP INDEX IF EXISTS idx_book_genres_genre;
DROP INDEX IF EXISTS idx_book_authors_author;
DROP INDEX IF EXISTS idx_book_files_book;
DROP INDEX IF EXISTS idx_books_library_added_at_id;
DROP INDEX IF EXISTS idx_books_added_at_id;
DROP INDEX IF EXISTS idx_books_series;
DROP INDEX IF EXISTS idx_books_publisher_fold;
DROP INDEX IF EXISTS idx_books_library;
DROP TABLE IF EXISTS book_identifiers;
DROP TABLE IF EXISTS book_genres;
DROP TABLE IF EXISTS genres;
DROP TABLE IF EXISTS book_authors;
DROP TABLE IF EXISTS authors;
DROP TABLE IF EXISTS book_files;
DROP TABLE IF EXISTS books;
DROP TABLE IF EXISTS series;
DROP TABLE IF EXISTS settings;
DROP TABLE IF EXISTS libraries;

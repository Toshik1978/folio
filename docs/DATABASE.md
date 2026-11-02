# Database & Ingestion

> SQLite schema, FTS5 search, sync engine, and ingestion protocols.
>
> **Status:** Implemented. Books use a logical `book` ↔ `book_file` model with
> stable per-library identity and reconciliation-based sync (see below).

---

## SQLite Stack

| Concern | Choice | Rationale |
| :--- | :--- | :--- |
| **Driver** | `modernc.org/sqlite` | Pure-Go translation of SQLite C. No CGo required (`CGO_ENABLED=0` compatible). Full FTS5 support. Battle-tested, widely adopted. |
| **Migrations** | `pressly/goose` | Simple versioned SQL files. Works with any `database/sql` driver. No codegen magic. |
| **DB codegen** | `sqlc` | Compiles raw SQL queries into type-safe Go code. |
| **Dynamic queries** | `stephenafamo/bob` | Builder-only (no codegen) for the one dynamic query sqlc can't express — the book filter. |

### Why Pure-Go?

The project compiles with `CGO_ENABLED=0` for static linking in minimal Docker containers like Alpine/Distroless. This rules out `mattn/go-sqlite3` (CGo-based). `modernc.org/sqlite` is a mechanical translation of the C SQLite source into Go, preserving full compatibility including FTS5, triggers, and WAL mode.

### Driver Registration

`modernc.org/sqlite` registers as `"sqlite"` with `database/sql`:

```go
import (
    "database/sql"
    _ "modernc.org/sqlite"
)

db, err := sql.Open("sqlite", "/data/folio.db")
```

### Migration Layout

Goose expects numbered SQL files in a migrations directory:

```
internal/db/migrations/
├── 001_create_schema.sql              # libraries, books, book_files, authors, series, genres, … + indexes
├── 002_create_fts5.sql                # books_fts virtual table + delete trigger
├── 003_book_identifiers_lookup.sql    # idx_book_identifiers_lookup on book_identifiers(type, value) — accelerates identifier-based lookups (ISBN / Amazon / Goodreads) used by metasearch enrichment
├── 004_book_cover_state.sql           # cover state columns on books
├── 005_fts_rowid_key.sql              # books_fts keyed by rowid (= books.id) instead of a book_id column
├── 006_fts_contentless.sql            # books_fts becomes contentless-delete; adds the books_fts_vocab view
└── 007_book_read_failures.sql         # book_read_failures: remembered unreadable sources, per book and flow
```

The migration files are embedded into the binary (`go:embed`) and run
automatically at startup via goose against the `database/sql` handle.

---

## Database: SQLite 3 + FTS5

The application uses an internal SQLite database as an abstraction layer over heterogeneous book sources. The database is the only writable data store — all source files and external databases are read-only.

### Relational Schema

```sql
CREATE TABLE libraries (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,                -- user-facing display name (shown in the header selector)
    type TEXT NOT NULL,                -- 'calibre' | 'inpx' | 'folder'
    path TEXT NOT NULL UNIQUE,         -- filesystem path to source
    sync_interval_seconds INTEGER NOT NULL DEFAULT 3600,
    status TEXT NOT NULL DEFAULT 'active', -- 'active' | 'syncing' | 'pending_purge' | 'error';
                                       -- a successful (or skipped-unchanged) sync resets
                                       -- 'error' back to 'active'
    purge_at INTEGER,                  -- unix timestamp; NULL if not pending purge
    last_sync_at INTEGER,
    last_sync_error TEXT,
    checkpoint TEXT,                   -- last-seen source artifact fingerprint (mtime:size); gates re-reads
    created_at INTEGER NOT NULL
);

CREATE TABLE settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- A logical book: one row per work per library, identified by a stable
-- (library_id, library_key). Its downloadable files live in book_files.
CREATE TABLE books (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    library_id INTEGER NOT NULL,
    library_key TEXT NOT NULL,    -- stable per-library identity + grouping key
                                  -- (Calibre book id; else norm(title)+authors+language)
    title TEXT NOT NULL,
    series_id INTEGER,
    series_number REAL,
    language TEXT NOT NULL DEFAULT 'en', -- 'und' (ISO 639-2 "undetermined") is the
                                  -- sentinel for a book whose language could not be
                                  -- parsed; the sync merge gap-fills it from any
                                  -- edition while the stored value is still 'und'
                                  -- (and never overwrites a real language code).
                                  -- The column DEFAULT 'en' is dead code — the app
                                  -- always supplies an explicit value. Existing
                                  -- catalogs imported before this sentinel was
                                  -- introduced need a fresh re-import to populate
                                  -- 'und' for their unknown-language books.
    annotation TEXT,
    metadata_checked INTEGER NOT NULL DEFAULT 0, -- 1 once the lazy local-file
                                  -- backfill ran (found data or not), so a source
                                  -- sync didn't fully populate isn't re-parsed on
                                  -- every view. Sync writes metadata directly,
                                  -- ignoring this flag.
    enrichment_checked INTEGER NOT NULL DEFAULT 0, -- 1 once online (Google Books)
                                  -- enrichment was attempted, set even on a
                                  -- no-match so the API isn't re-queried per view.
    publisher TEXT,               -- nullable; parsed from the source where available.
                                  -- Not normalized into a lookup table like authors;
                                  -- grouping stays exact on the raw value.
    publisher_fold TEXT,          -- uppercase Unicode case-fold of publisher, written by
                                  -- the app (db.FoldNull) on every books write; backs
                                  -- idx_books_publisher_fold so the publisher browse
                                  -- range-seeks an index instead of scanning books.
    year INTEGER,                 -- publication year, nullable
    rating INTEGER,               -- 1..5 stars, nullable (NULL = unrated)
    content_hash TEXT NOT NULL,   -- fingerprint of the merged metadata (see contentHash)
    metadata_format TEXT,         -- format that currently owns the scalar metadata
                                  -- (drives the format-priority merge on re-sync)
    added_at INTEGER NOT NULL,    -- source add-date when known (Calibre books.timestamp,
                                  -- INPX date), else the first sync time; set on insert only.
                                  -- Backs the sort=source ("Newest") order.
    imported_at INTEGER NOT NULL, -- when the book entered Folio. Stamped from a single
                                  -- per-sync-run timestamp (driver.go captures time.Now()
                                  -- once; every book inserted in that run shares it), set
                                  -- on insert only. Backs the default "Recently added"
                                  -- browse sort (idx_books_imported_at) so a freshly
                                  -- imported batch floats above the existing catalog, with
                                  -- added_at/id as tiebreakers (see Sort model below).
    manually_matched INTEGER NOT NULL DEFAULT 0, -- 1 once the user corrected this
                                  -- book via Fix Match; sync then gap-fills but never
                                  -- overwrites (manual data outranks any source edition)
    cover_prio INTEGER NOT NULL DEFAULT 0, -- format rank of the format whose cover is
                                  -- cached on disk; 1 = extracted by the lazy flow,
                                  -- 1000 = manual, 0 = none or online enrichment.
                                  -- Persisted so a partial re-sync can't downgrade a
                                  -- richer edition's cover
    UNIQUE (library_id, library_key),
    FOREIGN KEY (library_id) REFERENCES libraries(id) ON DELETE CASCADE,
    FOREIGN KEY (series_id) REFERENCES series(id) ON DELETE SET NULL
);

-- One physical file (format) of a book. A book may have several.
CREATE TABLE book_files (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    book_id INTEGER NOT NULL,
    file_format TEXT NOT NULL,    -- 'epub', 'fb2', 'mobi', etc.
    file_size INTEGER NOT NULL,
    source_path TEXT NOT NULL,    -- relative path within source (or "{archive}.zip/{inner}")
    pages INTEGER,                -- page count (PDF), nullable
    mtime INTEGER NOT NULL DEFAULT 0, -- file mod time (unix); folder diff signal, 0 for calibre/inpx
    UNIQUE (book_id, source_path),
    FOREIGN KEY (book_id) REFERENCES books(id) ON DELETE CASCADE
);

CREATE TABLE book_identifiers (
    book_id INTEGER NOT NULL,
    type TEXT NOT NULL,           -- 'isbn', 'amazon', 'goodreads', ...
    value TEXT NOT NULL,
    PRIMARY KEY (book_id, type),
    FOREIGN KEY (book_id) REFERENCES books(id) ON DELETE CASCADE
);

CREATE TABLE authors (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,           -- display form (first writer wins)
    name_fold TEXT NOT NULL UNIQUE -- uppercase Unicode case-fold (db.Fold); dedup + sort key
);

CREATE TABLE book_authors (
    book_id INTEGER NOT NULL,
    author_id INTEGER NOT NULL,
    PRIMARY KEY (book_id, author_id),
    FOREIGN KEY (book_id) REFERENCES books(id) ON DELETE CASCADE,
    FOREIGN KEY (author_id) REFERENCES authors(id) ON DELETE CASCADE
);

CREATE TABLE series (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    name_fold TEXT NOT NULL UNIQUE -- see authors.name_fold
);

CREATE TABLE genres (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    name_fold TEXT NOT NULL UNIQUE -- see authors.name_fold
);

CREATE TABLE book_genres (
    book_id INTEGER NOT NULL,
    genre_id INTEGER NOT NULL,
    PRIMARY KEY (book_id, genre_id),
    FOREIGN KEY (book_id) REFERENCES books(id) ON DELETE CASCADE,
    FOREIGN KEY (genre_id) REFERENCES genres(id) ON DELETE CASCADE
);

-- Performance Optimization Indexes
CREATE INDEX idx_books_library ON books(library_id);
CREATE INDEX idx_books_publisher_fold ON books(publisher_fold); -- publisher browse range-seek
CREATE INDEX idx_books_series ON books(series_id);
CREATE INDEX idx_books_added_at_id ON books (added_at DESC, id DESC);            -- sort=source ("Newest")
CREATE INDEX idx_books_library_added_at_id ON books (library_id, added_at DESC, id DESC);
CREATE INDEX idx_book_files_book ON book_files(book_id);
CREATE INDEX idx_book_authors_author ON book_authors(author_id);
CREATE INDEX idx_book_genres_genre ON book_genres(genre_id);
CREATE INDEX idx_book_identifiers_book ON book_identifiers (book_id);
CREATE INDEX idx_books_imported_at ON books (imported_at DESC, added_at DESC, id DESC); -- default "Recently added" browse sort
CREATE INDEX idx_book_files_format ON book_files (file_format, book_id);         -- covering: per-format facet / stats group-by
CREATE INDEX idx_books_language ON books (language);                            -- language facet / stats
```

### Read Failures

`book_read_failures` (migration `007`) remembers books whose source file could
not be read, so an unchanged broken file is not re-opened on every sync, cover
request or detail view. It holds at most one row per `(book_id, flow)`.

```sql
CREATE TABLE book_read_failures
(
    book_id        INTEGER NOT NULL,
    flow           TEXT    NOT NULL CHECK (flow IN ('metadata', 'cover')),
    reason         TEXT    NOT NULL CHECK (reason IN ('corrupt', 'missing_entry', 'too_large',
    'crashed')),
    detail         TEXT    NOT NULL DEFAULT '',  -- short human-readable cause
    fingerprint    TEXT    NOT NULL,             -- identity of the source file when it failed
    reader_version INTEGER NOT NULL,             -- reader generation that failed
    recorded_at    INTEGER NOT NULL,             -- unix seconds
    PRIMARY KEY (book_id, flow),
    FOREIGN KEY (book_id) REFERENCES books (id) ON DELETE CASCADE
) WITHOUT ROWID;
CREATE INDEX idx_book_read_failures_flow_version
    ON book_read_failures (flow, reader_version, book_id);
```

- **Upsert.** `UpsertReadFailure` replaces the row for `(book_id, flow)`, so a
  repeat failure updates reason, detail, fingerprint, reader version and time.
- **Lifecycle.** `DeleteReadFailure` (one flow) and `DeleteReadFailuresForBook`
  (both) clear rows; `GetReadFailure` looks one up. `detail` is capped at 256
  bytes and is never sent to clients; it is for logs and diagnosis only.
  Deleting a book, or purging its library, removes its rows through
  `ON DELETE CASCADE`. A sync that sees a file of an existing book new or
  changed (size or mod time) also clears both rows, in the import transaction
  (`importer.add`); an unchanged re-sync keeps them. A brand-new book has none.
- **Post-sync re-check.** Sync's size/mtime signal can miss a changed file: an
  archive entry replaced under an unchanged `.inpx` keeps the indexed size and
  date. So after every sync run commits, still under the sync's write guard,
  `recheckReadFailures` (`internal/ingest/recheck.go`, called from
  `runReconcile`) loads the library's `metadata` rows of the current
  `ebook.ReaderVersion` with their books' files (`ListMetadataFailureFiles`),
  recomputes each book's fingerprint exactly as the read gate does (ranked
  metadata candidates; stat or zip central directory, nothing inflated or
  parsed) and deletes the rows whose fingerprint differs. A row whose
  fingerprint cannot be computed (a stat or open failure, e.g. the library is
  unreachable) is left in place: nothing is deleted on uncertainty. Cover rows
  and rows of other reader versions are not touched. The re-check is
  best-effort: a failure is logged and the sync result stands. A scheduled
  sync skipped by an unchanged checkpoint re-checks too: the engine calls the
  parser's `RecheckReadFailures` (the `sync.ReadFailureRechecker` capability
  of the INPX and Calibre parsers) under the same write guard. Only books with
  a record are looked at, so a library without unreadable books pays one
  empty query. When records are lifted the engine invalidates the stats cache
  and emits a `library` event, since the unreadable count changed; otherwise
  the skip stays silent.
- **Read gate.** Every local read of a flow goes through the Extractor's
  `readBook`, which returns an `ingest.ReadResult` (done, broken or retryable,
  with the reason, a 256-byte detail and the fingerprint). It first loads the
  `(book_id, flow)` row; when `reader_version` equals `ebook.ReaderVersion` and
  the fingerprint recomputed from the flow's candidate files (in rank order:
  `source_path|size|mtime` from `os.Stat`; for an archive entry the zip
  header's size and mtime plus the archive's own `os.Stat`, so a damaged
  archive replaced by a good copy of the same release re-checks its books; the
  archive's `os.Stat` alone when the entry is gone or the archive container is
  corrupt) is unchanged, the read is broken and `Known`
  and no file is opened. Otherwise every candidate is read and classified: a
  parser content error, a corrupt archive container or a missing archive entry
  is broken; open/stat failures, source I/O errors and cancellation are
  retryable. A book with no candidate file (e.g. PDF-only, for descriptions) is
  done without a lookup. Recording and clearing rows from these results is the
  caller's job: the metadata flow is written by the on-view fill and the Deep
  Index, the cover flow by `Extractor.Cover`.
- **Cover flow.** `Extractor.Cover` reads the cover through the same gate. A
  done read deletes the book's stale `cover` row (only when the gate found one,
  so a normal first view never takes the write guard); a newly broken read upserts it
  (logged once); a broken read, new or known, returns an error wrapping
  `ebook.ErrMalformed`, and a known one opens no file. A retryable read records
  nothing. Both writes are best-effort under the write guard (a busy guard skips
  them). The rule: a cover payload that cannot be extracted (bad encoding, an
  unreadable or oversized cover entry) is **broken**; a payload that is
  extracted but is not an image is **none** — the lazy cover flow's
  `convertToJPEG` fails and marks `cover_state = 2`, and no row is written.
  On a broken cover the lazy flow serves the placeholder and leaves
  `cover_state` at 0, so a later view consults the row rather than the file
  until the file's fingerprint or the reader version changes. Cover rows are
  never counted as unreadable books; only metadata-flow rows are.

### Full-Text Search (FTS5)

```sql
CREATE VIRTUAL TABLE books_fts USING fts5(
    title,
    authors,
    series,
    annotation,
    content = '',
    contentless_delete = 1,
    tokenize = 'unicode61 remove_diacritics 1'
);

CREATE VIRTUAL TABLE books_fts_vocab USING fts5vocab (books_fts, 'row');
```

- **Keyed by rowid.** An index row's `rowid` is `books.id`; there is no `book_id`
  column. FTS5 can seek only by rowid or `MATCH`, so every write goes by rowid.
- **Contentless (migration 006).** `content=''` keeps the inverted index but not a
  second copy of the text, which already lives in `books` / `authors` / `series`.
  `contentless_delete=1` still allows `DELETE` by rowid and full-row `UPDATE`.
  Consequences: a partial-column `UPDATE` is rejected by SQLite and column values
  cannot be read back, so a row must always be rewritten whole.
- **`RefreshBookFTS`** (`internal/db/fts.go`) is the single way to change an
  existing row. It re-reads title, authors (in `book_authors` rowid order),
  series and annotation (markup stripped) from the relational tables, then does a
  full-row `UPDATE` if the rowid exists or an `INSERT` if not. Callers change
  `books` / joins first and call it inside the same transaction. `InsertBookFTS`
  writes the row for a new book. Both are built with `bob` rather than sqlc
  (sqlc cannot parse a table with a `rowid` column).
- **`books_fts_vocab`** is an `fts5vocab` `row` view over the index; it exposes
  the document frequency of each term.
- `unicode61 remove_diacritics 1` normalizes accented characters for multilingual search.
- The up leg of 006 re-tokenizes every row once, on the first startup that carries it.
  The down leg is lossy: it rebuilds rows from `books` with the raw (unstripped)
  annotation until the next refresh.

### Sync Trigger

```sql
CREATE TRIGGER books_ad AFTER DELETE ON books BEGIN
    DELETE FROM books_fts WHERE rowid = old.id;
END;
```

Insertions and full metadata population (including annotations) are written in
batched transactions: each batch commits atomically, and a sync **run** is not a
single transaction. A mid-run failure leaves already-committed batches in place,
but reconciliation is idempotent — the next sync self-heals — so runs are
effectively resumable.

---

## Ingestion Sources

| Source | Detection | Diff Strategy |
| :--- | :--- | :--- |
| **Calibre Library** | Checkpoint = `mtime:size` of `metadata.db` | Read-only connection to Calibre SQLite; reconcile by Calibre book id |
| **INPX Inventories** | Checkpoint = `mtime:size` of `.inpx` bundle | Parse index → reconcile by `groupKey`; no pre-emptive unzip |
| **Arbitrary Directory** | `fsnotify` + cron/ticker | Fast comparison (path + size + mtime, stored on `book_files`); parse metadata only for new/changed files |

Beyond the core fields, **Calibre** ingestion also reads ratings
(`books_ratings_link` → `ratings`, mapped from Calibre's 0–10 scale to 1–5
stars), the per-book page count from the dynamic `pages` custom column
(`custom_columns` → `custom_column_<id>`), and `books.timestamp` (→ `added_at`).
**INPX** reads its per-line date field (→ `added_at`). When a source supplies an
add-date it is preferred over the sync-run time, preserving the library's
original chronology; `added_at` is written on insert only.

#### Sort model (`added_at` vs `imported_at`)

Two insert-only timestamps drive the non-search browse orders (see
`db.FilterBooks` and [API.md](./API.md#query-parameters-for-apibooks)):

- **`added_at`** — the *source* chronology (Calibre/INPX date, else first sync
  time). Backs the **`sort=source` ("Newest")** order.
- **`imported_at`** — when the book entered Folio, stamped from a **single
  per-sync-run timestamp** (`driver.go` captures `time.Now()` once per run; every
  book inserted in that run shares it). Backs the **default "Recently added"**
  order, so a newly imported batch floats above the existing catalog, then sorts
  by source date within the batch. Because the tiebreakers are `added_at` then
  `id`, the relative order of pre-existing rows is unchanged.

> **Accepted limitation (INPX dates):** INP exporters disagree on the date
> column order — Y-M-D vs Y-D-M. `parseINPXDate` (`internal/ingest/inpx.go`)
> tries Y-M-D first; a value with day > 12 (e.g. `2021-31-05`) unambiguously
> falls through to Y-D-M, but a value where both fields are ≤ 12 (e.g.
> `2021-05-06`) is inherently ambiguous and parses as Y-M-D. For a Y-D-M source
> this can silently skew `added_at` (and thus the `sort=source` "Newest"
> ordering) — a known, unresolvable property of the format; no error is raised.

Some Calibre fields are **intentionally not imported**: the `read` / `read_status`
custom columns (too personal-library-specific), and `books.sort` /
`books.author_sort` / `books.uuid` (Folio derives its own ordering via `db.Fold`
and its own identity via `library_key`, so Calibre's sort keys and UUID are
redundant). Multi-language books take only their **first** language
(`books_languages_link` `LIMIT 1`); a different language is otherwise treated as a
different book (see [Stable identity](#stable-identity-library_key)).

---

## Sync Engine

### Architecture

```
[ fsnotify Event ] ---+
                      +---> [ Go Channel (cap: 1) ] ---> [ Sequential Worker Goroutine ]
[ Cron / Ticker ]  ---+
                      |
[ UI POST /sync ]  ---+
```

### Design Constraints

1. **Single writer** — The work-queue channel has capacity `1`, so only one indexing
   routine runs at a time. Process-wide, *every* writer serializes on one shared
   `db.WriteGuard`: a sync, a library **teardown** (the deadline-driven purge sweep and
   the on-demand "Purge Now"), and the API write handlers (manual edit, Fix Match,
   cover upload, library CRUD). So a cascade delete or an API edit never races an
   in-flight sync transaction even though it is invoked off the worker goroutine.
   Readers do **not** take the guard — WAL lets them run concurrently with the one
   writer. Acquisition is **context-aware** (`Lock(ctx) error`): the sync and purge
   pass their lifecycle context and hold the guard for the whole run (releasing only on
   completion or shutdown), while an API write waits at most a short budget (≈2s) and
   then returns `503` ("indexing in progress; retry shortly") rather than blocking past
   the server's 60s `WriteTimeout`. Best-effort write-on-read paths (lazy metadata
   backfill, enrichment mark-checked) skip the write and retry on a later view.
2. **Debouncing** — `fsnotify` file-write events start a 10-second cooldown timer. Subsequent mutations reset the timer. This avoids parsing partially transferred files.
3. **Checkpoint gating** — Before reading a source the engine compares the parser's current artifact fingerprint (`Checkpointer.Checkpoint`) with the stored `libraries.checkpoint`; an unchanged Calibre/INPX source is skipped entirely. The fingerprint is computed once, *before* the read, and that pre-read value is what gets stored on success — so an artifact modified mid-sync mismatches on the next pass and is re-read instead of skipped. Folder is event-driven (fsnotify) and has no single artifact, so it always walks. A manual "Sync Now" bypasses the gate.
4. **Reconciliation (no wipe-and-reinsert)** — Each run upserts books by `(library_id, library_key)` and diffs their files by `source_path`+size, writing only real changes, then prunes files that vanished and books left with no files. Because IDs are not regenerated, `/books/{id}` links and cached covers survive re-syncs. Metadata a grouped book lacks (annotation, series, publisher, year, rating) is gap-filled from whichever edition carries it.
5. **Atomic purge teardown** — `purgeLibrary` evicts each book's cached cover
   first, **best-effort** (a stuck cover file must not block reclaiming the rows;
   orphaned cover files are harmless — book IDs never reuse). It then deletes the
   library's books and the library row itself in **one transaction**, so a crash
   mid-teardown leaves either nothing done or fully done — never an empty-but-present
   library row. The books are deleted explicitly (not via `ON DELETE CASCADE`) so
   the `books_ad` FTS-cleanup trigger fires per row. Purge is idempotent: a second
   call on an already-deleted library is a clean no-op, and the minute-interval
   deadline sweep retries automatically if the async teardown fails.
6. **Cover Extraction & Caching** — A cover is cached keyed to the **logical** `books.id` under `/data/covers/{id/1000}/{id}.jpeg`, normalized to JPEG (`covers.Save` → `convertToJPEG`). Only a Calibre sync saves cover bytes at import (its `cover.jpg`). Every other library type, folder included, imports metadata only: the folder walk opens each new or changed file and reads its metadata, never its cover. Those covers come from the lazy cover flow: the first view of a book with `cover_state = 0` extracts the cover from its best file (preferring richer formats: epub→fb2→mobi/azw3→pdf) and records the result. Every cached cover is therefore a JPEG, so serving and the OPDS feed declare `image/jpeg` without sniffing each file (see [API.md](./API.md#cover-image-serving--security)). The priority of the format whose cover is cached is **persisted** in `books.cover_prio`, so the "prefer richer formats" rule holds *across* runs, not just within one — a partial re-sync (e.g. only the PDF changed) can never downgrade a richer edition's cover. `covers.Save` skips a write when the new bytes are byte-identical to the cached file, so the on-disk mtime — and thus the `?v=` cache-buster — only changes when the image actually changes. Lazy extraction negative-caches a *missing* cover only after a **successful** parse that found none; a parse **error** is never cached in `cover_state`, so a transient failure retries on the next view, while a broken cover is remembered in `book_read_failures` and not re-read until its file changes (see [Read Failures](#read-failures)). When the lazy flow caches a cover extracted from the book's file it records `cover_state = 1` with `cover_prio = 1` (`MarkFileCover`), marking it file-derived; an online-enrichment cover stays at `cover_prio = 0`. **Invalidation:** when a sync sees a file of an **existing** book that is new or whose size or mod time changed, and the record brings no cover bytes of its own, the importer queues an invalidation of the state that file produced: `cover_state = 2` (none), or `cover_state = 1` with `1 <= cover_prio < 1000` (a lazily extracted cover, or a cover saved at import). It is skipped when `cover_prio` outranks the file's format, or when a cover was already saved for that book in the same run. A brand-new book queues nothing: it has no cover to invalidate. After the batch commits the importer deletes the cached cover and thumbnail and runs `ResetCoverState` (`cover_state = 0`, `cover_prio = 0`), so the next view re-extracts the cover from the changed file. A rolled-back batch discards the invalidation. A file that **moves** between two existing books with unchanged bytes (an INPX index fix changed its grouping key) counts as changed on both sides: the book it joined is invalidated as above (`ensureFile` reports the new file row), and the book it left gets the same file-derived invalidation (no format-rank check, since the cover may have come from the departed file) and loses its `book_read_failures` rows. Two covers are kept: one with `cover_prio = 0` (an online cover, which nothing would fetch again, or a cover cached lazily before `cover_prio = 1` was recorded), and a manual cover (`cover_prio = 1000`), which is never touched and which `ResetCoverState` also skips.
7. **Post-sync description fill** — A sync that added files (`Result.Added > 0`) then runs the Deep Index over just the books that run inserted, in the same queue turn. It runs only when a deep indexer is wired (`WithDeepIndexer`, as `cmd/folio-idx` does); otherwise it is a no-op. It releases the whole-run write guard first, so the indexer takes the guard per batch like a Deep Index run. The scope is `ingest.DeepIndexImportedAt(stamp)`, where `stamp` is the run's `imported_at` (`Result.ImportedAt`): unchecked books stamped with that value, never the backlog. The backlog is the Deep Index button's job. A checkpoint-skipped sync and a sync with no additions run no fill. On a library's first sync every book is new, so the fill is a full Deep Index for that library in that turn. A fill failure is logged as a warning and does not mark the library errored; the books stay unchecked for a later Deep Index.

---

## Book Identity, Grouping & Metadata Merge

### Stable identity (`library_key`)

Every book has a stable within-library identity in `books.library_key`, enforced
by `UNIQUE(library_id, library_key)`:

- **Calibre** — the native Calibre book id.
- **INPX / Folder** — `groupKey(title, authors, language)`
  (`internal/ingest/groupkey.go`): normalized lowercased title + sorted authors
  + language, joined with `\x1f`. A different language is a different book.

Because formats of one work share a `library_key`, they reconcile onto **one**
logical `books` row with several `book_files`. Re-syncs upsert by this key
rather than wipe-and-reinsert, so `books.id` — and the cover cache keyed to it —
survive (see the Sync Engine's reconciliation constraint above).

### Metadata merge & gap-fill

A logical book's scalar metadata (annotation, series + number, publisher, year,
rating) and its author/genre links come from whichever edition supplies them.
Two merge modes (`mergeMode` constants `modeGapFill` / `modeOverwrite`) run
during reconciliation (`internal/ingest/merge.go`):

- **Gap-fill** (`modeGapFill`) — fill only *empty* fields from an edition that
  has them. This is how a MOBI/AZW3 with no EXTH 103 description inherits its
  EPUB sibling's `<dc:description>`, and how series/publisher/year propagate
  across formats.
- **Overwrite (format-priority)** (`modeOverwrite`) — when a **strictly
  higher-priority** edition is seen, `planOverwrite` makes it authoritative: it
  overwrites the scalar fields, re-stamps `books.metadata_format`, re-links
  authors and genres (genres normalized to the taxonomy — see
  [EBOOK-PARSING.md](./EBOOK-PARSING.md#genre-taxonomy)) from that edition, and
  refreshes the `books_fts` row so search stays consistent.

`content_hash` (`contentHash` in `groupkey.go`) fingerprints the **effective**
(post-normalization) metadata of the owning edition: title, authors, series,
language, annotation, publisher, year, **rating**, genres, and identifiers. Genres
and identifiers run through the same taxonomy/cleaning that gets persisted, so the
fingerprint matches what is stored and junk-only source edits (dropped tags,
calibre UUIDs, ISBN punctuation) don't trigger a spurious refresh. `added_at` and
files are excluded (files are diffed separately by `source_path`/size/mtime). On re-sync `planMerge` compares the
record's hash against the stored one to detect an in-place edit of the owning
edition and force a metadata refresh; `planMerge` stages the new hash and
`applyPlan` persists it, so an unchanged sync stays a no-op.

#### Format priority

The importer's injected rank (`ebook.Dispatcher.Rank`, from each parser's
`Traits().Rank`) orders formats by metadata/cover reliability — the same ranking
drives cover selection (`saveCoverIfBetter`):

```
epub (4) > fb2 (3) > mobi / azw3 / azw (2) > pdf (1)
```

This converges a grouped book to its highest-priority edition's metadata
regardless of the order files are processed.

#### Accepted limitation

An in-place edit of the owning edition **does** propagate on re-sync:
`planMerge` detects it via `content_hash` and takes the overwrite branch even
when the format priority isn't strictly greater (an epub-over-epub re-read of an
edited book now refreshes) — **unless** a same-format sibling file exists, in
which case the edited-in-place branch is suppressed. With two same-format
editions, neither hash wins deterministically, so re-applying every sync would
ping-pong the row; gap-fill still runs. The residual is *erasure* — an empty
field never clobbers a populated one (a higher-priority edition can add or
replace, not blank out), so clearing a field at the source is not reflected. For
Folder/INPX a title/author edit also changes `library_key`, surfacing as a new
book (old one pruned). Fully clearing a field still needs a force-rebuild
(`POST /api/libraries/{id}/purge`; see
[API.md](./API.md#library-management-apilibraries)).

#### Merge invariants

- **Manual matches are user-owned.** Once the user corrects a book via Fix Match
  (`POST /api/books/{id}/match`), `books.manually_matched` is set to 1.
  Thereafter `planMerge` short-circuits to gap-fill only: a later sync may fill
  fields that are still empty but **never overwrites** the manual data, even when
  a higher-priority edition or an in-place edit is seen. This is what keeps a
  manual correction from silently reverting on the next sync.
- **Language propagates on overwrite.** The edited-in-place / higher-priority
  overwrite path carries `language` alongside the other scalar fields, so a
  corrected language is no longer stranded.
- **`'und'` language is gap-filled on merge.** A book whose language could not be
  parsed at import time is stored with the `'und'` (ISO 639-2 "undetermined")
  sentinel. During reconciliation, if any edition of that book carries a real
  language code the gap-fill path upgrades `'und'` to that value; a real language
  code is never replaced by gap-fill from a sibling edition.
- **Identifier writes depend on the mode.** In overwrite mode identifiers are
  upserted (the owning edition's values win); in gap-fill mode they are
  insert-if-absent (a sibling edition can add an ISBN it knows, but never
  replaces one already present).

#### Lazy metadata tiers (on view)

Sync writes what each source cheaply offers. Two further tiers fill gaps the
first time a book's detail is viewed (`GET /api/books/{id}`), each guarded by a
`books` flag so it runs at most once. A per-book in-flight claim
(`Handler.claimLazy`) additionally keeps two *concurrent* first views from
running the tiers twice: the losing request skips them and serves the current
row, and the persisted result shows on the next view.

1. **Local backfill** (`metadata_checked`) — parse the actual book file (an INPX
   index, unlike the file it points at, carries no annotation or identifiers) and
   persist the annotation + cleaned identifiers. Implemented by `ingest.Extractor`
   (`Description`) behind the api `MetadataExtractor` interface. Formats that are
   not read for descriptions (PDF) are skipped; of a book's other files, the
   highest-ranked one that reads successfully wins (EPUB, then FB2, then the
   MOBI family). Files are read in place through `ingest`'s resolver: a plain
   file is read directly, and an INPX archive entry inflates lazily into memory
   (capped at `ebook.MaxEntryBytes`). Nothing is written to disk, and a stored `source_path`
   that escapes its library or archive directory (`../`) is rejected.

   *Outcomes of the on-view fill* (`LocalBackfiller.fill`, flow `metadata`):
   **done** persists as above, deletes the book's `metadata` read-failure row
   and sets `metadata_checked`, all inside one guarded section. **Broken, not
   known** upserts the row (reason, detail, fingerprint, `ebook.ReaderVersion`,
   now) under the write guard with `backfillAcquireBudget`, logs one
   `Warn("unreadable book file")` and leaves the book unchecked. **Broken,
   known** (the record gate matched) writes and logs nothing. **Retryable**
   logs at `Debug` only and writes nothing; a busy guard likewise skips the
   write. A recorded book is re-read only when its file fingerprint or the
   reader version changes, so repeated views of a corrupt book cost no file
   I/O.
2. **Online enrichment** (`enrichment_checked`) — when the local tiers still leave
   a gap, query Google Books by the book's ISBN (else title+author) for the
   description, publisher, year, categories (genres), identifiers, and cover.
   Gap-fill only; it never clobbers local data (genres are added only when the
   book has none), and the flag is set even on a no-match. The cover is saved
   first, *before* the write transaction opens, so the SQLite write lock
   (`_txlock=immediate` takes it at BEGIN) is never held across file I/O; the
   scalar fields, FTS, identifiers, and genre relinks then run in a single
   transaction. The Google fetch is bounded by `enrichTimeout` (5s) and the
   persist by its own detached `persistTimeout` (3s), so a slow answer or a
   client disconnect can't roll back a commit (see
   [NETWORKING.md](./NETWORKING.md#outbound-connections)). Implemented by `ingest.Enricher` behind the api
   `MetadataEnricher` interface; see [BACKEND.md](./BACKEND.md) and
   [EBOOK-PARSING.md](./EBOOK-PARSING.md#metadata-coverage-by-format).

#### Deep Index (bulk, on demand)

The lazy tiers above fill one book at a time on view. **Deep Index** does the
local-backfill tier in bulk so descriptions become full-text searchable without
opening each book. It is a sync-engine request kind (see
[SYNC-EVENTS.md](./SYNC-EVENTS.md)) triggered from Settings → Libraries, and is
implemented by `ingest.DeepIndexer` (`internal/ingest/deepindex.go`). It reads
local files only, never calls the network, and never writes to a library source.

- **Work list, in pages.** A run never holds the whole work list: on a
  580k-book INPX library one list took about 170 MB of heap before any book
  was read. `CountDeepIndexWork` gives the progress total (unchecked books with
  a file, in scope), then `ListDeepIndexWork` is read one page at a time:
  every file of the next 5,000 unchecked books (`deepIndexPageSize`) with an id
  above the previous page's largest, in id order. Paging by book keeps all of a
  book's files on one page, and since the run only moves forward, a book it
  leaves unchecked (broken or retryable) is not picked up again in the same
  run. Each page goes through the same jobs, worker pool and writer, and the
  run's result is the sum of its pages; a cancel stops before the next page.
  An archive whose books straddle two pages is opened once per page. Within a
  page, rows are ordered by `source_path` (then book id), so an archive's
  entries are contiguous. SQL ranks nothing: Go groups the rows by
  book and orders each book's files by `Dispatcher.Rank`. Each row also carries
  the book's metadata-flow record from `book_read_failures` (`LEFT JOIN` on its
  `(book_id, flow)` primary key, `flow = 'metadata'`), as the nullable
  `failure_fingerprint` and `failure_reader_version`, so the read gate gets the
  record from the work row and no book costs an extra query.
- **Reading.** Every format is read the same way, through the dispatcher: each
  book goes through the Extractor's shared `readBook` (the same read as the
  on-view fill), which opens its top-ranked describing file via the resolver
  and calls `Dispatcher.Metadata`, falling back to lower-ranked files when a
  read fails. There is no per-format special case in Deep Index. Books are grouped into jobs by `resolver.group` of their
  top-ranked file: one job per archive (one job per book when the file is not
  archive-backed), and each job is read in one `readSession` whose
  `archiveCache` opens that archive once. Books with no file read for
  descriptions (PDF-only, or no supported format) are marked checked without
  being read. Deep Index names no format and no library type. A bounded worker
  pool (`GOMAXPROCS`) reads, one session per job; a single writer commits.
- **Annotation only when NULL.** The annotation is written with
  `SetBookAnnotationIfNull` (`... WHERE annotation IS NULL`), so an existing
  description — including one from enrichment or a manual edit — is never
  replaced. Only when a row was actually changed is `RefreshBookFTS` called, in
  the same transaction. Identifiers are insert-if-absent. Then `metadata_checked`
  is set.
- **Per-batch guard.** The writer commits 500 books per transaction and takes the
  shared `db.WriteGuard` for each batch only (`guard.WithTx`), not for the whole
  run. Other writers (edits, syncs, API writes) interleave between batches
  instead of waiting for the run to end.
- **Skip rule (known broken).** Before reading a book, the gate compares the
  joined record with the book's current fingerprint (stat or zip header, no
  entry inflated). When the record's `reader_version` is `ebook.ReaderVersion`
  and its fingerprint matches, the book is known broken: no entry is opened and
  nothing is written. A replaced file or a new reader version lifts the skip,
  and the book is read again.
- **Failures and cancellation.** Each read outcome is handled in the writer's
  batch transaction. A done book is persisted as above, its metadata record is
  deleted (`DeleteReadFailure`) and it is marked checked. A newly broken book
  (corrupt content, missing entry, …) gets its record upserted
  (`UpsertReadFailure`) and one `unreadable book file` warning after the batch
  commits; it stays unchecked. A known broken or retryable book (I/O error,
  unreadable or missing archive, cancellation) is not written, and a retryable
  one is read again by the next run. On cancellation the batches already
  committed stay, no further file is opened, the uncommitted outcomes record
  nothing, and the rest is picked up next time.
- **Result.** `DeepIndexResult` counts `Checked`, `Filled` (annotation newly
  set), `Skipped` (already had one), `Broken` (recorded broken this run),
  `KnownBroken` (skipped by the rule above) and `Errors` (retryable only). Each
  committed book lands in exactly one of `Checked`, `Broken`, `KnownBroken` or
  `Errors`. The `deep index finished` log line carries all six, as `checked`,
  `filled`, `skipped`, `broken`, `known_broken` and `errors`. A re-run over an
  unchanged library therefore reports `errors = 0` and `broken = 0`.

Online enrichment restamps `content_hash` so the `?v=<content_hash>-<cover
mtime>` cache-buster changes (the second component, `covers.Store.Version`,
busts caches when the cover *bytes* change without a metadata change); the next sync detects the mismatch and restamps it to the canonical
hash, a one-time self-correcting refresh that preserves the gap-filled fields
(sync never overwrites a populated field with an empty one). One consequence: for
an enriched row the canonical hash is computed from the index record *without* the
enriched annotation, so `content_hash` stops being a true fingerprint of the
*displayed* metadata for those rows — it stays stable (no loop), but slightly
weakens in-place-edit detection for them. The manual **Fix Match**
(`POST /api/books/{id}/match`) overwrites instead of gap-filling, and it replaces
the most visible fields too — title, authors, series, and genres — not just the
description/publisher/year.

---

### DB Access Pattern

- **`sqlc`** generates type-safe Go from raw SQL for every query **except** the
  dynamic book filter.
- **Dynamic book filter** — `db.FilterBooks` / `CountFilteredBooks`
  (`internal/db/booksfilter.go`) assemble their query with the
  `github.com/stephenafamo/bob` query builder (sqlite dialect): conditional
  joins/WHERE via `sm` mods, FTS5 `MATCH` and the `EXISTS` format guard via
  `sqlite.Raw`, and a dynamic `ORDER BY`: BM25 relevance for searches; otherwise
  `rating` DESC (unrated last) when `sort=rating`, `added_at` DESC ("Newest")
  when `sort=source`, and `imported_at` DESC ("Recently added", the default)
  otherwise — each non-relevance order tie-broken by `added_at`/`id`. Pure sqlc can't express optional joins +
  conditional WHERE + FTS `MATCH` + dynamic ordering in one static query, and
  the previous `fmt.Sprintf` fragment concatenation (with `//nolint:gosec`) was
  the one place sidestepping sqlc. Bob is used **builder-only — no codegen** —
  so sqlc remains the layer for everything else. `scanBook` centralizes the
  manual `dbq.Book` scan, and `TestFilterBooksScanMatchesGetBook` guards the
  column-list ↔ scan drift.
- **Connection Configuration**: The pragmas are carried in the DSN so the driver
  applies them to **every** pooled connection, not just the first:
  ```go
  sql.Open("sqlite", dbPath+
      "?_pragma=busy_timeout(5000)"+
      "&_pragma=journal_mode(WAL)"+
      "&_pragma=synchronous(NORMAL)"+
      "&_pragma=foreign_keys(1)"+
      "&_txlock=immediate")
  ```
  `foreign_keys` and `busy_timeout` are **per-connection** settings that SQLite
  does not persist. Running them once via `db.Exec("PRAGMA …")` configures only
  whichever single connection served that `Exec`, leaving every other pooled
  connection with `foreign_keys=0` (so `ON DELETE CASCADE` is silently skipped →
  orphaned junction rows) and `busy_timeout=0` (immediate `SQLITE_BUSY` on write
  contention). Encoding them in the DSN guarantees they hold on each connection.
  (WAL is persisted in the file header regardless; it is listed for consistency.)
- All write operations (insert book + FTS entry + junction tables) execute inside a single `BEGIN IMMEDIATE` transaction. The DSN's `_txlock=immediate` makes every `BeginTx` issue `BEGIN IMMEDIATE`, acquiring the write lock upfront — matching the single-writer design and avoiding deferred-to-immediate promotion conflicts.
- Read operations run concurrently using shared-read mode enabled by WAL.

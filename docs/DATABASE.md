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
└── 003_book_identifiers_lookup.sql    # idx_book_identifiers_lookup on book_identifiers(type, value) — accelerates identifier-based lookups (ISBN / Amazon / Goodreads) used by metasearch enrichment
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
    cover_prio INTEGER NOT NULL DEFAULT 0, -- filePriority of the format whose cover is
                                  -- cached on disk; 0 = none. Persisted so a partial
                                  -- re-sync can't downgrade a richer edition's cover
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

### Full-Text Search (FTS5)

```sql
CREATE VIRTUAL TABLE books_fts USING fts5(
    book_id UNINDEXED,
    title,
    authors,
    series,
    annotation,
    tokenize = 'unicode61 remove_diacritics 1'
);
```

- `book_id UNINDEXED` stores the FK without indexing it for search.
- `unicode61 remove_diacritics 1` normalizes accented characters for multilingual search.
- FTS sync is managed by triggers on `DELETE` and explicitly inside transactions on `INSERT`.

### Sync Trigger

```sql
CREATE TRIGGER books_ad AFTER DELETE ON books BEGIN
    DELETE FROM books_fts WHERE book_id = old.id;
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

1. **Single writer** — Channel capacity is `1`. Only one indexing routine runs at a
   time. This prevents SQLite write-lock contention. Library **teardown** (the
   deadline-driven purge sweep and the on-demand "Purge Now") shares the same
   single-writer lock (`Engine.writeMu`), so a cascade delete never races an
   in-flight sync transaction even though it is invoked off the worker goroutine.
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
6. **Cover Extraction & Caching** — The sync engine extracts a cover during ingestion (preferring richer formats: epub→fb2→mobi/azw3→pdf), normalizes it to JPEG (`covers.Save` → `convertToJPEG`), and caches it keyed to the **logical** `books.id` under `/data/covers/{id/1000}/{id}.jpeg`. Every cached cover is therefore a JPEG, so serving and the OPDS feed declare `image/jpeg` without sniffing each file (see [API.md](./API.md#cover-image-serving--security)). The priority of the format whose cover is cached is **persisted** in `books.cover_prio`, so the "prefer richer formats" rule holds *across* runs, not just within one — a partial re-sync (e.g. only the PDF changed) can never downgrade a richer edition's cover. `covers.Save` skips a write when the new bytes are byte-identical to the cached file, so the on-disk mtime — and thus the `?v=` cache-buster — only changes when the image actually changes. Lazy extraction negative-caches a *missing* cover only after a **successful** parse that found none; a parse **error** is never cached, so a transient failure retries on the next view.

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

`filePriority` (`internal/ingest/merge.go`) ranks formats by metadata/cover
reliability — the same ranking drives cover selection (`saveCoverIfBetter`):

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
   (`Backfill`) behind the api `MetadataExtractor` interface. PDFs are skipped.
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

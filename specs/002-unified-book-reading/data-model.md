# Data Model: Unified Book Reading

## Schema changes (migration `007_book_read_failures.sql`)

### `book_read_failures` (new)

```sql
CREATE TABLE book_read_failures
(
    book_id        INTEGER NOT NULL,
    flow           TEXT    NOT NULL CHECK (flow IN ('metadata', 'cover')),
    reason         TEXT    NOT NULL CHECK (reason IN ('corrupt', 'missing_entry', 'too_large', 'crashed')),
    detail         TEXT    NOT NULL DEFAULT '',   -- short, log-grade; never sent to clients
    fingerprint    TEXT    NOT NULL,              -- see "Fingerprint"
    reader_version INTEGER NOT NULL,              -- ebook.ReaderVersion at record time
    recorded_at    INTEGER NOT NULL,              -- unix seconds
    PRIMARY KEY (book_id, flow),
    FOREIGN KEY (book_id) REFERENCES books (id) ON DELETE CASCADE
) WITHOUT ROWID;

CREATE INDEX idx_book_read_failures_flow_version
    ON book_read_failures (flow, reader_version, book_id);
```

- At most one row per (book, flow). The row exists only while the book is broken for that flow.
- Deleting a book (sync prune) deletes its rows, and so does purging its library (cascade through `books`).
- `detail` is capped at 256 bytes by the writer. It may hold library-relative names and stays out of API responses.
- Down migration: `DROP TABLE book_read_failures`. No other table changes.

### Existing columns (unchanged meaning)

| Column | Role in this feature |
|---|---|
| `books.metadata_checked` | Set to 1 only on a **done** metadata read (with or without a description). Broken and retryable books keep 0, so they stay in the Deep Index work list, where the gate skips known-broken books without reading. |
| `books.cover_state` | 0 unknown / 1 has / 2 none, as today. A broken cover leaves it at 0, and the failure record suppresses re-reads. 2 still means "read fine, no cover". |
| `books.cover_prio` | As today. The importer resets it to 0, together with `cover_state`, when it invalidates a lazily extracted cover (new or changed file of rank ≥ `cover_prio`). |
| `book_files.file_size`, `mtime` | The reconciler's changed-file signal. A change also deletes the book's failure rows. |

## Enums

**Flow**

| Value | Read by | Counted/shown |
|---|---|---|
| `metadata` | Deep Index, on-view fill (`LocalBackfiller`) | Yes: library `unreadable_count`, `unreadable` filter, book notice |
| `cover` | lazy cover flow (`Extractor.Cover`) | No, recorded silently (clarification 2026-11-02) |

**What counts as a broken cover** (US2 scenario 7, "a cover cannot be decoded from a readable file"): the cover payload declared inside the file cannot be extracted. That means FB2 coverpage base64 that is invalid or truncated, or an EPUB manifest cover item whose zip entry is missing or fails to inflate. That is a cover-flow `corrupt` row, with `cover_state` left at 0. A file that declares no cover, or extracted bytes that are not a decodable image, is **not** broken. `covers` sets `cover_state = 2` (none), which already prevents re-reads, and no row is written.

Folder import also reads metadata, but an unparseable file never becomes a book, so there is no `book_id` to record. That behaviour is unchanged.

**Reason** (stored value → user label in the SPA)

| Value | Cause (see research R6) | Label |
|---|---|---|
| `corrupt` | malformed content inside a readable file or entry | "The file is damaged or not a valid ebook." |
| `missing_entry` | entry absent from an archive that opened fine | "The file is missing from its archive." |
| `too_large` | inflated entry exceeds `ebook.MaxEntryBytes` | "The file is too large to read." |
| `crashed` | the reader panicked on the file | "Folio's reader couldn't process this file." |

Retryable failures have no reason and are never stored.

## Fingerprint

`fingerprint = hex(sha256(candidate₁ ‖ "\n" ‖ candidate₂ ‖ …)[:16])`. The candidates are the book's files that the flow would try, in `Dispatcher.Rank` order. Each candidate contributes `source_path|size|mtime_unix`:

| Source | size | mtime |
|---|---|---|
| Plain file (folder, Calibre book file) | `os.Stat` size | `os.Stat` modification time |
| Archive entry (present) | zip header `UncompressedSize64` | zip header `Modified` |
| Archive entry (missing from a readable archive) | archive `os.Stat` size | archive `os.Stat` modification time |

The fingerprint is computed without inflating any entry or opening any plain file.

## Runtime types (`internal/ingest`)

| Type | Fields | Notes |
|---|---|---|
| `ReadStatus` (exported) | `ReadDone` \| `ReadBroken` \| `ReadRetryable` | |
| `ReadResult` (exported) | `Status`, `Reason ebook.Reason`, `Detail`, `Fingerprint`, `Known bool`, `Err` | `Known` = broken by an applicable record, so the file was not read. Exported so `FileExtractor` can be faked outside `ingest`. |
| `DeepIndexResult` | `Checked`, `Filled`, `Skipped`, **`Broken`**, **`KnownBroken`**, `Errors` | Each book of the run lands in exactly one of Checked / Broken / KnownBroken / Errors. `Errors` = retryable only. |

## State transitions (per book, per flow)

```text
            read ok                                   read ok
pending ───────────────▶ done      broken(stale) ───────────────▶ done   (row deleted)
   │  ▲                                  ▲
   │  │ retryable (nothing stored)       │ fingerprint changed
   │  └──────────┐                       │ or reader_version ≠ current
   │             │                       │ or sync saw the file change (row deleted → pending)
   │ content error / too large /         │
   │ crash / missing entry               │
   └──────────────▶ broken (row upserted, logged once at Warn)
                      │
                      └── same fingerprint + same version: every read path
                          returns broken(known) without reading the file
```

- **pending**: metadata: `metadata_checked = 0` and no applicable row. Cover: `cover_state = 0` and no applicable row.
- **done**: metadata: `metadata_checked = 1`. Cover: `cover_state ∈ {1, 2}`. Any row for that flow is deleted in the same write.
- **retryable**: no write, and the book stays pending for the next run or view (FR-016).
- **broken**: a row with the current fingerprint and version. It becomes **stale** (treated as pending, then re-read) when either one no longer matches (FR-015). A read that ends broken again overwrites the row with the new fingerprint and version.

## Counts

- **Per library (`unreadable_count`)**:

  ```sql
  SELECT COUNT(*) FROM book_read_failures f JOIN books b ON b.id = f.book_id
  WHERE b.library_id = ? AND f.flow = 'metadata' AND f.reader_version = ?
  ```

  It is computed by two new queries, `ListUnreadableCounts(reader_version)` (`library_id, unreadable_count` grouped by library, merged in Go into the list response) and `CountUnreadableBooks(library_id, reader_version)` (single-library responses). `ListLibrariesWithBookCount` is unchanged, so its callers (`api/libraries.go`, `mcpserver/tools.go`) are untouched.
- **Book list filter**: `EXISTS (SELECT 1 FROM book_read_failures f WHERE f.book_id = b.id AND f.flow = 'metadata' AND f.reader_version = ?)`, ANDed with the existing `library` filter.
- **Book detail**: `GetReadFailure(book_id, 'metadata')`, which counts as unreadable when `reader_version` = current. When a row exists, `getBook` runs the on-view fill even if `needsBackfill` is false, so the fill's fingerprint gate re-checks a changed file before the view renders.
- Cover-flow rows are never counted.

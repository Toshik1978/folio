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
- `detail` is capped at 256 bytes by the writer. It may hold library paths (source paths, archive paths), is logged with the `Warn` line, and stays out of API responses.
- Down migration: `DROP TABLE book_read_failures`. No other table changes.

### Existing columns (unchanged meaning)

| Column | Role in this feature |
|---|---|
| `books.metadata_checked` | Set to 1 only on a **done** metadata read (with or without a description). Broken and retryable books keep 0, so they stay in the Deep Index work list, where the gate skips known-broken books without reading. |
| `books.cover_state` | 0 unknown / 1 has / 2 none, as today. A broken cover leaves it at 0, and the failure record suppresses re-reads. 2 still means "read fine, no cover". |
| `books.cover_prio` | As today, plus one value: the lazy cover flow records `1` (`MarkFileCover`) when it caches a cover extracted from the book's file, so the cover is known to be file-derived; an online-enrichment cover stays `0`. The importer resets it to 0 (`ResetCoverState`), together with `cover_state`, when it invalidates file-produced cover state: `cover_state = 2`, or `cover_state = 1` with `1 <= cover_prio < 1000`, after a new, changed or newly joined file of rank ≥ `cover_prio` (or a file that left the book). Online (0) and manual (1000) covers are kept. |
| `book_files.file_size`, `mtime` | The reconciler's changed-file signal. A change also deletes the book's failure rows, and so does a file joining or leaving an existing book. |

## Enums

**Flow**

| Value | Read by | Counted/shown |
|---|---|---|
| `metadata` | Deep Index, on-view fill (`LocalBackfiller`) | Yes: library `unreadable_count`, `unreadable` filter, book notice |
| `cover` | lazy cover flow (`Extractor.Cover`) | No, recorded silently (clarification 2026-11-02) |

**What counts as a broken cover** (US2 scenario 7, "a cover cannot be decoded from a readable file"): the cover payload declared inside the file cannot be extracted. That means FB2 coverpage base64 that is invalid or truncated, an EPUB manifest cover item whose zip entry is missing or fails to inflate, or a MOBI cover record that cannot be read. That is a cover-flow `corrupt` row, with `cover_state` left at 0. A declared cover over the 10 MiB cover cap is a `too_large` row in every format. A file that declares no cover, or extracted bytes that are not a decodable image, is **not** broken. `covers` sets `cover_state = 2` (none), which already prevents re-reads, and no row is written.

Folder import also reads metadata, but an unparseable file never becomes a book, so there is no `book_id` to record. That behaviour is unchanged: a new file that fails is skipped, and a known file whose changed bytes now fail as broken is not kept, so its book is pruned by that sync if it has no other file. A known file whose read fails for a retryable reason keeps its book, and the next sync reads it again.

**Reason** (stored value → user label in the SPA)

| Value | Cause (see research R6) | Label |
|---|---|---|
| `corrupt` | malformed content inside a readable file or entry, or an archive whose zip container is damaged | "The file is damaged or not a valid ebook." |
| `missing_entry` | entry absent from an archive that opened fine | "The file is missing from its archive." |
| `too_large` | inflated entry exceeds `ebook.MaxEntryBytes`, an FB2 document exceeds its 64 MiB read cap, or a declared cover exceeds the 10 MiB cover cap | "The file is too large to read." |
| `crashed` | the reader panicked on the file | "Folio's reader couldn't process this file." |

Retryable failures have no reason and are never stored.

## Fingerprint

`fingerprint = hex(sha256(line₁ ‖ line₂ ‖ …)[:16])`. The candidates are the book's files that the flow would try, in `Dispatcher.Rank` order. Each candidate contributes one line, `source_path|size|mtime_unix` followed by `"\n"` (an archive entry appends the archive's own `|size|mtime_unix`):

| Source | size | mtime |
|---|---|---|
| Plain file (folder, Calibre book file) | `os.Stat` size | `os.Stat` modification time |
| Archive entry (present) | zip header `UncompressedSize64`, then the archive's `os.Stat` size | zip header `Modified`, then the archive's `os.Stat` modification time |
| Archive entry (missing from a readable archive, or in an archive whose zip container is corrupt) | archive `os.Stat` size | archive `os.Stat` modification time |

The fingerprint is computed without inflating any entry or opening any plain file.

## Runtime types (`internal/ingest`)

| Type | Fields | Notes |
|---|---|---|
| `ReadStatus` (exported) | `ReadDone` \| `ReadBroken` \| `ReadRetryable` | |
| `ReadResult` (exported) | `Status`, `Reason ebook.Reason`, `Detail`, `Fingerprint`, `Known bool`, `Recorded bool`, `Err` | `Known` = broken by an applicable record, so the file was not read. `Recorded` = a record (applicable or not) existed when the read began, so a done read knows to clear it. Exported so `FileExtractor` can be faked outside `ingest`. |
| `DeepIndexResult` | `Checked`, `Filled`, `Skipped`, **`Broken`**, `NewlyBroken`, **`KnownBroken`**, `Errors` | Each book the run committed lands in exactly one of Checked / Broken / KnownBroken / Errors; a book whose batch was not committed (cancel, failure) is counted nowhere and stays unchecked. `Errors` = retryable only. `NewlyBroken` (added with the Telegram notifications, spec 003) samples up to 5 of the `Broken` books with their reasons. |

## State transitions (per book, per flow)

```text
            read ok                                   read ok
pending ───────────────▶ done      broken(stale) ───────────────▶ done   (row deleted)
   │  ▲                                  ▲
   │  │ retryable (nothing stored)       │ fingerprint changed
   │  └──────────┐                       │ or reader_version ≠ current
   │             │                       │ or sync saw the file change, or the
   │             │                       │ post-sync re-check found a new fingerprint
   │             │                       │ (row deleted → pending)
   │ content error / too large /         │
   │ crash / missing entry               │
   └──────────────▶ broken (row upserted, logged once at Warn)
                      │
                      └── same fingerprint + same version: every read path
                          returns broken(known) without reading the file
```

- **pending**: metadata: `metadata_checked = 0` and no applicable row. Cover: `cover_state = 0` and no applicable row.
- **done**: metadata: `metadata_checked = 1`. Cover: `cover_state ∈ {1, 2}`. Any row for that flow is deleted: in the batch transaction (Deep Index), just before the book is marked checked (on-view fill; a failed delete leaves the book pending), or best-effort when the gate found a row (cover flow).
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
- **Book detail**: `GetReadFailure(book_id, 'metadata')`, which counts as unreadable when `reader_version` = current. When a row exists, `getBook` runs the on-view fill even if `needsBackfill` is false, so the fill's fingerprint gate re-checks a changed file before the view renders. The edit, cover and match responses carry the same marker, without the re-check.
- **Stale rows**: besides the read paths, every sync (and a sync skipped on an unchanged checkpoint) re-checks the library's current-version `metadata` rows against their files' fingerprints and deletes the ones that no longer match (`ListMetadataFailureFiles`, `recheckReadFailures`).
- Cover-flow rows are never counted.

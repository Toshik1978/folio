# Quickstart: validating Unified Book Reading

## Prerequisites

- Run `task setup`, then `task build` (or `task dev:backend` + `task dev:frontend`).
- Build the branch image with `task build:docker`. Pull the previous release image for comparison (`<previous-release-image>`).
- Use a **copy** of a real database for the end-to-end checks. A live Folio keeps its database in WAL mode, so take an online backup opened read-only instead of a `cp`:

  ```bash
  mkdir -p <copy-dir>
  sqlite3 'file:<data-dir>/folio.db?mode=ro' ".backup <copy-dir>/folio.db"
  ```

  Take one fresh copy per measured run (`<copy-a>`, `<copy-b>`).
- Mount the libraries read-only at the same container path the copied database refers to.

## 1. Automated gates

```bash
task test && task lint
```

These cover:
- **ebook**:
  - each parser's `Metadata`/`Cover` over a plain-file `Source` and an archive-entry `Source`;
  - `Metadata` never returns cover bytes;
  - `.fb2.zip` wrappers;
  - FB2 header reads inflate only the start of the entry (a counting `ReaderAt` asserts the furthest offset is far below the entry size);
  - an entry over a test-lowered cap gives `ErrTooLarge`;
  - a panicking parser gives `*PanicError`;
  - an injected read error gives `ErrSourceIO`;
  - `ReasonOf` mapping.
- **ingest**:
  - the archive fixture with FB2/EPUB/MOBI/PDF entries: FB2, EPUB and MOBI gain descriptions, PDF is marked checked without being read (SC-003, FR-007);
  - no file is created under the data dir during a run (SC-005);
  - a corrupt entry is recorded broken once, and a re-run skips it with `Errors = 0` and `KnownBroken = 1` without reading it (SC-004);
  - a changed fingerprint, or a bumped reader version in the test, re-reads the book, and a successful read deletes the row;
  - a missing archive and a cancelled run record nothing (FR-012, FR-016);
  - multi-file books;
  - folder import stores no cover, and a changed file invalidates a lazily extracted cover but never a manual one;
  - a sync (full, or skipped on an unchanged checkpoint) lifts the record of a replaced archive entry and keeps an unchanged broken book's record;
  - Deep Index pages its work list without skipping or repeating a book.
- **db/api**:
  - the migration 007 round trip;
  - cascade on book delete and library purge;
  - `unreadable_count`, the `unreadable=1` filter scoped by `library`, and `unreadable` on detail only while the record applies.
- **covers** (driven through a real `covers.Store` in ingest's `extractorSuite`): a broken cover is recorded once, the placeholder is served, `cover_state` stays 0, and the next view does not read the file.
- **Vitest**: see [contracts/spa-ui.md](./contracts/spa-ui.md#vitest-coverage).
- **Format-name guards (SC-007, FR-004)**: the authoritative checks are the Go tests `ingestSuite.TestNoFormatNames`, which covers every non-test file of `internal/ingest` and `internal/covers` (string literals equal to a format label or containing its dotted extension, plus any use of `ebook` format constants or identifiers; the format list is taken from the dispatcher; identifier words are not matched), and the `ebookSuite` public-surface test. Both run in `task test`. A quick manual approximation, which must print nothing:

  ```bash
  grep -rnE --include='*.go' --exclude='*_test.go' --exclude-dir=testdata \
    'ebook\.Format|"\.?([Ee][Pp][Uu][Bb]|[Ff][Bb]2(\.zip)?|[Mm][Oo][Bb][Ii]|[Aa][Zz][Ww]3?|[Pp][Dd][Ff])"|"[^"]*\.(epub|fb2|fbz|mobi|azw3?|pdf)\b[^"]*"' \
    internal/ingest internal/covers | grep -vE '^[^:]+:[0-9]+:\s*//'
  ```

  Like the Go guard, it matches only exact or dotted labels in literals and `ebook.Format*` references, and the last filter drops comment lines (the guard does not check comments; `source.go` has one naming `"../x.fb2"`). Identifier-scheme literals such as `"mobi-asin"`/`"mobi-uid"` and shared vocabulary such as `fb2GenreMap` are allowed and do not match.

## 2. Non-regression: Deep Index time (SC-001, SC-002)

Run this at the end of the US1 phase (the description ranking and job grouping change there), and again on the finished branch.

Run the same measurement with the previous release and with this branch, on the same host, each against its own fresh copy. Run each container without network access and with read-only libraries, and start it only after step 1:

```bash
docker run -d --name folio-perf --network none \
  -v <copy-a>:/data -v <library-root>:/library:ro <image>
```

1. **Reset the work list** on the copy before starting the container, the same way for both runs. The branch creates `book_read_failures` empty on first start (migration 007), so nothing else needs clearing:

   ```bash
   sqlite3 <copy-a>/folio.db "UPDATE books SET metadata_checked = 0 WHERE library_id = <id>;"
   ```

2. **Trigger Deep Index** for the archive-collection library. Use a throwaway client that shares the container's network namespace, so loopback works and nothing gets out:

   ```bash
   docker run --rm --network container:folio-perf curlimages/curl \
     -s -X POST http://127.0.0.1:8080/api/libraries/<id>/deep-index
   ```

3. **Read the result** from `docker logs folio-perf`, in the `deep index finished` line: `duration`, `checked`, `filled`, `errors`, plus `broken` and `known_broken` on the branch.
4. **Pass**:
   - the branch `duration` is ≤ 1.10 × the previous release's (SC-001);
   - `filled` is ≥ the previous release's (EPUB/MOBI entries now also fill).
5. **Re-run** step 2 on the branch container without resetting. **Pass**: it finishes in under 1 min with `errors = 0` and `broken = 0`, and `known_broken` equals the first run's `broken` (SC-002, FR-017).
6. Confirm that `<copy-a>/tmp` was not created by the branch run (SC-005).
7. `docker rm -f folio-perf` and discard the copies.

## 3. Broken-book scenarios (US2)

Use a scratch library built from test fixtures, not a real collection:
- an archive collection whose archive holds a valid FB2, a corrupt FB2 (truncated XML), a truncated EPUB and a PDF;
- a folder library with one valid EPUB.

| # | Action | Expected |
|---|---|---|
| 1 | Run Deep Index | One `Warn` per broken book. Summary `broken = 2`. The corrupt FB2 and the EPUB have rows (`flow = metadata`, reasons `corrupt`). The PDF is checked. |
| 2 | Run Deep Index again | `broken = 0`, `errors = 0`, `known_broken = 2`, and no new `Warn` lines. |
| 3 | Open a broken book's page | No new log line, and the notice shows. |
| 4 | Replace the archive with one where the FB2 is fixed, then run Deep Index | That book is re-read and filled, and its row is gone. The archive's own size and mtime are part of every entry's fingerprint, so the EPUB is re-read once too and recorded again: `broken = 1`, `known_broken = 0`. A further run reports it as `known_broken = 1`. |
| 5 | Make the archive unreadable (rename it away), reset one book's `metadata_checked`, run Deep Index | `errors ≥ 1`, and no new rows are recorded (retryable). |
| 6 | Cancel a running Deep Index from the engine (shutdown) | Uncommitted books stay pending, and no rows are added by the cancellation. |
| 7 | Display a book whose FB2 is readable but whose coverpage `<binary>` holds invalid base64 | A `cover` row is recorded once and the placeholder is served. Reloading does not re-read the file, and the book is not counted as unreadable. |
| 7b | Display a book whose coverpage `<binary>` is valid base64 of non-image bytes | No row. `cover_state = 2` (none), the placeholder is served, and reloading does not re-read the file. |
| 8 | Folder library: import, then browse | Import logs no cover work. Covers appear on first display. Touching the EPUB (new mtime) and syncing re-extracts its cover on the next display. |

## 4. UI check (US3)

1. Open Settings → Libraries. The scratch archive library shows `⚠ 1 unreadable` under its book count (after step 4 above), and the folder library shows nothing.
2. Click the link. The book list opens with that library selected and an `Unreadable` chip, listing exactly the broken book (SC-006).
3. Open the book. The notice reads "Folio couldn't read this book's file: The file is damaged or not a valid ebook. Downloading it may still work." There is no retry button.
4. Replace the file with a valid one, then reopen the book page. The notice is gone, and after a refresh the library row no longer shows the warning (US3 scenario 5).
5. Remove the chip. The full list returns.

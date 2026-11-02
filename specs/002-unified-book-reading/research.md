# Research: Unified Book Reading

These decisions were taken with the maintainer during brainstorming, or follow from reading the current code. Code references are as of the branch point.

## R1. Baseline: how books are read today

- **Findings**:
  - `ebook.Parser.Parse(ctx, path)`: every parser opens a path itself. MOBI already works on an `io.ReaderAt` internally (`openMOBI`). EPUB and `.fb2.zip` use `zip.OpenReader`. PDF passes an `*os.File` to pdfcpu.
  - `ebook.ParseFB2Description(io.Reader)` is a public, FB2-only shortcut. `ingest.DeepIndexer` calls it directly (`parseFB2` field, `inpxItem` switching on `ebook.FormatFB2`/`FormatPDF`).
  - EPUB/MOBI entries of an archive collection go through `Extractor.Backfill`. That re-opens the archive per book and copies the entry into `<data-dir>/tmp` (`materialize`).
  - Folder import (`folder.go` → `recordFromMeta`) takes `Metadata.Cover` and saves it through `importer.saveCoverIfBetter`. Archive-collection books get covers lazily (`covers.Store.lazyExtract` → `Extractor.Cover`).
  - Every local read error is retried:
    - `LocalBackfiller.fill` logs `Warn` and leaves the book unchecked;
    - Deep Index counts `Errors` and leaves the book unchecked;
    - `lazyExtract` serves the placeholder without recording state.
  - `ListFilesForBook` orders by `file_format` alphabetically, so the lazy cover flow picks a different file than import does (`filePriority`).
- **Conclusion**: there are two reading paths per format, two library-type branches on the reading path (`extract.go`, `deepindex.go`; `bookfile` downloads have a third, which is out of scope), and no negative cache for local failures.

## R2. Parser API: one random-access source, two operations

- **Decision**: `ebook.Source{Name string; R io.ReaderAt; Size int64}`. Each `Parser` has `Metadata(ctx, Source)` (never decodes covers), `Cover(ctx, Source)` and `Traits()`. The `Dispatcher` routes both operations by `Source.Name` extension, checks ctx before work, recovers panics, and wraps unclassified errors as malformed content.
- **Rationale**:
  - `io.ReaderAt` + size is what `archive/zip.NewReader`, `openMOBI` and `io.NewSectionReader` (→ `io.ReadSeeker` for pdfcpu, `io.Reader` for the FB2 decoder) need. So every format reads the same value, whether it is a plain file or an archive entry.
  - Splitting Metadata from Cover lets the description flow skip `<binary>` blocks, EPUB images, MOBI image records and the PDF page-1 extraction.
- **Alternatives considered**:
  - *An optional stream-capability interface next to the path API*: two ways to read every format, and the leak stays.
  - *Whole entries loaded into memory*: every FB2 would be fully inflated, so Deep Index would take hours instead of minutes.

## R3. Lazy in-memory archive entries

- **Decision**: `ebook.OpenZipEntry(f *zip.File, limit int64) (Source, io.Closer)` returns a reader that:
  - opens the entry's decompressor on the first read;
  - on `ReadAt(p, off)`, inflates sequentially until `off+len(p)` (or EOF), appending to an in-memory prefix buffer, then serves from the buffer;
  - is guarded by a mutex (the `io.ReaderAt` contract allows concurrent calls);
  - returns `ebook.ErrTooLarge` once the buffer would exceed `limit`. Production passes `ebook.MaxEntryBytes`, 64 MiB, the existing anti-bomb ceiling. Passing the limit explicitly lets tests in other packages lower it without a global.
  - Its `Size` is the header's `UncompressedSize64`. A stream that ends early is malformed. Bytes past the declared size are never served.
- **Rationale**:
  - FB2 metadata sits at the start, so the header read inflates a few KB.
  - EPUB (central directory at the end), PDF (xref at the end) and MOBI cover records have to inflate further, which is inherent to those formats.
  - The cap counts inflated bytes, not the declared size: a large FB2 still has its description read, while an oversized EPUB fails cleanly (US1 scenario 5).
  - The same constructor serves `.fb2.zip` wrapper files, so wrappers and archive collections share one path.
  - *Later addition:* the FB2 XML itself, plain file or wrapper, is read through a 64 MiB cap (`maxArchiveTextBytes`) that fails with `ErrTooLarge`, so a document read past the cap is `too_large` rather than a truncated, i.e. `corrupt`, file. A header read stops long before it.
- **Alternatives considered**:
  - *Temp files*: forbidden by FR-005.
  - *Rejecting up front on `UncompressedSize64 > cap`*: it would make large FB2s unreadable for descriptions, and the declared size is untrusted anyway.
  - *Re-opening the decompressor for backward reads*: the readers seek backward often (zip directory, then entries), so keeping the prefix is simpler and bounded by the cap.

## R4. Covers: one lazy flow for all library types

- **Decision**:
  - Folder import reads metadata only.
  - Covers come from `covers.Store.lazyExtract` → `Extractor.Cover` for every library type. That picks the best-ranked file (`Dispatcher.Rank`, the old `filePriority` order).
  - Calibre records keep carrying `cover.jpg` bytes into `saveCoverIfBetter`.
  - Existing covers and `cover_state`/`cover_prio` values stay valid.
  - When the reconciler sees a new or changed file (`rc.prev` size/mtime) whose rank is ≥ the book's `cover_prio`, and the record has no cover bytes, the importer queues a post-commit invalidation: delete the cached cover and thumbnail, then set `cover_state = 0` and `cover_prio = 0`. Manual covers (prio 1000) are never invalidated.
- **Rationale**: one flow, and broken-cover outcomes handled in one place. The invalidation keeps today's "a better or changed edition refreshes the cover" behaviour without reading the cover at import.
- **Cost accepted**: a second read per book. On the first browse after a folder import, covers appear as they are extracted.
- **Superseded (invalidation rule)**: the "rank ≥ `cover_prio`" test alone would also wipe an online-enrichment cover (`cover_prio = 0`) that nothing would fetch again. What shipped marks a lazily extracted cover as file-derived (`cover_state = 1`, `cover_prio = 1`, via `MarkFileCover` on `covers.CoverState`) and invalidates only state a file produced: `cover_state = 2`, or `cover_state = 1` with `1 <= cover_prio < 1000`. The trigger also covers a file newly joined to an existing book, and a file that moves between books (an INPX index fix changed its grouping key) resets the book it left too, along with that book's read-failure rows. A brand-new book, and a book that already had a cover saved in the same run, queue nothing.
- **Alternatives considered**:
  - *Keep import-time covers for folders*: a library-type special case, plus a second place to classify cover failures.

## R5. Where read outcomes are stored

- **Decision**: a new table `book_read_failures`, with one row per (book, flow) and `ON DELETE CASCADE` from `books` (schema in [data-model.md](./data-model.md)).
  - A row exists only while a book is broken for that flow.
  - `metadata_checked` and `cover_state` keep their meaning: broken books stay `metadata_checked = 0` and `cover_state = 0`, and the record is what suppresses re-reads.
- **Rationale**:
  - Small: rows exist only for broken books.
  - No new columns on the largest table.
  - Backfill, Deep Index and sync keep their existing flags.
  - Library purge and book deletion clean up through foreign keys (enabled on every pooled connection).
- **Alternatives considered**:
  - *State, reason, fingerprint and version columns on `books` for both flows*: about 8 columns, and it changes what `metadata_checked`/`cover_state` mean to three subsystems.
  - *Mark broken books as checked and only log*: no UI and no re-check.

## R6. Outcome classification

- **Decision**: the ebook layer raises typed errors, and `ingest.classify` maps them once.

| Source of error | Outcome | Reason |
|---|---|---|
| Parser content error (XML, zip format, MOBI/PDF structure, checksum, early EOF of an entry); the Dispatcher wraps it as `ebook.ErrMalformed` | broken | `corrupt` |
| `ebook.ErrTooLarge` (an entry over `MaxEntryBytes`, an FB2 document over its 64 MiB read cap, a declared cover over the 10 MiB cover cap) | broken | `too_large` |
| Recovered panic (`*ebook.PanicError`) | broken | `crashed` |
| Entry absent from an archive that opened fine | broken | `missing_entry` |
| Archive that opens but whose zip container `zip.NewReader` rejects (not a source I/O error) | broken | `corrupt` |
| `os.Open`/`os.Stat` error on a plain file, an archive or the library root (missing, permission, unmounted) | retryable | — |
| Read error from the underlying file (`ebook.ErrSourceIO`, set by `ebook.TagIO` around the `*os.File`) | retryable | — |
| `context.Canceled`/`DeadlineExceeded`, write-guard acquire timeout | retryable | — |
| A broken read whose fingerprint cannot be computed (a stat or open failure), so it cannot be recorded | retryable | — |
| No supported or describing format among the book's files | done (nothing found) | — |

- **Rationale**:
  - It mirrors online enrichment (`api/enrich.go`), where a no-match is recorded and transient errors are retried.
  - `TagIO` is what tells "the disk dropped mid-read" apart from "the bytes are corrupt". Without it both would surface as a short read inside the parser.
- **Alternatives considered**:
  - *Matching on error strings*: fragile.
  - *Treating every parse error as retryable (today)*: the defect this feature fixes.

## R7. File fingerprint

- **Decision**: a TEXT value, the hex of the first 16 bytes of `sha256` over the flow's candidate files in rank order. Each candidate contributes `source_path | size | mtime_unix`, where size and mtime come from:
  - a plain file (folder, Calibre): `os.Stat` size and modification time;
  - an archive entry: the zip header's `UncompressedSize64` and `Modified`, read from the central directory without inflating;
  - an entry missing from its archive: the archive's `os.Stat` size and mtime, so replacing the archive re-checks the book.
- **Rationale**:
  - It is cheap for every source type.
  - It changes when any candidate changes, or when a file is added to or removed from the book (the multi-file edge case).
  - One TEXT column keeps the comparison opaque to SQL.
- **Superseded (archive entries)**: the zip header alone is shared by a damaged archive and a good copy of the same release, so replacing the archive never lifted a `corrupt` record. What shipped appends the archive's own `os.Stat` size and mtime to each present entry's stamp (`UncompressedSize64|Modified|archive size|archive mtime`). An entry missing from its archive, or held by an archive whose container is corrupt, stamps the archive's `os.Stat` alone. Each candidate contributes one line, `source_path|stamp` followed by a newline.
- **Alternatives considered**:
  - *Content hash*: it would read the file, which defeats SC-004.
  - *Stored `book_files.file_size/mtime` only*: archive entries have no reliable mtime there, and a replaced archive goes unseen until the next sync.

## R8. Reader version

- **Decision**: `const ReaderVersion = 1` in `internal/ebook/version.go`. It is bumped by hand in the same change as a reader fix that could turn a broken file readable. A record applies only when `reader_version == ebook.ReaderVersion`.
- **Rationale**:
  - The version belongs to the reading logic, so it lives in the reading layer.
  - Using equality instead of `<` also re-checks after a downgrade, which is harmless.
- **Alternatives considered**:
  - *A per-parser version*: finer, but more to maintain.
  - *Automatic detection (binary version)*: every release would re-read every broken file.

## R9. Format ranking and descriptions without format names in `ingest`

- **Decision**: `Parser.Traits()` returns `{Rank, Describes}`. `Dispatcher.Rank(format)` replaces both `ingest.filePriority` and the `CASE` ranking in `ListDeepIndexWork`. `Dispatcher.Describes(format)` replaces `acceptNonPDF`/`isPDF`, and only the PDF parser returns false (FR-007).
  - The description flow uses a single rank order (EPUB > FB2 > MOBI family > PDF), the import's existing metadata-quality order.
- **Rationale**: `ingest` asks capability questions and never names formats (FR-006, SC-007).
- **Trade-off**: Deep Index used to prefer FB2 over EPUB for speed. A book that has both now reads its EPUB, which inflates fully. Books with both formats are rare in archive collections, and each is read once. Measured by quickstart §2.
- **Alternatives considered**:
  - *A separate "description rank"*: two orders to keep consistent, for a marginal gain.

## R9b. Format-agnostic `ebook` public surface

- **Decision**: unexport the concrete parser types and constructors, and the `Format*` label constants. Export `NewStandardDispatcher()` as the single way to get the standard parser set, plus `Dispatcher.Extensions()` and `ContentType(format)`. `Parser`/`NewDispatcher` stay exported, because they are format-agnostic and test fakes need them. An `ebookSuite` AST test fails on any exported identifier with a camelCase word equal to a format label.
- **Rationale**: the review objected to a public format-specific shortcut. With exported parser types, a caller could still bypass the Dispatcher's ctx check, panic recovery and error wrapping.
- **Alternatives considered**:
  - *Keep exported types and only document "use the Dispatcher"*: unenforceable.
  - *Unexport `Parser` too*: tests in `ingest` could no longer register fakes.

## R9c. Format-name guard for `ingest`

- **Decision**: `ingestSuite.TestNoFormatNames` parses (`go/parser`) every non-test `.go` file of `internal/ingest` and `internal/covers`, excluding `testdata`. It fails on:
  - (a) a string literal that equals a registered format label or extension, or contains its dotted extension (`.pdf`, `.fb2.zip`, …). Labels are taken from `NewStandardDispatcher().Extensions()` and `Format`, case-insensitively;
  - (b) any use of a format constant or format-specific identifier from package `ebook` (`ebook.Format*`, `ebook.New<Format>`, `ebook.<Format>Parser`, `ebook.ParseFB2Description`).
  It checks behaviour, not naming: words inside identifiers are not matched, and comments are not checked. Identifier-scheme literals such as `"mobi-asin"`/`"mobi-uid"` name an identifier scheme from external metadata, not a format, and pass rule (a).
- **Rationale**: a hand-picked file list and a regex miss new files, `azw`/`azw3`, dotted literals and case variants. A list derived from the dispatcher stays correct when a format is added. Matching words inside identifiers would flag shared vocabulary such as `fb2GenreMap` that does no format branching.

## R9e. FB2 genre taxonomy stays in `ingest`

- **Decision**: `fb2GenreMap` keeps its name and its place in `internal/ingest/genres.go`. It is FictionBook's own genre taxonomy (the FB2 XSD codes) mapped to canonical BISAC genres. Every library's genres (INPX codes, FB2/EPUB subjects, Calibre tags, online categories, manual edits) are mapped through it, and the genre filters reuse it (`CanonicalGenres`).
- **Rationale**: it is the source vocabulary of Folio's genre model, not format-reading logic. The ingest guard checks behaviour, so it passes.
- **Alternatives considered**:
  - *Rename it neutrally (`genreCodeMap`)*: rejected, because it hides the taxonomy's origin.
  - *Move it into `ebook`*: rejected, because it serves filtering and every source, not format reading.

## R9d. Broken cover vs no cover

- **Decision**: the cover flow is broken (row, `corrupt`) only when the file declares a cover whose payload cannot be extracted: FB2 base64 that is invalid or truncated, or an EPUB manifest cover entry that is missing or fails to inflate. No declared cover, a reference pointing nowhere, or extracted bytes that are not a decodable image all stay "none" (`cover_state = 2`).
  - This changes `decodeBase64Binary` and `extractEPUBCover`, which today swallow these errors as "no cover".
- **Rationale**: "none" already stops re-reads and is the right answer for a valid file whose image is odd. A payload that cannot be extracted means the file is damaged, which is what US2 scenario 7 describes.
- **Later refinements** (same rule, more cases):
  - a declared cover over the 10 MiB cover cap is `too_large` in every parser (FB2, EPUB, MOBI); a MOBI cover record over the cap no longer falls back to the first-image scan;
  - a failed MOBI cover-record read is returned instead of being taken for "no cover": source I/O stays retryable, anything else is `corrupt`;
  - an EPUB cover href's `./` and `../` segments are cleaned before the entry lookup, so a valid book is not reported `corrupt`;
  - a MOBI `FirstImageIndex` outside the records falls back to the first-image scan.

## R10. Deep Index after unification

- **Decision**:
  - `ListDeepIndexWork` returns every file of each unchecked book, plus that book's metadata-flow failure record (fingerprint, version), ordered by `source_path`.
  - Go groups files by book, ranks them, and groups books into jobs by `resolver.group` of the top-ranked file (the archive for archive-backed books, otherwise one book per job).
  - Each job runs in its own read session with its own `archiveCache`, so an archive is opened once per job, and a fallback file in another archive is opened through the same cache.
  - The worker calls the shared `readBook`, so Deep Index, the on-view fill and the cover flow cannot diverge.
  - The writer batch, in the same transaction:
    - upserts records for new broken outcomes;
    - deletes the record on done;
    - marks checked only on done.
  - Known-broken books are skipped before reading, and are counted as `KnownBroken`.
- **Rationale**:
  - Throughput: the FB2 path keeps a seek plus a few-KB inflate per entry, and EPUB/MOBI entries drop the temp-file round trip.
  - Re-run: known-broken books cost one central-directory or stat lookup each.
- **Alternatives considered**:
  - *Excluding known-broken books in SQL by version alone*: it skips the fingerprint check that FR-015 requires.
- **Superseded (one work list)**: loading every unchecked book's files up front held about 170 MB on a 580k-book library, and a large library's first sync runs a full Deep Index automatically. What shipped counts the work once (`CountDeepIndexWork`, the progress total), then reads `ListDeepIndexWork` 5,000 books at a time in book-id order (`after_id`, `lim`), each page through the same jobs, workers and writer. Paging by book keeps a book's files on one page; moving forward by id means a book left unchecked is not picked up again in the same run. Within a page, rows stay ordered by `source_path`.

## R11. Keeping counts honest when files change

- **Decision**:
  - Counts and the `unreadable` filter use `flow = 'metadata' AND reader_version = ?` in SQL.
  - Fingerprint staleness is handled two ways:
    1. The reconciler deletes a book's records when it sees one of its files added or changed.
    2. Every read path re-checks the fingerprint before honouring a record, and the book page runs that check synchronously on view.
- **Rationale**: SQL cannot stat files. Sync covers the common case, and the read paths cover changes sync cannot see (an archive replaced without an index change).
- **Residual**: a file changed behind sync's back keeps counting on the library row until it is next viewed or Deep-Indexed. Accepted.
- **Superseded (residual)**: the residual was closed rather than accepted. After every sync commits, still under its write guard, `recheckReadFailures` recomputes the fingerprint of each book with a current-version `metadata` row (stat or zip directory only, nothing inflated) and deletes the rows that no longer match; a row whose fingerprint cannot be computed is kept. A sync the engine skips on an unchanged `.inpx` or `metadata.db` runs the same re-check through `RecheckReadFailures` (`sync.ReadFailureRechecker`) and emits a `library` event when it lifts rows. A changed file now stops counting by the next sync at the latest. The reconciler also clears a book's rows when a file joins or leaves it.

## R12. Non-regression measurement

- **Decision**: measure SC-001/SC-002 on a copy of a real database, as in [quickstart.md](./quickstart.md) §2.
  - The copy is an online read-only backup (`sqlite3 'file:<data-dir>/folio.db?mode=ro' ".backup …"`).
  - Libraries are mounted read-only and the container runs with `--network none`.
  - Compare the previous release image and this branch on the same host. Reset `metadata_checked` on each fresh copy before the run.
- **Rationale**: same catalog, same host, no egress, and the live data stays untouched.

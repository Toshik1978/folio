# Implementation Plan: Unified Book Reading

**Branch**: `feature/unified-book-reading` | **Date**: 2026-11-02 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/002-unified-book-reading/spec.md`

## Summary

Three parts, delivered in order. Each phase leaves `task build`, `task test` and `task lint` green.

1. **Uniform reading** (US1, FR-001…FR-009). Every ebook parser reads one random-access `ebook.Source` (`io.ReaderAt` + size + name), backed either by a plain file or by a zip entry that inflates lazily and is held in memory up to a size cap. Each parser offers two operations, `Metadata` (never decodes covers) and `Cover`. The `Dispatcher` routes both by extension and keeps the panic recovery and ctx checks. The FB2 header-only read becomes the private body of the unexported `fb2Parser.Metadata`; `ParseFB2Description` and the temp-file materialize go away. A single resolver in `ingest` maps a book file of any library type to a `Source` and a fingerprint, so Deep Index, the on-view fill and the cover flow name no formats and no library types. Folder import reads metadata only, and covers for every library type come from the lazy cover flow (Calibre keeps its `cover.jpg`).
2. **Read outcomes** (US2, FR-010…FR-019). Every local read for a flow ends `done`, `broken` or `retryable`. Broken reads are stored in a new table `book_read_failures` (one row per book and flow, with the reason, a short detail, the file fingerprint and the reader version). A record that still matches the current fingerprint and `ebook.ReaderVersion` stops Deep Index, the on-view fill and the lazy cover flow from reading the file again. Errors are classified once, from typed errors raised by the ebook layer.
3. **Visibility** (US3, FR-020…FR-024). `GET /api/libraries` gains `unreadable_count`, `GET /api/books` gains an `unreadable=1` filter, and `GET /api/books/{id}` (and every other single-book response) gains `unreadable: {reason}`. The SPA shows "⚠ N unreadable" on the library row, linking to the filtered book list, and a plain-language notice on the book page. There is no retry button.

Research and the decisions behind this plan: [research.md](./research.md). The open `CLAUDE?` review comments in `internal/ebook/fb2.go` and `internal/ingest/deepindex.go` are resolved by Phase 1 (see "Review comments resolved").

### Approaches considered

**Parser API**
- **(A) One random-access source per parser, two operations: chosen.** `Metadata(ctx, Source)` and `Cover(ctx, Source)` on every parser. Plain files come in as `*os.File` + stat, archive entries as a lazily inflating in-memory reader. It gives one way to read every format, wherever the book is stored. Its cost is touching all four parsers and their tests.
- **(B) Optional stream-capability interface next to the path-based API: rejected.** It would leave two ways to read every format and keep the format leak in Deep Index.
- **(C) Load whole archive entries into memory: rejected.** Every FB2 would be fully decompressed, so Deep Index would take hours instead of minutes.

**Lazy archive source**
- **Inflate sequentially, only up to the furthest offset read, and keep the inflated prefix: chosen.** An FB2 header read stops at `</description>`, so only the first KBs inflate. An EPUB needs the zip central directory at its end, so its entry inflates fully (inherent to the format). The cap counts inflated bytes, not the declared size, so a large FB2 still reads its header.
- **Temp files (today): rejected** by FR-005 and SC-005.

**Covers**
- **One lazy cover flow for every library type: chosen.** Folder import stops extracting covers. The cost is a second read per book, and on the first browse after a folder import covers are extracted as they are displayed. Calibre keeps its cover file.
- **Keep import-time covers for folders: rejected.** It keeps a library-type special case, and broken covers would need a second outcome path inside the importer.

**Where read outcomes are stored**
- **(1) A new shared table `book_read_failures`: chosen.** Columns: `book_id`, `flow` ∈ {metadata, cover}, reason, detail, fingerprint, reader version, `recorded_at`. One row per (book, flow), deleted with the book (`ON DELETE CASCADE`). The `books` table and the meaning of `metadata_checked` and `cover_state` stay unchanged. Rows exist only for broken books, so the table stays small.
- **(2) State, reason, fingerprint and version columns on `books` for both flows: rejected.** That is about 8 columns on the largest table. It would also change what `metadata_checked` and `cover_state` mean to backfill, Deep Index and sync.
- **(3) Mark broken books as checked and only log: rejected.** The user sees nothing, and a changed file is never re-checked.

**Outcome classification**
- **Typed errors from the ebook layer, mapped once in `ingest`: chosen.** It mirrors online enrichment, where a no-match is recorded and a transient failure is retried. Content errors, oversize entries and recovered panics are broken. Source I/O errors, open errors, ctx errors and guard timeouts are retryable.
- **Matching on error strings: rejected.** It is fragile, and it would mix up "the disk dropped" with "the file is corrupt".

## Technical Context

**Language/Version**: Go 1.27 (backend). TypeScript + Vue 3 (SPA).

**Primary Dependencies**: Existing only: chi, modernc.org/sqlite, goose, sqlc, bob, pdfcpu, `archive/zip`, `encoding/xml`. **No new dependency.** The fingerprint digest uses `crypto/sha256` from the standard library.

**Storage**: Folio SQLite at `<data-dir>/folio.db`.
- Migration `007_book_read_failures.sql`: a new table and one index ([data-model.md](./data-model.md)).
- New and changed sqlc queries: upsert, get, delete read failure; per-library unreadable count; `ListMetadataFailureFiles` for the post-sync re-check; `ResetCoverState` and `MarkFileCover` for the lazy-cover state; `ListDeepIndexWork` returns one page of unchecked books (every file of each, plus its metadata-failure record), and `CountDeepIndexWork` counts the work for the progress total.
- `booksfilter.go` (bob): an `Unreadable` filter, an `EXISTS` subquery with the reader version bound as a parameter.

**Testing**: Go testify suites (one `Test<Package>` per package, entry point only `suite.Run`). Vitest for the SPA. Fixtures under `testdata/`:
- an archive collection with FB2, EPUB, MOBI and PDF entries, one corrupt FB2 and one truncated EPUB;
- an entry larger than a small limit passed to `OpenZipEntry` by the test;
- an `.fb2.zip` wrapper.

**Target Platform**: Linux container (multi-arch), `CGO_ENABLED=0`.

**Project Type**: Single Go web service with an embedded SPA.

**Performance Goals**:
- A full Deep Index run of a large archive collection takes at most 10% longer than with the previous release on the same host and catalog (SC-001).
- A re-run finishes in under 1 min with zero errors (SC-002). Known-broken books cost one fingerprint lookup each and no entry read.
- Archive EPUB and MOBI entries no longer go through a temp file, so they get faster.

**Constraints**:
- Sources are read-only, and nothing is written outside `<data-dir>`. No temp files are used to read books (SC-005).
- Memory is bounded: an inflated entry is capped at `ebook.MaxEntryBytes`, a code constant of 64 MiB, the same value as today's anti-bomb ceiling. At most one entry per Deep Index worker is held at a time, and Deep Index holds one page of its work list (5,000 books) at a time.
- No online calls from Deep Index or MCP (FR-019).

**Scale/Scope**: large archive collections, plus folder and Calibre libraries. Broken files are expected to be a tiny fraction of any library.

No NEEDS CLARIFICATION remains. Every open point is settled in [research.md](./research.md).

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | Status | Notes |
|---|---|---|
| I. Read-only sources | PASS | Files and archives are opened read-only. The temp-file materialize under `<data-dir>/tmp` is removed, so reading a book writes nothing. Read-failure rows live in `folio.db` under the data dir. |
| II. Single CGO-free binary | PASS | Standard library only (`archive/zip`, `crypto/sha256`). No new routes, so the SPA fallback guard is untouched. |
| III. Resource efficiency & minimal egress | PASS | Lazy inflation with a cap bounds memory. Broken files are no longer re-read on every run and view. No new outbound calls, and the enrichment path is unchanged. |
| IV. Schema & query discipline | PASS | Goose migration 007. Static queries go through sqlc. The one dynamic addition, the `unreadable` filter, goes into the bob builder in `booksfilter.go` with parameterized values. |
| V. Testify suites | PASS | New tests are methods on existing or new suites. `ebook` and `ingest` keep their single `Test<Package>` entry point. Fixtures are committed under `testdata/` (each package generates its own), and the entry cap is passed explicitly, so tests lower it without globals. |
| VI. Idiomatic Go & explicit deps | PASS | No new dependency. The interfaces stay at the consumer (`ingest.bookReader`, `covers.CoverExtractor`). Sentinel and typed errors use `errors.Is`/`errors.As`. Wiring stays in `cmd/folio-idx/main.go`. |
| VII. Security boundaries | PASS | The API exposes only the reason category, never the detail, which may contain library paths. Read-only GET changes only, so CSRF is unaffected. The SPA renders the notice as text, not `v-html`. |

Post-design re-check: PASS (no change). The `ebook` public surface becomes format-agnostic (parser types unexported, one exported standard parser set), guarded by a test. `cmd/folio-idx` stays free of logic. Docs are updated in the task that changes the behaviour, so each phase commit carries its docs.

## Design

### Phase 1: Parser unification (US1, FR-001…FR-009, SC-003, SC-005, SC-007)

Contract: [contracts/ebook-reading.md](./contracts/ebook-reading.md).

**Ebook layer (`internal/ebook`)**
- `source.go` (new):
  - `Source{Name, R io.ReaderAt, Size}`.
  - `OpenFile(path) (Source, io.Closer, error)`: `*os.File` + stat.
  - `OpenZipEntry(f *zip.File, limit int64) (Source, io.Closer)`: the lazy entry reader. It inflates sequentially to the furthest offset requested, keeps the inflated prefix, is mutex-guarded (the `io.ReaderAt` contract allows concurrent use), and fails with `ErrTooLarge` once more than `limit` bytes would be held. Production passes `MaxEntryBytes`; tests pass a small limit.
  - `TagIO(io.ReaderAt) io.ReaderAt`: wraps the underlying file so its read errors are distinguishable from content errors (`ErrSourceIO`).
- `errors.go` (new):
  - `ErrSourceIO`, `ErrTooLarge`, `ErrMalformed`, and `*PanicError`.
  - `Reason`, plus `ReasonOf(err) (Reason, bool)`, which returns the broken reason. `ok` is false for retryable errors.
- `version.go` (new): `const ReaderVersion = 1` (FR-018). The comment states the bump rule.
- `parser.go`: `Parser` gets `Extensions()`, `Metadata(ctx, Source)`, `Cover(ctx, Source)` and `Traits() Traits{Rank int, Describes bool}`.
  - The concrete parsers become unexported (`fb2Parser`, `epubParser`, `mobiParser`, `pdfParser`, with no exported constructors).
  - `NewStandardDispatcher(opts ...Option) *Dispatcher` is the one exported way to get the standard parser set, used by `main.go` and tests. `Parser` and `NewDispatcher` stay exported, format-agnostic, for test fakes. (The `WithCrashReporter` option, which reports recovered parser panics, came later with the Telegram notifications, spec 003.)
  - `Rank` takes over from `ingest.filePriority` (EPUB 4, FB2 3, MOBI family 2, PDF 1).
  - `Describes` is false only for PDF (FR-007).
- `dispatch.go`:
  - `Metadata(ctx, log, Source)` and `Cover(ctx, log, Source)`, each with ctx-check, routing, recover → `*PanicError`, and wrapping of unclassified parser errors as `ErrMalformed`.
  - `Supported`/`Format` keep their meaning.
  - New `Rank(format)`, `Describes(format)` and `Extensions()`, keyed by the stored format label.
  - New package function `ContentType(format) string`, so the `Format*` label constants can be unexported. `bookfile.ContentType` delegates to it.
  - Public surface: only format-agnostic identifiers are exported. An `ebookSuite` AST test fails on any exported identifier whose camelCase words include a format label.
- `fb2.go`:
  - `Metadata` = the header-only decode, unexported (`parseFB2Header`, formerly `ParseFB2Description`), over `bufio` on an `io.SectionReader`. A well-formed document without a `<description>` reads as empty metadata (the title falls back to the file name), not as corrupt; a stream with no XML element at all is an error.
  - Both reads cap the FB2 XML at 64 MiB (`maxArchiveTextBytes`), plain file or `.fb2.zip` alike; going past the cap is `ErrTooLarge`, not a truncated (corrupt) document.
  - `Cover` = a token scan for the coverpage href, then the matching `<binary>` only.
  - `.fb2.zip` = `zip.NewReader(src.R, src.Size)` → `OpenZipEntry` → the same functions.
  - `ParseFB2Description` is removed (FR-004).
- `epub.go`: `zip.NewReader(src.R, src.Size)`. `Metadata` reads container + OPF. `Cover` reads OPF + manifest.
- `mobi.go`: `openMOBI(src.R, src.Size)` as today, with the record-0 read capped at 1 MiB so a lying entry size cannot force a huge allocation. `Metadata` = title + EXTH. `Cover` = EXTH 201 + `extractCover`; a `FirstImageIndex` outside the records falls back to the first-image scan.
- `pdf.go`: `pdfcpu.ReadContext(io.NewSectionReader(...))`. `Metadata` skips `coverFromContext`, and `Cover` uses it.
- `ebook.go`: add `MaxEntryBytes`, and unexport the `Format*` constants. `Metadata.Cover` stays, because online providers (`metasearch`, `api/enrich.go`, `api/match.go`) fill it. Ebook parsers' `Metadata` never sets it.
- Cover-decode failures are classified as follows:
  - when the cover payload declared inside the file cannot be extracted, `Cover` returns an error that becomes `ErrMalformed`. That covers FB2 coverpage base64 that is invalid or truncated, and an EPUB manifest cover item whose zip entry is missing or corrupt (the href's `./` and `../` segments are cleaned first, so a valid book is not reported corrupt).
  - a declared cover over the 10 MiB cover cap is `ErrTooLarge` in every parser (FB2, EPUB and MOBI); a MOBI cover record over the cap does not fall back to the first-image scan.
  - a failed MOBI cover-record read is returned, not treated as "no cover": source I/O stays retryable, anything else is malformed.
  - extracted bytes that are not a decodable image keep today's `covers` behaviour (`cover_state = 2`, none), which already stops re-reads.

**Ingest layer (`internal/ingest`)**
- `source.go` (new): `resolver`, the one place that knows the library layout.
  - `open(library, file, ac) (ebook.Source, io.Closer, error)`, `group(library, file) string`, and from Phase 2 `fingerprint(library, files, ac) (string, error)`. The group key is the archive path for archive-backed files and empty otherwise. It takes over the INPX branches of `extract.go` and `deepindex.go`.
  - A new ingest-local `withinRoot` helper rejects a stored `source_path` that escapes the library or archive directory.
  - `bookfile` (downloads) keeps its own archive lookup; it is out of scope for this feature.
  - A per-caller `archiveCache` opens each archive once (with `ebook.TagIO`). Deep Index uses one per worker job; per-book reads use a one-shot cache. An archive that opens but that `zip.NewReader` rejects for a reason other than source I/O is `ErrMalformed` (broken `corrupt`) for every entry read from it.
- `extract.go`: `Extractor` loses `tmpDir`, `materialize`, `parseFromZip`, `acceptNonPDF` and `ebook.FormatPDF`.
  - `Description(ctx, bookID)` and `Cover(ctx, bookID)` both go through the shared `readBook(ctx, flow, files)`.
  - `readBook` orders candidates by `Dispatcher.Rank`, skips formats that cannot serve the flow (`Describes` for metadata, `Supported` for both), and returns the first good read.
  - `NewExtractor(db, log, reader, guard)`: the `dataDir` argument is dropped; the write guard (Phase 2) serializes the cover-flow record writes.
  - `newSession() *readSession` gives Deep Index workers a session that owns an `archiveCache`.
- `deepindex.go`:
  - `parseFB2`, `inpxItem`, `parseEntry`, `deepIndexArchive` and `isPDF` are removed.
  - Jobs are grouped by `resolver.group`, and each worker runs the same `readBook` with its job's archive cache. The code names no format and no library type (SC-007).
  - `NewDeepIndexer(log, db, guard, extractor *Extractor)`. Tests inject a fake `bookReader` into a real `Extractor`; there is no extra session interface.
- `folder.go`: `visit` opens `ebook.OpenFile` → `Dispatcher.Metadata` (FR-008). `recordFromMeta` no longer carries a cover. A known file whose read fails for a retryable reason is marked seen, so its book is kept rather than pruned, and re-read on the next sync.
- `importer.go`/`reconcile.go`: lazy-cover invalidation for records that carry no cover bytes, generic across library types.
  - File-derived marker: when the lazy flow caches a cover extracted from the book's file, `covers.Store` records `cover_state = 1` with `cover_prio = 1` (`MarkFileCover`, a new method on `covers.CoverState`). An online-enrichment cover keeps `cover_prio = 0`.
  - Trigger: for an existing book (never a brand-new one), the reconciler sees a new file, a changed `(file_size, mtime)` versus `rc.prev`, or a file newly joined to the book (`ensureFile` reports it); the file's rank is ≥ the persisted `cover_prio`; and the state is one a file produced: `cover_state = 2`, or `cover_state = 1` with `1 <= cover_prio < 1000`. It is skipped when a cover was already saved for the book this run. Online covers (prio 0) and manual covers (prio 1000) are never invalidated.
  - A file that moves to another book (its grouping key changed) also resets the book it left: the same file-derived invalidation, without the rank check.
  - Action: queue a cover op that deletes the cached cover and thumbnail, then runs `ResetCoverState` (`cover_state = 0`, `cover_prio = 0`, skipping manual covers), after commit like today's ops. The next display re-extracts through the lazy flow from the best-ranked file.
  - Calibre records with `cover.jpg` bytes keep the existing Save path.
- `merge.go`: `filePriority` is replaced by a `rank func(format string) int` passed to `newImporter` by `runReconcile`, which takes the dispatcher (`bookReader`) from the INPX, Calibre and Folder parser constructors (`NewCalibreParser(log, parser)`, `NewINPXParser(log, parser)`, `NewFolderParser(log, parser)`) and hands `reader.Rank` on, so no format names remain.
- Guard: `ingestSuite.TestNoFormatNames` parses every non-test `.go` file of `internal/ingest` and `internal/covers` (excluding `testdata`). It checks behaviour, not naming. It reports only (a) string literals that equal a supported format label or extension, or contain the dotted extension form (`.epub`, `.fb2.zip`, …), derived from `NewStandardDispatcher().Extensions()`, case-insensitive; and (b) any use of a format constant or format-specific identifier from package `ebook` (`ebook.Format*`, `ebook.New<Format>`, `ebook.<Format>Parser`, `ebook.ParseFB2Description`). Words inside identifiers are not matched.
  - Policy: identifier-scheme labels from external metadata (`"mobi-asin"`, `"mobi-uid"`, `"amazon-asin"` in `identifier.go`) name an identifier scheme, not a file format. They are allowed, and the literal rule (exact or dotted match only) passes them.
  - `fb2GenreMap` (`internal/ingest/genres.go`) stays as it is, with the same name and location. It holds FictionBook's own genre taxonomy (the FB2 XSD codes) mapped to canonical BISAC genres. Every source's genres are mapped through it, and the genre filters reuse it (`CanonicalGenres`), so it is shared vocabulary, not format-reading logic. The guard passes it because identifier words are not matched.

**DB**: `ListDeepIndexWork` returns every file of each unchecked book (`book_id, file_id, file_format, source_path`), ordered by `source_path`. The format `CASE` ranking leaves the SQL, and Go ranks by `Dispatcher.Rank`. Later the list became paged (see Phase 2, `DeepIndexer`).

**Composition**: `cmd/folio-idx/main.go` calls `ingest.NewExtractor(database, log, parser, writeGuard)`.

**Covers**: `covers.CoverExtractor` keeps its signature. The only change is the file-derived marker: `covers.CoverState` gains `MarkFileCover`, which `lazyExtract` calls instead of setting `StateHas` when it caches an extracted cover.

**Performance checkpoint**: run quickstart §2 at the end of Phase 1, so an SC-001 regression from the EPUB-first ranking or the job regrouping shows before Phase 2 builds on it.

**Housekeeping**: add `.superpowers/` to `.gitignore` (FR-025).

### Phase 2: Read outcomes and re-check rules (US2, FR-010…FR-019, SC-002, SC-004)

Data: [data-model.md](./data-model.md).

**Migration `007_book_read_failures.sql`**: the table, its `CHECK` constraints and index. Down drops the table.

**Queries (`queries/readfailures.sql`, new)**:
- `UpsertReadFailure`
- `GetReadFailure(book_id, flow)`
- `DeleteReadFailure(book_id, flow)`
- `DeleteReadFailuresForBook(book_id)`
- `ListMetadataFailureFiles(library_id, reader_version)`: every file of each book with a current-version metadata record, with the record's fingerprint, for the post-sync re-check

`ListDeepIndexWork` LEFT JOINs the metadata-flow record (`fingerprint`, `reader_version`). The library counts are separate queries (Phase 3), so `ListLibrariesWithBookCount` and its callers (`api/libraries.go`, `mcpserver/tools.go`) keep their signature.

**Outcome (`internal/ingest/outcome.go`, new)**:
- Exported types, so that `FileExtractor` stays implementable outside `ingest` (the api tests fake it):
  - `ReadStatus`: `ReadDone`, `ReadBroken`, `ReadRetryable`;
  - `ReadResult{Status, Reason ebook.Reason, Detail, Fingerprint string, Known bool, Recorded bool, Err error}`. `Recorded` says a record (applicable or not) existed when the read began, so a done cover read takes the write guard to clear a record only when one existed.
- `FileExtractor.Description(ctx, bookID) (ebook.Metadata, ReadResult)`.
- `classify(err)`:
  - `ebook.ReasonOf` covers malformed, too large and crashed.
  - A missing entry in an opened archive is broken with `missing_entry`.
  - An archive container `zip.NewReader` rejects (not a source I/O error) is broken `corrupt`.
  - Open errors on the file, archive or library root, `ErrSourceIO`, ctx errors and guard timeouts are retryable.
- Multi-file books (edge case):
  - the first done read wins;
  - otherwise, if any candidate is retryable, the outcome is retryable;
  - otherwise the outcome is broken with the reason of the top-ranked candidate.
- The fingerprint is computed over every candidate file.

**Gate in `readBook`**:
1. Load the `(book, flow)` record first.
2. If `reader_version == ebook.ReaderVersion` and the fingerprint recomputed from stat or the zip header plus the archive's stat (no entry inflate) is equal, return `broken, known=true` without reading (FR-014, SC-004).
3. Otherwise read, and return the result with the fingerprint. A broken read whose fingerprint could not be computed is returned as retryable, since it could not be recorded. The caller deletes the record on done, and upserts it on a new broken result, logging once at `Warn` with the reason and the detail (FR-013, FR-015).

**Callers**:
- `LocalBackfiller.fill`:
  - done → delete the record first (a failed delete leaves the book pending), then persist and mark checked. The annotation is written only while the book still has none (`SetBookAnnotationIfNull`), so one stored meanwhile wins; a failed annotation write leaves the book unchecked;
  - broken → record (a known-broken result writes nothing), leave unchecked;
  - retryable → nothing (FR-016).
  - The write uses the existing guard budget, and a busy guard is retryable. The writes are detached from the request's cancellation, so a closed tab does not throw away a read that already happened.
- `DeepIndexer`:
  - known-broken books are skipped without reading;
  - the writer batch upserts records for new broken outcomes and deletes records on done.
  - `DeepIndexResult` gains `Broken` (recorded this run) and `KnownBroken`. `Errors` counts retryable only (FR-017).
  - The run summary log carries all of them.
  - Later additions: the work list is paged, 5,000 books at a time in id order (`ListDeepIndexWork` with `after_id`/`lim`, `CountDeepIndexWork` for the progress total), so memory stays flat on very large libraries. The Telegram notifications (spec 003) added `NewlyBroken`, a sample of up to 5 newly recorded books with their reasons, and `WorkerPanic`, and made a run whose library root is unreachable (`ingest.CheckReadRoot`) fail as a whole instead of counting one retryable error per book.
- Cover flow:
  - `Extractor.Cover` applies the same gate and records cover-flow failures itself (guarded, best-effort).
  - It returns an error for broken and retryable alike. `covers.Store.lazyExtract` already serves the placeholder without setting `cover_state` on error, so `cover_state` keeps its meaning and a fingerprint change is re-checked on the next view.
  - Like the backfill, the record writes are detached from the request's cancellation.
- Importer: the reconciler's changed-file signal (Phase 1), including a file joining or leaving a book, also runs `DeleteReadFailuresForBook`, so a file that sync sees change stops counting at once.
- Post-sync re-check (`internal/ingest/recheck.go`): sync's size/mtime signal misses an archive entry replaced under an unchanged `.inpx`. So after every sync commits, still under its write guard, `runReconcile` calls `recheckReadFailures`: it recomputes the fingerprint of every book with a current-version metadata record (stat or zip directory only) and deletes the records whose fingerprint differs. A record whose fingerprint cannot be computed is kept. A sync the engine skips on an unchanged checkpoint runs the same re-check through the INPX and Calibre parsers' `RecheckReadFailures` (`sync.ReadFailureRechecker`), and emits a `library` event when it lifts records. Best-effort: a failure is logged and the sync result stands.

### Phase 3: Visibility (US3, FR-020…FR-024, SC-006)

Contracts: [contracts/rest-api.md](./contracts/rest-api.md), [contracts/spa-ui.md](./contracts/spa-ui.md).

- **API**:
  - `api/libraries.go`: `libraryView.UnreadableCount`. It is filled from new queries: `ListUnreadableCounts(reader_version)` (library_id → count, merged in Go for the list) and `CountUnreadableBooks(library_id, reader_version)` (single library).
  - `api/books.go`: `listBooks` reads `unreadable` into `db.BookFilter.Unreadable`. `getBook` loads the metadata-flow record. If a record exists, `runLazyTiers` runs the backfill even when `needsBackfill` is false, so the fingerprint gate re-checks the file. The view then reloads the record and sets `bookView.Unreadable` when it applies to the current version. Every single-book response (`toSingleBookView`: the detail read and the edit, cover and match writes) carries the marker, so an edit does not drop it from the open detail view.
  - `api/bookview.go`: `Unreadable *unreadableView` with `omitempty`.
- **DB**: `booksfilter.go` gets `BookFilter.Unreadable bool` and `ReaderVersion int64`. When set, it adds the `EXISTS` subquery.
- **SPA**:
  - `types.ts`: `Library.unreadable_count`, `Book.unreadable`, `BookFilters.unreadable`.
  - `api.ts`: `buildQuery` passes `unreadable`.
  - `components/settings/LibraryTable.vue`: the warning link under the book count. Its `href` (`/?unreadable=1&library={id}`) also works opened in a new tab: `BooksPage` adopts `?library=` as the library selection and drops it from the URL.
  - `components/SearchInput.vue`: an "Unreadable" chip.
  - `pages/BooksPage.vue`: the filter from `route.query.unreadable`.
  - `components/BookDetail.vue`: the notice.
  - Vitest specs for each.

### Documentation (in the task that changes the behaviour, constitution "Documentation")

Each phase is its own commit, so each doc change goes in with the behaviour that makes it stale:

- Phase 1: `docs/EBOOK-PARSING.md`: `Source`, the two operations, lazy entries and the cap, the error kinds, `ReaderVersion` and when to bump it. The header-only FB2 read becomes an FB2 implementation detail.
- Phase 1: `docs/DATABASE.md`: folder covers via the lazy flow, the removal of `<data-dir>/tmp`, Deep Index reading every format. `docs/BACKEND.md`: the `ingest` resolver, the constructor signatures, `NewStandardDispatcher`.
- Phase 2: `docs/DATABASE.md`: `book_read_failures`, the outcome and re-check rules, the Deep Index counts.
- Phase 3: `docs/API.md`: the three JSON changes. `docs/FRONTEND.md`: the warning link, the chip and the notice.

### Review comments resolved

| `CLAUDE?` comment | Resolution |
|---|---|
| `fb2.go`: public `ParseFB2Description` in the middle of the package breaks isolation | Removed. The header-only read is the unexported body of the unexported `fb2Parser.Metadata`, reached only through `Dispatcher.Metadata` (FR-004). An AST guard keeps the `ebook` public surface format-agnostic. |
| `deepindex.go`: FB2-only leak, since INPX is not limited to FB2 | Deep Index reads every entry through the resolver + `Dispatcher.Metadata`. EPUB/MOBI entries get the same lazy in-memory path, and the code names no format or library type (FR-006, SC-003, SC-007). |
| `deepindex.go`: `parseFB2: ebook.ParseFB2Description` field | Removed. Tests inject a fake `bookReader` at the consumer interface instead of a format function. |

### Requirement traceability

| Requirement | Plan section |
|---|---|
| FR-001, FR-002, FR-003, FR-004 | Phase 1: ebook layer |
| FR-005, SC-005 | Phase 1: `source.go` lazy entry, removal of `materialize` |
| FR-006, SC-007 | Phase 1: resolver, `readBook`, `Rank`/`Describes`, injected rank, package-wide `TestNoFormatNames` guard |
| FR-007 | Phase 1: `Traits.Describes` |
| FR-008 | Phase 1: `folder.go`, importer lazy-cover invalidation |
| FR-009, SC-001, SC-002 | Technical Context, Performance Goals; [quickstart.md](./quickstart.md) §2 |
| FR-010, FR-011, FR-012 | Phase 2: `outcome.go` classification |
| FR-013, FR-014, FR-015, SC-004 | Phase 2: gate in `readBook`, `book_read_failures`, post-sync re-check |
| FR-016 | Phase 2: callers (retryable records nothing) |
| FR-017 | Phase 2: `DeepIndexResult.Broken`/`KnownBroken` |
| FR-018 | Phase 1: `ebook.ReaderVersion` |
| FR-019 | Unchanged enrichment; Deep Index untouched for network |
| FR-020, FR-021, FR-022, FR-023, FR-024, SC-006 | Phase 3 |
| FR-025 | Phase 1: housekeeping |
| SC-003 | Phase 1: Deep Index reads EPUB/MOBI entries like FB2; quickstart §3 |

## Project Structure

### Documentation (this feature)

```text
specs/002-unified-book-reading/
├── plan.md
├── research.md
├── data-model.md
├── quickstart.md
├── contracts/
│   ├── ebook-reading.md
│   ├── rest-api.md
│   └── spa-ui.md
├── checklists/requirements.md
└── tasks.md            # /speckit-tasks
```

### Source Code (repository root)

```text
internal/
├── ebook/
│   ├── source.go            # new: Source, OpenFile, OpenZipEntry (lazy, capped), TagIO
│   ├── errors.go            # new: ErrSourceIO, ErrTooLarge, ErrMalformed, PanicError, Reason, ReasonOf
│   ├── version.go           # new: ReaderVersion
│   ├── parser.go            # Parser: Metadata/Cover/Traits over Source; parsers unexported
│   ├── dispatch.go          # Metadata/Cover routing, recover, Rank, Describes, Extensions, NewStandardDispatcher, ContentType
│   ├── fb2.go               # header-only Metadata (unexported), streaming Cover; ParseFB2Description removed
│   ├── epub.go, mobi.go, pdf.go
│   ├── ebook.go             # MaxEntryBytes; Format* unexported; Metadata.Cover kept for online providers
│   └── testdata/            # + corrupt/truncated/badcover/archive-entry fixtures
├── ingest/
│   ├── source.go            # new: resolver (library layout → Source + fingerprint), archiveCache
│   ├── readfailure.go       # new: record/clear helpers for the per-book read paths
│   ├── recheck.go           # new: post-sync re-check of stale metadata records
│   ├── outcome.go           # new: ReadStatus/ReadResult, classify, combine (Phase 2)
│   ├── extract.go           # Description/Cover via readBook; temp files removed
│   ├── deepindex.go         # format/library-type free; Broken/KnownBroken
│   ├── backfill.go          # outcome-aware fill
│   ├── folder.go            # metadata-only import
│   ├── importer.go, reconcile.go  # lazy-cover invalidation, failure cleanup on changed files
│   ├── merge.go, driver.go, inpx.go, calibre.go  # filePriority → injected rank func; parsers take the dispatcher
│   └── ingest_test.go       # TestNoFormatNames guard
├── db/
│   ├── migrations/007_book_read_failures.sql   # new
│   ├── queries/readfailures.sql                # new (incl. ListMetadataFailureFiles)
│   ├── queries/books.sql                       # ListDeepIndexWork (paged): all files + failure record; CountDeepIndexWork; ResetCoverState, MarkFileCover
│   ├── queries/libraries.sql                   # ListUnreadableCounts, CountUnreadableBooks (new; existing queries unchanged)
│   └── booksfilter.go                          # Unreadable filter
├── api/
│   ├── libraries.go         # unreadable_count
│   ├── books.go             # unreadable filter param, detail field
│   ├── bookview.go          # unreadableView
│   └── books_test.go        # fakeExtractor follows FileExtractor changes
├── bookfile/bookfile.go     # ContentType delegates to ebook.ContentType (downloads otherwise out of scope)
├── sync/engine.go, parser.go  # re-check on a checkpoint-skipped sync (ReadFailureRechecker)
└── covers/store.go          # MarkFileCover on CoverState; error path already skips cover_state
cmd/folio-idx/main.go        # NewStandardDispatcher, NewExtractor(…, writeGuard) and parser constructor signatures
web/src/
├── types.ts, api.ts
├── components/settings/LibraryTable.vue
├── components/SearchInput.vue
├── components/BookDetail.vue
└── pages/BooksPage.vue
.gitignore                   # .superpowers/
docs/EBOOK-PARSING.md, DATABASE.md, API.md, FRONTEND.md, BACKEND.md
```

**Structure Decision**: This is the existing single Go service with an embedded SPA. There are no new packages. Source construction lives in `ebook`, the reading layer. Library-layout resolution lives in `ingest`, the only package that knows library types.

## Complexity Tracking

No constitution violations.

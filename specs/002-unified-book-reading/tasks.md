---

description: "Task list for Unified Book Reading"
---

# Tasks: Unified Book Reading

**Input**: Design documents from `/specs/002-unified-book-reading/`

**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/, quickstart.md

**Tests**: Every task is implemented test-first, so each task writes its own tests. For Go, those are testify suite methods under the package's single `Test<Package>` runner, whose entry point only calls `suite.Run`. For the SPA, they are Vitest specs. There are no standalone test tasks. Each task names the tests it must write.

**Docs**: the constitution requires docs to change in the same change as the behaviour. Each task that makes a `docs/*.md` passage stale updates it itself. The Polish phase keeps only a residual cross-check.

**Organization**: The three delivery phases from plan.md map to the three user stories: parser unification (US1), read outcomes and re-check rules (US2), and UI (US3). A short Setup phase comes first and a Polish phase comes last. Each phase is one feature commit. **Every phase leaves `task build`, `task test` and `task lint` green.** A task that changes a signature updates every caller listed in it (the lists come from a grep of the current tree).

## Format: `[ID] [P?] [Story] Description`

- **[P]**: can run in parallel (different files, no dependency on an incomplete task)
- **[Story]**: US1 / US2 / US3 (spec.md user stories)

---

## Phase 1: Setup

**Purpose**: Housekeeping and shared fixtures.

- [x] T001 Add `.superpowers/` to `.gitignore` (FR-025). Verify that `git status` no longer lists `.superpowers/`.
- [x] T002 [P] Extend the committed fixture generators. Regenerate the fixtures and commit both the generators and the output. Each package generates its own fixtures, so no test reads another package's `testdata`.
  - `internal/ebook/testdata/gen.go` adds:
    - `corrupt.fb2`: XML truncated inside `<description>`.
    - `truncated.epub`: the zip cut before the central directory.
    - `badcover.fb2`: a valid description; the coverpage `l:href="#cover.jpg"` points at a `<binary id="cover.jpg" content-type="image/jpeg">` whose content is **invalid base64** (contains `!!`).
    - `oddcover.fb2`: the same, but the `<binary>` holds **valid base64 of non-image bytes**.
    - `badcover.epub`: a valid OPF whose manifest cover item exists, but whose zip entry has a corrupted deflate stream (flip bytes after writing, so the CRC check fails).
    - `entries.zip`: an archive holding `big.fb2` (a `<description>` with an annotation at the start, followed by a `<binary>` larger than 1 MiB), `book.epub` (with `dc:description`), `book.mobi` (EXTH description) and `book.pdf`.
  - `internal/ingest/testdata/deepindex/gen.go` adds archive `c.fb2-000007-000010.zip` and its `.inp` lines in `library.inpx`:
    - `7.epub`: a valid EPUB with a description;
    - `8.mobi`: a valid MOBI with an EXTH description;
    - `9.epub`: a truncated EPUB;
    - `10.fb2`: the `big.fb2` shape.

  Tests: extend `deepIndexFixturesSuite.TestINPXListsExpectedBooks` (`internal/ingest/deepindex_fixtures_test.go`) with the four new titles and source paths (`Added` becomes 10). Existing `deepIndexSuite.SetupTest` copies only archives a and b, so its INPX sync still adds 6 (entries of a missing archive are skipped).

---

## Phase 2: User Story 1 — Every book is read the same way (Priority: P1) 🎯 MVP

**Goal**: One random-access `ebook.Source` and two operations (`Metadata`, `Cover`) for every format and library type. No temp files. No format names in `internal/ingest`/`internal/covers`. A format-agnostic `ebook` public surface. Folder covers come from the lazy flow (plan.md "Phase 1"). This resolves the `CLAUDE?` review comments in `internal/ebook/fb2.go` and `internal/ingest/deepindex.go`.

**Independent Test**: Run Deep Index on the fixture archive collection. FB2, EPUB and MOBI entries gain descriptions, and PDF is checked without being parsed. No file appears under the data dir. `ingestSuite.TestNoFormatNames` and `ebookSuite.TestPublicSurfaceFormatAgnostic` pass.

- [x] T003 [P] [US1] Add the error kinds, the reader version and the `Traits` type to `internal/ebook`, per contracts/ebook-reading.md. This is purely additive.
  - New file `internal/ebook/errors.go`:
    - `var ErrSourceIO = errors.New("ebook: source read failed")`
    - `var ErrTooLarge = errors.New("ebook: entry exceeds size cap")`
    - `var ErrMalformed = errors.New("ebook: malformed content")`
    - `type PanicError struct{ Value any }`, with `Error()`
    - `type Reason string`, with `ReasonCorrupt = "corrupt"`, `ReasonMissingEntry = "missing_entry"`, `ReasonTooLarge = "too_large"`, `ReasonCrashed = "crashed"`
    - `func ReasonOf(err error) (Reason, bool)`: `ErrTooLarge` → too_large, `*PanicError` → crashed, `ErrMalformed` → corrupt. `ErrSourceIO`, `context.Canceled`, `context.DeadlineExceeded`, `fs.ErrNotExist`, `fs.ErrPermission` and unknown errors → `("", false)`. `ErrSourceIO` wins when an error matches both.
  - New file `internal/ebook/version.go`: `const ReaderVersion = 1`. Its doc comment says: "Bump it in the same change as a reader fix that could make a previously broken file readable."
  - In `internal/ebook/parser.go`, add `type Traits struct{ Rank int; Describes bool }`. No interface change yet.

  Tests: a new `errorsSuite` wired into the existing `TestEbook` runner, with a table test of `ReasonOf` over wrapped (`fmt.Errorf("…: %w", …)`) instances of every kind.
- [x] T004 [US1] Add the random-access source to `internal/ebook`. This is purely additive.
  - New file `internal/ebook/source.go`:
    - `type Source struct{ Name string; R io.ReaderAt; Size int64 }`
    - `func OpenFile(path string) (Source, io.Closer, error)`: `os.Open` + `Stat`. `R = TagIO(f)` and `Name = filepath.Base(path)`.
    - `func TagIO(r io.ReaderAt) io.ReaderAt`: wraps every non-EOF read error so that it matches `ErrSourceIO`, keeping the original in the chain.
    - `func OpenZipEntry(f *zip.File, limit int64) (Source, io.Closer)`:
      - `Name = path.Base(f.Name)` and `Size = int64(f.UncompressedSize64)`.
      - `R` is a mutex-guarded lazy reader: it opens `f.Open()` on the first `ReadAt`, inflates sequentially until `off+len(p)` (or EOF) into an in-memory prefix buffer, and serves reads from that buffer.
      - Once the buffer would exceed `limit` it returns `ErrTooLarge`.
      - A stream that ends before `Size`, or a flate or checksum error, → `ErrMalformed` (unless the error matches `ErrSourceIO`).
      - Offsets ≥ `Size` → `io.EOF`. `Close` releases the decompressor and the buffer.
      - An unexported `inflated() int64` accessor exists for tests.
  - In `internal/ebook/ebook.go`, add `const MaxEntryBytes int64 = 64 << 20` and leave `maxArchiveTextBytes`/`maxCoverBytes` unchanged. There is no global override: callers pass the limit, so tests lower it explicitly.

  Tests (`sourceSuite`, using `testdata/entries.zip`):
  - reading the first 4 KiB of `big.fb2` keeps `inflated()` below 64 KiB;
  - backward `ReadAt` after a forward read serves from the buffer without reopening;
  - concurrent `ReadAt` under `-race`;
  - `OpenZipEntry(f, 1024)` on `book.epub`, read to the end → `ErrTooLarge`, and `inflated() ≤ 1024 + one chunk`;
  - a truncated entry → `ErrMalformed`;
  - `TagIO` over a failing `ReaderAt` → `errors.Is(err, ErrSourceIO)`;
  - `OpenFile` on a missing path → `fs.ErrNotExist` and not `ErrSourceIO`.
- [x] T005 [P] [US1] Add Source-based operations to the FB2 parser in `internal/ebook/fb2.go`. They are additive methods on the existing `FB2Parser` type (unexported in T014); `Parse` stays for now.
  - `Traits()` returns `Traits{Rank: 3, Describes: true}`.
  - `Metadata(ctx, src Source) (Metadata, error)`:
    - routes `.fb2.zip` by `src.Name`: `zip.NewReader(src.R, src.Size)`, then the first `.fb2` entry, then `OpenZipEntry(entry, MaxEntryBytes)`, then the same path;
    - runs the header-only decode over `bufio.NewReader(io.NewSectionReader(src.R, 0, src.Size))`, capped at `maxArchiveTextBytes`;
    - moves the body of `ParseFB2Description` into the unexported `parseFB2Header(r io.Reader) (Metadata, error)`, and turns `ParseFB2Description` into a one-line wrapper (removed in T014);
    - never sets `Metadata.Cover`.
  - `Cover(ctx, src Source) ([]byte, error)`:
    - token-scans `<description>` for the coverpage `l:href`/`xlink:href`, then decodes only the `<binary>` with that id;
    - no coverpage, or no matching binary → `(nil, nil)`;
    - invalid or truncated base64 → an error (new unexported `decodeFB2Binary(s) ([]byte, error)`; the old `decodeBase64Binary` stays for `Parse` until T014);
    - cover bytes are capped at `maxCoverBytes`.

  Tests (`fb2Suite`):
  - `Metadata` over `OpenFile` of `test.fb2`, `test_win1251.fb2` and `test.fb2.zip` equals the old `Parse` result with `Cover` cleared;
  - `Metadata` over `OpenZipEntry(big.fb2)` keeps `inflated()` below 64 KiB;
  - `Cover` equals the old `Parse(...).Cover` for `test.fb2`/`test.fb2.zip`;
  - `Cover` of `test_nocover.fb2` → `(nil, nil)`;
  - `Cover` of `badcover.fb2` → an error;
  - `Cover` of `oddcover.fb2` → non-image bytes and no error;
  - `corrupt.fb2` → a `Metadata` error.
- [x] T006 [P] [US1] Add Source-based operations to the EPUB parser in `internal/ebook/epub.go`. They are additive methods on `EPUBParser`.
  - `Traits()` returns `{Rank: 4, Describes: true}`.
  - Split `parseEPUB` into a shared `readEPUBPackage(zr *zip.Reader) (opfPackage, string, error)`.
  - `Metadata(ctx, src)`: `zip.NewReader(src.R, src.Size)`, then `readEPUBPackage`, with the same field mapping and no cover.
  - `Cover(ctx, src)`:
    - no cover id or manifest item → `(nil, nil)`;
    - a manifest cover item whose zip entry is missing or fails to read → an error, via a new `epubCover(zr, pkg, opfPath) ([]byte, error)`; `extractEPUBCover` stays for `Parse` until T014.

  Tests (`epubSuite`):
  - `Metadata` equals the old `Parse` with `Cover` cleared for `test.epub`, and `Cover` equals the old cover;
  - `truncated.epub` → a `Metadata` error;
  - `badcover.epub` → `Metadata` OK and `Cover` error;
  - `Metadata` over `OpenZipEntry(entries.zip/book.epub)` returns its description.
- [x] T007 [P] [US1] Add Source-based operations to the MOBI parser in `internal/ebook/mobi.go`. They are additive methods on `MOBIParser`.
  - `Traits()` returns `{Rank: 2, Describes: true}`.
  - Factor `readEXTH` so the EXTH 201 cover offset is stored on `mobiFile`, not decoded.
  - `Metadata(ctx, src)`: `openMOBI(src.R, src.Size)`, then `readTitle` + `readEXTH` + `html.UnescapeString`, without `extractCover`.
  - `Cover(ctx, src)`: `openMOBI` + `readEXTH`, then `extractCover`. An offset outside the records → `(nil, nil)`.

  Tests (`mobiSuite`):
  - `Metadata`/`Cover` over `OpenFile` of `test.mobi`, `test.azw3` and `test_cp1252.mobi` match the old `Parse` (minus the cover, plus the cover);
  - `Metadata` over `OpenZipEntry(entries.zip/book.mobi)` returns its description.
- [x] T008 [P] [US1] Add Source-based operations to the PDF parser in `internal/ebook/pdf.go`. They are additive methods on `PDFParser`.
  - `Traits()` returns `{Rank: 1, Describes: false}`.
  - `Metadata(ctx, src)`: `pdfcpuapi.ReadContext(ctx, io.NewSectionReader(src.R, 0, src.Size), …)`, the info dict and `Pages`, without `coverFromContext`.
  - `Cover(ctx, src)`: the same read, then `coverFromContext`.

  Tests (`pdfSuite`): `Metadata` of `test.pdf` has `Pages > 0` and an empty `Cover`; `Cover` returns PNG bytes (move the existing assertion here).
- [x] T009 [US1] Switch the `Parser` interface and the `Dispatcher` to Sources, and add the standard parser set, in `internal/ebook/parser.go` and `dispatch.go`.
  - Interface: `Extensions() []string`, `Traits() Traits`, `Metadata(ctx, Source) (Metadata, error)`, `Cover(ctx, Source) ([]byte, error)`. Keep `Parse(ctx, path)` on the interface until T014.
  - Add to the Dispatcher:
    - `Metadata(ctx, log, src Source)` and `Cover(ctx, log, src Source)`. Each:
      1. returns a wrapped `ctx.Err()` before touching `src`;
      2. routes by `fileExt(src.Name)`;
      3. recovers a panic into `&PanicError{Value: r}`;
      4. passes through errors matching `ErrSourceIO`, `ErrTooLarge`, `context.Canceled` or `context.DeadlineExceeded`;
      5. wraps any other error as `fmt.Errorf("%s parser: %w: %w", format, ErrMalformed, err)`.
    - `Rank(format string) int`, `Describes(format string) bool` (keyed by the stored label, e.g. "azw3" → MOBI; unknown → `0`/`false`), and `Extensions() []string`.
  - Add `func NewStandardDispatcher() *Dispatcher` (EPUB, FB2, MOBI, PDF) and `func ContentType(format string) string`, which moves the MIME switch out of `bookfile`.
  - Callers to update in this task:
    - `cmd/folio-idx/main.go` (`ebook.NewStandardDispatcher()`);
    - `internal/ingest/ingest_test.go` (replaces `ebook.NewDispatcher(ebook.NewEPUB(), …)`);
    - `internal/bookfile/bookfile.go` (`ContentType` delegates to `ebook.ContentType`);
    - `internal/ebook/dispatch_test.go` (`panicParser` gains `Traits`/`Metadata`/`Cover`).
  - Doc: in `docs/EBOOK-PARSING.md`, add the `Source`, `Metadata`/`Cover` and `Traits` sections (the old API is still described until T014).

  Tests (`dispatchSuite`):
  - routing for every extension, including `.fb2.zip`;
  - a cancelled ctx never calls the parser;
  - a panicking fake → `*PanicError` and `ReasonOf` = crashed;
  - `ErrSourceIO` passes through and `ReasonOf` is false;
  - a generic error → `ErrMalformed`;
  - `Rank("epub") > Rank("fb2") > Rank("mobi") == Rank("azw3") > Rank("pdf")`;
  - `Describes("pdf") == false`;
  - `ContentType` for every label, plus the unknown default;
  - `bookfileSuite` still passes.
- [x] T010 [US1] Add the library-layout resolver and switch `Extractor` to Sources, with no temp files.
  - New file `internal/ingest/source.go`:
    - `type resolver struct{ entryLimit int64 }` (production `ebook.MaxEntryBytes`; tests lower it).
    - `open(library dbq.Library, file dbq.BookFile, ac *archiveCache) (ebook.Source, io.Closer, error)` and `group(library, file) string`. This is the only reading code that knows archive-collection books live in sibling archives (`filepath.Join(filepath.Dir(library.Path), archiveName)`). Other library types join `library.Path` + `file.SourcePath`.
    - A new ingest-local `withinRoot(root, full string) bool` rejects any stored `source_path` that escapes its root (`../`).
    - `type archiveCache` opens each archive once (`os.Open` → `ebook.TagIO` → `zip.NewReader`) and has `close()`.
    - A missing inner entry → the unexported sentinel `errMissingEntry`.
  - `internal/ingest/extract.go`:
    - rename the interface `bookParser` to `bookReader` (contracts/ebook-reading.md), keeping `Parse` until T012;
    - delete `tmpDir`, `materialize`, `parseFromZip`, `parseFile`, `acceptAnyFormat` and `acceptNonPDF`;
    - add `readBook(ctx, flow readFlow, library, files []dbq.BookFile, ac *archiveCache)`. It orders files by `reader.Rank(format)` descending (ties by id), skips files that are not `reader.Supported(SourcePath)` (and, for `flowMetadata`, those that are not `reader.Describes(format)`), and returns the first success. For now, an error from the top candidate is returned as today.
    - `Backfill` is renamed `Description(ctx, bookID) (ebook.Metadata, bool, error)`. It returns all parsed metadata (annotation, identifiers, …), not only the description.
    - `Cover(ctx, bookID)` calls `reader.Cover`.
    - `NewExtractor(db, log, reader bookReader)` drops `dataDir`.
  - Callers to update in this task:
    - `internal/ingest/backfill.go` (`FileExtractor.Backfill` → `Description`);
    - `internal/ingest/backfill_test.go`;
    - `internal/ingest/deepindex.go` (`extract` calls `Description`);
    - `internal/ingest/deepindex_test.go` (fake extractor);
    - `internal/ingest/folder.go` (`NewFolderParser(log, parser bookReader)`);
    - `internal/ingest/folder_test.go` (fake implements `bookReader`);
    - `internal/ingest/extractor_test.go`;
    - `internal/api/books_test.go` (`fakeExtractor.Backfill` → `Description`);
    - `cmd/folio-idx/main.go` (`ingest.NewExtractor(database, log, parser)`).
  - Docs: `docs/DATABASE.md` (the `<data-dir>/tmp` materialize is gone, and archive entries are read in memory) and `docs/BACKEND.md` (the resolver, the `NewExtractor` signature).

  Tests (`extractorSuite`):
  - a folder EPUB, an archive FB2, an archive EPUB (`7.epub`) and an archive MOBI (`8.mobi`) each return their description;
  - a PDF-only book → `ok=false`, and the counting fake `bookReader` saw no `Metadata` call;
  - a multi-file book reads the highest-ranked file;
  - `Cover` for an archive EPUB returns bytes;
  - a stored `source_path` of `../x.fb2` is rejected;
  - nothing is created under the data dir (`t.TempDir()`);
  - the existing backfill, deep-index, folder and api suites stay green.
- [x] T011 [US1] Make Deep Index free of formats and library types.
  - `internal/db/queries/books.sql` + `task generate`: `ListDeepIndexWork` returns **every** file of each book with `metadata_checked = 0` (`book_id, file_id, file_format, source_path`, with the `imported_at` narg), `ORDER BY source_path, book_id`. The `CASE` ranking is removed. Callers: `internal/ingest/deepindex.go` and `internal/db/deepindex_test.go` (update the expectations: every file returned, in `source_path` order).
  - `internal/ingest/extract.go`: `newSession() *readSession` (owns an `archiveCache`), plus `(*readSession).description(ctx, library, files)` (on `readBook`) and `close()`.
  - `internal/ingest/deepindex.go`:
    - `NewDeepIndexer(log, db, guard, extractor *Extractor)`, with no extra interface. Callers: `cmd/folio-idx/main.go` (unchanged call) and `deepindex_test.go`, which injects a counting fake `bookReader` into a real `Extractor`.
    - Group work rows by book, then group books into jobs by `resolver.group` of the top-ranked file, with one session per job per worker.
    - Books whose files are all non-describing, or have no supported format, are marked checked without reading.
    - Delete `parseFB2`, `inpxItem`, `parseEntry`, `deepIndexArchive`, `isPDF` and the three `CLAUDE?` comments.
  - `internal/ingest/deepindex_test.go`: `SetupTest` also copies archive `c.fb2-000007-000010.zip` (`Added` becomes 10), and every exact `DeepIndexResult{…}` assertion is updated.
  - Doc: in `docs/DATABASE.md`, the Deep Index section describes every format being read through the dispatcher, one archive open per job, and no FB2 special case.

  Tests (`deepIndexSuite`):
  - `7.epub` and `8.mobi` are filled (SC-003), FB2s are filled as before, and the PDF-only book is checked without being read;
  - `10.fb2` inflates less than 64 KiB;
  - each archive is opened once per job (counting hook on `archiveCache`);
  - a cancelled run keeps committed batches.
- [x] T012 [US1] Make folder import metadata-only, and invalidate lazy covers generically.
  - `internal/ingest/folder.go`: `folderWalk.visit` opens `ebook.OpenFile(path)`, calls `reader.Metadata(ctx, log, src)`, then closes. `recordFromMeta` no longer sets `Cover`. Unparseable files are still skipped. `bookReader` drops `Parse`; update the fake in `folder_test.go`.
  - `internal/ingest/reconcile.go`: `upsert` computes `changed := !existed || prev.FileSize != rec.FileSize || prev.Mtime != rec.Mtime` and passes it to `importer.add`. Callers of `add`: `reconcile.go` and every test that calls it directly (`folder_test.go`, `ingest_test.go`, `importer_merge_test.go`, `importer_idmatch_test.go`, `importer_idlog_test.go`, `importer_manualcover_test.go`, `identifier_lookup_test.go`). Re-grep `.add(ctx` before finishing.
  - `internal/ingest/importer.go`:
    - `add(ctx, rec, now, changed bool)`. When `changed && len(rec.Cover) == 0 && filePriority(rec.FileFormat) >= persistedPrio`, queue `coverOp{bookID, invalidate: true}`.
    - `flushPendingCovers` runs it after commit: `covers.Delete(bookID)`, then the new sqlc query `ResetCoverState` (`UPDATE books SET cover_state = 0, cover_prio = 0 WHERE id = ? AND cover_prio < 1000`, in `internal/db/queries/books.sql`, followed by `task generate`).
    - Manual covers (prio 1000) are never touched.
  - Doc: in `docs/DATABASE.md`, folder covers come from the lazy flow, plus the invalidation rule.

  Tests (`folderSuite`, `importerSuite`):
  - a folder import makes no `Cover` call (counting fake) and leaves `cover_state = 0`;
  - touching a file and re-syncing deletes the cached cover and resets the state;
  - a manual cover (prio 1000) survives;
  - Calibre `cover.jpg` bytes are still saved;
  - a rolled-back batch leaves the cover untouched.
- [x] T013 [US1] Replace `filePriority` with an injected rank, and add the package-wide format-name guard.
  - `internal/ingest/merge.go`: delete `filePriority`.
  - `internal/ingest/importer.go`: the `importer` gains `rank func(format string) int`, and `newImporter(log, db, covers, batchSize, rank)`.
  - `internal/ingest/driver.go`: `runReconcile(…, rank)`.
  - `internal/ingest/inpx.go`: `NewINPXParser(log, rank)`. `internal/ingest/calibre.go`: `NewCalibreParser(log, rank)`. `internal/ingest/folder.go`: `NewFolderParser(log, reader)` uses `reader.Rank`.
  - Callers to update in this task:
    - `cmd/folio-idx/main.go` (`parser.Rank`);
    - `internal/ingest/ingest_test.go`, `deepindex_test.go`, `deepindex_fixtures_test.go`, `folder_test.go`;
    - `importer_merge_test.go`, `importer_idmatch_test.go`, `importer_idlog_test.go`, `importer_manualcover_test.go`, `identifier_lookup_test.go`;
    - any `ebook.Format*` reference in `internal/ingest/*_test.go`, if one exists (a current grep finds none), becomes a string literal in the test.
  - Guard `ingestSuite.TestNoFormatNames` (in `internal/ingest/ingest_test.go`):
    - parse with `go/parser` every non-test `.go` file under `internal/ingest` and `internal/covers` (walk the directories; skip `testdata` and `_test.go`);
    - build the label set from `ebook.NewStandardDispatcher().Extensions()` plus `Format(ext)` for each, lower-cased, with and without the leading dot (`epub`, `fb2`, `fb2.zip`, `mobi`, `azw`, `azw3`, `pdf`, …);
    - fail on any `*ast.BasicLit` string whose lower-cased value equals a label or contains `.<label>`. Identifier-scheme literals such as `"mobi-asin"`, `"mobi-uid"` and `"amazon-asin"` (`identifier.go`) are allowed by policy, because they name an identifier scheme from external metadata, not a format. The exact-or-dotted rule passes them, and the self-test asserts it;
    - fail on any selector into package `ebook` that names a format constant or format-specific identifier (`ebook.Format*`, `ebook.New<Format>`, `ebook.<Format>Parser`, `ebook.ParseFB2Description`);
    - words inside identifiers are not matched (`fb2GenreMap` in `genres.go` is shared genre vocabulary and stays as it is), and comments are not checked.
  - Doc: in `docs/BACKEND.md`, the constructor signatures and the injected rank.

  Tests: the guard itself, run over the real tree, plus a self-test on an in-memory source. `".azw3"`, `"pdf"` as a compared literal (`format == "pdf"`) and an `ebook.FormatPDF` reference are reported. `fb2GenreMap`, `calibrePublishersQuery` and `"mobi-asin"` are not. Existing merge, importer and cover-priority suites stay green with the injected rank.
- [x] T014 [US1] Make the `ebook` public surface format-agnostic, and remove the path-based API.
  - In `internal/ebook/*.go`:
    - remove `Parser.Parse`, `Dispatcher.Parse`, the per-format `Parse` methods, `parseFB2(path)`/`parseFB2Zip`/`parseEPUB(path)`/`parseMOBI(path)`/`parsePDF(path)`, `decodeBase64Binary`/`extractEPUBCover` (superseded in T005/T006), `ParseFB2Description` and its `CLAUDE?` comment;
    - unexport `FB2Parser`/`EPUBParser`/`MOBIParser`/`PDFParser` (→ `fb2Parser`, …) and delete `NewEPUB`/`NewFB2`/`NewMOBI`/`NewPDF`, so `NewStandardDispatcher` builds them internally;
    - unexport the `Format*` constants (→ `formatEPUB`, …);
    - keep `Metadata.Cover`, which online providers fill.
  - Callers to update in this task:
    - `internal/bookfile/bookfile.go` (already delegating since T009; drop any remaining `ebook.Format*`);
    - the ebook tests (`ebook_test.go`, `dispatch_test.go`, `fb2_test.go`, `epub_test.go`, `mobi_test.go`, `pdf_test.go`, `metadata_test.go`), migrated to `OpenFile` + `Metadata`/`Cover`.
  - Guard `ebookSuite.TestPublicSurfaceFormatAgnostic`: parse the non-test files of `internal/ebook`, collect every exported identifier (top-level types, funcs, consts, vars, and methods whose receiver type is exported), split it into camelCase words, and fail when a word equals a format label or extension (`epub`, `fb2`, `fbz`, `mobi`, `azw`, `azw3`, `pdf`).
  - Doc: rewrite `docs/EBOOK-PARSING.md` for the final API (`NewStandardDispatcher`, `Source`, `OpenFile`/`OpenZipEntry` and the limit, `Metadata`/`Cover`, the cover-payload error rule, `Traits`, error kinds and `ReasonOf`, `ReaderVersion` and its bump rule). The header-only FB2 read becomes an implementation note, and every `ParseFB2Description`/`Parse(path)`/`NewFB2` mention is removed. Add one sentence in the genre section: `fb2GenreMap` holds the FB2 genre taxonomy (FictionBook XSD codes mapped to canonical BISAC genres), is the source vocabulary every library's genres are mapped through (including the genre filters), and lives in `internal/ingest` because it serves all sources and filtering, not format reading.

  Tests: the guard; a self-test that runs it on an in-memory source with `func NewPDF()` and expects a failure; all ebook suites green on the migrated API.

**Checkpoint**: US1 is complete. Verify the Phase 2 Independent Test and that `task build`/`task test`/`task lint` are green. **Run the quickstart §2 performance measurement now** (US1 changes the description ranking and job grouping); a regression is fixed before US2 starts.

---

## Phase 3: User Story 2 — Broken files are not re-read without reason (Priority: P1)

**Goal**: Each local read ends `done`, `broken` or `retryable`. Broken reads are recorded once per book and flow, and skipped while their fingerprint and `ebook.ReaderVersion` still match (plan.md "Phase 2").

**Independent Test**: The corrupt fixture is recorded once. A re-run reports `errors = 0`, `broken = 0`, `known_broken ≥ 1` without opening the entry. A changed fingerprint re-reads it. A missing archive and a cancelled run record nothing. A `badcover.fb2` cover is recorded once and not re-read.

- [x] T015 [US2] Add migration `internal/db/migrations/007_book_read_failures.sql` and `internal/db/queries/readfailures.sql` (new), then run `task generate`. The DDL, verbatim from data-model.md:

  ```sql
  CREATE TABLE book_read_failures
  (
      book_id        INTEGER NOT NULL,
      flow           TEXT    NOT NULL CHECK (flow IN ('metadata', 'cover')),
      reason         TEXT    NOT NULL CHECK (reason IN ('corrupt', 'missing_entry', 'too_large', 'crashed')),
      detail         TEXT    NOT NULL DEFAULT '',
      fingerprint    TEXT    NOT NULL,
      reader_version INTEGER NOT NULL,
      recorded_at    INTEGER NOT NULL,
      PRIMARY KEY (book_id, flow),
      FOREIGN KEY (book_id) REFERENCES books (id) ON DELETE CASCADE
  ) WITHOUT ROWID;

  CREATE INDEX idx_book_read_failures_flow_version
      ON book_read_failures (flow, reader_version, book_id);
  ```

  The Down migration is `DROP TABLE book_read_failures;`.

  Queries (all new, so no existing callers):
  - `UpsertReadFailure` (`INSERT … ON CONFLICT(book_id, flow) DO UPDATE SET reason, detail, fingerprint, reader_version, recorded_at`)
  - `GetReadFailure(book_id, flow)`
  - `DeleteReadFailure(book_id, flow)`
  - `DeleteReadFailuresForBook(book_id)`

  Doc: in `docs/DATABASE.md`, the schema section for `book_read_failures`.

  Tests (`internal/db`):
  - goose up/down round trip;
  - the `CHECK` constraints reject an unknown flow or reason;
  - an upsert replaces the row;
  - deleting a book and purging its library both cascade.
- [x] T016 [US2] Add outcome classification and fingerprints in `internal/ingest/outcome.go` (new) and `internal/ingest/source.go`.
  - Exported types:
    - `type ReadStatus int`, with `ReadDone`, `ReadBroken`, `ReadRetryable`;
    - `type ReadResult struct{ Status ReadStatus; Reason ebook.Reason; Detail string; Fingerprint string; Known bool; Err error }`.
  - Unexported `type readFlow string`, with `flowMetadata = "metadata"` and `flowCover = "cover"`.
  - `classify(err error) ReadResult`:
    - `errMissingEntry` → broken `missing_entry`;
    - `ebook.ReasonOf` true → broken with that reason;
    - anything else (open/stat errors, `ebook.ErrSourceIO`, ctx errors, the `WriteGuard.Lock` error under a budget ctx) → retryable;
    - `Detail` = `err.Error()`, truncated to 256 bytes.
  - `combine(results []ReadResult) ReadResult` (multi-file books):
    - the first done wins;
    - else any retryable → retryable;
    - else broken with the top-ranked candidate's reason.
  - `resolver.fingerprint(library, files, ac) (string, error)`: hex of the first 16 bytes of `sha256` over `source_path|size|mtime_unix` lines in rank order. A plain file uses `os.Stat`; a present entry uses the zip header's `UncompressedSize64` and `Modified`; a missing entry uses the archive's `os.Stat`. It never inflates; stat or open failures are returned as errors (callers treat them as retryable).

  Tests (`outcomeSuite`):
  - `classify` table over every row of research.md R6;
  - `combine` cases;
  - the fingerprint is stable, changes on an mtime change, on a replaced entry, and on a file added to the book, and reads no entry data (counting `ReaderAt`).
- [x] T017 [US2] Add the record gate to `readBook`, and make reads return `ReadResult`.
  - `internal/ingest/extract.go`:
    - `readBook` loads `GetReadFailure(book, flow)` first. If `reader_version == ebook.ReaderVersion` and the recomputed fingerprint is equal, it returns `ReadResult{Status: ReadBroken, Known: true, Reason: …}` without opening any candidate. Otherwise it reads, classifies each candidate, combines the results, and sets `Fingerprint`.
    - `Description(ctx, bookID) (ebook.Metadata, ReadResult)`, and `(*readSession).description(ctx, library, files) (ebook.Metadata, ReadResult)`.
  - Callers to update in this task, keeping behaviour equivalent for now (non-done is treated like today's error):
    - `internal/ingest/backfill.go` (`FileExtractor.Description` signature);
    - `internal/ingest/backfill_test.go`;
    - `internal/ingest/deepindex.go` (maps `readSession.description` non-done to `err`);
    - `internal/ingest/deepindex_test.go`;
    - `internal/ingest/extractor_test.go`;
    - `internal/api/books_test.go` (`fakeExtractor.Description` returns `(ebook.Metadata, ingest.ReadResult)`).

  Tests (`extractorSuite`):
  - an applicable record → known broken, and the fake reader is not called;
  - a stale version → the file is read; a stale fingerprint → the file is read;
  - corrupt fixture → broken `corrupt`; `9.epub` → broken `corrupt`;
  - `resolver.entryLimit = 1024` on `7.epub` → broken `too_large`;
  - a missing entry → `missing_entry`;
  - a renamed-away archive → retryable.
- [x] T018 [US2] Make the on-view fill outcome-aware in `internal/ingest/backfill.go`. `LocalBackfiller.fill` handles each result:
  - **done**: as today (annotation if NULL, identifiers, `MarkMetadataChecked`), plus `DeleteReadFailure(book, metadata)` in the same guarded section.
  - **broken, not `Known`**: under the guard with `backfillAcquireBudget`, call `UpsertReadFailure` (flow `metadata`, the reason, the detail, the fingerprint, `ebook.ReaderVersion`, `time.Now().Unix()`). Log one `Warn("unreadable book file", book, flow, reason)`. The book stays unchecked.
  - **broken, `Known`**: nothing, and no log line.
  - **retryable**: `Debug` only, and no write (FR-016). A busy guard skips the write.

  Doc: in `docs/DATABASE.md`, the outcome and re-check rules for the on-view fill.

  Tests (`backfillSuite`, and `booksSuite` in `internal/api/books_test.go` with the fake returning each status):
  - the corrupt book gets one row and one Warn;
  - a second `Fill` reads nothing and logs nothing (captured slog handler);
  - after the file is fixed, the row is deleted and the book is checked;
  - retryable → no row.
- [x] T019 [US2] Make Deep Index outcome-aware and count it.
  - `internal/db/queries/books.sql` + `task generate`: `ListDeepIndexWork` LEFT JOINs `book_read_failures` (`flow = 'metadata'`) and returns the nullable `failure_fingerprint` and `failure_reader_version`. Callers: `internal/ingest/deepindex.go`, `internal/db/deepindex_test.go`.
  - `internal/ingest/deepindex.go`:
    - the worker passes those columns into the gate, so there is no query per book;
    - the writer batch, inside `WithTx`: done → persist as today + `DeleteReadFailure` + `MarkMetadataChecked`; new broken → `UpsertReadFailure`, plus one Warn per book after commit; known broken or retryable → no write.
  - `DeepIndexResult` gains `Broken int` and `KnownBroken int`; `Errors` counts retryable only. The `deep index finished` log adds `broken` and `known_broken`. Each committed book lands in exactly one of Checked / Broken / KnownBroken / Errors. Callers of `DeepIndexResult` literals: `internal/ingest/deepindex_test.go`. `internal/sync` only reads `Run`'s error, so it needs no change.
  - Doc: in `docs/DATABASE.md`, the Deep Index counts and skip rule.

  Tests (`deepIndexSuite`):
  - first run on the fixtures: `Broken = 2` (`4.fb2`, `9.epub`), with rows written;
  - second run: `Broken = 0`, `Errors = 0`, `KnownBroken = 2`, and no entry is opened for those books (SC-002, SC-004);
  - an archive replaced with a fixed `4.fb2` → re-read, filled, row deleted;
  - a missing archive → `Errors > 0` and no rows;
  - cancellation adds no rows.
- [x] T020 [US2] Record cover-flow failures in `Extractor.Cover` (`internal/ingest/extract.go`), with `NewExtractor(db, log, reader, guard *db.WriteGuard)`. Callers: `cmd/folio-idx/main.go`, `internal/ingest/extractor_test.go`, `internal/ingest/deepindex_test.go`, `internal/ingest/backfill_test.go`.
  - `Cover` runs `readBook(flowCover)`:
    - done → delete any `cover` row (guarded, best-effort) and return the bytes (`(nil, false, nil)` when there is no cover);
    - new broken → upsert a `cover` row (guarded, best-effort, one Warn) and return an error wrapping `ebook.ErrMalformed`;
    - known broken → the same error, without reading;
    - retryable → the error.
  - `covers.CoverExtractor` and `internal/covers/store.go` are unchanged: on error `lazyExtract` serves the placeholder and leaves `cover_state` at 0; a `convertToJPEG` failure still marks `StateNone`.
  - Doc: in `docs/DATABASE.md`, the cover-flow rule (a cover payload that cannot be extracted is broken; a non-image payload is none).

  Tests (`extractorSuite`, plus a `coversSuite` case over a real `Extractor`):
  - folder `badcover.fb2` → one `cover` row and the placeholder; the second serve does not call the reader; `cover_state` stays 0; not counted as unreadable;
  - `badcover.epub` → one `cover` row;
  - `oddcover.fb2` → no row and `cover_state = 2`;
  - touching `badcover.fb2` (new fingerprint) re-reads it.
- [x] T021 [US2] Drop stale records when sync sees a file change, in `internal/ingest/importer.go`. When `changed` (T012) is true for an upserted file, call `DeleteReadFailuresForBook(bookID)` in the import transaction.

  Tests (`importerSuite`): a book with metadata and cover rows loses both when its folder file's mtime changes; an unchanged re-sync keeps them.

**Checkpoint**: US2 is complete. Verify the Phase 3 Independent Test and quickstart §3 rows 1–7b (manual), with `task test` green.

---

## Phase 4: User Story 3 — The user sees which books are unreadable and why (Priority: P2)

**Goal**: An unreadable count per library, an `unreadable` book filter, and a book-page notice (plan.md "Phase 3", contracts/rest-api.md, contracts/spa-ui.md).

**Independent Test**: With one broken fixture book, the library row shows "⚠ 1 unreadable". The link lists exactly that book. Its page shows the notice. After the file is fixed and re-read, both disappear.

- [x] T022 [US3] Add `unreadable_count` to library responses, using new queries only (existing query signatures are unchanged).
  - `internal/db/queries/libraries.sql` + `task generate`:
    - `ListUnreadableCounts(reader_version)`: `SELECT b.library_id, COUNT(*) AS unreadable_count FROM book_read_failures f JOIN books b ON b.id = f.book_id WHERE f.flow = 'metadata' AND f.reader_version = ? GROUP BY b.library_id`;
    - `CountUnreadableBooks(library_id, reader_version)`.
  - `ListLibrariesWithBookCount` stays as it is, so `internal/mcpserver/tools.go` and `internal/api/libraries_test.go` need no change.
  - `internal/api/libraries.go`: `libraryView.UnreadableCount int64` (`json:"unreadable_count"`). `listLibraries` merges `ListUnreadableCounts(ebook.ReaderVersion)` by library id. `getLibrary`/`respondLibrary`, create, update and reactivate use `CountUnreadableBooks` (0 on create).
  - Doc: `docs/API.md`, the library field.

  Tests (`librariesSuite`):
  - only metadata rows are counted (cover rows ignored);
  - older `reader_version` rows are ignored;
  - another library's rows are ignored;
  - the field is present (0) on every library response;
  - the `mcpserver` suite still passes.
- [x] T023 [US3] Add the `unreadable` book-list filter.
  - `internal/db/booksfilter.go`: `BookFilter` gains `Unreadable bool` and `ReaderVersion int64`. When `Unreadable` is set, add `EXISTS (SELECT 1 FROM book_read_failures f WHERE f.book_id = b.id AND f.flow = 'metadata' AND f.reader_version = ?)` with a bound parameter, in both `FilterBooks` and `CountFilteredBooks`. Existing `BookFilter{…}` literals (`internal/api/books.go`, `internal/opds/feeds.go`, `internal/mcpserver/tools.go`) compile unchanged, because the zero value means off.
  - `internal/api/books.go`: `listBooks` sets `Unreadable: qp.Get("unreadable") == "1"` and `ReaderVersion: ebook.ReaderVersion`.
  - Doc: in `docs/API.md`, the query parameter.

  Tests (`booksFilterSuite`, `booksSuite`):
  - `unreadable=1&library={id}` returns exactly that library's metadata-broken books, and `total` matches;
  - cover-only and stale-version rows are excluded;
  - the filter combines with `q` and `format`;
  - any other value is ignored.
- [x] T024 [US3] Add the book-detail `unreadable` field, and make the on-view fill re-check.
  - `internal/api/bookview.go`: `bookView` gains `Unreadable *unreadableView` (`json:"unreadable,omitempty"`), with `type unreadableView struct{ Reason string \`json:"reason"\` }`.
  - `internal/api/books.go`:
    - `getBook` loads `GetReadFailure(id, 'metadata')` before `runLazyTiers`;
    - `runLazyTiers` runs the backfill when `book.MetadataChecked == 0 && (needsBackfill || recordExists)`;
    - after the tiers, reload the record and set `Unreadable` only when `reader_version == ebook.ReaderVersion`;
    - list items never set it.
  - Doc: in `docs/API.md`, the detail field.

  Tests (`booksSuite`, with rows inserted through `UpsertReadFailure` and a real `LocalBackfiller` over a fake `FileExtractor`):
  - broken detail → `unreadable.reason == "corrupt"`, and the list item has no field;
  - when the fake now returns `ReadDone` (file fixed), the same `GET` omits the field and the row is gone (US3 scenario 5);
  - stale version → omitted;
  - cover-only row → omitted.
- [x] T025 [US3] Add the SPA types and the list filter.
  - `web/src/types.ts`:
    - `Library.unreadable_count: number`;
    - `type UnreadableReason = 'corrupt' | 'missing_entry' | 'too_large' | 'crashed'`;
    - `Book.unreadable?: { reason: UnreadableReason }`;
    - `BookFilters.unreadable?: boolean`.
  - `web/src/api.ts`: `buildQuery` emits `unreadable=1` only when it is true.
  - `web/src/pages/BooksPage.vue`: `filters.unreadable = route.query.unreadable === '1'`.
  - `web/src/components/SearchInput.vue`: an `unreadable` key in `OWNED_KEYS`/`CHIP_META`, rendered as the fixed label `Unreadable` with `badge-warning`. Removing it drops the param.
  - `web/src/test/factories.ts` and `composables/useLibrary.spec.ts` library fixtures gain `unreadable_count: 0`.
  - Doc: in `docs/FRONTEND.md`, the `Unreadable` chip and filter.

  Vitest: `api.spec.ts`, `BooksPage.spec.ts`, `SearchInput.spec.ts` (emitted only when true, reaches `fetchBooks`, the chip renders and can be removed).
- [x] T026 [US3] Add the library-row warning link in `web/src/components/settings/LibraryTable.vue`. When `library.unreadable_count > 0`, render under the book count a link `⚠ {N} unreadable` (`text-warning text-xs`, `data-testid="library-unreadable"`). On click, call `setLibrary(library.id)` from `useLibrary`, then `router.push({ path: '/', query: { unreadable: '1' } })`. Render nothing for 0, and keep the link enabled while the library syncs.

  Doc: in `docs/FRONTEND.md`, the Settings → Libraries warning link.

  Vitest (`LibraryTable.spec.ts`): shown for N > 0 with the right text; hidden for 0; a click sets the library and navigates with `unreadable=1`.
- [x] T027 [US3] Add the book-page notice in `web/src/components/BookDetail.vue`. When `book.unreadable` is set, render in the description section, above any annotation, a `role="status"` element `data-testid="unreadable-notice"` with plain-text interpolation (no `v-html`): "Folio couldn't read this book's file: {label}. Downloading it may still work." The labels:
  - `corrupt` → "The file is damaged or not a valid ebook."
  - `missing_entry` → "The file is missing from its archive."
  - `too_large` → "The file is too large to read."
  - `crashed` → "Folio's reader couldn't process this file."

  There is no retry control.

  Doc: in `docs/FRONTEND.md`, the book-page notice.

  Vitest (`BookDetail.spec.ts`): each reason renders its label; absent when unset; rendered above an existing annotation.

**Checkpoint**: US3 is complete. Verify the Phase 4 Independent Test and quickstart §4 (manual).

---

## Phase 5: Polish & Cross-Cutting Concerns

- [x] T028 [P] Run the residual doc cross-check:
  - grep `docs/`, `README.md` and `CLAUDE.md` for stale mentions (`ParseFB2Description`, `Parse(ctx, path)`, `NewFB2`/`NewEPUB`/`NewMOBI`/`NewPDF`, `filePriority`, `data-dir>/tmp`, `materialize`, "header-only FB2" as a Deep Index rule) and fix them;
  - update `docs/ARCHITECTURE.md` if its directory tree or flows name the removed pieces.

  No behaviour docs are written here; those went in with their tasks.
- [x] T029 Run `task format`, `task lint`, `task test` and `task build`, and fix any findings. Walk quickstart §3 and §4 on a scratch library built from the test fixtures.
- [x] T030 Run the final quickstart §2 performance check:
  - take an online read-only backup of a real database (`sqlite3 'file:<data-dir>/folio.db?mode=ro' ".backup <copy-dir>/folio.db"`), one fresh copy per run;
  - mount the libraries read-only, run the container with `--network none`, and trigger the run through a client sharing the container's network namespace;
  - compare the previous release image and this branch on the same host.

  Record the results in the PR description, without library names, counts or paths:
  - the full-run `duration` ratio (pass: ≤ 1.10, SC-001);
  - the re-run duration (pass: < 1 min, with `errors = 0` and `broken = 0`, SC-002);
  - `filled` versus the previous release.

---

## Dependencies & Execution Order

- **Setup (T001–T002)**: T001 is independent. The T002 fixtures are used by T004–T008, T010–T012 and US2.
- **US1 (T003–T014)**:
  - T003 comes first; T004 needs T003 (error kinds).
  - T005–T008 need T003 (`Traits`, errors) and T004 (`Source`), and run in parallel with each other.
  - T009 needs T005–T008.
  - T010 needs T009. T011 and T012 need T010; run them sequentially, because both edit `extract.go`/`deepindex_test.go`.
  - T013 needs T012. T014 needs T013 (it unexports `Format*`, which `merge.go` used until T013).
- **US2 (T015–T021)** depends on US1:
  - T015 comes first; T016 needs T010 (resolver).
  - T017 needs T015 and T016.
  - T018, T019 and T020 need T017, and run sequentially (shared `extract.go`, `deepindex_test.go` and docs).
  - T021 needs T012 and T015.
- **US3 (T022–T027)** depends on T015 and T017–T019:
  - T022, T023 and T024 run sequentially (shared api test fixtures and `docs/API.md`).
  - T025 needs T022–T024; T026 and T027 need T025, and run sequentially (shared `docs/FRONTEND.md`).
- **Polish (T028–T030)** comes last; T030 needs a release build of the branch.

## Parallel Opportunities

- T002 runs alongside T001; T003 runs alongside T002.
- T005, T006, T007 and T008 (one parser file each in `internal/ebook`) run together after T003 and T004.
- T028 (docs cross-check) runs alongside T029.

## Implementation Strategy

1. **MVP**: Phases 1–2 (US1). Every format is read the same way, from plain files and archives alike, with no temp files. EPUB/MOBI books in archive collections gain descriptions. The `ebook` surface is format-agnostic. It ships on its own with its docs, with the review comments resolved, after the US1 performance checkpoint.
2. **Stop the re-reads**: Phase 3 (US2). Broken files are recorded once and skipped until their file or the reader version changes.
3. **Visibility**: Phase 4 (US3). The warning link, the filter and the notice.
4. **Polish**: Phase 5. Residual docs, gates, and the final real-data performance check.

Phase headings follow `## Phase N: …` for the beads import; each phase is one feature commit.

## Phase 6: Convergence

- [x] T031 Make the MOBI cover read return its read errors instead of treating them as "no cover". In `internal/ebook/mobi.go`, change `readRecordImage` and `findFirstImageRecord`, and through them `extractCover`/`mobiReadCover`, so that a `ReadAt` error matching `ebook.ErrSourceIO`, `ebook.ErrTooLarge` or `ebook.ErrMalformed` is returned. The Dispatcher then classifies it: retryable for source I/O, broken `too_large`/`corrupt` otherwise. Only a record that reads cleanly but is out of range or not an image stays `(nil, nil)`. Add `mobiSuite` tests: `Cover` over a `TagIO`-wrapped failing `ReaderAt` returns an error matching `ErrSourceIO`; `Cover` over `OpenZipEntry(entries.zip/book.mobi, <limit below the cover record>)` returns `ErrTooLarge`. Add an `extractorSuite` test showing that a retryable MOBI cover error leaves `cover_state = 0` and writes no row. Per FR-012, FR-016 (contradicts).
- [x] T032 Classify an oversized declared cover the same way in every parser. Make the EPUB `epubCover`/`readZipEntry` over-limit error wrap `ebook.ErrTooLarge` (`internal/ebook/epub.go:296`). Make the MOBI `recordBounds` over-`maxCoverBytes` case for the EXTH-201 cover record return an `ErrTooLarge` error instead of falling back to the first-image scan (`internal/ebook/mobi.go:398`). Keep FB2 as it is (`fb2.go:431,438`). Add one test per parser asserting `ebook.ReasonOf(err) == ReasonTooLarge` for an oversized cover, and update `contracts`-aligned docs in `docs/EBOOK-PARSING.md` (cover-payload error rule). Per FR-011 (partial).
- [x] T033 Add an end-to-end folder-sync test for read-failure cleanup in `internal/ingest` (`importerSuite` or `folderSuite`). Sync a folder library, seed `metadata` and `cover` rows for the book with `UpsertReadFailure`, run `os.Chtimes` on the book file, and re-sync through the real `FolderParser`/`reconciler.upsert` path. Assert that both rows are gone. In a sibling test, an untouched re-sync keeps both rows. Per T021, FR-015 (partial).
- [x] T034 Make `ebookSuite.TestOversizedCoverIsBounded` (`internal/ebook/epub_test.go:50-104`) pass under `go test -race ./internal/ebook/`. Keep the zip-bomb assertion, so that the 128 MiB inflated cover is not loaded, but measure only the cover read, or derive the bound from `maxCoverBytes` with explicit race-detector headroom instead of the fixed 32 MiB. Verify with `go test -race -count=1 ./internal/ebook/`. Per T004 (partial).
- [x] T035 Re-check stale metadata failure records after each library sync, so the library `unreadable_count` and the `unreadable` filter stop counting books whose file changed even when sync's size/mtime signal did not move (an archive replaced under an unchanged `.inpx`, or a folder file changed between syncs). After the reconcile, inside the existing sync run and under its write guard, load the library's current-version `metadata` rows. Recompute each book's fingerprint with `resolver.fingerprint` (stat or zip header only, no inflate) and delete the rows whose fingerprint differs; a stat error leaves the row. Add a test that replaces an archive entry, re-syncs, and asserts `CountUnreadableBooks` drops to 0 while an unchanged broken book stays counted. Document the rule in `docs/DATABASE.md`. Per US3/AC5, FR-020 (partial).

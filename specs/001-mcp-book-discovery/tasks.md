---

description: "Task list for Book Discovery for Claude.ai (Deep Index + MCP)"
---

# Tasks: Book Discovery for Claude.ai (Description Indexing + MCP)

**Input**: Design documents from `/specs/001-mcp-book-discovery/`

**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/, quickstart.md

**Tests**: Every task is implemented test-first, so each task writes its own tests. That means Go testify suite methods under the package's single `Test<Package>` runner, and Vitest specs for the SPA. There are no standalone test tasks.

**Organization**: One phase per user story. Phases follow the chosen delivery order: indexing first (US1, US2), then MCP (US3, US4).

## Format: `[ID] [P?] [Story] Description`

---

## Phase 1: Setup

**Purpose**: Dependency and fixtures shared by later phases.

- [x] T001 Add `github.com/modelcontextprotocol/go-sdk@v1.8.0` to `go.mod`/`go.sum` and record it under "Approved direct dependencies" in `CLAUDE.md`: approved 2026-11-02; it implements the MCP Streamable-HTTP server for `/mcp` (`internal/mcpserver`); the standard library has no MCP implementation, and hand-rolling it would mean owning spec compliance. Verify `CGO_ENABLED=0 task build` still succeeds.
- [x] T002 [P] Add INPX test fixtures under `internal/ingest/testdata/deepindex/`: a tiny `.inpx` plus two ZIP archives whose FB2 entries cover a UTF-8 annotation, a windows-1251 annotation, no annotation, a corrupt entry, a large `<binary>` after `</description>`, and a PDF-only book. Generate them with a committed `gen.go` (or `//go:generate`) so they stay reproducible.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Store the description text once, and add a header-only FB2 parse. Every story depends on these.

- [x] T003 Write goose migration `internal/db/migrations/006_fts_contentless.sql`:
  - Drop trigger `books_ad`.
  - Create `books_fts_new USING fts5(title, authors, series, annotation, content='', contentless_delete=1, tokenize='unicode61 remove_diacritics 1')`.
  - `INSERT INTO books_fts_new(rowid, title, authors, series, annotation) SELECT rowid, title, authors, series, annotation FROM books_fts`.
  - Drop the old table, rename the new one, recreate `books_ad` (`DELETE FROM books_fts WHERE rowid = old.id`).
  - Create `books_fts_vocab USING fts5vocab(books_fts, 'row')`.
  - Down migration: rebuild a regular FTS table from `books` joins, documented as lossy for annotation markup.

  Tests in `internal/db` (extend `ftsmigrate_test.go`):
  - row counts are preserved;
  - MATCH still finds title, author and annotation terms;
  - deleting a book removes its FTS row;
  - `books_fts_content` no longer exists;
  - the vocab table returns the document frequency for a term.
- [x] T004 Replace `updateBookFTSColumn` and the four per-column wrappers in `internal/db/fts.go` with `RefreshBookFTS(ctx, dbtx, bookID)`. It recomputes all four columns from `books`, the `book_authors` names in link order joined by a space, `series.name`, and `htmltext.StripMarkup(annotation)`, then does a full-row UPDATE, or an INSERT when the row is missing.

  Switch every caller over: `internal/ingest/merge.go` (`applyFTS`, authors relink), `internal/ingest/backfill.go` (`persistAnnotation`), `internal/api/enrich.go` (enrichment and Fix Match).

  Tests: a refresh after a title, author, series or annotation change makes the new terms match and the old ones stop matching; a refresh of a book with no FTS row inserts one; the existing ingest, merge, backfill and enrich suites stay green.
- [x] T005 [P] Add exported `ParseFB2Description(r io.Reader) (Metadata, error)` in `internal/ebook/fb2.go`:
  - Read tokens up to `<description>`, call `DecodeElement`, stop.
  - Reuse `htmltext.NewDisplayDecoder` (charset reader), `normalizeFB2Annotation`, and the full parser's identifier extraction.
  - Wrap the input in the existing 64 MiB limit.

  Tests in the `internal/ebook` suite:
  - it returns the same annotation and identifiers as `Parse` for the UTF-8 and windows-1251 fixtures;
  - it returns an empty annotation when there is none;
  - it errors on malformed XML before `</description>`;
  - it does not read past `</description>`: wrap the fixture in a counting reader and assert bytes read is well below the file size.

**Checkpoint**: The FTS is contentless and the header parser exists. Stories can start.

---

## Phase 3: User Story 1 — Index all missing descriptions on demand (Priority: P1) 🎯 MVP

**Goal**: The Deep Index / Deep Index All buttons fill every unchecked book's description from local files, with live progress, and make it searchable.

**Independent Test**: On a library with no stored descriptions, press Deep Index. Afterwards the web UI search finds a book by a word from its description. A second press finishes almost immediately.

- [x] T006 [US1] Add sqlc query `ListDeepIndexWork` in `internal/db/queries/books.sql` and regenerate with `task generate`.
  - Returns `book_id, file_id, file_format, source_path` for books with `metadata_checked = 0` in a library.
  - One preferred file per book: fb2, then epub, then mobi or anything else, then PDF last.
  - Optional `imported_at = ?` filter (NULL means all).
  - `ORDER BY source_path`.

  Tests: preference order; checked books are excluded; the optional filter works; a PDF-only book is returned with its PDF file.
- [x] T007 [US1] Implement `DeepIndexer` in `internal/ingest/deepindex.go` with `NewDeepIndexer(log, db, guard, extractor)` and `Run(ctx, library dbq.Library, scope DeepIndexScope, rep Reporter) (DeepIndexResult, error)`.
  - **Scope**: `All` or `ImportedAt(stamp)`.
  - **INPX libraries**: group the work list by archive, using the archive path resolved as `filepath.Join(filepath.Dir(library.Path), archiveName)`. A bounded worker pool (`runtime.GOMAXPROCS(0)`) opens each archive once and calls `ebook.ParseFB2Description` on each wanted entry.
  - **Other library types**: per-book `Extractor.Backfill`.
  - **PDF-only books**: marked checked without parsing.
  - **Writer**: a single goroutine commits batches of 500 under `WriteGuard.WithTx`, taking the guard per batch, never for the whole run. Per book it sets `annotation` **only when NULL**, calls `RefreshBookFTS`, inserts identifiers if absent, and calls `MarkMetadataChecked`.
  - **Errors**: an unreadable archive, a missing entry or a parse error leaves the book unchecked and counts toward `errors`.
  - **Reporting**: `rep.SetTotal`/`rep.Add` per batch. Returns `{Checked, Filled, Skipped, Errors}`. Honours ctx cancellation: committed batches stay committed.
  - Never makes network calls.

  Tests (use the T002 fixtures):
  - counts are correct;
  - an existing annotation is not overwritten;
  - the windows-1251 text is stored correctly;
  - the corrupt entry stays unchecked;
  - a re-run does no parsing;
  - a cancelled run keeps earlier batches;
  - the `ImportedAt` scope touches only matching books;
  - the archives are unchanged afterwards (mtime and size).
- [x] T008 [US1] Add request kinds to the engine in `internal/sync/engine.go`:
  - `syncReq` gains `kind` (`kindSync` | `kindDeepIndex`); queue dedup is by `(id, kind)`; the force upgrade applies only to sync requests.
  - New triggers `TriggerDeepIndex(id)` and `TriggerDeepIndexAll()`, the latter skipping `pending_purge`.
  - `syncLibrary` branches on kind: a deep-index run skips the checkpoint and the whole-run guard, and calls `deepIndexer.Run(ctx, lib, All, reporter)` with progress from `beginProgress`. On success it emits `library{id,"active"}`; on error it calls `markError`.
  - `Status` gains `Kind string` (`json:"kind,omitempty"`). Inject the indexer through `sync.New` (or an Option), using an interface declared in the `sync` package.

  Tests in `engine_test.go` / `engine_events_test.go`:
  - dedup per kind;
  - deep-index and sync for the same library are both queued and run sequentially;
  - the status payload carries `kind`;
  - deep-index ignores the checkpoint.
- [x] T009 [US1] Add REST endpoints:
  - `POST /api/libraries/{id}/deep-index` (`internal/api/libraries_handler.go`, `libraries.go`): `requireLibrary`, then `TriggerDeepIndex`, then 202 `{"status":"queued"}`.
  - `POST /api/deep-index` (`internal/api/sync_handler.go`, `sync.go`): `TriggerDeepIndexAll`, then 202 with the `Status` body.
  - Extend the api package's consumer interface for the engine.

  Tests: 202 bodies; 404 for an unknown library; the CSRF guard rejects a cross-site POST.
- [x] T010 [US1] Wire it up in `cmd/folio-idx/main.go`: build `ingest.NewDeepIndexer(log, database, writeGuard, extractor)` and pass it to `sync.New`. No business logic in main.
- [x] T011 [P] [US1] SPA:
  - `web/src/api.ts`: `deepIndexLibrary(id)`, `deepIndexAll()`.
  - `web/src/composables/useLibraryActions.ts`: `deepIndexLibrary`, `deepIndexAll`, with toasts.
  - `web/src/components/settings/LibraryTable.vue`: a "Deep Index" button after Re-index, `data-testid="library-action"`, disabled by `rowBusy`, emitting `deepIndex(id)`.
  - `web/src/pages/SettingsPage.vue`: a "Deep Index All" button next to Re-index All, `data-testid="deep-index-all"`, disabled while running.
  - `web/src/composables/useSyncStatus.ts`: expose `currentKind` from `status.kind`.
  - `web/src/components/SyncBanner.vue`: show "Deep indexing…" when `currentKind === 'deep_index'`.

  Vitest: extend `api.spec.ts`, `useLibraryActions.spec.ts`, `LibraryTable.spec.ts`, `SettingsPage.spec.ts`, `useSyncStatus.spec.ts`, `SyncBanner.spec.ts`.
- [x] T012 [US1] Docs:
  - `docs/DATABASE.md`: contentless FTS, `RefreshBookFTS`, vocab table, Deep Index flow, the "annotation only when NULL" rule, per-batch guard.
  - `docs/SYNC-EVENTS.md`: `kind` in the status payload, Deep Index runs.
  - `docs/API.md`: the two endpoints.
  - `docs/FRONTEND.md`: the buttons.
  - `docs/EBOOK-PARSING.md`: header-only FB2 parse.

**Checkpoint**: Deep Index works end to end. Validate with quickstart §2–3.

---

## Phase 4: User Story 2 — New books get descriptions automatically (Priority: P2)

**Goal**: A sync run that inserts books fills those books' descriptions in the same queue turn, never the backlog.

**Independent Test**: Add a book to an indexed INPX library and run a sync. The new book is checked and searchable by a description word; backlog books stay unchecked.

- [x] T013 [US2] Add `ImportedAt int64` to `ingest.Result` in `internal/ingest/ingest.go`, set in `runReconcile` (`internal/ingest/driver.go`) from the run's `now` stamp.

  Test: the stamp equals the `imported_at` of the books inserted in that run.
- [x] T014 [US2] Post-sync fill in `internal/sync/engine.go`: after a successful `runSync` with `res.Added > 0`, release the sync write guard, then call `deepIndexer.Run(ctx, lib, ImportedAt(res.ImportedAt), reporter)`. Errors are logged and do not mark the library errored. Skipped (checkpoint-unchanged) syncs run no fill.

  Tests: a sync that adds books fills only those; a sync with `Added == 0` or a skipped sync does not call the indexer; the backlog stays unchecked.
- [x] T015 [US2] Docs: describe the post-sync fill in `docs/DATABASE.md` (sync section) and `docs/SYNC-EVENTS.md`.

**Checkpoint**: The index stays complete with no manual upkeep. Validate with quickstart §4.

---

## Phase 5: User Story 3 — Find books by description from Claude.ai (Priority: P1)

**Goal**: A read-only `/mcp` endpoint with `list_libraries`, `list_facets`, `search_books` and `get_book`. Results carry links.

**Independent Test**: An MCP client lists the tools; `search_books` with prefix and `any_terms` returns matching books with links; `get_book` returns the full description.

- [ ] T016 [US3] Extend `internal/db/booksfilter.go`:
  - A token ending in `*` (length > 1) becomes the prefix form `"stem"*`, and the quoting stays safe.
  - `BookFilter.AnyTerms []string` becomes an OR group ANDed with `Query`.
  - `YearFrom` and `YearTo`, inclusive.

  Tests: a prefix matches word forms (Cyrillic); a lone `*` or embedded quotes cannot inject syntax; the OR group works together with the AND query; the year bounds work; existing filter tests stay green.
- [ ] T017 [US3] Add sqlc query `ListLanguagesWithCounts(library_id)` in `internal/db/queries/books.sql` (0 = all libraries, ordered by count desc) and regenerate.

  Test: counts are correct per library and overall.
- [ ] T018 [US3] Create package `internal/mcpserver`:
  - `server.go`: `New(log, database, publicURL, version) http.Handler`, which builds `mcp.NewServer(&mcp.Implementation{Name:"folio", Title:"Folio library", Version})` with the server instructions from `contracts/mcp-tools.md`, then `mcp.NewStreamableHTTPHandler(..., &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})`.
  - `links.go`: `bookLink(publicURL, id)`, giving `{PUBLIC_URL trimmed of "/"}/books/{id}` or `/books/{id}`.
  - `summary.go`: BookSummary/BookDetail mapping. The excerpt is the stripped annotation, at most 300 characters, cut at a word boundary, with `…` appended when cut.
  - Runner `mcpserver_test.go`.

  Tests: link building with and without a public URL; excerpt truncation for Cyrillic multibyte text; an initialize plus `tools/list` round trip through the SDK's in-memory transport.
- [ ] T019 [US3] Implement the `list_libraries`, `list_facets`, `search_books` and `get_book` tools in `internal/mcpserver/tools.go` exactly per `contracts/mcp-tools.md`:
  - typed `mcp.AddTool` inputs and outputs with `jsonschema` descriptions;
  - every tool annotated `readOnlyHint`;
  - `limit` default 20, max 50;
  - at least one of the search criteria required;
  - errors returned as tool errors (`isError`) with actionable messages;
  - no enrichment and no writes.

  Tests through the in-memory MCP client, against a fixture DB:
  - each tool's output shape and links;
  - prefix and `any_terms` searches on Cyrillic;
  - an unknown genre or book id yields a tool error;
  - an empty-criteria error;
  - pagination;
  - no rows change in the DB after the calls.
- [ ] T020 [US3] Mount it in `internal/server/server.go`: `Handlers.MCP http.Handler`, then `r.Route("/mcp", …)` serving the handler at `/mcp`, with no `sameSiteGuard` or `formBodyGuard`. Wire it in `cmd/folio-idx/main.go` with `mcpserver.New(log, database, cfg.PublicURL, version)`.

  Tests in `server_test.go`:
  - extend `TestAPIAndOPDSFallbackProtection` so `/mcp/unknown` returns 404, not the SPA;
  - a POST to `/mcp` without an Origin reaches the MCP handler;
  - a GET on `/mcp` returns 405.
- [ ] T021 [US3] Docs:
  - `docs/API.md`: the `/mcp` endpoint and its tools.
  - `docs/NETWORKING.md`: `/mcp` has no app auth, so an external authenticator is mandatory; a worked Cloudflare Access self-hosted application with OAuth for MCP; the security table gets a new row.
  - `docs/BACKEND.md`: router, middleware table, routes, `internal/mcpserver`.
  - `docs/ARCHITECTURE.md`: diagram and package tree.

**Checkpoint**: Claude.ai can search and link. Validate with quickstart §5–6, steps 1–3.

---

## Phase 6: User Story 4 — "Books like this" (Priority: P2)

**Goal**: A `similar_books` tool that returns ranked candidates with reasons.

**Independent Test**: For a book with a description, the candidates share its distinctive themes or genre and exclude the book itself. For a book without one, the candidates come from series, author and genre.

- [ ] T022 [US4] Implement `internal/db/similar.go`:
  - **`DistinctiveTerms(ctx, db, text, k)`**: tokenize the stripped annotation the way unicode61 with remove_diacritics does (lowercase, letters and digits, ё→е; compare by querying `books_fts_vocab`). Read the document frequency per term from `books_fts_vocab`. Return the top k by tf·idf among terms with df > 1.
  - **`SimilarBooks(ctx, db, bookID, limit)`**: merge these candidate sources and attach the `reason`:
    - a bm25-ranked OR match on the distinctive terms (built with bob, parameterized);
    - the same series;
    - the same authors;
    - genre overlap.

    Dedupe the results, exclude the source book, and cap `limit` (default 20, max 50).

  Tests:
  - the distinctive terms exclude very common words;
  - candidates rank a thematically close fixture book first;
  - the source book is excluded;
  - the fallback works when the book has no description;
  - the limit caps the result.
- [ ] T023 [US4] Add the `similar_books` tool in `internal/mcpserver/tools.go` per `contracts/mcp-tools.md` (`{source, candidates[]}` with `reason` and `matched_terms`). Document it in `docs/API.md`.

  Tests through the in-memory client: output shape, links, reasons, an unknown id yields a tool error.

**Checkpoint**: Both discovery modes work. Validate with quickstart §6, step 4.

---

## Phase 7: Polish & Cross-Cutting Concerns

- [ ] T024 Run `task format`, `task lint`, `task test` and `task build`, and fix any findings. Run the quickstart §2–3 performance check against a **copy** of a real production database (→ temp dir) with the libraries mounted read-only. Record the Deep Index duration, the coverage and the database growth in the PR description (SC-001…SC-004). Any final doc touch-ups go in this task.

---

## Dependencies & Execution Order

- **Setup (T001–T002)**, then **Foundational (T003–T005)**: T004 depends on T003; T005 is independent.
- **US1 (T006–T012)** depends on Foundational. T007 needs T005 and T006. T008 needs T007. T009 needs T008. T010 needs T007–T009. T011 can run in parallel with the backend once the T009 contract is fixed.
- **US2 (T013–T015)** depends on US1 (the DeepIndexer and engine kinds).
- **US3 (T016–T021)** depends on Foundational and T001. It is independent of US1/US2 in code, but only useful once descriptions are indexed.
- **US4 (T022–T023)** depends on US3 (the mcpserver package) and on T003 (the vocab table).
- **Polish (T024)** comes last.

## Parallel Opportunities

- T002 runs alongside T001. T005 runs alongside T003/T004.
- T011 (SPA) runs alongside T006–T010.
- US3's T016/T017 can start while US1/US2 are in progress if staffing allows. Delivery order stays US1 → US2 → US3 → US4.

## Implementation Strategy

1. **MVP**: Phases 1–3. The user presses Deep Index and the whole catalog becomes content-searchable in the web UI.
2. **Upkeep**: Phase 4. New books index themselves.
3. **Claude.ai**: Phase 5. Search with links through the connector.
4. **Discovery**: Phase 6. "Books like this".
5. **Polish**: Phase 7, including the real-data performance check.

# Implementation Plan: Book Discovery for Claude.ai (Description Indexing + MCP)

**Branch**: `feature/mcp-book-discovery` | **Date**: 2026-11-02 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/001-mcp-book-discovery/spec.md`

## Summary

Two parts, delivered in order.

1. **Deep Index**: fill missing book descriptions from local files. It can be triggered per library ("Deep Index") or for every library ("Deep Index All"). It also runs automatically for the books a sync run inserted. A worker pool groups books by the archive of their best file, opens each archive once, and reads each book through the shared book reader (for FB2, only the `<description>` header). A single writer persists results in batches of 500. The full-text index switches to an FTS5 **contentless-delete** table, so the description text is stored only once.
2. **`/mcp` endpoint**: a read-only, stateless Streamable-HTTP MCP server built on the official Go SDK, with five tools: `list_libraries`, `list_facets`, `search_books`, `get_book`, `similar_books`. Every book in a result carries a `{PUBLIC_URL}/books/{id}` link. Auth is delegated to Cloudflare Access (OAuth for MCP), and Folio adds no auth code.

Research and the decisions behind this plan: [research.md](./research.md).

### Approaches considered

**MCP endpoint**
- **(A) Official MCP Go SDK v1.8.0: chosen.** Maintained by the MCP project and Google. It handles transport, protocol version negotiation and tool schemas. Its cost is one new dependency, approved on 2026-11-02.
- **(B) Hand-rolled JSON-RPC on the standard library: rejected.** Folio would own spec compliance and have to follow every protocol revision.
- **(C) `mark3labs/mcp-go`: dropped.** It is still a dependency, and has no advantage over the official SDK.

**Search semantics**
- **(1) Claude plus FTS and filters: chosen as the smallest change.** Claude understands the request and re-ranks; Folio supplies candidates.
- **(2) Embeddings: deferred.** No vector extension works with the CGO-free driver, it needs a model and somewhere to run it, and it adds about 0.7 GB.

**Storing the description once**
- **Contentless-delete FTS: chosen.** It was verified on the bundled SQLite 3.53.4.
- **External content (`content='books'`): rejected.** The HTML in `books.annotation` differs from the stripped text the index needs, and authors and series come from joined tables, which makes deletes fragile.

## Technical Context

**Language/Version**: Go 1.27 (backend). TypeScript + Vue 3 (SPA).

**Primary Dependencies**:
- Existing: chi, modernc.org/sqlite v1.60.1 (SQLite 3.53.4), goose, sqlc, bob, x/text.
- **New:** `github.com/modelcontextprotocol/go-sdk` v1.8.0, approved.

**Storage**: Folio SQLite at `/data/folio.db`.
- Migration `006`: `books_fts` becomes contentless-delete, plus an `fts5vocab` row table named `books_fts_vocab`.
- Migration `008`: `books_fts_vocab` is replaced by the `fts5vocab` col table `books_fts_vocab_col`, so "similar books" weighs terms by the annotation column alone.
- Migration `009`: the partial index `idx_books_described` over books with a description, for the "similar books" document count.
- New sqlc queries:
  - unchecked books for a library: a count (`CountDeepIndexWork`) and a page of them with all their files (`ListDeepIndexWork`);
  - languages with counts per library;
  - annotation fill only when NULL (`SetBookAnnotationIfNull`, `BookHasAnnotation`);
  - series, author and genre mates of a book, for "similar books".

**Testing**: Go testify suites (one `Test<Package>` per package). Vitest for the SPA. Fixtures:
- small INPX/ZIP files with FB2 entries under `testdata/`, covering windows-1251, a missing annotation, a corrupt entry and a PDF;
- an MCP in-process client from the SDK, used for the tool tests.

**Target Platform**: Linux container (multi-arch), CGO_ENABLED=0.

**Project Type**: Single Go web service with an embedded SPA.

**Performance Goals**:
- Deep Index of the whole backlog (~500k INPX books): under 1 h on a multi-core host (SC-002). Header-only reads make it I/O-light, so it should take minutes.
- Re-run on an already-indexed library: under 1 min (SC-003).
- MCP search: under 2 s (SC-005).

**Constraints**:
- Sources stay read-only.
- No online calls from Deep Index or MCP.
- Memory stays bounded: about one decoded description header per worker, a 500-row batch, and a 5,000-book page of the work list.
- Write-guard holds are per batch, so web-UI edits never wait behind a run for long.

**Scale/Scope**: hundreds of thousands of books in a few hundred archives (hundreds of GB). Expected database growth is about 0.5–0.8 GB (SC-004 allows up to 1 GB).

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | Status | Notes |
|---|---|---|
| I. Read-only sources | PASS | Archives are opened read-only and stream-decoded. Writes go only to `/data/folio.db`. `annotation` is set only when NULL, so manual or enriched data is never overwritten. |
| II. Single CGO-free binary | PASS | The SDK and its dependencies are pure Go. `/mcp` is a sub-router with SPA-fallback protection, and a test is added for it. |
| III. Resource efficiency & minimal egress | PASS | Deep Index and MCP make no outbound calls. Worker count is bounded by GOMAXPROCS. Header-only parsing keeps memory small. |
| IV. Schema & query discipline | PASS | Goose migrations 006, 008 and 009. Static queries go through sqlc. Dynamic FTS and filter SQL stays in bob (`booksfilter.go`, `fts.go`), and every value is parameterized. |
| V. Testify suites | PASS | New packages each get a runner file. All tests are suite methods and hermetic, using fixtures and `t.TempDir()`. |
| VI. Idiomatic Go & explicit deps | PASS | The new dependency was approved and gets recorded in `CLAUDE.md`. Interfaces are declared at the consumer. Wiring happens in `cmd/folio-idx/main.go` only. |
| VII. Security boundaries | PASS | `/mcp` has no app auth, like `/api`, and the docs require an external authenticator (Cloudflare Access OAuth). It is read-only, so the state-changing CSRF guard does not apply. Links use `PUBLIC_URL`, never `X-Forwarded-Host`. |

Post-design re-check: PASS (no change).

## Design

### Part 1: Deep Index

**Schema**: [data-model.md](./data-model.md).
- **Migration 006**:
  1. Drop the trigger.
  2. Create `books_fts_new` as contentless-delete.
  3. Copy the existing rows (the old table still has its content).
  4. Drop the old table and rename the new one.
  5. Recreate the `books_ad` trigger and create the `books_fts_vocab` table.
  6. Down migration: restore the regular table by rebuilding it from `books` joins. Documented as lossy for the stripped annotation, then re-derived with `htmltext` on the next refresh.

**FTS writes (`internal/db/fts.go`)**:
- Keep `InsertBookFTS`, which writes the full row.
- Replace `updateBookFTSColumn` and its four wrappers with `RefreshBookFTS(ctx, dbtx, bookID)`. It reads the title, the authors in `book_authors` order, the series name and the annotation, then does a full-row `UPDATE`, or an `INSERT` if the row is missing. Callers to switch over: `ingest/merge.go` (`applyFTS`, authors relink), `ingest/backfill.go`, `api/enrich.go` (enrichment and Fix Match).

**FB2 header parser (`internal/ebook`)**:
- `parseFB2Header(io.Reader) (Metadata, error)` reads tokens up to `<description>`, decodes that element, then stops. It also stops at the first `<body>` or `<binary>`, so a well-formed FB2 without a `<description>` reads as empty metadata.
- It was first shipped as the exported `ParseFB2Description`; since feature 002 (unified book reading) it is reached through the FB2 parser's `Metadata` in the `ebook.Dispatcher`.
- It shares the charset decoder, the annotation normalizer and the identifier extraction with the full parser.

**DeepIndexer (`internal/ingest/deepindex.go`)**:
- `Run(ctx, library, scope, reporter) (DeepIndexResult, error)`.
- Scope is either `DeepIndexAll()` or `DeepIndexImportedAt(stamp)`.
- Flow:
  1. Count the work with sqlc (`CountDeepIndexWork`) and call `reporter.SetTotal`.
  2. Load the work list a page of 5,000 books at a time, in book-id order (`ListDeepIndexWork` with `after_id`/`lim`), each book with all its files.
  3. Group each page's books by the archive of their top-ranked file (the reader ranks EPUB, FB2, MOBI; PDF is not read for descriptions) and hand each group to a worker, which opens the archive once. A book not in an archive is a job of its own. This applies to every library type.
  4. Results go through a channel to a single writer.
  5. Writer: each batch of 500 commits under `WriteGuard.WithTx` and calls `reporter.Add`.
- Skip rules:
  - PDF-only books are marked checked.
  - Read or parse errors leave the book unchecked. A failure that may pass (I/O, an unreadable archive) is retried by the next run; a missing entry or unparseable file is recorded in `book_read_failures` and not read again until the file or the reader version changes (feature 002).
  - An entry with no annotation is marked checked.
- Result counts: `checked`, `filled`, `skipped`, `broken`, `known_broken`, `errors`.

**Engine (`internal/sync`)**:
- `syncReq{id, force, kind}`; dedup is per `(id, kind)`.
- New triggers: `TriggerDeepIndex` and `TriggerDeepIndexAll`.
- A `kindDeepIndex` request goes through `deepIndexer.Run`. It takes no lock for the whole run, because the indexer locks per batch.
- **Post-sync fill**: after a successful `runSync` with `res.Added > 0`, call `deepIndexer.Run(scope = ImportedAt(res.ImportedAt))` once the sync guard is released.
- `ingest.Result` gains `ImportedAt`.
- `Status` gains `Kind`, and the `status` SSE payload gains a `"kind"` field.

**REST (`internal/api`)**:
- `POST /api/libraries/{id}/deep-index` returns 202 `{status:"queued"}`.
- `POST /api/deep-index` returns 202 with the status body.
- Both go through the existing CSRF guard. Contract: [contracts/rest-api.md](./contracts/rest-api.md).

**SPA**:
- `LibraryTable.vue`: a "Deep Index" button that emits `deepIndex(id)`, disabled by `rowBusy`.
- `SettingsPage.vue`: a "Deep Index All" button next to Re-index All.
- `api.ts`: `deepIndexLibrary` and `deepIndexAll`.
- `useLibraryActions`: the matching actions, with toasts.
- `useSyncStatus` and `SyncBanner`: read `kind` and label the run "Deep indexing…".

### Part 2: MCP endpoint

**Search filter (`internal/db/booksfilter.go`)**:
- `quoteSearchToken` supports a trailing `*` as a prefix match, and drops a token with no letter or digit (punctuation or `*` only).
- `BookFilter` gains `AnyTerms []string` (an OR group), `YearFrom` and `YearTo`.
- bm25 ties are broken by book id, so offset paging is stable.
- `SearchTerms` exposes each token and its prefix length, so `search_books` can bound a request's cost before running it.

**Package `internal/mcpserver`**:
- `New(log, database, publicURL, version) http.Handler` builds an `mcp.Server` with the five tools and returns `mcp.NewStreamableHTTPHandler(..., Stateless: true, JSONResponse: true, DisableLocalhostProtection: true)`. The SDK's localhost Host-header guard is off because cloudflared or another proxy may reach Folio over loopback with the public Host.
- Each tool is a typed `mcp.AddTool` handler that queries the `*sql.DB` through `db` and `dbq`.
- `search_books` bounds its input: at most 20 `any_terms`, 500 characters per text field, 40 words across all fields, 10 prefix terms, and 3 letters before a `*`.
- Contract: [contracts/mcp-tools.md](./contracts/mcp-tools.md).

**Similar books (`internal/db/similar.go`)**:
1. Pick distinctive terms: tokenize the stripped annotation in Go, take the annotation-column document frequency from `books_fts_vocab_col`, with N the number of described books, and keep the top 8 by tf·idf among terms with df > 1.
2. Build candidates from the bm25-ranked OR match on those terms, scoped to the annotation column, plus same-series books, same-author books, and genre overlap.
3. Merge and dedupe in that order, excluding the source book, and attach a `reason` to each.
4. Limit to 20 by default, 50 at most. A candidate deleted by a concurrent sync before it is loaded is dropped.

**Server (`internal/server/server.go`)**:
- `Handlers.MCP http.Handler`.
- `r.Route("/mcp", ...)` with no CSRF or form guards.
- `TestAPIAndOPDSFallbackProtection` gets cases for `/mcp/*`.

**Composition**: `cmd/folio-idx/main.go` builds `mcpserver.New(log, database, cfg.PublicURL, buildVersion())` and creates `ingest.NewDeepIndexer`, which it injects into `sync.New` with the `sync.WithDeepIndexer` option.

### Documentation (same change, constitution "Documentation")

- `docs/DATABASE.md`: contentless FTS, `RefreshBookFTS`, Deep Index, post-sync fill, the vocab table.
- `docs/SYNC-EVENTS.md`: the `kind` field, Deep Index runs.
- `docs/API.md`: the deep-index endpoints, the `/mcp` endpoint and its tools.
- `docs/NETWORKING.md`: `/mcp` must sit behind an authenticator, with a worked Cloudflare Access (OAuth for MCP) example.
- `docs/BACKEND.md`, `docs/ARCHITECTURE.md`: the router, the package tree, `internal/mcpserver`.
- `docs/FRONTEND.md`: the new buttons.
- `docs/EBOOK-PARSING.md`: the header-only FB2 parse.
- `CLAUDE.md`: approved-dependency record for `github.com/modelcontextprotocol/go-sdk`, approved 2026-11-02, with what it solves and why the standard library falls short.

## Project Structure

### Documentation (this feature)

```text
specs/001-mcp-book-discovery/
├── plan.md
├── research.md
├── data-model.md
├── quickstart.md
├── contracts/
│   ├── mcp-tools.md
│   └── rest-api.md
├── checklists/requirements.md
└── tasks.md            # /speckit-tasks
```

### Source Code (repository root)

```text
internal/
├── db/
│   ├── migrations/006_fts_contentless.sql      # new
│   ├── migrations/008_fts_vocab_col.sql        # new: annotation-column vocab
│   ├── migrations/009_books_described_index.sql # new: described-books index
│   ├── queries/books.sql                       # + unchecked work list, languages w/ counts
│   ├── fts.go                                  # RefreshBookFTS replaces column updates
│   ├── booksfilter.go                          # prefix terms, AnyTerms, YearFrom/To
│   └── similar.go                              # new: distinctive terms + candidates
├── ebook/fb2.go                                # + header-only FB2 parse
├── ingest/
│   ├── deepindex.go                            # new: DeepIndexer
│   ├── ingest.go / driver.go                   # Result.ImportedAt
│   ├── merge.go, backfill.go                   # → RefreshBookFTS
├── api/
│   ├── libraries.go, libraries_handler.go      # deep-index endpoints
│   ├── sync.go, sync_handler.go                # deep-index all
│   └── enrich.go                               # → RefreshBookFTS
├── sync/engine.go, reporter.go                 # request kind, post-sync fill, Status.Kind
├── mcpserver/                                  # new: server.go, tools.go, summary.go, links.go, + tests
└── server/server.go                            # /mcp route
cmd/folio-idx/main.go                           # wiring
web/src/
├── api.ts
├── composables/useLibraryActions.ts, useSyncStatus.ts
├── components/settings/LibraryTable.vue, SyncBanner.vue
└── pages/SettingsPage.vue
```

**Structure Decision**: This is the existing single Go service with an embedded SPA. The only new package is `internal/mcpserver`. It is named so it doesn't clash with the SDK's `mcp` package.

## Complexity Tracking

No constitution violations.

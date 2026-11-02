# Research: Book Discovery for Claude.ai

Decisions taken during brainstorming and from code/library research.
Measurements were taken read-only against a real large catalog.

## R1. Baseline: what is searchable today

- **Decision**: Description indexing is a prerequisite for plot-level search.
- **Findings**: `books_fts(title, authors, series, annotation)` exists (unicode61,
  remove_diacritics). INPX books almost never have an annotation stored; Calibre books
  almost always do. Annotations are filled lazily on first book view
  (`ingest.LocalBackfiller`, gated by `books.metadata_checked`) and by Google Books enrichment
  (`enrichment_checked`).
- **Sample (random INPX FB2 files)**: about 80% have an annotation of about 1 KB; the
  current per-book path costs tens of milliseconds per book → hours single-threaded for a
  large library. Archives are hundreds of GB; an FB2 is ~1–2 MB uncompressed.

## R2. No online enrichment on bulk / hourly / MCP paths

- **Decision**: Deep Index and the post-sync fill parse local files only; MCP tools never
  trigger enrichment. The page-view tiers (`claimLazy`) stay unchanged.
- **Rationale**: Google Books is rate-limited; constitution III requires minimal egress.

## R3. Storing the annotation once — FTS5 contentless-delete

- **Decision**: Migration `006` recreates `books_fts` as
  `fts5(title, authors, series, annotation, content='', contentless_delete=1, tokenize='unicode61 remove_diacritics 1')`,
  populated from the existing table (`INSERT … SELECT rowid, title, authors, series, annotation FROM books_fts`
  — the old table still stores its content, so the copy needs no Go code). All partial-column
  FTS updates are replaced by one full-row helper `db.RefreshBookFTS(ctx, dbtx, bookID)` that
  recomputes all four columns from `books` / `book_authors` / `series` with
  `htmltext.StripMarkup(annotation)`.
- **Verified**: bundled SQLite (modernc v1.60.1) is **3.53.4**. In a probe: contentless-delete
  table accepts INSERT, DELETE by rowid, and full-row UPDATE; a partial-column UPDATE fails
  with "cannot UPDATE a subset of columns on fts5 contentless-delete table". The `books_ad`
  trigger (`DELETE … WHERE rowid = old.id`) keeps working. No code uses `snippet()` /
  `highlight()` or reads columns back from `books_fts`.
- **Alternatives rejected**:
  - *External content (`content='books'`)*: `books.annotation` is HTML while the index holds
    stripped text; `authors`/`series` aren't `books` columns (would need a view with a
    deterministic GROUP_CONCAT); external content needs exact old values on every delete, so
    a mismatch silently corrupts the index. Too fragile.
  - *Keep duplication*: costs ~470 MB extra once the catalog is indexed (SC-004 ≤ 1 GB).
- **Cost**: `RefreshBookFTS` reads 3 small rows per update; updates are rare outside sync
  merges and Deep Index batches.

## R4. Header-only FB2 parsing

- **Decision**: Add exported `ebook.ParseFB2Description(r io.Reader) (Metadata, error)` using
  the XML token API: advance to `<description>`, `DecodeElement` into the existing description
  struct, stop. Reuses `htmltext.NewDisplayDecoder` (charset: windows-1251/koi8-r via the
  prolog) and `normalizeFB2Annotation`; wraps input in the existing 64 MiB `LimitReader`.
  Identifiers are extracted as in the full parser; covers are not.
- **Rationale**: `parseFB2Reader` decodes into a struct containing `<binary>` blocks, so it
  reads to EOF and buffers base64 covers. ZIP entries are compressed individually, so stopping
  after `</description>` means only the first few KB of each entry are inflated — disk I/O
  drops from reading whole archives to a seek plus a small read per book.

## R5. Deep Index execution model

- **Decision**: New `ingest.DeepIndexer` with `Run(ctx, library, scope, reporter)`:
  1. Query unchecked books of the library (`metadata_checked = 0`), optionally restricted to a
     sync run's `imported_at` stamp (scope "new"), with their preferred non-PDF file,
     ordered by `source_path` (new sqlc query).
  2. INPX: group by archive; a bounded worker pool (`runtime.GOMAXPROCS`) takes one archive at a
     time, opens it once, and parses each wanted entry header-only. Other library types fall
     back to the existing `Extractor.Backfill` per book (folder/Calibre backlogs are tiny).
     PDF-only books are marked checked without parsing (unchanged semantics).
  3. A single writer goroutine batches results (500 books per transaction) under the write
     guard: `annotation` set only when NULL (manual/enriched data never overwritten),
     `RefreshBookFTS`, identifiers insert-if-absent, `MarkMetadataChecked`.
  4. Unreadable archive / missing entry / parse error: skip, leave unchecked (retried next run);
     logged at debug with a per-run summary count at info.
- **Write guard**: Deep Index takes the guard **per batch**, not for the whole run, so a long
  backlog run does not block web-UI edits or lazy page-view backfills for an hour. Mutual
  exclusion with sync of the same DB is guaranteed by the engine's single worker.
- **Cancellation**: the engine's stop-cancellable ctx; committed batches persist; unchecked
  books resume next run.

## R6. Engine integration

- **Decision**: `syncReq` gains `kind` (`kindSync` | `kindDeepIndex`); queue dedup becomes per
  `(library, kind)`; the force-upgrade applies only to sync requests. `syncLibrary` branches on
  kind before the parser/checkpoint logic (Deep Index ignores checkpoints).
  New triggers: `TriggerDeepIndex(id)`, `TriggerDeepIndexAll()` (skips `pending_purge`).
- **Post-sync fill (FR-004)**: `ingest.Result` gains `ImportedAt int64` (the run stamp already
  used for every insert). After a successful sync with `Added > 0`, the worker releases the sync
  guard and runs Deep Index with scope "new" (`imported_at = stamp`) in the same queue turn.
  Skipped (checkpoint-unchanged) syncs add nothing and run no fill.
- **Status**: `Status` gains `kind` for the current item (`"sync"` | `"deep_index"`), additive
  in the `status` SSE payload; progress reuses the existing `progress` event.

## R7. MCP SDK

- **Decision**: `github.com/modelcontextprotocol/go-sdk` **v1.8.0** (approved). Go 1.25+
  (Folio: 1.27). Transitive deps (`google/jsonschema-go`, `yosida95/uritemplate/v3`,
  `segmentio/encoding`, `golang-jwt/jwt/v5`, `x/oauth2`, …) are pure Go; `CGO_ENABLED=0` holds.
  Protocols 2024-11-05 … 2026-07-28 incl. 2025-06-18 / 2025-11-25 used by Claude.ai.
- **Transport**: `mcp.NewStreamableHTTPHandler(..., &StreamableHTTPOptions{Stateless: true, JSONResponse: true})`
  — POST-only, no session affinity (safe behind Cloudflare), single request/response so the
  60 s `WriteTimeout` suffices.
- **Alternatives**: hand-rolled stdlib JSON-RPC (rejected: own the spec compliance);
  `mark3labs/mcp-go` (dropped: dependency without advantage).

## R8. Routing, auth and CSRF

- **Decision**: Mount with `r.Route("/mcp", …)` so `/mcp/*` unknown paths 404 via the
  sub-router (same mechanism as `/api`, `/opds`). Not wrapped in `sameSiteGuard`: the endpoint
  is read-only (no state change), and Claude.ai's server-side client sends no browser
  `Origin`. SDK localhost/cross-origin protections are left at defaults unless the quickstart
  check shows the tunnel's Host header trips them.
- **Auth**: none in Folio (constitution VII). Operator adds a Cloudflare Access self-hosted
  application with OAuth for MCP on `/mcp`. Documented in
  `docs/NETWORKING.md`.
- **Links**: `{PUBLIC_URL}/books/{id}`; relative `/books/{id}` when `PUBLIC_URL` is unset.

## R9. Search semantics for Claude

- **Decision**: Reuse `db.FilterBooks` (exported, bob-built). Extend `BookFilter` with:
  - prefix terms: a token ending in `*` becomes `"stem"*` (safe: quoting is kept);
  - `AnyTerms []string`: OR-group ANDed with `Query` (synonyms / word forms);
  - `YearFrom`, `YearTo`.
  The web UI inherits prefix support (a typed `*` currently matches literally — harmless
  change, documented).
- **Morphology**: no server-side stemming this iteration; tool descriptions instruct
  Claude to use `stem*` and `any_terms`. Snowball stemming deferred (would need a dependency).
- **Similar books**: candidates from (a) top distinctive terms of the book's annotation —
  tokenized in Go, document frequency read from an `fts5vocab` `row` table over `books_fts`
  (works on contentless tables), top 8 by tf·idf with df > 1, OR-matched and bm25-ranked;
  (b) same series; (c) same authors; (d) shared genres as a tie-breaker / fallback when the
  book has no annotation. Each candidate carries a `reason`.
- **Embeddings**: deferred (no CGO-friendly vector extension; model hosting; ~0.7 GB).

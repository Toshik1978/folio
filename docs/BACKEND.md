# Backend

> Go server: routing, middleware, and SPA embedding.

---

## Entry Point

**Binary:** `cmd/folio-idx/main.go`
**Module:** `github.com/Toshik1978/folio`
**Import path:** `github.com/Toshik1978/folio/web` (for embedded SPA assets)

```
main() → run():
  godotenv.Load() → config.MustParse() → logging.New() (base logger)
  → db.Open() → db.NewWriteGuard() → notify.New(base, store, client, …)
  → logging.WithErrorSink(base, notifier) (tapped logger `log`, used from here on)
  → ebook.NewStandardDispatcher(ebook.WithCrashReporter(notifier)) → ingest.NewExtractor(…,parser,guard)
  → ingest.NewLocalBackfiller() → covers.NewStore(…, extractor, ingest.NewCoverState())
  → metasearch.NewRegistry(amazon, goodreads, openlibrary, googlebooks) → NewAggregator() · NewCoordinator()
  → auth.New().WarnIfUnprotected() → events.NewBroker() → api.NewCatalog() → ingest.NewDeepIndexer()
  → sync.New(…, parserMap, coverStore, WithEvents(broker), WithStatsObserver(catalog),
             WithDeepIndexer(deepIndexer), WithNotifier(notifier)).Start()
  → notifier.Start()
  → http.Server{Handler: server.New(log, server.Handlers{API:[…], OPDS:…, MCP:…, Crashes:…}, env, noColor, publicURL)}
  → serve(): ListenAndServe until SIGINT/SIGTERM or a server failure
    → shutdown(): broker.Close + graceful Shutdown + Engine.Stop + notifier.Stop
```

`main()` is a thin `os.Exit(run())`; `run` is the composition root (see
[below](#composition-root-maingo)). Configuration is loaded from environment
variables (with `.env` auto-load via `godotenv`) into a typed `Config` struct via
`caarlos0/env`. Key fields: `APP_ENV` (default `development`; gates the dev-only
request logger and is attached to every log line — the Docker image sets
`APP_ENV=production`), `PORT` (default `8080`), `DATA_DIR` (default `./data`
for local dev; the Docker image sets `DATA_DIR=/data` to match the mounted
volume — the binary runs as nonroot and cannot write the default `./data`
under the root-owned `/app` workdir), `PUBLIC_URL`
(optional canonical external base URL, e.g. `https://folio.example.com`, used to
build the absolute OPDS OpenSearch URL and the book links in MCP results and
Telegram messages, and allowed as a first-party origin by the `/api` same-site
guard — see `NETWORKING.md`), `GOOGLE_KEY`
(optional Google Books API key for online enrichment; empty uses the anonymous
quota), and `LIBRARY_ROOT` (optional defense-in-depth: when set, the libraries
API refuses to create or edit a library whose path resolves outside this base
directory, so an admin — or a stolen admin session — cannot point a library at
an arbitrary host path like `/etc` and serve files back out; empty leaves
library paths unconstrained, matching historical behavior). `TELEGRAM_API_BASE`
(default `https://api.telegram.org`; testing only — an absolute `http`/`https`
URL pointing the Telegram client at a local fake endpoint, trailing `/` trimmed;
not for production use). OPDS Basic Auth credentials are **not** environment variables — they are
configured at runtime via `PUT /api/settings` and stored hashed in the `settings`
table (the `auth` package owns them). Structured logging uses the standard library `log/slog`. `run`
returns a non-zero exit code on a failed dependency so the container restarts
cleanly: `1` database open, `2` cover store, `3` sync engine init, `4` server
error, `5` notifier init. Shutdown is graceful (drain in-flight requests within
a 15s timeout, then stop the sync engine, then the notifier; the SSE broker is
closed first so its long-lived streams do not hold the drain open).

---

## Router: `server.New()`

`server.New(log, Handlers, env, noColor, publicURL)` (→ `newWithFS`) builds the root
`go-chi/chi/v5` router. It accepts an `http.FileSystem` to decouple static file
serving from the embed mechanism (enables test injection via `fstest.MapFS`).
Handlers arrive already constructed by the composition root, each satisfying the
local `Registrar` interface (`Register(chi.Router)`); `server` knows nothing about
their concrete types. The root router wires the built-in health route, mounts the
ordered API + OPDS registrars, mounts the optional MCP handler, and adds the SPA
catch-all:

```go
r.Route("/api", func(api chi.Router) {
    api.Use(sameSiteGuard(publicURL)) // CSRF: cross-site state-changing calls → 403
    api.Use(formBodyGuard)
    api.Get("/health", apipkg.Health) // built-in liveness
    for _, reg := range h.API {       // api.* handlers + the settings handler, in order
        reg.Register(api)
    }
})
r.Route("/opds", func(o chi.Router) { h.OPDS.Register(o) }) // internal/opds
if h.MCP != nil {                                           // internal/mcpserver
    r.Route("/mcp", func(m chi.Router) { m.Handle("/", h.MCP) })
}
r.HandleFunc("/*", webHandler(fs))                          // SPA + static assets
```

### Middleware Stack

Applied globally on the root router, in this order:

| Middleware | Purpose |
| :--- | :--- |
| `proxyHeaders` (custom) | Sets `URL.Scheme` from `X-Forwarded-Proto` (falling back to the TLS state). It deliberately does **not** honor `X-Forwarded-Host` — see [NETWORKING.md](./NETWORKING.md#canonical-host-public_url-not-x-forwarded-host) |
| `middleware.Logger` (chi) | Request logging via chi's logger, bridged to the `log/slog` logger through a `slogger` adapter (`Print` → `slog.Info`) — **registered only when `env == "development"`** |
| `recoverer` (`internal/server/recoverer.go`) | Panic recovery: logs the panic and stack through `slog` at Error (marked reported), hands it to `Handlers.Crashes` (a crash reporter; nil = log only) with the chi route pattern, and answers `500` (skipped for connection upgrades). `http.ErrAbortHandler` (direct or wrapped) is re-raised as the bare sentinel. Replaces chi's `middleware.Recoverer` |

The OPDS sub-router adds HTTP Basic Auth on its protected group (see
[NETWORKING.md](./NETWORKING.md)). The `/api` group additionally carries
`sameSiteGuard` and `formBodyGuard` (CSRF). `/mcp` carries **neither** and no
application auth: Claude.ai posts JSON-RPC cross-origin and authentication is
external (see [NETWORKING.md](./NETWORKING.md#mcp-endpoint-mcp-and-claudeai)). Its
sub-router handles only `/mcp`, so unknown `/mcp/*` paths return `404` rather than
falling through to the SPA.

### Route Table

`/api/*` is mounted from the `internal/api` and `internal/settings` registrars,
`/opds/*` from `internal/opds`, `/mcp` from `internal/mcpserver`; all are listed
with their full external paths.

| Method | Path | Description |
| :--- | :--- | :--- |
| `GET` | `/api/health` | Liveness — `{"status":"ok"}` |
| `GET` | `/api/books` | Paginated, filtered book list (BM25 order for searches) |
| `GET` | `/api/books/{id}` | Single book detail (lazy metadata backfill + online enrichment) |
| `GET` | `/api/books/{id}/files/{fileID}` | Streamed file download (one format) |
| `GET` | `/api/books/{id}/cover` | Cached cover image (placeholder fallback) |
| `GET` | `/api/books/{id}/cover/thumbnail` | Aspect-preserving cover thumbnail (cover fallback + self-heal) |
| `GET` | `/api/books/{id}/cover/search` | Cover search: multi-source cover candidates (`?q=`) |
| `GET`/`POST` | `/api/books/{id}/match` | Fix Match: Google Books candidates · apply a chosen volume |
| `PUT` | `/api/books/{id}` | Update book fields (title, authors, annotation, etc.) |
| `PUT`/`POST` | `/api/books/{id}/cover` | Upload cover image (binary) · set cover from URL |
| `GET` | `/api/genres` | Genre taxonomy list |
| `GET` | `/api/authors` · `/api/authors/letters` | One alphabet bucket of authors · available letters |
| `GET` | `/api/series` · `/api/series/letters` | One alphabet bucket of series · available letters |
| `GET` | `/api/tags` · `/api/tags/letters` | One alphabet bucket of tags · available letters |
| `GET` | `/api/publishers` · `/api/publishers/letters` | One alphabet bucket of publishers · available letters |
| `GET` | `/api/stats` | Library statistics (counts, sizes, per-format/per-language breakdowns) |
| `GET` | `/api/facets` | Distinct format/language values for the search-bar facet pickers |
| `POST` | `/api/sync` | Trigger re-index for all active libraries |
| `POST` | `/api/deep-index` | Queue a Deep Index run for every library not pending purge |
| `GET` | `/api/sync/status` | Current sync state |
| `GET` | `/api/sync/events` | Live sync status + indexing progress over SSE (see [SYNC-EVENTS.md](./SYNC-EVENTS.md)) |
| `GET`/`POST` | `/api/libraries` | List libraries · add a library |
| `GET`/`PUT`/`DELETE` | `/api/libraries/{id}` | Details · update · deactivate (7-day purge) |
| `POST` | `/api/libraries/{id}/reactivate` | Cancel pending purge |
| `POST` | `/api/libraries/{id}/purge` | Force-purge now (skip the grace period) |
| `POST` | `/api/libraries/{id}/sync` | Trigger sync for one library |
| `POST` | `/api/libraries/{id}/reindex` | Force a full re-read of one library (bypass checkpoint gating) |
| `POST` | `/api/libraries/{id}/deep-index` | Queue a Deep Index run for one library |
| `GET`/`PUT` | `/api/settings` | Current settings · update settings |
| `GET`/`PUT` | `/api/settings/telegram` | Telegram notification settings · update them |
| `POST` | `/api/settings/telegram/test` | Send a test message |
| `GET` | `/api/settings/telegram/status` | Delivery status (last success, last failure and its error) |
| `GET` | `/opds/` | OPDS root navigation feed (Basic Auth) |
| `GET` | `/opds/authors` · `/opds/series` · `/opds/genres` | Authors / series / tags index feeds (Basic Auth) |
| `GET` | `/opds/opensearch.xml` | OpenSearch description XML (Basic Auth) |
| `GET` | `/opds/search` | Search results OPDS feed (Basic Auth) |
| `GET` | `/opds/books/{id}/files/{fileID}` | OPDS file download (Basic Auth) |
| `GET` | `/opds/books/{id}/cover` | Book cover image (unauthenticated) |
| `GET` | `/opds/books/{id}/cover/thumbnail` | Book cover thumbnail (unauthenticated) |
| `POST` | `/mcp` | MCP (Streamable HTTP, stateless): `list_libraries`, `list_facets`, `search_books`, `get_book`, `similar_books` — no app auth (see [API.md](./API.md#mcp-endpoint-mcp)) |
| `GET` | `/*` | SPA: serve embedded static asset or fall back to `index.html` |

Full request/response shapes for these endpoints live in
[API.md](./API.md).

---

## SPA Serving Logic

The `/*` catch-all handler implements this decision tree:

```
Request path
    │
    ├── /api/*, /opds/* (and /mcp/*) never get here: their sub-routers answer
    │   unknown paths with 404 (never serve HTML for API/OPDS misses)
    │
    ├── / or "" → serve via http.FileServer (index.html)
    │
    ├── file exists in embedded FS and is not a directory → serve file
    │
    ├── has non-.html extension → 404 (guard: don't serve HTML for missing .js/.css/.png)
    │
    └── else → rewrite to / and serve index.html (SPA client-side routing)
```

This prevents two common SPA-serving bugs:
1. Missing static assets silently returning `200` with HTML content (breaks JS/CSS loading).
2. API route typos returning the SPA shell instead of a proper `404`.

---

## Embedding: `web/embed.go`

```go
//go:embed all:dist
var embedFS embed.FS
```

- `all:` prefix includes files starting with `.` or `_` (future-proofing).
- `fs.Sub(embedFS, "dist")` strips the `dist/` prefix so paths resolve as `/index.html`, `/assets/main.js`, etc.
- The result is cached in a package-level `http.FileSystem` variable via `init()`.

---

## Test Strategy

Every package owns its tests and follows the same testify-suite layout. Tests are hermetic: no `npm run build` output, no external services, a fresh temp dir / DB per test.

### Test Layout (testify suites)

All tests use `github.com/stretchr/testify/suite`. Three non-negotiable rules:

1. **One entry point per package.** Exactly one top-level `func Test<Package>(t *testing.T)` exists in the package — no other top-level `Test*` function. It lives in a **runner file** named after the package (`<package>_test.go`), alongside shared setup (a `baseSuite` + common helpers).
2. **The entry point only wires suites.** Its body is nothing but `suite.Run(t, new(<suite>))` calls — one per suite, as many as needed — and holds no test logic.
3. **All real tests are suite methods.** Every assertion lives in a method on a `suite.Suite`, using suite assertion methods (`s.Equal`, `s.Require().NoError`, …). A bare `func TestX(t *testing.T)` containing `require.X(t, …)`/`assert.X(t, …)` is forbidden.

Layout that follows from the rules:

- **One file per area of behaviour**, each declaring its own `suite.Suite` (embedding `baseSuite` when it shares setup) and its test methods. Use `s.Run(name, func(){…})` for table subtests.

This groups `go test` output by suite, isolates fixtures per concern, and gives each suite its own `SetupTest`/`TearDownTest`. A single-concern package keeps one suite (`covers`, `server`); a multi-concern package splits per concern (`ebook`, `ingest`).

### Coverage by package

| Package | Runner | Suites / focus |
| :--- | :--- | :--- |
| `internal/server` | `TestServer` | Router + SPA serving via `httptest` + `fstest.MapFS`: health, SPA routing & directory fallback, `/api` & `/opds` 404 protection, static-asset serving |
| `internal/covers` | `TestCovers` | Cover store (save / read / overwrite) and HTTP serving with placeholder fallback |
| `internal/ebook`  | `TestEbook`  | Per-format parsers (`epub`/`fb2`/`mobi`/`pdf`) + dispatcher, against `testdata/` fixtures |
| `internal/ingest` | `TestIngest` | Source parsers (`folder`/`calibre`/`inpx`), merge/importer, extractor and read outcomes, lazy backfill, Deep Index, against a temp folio DB + generated fixtures |
| `internal/api` | `TestAPI` | REST handlers over a temp DB: `booksSuite`, `matchSuite`, `enrichSuite`, `editSuite`, `coverSearchSuite`, `librariesSuite`, `listsSuite`, `lettersBoundsSuite`, `statsSuite`, `metaSuite`, `contractSuite`, `utilSuite` |
| `internal/opds` | `TestOPDS` | Catalog handlers: `authSuite` (Basic Auth), `downloadSuite`, `feedsSuite` (golden-file feed XML), `enrichSuite` (offline metadata backfill on the acquisition feed) |
| `internal/db` | `TestDB` | `booksFilterSuite` (the Bob dynamic book filter + `scanBook`/`GetBook` column-drift guard), `ftsSuite`, `ftsMigrationSuite`, `connSuite`, `writeGuardSuite`, `deepIndexWorkSuite`, `similarSuite`, `readFailuresSuite` |
| `internal/notify` | `TestNotify` | Telegram notifier: `configSuite`, `storeSuite` (settings store), `clientSuite` (Telegram client against an `httptest` fake), `statusSuite`, `formatSuite`, `queueSuite` (delivery queue), `serviceSuite`, `crashSuite` (crash de-duplication), `runsSuite`, `errWindowSuite`, `leakSuite` |
| `internal/logging` | `TestLogging` | `loggingSuite` (logger construction), `reportedSuite` (`MarkReported`), `sinkSuite` (the `WithErrorSink` tap) |
| `internal/sync` | `TestSync` | Sync engine: `engineSuite`, `engineEventsSuite`, `reporterSuite`, `schedulerSuite`, `watcherSuite` (fsnotify debounce) |
| `internal/mcpserver` | `TestMcpserver` | `serverSuite`, `toolsSuite`, `summarySuite`, `linksSuite` |
| `internal/settings` | `TestSettings` | `settingsSuite` (OPDS credentials), `telegramSuite` |
| `internal/tools/layoutcheck` | `TestLayoutcheck` | `layoutSuite`: the source-layout rules, generated/test-file handling, exit codes |

### Fixtures & hermeticity

- `server` injects a `fstest.MapFS` with a minimal `index.html` (+ optional assets) instead of real build output.
- `ebook` uses committed sample files under `internal/ebook/testdata/` (generated by `gen.go`).
- `ingest` and `covers` build everything in `t.TempDir()` — a fresh folio DB (`db.Open`), cover store, and synthetic Calibre `metadata.db` / `.inpx` / folder trees per test.

> **Note (SQLite driver):** when opening an external SQLite file (e.g. Calibre's `metadata.db`) read-only with `modernc.org/sqlite`, use a **plain path** with a query string (`path + "?mode=ro"`), not a `file:` URI. modernc mis-reads `file:` URIs — returning an empty schema — once the process has opened other SQLite databases.

---

## Package Structure

### Decision: all `internal/`, no `pkg/`

Folio is a self-hosted single-binary application with no external consumers of
its Go packages, so everything private lives under `internal/` (enforced by the
compiler). There is no `pkg/`.

### Layout

```
cmd/
└── folio-idx/
    └── main.go            # Composition root: wire deps, serve, graceful shutdown

internal/
├── config/                # Env-var config parsing (caarlos0/env)        [leaf]
├── logging/               # slog logger construction + error tap           [leaf]
│                          #   (WithErrorSink, MarkReported)
├── server/                # Router assembly + global middleware
│   ├── server.go          #   New(log, Handlers, env, noColor, publicURL) → *chi.Mux; Registrar iface
│   ├── middleware.go      #   proxyHeaders, sameSiteGuard, formBodyGuard + slog request-logger bridge
│   └── recoverer.go       #   slog-aware panic recovery + crash report
├── api/                   # REST handlers (/api/*) — books, lists, libraries,
│   │                      #   sync, stats, facets, health; JSON responses
│   ├── base.go            #   shared response helpers (writeJSON / writeError)
│   ├── books_handler.go · catalog_handler.go · libraries_handler.go · sync_handler.go
│   │                      #   per-area constructors + Register(chi.Router) + needed ifaces
│   ├── facets.go · stats.go · letters.go · lists.go   # browse / facet / stats endpoints
│   ├── books.go · edit.go · covers_search.go   # book detail/lazy tiers, edits, cover search
│   ├── events.go          #   GET /api/sync/events SSE stream
│   ├── guard.go           #   write-guard acquire budget (2s) → 503
│   ├── enrich.go          #   on-view online enrichment (gap-fill + hash restamp)
│   ├── match.go           #   Fix Match endpoints (search/apply a metadata-source volume)
│   └── bookview.go        #   DB rows → API view (+ annotation sanitize)
├── settings/              # /api/settings handler (thin adapter over the auth
│   ├── settings.go        #   service; Service interface satisfied by *auth.Authenticator)
│   └── telegram.go        #   /api/settings/telegram handler; TelegramService iface
│                          #   satisfied by *notify.Service
├── notify/                # Opt-in Telegram owner notifications: settings store,
│                          #   Bot API client, bounded send queue, delivery status,
│                          #   crash reports de-duplicated for an hour (crash.go),
│                          #   run outcomes: failed/recovered runs, newly unreadable
│                          #   books, mass removals (runs.go), and a 5-minute summary
│                          #   of other errors (errwindow.go)
├── auth/                  # OPDS Basic Auth: credential storage, hashing,
│   ├── auth.go            #   Authenticator: Middleware, View/SetCredentials, caches
│   └── store.go           #   settings-table credential persistence
├── opds/                  # OPDS handlers + Atom XML (/opds/*)
│   ├── opds.go            #   Handler + Register; injected CoverServer/Authenticator ifaces
│   ├── atom.go            #   hand-rolled OPDS feed structs (see API.md)
│   ├── feeds.go · opensearch.go · download.go · enrich.go
├── db/                    # Persistence layer
│   ├── db.go              #   Open(), migrations, WAL/FK/busy_timeout pragmas
│   ├── booksfilter.go     #   dynamic book filter (Bob builder; see DATABASE.md)
│   ├── fts.go · similar.go   # books_fts writes (bob) · "similar books" candidates
│   ├── writeguard.go      #   process-wide WriteGuard + WithTx / TxPanic
│   ├── fold.go · keys.go · errors.go   # case-fold keys, settings keys, SQLite error helpers
│   ├── migrations/        #   goose numbered SQL 001–009 (see DATABASE.md)
│   └── dbq/               #   sqlc-generated queries + models
├── mcpserver/             # Read-only MCP server (/mcp): book discovery tools
│   ├── server.go          #   New(log, db, publicURL, version) → stateless Streamable HTTP handler
│   ├── tools.go           #   list_libraries / list_facets / search_books / get_book / similar_books
│   ├── summary.go         #   BookSummary / BookDetail shapes, excerpt building
│   └── links.go           #   bookLink: {PUBLIC_URL}/books/{id}
├── bookfile/              # Book-file download streaming shared by /api and /opds (Serve, ContentType)
├── sync/                  # Background sync engine
│   ├── engine.go          #   New(), Start(), Stop(), triggers, Status()
│   ├── parser.go          #   Parser/Checkpointer/ReadFailureRechecker interfaces (defined at the consumer)
│   ├── scheduler.go       #   per-library tickers + purge-deadline sweep
│   ├── purge.go           #   deadline sweep + RequestPurge teardown
│   ├── reporter.go        #   indexing-progress reporter → events broker
│   └── watcher.go         #   fsnotify + debounce
├── ingest/                # Source parsers + reconciliation
│   ├── ingest.go          #   package doc + shared CoverStore/Result/Reporter ifaces
│   ├── calibre.go · inpx.go · folder.go   # one sync.Parser impl per source type
│   ├── driver.go          #   shared reconcile lifecycle (runReconcile; stamps imported_at)
│   ├── recheck.go         #   post-sync (and skipped-sync) re-check of stale metadata read-failure rows
│   ├── reconcile.go       #   upsert/diff books & files (stable identity)
│   ├── merge.go           #   pure merge decision (planMerge, injected rank) + applyPlan
│   ├── importer.go        #   transactional batch writer (add/remove, saveCoverIfBetter, cover invalidation)
│   ├── insert.go · record.go   # row insert + the in-flight bookRecord model
│   ├── groupkey.go        #   library_key + content_hash
│   ├── genres.go          #   genre taxonomy normalization (see EBOOK-PARSING.md)
│   ├── identifier.go      #   identifier cleaning/dedup (see EBOOK-PARSING.md)
│   ├── extract.go         #   lazy cover/metadata extractor (Cover, Description, readBook)
│   ├── outcome.go · readfailure.go   # read outcomes (done/broken/retryable) + book_read_failures writes
│   ├── backfill.go        #   LocalBackfiller: on-view metadata fill (REST + OPDS)
│   ├── deepindex.go       #   DeepIndexer: bulk description fill (paged, worker pool, single writer)
│   ├── coverstate.go      #   CoverState: books.cover_state for the cover store
│   ├── source.go          #   resolver: library layout → ebook.Source (archiveCache, withinRoot, CheckReadRoot)
│   ├── booklookup.go      #   BookLookup: book → metasearch.Query (ISBN first) for the Coordinator
│   └── enrich.go          #   VolumeToMetadata: Google Books volume → ebook.Metadata
├── ebook/                 # Ebook file parsers (epub/fb2(.zip)/mobi+azw3/pdf)
│   │                      #   assembled into a Dispatcher by main and injected
├── googlebooks/           # Minimal stdlib Google Books client              [leaf]
├── metasearch/            # Federated metadata + cover providers
│   │                      #   Registry fans queries to capability-matched sources;
│   │                      #   Aggregator merges/deduplicates results; Coordinator
│   │                      #   wires lookup → query → enrich; retry wraps transient
│   │                      #   failures. Adapters (amazon/goodreads/googlebooks/
│   │                      #   openlibrary) live under providers/; no provider logic
│   │                      #   in this package itself.
├── libtype/               # Library-type constants (calibre/inpx/folder)   [leaf]
│                          #   Dependency-free; shared by API, composition root,
│                          #   sync engine, and ingest without import cycles.
├── htmltext/              # HTML annotation → plain text / entity tables    [leaf]
├── covers/                # Cover store + HTTP serving + placeholder       [leaf]
│                          #   The cover Store also generates and serves aspect-preserving
│                          #   thumbnails (ServeThumbnail, generated eagerly in writeFile
│                          #   alongside the cover); the cover-serving method is ServeCover.
├── events/                # Sync-event broker (SSE fan-out + coalescing)   [leaf]
└── tools/layoutcheck/     # Dev tool run by `task lint`: methods grouped by receiver,
                           #   no compile-time interface guards (stdlib go/ast only)
```

Per-format parser internals (metadata, covers, the annotation pipeline) are
documented in [EBOOK-PARSING.md](./EBOOK-PARSING.md).

### Composition root: `main.go`

`cmd/folio-idx/main.go` is the only place that knows about all packages. It
wires dependencies in order and starts the server; no business logic lives here.
One `ebook.Dispatcher` owns the per-format parser set (`epub`/`fb2`/`mobi`/`pdf`)
and is injected into the extractor and every source parser, so no package reaches
for a global registry. Its `Rank` method (EPUB 4, FB2 3, MOBI/AZW/AZW3 2,
PDF 1, unknown 0) is the only format ranking `ingest` uses: every source parser
receives the dispatcher — `NewCalibreParser(log, parser)`,
`NewINPXParser(log, parser)` and `NewFolderParser(log, parser)` — and hands it
through `runReconcile(…, reader, scan)`, which passes `reader.Rank` to
`newImporter(log, db, covers, batchSize, rank)` and uses the dispatcher's
candidate traits for the post-sync broken-read re-check
(`recheckReadFailures`, see [DATABASE.md](./DATABASE.md#read-failures)). The
rank decides cover saves
(`saveCoverIfBetter`), cover invalidation and metadata ownership
(`planMerge`). `ingest` and `covers` hold no format names of their own:
`ingestSuite.TestNoFormatNames` parses their non-test sources and fails on a
string literal equal to a registered format label or containing its dotted
extension, and on a format-specific `ebook` symbol (`ebook.Format*`,
`ebook.New<Format>`, `ebook.<Format>Parser`, `ebook.ParseFB2Description`).
Words inside identifiers (`fb2GenreMap`) and comments are not checked, and
identifier-scheme literals such as `"mobi-asin"` pass. One `ingest.Extractor` (built over `*sql.DB` and the
dispatcher and the process-wide write guard: `NewExtractor(db, log, reader, guard)`, the guard serializing its cover-flow `book_read_failures` writes) is shared by the cover store,
the on-view `LocalBackfiller` (`NewLocalBackfiller(log, db, guard, extractor)`,
itself shared by the books handler and the OPDS handler) and the Deep Index
(`NewDeepIndexer(log, db, guard, extractor)`, handed to the engine with
`sync.WithDeepIndexer`) — always passed as a constructor parameter, not a setter. It reads a
book's files through `ingest`'s resolver (`internal/ingest/source.go`), the only
reading code that knows the library layout: a Calibre or folder file is
`library.Path` + `source_path`, while an INPX book is an entry of a sibling
archive (`filepath.Join(filepath.Dir(library.Path), archiveName)`) that inflates
lazily into memory. An `archiveCache` opens each archive once per caller
(`os.Open` → `ebook.TagIO` → `zip.NewReader`, so a disk read error stays
retryable), and `withinRoot` rejects a `source_path` that escapes its root.
Nothing is written to disk. One `metasearch.Registry` holds the Amazon,
Goodreads and Open Library providers (8s per-request timeout each) and the Google
Books adapter over a `googlebooks.Client` built from `cfg.GoogleKey` (an empty key
uses the anonymous quota). A `metasearch.Aggregator` over it backs cover search,
and a `metasearch.Coordinator` (with `ingest.NewBookLookup`) backs on-view online
enrichment and Fix Match through the registry's identify-capable source, Google
Books; the cover store doubles as the `CoverSaver` for covers it fetches. One `auth.Authenticator` owns OPDS
credentials and is shared by the settings and opds handlers. One
`notify.Service` (built over the store, the Telegram client for
`cfg.TelegramAPIBase` and `cfg.PublicURL`) delivers the opt-in Telegram messages;
construction order is base logger, database and write guard, notifier, tapped
logger (`logging.WithErrorSink(base, notifier)`), then the dispatcher, sync
engine and server. Only `db.Open` and `notify.New` keep the untapped base logger
(the notifier's own records must never loop back into it); every other
component logs through the tapped one, so an Error not already reported by a
producer (`logging.MarkReported`) reaches the error window.

The notifier is created before the dispatcher because it is injected into all
three producers (`ebook.WithCrashReporter`, `sync.WithNotifier`,
`server.Handlers.Crashes`). It is started after the sync
engine and stopped after it on shutdown. It backs the `/api/settings/telegram` handler
through the `settings.TelegramService` interface. An `events.Broker`
(created here) is shared by the sync engine (publisher) and the API SSE handler
(subscriber). `main` assembles the per-library-type parser
map the sync engine dispatches on, and `WithStatsObserver(catalogHandler)`
lets the engine notify the handler when stats change so it invalidates its cached stats:

```go
writeGuard := db.NewWriteGuard()
notifier, _ := notify.New(base, notify.NewStore(database, writeGuard),
    notify.NewClient(cfg.TelegramAPIBase, &http.Client{Timeout: 10 * time.Second}),
    notify.WithPublicURL(cfg.PublicURL))
log := logging.WithErrorSink(base, notifier)
parser := ebook.NewStandardDispatcher(ebook.WithCrashReporter(notifier))
extractor := ingest.NewExtractor(database, log, parser, writeGuard)
backfiller := ingest.NewLocalBackfiller(log, database, writeGuard, extractor)
coverStore, _ := covers.NewStore(cfg.DataDir, extractor, ingest.NewCoverState(database))
coverRegistry := metasearch.NewRegistry(msamazon.New(8*time.Second), msgoodreads.New(8*time.Second),
    msopenlibrary.New(8*time.Second),
    msgoogle.New(googlebooks.NewClient(log, cfg.GoogleKey), ingest.VolumeToMetadata))
coverSearch := metasearch.NewAggregator(log, coverRegistry)
enricher := metasearch.NewCoordinator(log, coverRegistry, ingest.NewBookLookup(database))
authn := auth.New(log, database, writeGuard)
authn.WarnIfUnprotected(ctx)
broker := events.NewBroker()
catalogHandler := api.NewCatalog(log, database, ingest.CanonicalGenres())
deepIndexer := ingest.NewDeepIndexer(log, database, writeGuard, extractor)
syncEngine, _ := sync.New(
    log, database, writeGuard, parserMap, coverStore, // parsers get the dispatcher (Rank)
    sync.WithEvents(broker), sync.WithStatsObserver(catalogHandler),
    sync.WithDeepIndexer(deepIndexer), sync.WithNotifier(notifier))
syncEngine.Start()
notifier.Start()

srv := &http.Server{Addr: ":" + cfg.Port, Handler: server.New(log, server.Handlers{
    API: []server.Registrar{
        api.NewBooks(log, database, writeGuard, coverStore, backfiller, enricher, coverStore, coverSearch),
        catalogHandler,
        api.NewLibraries(log, database, writeGuard, syncEngine, cfg.LibraryRoot),
        api.NewSync(log, syncEngine, broker),
        settings.New(log, authn),
        settings.NewTelegram(log, notifier),
    },
    OPDS:    opds.New(log, database, coverStore, backfiller, authn, cfg.PublicURL),
    MCP:     mcpserver.New(log, database, cfg.PublicURL, buildVersion()),
    Crashes: notifier,
}, cfg.Env, cfg.NoColorEnabled(), cfg.PublicURL)}
```

**OPDS credential changes need no cross-handler hook.** The settings and opds
handlers share the one `*auth.Authenticator` (injected as the `settings.Service`
and `opds.Authenticator` interfaces). A `PUT /api/settings` calls
`Authenticator.SetCredentials`, which **self-invalidates** the Authenticator's
internal credential and auth-success caches, so the new Basic Auth password takes
effect on the next OPDS request without a restart — there is no `OnSettingsChange`
/ `InvalidateCredentials` wiring between the handlers.

### Dependency rules

`server` imports only the `api` package — for the built-in `Health` handler — and
`logging`, for the `MarkReported` error-tap marker. The composition root builds every concrete handler and hands them in
via `server.Handlers{API: []Registrar, OPDS: Registrar, MCP: http.Handler, Crashes:
CrashReporter}`, so `server` needs no handle on `opds`, `settings`, `mcpserver`,
`sync`, or the leaves. Each handler package declares the narrow interfaces it
consumes (`opds.CoverServer` / `opds.Authenticator` / `opds.MetadataFiller`,
`settings.Service`, the api `*_handler.go` interfaces) rather than importing the
concrete engines, so `covers`/`auth` stay injected behind interfaces; `api` takes
the concrete `*ingest.LocalBackfiller` (see below).
Verified import graph (`go list`):

| Package | Imports (internal) |
| :--- | :--- |
| `config`, `logging`, `htmltext`, `covers`, `googlebooks`, `events`, `libtype`, `db/dbq`, `tools/layoutcheck` | _(none — leaves)_ |
| `db` | `db/dbq`, `htmltext` |
| `notify` | `db`, `db/dbq` |
| `settings` | `notify` |
| `bookfile` | `db/dbq`, `ebook` (`ContentType`), `libtype` |
| `ebook` | `htmltext` |
| `metasearch` | `ebook` |
| `metasearch/providers/*` | `metasearch` (+ `ebook`, `googlebooks` for the Google Books adapter) |
| `auth` | `db`, `db/dbq` |
| `ingest` | `db`, `db/dbq`, `ebook`, `htmltext`, `googlebooks`, `libtype`, `metasearch` |
| `sync` | `db`, `db/dbq`, `events`, `ingest`, `libtype`, `logging`, `notify` |
| `api` | `db`, `db/dbq`, `sync`, `bookfile`, `ebook`, `events`, `ingest`, `libtype`, `metasearch` |
| `opds` | `db`, `db/dbq`, `bookfile` |
| `mcpserver` | `db`, `db/dbq`, `htmltext` |
| `server` | `api`, `logging` |

`settings` imports only `notify`, for the Telegram settings and status types in
its `TelegramService` interface; it drives the auth service purely through its
own `Service` interface. `auth` is a near-leaf over `db` (the write guard) and
`db/dbq`. Neither
`opds` nor `api` imports `covers` or `auth` — covers serving, cover saving, and
OPDS Basic Auth all arrive as injected interfaces.

`api` imports `metasearch` for the `Volume` / `Query` / `CoverCandidate` types in
its `MetadataEnricher` and `CoverSearcher` interfaces and the Fix Match response
mapping (the enricher and cover search themselves are injected as interfaces). It
imports `ingest` for the concrete `*ingest.LocalBackfiller` and a few pure helpers
(`CanonicalizeGenres`, `CleanIdentifiers`, `ApplyIdentifierQuery`). It imports the
`events` leaf for the `EventBroker` interface subset and the `Event`/`Subscription`
types the `/api/sync/events` SSE stream frames.

### Type ownership

- **sqlc types** live in `db/dbq/`; other packages import them for query
  results and parameters.
- **`ebook.Metadata`** is the shared return type of every format parser;
  `ingest` is its primary consumer. There is no shared `model/` package — types
  belong where they are produced.
- **Interfaces for decoupling:** each handler package declares the interfaces it
  needs next to the handler that uses them — `api`'s `SyncEngine`/`EventBroker`
  (`sync_handler.go`), `CoverServer`/`MetadataEnricher`/`CoverSaver`
  (`books_handler.go`) and `CoverSearcher` (`covers_search.go`), `opds`'s
  `CoverServer`/`Authenticator`/`MetadataFiller` (`opds.go`), and `settings`'s `Service` (`settings.go`) and `TelegramService` (`telegram.go`). There is no `deps.go`;
  the shared per-handler response helpers live in `api/base.go`. Concrete
  implementations in `ingest`/`covers`/`auth`/`sync`/`metasearch` satisfy them
  structurally, so `opds`/`settings` never import `auth`/`covers`, `api` never
  imports `covers`/`auth`, and `server` stays free of leaf imports.

### Logging tap

`logging.WithErrorSink(base, sink)` wraps a logger's handler in a `sinkHandler`
that shares the original handler (and so its output mutex): log output is
byte-identical with or without the tap. After writing the line, every record at
`slog.LevelError` or above is handed to the `ErrorSink` as a clone, with the
attrs added through `With`/`WithGroup` already baked in, unless the context was
marked with `logging.MarkReported`. Sinks must not block or log through the
tapped logger.

Contributor rule: a new Error log on a path that already notifies (a run failure,
a crash) must log with a context marked by `logging.MarkReported(ctx)`, otherwise
the owner receives the failure twice.

### `internal/mcpserver`

A read-only MCP server (`github.com/modelcontextprotocol/go-sdk`) exposing five
tools: `list_libraries`, `list_facets`, `search_books`, `get_book` and
`similar_books`. `New`
returns an `http.Handler` (stateless Streamable HTTP, JSON responses) that
`server` mounts at `/mcp` through the `Handlers.MCP` field; `server` sees only
`http.Handler`. The server identity is `folio` / `Folio library`, with the version
supplied by `buildVersion()` in `main.go` (the module version, or `dev`). Searches
go through the same `db` dynamic filter as the web UI, so a trailing `*` is a
prefix match and results rank by BM25; `similar_books` uses `db.SimilarBooks`
(distinctive description terms, then same series, same author, shared genres).
Result links are built from `PUBLIC_URL`.
The package performs no authentication and makes no outbound calls. Tool
contracts are in [API.md](./API.md#mcp-endpoint-mcp).

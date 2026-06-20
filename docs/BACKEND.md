# Backend

> Go server: routing, middleware, and SPA embedding.

---

## Entry Point

**Binary:** `cmd/folio-idx/main.go`
**Module:** `github.com/Toshik1978/folio`
**Import path:** `github.com/Toshik1978/folio/web` (for embedded SPA assets)

```
main() → run():
  godotenv.Load() → config.MustParse() → logging.New()
  → db.Open() → ebook.NewDispatcher(epub,fb2,mobi,pdf) → ingest.NewExtractor(…,parser)
  → covers.NewStore() → ingest.NewEnricher()
  → auth.New().WarnIfUnprotected() → events.NewBroker() → api.NewCatalog()
  → sync.New(…, buildParsers(log,parser), …, WithStatsObserver(catalog)).Start()
  → http.Server{Handler: server.New(log, server.Handlers{API:[…], OPDS:…}, env, noColor)}
  → serve(): ListenAndServe until SIGINT/SIGTERM → graceful Shutdown + Engine.Stop
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
build the absolute OPDS OpenSearch URL — see `NETWORKING.md`), and `GOOGLE_KEY`
(optional Google Books API key for online enrichment; empty uses the anonymous
quota). OPDS Basic Auth credentials are **not** environment variables — they are
configured at runtime via `PUT /api/settings` and stored hashed in the `settings`
table (the `auth` package owns them). Structured logging uses the standard library `log/slog`. `run`
returns a non-zero exit code on a failed dependency (DB open, cover store, sync
engine init, or server error) so the container restarts cleanly; shutdown is
graceful (drain in-flight requests within a 15s timeout, then stop the sync
engine).

---

## Router: `server.New()`

`server.New(log, Handlers, env, noColor)` (→ `newWithFS`) builds the root
`go-chi/chi/v5` router. It accepts an `http.FileSystem` to decouple static file
serving from the embed mechanism (enables test injection via `fstest.MapFS`).
Handlers arrive already constructed by the composition root, each satisfying the
local `Registrar` interface (`Register(chi.Router)`); `server` knows nothing about
their concrete types. The root router wires the built-in health route, mounts the
ordered API + OPDS registrars, and adds the SPA catch-all:

```go
r.Route("/api", func(api chi.Router) {
    api.Get("/health", apipkg.Health) // built-in liveness
    for _, reg := range h.API {       // api.* handlers + the settings handler, in order
        reg.Register(api)
    }
})
r.Route("/opds", func(o chi.Router) { h.OPDS.Register(o) }) // internal/opds
r.HandleFunc("/*", webHandler(fs))                          // SPA + static assets
```

### Middleware Stack

Applied globally on the root router, in this order:

| Middleware | Purpose |
| :--- | :--- |
| `proxyHeaders` (custom) | Sets `URL.Scheme` from `X-Forwarded-Proto` (falling back to the TLS state). It deliberately does **not** honor `X-Forwarded-Host` — see [NETWORKING.md](./NETWORKING.md#canonical-host-public_url-not-x-forwarded-host) |
| `middleware.Logger` (chi) | Request logging via chi's logger, bridged to the `log/slog` logger through a `slogger` adapter (`Print` → `slog.Info`) — **registered only when `env == "development"`** |
| `middleware.Recoverer` | Panic recovery → `500` |

The OPDS sub-router adds HTTP Basic Auth on its protected group (see
[NETWORKING.md](./NETWORKING.md)).

### Route Table

`/api/*` is mounted from `internal/api` (`Routes()`), `/opds/*` from
`internal/opds`; both are listed with their full external paths.

| Method | Path | Description |
| :--- | :--- | :--- |
| `GET` | `/api/health` | Liveness — `{"status":"ok"}` |
| `GET` | `/api/books` | Paginated, filtered book list (BM25 order for searches) |
| `GET` | `/api/books/{id}` | Single book detail (lazy metadata backfill + online enrichment) |
| `GET` | `/api/books/{id}/files/{fileID}` | Streamed file download (one format) |
| `GET` | `/api/books/{id}/cover` | Cached cover image (placeholder fallback) |
| `GET`/`POST` | `/api/books/{id}/match` | Fix Match: Google Books candidates · apply a chosen volume |
| `GET` | `/api/authors` · `/api/authors/letters` | One alphabet bucket of authors · available letters |
| `GET` | `/api/series` · `/api/series/letters` | One alphabet bucket of series · available letters |
| `GET` | `/api/tags` · `/api/tags/letters` | One alphabet bucket of tags · available letters |
| `GET` | `/api/publishers` · `/api/publishers/letters` | One alphabet bucket of publishers · available letters |
| `GET` | `/api/stats` | Library statistics (counts, sizes, per-format/per-language breakdowns) |
| `GET` | `/api/facets` | Distinct format/language values for the search-bar facet pickers |
| `POST` | `/api/sync` | Trigger re-index for all active libraries |
| `GET` | `/api/sync/status` | Current sync state |
| `GET` | `/api/sync/events` | Live sync status + indexing progress over SSE (see [SYNC-EVENTS.md](./SYNC-EVENTS.md)) |
| `GET`/`POST` | `/api/libraries` | List libraries · add a library |
| `GET`/`PUT`/`DELETE` | `/api/libraries/{id}` | Details · update · deactivate (7-day purge) |
| `POST` | `/api/libraries/{id}/reactivate` | Cancel pending purge |
| `POST` | `/api/libraries/{id}/purge` | Force-purge now (skip the grace period) |
| `POST` | `/api/libraries/{id}/sync` | Trigger sync for one library |
| `POST` | `/api/libraries/{id}/reindex` | Force a full re-read of one library (bypass checkpoint gating) |
| `GET`/`PUT` | `/api/settings` | Current settings · update settings |
| `GET` | `/opds/` | OPDS root navigation feed (Basic Auth) |
| `GET` | `/opds/authors` · `/opds/series` · `/opds/genres` | Authors / series / tags index feeds (Basic Auth) |
| `GET` | `/opds/opensearch.xml` | OpenSearch description XML (Basic Auth) |
| `GET` | `/opds/search` | Search results OPDS feed (Basic Auth) |
| `GET` | `/opds/books/{id}/files/{fileID}` | OPDS file download (Basic Auth) |
| `GET` | `/opds/books/{id}/cover` | Book cover image (unauthenticated) |
| `GET` | `/*` | SPA: serve embedded static asset or fall back to `index.html` |

Full request/response shapes for these endpoints live in
[API.md](./API.md).

---

## SPA Serving Logic

The `/*` catch-all handler implements this decision tree:

```
Request path
    │
    ├── /api/* or /opds/* → 404 (guard: never serve HTML for API/OPDS misses)
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

All tests use `github.com/stretchr/testify/suite`:

- A **runner file** named after the package (`<package>_test.go`) holds the package's *only* top-level `Test*` function, `Test<Package>(t)`, which just `suite.Run`s each suite. Shared setup (a `baseSuite` + common helpers) lives here.
- **One file per area of behaviour**, each declaring its own `suite.Suite` (embedding `baseSuite` when it shares setup) and its test methods. Use `s.Run(name, func(){…})` for table subtests.

This groups `go test` output by suite, isolates fixtures per concern, and gives each suite its own `SetupTest`/`TearDownTest`. A single-concern package keeps one suite (`covers`, `server`); a multi-concern package splits per concern (`ebook`, `ingest`).

### Coverage by package

| Package | Runner | Suites / focus |
| :--- | :--- | :--- |
| `internal/server` | `TestServer` | Router + SPA serving via `httptest` + `fstest.MapFS`: health, SPA routing & directory fallback, `/api` & `/opds` 404 protection, static-asset serving |
| `internal/covers` | `TestCovers` | Cover store (save / read / overwrite) and HTTP serving with placeholder fallback |
| `internal/ebook`  | `TestEbook`  | Per-format parsers (`epub`/`fb2`/`mobi`/`pdf`) + dispatcher, against `testdata/` fixtures |
| `internal/ingest` | `TestIngest` | Source parsers (`folder`/`calibre`/`inpx`) against a temp folio DB + generated fixtures |
| `internal/api` | `TestAPI` | REST handlers over a temp DB: `booksSuite`, `librariesSuite`, `listsSuite`, `metaSuite` |
| `internal/opds` | `TestOPDS` | Catalog handlers: `authSuite` (Basic Auth), `downloadSuite`, `feedsSuite` (golden-file feed XML) |
| `internal/db` | `TestDB` | `booksFilterSuite` — the Bob dynamic book filter + `scanBook`/`GetBook` column-drift guard |
| `internal/sync` | `TestSync` | Sync engine: `engineSuite`, `schedulerSuite`, `warmSuite` (cover warming), `watcherSuite` (fsnotify debounce) |

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
├── logging/               # slog logger construction                      [leaf]
├── server/                # Router assembly + global middleware
│   ├── server.go          #   New(log, Handlers, env, noColor) → *chi.Mux; Registrar iface
│   └── middleware.go      #   proxyHeaders + slog request-logger bridge
├── api/                   # REST handlers (/api/*) — books, lists, libraries,
│   │                      #   sync, stats, facets, health; JSON responses
│   ├── base.go            #   shared response helpers (writeJSON / writeError)
│   ├── books_handler.go · catalog_handler.go · libraries_handler.go · sync_handler.go
│   │                      #   per-area constructors + Register(chi.Router) + needed ifaces
│   ├── facets.go · stats.go · letters.go · lists.go   # browse / facet / stats endpoints
│   ├── enrich.go          #   on-view online enrichment (gap-fill + hash restamp)
│   ├── match.go           #   Fix Match endpoints (search/apply a Google volume)
│   └── bookview.go        #   DB rows → API view (+ annotation sanitize)
├── settings/              # /api/settings handler (thin adapter over the auth
│   └── settings.go        #   service; Service interface satisfied by *auth.Authenticator)
├── auth/                  # OPDS Basic Auth: credential storage, hashing,
│   ├── auth.go            #   Authenticator: Middleware, View/SetCredentials, caches
│   └── store.go           #   settings-table credential persistence
├── opds/                  # OPDS handlers + Atom XML (/opds/*)
│   ├── opds.go            #   Handler + Register; injected CoverServer/Authenticator ifaces
│   ├── atom.go            #   hand-rolled OPDS feed structs (see API.md)
│   ├── feeds.go · opensearch.go · download.go
├── db/                    # Persistence layer                             [leaf]
│   ├── db.go              #   Open(), migrations, WAL/FK/busy_timeout pragmas
│   ├── booksfilter.go     #   dynamic book filter (Bob builder; see DATABASE.md)
│   ├── migrations/        #   goose numbered SQL (consolidated; see DATABASE.md)
│   └── dbq/               #   sqlc-generated queries + models
├── bookfile/              # Shared file-format helpers (content types, ext)
├── sync/                  # Background sync engine
│   ├── engine.go          #   New(), Start(), Stop(), triggers, Status()
│   ├── parser.go          #   Parser/Checkpointer interfaces (defined at the consumer)
│   ├── scheduler.go       #   per-library tickers + purge-deadline sweep
│   ├── purge.go           #   deadline sweep + RequestPurge teardown
│   ├── warmer.go          #   cover warming
│   ├── reporter.go        #   indexing-progress reporter → events broker
│   └── watcher.go         #   fsnotify + debounce
├── ingest/                # Source parsers + reconciliation
│   ├── ingest.go          #   package doc + shared CoverStore/Result/Reporter ifaces
│   ├── calibre.go · inpx.go · folder.go   # one sync.Parser impl per source type
│   ├── driver.go          #   shared reconcile lifecycle (runReconcile; stamps imported_at)
│   ├── reconcile.go       #   upsert/diff books & files (stable identity)
│   ├── merge.go           #   pure merge decision (planMerge / filePriority) + applyPlan
│   ├── importer.go        #   transactional batch writer (add/remove, saveCoverIfBetter)
│   ├── insert.go · record.go   # row insert + the in-flight bookRecord model
│   ├── groupkey.go        #   library_key + content_hash
│   ├── genres.go          #   genre taxonomy normalization (see EBOOK-PARSING.md)
│   ├── identifier.go      #   identifier cleaning/dedup (see EBOOK-PARSING.md)
│   ├── extract.go         #   lazy cover/metadata extractor (Backfill)
│   └── enrich.go          #   online enricher (Google Books → ebook.Metadata)
├── ebook/                 # Ebook file parsers (epub/fb2(.zip)/mobi+azw3/pdf)
│   │                      #   assembled into a Dispatcher by main and injected
├── googlebooks/           # Minimal stdlib Google Books client              [leaf]
├── htmltext/              # HTML annotation → plain text / entity tables    [leaf]
├── covers/                # Cover store + HTTP serving + placeholder       [leaf]
└── events/                # Sync-event broker (SSE fan-out + coalescing)   [leaf]
```

Per-format parser internals (metadata, covers, the annotation pipeline) are
documented in [EBOOK-PARSING.md](./EBOOK-PARSING.md).

### Composition root: `main.go`

`cmd/folio-idx/main.go` is the only place that knows about all packages. It
wires dependencies in order and starts the server; no business logic lives here.
One `ebook.Dispatcher` owns the per-format parser set (`epub`/`fb2`/`mobi`/`pdf`)
and is injected into the extractor and the folder parser, so no package reaches
for a global registry. One `ingest.Extractor` (built over `*sql.DB` and the
dispatcher) is shared by the cover store and the sync engine — passed as a
constructor parameter, not a setter. An `ingest.Enricher` wraps a
`googlebooks.Client` built from `cfg.GoogleKey` (an empty key uses the anonymous
quota) and backs on-view online enrichment; the cover store doubles as the
`CoverSaver` for covers it fetches. One `auth.Authenticator` owns OPDS
credentials and is shared by the settings and opds handlers. An `events.Broker`
(created here) is shared by the sync engine (publisher) and the API SSE handler
(subscriber). `buildParsers(log, parser)` assembles the per-library-type parser
map the sync engine dispatches on, and `WithStatsObserver(catalogHandler)`
lets the engine notify the handler when stats change so it invalidates its cached stats:

```go
parser := ebook.NewDispatcher(ebook.NewEPUB(), ebook.NewFB2(), ebook.NewMOBI(), ebook.NewPDF())
extractor := ingest.NewExtractor(database, log, cfg.DataDir, parser)
coverStore, _ := covers.NewStore(cfg.DataDir, extractor)
enricher := ingest.NewEnricher(database, googlebooks.NewClient(log, cfg.GoogleKey))
authn := auth.New(log, database)
authn.WarnIfUnprotected(ctx)
broker := events.NewBroker()
catalogHandler := api.NewCatalog(log, database)
syncEngine, _ := sync.New(
    log, database, buildParsers(log, parser), coverStore, extractor,
    sync.WithEvents(broker), sync.WithStatsObserver(catalogHandler))
syncEngine.Start()

srv := &http.Server{Addr: ":" + cfg.Port, Handler: server.New(log, server.Handlers{
    API: []server.Registrar{
        api.NewBooks(log, database, coverStore, extractor, enricher, coverStore),
        catalogHandler,
        api.NewLibraries(log, database, syncEngine),
        api.NewSync(log, syncEngine, broker),
        settings.New(log, authn),
    },
    OPDS: opds.New(log, database, coverStore, authn, cfg.PublicURL),
}, cfg.Env, cfg.NoColorEnabled())}
```

**OPDS credential changes need no cross-handler hook.** The settings and opds
handlers share the one `*auth.Authenticator` (injected as the `settings.Service`
and `opds.Authenticator` interfaces). A `PUT /api/settings` calls
`Authenticator.SetCredentials`, which **self-invalidates** the Authenticator's
internal credential and auth-success caches, so the new Basic Auth password takes
effect on the next OPDS request without a restart — there is no `OnSettingsChange`
/ `InvalidateCredentials` wiring between the handlers.

### Dependency rules

`server` imports only the `api` package — for the built-in `Health` handler and
nothing else. The composition root builds every concrete handler and hands them in
via `server.Handlers{API: []Registrar, OPDS: Registrar}`, so `server` needs no
handle on `opds`, `settings`, `sync`, or the leaves. Each handler package declares
the narrow interfaces it consumes (`opds.CoverServer` / `opds.Authenticator`,
`settings.Service`, the api `*_handler.go` interfaces) rather than importing the
concrete engines, so `covers`/`auth`/`ingest` stay injected behind interfaces.
Verified import graph (`go list`):

| Package | Imports (internal) |
| :--- | :--- |
| `config`, `logging`, `htmltext`, `covers`, `db`, `googlebooks`, `events`, `settings` | _(none — leaves)_ |
| `bookfile` | `db/dbq`, `ebook` (format constants for Content-Type mapping) |
| `ebook` | `htmltext` |
| `auth` | `db/dbq` |
| `ingest` | `db`, `db/dbq`, `ebook`, `htmltext`, `googlebooks` |
| `sync` | `db/dbq`, `ingest`, `events` |
| `api` | `db`, `db/dbq`, `sync`, `bookfile`, `htmltext`, `ebook`, `googlebooks`, `events` |
| `opds` | `db`, `db/dbq`, `bookfile` |
| `server` | `api` |

`settings` imports no internal package at all (it drives the auth service purely
through its own `Service` interface). `auth` is a near-leaf over `db/dbq`. Neither
`opds` nor `api` imports `covers` or `auth` — covers serving, cover saving, and
OPDS Basic Auth all arrive as injected interfaces.

`api` imports the leaf `googlebooks` only for the `Volume` type in its
`MetadataEnricher` interface and Fix Match response mapping; it still never
imports `ingest` (the enricher is injected as an interface). It imports the
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
  (`sync_handler.go`) and `CoverServer`/`MetadataExtractor`/`MetadataEnricher`/
  `CoverSaver` (`books_handler.go`), `opds`'s `CoverServer`/`Authenticator`
  (`opds.go`), and `settings`'s `Service` (`settings.go`). There is no `deps.go`;
  the shared per-handler response helpers live in `api/base.go`. Concrete
  implementations in `ingest`/`covers`/`auth`/`sync` satisfy them structurally, so
  `api` never imports `ingest`, `opds`/`settings` never import `auth`/`covers`, and
  `server` stays free of leaf imports.

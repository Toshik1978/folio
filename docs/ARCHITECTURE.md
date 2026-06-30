# Architecture

> System-level design: how the pieces fit together.

---

## System Diagram

```
[ Browser ]            [ Reading App ]
     |                  (Moon+, KyBook)
     |                       |
     v                       |
[ Authenticator ]            |
 (SSO proxy / etc.)          |
     |                       |
     |-- /api/* ---> auth    |
     |-- /*    ---> auth     |
     |-- /opds* --> direct --+
     |                       |
     +----------+------------+
                |
                v
  +----------------------------+
  |   Single Docker Container  |
  |                            |
  |   +----------------------+ |
  |   |  Go Binary           | |
  |   |  (folio-idx)         | |
  |   |                      | |
  |   |  /api/*   chi router | |
  |   |  /*       embedded   | |
  |   |           SPA        | |
  |   |  /opds/*  Basic Auth | |
  |   |           + Atom XML | |
  |   |                      | |
  |   |  +------+ +-------+  | |
  |   |  |SQLite| |go-chi |  | |
  |   |  |+FTS5 | |Router |  | |
  |   |  +------+ +-------+  | |
  |   +----------------------+ |
  |                            |
  |   Volume Mounts:           |
  |     /data    (DB + covers) |
  |     /library (book files)  |
  +----------------------------+
                |
   +------------+------------+
   |            |            |
   v            v            v
[ Calibre ] [ INPX+ZIPs ] [ Raw Files ]
(read-only) (read-only)   (read-only)
```

---

## Core Design Decisions

### Single Binary, Single Container

The Vue 3 SPA is compiled to static assets (`web/dist/`), then embedded into the Go binary via `go:embed`. At runtime, `http.FileServer` serves these assets directly from memory. There is no NGINX sidecar, no separate frontend container, and no reverse proxy inside the container.

**Rationale:** Minimizes moving parts. A single `COPY` in the Dockerfile, a single process to monitor, a single port to expose.

### Read-Only Contract

The application never writes to:
- Book files on disk
- External Calibre `metadata.db`
- INPX/ZIP archives

The only write targets are the internal SQLite database and the extracted cover cache residing in `/data/` (e.g. `/data/folio.db` and `/data/covers/`).

### SPA Routing Strategy

The `/*` catch-all handler implements this decision tree:

1. **API/OPDS guard** — Requests to `/api/*` or `/opds/*` that don't match a registered handler return `404` (never serve HTML for missed API/OPDS routes).
2. **Root path** — `/` or empty path serves `index.html` directly via `http.FileServer`.
3. **Static asset check** — The embedded FS is probed. If the file exists and is not a directory, it is served directly.
4. **Missing asset guard** — Requests with a non-`.html` file extension that don't exist in the FS return `404` (prevents broken JS/CSS loads returning HTML).
5. **SPA fallback** — All remaining requests rewrite to `/` and serve `index.html`, letting Vue Router handle client-side navigation.

---

## Project Structure

```
folio/
├── cmd/
│   └── folio-idx/
│       └── main.go         # Composition root: wire deps, serve, graceful shutdown
├── internal/               # Private Go packages (compiler-enforced; no pkg/)
│   ├── api/                # REST handlers (/api/*) — books, lists, libraries, sync, stats, facets
│   ├── settings/           # /api/settings handler (thin adapter over the auth service)
│   ├── auth/               # OPDS Basic Auth: credential storage, hashing, middleware
│   ├── opds/               # OPDS catalog handlers + hand-rolled Atom XML (/opds/*)
│   ├── server/             # Router assembly, middleware, SPA serving
│   ├── db/                 # SQLite open/migrations, sqlc queries (dbq/), dynamic book filter (Bob)
│   ├── ingest/             # Source parsers (calibre/inpx/folder) + reconciliation/merge
│   ├── ebook/              # Per-format file parsers (epub/fb2(.zip)/mobi+azw3/pdf)
│   ├── googlebooks/        # Minimal stdlib Google Books client (enrichment / Fix Match)
│   ├── metasearch/         # Federated metadata + cover providers (registry, aggregator, retry); adapters under providers/
│   ├── libtype/            # Library-type constants (calibre/inpx/folder); dependency-free leaf shared across layers
│   ├── htmltext/           # HTML annotation → plain text / entity tables
│   ├── covers/             # Cover store + HTTP serving + embedded placeholder
│   ├── sync/               # Background sync engine (scheduler, fsnotify watcher, purge teardown)
│   ├── bookfile/           # Shared file-format helpers (content types, extensions)
│   ├── config/             # Env-var config parsing
│   ├── logging/            # slog logger construction
│   └── events/             # Sync-event broker (SSE fan-out); see SYNC-EVENTS.md
├── web/                    # Vue 3 SPA (separate npm project; see FRONTEND.md)
│   ├── embed.go            # go:embed directive, GetFileSystem()
│   ├── index.html          # SPA entry HTML
│   ├── src/                # components/, pages/, composables/, router.ts, api.ts, style.css
│   ├── public/             # Static assets
│   ├── dist/               # Build output (git-ignored; embedded into the binary at build)
│   ├── package.json        # npm dependencies & scripts
│   ├── vite.config.ts      # Vite + Vue + Tailwind/DaisyUI plugins + Vitest config
│   └── tsconfig.json       # TypeScript strict config
├── docs/                   # Living design documents
├── Dockerfile              # Multi-stage production build
├── Taskfile.yml            # Dev and build automation (go-task)
├── sqlc.yaml               # sqlc codegen config
├── go.mod / go.sum         # Go module definition
├── .gitignore
└── .dockerignore
```

The composition root (`cmd/folio-idx/main.go`) owns the wiring: it injects an
`ebook.Dispatcher` of the per-format parsers, assembles the per-library-type
parser map (`buildParsers`) the sync engine dispatches on, and registers a stats
observer (`WithStatsObserver`) the engine notifies when catalog stats change, so a
finished sync refreshes the cached catalog stats. The full `internal/` package layout, dependency graph, and
composition root are documented in [BACKEND.md](./BACKEND.md#package-structure).

### Boundary: `web/` Package

The `web/` directory serves a dual purpose:

1. **npm project** — Contains `package.json`, `vite.config.ts`, and all frontend source. `npm install` and `npm run build` operate here.
2. **Go package** — Contains `embed.go`, which declares `//go:embed all:dist` and exports `GetFileSystem()`. The Go compiler reads `web/dist/` at build time.

The Go module root is `/` (module path `github.com/Toshik1978/folio`). The `web`
package is imported as `github.com/Toshik1978/folio/web`.

---

## Technology Stack

| Layer | Technology | Version | Purpose |
| :--- | :--- | :--- | :--- |
| Backend language | Go | 1.26 | Server, embedding, file I/O |
| HTTP router | go-chi/chi/v5 | 5.3.x | Routing, middleware |
| Logging | log/slog (stdlib) | — | Structured logging via a custom `slog.Handler` (`logging.New`) that writes to **stdout** with per-env levels (dev → Debug, else Info), an `[ENV]` line tag, and ANSI colors when the output is a TTY; chi `middleware.Logger` bridged to slog for dev request logs |
| Config | caarlos0/env/v11 | 11.x | Struct-based environment variable parsing |
| Env files | joho/godotenv | 1.5.x | Auto-load `.env` files in development |
| SQLite driver | modernc.org/sqlite | 1.51.x | Pure-Go SQLite (no CGo), includes FTS5 |
| Migrations | pressly/goose/v3 | 3.27.x | Versioned SQL migration files (embedded) |
| DB codegen | sqlc | — | Type-safe Go from SQL (config in `sqlc.yaml`) |
| Dynamic queries | stephenafamo/bob | 0.45.x | Builder-only (no codegen) for the dynamic book filter |
| Scheduler | go-co-op/gocron/v2 | 2.21.x | Per-library periodic sync scheduling |
| File watching | fsnotify | 1.10.x | Kernel-level FS events (folder sources) |
| HTML sanitizer | microcosm-cc/bluemonday | 1.0.x | Sanitize annotations at the serve boundary |
| PDF parsing | pdfcpu/pdfcpu | 0.12.x | Pure-Go PDF cover/page extraction |
| Crypto | golang.org/x/crypto | — | OPDS password hashing (bcrypt) |
| Charset decoding | golang.org/x/text | — | Legacy encodings in ebook parsers (FB2 via `encoding/ianaindex`, MOBI cp1252 via `encoding/charmap`); name folding itself is stdlib `strings.ToUpper` (`db.Fold`) |
| Image decoding | golang.org/x/image | — | BMP/TIFF/WebP cover decoding for JPEG normalization (`covers/convert.go`) |
| UUID detection | gofrs/uuid/v5 | 5.4.x | Drop UUID-like junk identifiers during identifier cleaning (`ingest/identifier.go`) |
| Frontend framework | Vue 3 | 3.4.x | SPA (Composition API, `<script setup>`) |
| Router | Vue Router | 5.x | Client-side routing |
| Frontend lang | TypeScript | 6.x | Type safety across all `.ts` and `.vue` files |
| Bundler / tests | Vite · Vitest | 8.x · 4.x | Dev server, production build, unit/component tests |
| CSS framework | Tailwind CSS | 4.3.x | Utility-first styling via `@tailwindcss/vite` |
| Component classes | DaisyUI | 5.x | Tailwind plugin: component classes + multi-theme system |
| Icon font | PrimeIcons | 7.x | Icon set (`pi pi-*`); no Vue component library (e.g. PrimeVue) |
| Client sanitizer | DOMPurify | 3.x | Client-side re-sanitization of annotations before `v-html` (defense-in-depth over bluemonday) |

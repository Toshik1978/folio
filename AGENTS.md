# Folio Agent Onboarding Guide

This guide helps AI agents and developers set up, develop, and navigate the Folio repository.

---

## CLI Command Reference

All automation is managed via `go-task` (`Taskfile.yml`).

### Setup & Environment
* Initialize repository dependencies (npm packages, Go modules) and pre-create empty build dirs:
  ```bash
  task setup
  ```
* Regenerate sqlc query code after changing SQL queries or migrations:
  ```bash
  task generate
  ```

### Local Development Servers
* Run backend Go server (watches for changes, listens on `PORT` or default `8080`):
  ```bash
  task dev:backend
  ```
* Run frontend Vite dev server (supports Hot Module Replacement):
  ```bash
  task dev:frontend
  ```

### Compilation & Quality Gates
* Full compilation (Type-checks & builds Vue SPA, embeds assets, compiles Go binary):
  ```bash
  task build
  ```
* Run integration & unit test suites:
  ```bash
  task test
  ```
* Run all linters (Go + frontend) — the same gate CI enforces. Run it before finishing any change:
  ```bash
  task lint
  ```
* Auto-format the codebase (gofumpt for Go, Prettier for the frontend):
  ```bash
  task format
  ```
* Remove compiled binaries and temporary build assets:
  ```bash
  task clean
  ```

---

## Task Rules

These rules apply to every task. Non-negotiable.

1. **Golang Best Practices**: Follow idiomatic Go — standard library first, proper interface declarations, correct package layout, canonical error handling. Comply with community conventions and official Effective Go guidelines.
2. **Third-Party Dependencies Require Approval**: Before introducing any external dependency, ask for explicit approval. State the package name, what it solves, and why the standard library is insufficient. Do not add the dependency until approved.

   **Approved direct dependencies (recorded):**
   * `golang.org/x/net/html` — HTML tokenizer/parser for the Amazon product-page
     cover scraper (`internal/metasearch/providers/amazon`). The standard library
     has no HTML parser; this is the canonical x/ package and was already in the
     module graph as an indirect dependency. Approved 2026-06-25.

3. **Testing with testify suites**: All Go tests use `github.com/stretchr/testify`, organised as suites. Three non-negotiable rules:
   1. **One entry point per package** — exactly one top-level `func Test<Package>(t *testing.T)`. No other top-level `Test*` function may exist in the package.
   2. **The entry point only wires suites** — it consists solely of `suite.Run(t, new(...))` calls, one per `suite.Suite`, and contains no test logic itself.
   3. **All real tests are suite methods** — every assertion lives in a method on a `suite.Suite`, using suite assertion methods (`s.Equal`, `s.NoError`, `s.Require().NoError`, `s.Contains`, …). Never write a bare `func TestX(t *testing.T)` with `require.X(t, …)` / `assert.X(t, …)` for an actual test.
4. **Branch Naming**: Any branch created for feature work **MUST** use the prefix `feature/` (e.g. `feature/sync-events`). Never use `feat/`, `feat-`, or any other variant. Only `feature/` is permitted.

---

## Changelog vs. Release Notes

Folio keeps two separate, complementary files. Do not merge them.

* **`CHANGELOG.md` — machine-generated, do not hand-edit.** Commitizen
  regenerates it from conventional commits on `cz bump` (see `.cz.yaml`). It is
  the exhaustive, commit-level technical log for developers.
* **`RELEASE_NOTES.md` — curated, hand-written.** Short, specific,
  human-readable highlights for people running Folio. After a release is
  bumped, add a new entry derived from the fresh `CHANGELOG.md` section (and the
  diffs where needed): group by user-facing theme, call out breaking changes and
  upgrade steps, and skip noise like pure refactors or test-only commits. Use
  the same `## vX.Y.Z — YYYY-MM-DD` heading style so the two files cross-reference
  cleanly. `RELEASE_NOTES.md` links to `CHANGELOG.md`; `CHANGELOG.md` stays
  untouched.

---

## Core Architectural Constraints

When implementing features or bug fixes, you **MUST** adhere to the following rules:

1. **CGO-Free SQLite (`CGO_ENABLED=0`)**: The project must remain compatible with static Alpine/Distroless Docker compilation. Use `modernc.org/sqlite` as the database driver. Do **NOT** use `mattn/go-sqlite3` as it requires CGo.
2. **Versioned Migrations**: All SQLite schema changes must be declared inside numbered migration SQL files under `internal/db/migrations/` and run via `pressly/goose`. Do not execute ad-hoc DDL queries in codebase initialization.
3. **Read-Only Sources**: The catalog sources (Calibre DBs, ZIP archives, directories) are strictly read-only. The application must never write back to any book source files. Writable state is restricted to the `/data/` volume.
4. **SPA Embed Routing**: The Vue SPA resides in `web/` and compiles to `web/dist/`. It is embedded into the Go binary via `web/embed.go` using `go:embed`. Handlers must reject non-existent static asset routes from falling back to SPA index routing.

---

## Task-to-File Reference Map

To keep context windows small and focus edits, load only the specific documentation you need for your current task:

| If you are working on... | Read this document first | Description |
| :--- | :--- | :--- |
| **Overall system layout & fallback routing** | [docs/ARCHITECTURE.md](./docs/ARCHITECTURE.md) | High-level system flows, directory tree. |
| **Database ingestion, schema, or SQL queries** | [docs/DATABASE.md](./docs/DATABASE.md) | Ingestion workers, SQLite stacks, schema design. |
| **REST API endpoints or OPDS Catalog XML** | [docs/API.md](./docs/API.md) | API specs, feed schemas, file streaming. |
| **UI Views, Vue 3 SPA, or component styling** | [docs/FRONTEND.md](./docs/FRONTEND.md) | UI structure, Tailwind v4 + DaisyUI, theming. |
| **Authentication (Cloudflare Tunnel / OPDS Basic Auth)** | [docs/NETWORKING.md](./docs/NETWORKING.md) | SSO configurations, security rules. |
| **Docker configurations, Taskfile, or CI/CD pipelines** | [docs/BUILD-AND-DEPLOY.md](./docs/BUILD-AND-DEPLOY.md) | Dockerfiles, multi-stage pipelines, caching rules. |
| **Backend architecture, Go packages, and embed details** | [docs/BACKEND.md](./docs/BACKEND.md) | Embedded FS mappings, router setup, `internal/` layout, dependency rules, composition. |
| **Live sync status & SSE progress events** | [docs/SYNC-EVENTS.md](./docs/SYNC-EVENTS.md) | Event broker, SSE handler (`/api/sync/events`), frontend `EventSource`. |
| **Ebook parsing (per-format metadata/covers)** | [docs/EBOOK-PARSING.md](./docs/EBOOK-PARSING.md) | epub/fb2/mobi/pdf parsers, annotation pipeline. |

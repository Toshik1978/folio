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
* Prepend the generated commit list for the next release to `CHANGELOG.md`
  (requires [git-cliff](https://git-cliff.org) ≥ 2.13.0, installed globally with
  `mise use -g git-cliff@latest`):
  ```bash
  TAG=v1.6.0 task changelog
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
   * `github.com/stephenafamo/bob` — SQL query builder used to assemble the one
     dynamic query in the codebase, the faceted/full-text book filter
     (`internal/db/booksfilter.go`). sqlc (the primary query layer) generates code
     only for statically known SQL, so the runtime-composed WHERE/ORDER BY of the
     filter — optional facets, FTS match, sort — cannot be expressed as a sqlc
     query; hand-concatenating SQL there would risk injection. bob keeps every
     value parameterized. All other queries remain sqlc-generated. Approved
     2026-07-11.

3. **Testing with testify suites**: All Go tests use `github.com/stretchr/testify`, organised as suites. Three non-negotiable rules:
   1. **One entry point per package** — exactly one top-level `func Test<Package>(t *testing.T)`. No other top-level `Test*` function may exist in the package.
   2. **The entry point only wires suites** — it consists solely of `suite.Run(t, new(...))` calls, one per `suite.Suite`, and contains no test logic itself.
   3. **All real tests are suite methods** — every assertion lives in a method on a `suite.Suite`, using suite assertion methods (`s.Equal`, `s.NoError`, `s.Require().NoError`, `s.Contains`, …). Never write a bare `func TestX(t *testing.T)` with `require.X(t, …)` / `assert.X(t, …)` for an actual test.
4. **Branch Naming**: Any branch created for feature work **MUST** use the prefix `feature/` (e.g. `feature/sync-events`). Never use `feat/`, `feat-`, or any other variant. Only `feature/` is permitted.
5. **Commit Messages**: Never add `Co-Authored-By` and/or `Claude-Session` trailers — no AI/agent attribution trailers of any kind. Keep commit messages to the conventional-commit subject and body only.

---

## Changelog & Releases

`CHANGELOG.md` is the single release document. `RELEASE_NOTES.md` no longer
exists; its prose was folded in.

Each release section is hand-written prose sitting above a generated commit
list:

* **Prose — written by hand, and the reason the file exists.** One or two
  framing paragraphs, then `### Highlights` (plus `### Notable fixes`,
  `### Robustness`, or similar where a release warrants them). Write for someone
  running Folio: what changes for them, not which function moved. Use two or
  more paragraphs when one cannot carry the release.
* **Commit list — generated, do not hand-edit.** The `### Features`,
  `### Bug Fixes`, and `### Others` blocks come from git-cliff (`.cliff.toml`).
  Regenerate rather than editing them by hand.

The `## vX.Y.Z — YYYY-MM-DD` heading format is load-bearing:
`.github/scripts/extract-changelog.sh` matches it literally to build the GitHub
release body. Do not change its shape.

### Cutting a release

1. `TAG=v1.6.0 task changelog` — prepends the generated section for everything
   since the last tag.
2. Write the prose into that new section by hand.
3. Preview the release body: `.github/scripts/extract-changelog.sh v1.6.0`.
4. Commit the changelog.
5. `git tag v1.6.0 && git push origin v1.6.0`.

The tag push is what triggers `release.yml` — it builds and pushes the
multi-arch image and opens the GitHub release using that changelog section as
the body. No workflow writes back to the repository; if step 2 was skipped the
job fails before anything is published.

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

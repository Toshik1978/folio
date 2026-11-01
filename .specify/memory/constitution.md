# Folio Constitution

## Core Principles

### I. Read-Only Sources (NON-NEGOTIABLE)

- Folio MUST NOT modify, write to, rename, or reorganize any catalog source: book files on
  disk, Calibre `metadata.db`, INPX indexes, or ZIP archives.
- External SQLite sources MUST be opened read-only (`path + "?mode=ro"`, not a `file:` URI).
- All writable state MUST live under the data directory (`DATA_DIR`, `/data` in the container):
  the Folio SQLite database and the cover/thumbnail cache. Nothing else is writable.
- User corrections (e.g. Fix Match, `manually_matched`) are user-owned: sync MAY gap-fill
  empty fields but MUST NOT overwrite manual data.

Rationale: users mount their libraries `:ro` and trust Folio with collections that may be
irreplaceable. Folio is an index and a distributor, never an editor of source material.

### II. Single CGO-Free Binary

- The product ships as one Go binary (`folio-idx`) in one container, built with
  `CGO_ENABLED=0` so it runs on static Alpine/Distroless images across architectures.
- The SQLite driver MUST be `modernc.org/sqlite`; `mattn/go-sqlite3` or any other CGo
  dependency is forbidden.
- The Vue SPA (`web/`) MUST be compiled to `web/dist/` and embedded via `go:embed`
  (`web/embed.go`). No sidecar web server, separate frontend container, or in-container proxy.
- The SPA catch-all MUST return `404` for unmatched `/api/*` and `/opds/*` routes and for
  missing non-HTML static assets; only extension-less/`.html` paths fall back to `index.html`.

Rationale: a single process, port, and artifact minimizes moving parts for self-hosters.

### III. Resource Efficiency & Minimal Egress

- Folio MUST run comfortably on low-spec hosts (NAS, Raspberry Pi, minimal VPS): minimal
  memory, no required external services (no separate DB server, cache, or queue).
- Folio catalogues, filters, and serves downloads; it MUST NOT include an embedded e-book
  reader or render book content.
- Outbound network calls MUST be optional, best-effort, time-bounded, and documented in
  `docs/NETWORKING.md` (currently: metadata/cover providers such as Google Books). They MUST
  NOT transmit catalog contents or credentials, and their failure MUST NOT break a user-facing
  request.

Rationale: Folio's audience runs it on modest hardware and expects a quiet, private service.

### IV. Schema & Query Discipline

- Every schema change MUST be a numbered goose migration under `internal/db/migrations/`.
  Ad-hoc DDL in application startup or code paths is forbidden.
- Static queries MUST be sqlc-generated (`internal/db/dbq`, regenerated with `task generate`).
- Dynamic SQL is permitted only where sqlc cannot express it (currently the faceted/FTS book
  filter in `internal/db/booksfilter.go`) and MUST use the approved query builder (bob) with
  every value parameterized. Hand-concatenated SQL with user input is forbidden.

Rationale: reproducible schemas and parameterized queries keep upgrades safe and
injection-free.

### V. Testify-Suite Testing (NON-NEGOTIABLE)

- All Go tests use `github.com/stretchr/testify` suites.
- Each package has exactly one top-level `func Test<Package>(t *testing.T)`, living in the
  package's runner file (`<package>_test.go`); no other top-level `Test*` function may exist.
- The entry point consists solely of `suite.Run(t, new(...))` calls and contains no test logic.
- Every assertion lives in a suite method using suite assertions (`s.Equal`,
  `s.Require().NoError`, …). Bare `require.X(t, …)` / `assert.X(t, …)` tests are forbidden.
- Tests MUST be hermetic: no dependency on `npm run build` output or external services; use
  `t.TempDir()`, a fresh DB per test, `fstest.MapFS`, and committed `testdata/` fixtures.
- Behavior changes MUST ship with tests (Go and/or Vitest) covering the changed behavior.

Rationale: a uniform layout keeps `go test` output grouped and fixtures isolated per concern.

### VI. Idiomatic Go & Explicit Dependencies

- Code follows Effective Go and community conventions: standard library first, canonical error
  handling (wrap with context, no silent drops), `log/slog` for logging.
- All Go packages live under `internal/` (no `pkg/`). `cmd/folio-idx/main.go` is the sole
  composition root and holds no business logic.
- Interfaces are declared by the consumer, next to the code that uses them; concrete
  implementations are injected via constructors, not globals or setters. No shared `model/`
  package — types live where they are produced.
- Any new third-party dependency (Go or npm runtime) MUST receive explicit maintainer approval
  first, stating the package, the problem it solves, and why the standard library is
  insufficient. Approved direct Go dependencies are recorded in `.claude/CLAUDE.md`.

Rationale: a small, explicit dependency graph keeps the binary lean and the code navigable.

### VII. Security Boundaries

- The web UI and `/api/*` have no built-in authentication; they MUST be deployed behind an
  external authenticator, and documentation MUST say so. Folio MUST NOT grow its own user
  database, session management, or identity provider.
- `/opds/*` is protected by HTTP Basic Auth with bcrypt-hashed credentials configured at
  runtime (settings table), never via environment variables. Only cover/thumbnail endpoints are
  intentionally public.
- State-changing `/api` calls MUST pass the origin-based CSRF guard.
- `X-Forwarded-Host` MUST NOT be trusted; canonical external URLs come from `PUBLIC_URL`.
- HTML from book metadata MUST be sanitized server-side (bluemonday) and re-sanitized
  client-side (DOMPurify) before `v-html`.
- Library paths MUST be confinable to a base directory (`LIBRARY_ROOT`): when it is set, the
  API MUST refuse to create or edit a library that resolves outside it.

Rationale: OPDS clients cannot do browser SSO, so Folio secures only what reading apps need and
delegates the rest to proven infrastructure.

## Architecture & Product Constraints

- **Stack**: Go (version per `go.mod`), go-chi router, SQLite + FTS5 (WAL, foreign keys),
  goose, sqlc; Vue 3 + TypeScript + Vite, Tailwind CSS v4 + DaisyUI, PrimeIcons, Vitest.
- **Configuration**: environment variables parsed into a typed struct (`caarlos0/env`), with
  optional `.env` auto-load. Runtime-mutable settings live in the database.
- **Deployment**: single multi-arch Docker image (multi-stage build), running as nonroot with
  `/library` mounted read-only and `/data` as the only writable volume.
- **Lifecycle**: the process MUST exit non-zero on failed critical dependencies and shut down
  gracefully (drain HTTP, stop the sync engine) on SIGINT/SIGTERM.
- **Documentation**: `docs/` is the support channel. Any change to behavior, routes, schema,
  security posture, or egress MUST update the corresponding document listed in the
  `.claude/CLAUDE.md` task-to-file map in the same change.

## Development Workflow & Quality Gates

- **Automation** runs through `go-task` (`Taskfile.yml`): `task format`, `task lint`,
  `task test`, `task build`. `task lint` and `task test` are the CI gates and MUST pass before a
  change is considered complete.
- **Hooks**: pre-commit enforces commit-message format and fast checks at commit; full lint and
  test run at pre-push.
- **Branches**: feature work MUST use the `feature/` prefix. No other variant is permitted.
- **Commits**: Conventional Commits subjects (`feat:`, `fix:`, `docs:`, …), written for a
  changelog reader.
- **Releases**: `CHANGELOG.md` is the single release document. Each release section carries
  hand-written prose above a git-cliff-generated commit list that MUST NOT be hand-edited. The
  `## vX.Y.Z — YYYY-MM-DD` heading shape is load-bearing and MUST NOT change. Releases are cut
  by the maintainer by pushing a tag after the changelog section is committed.

## Governance

- This constitution codifies the project's existing rules and supersedes conflicting guidance
  elsewhere. `.claude/CLAUDE.md`, `CONTRIBUTING.md`, and `docs/` provide operational detail and MUST
  stay consistent with it; when they diverge, fix the drift in the same change.
- **Amendments** are made by the maintainer via a commit that updates this file (and any
  dependent guidance), with a Conventional Commits subject such as
  `docs: amend constitution to vX.Y.Z`.
- **Versioning** follows semantic versioning: MAJOR for removing or redefining a principle;
  MINOR for adding a principle or section or materially expanding guidance; PATCH for
  clarifications and wording.
- **Compliance**: every spec, plan (Constitution Check), and review MUST verify adherence.
  Deviations MUST be justified explicitly in the plan's complexity tracking and approved by the
  maintainer; unjustified violations block the change.
- Runtime development guidance for agents and contributors lives in `.claude/CLAUDE.md`.

**Version**: 1.0.0 | **Ratified**: 2026-11-01 | **Last Amended**: 2026-11-01

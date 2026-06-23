# Build & Deploy

> Taskfile targets, Docker multi-stage build, and local development workflow.

---

## Local Development

### Prerequisites

- Go ≥ 1.26
- Node.js ≥ 20
- npm (bundled with Node)
- [Task](https://taskfile.dev) (`brew install go-task`)
- Optional: `golangci-lint` (for `task lint:backend`) and `sqlc` (for `task generate`)

### First-Time Setup

```bash
task setup
```

This runs:
1. `mkdir -p web/dist && touch web/dist/.gitkeep` — ensures the embed directory exists for `go build`.
2. `cd web && npm install` — installs frontend dependencies.
3. `go mod tidy` — resolves Go dependencies.

### Development Workflow

Run two terminals:

```bash
# Terminal 1: Go backend on :8080
task dev:backend

# Terminal 2: Vite dev server on :5173 (proxies /api → :8080)
task dev:frontend
```

Access the app at `http://localhost:5173`. The Vite dev server handles HMR for Vue/TS/CSS changes. API requests are transparently proxied to the Go backend. The backend uses `DATA_DIR` (defaults to `./data` via `.env.dist`) for the SQLite database and cover cache.

### Production Build (local)

```bash
task build
```

This chain:
1. `task build:frontend` → `npm run build` in `web/` (type-check + Vite bundle to `web/dist/`).
2. `task build:backend` → `CGO_ENABLED=0 go build -ldflags="-w -s" -o bin/folio-idx cmd/folio-idx/main.go` (embeds `web/dist/`).

The output binary is `./bin/folio-idx`.

---

## Taskfile Reference

**File:** `Taskfile.yml`

| Task | Command | Description |
| :--- | :--- | :--- |
| `setup` | see above | Install deps, create dirs (`setup:ci` uses `npm ci` + `go mod download`) |
| `generate` | `sqlc generate` | Regenerate `internal/db/dbq/` from SQL (needs `sqlc`) |
| `dev:backend` | `go run cmd/folio-idx/main.go` | Run Go server locally |
| `dev:frontend` | `npm run dev` (in `web/`) | Run Vite dev server |
| `build:frontend` | `npm run build` (in `web/`) | Type-check + bundle frontend |
| `build:backend` | depends on `build:frontend` | Compile static Go binary (`CGO_ENABLED=0`, `-ldflags="-w -s"`) with embedded SPA |
| `build` | depends on `build:backend` | Full production build |
| `test:backend` | `go test -v -coverpkg=./internal/... ./internal/...` | Run all Go tests with coverage |
| `test:frontend` | `npm run test` (in `web/`) | Run Vitest unit/component tests (`test:frontend:ci` adds coverage + JSON) |
| `test` | `test:backend` + `test:frontend` | Run all tests |
| `lint:backend` | `golangci-lint run` | Run Go linters |
| `lint:frontend` | `npm run lint` (in `web/`) | Run ESLint |
| `lint` | backend + frontend + `check:frontend` | Lint everything (incl. Prettier `format:check`) |
| `format:backend` | `golangci-lint fmt` | Run gofumpt via golangci-lint |
| `format:frontend` | `npm run format` (in `web/`) | Prettier write |
| `format` | backend + frontend | Run all formatters |
| `build:docker` | `docker build -t folio-idx:latest .` | Build the production image |
| `clean` | `rm -rf bin/ web/dist/*` + restore `.gitkeep` | Remove build artifacts |

List all available tasks: `task --list`

---

## Testing & Coverage

**Both the backend and the frontend must keep test coverage at or above 80%.**
This is a floor, not a target: it holds today (backend **82.0%**, frontend
**85.5%** lines as of 2026-06-11) and must not regress. New code that drops
either stack below 80% should ship with the tests that bring it back up.

Coverage is measured exactly as CI does (`.github/workflows/ci.yml`), so the
numbers are reproducible locally:

- **Backend** — `go test -v -coverpkg=./internal/... ./internal/...` (the `test:backend` Taskfile task).
  For a filtered coverage report excluding generated/boilerplate packages:

  ```bash
  go test -v -coverpkg=./internal/... ./internal/... \
    -covermode=count -coverprofile=coverage.out
  grep -vE "/internal/db/dbq|/internal/logging|/internal/config" \
    coverage.out > coverage.filtered.out
  go tool cover -func=coverage.filtered.out | tail -1
  ```

- **Frontend** — `npm run test:ci` (in `web/`), then read `total.lines.pct` from
  `web/coverage/coverage-summary.json`.

> **Note (why `./internal/...` not `./...`):** the `flatted` npm package vendors a
> Go file (`web/node_modules/flatted/golang/`), so a bare `go test ./...` /
> `-coverpkg=./...` wildcard would compile and instrument arbitrary third-party
> code once `npm install` has run — and a broken Go file inside any npm package
> would break `task test`. The `test:backend` task therefore scopes patterns to
> `./internal/...` only. Keep that in mind when pointing other Go tooling at the
> repo. (The Dockerfile's `go test ./...` is unaffected: its build stage copies
> only the Go sources, never `node_modules`.)

CI **enforces** this floor: the `test` job's final "Check test and coverage
gates" step fails the workflow when either stack falls below 80% (after still
publishing the now-red badge), so a coverage regression blocks the PR. CI also
publishes both numbers as the coverage badges in the README, green only at ≥80%.

---

## CI/CD Workflows

Two GitHub Actions workflows live in `.github/workflows/`.

### `ci.yml` — "Code Verification"

Runs on every push to `main` and on every pull request. Three jobs:

| Job | Steps |
| :--- | :--- |
| `lint` | `task setup:ci`, then golangci-lint (via `golangci/golangci-lint-action`, pinned version), ESLint (`task lint:frontend`), and Prettier check (`task check:frontend`) |
| `test` | Both test suites with coverage (commands above). Test/coverage results are parsed into shields.io badge JSON and pushed to a Gist (`GIST_ID`/`GIST_SECRET_TOKEN` secrets) — **badges update only on push to `main`**. The final gate step fails the workflow on any test failure or a <80% coverage stack, *after* the (red) badges have published |
| `build` | `needs: [lint, test]`; runs `task build` to prove the full frontend-embed-backend production build compiles |

### `release.yml` — "Create and publish a Docker image"

Runs when a GitHub **release is published**. It builds the production image
from the repo `Dockerfile` and pushes it to **GitHub Container Registry**
(`ghcr.io/<owner>/<repo>`), tagged/labelled via `docker/metadata-action` (so a
release tag becomes the image tag), authenticated with the workflow's
`GITHUB_TOKEN`. After the push it generates a **build provenance attestation**
(`actions/attest`) for the image digest and pushes that to the registry too.

There is no other deployment automation — deploying a release means pulling the
published image (see [Build & Run](#build--run)).

---

## Docker Build

**File:** `Dockerfile`

### Multi-Stage Pipeline

```
Stage 1: frontend-builder (node:24-alpine)
  ├── npm install
  └── npm run build → /app/web/dist/

Stage 2: backend-builder (golang:1.26-alpine)
  ├── go mod download
  ├── COPY Go sources (cmd/, internal/, web/embed.go)
  ├── COPY --from=frontend-builder dist/ → web/dist/
  ├── CGO_ENABLED=0 go test -v ./...
  └── CGO_ENABLED=0 go build → /app/bin/folio-idx

Stage 3: runtime (gcr.io/distroless/static-debian12)
  ├── Pre-installed ca-certificates, tzdata, nonroot user
  ├── COPY binary to /app (root-owned, read-only)
  ├── COPY /data directory (nonroot-owned, writable)
  ├── USER 65532:65532 (nonroot)
  └── CMD ["/app/folio-idx"]
```

### Key Design Points

| Concern | Solution |
| :--- | :--- |
| **Build reproducibility** | Tests run inside Stage 2 with the same `CGO_ENABLED=0` flag used for compilation |
| **Image size** | Final stage is Distroless static (~2MB overhead). Only the static binary + pre-created data folder are copied |
| **Security** | Non-root `nonroot` user (UID 65532). Binary is root-owned (read-only). Only `/data` is writable |
| **Static linking** | `CGO_ENABLED=0` removes glibc dependency. Safe for scratch, Alpine, or Distroless bases |
| **Build cache** | `go.mod`/`go.sum` are copied before source to maximize layer caching on `go mod download` |

### Build & Run

```bash
docker build -t folio .
docker run -p 8080:8080 -v /path/to/library:/library:ro -v folio-data:/data folio
```

- `/library:ro` — mount book sources read-only.
- `folio-data:/data` — named volume for SQLite database persistence.

---

## `.gitignore` Summary

| Pattern | Purpose |
| :--- | :--- |
| `node_modules/` | npm dependencies |
| `/web/dist` | Build output directory (fully git-ignored) |
| `/bin/` | Compiled binary |
| `*.db`, `*.db-journal`, `*.db-shm`, `*.db-wal` | SQLite runtime files |
| `.env` | Local environment secrets |

## `.dockerignore` Summary

Excludes `.git`, `node_modules`, `bin`, `web/dist`, DB files, `.env`, `Dockerfile`, and `docs` from the Docker build context.

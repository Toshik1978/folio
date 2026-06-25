# Contributing to Folio

Thanks for your interest in improving Folio! This guide covers the essentials.
For the full developer/agent onboarding — architecture map, task rules, and a
task-to-doc reference — see [AGENTS.md](AGENTS.md).

## Prerequisites

- **Go 1.26+** (the project compiles with `CGO_ENABLED=0`)
- **Node.js** (for the Vue 3 SPA in `web/`)
- **[go-task](https://taskfile.dev)** — all automation is driven through `Taskfile.yml`

## Getting started

```bash
task setup        # install Go modules + npm packages, create build dirs
task dev:backend  # run the Go server (default :8080)
task dev:frontend # run the Vite dev server with HMR
```

## Before you open a pull request

Run the same gates CI enforces:

```bash
task format   # gofumpt (Go) + Prettier (frontend)
task lint     # all Go + frontend linters
task test     # unit + integration suites
task build    # type-check SPA, embed assets, compile the binary
```

## Conventions

- **Commits** follow [Conventional Commits](https://www.conventionalcommits.org/)
  (`feat:`, `fix:`, `docs:`, `refactor:`, …). `CHANGELOG.md` is generated from
  them by Commitizen — do not hand-edit it.
- **Branches** for feature work use the `feature/` prefix (e.g.
  `feature/sync-events`). No other variant is accepted.
- **Tests** use `testify` suites — one `Test<Package>` entry point per package
  that only wires suites; all assertions live in suite methods. See
  [AGENTS.md](AGENTS.md) for the full testing rules.
- **New dependencies** require approval before they're added — state the package,
  what it solves, and why the standard library is insufficient.

## Core constraints

Folio is **read-only** over your book sources, ships as a **single CGO-free
binary**, and is designed to run on **low-spec hosts**. Please keep changes
within these invariants — see [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for
the full set.

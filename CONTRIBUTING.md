# Contributing to Folio

Thanks for your interest in improving Folio! This guide covers the essentials.
For the full developer/agent onboarding — architecture map, task rules, and a
task-to-doc reference — see [CLAUDE.md](CLAUDE.md).

## Before you start

Folio is a personal project. It is maintained in whatever time is left over
after everything else, and that shapes what you can expect:

- **Pull requests may not be reviewed.** Not because they are unwelcome — there
  simply may not be time. Please do not read silence as a judgement on your work.
- **Issues may not get a response,** and there is no target response time.
- **No support is offered.** The docs in [docs/](docs/) are thorough; they are
  the support.

If you need a change on a schedule you control, **fork the project**. That is a
first-class outcome here, not a fallback — the licence permits it and it will
serve you better than waiting. Bug reports with a clear reproduction are still
genuinely useful even when they go unanswered for a while.

## Prerequisites

- **Go 1.26+** (the project compiles with `CGO_ENABLED=0`)
- **Node.js** (for the Vue 3 SPA in `web/`)
- **[go-task](https://taskfile.dev)** — all automation is driven through `Taskfile.yml`
- **[git-cliff](https://git-cliff.org) 2.13.0+** — only needed to cut a release;
  install with `mise use -g git-cliff@latest`

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
  (`feat:`, `fix:`, `docs:`, `refactor:`, …), enforced by a `commit-msg`
  pre-commit hook. The subject line is what shows up in the changelog, so write
  it for a reader who was not there.
- **Branches** for feature work use the `feature/` prefix (e.g.
  `feature/sync-events`). No other variant is accepted.
- **Tests** use `testify` suites — one `Test<Package>` entry point per package
  that only wires suites; all assertions live in suite methods. See
  [CLAUDE.md](CLAUDE.md) for the full testing rules.
- **New dependencies** require approval before they're added — state the package,
  what it solves, and why the standard library is insufficient.

## Releases

Releases are cut by hand, by the maintainer. `CHANGELOG.md` is the single
release document — `RELEASE_NOTES.md` no longer exists.

1. `TAG=v1.6.0 task changelog` — prepends the generated commit list for
   everything since the last tag.
2. Write the prose highlights into that new section by hand. This is the part
   that matters; the generated list is supporting detail.
3. Preview the release body: `.github/scripts/extract-changelog.sh v1.6.0`.
4. Commit the changelog.
5. `git tag v1.6.0 && git push origin v1.6.0`.

Pushing the tag triggers `release.yml`, which builds and pushes the multi-arch
image to GHCR and opens the GitHub release using that changelog section as the
body. It fails if the tag has no section, so step 2 cannot be skipped.

## Core constraints

Folio is **read-only** over your book sources, ships as a **single CGO-free
binary**, and is designed to run on **low-spec hosts**. Please keep changes
within these invariants — see [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for
the full set.

[![Build](https://github.com/Toshik1978/folio/actions/workflows/ci.yml/badge.svg)](https://github.com/Toshik1978/folio/actions)
![Tests](https://img.shields.io/endpoint?url=https://gist.githubusercontent.com/Toshik1978/2f618893c4512ae0ec7feb20e4fa9e25/raw/tests.json&maxAge=180)
![Coverage](https://img.shields.io/endpoint?url=https://gist.githubusercontent.com/Toshik1978/2f618893c4512ae0ec7feb20e4fa9e25/raw/coverage.json&maxAge=180)
![Web Tests](https://img.shields.io/endpoint?url=https://gist.githubusercontent.com/Toshik1978/2f618893c4512ae0ec7feb20e4fa9e25/raw/web-tests.json&maxAge=180)
![Web Coverage](https://img.shields.io/endpoint?url=https://gist.githubusercontent.com/Toshik1978/2f618893c4512ae0ec7feb20e4fa9e25/raw/web-coverage.json&maxAge=180)
# Folio

<p align="center">
  <img src="docs/folio.png" alt="Folio logo" width="160" />
</p>

> Self-hosted, read-only digital book library manager. Catalogues, indexes, and distributes personal e-book collections through a web UI and an OPDS feed.

---

## Quick Start

```bash
docker build -t folio .
docker run -p 8080:8080 -v /path/to/library:/library:ro -v folio-data:/data folio
```

Open `http://localhost:8080`. Mount your book sources read-only at `/library`; Folio indexes them into its own SQLite database at `/data`.

---

## Quick Reference

| Aspect | Value |
| :--- | :--- |
| **Module** | `github.com/Toshik1978/folio` |
| **Go version** | 1.26 |
| **Binary** | `folio-idx` |
| **Default port** | `8080` (env `PORT`) |
| **Database** | SQLite 3 + FTS5 |
| **Frontend** | Vue 3 · TypeScript · Tailwind CSS v4 · DaisyUI · PrimeIcons |
| **Deployment** | Single Docker container (multi-stage build) |

---

## Documentation Map

| Document | Contents |
| :--- | :--- |
| [ARCHITECTURE.md](docs/ARCHITECTURE.md) | System overview, core constraints, deployment model, project structure. |
| [BACKEND.md](docs/BACKEND.md) | Go server: routing, SPA embedding, middleware, health check. |
| [EBOOK-PARSING.md](docs/EBOOK-PARSING.md) | Per-format metadata & cover extraction (epub/fb2/mobi/pdf), annotation pipeline. |
| [FRONTEND.md](docs/FRONTEND.md) | Vue 3 SPA: Vite, TypeScript, Tailwind v4 + DaisyUI, theming, dev proxy. |
| [DATABASE.md](docs/DATABASE.md) | SQLite schema, FTS5 search, ingestion sources, sync engine. |
| [API.md](docs/API.md) | REST API, OPDS catalog, file streaming strategy. |
| [SYNC-EVENTS.md](docs/SYNC-EVENTS.md) | Real-time sync status via Server-Sent Events (SSE), event broker, progress reporting. |
| [NETWORKING.md](docs/NETWORKING.md) | Cloudflare Access, OPDS auth bypass, Basic Auth. |
| [BUILD-AND-DEPLOY.md](docs/BUILD-AND-DEPLOY.md) | Taskfile targets, Docker multi-stage build, local dev workflow. |
| [CHANGELOG.md](CHANGELOG.md) | Dated history of notable changes, with the rationale behind each. |

---

## Core Constraints

These invariants apply project-wide. Every design decision must respect them.

1. **Read-Only** — The application never modifies, writes to, or reorganizes source book files or external databases.
2. **No Embedded Reader** — The UI catalogues, filters, and serves downloads. It does not render e-book content.
3. **Resource Efficient** — Designed for low-spec hosts (NAS, Raspberry Pi, minimal VPS). Minimal memory, single binary, no external services.

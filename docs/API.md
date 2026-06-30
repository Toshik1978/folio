# API

> REST endpoints, OPDS catalog, and file streaming.
>
> **Status:** Implemented. The full REST API (`/api/*`) and OPDS catalog (`/opds/*`) are coded and tested; the Vue SPA consumes the REST API directly.

---

## REST API (`/api/*`)

`/api/*` has **no application-level authentication** — it must be protected by an external authenticator in front of Folio (reverse-proxy SSO/forward-auth, or network isolation). See [NETWORKING.md](NETWORKING.md). A token-less, origin-based CSRF guard is applied, but it is not a substitute for authentication.

| Method | Path | Description | Response |
| :--- | :--- | :--- | :--- |
| `GET` | `/api/health` | Liveness probe | `{"status":"ok"}` (JSON) |
| `GET` | `/api/books` | Paginated book list with filter params (sorted by relevance for searches, else recency or rating) | JSON `{items,total,page,limit}` |
| `GET` | `/api/books/{id}` | Single book detail; triggers lazy metadata backfill + online enrichment on first view (a per-book in-flight claim makes concurrent first views run them once — the losing view serves the current row) | JSON book object |
| `GET` | `/api/books/{id}/files/{fileID}` | Download one format of a book (a book may have several; see `formats[]`) | Binary stream with correct `Content-Type` |
| `GET` | `/api/books/{id}/cover` | Fetch cached cover image | Image binary (`image/jpeg`; covers are normalized to JPEG on import) |
| `GET` | `/api/books/{id}/cover/thumbnail` | Aspect-preserving cover thumbnail (≤400px longest side); falls back to the full cover (no-cache) until generated | Image binary (`image/jpeg`) |
| `GET` | `/api/books/{id}/cover/search` | Cover search: multi-source cover candidates for `?q=<query>` | JSON array of `CoverCandidate` objects |
| `GET` | `/api/books/{id}/match` | Fix Match: Google Books candidates for `?q=<query>` | JSON array of candidates |
| `POST` | `/api/books/{id}/match` | Apply a chosen volume (`{"volume_id":"…"}`), overwriting the book's metadata | JSON updated book object |
| `PUT` | `/api/books/{id}` | Update book fields (title, authors, annotation, etc.) | JSON updated book object |
| `PUT` | `/api/books/{id}/cover` | Upload a new cover image (binary body) | JSON updated book object |
| `POST` | `/api/books/{id}/cover` | Set cover from URL (`{"url":"…"}`) — server downloads the image | JSON updated book object |
| `GET` | `/api/genres` | Canonical genre taxonomy list (for the edit autocomplete) | JSON array of genre name strings |
| `GET` | `/api/authors` | One alphabet bucket of authors (`?letter=&page=&limit=`) | JSON array `[{id,name,book_count}]` |
| `GET` | `/api/authors/letters` | Alphabet buckets that have authors (drives the selector) | JSON array of letters |
| `GET` | `/api/series` | One alphabet bucket of series (`?letter=&page=&limit=`) | JSON array `[{id,name,book_count}]` |
| `GET` | `/api/series/letters` | Alphabet buckets that have series | JSON array of letters |
| `GET` | `/api/tags` | One alphabet bucket of genres/tags (`?letter=&page=&limit=`) | JSON array `[{name,book_count}]` |
| `GET` | `/api/tags/letters` | Alphabet buckets that have tags | JSON array of letters |
| `GET` | `/api/publishers` | One alphabet bucket of publishers (`?letter=&page=&limit=`) | JSON array `[{name,book_count}]` |
| `GET` | `/api/publishers/letters` | Alphabet buckets that have publishers | JSON array of letters |
| `POST` | `/api/sync` | Force a re-index of all active libraries, bypassing checkpoint gating (the "Re-index All" action) | JSON sync status |
| `GET` | `/api/sync/status` | Current sync state (idle/running, active library, queue) | JSON object |
| `GET` | `/api/stats` | Whole-catalog statistics (counts, sizes, per-format/per-language breakdowns). **Always global** — `/api/stats` is not library-scoped; any `?library=` is ignored | JSON object (see Stats Object) |
| `GET` | `/api/facets` | Distinct format & language **values** for the search-bar facet pickers; `?library=<id>` scopes, absent = all | JSON `{"formats":[…],"languages":[…]}` |

> **Sync gating rule:** the forced manual triggers (`POST /api/sync` and
> `POST /api/libraries/{id}/reindex`) bypass checkpoint gating and re-read every
> matched source. The cheap `POST /api/libraries/{id}/sync` and all automatic
> triggers (scheduled, watcher, startup) respect it — a library whose source
> fingerprint is unchanged is skipped.

> **Single-writer 503:** every write endpoint (the `POST`/`PUT`/`DELETE` book and
> library routes) shares one process-wide write guard with the indexing engine
> (see [DATABASE.md](./DATABASE.md#design-constraints)). While a long re-index holds
> it, a write waits a short budget (≈2s) and then returns
> `503 {"error":"indexing in progress; retry shortly"}` instead of blocking — the
> client should retry. Reads are never blocked.

### Library Management (`/api/libraries`)

| Method | Path | Description | Response |
| :--- | :--- | :--- | :--- |
| `GET` | `/api/libraries` | List all libraries (with status, book count, last sync) | JSON array |
| `POST` | `/api/libraries` | Add library (name, type, path, sync interval) | JSON created library |
| `GET` | `/api/libraries/{id}` | Library details | JSON object |
| `PUT` | `/api/libraries/{id}` | Update library (name, path, schedule) | JSON updated library |
| `DELETE` | `/api/libraries/{id}` | Deactivate library, start 7-day purge countdown | JSON acknowledgement |
| `POST` | `/api/libraries/{id}/reactivate` | Cancel pending purge, resume syncing | JSON acknowledgement |
| `POST` | `/api/libraries/{id}/purge` | Force-purge now (skip the 7-day grace): stamps `pending_purge` + `purge_at=now`, then tears down asynchronously; retried by the minute-interval deadline sweep if the async teardown fails | `202 {"status":"purging"}` |
| `POST` | `/api/libraries/{id}/sync` | Trigger an incremental sync for this library (respects checkpoint gating; skips an unchanged source) | `202 {"status":"queued"}` |
| `POST` | `/api/libraries/{id}/reindex` | Force a full re-read of this library, bypassing checkpoint gating (the per-library "Re-index" action) | `202 {"status":"queued"}` |

`name` and `path` are required on `POST`/`PUT` (empty → `400`). A duplicate
`path` returns `409 Conflict` (not a generic `500`). `DELETE`, `PUT`,
`reactivate`, `purge`, `sync`, and `reindex` on an unknown id return `404 Not Found`. A
`PUT` updates only name/path/schedule — it never resets `status`, `purge_at`, or
`last_sync_error`, so editing a library mid-purge does not silently cancel the
purge.

#### Library Object (JSON)

```json
{
  "id": 1,
  "name": "My Calibre Library",
  "type": "calibre",
  "path": "/library/calibre",
  "sync_interval_seconds": 3600,
  "status": "active",
  "purge_at": null,
  "last_sync_at": 1717200000,
  "last_sync_error": null,
  "book_count": 1423
}
```

`status` values: `active`, `queued`, `syncing`, `pending_purge`, `error`. `queued` is
a live overlay: the engine reports it for a library waiting its turn in the sync
queue; it is never persisted to the database.

#### Deletion & Purge Lifecycle

1. `DELETE /api/libraries/{id}` → status becomes `pending_purge`, `purge_at` set to now + 7 days. Sync stops.
2. During grace period: `POST /api/libraries/{id}/reactivate` → status returns to `active`, `purge_at` cleared.
3. After grace period: sync worker purges all books, FTS entries, and cached covers belonging to the library.
4. Skip the wait: `POST /api/libraries/{id}/purge` ("Purge Now") first stamps the
   library `pending_purge` with `purge_at` set to the current time, then delegates
   the teardown to a background goroutine and returns `202 {"status":"purging"}`
   immediately, so the request never blocks on evicting a large library's covers.
   The teardown (`Engine.RequestPurge` → evict covers best-effort → `DeleteBooksByLibrary`
   + `DeleteLibrary` in one transaction) runs under `context.Background()`, and
   `Engine.Stop` waits for any in-flight purge to finish before returning. If the
   async teardown fails, the minute-interval deadline sweep picks up the
   `pending_purge` row (since `purge_at` is now in the past) and retries — so the
   stamp is both the durability record and the retry signal.
5. A sync that finishes *after* a `DELETE` can no longer resurrect the library:
   `recordLastSync` leaves a `pending_purge` row in `pending_purge` (it does not
   flip it back to `active`), so the purge still runs.

### Settings (`/api/settings`)

| Method | Path | Description | Response |
| :--- | :--- | :--- | :--- |
| `GET` | `/api/settings` | Current application settings | JSON object |
| `PUT` | `/api/settings` | Update settings | JSON updated settings |

#### Settings Object (JSON)

```json
{
  "opds_user": "reader",
  "opds_pass_set": true
}
```

Password is write-only — `GET` returns `opds_pass_set` (boolean), `PUT` accepts `opds_pass` (plaintext, stored hashed).

#### Stats Object (JSON)

```json
{
  "total_books": 12403,
  "total_size_bytes": 3221225472,
  "authors": 3210,
  "series": 142,
  "libraries": 2,
  "formats": { "epub": 1203, "fb2": 840 },
  "languages": { "en": 1500, "ru": 5 }
}
```

`formats` and `languages` are `{ value: count }` maps. These totals are
**always whole-catalog**: `/api/stats` is deliberately not library-scoped — the
result is computed globally, cached in-process, and invalidated on any catalog
change (a sync, enrichment, or purge). A `?library=` query parameter is accepted
by the router but ignored. The frontend surfaces this as a catalog overview card
on Settings → Libraries (it requests `/stats` with no parameters). The search
bar's Format/Language value-picker facets are populated separately from
[`/api/facets`](#rest-api-api), which returns just the distinct values
(that endpoint **is** `?library=`-scoped) rather than the counted breakdown here.

### Query Parameters for `/api/books`

| Param | Type | Description |
| :--- | :--- | :--- |
| `q` | string | Free-text search (FTS5) across title, authors, series, annotation. Ranked by BM25. |
| `title` | string | Search the title. Token-level FTS by default; a leading `=` (e.g. `=Dune`) matches the exact title. |
| `author` | string | Search authors. Token-level FTS by default (e.g. `Pratchett`); a leading `=` matches the exact author name. |
| `series` | string | Search series. Token-level FTS by default; a leading `=` matches the exact series name. |
| `tag` | string | Exact genre/tag name (not full-text indexed). |
| `publisher` | string | Filter by publisher name |
| `format` | string | Filter by file format (`epub`, `fb2`, …). Selectable from the search bar's Format facet (values from `/api/facets`). |
| `library` | int | Restrict to a single library by id (`0`/absent = all libraries). Also honored by `/api/authors`, `/api/series`, `/api/tags`, `/api/publishers`, their `/letters` variants, and `/api/facets`. (`/api/stats` is **not** scoped — it is always whole-catalog.) |
| `lang` | string | Filter by language code (e.g. `en`, `ru`). Values are normalized ISO 639-1 across all sources — Calibre's ISO 639-2/B codes (e.g. `eng`, `rus`) are mapped to two-letter codes at ingest, so a single `lang=en` matches books from every source. Selectable from the search bar's Language facet (values from `/api/facets`). |
| `sort` | string | Result order when not searching, three modes: absent/unrecognized → `imported_at` desc (**"Recently added"** — when the book entered Folio; the default), `source` → `added_at` desc (**"Newest"** — source/original chronology), `rating` → rating desc (highest first, unrated last). Each tie-breaks by `added_at`/`id`. Ignored while a free-text/FTS `q` is active (BM25 relevance wins). |
| `page` | int | Page number (1-indexed) |
| `limit` | int | Results per page |

The book object includes a nullable `rating` (1–5 stars), populated from Calibre
ratings (see [DATABASE.md](./DATABASE.md#ingestion-sources)). It also includes:

- `cover_url` (string|null) — the cached cover image URL, versioned `?v=<content_hash>-<cover_version>` (e.g. `/api/books/42/cover?v=abc123-1717200000`).
- `thumbnail_url` (string|null) — the downscaled cover URL, versioned `?v=<content_hash>-<cover_version>-<thumb_token>` (e.g. `/api/books/42/cover/thumbnail?v=abc123-1717200000-t400q85`). The token encodes the rendering spec (max dimension + JPEG quality) and changes when those constants change, so clients automatically re-fetch when the thumbnail generation parameters are updated.

### Alphabet browse (`/api/<entity>` + `/api/<entity>/letters`)

The author/series/tag/publisher list endpoints load **one alphabet bucket at a
time** rather than the whole table, so they stay fast on large (e.g. Cyrillic)
libraries. `/api/<entity>/letters` returns the buckets that have data, in display
order; `/api/<entity>?letter=<L>&page=&limit=` returns that bucket's entries. A
bucket is a single uppercase first letter over the Cyrillic (`А`–`Я`) and Latin
(`A`–`Z`) ranges; the catch-all `#` bucket collects everything else (digits,
punctuation, other scripts). An empty/unknown `letter` yields `[]`. The bucketing
logic lives in `internal/api/letters.go` and is mirrored by the SQL
`*FirstChars` / `*ByLetter` / `*NonLetter` queries.

### Fix Match (`/api/books/{id}/match`)

These endpoints let the user correct a book's metadata against Google Books. The
client is always constructed; the API key is optional (`GOOGLE_KEY`, anonymous
quota when empty). When no enricher is wired both return `501`.

- `GET /api/books/{id}/match?q=<query>` — candidates for a free-text query:
  ```json
  [{ "volume_id": "zyTCAlFPjgYC", "title": "Dune", "authors": ["Frank Herbert"], "year": 1965, "thumbnail": "https://…" }]
  ```
  A missing `q` → `400`; an upstream failure → `502`.
- `POST /api/books/{id}/match` with `{"volume_id":"…"}` fetches that volume,
  **overwrites** the book's annotation/publisher/year (plus identifiers and
  cover), restamps `content_hash` (so the `?v=<content_hash>-<cover mtime>`
  cache-buster updates), and returns the updated book object. A missing `volume_id` → `400`.

This is the manual, overwrite counterpart to the automatic, gap-fill online
enrichment that runs on first view (see
[DATABASE.md](./DATABASE.md#lazy-metadata-tiers-on-view)).

---

## OPDS Catalog (`/opds/*`)

OPDS routes are reachable directly (so reading apps that can't do browser SSO can connect) but are protected by Folio's own application-level HTTP Basic Authentication.

OPDS feeds are **never library-scoped**: they always span the whole catalog,
unlike the web UI, which has a per-library selector in its header.

| Method | Path | Description | Content-Type |
| :--- | :--- | :--- | :--- |
| `GET` | `/opds/` | Root navigation feed | `application/atom+xml;profile=opds-catalog` |
| `GET` | `/opds/authors` | Author-grouped index | `application/atom+xml;profile=opds-catalog` |
| `GET` | `/opds/series` | Series-grouped index | `application/atom+xml;profile=opds-catalog` |
| `GET` | `/opds/genres` | Tag-grouped index | `application/atom+xml;profile=opds-catalog` |
| `GET` | `/opds/opensearch.xml` | OpenSearch description XML | `application/opensearchdescription+xml` |
| `GET` | `/opds/search` | Search results OPDS feed (`?q=` / `?author=` / `?series=` / `?tag=`; ordered by BM25 relevance for `q`) | `application/atom+xml;profile=opds-catalog` |
| `GET` | `/opds/books/{id}/files/{fileID}` | File download for readers — one acquisition link per format (Protected) | Binary stream |
| `GET` | `/opds/books/{id}/cover` | Book cover image (Public / Unauthenticated) | `image/jpeg` |
| `GET` | `/opds/books/{id}/cover/thumbnail` | Book cover thumbnail (Public / Unauthenticated) | `image/jpeg` |

### OPDS Auth

`auth.Authenticator.Middleware` (`internal/auth/auth.go`), injected into the opds
handler as the `opds.Authenticator` interface, guards the protected group — not
chi's built-in `middleware.BasicAuth` — because it does a constant-time username
compare plus `bcrypt` password check. **Until OPDS credentials are configured the
protected routes all return `401`** — the catalog is *closed*, not open (the
unconfigured branch even sends a bare `401` with no `WWW-Authenticate`, so a reader
app can't prompt). OPDS is therefore unusable until a credential is set via
`PUT /api/settings`; a startup warning (`WarnIfUnprotected`) flags the empty state:

```go
pr.Use(h.authn.Middleware) // realm "OPDS Library Manager"; bcrypt, timing-equalized + success-cached
```

Two hardening details (see [NETWORKING.md](./NETWORKING.md) for the full
rationale): a username mismatch still burns a bcrypt compare against a dummy
hash, so response timing can't reveal which usernames exist; and the last
successful `(user, password, stored-hash)` triple is cached as a SHA-256 key so
reader-app request bursts don't pay ~100ms bcrypt per request (the cache clears
on `PUT /api/settings` — which calls `SetCredentials` → self-invalidate — and
implicitly on credential rotation, since the cache key embeds the stored hash).

Credentials are stored in the `settings` table (password hashed) and are
configured solely via `PUT /api/settings`; there is no environment-variable seed.
Until a credential is set, every protected OPDS route returns `401` — the catalog
is closed (the public cover endpoint is the only exception).

### OPDS Feed Format

Responses conform to the OPDS 1.2 / Atom XML specification. Reading apps (Moon+ Reader, KyBook, etc.) parse these feeds to browse and download books.

#### Decision: hand-rolled feed, not a feed library

`internal/opds/atom.go` defines its own `feed`/`entry`/`link` structs and
marshals the XML directly (~107 lines, dependency-free). This is deliberate:
OPDS is **not** vanilla Atom but a constrained Atom profile with required
extensions this code models explicitly — OPDS link relations
(`http://opds-spec.org/acquisition`, `/image`, `/image/thumbnail`), the catalog
media-type profiles (`…;profile=opds-catalog;kind=navigation|acquisition`), the
OPDS namespace, and Dublin Core terms (`dc:publisher`/`dc:issued`/`dc:language`).
Acquisition-entry image links: `rel="http://opds-spec.org/image"` points at the
full cover (`/opds/books/{id}/cover`); `rel="http://opds-spec.org/image/thumbnail"`
points at the dedicated thumbnail route (`/opds/books/{id}/cover/thumbnail`) — a
smaller, aspect-preserving image (≤400px longest side, JPEG quality 85).
Generic feed libraries (`gorilla/feeds`, `golang.org/x/tools/blog/atom`) model
plain Atom/RSS only, so adopting one would add a dependency yet still require
custom structs. The feed is hardened with golden-file tests (`feedsSuite`)
instead. Revisit only if a genuinely OPDS-aware, maintained Go library appears.

#### Pagination

The `authors`, `series`, `genres`, and `search` feeds are paginated with a `?page=`
query parameter (1-indexed, 50 entries per page — `defaultLimit`). Each feed
advertises standard Atom navigation links so readers can walk the catalog:

- `rel="next"` — present when the page came back full (more entries may follow).
- `rel="previous"` — present when `page > 1`.

`next`/`previous` hrefs preserve the other query parameters (e.g.
`/opds/search?author=Asimov&page=2`), so filtered feeds page correctly.

#### Offline metadata enrichment

The acquisition feed (`/opds/search` and the browse feeds that drill into it)
backfills **offline** metadata — annotation and identifiers parsed from each
book's own source file — for the books on the requested page that have not been
checked yet (`metadata_checked = 0`). This runs in `internal/opds/enrich.go`
before the entries are rendered, so an OPDS-first reader sees a book's summary
without ever opening it in the web UI. It is bounded so a feed never hangs: a
fan-out of `opdsBackfillWorkers` (6) goroutines under a `opdsBackfillBudget`
(5s) deadline, after which any unfinished books simply render from the current
DB and enrich on a later load. The fill is best-effort and at most once per book
(single-flight, gated by `metadata_checked`). The injected `MetadataFiller`
interface keeps `opds` a leaf package — it imports neither `ingest` nor the
online enrichment tier, so **OPDS never makes an online (Google Books) request**;
covers remain lazy on the separate public cover endpoint. Navigation/index feeds
(`authors`/`series`/`genres`/root) render from the DB only and trigger no fill.

#### Annotation sanitization

`<content type="html">` annotations are sanitized with `bluemonday.UGCPolicy()`
at the OPDS serve boundary (`bookEntry`), matching the REST API — see
`EBOOK-PARSING.md`'s annotation rendering pipeline.

---

## File Streaming Strategy

To prevent memory exhaustion on resource-constrained hosts, file downloads use streaming — never buffered entirely in memory.

| Source Type | Streaming Method |
| :--- | :--- |
| **Folder / Calibre** | `http.ServeFile()` — delegates to OS-level `sendfile` syscall for zero-copy transfer; supports HTTP Range (resumable downloads) |
| **INPX (ZIP archives)** | Open ZIP → seek to internal file offset → `io.Copy(w, zipReader)` with correct headers; **no Range** (the entry streams out of a DEFLATE archive, so a Range request gets a `200` full body) |

Both routes stream through `bookfile.Serve`, which lifts the server-wide
`WriteTimeout` for the response (via `http.NewResponseController().SetWriteDeadline`):
a multi-hundred-MB book over a slow mobile link (the OPDS use case) legitimately
exceeds any fixed deadline. Every other route keeps the 60s bound. The exemption
is best-effort — proxies/recorders without deadline support just keep the global
timeout.

### Content-Type Headers

| Format | Content-Type |
| :--- | :--- |
| EPUB | `application/epub+zip` |
| FB2 | `application/x-fictionbook+xml` |
| MOBI / AZW / AZW3 | `application/x-mobipocket-ebook` |
| PDF | `application/pdf` |
| _anything else_ | `application/octet-stream` (fallback) |

The mapping lives in `bookfile.ContentType` (`internal/bookfile/bookfile.go`),
keyed on the stored `book_files.file_format` label. A `.fb2.zip` file is stored
with format `fb2` (the dispatcher normalizes the wrapper — see
[EBOOK-PARSING.md](./EBOOK-PARSING.md#fb2)), so it serves with the FB2 type.

---

## Cover Image Serving & Security

To optimize performance and security when loading cover images for the Web UI or OPDS feeds, the following design rules apply:

1. **JPEG Normalization**: Covers are transcoded to JPEG on save (`covers/store.go:Save` → `convertToJPEG`; already-JPEG bytes pass through untouched). Every cached cover is therefore a JPEG, so serving and the OPDS feed can declare `image/jpeg` without sniffing each file's bytes. Serving raw bytes as a fixed image type also keeps a mislabeled "cover" from ever being rendered as HTML.
2. **Cached Reads**: Requests for `/api/books/{id}/cover`, `/opds/books/{id}/cover`, and their `/cover/thumbnail` variants serve cached files directly from the sharded structure inside the `/data/covers/` directory (`/data/covers/0/42.jpeg`, `/data/covers/0/42.thumb.jpeg`).
3. **Directory Traversal Prevention**: The `{id}` parameter is strictly validated before any filesystem interaction:
   - The string `{id}` is converted to a positive integer in Go. If conversion fails, the backend immediately returns a `400 Bad Request` or `404 Not Found`.
   - File lookups are sharded and constrained within `/data/covers/` using the integer `{id}` (e.g. `/data/covers/0/42.jpeg`), entirely eliminating path traversal vectors (e.g., `../../etc/passwd`).
4. **Lazy Extraction**: On a cache miss, `covers/store.go:ServeCover` attempts a one-time extraction of the cover directly from the book's source file (via the ingest `CoverExtractor`), normalizing and caching the result for subsequent requests. The outcome is recorded in the `books.cover_state` column (`unknown`/`has`/`none`) via the injected `CoverState` adapter (`internal/ingest/coverstate.go`): a book with no extractable cover is marked `none` and **no placeholder file is written to disk** — subsequent requests serve the in-memory placeholder straight from the marker without re-parsing the source. This replaces the former on-disk placeholder negative cache, which cost ~40 KB per cover-less book. `covers/store.go:ServeThumbnail` self-heals a missing thumbnail on serve (regenerates from the cached cover) and falls back to the full cover with `no-cache` until a thumbnail is available.
5. **Graceful Fallback**: If no cached cover exists and no extractor can produce one, the server responds with a placeholder image embedded in the Go binary (`internal/covers/`), rather than throwing an error.
6. **Thumbnail generation**: Thumbnails are generated eagerly in the cover write path (`writeFile`) — at ingest and on lazy extraction — so the thumbnail is ready immediately after the cover is first stored. Thumbnails preserve aspect ratio, never upscale, longest side ≤400px, JPEG quality 85.
7. **Cache policy (`?v=` buster)**: cover URLs carry `?v=<content_hash>-<cover file mtime>` (`covers.Store.Version`), so the URL changes when the cover *selection* changes (metadata hash) **or** when the cover *bytes* change without a metadata change (a placeholder later upgraded to a real cover, a better edition's cover saved by a later sync). Thumbnail URLs add a third component: `?v=<content_hash>-<cover_version>-<thumb_token>` (e.g. `t400q85`), where the token encodes the rendering spec and changes when thumbnail generation parameters are updated. Real covers and thumbnails are served `Cache-Control: public, max-age=31536000, immutable`; placeholder and fallback responses are served `no-cache` so clients revalidate and pick up a real cover that appears later.

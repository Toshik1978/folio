# Release Notes

Human-readable highlights of what changed in each Folio release, written for
people running the app. For the full commit-level technical log, see
[CHANGELOG.md](./CHANGELOG.md).

---

## v1.3.0 — 2026-06-26

This release makes covers and metadata **lazy**: instead of an eager pass that
hammered providers after every sync, Folio now fetches art and metadata on
demand and remembers what it has already resolved.

### Highlights

- **Lazy covers and metadata.** The eager cover warmer is gone. Syncs finish
  faster and stay quiet, and covers/metadata are resolved when they're actually
  needed rather than all at once after an INPX sync.
- **Cover serving driven by tracked state.** Folio now records each book's cover
  status (`cover_state`) and serves from it, dropping the on-disk placeholder
  file. Books known to have no cover short-circuit immediately instead of
  re-attempting work on every request.
- **Offline metadata backfill in OPDS feeds.** The acquisition feed now fills in
  missing metadata from sources you already have — bounded and with no online
  trip — so OPDS clients see richer entries without slowing the feed down.

---

## v1.2.1 — 2026-06-25

A small follow-up to v1.2.0.

### Notable fixes

- **Cover-less books stop hammering the providers.** The warm sync pass now
  negative-caches books that have no cover, so each run no longer re-searches
  every source for art that doesn't exist.

---

## v1.2.0 — 2026-06-25

This release is about **faster browsing** and **better covers**: real
thumbnails in the grid, offline metadata backfill after a sync, and sharper,
higher-resolution cover art.

### Highlights

- **Cover thumbnails everywhere.** Folio now generates aspect-preserving
  thumbnails on the cover write path and serves them in the book grid and OPDS
  feeds, so listings load quickly instead of pulling full-size images. Thumbnail
  URLs are built server-side with a cache-spec token, and a self-healing
  fallback regenerates a missing thumbnail from the original cover on demand.
- **Offline metadata backfill.** A new local backfiller fills in missing
  metadata from sources you already have — no network needed. The sync warmer
  runs it automatically after an INPX sync, and single-book lookups backfill on
  the fly.
- **Sharper, higher-resolution covers.** Cover search now fetches correct
  high-res art via ISBN/ASIN sources and filters candidates for relevance
  centrally in the aggregator, with deterministic ordering so the best cover
  wins consistently.

### Robustness

- **Friendlier to Amazon/Goodreads.** The Amazon direct search is throttled to
  protect IP reputation, Akamai and Cloudflare anti-bot interstitials are
  detected and no longer retried in a loop, and a new `ErrNoRetry` signal stops
  pointless retries on terminal errors.
- **INPX sync skips missing files.** Books whose archive file is absent are
  skipped instead of failing the import.
- **Thumbnail decoding is memory-bounded** to prevent out-of-memory on hostile
  images.

### Notable fixes

- Infinite scroll no longer retry-storms when a page fails to load.
- A local cover is kept if extraction times out mid-enrich, instead of being
  dropped.
- Cover priority is only raised once the cover file has actually been saved.

---

## v1.1.0 — 2026-06-24

This release is mostly about **metadata and covers**: finding better ones,
editing them by hand, and doing it all more safely.

### Highlights

- **Cover search across multiple sources.** Folio can now pull cover art from
  Open Library, Google Books, Amazon, and Goodreads at once, deduplicate the
  results, and rank them. A new cover-picker grid lets you browse matches and
  jump straight to the source.
- **Manual metadata editing.** A reworked book-edit modal lets you fix titles,
  authors, tags, language, and identifiers (ISBN, etc.) directly in the UI —
  and set a cover by upload, paste, drag-and-drop, or URL.
- **Canonical genre taxonomy.** Genres are normalized to a single BISAC-based
  list and exposed via `GET /genres`, so edits and imports agree on the same
  labels.

### Security & robustness

- **SSRF protection** on cover-URL fetching: requests to internal/private
  addresses are blocked, and the DNS-rebinding gap is closed by pinning dials.
- **Library paths confined** to an optional `LIBRARY_ROOT`.
- **Token-less CSRF guard** added on `/api`.
- **Bounded resource use** when decoding covers and parsing source files
  (decompression-bomb caps, memory limits, safer MOBI/EPUB parsing).
- **Single-writer SQLite guard** so concurrent API writes and syncs serialize
  cleanly instead of contending.

### Notable fixes

- Manual book edits are now atomic.
- Sync is steadier: idle SSE streams stop reconnecting, the silence watchdog
  re-arms correctly, and last-sync checkpoints persist reliably.
- Anti-bot blocks from Amazon/Goodreads scrapers are retried through transient
  failures instead of failing the whole search.

---

## v1.0.0 — 2026-06-21

The first stable release of Folio: a self-hosted, **read-only** digital book
library that catalogues your e-book collection and serves it through a web UI
and an OPDS feed. Mount your sources read-only, and Folio indexes them into its
own SQLite database — it never writes back to your books.

### Highlights

- **Smarter book grouping.** Editions of the same book are matched by strong
  identifiers (ISBN and friends) before falling back to title/author keys, with
  order-independent author matching — so the same book from different sources
  collapses into one entry.
- **Reliable cover handling.** Cover files are written atomically (temp +
  rename), so an interrupted import never leaves a half-written image.
- **Robust ingestion.** Identifiers are validated before grouping, MOBI titles
  decode correctly even without an EXTH header, and image/JSON decoding is
  memory-bounded to keep imports from blowing up on malformed files.

### Under the hood

- Genre taxonomy aligned to BISAC subject labels.
- Pagination hardened against offset overflow.
- Sync avoids busting caches on no-op checkpoint skips.

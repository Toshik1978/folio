## v1.3.2 (2026-06-26)

### Fix

- **opds**: emit inline templated search link for Moon+ Reader

## v1.3.1 (2026-06-26)

### Fix

- **opds**: advertise OpenSearch with correct media type

## v1.3.0 (2026-06-26)

### Feat

- **opds**: offline metadata backfill on acquisition feed (bounded, no online trip)
- **covers**: drive cover serving from cover_state, drop on-disk placeholder
- **ingest**: add CoverState adapter over books.cover_state
- **db**: add books.cover_state column and queries

### Fix

- **covers**: short-circuit thumbnail serve on StateNone; test-double + comment cleanup

### Refactor

- **sync**: remove eager cover warmer; covers/metadata now lazy

## v1.2.1 (2026-06-25)

### Fix

- **sync**: warm pass negative-caches cover-less books

## v1.2.0 (2026-06-25)

### Feat

- **ingest**: skip INPX books whose archive file is missing
- **sync**: warmer backfills offline metadata after INPX sync
- **ingest**: add LocalBackfiller offline metadata tier
- **metasearch**: filter cover relevance centrally in the aggregator
- **metasearch**: fetch correct high-res covers via ISBN/ASIN sources
- **amazon**: throttle the direct search to protect IP reputation
- **amazon**: detect Akamai interstitial and stop retrying it
- **amazon**: filter search thumbnails by title relevance
- **amazon**: add title-relevance filter for cover candidates
- **metasearch**: add ErrNoRetry to stop RetryCovers on terminal errors
- **api,opds**: build thumbnail URLs server-side with a cache-spec token
- **web**: load cover thumbnails in the book grid
- **api**: add cover thumbnail route
- **opds**: serve cover thumbnails and point rel=thumbnail at them
- **covers**: serve thumbnails with self-healing cover fallback
- **covers**: generate and invalidate thumbnails in the cover write path
- **covers**: add aspect-preserving thumbnail generator

### Fix

- **web**: reject failed loadMore so infinite scroll stops retry-storming
- **covers**: keep a local cover when extraction times out mid-enrich
- **ingest**: raise cover_prio only after the cover file actually saves
- **metasearch**: deterministic cover ordering with a FullURL tiebreak
- **goodreads**: stop retrying the terminal Cloudflare 202 challenge
- **amazon**: retry transient blocks and honor the interstitial ErrNoRetry
- **metasearch**: require explicit source in ApplyMatch, drop guess-fallback
- **metasearch**: resolve enrichment in one call instead of search+get
- **amazon**: broaden audiobook/audio-cd junk markers
- **web**: add thumbnail_url to hand-built Book literal in EditBookModal spec
- **covers**: cap thumbnail decode dimensions to prevent OOM

### Refactor

- **ingest**: move logger after context in ingestINP signature
- **api**: getBook delegates offline backfill to ingest.LocalBackfiller
- **metasearch**: final-review polish (docs, test clarity, named type)
- **metasearch**: single-scan CDN regex and drop stale comments
- **metasearch**: name CDN image transforms for their real contract
- **metasearch**: share one book->query identifier builder (fixes ASIN drift)
- **covers**: share one decompression-bomb pixel-cap guard
- **goodreads**: drop per-provider relevance filter (now in aggregator)
- **amazon**: drop the dead DuckDuckGo cover fallback
- **web**: use server thumbnail_url in the book grid
- **covers**: extract shardDir and serveImmutableFile helpers
- **covers**: rename Store.ServeHTTP to ServeCover

### Perf

- **covers**: decode a non-JPEG cover once on the write path

## v1.1.0 (2026-06-24)

### Feat

- **api**: confine library paths to optional LIBRARY_ROOT
- **metasearch**: log per-source cover-search outcomes
- **metasearch**: add DuckDuckGo fallback for blocked Amazon covers
- **metasearch**: use Goodreads autocomplete JSON API for covers
- **metasearch**: add ErrBlocked sentinel and RandomUserAgent helper
- **server**: add token-less CSRF guard on /api
- **metasearch**: full-resolution covers from amazon-cdn providers
- **web**: source-qualified fix match apply
- **ingest**: book lookup seam for the metasearch coordinator
- **metasearch**: coordinator reimplementing the enricher facade
- **metasearch**: promote google books to dual-capability source
- **web**: cover search grid and provider deep-links
- **web**: api client for cover search
- **cmd**: wire cover-search providers into the books handler
- **api**: cover-search endpoint seeded from book metadata
- **metasearch**: goodreads cover scraper with golden-html parser test
- **metasearch**: amazon cover scraper with golden-html parser test
- **metasearch**: google books cover adapter (cover-only)
- **metasearch**: open library cover source
- **metasearch**: concurrent cover aggregator with dedupe and ranking
- **metasearch**: registry with capability fan-out
- **metasearch**: core capability/source/candidate types
- **web**: rework book edit modal with tag, language and identifier editing
- **web**: cover picker modal (upload, paste, drag, url)
- **web**: manual metadata edit modal
- **web**: api client for manual edit and cover endpoints
- **api**: expose canonical genre taxonomy via GET /genres
- **api**: manual metadata edit via overwrite engine
- **api**: set book cover from an image URL
- **api**: manual cover upload with sticky cover_prio

### Fix

- **metasearch**: harden cover-fetch fallback and tidy duplication
- **api**: treat explicit cover-search ?q= as verbatim
- **web**: re-arm infinite scroll so short pages don't stall
- **ebook**: bound cover and metadata extraction from source files
- **api**: pin cover-fetch dials to close the SSRF rebinding gap
- **web**: split book download and curation actions into separate rows
- **web**: treat the SSE heartbeat as liveness so idle streams stop reconnecting
- **db**: bound API write waits on the single-writer guard
- **api**: make manual book edit atomic
- address deferred review minors (stale facet toast, test hardening)
- **api**: compute cold-cache stats once under the cache lock
- **web**: degrade gracefully when matchMedia/localStorage are unavailable
- **web**: treat total=0 as determinate in the sync progress bar
- **web**: correct emitted() indexing in LibraryForm spec type cast
- **web**: preserve identifier row identity on edit via in-place rows
- **web**: key identifier rows by stable id, not index
- **web**: re-arm the SSE silence watchdog on every event
- **web**: default the sync interval when the field is cleared
- **web**: drop stale facet loads on rapid library switch
- **web**: ignore stale book fetches in BookDetailModal
- **ebook**: bounds-check MOBI title offset with unsigned math
- **covers**: apply the decompression-bomb pixel cap to JPEG too
- **opds**: cap feed pagination to avoid int64 offset overflow
- **metasearch**: skip malformed srcset densities and pick the highest valid cover
- **api**: canonicalize genres on edit/enrich to match the import path
- **ingest**: defer cover writes/deletes until the import batch commits
- **sync**: persist last-sync checkpoint on a detached context
- **sync**: run the purge checker in singleton mode
- **sync**: cancel in-flight work before stopping the scheduler
- **db**: serialize writers through a shared write guard
- **metasearch**: retry amazon/goodreads scrapers through transient anti-bot blocks
- **web**: Esc closes tag/language dropdown instead of the edit modal
- **metasearch**: wrap empty-source fallback error and test Enrich error path
- **api**: block SSRF to internal addresses in cover URL fetch

### Refactor

- **metasearch**: split Amazon scraper and detect interstitials
- **web**: drop redundant save click handler in EditBookModal
- share a single ISBN identifier constant
- **web**: extract a shared StarRating component
- **htmltext**: unexport entity table behind NewDisplayDecoder
- **ebook**: replace mutable series-prefix cache with one precompiled regexp
- **metasearch**: removed a few global variables
- **amazon**: rename http field to client; add HTTP fetch-path tests
- route enrichment through the metasearch coordinator
- **api**: neutral metadata candidates and source-qualified apply
- **ingest**: export VolumeToMetadata for metasearch reuse
- merged tests in the same package, added stricter linter rules and refactored multiple global objects
- **ingest**: moved logger and batchSize initialization to newImporter

### Perf

- rolled back previous commit partially due to performance concerns

## v1.0.0 (2026-06-21)

### Feat

- **ingest**: log when identifier grouping overrides key-based grouping
- **ingest**: enable identifier grouping for folder and inpx sources
- **ingest**: identifier pre-match for derived-identity sources
- **db**: reverse (type,value) identifier lookup query + index
- **ingest**: order-independent author key in groupKey

### Fix

- **covers**: write cover files atomically via temp + rename
- **api**: clamp pagination page to prevent offset overflow
- **logging**: namespace WithGroup attrs and correct the logging docs
- **covers,googlebooks**: bound image decode and JSON response in memory
- **ingest**: validate strong identifiers before grouping books
- **ebook**: decode MOBI header title entities when EXTH is absent

### Refactor

- **ingest**: align genre taxonomy to BISAC subject labels
- **api**: drop unused error return from toBookView
- **api**: minor test refactoring
- **db**: align bookColumns/scanBook order with regenerated dbq.Book

### Perf

- **sync**: don't bust caches on a no-op checkpoint skip

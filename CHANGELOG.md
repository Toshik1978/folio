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

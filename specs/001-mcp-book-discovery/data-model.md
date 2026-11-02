# Data Model: Book Discovery for Claude.ai

## Schema changes (migrations `006_fts_contentless.sql`, `008_fts_vocab_col.sql`, `009_books_described_index.sql`)

### `books_fts`: contentless-delete

```sql
CREATE VIRTUAL TABLE books_fts USING fts5(
    title, authors, series, annotation,
    content = '', contentless_delete = 1,
    tokenize = 'unicode61 remove_diacritics 1'
);
CREATE VIRTUAL TABLE books_fts_vocab USING fts5vocab(books_fts, 'row');
-- migration 008 replaces it with books_fts_vocab_col USING fts5vocab(books_fts, 'col'),
-- so "similar books" can weigh terms by the annotation column alone
-- books_ad trigger unchanged: DELETE FROM books_fts WHERE rowid = old.id
```

- `rowid` = `books.id`, same as migration 005.
- Column values:
  - `title` = `books.title`
  - `authors` = the author names in `book_authors` order, joined with a space
  - `series` = `series.name`
  - `annotation` = `htmltext.StripMarkup(books.annotation)`
- **Write rules**:
  - INSERT a full row.
  - UPDATE only as a full row, through `RefreshBookFTS`.
  - DELETE by rowid, done by the trigger.
  - A partial-column UPDATE is rejected by SQLite.
- Up migration: copy the rows from the existing table, which still stores its content.
- Down migration: rebuild a regular table from `books` joins. The annotation is copied raw, which is lossy for markup. The next `RefreshBookFTS` or re-index restores the stripped text.

### `idx_books_described` (migration 009)

```sql
CREATE INDEX idx_books_described ON books (id) WHERE annotation IS NOT NULL AND annotation <> '';
```

- Serves the count of described books (N in the "similar books" idf). Its WHERE clause must stay identical to that count in `internal/db/similar.go`.

### `books` (no new columns)

| Column | Role in this feature |
|---|---|
| `annotation` | Filled by Deep Index **only when NULL**. HTML as normalized by the FB2 parser. |
| `metadata_checked` | Set to 1 once Deep Index or the page view has read the book's own file, whether or not it found an annotation. This is the shared "done" marker. |
| `imported_at` | The sync-run stamp. It limits the post-sync fill to books inserted in that run. |
| `enrichment_checked` | Never touched by Deep Index or MCP. |

### New sqlc queries

- `CountDeepIndexWork(library_id, imported_at nullable)`: the number of books with `metadata_checked = 0` and at least one file in the library, optionally restricted to `imported_at = ?`. It is the run's progress total.
- `ListDeepIndexWork(library_id, imported_at nullable, after_id, lim)`:
  - Returns `book_id, file_id, file_format, source_path, failure_fingerprint, failure_reader_version` for every file of the next `lim` books in the same scope with `id > after_id`, taken in id order.
  - The failure columns come from the book's `book_read_failures` row for the `metadata` flow (NULL when none), so the read gate needs no query per book.
  - Within the page, rows are ordered by `source_path`, then book id.
  - The preferred file is picked in Go, not SQL: the reader ranks EPUB, then FB2, then MOBI; PDF is never read for descriptions.
- `MarkMetadataChecked`, reused per row inside the batch transaction.
- `SetBookAnnotationIfNull` (stores the annotation only when the book has none) and `BookHasAnnotation`.
- `ListLanguagesWithCounts(library_id)`: `language, count(*)`, library-scoped, 0 meaning all libraries. Empty language codes are left out; ordered by count descending, then code.

## Runtime entities

### Engine request (`internal/sync`)

| Field | Type | Notes |
|---|---|---|
| `id` | int64 | library id |
| `kind` | enum | `sync` \| `deep_index` |
| `force` | bool | sync only |

- Dedup key: `(id, kind)`.
- A forced sync upgrades a pending sync of the same library, as today.

### Deep Index run

- **Scope**: `All` (button) or `ImportedAt(stamp)` (post-sync fill).
- **Result**:
  - `checked`: marked done, including books with no annotation and PDF-only books.
  - `filled`: annotation stored.
  - `skipped`: already had an annotation; marked checked.
  - `broken`: newly recorded as unreadable in `book_read_failures`; left unchecked.
  - `known_broken`: skipped unopened because their record still applies; left unchecked.
  - `errors`: left unchecked for a retry (a failure that may pass: I/O, an unreadable archive).
- **Progress**: `processed` / `total` books, sent through the existing `progress` event. The work list is read 5,000 books at a time; results are committed 500 books per transaction.

### MCP result shapes

**BookSummary**:
- `id`, `title`, `authors[]`, `series`, `series_number`, `genres[]`, `language`, `year`
- `excerpt`: the stripped annotation, at most 300 characters, cut at a word boundary
- `has_description`
- `link`

**BookDetail**:
- everything in BookSummary minus `excerpt`
- `description`: the full stripped annotation
- `publisher`, `rating`, `identifiers[] {type, value}`, `formats[]`, `library {id, name}`

**SimilarCandidate**: a BookSummary plus `reason`, one of `distinctive_terms`, `same_series`, `same_author` or `shared_genres`. When the reason is `distinctive_terms`, `matched_terms[]` is filled.

**Link**: `strings.TrimRight(PUBLIC_URL, "/") + "/books/" + id`, or `/books/{id}` when `PUBLIC_URL` is unset.

## State transitions: `books.metadata_checked`

```text
0 ──(Deep Index / page-view backfill reads file: found or not, or PDF-only)──► 1
0 ──(archive unreadable / I/O error)──► 0   (retried next run)
0 ──(entry missing / parse error)──► 0   (recorded in book_read_failures; not read again
                                          until the file or the reader version changes)
1 ──(no path resets it in this feature)
```

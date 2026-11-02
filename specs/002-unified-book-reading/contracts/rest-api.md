# Contract: REST/JSON changes

All changes are additive. Existing fields and routes keep their shape, and no state-changing route is added (FR-024: no retry endpoint).

## `GET /api/libraries` and `GET /api/libraries/{id}`

Each library object gains:

```json
{ "id": 1, "name": "…", "book_count": 1234, "unreadable_count": 3 }
```

- `unreadable_count` (integer, ≥ 0): the number of the library's books with a metadata-flow read-failure record whose `reader_version` equals the current reader version (FR-020).
- Cover-flow failures are never counted.
- The same field appears in every response that returns a library view (create, update, reactivate). A newly created library reports 0.

## `GET /api/books`

New query parameter:

| Param | Values | Effect |
|---|---|---|
| `unreadable` | `1` (anything else = unset) | Restricts results to books that are currently unreadable (same rule as above). It combines with every existing filter. With `library={id}` it lists exactly that library's unreadable books (FR-022). |

Pagination, sorting and the response envelope (`{items, total, page, limit}`) are unchanged. List items carry no `unreadable` field.

## `GET /api/books/{id}` and the other single-book responses

The detail object gains `unreadable`, present only when the book is currently unreadable. Every response that returns one book carries it the same way: `GET /api/books/{id}`, `PUT /api/books/{id}` (edit), `PUT`/`POST /api/books/{id}/cover` and `POST /api/books/{id}/match`, so a write never drops the notice from the open detail view:

```json
{ "id": 42, "title": "…", "annotation": null, "unreadable": { "reason": "corrupt" } }
```

- `reason` ∈ `corrupt` | `missing_entry` | `too_large` | `crashed` ([data-model.md](../data-model.md#enums)).
- The field is omitted when the book is readable, or when its record is stale (the file changed, or a newer reader version), because the detail handler re-checks before rendering (FR-023, US3 scenario 5). The write responses report the stored record of the current reader version without re-checking the file.
- The stored `detail` text is never returned.

## Unchanged

- The OPDS feeds, `/mcp` tools, cover and thumbnail endpoints and download endpoints keep their shape and behaviour.
- A broken cover still serves the placeholder (`no-cache`).
- The sync SSE payloads keep their shape. A sync skipped on an unchanged checkpoint that lifts stale records emits a `library {id, "active"}` event, so clients refetch the changed count. The Deep Index summary counts (`Broken`, `KnownBroken`) are logged; since the Telegram notifications (spec 003), `Broken` and a sample of the newly unreadable books also feed the run notification.

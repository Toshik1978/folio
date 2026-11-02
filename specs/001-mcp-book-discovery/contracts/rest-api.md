# Contract: REST and SSE changes

Every state-changing route passes the existing `sameSiteGuard` and `formBodyGuard`.

## `POST /api/libraries/{id}/deep-index`

- Queues a Deep Index run (scope: every unchecked book) for that library.
- **202** `{"status":"queued"}`
- **404** when the library is unknown.
- A `pending_purge` library is accepted (202) but skipped by the engine at run time, which matches how `POST /api/libraries/{id}/sync` behaves today (`requireLibrary` + engine check).
- If the library is already queued for Deep Index, the request is a no-op and still returns 202.

## `POST /api/deep-index`

- Queues Deep Index for every library that is not `pending_purge`.
- **202** with the `Status` body (same as `POST /api/sync`).

## SSE `/api/sync/events`: additive change

`status` payload:

```json
{"running": true, "current": 2, "kind": "deep_index", "queued": [1]}
```

- `kind` is `"sync"` or `"deep_index"`. It is omitted when nothing is running.
- `queued` stays a list of library ids, to stay backward compatible.
- `progress` and `library` events are unchanged. A Deep Index run emits `progress` with `{library, processed, total}` and, at the end, `library {id, status: "active"}`, so the UI refetches.

## SPA

| Location | Control | Calls |
|---|---|---|
| Library row (`LibraryTable.vue`) | **Deep Index** button, disabled while the row is busy | `POST /api/libraries/{id}/deep-index` |
| Libraries tab header (`SettingsPage.vue`) | **Deep Index All**, next to Re-index All, disabled while running | `POST /api/deep-index` |
| Sync banner / row status | Labels a running Deep Index "Deep indexing…" and shows its progress | reads `status.kind`, `progress` |

# Quickstart: validating Book Discovery

## Prerequisites

- `task setup`, then `task build` (or `task dev:backend` + `task dev:frontend`).
- Use a **copy** of a real database for the end-to-end checks: `cp <data-dir>/folio.db /tmp/folio-copy/`. Point `DATA_DIR` at that copy, and mount the libraries read-only.
- Set `PUBLIC_URL` to the external address.

## 1. Automated gates

```bash
task test && task lint
```

These cover:
- the FTS migration round trip;
- `RefreshBookFTS`;
- the header-only FB2 parse fixtures (windows-1251, no annotation, corrupt entry);
- DeepIndexer scopes and skip rules;
- engine kinds and dedup;
- REST endpoints;
- MCP tools through the SDK's in-memory client;
- the `/mcp/*` 404 guard;
- the Vitest specs for the buttons and the status `kind`.

## 2. Migration on a real-sized database

Start Folio on the copied database. It should log goose migration 006, and the run should take seconds.

```bash
sqlite3 /tmp/folio-copy/folio.db "select count(*) from books_fts; select count(*) from books;"
```

Expected: the two counts are equal. No `books_fts_content` table exists any more.

## 3. Deep Index

1. Open Settings → Libraries and press **Deep Index** on the INPX library.
2. Expected:
   - the banner shows "Deep indexing…" with processed/total;
   - the run finishes within 1 hour (SC-002);
   - the log has a summary line with the `checked`/`filled`/`skipped`/`errors` counts.
3. Check coverage:

   ```bash
   sqlite3 /tmp/folio-copy/folio.db "select sum(annotation is not null)*1.0/count(*) from books where library_id=<inpx>;"
   ```

   Expected: ≥ 0.75 (SC-001).
4. Press **Deep Index** again. Expected: it finishes in under 1 minute (SC-003).
5. Database size: `ls -la /tmp/folio-copy/folio.db`. Expected: growth ≤ 1 GB (SC-004).
6. Read-only check: the archives' mtimes and checksums are unchanged (compare `ls -l --time-style=full-iso` before and after).
7. Egress check: no outbound connections during the run (e.g. `ss -tnp` or container network logs).

## 4. Post-sync fill

Add a new FB2 entry to a test INPX library and update its index, or use a small test library. Let the scheduled sync run, or press **Sync Now**. Expected: after the sync, the new book has `metadata_checked = 1` and is found by a word from its description. Backlog books stay unchecked.

## 5. MCP

```bash
curl -s -X POST "$PUBLIC_URL/mcp" -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
```

Run this locally against `http://localhost:8080/mcp`; the public URL goes through Access. Expected: the 5 tools are listed. A `search_books` call with `{"query":"detective*","any_terms":["murder*","killer*"],"language":"en"}` returns books with links.

## 6. Claude.ai end to end

1. In Cloudflare Zero Trust, add a self-hosted Access application for `<host>/mcp` with OAuth for MCP enabled.
2. In Claude.ai, add a custom connector with the URL `https://<host>/mcp` and sign in through Access.
3. Ask: "Find me detectives about a smart killer and a stupid private detective." Expected: candidate books with working Folio links (SC-006).
4. Ask: "Books like <one of them>." Expected: similar candidates, without the source book.

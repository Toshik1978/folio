# Quickstart: validating Telegram Notifications

## Prerequisites

- Run `task setup`, then `task build` (or `task dev:backend` + `task dev:frontend`).
- Use a scratch data directory and scratch libraries, never a real one: `DATA_DIR=<scratch>/data`.
- Libraries are folder libraries created under `<scratch>/lib-*` through the Settings page (or `POST /api/libraries`). §4 needs a scratch INPX library instead, built as described there.
- No real bot is needed for §§1–7: the server talks to a local fake Telegram endpoint through `TELEGRAM_API_BASE` ([research.md §R13](./research.md#r13-testability-of-time-and-the-api-endpoint)). §8 uses a real test bot and channel that you own.
- Message texts are specified in [contracts/messages.md](./contracts/messages.md), endpoints in [contracts/rest-api.md](./contracts/rest-api.md), and state rules in [data-model.md](./data-model.md).

### Fake Telegram endpoint

This is a scratch tool, not part of the repository. Save it outside the repo as `<scratch>/fake-telegram.py` and run it with `python3 -I <scratch>/fake-telegram.py <port> <mode>`:
- `mode=ok` answers `{"ok":true}`;
- `mode=429` answers 429 with `retry_after: 2` for the first two calls of each message, then ok;
- `mode=400` always answers `Bad Request: chat not found`;
- `mode=down`: do not start the script at all, so the port refuses connections.

```python
import json, sys
from http.server import BaseHTTPRequestHandler, HTTPServer
port, mode, seen = int(sys.argv[1]), sys.argv[2], {}
class H(BaseHTTPRequestHandler):
    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        method = self.path.rsplit("/", 1)[-1]          # never print self.path: it holds the token
        n = seen[body["text"]] = seen.get(body["text"], 0) + 1
        print(f"--- {method} chat={body['chat_id']} thread={body.get('message_thread_id')} "
              f"silent={body.get('disable_notification')} attempt={n}\n{body['text']}", flush=True)
        if mode == "400": code, out = 400, {"ok": False, "error_code": 400, "description": "Bad Request: chat not found"}
        elif mode == "429" and n <= 2: code, out = 429, {"ok": False, "error_code": 429, "description": "Too Many Requests: retry after 2", "parameters": {"retry_after": 2}}
        else: code, out = 200, {"ok": True, "result": {"message_id": 1}}
        self.send_response(code); self.send_header("Content-Type", "application/json"); self.end_headers()
        self.wfile.write(json.dumps(out).encode())
    def log_message(self, *a): pass
HTTPServer(("127.0.0.1", port), H).serve_forever()
```

Start Folio against it:

```bash
TELEGRAM_API_BASE=http://127.0.0.1:<port> DATA_DIR=<scratch>/data task dev:backend
```

Run some sections once with `PUBLIC_URL=https://folio.example.com` and once without it, to see both the prefix and the neutral `Folio` label (FR-012).

## 1. Automated gates

```bash
task test && task lint
```

These cover:
- **notify**:
  - client request shape (`chat_id`, `message_thread_id` only with a topic, `disable_notification`, `link_preview_options`);
  - `APIError` decoding, including `retry_after`;
  - redaction of the token from transport errors and from Telegram's own `description`;
  - retry policy (3 attempts in total within a 30 s budget; 429 honoured up to 10 s, dropped above; 5xx / network retried; no retry on 400/401/403);
  - spacing, and a crash sent ahead of a full batch queue within the SC-002 bound;
  - a message dropped on a 429 pausing the sender for `retry_after` (capped at 10 min);
  - shutdown returning without the spacing wait after the last message, and staying bounded while the endpoint hangs;
  - queue overflow recorded as a failure;
  - store round trip and validation;
  - crash de-dup (first message at once, N repeats → one "repeated N more times" after the window, none when N = 0, normalization, the 256-key cap);
  - run state machine (SC-003 for N = 1, 3, 10; a crash-reported failure; skip-as-success; `LibraryRemoved`);
  - `ReportCrash` reporting whether it queued the message;
  - unreadable summary cap (SC-004);
  - mass-removal thresholds (19/100 no, 20/200 yes, 20/201 no, 25/100 yes);
  - error window (one message per window, the count, 5 samples, empty window silent, run lines);
  - off means nothing is collected;
  - truncation at 4000 characters;
  - the token-leak sweep (SC-006).
- **logging**: Error is tapped and Warn is not; `MarkReported` is skipped; `With` attrs are carried; output is unchanged.
- **ebook / db / sync / server**:
  - the parser panic is reported without a file name;
  - `WithTx` re-panics a `*TxPanic`;
  - engine outcomes per situation ([contracts/notifier.md](./contracts/notifier.md#internalsync-run-outcomes-and-engine-crashes)), including: a de-duplicated `safeSync` crash still sends `failed`; a library in `error` state is read in full, not skipped into a recovery; a Deep Index over an unreachable root fails, then recovers; a post-sync fill panic is a crash plus `FillFailed`, not a failed sync; panics in the purge sweep and in a folder-watcher event are reported and do not escape;
  - the folder watcher marks a missing-root watch error as already reported and forwards other watch errors;
  - the recoverer gives 500 + a report, re-raises `ErrAbortHandler` as the bare sentinel, and reports a TxPanic with its place.
- **ingest**: `RemovedBooks` counts books; `NewlyBroken` is capped at 5 while `Broken` counts all.
- **settings**: the `telegramSuite` ([contracts/rest-api.md](./contracts/rest-api.md#tests-telegramsuite-wired-in-testsettings)).
- **Vitest**: see [contracts/spa-ui.md](./contracts/spa-ui.md#vitest-coverage).

## 2. Setup and test message (US1, SC-001)

1. Open Settings. The first tab is labelled **General**, with the **Telegram notifications** section below OPDS.
2. Switch on with the fields empty: an inline message says both values are required, and the toggle stays off. The API gives the same answer: `PUT /api/settings/telegram` with `{"enabled":true,"chat_id":"","silent":false}` returns 400.
3. Enter a syntactically valid fake token `<digits>:<letters>`, channel `@test_channel` and topic `7`. Turn Silent on and Save. Press **Send test message**.
   - The fake endpoint prints a `sendMessage` with `chat=@test_channel thread=7 silent=True` and the test text.
   - The page shows success, and the status line shows `Last delivered …`.
   - All of this happens within 10 s.
4. Reload the page.
   - The token field is empty, and its placeholder shows only the last 4 characters (`Stored (ends with …)`; a token shorter than 8 characters shows just `Stored`).
   - `curl -s localhost:8080/api/settings/telegram` shows `token_set: true` and no token.
   - `grep -c '<the token>' <server log>` prints `0`.
5. Save again without typing a token, then send a test: it still works (the token was kept).
6. Restart the fake endpoint in `mode=400` and send a test: the page shows `Bad Request: chat not found`, and the status line shows it as the last failure (US1-AS4).
7. Switch off and save, then trigger §3's failure: nothing reaches the fake endpoint. Switch on again: nothing old is replayed (FR-015).

## 3. Failing and recovered library (US2, SC-003)

1. Create a folder library `<scratch>/lib-a` with a few readable EPUB files and let it sync.
2. Make it unreachable: `mv <scratch>/lib-a <scratch>/lib-a.off`. Press **Sync now** three times, waiting for each run.
   - Exactly **one** `Library "<library>": sync failed` message arrives, with the reason.
   - The server log has the `sync failed` lines.
   - No error summary repeats them 5 minutes later.
3. `mv <scratch>/lib-a.off <scratch>/lib-a`, then **Sync now**: exactly one `recovered` message arrives.
4. Fail it again, restart Folio, and fail it once more: one new `failed` message arrives (no memory across restarts, US2-AS5).
5. Fail it, then delete the library and let the purge complete (or **Purge now**). No `recovered` message is ever sent for it.

## 4. Unreadable summary (US3, SC-004)

The summary comes from the post-sync fill, which reads each INPX book out of its archive. A folder library cannot produce it: files that fail to parse are dropped at import and never recorded as unreadable books, so they are never reported (FR-008). Use a scratch INPX library instead.

1. Build `<scratch>/lib-inpx` holding `lib.inpx` and `lib.zip`. The archive has 7 entries, `1.fb2` to `7.fb2`, each random bytes; the index lists them. For example:

   ```python
   import os, zipfile
   d = "<scratch>/lib-inpx"; os.makedirs(d, exist_ok=True)
   with zipfile.ZipFile(f"{d}/lib.zip", "w") as z:
       for i in range(1, 8): z.writestr(f"{i}.fb2", os.urandom(512))
   # INP fields, \x04-separated: author, genre, title, series, serno, file, size, libid, del, ext
   rows = [f"Doe,Jane,\x04sf\x04Junk {i}\x04\x040\x04{i}\x04512\x04{i}\x040\x04fb2\x04" for i in range(1, 8)]
   with zipfile.ZipFile(f"{d}/lib.inpx", "w") as z: z.writestr("lib.inp", "\n".join(rows) + "\n")
   ```

   Add it as an INPX library (Settings page, or `POST /api/libraries`). INPX sync reads only the archive's file list, so the 7 books import normally.
2. Wait for the sync and its post-sync fill to finish. Exactly one message arrives: `sync found 7 newly unreadable book file(s)`, with 5 `- <book-ref>: file is damaged or not in the expected format` lines and `…and 2 more. They will not be reported again unless the files change.`
   - With `PUBLIC_URL` set, each ref is `<PUBLIC_URL>/books/<id>`; otherwise `book #<id>`.
   - The summary lines name books by link or ID and reason label only; no book title appears. (File paths inside a library are allowed in error texts, see §6, but the summary has none.)
3. Run **Deep Index** on that library: no message, because the 7 books are already recorded as unreadable.

## 5. Mass removal (US3)

1. In a folder library with 100 small valid books, delete 25 files and sync: one alert, `sync removed 25 of 100 books (25%)`. The books are removed as before.
2. Reset to 100 books, delete 19 and sync: no alert. Reset to 300 books, delete 25 (8%) and sync: no alert.

## 6. Error window (US3-AS5)

1. Produce errors outside runs, using the INPX scratch library from §4 (let its sync and fill finish first). Move its archive aside, `mv <scratch>/lib-inpx/lib.zip <scratch>/lib-inpx/lib.zip.off`, and download a few of its books through OPDS (Basic Auth required), for example `GET /opds/books/<id>/files/<fileID>` with the IDs from the library's OPDS feed. Each attempt answers 500 and logs the Error `opds: serve book file`. Do **not** sync, Deep Index, or wait for the watcher while the archive is away: the library's root is still there, but its archive is missing, so a run would report it as failing and muddy this check. (A library whose root itself is unreachable is already reported by its `failed` message, and its watch error is marked as already reported, so it never appears in this summary.) Any Error-level log outside a run works the same way.
2. After 5 minutes (the window), exactly one `<N> error(s) in the last 5 minutes` message arrives, with at most 5 lines and `…and N-5 more. See the server log for details.` when needed. Each line is `HH:MM:SS <log message>: <error text>`. For a missing archive it reads like `opds: serve book file: open archive …/lib.zip: open …/lib.zip: no such file or directory`; the path inside the library is allowed for the owner's channel (constitution v1.1.0) and is not stripped. No book title appears. Move the archive back afterwards (`mv <scratch>/lib-inpx/lib.zip.off <scratch>/lib-inpx/lib.zip`).
3. Wait another 5 minutes with no errors: nothing is sent.
4. A Deep Index run that leaves some books for retry adds one `completed with <K> book(s) left for retry` line to the next summary, and sends no `failed` message.

## 7. Delivery failure and rate limiting (US4)

1. Stop the fake endpoint (`mode=down`) and trigger §3's failure.
   - Within about 10 s (3 attempts in total, 2 s and 4 s backoff; never more than the 30 s budget), the status line shows `Last delivery failed … connection refused` (or similar), with no token in the text.
   - No message is sent about the failed delivery.
   - Syncs, browsing and downloads behave normally.
2. Restart the fake endpoint in `mode=429` and send a test message: the test reports Telegram's 429 text (a test send is a single attempt). Then trigger §3: the fake endpoint logs attempts 1, 2 and 3 for the same text (2 s, then 4 s apart: `max(retry_after, backoff)`), and the third, last allowed attempt succeeds.
3. Burst: with the fake endpoint down, create more than 32 scratch folder libraries, move their directories away, then use **Re-index All**. Each failure queues one message. Memory stays flat, and the status shows `queue full: … dropped` (FR-014). The automated `queueSuite` covers the same case deterministically.

## 8. Real Telegram (SC-001, manual, owner only)

Unset `TELEGRAM_API_BASE`. Use your own test bot and a test channel (or a supergroup with topics):
1. Enter the real token and channel, then send a test. The message arrives within 10 s, silently when Silent is on, and in the topic when one is set.
2. Use a wrong channel: the page shows Telegram's error text.

Do not paste the real token or channel ID into issues, logs or commits.

## 9. Overhead with Telegram unreachable (SC-005)

Compare the same scratch library, notifications off versus on with `mode=down`:
- `time` a forced re-index (**Re-index**) and a **Deep Index** three times each;
- measure `GET /api/books?limit=50` latency over 200 requests (`curl -w '%{time_total}'` in a loop).

Medians must be within 5% of each other.

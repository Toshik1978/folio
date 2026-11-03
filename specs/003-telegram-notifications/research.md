# Research: Telegram Notifications

Each topic has a Decision, a Rationale and the Alternatives considered. Telegram Bot API facts are stated from working knowledge of the public Bot API documentation. No network was used. The quickstart fake endpoint and the `telegramSuite` tests pin the behaviour Folio relies on, so a drift in the real API shows up when the test message is sent from Settings.

## R0. Overall approach

- **Decision**: a dedicated notifier service, `internal/notify`. Producers call it explicitly with structured outcomes, and a tap on Error-level logs feeds a 5-minute error window.
- **Rationale**: every notification the spec names already exists as a value at a known call site:
  - run status and error in `sync.Engine.runSync` and `deepIndexLibrary`;
  - `DeepIndexResult.Broken`/`Errors`;
  - pruned books in `ingest.reconciler.prune`;
  - recovered values at each recover.

  Typed outcomes make thresholds, de-duplication and the failing state unit-testable. The tap covers the open-ended "other errors" (FR-010) without editing ~80 `log.Error` sites.
- **Alternatives**:
  - *Logs only*: rebuild every summary by parsing log lines. Rejected as brittle: rewording a log message silently breaks a notification. It also cannot work, because some facts are not logged in a usable form:
    - `"sync done"` logs files removed, not books;
    - the library size is never logged;
    - newly unreadable books are logged one by one at Warn;
    - failed versus recovered needs state the logs do not carry.
  - *Subscribe to the UI event broker* (`internal/events`). Rejected:
    - the broker coalesces status and progress events and drops events for slow subscribers by design;
    - `library` events carry only `{id, status}`, with no reason, counts or unreadable list;
    - it sees no crashes and no non-sync errors, so the log tap is needed anyway.

    That is option 1 plus a lossy extra hop.

## R1. Telegram Bot API surface

- **Decision**: a single method, `sendMessage`:
  - Request: `POST {base}/bot{token}/sendMessage`, `Content-Type: application/json`.
  - Body:
    - `chat_id` (string, either `@channelusername` or the numeric ID such as `-100…`; the API accepts both as a string);
    - `text` (plain text, no `parse_mode`, so no escaping is needed and nothing in an error text can be read as markup);
    - `message_thread_id` (integer; only when a topic is set; forum topics in supergroups);
    - `disable_notification` (the Silent option);
    - `link_preview_options: {"is_disabled": true}`, so an instance URL in the prefix does not expand into a preview card.

    The older `disable_web_page_preview` is deprecated in favour of `link_preview_options`, and sending only the new field is enough.
  - Response, always JSON:
    - success is `{"ok": true, "result": {…}}`;
    - failure is `{"ok": false, "error_code": <int>, "description": "<text>", "parameters": {"retry_after": <seconds>}}`. `parameters` is present on 429, and `migrate_to_chat_id` appears when a group was upgraded.
  - Typical errors:
    - 400 `Bad Request: chat not found`;
    - 400 `Bad Request: message thread not found`;
    - 400 `Bad Request: message is too long`;
    - 401 `Unauthorized` (bad token);
    - 403 `Forbidden: bot is not a member of the channel chat` / `bot was kicked`;
    - 404 `Not Found` (malformed token path);
    - 429 `Too Many Requests: retry after N`.
  - Limits:
    - `text` is 1–4096 characters after entity parsing. Plain text has no entities, so it is 4096 characters.
    - Rate guidance: about 1 message per second per chat, at most about 20 messages per minute to the same group or channel, and about 30 messages per second per bot overall.
- **Rationale**: one HTTPS POST with a JSON body is easy to write with `net/http` + `encoding/json`. A client library would add a dependency (it needs approval) for one call.
- **Alternatives**:
  - `parse_mode: HTML` or `MarkdownV2`: rejected. Every dynamic string would need escaping, and an unescaped error text would make the send fail with 400.
  - `getMe` to validate the token on save: rejected. "Send test message" already proves token, chat and topic together, which is what the owner needs to know.

## R2. Token handling

- **Decision**:
  - The token is stored as plain text in the `settings` row `telegram_bot_token` (clarified in the spec).
  - It is validated on save against `^[0-9]+:[A-Za-z0-9_-]+$`, so it cannot inject `/`, `?` or `#` into the request path.
  - It is never returned: the view carries only `token_set` and `token_last4`.
  - It is never logged: no handler logs request bodies, and the store logs no values.
  - It is redacted from every error before the error leaves `notify.Client`. A `*url.Error` from `http.Client.Do` formats as `Post "https://…/bot<token>/sendMessage": <cause>`, so the client unwraps `*url.Error` to its inner `Err`, and as a second safeguard replaces any remaining occurrence of the token with `<redacted>`.
  - The same replacement is applied to `APIError.Description` before `Send` returns it: Telegram's own error text can echo the request path (`/bot<token>/…`), and that text is what the status line stores and the logs print.
  - An empty or absent token in `PUT` keeps the stored one (FR-002).
- **Rationale**: FR-002, FR-005, FR-016 and SC-006. `*url.Error` and an echoed `description` are the two places the token could leak into a log line or status text without anyone noticing.
- **Alternatives**:
  - Encrypting at rest: rejected by the clarification. The key would have to live in the same data directory.
  - Returning a masked token like `••••abcd` as a string field: equivalent. Separate `token_set` and `token_last4` fields are easier for the form.

## R3. Settings storage and access

- **Decision**:
  - The existing `settings` key/value table stores five keys, with constants in `internal/db/keys.go` next to the OPDS keys (listed in [data-model.md](./data-model.md#settings-keys)).
  - `notify.Store` reads them with the existing sqlc `GetSetting`; a missing key reads as its zero value.
  - It writes them with `UpsertSetting` inside `WriteGuard.WithTx`, with a 2 s acquire budget. Timeout → 503, the same contract as the API write handlers.
  - The notifier caches the active `Config` in an `atomic.Pointer`, loaded at start and swapped on save. Pending messages read it at send time ("settings in force when sent").
  - The HTTP adapter is a new `settings.TelegramHandler` over a consumer-declared `settings.TelegramService` interface, mirroring how `settings.Handler` drives `auth` through `settings.Service`. It is registered as one more `server.Registrar` in `main.go`.
- **Rationale**: no schema change (Principle IV) and no new sqlc query. The write guard keeps the process-wide single-writer rule, which `auth.writeCredentials` predates. The separate handler keeps `GET/PUT /api/settings` and its tests and SPA types unchanged.
- **Note (later fix)**: `auth.writeCredentials` no longer predates the rule: a separate fix moved the OPDS credentials save under the same write guard with a 2 s budget, so `PUT /api/settings` now also answers 503 `busy, try again` during a sync. The Telegram handler is unaffected.
- **Alternatives**:
  - Extending `GET/PUT /api/settings` with Telegram fields: rejected. It couples two unrelated services in one handler, and the `settings.Service` interface would grow Telegram methods that `auth` would have to satisfy.
  - A new `notification_settings` table: rejected, because a migration for five scalars buys nothing.

## R4. Crash recovery points

- **Decision**: four places report crashes. Each passes `place`, `detail`, the recovered value and a stack.

  | Place label | Code | How it reports |
  |---|---|---|
  | `book-file reading` | `ebook.Dispatcher.run` recover | Through `ebook.CrashReporter`, injected by `NewStandardDispatcher(WithCrashReporter(n))`. Detail is `"<format> parser, <metadata\|cover> read"`, never the file name. Its `PanicError` is still returned, so the book is classified as unreadable `crashed`, as today. |
  | `database write path` | `db.WriteGuard.WithTx` recover | **It does not report.** It rolls back and re-panics with `*db.TxPanic{Value, Stack}`, which exposes `CrashPlace()` and `CrashStack()`. The outer recoverer reports it, and `notify` uses the TxPanic's place, the original stack and "via <outer place>". |
  | `sync engine` | `sync.Engine.safeSync` recover | Through `sync.Notifier.ReportCrash`. Detail is `library "<name>" sync\|deep index`. |
  | `web request handling` | the new `server` recoverer (replaces chi `middleware.Recoverer`) | Through `server.CrashReporter`. Detail is `"<METHOD> <chi route pattern>"`. It logs through slog, which chi's Recoverer did not (it wrote to stderr through `PrintPrettyStack`). |

- **Rationale**:
  - The `ebook` hook is the only point that sees every parser crash: Deep Index, the on-view fill, the lazy cover flow and the folder import all go through `Dispatcher.run`. An injected interface whose name carries no format word keeps the public surface format-agnostic.
  - `WithTx` re-panics, so the panic is really recovered (or not) further up. Reporting there would send two messages for one crash, under different places so de-dup would not merge them. It would also report a crash that then kills the process. The wrapper keeps "database write path" as the place while reporting exactly once, from the recover that actually keeps the process alive.
  - `ErrAbortHandler` stays intact: the recoverer checks it with `errors.Is` (through `TxPanic.Unwrap`) and re-panics the original value, so `net/http`'s equality check still matches.

    **Superseded:** re-panicking the original value only matches when it *is* the sentinel; a wrapped one (e.g. inside a `*db.TxPanic`) failed `net/http`'s equality check, so `net/http` treated it as a real panic and logged its stack instead of aborting quietly. The recoverer now re-panics the bare `http.ErrAbortHandler` whenever `errors.Is` matches.
  - The route pattern is used instead of the raw path, so search queries and IDs never reach the message.
- **Alternatives**:
  - Report from `ingest`'s classification of `*PanicError`: rejected, because it has no stack and misses the folder import path.
  - A global crash hook variable: rejected by Principle VI (no globals).
  - Inject a reporter into `WriteGuard` and mark the value as reported: needs the same wrapper anyway, and reports before knowing whether the panic is recovered.
  - Keep chi's Recoverer and add a second middleware: two recovers on the same path, and chi's still prints to stderr outside slog.
- **Superseded (in part):** four points were not enough to keep every panic from killing the process or going unreported. Shipped in addition:
  - `sync.Engine.recoverCrash` / `reportPanic` guard engine work that runs outside `safeSync`, reported as place `sync engine` with a task detail: the post-sync fill (`library "<name>" post-sync fill`, which must not unwind into `safeSync` and turn a committed sync into a failed one), the purge sweep (gocron recovers job panics silently), a background purge, and each folder-watcher event (`folder watcher`, via `watcher.handleSafely`).
  - A Deep Index worker goroutine that panics outside the parser would have killed the process. The pool recovers it and re-panics `*ingest.WorkerPanic{Value, Stack}` on the calling goroutine; like `TxPanic` it carries `CrashPlace()` (`book-file reading`) and `CrashStack()`, so the engine's recover reports it with the worker's stack, via the engine place.
  - `ReportCrash` returns whether it queued the crash message, so callers can tell a sent crash from a counted or dropped one (see R6).

## R5. Crash de-duplication

- **Decision**:
  - Key: `place | firstAppFrame | normalize(errorText)`.
    - `normalize` replaces runs of digits and `0x[0-9a-f]+` addresses with `#`, so `index out of range [7] with length 3` and `[9] with length 5` match.
    - `firstAppFrame` is the first stack frame inside the module, skipping `runtime`, `runtime/debug`, `notify` and the recover wrappers.
  - First occurrence: send at once and open an entry `{first: now, suppressed: 0}`.
  - Same key within 1 h: `suppressed++` and send nothing.
  - When the hour ends (checked by `tick`, every 30 s): if `suppressed > 0`, send one "repeated N more times" message; delete the entry either way.
  - A crash after the entry ends starts a new cycle.
  - At most 256 live entries. Past that, crashes are sent without de-dup and the overflow is recorded in the status.
- **Rationale**: the spec's "same place, same error" (FR-006, SC-002). Without normalization, a corrupt collection whose parser panics with varying indexes would defeat de-dup and flood the channel.
- **Alternatives**:
  - Exact text match: rejected, for the flood risk above.
  - Key without a frame: rejected, because two different bugs panicking with "nil pointer dereference" in the same component would merge.

## R6. Library failing state and run outcomes

- **Decision**:
  - State is in memory, keyed **per library**, not per library and kind. Transitions are in [data-model.md](./data-model.md#library-failing-state).
  - A run is *failed* only when it could not complete:
    - the parser returned a non-cancel error;
    - "no parser for library type";
    - the Deep Index returned a non-cancel error;
    - a panic recovered in `safeSync`. The crash message stands in for "failed": `CrashReported: true` sets the state without a second message.

      **Superseded:** when the crash was folded into an open de-dup window (or dropped on a full lane) no crash message went out, and the failure was silent. `CrashReported` is now `ReportCrash`'s result (whether the crash message was queued), so "failed" is sent unless a crash message really stands in for it.
    - **Added in convergence:** a Deep Index whose read root (`ingest.CheckReadRoot`: the library path, or the `.inpx` file's directory) is missing, not a directory or unreachable. It is checked before any book is read, because otherwise every book failed one by one and the run *completed* with per-book errors, never reporting the library as failing.
  - A shutdown cancellation reports nothing.
  - A checkpoint-skipped sync counts as a success: the artifact was reachable and unchanged since the last good run, because failed runs store no checkpoint.

    **Superseded:** "failed runs store no checkpoint" does not cover a failed Deep Index, or a forced sync that failed on an unchanged file: the checkpoint still matches, so the next skip reported a recovery (and set the library back to active) without reading anything. A library in `error` state is now never skipped; it is read in full, so a recovery is only reported after a run that actually read the library.
  - A post-sync fill failure is not a failed run: the sync result stands, as today, and the failure becomes one error-window line.
  - A purged library is forgotten without a "recovered" message.
- **Rationale**: spec FR-007, SC-003, and the edge cases (deleted library, and a completed run with per-book errors counts as a success).
  - Keying per library gives exactly two messages for an unreachable library, whose sync and Deep Index both fail. Keying per kind would give four.
  - The risk, a success of one kind "recovering" a failure of the other, is small: Deep Index runs only on a manual trigger or as a post-sync fill, which never counts as failed.
- **Alternatives**:
  - Per (library, kind): rejected (above).
  - Persisting the state: rejected by the spec (memory only; "failed" may be sent again after a restart).

## R7. Delivery status in memory

- **Decision**: `DeliveryStatus{LastSuccessAt, LastFailureAt, LastFailureError}` is held in memory by the notifier. It is returned inside `GET /api/settings/telegram` and by `GET /api/settings/telegram/status`.
- **Rationale**:
  - A persisted status would need a DB write after every send. Writes go through the process-wide write guard, which a sync holds for its whole run. A status write would then wait minutes, or be skipped with a short budget, exactly while a run is reporting. Either way the sender stalls or the status is wrong, which conflicts with FR-014.
  - The status answers "is delivery working now", which is a property of the running process. After a restart the line says "No messages sent since the server started", and a test message refreshes it in seconds.
- **Alternatives**:
  - Persisting with a skip-if-busy write: rejected. The UI would show stale success during exactly the runs that matter.
  - Persisting only failures: same contention, and only half the line.

## R8. Queue, spacing, retry

- **Decision**:
  - Two buffered channels and one sender goroutine: an `urgent` lane (8 slots, crash messages only) that is always drained first, and a `batch` lane (32 slots, every other kind).
  - Spacing of 3 s between sends, which stays under about 20 messages per minute per channel.
  - Each message gets **3 attempts in total** (the spec's "3 attempts"): backoff 2 s, then 4 s, all inside a 30 s per-message budget, so a message is delivered or dropped well under a minute.
  - 429: wait `max(retry_after, backoff)`. `retry_after` is honoured up to a 10 s cap; above the cap, or when the wait would overrun the budget, the message is dropped and the failure recorded (no retry).
  - 5xx and network errors: retry. Other 4xx: no retry, record Telegram's description.
  - 10 s timeout per attempt (`http.Client.Timeout`).
  - A full queue drops the new message and records "queue full: N message(s) dropped" as the last failure, with no extra message (FR-011).
  - `Stop()` flushes open windows into the queue and gives the sender 5 s, then drops the rest.

    **Superseded (detail):** the sender still waited the 3 s spacing after the last message, eating most of the 5 s drain and the container's stop grace. It now returns as soon as intake is closed and both lanes are empty.
- **Rationale**: FR-013, FR-014 and SC-002. The 3 s spacing turns a burst of 10 distinct crashes into 30 s of delivery instead of a 429 storm. The urgent lane means a crash waits at most for the message in flight (≤ 30 s) plus the spacing, never for a full batch backlog, so SC-002's 1 minute holds while Telegram is reachable. Dropping on a long `retry_after` keeps the "well under a minute" intent instead of stalling every later message.

  **Superseded:** dropping one message did not stop the next ones from being sent straight into the same flood ban, each dropped in turn and prolonging the ban. After a message dropped on a 429 the sender now waits `max(Spacing, min(retry_after, 10 min))` before the next send, so later messages wait in their bounded lanes (overflow is still dropped and counted). The per-message policy (3 attempts, 10 s cap, 30 s budget) is unchanged; during a flood ban a crash can wait longer than SC-002's minute, which Telegram would refuse anyway.
- **Alternatives**:
  - Unbounded queue: forbidden (FR-014).
  - Retrying on all 4xx: pointless; a wrong chat stays wrong.
  - One queue for all kinds: rejected, a backed-up batch queue (32 × 3 s plus retries) could hold a crash past 1 minute (SC-002).
  - A priority queue per kind: unnecessary; only crashes have a latency target.

## R9. Error window and log tap

- **Decision**:
  - `logging.WithErrorSink(base, sink)` wraps the base handler. The wrapper shares the same `customHandler` value, with its mutex and output, so log output is byte-identical.
  - The wrapper forwards Error-level records to `notify.Service.LogError` unless `logging.IsReported(ctx)`.
  - Producers that report through an outcome or crash log with `log.ErrorContext(logging.MarkReported(ctx), …)`.
  - The notifier is built with the **untapped** base logger, so its own records can never re-enter (no loops by construction).
  - Window: the first record opens it with `deadline = now + 5 min`. Each record adds to the count; the first 5 are kept as `HH:MM:SS message: error` lines (the `error` attr only, capped at 300 characters). At the deadline, `tick` sends one message. An empty window sends nothing.
  - Completed runs with `PerBookErrors > 0`, and post-sync fill failures, add one line each.
- **Rationale**:
  - FR-010 and FR-011.
  - The context mark is invisible in log output, whereas a marker attr would print as `reported=true` on every line.
  - Construction order (base → notifier → tapped) avoids a setter on the logger, as Principle VI requires.
  - Using only the message and the `error` attr keeps lines short. The error text is sent as is, so it may contain file paths inside a library; constitution v1.1.0 allows that for the owner's channel, and paths are not stripped.
- **Alternatives**:
  - A marker attr: visible noise, and it must be stripped in two handlers.
  - A setter `logging.SetSink`: rejected by Principle VI.
  - Two independent handlers writing to stdout: lines could interleave.

## R10. Routine noise exclusions (FR-011)

- **Decision**:
  - Only Error level is tapped. Warn and below never notify.
  - Two existing Error logs on online-enrichment paths move to Warn:
    - the `googlebooks` transport failure (`internal/googlebooks/logger.go`);
    - the Fix Match search failure (`internal/api/match.go`, "match search").

    **Superseded (extended):** a third moved in convergence: the Fix Match apply failure when the provider call fails (`internal/api/match.go`, "apply match" after `ApplyMatch`). That call makes no DB access, so its failure is an upstream miss; the handler's DB faults stay at Error.
  - Unreadable files stay at Warn, as today. They reach the owner only through the run summary.
  - Delivery failures are logged at Warn on the base logger and never notified.
- **Rationale**: FR-011 and FR-017. The enrichment behaviour (responses, retries) is unchanged; only the log level moves.
- **Alternatives**: an allowlist or denylist of messages in the tap. Rejected as brittle (same problem as option 2).

## R11. Instance prefix

- **Decision**:
  - The first line of every message is the configured `PUBLIC_URL` (`config.Config.PublicURL`, already validated as absolute), with any trailing `/` trimmed.
  - When it is unset, the first line is the neutral label `Folio`.
  - Book references become `{PUBLIC_URL}/books/{id}` links (the SPA route, the same shape as `mcpserver.bookLink`). Without a public URL they are `book #{id}`.
- **Rationale**: FR-012. Principle VII says canonical external URLs come from `PUBLIC_URL`, never from request headers, and the notifier has no request anyway.
- **Alternatives**:
  - The hostname from `os.Hostname()`: leaks container IDs and is not useful.
  - A separate "instance name" setting: out of scope.

## R12. Message content and Principle III

- **Decision**:
  - Messages may contain:
    - library names;
    - run kinds and counts;
    - error and panic texts (capped at 300 characters per item);
    - the place and detail of a crash, plus one `pkg.Func (file.go:line)` frame;
    - book IDs or links;
    - unreadable reason labels;
    - file paths inside a library, when they occur in an error text (for example an I/O error); they are not stripped.
  - Messages never contain:
    - book titles, authors or descriptions;
    - book contents;
    - the per-book read `detail` (not an error text; it is never put into a message);
    - the raw request path or query;
    - the token, credentials or headers.
- **Rationale**: An actionable message needs the library and the error. Constitution v1.1.0 (Principle III) allows failure notifications to the owner-configured channel to carry library names, error texts including file paths inside a library, and book IDs or links, and forbids book titles, book contents, credentials and tokens. This list follows that exception.
- **Alternatives**:
  - Titles in the unreadable summary: rejected (catalog content); the link gives the same information one click away.
  - No error text at all: rejected, because the owner would have to read the logs to act.

## R13. Testability of time and the API endpoint

- **Decision**:
  - `notify.New` takes `WithClock(func() time.Time)` and `WithTiming(Timing{Spacing, Backoff []time.Duration, MaxRetryAfter, MessageBudget, ErrorWindow, CrashWindow, TickEvery, StopDrain, QueueSize, UrgentSize})`.
  - The state logic runs in `tick(now)`, which tests call directly.
  - The Telegram base URL is a constructor argument (`notify.NewClient(base, httpClient)`), wired from `TELEGRAM_API_BASE` (default `https://api.telegram.org`), so suites use `httptest.NewServer` and the quickstart uses a local fake.
- **Rationale**: Principle V wants hermetic tests with no sleeps on real windows. The env var lets the quickstart validate end to end against a local endpoint with no real bot.
- **Alternatives**:
  - A package-level `var apiBase`: a global, rejected.
  - Only a test hook with no env var: then the manual end-to-end check would need a real bot and channel.

## R14. Settings UI placement

- **Decision**:
  - `TelegramSettingsForm.vue` sits directly below `OpdsSettingsForm.vue`, in the first Settings tab (layout B):
    - the on/off switch at the top;
    - bot token, channel ID, topic ID, Silent;
    - Save and "Send test message";
    - the status line.
  - The first tab's **label** changes from "OPDS" to "General", with in-page headings "OPDS" and "Telegram notifications". Its **id** stays `opds`, so `?tab=opds` links keep working.
- **Rationale**: FR-001 puts the section "below the OPDS section". A tab labelled "OPDS" holding Telegram settings would mislead.
- **Alternatives**:
  - A third "Notifications" tab: contradicts FR-001's placement.
  - Keeping the "OPDS" label: misleading.

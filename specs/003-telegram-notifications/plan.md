# Implementation Plan: Telegram Notifications

**Branch**: `feature/telegram-notifications` | **Date**: 2026-11-03 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/003-telegram-notifications/spec.md`

## Summary

Folio runs unattended, so the owner should hear about real failures in a Telegram channel without being flooded. A new package `internal/notify` owns the whole feature. It holds the settings, the Telegram Bot API client (`net/http` only), a bounded send queue with spacing and retries, crash de-duplication, the per-library failing memory, the unreadable and mass-removal summaries, and the 5-minute error window. Producers call it explicitly with structured outcomes. Every call is non-blocking.

- **Settings** (US1, FR-001…FR-005). Five keys in the existing `settings` table. The token is stored as plain text, never returned (only `token_set` and the last 4 characters) and never logged. Endpoints: `GET/PUT /api/settings/telegram`, `POST /api/settings/telegram/test` and `GET /api/settings/telegram/status`. The SPA adds `TelegramSettingsForm.vue` under `OpdsSettingsForm.vue`.
- **Crashes** (US2, FR-006). The four recovery points report to the notifier:
  - the `ebook` dispatcher recover, through an injected `CrashReporter`;
  - the database write path: `WriteGuard.WithTx` re-panics with a `*db.TxPanic` that carries its place and the original stack, and the outer recoverer reports it;
  - the sync engine's `safeSync`;
  - a new slog-aware HTTP recoverer that replaces chi's `middleware.Recoverer`.

  Convergence fixes added guards for engine work outside `safeSync` (the post-sync fill, the purge sweep, a background purge and each folder-watcher event), reported as `sync engine` crashes, and Deep Index worker panics, re-raised on the calling goroutine as `*ingest.WorkerPanic` (place `book-file reading`). The first crash is sent at once. Identical crashes within an hour are counted into one "repeated N more times" follow-up.
- **Run outcomes** (US2, US3, FR-007…FR-009). At the end of every sync and Deep Index run (the spec's "bulk description indexing (Deep Index)"), the sync engine hands `notify.RunOutcome` to the notifier: failed or completed, the per-book error count, a sample of newly unreadable books, and the books removed against the library size. The notifier keeps the in-memory failing/recovered state per library and sends the unreadable summary and the mass-removal alert (≥10% and ≥20 books).
- **Error window** (US3, FR-010, FR-011). A tap in `internal/logging` forwards Error-level records into a 5-minute window, which sends one message with the count and the first 5 records. Records whose context is marked "already reported" are skipped (run failures and crashes log at Error with that mark). The notifier logs through the untapped base logger, so its own errors can never loop back.
- **Delivery** (US4, FR-012…FR-016). Plain-text English messages, prefixed with `PUBLIC_URL` or the neutral label `Folio`. Crash messages go through a small priority lane that the sender drains ahead of the 32-message batch queue; messages are sent at least 3 s apart. Each message gets **3 attempts in total** (2 retries, 2 s and 4 s backoff) inside a 30 s per-message budget, matching the spec's "3 attempts over well under a minute". A 429 `retry_after` is honoured up to a 10 s cap; when it exceeds the cap or the remaining budget, the message is dropped and the failure recorded, and the sender then waits out `retry_after` (at most 10 min) before the next message instead of sending it into the flood ban. The token is redacted from every error text before it is stored or logged, including Telegram's own `description`. Delivery status (last success; last failure time and error text) is kept in memory.
- **Message content** (constitution v1.1.0, Principle III exception). Messages go to the owner's own channel and may carry library names, error texts **including file paths inside a library**, and book IDs or links to the instance. They never carry book titles, book contents, credentials or tokens. Paths are not stripped from error texts.
- **Terminology.** "Deep Index" is the spec's "bulk description indexing (Deep Index)"; the spec's "channel ID" is Telegram's `chat_id` (settings key `telegram_chat_id`).

Research and the decisions behind this plan: [research.md](./research.md). Entities and state: [data-model.md](./data-model.md). Contracts: [contracts/rest-api.md](./contracts/rest-api.md), [contracts/notifier.md](./contracts/notifier.md), [contracts/messages.md](./contracts/messages.md), [contracts/spa-ui.md](./contracts/spa-ui.md).

### Approaches considered

**Where notifications come from**
- **(1) A dedicated notifier service that producers call explicitly with structured outcomes, plus a tap on Error-level logs: chosen.** Every outcome the spec names already exists as a value at a known call site:
  - run status and error at the end of `runSync` and `deepIndexLibrary`;
  - `DeepIndexResult.Broken`/`Errors`;
  - the books pruned in `reconciler.prune`;
  - the recovered value at each recover.

  Passing them as typed structs makes thresholds, de-duplication and the failing state testable without parsing text. The log tap covers "other errors" (FR-010) without touching the ~80 existing `log.Error` call sites. The cost is a few explicit calls in `sync`, `ebook`, `db` and `server`, plus one new package.
- **(2) Logs only (rebuild every summary by parsing log lines): rejected.** It is brittle, because a reworded message silently breaks a notification. Some outcomes are not logged in a usable form: the number of books removed (the log carries files removed, not books), the library size, which library is failing versus recovered, and the newly unreadable list (logged per book at Warn).
- **(3) Subscribe to the UI live-status broker (`internal/events`): rejected.** The broker is built for the UI. It coalesces and drops intermediate events for slow subscribers, and its `library` events carry only `{id, status}`, with no failure reason, counts or unreadable list. It knows nothing about crashes or other errors, so the log tap is still needed. That makes it option 1 with extra plumbing and a lossy channel.

**Delivery status: in memory, not persisted.** A status write on every send would go through the process-wide write guard. A sync holds that guard for its whole run, so recording a delivery could wait minutes, or be dropped, exactly while a run is reporting. The status describes the current process. After a restart the line reads "No messages sent since the server started", which is truthful. See [research.md §R7](./research.md#r7-delivery-status-in-memory).

**Crash reporting at the write path.** `WithTx` only rolls back and re-panics; the panic is really recovered further up (the HTTP recoverer or `safeSync`). Reporting at `WithTx` would produce two messages for one crash, or a message for a crash that then kills the process. So `WithTx` re-panics with `*db.TxPanic{Value, Stack}`, which names the place ("database write path") and keeps the original stack. The outer recoverer reports it once, as "database write path via web request handling / sync engine". See [research.md §R4](./research.md#r4-crash-recovery-points).

## Technical Context

**Language/Version**: Go 1.27 (backend). TypeScript + Vue 3 (SPA).

**Primary Dependencies**: Existing only: chi, modernc.org/sqlite, goose, sqlc. Standard library `net/http`, `encoding/json`, `log/slog`, `runtime/debug`. **No new dependency** (Go or npm). The Telegram Bot API is called directly over HTTPS ([research.md §R1](./research.md#r1-telegram-bot-api-surface)).

**Storage**: Folio SQLite at `<data-dir>/folio.db`, existing `settings` key/value table. **No migration.** The existing sqlc queries `GetSetting` and `UpsertSetting` suffice. New keys are listed in [data-model.md](./data-model.md#settings-keys). Everything else (queue, delivery status, crash de-dup, library failing state, error window) is in memory and lost on restart, by design (FR-007).

**Testing**: Go testify suites (one `Test<Package>` per package whose entry point only calls `suite.Run`). The new `internal/notify` gets `TestNotify`. `internal/logging` already has `TestLogging` → `loggingSuite`; the new `reportedSuite` and `sinkSuite` are wired into it. Telegram is faked with `httptest.Server` through the injectable API base URL. Time is injected (`now func() time.Time`, plus a `tick(now)` method driven directly by tests), so windows and backoff are tested without sleeping. Vitest covers `TelegramSettingsForm.vue` and the `api.ts` additions.

**Target Platform**: Linux container (multi-arch), `CGO_ENABLED=0`.

**Project Type**: Single Go web service with an embedded SPA.

**Performance Goals**:
- A producer call costs one mutex-guarded update and a non-blocking channel send, with no I/O on the caller's goroutine.
- SC-005: with Telegram unreachable, request latency and sync/Deep Index durations stay within 5% of runs with notifications off.
- SC-001: a test message arrives within 10 s, because it is sent synchronously with a 10 s client timeout.
- SC-002: a crash is enqueued at once on the priority lane, which the sender drains before the batch queue. With Telegram reachable, it waits at most for the message in flight (≤ 30 s budget) plus the 3 s spacing, so it is sent well within 1 minute even when the batch queue is full. `queueSuite` asserts it. (The exception is a Telegram flood ban: after a message dropped on a long 429, the sender pauses for `retry_after`, up to 10 min, and a crash waits with the rest.)

**Constraints**:
- Bounded memory:
  - queue of 32 messages;
  - the error window keeps a count plus at most 5 samples;
  - crash de-dup keeps one entry per distinct key for at most 1 h, capped at 256 keys;
  - the library failing map is bounded by the number of libraries;
  - message text is capped at 4000 characters.
- The single sender goroutine holds no locks while doing I/O.
- No write-guard use outside the settings `PUT`.
- The token never appears in logs, messages or responses (SC-006). That includes `*url.Error` strings, which embed the request URL, and Telegram's own `description`, which can echo the request path; see [research.md §R2](./research.md#r2-token-handling).
- Read-only sources: the notifier reads nothing from libraries.

**Scale/Scope**: one channel per installation, a handful of libraries, and bursts of at most a few hundred error records per window.

No NEEDS CLARIFICATION remains. Every open point is settled in [research.md](./research.md).

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | Status | Notes |
|---|---|---|
| I. Read-only sources | PASS | The notifier reads no library source. Its only writes are the five settings keys in `folio.db` under the data dir. |
| II. Single CGO-free binary | PASS | Standard library only. New `/api/settings/telegram*` routes sit under `/api`, so the SPA fallback guard (404 for unmatched `/api/*`) is unchanged. |
| III. Resource efficiency & minimal egress | PASS (constitution v1.1.0) | The egress is optional (off by default), best-effort, time-bounded (10 s per attempt, 3 attempts within a 30 s budget, bounded queue), documented in `docs/NETWORKING.md` (T009), and never breaks a user-facing request (FR-014). Content falls under the v1.1.0 owner-notification exception: library names, error texts including file paths inside a library, and book IDs or links to the instance; never book titles, book contents, credentials or tokens. |
| IV. Schema & query discipline | PASS | No schema change. Existing sqlc queries only. No dynamic SQL. |
| V. Testify suites | PASS | `TestNotify` and `TestLogging` are new single entry points. New tests in `sync`, `ebook`, `db`, `server`, `settings`, `ingest` are methods on existing or new suites wired from the existing entry point. Telegram is faked with `httptest`, so tests stay hermetic. |
| VI. Idiomatic Go & explicit deps | PASS | No new dependency. Interfaces are declared by consumers:<br>• `ebook.CrashReporter`, `sync.Notifier`, `server.CrashReporter`, `settings.TelegramService`, `logging.ErrorSink`.<br>• The concrete `*notify.Service` is injected by constructor or option in `cmd/folio-idx/main.go`, with no setters or globals.<br>• The logger cycle is broken by construction order (base logger → notifier → tapped logger), not by a setter. |
| VII. Security boundaries | PASS | The settings routes live under `/api` (external authenticator plus `sameSiteGuard`/`formBodyGuard`). `PUT` and the test `POST` are state-changing and so CSRF-guarded. The token is write-only on the wire, validated against `^[0-9]+:[A-Za-z0-9_-]+$` so it cannot inject into the request path, and redacted from every error string. The instance prefix comes from `PUBLIC_URL`, never `X-Forwarded-Host`. |

Post-design re-check: PASS. The Principle III tension found in the first draft is resolved by the constitution amendment to v1.1.0 (owner-notification exception), made before any code.
- The `ebook` public surface stays format-agnostic: the new `CrashReporter` and `WithCrashReporter` names carry no format word, and the existing `ebookSuite` public-surface test still guards it.
- No import cycles: `notify` imports only `db` and `db/dbq`. `sync`, `server` and `settings` import `notify` for types. `ebook`, `db` and `logging` import nothing new.
- `cmd/folio-idx` stays free of logic.
- Docs are updated in the phase that changes behaviour.

## Design

### Package layout and dependency direction

```text
logging  (leaf)            ← declares ErrorSink, MarkReported/IsReported; no internal imports
ebook                      ← declares CrashReporter; option WithCrashReporter
db                         ← TxPanic (CrashPlace/CrashStack/Unwrap); no reporter dependency
notify   → db, db/dbq      ← Service, Store, Client, outcome types; implements all sinks structurally
sync     → notify, logging ← declares Notifier (consumer interface); WithNotifier option
server   → logging         ← declares CrashReporter; Handlers.Crashes field; slog recoverer
settings → notify          ← declares TelegramService; TelegramHandler
main     → all             ← base logger → db → notify → tapped logger → everything else
```

`notify` never imports `sync`, `ingest`, `ebook`, `server`, `settings` or `logging`. It satisfies `ebook.CrashReporter`, `server.CrashReporter`, `sync.Notifier`, `settings.TelegramService` and `logging.ErrorSink` structurally. The method sets are in [contracts/notifier.md](./contracts/notifier.md).

### Phase A: Notifier core, Telegram client, settings, API, UI and test message (US1, US4)

**`internal/notify` (new)**
- `doc.go`: package doc. It states the invariants: non-blocking producers, no loops, the token never leaves the store/client, in-memory state.
- `config.go`:
  - `Config{Enabled bool; BotToken, ChatID string; TopicID int64; Silent bool}`.
  - `Update{Enabled bool; BotToken *string; ChatID string; TopicID *int64; Silent bool}`.
  - `(Config).Ready() bool` (token and chat set).
  - `validate(Update, stored Config) (Config, error)` returns `*ValidationError{Field, Msg}`:
    - token must match `^[0-9]+:[A-Za-z0-9_-]+$`;
    - chat must be `@name` (5–32 of `[A-Za-z0-9_]`) or a signed int64;
    - topic must be > 0;
    - `Enabled` requires both token and chat (FR-003).
  - `Settings` is the redacted view: `Enabled, TokenSet bool, TokenLast4, ChatID string, TopicID *int64, Silent bool, Status DeliveryStatus`.
- `store.go`:
  - `Store` over `*sql.DB` + `*db.WriteGuard`.
  - `NewStore(database, guard)`.
  - `Load(ctx) (Config, error)` makes five `GetSetting` calls; a missing key reads as its zero value.
  - `Save(ctx, Config) error` takes the write guard with a 2 s budget (`saveAcquireBudget`) and upserts the five keys in one transaction through `WithTx`.
  - Keys: constants `db.SettingTelegram*` in `internal/db/keys.go`.
- `telegram.go`:
  - `Client{http *http.Client; base string}` and `NewClient(base string, httpClient *http.Client) *Client`.
  - `Send(ctx, cfg Config, text string) error` does `POST {base}/bot{token}/sendMessage` with JSON `{chat_id, text, message_thread_id?, disable_notification, link_preview_options:{is_disabled:true}}`.
  - It decodes `{ok, description, error_code, parameters:{retry_after}}`.
  - Errors:
    - `*APIError{Code int; Description string; RetryAfter time.Duration}`, whose `Description` is passed through the same token replacement before the error is returned (Telegram's description can echo the request path `/bot<token>/…`);
    - network errors pass through `redact(err, token)`, which unwraps `*url.Error` to its inner error and replaces any token occurrence with `<redacted>`;
    - so nothing `Send` returns can carry the token, and the status line and logs store only what `Send` returned.
  - `retryable(err) (bool, time.Duration)`:
    - 429 → wait `retry_after`;
    - 5xx and network errors → backoff;
    - other 4xx → do not retry.
- `queue.go`:
  - `message{kind Kind; text string}`.
  - Two bounded channels: `urgent` (cap 8, `KindCrash` only) and `batch` (cap 32, every other kind). `Service.enqueue(m)` is a non-blocking send on the lane for `m.kind`. On overflow it counts a drop and records the last failure as "queue full: N message(s) dropped".
  - `sender(ctx)` is one goroutine:
    1. takes the next message, always draining `urgent` before `batch`;
    2. reads the current `Config` from the `atomic.Pointer[Config]` and drops the message if notifications are now disabled or not ready;
    3. sends with **3 attempts in total** (`Backoff` 2 s, 4 s) inside `MessageBudget` (30 s); a 429 waits `max(retry_after, backoff)`; when `retry_after` > `MaxRetryAfter` (10 s), or a wait would overrun the budget, the message is dropped and the failure recorded;
    4. records the status;
    5. waits `spacing` (3 s) before the next message; after a message dropped on a 429 it waits `max(spacing, min(retry_after, 10 min))` instead, and once intake is closed with both lanes empty it returns without waiting.
- `status.go`: `DeliveryStatus{LastSuccessAt, LastFailureAt *time.Time; LastFailureError string}`, guarded by a mutex. `recordSuccess(t)` and `recordFailure(t, msg)` update it. Failures are logged once per message at Warn on the base logger, and never notified (FR-011, US4-AS3).
- `format.go`:
  - `prefix(publicURL)` returns the trimmed `PUBLIC_URL` or `Folio`.
  - `truncate(text)` keeps the header and whole item lines up to 4000 characters, then adds `… (truncated)`.
  - `oneLine(s, max)` collapses newlines and caps each item at 300 characters.
  - The test text. Each later kind's builder lives in the file of its feature (`crash.go`, `runs.go`, `errwindow.go`), so `format.go` holds only shared helpers and no two tasks edit it. Formats: [contracts/messages.md](./contracts/messages.md).
- `notify.go`:
  - `Service` and `New(log *slog.Logger, store *Store, client *Client, opts ...Option) (*Service, error)`. It loads `Config` once and fails only on a DB error.
  - Options: `WithPublicURL(string)`, `WithClock(func() time.Time)`, `WithTiming(Timing)` (tests: spacing, backoff, windows, tick period).
  - `Start()` launches `sender` and a ticker loop (`tick(now)` every 30 s).
  - `Stop()` stops the ticker, flushes open windows into the queue, closes intake, and gives the sender up to 5 s to drain before cancelling it.
  - Settings methods: `TelegramSettings(ctx)`, `UpdateTelegram(ctx, Update)`, `SendTest(ctx) error` (synchronous, single attempt, 10 s, records status; returns `ErrNotConfigured` without token and chat) and `DeliveryStatus()`.
  - `UpdateTelegram` swaps the cached `Config`. Switching off clears the crash de-dup, the failing map and the error window (FR-015: nothing is collected while off).

**Config (`internal/config/config.go`)**: `TelegramAPIBase string \`env:"TELEGRAM_API_BASE" envDefault:"https://api.telegram.org"\``. `Validate` requires an absolute http(s) URL. It exists for tests and the quickstart fake endpoint, and is documented as such.

**Settings HTTP (`internal/settings/telegram.go`, new)**:
- `TelegramService` interface and `TelegramHandler`, built by `NewTelegram(log, svc)`.
- `Register` mounts `GET/PUT /settings/telegram`, `POST /settings/telegram/test` and `GET /settings/telegram/status`. The contract is in [contracts/rest-api.md](./contracts/rest-api.md).
- Error mapping:
  - `*notify.ValidationError` → 400;
  - guard timeout (`context.DeadlineExceeded` from the store) → 503, on `PUT` only: a test send never takes the guard, so its timeout is a network failure → 502;
  - `notify.ErrNotConfigured` → 409;
  - a Telegram failure on test → 502 with Telegram's description (already token-free).
- It never logs request bodies. A failed test send is logged at **Warn** (never Error): after Phase C this handler gets the tapped logger, and a delivery failure must not enter the error window (FR-011, US4-AS3). Store and other server faults stay at Error.
- `settings.go` stays as is. The existing `settingsSuite` is unchanged, and a new `telegramSuite` is wired in `TestSettings`.

**Composition (`cmd/folio-idx/main.go`)**:
1. `base := logging.New(...)`, then `db.Open(base, …)` and `writeGuard := db.NewWriteGuard()`.
2. `notifier, err := notify.New(base, notify.NewStore(database, writeGuard), notify.NewClient(cfg.TelegramAPIBase, &http.Client{Timeout: 10 * time.Second}), notify.WithPublicURL(cfg.PublicURL))`. On error: log and exit with the next unused code, 5 (codes 1–4 are in use; 4 is a server failure).
3. `log := base` in Phase A (the tap arrives in C).
4. Register `settings.NewTelegram(log, notifier)`.
5. Call `notifier.Start()`. `shutdown` calls `notifier.Stop()` after `syncEngine.Stop()`, so the final run outcomes are flushed.

**SPA**
- `web/src/types.ts`: `TelegramSettings`, `TelegramSettingsUpdate`, `TelegramDeliveryStatus`.
- `web/src/api.ts`: `fetchTelegramSettings`, `updateTelegramSettings`, `sendTelegramTest`, `fetchTelegramStatus`.
- `web/src/components/settings/TelegramSettingsForm.vue` (new, layout B):
  - an on/off toggle, the bot token (password input, placeholder `Stored (ends with <last4>)` when set, or `Stored` when `token_last4` is empty), channel ID, topic ID, the Silent checkbox, Save, "Send test message" and the status line.
  - It self-loads on mount like `OpdsSettingsForm`, and stays locked (a disabled `fieldset`) until the stored settings have loaded; a failed load offers Retry.
  - A topic ID that is not a positive integer is rejected inline before saving; "Send test message" is held back while the form has unsaved edits, since it uses the saved settings.
  - Switching on without a token (stored or typed) and a channel shows an inline message and reverts the toggle (FR-003); the server validates as well.
  - The token field is cleared after save and sent only when typed (FR-002).
- `web/src/pages/SettingsPage.vue`: the first tab keeps id `opds` (deep links stay valid). Its label becomes "General", with an "OPDS" and a "Telegram notifications" heading. `<TelegramSettingsForm />` renders below `<OpdsSettingsForm />`. The UI contract is in [contracts/spa-ui.md](./contracts/spa-ui.md).

**Docs (Phase A)**:
- `docs/API.md`: the new settings routes.
- `docs/NETWORKING.md`:
  - Outbound Connections: `api.telegram.org`, opt-in, what is sent (including that error texts may contain file paths inside a library) and what is never sent, and how to block it;
  - rewrite the "Historically … the one egress path" paragraph and Security Considerations item 6 "Minimal egress" so they name both outbound dependencies and state the v1.1.0 owner-notification exception;
  - security rules for the token.
- `docs/FRONTEND.md`: the Settings page section and component list.
- `docs/BACKEND.md`: layout (`notify/`), dependency table, composition order.
- `.env.dist`: `TELEGRAM_API_BASE` (commented, testing only).

### Phase B: Crash and run-outcome producers (US2, US3 except the error window)

**Crash de-dup (`internal/notify/crash.go`)**:
- `ReportCrash(place, detail string, recovered any, stack []byte) (queued bool)` reports whether the crash message was queued:
  1. returns at once when off or not ready;
  2. unwraps `interface{ CrashPlace() string; CrashStack() []byte }` (TxPanic), so the place becomes `"database write path"` and the outer place becomes "via <place> (<detail>)";
  3. extracts the first in-module frame from the stack (`firstAppFrame`: skips `runtime/`, `runtime/debug`, `notify`, and the recover helpers; renders `pkg.Func (file.go:line)` with a module-relative path);
  4. builds the key `place | frame | normalize(err)`. `normalize` replaces digit runs and `0x…` addresses with `#`.

  New key: send the crash message immediately and open a 1 h entry. Existing key: increment the suppressed count.
- `tick(now)` emits one "repeated N more times" for each entry whose hour has ended with N > 0, then deletes it.
- At most 256 live keys. Past that, new crashes are still sent but not de-duplicated, and the overflow is counted in the status.

**Crash producers**
- `internal/ebook/dispatch.go`:
  - `type CrashReporter interface{ ReportCrash(place, detail string, recovered any, stack []byte) bool }`, `type Option func(*Dispatcher)` and `WithCrashReporter(r CrashReporter) Option`.
  - `NewStandardDispatcher(opts ...Option)`. `NewDispatcher(parsers ...Parser)` is unchanged; test fakes get no reporter.
  - In `run`'s recover: `stack := debug.Stack()`, then `err = &PanicError{Value: r}`, then `d.reporter.ReportCrash("book-file reading", format+" parser, "+what+" read", r, stack)` when non-nil. The file name is not passed: the crash message describes the code fault, and the book itself is listed in the run's unreadable summary (see the double-reporting rule below).
  - `ebookSuite` adds `TestPanicReportsCrash` (a fake reporter receives the place and a detail without a file name) and `TestNoReporterStillRecovers`.
- `internal/db/writeguard.go`:
  - `type TxPanic struct{ Value any; Stack []byte }` with `Error()` (`"panic in write transaction: <v>"`), `Unwrap() error` (the value when it is an error), `CrashPlace() string` (`"database write path"`) and `CrashStack() []byte`.
  - The `WithTx` recover rolls back and re-panics with `&TxPanic{Value: p, Stack: debug.Stack()}`. A value that is already a `*TxPanic` (nested) is re-panicked as is.
  - `writeGuardSuite`: `TestPanicRollsBackAndWraps`.
- `internal/sync/engine.go`:
  - `type Notifier interface{ ReportCrash(...); RunFinished(notify.RunOutcome); LibraryRemoved(id int64) }` and `WithNotifier(n Notifier) Option`. A nil notifier is a no-op through the `e.notify*` helpers.
  - `safeSync`'s recover:
    - logs with `e.log.ErrorContext(logging.MarkReported(ctx), "sync panicked", …, slog.String("stack", …))`;
    - reports the crash with place `"sync engine"` and detail `library "<name>" <kind>` (the name comes from a best-effort `GetLibrary`, falling back to `#<id>`);
    - then sends `RunFinished{Status: RunFailed, CrashReported: <whether ReportCrash queued the crash message>}`, so the next success sends "recovered" but no separate "failed" message goes out. When the crash was only counted (open de-dup window) or dropped, `CrashReported` is false and the "failed" message is sent.
  - `recoverCrash(detail, onPanic)` (deferred) and `reportPanic` guard engine work outside `safeSync`: `safeFill` (a post-sync fill panic becomes the outcome's `FillFailed`, the sync stays completed), the purge sweep (gocron would swallow its panic), a background purge, and the folder watcher's `handleSafely` per event. Each logs `"engine task panicked"` with `MarkReported` and reports a `sync engine` crash.
  - `internal/ingest/deepindex.go`: a worker goroutine's panic is recovered in `process`, which cancels the pool and re-panics `*WorkerPanic{Value, Stack}` (`CrashPlace()` `"book-file reading"`, `CrashStack()`) on the calling goroutine, where the engine's recover reports it.
- `internal/server/recoverer.go` (new):
  - `recoverer(log *slog.Logger, crashes CrashReporter) func(http.Handler) http.Handler` replaces `middleware.Recoverer`.
  - On panic: an `http.ErrAbortHandler` (checked with `errors.Is` through `TxPanic.Unwrap`) is re-panicked as the bare `http.ErrAbortHandler`, because `net/http` compares it by equality. Otherwise it:
    - logs at Error with `MarkReported`: method, the route pattern from `chi.RouteContext`, the panic value and the stack, through slog rather than stderr;
    - reports with place `"web request handling"` and detail `"<METHOD> <route pattern>"` (never the raw path or query);
    - writes 500 unless the request is a connection upgrade.
  - It does not wrap the ResponseWriter, so SSE flushing is unaffected.
  - `Handlers` gains `Crashes CrashReporter` (nil is allowed).
  - `serverSuite`: `TestRecovererReportsAnd500`, `TestRecovererReportsTxPanicPlace` and `TestRecovererReraisesAbortHandler`.

**Run outcomes (`internal/notify/runs.go`)**: `RunFinished(o RunOutcome)` returns at once when off. It applies the state machine in [data-model.md](./data-model.md#library-failing-state):
- send "failed" on the first failure;
- send "recovered" on the first completion after a reported failure;
- send the unreadable summary when `NewlyUnreadable > 0`;
- for sync runs, send the mass-removal alert when `BooksRemoved >= 20 && BooksRemoved*10 >= BooksBefore` (`BooksBefore` > 0).

`LibraryRemoved(id)` drops the failing entry (no "recovered").

**Double-reporting rule for parser crashes.** Each fact is reported once. A parser panic during a run sends a crash message (the code fault, de-duplicated for an hour). The book is recorded as unreadable `crashed` and is listed once in that run's unreadable summary, like any other recorded read failure; no `crashed` filtering is applied. Neither reaches the error window: the crash path logs with `MarkReported` and the per-book read failure is logged at Warn.

**Out of scope: unreadable books recorded outside runs.** The on-view backfill (`ingest.LocalBackfiller.recordBroken`) records unreadable books when a book is opened. These are not notified: they are logged at Warn as today, shown as unreadable in the UI, and later runs skip them as already known (spec Assumptions).

**Run-outcome producers**
- `internal/ingest/reconcile.go`: `prune` returns `(filesRemoved, booksRemoved int, err error)`. `booksRemoved` is `len(ids)` of the empty books it removed.
- `internal/ingest/ingest.go`: `Result` gains `RemovedBooks int`, set in `driver.go`.
- `internal/ingest/deepindex.go`: `DeepIndexResult` gains `NewlyBroken []BrokenBook` (`BrokenBook{BookID int64; Reason ebook.Reason}`). `commit` appends up to `brokenSampleSize = 5` across batches in `Run`. `Broken` stays the total.
- `internal/sync/engine.go`:
  - `guardedSync` reads `CountBooksByLibrary` (existing sqlc query) under the guard before `parser.Sync` to get `BooksBefore`.
  - `runSync` reports `RunFailed` with `Reason: err.Error()` on a non-cancel error (the log changes to `ErrorContext(logging.MarkReported(ctx), "sync failed", …)`), and `RunCompleted` with `BooksBefore`/`BooksRemoved` on success.
  - A checkpoint skip reports `RunCompleted` with zero counts (the artifact was reachable, so it counts as success for recovery). A library in `error` state is never skipped but read in full: a Deep Index failure, or a forced sync that failed on an unchanged file, leaves the checkpoint matching, and a skip would otherwise report a recovery without reading anything.
  - "No parser for library type" reports `RunFailed`.
  - A cancelled run (shutdown) reports nothing.
  - `fillImported`'s result (newly unreadable sample and `Errors`) is merged into the same sync outcome, so one sync gives one summary. A fill failure becomes `FillFailed` text, an error-window line in Phase C; it is not a failed run.
  - `runDeepIndex` returns `(ingest.DeepIndexResult, error)`. `deepIndexLibrary` reports `RunFailed` (and logs with `MarkReported`) or `RunCompleted` with `PerBookErrors: res.Errors` and the unreadable sample. Before reading any book it stats the read root (`ingest.CheckReadRoot`: the library path, or the `.inpx` file's directory); an unreachable root fails the run as a whole instead of leaving every book for retry.
- `internal/sync/purge.go`: after a successful `purgeLibrary`, call `e.notifyLibraryRemoved(id)`.
- Composition: `ebook.NewStandardDispatcher(ebook.WithCrashReporter(notifier))`, `sync.WithNotifier(notifier)` and `server.Handlers{…, Crashes: notifier}`.
- Tests:
  - `engineSuite` with a recording fake `Notifier`: failed, completed, skip-as-success, cancel-reports-nothing, panic → crash and `CrashReported`, mass-removal inputs, purge → `LibraryRemoved`.
  - `ingestSuite`: `RemovedBooks` counts books, not files; `NewlyBroken` is capped at 5 while `Broken` counts all.
  - `notify` suites: `crashSuite`, `runsSuite`.

**Docs (Phase B)**:
- `docs/BACKEND.md`: middleware table (slog recoverer), dependency table.
- `docs/SYNC-EVENTS.md`: the middleware stack line.
- `docs/DATABASE.md`: no schema change; note `TxPanic` in the write-guard section.
- `docs/EBOOK-PARSING.md`: the Dispatcher's optional crash reporter.
- `docs/NETWORKING.md`: what crash, failed/recovered, unreadable and mass-removal messages carry (error texts may include file paths inside a library; never book titles or contents).

### Phase C: Log tap and 5-minute error window (FR-010, FR-011)

**`internal/logging`**
- `reported.go` (new): `MarkReported(ctx) context.Context` and `IsReported(ctx) bool`, using an unexported context key.
- `sink.go` (new):
  - `type ErrorSink interface{ LogError(ctx context.Context, r slog.Record) }`.
  - `WithErrorSink(base *slog.Logger, sink ErrorSink) *slog.Logger` wraps `base.Handler()` in a `sinkHandler`, sharing the same `customHandler`, its mutex and its output, so lines never interleave.
  - `sinkHandler.Handle`:
    1. calls the inner handler;
    2. when `r.Level >= slog.LevelError && !IsReported(ctx)`, passes `r.Clone()` to the sink, with the attrs baked in through `WithAttrs` (`WithAttrs` and `WithGroup` return new `sinkHandler`s that track them).
- `logging_test.go` (new): `TestLogging` → `loggingSuite`:
  - Error is forwarded, Warn is not;
  - a marked context is not forwarded;
  - `With` attrs are included;
  - output is identical with and without the sink.

**`internal/notify/errwindow.go`**:
- `LogError(ctx, r)` returns at once when off.
- Otherwise it builds `errorEntry{At, Text}`: `r.Message`, plus `": " + error` when an attr named `error` exists, capped at 300 characters. The error text is sent as is: it may contain file paths inside a library (allowed by constitution v1.1.0; no stripping). Other attrs are not used, to keep lines short.
- It appends to the open window (count++, keep the first 5). The first entry opens the window with `deadline = now + 5 min`.
- `addRunErrors(o)` adds one line per completed run with `PerBookErrors > 0`, or with `FillFailed` set (FR-010).
- `tick(now)` closes an expired window into one message. Nothing is sent for an empty window.

**Wiring and producers**
- `main.go`: `log := logging.WithErrorSink(base, notifier)` is the logger for every component except `db.Open` (startup only) and the notifier itself.
- The run-failure and crash logs from Phase B already carry `MarkReported`. Audit the other Error logs on those paths: `markError`'s own "record sync error" stays unmarked, because it is a separate DB fault. The folder watcher's `"watch library"` Error is marked when the root is missing or permission-denied, because the library's run reports that through its "failed" message; other watch errors stay unmarked.
- FR-011, online-enrichment misses become Warn:
  - `internal/googlebooks/logger.go` transport failure: Error → Warn;
  - `internal/api/match.go` "match search": Error → Warn;
  - `internal/api/match.go` "apply match" when the provider call (`ApplyMatch`) fails: Error → Warn (added in convergence; the DB faults in the same handler stay at Error).

  Behaviour is unchanged (FR-017).
- Tests: `errWindowSuite` covers the window lifecycle, the count of 5, an empty window, the run-error lines, that an entry never carries the token, and that a `*fs.PathError` error text keeps its path (no stripping). The `api` and `googlebooks` tests are adjusted only if they assert levels.

**Docs (Phase C)**:
- `docs/BACKEND.md`: logging tap and "already reported" marking rule for contributors: a new Error log on a path that already notifies must use `logging.MarkReported`.
- `docs/NETWORKING.md`: what the error summary may contain (log message and `error` text, which may include file paths inside a library).

### Phase D: Polish

- Run quickstart §§2–7 end to end against the fake endpoint, and §8 against a real test channel.
- SC-005 measurement (quickstart §9).
- Token-leak sweep: a `notifySuite` test drives every message kind with a sentinel token and asserts that no text, status string or `Settings` view contains it. A `telegramSuite` test asserts the same for the handler responses.
- Run `task lint && task test && task build`.
- Residual doc cross-check (the behaviour docs themselves change in the phase that changes behaviour).

### Delivery phases (for `/speckit-tasks`)

Each phase leaves `task build`, `task test` and `task lint` green and carries its own docs.

1. **Phase A, delivery path end to end:**
   - `notify` core (config, store, client, queue, status, format);
   - `TELEGRAM_API_BASE`;
   - settings handler and routes;
   - the main wiring with Start/Stop;
   - SPA form and tab;
   - test message.

   It is shippable on its own: the owner can configure and verify delivery.
2. **Phase B, crash and run-outcome producers:**
   - crash de-dup with repeat follow-ups;
   - `ebook`, `db`, `sync` and `server` crash points (slog recoverer);
   - `RunOutcome` from sync and Deep Index;
   - failed/recovered state;
   - unreadable summary;
   - mass-removal alert;
   - forgetting a purged library.
3. **Phase C, error window:**
   - `logging` tap and `MarkReported`;
   - 5-minute window with per-book error lines;
   - online-enrichment Error logs → Warn.
4. **Phase D, polish:** quickstart validation, SC-005 measurement, token-leak sweep, residual doc cross-check.

## Project Structure

### Documentation (this feature)

```text
specs/003-telegram-notifications/
├── plan.md              # This file
├── research.md          # Phase 0 output
├── data-model.md        # Phase 1 output
├── quickstart.md        # Phase 1 output
├── contracts/
│   ├── rest-api.md      # /api/settings/telegram* endpoints
│   ├── notifier.md      # Go-level interfaces producers call; outcome structs
│   ├── messages.md      # message text per notification kind
│   └── spa-ui.md        # TelegramSettingsForm behaviour + Vitest coverage
├── checklists/
│   └── requirements.md  # (from /speckit-specify)
└── tasks.md             # Phase 2 output (/speckit-tasks — not created here)
```

### Source Code (repository root)

```text
cmd/folio-idx/main.go                       # wiring order: base log → db → notify → tapped log; Start/Stop

internal/notify/                            # NEW package
├── doc.go
├── notify.go                               # Service, New, options, Start/Stop, tick loop
├── config.go                               # Config, Update, Settings view, validate, ValidationError, ErrNotConfigured
├── store.go                                # Store over settings table (GetSetting/UpsertSetting, write guard)
├── telegram.go                             # Client (net/http), APIError, redact, retryable
├── queue.go                                # urgent + batch lanes, sender, spacing, retry/backoff, budget
├── status.go                               # DeliveryStatus
├── format.go                               # prefix, truncate, oneLine, test text (shared helpers only)
├── crash.go                                # ReportCrash, de-dup, firstAppFrame, normalize, crash texts (Phase B)
├── runs.go                                 # RunOutcome, RunFinished, LibraryRemoved, run texts (Phase B)
├── errwindow.go                            # LogError, error window, run-error lines, summary text (Phase C)
├── notify_test.go                          # TestNotify → suite.Run only
├── config_test.go · telegram_test.go · queue_test.go · store_test.go · status_test.go · format_test.go · service_test.go
├── crash_test.go · runs_test.go · errwindow_test.go · leak_test.go

internal/config/config.go                   # TelegramAPIBase + Validate
internal/db/keys.go                         # SettingTelegram* keys
internal/db/writeguard.go                   # TxPanic; WithTx re-panics wrapped                (Phase B)
internal/ebook/dispatch.go                  # CrashReporter, Option, WithCrashReporter; recover reports (Phase B)
internal/ingest/ingest.go · driver.go · reconcile.go   # Result.RemovedBooks               (Phase B)
internal/ingest/deepindex.go                # DeepIndexResult.NewlyBroken (≤5), BrokenBook; WorkerPanic (Phase B)
internal/ingest/source.go                   # CheckReadRoot: Deep Index fails on an unreachable root (convergence)
internal/sync/engine.go                     # Notifier iface, WithNotifier, outcomes, safeSync crash, recoverCrash (Phase B)
internal/sync/watcher.go                    # handleSafely; unreachable-root watch error marked reported (convergence)
internal/sync/purge.go                      # LibraryRemoved after purge                       (Phase B)
internal/server/server.go                   # Handlers.Crashes; recoverer replaces middleware.Recoverer (Phase B)
internal/server/recoverer.go                # NEW slog-aware recoverer                         (Phase B)
internal/settings/telegram.go               # NEW TelegramService + TelegramHandler
internal/settings/telegram_test.go          # NEW telegramSuite (wired in TestSettings)
internal/logging/reported.go · sink.go      # NEW MarkReported/IsReported, ErrorSink, WithErrorSink (Phase C)
internal/logging/logging_test.go            # existing TestLogging; wires reportedSuite, sinkSuite (Phase B/C)
internal/googlebooks/logger.go              # transport failure Error → Warn                   (Phase C)
internal/api/match.go                       # "match search", provider "apply match" Error → Warn (Phase C)

web/src/types.ts · api.ts · api.spec.ts     # Telegram types + 4 calls
web/src/components/settings/TelegramSettingsForm.vue      # NEW
web/src/components/settings/TelegramSettingsForm.spec.ts  # NEW
web/src/pages/SettingsPage.vue · SettingsPage.spec.ts     # tab label "General", headings, form below OPDS

docs/API.md · NETWORKING.md · BACKEND.md · FRONTEND.md · SYNC-EVENTS.md · DATABASE.md
.env.dist                                   # TELEGRAM_API_BASE (commented)
```

**Structure Decision**: single Go service plus embedded SPA, as today. One new internal package (`notify`) and three new files in existing packages (`server/recoverer.go`, `settings/telegram.go`, `logging/sink.go` + `reported.go`). No new top-level directories.

## Complexity Tracking

No unjustified violations. The first draft recorded a Principle III deviation (messages carry library names, error texts and book IDs or links). The maintainer resolved it by amending the constitution to v1.1.0 before any code: Principle III now allows owner-configured failure notifications to carry library names, error texts (including file paths inside a library) and book IDs or links to the instance, and forbids book titles, book contents, credentials and tokens. This plan stays inside that exception.

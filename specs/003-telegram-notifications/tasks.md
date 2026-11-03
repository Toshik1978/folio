---

description: "Task list for Telegram Notifications"
---

# Tasks: Telegram Notifications

**Input**: Design documents from `/specs/003-telegram-notifications/`

**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/ (rest-api.md, notifier.md, messages.md, spa-ui.md), quickstart.md

**Tests**: Every task is implemented test-first, so each task writes its own tests. For Go, those are testify suite methods under the package's single `Test<Package>` runner, whose entry point only calls `suite.Run`. For the SPA, they are Vitest specs. There are no standalone test tasks. Each task names the tests it must write. Telegram is always faked with `httptest.NewServer` through the injectable API base; time is injected (`WithClock`, `WithTiming`, direct `tick(now)` calls), so no test sleeps on a real window.

**Docs**: the constitution requires docs to change in the same change as the behaviour. Each task that makes a `docs/*.md` passage stale updates it itself. The Polish phase keeps only a residual cross-check.

**Organization**: The four delivery phases from plan.md map to phases here. Phase A (notifier core, Telegram client, settings, API, UI and test message) delivers US1 and the delivery/status half of US4. Phase B (crash points, run outcomes, failed/recovered, unreadable summary, mass removal) delivers US2 and most of US3. Phase C (log tap and error window) completes US3. Phase D is Polish. No Setup phase is needed: the only shared prerequisites (settings keys, `TELEGRAM_API_BASE`) belong to Phase A's first tasks. Each phase is one feature commit. **Every phase leaves `task build`, `task test` and `task lint` green.** A task that changes a signature updates every caller listed in it.

**Content rule**: no personal or deployment-specific details (library names, paths, hosts, tokens, chat IDs) in code, tests, docs, commits or the PR. Tests use sentinel values such as `123456:TEST-token_SENTINEL`, `@test_channel` and `https://folio.example.com`.

**Message content (product behaviour)**: constitution v1.1.0, Principle III exception. Messages to the owner's channel may carry library names, error texts **including file paths inside a library**, and book IDs or links to the instance; never book titles, book contents, credentials or tokens. Paths are not stripped. Every doc a task touches says so in these words.

**Gate**: the constitution amendment to v1.1.0 is already in the tree and lands as its own `docs: amend constitution to v1.1.0` commit (Sync Impact Report comment removed) before Phase 1's feature commit.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: can run in parallel (different files, no dependency on an incomplete task)
- **[Story]**: US1 / US2 / US3 / US4 (spec.md user stories)

`internal/notify/notify_test.go` is the package's runner (`TestNotify`). Each task that adds a `notify` suite adds exactly one `suite.Run(t, new(<suite>))` line to it; when two [P] tasks run together, merge those lines by hand.

---

## Phase 1: User Story 1 + User Story 4 — Configure, verify and watch delivery (Priority: P1 / P3) 🎯 MVP

**Goal**: plan.md "Phase A". The owner can fill in the Telegram section under OPDS, save it (token write-only), send a test message, and see the delivery status line. The notifier service runs with its bounded queue, spacing and retry policy, but no producer feeds it yet except the test message.

**Independent Test**: quickstart §2 against the local fake endpoint: switching on with empty fields is refused; a test message reaches the fake endpoint with `chat=@test_channel thread=7 silent=True` within 10 s; a reload shows only the masked token; `mode=400` shows `Bad Request: chat not found` as the last failure; `GET /api/settings/telegram` never contains the token.

- [x] T001 [US1] Create `internal/notify` with its configuration model, and add the settings keys.
  - `internal/db/keys.go`: add `SettingTelegramEnabled = "telegram_enabled"`, `SettingTelegramBotToken = "telegram_bot_token"` (with the same `//nolint:gosec // settings key name, not a secret` comment as `SettingOPDSPassHash`), `SettingTelegramChatID = "telegram_chat_id"`, `SettingTelegramTopicID = "telegram_topic_id"`, `SettingTelegramSilent = "telegram_silent"`.
  - New file `internal/notify/doc.go`: package doc stating the invariants — producers never block and never do I/O; the notifier never logs through the tapped logger (no loops); the token never leaves `Store`/`Client`; all state except the five settings rows is in memory and lost on restart.
  - New file `internal/notify/config.go`, per data-model.md:
    - `type Config struct{ Enabled bool; BotToken string; ChatID string; TopicID int64 /* 0 = none */; Silent bool }` and `func (c Config) Ready() bool // BotToken != "" && ChatID != ""`.
    - `type Update struct{ Enabled bool; BotToken *string /* nil or "" = keep stored */; ChatID string; TopicID *int64 /* nil = none */; Silent bool }`.
    - `type Settings struct{ Enabled bool; TokenSet bool; TokenLast4 string; ChatID string; TopicID *int64; Silent bool; Status DeliveryStatus }`, where `TokenLast4` is `"" when unset or shorter than 8 characters`. Add `func view(c Config, st DeliveryStatus) Settings`. `Settings` has no token field.
    - `var ErrNotConfigured = errors.New(...)` and `type ValidationError struct{ Field, Msg string }` with `Error() string` = `"<field>: <msg>"`.
    - `func validate(u Update, stored Config) (Config, error)` applying the data-model "Validation on save" column verbatim:
      - `telegram_bot_token`: `^[0-9]+:[A-Za-z0-9_-]+$`; an absent or empty update keeps the stored value;
      - `telegram_chat_id`: `@` + 5–32 `[A-Za-z0-9_]`, or a signed 64-bit integer (e.g. `-100…`); `""` is allowed while `enabled` is false;
      - `telegram_topic_id`: integer > 0 when present;
      - `telegram_enabled`: `"1"` only when token and chat are set (FR-003) — "Saving with notifications on while the token or chat is missing returns 400, and nothing is written", with message `notifications need a bot token and a channel ID`;
      - `telegram_silent`: none.
      - "Switching off keeps every other value (US1-AS5)."
    - `ValidationError.Msg` never echoes the rejected token value.
  - New file `internal/notify/notify_test.go`: `func TestNotify(t *testing.T)` wiring only `suite.Run(t, new(configSuite))`.

  Tests (`configSuite`, `internal/notify/config_test.go`):
  - a table of token values (valid `123:abc_-X`; invalid with `/`, `?`, `#`, space, missing colon, non-digit id) → accepted / `*ValidationError{Field: "bot_token"}`;
  - chat IDs: `@abcde` and `@` + 32 chars accepted; `@abcd`, `@` + 33 chars, `@a-b` rejected; `-1001234567890` and `42` accepted; `-` and `9223372036854775808` rejected;
  - topic `0` and `-1` rejected, `nil` → `TopicID 0`;
  - `BotToken` nil and `""` keep the stored token; a new valid token replaces it;
  - `Enabled: true` with no stored or new token, or with an empty chat → the FR-003 error;
  - `Enabled: false` with every other field kept;
  - `view`: `TokenLast4` is the last 4 for an 8+ character token, `""` for a 7-character token and for no token; the rendered `%+v` of `Settings` never contains the token.
- [x] T002 [P] [US1] Add the settings store in new file `internal/notify/store.go`.
  - `type Store struct` over `*sql.DB` + `*db.WriteGuard`; `func NewStore(database *sql.DB, guard *db.WriteGuard) *Store`.
  - `Load(ctx) (Config, error)`: five `dbq.GetSetting` calls; `sql.ErrNoRows` reads as the zero value (`"1"`/`"0"` for the two flags, decimal text or empty for the topic).
  - `Save(ctx, Config) error`: derives a context with `saveAcquireBudget = 2 * time.Second`, then `guard.WithTx(ctx, s.db, …)` upserting all five keys with `dbq.UpsertSetting` in one transaction. A guard timeout surfaces as an error matching `context.DeadlineExceeded`. No value is ever logged.

  Tests (`storeSuite`, `internal/notify/store_test.go`, fresh `db.Open` on `s.T().TempDir()` per test):
  - an empty DB loads the zero `Config`;
  - a `Save`/`Load` round trip with and without a topic, Silent on and off;
  - a second `Save` overwrites every key;
  - with the guard held by the test, `Save` returns an error matching `context.DeadlineExceeded` and nothing is written (use a short budget through an unexported field set by the test).
- [x] T003 [P] [US4] Add the Telegram Bot API client in new file `internal/notify/telegram.go`, per contracts/notifier.md "Telegram client contract" (stdlib `net/http` + `encoding/json` only).
  - `type Client struct{ http *http.Client; base string }`, `func NewClient(base string, httpClient *http.Client) *Client`.
  - `Send(ctx, cfg Config, text string) error`: `POST {base}/bot{token}/sendMessage`, `Content-Type: application/json`, body `{"chat_id", "text", "message_thread_id" (omitted when `TopicID == 0`), "disable_notification": cfg.Silent, "link_preview_options": {"is_disabled": true}}`. No `parse_mode`.
  - Decode `{ok, description, error_code, parameters:{retry_after}}`: `ok: true` → `nil`; `ok: false` (any status) → `*APIError{Code, Description, RetryAfter: retry_after seconds}`; non-JSON body → a wrapped error carrying the HTTP status.
  - `type APIError struct{ Code int; Description string; RetryAfter time.Duration }` with `Error()` returning `Description`.
  - `redact(err error, token string) error`: unwraps `*url.Error` to its inner `Err`, then replaces every occurrence of the token with `<redacted>`. Every non-`APIError` error returned by `Send` goes through it, and `Send` applies the same replacement to `APIError.Description` before returning it (Telegram's description can echo `/bot<token>/…`). Nothing `Send` returns contains the token, so the last-failure text and the Warn log built from it are token-free.
  - `retryable(err error) (bool, time.Duration)`: `APIError.Code == 429` → `(true, RetryAfter)`; `Code >= 500`, transport errors, timeouts → `(true, 0)`; other `APIError` (400/401/403/404) → `(false, 0)`.

  Tests (`clientSuite`, `internal/notify/telegram_test.go`, against `httptest.NewServer`):
  - the request path is `/bot<token>/sendMessage`, the method POST, and the JSON body has exactly the fields above; `message_thread_id` is absent without a topic and `42` with one; `disable_notification` follows Silent;
  - `{"ok":true}` → nil;
  - `400 Bad Request: chat not found` → `*APIError{Code: 400}` with that description;
  - `429` with `parameters.retry_after: 7` → `RetryAfter == 7s`;
  - an HTML 502 body → a non-`APIError` error that `retryable` accepts;
  - a closed server (connection refused) → an error whose text contains neither the token nor `/bot`;
  - a `400` whose `description` echoes `Not Found: /bot<token>/sendMessage` → `*APIError` whose `Description` and `Error()` contain `<redacted>` and not the token;
  - `retryable` table over 429, 500, 503, 400, 401, 403, 404, a transport error and `context.DeadlineExceeded`.
- [x] T004 [P] [US4] Add the delivery status and the message-format helpers.
  - New file `internal/notify/status.go`: `type DeliveryStatus struct{ LastSuccessAt, LastFailureAt *time.Time; LastFailureError string }` behind a mutex (`statusBox` with `recordSuccess(t)`, `recordFailure(t, msg)`, `snapshot() DeliveryStatus`). Per data-model.md: "A failure does not clear `LastSuccessAt`, and a success does not clear the failure." `snapshot` returns copies of the times.
  - New file `internal/notify/format.go`, per contracts/messages.md:
    - `type Kind uint8` with `KindTest, KindCrash, KindCrashRepeat, KindRunFailed, KindRunRecovered, KindUnreadable, KindMassRemoval, KindErrorSummary`;
    - `prefix(publicURL string) string`: trimmed `PUBLIC_URL` without a trailing `/`, or `Folio` when empty;
    - `oneLine(s string, max int) string`: collapses newlines/tabs to single spaces, caps at `max` (300 for items) runes and appends `…` when cut;
    - `truncate(text string) string`: caps the whole message at `maxMessageChars = 4000`; when cut, drops whole trailing item lines and appends `… (truncated)`; the prefix and headline are never cut;
    - `testText(prefix string) string` → `<instance>\nTest message: Telegram notifications are working.`
    - `format.go` holds only these shared helpers; each later kind's text builder lives in its own feature file (`crash.go`, `runs.go`, `errwindow.go`), so no later task edits `format.go`.

  Tests (`statusSuite` in `status_test.go`, `formatSuite` in `format_test.go`):
  - success then failure keeps both; failure then success keeps both; `snapshot` copies cannot mutate the box;
  - `prefix("https://folio.example.com/")` → `https://folio.example.com`; `prefix("")` → `Folio`;
  - `oneLine` with `\n` and 400 characters → one line of 300 runes ending in `…`; multi-byte runes are not split;
  - `truncate` of a 5000-character message with 100 items → ≤ 4000 characters, the first two lines intact, ends with `… (truncated)`, no partial item line; a short message is unchanged;
  - `testText` matches the contract exactly.
- [x] T005 [US4] Add the `Service`, its bounded queue, the sender and its retry policy.
  - New file `internal/notify/notify.go`:
    - `type Service struct` holding the base logger, `*Store`, `*Client`, `cfg atomic.Pointer[Config]`, `statusBox`, the clock, `Timing`, the publicURL prefix, the queue and the lifecycle context;
    - `type Timing struct{ Spacing time.Duration /* 3s */; Backoff []time.Duration /* 2s, 4s: 3 attempts in total */; MaxRetryAfter time.Duration /* 10s */; MessageBudget time.Duration /* 30s */; ErrorWindow time.Duration /* 5m */; CrashWindow time.Duration /* 1h */; TickEvery time.Duration /* 30s */; StopDrain time.Duration /* 5s */; QueueSize int /* 32, batch lane */; UrgentSize int /* 8, crash lane */ }`, zero fields filled from the defaults;
    - `type Option func(*Service)`; `WithPublicURL(u string)`, `WithClock(now func() time.Time)`, `WithTiming(t Timing)`;
    - `func New(log *slog.Logger, store *Store, client *Client, opts ...Option) (*Service, error)`: loads `Config` once through `Store.Load`; fails only on a DB error;
    - `Start()`: launches the `sender` goroutine and a ticker loop calling `s.tick(s.now())` every `TickEvery` (`tick` is an empty hook in this phase; later tasks add windows to it);
    - `Stop()`: cancels the ticker, flushes open windows into the queue (none yet), closes intake and gives the sender up to `StopDrain` to finish, then drops the rest. Idempotent.
  - New file `internal/notify/queue.go`:
    - `type message struct{ kind Kind; text string }`; two lanes: `urgent chan message` (capacity `UrgentSize`, `KindCrash` only) and `batch chan message` (capacity `QueueSize`, every other kind);
    - `enqueue(m message)`: non-blocking send on the lane for `m.kind`. Per data-model.md: "A full lane drops the new message and records the failure status." The failure text is `queue full: N message(s) dropped`, with N cumulative since the last successful send;
    - `sender(ctx)`: for each message: (0) take from `urgent` whenever it is non-empty, else from `batch`; (1) read `s.cfg.Load()`; (2) drop the message if `!Enabled || !Ready()`; (3) `client.Send` with **3 attempts in total** (1 + `len(Backoff)`) inside `MessageBudget` — a retryable error waits `Backoff[i]`, a 429 waits `max(RetryAfter, Backoff[i])`; a `RetryAfter > MaxRetryAfter`, or any wait that would overrun `MessageBudget`, drops the message and records the failure; a non-retryable error stops retrying; each attempt's context deadline is the earlier of the 10 s client timeout and the end of `MessageBudget`; (4) record success or the final failure (the already token-free error text from `Send`); (5) wait `Spacing` before the next message. Waits honour `ctx`. No lock is held during I/O.
    - Each final failure is logged once at Warn on the base logger (`"telegram delivery failed"`, `kind`, `error`), never at Error and never notified (FR-011, US4-AS3).

  Tests (`queueSuite`, `internal/notify/queue_test.go`; fake endpoint counting requests; `Timing` with millisecond spacing/backoff; `WithClock` fixed time):
  - a 429 `retry_after: 0` twice then ok → 3 requests, status `LastSuccessAt` set;
  - 500 on every call → exactly 3 requests (3 attempts in total), then dropped, `LastFailureError` = Telegram's description;
  - 400 → exactly 1 request and the failure recorded;
  - 429 with `retry_after` above `MaxRetryAfter` → 1 request, dropped, failure recorded;
  - with a scaled `MessageBudget`, a 429 whose wait would overrun the remaining budget → dropped after that attempt, failure recorded, total time within the budget;
  - SC-002: with the batch lane full and one batch message in flight, a `KindCrash` enqueued next is the very next request after the in-flight one (before every remaining batch message), and its send time is at most `MessageBudget + Spacing` after enqueue (scaled timing);
  - two messages are at least `Spacing` apart (timestamps on the fake);
  - with the sender not started, `QueueSize + 3` batch enqueues → 3 dropped and `LastFailureError == "queue full: 3 message(s) dropped"`; `UrgentSize + 1` crash enqueues → 1 dropped; `enqueue` never blocks (bounded by a 100 ms test deadline);
  - a message queued while enabled is dropped when the config is switched off before it is sent (settings in force at send time);
  - `Stop()` returns within `StopDrain` while the fake endpoint hangs;
  - the Warn log for a failure (captured through a buffer logger) contains no token.
- [x] T006 [US1] Add the settings surface of the `Service` in `internal/notify/notify.go` (after T005; same file).
  - `TelegramSettings(ctx) (Settings, error)`: `view(*s.cfg.Load(), s.status.snapshot())`.
  - `UpdateTelegram(ctx, u Update) (Settings, error)`: `validate(u, current)`; on success `Store.Save`, then swap `s.cfg`. On a validation or store error nothing changes. When the result is off (or not ready), call `s.resetCollected()`, an unexported hook that clears in-memory collected state (empty in this phase; T014, T018 and T025 extend it) — FR-015: nothing is collected while off.
  - `SendTest(ctx) error`: synchronous, single attempt, `context.WithTimeout(ctx, 10*time.Second)`, with the **saved** config whether enabled or not; `ErrNotConfigured` when `!Ready()`; records success or failure in the status; does not go through the queue.
  - `DeliveryStatus() DeliveryStatus`.

  Tests (`serviceSuite`, `internal/notify/service_test.go`, real `Store` on a temp DB, fake endpoint):
  - `New` on an empty DB → off, `TokenSet == false`;
  - `UpdateTelegram` persists and a second `New` on the same DB reads it back;
  - an update without a token keeps the stored one (`Load` shows it unchanged);
  - an invalid update returns `*ValidationError` and the stored config is untouched;
  - `SendTest` without a saved token → `ErrNotConfigured` and no request;
  - `SendTest` with notifications off but configured → one request and `LastSuccessAt` set;
  - `SendTest` against `400` → `*APIError` and `LastFailureError` set;
  - no returned `Settings` or error string contains the token.
- [x] T007 [P] [US1] Add `TELEGRAM_API_BASE` to `internal/config/config.go`.
  - `TelegramAPIBase string \`env:"TELEGRAM_API_BASE" envDefault:"https://api.telegram.org"\``; `Validate` requires an absolute `http` or `https` URL with a host, and trims a trailing `/`. Its doc comment says it exists for tests and the local fake endpoint.
  - `.env.dist`: a commented `# TELEGRAM_API_BASE=https://api.telegram.org` with a one-line "testing only" note.
  - Doc: `docs/BUILD-AND-DEPLOY.md` environment variable table, if it lists variables (testing only).

  Tests (`validateSuite`): the default passes; `ftp://x`, `api.telegram.org` (no scheme) and `http://` (no host) fail; `http://127.0.0.1:9000/` is accepted and trimmed.
- [x] T008 [US1] Add the Telegram settings HTTP handler in new file `internal/settings/telegram.go`, per contracts/rest-api.md.
  - `type TelegramService interface{ TelegramSettings(ctx) (notify.Settings, error); UpdateTelegram(ctx, notify.Update) (notify.Settings, error); SendTest(ctx) error; DeliveryStatus() notify.DeliveryStatus }`; `type TelegramHandler struct`; `func NewTelegram(log *slog.Logger, svc TelegramService) *TelegramHandler`; `Register(r chi.Router)` mounts `GET /settings/telegram`, `PUT /settings/telegram`, `POST /settings/telegram/test`, `GET /settings/telegram/status`.
  - JSON views: `enabled`, `token_set`, `token_last4`, `chat_id`, `topic_id` (`*int64`, `null`), `silent`, `status{last_success_at, last_failure_at (RFC 3339 UTC or null), last_failure_error}`.
  - `PUT` body: `bot_token` absent, `null` or `""` → `Update.BotToken == nil`; `topic_id` absent or `null` → `nil`.
  - Error mapping: `errors.As(*notify.ValidationError)` → 400 `{"error":"<field>: <reason>"}` (or the FR-003 message); `errors.Is(context.DeadlineExceeded)` → 503 `{"error":"busy, try again"}`; `errors.Is(notify.ErrNotConfigured)` → 409 `{"error":"save a bot token and a channel ID first"}`; any other send error → 502 `{"error":"<description or redacted network error>","status":{…}}`; bad JSON → 400 `{"error":"invalid JSON body"}`; other store errors → 500 `failed to read telegram settings` / `failed to save telegram settings`. Test success → 200 `{"ok":true,"status":{…}}`.
  - Never log request bodies. A failed test send (502) is logged at **Warn** with only the (token-free) error, never at Error: from T026 on, this handler gets the tapped logger, and a delivery failure must not enter the error window (FR-011, US4-AS3). `ErrNotConfigured` and validation errors are not logged; store failures (500) log at Error with only the error.
  - `internal/settings/settings_test.go`: add `suite.Run(t, new(telegramSuite))` to `TestSettings`.
  - Doc: `docs/API.md`, a "Telegram notifications (`/api/settings/telegram`)" subsection under Settings with the four routes, bodies and status codes.

  Tests (`telegramSuite`, `internal/settings/telegram_test.go`, over a recording fake `TelegramService`, routed through `chi`):
  - `GET` returns the view with `token_set`/`token_last4` and never the token;
  - `PUT` without `bot_token`, with `null` and with `""` → the fake sees `BotToken == nil`; with a value → the fake sees it;
  - `PUT` where the fake returns the FR-003 `ValidationError` → 400 with that message; an invalid-token `ValidationError` → 400 `bot_token: …`; `context.DeadlineExceeded` → 503; bad JSON → 400;
  - `POST /test`: nil → 200 `ok:true` with status; `ErrNotConfigured` → 409; `&notify.APIError{Code:400, Description:"Bad Request: chat not found"}` → 502 with that text and a status object;
  - a failed test send (502) produces no Error-level record and exactly one Warn record in a buffer logger handed to `NewTelegram`;
  - `GET /status` returns only the status object;
  - with the fake holding the sentinel token, no response body of any route contains it (SC-006).
- [x] T009 [US1] Wire the notifier into `cmd/folio-idx/main.go` (after T006–T008).
  - After `db.Open` and `writeGuard := db.NewWriteGuard()`: `notifier, err := notify.New(log, notify.NewStore(database, writeGuard), notify.NewClient(cfg.TelegramAPIBase, &http.Client{Timeout: 10 * time.Second}), notify.WithPublicURL(cfg.PublicURL))`; on error, log and return the next unused exit code, 4 (codes 1–3 are in use).
  - In this phase the logger passed to `notify.New` is the same base logger every component uses (the tap arrives in T026).
  - Register `settings.NewTelegram(log, notifier)` in `server.Handlers.API` after `settings.New(log, authn)`.
  - `notifier.Start()` after the sync engine starts; `shutdown` calls `notifier.Stop()` after `syncEngine.Stop()`.
  - Compile-time guard: `var _ settings.TelegramService = (*notify.Service)(nil)`.
  - Docs:
    - `docs/BACKEND.md`: `notify/` in the Layout tree; the dependency table (`notify → db, db/dbq`; `settings → notify`); the composition order in "Composition root: `main.go`"; the new exit code; `notify` in "Coverage by package";
    - `docs/NETWORKING.md` (this task introduces the egress, so it carries the outbound-dependency wording):
      - a second entry under "Outbound Connections" — `api.telegram.org`, opt-in and off by default; what is sent (instance prefix, test text; later phases add their kinds) and the content rule of constitution v1.1.0: messages may carry library names, error texts including file paths inside a library, and book IDs or links to the instance; never book titles, book contents, credentials, tokens or headers; timeouts, 3 attempts within a 30 s budget, bounded queue; failure never breaks a request; how to block it;
      - rewrite the "Historically Folio made **no outbound network calls** … the one egress path" paragraph so it names both outbound dependencies (Google Books, Telegram);
      - rewrite "Security Considerations" item 6 "Minimal egress" ("The only outbound dependency is the Google Books API …") to name both and state the owner-notification exception;
      - under "Security Considerations": the token is stored as plain text in the data directory (clarified), never returned, never logged, redacted from errors (including Telegram's own error text), and validated so it cannot alter the request path.

  Tests: no new test file (main holds no logic); the full `task test` and `task build` stay green.
- [x] T010 [P] [US1] Add the SPA types and API calls, per contracts/spa-ui.md.
  - `web/src/types.ts`: `TelegramDeliveryStatus`, `TelegramSettings`, `TelegramSettingsUpdate` (with `bot_token?: string`).
  - `web/src/api.ts`: `fetchTelegramSettings()` → `GET /settings/telegram`; `updateTelegramSettings(u)` → `PUT /settings/telegram` (omit `bot_token` when absent or empty); `sendTelegramTest()` → `POST /settings/telegram/test`; `fetchTelegramStatus()` → `GET /settings/telegram/status`.

  Vitest (`web/src/api.spec.ts`): each call uses the right method and path; the `PUT` body omits `bot_token` when not provided and includes it when given; a 502 `{error}` from the test call rejects with Telegram's description as the message.
- [x] T011 [US1] Add `web/src/components/settings/TelegramSettingsForm.vue` (layout B), per contracts/spa-ui.md (after T010).
  - Card `card bg-base-200 max-w-xl gap-4 p-5`; elements and `data-testid`s exactly as the contract table: `tg-enabled`, `tg-token` (`type="password"`, `autocomplete="off"`, placeholder `Stored (ends with <last4>)` when `token_set`, else `Bot token from @BotFather`), `tg-chat` (placeholder `@channel or -100…`), `tg-topic`, `tg-silent` (`Send without sound`), `tg-save`, `tg-test`, `tg-status`, `tg-error`.
  - Switching on with no token (stored or typed) or no channel shows `A bot token and a channel ID are required.` and reverts the toggle, without a request (FR-003).
  - Save sends `{enabled, chat_id, topic_id, silent}` plus `bot_token` only when typed; re-renders from the response, clears the token field, toast `Telegram settings saved`; a 400 shows the server's error inline; other errors toast `Failed to save Telegram settings: <message>`.
  - `tg-test` is disabled while a request is in flight and when the **saved** settings lack a token or channel (`title` says to save first); success toast `Test message sent`; failure toast `Test message failed: <error>`; then `fetchTelegramStatus()` refreshes the line.
  - Status line (US4): no timestamps → `No messages sent since the server started.`; success only or newer → `Last delivered <local datetime>.`; failure newer or only → `Last delivery failed <local datetime>: <last_failure_error>` in warning colour, plus `Last delivered …` when a success exists.
  - Self-loads on mount like `OpdsSettingsForm`; load failure toasts `Failed to load Telegram settings: <message>`. The token value is never rendered.

  Vitest (`web/src/components/settings/TelegramSettingsForm.spec.ts`): every case listed in contracts/spa-ui.md "Vitest coverage" for this component — masked placeholder and no token value; save payload with and without `bot_token`; token cleared after save; switch-on refusal without request; server 400 inline; test button disabled for incomplete saved settings; test success and failure each update status line and toast; all three status-line variants.
- [x] T012 [US1] Place the form on the Settings page in `web/src/pages/SettingsPage.vue` (after T011).
  - The first tab keeps id `opds` (so `?tab=opds` keeps working); its label becomes `General`. Its content: heading `OPDS` + `<OpdsSettingsForm />`, then heading `Telegram notifications` + `<TelegramSettingsForm />` (FR-001).
  - Doc: `docs/FRONTEND.md` — the Settings page tabs (General / Libraries), the new component in the component list, and the Telegram section's behaviour (token write-only, test message, status line).

  Vitest (`web/src/pages/SettingsPage.spec.ts`): the first tab is labelled `General`; both forms render in it (stub the API calls); `?tab=opds` still selects it; the libraries tab is unaffected.

**Checkpoint**: Phase A is complete and shippable on its own. Run `task lint && task test && task build`, then quickstart §2 against the fake endpoint.

---

## Phase 2: User Story 2 + User Story 3 — Crashes, run outcomes, unreadable summary and mass removal (Priority: P1 / P2)

**Goal**: plan.md "Phase B". The four crash recovery points report to the notifier with one-hour de-duplication. Every sync and Deep Index run hands a `notify.RunOutcome` to the notifier, which keeps the per-library failing/recovered state, sends the unreadable summary and the mass-removal alert, and forgets purged libraries.

**Independent Test**: quickstart §3 (exactly one `failed` for three failing syncs, one `recovered`, a new `failed` after a restart, no `recovered` after a purge), §4 (one unreadable summary with 5 items and `…and 2 more`, none on re-run) and §5 (25/100 alerts, 19/100 and 25/300 do not). A forced handler panic yields one crash message; repeating it within the hour yields no more until the follow-up.

- [x] T013 [P] [US2] Add the "already reported" context mark in new file `internal/logging/reported.go` (moved forward from plan.md Phase C, because the Phase B crash and run-failure logs need it).
  - `func MarkReported(ctx context.Context) context.Context` and `func IsReported(ctx context.Context) bool`, using an unexported context key type. `MarkReported(nil)` is not supported; callers pass `context.Background()` when they have no context. No output change: the mark is invisible in log lines.

  Tests (`internal/logging/logging_test.go` already has `TestLogging` → `loggingSuite`; add a `reportedSuite` in `reported_test.go` and wire it with one `suite.Run` line): unmarked `context.Background()` → false; marked → true; a child `context.WithValue` of a marked context stays marked; a record logged with a marked context renders byte-identically to an unmarked one.
- [x] T014 [P] [US2] Add crash de-duplication and the crash messages in new file `internal/notify/crash.go`.
  - `ReportCrash(place, detail string, recovered any, stack []byte)`: returns at once when off or not ready (FR-015); never panics (it recovers its own formatting).
  - When `recovered` implements `interface{ CrashPlace() string; CrashStack() []byte }`, the place becomes `CrashPlace()`, the stack `CrashStack()`, and the caller's place and detail become `via` = `"<outer place> (<outer detail>)"`; the message's `Where:` reads `via <outer place> (<outer detail>)`.
  - `firstAppFrame(stack []byte) string`: first frame inside the module, skipping `runtime/`, `runtime/debug`, `internal/notify` and the recover helpers (`server.recoverer`, `sync.(*Engine).safeSync.func`, `ebook.(*Dispatcher).run.func`, `db.(*WriteGuard).WithTx.func`); renders `pkg.Func (path/file.go:line)` with a module-relative path; `""` when none.
  - `normalize(s string) string`: digit runs and `0x[0-9a-f]+` → `#`.
  - Key `place + "|" + frame + "|" + normalize(errorText)`; `crashes map[string]*crashEntry` with `crashEntry{first time.Time; place, via, detail, frame, text string; suppressed int}`, `text` capped at 300 characters. Data-model rules verbatim: "`crashes map[string]*crashEntry`, at most 256 entries"; "map full (256) ── new key ───▶ not stored, send crash message now (no de-dup)", and the overflow is recorded in the status.
  - New key → enqueue `KindCrash` at once (urgent lane) and open the entry; existing key within `CrashWindow` → `suppressed++` and send nothing.
  - `tick(now)`: for each entry with `now ≥ first + CrashWindow`, enqueue one `KindCrashRepeat` when `suppressed > 0`, then delete it either way.
  - `resetCollected` clears `crashes`.
  - `crash.go` (not `format.go`): `crashText(...)` and `crashRepeatText(...)` exactly as contracts/messages.md (`At:` omitted when no frame; panic text through `oneLine(…, 300)`; whole message through `truncate`).

  Tests (`crashSuite`, `internal/notify/crash_test.go`, sender not started so the queue can be inspected; fixed clock advanced by the test):
  - first crash → one `KindCrash` whose text matches the contract; 50 identical crashes within the hour → still one message; `tick` at +1h → one `repeated 49 more times`; `tick` again → nothing (SC-002: at most 2);
  - a single crash then `tick` at +1h → no follow-up;
  - `index out of range [7] with length 3` and `[9] with length 5` share a key; the same text from different frames does not;
  - a fake `TxPanic`-like value → place `database write path`, `Where: via web request handling (GET /api/books/{id})`;
  - with 256 live keys, a 257th distinct crash is sent and not stored, and the overflow shows in the status;
  - off → nothing enqueued, nothing stored; switching off clears the map;
  - `firstAppFrame` over a captured `debug.Stack()` from a helper in this package's test returns a module-relative frame and skips the runtime frames;
  - a crash message is enqueued on the urgent lane, a repeat follow-up on the batch lane.
- [x] T015 [P] [US2] Wrap write-path panics in `internal/db/writeguard.go`.
  - `type TxPanic struct{ Value any; Stack []byte }` with `Error() string` (`"panic in write transaction: <value>"`), `Unwrap() error` (the value when it is an `error`, else nil), `CrashPlace() string` (`"database write path"`), `CrashStack() []byte`.
  - The `WithTx` recover rolls back and re-panics with `&TxPanic{Value: p, Stack: debug.Stack()}`; a value that is already a `*TxPanic` is re-panicked as is. No reporter dependency in `db`.
  - Doc: `docs/DATABASE.md`, the write-guard section notes the `*TxPanic` re-panic and why (the outer recover reports it once).

  Tests (`writeGuardSuite`): `TestPanicRollsBackAndWraps` — a panic inside `fn` after an insert leaves no row, the recovered value is a `*TxPanic` with the original value, a non-empty stack and `CrashPlace() == "database write path"`; a nested `WithTx` panic is wrapped once; `errors.Is(p, http.ErrAbortHandler)` holds when the original value is `http.ErrAbortHandler`; the guard is released after the panic.
- [x] T016 [P] [US2] Report parser crashes from `internal/ebook/dispatch.go`.
  - `type CrashReporter interface{ ReportCrash(place, detail string, recovered any, stack []byte) }`; `type Option func(*Dispatcher)`; `func WithCrashReporter(r CrashReporter) Option`; `NewStandardDispatcher(opts ...Option) *Dispatcher` (`NewDispatcher(parsers ...Parser)` unchanged; test fakes get no reporter). The existing `cmd/folio-idx/main.go` call compiles unchanged.
  - In `run`'s recover: `stack := debug.Stack()`, `err = &PanicError{Value: r}`, then, when a reporter is set, `ReportCrash("book-file reading", format+" parser, "+what+" read", r, stack)`. `src.Name` (a file name, catalog content) is never passed.
  - Doc: `docs/EBOOK-PARSING.md`, the Dispatcher section notes the optional crash reporter, that the reporter receives no file name, and that the book itself is listed once in the run's unreadable summary as `crashed`.

  Tests (`ebookSuite` / `dispatchSuite`): `TestPanicReportsCrash` — a panicking fake parser under a recording reporter gives `place == "book-file reading"`, a detail containing the format and `metadata read`/`cover read` but not the source name, a non-empty stack, and the caller still gets `*PanicError`; `TestNoReporterStillRecovers`; the existing `TestPublicSurfaceFormatAgnostic` still passes with the new names.
- [x] T017 [US2] Replace chi's `middleware.Recoverer` with a slog-aware recoverer (after T013, T015).
  - New file `internal/server/recoverer.go`: `type CrashReporter interface{ ReportCrash(place, detail string, recovered any, stack []byte) }`; `func recoverer(log *slog.Logger, crashes CrashReporter) func(http.Handler) http.Handler`. On panic:
    - when the value is, or (through `errors.Is` and `TxPanic.Unwrap`) wraps, `http.ErrAbortHandler` → re-panic the original value unchanged;
    - otherwise `log.ErrorContext(logging.MarkReported(r.Context()), "http handler panicked", method, route pattern from `chi.RouteContext`, panic, stack)`; call `crashes.ReportCrash("web request handling", "<METHOD> <route pattern>", v, debug.Stack())` when non-nil (never the raw path or query); write 500 unless the request is a connection upgrade.
    - It does not wrap the `ResponseWriter` (SSE flushing unaffected).
  - `internal/server/server.go`: `Handlers` gains `Crashes CrashReporter // nil = log only`; `r.Use(recoverer(log, h.Crashes))` replaces `r.Use(middleware.Recoverer)` in the same position.
  - Docs: `docs/BACKEND.md` middleware table (`recoverer` replaces `middleware.Recoverer`: slog logging, crash report, 500); `docs/SYNC-EVENTS.md` the middleware chain line (`proxyHeaders → (dev) Logger → recoverer`).

  Tests (`serverTestSuite`, `internal/server/server_test.go`, with a test route registered through a fake `Registrar`):
  - `TestRecovererReportsAnd500`: a panicking `GET /api/boom/{id}` → 500, one report with place `web request handling` and detail `GET /api/boom/{id}` (the pattern, not `/api/boom/7?q=x`), and one Error log line;
  - `TestRecovererReportsTxPanicPlace`: a handler panicking with `&db.TxPanic{…}` → the reporter receives the `*TxPanic` value unchanged;
  - `TestRecovererReraisesAbortHandler`: `panic(http.ErrAbortHandler)` propagates out of the middleware and nothing is reported;
  - with `Crashes == nil` a panic still gives 500;
  - the SSE suite still passes.
- [x] T018 [US2] Add run outcomes and the failing/recovered state machine in new file `internal/notify/runs.go` (after T014: both extend `resetCollected` and the `Service` fields in `notify.go`).
  - Types per data-model.md "Outcome types": `RunKind` (`RunSync`, `RunDeepIndex`, `String()` → `sync` / `deep index`), `RunStatus` (`RunFailed`, `RunCompleted`), `UnreadableBook{BookID int64; Reason string}`, `RunOutcome{LibraryID; Library; Kind; Status; Reason; CrashReported; PerBookErrors; FillFailed; NewlyUnreadable; UnreadableSample; BooksBefore; BooksRemoved}`.
  - `RunFinished(o RunOutcome)`: returns at once when off; applies the data-model transitions verbatim:
    - `HEALTHY ── RunFailed (CrashReported=false) ──▶ FAILING`: send "failed";
    - `HEALTHY ── RunFailed (CrashReported=true) ──▶ FAILING`: send nothing (crash already sent);
    - `FAILING ── RunFailed (any) ──▶ FAILING`: send nothing;
    - `FAILING ── RunCompleted ──▶ HEALTHY`: send "recovered";
    - `HEALTHY ── RunCompleted ──▶ HEALTHY`: send nothing (for the state).
  - `LibraryRemoved(id int64)`: `FAILING ── LibraryRemoved ──▶ (deleted)`, send nothing.
  - `resetCollected` clears `failing` ("any ── notifications switched off ▶ (cleared)").
  - `runs.go` (not `format.go`): `runFailedText` (`Library "<library>": <kind> failed` / `Reason: <error text>` / `No further messages for this library until a run succeeds.`) and `runRecoveredText` (`Library "<library>": recovered` / `The last <kind> completed successfully.`), reason through `oneLine(…, 300)`.

  Tests (`runsSuite`, `internal/notify/runs_test.go`):
  - SC-003 for N = 1, 3 and 10 failed runs then one success → exactly 2 messages, texts per contract;
  - a crash-reported failure then success → only `recovered`;
  - failure then `LibraryRemoved` then success for the same id → `failed` only;
  - a failed sync then a completed deep index for the same library → `recovered` naming `deep index` (per-library state, R6);
  - a completed run with `PerBookErrors > 0` counts as success (no `failed`, `recovered` after a failure);
  - off → nothing enqueued and no state; switching off clears the state;
  - two libraries keep independent state.
- [x] T019 [US3] Add the unreadable summary and the mass-removal alert to `internal/notify/runs.go` (after T018; same file).
  - On every `RunCompleted`, independently of the transition (data-model.md):
    - "unreadable summary: `NewlyUnreadable > 0`" → one `KindUnreadable`: `Library "<library>": <kind> found <N> newly unreadable book file(s)`, at most 5 `- <book-ref>: <reason>` lines from `UnreadableSample`, and `…and <N-5> more. They will not be reported again unless the files change.` only when N > 5. `<book-ref>` is `<PUBLIC_URL>/books/<id>` when a public URL is set, else `book #<id>`. Reason labels from the contracts/messages.md table (`corrupt`, `missing_entry`, `too_large`, `crashed`; any other value raw).
    - "mass removal: sync only, `BooksRemoved >= 20 && BooksRemoved*10 >= BooksBefore && BooksBefore > 0`" → one `KindMassRemoval`: `Library "<library>": sync removed <N> of <M> books (<P>%)` with `<P>` rounded down, and `The removal has been applied. If this was not expected, check that the library source is complete and mounted.`
  - Double-reporting rule (plan.md): no `crashed` filtering; a book whose read crashed is listed once here with the `crashed` label, while the crash itself went out once as a crash message.
  - Doc: `docs/NETWORKING.md` "what is sent" adds crash, failed/recovered, unreadable and mass-removal messages (library names, run kinds, counts, error/panic texts capped at 300 characters — which may include file paths inside a library — one code frame, book IDs or links, reason labels) and restates what is never sent (book titles, authors, descriptions, book contents, per-book read detail, raw request paths or queries, credentials, the token).

  Tests (`runsSuite`):
  - SC-004: `NewlyUnreadable: 7` with 5 samples → exactly one message, 5 item lines and `…and 2 more`; `NewlyUnreadable: 3` → 3 lines, no `…and`; `NewlyUnreadable: 0` → no message;
  - book refs with and without `WithPublicURL("https://folio.example.com")`; every reason label; an unknown reason rendered raw;
  - mass-removal thresholds: 19/100 no, 20/200 yes, 20/201 no, 25/100 yes (`25 of 100 books (25%)`), 25/300 no, `BooksBefore == 0` no, a deep index run with removals no;
  - a `RunCompleted` after a failure with `NewlyUnreadable > 0` → `recovered` and the summary (two messages);
  - `ReportCrash("book-file reading", …)` followed by a `RunCompleted` whose sample holds that book with reason `crashed` → exactly one `KindCrash` and one `KindUnreadable` listing the book once; nothing enters the error window.
- [x] T020 [P] [US3] Expose removed books and the newly-unreadable sample from `internal/ingest`.
  - `internal/ingest/reconcile.go`: `prune` returns `(filesRemoved, booksRemoved int, err error)`; `booksRemoved` is the number of empty books it removed (`len` of the `ListEmptyBooksByLibrary` ids).
  - `internal/ingest/ingest.go`: `Result` gains `RemovedBooks int` (doc comment: books, whereas `Removed` stays files); `internal/ingest/driver.go` sets it. The `"sync done"` log is unchanged.
  - `internal/ingest/deepindex.go`: `type BrokenBook struct{ BookID int64; Reason ebook.Reason }`; `DeepIndexResult` gains `NewlyBroken []BrokenBook` (first `brokenSampleSize = 5` across batches); `commit` records the book and reason where it increments `tally.Broken`, and `Run` merges up to 5. `Broken` stays the total.
  - Callers: `internal/ingest/importer_idmatch_test.go` (`_, _, err = rc.prune(ctx)`), and every exact `DeepIndexResult{…}` assertion in `deepindex_test.go` (`NewlyBroken` compared separately or set).

  Tests: `reconcileReportSuite` (or `ingestSuite`) — a folder sync where one book with two files loses both and another loses one of two → `Removed == 3`, `RemovedBooks == 1`; `deepIndexSuite` — a run over 7 newly broken books gives `Broken == 7`, `len(NewlyBroken) == 5` with their reasons; a re-run gives `Broken == 0` and an empty sample.
- [x] T021 [US2] Report crashes and failed/recovered outcomes from `internal/sync` (after T013, T018).
  - `internal/sync/engine.go`: `type Notifier interface{ ReportCrash(place, detail string, recovered any, stack []byte); RunFinished(o notify.RunOutcome); LibraryRemoved(id int64) }`; `func WithNotifier(n Notifier) Option`; unexported `e.notifyCrash`, `e.notifyRun`, `e.notifyLibraryRemoved` helpers that are no-ops for a nil notifier.
  - `safeSync` recover: `e.log.ErrorContext(logging.MarkReported(context.Background()), "sync panicked", library, panic, slog.String("stack", string(stack)))`; `ReportCrash("sync engine", "library \"<name>\" <sync|deep index>", r, stack)` with the name from a best-effort `GetLibrary` (fallback `#<id>`) and the kind from `req.kind`; then `RunFinished{Status: RunFailed, CrashReported: true, Reason: <panic text>}`; `markError` as today.
  - `runSync`: a non-cancel parser error → `e.log.ErrorContext(logging.MarkReported(ctx), "sync failed", …)` and `RunFinished{Kind: RunSync, Status: RunFailed, Reason: err.Error()}`.
  - `guardedSync`: "no parser for library type" → `RunFinished{Kind: RunSync, Status: RunFailed, Reason: …}`; a checkpoint skip → `RunFinished{Kind: RunSync, Status: RunCompleted}` (zero counts).
  - `syncLibrary`: a successful sync → one `RunFinished{Kind: RunSync, Status: RunCompleted}` (counts arrive in T022).
  - `deepIndexLibrary`: a non-cancel error → `ErrorContext(logging.MarkReported(ctx), "deep index failed", …)` and `RunFinished{Kind: RunDeepIndex, Status: RunFailed, Reason: err.Error()}`; success → `RunCompleted` (counts in T022).
  - Shutdown cancellation, a missing library and a `pending_purge` skip report nothing. `markError`'s own `"record sync error"` stays unmarked (a separate DB fault).
  - The on-view backfill (`internal/ingest/backfill.go`, `recordBroken`) is unchanged and reports nothing: unreadable books recorded outside runs are out of scope (spec Assumptions).
  - `internal/sync/purge.go`: after a successful `purgeLibrary` (reached from `checkPurge`, `RequestPurge` and `PurgeLibrary`) call `e.notifyLibraryRemoved(id)`.
  - Doc: `docs/BACKEND.md` dependency table (`sync → notify, logging`).

  Tests (`engineSuite`, recording fake `Notifier`, existing fake parsers):
  - a parser error → one `RunFailed` sync outcome with the reason; a `context.Canceled` error → nothing;
  - success → one `RunCompleted`; checkpoint skip → one `RunCompleted` with zero counts;
  - an unknown library type → `RunFailed`;
  - a panicking parser → one `ReportCrash` with place `sync engine` and a detail naming the library and `sync`, then `RunFailed` with `CrashReported: true`;
  - deep index failure and success outcomes; a deep index of a `pending_purge` library → nothing;
  - a completed purge → `LibraryRemoved(id)`;
  - a nil notifier changes nothing (existing tests unchanged).
- [x] T022 [US3] Fill the sync and Deep Index outcomes with counts in `internal/sync/engine.go` (after T020, T021).
  - `guardedSync` reads `CountBooksByLibrary` (existing sqlc query) under the guard before `parser.Sync` → `BooksBefore`; `runSync` passes `res.RemovedBooks` → `BooksRemoved`.
  - `runDeepIndex` returns `(ingest.DeepIndexResult, error)`; `fillImported` returns the fill's result and error. `syncLibrary` merges them into the single sync outcome (`PerBookErrors: res.Errors`, `NewlyUnreadable: res.Broken`, `UnreadableSample` mapped from `NewlyBroken` with `string(Reason)`), so one sync gives one summary. A non-cancel fill failure sets `FillFailed: err.Error()` and stays a `RunCompleted` (the sync result stands; its error-window line arrives in T026).
  - `deepIndexLibrary` success → `RunCompleted{Kind: RunDeepIndex, PerBookErrors: res.Errors, NewlyUnreadable: res.Broken, UnreadableSample: …}`.

  Tests (`engineSuite`, with a fake `DeepIndexer` returning a scripted result):
  - a sync that removes books reports `BooksBefore` (counted before the run) and `BooksRemoved`;
  - a sync with a fill that returns `Broken: 7` and 5 `NewlyBroken` → one outcome carrying both, mapped reasons;
  - a fill failure → `RunCompleted` with `FillFailed` set, no `RunFailed`;
  - a deep index with `Errors: 3` → `PerBookErrors: 3`.
- [x] T023 [US2] Wire the producers in `cmd/folio-idx/main.go` (after T016, T017, T021, T022).
  - `ebook.NewStandardDispatcher(ebook.WithCrashReporter(notifier))`; `sync.WithNotifier(notifier)` in `sync.New`; `server.Handlers{…, Crashes: notifier}`. The notifier is built before the dispatcher (reorder construction if needed; main stays free of logic).
  - Compile-time guards: `_ ebook.CrashReporter`, `_ server.CrashReporter`, `_ sync.Notifier` = `(*notify.Service)(nil)`.
  - Doc: `docs/BACKEND.md` composition-root description (construction order: base logger → db → notify → dispatcher/engine/server) and the dependency table (`ebook`, `db` and `logging` import nothing new).

  Tests: `task test` and `task build` green; the compile-time guards are the check.

**Checkpoint**: Phase B is complete. Run `task lint && task test && task build`, then quickstart §3, §4 and §5 against the fake endpoint.

---

## Phase 3: User Story 3 — Error window for other errors (Priority: P2)

**Goal**: plan.md "Phase C". Error-level log records that are not already reported are grouped into one message per 5-minute window, together with one line per completed run that left books for retry or whose post-sync fill failed. Online-enrichment misses stop being Errors.

**Independent Test**: quickstart §6: several Error logs outside runs give one `<N> error(s) in the last 5 minutes` message with at most 5 lines; an empty window sends nothing; a Deep Index with books left for retry adds one line and no `failed`; the `sync failed` lines of §3 never appear in a summary.

- [ ] T024 [P] [US3] Add the error tap in new file `internal/logging/sink.go`.
  - `type ErrorSink interface{ LogError(ctx context.Context, r slog.Record) }`.
  - `func WithErrorSink(base *slog.Logger, sink ErrorSink) *slog.Logger`: wraps `base.Handler()` in a `sinkHandler` that shares the same `customHandler` (its mutex and output), so lines never interleave and output is byte-identical.
  - `sinkHandler.Handle`: (1) call the inner handler; (2) when `r.Level >= slog.LevelError && !IsReported(ctx)`, pass `r.Clone()` with the attrs baked in through `WithAttrs` added to it. `WithAttrs` and `WithGroup` return new `sinkHandler`s that track them. `Enabled` delegates.
  - Doc: `docs/BACKEND.md`, a "Logging tap" paragraph and the contributor rule: a new Error log on a path that already notifies (a run failure, a crash) must use `logging.MarkReported(ctx)`.

  Tests (`sinkSuite` in `internal/logging/sink_test.go`, wired into the existing `TestLogging`): Error is forwarded and Warn/Info are not; a `MarkReported` context is not forwarded; `log.With("error", e).Error("x")` forwards a record carrying the `error` attr; the buffer output with and without the sink is identical for the same records; concurrent logging under `-race`.
- [ ] T025 [P] [US3] Add the 5-minute error window in new file `internal/notify/errwindow.go` (after T019; it also edits `runs.go` and `notify.go`).
  - `LogError(ctx, r slog.Record)`: returns at once when off. Builds one line `"HH:MM:SS <message>"` plus `": <error>"` when an attr named `error` exists (searching baked attrs and record attrs), through `oneLine(…, 300)`, with the time in the server's local zone. The error text is used as is: it may contain file paths inside a library (constitution v1.1.0; no stripping). No other attr is used.
  - `errorWindow` per data-model.md: `open bool`, `deadline time.Time // first entry time + 5 min`, `count int // all entries, incl. run lines`, `samples []string // first 5 lines`. Transitions verbatim: `CLOSED ── LogError / run line ─▶ OPEN{deadline=now+5m, count=1, samples=[line]}`; `OPEN ── LogError / run line ─▶ OPEN{count+1, samples append while <5}`; `OPEN ── tick, now ≥ deadline ─▶ CLOSED`, send one summary; `OPEN ── Stop() ─▶ CLOSED`, flush the summary into the queue; `any ── switched off ─▶ CLOSED (discarded)`.
  - `addRunErrors(o RunOutcome)` called from `RunFinished` on `RunCompleted`: one line per outcome with `PerBookErrors > 0` (`Library "<library>": <kind> completed with <K> book(s) left for retry`) and one with `FillFailed != ""` (`Library "<library>": sync completed, but filling descriptions for new books failed: <error text>`) — FR-010.
  - `errwindow.go` (not `format.go`) holds the summary text builder. `tick(now)` closes an expired window into one `KindErrorSummary`: `<N> error(s) in the last 5 minutes`, the samples as `- ` lines, and `…and <N-5> more. See the server log for details.` when N > 5. An empty window sends nothing. `Stop()` flushes an open window. `resetCollected` discards it.
  - Doc: `docs/NETWORKING.md` — what the error summary may contain (log message and its `error` text, which may include file paths inside a library; run lines) and what it never contains (book titles, book contents, credentials, the token).

  Tests (`errWindowSuite`, `internal/notify/errwindow_test.go`, fixed clock):
  - 12 records within 5 minutes → nothing before the deadline; `tick` at the deadline → one message with `12 error(s)`, 5 lines and `…and 7 more`;
  - a window with no entries → no message; a second window after the first closes is independent;
  - only the message and the `error` attr are used (a separate `path` attr is not); a `*fs.PathError` error keeps its path in the line (no stripping); a 400-character error is capped;
  - a run outcome with `PerBookErrors: 3` adds the retry line and counts toward N; `FillFailed` adds its line; `PerBookErrors: 0` adds nothing;
  - `Stop()` with an open window enqueues its summary;
  - off → `LogError` stores nothing; switching off discards an open window;
  - a summary with 5 long samples stays within 4000 characters (`truncate`).
- [ ] T026 [US3] Tap the application logger, and stop online-enrichment misses from counting as errors (after T024, T025).
  - `cmd/folio-idx/main.go`: `log := logging.WithErrorSink(base, notifier)` is the logger for every component except `db.Open` (startup only) and `notify.New` (which keeps `base`, so the notifier's own records can never loop back). Compile-time guard `_ logging.ErrorSink = (*notify.Service)(nil)`.
  - Audit Error logs on paths that already notify: `"sync failed"`, `"deep index failed"`, `"sync panicked"` and `"http handler panicked"` carry `MarkReported` (T017, T021); `"record sync error"` stays unmarked; `fillImported`'s `"post-sync fill failed"` stays at Warn (its line comes through `FillFailed`); the Telegram settings handler's failed test send stays at Warn (T008), so `settings.NewTelegram(log, notifier)` may take the tapped logger.
  - FR-011 / FR-017: `internal/googlebooks/logger.go` transport failure `t.log.Error` → `t.log.Warn`; `internal/api/match.go` `"match search"` `h.log.Error` → `h.log.Warn`. Responses, retries and status codes are unchanged.
  - Docs: `docs/BACKEND.md` composition order (base logger → db → notify → tapped logger → everything else); `docs/NETWORKING.md` Google Books "Failure mode" notes these misses are logged at Warn.

  Tests: `transportSuite` (`internal/googlebooks`) and `matchSuite` (`internal/api`) assert the Warn level if they assert levels today, otherwise add one assertion each through a buffer logger; behaviour assertions unchanged. `task test` green with the tap in place.

**Checkpoint**: Phase C is complete. Run `task lint && task test && task build`, then quickstart §6 against the fake endpoint.

---

## Phase 4: Polish & Cross-Cutting Concerns

**Purpose**: plan.md "Phase D". Leak sweep, residual docs, gates, end-to-end validation, the SC-005 measurement and the Principle III (v1.1.0) content check.

- [ ] T027 [P] Add the token-leak sweep in new file `internal/notify/leak_test.go` (`leakSuite`, wired in `TestNotify`). With the sentinel token stored and `WithPublicURL` set, drive every message kind — test, crash, crash repeat, run failed, run recovered, unreadable summary, mass removal, error summary — through a fake endpoint that also fails once with a transport error and once with a 400 whose description echoes the request path. Assert that no sent `text`, no `DeliveryStatus.LastFailureError`, no `Settings` view (`%+v` and JSON) and no line of the base logger's buffer contains the token (SC-006). The settings-handler side is already covered by T008.
- [ ] T028 [P] Run the residual doc cross-check: grep `docs/`, `README.md` and `.claude/CLAUDE.md` for `middleware.Recoverer`, the Settings tab label `OPDS`, `NewStandardDispatcher()` without options and the old outbound-dependency sentences ("the one egress path", "The only outbound dependency is the Google Books API"), which T009 rewrote, and fix stale mentions; update `docs/ARCHITECTURE.md` if its directory tree or flows should show `internal/notify`. No behaviour docs are written here.
- [ ] T029 Run `task format`, `task lint`, `task test` and `task build`, and fix any findings.
- [ ] T030 Walk quickstart §§2–7 end to end against the local fake Telegram endpoint (the scratch script from quickstart.md, kept outside the repository; scratch `DATA_DIR` and scratch folder libraries only), once with `PUBLIC_URL=https://folio.example.com` and once without, to see both the URL prefix and the `Folio` label. Confirm with `grep -c` that the server log never contains the token. §8 (a real bot and channel) is the owner's manual check and is not part of this task; nothing from it is recorded.
- [ ] T031 Run the quickstart §9 SC-005 measurement (notifications off versus on with the fake endpoint down): three timed forced re-indexes and Deep Index runs, and 200 `GET /api/books?limit=50` requests each way. Record the medians and the pass/fail against 5% in the PR description, without host, path or library details.
- [ ] T032 Check the shipped feature against the constitution v1.1.0 Principle III exception in `internal/notify/` and `docs/NETWORKING.md`: no message builder reads a book title, author, description or book content; error texts are passed through unstripped (paths allowed); `docs/NETWORKING.md` "Outbound Connections" and "Security Considerations" item 6 state the exception in the same words as the constitution (written by T009, T019, T025). Record the result in one line of the PR description. The constitution itself is not edited here (amended to v1.1.0 before Phase 1).
---

## Dependencies & Execution Order

### Phase Dependencies

- **Gate before Phase 1**: constitution v1.1.0 (Principle III owner-notification exception) is committed. It is already amended in the tree.
- **Phase 1 (US1 + US4, plan Phase A)**: no other prerequisites. Blocks everything else, because every producer feeds the `Service` it creates.
- **Phase 2 (US2 + US3, plan Phase B)**: depends on Phase 1.
- **Phase 3 (US3, plan Phase C)**: depends on Phase 2 (the run-failure and crash logs must already carry `MarkReported`, and run outcomes must exist before run lines can enter the window).
- **Phase 4 (Polish, plan Phase D)**: depends on Phases 1–3.

### Within Phase 1

- T001 first. T002, T003, T004 and T007 need only T001 (T004 and T007 need nothing) and run in parallel.
- T005 needs T002, T003, T004. T006 needs T005 (same file).
- T008 needs T006 (it uses the `notify` types and errors). T009 needs T006–T008.
- T010 needs nothing on the backend (it follows the contract) and runs in parallel with T001–T009. T011 needs T010; T012 needs T011.

### Within Phase 2

- T013, T014, T015, T016 and T020 are independent of each other (T014 needs Phase 1).
- T017 needs T013 and T015. T018 needs T014 (both edit `notify.go`: `Service` fields and `resetCollected`); T019 needs T018 (same file).
- T020 is independent of the `notify` and `sync` work.
- T021 needs T013 and T018. T022 needs T020 and T021 (same file). T023 needs T016, T017, T021, T022.

### Within Phase 3

- T024 and T025 are independent of each other (T025 needs T019, since it edits `runs.go` and `notify.go`). T026 needs both.

### Within Phase 4

- T027 and T028 run in parallel; T029 after them; T030 and T031 need a build from T029; T032 after T028.

### User Story Dependencies

- **US1 (P1)**: Phase 1 only. Independently shippable (MVP).
- **US4 (P3)**: its delivery path and status line ship in Phase 1 with US1; quickstart §7's run-triggered checks need US2's producers.
- **US2 (P1)**: needs Phase 1 (the notifier and settings to switch it on).
- **US3 (P2)**: the unreadable summary and mass removal need US2's run outcomes (T018, T021); the error window needs T013 and the outcomes.

## Parallel Opportunities

- Phase 1: T002, T003, T004, T007 after T001; T010 alongside any backend task.
- Phase 2: T013, T014, T015, T016 and T020 together; then T018 → T019 in sequence (shared `notify.go` / `runs.go`), alongside T017.
- Phase 3: T024 alongside T025.
- Phase 4: T027 alongside T028.

## Parallel Example: Phase 1 (US1 + US4)

```bash
# After T001 lands:
Task: "T002 Settings store in internal/notify/store.go (storeSuite)"
Task: "T003 Telegram client in internal/notify/telegram.go (clientSuite)"
Task: "T004 DeliveryStatus + format helpers in internal/notify/status.go, format.go"
Task: "T007 TELEGRAM_API_BASE in internal/config/config.go (validateSuite)"
Task: "T010 SPA types and API calls in web/src/types.ts, web/src/api.ts (api.spec.ts)"
```

## Parallel Example: Phase 2 (US2 + US3)

```bash
Task: "T013 MarkReported/IsReported in internal/logging/reported.go"
Task: "T015 TxPanic in internal/db/writeguard.go (writeGuardSuite)"
Task: "T016 CrashReporter in internal/ebook/dispatch.go (ebookSuite)"
Task: "T020 RemovedBooks / NewlyBroken in internal/ingest (reconcileReportSuite, deepIndexSuite)"
Task: "T014 Crash de-dup in internal/notify/crash.go (crashSuite)"
# then T017 recoverer.go alongside T018 → T019 (sequential: shared internal/notify/notify.go, runs.go)
```

## Parallel Example: Phase 3 (US3)

```bash
Task: "T024 Error tap in internal/logging/sink.go (sinkSuite)"
Task: "T025 Error window in internal/notify/errwindow.go (errWindowSuite)"
```

## Implementation Strategy

1. **MVP**: Phase 1 (US1 + the US4 delivery path). The owner configures the channel, proves delivery with a test message and sees the status line; nothing else notifies yet. Ships on its own with its docs.
2. **The failures that matter**: Phase 2 (US2, and US3's unreadable summary and mass-removal alert). Crashes and failing libraries reach the owner, without floods.
3. **Everything else, grouped**: Phase 3 (US3's error window). Other errors arrive as one message per 5 minutes.
4. **Polish**: Phase 4. Leak sweep, residual docs, gates, the quickstart walk against the fake endpoint, the SC-005 measurement and the Principle III (v1.1.0) content check.

Phase headings follow `## Phase N: …` for the beads import; each phase is one feature commit and leaves `task build`, `task test` and `task lint` green.

## Notes

- [P] tasks touch different files and depend on no incomplete task; the only shared file among [P] `notify` tasks is the runner's `suite.Run` list. `internal/notify/format.go` is edited only by T004. `internal/notify/notify.go` is shared by T005, T006, T014, T018 and T025, which therefore run in sequence.
- Every task writes its tests first and watches them fail before the implementation.
- Never paste a real token, chat ID, host or library name into code, tests, logs, commits or the PR.

# Contract: REST/JSON, Telegram settings

All routes are new and additive, mounted under `/api` by `settings.TelegramHandler.Register`. The existing `GET/PUT /api/settings` (OPDS) is unchanged.

The routes inherit the `/api` group middleware:
- the external authenticator;
- `sameSiteGuard` and `formBodyGuard` on `PUT`/`POST`.

The JSON bodies use `Content-Type: application/json`. The test `POST` has no body, so `formBodyGuard` lets it through.

Errors use the existing shape `{"error": "<message>"}`. No response ever contains the bot token.

## `GET /api/settings/telegram`

Returns the redacted settings and the delivery status.

```json
{
  "enabled": true,
  "token_set": true,
  "token_last4": "<last4>",
  "chat_id": "<channel-id>",
  "topic_id": 42,
  "silent": false,
  "status": {
    "last_success_at": "2026-11-03T10:15:02Z",
    "last_failure_at": null,
    "last_failure_error": ""
  }
}
```

- `token_last4`: the last 4 characters of the stored token. It is `""` when no token is set or the token is shorter than 8 characters, so a short token is never shown nearly in full.
- `topic_id`: integer or `null`.
- `status.*_at`: RFC 3339 UTC, or `null` when nothing has happened since the server started.
- 500 `{"error":"failed to read telegram settings"}` on a DB error.

## `PUT /api/settings/telegram`

The request replaces the non-secret fields. The token is write-only.

```json
{
  "enabled": true,
  "bot_token": "<bot-token>",
  "chat_id": "<channel-id>",
  "topic_id": 42,
  "silent": true
}
```

| Field | Required | Semantics |
|---|---|---|
| `enabled` | yes | On/off. `true` requires a token (stored or in this request) and a `chat_id`. |
| `bot_token` | no | Absent, `null` or `""` keeps the stored token. Any other value replaces it after validation. |
| `chat_id` | yes (may be `""` while `enabled` is false) | `@channelname` or a signed integer ID. |
| `topic_id` | no | Positive integer; absent or `null` means no topic. |
| `silent` | yes | Sends with `disable_notification`. |

Responses:
- 200: the same body as `GET` (redacted).
- 400 `{"error":"<field>: <reason>"}` for an invalid token format, chat ID or topic. Nothing is written.
- 400 `{"error":"notifications need a bot token and a channel ID"}` for `enabled: true` without a token or chat (FR-003). Nothing is written.
- 400 `{"error":"invalid JSON body"}`.
- 503 `{"error":"busy, try again"}` when the write guard could not be taken within 2 s (an indexing run is writing).
- 500 `{"error":"failed to save telegram settings"}`.

Side effects:
- The notifier's active configuration is swapped at once.
- Switching off clears the pending error window, the crash de-dup and the library failing state. Messages already queued are dropped by the sender at send time.

## `POST /api/settings/telegram/test`

Sends one test message synchronously with the **saved** settings (FR-004). It is a single attempt with a 10 s timeout and no retry. It works whether notifications are on or off, so the owner can verify before switching on. The status is updated either way.

Responses:
- 200 `{"ok": true, "status": { … }}`.
- 409 `{"error":"save a bot token and a channel ID first"}` when the saved token or chat is missing.
- 502 `{"error":"<Telegram description or network error>", "status": { … }}` when the send failed. The text is Telegram's `description` (e.g. `Bad Request: chat not found`) or the token-redacted network error.

## `GET /api/settings/telegram/status`

Returns the delivery status only, so the page can refresh the line without reloading the form.

```json
{ "last_success_at": "2026-11-03T10:15:02Z", "last_failure_at": "2026-11-03T10:20:40Z", "last_failure_error": "Forbidden: bot is not a member of the channel chat" }
```

## Go handler contract (`internal/settings/telegram.go`)

```go
// TelegramService is the notifier surface the Telegram settings endpoints drive.
// *notify.Service satisfies it.
type TelegramService interface {
    TelegramSettings(ctx context.Context) (notify.Settings, error)
    UpdateTelegram(ctx context.Context, u notify.Update) (notify.Settings, error)
    SendTest(ctx context.Context) error
    DeliveryStatus() notify.DeliveryStatus
}

func NewTelegram(log *slog.Logger, svc TelegramService) *TelegramHandler
func (h *TelegramHandler) Register(r chi.Router)
```

Error mapping: `errors.As(*notify.ValidationError)` → 400; `errors.Is(context.DeadlineExceeded)` → 503; `errors.Is(notify.ErrNotConfigured)` → 409; `errors.As(*notify.APIError)` or any other send error → 502.

Logging: request bodies are never logged. A failed test send (502) is logged at **Warn**, never Error, because the handler's logger is the tapped one after Phase C and a delivery failure must not reach the error window (FR-011, US4-AS3). `ErrNotConfigured` (409) and validation errors (400) are not logged. Store failures (500) stay at Error.

## Tests (`telegramSuite`, wired in `TestSettings`)

- `GET` never contains the token; `token_last4` follows the rule above.
- `PUT`:
  - without `bot_token` keeps the stored token (the fake service sees `nil`);
  - with a new one replaces it;
  - `enabled: true` without a chat returns 400;
  - an invalid token format returns 400;
  - a guard timeout returns 503.
- `POST /test`: success returns 200; `ErrNotConfigured` returns 409; an `APIError` returns 502 with its description.
- A failed test send (502) leaves no Error-level record in the handler's log (captured through a buffer logger); it logs one Warn.
- A sentinel token never appears in any response body (SC-006).

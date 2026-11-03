# Contract: SPA, Telegram settings form

## Placement

`SettingsPage.vue`, first tab:
- The tab's **id** stays `opds`, so `?tab=opds` links still work.
- Its **label** becomes `General`.
- The tab content is a heading `OPDS` with `<OpdsSettingsForm />`, then a heading `Telegram notifications` with `<TelegramSettingsForm />` (FR-001).
- The tab panel is toggled with `v-show`, not `v-if`, so switching to Libraries and back keeps unsaved OPDS and Telegram input (and does not reload the forms).

## `TelegramSettingsForm.vue` (layout B)

It is a card in the same style as `OpdsSettingsForm` (`card bg-base-200 max-w-xl gap-4 p-5`), rendered as a `<fieldset>` that is disabled while the form is locked: until the stored settings have loaded (so defaults can never be saved over the stored chat and topic) and while a request is in flight. Fields from top to bottom:

| Element | `data-testid` | Behaviour |
|---|---|---|
| On/off toggle (`toggle`) | `tg-enabled` | Bound to `enabled`. Switching on while there is no token (stored or typed) or no channel shows the inline error `A bot token and a channel ID are required.` (`tg-error`) and reverts to off (FR-003). |
| Bot token (`type="password"`, `autocomplete="new-password"`, `data-1p-ignore`, `data-lpignore="true"`, so password managers do not fill it) | `tg-token` | Empty after load and after save. Placeholder: `Stored (ends with <last4>)` when `token_set` and `token_last4` is non-empty, `Stored` when `token_set` with an empty `token_last4`, else `Bot token from @BotFather`. Trimmed, and sent only when non-empty (FR-002). |
| Channel ID | `tg-chat` | Text, placeholder `@channel or -100…`. Trimmed before saving. |
| Topic ID | `tg-topic` | Optional number (`min="1"`, `step="1"`). Empty sends `null`. Anything other than a positive integer, including input the browser cannot parse (e.g. `12-`, which reads back as empty), is rejected before sending with the inline error `Topic ID must be a positive integer.` (`tg-error`). |
| Silent checkbox | `tg-silent` | `Send without sound`. |
| Save button | `tg-save` | Disabled while the form is locked. Sends `PUT /api/settings/telegram` with `{enabled, chat_id, topic_id, silent}` plus `bot_token` only when typed. On success it re-renders from the response, clears the token field and shows the toast `Telegram settings saved`. On a 400 it shows the server's error inline (`tg-error`). Other errors give the toast `Failed to save Telegram settings: <message>`. |
| Send test message | `tg-test` | Disabled while the form is locked; when the **saved** settings lack a token or channel (`title`: `Save a bot token and a channel ID first`); and while the form has unsaved edits, since the test uses the saved settings (`title`: `Save your changes first`). `POST /api/settings/telegram/test`: success gives the toast `Test message sent` and takes the status line from the response; failure gives the toast `Test message failed: <error>` and re-reads the status line with `fetchTelegramStatus()`, because a rejected request does not expose the 502 body (FR-004). |
| Status line | `tg-status` | Rendered only once the settings have loaded. See below. |

Status line text, computed from `status`:
- No timestamps: `No messages sent since the server started.`
- Success only, or success later than failure: `Last delivered <local datetime>.`
- Failure later than success, or failure only: `Last delivery failed <local datetime>: <last_failure_error>` (warning colour), plus `Last delivered <local datetime>.` when a success exists.

Loading: it self-loads on mount through `fetchTelegramSettings()`. A load failure gives the toast `Failed to load Telegram settings: <message>`, keeps the form locked, and shows `Couldn't load Telegram settings.` with a **Retry** button (`tg-load-error`, `tg-retry`, outside the disabled fieldset); a successful retry unlocks the form.

## `api.ts` / `types.ts`

```ts
export interface TelegramDeliveryStatus {
  last_success_at: string | null;
  last_failure_at: string | null;
  last_failure_error: string;
}
export interface TelegramSettings {
  enabled: boolean;
  token_set: boolean;
  token_last4: string;
  chat_id: string;
  topic_id: number | null;
  silent: boolean;
  status: TelegramDeliveryStatus;
}
export interface TelegramSettingsUpdate {
  enabled: boolean;
  bot_token?: string;
  chat_id: string;
  topic_id: number | null;
  silent: boolean;
}

export function fetchTelegramSettings(): Promise<TelegramSettings>;              // GET  /settings/telegram
export function updateTelegramSettings(u: TelegramSettingsUpdate): Promise<TelegramSettings>; // PUT
export function sendTelegramTest(): Promise<{ ok: true; status: TelegramDeliveryStatus }>;     // POST /settings/telegram/test
export function fetchTelegramStatus(): Promise<TelegramDeliveryStatus>;          // GET  /settings/telegram/status
```

`request()` already turns a non-2xx `{error}` body into an `Error(message)`, so a 502 test failure surfaces Telegram's description.

## Vitest coverage

- `api.spec.ts`: each of the four calls hits the right method and path. `bot_token` is omitted from the `PUT` body when it is not provided.
- `TelegramSettingsForm.spec.ts`:
  - loads and renders the masked token placeholder, never a token value;
  - the `Stored` placeholder without a suffix when `token_last4` is empty;
  - the save payload omits `bot_token` when the field is empty and includes it when typed; the channel ID and token are trimmed; an empty topic is sent as `null`;
  - the token field is cleared after save;
  - a non-integer or unparseable topic ID is rejected inline with no request sent;
  - the form stays locked until the settings load and after a failed load; Retry unlocks it;
  - switching on without a token or channel shows `tg-error` and the toggle stays off, with no request sent;
  - a server 400 is shown inline;
  - the test button is disabled when the saved settings are incomplete and while the form has unsaved edits; a test success and a test failure each update the status line and toast;
  - all three status-line variants render.
- `SettingsPage.spec.ts`: the first tab is labelled `General`; both forms render in it; `?tab=opds` still selects it; the Telegram form is hidden on the Libraries tab; unsaved General-tab input survives a tab switch.

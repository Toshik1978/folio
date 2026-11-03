# Data Model: Telegram Notifications

There is no schema change and no migration. Persistent state is five rows in the existing `settings` table. Everything else lives in memory in `notify.Service` and is lost on restart, by design (FR-007, R7).

## Settings keys

Stored in `settings(key TEXT PRIMARY KEY, value TEXT)`. They are read with `GetSetting` and written with `UpsertSetting`, both existing sqlc queries, in one transaction under the write guard. The constants live in `internal/db/keys.go`.

| Constant | Key | Value encoding | Default (missing row) | Validation on save |
|---|---|---|---|---|
| `SettingTelegramEnabled` | `telegram_enabled` | `"1"` / `"0"` | off | `"1"` only when token and chat are set (FR-003) |
| `SettingTelegramBotToken` | `telegram_bot_token` | plain text (clarified) | empty | `^[0-9]+:[A-Za-z0-9_-]+$`; an absent or empty update keeps the stored value |
| `SettingTelegramChatID` | `telegram_chat_id` | text | empty | `@` + 5–32 `[A-Za-z0-9_]`, or a signed 64-bit integer (e.g. `-100…`) |
| `SettingTelegramTopicID` | `telegram_topic_id` | decimal text, or empty | none | integer > 0 when present |
| `SettingTelegramSilent` | `telegram_silent` | `"1"` / `"0"` | off | none |

Rules:
- The token value is never returned by any API, never logged, and never included in an error string (R2).
- Saving with notifications on while the token or chat is missing returns 400, and nothing is written.
- Switching off keeps every other value (US1-AS5).

## Go types (`internal/notify`)

### Config (active settings, cached)

```go
type Config struct {
    Enabled  bool
    BotToken string // secret
    ChatID   string
    TopicID  int64  // 0 = none
    Silent   bool
}
func (c Config) Ready() bool // BotToken != "" && ChatID != ""
```

It is held in an `atomic.Pointer[Config]`, loaded in `New` and swapped by `UpdateTelegram`. The sender reads it before each attempt, so a message uses the settings in force when it is sent.

### Update (from `PUT`)

```go
type Update struct {
    Enabled  bool
    BotToken *string // nil or "" = keep stored
    ChatID   string
    TopicID  *int64  // nil = none
    Silent   bool
}
```

### Settings (redacted view)

```go
type Settings struct {
    Enabled    bool
    TokenSet   bool
    TokenLast4 string // "" when unset or shorter than 8 characters
    ChatID     string
    TopicID    *int64
    Silent     bool
    Status     DeliveryStatus
}
```

### DeliveryStatus (in memory)

```go
type DeliveryStatus struct {
    LastSuccessAt    *time.Time
    LastFailureAt    *time.Time
    LastFailureError string // Telegram description or network error (both token-redacted), or "queue full: N message(s) dropped"
}
```

It is updated by the sender after each message's final outcome, and by `SendTest`. A failure does not clear `LastSuccessAt`, and a success does not clear the failure. The UI shows the failure when `LastFailureAt` is after `LastSuccessAt`.

### Errors

- `ErrNotConfigured`: test send without a token and chat.
- `*ValidationError{Field, Msg string}`: the `PUT` validation result.
- `*APIError{Code int; Description string; RetryAfter time.Duration}`: Telegram returned `ok: false`. `Description` has the token replaced with `<redacted>` before it is returned.

### Message (queue item)

```go
type Kind uint8 // KindTest, KindCrash, KindCrashRepeat, KindRunFailed, KindRunRecovered,
                // KindUnreadable, KindMassRemoval, KindErrorSummary
type message struct { kind Kind; text string }
```

Queues: `urgent chan message` (capacity 8, `KindCrash` only, always drained first) and `batch chan message` (capacity 32, every other kind). A full lane drops the new message and records the failure status. Each message gets 3 attempts in total within a 30 s budget; a 429 `retry_after` above 10 s, or one that would overrun the budget, drops it and records the failure. The sender waits 3 s after each message, or `min(retry_after, 10 min)` after one dropped on a 429, so the queued messages wait out the flood ban. On shutdown it returns as soon as intake is closed and both lanes are empty, without a final wait.

## Outcome types (producers → notifier)

Producers build these; the notifier consumes them. Their full contract is in [contracts/notifier.md](./contracts/notifier.md).

```go
type RunKind uint8   // RunSync, RunDeepIndex
type RunStatus uint8 // RunFailed, RunCompleted

type UnreadableBook struct {
    BookID int64
    Reason string // ebook.Reason value: corrupt | missing_entry | too_large | crashed
}

type RunOutcome struct {
    LibraryID       int64
    Library         string // library name
    Kind            RunKind
    Status          RunStatus
    Reason          string // RunFailed: the error text
    CrashReported   bool   // RunFailed came from a recovered panic whose crash message was queued (false when it was de-duplicated or dropped)
    PerBookErrors   int    // RunCompleted: books left for retry (DeepIndexResult.Errors, incl. post-sync fill)
    FillFailed      string // RunSync completed but its post-sync fill could not complete: the error text
    NewlyUnreadable int    // total newly recorded unreadable books (DeepIndexResult.Broken)
    UnreadableSample []UnreadableBook // first ≤5
    BooksBefore     int    // RunSync: book count before the run
    BooksRemoved    int    // RunSync: books pruned by the run (ingest.Result.RemovedBooks)
}
```

The fields come from these existing types:

| Field | Source |
|---|---|
| `Status`/`Reason` | `runSync` parser error, the "no parser" branch, a checkpoint skip (`RunCompleted`), `deepIndexLibrary` error or unreachable read root (`ingest.CheckReadRoot`), `safeSync` recover |
| `PerBookErrors` | `ingest.DeepIndexResult.Errors` (Deep Index, or the sync's post-sync fill) |
| `FillFailed` | the post-sync fill's error, or `post-sync fill panicked: <value>` when `safeFill` recovered a panic |
| `NewlyUnreadable` | `ingest.DeepIndexResult.Broken` |
| `UnreadableSample` | new `ingest.DeepIndexResult.NewlyBroken []BrokenBook` (≤5) |
| `BooksBefore` | `CountBooksByLibrary` (existing sqlc query) read under the guard before `parser.Sync` |
| `BooksRemoved` | new `ingest.Result.RemovedBooks` = `len(ListEmptyBooksByLibrary)` removed in `prune` |

`ingest.Result.Removed` keeps its meaning (files removed) and its `"sync done"` log.

## In-memory state

### Library failing state

`failing map[int64]bool`: present means the library's last run failed and the owner has been told (or a crash message stood in).

```text
             RunFailed (CrashReported=false)
  HEALTHY ───────────────────────────────▶ FAILING      send "failed"
  HEALTHY ───────────────────────────────▶ FAILING      RunFailed (CrashReported=true): send nothing (crash already sent)
  FAILING ── RunFailed (any) ───────────▶ FAILING      send nothing
  FAILING ── RunCompleted ──────────────▶ HEALTHY      send "recovered"
  HEALTHY ── RunCompleted ──────────────▶ HEALTHY      send nothing (for the state)
  FAILING ── LibraryRemoved ────────────▶ (deleted)    send nothing
  any     ── notifications switched off ▶ (cleared)
  process start ─────────────────────────▶ all HEALTHY (no memory across restarts)
```

- The state is per library, not per run kind (R6). The "failed" and "recovered" messages name the kind of the run that caused the transition.
- `CrashReported=true` only when `ReportCrash` queued the crash message. A crash that was only counted in an open de-dup window, or dropped on a full lane, gives `CrashReported=false`, so the first failure still sends "failed".
- A checkpoint-skipped sync is a `RunCompleted`, but a library whose last run failed is in `error` state in the database and is never skipped: it is read in full, so only a run that actually read the library can take it back to HEALTHY.
- Messages that do not depend on the state are evaluated on every `RunCompleted`, independently of the transition:
  - unreadable summary: `NewlyUnreadable > 0`;
  - mass removal: sync only, `BooksRemoved >= 20 && BooksRemoved*10 >= BooksBefore && BooksBefore > 0`;
  - error-window line: `PerBookErrors > 0` or `FillFailed != ""`.
- SC-003 holds: N failed runs followed by one success give exactly 2 messages.

### Crash de-dup entries

`crashes map[string]*crashEntry`, at most 256 entries.

```go
type crashEntry struct {
    first      time.Time
    place, via string // via = "<outer place> (<detail>)" when the value carries its own place (db.TxPanic, ingest.WorkerPanic); "" otherwise
    detail     string
    frame      string // firstAppFrame of the first occurrence
    text       string // the first occurrence's error text (not normalized), capped at 300 chars
    suppressed int
}
```

Key: `place + "|" + firstAppFrame + "|" + normalize(errorText)`. `normalize` maps digit runs and `0x…` addresses to `#`.

```text
  (none) ── crash K ─────────────────────────▶ OPEN{first=now, suppressed=0}   send crash message now
  OPEN   ── crash K, now < first+1h ─────────▶ OPEN{suppressed+1}              send nothing
  OPEN   ── tick, now ≥ first+1h, suppressed>0 ▶ (deleted)                     send "repeated N more times"
  OPEN   ── tick, now ≥ first+1h, suppressed=0 ▶ (deleted)                     send nothing
  OPEN   ── crash K, now ≥ first+1h (before the tick) ▶ OPEN{first=now, suppressed=0}   send "repeated N more times" if suppressed>0, then the crash message now
  any    ── notifications switched off ────────▶ (cleared)
  map full (256) ── new key ───────────────────▶ not stored                    send crash message now (no de-dup); record the overflow as the last failure
```

SC-002 holds: the same crash repeated any number of times within an hour gives at most 2 messages.

### Error window

```go
type errorWindow struct {
    open     bool
    deadline time.Time      // first entry time + 5 min
    count    int            // all entries, incl. run lines
    samples  []string       // first 5 lines: "HH:MM:SS <message>: <error>" or a run line
}
```

```text
  CLOSED ── LogError / run line ─▶ OPEN{deadline=now+5m, count=1, samples=[line]}
  OPEN   ── LogError / run line ─▶ OPEN{count+1, samples append while <5}
  OPEN   ── tick, now ≥ deadline ─▶ CLOSED                send one summary (count + samples)
  OPEN   ── Stop() ───────────────▶ CLOSED                flush summary into the queue
  any    ── switched off ─────────▶ CLOSED (discarded)
```

- A record is entered only when it is at Error level, notifications are on, and the record's context is not `logging.MarkReported`.
- Records from the notifier's own (untapped) logger never arrive.

## Validation summary

| Rule | Where | Spec |
|---|---|---|
| Enabling requires token and chat | `notify.validate` (server), `TelegramSettingsForm` (client) | FR-003 |
| Token never returned or logged | `Settings` view has no token field; `redact` | FR-002, FR-005, FR-016, SC-006 |
| Untouched token kept | `Update.BotToken` nil or empty | FR-002 |
| Off means nothing is collected | early return in `ReportCrash`, `RunFinished`, `LogError`; state cleared on switch-off | FR-015 |
| Bounded pending work | urgent lane 8, batch queue 32, crash map 256, window samples 5 | FR-014 |
| Message ≤ 4000 characters | `format.truncate` | edge case "longer than Telegram allows" |

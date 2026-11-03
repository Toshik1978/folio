# Contract: Go-level notifier interfaces

`*notify.Service` is the single implementation. Each consumer declares the narrow interface it needs next to the code that uses it (Principle VI). `notify` imports none of the consumers, so there are no cycles. All producer methods:

- are safe for concurrent use;
- never block on I/O: each does a mutex-guarded state update and at most a non-blocking channel send;
- never panic;
- return at once, without storing anything, when notifications are off or not configured (FR-015).

## Consumer interfaces

### `internal/ebook`: crash in a parser

```go
// CrashReporter receives parser panics recovered by the Dispatcher.
type CrashReporter interface {
    ReportCrash(place, detail string, recovered any, stack []byte) bool // true when the crash message was queued
}

type Option func(*Dispatcher)
func WithCrashReporter(r CrashReporter) Option
func NewStandardDispatcher(opts ...Option) *Dispatcher // NewDispatcher(parsers ...Parser) unchanged
```

Call site: the `Dispatcher.run` recover makes the call `ReportCrash("book-file reading", "<format> parser, <metadata|cover> read", r, debug.Stack())`. The file name is not passed. `*PanicError` is still returned as before, and the book is recorded as unreadable `crashed`.

Double-reporting rule: the crash message reports the code fault once (de-duplicated for an hour); during a run, the book is listed once in that run's unreadable summary with the `crashed` reason, like any other recorded read failure. Neither enters the error window. A book recorded unreadable outside a run (the on-view backfill) is not notified.

### `internal/db`: write path (no interface)

```go
// TxPanic is re-panicked by WithTx after rolling back, so the recover that
// keeps the process alive can report the crash with its true place and stack.
type TxPanic struct {
    Value any
    Stack []byte
}
func (p *TxPanic) Error() string        // "panic in write transaction: <value>"
func (p *TxPanic) Unwrap() error        // Value when it is an error, else nil
func (p *TxPanic) CrashPlace() string   // "database write path"
func (p *TxPanic) CrashStack() []byte   // stack captured inside WithTx
```

`notify.ReportCrash` checks the recovered value for `interface{ CrashPlace() string; CrashStack() []byte }`. When it matches:
- the place becomes `CrashPlace()`;
- the stack becomes `CrashStack()`;
- the caller's place and detail become `via` ("web request handling (GET /api/books/{id})").

### `internal/server`: HTTP recoverer

```go
type CrashReporter interface {
    ReportCrash(place, detail string, recovered any, stack []byte) bool // true when the crash message was queued
}

type Handlers struct {
    API     []Registrar
    OPDS    Registrar
    MCP     http.Handler
    Crashes CrashReporter // nil = log only
}
```

`recoverer(log, crashes)` replaces `middleware.Recoverer`. On a panic:
- `http.ErrAbortHandler` (matched with `errors.Is` through `Unwrap`) is re-panicked as the bare `http.ErrAbortHandler` (net/http compares it by equality);
- otherwise it calls `log.ErrorContext(logging.MarkReported(ctx), "http handler panicked", method, route, panic, stack)` and `ReportCrash("web request handling", "<METHOD> <route pattern>", v, debug.Stack())`, then writes 500 unless the request is an upgrade.

### `internal/sync`: run outcomes and engine crashes

```go
// Notifier receives run outcomes and engine crashes. *notify.Service satisfies it.
type Notifier interface {
    ReportCrash(place, detail string, recovered any, stack []byte) bool // true when the crash message was queued
    RunFinished(o notify.RunOutcome)
    LibraryRemoved(id int64)
}

func WithNotifier(n Notifier) Option
```

Call sites in `engine.go` and `purge.go`:

| Situation | Call |
|---|---|
| `safeSync` recovers a panic | `ReportCrash("sync engine", "library \"<name>\" <sync\|deep index>", r, debug.Stack())`, then `RunFinished{Status: RunFailed, CrashReported: true, Reason: "<panic text>"}` |
| `runSync` parser error (not `context.Canceled`) | `RunFinished{Kind: RunSync, Status: RunFailed, Reason: err.Error()}` |
| no parser for the library type | `RunFinished{Kind: RunSync, Status: RunFailed, Reason: …}` |
| `runSync` success (after the post-sync fill, if any) | `RunFinished{Kind: RunSync, Status: RunCompleted, BooksBefore, BooksRemoved, PerBookErrors, NewlyUnreadable, UnreadableSample, FillFailed}` from the fill's `DeepIndexResult` |
| checkpoint skip | `RunFinished{Kind: RunSync, Status: RunCompleted}` (zero counts) |
| `deepIndexLibrary` error (not cancel) | `RunFinished{Kind: RunDeepIndex, Status: RunFailed, Reason: err.Error()}` |
| `deepIndexLibrary` success | `RunFinished{Kind: RunDeepIndex, Status: RunCompleted, PerBookErrors: res.Errors, NewlyUnreadable: res.Broken, UnreadableSample: …}` |
| shutdown cancellation, library missing, `pending_purge` skip | nothing |
| a purge completes (`checkPurge`, `RequestPurge`, `PurgeLibrary`) | `LibraryRemoved(id)` |

The run-failure logs (`"sync failed"`, `"deep index failed"`, `"sync panicked"`) use `ErrorContext(logging.MarkReported(ctx), …)`, so they do not also enter the error window.

### `internal/logging`: error tap

```go
// ErrorSink receives Error-level records not marked as already reported.
type ErrorSink interface {
    LogError(ctx context.Context, r slog.Record)
}

func WithErrorSink(base *slog.Logger, sink ErrorSink) *slog.Logger
func MarkReported(ctx context.Context) context.Context
func IsReported(ctx context.Context) bool
```

`r` is a clone that also carries the attrs baked in through `With`. The sink must not log through the tapped logger; `notify` is built with the base logger, which guarantees that.

### `internal/settings`

See [rest-api.md](./rest-api.md#go-handler-contract-internalsettingstelegramgo).

## `internal/notify` exported surface

```go
func NewStore(database *sql.DB, guard *db.WriteGuard) *Store
func NewClient(base string, httpClient *http.Client) *Client
func New(log *slog.Logger, store *Store, client *Client, opts ...Option) (*Service, error)

func WithPublicURL(u string) Option
func WithClock(now func() time.Time) Option
func WithTiming(t Timing) Option

type Timing struct {
    Spacing       time.Duration   // default 3s
    Backoff       []time.Duration // default 2s, 4s (len = retries; 3 attempts in total)
    MaxRetryAfter time.Duration   // default 10s: cap on an honoured 429 retry_after
    MessageBudget time.Duration   // default 30s: all attempts and waits for one message
    ErrorWindow time.Duration   // default 5m
    CrashWindow time.Duration   // default 1h
    TickEvery   time.Duration   // default 30s
    StopDrain   time.Duration   // default 5s
    QueueSize   int             // default 32 (batch lane)
    UrgentSize  int             // default 8 (crash lane, drained first)
}

func (s *Service) Start()
func (s *Service) Stop()

// Producers
func (s *Service) ReportCrash(place, detail string, recovered any, stack []byte) (queued bool)
func (s *Service) RunFinished(o RunOutcome)
func (s *Service) LibraryRemoved(id int64)
func (s *Service) LogError(ctx context.Context, r slog.Record)

// Settings
func (s *Service) TelegramSettings(ctx context.Context) (Settings, error)
func (s *Service) UpdateTelegram(ctx context.Context, u Update) (Settings, error)
func (s *Service) SendTest(ctx context.Context) error
func (s *Service) DeliveryStatus() DeliveryStatus

var ErrNotConfigured error
type ValidationError struct{ Field, Msg string }
type APIError struct{ Code int; Description string; RetryAfter time.Duration }
```

The types `RunOutcome`, `RunKind`, `RunStatus`, `UnreadableBook`, `Config`, `Update`, `Settings` and `DeliveryStatus` are defined in [data-model.md](../data-model.md).

Compile-time guards live in `cmd/folio-idx/main.go`, or in the consumer's test file, so `notify` imports none of the consumers:

```go
var (
    _ ebook.CrashReporter      = (*notify.Service)(nil)
    _ server.CrashReporter     = (*notify.Service)(nil)
    _ sync.Notifier            = (*notify.Service)(nil)
    _ settings.TelegramService = (*notify.Service)(nil)
    _ logging.ErrorSink        = (*notify.Service)(nil)
)
```

## Telegram client contract (`notify.Client.Send`)

- `POST {base}/bot{token}/sendMessage` with this JSON:

  ```json
  {"chat_id":"<channel-id>","text":"<text>","message_thread_id":42,"disable_notification":true,"link_preview_options":{"is_disabled":true}}
  ```

  `message_thread_id` is omitted when there is no topic.
- 2xx with `ok: true` returns `nil`.
- `ok: false` (any HTTP status) returns `*APIError{Code: error_code, Description: description, RetryAfter: parameters.retry_after s}`, with every occurrence of the token in `description` replaced by `<redacted>` (Telegram can echo the request path `/bot<token>/…`).
- Non-JSON or transport failure returns a wrapped error with the token redacted (no `*url.Error` text).
- Therefore no error `Send` returns contains the token, and the last-failure text and the Warn log that store it are token-free.
- Retry policy (the sender, not `Send`):

  | Error | Retry |
  |---|---|
  | `APIError.Code == 429` | yes; wait `max(RetryAfter, backoff[i])`; drop and record the failure if `RetryAfter > MaxRetryAfter` (10 s) or the wait would overrun `MessageBudget` |
  | `APIError.Code >= 500`, transport error, timeout | yes; wait `backoff[i]` |
  | other `APIError` (400/401/403/404) | no |

  At most 3 attempts per message (1 + `len(Backoff)`), all inside `MessageBudget`; each attempt's deadline is the earlier of the 10 s client timeout and the end of the budget. A wait that would overrun the budget drops the message and records the failure.
- Ordering: the sender always drains the urgent lane (crash messages) before the batch lane, then waits `Spacing` after each message. After a message dropped on a 429 it waits `max(Spacing, min(RetryAfter, 10 min))` instead, so queued messages wait out the flood ban in their bounded lanes rather than each being sent into it and dropped.

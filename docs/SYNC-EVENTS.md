# Sync Events (SSE)

> Server-pushed sync state and indexing progress over Server-Sent Events.
>
> **Status:** Implemented. SSE replaces the old frontend polling loops; the
> per-library progress bar is live. `GET /api/sync/status` is retained as the
> initial snapshot source and the polling fallback.

---

## Why

The live UI (sync banner, per-row syncing/queued badges, the Settings library
list) used to be driven by two polls: a global 3s `useSyncStatus` poll and a 5s
`SettingsPage` libraries poll. The 3s cadence could **not** be backed off because
a sync can start *and* finish inside a poll interval (a bad-path library failing
in milliseconds, or a tiny library). When that happened the poll missed the whole
episode: no banner, the `running` flag never flipped, and the **final per-library
error status was silently dropped** — a correctness gap, not just latency.

Server push fixes it: start/finish/error events arrive in order over TCP, so even
a millisecond-long run reports its outcome. Push also feeds the **per-library
progress bar**, which coarse polling cannot, and removes the idle round-trips.

**Transport — SSE, not WebSockets.** The flow is purely server → client (all
triggers are already separate `POST` endpoints), so WebSockets would add a
dependency and a protocol upgrade (which complicates the Cloudflare Tunnel path)
for nothing. SSE is plain HTTP, stdlib-only (`http.Flusher` +
`http.ResponseController`), and the browser's `EventSource` auto-reconnects.

---

## Architecture

A transport-agnostic event broker fans domain events out to SSE subscribers. The
sync engine emits events; the broker handles fan-out + slow-consumer coalescing;
the API layer owns the SSE framing.

```
api  ──▶ events   (api/events.go: SSE handler subscribes to the Broker)
api  ──▶ sync     (handler reads Engine.Status for the initial snapshot)
sync ──▶ events   (Engine publishes events.Event at its mutation points)
```

`internal/events` imports nothing from `sync`/`api` — it is a leaf, so there are
no import cycles.

```
internal/events/
  event.go      // Event envelope + EventType constants (no behavior)
  broker.go     // Broker + Subscription: Subscribe/Unsubscribe/Publish/Close
  broker_test.go
internal/api/
  events.go     // GET /api/sync/events handler (SSE framing)
```

**The `Publisher` interface lives in `sync` (the consumer), not `events`.**
`type Publisher interface { Publish(events.Event) }` is defined in
`internal/sync/engine.go`, and `*events.Broker` satisfies it structurally. This
is the idiomatic Go "accept interfaces where they're used" placement (and the
`iface` linter enforces it — nothing in `events` itself consumes a publisher).

**Wiring.** The composition root (`cmd/folio-idx/main.go`) constructs one
`*events.Broker` and injects the same instance into both `sync.New(...)` (via the
`WithEvents` option) and `api.New(...)` (through `server.Deps.Events`). The
publisher is **nil-tolerant** — a nil publisher makes every emit a no-op, so
engine/parser tests that don't care about events stay unchanged.

---

## Event model

The envelope is deliberately generic so the broker never inspects payloads:

```go
type EventType string

const (
    TypeStatus   EventType = "status"
    TypeProgress EventType = "progress"
    TypeLibrary  EventType = "library"
)

type Event struct {
    Type        EventType // becomes the SSE `event:` field
    CoalesceKey string    // "" = never drop; non-empty = keep-latest-per-key
    Data        any       // JSON-marshaled into the SSE `data:` field
}
```

`CoalesceKey` lets the broker collapse floods without understanding payloads:
empty = reliable delivery, non-empty = a slow subscriber only sees the latest
event sharing that key.

### Payloads & wire contract

| Event | Delivery | `CoalesceKey` | JSON `data` |
| :--- | :--- | :--- | :--- |
| `status` | reliable | `"status"` | `{"running":bool,"current":int,"queued":[int]}` |
| `library` | reliable | `""` | `{"id":int,"status":"active"\|"error"}` |
| `progress` | best-effort | `"progress:<id>"` | `{"library":int,"processed":int,"total":int?}` |

- `status` is `sync.Status` verbatim, so the frontend `SyncStatus` type matches.
  `current` is `omitempty`, so an idle snapshot omits it. Keyed `"status"` so a
  burst of rapid transitions collapses to the latest snapshot — what the banner
  wants.
- `library` carries only primitive fields (no API DTO) → keeps `sync` ↔ `api`
  decoupled. The client refetches the libraries list on receipt.
- `progress` omits `total` when unknown → the frontend renders the indeterminate
  count-up bar; present → determinate %-bar.

**SSE frame** (named events so the client attaches typed handlers):

```
event: status
data: {"running":true,"current":7,"queued":[9,3]}

event: progress
data: {"library":7,"processed":1200,"total":5000}
```

**On connect**, the handler immediately writes one `status` event from the
current `Engine.Status()`. This replaces the frontend's startup poll and
guarantees a fresh client never waits for the next transition. **No initial
`progress` frame is sent** — the handler has no access to the per-run reporter's
counts; during an active sync the first `progress` event follows within one
throttle interval (~250 ms). The snapshot is eventually-consistent rather than
strictly serialized: because the engine emits `status` *after* releasing `e.mu`,
a coalesced `status` event may land immediately after the connect snapshot, but
coalescing keeps only the latest, so the client converges on current state within
microseconds. Because every reconnect re-delivers a full snapshot, **no
`Last-Event-ID` / replay is implemented** (YAGNI).

---

## Broker internals

The broker owns **no goroutines of its own** — it is passive; the SSE request
goroutine does the reading, so lifecycle ties to the request context and nothing
leaks.

```go
type Broker struct {
    mu     sync.RWMutex
    subs   map[*Subscription]struct{}
    closed bool
}
func (b *Broker) Subscribe() (*Subscription, bool) // false = at cap or closed
func (b *Broker) Unsubscribe(s *Subscription)       // idempotent; closes the sub
func (b *Broker) Publish(ev Event)                  // RLock; fan out to each sub
func (b *Broker) Close()                            // close all subs; reject new

type Subscription struct {
    mu      sync.Mutex
    pending []Event       // ordered; small, drained on every wake
    wake    chan struct{} // cap 1, non-blocking signal
    done    chan struct{} // closed when the broker recycles the subscription
    closed  bool
}
func (s *Subscription) C() <-chan struct{}    // wake signal for the handler
func (s *Subscription) Done() <-chan struct{} // closed on recycle/Close
func (s *Subscription) Drain() []Event        // swap out pending under lock
```

**Coalescing** (`Subscription.offer`, called by `Publish`):

- `CoalesceKey == ""` → **append** (reliable; `status`*, `library`).
- `CoalesceKey != ""` → scan `pending` for a same-key entry; if found, **overwrite
  in place** (keeps position, collapses the flood to latest); else append.

`pending` stays short because the handler drains on every wake, so the linear
scan is trivial. The cap-1 `wake` may occasionally fire with nothing buffered, so
the handler tolerates an empty `Drain()`.

**Overflow → recycle** (slow-consumer guard): `pending` has a hard cap
(`maxPending = 64`). Reliable events are infrequent and `progress` coalesces to
one entry per library, so the cap is only ever hit by a genuinely stuck client.
On overflow the broker **closes that subscription** (its `Done()` fires);
`EventSource` auto-reconnects and receives a fresh snapshot — self-healing,
bounded memory. A `maxSubscribers = 128` cap makes the handler answer `503`
rather than allow unbounded streams. This cap is **global (per process), not per
client**: each open browser tab holds one subscription, so a single user with many
tabs across several devices draws on the same 128-stream budget. That is
acceptable behind Cloudflare Access (a small, trusted user set); a public-facing
deployment would add a per-remote-addr limit upstream.

**Concurrency rules:**

- `Publish` is non-blocking and does no IO. Safe to call from HTTP handlers and
  scheduler callbacks.
- The Engine snapshots `Status()` under `e.mu` and calls `Publish` **while still
  holding `e.mu`**. Lock order is strictly `e.mu → broker.mu → sub.mu` with no
  reverse path (subscriptions never call back into the Engine), so there is no
  deadlock; holding the lock across publish linearizes `status` events with the
  state mutations.
- `RWMutex`: `Publish` takes `RLock`; `Subscribe`/`Unsubscribe`/`Close` take
  `Lock`. Each `Subscription` guards its own `pending`.

---

## Graceful shutdown

`cmd/folio-idx/main.go`'s `serve()` calls `broker.Close()` **before**
`srv.Shutdown()`. SSE handlers are long-lived and never become idle on their own,
so `http.Server.Shutdown` (which waits for in-flight requests to finish) would
otherwise block for the full shutdown timeout. `broker.Close()` fires every
subscription's `Done()`, the handlers return promptly (their `select` unblocks on
`sub.Done()`), the connections go idle, and `Shutdown` completes cleanly. `Close`
also flips `closed`, so any SSE request arriving during shutdown gets `503`.

---

## Engine emission

`Engine` holds an `events Publisher` field (wired via `sync.New(..., WithEvents)`,
nil-tolerant) plus helpers `emitStatus()` (snapshot + publish a `status` event)
and `emitLibrary(id, status)`. Emission points are the existing state
chokepoints — surgical instrumentation:

| Trigger | Location | Event |
| :--- | :--- | :--- |
| queue grows | `enqueue` (`internal/sync/engine.go`) | `status` |
| run starts / advances | `dequeue` sets `current` | `status` |
| run ends (`current=0`) | worker loop | `status` |
| sync success / checkpoint-skip | `recordLastSync` | `library {id,"active"}` |
| sync error / panic | `markError` (covers both `runSync` error and `safeSync` panic) | `library {id,"error"}` |

Progress events are emitted from the per-run reporter (see [Progress](#progress)).

**Scope boundary (deliberate):** `library` events cover **background** row changes
(scheduled / watcher syncs) — what the 5s poll existed to catch. User-initiated
**add / purge** keep their existing client-side refetch-after-action. Accepted
trade-off: a library added/removed *from another tab* won't appear live until a
sync event or manual refresh; a future `library`-added/removed event closes that
gap with no schema change.

---

## SSE HTTP handler

`GET /api/sync/events` — handler in `internal/api/events.go`, registered beside
`GET /api/sync/status`.

**Write deadline (critical).** `cmd/folio-idx/main.go` sets `WriteTimeout: 60s`,
which would terminate the stream after 60s. The handler clears it exactly as the
download path does (`internal/bookfile/bookfile.go`):

```go
_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
```

**Compression.** The middleware chain (`internal/server/server.go`) is
`proxyHeaders → (dev) Logger → Recoverer` — there is **no** `middleware.Compress`,
so no gzip buffering to defeat.

**Flow:**

1. Assert `http.Flusher` (else `500`); reject a nil/closed broker with `503`.
2. `broker.Subscribe()` → `(sub, ok)`; `!ok` → `503`. `defer Unsubscribe(sub)`.
3. Clear the write deadline (above).
4. Headers, then flush: `Content-Type: text/event-stream`,
   `Cache-Control: no-cache`, `Connection: keep-alive`, `X-Accel-Buffering: no`.
5. `retry: 3000\n\n` once (explicit reconnect backoff).
6. Initial snapshot: a single `status` event from `h.sync.Status()` (no initial
   `progress` frame; the first one arrives within ~250 ms during an active run).
7. Loop: `select` on `ctx.Done()` (client gone) / `sub.Done()` (broker recycle or
   shutdown) / ~20s heartbeat (`: ping\n\n`, under `IdleTimeout: 120s`) / `sub.C()`
   → `Drain` → `writeEvent` + `Flush`. A failed write breaks the loop → `defer`
   unsubscribes.

`writeEvent`: `event: <type>\n` + `data: <json>\n\n` (`json.Marshal` is
single-line, so one `data:` line suffices).

---

## Frontend

`useSyncStatus.ts` uses an `EventSource` instead of a poll; its public API
(`running`/`current`/`queued`/`isSyncing`/`isBusy`/`currentName`/`currentProgress`)
is consumed by `SyncBanner`, `LibraryStats`, and `SettingsPage`. `connect()` /
`disconnect()` are driven by the app-shell lifecycle. SSE payloads are parsed
through a guarded `parseEvent<T>` helper (a malformed frame is ignored, not
thrown out of the listener).

**Three-state connection machine** (the cost of the fallback):

```
connect():  open EventSource('/api/sync/events'); state = connecting; watchdog ~6s
  on 'status'   → applyStatus(data); state = live; clear watchdog; stop fallback
  on 'library'  → refreshLibraries()            // replaces the 5s SettingsPage poll
  on 'progress' → set currentProgress (if library === current)
  on any event  → reset watchdog; state = live
  watchdog fires (opened but silent ~6s)  → FALLBACK   // catches a buffering proxy
  onerror w/ readyState === 2 (CLOSED)    → FALLBACK
FALLBACK: run legacy 3s poll; retry SSE ~30s; on a delivered event → live, stop poll
```

The watchdog is the detector for a **buffering proxy** — the one failure mode
`EventSource`'s own auto-reconnect cannot catch. The server sends the initial
`status` immediately, so a healthy stream delivers an event within milliseconds;
"opened but silent for ~6s" reliably means buffered. The fallback poll keeps the
legacy behavior (status fetch + `refreshLibraries` on a running/just-finished
transition), since no `library` events flow while polling.

`SettingsPage.vue` has no libraries poll; background updates arrive via `library`
events, and entering the libraries tab does a one-shot refresh. `fetchSyncStatus`
remains for the fallback poll; `types.ts` defines `SyncStatus`, `LibraryEvent`,
and `SyncProgress`.

**Deliberate non-goal:** no Page-Visibility disconnect-on-hidden. SSE idle cost is
~one connection + a 20s heartbeat, and staying open keeps the banner correct on
tab-switch-back.

---

## Progress

### Parser interface

`internal/ingest/ingest.go` carries a `Reporter` on the parser contract (explicit
param, not context-smuggled):

```go
type Reporter interface {
    SetTotal(n int) // call when an exact total is cheaply known (0 = indeterminate)
    Add(n int)      // increment processed
}
type Parser interface {
    Sync(ctx context.Context, library dbq.Library, db *sql.DB, covers CoverStore, r Reporter) (Result, error)
}
```

`ingest.NopReporter` (no-op) is used by parser tests and non-progress callers.

**Counting lives in the reconciler**, the single per-record chokepoint shared by
all three parsers: `reconciler.upsert` calls `Add(1)` *after* a record is
persisted (a failed upsert doesn't tick the bar), and `markSeen` (unchanged files
on re-sync) also counts. So every parser reports progress with no per-loop
plumbing.

**The concrete reporter** lives in `sync`, constructed per run in `runSync`, bound
to the library id, the broker, and the injectable clock `e.now`:

- Throttles to ~250ms between emits (via `e.now`, so tests are deterministic).
  `Add` accumulates and emits only when the interval elapsed; `SetTotal` emits
  immediately; a final flush at run end (even on error) paints the last frame.
- Publishes `{library, processed, total?}` with `CoalesceKey "progress:<id>"`.
- The reporter is single-goroutine-per-run (the worker drives one sync at a time);
  its mutex is defensive, and a redundant emit would be harmless (coalesced).
- When `e.events == nil`, `runSync` passes `NopReporter` — zero overhead.

**Per-parser total** (determinate vs count-up):

| Parser | `SetTotal` | Bar |
| :--- | :--- | :--- |
| Calibre | yes — `COUNT(*)` over the same query it syncs | determinate %-bar |
| INPX | no — counting records re-reads the zip (doubles IO) | count-up |
| Folder | no — total unknown without a pre-walk | count-up |

Calibre's total is `SELECT COUNT(*) FROM (<calibreBooksQuery>)` — wrapping the
exact main query as a subquery means the total can never drift from the number of
rows actually synced. A count failure degrades to an indeterminate bar rather than
failing the sync; an empty library skips `SetTotal` entirely. A cheap total can be
added to INPX/Folder later with zero schema/frontend change.

### Progress-bar UI

`useSyncStatus` holds `currentProgress: {processed, total?} | null`. The engine is
single-worker (one `current` at a time), so one ref suffices: set on a `progress`
event matching `current`, cleared when `status` shows `current` changed or
`running=false`. Rendered with DaisyUI `progress` — determinate (`value`/`max`)
when `total` is present, indeterminate-animated otherwise — with a count label
formatted by `formatProgress` (shared between the syncing row in `SettingsPage`
and the one-line summary in `SyncBanner`).

---

## Testing

**Backend**

- `events` broker (no HTTP): coalesce-to-latest per key; reliable events in order;
  overflow → subscription closed; unsubscribe/Close idempotent and recycle subs;
  `go test -race` clean.
- Engine emission (testify suite + recording fake `Publisher`, injectable clock):
  `enqueue` emits a `status`; `emitLibrary` emits a `library`; a nil publisher is
  a no-op.
- Reporter throttle: fake clock drives `Add`/`SetTotal`; asserts emit cadence,
  immediate `SetTotal`, final flush, and `total` omitted from the wire when zero.
- Reconciler: `markSeen` counts progress.
- SSE handler (`httptest.NewServer` + streaming client): `text/event-stream` +
  `no-cache` headers, the initial `status` frame, a broker-published event on the
  wire.
- Calibre: a recording reporter asserts `total > 0` and `total == processed`.

**Frontend (Vitest)**

- `useSyncStatus.spec.ts`: mocked `EventSource` — status-apply, `library` →
  `refreshLibraries`, watchdog → fallback, close → fallback, progress
  set/clear-on-advance, wrong-library/indeterminate handling.
- `SettingsPage` / `SyncBanner` specs: tab-switch refresh, determinate-vs-count-up
  bar rendering; `format.spec.ts` covers `formatProgress`.

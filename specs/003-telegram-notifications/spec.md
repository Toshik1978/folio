# Feature Specification: Telegram Notifications

**Feature Branch**: `feature/telegram-notifications`

**Created**: 2026-11-03

**Status**: Draft

**Input**: User description: "Telegram notifications: tell the Folio owner through a Telegram channel
when something genuinely goes wrong, without flooding. Folio runs unattended; today failures are
only noticed by chance in the UI or the logs."

## Clarifications

### Session 2026-11-03

- Q: Should identical repeated crashes each be a message, or be collapsed? → A: The first occurrence goes out immediately; identical crashes (same place, same error) within the next hour are counted and reported once as "repeated N more times".
- Q: Does a completed Deep Index run with a few per-book errors count as a failed run? → A: No. Only a run that could not complete (library unreachable, aborted) is a failed run; per-book errors in a completed run go into the few-minute error summary.
- Q: May the bot token be stored as plain text with the other settings? → A: Yes — plain text in the settings store in the data directory, never returned by the server and never logged.
- Q: May a message include file paths inside a library (for example in an error text)? → A: Yes. The channel is the owner's own: messages may carry library names, error texts including file paths inside a library, and book IDs or links to the instance; never book titles, book contents, credentials or tokens. Paths are not stripped.
- Q: When a book-reading crash during a run also leaves that book unreadable, is it reported twice? → A: Each fact once: the crash message reports the crash (where it happened, de-duplicated for an hour); the run's unreadable summary lists the book once with the reason "reading the file crashed the reader", because it is a recorded read failure. Neither goes into the few-minute error summary.
- Q: Are books found unreadable when someone opens a book outside any run reported? → A: No, out of scope. They are shown as unreadable in the application and logged as warnings; later runs skip them without reporting.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - The owner sets up notifications and confirms they arrive (Priority: P1)

The owner opens Settings, fills in the Telegram notifications section (bot token, channel, an
optional topic, silent or not), switches it on, saves, and presses "Send test message". A message
arrives in the channel and the page confirms delivery. The token is never shown again after saving.

**Why this priority**: Nothing else can reach the owner until delivery is configured and proven.

**Independent Test**: Configure the section against a test channel, press "Send test message",
see the message arrive and the page report success; reload the page and see the token masked.

**Acceptance Scenarios**:

1. **Given** an empty section, **When** the owner saves a bot token and channel and presses
   "Send test message", **Then** a test message arrives in that channel (in the given topic, if
   set; without sound, if Silent is on) and the page reports delivery succeeded.
2. **Given** saved settings, **When** the page is reloaded, **Then** the token field shows only that
   a token is stored (masked, with its last few characters at most) and the server never returns
   the full token.
3. **Given** saved settings, **When** the owner types a new token and saves, **Then** the new token
   replaces the old one; **When** the owner saves without touching the token field, **Then** the
   stored token is kept.
4. **Given** a wrong channel or token, **When** the owner presses "Send test message", **Then** the
   page reports that delivery failed with Telegram's error text, and the status line shows it as the
   last failure.
5. **Given** a configured section, **When** the owner switches notifications off, **Then** no message
   is sent for any event and nothing is collected for later; the settings stay as they were.
6. **Given** the bot token or channel is missing, **When** the owner tries to switch notifications on,
   **Then** the page explains both are required and notifications stay off.

---

### User Story 2 - The owner hears about crashes and failing libraries (Priority: P1)

When the application recovers from a crash, the owner gets a message right away. When a library's
sync or bulk description indexing (Deep Index) starts failing (for example, its storage is unreachable), the
owner gets one message; while it keeps failing, nothing more; when the next run for that library
succeeds, one "recovered" message.

**Why this priority**: These are the failures that silently stop the catalog from working.

**Independent Test**: Force a recovered crash and see one immediate message; make a library
unreachable, run its sync three times and see exactly one "failed" message; restore it, run once,
and see one "recovered" message.

**Acceptance Scenarios**:

1. **Given** notifications are on, **When** any part of the application (book-file reading, the
   database write path, the sync engine, web request handling) recovers from a crash, **Then** one
   message is sent immediately saying what crashed and where.
1a. **Given** a crash was just reported, **When** the identical crash (same place, same error)
   recurs within the next hour, **Then** no new message is sent per occurrence; one follow-up
   message reports "repeated N more times" when that hour ends.
2. **Given** a library whose last run succeeded, **When** its sync or Deep Index run fails,
   **Then** one "failed" message names the library, the kind of run, and the reason.
3. **Given** a library already reported as failing, **When** further runs fail, **Then** no further
   message is sent for it.
4. **Given** a library reported as failing, **When** its next run succeeds, **Then** one "recovered"
   message is sent and the library counts as healthy again.
5. **Given** the application restarts while a library is failing, **When** the next run fails,
   **Then** one "failed" message may be sent again (the failing memory is not kept across restarts).

---

### User Story 3 - The owner gets one summary instead of a flood (Priority: P2)

At the end of a run, newly unreadable book files arrive as a single summary. A sync that removes a
large share of a library raises an alert. Other errors that happen outside runs (for example, a
failing web request) are grouped into one message per few minutes.

**Why this priority**: Volume control is what keeps the channel useful; without it the owner mutes it.

**Independent Test**: Run Deep Index over a library with seven newly broken files and see one message
listing the count and five of them; re-run and see no message; remove a large part of a library
and sync, and see one removal alert; trigger several errors within a few minutes and see one
grouped message.

**Acceptance Scenarios**:

1. **Given** a run that finds newly unreadable book files, **When** the run ends, **Then** one
   summary message gives their count and lists the first five with their reasons.
2. **Given** books already recorded as unreadable, **When** later runs skip them, **Then** they are
   never reported again.
3. **Given** a library, **When** one sync removes at least 10% of its books and at least 20 books,
   **Then** one alert gives the library, the number removed and the share of the library; the
   removal itself proceeds as before.
4. **Given** a sync that removes fewer than 20 books, or less than 10% of the library, **When** it
   ends, **Then** no removal alert is sent.
5. **Given** several errors outside runs within one grouping window, **When** the window closes,
   **Then** one message summarises them (count plus the first few), and nothing is sent for a window
   with no errors.

---

### User Story 4 - The owner can see whether delivery is working (Priority: P3)

The settings section shows when a message was last delivered and, if the last attempt failed, when
and why. When Telegram is unreachable or limits the bot, the application keeps working normally.

**Why this priority**: Notifications that silently stop are worse than none; the status line makes
that visible without notifying about notifications.

**Independent Test**: Point the section at an unreachable endpoint (or a wrong token), trigger an
event, and see the status line report the failure while the rest of the application keeps working.

**Acceptance Scenarios**:

1. **Given** a delivered message, **When** the owner opens Settings, **Then** the status line shows
   the time of the last successful send.
2. **Given** Telegram is unreachable or rate-limiting, **When** a message is due, **Then** it is
   retried a few times with increasing delay, then dropped, and the status line shows the time and
   Telegram's (or the network) error as the last failure.
3. **Given** delivery is failing, **When** the application keeps running, **Then** no request, sync
   or Deep Index run waits on or fails because of notifications, and no notification is sent about the
   failed delivery itself.

---

### Edge Cases

- Notifications switched off or not configured: events are neither sent nor queued for later;
  switching on later does not replay past events.
- A burst of crashes (for example, the same failing request repeated): only the first occurrence of
  an identical crash is sent; repeats within the hour are counted into one follow-up. Different
  crashes are each sent, spaced to stay within Telegram's limits; overflow beyond a bounded queue
  is dropped and counted as the last failure rather than growing without limit.
- A message longer than Telegram allows: it is shortened, keeping the header and the first items.
- The application's public address is not configured: messages are prefixed with a neutral
  instance label instead.
- Settings change while messages are pending: pending messages use the settings in force when they
  are sent.
- A library is deleted while reported as failing: its failing memory is dropped; no "recovered"
  message is sent.
- An error that is part of a run's outcome (a failed run, an unreadable file) is reported only
  through that run's message, not again in the few-minute error summary.
- A completed run with a few per-book errors is not a failed run: it sends no "failed"
  message, and it counts as a success for the "recovered" message; its error count appears as one
  line in the next few-minute error summary.
- Errors raised while sending a notification are never themselves notified.
- A book-reading crash during a run is reported once as a crash (de-duplicated for an hour) and
  the book is listed once in that run's unreadable summary; it is not repeated in the few-minute
  error summary.
- A book found unreadable when it is opened outside any run is not notified; later runs skip it
  as already recorded.
- Message text never contains the bot token, credentials, or authentication headers.

## Requirements *(mandatory)*

### Functional Requirements

**Settings**

- **FR-001**: The Settings page MUST have a "Telegram notifications" section, below the OPDS
  section, with: bot token (required), channel ID (required), topic ID (optional), a Silent option,
  an on/off switch, a "Send test message" action, and a delivery status line.
- **FR-002**: The bot token MUST never be returned by the server after it is saved; the page shows
  only that a token is stored (at most its last four characters). Saving with an untouched token
  field MUST keep the stored token; entering a new value MUST replace it.
- **FR-003**: Notifications MUST NOT be switched on unless a bot token and channel ID are stored.
- **FR-004**: "Send test message" MUST send one message using the saved settings, report success or
  Telegram's error text to the owner, and update the status line.
- **FR-005**: Settings MUST be stored with the application's other settings in its data directory
  and survive restarts. The bot token is stored as plain text there (like the other settings) and
  MUST never be returned by the server or written to the logs.

**What is notified**

- **FR-006**: A recovered crash anywhere in the application (book-file reading, the database write
  path, the sync engine, web request handling) MUST produce one message, sent without waiting for a
  grouping window, naming what crashed and where. Identical crashes (same place, same error)
  within one hour of the first MUST NOT each produce a message; they MUST be counted and reported
  once, at the end of that hour, as "repeated N more times" (no follow-up when there were none).
- **FR-007**: A run counts as failed only when it could not complete (for example, the library is
  unreachable or the run aborted); a completed run with per-book errors is not a failed run. A
  failed library sync or Deep Index run MUST produce one "failed"
  message when the library was not already reported as failing; while it stays failing, further
  failures MUST NOT produce messages; the next successful run for that library MUST produce one
  "recovered" message. This memory is kept only while the application runs.
- **FR-008**: At the end of a run that recorded newly unreadable book files, one summary message
  MUST give their count and list the first five with their reasons. Books already recorded as
  unreadable MUST NOT be reported again.
- **FR-009**: A sync that removes at least 10% of a library's books and at least 20 books MUST
  produce one alert with the library, the number removed and the share; the removal itself MUST
  proceed unchanged.
- **FR-010**: Other errors the application logs outside runs MUST be grouped into one message per
  grouping window of a few minutes (count plus the first few); errors already reported through
  FR-006 to FR-009 MUST NOT be repeated there. A completed run that had per-book errors MUST
  contribute one line to this summary (library, kind of run, error count).
- **FR-011**: Routine warnings, online-enrichment misses and failures to deliver notifications MUST
  NOT produce messages.

**Delivery**

- **FR-012**: Messages MUST be plain text in English, start with the instance's public address (or a
  neutral label when none is configured), and be sent to the configured channel and, when set, topic,
  honouring the Silent option.
- **FR-013**: When delivery fails or is rate-limited, the message MUST be retried a few times with
  increasing delay and then dropped; the last success and the last failure (time and error text)
  MUST be recorded for the status line.
- **FR-014**: Notifications MUST NOT block, slow down or fail any request, sync or Deep Index run, and
  MUST NOT crash the application; pending messages MUST be bounded.
- **FR-015**: When notifications are off or not configured, events MUST be neither sent nor queued.
- **FR-016**: Messages MUST NOT contain the bot token, credentials or authentication headers.
- **FR-017**: Online enrichment behaviour MUST stay unchanged.

### Key Entities

- **Notification settings**: bot token (secret), channel ID, optional topic ID, Silent flag, on/off
  flag; stored with the other application settings.
- **Delivery status**: time of the last successful send; time and error text of the last failure.
- **Library failing state** (in memory only): per library, whether its last run failed and was
  reported.
- **Notification**: one message — its kind (crash, run failed, run recovered, unreadable summary,
  mass removal, error summary, test), text, and delivery attempts.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A test message sent from Settings arrives in the configured channel within 10 seconds
  when Telegram is reachable.
- **SC-002**: A recovered crash reaches the channel within 1 minute; the same crash repeated any
  number of times within an hour produces at most 2 messages.
- **SC-003**: A library that fails on N consecutive runs and then succeeds produces exactly 2
  messages (one "failed", one "recovered") for any N ≥ 1.
- **SC-004**: A run that newly records any number of unreadable files produces exactly 1 summary
  message listing at most 5 files; a re-run that finds none new produces 0.
- **SC-005**: With Telegram unreachable, request latency and sync and Deep Index durations stay within 5%
  of the same operations with notifications switched off.
- **SC-006**: In 100% of messages and API responses checked, the bot token never appears.

## Assumptions

- The owner already has a Telegram bot and a channel or group the bot can post to; creating them is
  out of scope.
- One channel per installation; per-event routing, per-user subscriptions and other messengers are
  out of scope.
- The few-minute grouping window is 5 minutes; "first few" errors in a grouped message is 5; retries
  are 3 attempts over well under a minute. The planner may tune these within the same intent.
- The 10% / 20-book removal threshold is fixed, not configurable in this feature.
- Messages go only to the owner's own channel and may carry library names, error texts
  (including file paths inside a library) and book IDs or links to the instance; they never
  carry book titles, book contents, credentials or tokens (constitution v1.1.0, Principle III).
- Unreadable books recorded outside runs (when a book is opened) are out of scope for
  notifications; the application already shows them as unreadable.
- Messages are English only; no localisation.
- Existing Settings access control applies to the new section; only the owner configures it.
- No personal or deployment-specific details (library names, paths, hosts, tokens, chat IDs) appear
  in this specification.

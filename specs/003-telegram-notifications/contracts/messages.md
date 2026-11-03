# Contract: Message formats

All messages are plain text (no `parse_mode`), in English, sent with link previews disabled.

- **Line 1** is always the instance prefix: the configured `PUBLIC_URL` without a trailing `/`, or `Folio` when it is unset (FR-012).
- **Line 2** is the headline.
- Items are prefixed `- `.

Length rules (`format.go`):
- each dynamic item (error text, panic value, reason) is collapsed to one line and capped at 300 characters, with `…` appended when cut;
- the whole message is capped at 4000 characters. When cut, whole trailing item lines are dropped and `… (truncated)` is appended. The prefix and headline are never cut.

Placeholders below: `<instance>` is the prefix; `<library>` is the library name; `<kind>` is `sync` or `deep index`; `<N>`/`<M>`/`<P>` are numbers; `<book-ref>` is `<instance-url>/books/<id>` when `PUBLIC_URL` is set, else `book #<id>`; `<reason>` is a reason label from the table at the end.

Content rule (constitution v1.1.0, Principle III exception; research R12): messages go to the owner's own channel and **may** carry library names, error texts **including file paths inside a library** (for example in an I/O error), and book IDs or links to the instance. Paths are not stripped from error texts.

Messages never contain the bot token, credentials, request headers, raw request paths or queries, book titles, authors, descriptions, book contents, or per-book read detail (FR-016).

## Test (`KindTest`), FR-004

```text
<instance>
Test message: Telegram notifications are working.
```

## Crash (`KindCrash`), FR-006, sent at once

```text
<instance>
Crash recovered in <place>
Where: <detail>
At: <pkg.Func> (<file.go>:<line>)
Error: <panic text>
Identical crashes in the next hour will be counted, not sent.
```

- `<place>` is one of `book-file reading`, `database write path`, `sync engine` or `web request handling`.
- For a `database write path` crash, `Where:` reads `via <outer place> (<outer detail>)`. So does a `book-file reading` crash re-raised from a Deep Index worker (`ingest.WorkerPanic`), e.g. `via sync engine (library "<library>" deep index)`.
- Engine work outside a run reports as `sync engine` with `Where:` naming the task: `library "<library>" post-sync fill`, `purge sweep`, `library #<id> purge` or `folder watcher`.
- `At:` is omitted when no frame inside the module is found.

Example (web request):

```text
https://folio.example.com
Crash recovered in web request handling
Where: GET /api/books/{id}
At: api.(*Books).get (internal/api/books_handler.go:<line>)
Error: runtime error: invalid memory address or nil pointer dereference
Identical crashes in the next hour will be counted, not sent.
```

Example (write path inside a sync):

```text
Folio
Crash recovered in database write path
Where: via sync engine (library "<library>" sync)
At: ingest.(*importer).flush (internal/ingest/importer.go:<line>)
Error: <panic text>
Identical crashes in the next hour will be counted, not sent.
```

## Crash repeated (`KindCrashRepeat`), FR-006, at the end of the hour

```text
<instance>
Crash in <place> repeated <N> more times in the last hour
Where: <detail>
Error: <panic text of the first occurrence>
```

It is sent only when N ≥ 1: at the first 30 s tick after the hour ends, or at once when the same crash recurs after the hour but before that tick (the new occurrence then opens a new hour).

## Run failed (`KindRunFailed`), FR-007

```text
<instance>
Library "<library>": <kind> failed
Reason: <error text>
No further messages for this library until a run succeeds.
```

## Run recovered (`KindRunRecovered`), FR-007

```text
<instance>
Library "<library>": recovered
The last <kind> completed successfully.
```

## Newly unreadable books (`KindUnreadable`), FR-008

```text
<instance>
Library "<library>": <kind> found <N> newly unreadable book file(s)
- <book-ref>: <reason>
- <book-ref>: <reason>
- <book-ref>: <reason>
- <book-ref>: <reason>
- <book-ref>: <reason>
…and <N-5> more. They will not be reported again unless the files change.
```

- At most 5 items. The `…and` line appears only when N > 5.
- For a sync, `<kind>` is `sync` (unreadable books found by its post-sync fill).
- A book whose read crashed the reader is listed here once with the `crashed` label, in addition to the crash message for the code fault (each fact once; see contracts/notifier.md).
- Books recorded unreadable outside runs (when a book is opened) are never listed.

## Mass removal (`KindMassRemoval`), FR-009

Sent when `removed >= 20` and `removed / books_before >= 10%` in one sync.

```text
<instance>
Library "<library>": sync removed <N> of <M> books (<P>%)
The removal has been applied. If this was not expected, check that the library source is complete and mounted.
```

- `<P>` is rounded down to a whole percent.

## Error summary (`KindErrorSummary`), FR-010

Sent when a 5-minute window that had at least one entry closes. On shutdown an open window is flushed early, with the same headline.

```text
<instance>
<N> error(s) in the last 5 minutes
- <HH:MM:SS> <log message>: <error text>
- <HH:MM:SS> <log message>
- Library "<library>": <kind> completed with <K> book(s) left for retry
- Library "<library>": sync completed, but filling descriptions for new books failed: <error text>
- <HH:MM:SS> <log message>: <error text>
…and <N-5> more. See the server log for details.
```

- Times are in the server's local time zone, the same as the log output.
- Log entries carry only the record message and its `error` attr; other attributes are not used. The `error` text is sent as is (capped), so it may contain file paths inside a library.
- Run lines count toward N like any other entry.

## Reason labels

| `ebook.Reason` | Label |
|---|---|
| `corrupt` | `file is damaged or not in the expected format` |
| `missing_entry` | `file is missing from its archive` |
| `too_large` | `file is too large to read` |
| `crashed` | `reading the file crashed the reader` |
| any other value | the raw value |

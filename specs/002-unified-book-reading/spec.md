# Feature Specification: Unified Book Reading

**Feature Branch**: `feature/unified-book-reading`

**Created**: 2026-11-02

**Status**: Draft

**Input**: User description: "One consistent way to read book files for every format and library type, explicit read outcomes with re-check rules, and UI for unreadable books. A processed item must not be reprocessed unless we are confident a retry can turn out differently. Everything must be generalized; avoid folder/Calibre/INPX special cases where possible."

## Context

Folio reads a book's own file for three things: its metadata at import (folder libraries), its
description on demand (first book view and the bulk Deep Index), and its cover on first display.
Today these paths differ by library type and by format:

- The reading layer exposes one format-specific shortcut (a fast header-only description read for
  one format) next to its general format-routing abstraction, and the bulk indexer picks the
  shortcut by format name. Archive-based collections can hold any format, so this is both a leak
  and a gap.
- Books stored inside archives are first copied to a temporary file before they can be read.
- Folder libraries take their cover during import, while archive-based libraries take it lazily on
  first display.
- Every local read failure is retried. A permanently broken file is therefore re-read on every
  bulk run and every view, forever, and the user never learns why such a book has no description
  or cover. The online enrichment path, by contrast, already records "no match" and retries only
  transient failures.

This feature makes reading uniform, records outcomes so broken files are not re-read without
reason, and shows the user which books are unreadable and why.

## Clarifications

### Session 2026-11-02 (brainstorming)

- Q: How should formats be read? → A: Every format reader reads from the same kind of
  random-access source, whether the book is a plain file or an entry inside an archive. Each
  reader offers two separate operations: read metadata (never decodes covers) and read cover.
- Q: How are archive entries read? → A: In memory, decompressed lazily, only as far as the reader
  actually reads, with a size cap. No temporary files.
- Q: PDFs? → A: Keep ignoring them for descriptions, as today; they carry nothing usable.
- Q: Covers for folder libraries? → A: Same lazy cover flow as every other library type: import
  reads metadata only; the cover is read on first display. Two read cycles per book are accepted.
  Calibre keeps using its own cover image file.
- Q: Which outcomes may be retried? → A: A local read ends as done, broken (recorded, not
  retried) or retryable, mirroring the online enrichment path.
- Q: When is a broken book re-checked? → A: When its file changed or a newer Folio reader version
  exists. An unreachable library or an interrupted run is retryable, never broken.
- Q: Where are failures recorded? → A: In one shared read-failures record per book and flow
  (metadata or cover), with the reason, the file fingerprint and the reader version. Existing
  per-book flags keep their meaning.
- Q: What does the user see? → A: (A) a "⚠ N unreadable" link on each library row in Settings →
  Libraries that opens the book list filtered to those books; (B) a plain-language notice on the
  book page. No retry button. Keep it simple.

### Session 2026-11-02

- Q: Should a book whose cover can't be extracted, though its file is otherwise readable, count
  as "unreadable"? → A: No. Only metadata-flow failures count and get the notice; cover-flow
  failures are recorded silently (so the file is not re-read) and the placeholder is shown.
- Q: How much slower may a full Deep Index run get before it counts as a regression? → A: At
  most 10% slower than the previous release on the same host and catalog.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Every book is read the same way, whatever its format or library (Priority: P1)

A user runs Deep Index, opens a book page, or imports a folder library. Whatever format the book
has and wherever it is stored (a plain file, an entry inside an archive, a Calibre library),
Folio reads it through the same path, with the same result quality and without temporary files.
Archive collections that contain EPUB or MOBI books get descriptions exactly like FB2 books.

**Why this priority**: It is the foundation for the other two stories (outcomes are classified in
one place) and removes the format leaks found in review. It must not make Deep Index slower.

**Independent Test**: Build a test archive collection holding FB2, EPUB, MOBI and PDF entries;
run Deep Index; FB2, EPUB and MOBI books gain descriptions, PDF books are marked read without a
description; no temporary file is created under the data directory; the bulk indexing code
contains no format names.

**Acceptance Scenarios**:

1. **Given** an archive collection with an EPUB entry that has a description, **When** Deep Index
   runs, **Then** that book's description is stored and searchable.
2. **Given** an archive entry of a format whose metadata sits at the start of the file, **When**
   only its description is needed, **Then** Folio decompresses only the start of the entry, not
   the whole entry.
3. **Given** a PDF-only book, **When** a description is requested, **Then** the book is marked
   read without the PDF being parsed, as today.
4. **Given** a folder library being imported, **When** a book is added, **Then** its metadata is
   stored without reading its cover, and the cover appears on its first display.
5. **Given** an archive entry larger than the size cap, **When** Folio tries to read it,
   **Then** the read fails cleanly as a broken outcome without exhausting memory.

---

### User Story 2 - Broken files are not re-read without reason (Priority: P1)

A user runs Deep Index repeatedly. Books whose files are corrupt are tried once, recorded as
unreadable with a reason, and skipped on later runs and later views, until their file changes or
Folio ships a better reader. Books that failed only because the library was unreachable or the
run was interrupted are retried next time.

**Why this priority**: It is the defect the user reported: the same broken books fail on every
run, waste work and produce noise.

**Independent Test**: Index a library containing one corrupt entry; the first run records it as
unreadable; a second run does not open it and reports zero errors; replacing the file (new
fingerprint) makes the next run re-read it; unmounting the library during a run leaves its books
retryable, not unreadable.

**Acceptance Scenarios**:

1. **Given** a book whose file is corrupt, **When** Deep Index reads it, **Then** it is recorded
   as unreadable for descriptions with a reason, and the run's summary counts it once.
2. **Given** a book recorded as unreadable, **When** Deep Index runs again or the book page is
   opened, **Then** the file is not read again and no new error is logged.
3. **Given** a book recorded as unreadable, **When** its file's fingerprint changes, **Then** the
   next description request or run reads it again.
4. **Given** a book recorded as unreadable by an older reader version, **When** Folio runs with a
   newer reader version, **Then** the book is read again.
5. **Given** the library's storage is unavailable during a read, **When** the read fails,
   **Then** nothing is recorded as unreadable and the book is retried later.
6. **Given** a run is cancelled mid-way, **When** it stops, **Then** books not yet read stay
   pending and nothing is recorded as unreadable because of the cancellation.
7. **Given** a book's cover cannot be decoded from a readable file, **When** it is displayed,
   **Then** the cover failure is recorded once for the cover flow (independently of the
   description flow) and the placeholder is shown without re-reading the file on every view.

---

### User Story 3 - The user sees which books are unreadable and why (Priority: P2)

In Settings → Libraries each library row shows a small warning link, "⚠ N unreadable", under the
book count when N > 0. Clicking it opens the book list filtered to those books. On a book's page,
where the description would be, a short plain-language notice explains that Folio couldn't read
the file and why, and that downloading may still work.

**Why this priority**: It turns a silent failure into something the user can act on (replace the
file), but the system works correctly without it.

**Independent Test**: With one unreadable book in a library, the library row shows "⚠ 1
unreadable"; the link lists exactly that book; its page shows the notice with the reason; after
the file is replaced and re-read successfully, the link and the notice disappear.

**Acceptance Scenarios**:

1. **Given** a library with unreadable books, **When** the user opens Settings → Libraries,
   **Then** the row shows "⚠ N unreadable" with the current count.
2. **Given** the user clicks that link, **When** the book list opens, **Then** it lists exactly
   the library's unreadable books.
3. **Given** a library with no unreadable books, **When** the row renders, **Then** no warning is
   shown.
4. **Given** an unreadable book, **When** the user opens its page, **Then** a notice explains in
   plain words that its file couldn't be read, gives the reason category, and says downloading
   may still work.
5. **Given** a book recorded unreadable whose record no longer applies (file changed or newer
   reader), **When** its page or the library row renders, **Then** it is not shown as unreadable.

---

### Edge Cases

- An archive is missing or cannot be opened (moved, unmounted, permission error): retryable for
  every book in it, never recorded as unreadable.
- An entry listed in the index no longer exists inside a readable archive: recorded as unreadable
  ("missing from archive") — retrying cannot help until the archive changes.
- An archive opens but is not a valid archive (its directory is damaged): recorded as unreadable
  ("corrupt") for the books read from it, until the archive changes.
- A file is readable but contains no description: done, not unreadable (no notice, no count).
- A file is readable but the cover is absent: done for covers (placeholder), not unreadable.
- A reader crashes on hostile input: treated as broken for that flow, never crashes Folio.
- A book has several files: it is unreadable for a flow only if no usable file could be read.
- A book is deleted from its library: its read-failure records go with it.
- The library is purged: its records go with it; counts never include books of other libraries.
- Unsupported formats (no reader exists): marked read with nothing found, not unreadable.

## Requirements *(mandatory)*

### Functional Requirements

**Uniform reading (Phase 1)**

- **FR-001**: Every format reader MUST read from one common random-access source type, whether
  the book is a plain file or an archive entry. No reader MAY require a file path.
- **FR-002**: Every format reader MUST offer two separate operations: read metadata (which MUST
  NOT decode cover images) and read cover.
- **FR-003**: Format selection, crash protection and cancellation checks MUST happen in one
  routing component for both operations.
- **FR-004**: The format-specific fast description read MUST become an internal detail of that
  format's metadata operation; no format-specific function MAY be public.
- **FR-005**: Archive entries MUST be read in memory, decompressing lazily only as far as the
  reader reads, with a configurable-by-code size cap; no temporary files MAY be written to read a
  book.
- **FR-006**: Code outside the reading layer (import, Deep Index, on-view fill, cover
  extraction) MUST NOT name formats; it MAY only ask the reading layer capability questions
  about a file or stored format (is it supported, its format label, its preference rank, whether
  it can carry a description).
- **FR-007**: Descriptions MUST NOT be read from PDFs (they are marked read with nothing found),
  as today.
- **FR-008**: Folder import MUST read metadata only; covers for every library type MUST come from
  the same lazy cover flow on first display, except where a library provides its own cover image
  file (Calibre), which keeps being used.
- **FR-009**: Deep Index throughput MUST NOT regress compared to the previous release on the same
  catalog (see SC-001, SC-002).

**Read outcomes and re-checks (Phase 2)**

- **FR-010**: Each local read for a flow (metadata or cover) MUST end in exactly one of: done,
  broken, or retryable.
- **FR-011**: Broken MUST cover failures that retrying the same file with the same reader cannot
  fix: malformed or corrupt content (including an archive that opens but is not a valid archive),
  an entry missing from a readable archive, an entry, document or cover over its size cap, a
  reader crash.
- **FR-012**: Retryable MUST cover failures that may resolve on their own: the library or archive
  is unreachable, a permission or I/O error opening the source, cancellation or shutdown, a
  write-lock timeout.
- **FR-013**: A broken outcome MUST be recorded once per book and flow, with a reason category, a
  short detail, the file's fingerprint and the reader version, and MUST be logged once at a level
  visible in default logs.
- **FR-014**: A book with a broken record that still matches the file's current fingerprint and
  the current reader version MUST NOT be read again for that flow by Deep Index, the on-view
  description fill or the lazy cover flow.
- **FR-015**: A broken record MUST stop applying when the file's fingerprint changes or the reader
  version is newer; the next request or run then reads the book again, and a successful read
  removes the record.
- **FR-016**: Retryable outcomes MUST NOT be recorded as broken and MUST leave the book eligible
  for the next run or view.
- **FR-017**: Deep Index summaries MUST count broken outcomes recorded in that run separately from
  retryable errors; a re-run over unchanged files MUST report zero of either for books already
  recorded broken.
- **FR-018**: The reader version MUST be a single value maintained in code and bumped when a
  reader change could turn a previously broken file into a readable one.
- **FR-019**: Online enrichment behavior MUST stay unchanged; Deep Index and the MCP endpoint MUST
  NOT trigger online calls.

**Visibility (Phase 3)**

- **FR-020**: The library list MUST expose, per library, the number of books currently
  unreadable. Only metadata-flow failures count; cover-flow failures are recorded but never
  counted or shown.
- **FR-021**: Settings → Libraries MUST show "⚠ N unreadable" under the book count when N > 0,
  linking to the book list filtered to that library's unreadable books.
- **FR-022**: The book list MUST support an "unreadable" filter scoped to a library.
- **FR-023**: The book details MUST expose whether the book is currently unreadable (metadata
  flow only) and the reason category; the book page MUST show a plain-language notice where the description
  would be, including that downloading may still work.
- **FR-024**: There MUST be no manual retry control; re-checks happen only per FR-015.

**Housekeeping**

- **FR-025**: `.superpowers/` MUST be ignored by version control.

### Key Entities

- **Read source**: a book's bytes, readable at any position, with a known size; backed by a plain
  file or an archive entry decompressed on demand.
- **Read flow**: what is being read — metadata (including description) or cover.
- **Read outcome**: done, broken or retryable, with a reason category for broken.
- **Read failure record**: book, flow, reason category, short detail, file fingerprint, reader
  version, time recorded. At most one per book and flow.
- **File fingerprint**: a cheap value that changes when a book's file changes (for example size
  and modification time of the file or archive entry).
- **Reader version**: a single integer identifying the current reading logic.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A full Deep Index of a large archive collection takes no longer than with the
  previous release on the same host and catalog, with at most 10% added time.
- **SC-002**: A repeated Deep Index over an already indexed library, including its unreadable
  books, finishes in under 1 minute and reports zero errors.
- **SC-003**: Archive collections' EPUB and MOBI books gain descriptions at the same rate as when
  those files are read individually.
- **SC-004**: After the first run, an unreadable book's file is opened zero times by later runs and
  page views until its file changes or the reader version changes.
- **SC-005**: Zero temporary files are written to read books.
- **SC-006**: The user can find every unreadable book of a library in two clicks from Settings.
- **SC-007**: No format name appears in the import, bulk indexing, on-view fill or cover
  extraction code.

## Assumptions

- File fingerprint = size + modification time, which every source type can provide cheaply;
  archive entries use their own recorded size and modification time plus the size and
  modification time of the archive file itself, so replacing a damaged archive with a good copy
  of the same release re-checks its books.
- The reader version starts at 1 and is bumped by developers as part of a reader fix; there is
  no automatic detection.
- The size cap for in-memory archive entries is a code constant generous enough for normal books
  (tens of MB) and low enough for low-spec hosts.
- Two read cycles (metadata at import, cover on first display) are acceptable for folder
  libraries; the first browse of newly imported books shows covers as they are extracted.
- Existing per-book flags (`read` for descriptions, has/none for covers) keep their meaning; the
  read-failure record is additional.
- Reason categories shown to users are few and plain (corrupt file, missing from archive, too
  large, reader crash); details stay in logs and the record.
- A record that no longer applies stops counting on the library row at the latest after the
  library's next sync (a scheduled sync skipped because the library index is unchanged re-checks
  too), the next Deep Index run, or the next view of the book; the book page re-checks on view.
- Calibre libraries keep using their own cover image files; they still use the shared flow for
  descriptions and outcomes.

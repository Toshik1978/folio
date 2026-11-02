# Feature Specification: Book Discovery for Claude.ai (Description Indexing + MCP)

**Feature Branch**: `feature/mcp-book-discovery`

**Created**: 2026-11-02

**Status**: Draft

**Input**: User description: "Let Claude.ai find books in my collection via an MCP server — e.g. 'find me all detectives about a smart killer and a stupid private detective', or 'books like this one' — and give me a link to each book. Most of the catalog has no indexed description today, so first index descriptions for the whole catalog from the local book files (never from online services), via an explicit button plus the regular hourly sync for new books."

## Context

A Folio catalog can hold large INPX collections (hundreds of thousands of books) next to
Calibre and folder libraries. Calibre and folder books usually carry descriptions already.
INPX indexes carry none, and Folio fills a book's description only when its page is first
opened, so almost no INPX book has one. A plot-level request therefore has nothing to match
for most of such a catalog.

In a sample of INPX book files, roughly four in five contain a description of about 1 KB.
Parsing them one at a time the way the page-view path does costs tens of milliseconds per
book, hours for a large library.

Folio is typically exposed through an authenticating reverse proxy (for example Cloudflare
Access), which can also act as the OAuth server for a remote MCP endpoint that Claude.ai
connects to.

## Clarifications

### Session 2026-11-02

- Q: Where should the description-indexing action appear in Settings? → A: Both — a per-library row button next to Sync Now / Re-index, and a global "… All" button next to Re-index All.
- Q: What should the new buttons be called? → A: "Deep Index" on each library row, "Deep Index All" globally.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Index all missing descriptions on demand (Priority: P1)

The user presses a new "Deep Index" button next to the existing Sync Now / Re-index controls for a library (or the global one for all
libraries).
Folio reads the description of every book in that library that has not been checked yet,
straight from the book's own file, stores it, and makes it full-text searchable. Progress is
visible live, like a sync. When it finishes, searching for a word that appears only in a
book's description finds that book.

**Why this priority**: Without descriptions, neither plot search in Claude nor the web UI can
match on content for 99.99% of the INPX library. Everything else builds on this.

**Independent Test**: On a library whose books have no stored descriptions, press the button;
afterwards the web UI search finds a book by a word from its description, and the book page
shows the description without having been opened before.

**Acceptance Scenarios**:

1. **Given** an INPX library whose books have never been checked, **When** the user presses
   the index button, **Then** every unchecked book's file is read once, its description (if
   any) is stored and searchable, and the book is marked checked whether or not a
   description was found.
2. **Given** the index run is in progress, **When** the user watches the sync status, **Then**
   they see the run's progress (books done / total) update live.
3. **Given** a library was fully indexed, **When** the user presses the button again, **Then**
   no book is parsed again and the run finishes almost immediately.
4. **Given** the index run is in progress, **When** a sync of the same library is requested,
   **Then** the two never run at the same time; the later one waits its turn.
5. **Given** any index run, **When** it executes, **Then** no online service is contacted and
   no source file or archive is modified.

---

### User Story 2 - New books get descriptions automatically (Priority: P2)

When the regular scheduled sync adds new books to a library, Folio fills their descriptions in
the same run, so newly arrived books are searchable by content without anyone pressing the
button. The scheduled sync does not touch the existing backlog.

**Why this priority**: Keeps the index complete after the one-time backlog run, with no
manual upkeep.

**Independent Test**: Add a new book to an indexed INPX library, let the scheduled sync run,
and find the new book by a word from its description.

**Acceptance Scenarios**:

1. **Given** a sync run adds new books, **When** the run completes, **Then** those new books
   have been checked and their descriptions are searchable.
2. **Given** a library with an unindexed backlog, **When** a scheduled sync runs that adds no
   books, **Then** no backlog books are parsed.

---

### User Story 3 - Find books by description from Claude.ai (Priority: P1)

The user adds Folio as a connector in Claude.ai and asks in plain language, e.g. "find me
detectives about a smart killer and a stupid private detective". Claude searches Folio by
words and word stems, narrows by genre and language, reads the candidates' descriptions, and
answers with the books that fit, each with a link that opens the book's page in Folio.

**Why this priority**: This is the outcome the user asked for.

**Independent Test**: With a connector configured, ask Claude a plot-level question whose
answer is a book known to be in the catalog; Claude returns it with a working link.

**Acceptance Scenarios**:

1. **Given** Claude is connected, **When** it searches with words and filters (genre,
   language, author, series, year, format, library), **Then** it receives matching books with
   title, authors, series, genres, language, year, a description excerpt and a link.
2. **Given** a query word in an inflected language, **When** Claude searches with a
   word-stem prefix (e.g. `murder*`), **Then** books whose descriptions contain any form of that word match.
3. **Given** a candidate book, **When** Claude asks for its details, **Then** it receives the
   full description, genres, authors, series and identifiers, plus the link.
4. **Given** Claude needs valid filter values, **When** it asks for the available genres and
   languages, **Then** it receives them with book counts.
5. **Given** any request from Claude, **When** Folio answers it, **Then** no online
   enrichment is triggered.

---

### User Story 4 - "Books like this" (Priority: P2)

The user asks Claude for books similar to one they name or that Claude just found. Folio
returns candidate books that share the book's genres, author or series, or that match the
most distinctive words of its description; Claude picks the ones that really fit.

**Why this priority**: The second discovery mode the user asked for; it reuses the search
and details capabilities.

**Independent Test**: Ask for books like a known book with a description; the candidates
include books that share its genre and distinctive themes, and exclude the book itself.

**Acceptance Scenarios**:

1. **Given** a book with a description, **When** Claude asks for similar books, **Then** it
   receives a bounded list of other books ranked by shared distinctive description words,
   each with the same summary fields and link as search results.
2. **Given** a book without a description, **When** Claude asks for similar books, **Then**
   it still receives candidates based on shared genres, author and series.

---

### Edge Cases

- A book file is missing from its archive, the archive is unreadable, or the file is corrupt:
  the book is skipped for this run without failing the run; it is not marked checked, so a
  later run retries it.
- A book's file has no description: it is marked checked and never re-parsed.
- PDF-only books: not parsed for descriptions (unchanged from today's behavior); marked
  checked so they are not revisited.
- The service shuts down mid-run: work done so far is kept; the next run continues with the
  books still unchecked.
- A sync adds or removes books while descriptions from an earlier run exist: descriptions of
  remaining books persist; removed books leave no stale search entries.
- A book page is opened while the index run is processing the same book: the book is parsed
  at most once more and never stored twice.
- `PUBLIC_URL` is not configured: links fall back to a relative path (`/books/{id}`) and the
  endpoint still works.
- A search query with invalid full-text syntax: Claude receives a clear error message it can
  correct, not a server failure.
- Very broad searches: results are paginated with a bounded page size.

## Requirements *(mandatory)*

### Functional Requirements

**Description indexing**

- **FR-001**: The system MUST fill missing book descriptions from the books' own local files
  only. It MUST NOT contact any online service while indexing.
- **FR-002**: The system MUST NOT modify any source file, archive or source database while
  indexing; all writes go to Folio's own data store.
- **FR-003**: The web UI MUST offer a per-library "Deep Index" action in each library row, next to the
  existing Sync Now and Re-index actions, that indexes every not-yet-checked book in that
  library, and a global "Deep Index All" action next to Re-index All that queues the same run for every
  library.
- **FR-004**: Each regular sync run MUST index the books it added in that run, and MUST NOT
  index other unchecked books.
- **FR-005**: The system MUST mark each successfully read book as checked, whether or not it
  had a description, using the same marker the page-view path uses, so no book is parsed
  twice by any path.
- **FR-006**: An index run MUST never run concurrently with a sync or another index run on
  the same library, and MUST report live progress through the existing sync status channel.
- **FR-007**: Indexed descriptions MUST be full-text searchable from the web UI search, the
  OPDS search and the MCP search.
- **FR-008**: Indexing the backlog of a library of ~500k books MUST complete within 1 hour
  on a typical multi-core home server with local disk.
- **FR-009**: The description text MUST be stored once, not duplicated between the book
  record and the search index.
- **FR-010**: The page-view path's online enrichment for a single opened book MUST keep
  working as today.

**MCP endpoint**

- **FR-011**: The system MUST serve a remote MCP endpoint at `/mcp` that Claude.ai can add as
  a custom connector.
- **FR-012**: The endpoint MUST NOT implement its own authentication; documentation MUST
  state that it requires an external authenticator, with Cloudflare Access (OAuth for MCP)
  as the worked example.
- **FR-013**: The endpoint MUST be read-only: no tool may change catalog data, settings, or
  trigger syncs or online enrichment.
- **FR-014**: The endpoint MUST offer a search tool combining full-text query (title,
  authors, series, description; prefix matching) with filters: library, genre, language,
  author, series, year range, format; with pagination and a bounded page size.
- **FR-015**: The endpoint MUST offer a book-details tool returning the full description,
  genres, authors, series and number, language, year, publisher, identifiers, formats.
- **FR-016**: The endpoint MUST offer a similar-books tool returning a bounded, ranked list of
  candidates for a given book, excluding that book, based on shared distinctive description
  words, genres, author and series.
- **FR-017**: The endpoint MUST offer a tool listing available genres and languages (with
  book counts), optionally per library, and a tool listing libraries.
- **FR-018**: Every book in any tool result MUST include a link to its page in the Folio web
  UI, built from `PUBLIC_URL` (relative `/books/{id}` when unset).
- **FR-019**: Tool descriptions MUST tell Claude how to search well: that matching is by
  exact words, that prefix queries (`stem*`) cover word forms (important for inflected languages), and
  that it should read details to judge plot-level fit.
- **FR-020**: Search result summaries MUST include a description excerpt short enough that a
  page of results stays small for Claude's context.

### Key Entities

- **Book**: an existing catalog entry; gains a populated description and an indexed search
  entry. Its "checked" marker records that its own file has been read.
- **Index run**: one execution of description indexing for one library (backlog or
  new-books-only), with progress (done / total) reported live.
- **MCP tool result**: a book summary (id, title, authors, series, genres, language, year,
  description excerpt, link) or a full book detail.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: After one backlog run, at least 75% of INPX books have a searchable description
  (samples show roughly four in five files carry one).
- **SC-002**: The backlog run for a ~500k-book library finishes in under 1 hour.
- **SC-003**: Pressing the button again on a fully indexed library finishes in under 1
  minute.
- **SC-004**: Folio's data store grows by no more than 1 GB after indexing the whole catalog.
- **SC-005**: A typical search from Claude returns in under 2 seconds.
- **SC-006**: For a plot-level request whose matching book is in the catalog and has a
  description, Claude returns it with a working link within a single conversation turn.
- **SC-007**: Zero online-service calls are made by index runs and MCP requests.

## Assumptions

- The user has `PUBLIC_URL` set to the externally reachable Folio address; otherwise links
  are relative.
- Cloudflare Access (self-hosted application with OAuth for MCP) in front of `/mcp` is
  configured by the operator; Folio adds no OAuth code.
- Folder libraries already store descriptions during sync and Calibre libraries already carry
  them; the backlog is mainly an INPX concern, but the action works for any library.
- Word-stem matching for inflected languages is done by Claude writing prefix queries; server-side
  stemming and embedding-based semantic search are out of scope for this iteration.
- The index action has no "force re-parse" variant in this iteration.
- Claude performs the semantic understanding and final re-ranking; Folio returns candidates.
- The new MCP library dependency (official MCP Go SDK) is approved per the project's dependency policy.

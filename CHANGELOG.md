# Changelog

What changed in each Folio release and why. Highlights are written by hand; the
commit lists under them are generated with [git-cliff](https://git-cliff.org)
via `TAG=vX.Y.Z task changelog`.

Versions follow [semver](https://semver.org). Commits follow
[Conventional Commits](https://www.conventionalcommits.org).

---

## v1.8.1 — 2026-11-07

A small fix release. In Chrome, the language filter showed a few languages as
bare codes (`ba`, `ce`, `cu`, `cv`, `ie`, `io`, `sah`) instead of their names.
The books were tagged correctly; Chrome's built-in language data just has no
English names for these languages, so the filter fell back to the code.

### Notable fixes

- **Every language in the filter now has a name.** Bashkir, Chechen, Church
  Slavic, Chuvash, Interlingue, Ido and Yakut now show by name in the language
  filter and on the book page. Folio supplies these names itself when the
  browser can't, and nothing changes for languages the browser already names.

### Bug Fixes

- [92462c9](https://github.com/Toshik1978/folio/commit/92462c93dea25a3571bfea8647ef460c32bc21a2) fix(web): name languages missing from Chrome's ICU data

---

## v1.8.0 — 2026-11-07

This release makes **Folio safe to run with a container memory limit**. Opening
a page of books whose covers Folio had never shown before (a fresh tag filter
over a large INPX library, say) made it pull every cover out of its archive and
decode it at the same time. That briefly took close to 1 GB of memory, so a
container capped at 1 GB was killed and restarted, and the page went blank.
The same page now peaks under 300 MB and loads in about the same time.

### Highlights

- **Covers are extracted at most four at a time.** Covers Folio has already
  cached are served immediately, as before. Only first-time extraction is
  queued, and a request the browser has abandoned leaves the queue.
- **Folio sizes its memory use to its container.** When the container has a
  memory limit, Folio tells the Go runtime to stay under 90% of it, so memory
  is reclaimed harder as the limit approaches instead of the process being
  killed. Setting `GOMEMLIMIT` yourself overrides this. Without a limit, or
  outside a Linux container, nothing changes.

### Notable fixes

- **The books grid no longer goes blank when a reload fails.** After a sync
  finishes, the grid now keeps the current books on screen until the fresh list
  arrives, and keeps them if the request fails. A filter whose first page fails
  to load shows "Couldn't load books" with a **Retry** button instead of an
  empty page.
- **No more manifest CORS error behind Cloudflare Access.** The web app
  manifest is now requested with your login cookie, so Access no longer
  redirects it to its sign-in page on every page load.

### Features

- [1f7f2ee](https://github.com/Toshik1978/folio/commit/1f7f2ee63b30dd659d0a0e6f421670e713d87998) feat: size the Go memory limit from the container's cgroup

### Bug Fixes

- [df8977b](https://github.com/Toshik1978/folio/commit/df8977b40bf949039c52ed88fe98b91c00ead25c) fix(covers): extract at most 4 cold covers at once
- [aa3327f](https://github.com/Toshik1978/folio/commit/aa3327f9adca9d950d9b8a837be383ae0483b4b2) fix(web): keep the books grid until a reload succeeds
- [f83857c](https://github.com/Toshik1978/folio/commit/f83857c247eae7104f089dc93fc0c7d08b28210d) fix(web): fetch the web manifest with credentials

### Others

- [eea4131](https://github.com/Toshik1978/folio/commit/eea4131e98451e032f71896f8b45aed0e06cfb0f) docs: document the cover extraction cap, memory limit and grid reloads

---

## v1.7.2 — 2026-11-04

A one-fix release: **search works from a phone**. On mobile, typing a
query and pressing the keyboard's → (Go) key did nothing, so the search bar
was effectively unusable there.

### Notable fixes

- **The mobile keyboard's Go key runs the search.** The search bar only
  reacted to an Enter key press, which Android keyboards often do not report
  as such. Some send an unnamed key code instead, and others send it while the
  word just typed is still underlined as unfinished, before the search bar has
  seen it. Either way the query was dropped. The search bar now runs on the
  keyboard's submit action and reads the text straight from the box, so both
  cases work. The keyboard also shows a search key in place of the plain
  arrow. Desktop behaviour is unchanged.

### Bug Fixes

- [07f73d2](https://github.com/Toshik1978/folio/commit/07f73d20df6943225554437d2217efc89855f0ed) fix(web): submit search from mobile keyboards

---

## v1.7.1 — 2026-11-04

A small follow-up to v1.7.0. Its new unreadable-book records showed which
files Folio could not read, and on a large flibusta library most of them turned
out to be one problem Folio could fix: **FB2 files saved as UTF-16**. This
release reads them, so their descriptions and covers become available.

### Notable fixes

- **UTF-16 FB2 files are readable.** Older archives contain FB2 files saved as
  UTF-16 with a byte order mark, which Folio reported as corrupt
  ("invalid UTF-8" on line 1). It now recognises the byte order mark and
  converts the file before parsing, trusting the mark over the encoding the
  file declares — some of these files wrongly declare UTF-8. On one
  580,000-book catalog this fixed 50 of the 53 unreadable books, and 39 of them
  carry a description. The other three are genuinely broken and stay reported
  as corrupt.

### Robustness

- **Faster multi-arch release builds.** The arm64 image no longer builds the
  frontend under emulation; it is built once and shared by both architectures.
  This changes nothing in the image itself.

### Upgrading

- **Run Deep Index once on affected libraries.** Books recorded as unreadable
  by v1.7.0 are re-read under the new reader instead of being skipped. Until
  that happens they drop out of the unreadable count and filter, so the count
  may briefly read lower than it will settle at. A Deep Index run re-reads
  them in seconds, since they are the only books it still has to check, and
  fills the recovered descriptions.

### Bug Fixes

- [baf20f0](https://github.com/Toshik1978/folio/commit/baf20f09cdbf814bf2b497a6eee0df6bf06833d5) fix(ebook): read UTF-16 FB2 files by their byte order mark

### Others

- [599a414](https://github.com/Toshik1978/folio/commit/599a4142741da4b8bbed7abdee5b41d9df455fed) build: build the frontend natively for multi-arch images

---

## v1.7.0 — 2026-11-03

This release is about **knowing what is in your library and hearing when
something goes wrong with it**. Folio now reads descriptions straight out of
your book files when the catalog has none, tells you which books it cannot read
and why, can let an AI assistant search the catalog for you, and can message you
on Telegram when a sync fails or the server crashes.

Underneath, every format — EPUB, FB2, MOBI and PDF — is now read through one
code path, whether the file sits in a folder or inside a ZIP or INPX archive.
That path is hardened against malformed files, which is what makes the
unreadable-book records trustworthy: a broken file is reported with a reason
instead of failing differently depending on where it lives.

### Highlights

- **Deep Index fills in missing descriptions from the books themselves.** INPX
  catalogs and many retail files carry no annotation in the index. Deep Index
  opens the book file and takes the description from it — on demand per library
  or for all libraries from Settings, and automatically for newly imported books
  after each sync. It reads only local files and never writes to them.
- **Unreadable books are visible.** A file Folio cannot read is recorded once,
  with the reason (corrupt, too large, missing from its archive, or crashed the
  reader), and re-checked only when the file changes. Book details show why,
  the search bar has an **Unreadable** filter chip, and the Settings library
  table links each library's unreadable count straight to that filter.
- **An MCP endpoint lets Claude.ai search your library.** `/mcp` serves a
  read-only Model Context Protocol server with five tools, including
  `search_books` and `similar_books` ("books like this one", matched on
  descriptions). Every tool is read-only and makes no outbound calls. See the
  upgrade note below before exposing it.
- **Telegram notifications.** Settings gains a Telegram section — bot token,
  chat, optional topic, and a test send. Folio then messages you when it
  crashes (reported on the next start), when a sync run fails or recovers, when
  new unreadable books appear, and when a sync removes many books at once. Other
  errors are batched into one summary every five minutes rather than sent one by
  one. Notifications are off until you configure them.
- **A smaller search index.** The full-text index no longer stores a second copy
  of every title, author, series and description; it keeps only what search
  needs. Same queries, same ranking.

### Notable fixes

- **Saving OPDS credentials during a sync no longer fails with a 500.** It now
  waits its turn like every other settings write, and answers `503 busy, try
  again` if a sync holds the database for too long.

### Upgrading

- **`/mcp` has no authentication of its own.** If Folio is reachable from
  outside, make sure your authenticator covers `/mcp` before you upgrade — an
  Access policy scoped to the web UI paths alone leaves it open, and anyone who
  can reach it can search every book's metadata. For Claude.ai you also need an
  authenticator that speaks OAuth for MCP clients, such as Cloudflare Access;
  `docs/NETWORKING.md` walks through the setup. Set `PUBLIC_URL` so result links
  are absolute.
- **The first start after upgrading rebuilds the search index.** A schema
  migration re-tokenizes every book once, before Folio begins serving, so expect
  a pause proportional to catalog size. Back up `/data` first
  (`sqlite3 /data/folio.db "VACUUM INTO '/backup/folio.db'"`) and leave disk
  headroom: the old and new index coexist until the rebuild finishes.
- **Deep Index is not run over your existing books automatically.** Only books
  imported after the upgrade are covered on their own; run **Deep Index All**
  from Settings once to backfill the rest.

### Features

- [c3b3168](https://github.com/Toshik1978/folio/commit/c3b31688590a0725ba6ad309a3f2d7b52dc603aa) feat: Deep Index — fill book descriptions from local files
- [3eb4432](https://github.com/Toshik1978/folio/commit/3eb44321f411256c6f6117b1528e0952c89a7cff) feat: read every book format through one random-access source
- [4553286](https://github.com/Toshik1978/folio/commit/455328676d9e846e7d90902668856e3c7dbe87e1) feat: record unreadable books and show which and why
- [87f96e9](https://github.com/Toshik1978/folio/commit/87f96e9f4b0f28171dbca1a3a0a14741308c9b35) feat: read-only MCP endpoint with search_books and similar_books
- [e5916ab](https://github.com/Toshik1978/folio/commit/e5916ab0cf900a4713219e9da8578ba61becfabc) feat: configure and test Telegram notifications
- [0df31df](https://github.com/Toshik1978/folio/commit/0df31df3da017ed12ec58a0af500a655567f68cb) feat: notify crashes, failed runs, unreadable books and mass removals
- [7856faa](https://github.com/Toshik1978/folio/commit/7856faa7aabde264d25c83b3b9efb7404104294f) feat: group other errors into a 5-minute summary

### Bug Fixes

- [e2bb276](https://github.com/Toshik1978/folio/commit/e2bb276573b310e69035c5388d2b904de2721ef8) fix(lint): drop unused revive nolint directive in metasearch (#54)
- [c547638](https://github.com/Toshik1978/folio/commit/c547638083824720051ad9e9225abb4d85cea116) fix: save OPDS credentials under the write guard

### Others

- [3fc9e1f](https://github.com/Toshik1978/folio/commit/3fc9e1fa64c8e06bfbcbb0cd4da300f357027f1c) build: bump Go to 1.27 and float the linter with CI
- [cc7be9d](https://github.com/Toshik1978/folio/commit/cc7be9d8d80c3328943da4e98f8e2dee1db13728) docs: drop the AI attribution rule
- [5bb2c0d](https://github.com/Toshik1978/folio/commit/5bb2c0debbb1046eadbc523ac4c5f9b76a763adf) build(beads): rebuild the beads DB on branch switch and require the merge driver
- [6323e54](https://github.com/Toshik1978/folio/commit/6323e54a2cb507467ad42da726a1b49ce53568ab) docs: move CLAUDE.md into .claude/
- [5874a55](https://github.com/Toshik1978/folio/commit/5874a554623242c5e53309fb25761bdef6fabd1f) docs: add Spec Kit and ratify constitution v1.0.0
- [ab16a29](https://github.com/Toshik1978/folio/commit/ab16a29b62bd6a4e4a6a9cd48fb9c04421fa7667) ci: group Dependabot security updates
- [d369b78](https://github.com/Toshik1978/folio/commit/d369b78de134363a5f64ed518cf6ec5e555c2657) ci: run CI weekly
- [7ecea16](https://github.com/Toshik1978/folio/commit/7ecea1656c5c69a444d2c9a0d9d00d75171296fc) docs: amend constitution to v1.1.0
- [c7c04b8](https://github.com/Toshik1978/folio/commit/c7c04b82507a377ee3daf76d6c5e15ca6ca60048) refactor: drop interface guards and group methods by receiver
- [9327ce9](https://github.com/Toshik1978/folio/commit/9327ce9cbe552a2558009dcd0ea1bc4ab99dff87) build: enforce Go source-layout conventions in lint
- [6258c2e](https://github.com/Toshik1978/folio/commit/6258c2ef136162967f47c1fa16f523d5c3fc3a61) docs: bring the docs in line with the code

---

## v1.6.1 — 2026-08-02

This release is about **how much of a sync is spent maintaining the search
index**, and it matters in proportion to how many books you have. Nothing else
about Folio changes: same features, same API, same search results.

Folio's full-text index is an SQLite FTS5 table, and FTS5 can look up a row only
by its rowid or by a text match — a condition on any other column has no index to
use. The index was keyed by an ordinary column, so writing one book's title,
authors, series, or annotation into it, or removing a deleted book from it, meant
scanning the whole index to find that single row. The cost of touching one book
therefore grew with the size of the entire catalog. On a small library this was
invisible; on a large one it quietly got worse as the library grew. On a
573,000-book INPX catalog, one index write measured 281ms and one book deletion
335ms, and a re-sync that pruned around 2,000 books spent 11 of its 31 minutes
inside the delete trigger alone.

### Highlights

- **Large catalogs sync substantially faster.** The index is now keyed by rowid,
  so every write seeks straight to its row instead of scanning. The same
  operations that took 281ms and 335ms now take 0.23ms and 0.37ms — roughly
  1,200x and 900x. The re-sync above gets about 11 minutes shorter, and the
  saving grows with the catalog rather than shrinking.
- **The problem was compounding.** Because each index write scanned the whole
  index, the per-book cost rose as books were added. That scaling is gone, not
  merely reduced, so this is worth more on the library you will have in a year
  than on the one you have today.
- **Search is unchanged.** Same queries, same BM25 ranking, same results. The
  full-text join also sheds a per-row type conversion it no longer needs.
- **Small libraries will not notice.** If your catalog is in the hundreds or low
  thousands, the old scan was already cheap. Nothing here regresses that case.

### Upgrading

- **The first start after upgrading pauses to rebuild the search index.** This
  runs once, as a schema migration, before Folio begins serving. Expect roughly
  9 seconds for 573,000 books, proportionally less for smaller catalogs.
- **Back up `/data` first.** The rebuild replaces the index in place. A snapshot
  taken with `sqlite3 /data/folio.db "VACUUM INTO '/backup/folio.db'"` is the
  fastest way back if the migration surprises you on your data.
- **Leave some disk headroom.** The old and new index both exist for the duration
  of the rebuild, so the database and its WAL grow temporarily.

### Others

- [6678833](https://github.com/Toshik1978/folio/commit/6678833094a456746fe5f3eb7ceedaf04435dfe1) perf(db): key books_fts by rowid instead of book_id

---

## v1.6.0 — 2026-07-28

Nothing here changes how Folio runs. The binary and the published image behave
exactly as v1.5.0 did, and there is nothing to do when you upgrade. What changed
is how releases get made and how they get written down.

Folio used to cut a release by clicking a button in GitHub Actions, which ran
Commitizen inside CI to pick a version, regenerate the changelog, and push a
commit and a tag back to `main`. Releases are now cut by pushing a tag, and no
workflow writes to the repository at all. The practical effect is that release
notes are written by hand before the tag exists — so what you are reading is a
deliberate summary rather than a dump of commit subjects.

### Highlights

- **Releases are cut by pushing a tag.** `git tag v1.6.0 && git push origin
  v1.6.0` builds the multi-arch image, attests its provenance, and opens the
  GitHub release using this changelog section as the body. If the pushed tag has
  no section, the job fails before anything is published rather than shipping an
  empty release.
- **One changelog instead of two files.** `RELEASE_NOTES.md` is gone and its
  prose moved here. Every release now carries hand-written highlights on top of
  the generated commit list, so there is one place to look instead of two that
  had to be kept in step with each other.
- **The tag is the version.** The `VERSION` file is gone — nothing ever read it —
  and Commitizen is no longer used for anything. Commits are still
  [Conventional Commits](https://www.conventionalcommits.org), enforced by a
  `commit-msg` hook that does not drag a Python runtime along with it.
- **Clearer expectations for contributors.** `AGENTS.md` was merged into
  `CLAUDE.md` so there is a single set of instructions, and `CONTRIBUTING.md` now
  says plainly what a personal project can offer: pull requests and issues may
  not get a response, and forking is a first-class outcome rather than a
  consolation prize.

### Features

- [9211c28](https://github.com/Toshik1978/folio/commit/9211c281f07236daefd19fdf83d1b0fb3db1b64d) feat(taskfile): make TAG optional in the changelog task

### Bug Fixes

- [8d652a0](https://github.com/Toshik1978/folio/commit/8d652a0e5c9908a0326d49e1433b0e7eacd5a8ca) fix(changelog): order commit groups and drop bump commits
- [52a0aa7](https://github.com/Toshik1978/folio/commit/52a0aa7e5836053eb1202b0b0a7466e26bb0914e) fix: avoid bash 3.2 pattern-substitution hang in changelog extractor
- [a1dcb30](https://github.com/Toshik1978/folio/commit/a1dcb30af74079d9352ea5e33932ce6872658831) fix(release): repair changelog prepend and correct release docs
- [2d86355](https://github.com/Toshik1978/folio/commit/2d86355d142f86868160400df344c2c01c6adb13) fix(changelog): append trailing separator to generated sections

### Others

- [db55072](https://github.com/Toshik1978/folio/commit/db55072553f893f4a6a8edb3f13aed3f160b4d20) build: replace commitizen with conventional-pre-commit
- [d48ab38](https://github.com/Toshik1978/folio/commit/d48ab38a889b2df076cf9838124831e448b9fc7d) build: add git-cliff config and changelog task
- [bf86883](https://github.com/Toshik1978/folio/commit/bf86883ccf7f6f6c8516856cdbf9049be035c180) docs: fold release notes into a hand-written changelog
- [eef0461](https://github.com/Toshik1978/folio/commit/eef0461466eed29dc33ab26bcd1979166cba6376) ci: add changelog section extractor for release bodies
- [a0025f2](https://github.com/Toshik1978/folio/commit/a0025f21c317c4b5ed5de3f0b079db5ba0b8023a) ci: trigger releases from pushed tags
- [7af0ba2](https://github.com/Toshik1978/folio/commit/7af0ba2e9dbea03cb26f9c53819fd458472e5875) docs: merge AGENTS.md into CLAUDE.md and document the tag flow
- [1643516](https://github.com/Toshik1978/folio/commit/16435167f05db1165177387a6b14daadc2f105aa) docs: set contribution expectations and document releases

---

## v1.5.0 — 2026-07-11

This release is about **limits**. Anywhere Folio reads something it does not
control — a JSON request body, a `.fb2` file, the SQLite connection pool — there
is now a ceiling on it. None of these were reachable bugs in normal use, but on
the small, low-spec boxes Folio is meant to run on, an oversized or hostile input
could consume memory or file descriptors until something else fell over.

The other half is **failing early and stopping cleanly**. A bad `PORT` or
`PUBLIC_URL` is now rejected at startup with a clear error instead of blowing up
opaquely later (or, worse, silently disabling the thing it was meant to
configure), and shutdown releases the same resources whether it was triggered by
Ctrl-C or by the server itself failing.

### Highlights

- **Invalid configuration is rejected at startup.** `PORT` must be a number in
  range and `PUBLIC_URL`, when set, must be an absolute URL. Previously an
  unparsable `PUBLIC_URL` quietly collapsed to empty, disabling the very
  CORS/OPDS origin pinning it was there to provide — a misconfiguration you would
  only discover from a client that stopped working.
- **Request bodies and file reads are bounded.** Every mutating JSON endpoint now
  caps the request body at 1 MiB (ample for metadata edits), and the plain `.fb2`
  read is capped at the same limit its `.fb2.zip` sibling already used. An FB2
  embeds its cover as inline base64, so an outsized file used to be buffered
  whole into memory.
- **The SQLite connection pool is capped.** It previously ran at
  `database/sql` defaults — unlimited. Each connection is a separate handle on
  the database and its WAL, so read concurrency could grow the pool until it
  exhausted file descriptors or memory. Idle connections are now released too.
- **Shutdown is prompt and symmetrical.** Ebook parsing honors cancellation, so
  a slow PDF or MOBI scan no longer keeps running after a sync is cancelled and
  delays the exit. And a server failure now tears down the SSE broker and the
  listener exactly like a signal does — before, those were only released on a
  clean signal shutdown.

### Notable fixes

- **OPDS search now behaves like the web UI.** The OPDS handler passed `q`,
  `author`, `series`, and `tag` through untrimmed, so a whitespace-only value ran
  a real full-text query on the OPDS side while the REST side treated it as
  empty. All four are trimmed now.
- **Quieter logs.** The metadata/cover aggregator gives each source its own
  deadline and cancels the losers; those expected cancellations were being logged
  at `Error`. They are `Debug` now, so `Error` means a genuine transport
  failure again.

### Features

- [b2f180b](https://github.com/Toshik1978/folio/commit/b2f180be1c25e8bfbd078ed5548f60a7f1b2b357) feat(config): validate PORT and PUBLIC_URL at startup

### Bug Fixes

- [13c265c](https://github.com/Toshik1978/folio/commit/13c265c28bc6ad1784fdec49931e23edbdd400d8) fix(db): bound the SQLite connection pool
- [8cc1ce9](https://github.com/Toshik1978/folio/commit/8cc1ce92f2857e7018910fe135973170755b288b) fix(api): cap JSON request bodies
- [8d67ff0](https://github.com/Toshik1978/folio/commit/8d67ff08c05479000739de4f6040646b38515a0b) fix(ebook): honor context cancellation during parsing
- [d4125c8](https://github.com/Toshik1978/folio/commit/d4125c81329aeb899f1d1993f05e032c9465f1ef) fix(ebook): cap the plain .fb2 read at maxArchiveTextBytes
- [2656d12](https://github.com/Toshik1978/folio/commit/2656d1298acbd2b27f700ea32f54b9b147a7446b) fix(googlebooks): don't log cancelled round trips at Error
- [44f6e5d](https://github.com/Toshik1978/folio/commit/44f6e5dd0f6100ed73a223366ef243f7e97bb465) fix(opds): trim search filter params to match REST

### Others

- [95b062d](https://github.com/Toshik1978/folio/commit/95b062dd35d024b7bddd21ddd4259181c55ba5f9) build: pin frontend toolchain to Node 24
- [3dd3c89](https://github.com/Toshik1978/folio/commit/3dd3c89c037e0e825208ddaf358baee00bfc0712) ci: group dependabot minor and patch updates per ecosystem
- [9d88433](https://github.com/Toshik1978/folio/commit/9d884339ec2d776a9dcb04741015be973cc1f77a) docs(agents): record bob as an approved direct dependency
- [6e9f7ee](https://github.com/Toshik1978/folio/commit/6e9f7eebf6b836e97e09e710b4df311d30e0667a) refactor(main): unify server-failure and signal shutdown paths

---

## v1.4.0 — 2026-06-30

A small release: two rough edges in the alphabetical browse pages, plus the
first properly published container image.

### Highlights

- **The alphabet selector only shows scripts you actually have.** Browsing
  authors, series, tags, or publishers used to render the full Cyrillic *and*
  Latin rows regardless of what was in the catalog, so a Latin-only library
  showed an entire greyed-out Cyrillic row (and vice versa). Each script block
  now appears only when at least one of its letters has entries, and it follows
  the library you have selected. `#` stays as the always-on catch-all.
- **"1 book", not "1 books".** The browse list pages pluralize the count
  correctly.
- **Multi-arch images with attested provenance.** `ghcr.io/toshik1978/folio` is
  now built for both `linux/amd64` and `linux/arm64` with build provenance
  attestation, so ARM hosts can pull the published image instead of building
  from source. The Quick Start leads with `docker run` on that image.

### Features

- [6e34156](https://github.com/Toshik1978/folio/commit/6e341564ece5707f5d23b4107c53d1162fc50c56) feat(web): hide alphabet script blocks with no entries

### Bug Fixes

- [636ba22](https://github.com/Toshik1978/folio/commit/636ba227cabef3021ffab2555933a74922f450b5) fix(web): pluralize the book count on browse list pages

### Others

- [f25caeb](https://github.com/Toshik1978/folio/commit/f25caeb9dbbaa325cd27a3f26c290dcdab4b4554) docs: add v1.3.1 and v1.3.2 release notes
- [fbc4c83](https://github.com/Toshik1978/folio/commit/fbc4c83448dcd4b516fe122f2dcaa4284a37a978) docs: prepare repository for public release
- [8d3cdea](https://github.com/Toshik1978/folio/commit/8d3cdea2d7844e01b7a5690e2bd545e6ddd70f98) docs: forbid AI attribution trailers in commit messages
- [8434fd8](https://github.com/Toshik1978/folio/commit/8434fd8022388b6d3a8b31a5c044d67ddec51ef2) docs: use the published ghcr image in Quick Start
- [ac5a039](https://github.com/Toshik1978/folio/commit/ac5a0391a5c5200722f0485c375e7decf40ad2a5) ci: build multi-arch image with provenance attestation

---

## v1.3.2 — 2026-06-26

A one-fix patch release for OPDS readers that don't implement OpenSearch.

### Notable fixes

- **OPDS search now works in Moon+ Reader (and Librera/Stanza).** These readers
  don't follow the OpenSearch description document — they look for a search link
  with the `{searchTerms}` template right in the feed. Folio now advertises that
  inline link alongside the standard one, so the search box appears and works.
  Spec-compliant clients like Koodo Reader are unaffected. Re-add (or refresh)
  the catalog in your reader to pick it up.

### Bug Fixes

- [06f86ad](https://github.com/Toshik1978/folio/commit/06f86ad3a1279bee108056c43fdaeea6da092e07) fix(opds): emit inline templated search link for Moon+ Reader

---

## v1.3.1 — 2026-06-26

A one-fix patch release correcting how OPDS search is advertised.

### Notable fixes

- **OPDS search advertised with the correct media type.** The search link and
  OpenSearch description document are now served as
  `application/opensearchdescription+xml`, as the OPDS spec requires. This is
  the groundwork that the v1.3.2 Moon+ Reader fix builds on.

### Bug Fixes

- [00195ee](https://github.com/Toshik1978/folio/commit/00195ee322cca9044a0efbe9acb54c16b8d61c2b) fix(opds): advertise OpenSearch with correct media type

### Others

- [41cb0ac](https://github.com/Toshik1978/folio/commit/41cb0ac83b66bf61e5518028e2ad7f68a3b09266) docs: add v1.3.0 release notes

---

## v1.3.0 — 2026-06-26

This release makes covers and metadata **lazy**: instead of an eager pass that
hammered providers after every sync, Folio now fetches art and metadata on
demand and remembers what it has already resolved.

### Highlights

- **Lazy covers and metadata.** The eager cover warmer is gone. Syncs finish
  faster and stay quiet, and covers/metadata are resolved when they're actually
  needed rather than all at once after an INPX sync.
- **Cover serving driven by tracked state.** Folio now records each book's cover
  status (`cover_state`) and serves from it, dropping the on-disk placeholder
  file. Books known to have no cover short-circuit immediately instead of
  re-attempting work on every request.
- **Offline metadata backfill in OPDS feeds.** The acquisition feed now fills in
  missing metadata from sources you already have — bounded and with no online
  trip — so OPDS clients see richer entries without slowing the feed down.

### Features

- [05f6f20](https://github.com/Toshik1978/folio/commit/05f6f20ec92a47cdde56923adfc375014ae8fe97) feat(db): add books.cover_state column and queries
- [5758ed0](https://github.com/Toshik1978/folio/commit/5758ed00d311471f5ce07b21daa7c121f8d206f2) feat(ingest): add CoverState adapter over books.cover_state
- [a01d38b](https://github.com/Toshik1978/folio/commit/a01d38bef7f2f3004373db0a42b0fd1af803f671) feat(covers): drive cover serving from cover_state, drop on-disk placeholder
- [14edb53](https://github.com/Toshik1978/folio/commit/14edb5388e24e601aa11a5876b44ba01bcd5e0a6) feat(opds): offline metadata backfill on acquisition feed (bounded, no online trip)

### Bug Fixes

- [3b8f888](https://github.com/Toshik1978/folio/commit/3b8f888e103b662d051db24c37a8df7c52720f17) fix(covers): short-circuit thumbnail serve on StateNone; test-double + comment cleanup

### Others

- [8d89f93](https://github.com/Toshik1978/folio/commit/8d89f93b031163c6fadbda10da5938551ecb9c1f) docs: add release notes and contributing guide for public release
- [e9f4566](https://github.com/Toshik1978/folio/commit/e9f4566d08fe39f74093ac3822957251ddfe17f8) ci: add Dependabot config for gomod, npm, and actions
- [995cb06](https://github.com/Toshik1978/folio/commit/995cb06fb61db69c2a62e6066893c2fc1bb9a803) ci: minor changes in the release process
- [716275a](https://github.com/Toshik1978/folio/commit/716275aeeb06a32f66cd2252b7c99208fea39df6) refactor(sync): remove eager cover warmer; covers/metadata now lazy
- [1f066a1](https://github.com/Toshik1978/folio/commit/1f066a1741ef35eca3e3aba5479654d0bfe0bd11) docs: cover_state + OPDS offline enrichment, drop warmer references

---

## v1.2.1 — 2026-06-25

A small follow-up to v1.2.0.

### Notable fixes

- **Cover-less books stop hammering the providers.** The warm sync pass now
  negative-caches books that have no cover, so each run no longer re-searches
  every source for art that doesn't exist.

### Bug Fixes

- [66251ae](https://github.com/Toshik1978/folio/commit/66251ae5308ea2504b50db832a3bad76760dd863) fix(sync): warm pass negative-caches cover-less books

---

## v1.2.0 — 2026-06-25

This release is about **faster browsing** and **better covers**: real
thumbnails in the grid, offline metadata backfill after a sync, and sharper,
higher-resolution cover art.

### Highlights

- **Cover thumbnails everywhere.** Folio now generates aspect-preserving
  thumbnails on the cover write path and serves them in the book grid and OPDS
  feeds, so listings load quickly instead of pulling full-size images. Thumbnail
  URLs are built server-side with a cache-spec token, and a self-healing
  fallback regenerates a missing thumbnail from the original cover on demand.
- **Offline metadata backfill.** A new local backfiller fills in missing
  metadata from sources you already have — no network needed. The sync warmer
  runs it automatically after an INPX sync, and single-book lookups backfill on
  the fly.
- **Sharper, higher-resolution covers.** Cover search now fetches correct
  high-res art via ISBN/ASIN sources and filters candidates for relevance
  centrally in the aggregator, with deterministic ordering so the best cover
  wins consistently.

### Robustness

- **Friendlier to Amazon/Goodreads.** The Amazon direct search is throttled to
  protect IP reputation, Akamai and Cloudflare anti-bot interstitials are
  detected and no longer retried in a loop, and a new `ErrNoRetry` signal stops
  pointless retries on terminal errors.
- **INPX sync skips missing files.** Books whose archive file is absent are
  skipped instead of failing the import.
- **Thumbnail decoding is memory-bounded** to prevent out-of-memory on hostile
  images.

### Notable fixes

- Infinite scroll no longer retry-storms when a page fails to load.
- A local cover is kept if extraction times out mid-enrich, instead of being
  dropped.
- Cover priority is only raised once the cover file has actually been saved.

### Features

- [088507f](https://github.com/Toshik1978/folio/commit/088507fbd99c76240b06159cb21e40628ba93303) feat(covers): add aspect-preserving thumbnail generator
- [539f87c](https://github.com/Toshik1978/folio/commit/539f87c1ad0b84056b7b2558a39997151e571516) feat(covers): generate and invalidate thumbnails in the cover write path
- [1b3ea61](https://github.com/Toshik1978/folio/commit/1b3ea61cbefe2ad82437b68873da8a6aafceabb6) feat(covers): serve thumbnails with self-healing cover fallback
- [b1f0f15](https://github.com/Toshik1978/folio/commit/b1f0f15d5e159b8c7d652e3e4f5457e278b1812d) feat(opds): serve cover thumbnails and point rel=thumbnail at them
- [8680f7b](https://github.com/Toshik1978/folio/commit/8680f7b72aee8b1b5574b963e81f1a59b3ddd7e8) feat(api): add cover thumbnail route
- [df5fb6d](https://github.com/Toshik1978/folio/commit/df5fb6df5ff974c693d172491a1d24f005d485a9) feat(web): load cover thumbnails in the book grid
- [e93758a](https://github.com/Toshik1978/folio/commit/e93758afccbfe313fb311139c94fe437f8932dcf) feat(api,opds): build thumbnail URLs server-side with a cache-spec token
- [531cc23](https://github.com/Toshik1978/folio/commit/531cc23f683a6309ec9307be04d9004f59f95755) feat(metasearch): add ErrNoRetry to stop RetryCovers on terminal errors
- [ba5bfa9](https://github.com/Toshik1978/folio/commit/ba5bfa9a201011948df727d38908467c265b8b8a) feat(amazon): add title-relevance filter for cover candidates
- [a0f92a6](https://github.com/Toshik1978/folio/commit/a0f92a6ebf760ae7d0ed369022d9f26c49bb8c65) feat(amazon): filter search thumbnails by title relevance
- [bf9f1cd](https://github.com/Toshik1978/folio/commit/bf9f1cd4d7bb9b847b5cfa6601dc10baa9c871e4) feat(amazon): detect Akamai interstitial and stop retrying it
- [244ec1e](https://github.com/Toshik1978/folio/commit/244ec1edc9bca0e3b680167d69437e9b34655a79) feat(amazon): throttle the direct search to protect IP reputation
- [0d41e50](https://github.com/Toshik1978/folio/commit/0d41e50a3f9886e8ece551636ee42be0cac6fcde) feat(metasearch): fetch correct high-res covers via ISBN/ASIN sources
- [e49976b](https://github.com/Toshik1978/folio/commit/e49976b025a25ae1f3cb93ddc34af07d8965fde9) feat(metasearch): filter cover relevance centrally in the aggregator
- [f4ec330](https://github.com/Toshik1978/folio/commit/f4ec330241f9f4265f949dfcf07a5fdc0e80f8ee) feat(ingest): add LocalBackfiller offline metadata tier
- [5d1a3ce](https://github.com/Toshik1978/folio/commit/5d1a3ce90aea9d61e6af6acc7d0d149f55da1f2f) feat(sync): warmer backfills offline metadata after INPX sync
- [dd4a5a4](https://github.com/Toshik1978/folio/commit/dd4a5a474595e7d1d59430d62d145635f8cfc59e) feat(ingest): skip INPX books whose archive file is missing

### Bug Fixes

- [1233034](https://github.com/Toshik1978/folio/commit/12330341181f739a3c8d78e26ff04d86f62fe19b) fix(covers): cap thumbnail decode dimensions to prevent OOM
- [42971f0](https://github.com/Toshik1978/folio/commit/42971f0c8e01d2db6cca27c2244e48bc38a729d3) fix(web): add thumbnail_url to hand-built Book literal in EditBookModal spec
- [bb1c9a8](https://github.com/Toshik1978/folio/commit/bb1c9a8a5b4f0f6c67fd8a595235be9def9a6613) fix(amazon): broaden audiobook/audio-cd junk markers
- [7fd23c3](https://github.com/Toshik1978/folio/commit/7fd23c3ebe1b459574b72242315f17fa5a176ea6) fix(metasearch): resolve enrichment in one call instead of search+get
- [44fa617](https://github.com/Toshik1978/folio/commit/44fa617496498eac7ee741394c2d9ddb8fa8356d) fix(metasearch): require explicit source in ApplyMatch, drop guess-fallback
- [652f75f](https://github.com/Toshik1978/folio/commit/652f75f8c86d939012602f61f44594725ae1dee5) fix(amazon): retry transient blocks and honor the interstitial ErrNoRetry
- [1d5ca27](https://github.com/Toshik1978/folio/commit/1d5ca27fc7e44a3254b234ca02eefa1a2344de6b) fix(goodreads): stop retrying the terminal Cloudflare 202 challenge
- [d5923f3](https://github.com/Toshik1978/folio/commit/d5923f3de79312982fb0ed3b6de79dff906a66a9) fix(metasearch): deterministic cover ordering with a FullURL tiebreak
- [4dfca83](https://github.com/Toshik1978/folio/commit/4dfca834f401a48c1766f6be8d96be2159d31a97) fix(ingest): raise cover_prio only after the cover file actually saves
- [f45c5cd](https://github.com/Toshik1978/folio/commit/f45c5cd6d7d6c925baab8b2b88cc86cb1d73316e) fix(covers): keep a local cover when extraction times out mid-enrich
- [7b1f450](https://github.com/Toshik1978/folio/commit/7b1f450e91bf9bb1a9e4628ce27c3e224ed16161) fix(web): reject failed loadMore so infinite scroll stops retry-storming

### Others

- [740de38](https://github.com/Toshik1978/folio/commit/740de38c2d929770fd361c70f64c8553abcd559e) refactor(covers): rename Store.ServeHTTP to ServeCover
- [2e05349](https://github.com/Toshik1978/folio/commit/2e0534944c13e17f4476419cff1d4e4147a4878d) refactor(covers): extract shardDir and serveImmutableFile helpers
- [209df02](https://github.com/Toshik1978/folio/commit/209df02e288d0db690ec24af831304ac9424baf1) refactor(web): use server thumbnail_url in the book grid
- [8a9e65f](https://github.com/Toshik1978/folio/commit/8a9e65f3fb56413223b6b43f93d358b3e519d099) docs: document cover thumbnail routes, thumbnail_url, and ServeCover
- [75589b0](https://github.com/Toshik1978/folio/commit/75589b080b3794dfe18e5e20e6eee94bbae93d03) docs: correct thumbnail cache filename to 42.thumb.jpeg
- [57e64d4](https://github.com/Toshik1978/folio/commit/57e64d46e2d40a83f6c6150806109bebd16db5d3) build: add typecheck task and run vue-tsc in 'task lint'
- [e67adb6](https://github.com/Toshik1978/folio/commit/e67adb65991a560ed11809bb1811885bef85744d) ci(github): fix ci workflow
- [139b9b5](https://github.com/Toshik1978/folio/commit/139b9b55f9357c41e681086f829ad70f87ea6805) refactor(amazon): drop the dead DuckDuckGo cover fallback
- [5beb76c](https://github.com/Toshik1978/folio/commit/5beb76c47b2a4298f4d07c66d016a0640935bfd4) docs: add CLAUDE.md and document testing/quality-gate rules
- [951f3de](https://github.com/Toshik1978/folio/commit/951f3decc3ef1264a58498a5e08794f3c50e1aba) refactor(goodreads): drop per-provider relevance filter (now in aggregator)
- [a20484b](https://github.com/Toshik1978/folio/commit/a20484b70fee19d8d6e86e7b1239f8fa2c285672) perf(covers): decode a non-JPEG cover once on the write path
- [43dc002](https://github.com/Toshik1978/folio/commit/43dc0025a6b1aca0c1c426ebe31232f2e740b6f8) refactor(covers): share one decompression-bomb pixel-cap guard
- [d45a944](https://github.com/Toshik1978/folio/commit/d45a944c863c040bc5cea5f8efb19b628f42ff1c) refactor(metasearch): share one book->query identifier builder (fixes ASIN drift)
- [ad663ed](https://github.com/Toshik1978/folio/commit/ad663edabd06da20d35a7faf7eec02ff085dabb5) refactor(metasearch): name CDN image transforms for their real contract
- [5eb3a49](https://github.com/Toshik1978/folio/commit/5eb3a49f3917db2f99c0ab80955b07a21a93ca9d) refactor(metasearch): single-scan CDN regex and drop stale comments
- [f78b64a](https://github.com/Toshik1978/folio/commit/f78b64a6d16f5cdf4c88e05f368c6d9cea4ba3a9) docs: record approval for the golang.org/x/net/html dependency
- [36e200a](https://github.com/Toshik1978/folio/commit/36e200a89bae6e763fcc5a6b79b0e4e8257e7496) refactor(metasearch): final-review polish (docs, test clarity, named type)
- [0f1625f](https://github.com/Toshik1978/folio/commit/0f1625fc7b58e64b29027f61302ad795e42ca0f4) refactor(api): getBook delegates offline backfill to ingest.LocalBackfiller
- [02f60a0](https://github.com/Toshik1978/folio/commit/02f60a0a73ba9e1bab83e3a7417910d3e1d4c620) refactor(ingest): move logger after context in ingestINP signature

---

## v1.1.0 — 2026-06-24

This release is mostly about **metadata and covers**: finding better ones,
editing them by hand, and doing it all more safely.

### Highlights

- **Cover search across multiple sources.** Folio can now pull cover art from
  Open Library, Google Books, Amazon, and Goodreads at once, deduplicate the
  results, and rank them. A new cover-picker grid lets you browse matches and
  jump straight to the source.
- **Manual metadata editing.** A reworked book-edit modal lets you fix titles,
  authors, tags, language, and identifiers (ISBN, etc.) directly in the UI —
  and set a cover by upload, paste, drag-and-drop, or URL.
- **Canonical genre taxonomy.** Genres are normalized to a single BISAC-based
  list and exposed via `GET /genres`, so edits and imports agree on the same
  labels.

### Security & robustness

- **SSRF protection** on cover-URL fetching: requests to internal/private
  addresses are blocked, and the DNS-rebinding gap is closed by pinning dials.
- **Library paths confined** to an optional `LIBRARY_ROOT`.
- **Token-less CSRF guard** added on `/api`.
- **Bounded resource use** when decoding covers and parsing source files
  (decompression-bomb caps, memory limits, safer MOBI/EPUB parsing).
- **Single-writer SQLite guard** so concurrent API writes and syncs serialize
  cleanly instead of contending.

### Notable fixes

- Manual book edits are now atomic.
- Sync is steadier: idle SSE streams stop reconnecting, the silence watchdog
  re-arms correctly, and last-sync checkpoints persist reliably.
- Anti-bot blocks from Amazon/Goodreads scrapers are retried through transient
  failures instead of failing the whole search.

### Features

- [220246d](https://github.com/Toshik1978/folio/commit/220246ddf4721a5bfcfe41e1cf7a39e10fe3ccf7) feat(api): manual cover upload with sticky cover_prio
- [41609e4](https://github.com/Toshik1978/folio/commit/41609e45bdfb51509b8820a246ecbcc01d5a0f54) feat(api): set book cover from an image URL
- [cfc4239](https://github.com/Toshik1978/folio/commit/cfc4239d039bc1fd9040e61b6705b8322ff21901) feat(api): manual metadata edit via overwrite engine
- [088533f](https://github.com/Toshik1978/folio/commit/088533fc2dc43678b19e48c33032860a19718dae) feat(api): expose canonical genre taxonomy via GET /genres
- [3c40571](https://github.com/Toshik1978/folio/commit/3c40571ea7a9d002e96a8664d5568694d3b99dc8) feat(web): api client for manual edit and cover endpoints
- [d9cb389](https://github.com/Toshik1978/folio/commit/d9cb389d84ccc2af0fa90cf5af91e6e1e67d92fb) feat(web): manual metadata edit modal
- [34bb322](https://github.com/Toshik1978/folio/commit/34bb322f28f6f4acbbf86e09e70af2f8aeca06b5) feat(web): cover picker modal (upload, paste, drag, url)
- [c830515](https://github.com/Toshik1978/folio/commit/c8305153ec8561e3616463428a1771af132225c0) feat(web): rework book edit modal with tag, language and identifier editing
- [8b3c447](https://github.com/Toshik1978/folio/commit/8b3c4472fde1ff612cb1e37f6bbe31a4aef1d6be) feat(metasearch): core capability/source/candidate types
- [dcc53a9](https://github.com/Toshik1978/folio/commit/dcc53a9737abf05d9903c9b90b6811d942bb1559) feat(metasearch): registry with capability fan-out
- [f00b8c4](https://github.com/Toshik1978/folio/commit/f00b8c41b7137ecc27d800d2cca5a97258e6e16b) feat(metasearch): concurrent cover aggregator with dedupe and ranking
- [ae35e9a](https://github.com/Toshik1978/folio/commit/ae35e9a1d750e8114281b22bbb028f35a2117c30) feat(metasearch): open library cover source
- [2f07cca](https://github.com/Toshik1978/folio/commit/2f07cca13ecbc5160aae6223293ef06cd3303e49) feat(metasearch): google books cover adapter (cover-only)
- [93d26a5](https://github.com/Toshik1978/folio/commit/93d26a5f9b7aded45293f3a059f93f922c515985) feat(metasearch): amazon cover scraper with golden-html parser test
- [b7741e7](https://github.com/Toshik1978/folio/commit/b7741e74d7e0f7cc05457023a36296a3dce19ee4) feat(metasearch): goodreads cover scraper with golden-html parser test
- [e11b8ab](https://github.com/Toshik1978/folio/commit/e11b8ab6bfbc8ecb06cb7dcf7c780272cab81920) feat(api): cover-search endpoint seeded from book metadata
- [3097a54](https://github.com/Toshik1978/folio/commit/3097a5436cfe2b6c7f787ff4b4a16055c4325e1a) feat(cmd): wire cover-search providers into the books handler
- [d077d1f](https://github.com/Toshik1978/folio/commit/d077d1f277def8d4a7b4ae213fdada2923497cee) feat(web): api client for cover search
- [2879916](https://github.com/Toshik1978/folio/commit/28799167e790835b8f67389baf0efe9535b706d8) feat(web): cover search grid and provider deep-links
- [ce518dc](https://github.com/Toshik1978/folio/commit/ce518dc08e8d67c233826accc3680e6a455bde94) feat(metasearch): promote google books to dual-capability source
- [97d7fdc](https://github.com/Toshik1978/folio/commit/97d7fdc0a66b5e0a7f000216c4bbaadb3b554b5c) feat(metasearch): coordinator reimplementing the enricher facade
- [156239b](https://github.com/Toshik1978/folio/commit/156239ba63dc66f5976a5248e37bd5072fe6cbc0) feat(ingest): book lookup seam for the metasearch coordinator
- [61327bd](https://github.com/Toshik1978/folio/commit/61327bda085ffead8af021a31cb3d6295e181716) feat(web): source-qualified fix match apply
- [407b01c](https://github.com/Toshik1978/folio/commit/407b01ced2bd1a6fdab4046b8e470e1a1da6228c) feat(metasearch): full-resolution covers from amazon-cdn providers
- [75f9419](https://github.com/Toshik1978/folio/commit/75f941972dd662fe1904dab416555f246506ada0) feat(server): add token-less CSRF guard on /api
- [29a8ae2](https://github.com/Toshik1978/folio/commit/29a8ae21b88e2b14d831fd933d7936d3fc6d27e2) feat(metasearch): add ErrBlocked sentinel and RandomUserAgent helper
- [50c4284](https://github.com/Toshik1978/folio/commit/50c428492d891ba8aac79cead7c9963d36035364) feat(metasearch): use Goodreads autocomplete JSON API for covers
- [5a1a153](https://github.com/Toshik1978/folio/commit/5a1a153528861f43407c8bbf481daaf87ccf5314) feat(metasearch): add DuckDuckGo fallback for blocked Amazon covers
- [e41a961](https://github.com/Toshik1978/folio/commit/e41a9615b5f51ca02c6dd41836791a97cc0310b7) feat(metasearch): log per-source cover-search outcomes
- [942f174](https://github.com/Toshik1978/folio/commit/942f1746159d73635fbed2a17a295b258d298cc0) feat(api): confine library paths to optional LIBRARY_ROOT

### Bug Fixes

- [219bd26](https://github.com/Toshik1978/folio/commit/219bd26efeb62941823cff6baf8debca1ad5cd23) fix(api): block SSRF to internal addresses in cover URL fetch
- [23dce85](https://github.com/Toshik1978/folio/commit/23dce8564c9be9147d32f75d0b090080eb9139cc) fix(metasearch): wrap empty-source fallback error and test Enrich error path
- [532f97f](https://github.com/Toshik1978/folio/commit/532f97f0c5758b7aee04c190db7b522166ecfe71) fix(web): Esc closes tag/language dropdown instead of the edit modal
- [10092ae](https://github.com/Toshik1978/folio/commit/10092ae5f14c39f6f967822218e673e310d2eb92) fix(metasearch): retry amazon/goodreads scrapers through transient anti-bot blocks
- [f51bd9c](https://github.com/Toshik1978/folio/commit/f51bd9c7a1e7eae92e0446527e7830636bec1a8c) fix(db): serialize writers through a shared write guard
- [9e53202](https://github.com/Toshik1978/folio/commit/9e532022b9de70c465a30a046fca5b61f85f5b77) fix(sync): cancel in-flight work before stopping the scheduler
- [bf59d8e](https://github.com/Toshik1978/folio/commit/bf59d8e37e4bd25ff32199ea02430f4077c30d32) fix(sync): run the purge checker in singleton mode
- [a7bd409](https://github.com/Toshik1978/folio/commit/a7bd409dc6a2e0d034f39e5a15d7ead10df31e6c) fix(sync): persist last-sync checkpoint on a detached context
- [186854e](https://github.com/Toshik1978/folio/commit/186854e580ae849a3769a4e64df6a4c9e2c72063) fix(ingest): defer cover writes/deletes until the import batch commits
- [9de2718](https://github.com/Toshik1978/folio/commit/9de27184d8f8e407a78e306bd9f060f3fbe142f3) fix(api): canonicalize genres on edit/enrich to match the import path
- [357c523](https://github.com/Toshik1978/folio/commit/357c523a4bcd1dc9140d19c7ebb4bc0a01f41ac8) fix(metasearch): skip malformed srcset densities and pick the highest valid cover
- [9a3b61a](https://github.com/Toshik1978/folio/commit/9a3b61a48d53d50f5b169ac650514313411cd245) fix(opds): cap feed pagination to avoid int64 offset overflow
- [8dbd7df](https://github.com/Toshik1978/folio/commit/8dbd7dff6af842adc508dc47bdedc3a969c17391) fix(covers): apply the decompression-bomb pixel cap to JPEG too
- [78af3ef](https://github.com/Toshik1978/folio/commit/78af3efa835d5ae678e8c257ba06ca71506bf8c4) fix(ebook): bounds-check MOBI title offset with unsigned math
- [68bf47e](https://github.com/Toshik1978/folio/commit/68bf47e2826402e51066c15b630b675a8bcf7008) fix(web): ignore stale book fetches in BookDetailModal
- [23dc668](https://github.com/Toshik1978/folio/commit/23dc668c8fee86cab2c1b9fb508570ca9ff75a0b) fix(web): drop stale facet loads on rapid library switch
- [97476b1](https://github.com/Toshik1978/folio/commit/97476b1a2543570dcef8c14ae439c4383d4d412c) fix(web): default the sync interval when the field is cleared
- [3fcfc38](https://github.com/Toshik1978/folio/commit/3fcfc389a0d3356d627ec4c30c31c8bdb4c65ee7) fix(web): re-arm the SSE silence watchdog on every event
- [861c2b0](https://github.com/Toshik1978/folio/commit/861c2b089ab6a81d1d9c26ac459830a87b3fd46e) fix(web): key identifier rows by stable id, not index
- [87d551a](https://github.com/Toshik1978/folio/commit/87d551acd9d35f68f3d4d59131e496be4d8d0b53) fix(web): preserve identifier row identity on edit via in-place rows
- [bc356c2](https://github.com/Toshik1978/folio/commit/bc356c2d7d45a38ffc7a7cd404fc5f79a8d44b3e) fix(web): correct emitted() indexing in LibraryForm spec type cast
- [7ee626f](https://github.com/Toshik1978/folio/commit/7ee626fcebdd0c1d71852dff01ff6df9b4e624f4) fix(web): treat total=0 as determinate in the sync progress bar
- [892b142](https://github.com/Toshik1978/folio/commit/892b14216a1d1826d252599965dc66b6a2ad3138) fix(web): degrade gracefully when matchMedia/localStorage are unavailable
- [1fd3d5b](https://github.com/Toshik1978/folio/commit/1fd3d5bec3f5186a0cd791f1e4d637cba2f25e0a) fix(api): compute cold-cache stats once under the cache lock
- [1308378](https://github.com/Toshik1978/folio/commit/1308378df9b9f8f9f466188da457daab699a4e7e) fix: address deferred review minors (stale facet toast, test hardening)
- [1476ad0](https://github.com/Toshik1978/folio/commit/1476ad057ffc82f30d5420231ae6b69e62b67856) fix(api): make manual book edit atomic
- [50cca4c](https://github.com/Toshik1978/folio/commit/50cca4cbb510033fa5acc3d63aa903700359b38e) fix(db): bound API write waits on the single-writer guard
- [43fc523](https://github.com/Toshik1978/folio/commit/43fc523638a52f691c6aa7ae29ba127713e0b99d) fix(web): treat the SSE heartbeat as liveness so idle streams stop reconnecting
- [875ec10](https://github.com/Toshik1978/folio/commit/875ec1065d03d5c7ab49def13f3abc8d076d40ac) fix(web): split book download and curation actions into separate rows
- [d7a96eb](https://github.com/Toshik1978/folio/commit/d7a96eb7c23d9f4cb73f2b4f3c484e1bb4c3ef53) fix(api): pin cover-fetch dials to close the SSRF rebinding gap
- [9027de2](https://github.com/Toshik1978/folio/commit/9027de204962da24cb8063e2d981d808ae1cb727) fix(ebook): bound cover and metadata extraction from source files
- [b934d84](https://github.com/Toshik1978/folio/commit/b934d8488dd52045291197d308c51a6e6bd77cc8) fix(web): re-arm infinite scroll so short pages don't stall
- [744118e](https://github.com/Toshik1978/folio/commit/744118ee444365b85e2b344e3690b987e30efc4e) fix(api): treat explicit cover-search ?q= as verbatim
- [b92525e](https://github.com/Toshik1978/folio/commit/b92525e1f3802243b894265ce2f518026adf4895) fix(metasearch): harden cover-fetch fallback and tidy duplication

### Others

- [1a6b7b1](https://github.com/Toshik1978/folio/commit/1a6b7b1f7fc4bc4074dc3a57f671ab917904c94a) refactor(ingest): moved logger and batchSize initialization to newImporter
- [b63198e](https://github.com/Toshik1978/folio/commit/b63198ebef60ece795e96e4fcccca2fdc640c664) refactor: merged tests in the same package, added stricter linter rules and refactored multiple global objects
- [73eae1e](https://github.com/Toshik1978/folio/commit/73eae1e33963aa6f62963c870815b7e87c5f8a5e) perf: rolled back previous commit partially due to performance concerns
- [c287ad5](https://github.com/Toshik1978/folio/commit/c287ad511a7fe587c4961882aa4aeee20ff40173) ci: guard against non-ASCII bytes in SQL that feeds sqlc
- [a31b8f8](https://github.com/Toshik1978/folio/commit/a31b8f882f69f5f556f1a0c3cef68413d5fba139) refactor(ingest): export VolumeToMetadata for metasearch reuse
- [b69f8c6](https://github.com/Toshik1978/folio/commit/b69f8c6d74d8a5aa85714d9763731456fde763df) refactor(api): neutral metadata candidates and source-qualified apply
- [9aec75b](https://github.com/Toshik1978/folio/commit/9aec75b678c2e2d792a4b34522bd01470dea72b6) refactor: route enrichment through the metasearch coordinator
- [ac5a3b8](https://github.com/Toshik1978/folio/commit/ac5a3b88fa24c93649d09f70180cc7748a53ddec) refactor(amazon): rename http field to client; add HTTP fetch-path tests
- [b6b139a](https://github.com/Toshik1978/folio/commit/b6b139a730c411cdf3c90c7b32b7e082643a9b89) docs: neutralize stale Google-Books-specific comments on provider-neutral code
- [2969574](https://github.com/Toshik1978/folio/commit/2969574d1d3c15c34d6187dedc12b49b8559a61f) refactor(metasearch): removed a few global variables
- [0010a75](https://github.com/Toshik1978/folio/commit/0010a75d567c9af44c2e580963bd8e6c71cf0515) refactor(ebook): replace mutable series-prefix cache with one precompiled regexp
- [31cbaab](https://github.com/Toshik1978/folio/commit/31cbaab5dd7a2a7733d8b9acfb5625ebe14c6836) refactor(htmltext): unexport entity table behind NewDisplayDecoder
- [854792b](https://github.com/Toshik1978/folio/commit/854792be6cfe057744a1f78e5cd05c644628c9ec) refactor(web): extract a shared StarRating component
- [2ac89a6](https://github.com/Toshik1978/folio/commit/2ac89a6eb3cec8b547eca37635e1c3e0b2c35c78) refactor: share a single ISBN identifier constant
- [08bee6f](https://github.com/Toshik1978/folio/commit/08bee6f9d68142304e96ffdc0e2d1a5eb5a72569) docs: sync API, architecture, database, and build docs with the code
- [ffe068c](https://github.com/Toshik1978/folio/commit/ffe068cf2fe2ca7f4b375c6883e3fd4225293bd0) docs(backend): fix metasearch leaf tag and add genres route
- [04df9b5](https://github.com/Toshik1978/folio/commit/04df9b52c625c78b436af3d0fb4894d1cf6e8cad) refactor(web): drop redundant save click handler in EditBookModal
- [2bf477e](https://github.com/Toshik1978/folio/commit/2bf477ed94d83cc460b7a77054b984641933aac9) refactor(metasearch): split Amazon scraper and detect interstitials

---

## v1.0.0 — 2026-06-21

The first stable release of Folio: a self-hosted, **read-only** digital book
library that catalogues your e-book collection and serves it through a web UI
and an OPDS feed. Mount your sources read-only, and Folio indexes them into its
own SQLite database — it never writes back to your books.

### Highlights

- **Smarter book grouping.** Editions of the same book are matched by strong
  identifiers (ISBN and friends) before falling back to title/author keys, with
  order-independent author matching — so the same book from different sources
  collapses into one entry.
- **Reliable cover handling.** Cover files are written atomically (temp +
  rename), so an interrupted import never leaves a half-written image.
- **Robust ingestion.** Identifiers are validated before grouping, MOBI titles
  decode correctly even without an EXTH header, and image/JSON decoding is
  memory-bounded to keep imports from blowing up on malformed files.

### Under the hood

- Genre taxonomy aligned to BISAC subject labels.
- Pagination hardened against offset overflow.
- Sync avoids busting caches on no-op checkpoint skips.

### Features

- [fe1f15c](https://github.com/Toshik1978/folio/commit/fe1f15c3aba60d53db417a6f15c9c8ca80ab9cab) feat(ingest): order-independent author key in groupKey
- [2eb52b5](https://github.com/Toshik1978/folio/commit/2eb52b501a1b16a9d6b92046ea620756fe32ec4d) feat(db): reverse (type,value) identifier lookup query + index
- [a3bb2d0](https://github.com/Toshik1978/folio/commit/a3bb2d0a6e51ad3ff2ee51d86dd604408a7a28d7) feat(ingest): identifier pre-match for derived-identity sources
- [a6aad60](https://github.com/Toshik1978/folio/commit/a6aad609b6b7dd961c2d7109d796811a02f694a2) feat(ingest): enable identifier grouping for folder and inpx sources
- [f140f65](https://github.com/Toshik1978/folio/commit/f140f655354f9e3649ccc42953307192b0bebf34) feat(ingest): log when identifier grouping overrides key-based grouping

### Bug Fixes

- [175a025](https://github.com/Toshik1978/folio/commit/175a02577de0551e9dc5e6b220d732c780694d25) fix(ebook): decode MOBI header title entities when EXTH is absent
- [a9a29a6](https://github.com/Toshik1978/folio/commit/a9a29a63bbcd4e457fb39661a2569b7f7f5cedfb) fix(ingest): validate strong identifiers before grouping books
- [12262b3](https://github.com/Toshik1978/folio/commit/12262b32c96a5f2334decda37641d43ff70c4c09) fix(covers,googlebooks): bound image decode and JSON response in memory
- [6058eab](https://github.com/Toshik1978/folio/commit/6058eab83544ec0abb9476cd47592182d1659cb4) fix(logging): namespace WithGroup attrs and correct the logging docs
- [ae40db2](https://github.com/Toshik1978/folio/commit/ae40db2eb9bc7f66b506a2e5aebb2e0461cb774e) fix(api): clamp pagination page to prevent offset overflow
- [db90060](https://github.com/Toshik1978/folio/commit/db90060237c0940b31ff1597f39b65e5c878f763) fix(covers): write cover files atomically via temp + rename

### Others

- [d9f9f40](https://github.com/Toshik1978/folio/commit/d9f9f40798dfa24a34ed06fb29396b61a4dc2326) refactor(db): align bookColumns/scanBook order with regenerated dbq.Book
- [41c3bc2](https://github.com/Toshik1978/folio/commit/41c3bc22f463edea67f1aae01ed252809f9ca0bb) docs(api): clarify /api/stats is whole-catalog, not library-scoped
- [462cc9c](https://github.com/Toshik1978/folio/commit/462cc9c6a5895e36104638a59874913f2d09535e) refactor(api): minor test refactoring
- [0fce077](https://github.com/Toshik1978/folio/commit/0fce077c7874840ae955e6a0002ce2685d943d71) docs(db): clarify sync writes are batched, not a single transaction
- [ef478b5](https://github.com/Toshik1978/folio/commit/ef478b5c894718f559755915cbc9860aa7a4d3b7) perf(sync): don't bust caches on a no-op checkpoint skip
- [5fe61fb](https://github.com/Toshik1978/folio/commit/5fe61fb0f9f537be68bd70b41f8e92e116e81871) refactor(api): drop unused error return from toBookView
- [0f68a11](https://github.com/Toshik1978/folio/commit/0f68a111b366b3bde39ce019b41254132e1828a1) refactor(ingest): align genre taxonomy to BISAC subject labels
- [16499ec](https://github.com/Toshik1978/folio/commit/16499ec324fa73e443b767785cc15e9b0fff9c88) docs(ingest): document BISAC alignment of the genre taxonomy

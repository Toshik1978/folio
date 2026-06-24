# Frontend

> Vue 3 SPA: TypeScript, Tailwind CSS v4 + DaisyUI, Vite.

---

## Stack Overview

| Tool | Version | Role |
| :--- | :--- | :--- |
| Vue 3 | 3.4.x | Framework (Composition API, `<script setup>`) |
| Vue Router | 5.x | Client-side routing |
| TypeScript | 6.x | All source files are `.ts` / `<script setup lang="ts">` |
| Vite | 8.x | Dev server (HMR), production bundler |
| Vitest | 4.x | Unit / component tests (happy-dom + `@vue/test-utils`) |
| Tailwind CSS | 4.x | Utility styling via `@tailwindcss/vite` plugin |
| DaisyUI | 5.x | Tailwind plugin: component classes (`btn`, `card`, `modal`, `toast`, …) + the multi-theme system |
| PrimeIcons | 7.x | Icon font (`pi pi-*`). There is no Vue component library (e.g. PrimeVue) — components are built from DaisyUI/Tailwind classes |
| DOMPurify | 3.x | Client-side re-sanitization of book annotations before `v-html` (defense-in-depth on top of the backend's bluemonday pass — see [EBOOK-PARSING.md](./EBOOK-PARSING.md#annotation-rendering-pipeline)) |

---

## Project Root: `web/`

The frontend is a standalone npm project inside `web/`. It has its own `package.json`, `tsconfig.json`, and `vite.config.ts`.

### npm Scripts

| Script | Command | Purpose |
| :--- | :--- | :--- |
| `dev` | `vite` | Dev server with HMR on `:5173` |
| `build` | `vue-tsc --noEmit && vite build` | Type-check then bundle to `dist/` |
| `preview` | `vite preview` | Preview production build locally |
| `test` | `vitest run` | Run unit/component tests once |
| `test:watch` | `vitest` | Run tests in watch mode |
| `test:ci` | `vitest run --coverage --reporter=default --reporter=json` | Tests + v8 coverage + JSON results (consumed by CI for badges/gates) |
| `lint` | `eslint . --ext .ts,.vue` | ESLint over all TS/Vue sources |
| `lint:fix` | `eslint . --ext .ts,.vue --fix` | ESLint with auto-fix |
| `format` | `prettier --write "src/**/*.{ts,vue,css}"` | Prettier write |
| `format:check` | `prettier --check "src/**/*.{ts,vue,css}"` | Prettier check (CI lint gate) |

The `build` script enforces type safety: `vue-tsc --noEmit` runs the TypeScript compiler against all `.ts` and `.vue` files before Vite bundles anything.

---

## Vite Configuration

**File:** `web/vite.config.ts`

### Plugins

1. `vue()` — Vue 3 SFC compilation.
2. `tailwindcss()` — Tailwind CSS v4 via the native Vite plugin. No PostCSS or Autoprefixer needed. DaisyUI is not a Vite plugin — it is loaded as a Tailwind plugin from `style.css` (`@plugin "daisyui"`).

`vite.config.ts` also holds the Vitest block (`test`): `happy-dom` environment,
`src/**/*.spec.ts` includes, a shared `src/test/setup.ts`, and v8 coverage with
a `json-summary` reporter consumed by CI for the coverage badge.

### Path Alias

`@` → `./src` (mirrored in `tsconfig.json` `paths`).

### Dev Proxy

```
/api → http://localhost:8080
```

During local development, the Vite dev server on `:5173` proxies `/api/*` to the Go backend on `:8080`. This enables working on the frontend independently while hitting the live API. OPDS routes are not proxied because they are consumed by reading apps (Moon+ Reader, KyBook), not the browser SPA.

---

## TypeScript Configuration

**File:** `web/tsconfig.json`

### Key Settings

| Setting | Value | Why |
| :--- | :--- | :--- |
| `target` | `ES2020` | Modern baseline, covers async/await and optional chaining natively |
| `module` | `ESNext` | Vite expects ES module output |
| `moduleResolution` | `bundler` | Vite-native resolution (not Node) |
| `strict` | `true` | Full strict mode |
| `noUnusedLocals` | `true` | Catch dead code |
| `noUnusedParameters` | `true` | Catch dead params |
| `noImplicitReturns` | `true` | Every code path must return |
| `isolatedModules` | `true` | Required for Vite/esbuild compatibility |
| `noEmit` | `true` | TypeScript is check-only; Vite handles emit |

### Included Files

```json
["src/**/*.ts", "src/**/*.d.ts", "src/**/*.vue", "vite.config.ts"]
```

`vite.config.ts` is explicitly included so that `@types/node` resolves for `fileURLToPath` / `import.meta.url`.

---

## Styling Architecture

### Entry: `web/src/style.css`

```css
@import 'tailwindcss';

@plugin "daisyui" {
  themes: all;
}

/* Chrome renderer-crash workaround — see note below. */
:root:has(.modal-open),
:root:has(.modal[open]),
:root:has(.modal:target) {
  animation: none !important;
}

@layer base {
  body {
    font-family: 'Inter', -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;
    margin: 0;
    padding: 0;
    min-height: 100vh;
  }
}
```

> **Chrome renderer-crash workaround (do not remove):** DaisyUI v5's `.modal`
> attaches a scroll-driven animation to the document root while a modal is open
> (`:root:has(.modal-open) { animation: … scroll() }`, for scrollbar-gutter
> stabilization). Rapidly opening/closing the modal — e.g. clicking books in
> quick succession — repeatedly creates and destroys that `scroll()`-timeline
> animation on `:root`, which **crashes Chrome's renderer** (EXC_BREAKPOINT, no
> JS error; only visible in `chrome://crashes`). The `animation: none` override
> above disables it; the page scroll-lock set by the same DaisyUI rule is
> unaffected.

**Tailwind v4 + DaisyUI notes:**
- `@import "tailwindcss"` replaces the v3 `@tailwind base/components/utilities` directives.
- `@plugin "daisyui" { themes: all; }` enables DaisyUI's component classes and
  registers **all** of its ~35 built-in themes; the active one is chosen at
  runtime by the `data-theme` attribute (see [Theming](#theming)).
- No `tailwind.config.js` — Tailwind v4 uses CSS-first configuration.
- No PostCSS config — `@tailwindcss/vite` handles everything.

### Icons

Only the PrimeIcons font is imported in `main.ts`. There is no Vue component
library (e.g. PrimeVue); components are composed from DaisyUI/Tailwind classes.
```ts
import 'primeicons/primeicons.css';
```

### Theming

Theming is driven by **DaisyUI themes**, not a hand-rolled palette. `style.css`
registers all DaisyUI themes (`themes: all`); the active theme is whatever
`data-theme` value is set on `<html>`.

The `useTheme` composable (`web/src/composables/useTheme.ts`) is a module-level
singleton holding the current theme **id**. It exports a curated `THEMES` list —
the subset surfaced in the picker (Light, Fantasy, CMYK, Dark, Abyss, Dim). Only
these 6 curated themes are supported and restored from `localStorage`. It defaults
from the OS `prefers-color-scheme` (light → `light`, else `dark`), persists the
choice to `localStorage` (key `folio-theme`), and `applyTheme` writes `data-theme`
onto `document.documentElement`. The `ThemePicker.vue` dropdown in the topbar lets
the user pick a theme from `THEMES`.

---

## Type Declarations

**File:** `web/src/vite-env.d.ts`

Provides:
- Vite client types (`/// <reference types="vite/client" />`)
- Vue SFC module declarations (`declare module '*.vue'`)

---

## Application Entry

**File:** `web/src/main.ts`

```ts
const app = createApp(App);
app.use(router);
app.mount('#app');
```

Bootstrap is minimal: create Vue app → register the router → mount to `#app` div in `index.html`.

---

## UI Structure

### Shell layout

A persistent shell wraps every route: a fixed left sidebar + a sticky topbar +
a scrollable main area.

```
+-------------------+--------------------------------------+
| Sidebar (fixed)   |  Top Bar (sticky)                    |
|  Brand            |  [Search bar (chips)]       [T] [S]  |
|  Books            +--------------------------------------+
|  Authors          |                                      |
|  Series           |  Main Content Area (scrollable)      |
|  Tags             |                                      |
|  Publishers       |                                      |
+-------------------+--------------------------------------+
```

`[T]` = theme picker, `[S]` = settings. The sidebar links are Books (`/`),
Authors, Series, Tags, Publishers; the topbar holds the library selector, the
tokenized search bar (see [Search & filters](#search--filters-searchinputvue)),
the theme picker, and a settings gear.

### Pages

- **Books grid (`/`)** — responsive cover-dominant card grid (`BookCard`,
  ~150px min, 2:3 cover, title + author, whole card links to `/books/:id`);
  `BookCard` loads the server-provided `thumbnail_url` (a downscaled cover, ≤400px
  longest side) rather than the full cover, reducing grid bandwidth. The grid uses
  infinite scroll (`useInfiniteScroll`, loads `?page=N`, spinner at the bottom,
  no pagination controls). Active filters are shown as dismissible chips inside
  the search bar (no separate filter bar). The grid also **auto-refreshes when a
  background sync finishes**: it watches the sync heartbeat's `current` library id
  (`useSyncStatus`) and reloads page 1 whenever that id leaves a non-zero value —
  the moment a library's rows are committed — so newly indexed books appear
  without a manual page refresh. Keying off the *departed* id (not the arriving
  one) skips a wasted reload at sync *start* and refreshes once per library in a
  queued multi-library run.
- **Book detail (`/books/:id`)** — a **modal dialog** (`BookDetailModal.vue`,
  DaisyUI `modal`) overlaid on the books grid, not a standalone page: navigating
  to `/books/:id` keeps `BooksPage` mounted and opens the modal; closing
  (✕ button, backdrop click, or Escape) returns to `/` preserving the active
  query. Its body (`BookDetail.vue`) is two columns: large cover left, metadata
  right (title, linked authors, series + `#index`, tag chips, publisher, year,
  pages, language), external identifier links, one download button per format
  (with file size), and the rendered HTML annotation (sanitized server-side and
  re-sanitized client-side via DOMPurify before `v-html`).
- **List pages (`/authors`, `/series`, `/tags`, `/publishers`)** — share an
  `AlphabetSelector` + `AlphabetList` (via `useAlphabetBrowse`). They load **one
  alphabet bucket at a time** — `/api/<entity>/letters` lights up the selector
  and `/api/<entity>?letter=` fetches that bucket — which keeps them fast on
  large (e.g. Cyrillic) libraries. Each entry shows a name + book count and links
  back to the grid with an exact filter applied (e.g. `/?author==…`).
- **Settings (`/settings`)** — a single page with OPDS settings (username +
  write-only password with "set"/"not set" status) and library management (list
  with status/last-sync/book-count, add-library form, and the per-library actions
  described under [Notable component behaviors](#notable-component-behaviors)).

### Routing

Routes: `/`, `/books/:id`, `/authors`, `/series`, `/tags`, `/publishers`,
`/settings`, plus a catch-all `/:pathMatch(.*)*` that redirects to `/` (the
backend's SPA fallback serves `index.html` for unknown paths, so without the
redirect the router would match nothing and render a blank view). `/books/:id`
reuses `BooksPage` and opens the detail modal over the
grid (there is no separate detail-page component). List items don't use `/:id`
detail routes — they navigate to the grid with a query filter.

Two robustness details live in `router.ts` and the list loaders:

- **Stale-deploy chunk reloads** — routes are lazy imports, so after a redeploy
  a stale tab's next navigation can 404 the old hashed chunk and render
  nothing. `router.onError` + `isChunkLoadError` reload the page once to pick
  up the new manifest (a `sessionStorage` flag, cleared on the next successful
  navigation, prevents reload loops).
- **Stale-response guards** — `BooksPage.loadBooks` and
  `useAlphabetBrowse.loadPage` carry a generation counter bumped on every
  reset (filter change, letter change, reload); a response from a superseded
  generation is discarded instead of appending stale rows into the fresh list.

Dark/light theming is described under [Theming](#theming).

### Component & data structure

```
App.vue
├── AppShell.vue
│   ├── AppTopBar.vue → LibrarySelect.vue, SearchInput.vue, ThemePicker.vue
│   ├── SyncBanner.vue     # global sync-heartbeat banner (useSyncStatus)
│   ├── <router-view> → BooksPage · AuthorListPage · SeriesListPage ·
│   │                   TagListPage · PublisherListPage · SettingsPage
│   └── AppSidebar.vue
├── ToastHost.vue          # renders queued toasts (DaisyUI `toast`)
└── ConfirmModal.vue       # app-wide confirm dialog (DaisyUI `modal`, useConfirm)
BooksPage → BookCard.vue (grid; star rating) + sort control
          + BookDetailModal.vue → BookDetail.vue → FixMatchModal.vue
            (the /books/:id detail overlay; "Fix match" opens the modal)
SettingsPage (thin composition surface) → LibraryStats.vue (catalog overview +
            Re-index All slot) · settings/OpdsSettingsForm.vue ·
            settings/LibraryTable.vue · settings/LibraryForm.vue
Shared: AlphabetSelector.vue, AlphabetList.vue
Composables: useInfiniteScroll.ts, useTheme.ts, useLibrary.ts, useLibraryActions.ts,
             useFacetValues.ts, useSyncStatus.ts, useAlphabetBrowse.ts,
             useToast.ts, useConfirm.ts, useModalFocus.ts
Utils: libraryStatus.ts (statusClass / statusLabel / typeLabel)
```

`useToast`/`ToastHost` and `useConfirm`/`ConfirmModal` replace ad-hoc
`alert`/`confirm` calls with app-styled DaisyUI components; `useAlphabetBrowse`
backs the list pages (loads one alphabet bucket at a time — see
[List pages](#pages) and [API.md](./API.md#rest-api-api)). `useModalFocus`
gives the three class-toggled modals (`BookDetailModal`, `ConfirmModal`,
`FixMatchModal`) the keyboard behavior a native `showModal()` would provide:
focus moves into the dialog on open, Tab is trapped inside it, focus returns to
the trigger on close, and Escape closes only the **top-most** modal (a shared
stack, so a confirm layered over another modal never closes both).

There is **no Pinia**, and most pages still fetch directly through a thin
`api.ts` `fetch()` wrapper that attaches a 30s `AbortSignal.timeout` to every
request (a hung fetch fails into the normal toast path as "request timed out"
instead of spinning forever) — `fetchBooks`, `fetchBook`, `searchMatch`,
`applyMatch`, `fetchAuthors`, `fetchLibraries`, `createLibrary`, `deleteLibrary`,
`syncLibrary`, `reactivateLibrary`, `forcePurgeLibrary`, `fetchSettings`,
`updateSettings`, `fetchFacets`, and so on. A few **feature composables** now sit
over that wrapper where logic is shared or stateful: `useLibraryActions`
centralizes every library mutation (create/update/sync/reindex/delete/reactivate/
purge) with the toast + refresh handling, and `useFacetValues` loads the search
bar's Format/Language pickers from `/api/facets`.

Pages are **composition surfaces, not monoliths.** `SettingsPage` is a thin shell
that delegates to `OpdsSettingsForm`, `LibraryStats`, `LibraryTable`, and
`LibraryForm` (props down, events up) over `useLibraryActions` / `useSyncStatus`,
with the row status/type formatting in the `libraryStatus` util. The book detail
is split the same way — `BookDetailModal` / `BookDetail` so the grid can host it
as an overlay.

The one piece of ambient state is the **selected library**: `useLibrary.ts` is a
module-level singleton (same pattern as `useTheme.ts`) holding `libraryId`
(persisted to `localStorage` as `folio-library`, `null` = All) plus the fetched
`libraries` list. `LibrarySelect.vue` in the header drives it; every browse page
passes `libraryId` into its fetch and `watch`es it to refetch, so the whole app
stays scoped to the chosen library (search, authors, series, tags, publishers,
stats). It is **not** reflected in the URL.

---

## Notable component behaviors

A few interactions are easy to regress, so they are spelled out here and covered
by Vitest specs.

### Search & filters (`SearchInput.vue`)

`SearchInput.vue` is a tokenized search bar and the single surface for all active
filters. `route.query` is the source of truth; chips are derived from it (so
navigation and removal stay in sync — the bar never holds stale local text).

- Focus the input to open the facet menu: **Author**, **Book Title**,
  **Book Series**.
- Pick a facet, type a value, press Enter to commit a chip. A value beginning
  with `=` (e.g. `=Terry Pratchett`) commits an **exact** match; otherwise it is a
  token-level full-text search.
- Typing without choosing a facet commits a free-text `q` chip (searches all
  fields).
- `tag` and `publisher` are exact-only and not in the menu; they appear as chips
  when reached via navigation links (browse-by-letter, book detail). Author/series
  nav links use the exact (`=`) form.
- Removing a chip (✕) clears that one route param.
- Editing chips preserves the current `sort` order. The sort `<select>` lives on
  `BooksPage`, not the search bar, so `nextQuery()` explicitly carries `route.query.sort`
  through a commit/remove — without it, touching a chip would silently reset the
  user's chosen order to the default.

There is no separate active-filters bar; the search bar renders every active
filter. Covered by `SearchInput.spec.ts` (real memory-router tests).

### Settings → Libraries tab (`SettingsPage.vue`)

- **Status-branched actions.** Each library card branches on `library.status`. A
  `pending_purge` library hides **Sync**/**Delete** and instead shows
  **Reactivate** and **Purge Now** (the latter behind a `ConfirmModal` via
  `useConfirm`), plus a
  "Books purge {purge_at}" note; all other statuses keep Sync/Delete. The
  matching API clients are `reactivateLibrary` and `forcePurgeLibrary` in
  `api.ts`. (Books remain visible until the grace period ends or Purge Now runs
  — `DELETE` only starts the countdown.)
- **Live polling.** While the Libraries tab is open the page polls `fetchLibraries`
  every 5 s (`watch(tab)` starts/stops the timer; cleared on unmount), so a card
  reflects `syncing → active`/`error` without a manual refresh. The backend
  leaves the DB status `active` on success / writes `error` on failure;
  `syncing` is in-memory only, so without polling the card froze on whatever
  status it first rendered. This tab-scoped list poll is **separate from** the
  app-wide sync heartbeat (`useSyncStatus`, a steady 3 s interval driving the global
  `SyncBanner`). Both must stay **fixed intervals — never an idle backoff**: a fast
  sync (a bad path fails in milliseconds, a small library indexes in seconds) can
  start *and* finish inside a longer gap, so a slower idle cadence silently misses
  the whole run — the banner never shows and the status never settles. The list poll
  exists precisely because that running window is unobservable to the heartbeat for
  such syncs, so the list must re-read regardless of what the heartbeat saw.
- **Empty state.** When `libraries.length === 0` the tab renders a centered
  `.source-empty` placeholder ("No libraries yet — add one below to start indexing
  your library.") instead of a collapsed list, so the gap between the tab border
  and the add-library separator reads intentionally.
- **Re-index All.** Above the libraries table (shown only when at least one
  library exists) a **Re-index All** button (behind a `ConfirmModal`) calls
  `triggerReindexAll` → `POST /api/sync`, which forces a full re-read of every
  library, bypassing checkpoint gating. Per-library **Sync Now** is forced the
  same way; automatic (scheduled/watcher) syncs stay gated.
- **`LibraryStats` is deliberately global.** The catalog-overview card at the top
  of the tab (`LibraryStats.vue` → `fetchStats()` with no `library` arg) always
  reports whole-catalog totals and **ignores the header library selector**
  (decision 2026-06-13): it sits above the all-libraries management table, where
  a per-library scope would be misleading.

Covered by `SettingsPage.spec.ts` (Reactivate/Purge-Now branch, Re-index All,
polling start/stop, empty-state) and `api.spec.ts` (purge path).

### Ratings & Fix Match (`BookCard.vue`, `BookDetail.vue`, `FixMatchModal.vue`)

- **Star ratings.** `BookCard.vue` (grid) and `BookDetail.vue` render a 1–5 star
  row when `book.rating` is set (`pi-star-fill` / `pi-star`); unrated books show
  none. `BooksPage` adds a "Newest / Top rated" sort `<select>` that writes
  `?sort=rating` to the route, which the grid `watch`es and refetches.
- **Fix Match.** `BookDetail.vue` has a "Fix match" button that opens
  `FixMatchModal.vue`, prefilled with the book's title + authors. The modal calls
  `searchMatch(id, q)` to list Google Books candidates and
  `applyMatch(id, volumeId)` to overwrite the metadata; on success it emits the
  updated book, which `BookDetailModal` swaps in (the cover refreshes
  automatically because its `?v=<content_hash>-<cover mtime>` cache-buster
  changed). Covered by
  `FixMatchModal.spec.ts`.

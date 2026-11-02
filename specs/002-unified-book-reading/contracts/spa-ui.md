# Contract: SPA UI

## Types (`web/src/types.ts`)

```ts
interface Library { /* … */ unreadable_count: number }
interface Book    { /* … */ unreadable?: { reason: UnreadableReason } }
type UnreadableReason = 'corrupt' | 'missing_entry' | 'too_large' | 'crashed';
interface BookFilters { /* … */ unreadable?: boolean }  // serialized as unreadable=1
```

## Settings → Libraries row (`components/settings/LibraryTable.vue`)

| Condition | Renders |
|---|---|
| `unreadable_count > 0` | Under the book count, a small warning link `⚠ {N} unreadable` (`text-warning text-xs`, `data-testid="library-unreadable"`) |
| `unreadable_count === 0` | Nothing extra |

Clicking the link calls `setLibrary(library.id)` (the ambient library selection from `useLibrary`), then `router.push({ path: '/', query: { unreadable: '1' } })`. That is two clicks from Settings to the list (SC-006). The link stays enabled while the library syncs. Its `href` is `/?unreadable=1&library={id}`, so opening it in a new tab works too: `BooksPage` adopts a `library` query param as the ambient selection, then drops it from the URL.

## Book list (`pages/BooksPage.vue`, `components/SearchInput.vue`)

- `filters.unreadable = route.query.unreadable === '1'`, passed to `fetchBooks`.
- `SearchInput` shows a removable chip labelled `Unreadable` (warning badge) while the param is `1`; any other value shows no chip, matching the filter. Removing it drops the param.
- The empty state is the existing "no books" message.

## Book page (`components/BookDetail.vue`)

When `book.unreadable` is set, a notice renders in the description section (`role="status"`, `data-testid="unreadable-notice"`, plain text, never `v-html`). If the book has an annotation, the notice sits above it:

> Folio couldn't read this book's file: {reason label} Downloading it may still work.

Reason labels (each ends with its own period, so the sentence adds none):

| reason | label |
|---|---|
| `corrupt` | The file is damaged or not a valid ebook. |
| `missing_entry` | The file is missing from its archive. |
| `too_large` | The file is too large to read. |
| `crashed` | Folio's reader couldn't process this file. |

There is no retry control anywhere (FR-024).

## Vitest coverage

- `LibraryTable.spec.ts`: the link appears for N > 0, is hidden for 0, and navigates with the library set and `unreadable=1`.
- `BooksPage.spec.ts` / `SearchInput.spec.ts`: the query param reaches `fetchBooks`, and the chip renders and can be removed.
- `BookDetail.spec.ts`: the notice shows the label for each reason, and is absent when `unreadable` is unset.
- `api.spec.ts`: `buildQuery` emits `unreadable=1` only when true.

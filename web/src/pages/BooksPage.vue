<template>
  <div>
    <div class="mb-5 flex justify-end">
      <select
        :value="sort"
        aria-label="Sort books"
        class="select select-sm select-bordered w-auto"
        @change="setSort(($event.target as HTMLSelectElement).value)"
      >
        <option value="">Recently added</option>
        <option value="source">Newest</option>
        <option value="rating">Top rated</option>
      </select>
    </div>

    <div
      v-if="loadError && books.length === 0"
      data-testid="books-load-error"
      class="flex flex-col items-center gap-3 py-16 text-center"
    >
      <span class="text-error text-sm" role="alert">Couldn't load books.</span>
      <button
        type="button"
        data-testid="books-retry"
        class="btn btn-sm"
        @click="loadBooks('replace')"
      >
        Retry
      </button>
    </div>

    <div class="grid gap-x-5 gap-y-7 [grid-template-columns:repeat(auto-fill,minmax(150px,1fr))]">
      <BookCard v-for="book in books" :key="book.id" :book="book" />
    </div>

    <div ref="scrollTrigger" class="flex min-h-20 justify-center py-8">
      <span v-if="loading" class="loading loading-spinner loading-lg text-base-content/60" />
    </div>

    <BookDetailModal :id="currentBookId" @close="closeBook" @updated="onBookUpdated" />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';

import { fetchBooks } from '@/api';
import BookCard from '@/components/BookCard.vue';
import BookDetailModal from '@/components/BookDetailModal.vue';
import { useInfiniteScroll } from '@/composables/useInfiniteScroll';
import { useLibrary } from '@/composables/useLibrary';
import { useSyncStatus } from '@/composables/useSyncStatus';
import { useToast } from '@/composables/useToast';
import type { Book, BookFilters } from '@/types';
import { asString } from '@/utils/query';

const route = useRoute();
const router = useRouter();
const { libraryId, setLibrary } = useLibrary();

// A link opened outside the SPA router (e.g. Settings' unreadable count in a new
// tab) names its library as ?library=; adopt it as the ambient selection, which
// localStorage alone would not carry, then drop it from the URL so the selector
// stays the single source of truth.
const linkedLibrary = Number(asString(route.query.library));
if (Number.isInteger(linkedLibrary) && linkedLibrary > 0) {
  setLibrary(linkedLibrary);
  const query = { ...route.query };
  delete query.library;
  void router.replace({ path: route.path, query });
}
const { current: syncingLibrary } = useSyncStatus();
const toast = useToast();

const currentBookId = computed(() => {
  const id = route.params.id;
  return id ? Number(id) : null;
});

function closeBook(): void {
  router.push({ path: '/', query: route.query });
}

// Patch the grid card in place after an enrich/Fix-Match update so its cover/title
// (and cache-buster) refresh without a full reload.
function onBookUpdated(updated: Book): void {
  const i = books.value.findIndex((b) => b.id === updated.id);
  if (i !== -1) books.value[i] = updated;
}

const PAGE_SIZE = 24;

const books = ref<Book[]>([]);
const page = ref(1);
// Set when the first page of a fresh result set failed, so the grid shows a
// retry state instead of a blank page.
const loadError = ref(false);
// Starts false so the IntersectionObserver, which can fire against the empty
// grid before the first page resolves, doesn't kick off a concurrent page-2
// fetch. It's flipped true only after a load reports more rows are available.
const hasMore = ref(false);
const scrollTrigger = ref<HTMLElement | null>(null);

const sort = computed(() => asString(route.query.sort));

const filters = computed<BookFilters>(() => ({
  q: asString(route.query.q) || undefined,
  title: asString(route.query.title) || undefined,
  author: asString(route.query.author) || undefined,
  series: asString(route.query.series) || undefined,
  tag: asString(route.query.tag) || undefined,
  publisher: asString(route.query.publisher) || undefined,
  format: asString(route.query.format) || undefined,
  lang: asString(route.query.lang) || undefined,
  unreadable: route.query.unreadable === '1',
  sort: sort.value || undefined,
  library: libraryId.value ?? undefined,
}));

function setSort(value: string): void {
  const query = { ...route.query };
  if (value) query.sort = value;
  else delete query.sort;
  router.push({ path: route.path, query });
}

// loadGen invalidates in-flight responses: a reload bumps the generation, so a
// slow response from the previous filter (or an in-flight page load) can't
// append stale rows into the fresh result set.
let loadGen = 0;

// How loadBooks treats the rows already on screen:
//   more    — append the next page (infinite scroll);
//   replace — a new result set (mount, filter change, retry): clear the grid now,
//             since the old rows no longer match the filters;
//   refresh — the same result set re-read (a sync finished): keep the rows on
//             screen until page 1 arrives, and keep them if the reload fails.
type LoadMode = 'more' | 'replace' | 'refresh';

async function loadBooks(mode: LoadMode = 'more'): Promise<boolean> {
  if (mode !== 'more') loadGen += 1;
  if (mode === 'replace') {
    page.value = 1;
    books.value = [];
    hasMore.value = false;
    loadError.value = false;
  }
  const gen = loadGen;
  const pageNo = mode === 'more' ? page.value : 1;

  try {
    const res = await fetchBooks({ ...filters.value, page: pageNo, limit: PAGE_SIZE });
    if (gen !== loadGen) return true; // superseded by a newer reload
    if (mode === 'more') {
      books.value.push(...res.items);
    } else {
      books.value = res.items;
      page.value = 1;
    }
    hasMore.value = books.value.length < res.total;
    loadError.value = false;
    return true;
  } catch (err) {
    if (gen !== loadGen) return true; // superseded; the newer load reports its own errors
    if (mode === 'more' && page.value > 1) {
      // Roll back the optimistic increment so the next scroll retries this page
      // instead of skipping it.
      page.value -= 1;
    } else if (mode === 'refresh') {
      // The rows stay; re-derive the page from them, since a scroll load this
      // refresh superseded may have advanced it without appending.
      page.value = Math.max(1, Math.ceil(books.value.length / PAGE_SIZE));
    } else {
      loadError.value = true;
    }
    toast.error(`Failed to load books: ${(err as Error).message}`);
    return false;
  }
}

async function loadMore(): Promise<void> {
  if (!hasMore.value) return;
  page.value++;
  // Reject on failure so useInfiniteScroll does NOT re-arm the observer (which,
  // with the sentinel still visible, would retry-storm the failing request). The
  // original observation stays intact, so a manual scroll still retries.
  if (!(await loadBooks())) {
    throw new Error('load more failed');
  }
}

const { loading } = useInfiniteScroll(scrollTrigger, loadMore, () => hasMore.value);

// Watch the serialized filters, not the computed object: `filters` returns a fresh
// object every tick, so an identity watch would fire on every unrelated reactive
// change and a deep watch would re-run on cosmetic churn. Serializing to a stable
// string makes the watcher fire exactly when a filter *value* changes.
watch(
  () => JSON.stringify(filters.value),
  (newVal, oldVal) => {
    if (newVal !== oldVal) {
      loadBooks('replace');
    }
  },
);

// The grid is otherwise a one-shot load (onMounted + filter changes), so books
// indexed by a background sync never appear until a manual page refresh. The
// sync engine reports the library id it's currently working (0 when idle); a
// library *finishes* exactly when that id moves away from a non-zero value — at
// which point its rows are committed and we reload to surface them. Keying off
// the departed id (not the arriving one) means we don't reload on sync *start*,
// when nothing new exists yet, and we refresh once per library in a queued run.
watch(syncingLibrary, (_now, previous) => {
  if (previous !== 0) loadBooks('refresh');
});

onMounted(() => loadBooks('replace'));
</script>

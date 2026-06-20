import type { Ref } from 'vue';
import { onMounted, ref, watch } from 'vue';

import { ALPHABET } from '@/alphabet';

import { useInfiniteScroll } from './useInfiniteScroll';
import { useLibrary } from './useLibrary';
import { useToast } from './useToast';

const PAGE_SIZE = 100;

// useAlphabetBrowse drives the author/series/tag/publisher browse pages, which
// share one shape: an alphabet selector plus a list loaded one letter at a
// time. It fetches the available letters, defaults to the first one, and loads
// the active letter's entries page by page via infinite scroll. Loading a
// single bucket (rather than the whole table) is what keeps these pages fast on
// a large library.
export function useAlphabetBrowse<T>(
  scrollTrigger: Ref<HTMLElement | null>,
  fetchLetters: (library?: number) => Promise<string[]>,
  fetchItems: (
    letter: string,
    library: number | undefined,
    page: number,
    limit: number,
  ) => Promise<T[]>,
) {
  const { libraryId } = useLibrary();
  const toast = useToast();

  const items = ref<T[]>([]) as Ref<T[]>;
  const availableLetters = ref<Set<string>>(new Set());
  const activeLetter = ref<string | null>(null);

  let page = 1;
  let hasMore = false;
  // gen invalidates in-flight pages: selecting a new letter (or a reload)
  // resets items and bumps it, so a slow response for the previous letter
  // can't append into the new bucket.
  let gen = 0;

  async function loadPage(): Promise<void> {
    if (!activeLetter.value) return;
    const g = gen;
    try {
      const batch = await fetchItems(
        activeLetter.value,
        libraryId.value ?? undefined,
        page,
        PAGE_SIZE,
      );
      if (g !== gen) return; // superseded by a newer letter/reload
      items.value.push(...batch);
      hasMore = batch.length === PAGE_SIZE;
    } catch (err) {
      if (g !== gen) return;
      if (page > 1) page -= 1; // retry this page on next scroll
      toast.error(`Failed to load entries: ${(err as Error).message}`);
    }
  }

  async function selectLetter(letter: string): Promise<void> {
    gen += 1;
    activeLetter.value = letter;
    page = 1;
    items.value = [];
    hasMore = false;
    await loadPage();
  }

  async function loadMore(): Promise<void> {
    if (!hasMore) return;
    page += 1;
    await loadPage();
  }

  async function reload(): Promise<void> {
    gen += 1;
    items.value = [];
    activeLetter.value = null;
    try {
      const letters = await fetchLetters(libraryId.value ?? undefined);
      availableLetters.value = new Set(letters);
    } catch (err) {
      toast.error(`Failed to load letters: ${(err as Error).message}`);
      return;
    }
    const first = ALPHABET.find((l) => availableLetters.value.has(l));
    if (first) await selectLetter(first);
  }

  const { loading } = useInfiniteScroll(scrollTrigger, loadMore);

  onMounted(reload);
  watch(libraryId, () => void reload());

  return { items, availableLetters, activeLetter, loading, selectLetter };
}

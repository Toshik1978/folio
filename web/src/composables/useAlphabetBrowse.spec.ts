import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { defineComponent, h, ref } from 'vue';

import { useLibrary } from '@/composables/useLibrary';

import { useAlphabetBrowse } from './useAlphabetBrowse';

// useInfiniteScroll is exercised in its own spec; here we mock it to capture the
// loadMore callback so pagination can be driven directly (no IntersectionObserver).
let capturedLoadMore: (() => Promise<void>) | null = null;
vi.mock('./useInfiniteScroll', () => ({
  useInfiniteScroll: (_trigger: unknown, onLoadMore: () => Promise<void>) => {
    capturedLoadMore = onLoadMore;
    return { loading: { value: false } };
  },
}));

const toastError = vi.fn();
vi.mock('./useToast', () => ({
  useToast: () => ({
    error: toastError,
    success: vi.fn(),
    toasts: { value: [] },
    dismiss: vi.fn(),
  }),
}));

type Item = { id: number };

function mountBrowse(
  fetchLetters: (library?: number) => Promise<string[]>,
  fetchItems: (
    letter: string,
    library: number | undefined,
    page: number,
    limit: number,
  ) => Promise<Item[]>,
) {
  let api!: ReturnType<typeof useAlphabetBrowse<Item>>;
  const wrapper = mount(
    defineComponent({
      setup() {
        api = useAlphabetBrowse(ref(null), fetchLetters, fetchItems);
        return () => h('div');
      },
    }),
  );
  return { api, wrapper };
}

const fullPage = (): Item[] => Array.from({ length: 100 }, (_, i) => ({ id: i }));

describe('useAlphabetBrowse', () => {
  beforeEach(() => {
    toastError.mockClear();
    capturedLoadMore = null;
    localStorage.clear();
    useLibrary().setLibrary(null);
  });

  it('on mount loads letters, defaults to the first present, and loads its first page', async () => {
    const fetchLetters = vi.fn().mockResolvedValue(['C', 'A']); // unordered
    const fetchItems = vi.fn().mockResolvedValue([{ id: 1 }]);
    const { api } = mountBrowse(fetchLetters, fetchItems);
    await flushPromises();

    expect(fetchLetters).toHaveBeenCalledWith(undefined);
    expect(api.availableLetters.value.has('A')).toBe(true);
    expect(api.availableLetters.value.has('C')).toBe(true);
    expect(api.activeLetter.value).toBe('A'); // first in ALPHABET order
    expect(fetchItems).toHaveBeenCalledWith('A', undefined, 1, 100);
    expect(api.items.value).toEqual([{ id: 1 }]);
  });

  it('selectLetter switches the active letter and resets the list', async () => {
    const fetchLetters = vi.fn().mockResolvedValue(['A', 'C']);
    const fetchItems = vi.fn().mockResolvedValue([{ id: 1 }]);
    const { api } = mountBrowse(fetchLetters, fetchItems);
    await flushPromises();

    fetchItems.mockClear();
    fetchItems.mockResolvedValue([{ id: 2 }]);
    await api.selectLetter('C');

    expect(api.activeLetter.value).toBe('C');
    expect(fetchItems).toHaveBeenCalledWith('C', undefined, 1, 100);
    expect(api.items.value).toEqual([{ id: 2 }]);
  });

  it('loadMore fetches the next page while a full batch signals more', async () => {
    const fetchLetters = vi.fn().mockResolvedValue(['A']);
    const fetchItems = vi.fn().mockResolvedValue(fullPage()); // first page is full
    const { api } = mountBrowse(fetchLetters, fetchItems);
    await flushPromises();
    expect(api.items.value).toHaveLength(100);

    fetchItems.mockClear();
    fetchItems.mockResolvedValue([{ id: 999 }]); // short → no more after this
    await capturedLoadMore!();
    expect(fetchItems).toHaveBeenCalledWith('A', undefined, 2, 100);
    expect(api.items.value).toHaveLength(101);

    // hasMore is now false: a further scroll is a no-op.
    fetchItems.mockClear();
    await capturedLoadMore!();
    expect(fetchItems).not.toHaveBeenCalled();
  });

  it('loadMore does nothing when the first batch was already short', async () => {
    const fetchLetters = vi.fn().mockResolvedValue(['A']);
    const fetchItems = vi.fn().mockResolvedValue([{ id: 1 }]); // short
    mountBrowse(fetchLetters, fetchItems);
    await flushPromises();

    fetchItems.mockClear();
    await capturedLoadMore!();
    expect(fetchItems).not.toHaveBeenCalled();
  });

  it('toasts and stops when loading letters fails', async () => {
    const fetchLetters = vi.fn().mockRejectedValue(new Error('letters boom'));
    const fetchItems = vi.fn();
    const { api } = mountBrowse(fetchLetters, fetchItems);
    await flushPromises();

    expect(toastError).toHaveBeenCalledWith(expect.stringContaining('letters boom'));
    expect(api.activeLetter.value).toBeNull();
    expect(api.items.value).toEqual([]);
    expect(fetchItems).not.toHaveBeenCalled();
  });

  it('toasts and rolls the page back so a failed page can be retried', async () => {
    const fetchLetters = vi.fn().mockResolvedValue(['A']);
    const fetchItems = vi.fn().mockResolvedValue(fullPage());
    mountBrowse(fetchLetters, fetchItems);
    await flushPromises();

    // Page 2 fails: the page counter must roll back so the next scroll retries it.
    fetchItems.mockClear();
    fetchItems.mockRejectedValueOnce(new Error('page boom'));
    await capturedLoadMore!();
    expect(toastError).toHaveBeenCalledWith(expect.stringContaining('page boom'));

    fetchItems.mockResolvedValueOnce([{ id: 7 }]);
    await capturedLoadMore!();
    expect(fetchItems).toHaveBeenLastCalledWith('A', undefined, 2, 100);
  });

  it('reloads when the selected library changes', async () => {
    const fetchLetters = vi.fn().mockResolvedValue(['A']);
    const fetchItems = vi.fn().mockResolvedValue([{ id: 1 }]);
    mountBrowse(fetchLetters, fetchItems);
    await flushPromises();
    expect(fetchLetters).toHaveBeenLastCalledWith(undefined);

    fetchLetters.mockClear();
    fetchItems.mockClear();
    useLibrary().setLibrary(5);
    await flushPromises();

    expect(fetchLetters).toHaveBeenCalledWith(5);
    expect(fetchItems).toHaveBeenCalledWith('A', 5, 1, 100);
  });

  it('discards a stale page that resolves after the letter changed (F1)', async () => {
    let resolveStale!: (v: Item[]) => void;
    const fetchItems = vi
      .fn()
      .mockImplementationOnce(
        () =>
          new Promise<Item[]>((resolve) => {
            resolveStale = resolve;
          }),
      )
      .mockResolvedValueOnce([{ id: 2 }]);
    const { api } = mountBrowse(() => Promise.resolve(['A', 'B']), fetchItems);
    await flushPromises(); // reload selected 'A'; its page is still in flight

    await api.selectLetter('B');
    resolveStale([{ id: 1 }]);
    await flushPromises();

    // The superseded letter-A response must not leak into the B bucket.
    expect(api.items.value).toEqual([{ id: 2 }]);
  });
});

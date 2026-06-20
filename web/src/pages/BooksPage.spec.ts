import { flushPromises, mount, RouterLinkStub } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { fetchBook, fetchBooks } from '@/api';
import BookCard from '@/components/BookCard.vue';
import BookDetailModal from '@/components/BookDetailModal.vue';
import { useLibrary } from '@/composables/useLibrary';
import { useSyncStatus } from '@/composables/useSyncStatus';
import BooksPage from '@/pages/BooksPage.vue';
import { makeBook } from '@/test/factories';
import type { Book } from '@/types';

const { push, route, toastError } = vi.hoisted(() => ({
  push: vi.fn(),
  route: { query: {} as Record<string, string>, params: {} as Record<string, string> },
  toastError: vi.fn(),
}));
vi.mock('vue-router', () => ({ useRoute: () => route, useRouter: () => ({ push }) }));
vi.mock('@/api', () => ({ fetchBooks: vi.fn(), fetchBook: vi.fn() }));
vi.mock('@/composables/useToast', () => ({
  useToast: () => ({
    error: toastError,
    success: vi.fn(),
    toasts: { value: [] },
    dismiss: vi.fn(),
  }),
}));

const opts = { global: { stubs: { RouterLink: RouterLinkStub } } };

// Capture the IntersectionObserver callback so tests can drive the infinite
// scroll deterministically (the global setup stub is inert and never fires).
let fireScroll: () => void;
function installCapturingObserver(): void {
  class CapturingObserver {
    root = null;
    rootMargin = '';
    scrollMargin = '';
    thresholds = [];
    observe = vi.fn();
    unobserve = vi.fn();
    disconnect = vi.fn();
    takeRecords = vi.fn(() => []);
    constructor(cb: IntersectionObserverCallback) {
      fireScroll = () => cb([{ isIntersecting: true } as IntersectionObserverEntry], this as never);
    }
  }
  vi.stubGlobal('IntersectionObserver', CapturingObserver);
}

describe('BooksPage', () => {
  beforeEach(() => {
    route.query = {};
    route.params = {};
    push.mockClear();
    vi.mocked(fetchBooks).mockReset();
    vi.mocked(fetchBooks).mockResolvedValue({ items: [], total: 0, page: 1, limit: 24 });
    vi.mocked(fetchBook).mockReset();
    vi.mocked(fetchBook).mockResolvedValue(makeBook());
    toastError.mockClear();
    localStorage.clear();
    useLibrary().setLibrary(null);
    // Reset the shared sync-status singletons so a run left over from another
    // test can't fire BooksPage's completion watcher.
    useSyncStatus().running.value = false;
    useSyncStatus().current.value = 0;
    installCapturingObserver();
  });

  it('loads the first page on mount and renders a card per book', async () => {
    vi.mocked(fetchBooks).mockResolvedValue({
      items: [makeBook({ id: 1 }), makeBook({ id: 2 })],
      total: 2,
      page: 1,
      limit: 24,
    });
    const wrapper = mount(BooksPage, opts);
    await flushPromises();

    expect(fetchBooks).toHaveBeenCalledWith(expect.objectContaining({ page: 1, limit: 24 }));
    expect(wrapper.findAllComponents(BookCard)).toHaveLength(2);
  });

  it('forwards route-query filters to the API', async () => {
    route.query = { author: 'Asimov', q: 'robots' };
    vi.mocked(fetchBooks).mockResolvedValue({ items: [], total: 0, page: 1, limit: 24 });
    mount(BooksPage, opts);
    await flushPromises();

    expect(fetchBooks).toHaveBeenCalledWith(
      expect.objectContaining({ author: 'Asimov', q: 'robots', page: 1, limit: 24 }),
    );
  });

  it('forwards format and language filters to the API', async () => {
    route.query = { format: 'epub', lang: 'ru' };
    vi.mocked(fetchBooks).mockResolvedValue({ items: [], total: 0, page: 1, limit: 24 });
    mount(BooksPage, opts);
    await flushPromises();
    expect(fetchBooks).toHaveBeenCalledWith(
      expect.objectContaining({ format: 'epub', lang: 'ru', page: 1, limit: 24 }),
    );
  });

  it('refetches scoped to the selected library and when the selection changes', async () => {
    vi.mocked(fetchBooks).mockResolvedValue({ items: [], total: 0, page: 1, limit: 24 });
    const { setLibrary } = useLibrary();
    setLibrary(5);
    mount(BooksPage, opts);
    await flushPromises();

    expect(fetchBooks).toHaveBeenCalledWith(expect.objectContaining({ library: 5 }));

    vi.mocked(fetchBooks).mockClear();
    setLibrary(8);
    await flushPromises();
    expect(fetchBooks).toHaveBeenCalledWith(expect.objectContaining({ library: 8 }));
  });

  // NOTE: this spec shares module-level singletons (useLibrary, useSyncStatus)
  // and never unmounts, so BooksPage instances from earlier tests stay alive and
  // also react to the shared `current` ref. These tests therefore assert on
  // whether a reload happened (and on their own wrapper's rendered grid), not on
  // absolute fetchBooks call counts, which the zombie instances would inflate.
  it('reloads the grid when a library sync completes, but not when it starts', async () => {
    vi.mocked(fetchBooks).mockResolvedValue({ items: [], total: 0, page: 1, limit: 24 });
    const wrapper = mount(BooksPage, opts);
    await flushPromises();

    const { running, current } = useSyncStatus();

    // A sync starts for library 5. Its books aren't indexed yet, so reloading
    // now would be wasted work — the grid must NOT refetch on start.
    vi.mocked(fetchBooks).mockClear();
    running.value = true;
    current.value = 5;
    await flushPromises();
    expect(fetchBooks).not.toHaveBeenCalled();

    // The sync finishes and the engine goes idle (current → 0). Now the freshly
    // indexed books exist, so the grid must refetch from page 1 and render them.
    vi.mocked(fetchBooks).mockClear();
    vi.mocked(fetchBooks).mockResolvedValue({
      items: [makeBook({ id: 1 }), makeBook({ id: 2 })],
      total: 2,
      page: 1,
      limit: 24,
    });
    running.value = false;
    current.value = 0;
    await flushPromises();

    expect(fetchBooks).toHaveBeenCalledWith(expect.objectContaining({ page: 1, limit: 24 }));
    expect(wrapper.findAllComponents(BookCard)).toHaveLength(2);
  });

  it('refreshes the grid as each library in a multi-library run finishes', async () => {
    vi.mocked(fetchBooks).mockResolvedValue({ items: [], total: 0, page: 1, limit: 24 });
    mount(BooksPage, opts);
    await flushPromises();

    const { running, current } = useSyncStatus();
    running.value = true;

    vi.mocked(fetchBooks).mockClear();
    current.value = 5; // first library starts — no reload
    await flushPromises();
    expect(fetchBooks).not.toHaveBeenCalled();

    vi.mocked(fetchBooks).mockClear();
    current.value = 8; // library 5 done, library 8 starts — reload for 5's books
    await flushPromises();
    expect(fetchBooks).toHaveBeenCalled();

    vi.mocked(fetchBooks).mockClear();
    running.value = false;
    current.value = 0; // library 8 done, engine idle — reload for 8's books
    await flushPromises();
    expect(fetchBooks).toHaveBeenCalled();
  });

  it('keeps the detail modal closed when the route has no book id', async () => {
    vi.mocked(fetchBooks).mockResolvedValue({ items: [], total: 0, page: 1, limit: 24 });
    const wrapper = mount(BooksPage, opts);
    await flushPromises();
    expect(wrapper.getComponent(BookDetailModal).props('id')).toBe(null);
  });

  it('opens the detail modal for the book id in the route', async () => {
    route.params = { id: '7' };
    vi.mocked(fetchBooks).mockResolvedValue({ items: [], total: 0, page: 1, limit: 24 });
    const wrapper = mount(BooksPage, opts);
    await flushPromises();
    expect(wrapper.getComponent(BookDetailModal).props('id')).toBe(7);
  });

  it('navigates home preserving filters when the modal closes', async () => {
    route.params = { id: '7' };
    route.query = { author: 'Asimov' };
    vi.mocked(fetchBooks).mockResolvedValue({ items: [], total: 0, page: 1, limit: 24 });
    const wrapper = mount(BooksPage, opts);
    await flushPromises();
    wrapper.getComponent(BookDetailModal).vm.$emit('close');
    expect(push).toHaveBeenCalledWith({ path: '/', query: { author: 'Asimov' } });
  });

  it('discards a stale response that resolves after a filter reset (F1)', async () => {
    // Keyed by filter args (not call order) because components mounted by
    // earlier tests share the libraryId singleton and also refetch on change.
    const staleResolvers: Array<
      (v: { items: Book[]; total: number; page: number; limit: number }) => void
    > = [];
    vi.mocked(fetchBooks).mockImplementation((f) =>
      f?.library === 7
        ? Promise.resolve({
            items: [makeBook({ id: 2, title: 'Fresh' })],
            total: 1,
            page: 1,
            limit: 24,
          })
        : new Promise((resolve) => {
            staleResolvers.push(resolve);
          }),
    );

    const wrapper = mount(BooksPage, opts);
    await flushPromises(); // the unfiltered request is in flight, unresolved

    // A library change reruns the filters watcher → loadBooks(true).
    useLibrary().setLibrary(7);
    await flushPromises();

    for (const resolve of staleResolvers) {
      resolve({ items: [makeBook({ id: 1, title: 'Stale' })], total: 1, page: 1, limit: 24 });
    }
    await flushPromises();

    const titles = wrapper.findAllComponents(BookCard).map((c) => (c.props('book') as Book).title);
    // The superseded response must not append stale rows.
    expect(titles).toEqual(['Fresh']);

    useLibrary().setLibrary(null); // don't leak the selection into later tests
    await flushPromises();
  });

  it('does not fetch page 2 while page 1 is still loading (no scroll race)', async () => {
    let resolvePage1!: (v: { items: Book[]; total: number; page: number; limit: number }) => void;
    vi.mocked(fetchBooks).mockReturnValueOnce(
      new Promise((resolve) => {
        resolvePage1 = resolve;
      }),
    );

    mount(BooksPage, opts);
    // Observer fires against the empty grid before page 1 resolves.
    fireScroll();
    await flushPromises();

    expect(fetchBooks).toHaveBeenCalledTimes(1);
    expect(fetchBooks).toHaveBeenCalledWith(expect.objectContaining({ page: 1 }));

    resolvePage1({ items: [makeBook({ id: 1 })], total: 100, page: 1, limit: 24 });
    await flushPromises();

    // Once page 1 reports more rows, scrolling fetches page 2.
    fireScroll();
    await flushPromises();
    expect(fetchBooks).toHaveBeenLastCalledWith(expect.objectContaining({ page: 2 }));
  });

  it('offers Recently added (default), Newest, and Top rated sort options', () => {
    const wrapper = mount(BooksPage, opts);
    const options = wrapper.findAll('select[aria-label="Sort books"] option');
    expect(options.map((o) => o.text())).toEqual(['Recently added', 'Newest', 'Top rated']);
    expect(options.map((o) => o.attributes('value'))).toEqual(['', 'source', 'rating']);
  });

  it('surfaces a toast and keeps scrolling alive when a page load fails', async () => {
    vi.mocked(fetchBooks).mockResolvedValueOnce({
      items: [makeBook({ id: 1 })],
      total: 100,
      page: 1,
      limit: 24,
    });
    mount(BooksPage, opts);
    await flushPromises();

    // Page 2 fails.
    vi.mocked(fetchBooks).mockRejectedValueOnce(new Error('boom'));
    fireScroll();
    await flushPromises();

    expect(toastError).toHaveBeenCalledWith(expect.stringContaining('boom'));

    // Scroll is not dead: the next intersection retries the failed page 2
    // (the optimistic increment was rolled back), not page 3.
    vi.mocked(fetchBooks).mockResolvedValueOnce({
      items: [makeBook({ id: 2 })],
      total: 100,
      page: 2,
      limit: 24,
    });
    fireScroll();
    await flushPromises();
    expect(fetchBooks).toHaveBeenLastCalledWith(expect.objectContaining({ page: 2 }));
  });
});

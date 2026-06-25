import { flushPromises, mount, RouterLinkStub } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { fetchBooks } from '@/api';
import { useLibrary } from '@/composables/useLibrary';
import { useSyncStatus } from '@/composables/useSyncStatus';
import BooksPage from '@/pages/BooksPage.vue';
import { makeBook } from '@/test/factories';

vi.mock('@/api', () => ({ fetchBooks: vi.fn(), fetchBook: vi.fn() }));
vi.mock('vue-router', () => ({
  useRoute: () => ({ query: {}, params: {} }),
  useRouter: () => ({ push: vi.fn() }),
}));
vi.mock('@/composables/useToast', () => ({
  useToast: () => ({
    error: vi.fn(),
    success: vi.fn(),
    toasts: { value: [] },
    dismiss: vi.fn(),
  }),
}));

const opts = { global: { stubs: { RouterLink: RouterLinkStub } } };

// A controllable IntersectionObserver stub that records the callback and lets
// tests fire it manually — same pattern as useInfiniteScroll.spec.ts.
type IOEntry = { isIntersecting: boolean };
let storedCb: ((entries: IOEntry[]) => void) | null = null;

class ControllableIO {
  root = null;
  rootMargin = '';
  scrollMargin = '';
  thresholds = [];
  observe = vi.fn();
  unobserve = vi.fn();
  disconnect = vi.fn();
  takeRecords = vi.fn(() => []);
  constructor(cb: (entries: IOEntry[]) => void) {
    storedCb = cb;
  }
  static fire(isIntersecting = true): void {
    storedCb?.([{ isIntersecting }]);
  }
}

describe('BooksPage infinite scroll — error handling', () => {
  beforeEach(() => {
    storedCb = null;
    vi.mocked(fetchBooks).mockReset();
    useLibrary().setLibrary(null);
    useSyncStatus().running.value = false;
    useSyncStatus().current.value = 0;
    vi.stubGlobal('IntersectionObserver', ControllableIO);
  });

  it('does not storm requests when a page load keeps failing', async () => {
    // First call (initial mount) succeeds with a short result so hasMore=true.
    // All subsequent calls (loadMore attempts) reject.
    vi.mocked(fetchBooks)
      .mockResolvedValueOnce({ items: [makeBook({ id: 1 })], total: 100, page: 1, limit: 24 })
      .mockRejectedValue(new Error('server error'));

    mount(BooksPage, opts);
    await flushPromises(); // initial load completes; hasMore=true

    // Fire the observer twice — in the broken case loadMore resolves on failure,
    // the observer re-arms and each re-arm fires the callback again, creating a
    // storm of fetchBooks calls. With the fix, loadMore rejects, re-arm is
    // skipped, and only at most one additional call is made per manual fire.
    ControllableIO.fire();
    await flushPromises();

    ControllableIO.fire();
    await flushPromises();

    // Initial mount (1) + at most one failed loadMore per manual fire (2 fires) = ≤ 3.
    // A storm would produce far more. We cap at 3 to allow for the two manual fires
    // while still catching any re-arm loop.
    expect(vi.mocked(fetchBooks).mock.calls.length).toBeLessThanOrEqual(3);
  });
});

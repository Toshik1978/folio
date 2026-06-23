import { beforeEach, describe, expect, it, vi } from 'vitest';
import { ref } from 'vue';

import { fetchFacets } from '@/api';

import { useFacetValues } from './useFacetValues';

vi.mock('@/api', () => ({ fetchFacets: vi.fn() }));

// Mutable libraryId so tests can simulate a library switch mid-load.
const libraryId = ref<number | null>(null);
vi.mock('./useLibrary', () => ({ useLibrary: () => ({ libraryId }) }));
const toastError = vi.fn();
vi.mock('./useToast', () => ({
  useToast: () => ({
    error: toastError,
    success: vi.fn(),
    toasts: { value: [] },
    dismiss: vi.fn(),
  }),
}));

const facets = {
  formats: ['epub', 'fb2'],
  languages: ['en', 'ru'],
};

describe('useFacetValues', () => {
  beforeEach(() => {
    vi.mocked(fetchFacets).mockReset();
    toastError.mockClear();
    libraryId.value = null;
  });

  it('loads format/language values', async () => {
    vi.mocked(fetchFacets).mockResolvedValue(facets);
    const { formats, languages, load } = useFacetValues();
    await load();
    expect(formats.value).toEqual(['epub', 'fb2']);
    expect(languages.value).toEqual(['en', 'ru']);
  });

  it('does not refetch for the same library', async () => {
    vi.mocked(fetchFacets).mockResolvedValue(facets);
    const { load } = useFacetValues();
    await load();
    await load();
    expect(fetchFacets).toHaveBeenCalledTimes(1);
  });

  it('toasts on failure and leaves values empty', async () => {
    vi.mocked(fetchFacets).mockRejectedValue(new Error('boom'));
    const { formats, load } = useFacetValues();
    await load();
    expect(toastError).toHaveBeenCalledWith(expect.stringContaining('boom'));
    expect(formats.value).toEqual([]);
  });

  it('ignores out-of-order load: later fetch for lib A resolves after lib B load', async () => {
    // Library A facets that will arrive LATE.
    const facetsA = { formats: ['mobi'], languages: ['de'] };
    // Library B facets that will arrive FIRST (but for a newer load).
    const facetsB = { formats: ['epub', 'fb2'], languages: ['en', 'ru'] };

    let resolveA!: (v: typeof facetsA) => void;
    const promiseA = new Promise<typeof facetsA>((res) => {
      resolveA = res;
    });

    // fetchFacets: first call (lib A) returns a promise we control; second call
    // (lib B) resolves immediately.
    vi.mocked(fetchFacets)
      .mockImplementationOnce(() => promiseA)
      .mockResolvedValueOnce(facetsB);

    libraryId.value = 1; // lib A
    const { formats, languages, load } = useFacetValues();

    // Start load for lib A — it is suspended at the await.
    const loadAPromise = load();

    // Switch to lib B and complete its load before A resolves.
    libraryId.value = 2;
    await load(); // lib B resolves immediately; B's facets now committed.

    // Now let lib A's stale response arrive.
    resolveA(facetsA);
    await loadAPromise;

    // B's facets must survive; A's stale response must be discarded.
    expect(formats.value).toEqual(facetsB.formats);
    expect(languages.value).toEqual(facetsB.languages);
  });
});

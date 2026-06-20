import { beforeEach, describe, expect, it, vi } from 'vitest';

import { fetchFacets } from '@/api';

import { useFacetValues } from './useFacetValues';

vi.mock('@/api', () => ({ fetchFacets: vi.fn() }));
vi.mock('./useLibrary', () => ({ useLibrary: () => ({ libraryId: { value: null } }) }));
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
});

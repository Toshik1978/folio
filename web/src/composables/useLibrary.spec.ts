import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('@/api', () => ({
  fetchLibraries: vi.fn(),
}));

import { fetchLibraries } from '@/api';

import { useLibrary } from './useLibrary';

describe('useLibrary', () => {
  beforeEach(() => {
    localStorage.clear();
    const { setLibrary } = useLibrary();
    setLibrary(null);
    vi.mocked(fetchLibraries).mockReset();
  });

  it('defaults to null (All) and persists a selection', () => {
    const { libraryId, setLibrary } = useLibrary();
    expect(libraryId.value).toBeNull();

    setLibrary(7);
    expect(libraryId.value).toBe(7);
    expect(localStorage.getItem('folio-library')).toBe('7');

    setLibrary(null);
    expect(localStorage.getItem('folio-library')).toBeNull();
  });

  it('resets a stale selection when the library no longer exists', async () => {
    const { libraryId, setLibrary, refreshLibraries } = useLibrary();
    setLibrary(99);
    vi.mocked(fetchLibraries).mockResolvedValue([makeLib(1), makeLib(2)]);

    await refreshLibraries();

    expect(libraryId.value).toBeNull();
  });

  it('keeps a valid selection after refresh', async () => {
    const { libraryId, setLibrary, refreshLibraries } = useLibrary();
    setLibrary(2);
    vi.mocked(fetchLibraries).mockResolvedValue([makeLib(1), makeLib(2)]);

    await refreshLibraries();

    expect(libraryId.value).toBe(2);
  });
});

function makeLib(id: number) {
  return {
    id,
    name: `Lib ${id}`,
    type: 'folder' as const,
    path: `/lib/${id}`,
    sync_interval_seconds: 3600,
    status: 'active' as const,
    purge_at: null,
    last_sync_at: null,
    last_sync_error: null,
    book_count: 0,
  };
}

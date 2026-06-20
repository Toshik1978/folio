import { flushPromises } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import {
  createLibrary,
  deleteLibrary as apiDeleteLibrary,
  fetchLibraries,
  reindexLibrary as apiReindexLibrary,
  syncLibrary as apiSyncLibrary,
} from '@/api';
import { useConfirm } from '@/composables/useConfirm';
import { useLibraryActions } from '@/composables/useLibraryActions';
import { useToast } from '@/composables/useToast';
import { makeLibrary } from '@/test/factories';

vi.mock('@/api', () => ({
  syncLibrary: vi.fn().mockResolvedValue(undefined),
  reindexLibrary: vi.fn().mockResolvedValue(undefined),
  deleteLibrary: vi.fn().mockResolvedValue(undefined),
  reactivateLibrary: vi.fn().mockResolvedValue(undefined),
  forcePurgeLibrary: vi.fn().mockResolvedValue(undefined),
  triggerReindexAll: vi.fn().mockResolvedValue(undefined),
  createLibrary: vi.fn().mockResolvedValue({
    id: 1,
    name: 'My Library',
    type: 'calibre',
    path: '/library/metadata.db',
    sync_interval_seconds: 3600,
    status: 'active',
    purge_at: null,
    last_sync_at: null,
    last_sync_error: null,
    book_count: 0,
  }),
  updateLibrary: vi.fn().mockResolvedValue({
    id: 1,
    name: 'My Library',
    type: 'calibre',
    path: '/library/metadata.db',
    sync_interval_seconds: 3600,
    status: 'active',
    purge_at: null,
    last_sync_at: null,
    last_sync_error: null,
    book_count: 0,
  }),
  fetchLibraries: vi.fn().mockResolvedValue([]),
  fetchSyncStatus: vi.fn().mockResolvedValue({ running: false, current: 0, queued: [] }),
}));

describe('useLibraryActions', () => {
  beforeEach(() => vi.clearAllMocks());
  afterEach(() => vi.clearAllMocks());

  it('triggers a sync without confirming', async () => {
    await useLibraryActions().syncLibrary(5);
    expect(apiSyncLibrary).toHaveBeenCalledWith(5);
  });

  it('re-indexes a library only after the confirm resolves true', async () => {
    const actions = useLibraryActions();
    const p = actions.reindexLibrary(makeLibrary({ id: 7 }));
    await flushPromises();
    useConfirm().respond(true);
    await p;
    expect(apiReindexLibrary).toHaveBeenCalledWith(7);
  });

  it('does not delete when the confirm is cancelled', async () => {
    const actions = useLibraryActions();
    const p = actions.deleteLibrary(9);
    await flushPromises();
    useConfirm().respond(false);
    await p;
    expect(apiDeleteLibrary).not.toHaveBeenCalled();
  });

  it('createLibrary returns true on success and refreshes the list', async () => {
    const ok = await useLibraryActions().createLibrary({
      name: 'L',
      type: 'calibre',
      path: '/p',
      sync_interval_seconds: 3600,
    });
    expect(ok).toBe(true);
    expect(createLibrary).toHaveBeenCalledOnce();
    expect(fetchLibraries).toHaveBeenCalled();
  });

  it('createLibrary returns false and toasts on failure', async () => {
    vi.mocked(createLibrary).mockRejectedValueOnce(new Error('boom'));
    const ok = await useLibraryActions().createLibrary({
      name: 'L',
      type: 'calibre',
      path: '/p',
      sync_interval_seconds: 3600,
    });
    expect(ok).toBe(false);
    expect(useToast().toasts.value.some((t) => t.message === 'Failed to add library: boom')).toBe(
      true,
    );
  });
});

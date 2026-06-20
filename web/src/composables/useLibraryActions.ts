import {
  createLibrary as apiCreateLibrary,
  deleteLibrary as apiDeleteLibrary,
  forcePurgeLibrary as apiForcePurgeLibrary,
  reactivateLibrary as apiReactivateLibrary,
  reindexLibrary as apiReindexLibrary,
  syncLibrary as apiSyncLibrary,
  triggerReindexAll,
  updateLibrary as apiUpdateLibrary,
} from '@/api';
import { useConfirm } from '@/composables/useConfirm';
import { useLibrary } from '@/composables/useLibrary';
import { useSyncStatus } from '@/composables/useSyncStatus';
import { useToast } from '@/composables/useToast';
import type { Library, NewLibrary } from '@/types';

// useLibraryActions centralizes every library mutation: the API call plus the
// confirm, toast, and list/sync refresh that wrap it. The page wires component
// events to these so no component performs its own mutations.
export function useLibraryActions() {
  const { refreshLibraries } = useLibrary();
  const { refresh: refreshSyncStatus } = useSyncStatus();
  const { confirm } = useConfirm();
  const toast = useToast();

  async function syncLibrary(id: number): Promise<void> {
    try {
      await apiSyncLibrary(id);
      await refreshSyncStatus();
    } catch (err) {
      toast.error(`Failed to trigger sync: ${(err as Error).message}`);
    }
  }

  async function reindexLibrary(library: Library): Promise<void> {
    const ok = await confirm({
      title: `Re-index "${library.name}"?`,
      body: 'This library is re-read from scratch, bypassing change detection. This may take a while.',
      confirmLabel: 'Re-index',
    });
    if (!ok) return;
    try {
      await apiReindexLibrary(library.id);
      await refreshSyncStatus();
      toast.success('Re-index started');
    } catch (err) {
      toast.error(`Failed to start re-index: ${(err as Error).message}`);
    }
  }

  async function deleteLibrary(id: number): Promise<void> {
    const ok = await confirm({
      title: 'Delete library?',
      body: 'Its books are purged after a 7-day grace period.',
      danger: true,
      confirmLabel: 'Delete',
    });
    if (!ok) return;
    try {
      await apiDeleteLibrary(id);
      await refreshLibraries();
    } catch (err) {
      toast.error(`Failed to delete library: ${(err as Error).message}`);
    }
  }

  async function reactivateLibrary(id: number): Promise<void> {
    try {
      await apiReactivateLibrary(id);
      await refreshLibraries();
    } catch (err) {
      toast.error(`Failed to reactivate library: ${(err as Error).message}`);
    }
  }

  async function forcePurgeLibrary(id: number): Promise<void> {
    const ok = await confirm({
      title: 'Purge library now?',
      body: 'Its books and covers are deleted immediately.',
      danger: true,
      confirmLabel: 'Purge',
    });
    if (!ok) return;
    try {
      await apiForcePurgeLibrary(id);
      toast.success('Purging library…');
      await refreshLibraries();
    } catch (err) {
      toast.error(`Failed to purge library: ${(err as Error).message}`);
    }
  }

  async function reindexAll(): Promise<void> {
    const ok = await confirm({
      title: 'Re-index all libraries?',
      body: 'Every library is re-read from scratch, bypassing change detection. This may take a while.',
      confirmLabel: 'Re-index',
    });
    if (!ok) return;
    try {
      await triggerReindexAll();
      await refreshSyncStatus();
      toast.success('Re-index started');
    } catch (err) {
      toast.error(`Failed to start re-index: ${(err as Error).message}`);
    }
  }

  async function createLibrary(payload: NewLibrary): Promise<boolean> {
    try {
      await apiCreateLibrary(payload);
      await refreshLibraries();
      return true;
    } catch (err) {
      toast.error(`Failed to add library: ${(err as Error).message}`);
      return false;
    }
  }

  async function updateLibrary(id: number, payload: NewLibrary): Promise<boolean> {
    try {
      await apiUpdateLibrary(id, payload);
      await refreshLibraries();
      return true;
    } catch (err) {
      toast.error(`Failed to update library: ${(err as Error).message}`);
      return false;
    }
  }

  return {
    syncLibrary,
    reindexLibrary,
    deleteLibrary,
    reactivateLibrary,
    forcePurgeLibrary,
    reindexAll,
    createLibrary,
    updateLibrary,
  };
}

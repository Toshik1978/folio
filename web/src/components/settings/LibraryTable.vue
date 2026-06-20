<template>
  <div>
    <div v-if="libraries.length === 0" data-testid="library-empty" class="alert mb-8" role="status">
      <span aria-hidden="true">📚</span>
      <span>No libraries yet — add one below to start indexing your library.</span>
    </div>
    <div class="border-base-300 bg-base-200 rounded-box mb-8 overflow-x-auto border">
      <table class="table table-sm">
        <thead>
          <tr class="text-base-content/50">
            <th class="w-[1%] whitespace-nowrap">Status</th>
            <th class="w-[1%] whitespace-nowrap">Type</th>
            <th class="max-w-0">Library</th>
            <th class="w-[1%] whitespace-nowrap">Books</th>
            <th class="w-[1%] whitespace-nowrap">Last sync</th>
            <th class="w-[1%]"></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="library in libraries" :key="library.id" data-testid="library-card">
            <td class="w-[1%] whitespace-nowrap">
              <span
                class="badge badge-sm whitespace-nowrap"
                :class="statusClass(displayStatus(library))"
              >
                {{ statusLabel(displayStatus(library)) }}
              </span>
            </td>
            <td class="w-[1%] whitespace-nowrap">
              <span class="badge badge-ghost badge-sm uppercase">{{ library.type }}</span>
            </td>
            <td class="max-w-0">
              <div class="truncate font-semibold capitalize" :title="library.name">
                {{ library.name }}
              </div>
              <div class="text-base-content/60 truncate font-mono text-xs" :title="library.path">
                {{ library.path }}
              </div>
              <template v-if="isSyncing(library.id) && currentProgress">
                <div class="flex items-center gap-2 mt-1">
                  <progress
                    class="progress progress-primary w-32"
                    :value="currentProgress.total ? currentProgress.processed : undefined"
                    :max="currentProgress.total ?? undefined"
                  />
                  <span class="text-xs opacity-70 whitespace-nowrap">{{
                    formatProgress(currentProgress.processed, currentProgress.total)
                  }}</span>
                </div>
              </template>
            </td>
            <td class="text-base-content/70 w-[1%] text-xs whitespace-nowrap">
              {{ library.book_count }}
            </td>
            <td class="text-base-content/70 w-[1%] text-xs whitespace-nowrap">
              <span
                v-if="library.status === 'pending_purge' && library.purge_at"
                class="text-warning"
              >
                Purges {{ formatTime(library.purge_at) }}
              </span>
              <span v-else-if="library.last_sync_at">{{ formatTime(library.last_sync_at) }}</span>
              <span v-else>—</span>
            </td>
            <td class="w-[1%]">
              <div data-testid="library-actions" class="flex justify-end gap-2 whitespace-nowrap">
                <template v-if="library.status === 'pending_purge'">
                  <button
                    type="button"
                    data-testid="library-action"
                    class="btn btn-xs"
                    @click="$emit('reactivate', library.id)"
                  >
                    Reactivate
                  </button>
                  <button
                    type="button"
                    data-testid="library-action"
                    data-danger
                    class="btn btn-xs btn-error"
                    @click="$emit('purge', library.id)"
                  >
                    Purge Now
                  </button>
                </template>
                <template v-else>
                  <button
                    type="button"
                    data-testid="library-action"
                    class="btn btn-xs"
                    :disabled="rowBusy(library)"
                    @click="$emit('sync', library.id)"
                  >
                    Sync Now
                  </button>
                  <button
                    type="button"
                    data-testid="library-action"
                    class="btn btn-xs"
                    :disabled="rowBusy(library)"
                    @click="$emit('reindex', library)"
                  >
                    Re-index
                  </button>
                  <button
                    type="button"
                    data-testid="library-action"
                    class="btn btn-xs"
                    :disabled="rowBusy(library)"
                    @click="$emit('edit', library)"
                  >
                    Edit
                  </button>
                  <button
                    type="button"
                    data-testid="library-action"
                    data-danger
                    class="btn btn-xs btn-error"
                    :disabled="rowBusy(library)"
                    @click="$emit('delete', library.id)"
                  >
                    Delete
                  </button>
                </template>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useSyncStatus } from '@/composables/useSyncStatus';
import type { Library } from '@/types';
import { formatProgress, formatTime } from '@/utils/format';
import { statusClass, statusLabel } from '@/utils/libraryStatus';

defineProps<{ libraries: Library[] }>();

defineEmits<{
  sync: [id: number];
  reindex: [library: Library];
  edit: [library: Library];
  delete: [id: number];
  reactivate: [id: number];
  purge: [id: number];
}>();

const { isSyncing, isQueued, currentProgress } = useSyncStatus();

// A row counts as syncing if the engine reports it current OR the persisted status
// is already "syncing" (the effectiveStatus overlay from the API).
function rowSyncing(library: Library): boolean {
  return isSyncing(library.id) || library.status === 'syncing';
}

function rowBusy(library: Library): boolean {
  return rowSyncing(library) || isQueued(library.id);
}

function displayStatus(library: Library): string {
  if (rowSyncing(library)) return 'syncing';
  if (isQueued(library.id)) return 'queued';
  return library.status;
}
</script>

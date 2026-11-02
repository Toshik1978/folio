<template>
  <div
    v-if="running"
    data-testid="sync-banner"
    class="alert alert-info rounded-none py-2"
    role="status"
  >
    <span class="loading loading-spinner loading-sm" aria-hidden="true" />
    <span>
      <template v-if="currentKind === 'deep_index'">Deep indexing…</template>
      <template v-else>Syncing "{{ currentName }}"</template
      ><template v-if="queued.length"> — {{ queued.length }} queued</template>
    </span>
    <span v-if="currentProgress">{{
      formatProgress(currentProgress.processed, currentProgress.total)
    }}</span>
  </div>
</template>

<script setup lang="ts">
import { useSyncStatus } from '@/composables/useSyncStatus';
import { formatProgress } from '@/utils/format';

const { running, currentKind, currentName, queued, currentProgress } = useSyncStatus();
</script>

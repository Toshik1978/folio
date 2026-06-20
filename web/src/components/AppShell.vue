<template>
  <div class="drawer lg:drawer-open h-screen overflow-hidden">
    <input id="app-drawer" type="checkbox" class="drawer-toggle" />
    <div class="drawer-content flex h-full flex-col overflow-hidden">
      <AppTopBar />
      <SyncBanner />
      <div class="min-h-0 flex-1 overflow-y-auto">
        <main class="p-6">
          <router-view />
        </main>
      </div>
    </div>
    <div class="drawer-side z-[100]">
      <label for="app-drawer" aria-label="close sidebar" class="drawer-overlay"></label>
      <AppSidebar />
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, onUnmounted } from 'vue';

import { useLibrary } from '@/composables/useLibrary';
import { useSyncStatus } from '@/composables/useSyncStatus';
import { useToast } from '@/composables/useToast';

import AppSidebar from './AppSidebar.vue';
import AppTopBar from './AppTopBar.vue';
import SyncBanner from './SyncBanner.vue';

// Load the library list once at app start so the header selector is populated
// and any stale stored selection is reconciled. Surface a startup failure as a
// toast instead of an unhandled rejection and a silently empty selector.
const { refreshLibraries } = useLibrary();
const { connect, disconnect } = useSyncStatus();
const toast = useToast();
onMounted(() => {
  refreshLibraries().catch((err: unknown) => {
    toast.error(`Failed to load libraries: ${(err as Error).message}`);
  });
  // App-wide SSE stream so the banner and per-row status reflect any sync
  // (manual or scheduled) regardless of which page is open.
  connect();
});
onUnmounted(() => {
  disconnect();
});
</script>

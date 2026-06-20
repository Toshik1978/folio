<template>
  <div class="max-w-5xl">
    <h1 class="mb-4 text-[22px] font-bold">Settings</h1>

    <div role="tablist" class="tabs tabs-border mb-6">
      <button
        type="button"
        role="tab"
        class="tab"
        :class="{ 'tab-active': tab === 'opds' }"
        @click="tab = 'opds'"
      >
        OPDS
      </button>
      <button
        type="button"
        role="tab"
        class="tab"
        :class="{ 'tab-active': tab === 'libraries' }"
        @click="tab = 'libraries'"
      >
        Libraries
      </button>
    </div>

    <div v-if="tab === 'opds'">
      <OpdsSettingsForm />
    </div>

    <div v-if="tab === 'libraries'">
      <LibraryStats>
        <template #action>
          <button
            v-if="libraries.length > 0"
            type="button"
            data-testid="reindex-all"
            class="btn btn-sm"
            :disabled="syncRunning"
            @click="actions.reindexAll"
          >
            <i class="pi pi-refresh" />
            Re-index All
          </button>
        </template>
      </LibraryStats>

      <LibraryTable
        :libraries="libraries"
        @sync="actions.syncLibrary"
        @reindex="actions.reindexLibrary"
        @edit="onEdit"
        @delete="actions.deleteLibrary"
        @reactivate="actions.reactivateLibrary"
        @purge="actions.forcePurgeLibrary"
      />

      <LibraryForm ref="formRef" :editing="editing" @submit="onSubmit" @cancel="onCancel" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';

import LibraryStats from '@/components/LibraryStats.vue';
import LibraryForm from '@/components/settings/LibraryForm.vue';
import LibraryTable from '@/components/settings/LibraryTable.vue';
import OpdsSettingsForm from '@/components/settings/OpdsSettingsForm.vue';
import { useLibrary } from '@/composables/useLibrary';
import { useLibraryActions } from '@/composables/useLibraryActions';
import { useSyncStatus } from '@/composables/useSyncStatus';
import { useToast } from '@/composables/useToast';
import type { Library, NewLibrary } from '@/types';

const route = useRoute();
const router = useRouter();
const validTabs = ['opds', 'libraries'] as const;
type Tab = (typeof validTabs)[number];
const initialTab: Tab = validTabs.includes(route.query.tab as Tab)
  ? (route.query.tab as Tab)
  : 'opds';
const tab = ref<Tab>(initialTab);

const toast = useToast();
const { libraries, refreshLibraries: loadLibraries } = useLibrary();
const { running: syncRunning } = useSyncStatus();
const actions = useLibraryActions();

const editing = ref<Library | null>(null);
const formRef = ref<InstanceType<typeof LibraryForm> | null>(null);

watch(tab, (t) => {
  void router.replace({ query: { ...route.query, tab: t } });
  if (t === 'libraries') void loadLibraries().catch(() => undefined);
});

function onEdit(library: Library): void {
  editing.value = library;
}

function onCancel(): void {
  editing.value = null;
}

async function onSubmit(payload: NewLibrary): Promise<void> {
  if (editing.value) {
    if (await actions.updateLibrary(editing.value.id, payload)) editing.value = null;
  } else if (await actions.createLibrary(payload)) {
    formRef.value?.reset();
  }
}

onMounted(() => {
  void loadLibraries().catch((err: unknown) => {
    toast.error(`Failed to load libraries: ${(err as Error).message}`);
  });
});
</script>

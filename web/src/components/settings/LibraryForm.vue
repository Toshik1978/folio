<template>
  <div data-testid="add-library" class="card bg-base-200 gap-4 p-5">
    <h3 data-testid="library-form-title" class="text-base font-semibold">
      {{ editing !== null ? 'Edit Library' : 'Add Library' }}
    </h3>
    <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
      <div>
        <label for="library-name" class="mb-1 block text-sm font-semibold">Name</label>
        <input
          id="library-name"
          v-model="form.name"
          type="text"
          class="input w-full"
          placeholder="My Library"
        />
      </div>
      <div>
        <label
          :for="editing !== null ? undefined : 'library-type'"
          class="mb-1 block text-sm font-semibold"
          >Type</label
        >
        <div
          v-if="editing !== null"
          class="input flex w-full items-center justify-between"
          aria-readonly="true"
        >
          <span>{{ typeLabel(form.type) }}</span>
          <i class="pi pi-lock opacity-70" aria-hidden="true" />
        </div>
        <select v-else id="library-type" v-model="form.type" class="select w-full">
          <option value="calibre">Calibre</option>
          <option value="inpx">INPX</option>
          <option value="folder">Folder</option>
        </select>
        <span v-if="editing !== null" class="text-base-content/60 mt-1 block text-xs">
          Type can't be changed after a library is created.
        </span>
      </div>
      <div class="sm:col-span-2">
        <label for="library-path" class="mb-1 block text-sm font-semibold">Path</label>
        <input
          id="library-path"
          v-model="form.path"
          type="text"
          class="input w-full"
          placeholder="/library/path"
        />
      </div>
      <div>
        <label for="library-interval" class="mb-1 block text-sm font-semibold"
          >Sync Interval (seconds)</label
        >
        <input
          id="library-interval"
          v-model.number="form.interval"
          type="number"
          class="input w-full"
          placeholder="3600"
        />
      </div>
    </div>
    <div class="flex gap-3">
      <button type="button" data-testid="library-save" class="btn btn-primary" @click="onSave">
        {{ editing !== null ? 'Save Changes' : 'Add Library' }}
      </button>
      <button v-if="editing !== null" type="button" class="btn btn-ghost" @click="$emit('cancel')">
        Cancel
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { reactive, watch } from 'vue';

import { useToast } from '@/composables/useToast';
import type { Library, NewLibrary } from '@/types';
import { typeLabel } from '@/utils/libraryStatus';

const props = defineProps<{ editing: Library | null }>();
const emit = defineEmits<{ submit: [payload: NewLibrary]; cancel: [] }>();

const toast = useToast();

const form = reactive({
  name: '',
  type: 'calibre' as Library['type'],
  path: '',
  interval: 3600,
});

// reset returns the form to add-mode defaults. Exposed so the page can clear it
// after a successful create.
function reset(): void {
  form.name = '';
  form.type = 'calibre';
  form.path = '';
  form.interval = 3600;
}

// Seed (or clear) the fields whenever the edit target changes, and scroll the form
// into view when entering edit mode (replacing the page's old editLibrary scroll).
watch(
  () => props.editing,
  (library) => {
    if (library) {
      form.name = library.name;
      form.type = library.type;
      form.path = library.path;
      form.interval = library.sync_interval_seconds;
      const el = document.querySelector('[data-testid="add-library"]');
      if (el) el.scrollIntoView({ behavior: 'smooth' });
    } else {
      reset();
    }
  },
  { immediate: true },
);

function onSave(): void {
  if (!form.name.trim()) {
    toast.error('Name is required');
    return;
  }
  if (!form.path.trim()) {
    toast.error('Path is required');
    return;
  }
  emit('submit', {
    name: form.name,
    type: form.type,
    path: form.path,
    sync_interval_seconds: form.interval,
  });
}

defineExpose({ reset });
</script>

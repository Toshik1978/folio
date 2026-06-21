<template>
  <Teleport to="body">
    <dialog class="modal" :class="{ 'modal-open': open }">
      <div ref="modalBox" class="modal-box max-w-2xl">
        <button
          type="button"
          data-testid="edit-close"
          class="btn btn-sm btn-circle btn-ghost absolute right-3 top-3"
          @click="emit('close')"
        >
          ✕
        </button>
        <h3 class="mb-4 text-lg font-semibold">Edit book</h3>

        <form class="flex flex-col gap-3" @submit.prevent="save">
          <label class="form-control">
            <span class="label-text mb-1">Title</span>
            <input
              v-model="form.title"
              data-testid="edit-title"
              type="text"
              class="input input-bordered input-sm"
            />
          </label>

          <label class="form-control">
            <span class="label-text mb-1">Authors (one per line)</span>
            <textarea
              v-model="authorsText"
              data-testid="edit-authors"
              class="textarea textarea-bordered textarea-sm"
              rows="2"
            />
          </label>

          <label class="form-control">
            <span class="label-text mb-1">Genres</span>
            <input
              v-model="genresText"
              data-testid="edit-genres"
              list="genre-options"
              class="input input-bordered input-sm"
              placeholder="comma-separated"
            />
            <datalist id="genre-options">
              <option v-for="g in genreOptions" :key="g" :value="g" />
            </datalist>
          </label>

          <div class="grid grid-cols-2 gap-3">
            <label class="form-control">
              <span class="label-text mb-1">Series</span>
              <input v-model="form.series" type="text" class="input input-bordered input-sm" />
            </label>
            <label class="form-control">
              <span class="label-text mb-1">Year</span>
              <input
                v-model.number="form.year"
                type="number"
                class="input input-bordered input-sm"
              />
            </label>
            <label class="form-control">
              <span class="label-text mb-1">Publisher</span>
              <input v-model="form.publisher" type="text" class="input input-bordered input-sm" />
            </label>
            <label class="form-control">
              <span class="label-text mb-1">Series #</span>
              <input
                v-model.number="form.series_number"
                type="number"
                step="0.1"
                class="input input-bordered input-sm"
              />
            </label>
          </div>

          <label class="form-control">
            <span class="label-text mb-1">Annotation</span>
            <textarea
              v-model="form.annotation"
              class="textarea textarea-bordered textarea-sm"
              rows="4"
            />
          </label>

          <div class="mt-2 flex justify-end gap-2">
            <button type="button" class="btn btn-ghost btn-sm" @click="emit('close')">
              Cancel
            </button>
            <button
              type="submit"
              data-testid="edit-save"
              class="btn btn-primary btn-sm"
              :disabled="saving"
              @click.prevent="save"
            >
              Save
            </button>
          </div>
        </form>
      </div>
      <div class="modal-backdrop" @click="emit('close')"></div>
    </dialog>
  </Teleport>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue';

import { fetchGenres, updateBookMetadata } from '@/api';
import { useModalFocus } from '@/composables/useModalFocus';
import { useToast } from '@/composables/useToast';
import type { Book, BookMetadataUpdate } from '@/types';

const props = defineProps<{ book: Book; open: boolean }>();
const emit = defineEmits<{ close: []; applied: [book: Book] }>();
const toast = useToast();

const modalBox = ref<HTMLElement | null>(null);
useModalFocus(
  computed(() => props.open),
  modalBox,
  () => emit('close'),
);

const form = reactive<BookMetadataUpdate>({ title: '', authors: [], genres: [] });
const authorsText = ref('');
const genresText = ref('');
const genreOptions = ref<string[]>([]);
const saving = ref(false);

// Reset the form from the current book each time the modal opens.
watch(
  () => props.open,
  async (open) => {
    if (!open) return;
    form.title = props.book.title;
    form.series = props.book.series ?? '';
    form.series_number = props.book.series_index ?? undefined;
    form.year = props.book.year ?? undefined;
    form.publisher = props.book.publisher ?? '';
    form.annotation = props.book.annotation ?? '';
    authorsText.value = props.book.authors.map((a) => a.name).join('\n');
    genresText.value = props.book.tags.join(', ');
    if (genreOptions.value.length === 0) {
      try {
        genreOptions.value = await fetchGenres();
      } catch {
        // a missing taxonomy only disables autocomplete, not editing
      }
    }
  },
);

async function save(): Promise<void> {
  if (!form.title.trim()) {
    toast.error('Title is required');
    return;
  }
  const payload: BookMetadataUpdate = {
    ...form,
    title: form.title.trim(),
    authors: authorsText.value
      .split('\n')
      .map((a) => a.trim())
      .filter(Boolean),
    genres: genresText.value
      .split(',')
      .map((g) => g.trim())
      .filter(Boolean),
  };
  saving.value = true;
  try {
    const updated = await updateBookMetadata(props.book.id, payload);
    toast.success('Book updated');
    emit('applied', updated);
    emit('close');
  } catch (err) {
    toast.error(`Update failed: ${(err as Error).message}`);
  } finally {
    saving.value = false;
  }
}
</script>

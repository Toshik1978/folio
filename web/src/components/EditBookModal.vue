<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue';

import { fetchGenres, updateBookMetadata } from '@/api';
import IdentifierEditor from '@/components/IdentifierEditor.vue';
import TagSelect from '@/components/TagSelect.vue';
import { useModalFocus } from '@/composables/useModalFocus';
import { useToast } from '@/composables/useToast';
import type { Book, BookMetadataUpdate, IdentifierInput } from '@/types';
import { editLanguageOptions } from '@/utils/language';

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
const tags = ref<string[]>([]);
const identifiers = ref<IdentifierInput[]>([]);
const genreOptions = ref<string[]>([]);
const saving = ref(false);

// Language options are derived from the book's current code so an out-of-list
// value stays selectable (see editLanguageOptions).
const languageOptions = computed(() => editLanguageOptions(form.language));

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
    form.language = props.book.language ?? 'und';
    form.annotation = props.book.annotation ?? '';
    authorsText.value = props.book.authors.map((a) => a.name).join('\n');
    tags.value = [...props.book.tags];
    identifiers.value = props.book.identifiers.map((id) => ({ type: id.type, value: id.value }));
    if (genreOptions.value.length === 0) {
      try {
        genreOptions.value = await fetchGenres();
      } catch {
        // a missing taxonomy only disables the tag suggestions, not editing
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
    genres: tags.value.map((t) => t.trim()).filter(Boolean),
    identifiers: identifiers.value
      .map((id) => ({ type: id.type.trim(), value: id.value.trim() }))
      .filter((id) => id.type && id.value),
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
        <h3 class="mb-5 text-lg font-semibold">Edit book</h3>

        <form @submit.prevent="save">
          <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <div class="sm:col-span-2">
              <label for="edit-title" class="mb-1 block text-sm font-semibold">Title</label>
              <input
                id="edit-title"
                v-model="form.title"
                data-testid="edit-title"
                type="text"
                class="input w-full"
              />
            </div>

            <div class="sm:col-span-2">
              <label for="edit-authors" class="mb-1 block text-sm font-semibold">
                Authors <span class="text-base-content/50 font-normal">(one per line)</span>
              </label>
              <textarea
                id="edit-authors"
                v-model="authorsText"
                data-testid="edit-authors"
                class="textarea w-full"
                rows="2"
              />
            </div>

            <div>
              <label for="edit-series" class="mb-1 block text-sm font-semibold">Series</label>
              <input id="edit-series" v-model="form.series" type="text" class="input w-full" />
            </div>
            <div>
              <label for="edit-series-number" class="mb-1 block text-sm font-semibold">
                Series #
              </label>
              <input
                id="edit-series-number"
                v-model.number="form.series_number"
                type="number"
                step="0.1"
                class="input w-full"
              />
            </div>

            <div>
              <label for="edit-publisher" class="mb-1 block text-sm font-semibold">Publisher</label>
              <input
                id="edit-publisher"
                v-model="form.publisher"
                type="text"
                class="input w-full"
              />
            </div>
            <div>
              <label for="edit-year" class="mb-1 block text-sm font-semibold">Year</label>
              <input id="edit-year" v-model.number="form.year" type="number" class="input w-full" />
            </div>

            <div>
              <label for="edit-language" class="mb-1 block text-sm font-semibold">Language</label>
              <select
                id="edit-language"
                v-model="form.language"
                data-testid="edit-language"
                class="select w-full"
                @keydown.escape.stop
              >
                <option v-for="lang in languageOptions" :key="lang.code" :value="lang.code">
                  {{ lang.label }}
                </option>
              </select>
            </div>

            <div class="sm:col-span-2">
              <label for="edit-tags" class="mb-1 block text-sm font-semibold">Tags</label>
              <TagSelect
                v-model="tags"
                input-id="edit-tags"
                :options="genreOptions"
                placeholder="Select tags…"
              />
            </div>

            <div class="sm:col-span-2">
              <span class="mb-1 block text-sm font-semibold">Identifiers</span>
              <IdentifierEditor v-model="identifiers" />
            </div>

            <div class="sm:col-span-2">
              <label for="edit-annotation" class="mb-1 block text-sm font-semibold">
                Annotation
              </label>
              <textarea
                id="edit-annotation"
                v-model="form.annotation"
                class="textarea w-full"
                rows="4"
              />
            </div>
          </div>

          <div class="mt-6 flex justify-end gap-2">
            <button type="button" class="btn btn-ghost" @click="emit('close')">Cancel</button>
            <button
              type="submit"
              data-testid="edit-save"
              class="btn btn-primary"
              :disabled="saving"
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

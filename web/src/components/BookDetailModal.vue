<template>
  <dialog class="modal" :class="{ 'modal-open': id !== null }">
    <div
      ref="modalBox"
      class="modal-box h-full max-h-screen max-w-none rounded-none p-6 sm:h-auto sm:max-h-[90vh] sm:max-w-4xl sm:rounded-2xl"
    >
      <button
        type="button"
        data-testid="modal-close"
        class="btn btn-sm btn-circle btn-ghost absolute right-3 top-3"
        @click="emit('close')"
      >
        ✕
      </button>

      <BookDetail v-if="book" :book="book" @updated="onUpdated" />
      <div v-else data-testid="detail-loading" class="flex justify-center py-16">
        <span class="loading loading-spinner loading-lg text-base-content/60" />
      </div>
    </div>
    <div class="modal-backdrop" data-testid="modal-backdrop" @click="emit('close')"></div>
  </dialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue';

import { fetchBook } from '@/api';
import BookDetail from '@/components/BookDetail.vue';
import { useModalFocus } from '@/composables/useModalFocus';
import { useToast } from '@/composables/useToast';
import type { Book } from '@/types';

const props = defineProps<{ id: number | null }>();
const emit = defineEmits<{ close: []; updated: [book: Book] }>();
const toast = useToast();

const book = ref<Book | null>(null);

// Escape (top-of-stack only), focus trap, and focus restoration for the
// class-toggled modal, which gets none of that from a native showModal().
const modalBox = ref<HTMLElement | null>(null);
useModalFocus(
  computed(() => props.id !== null),
  modalBox,
  () => emit('close'),
);

// Re-emit an enrich/Fix-Match update so the originating grid card refreshes its
// cover/title (cache-buster) without waiting for a full reload.
function onUpdated(updated: Book): void {
  book.value = updated;
  emit('updated', updated);
}

let reqSeq = 0;
watch(
  () => props.id,
  async (id) => {
    if (id === null) return;
    const seq = ++reqSeq;
    book.value = null;
    try {
      const loaded = await fetchBook(id);
      if (seq === reqSeq) book.value = loaded; // ignore stale resolutions
    } catch (err) {
      if (seq === reqSeq) {
        // Don't leave the modal spinning forever on a failed fetch; surface the
        // error and close it.
        toast.error(`Failed to load book: ${(err as Error).message}`);
        emit('close');
      }
    }
  },
  { immediate: true },
);
</script>

<template>
  <!--
    Teleport to <body> so the dialog escapes BookDetail's `.modal-box`. daisyUI's
    open modal-box keeps `scale`/`translate` set (non-`none`), which makes it a
    containing block for `position: fixed`; nested here, this dialog's `inset: 0`
    would resolve to that box instead of the viewport, shrinking its grid track
    below the box's `max-height: 100vh` so the centered, clipped box hides its
    bottom rows. Teleporting restores viewport-relative positioning.
  -->
  <Teleport to="body">
    <dialog class="modal" :class="{ 'modal-open': open }">
      <div ref="modalBox" class="modal-box max-w-2xl">
        <button
          type="button"
          data-testid="fixmatch-close"
          class="btn btn-sm btn-circle btn-ghost absolute right-3 top-3"
          @click="emit('close')"
        >
          ✕
        </button>
        <h3 class="mb-4 text-lg font-semibold">Fix match</h3>

        <form class="mb-4 flex gap-2" @submit.prevent="search">
          <input
            v-model="query"
            type="text"
            placeholder="Search Google Books…"
            class="input input-bordered input-sm flex-1"
          />
          <button type="submit" class="btn btn-primary btn-sm" :disabled="loading">Search</button>
        </form>

        <div v-if="loading" class="flex justify-center py-8">
          <span class="loading loading-spinner loading-lg text-base-content/60" />
        </div>

        <ul v-else-if="results.length" data-testid="fixmatch-results" class="flex flex-col gap-2">
          <li
            v-for="c in results"
            :key="c.source + c.volume_id"
            class="rounded-box bg-base-200 flex items-center gap-3 p-2"
          >
            <img
              v-if="c.thumbnail"
              :src="c.thumbnail"
              :alt="c.title"
              class="h-16 w-auto rounded"
              loading="lazy"
            />
            <div class="min-w-0 flex-1">
              <p class="truncate text-sm font-semibold">{{ c.title }}</p>
              <p class="text-base-content/60 truncate text-xs">
                {{ (c.authors ?? []).join(', ') }}<template v-if="c.year"> · {{ c.year }}</template>
              </p>
            </div>
            <button
              type="button"
              class="btn btn-primary btn-xs"
              :disabled="applying"
              @click="apply(c)"
            >
              Use this
            </button>
          </li>
        </ul>

        <p v-else-if="searched" class="text-base-content/60 py-8 text-center text-sm">
          No matches found.
        </p>
      </div>
      <div class="modal-backdrop" @click="emit('close')"></div>
    </dialog>
  </Teleport>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue';

import { applyMatch, searchMatch } from '@/api';
import { useModalFocus } from '@/composables/useModalFocus';
import { useToast } from '@/composables/useToast';
import type { Book, MatchCandidate } from '@/types';

const props = defineProps<{ bookId: number; open: boolean; initialQuery: string }>();
const emit = defineEmits<{ close: []; applied: [book: Book] }>();
const toast = useToast();

// Escape close (previously missing here entirely), focus trap, and restoration.
const modalBox = ref<HTMLElement | null>(null);
useModalFocus(
  computed(() => props.open),
  modalBox,
  () => emit('close'),
);

const query = ref('');
const results = ref<MatchCandidate[]>([]);
const loading = ref(false);
const applying = ref(false);
const searched = ref(false);

// Reset and prefill the query each time the modal opens.
watch(
  () => props.open,
  (open) => {
    if (!open) return;
    query.value = props.initialQuery;
    results.value = [];
    searched.value = false;
  },
);

async function search(): Promise<void> {
  const q = query.value.trim();
  if (!q) return;
  loading.value = true;
  try {
    results.value = await searchMatch(props.bookId, q);
    searched.value = true;
  } catch (err) {
    toast.error(`Search failed: ${(err as Error).message}`);
  } finally {
    loading.value = false;
  }
}

async function apply(candidate: MatchCandidate): Promise<void> {
  applying.value = true;
  try {
    const updated = await applyMatch(props.bookId, candidate.source, candidate.volume_id);
    toast.success('Metadata updated');
    emit('applied', updated);
    emit('close');
  } catch (err) {
    toast.error(`Update failed: ${(err as Error).message}`);
  } finally {
    applying.value = false;
  }
}
</script>

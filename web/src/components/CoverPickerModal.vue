<template>
  <Teleport to="body">
    <dialog class="modal" :class="{ 'modal-open': open }">
      <div ref="modalBox" class="modal-box max-w-lg">
        <button
          type="button"
          data-testid="cover-close"
          class="btn btn-sm btn-circle btn-ghost absolute right-3 top-3"
          @click="emit('close')"
        >
          ✕
        </button>
        <h3 class="mb-4 text-lg font-semibold">Set cover</h3>

        <!-- Drop zone doubles as the paste target. -->
        <div
          data-testid="cover-dropzone"
          class="border-base-300 rounded-box mb-4 border-2 border-dashed p-6 text-center"
          :class="{ 'border-primary': dragging }"
          tabindex="0"
          @dragover.prevent="dragging = true"
          @dragleave.prevent="dragging = false"
          @drop.prevent="onDrop"
          @paste="onPaste"
        >
          <p class="text-base-content/60 text-sm">
            Drag an image here, paste from the clipboard, or
            <label class="link cursor-pointer">
              choose a file
              <input
                data-testid="cover-file"
                type="file"
                accept="image/*"
                class="hidden"
                @change="onFile"
              />
            </label>
          </p>
          <p v-if="busy" class="text-base-content/60 mt-2 text-xs">Uploading…</p>
        </div>

        <form class="flex gap-2" @submit.prevent="applyURL">
          <input
            v-model="url"
            data-testid="cover-url-input"
            type="url"
            placeholder="…or paste an image URL"
            class="input input-bordered input-sm flex-1"
          />
          <button
            type="submit"
            data-testid="cover-url-apply"
            class="btn btn-primary btn-sm"
            :disabled="busy"
            @click.prevent="applyURL"
          >
            Apply
          </button>
        </form>

        <div class="divider my-3 text-xs">or search providers</div>

        <form class="mb-3 flex gap-2" @submit.prevent="runSearch">
          <input
            v-model="searchTerm"
            data-testid="cover-search-input"
            type="text"
            placeholder="Search Amazon, Goodreads, Open Library, Google Books"
            class="input input-bordered input-sm flex-1"
          />
          <button
            type="submit"
            data-testid="cover-search-go"
            class="btn btn-sm"
            :disabled="searching"
            @click.prevent="runSearch"
          >
            Search
          </button>
        </form>

        <p v-if="searching" class="text-base-content/60 text-xs">Searching…</p>
        <p v-else-if="searched && candidates.length === 0" class="text-base-content/60 text-xs">
          No covers found — try a deep link below.
        </p>

        <div v-if="candidates.length" class="grid grid-cols-4 gap-2">
          <button
            v-for="(c, i) in candidates"
            :key="c.full_url + i"
            type="button"
            data-testid="cover-candidate"
            class="border-base-300 hover:border-primary rounded border p-1"
            :title="c.source"
            :disabled="busy"
            @click="applyCandidate(c)"
          >
            <img
              :src="c.thumb_url"
              :alt="c.source"
              class="h-28 w-full object-contain"
              loading="lazy"
            />
            <span class="text-base-content/60 block truncate text-[10px]">{{ c.source }}</span>
          </button>
        </div>

        <div class="mt-3 flex flex-wrap gap-2">
          <a
            v-for="dl in deepLinks"
            :key="dl.label"
            :data-testid="`deeplink-${dl.label.toLowerCase()}`"
            :href="dl.href"
            target="_blank"
            rel="noopener noreferrer"
            class="btn btn-ghost btn-xs"
          >
            <i class="pi pi-external-link" />
            {{ dl.label }}
          </a>
        </div>
      </div>
      <div class="modal-backdrop" @click="emit('close')"></div>
    </dialog>
  </Teleport>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue';

import { fetchCoverCandidates, setCoverFromUrl, uploadCover } from '@/api';
import { useModalFocus } from '@/composables/useModalFocus';
import { useToast } from '@/composables/useToast';
import type { Book, CoverCandidate } from '@/types';

const props = defineProps<{ book: Book; open: boolean }>();
const emit = defineEmits<{ close: []; applied: [book: Book] }>();
const toast = useToast();

const modalBox = ref<HTMLElement | null>(null);
useModalFocus(
  computed(() => props.open),
  modalBox,
  () => emit('close'),
);

const url = ref('');
const busy = ref(false);
const dragging = ref(false);

async function send(action: () => Promise<Book>): Promise<void> {
  busy.value = true;
  try {
    const updated = await action();
    toast.success('Cover updated');
    emit('applied', updated);
    emit('close');
  } catch (err) {
    toast.error(`Cover update failed: ${(err as Error).message}`);
  } finally {
    busy.value = false;
  }
}

function onFile(e: Event): void {
  const file = (e.target as HTMLInputElement).files?.[0];
  if (file) void send(() => uploadCover(props.book.id, file));
}

function onDrop(e: DragEvent): void {
  dragging.value = false;
  const file = e.dataTransfer?.files?.[0];
  if (file) {
    void send(() => uploadCover(props.book.id, file));
    return;
  }
  // Dragged from another tab: a URL rather than a file.
  const dropped = e.dataTransfer?.getData('text/uri-list') || e.dataTransfer?.getData('text/plain');
  if (dropped) void send(() => setCoverFromUrl(props.book.id, dropped));
}

function onPaste(e: ClipboardEvent): void {
  const item = Array.from(e.clipboardData?.items ?? []).find((i) => i.type.startsWith('image/'));
  const file = item?.getAsFile();
  if (file) void send(() => uploadCover(props.book.id, file));
}

function applyURL(): void {
  const u = url.value.trim();
  if (u) void send(() => setCoverFromUrl(props.book.id, u));
}

const searchTerm = ref('');
const searching = ref(false);
const searched = ref(false);
const candidates = ref<CoverCandidate[]>([]);

// Seed the provider search from the book's title so one click usually suffices.
watch(
  () => props.open,
  (open) => {
    if (open && !searchTerm.value) searchTerm.value = props.book.title;
  },
  { immediate: true },
);

async function runSearch(): Promise<void> {
  searching.value = true;
  try {
    candidates.value = await fetchCoverCandidates(props.book.id, searchTerm.value);
  } catch (err) {
    toast.error(`Cover search failed: ${(err as Error).message}`);
    candidates.value = [];
  } finally {
    searching.value = false;
    searched.value = true;
  }
}

function applyCandidate(c: CoverCandidate): void {
  void send(() => setCoverFromUrl(props.book.id, c.full_url));
}

const deepLinks = computed(() => {
  const term = encodeURIComponent(searchTerm.value || props.book.title);

  return [
    { label: 'Amazon', href: `https://www.amazon.com/s?k=${term}&i=stripbooks` },
    { label: 'Goodreads', href: `https://www.goodreads.com/search?q=${term}` },
    { label: 'Google', href: `https://www.google.com/search?tbm=isch&q=${term}+book+cover` },
  ];
});
</script>

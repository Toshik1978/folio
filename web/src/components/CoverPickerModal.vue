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

        <!-- Phase 2 mounts the provider search grid below this line. -->
      </div>
      <div class="modal-backdrop" @click="emit('close')"></div>
    </dialog>
  </Teleport>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue';

import { setCoverFromUrl, uploadCover } from '@/api';
import { useModalFocus } from '@/composables/useModalFocus';
import { useToast } from '@/composables/useToast';
import type { Book } from '@/types';

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
</script>

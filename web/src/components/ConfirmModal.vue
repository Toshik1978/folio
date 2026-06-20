<template>
  <dialog class="modal" :class="{ 'modal-open': state.open }">
    <div ref="modalBox" class="modal-box">
      <h3 class="text-lg font-bold">{{ state.title }}</h3>
      <p class="py-4">{{ state.body }}</p>
      <div class="modal-action">
        <button type="button" class="btn" data-testid="confirm-cancel" @click="respond(false)">
          {{ state.cancelLabel ?? 'Cancel' }}
        </button>
        <button
          type="button"
          class="btn"
          :class="state.danger ? 'btn-error' : 'btn-primary'"
          data-testid="confirm-ok"
          @click="respond(true)"
        >
          {{ state.confirmLabel ?? 'Confirm' }}
        </button>
      </div>
    </div>
    <div class="modal-backdrop" @click="respond(false)"></div>
  </dialog>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue';

import { useConfirm } from '@/composables/useConfirm';
import { useModalFocus } from '@/composables/useModalFocus';

const { state, respond } = useConfirm();

// Escape dismisses as a cancel; useModalFocus scopes it to the top-most modal
// and adds the focus trap/restoration the class-toggled modal (no native
// showModal()) doesn't get for free.
const modalBox = ref<HTMLElement | null>(null);
useModalFocus(
  computed(() => state.value.open),
  modalBox,
  () => respond(false),
);
</script>

<template>
  <div class="toast toast-top toast-end z-[200]">
    <button
      v-for="t in toasts"
      :key="t.id"
      type="button"
      class="alert text-left"
      :class="t.type === 'error' ? 'alert-error' : 'alert-success'"
      :data-testid="`toast-${t.type}`"
      @click="dismiss(t.id)"
    >
      <span>{{ t.message }}</span>
    </button>
  </div>
</template>

<script setup lang="ts">
import { useToast } from '@/composables/useToast';

const { toasts, dismiss } = useToast();
</script>

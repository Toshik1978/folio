<template>
  <div class="mb-5 flex flex-wrap gap-1">
    <button
      v-for="letter in letters"
      :key="letter"
      type="button"
      data-testid="letter-btn"
      class="btn btn-xs"
      :class="letter === activeLetter ? 'btn-primary' : 'btn-ghost'"
      :aria-pressed="letter === activeLetter"
      :disabled="!availableLetters.has(letter)"
      @click="$emit('select', letter)"
    >
      {{ letter }}
    </button>
  </div>
</template>

<script setup lang="ts">
import { ALPHABET } from '@/alphabet';

const letters = ALPHABET;

defineProps<{
  activeLetter: string | null;
  availableLetters: Set<string>;
}>();

defineEmits<{ select: [letter: string] }>();
</script>

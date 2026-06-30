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
import { computed } from 'vue';

import { CYRILLIC, HASH_BUCKET, LATIN } from '@/alphabet';

const props = defineProps<{
  activeLetter: string | null;
  availableLetters: Set<string>;
}>();

defineEmits<{ select: [letter: string] }>();

// Render a script block only when at least one of its buckets has entries, so a
// purely Latin library hides the Cyrillic row and vice versa. '#' is always
// shown as the catch-all.
const letters = computed(() => {
  const hasAny = (block: string[]) => block.some((l) => props.availableLetters.has(l));
  return [...(hasAny(CYRILLIC) ? CYRILLIC : []), ...(hasAny(LATIN) ? LATIN : []), HASH_BUCKET];
});
</script>

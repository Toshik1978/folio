<template>
  <div>
    <h1 class="mb-4 text-[22px] font-bold">Publishers</h1>
    <AlphabetSelector
      :active-letter="activeLetter"
      :available-letters="availableLetters"
      @select="selectLetter"
    />
    <AlphabetList :items="items" filter-key="publisher" />
    <div ref="scrollTrigger" class="flex min-h-20 justify-center py-8">
      <span v-if="loading" class="loading loading-spinner loading-lg text-base-content/60" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue';

import { fetchPublisherLetters, fetchPublishers } from '@/api';
import AlphabetList from '@/components/AlphabetList.vue';
import AlphabetSelector from '@/components/AlphabetSelector.vue';
import { useAlphabetBrowse } from '@/composables/useAlphabetBrowse';
import type { Publisher } from '@/types';

const scrollTrigger = ref<HTMLElement | null>(null);
const { items, availableLetters, activeLetter, loading, selectLetter } =
  useAlphabetBrowse<Publisher>(scrollTrigger, fetchPublisherLetters, fetchPublishers);
</script>

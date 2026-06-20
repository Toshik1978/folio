<template>
  <div class="border-base-300 bg-base-200 rounded-box mb-8 border p-5" data-testid="library-stats">
    <div class="flex items-start justify-between gap-4">
      <div class="min-w-0 flex-1">
        <div v-if="loading" class="flex justify-center py-2">
          <span class="loading loading-spinner" />
        </div>
        <div v-else-if="stats" class="flex flex-wrap items-baseline gap-x-8 gap-y-2">
          <div>
            <span class="text-lg font-bold" data-testid="stat-books">{{
              stats.total_books.toLocaleString()
            }}</span>
            books
          </div>
          <div>
            <span class="text-lg font-bold">{{ formatSize(stats.total_size_bytes) }}</span>
          </div>
          <div>
            <span class="text-lg font-bold">{{ stats.authors.toLocaleString() }}</span> authors
          </div>
          <div>
            <span class="text-lg font-bold">{{ stats.series.toLocaleString() }}</span> series
          </div>
          <div
            v-if="formatList"
            class="text-base-content/70 w-full text-sm"
            data-testid="stat-formats"
          >
            {{ formatList }}
          </div>
        </div>
      </div>
      <slot name="action" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue';

import { fetchStats } from '@/api';
import { useLibrary } from '@/composables/useLibrary';
import { useToast } from '@/composables/useToast';
import type { Stats } from '@/types';
import { formatSize } from '@/utils/format';

const stats = ref<Stats | null>(null);
const loading = ref(true);
const toast = useToast();
const { libraries } = useLibrary();

const formatList = computed(() =>
  stats.value
    ? Object.entries(stats.value.formats)
        .sort((a, b) => b[1] - a[1])
        .map(([fmt, n]) => `${fmt} ${n.toLocaleString()}`)
        .join(' · ')
    : '',
);

async function load(): Promise<void> {
  try {
    // Deliberately unscoped (decision 2026-06-13): this block is a whole-catalog
    // overview sitting above the all-libraries management table, so it ignores the
    // header library selector. See docs/FRONTEND.md "Settings → Libraries tab".
    stats.value = await fetchStats();
  } catch (err) {
    toast.error(`Failed to load stats: ${(err as Error).message}`);
  } finally {
    loading.value = false;
  }
}

onMounted(load);

// These totals must track the shared library list, not just the initial mount:
// adding a library (and its background sync indexing books) changes the catalog
// while this component stays mounted. Watch a primitive signal — library count
// plus summed book counts — so we refetch only when the catalog actually
// changes, not on every poll that reassigns the array.
watch(
  () => `${libraries.value.length}:${libraries.value.reduce((n, l) => n + l.book_count, 0)}`,
  () => void load(),
);
</script>

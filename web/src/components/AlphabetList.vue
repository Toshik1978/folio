<template>
  <div class="flex flex-col gap-6">
    <div v-for="group in groups" :key="group.letter">
      <h3
        data-testid="group-heading"
        class="border-base-300 mb-2 border-b pb-1.5 text-lg font-bold"
      >
        {{ group.letter }}
      </h3>
      <ul class="menu w-full gap-0.5 p-0">
        <li v-for="(item, i) in group.items" :key="`${i}:${item.name}`">
          <router-link
            :to="{ path: '/', query: { [filterKey]: exactValue(item.name) } }"
            class="flex items-center justify-between"
          >
            <span data-testid="item-name" class="font-medium">{{ item.name }}</span>
            <span data-testid="item-count" class="text-base-content/60 text-sm">
              {{ item.book_count }} books
            </span>
          </router-link>
        </li>
      </ul>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue';

interface ListItem {
  name: string;
  book_count: number;
}

const props = defineProps<{
  items: ListItem[];
  filterKey: string;
}>();

// author and series are FTS-searchable facets, so a navigation link must request
// an exact match (leading '='). tag/publisher are exact-only already.
function exactValue(name: string): string {
  return props.filterKey === 'author' || props.filterKey === 'series' ? `=${name}` : name;
}

// Items arrive already scoped to one selected letter, but '#' mixes several
// first characters (digits, punctuation, other scripts), so we still group by
// first letter to give that bucket readable sub-headings.
const groups = computed(() => {
  const map = new Map<string, ListItem[]>();
  for (const item of props.items) {
    const letter = item.name.charAt(0).toUpperCase();
    if (!map.has(letter)) map.set(letter, []);
    map.get(letter)!.push(item);
  }
  return Array.from(map.entries())
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([letter, items]) => ({ letter, items }));
});
</script>

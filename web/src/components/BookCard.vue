<template>
  <router-link
    :to="{ path: `/books/${book.id}`, query: route.query }"
    class="group flex flex-col gap-2.5"
  >
    <figure
      class="rounded-box bg-base-200 aspect-[2/3] overflow-hidden shadow-md transition-all duration-300 ease-out group-hover:-translate-y-1.5 group-hover:shadow-xl"
    >
      <img
        v-if="thumbnailUrl"
        :src="thumbnailUrl"
        :alt="book.title"
        class="h-full w-full object-cover"
        loading="lazy"
      />
      <div
        v-else
        data-testid="cover-placeholder"
        class="text-base-content/40 flex h-full w-full items-center justify-center text-3xl"
      >
        <i class="pi pi-book" />
      </div>
    </figure>
    <div class="min-w-0">
      <h3 class="truncate text-sm font-semibold leading-tight">{{ book.title }}</h3>
      <p class="text-base-content/60 truncate text-xs">
        {{ book.authors.map((a) => a.name).join(', ') }}
      </p>
      <div v-if="book.rating" class="text-warning mt-1 text-xs">
        <StarRating :rating="book.rating" />
      </div>
    </div>
  </router-link>
</template>

<script setup lang="ts">
import { computed } from 'vue';
import { useRoute } from 'vue-router';

import StarRating from '@/components/StarRating.vue';
import type { Book } from '@/types';

const props = defineProps<{ book: Book }>();

// The card shows a small image; use the thumbnail variant of the cover URL.
// cover_url is `/api/books/<id>/cover?v=…`; insert `/thumbnail` before the query.
const thumbnailUrl = computed(() =>
  props.book.cover_url ? props.book.cover_url.replace('/cover?', '/cover/thumbnail?') : null,
);

const route = useRoute();
</script>

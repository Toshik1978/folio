<template>
  <router-link
    :to="{ path: `/books/${book.id}`, query: route.query }"
    class="group flex flex-col gap-2.5"
  >
    <figure
      class="rounded-box bg-base-200 aspect-[2/3] overflow-hidden shadow-md transition-all duration-300 ease-out group-hover:-translate-y-1.5 group-hover:shadow-xl"
    >
      <img
        v-if="book.cover_url"
        :src="book.cover_url"
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
      <div
        v-if="book.rating"
        class="text-warning mt-1 flex gap-0.5 text-xs"
        :aria-label="`Rating: ${book.rating} of 5`"
      >
        <i v-for="i in 5" :key="i" :class="i <= book.rating ? 'pi pi-star-fill' : 'pi pi-star'" />
      </div>
    </div>
  </router-link>
</template>

<script setup lang="ts">
import { useRoute } from 'vue-router';

import type { Book } from '@/types';

defineProps<{ book: Book }>();

const route = useRoute();
</script>

<template>
  <div>
    <div class="flex flex-col gap-8 md:flex-row">
      <div class="w-full shrink-0 md:w-60">
        <figure class="rounded-box bg-base-200 aspect-[2/3] overflow-hidden shadow-lg">
          <img
            v-if="book.cover_url"
            :src="book.cover_url"
            :alt="book.title"
            class="h-full w-full object-cover"
          />
          <div
            v-else
            class="text-base-content/40 flex h-full w-full items-center justify-center text-5xl"
          >
            <i class="pi pi-book" />
          </div>
        </figure>
      </div>

      <div class="min-w-0 flex-1">
        <h1 data-testid="detail-title" class="mb-2 text-2xl font-bold">{{ book.title }}</h1>

        <p class="mb-3 text-base">
          <router-link
            v-for="(author, i) in book.authors"
            :key="author.id"
            :to="{ path: '/', query: { author: `=${author.name}` } }"
            class="link link-hover link-primary"
            >{{ author.name }}{{ i < book.authors.length - 1 ? ', ' : '' }}</router-link
          >
        </p>

        <div v-if="book.tags.length" class="mb-4 flex flex-wrap gap-1.5">
          <router-link
            v-for="tag in book.tags"
            :key="tag"
            :to="{ path: '/', query: { tag } }"
            class="badge badge-outline hover:badge-primary"
            >{{ tag }}</router-link
          >
        </div>

        <dl
          data-testid="detail-fields"
          class="mb-4 grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm"
        >
          <template v-if="book.series">
            <dt class="text-base-content/60 font-medium">Series</dt>
            <dd class="m-0">
              <router-link
                :to="{ path: '/', query: { series: `=${book.series}` } }"
                class="link link-hover link-primary"
              >
                {{ book.series
                }}<template v-if="book.series_index !== null"> #{{ book.series_index }}</template>
              </router-link>
            </dd>
          </template>
          <template v-if="book.publisher">
            <dt class="text-base-content/60 font-medium">Publisher</dt>
            <dd class="m-0">
              <router-link
                :to="{ path: '/', query: { publisher: book.publisher } }"
                class="link link-hover link-primary"
              >
                {{ book.publisher }}
              </router-link>
            </dd>
          </template>
          <template v-if="book.year">
            <dt class="text-base-content/60 font-medium">Year</dt>
            <dd class="m-0">{{ book.year }}</dd>
          </template>
          <template v-if="book.pages">
            <dt class="text-base-content/60 font-medium">Pages</dt>
            <dd class="m-0">{{ book.pages }}</dd>
          </template>
          <template v-if="book.rating">
            <dt class="text-base-content/60 font-medium">Rating</dt>
            <dd class="text-warning m-0 flex gap-0.5" :aria-label="`${book.rating} of 5`">
              <i
                v-for="i in 5"
                :key="i"
                :class="i <= book.rating ? 'pi pi-star-fill' : 'pi pi-star'"
              />
            </dd>
          </template>
          <template v-if="book.language">
            <dt class="text-base-content/60 font-medium">Language</dt>
            <dd class="m-0">{{ languageLabel(book.language) }}</dd>
          </template>
        </dl>

        <div v-if="book.identifiers.length" class="mb-4 flex flex-wrap gap-2">
          <a
            v-for="ident in book.identifiers"
            :key="ident.type"
            :href="ident.url ?? undefined"
            :target="ident.url ? '_blank' : undefined"
            class="badge badge-ghost font-mono"
            :class="{ 'cursor-default': !ident.url }"
          >
            {{ ident.type.toUpperCase() }}: {{ ident.value }}
          </a>
        </div>

        <div class="flex flex-wrap items-center gap-2">
          <a
            v-for="fmt in book.formats"
            :key="fmt.download_url"
            :href="fmt.download_url"
            data-testid="download-link"
            class="btn btn-primary btn-sm gap-2"
          >
            <i class="pi pi-download" />
            {{ fmt.type.toUpperCase() }} ({{ formatSize(fmt.size_bytes) }})
          </a>
          <button
            type="button"
            data-testid="fixmatch-open"
            class="btn btn-ghost btn-sm gap-2"
            @click="fixMatchOpen = true"
          >
            <i class="pi pi-search" />
            Fix match
          </button>
          <button
            type="button"
            data-testid="edit-open"
            class="btn btn-ghost btn-sm gap-2"
            @click="editOpen = true"
          >
            <i class="pi pi-pencil" />
            Edit
          </button>
          <button
            type="button"
            data-testid="cover-open"
            class="btn btn-ghost btn-sm gap-2"
            @click="coverOpen = true"
          >
            <i class="pi pi-image" />
            Cover
          </button>
        </div>
      </div>
    </div>

    <FixMatchModal
      :book-id="book.id"
      :open="fixMatchOpen"
      :initial-query="matchQuery"
      @close="fixMatchOpen = false"
      @applied="emit('updated', $event)"
    />

    <EditBookModal
      :book="book"
      :open="editOpen"
      @close="editOpen = false"
      @applied="emit('updated', $event)"
    />

    <CoverPickerModal
      :book="book"
      :open="coverOpen"
      @close="coverOpen = false"
      @applied="emit('updated', $event)"
    />

    <div v-if="book.annotation" class="border-base-300 mt-8 border-t pt-6">
      <h2 class="mb-3 text-base font-semibold">Annotation</h2>
      <div
        data-testid="annotation-body"
        class="text-base-content/70 text-sm leading-relaxed"
        v-html="sanitizedAnnotation"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import DOMPurify from 'dompurify';
import { computed, ref } from 'vue';

import CoverPickerModal from '@/components/CoverPickerModal.vue';
import EditBookModal from '@/components/EditBookModal.vue';
import FixMatchModal from '@/components/FixMatchModal.vue';
import type { Book } from '@/types';
import { formatSize } from '@/utils/format';
import { languageLabel } from '@/utils/language';

const props = defineProps<{ book: Book }>();
const emit = defineEmits<{ updated: [book: Book] }>();

const fixMatchOpen = ref(false);
const editOpen = ref(false);
const coverOpen = ref(false);

// Seed the Fix Match search with the book's title and authors.
const matchQuery = computed(() =>
  [props.book.title, ...props.book.authors.map((a) => a.name)].join(' ').trim(),
);

// The backend already sanitizes annotations (bluemonday.UGCPolicy), so this is
// defense-in-depth: a client-side pass means a future unsanitized path can't turn
// the v-html sink into DOM XSS.
const sanitizedAnnotation = computed(() =>
  props.book.annotation ? DOMPurify.sanitize(props.book.annotation) : '',
);
</script>

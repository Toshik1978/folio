<template>
  <select class="select select-sm w-48" :value="libraryId ?? ''" @change="onChange">
    <option value="">All Libraries</option>
    <option v-for="lib in libraries" :key="lib.id" :value="lib.id">{{ lib.name }}</option>
  </select>
</template>

<script setup lang="ts">
import { useLibrary } from '@/composables/useLibrary';

const { libraryId, libraries, setLibrary } = useLibrary();

function onChange(e: Event): void {
  const value = (e.target as HTMLSelectElement).value;
  setLibrary(value === '' ? null : Number(value));
}
</script>

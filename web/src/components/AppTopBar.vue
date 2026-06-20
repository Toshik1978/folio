<template>
  <header class="navbar bg-base-100/90 border-base-300 z-[90] gap-2 border-b backdrop-blur">
    <label for="app-drawer" class="btn btn-ghost btn-sm lg:hidden" aria-label="Open menu">
      <i class="pi pi-bars" />
    </label>
    <LibrarySelect />
    <div class="max-w-md flex-1">
      <SearchInput />
    </div>
    <div class="ml-auto flex items-center gap-1">
      <ThemePicker />
      <router-link to="/settings" class="btn btn-ghost btn-sm" title="Settings">
        <i class="pi pi-cog" />
      </router-link>
    </div>
  </header>
</template>

<script setup lang="ts">
import LibrarySelect from './LibrarySelect.vue';
import SearchInput from './SearchInput.vue';
import ThemePicker from './ThemePicker.vue';
</script>

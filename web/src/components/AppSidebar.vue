<template>
  <aside class="bg-base-200 min-h-full w-56 p-3">
    <div class="border-base-300 mb-2 border-b px-3 pb-4 pt-2">
      <router-link to="/" class="text-base-content text-lg font-bold tracking-tight">
        Folio
      </router-link>
    </div>
    <ul class="menu w-full gap-0.5">
      <li v-for="link in links" :key="link.route">
        <router-link :to="link.route" :class="{ active: isActive(link.route) }">
          <i :class="link.icon" />
          <span>{{ link.label }}</span>
        </router-link>
      </li>
    </ul>
  </aside>
</template>

<script setup lang="ts">
import { useRoute } from 'vue-router';

const route = useRoute();

const links = [
  { label: 'Books', route: '/', icon: 'pi pi-book' },
  { label: 'Authors', route: '/authors', icon: 'pi pi-users' },
  { label: 'Series', route: '/series', icon: 'pi pi-list' },
  { label: 'Tags', route: '/tags', icon: 'pi pi-tags' },
  { label: 'Publishers', route: '/publishers', icon: 'pi pi-building' },
];

function isActive(path: string): boolean {
  if (path === '/') return route.path === '/' || route.path.startsWith('/books');
  return route.path.startsWith(path);
}
</script>

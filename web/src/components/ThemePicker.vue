<template>
  <div class="dropdown dropdown-end">
    <div tabindex="0" role="button" class="btn btn-ghost btn-sm" aria-label="Choose theme">
      <i class="pi pi-palette" />
    </div>
    <ul
      tabindex="0"
      class="dropdown-content menu menu-sm bg-base-200 rounded-box z-[100] mt-2 w-44 p-2 shadow"
    >
      <li v-for="t in themes" :key="t.id">
        <button
          type="button"
          class="flex items-center gap-2"
          :class="{ 'menu-active': theme === t.id }"
          :data-testid="`theme-${t.id}`"
          @click="select(t.id)"
        >
          <span
            :data-theme="t.id"
            class="bg-base-100 border-base-content/20 grid h-4 w-4 shrink-0 grid-cols-2 grid-rows-2 overflow-hidden rounded-sm border"
          >
            <span class="bg-base-content"></span>
            <span class="bg-primary"></span>
            <span class="bg-secondary"></span>
            <span class="bg-accent"></span>
          </span>
          <span class="flex-1 text-left text-xs">{{ t.label }}</span>
          <i v-if="theme === t.id" class="pi pi-check text-[10px]" aria-hidden="true" />
        </button>
      </li>
    </ul>
  </div>
</template>

<script setup lang="ts">
import { useTheme } from '@/composables/useTheme';

const { theme, themes, setTheme } = useTheme();

function select(id: string): void {
  setTheme(id);
  // The DaisyUI dropdown is focus-driven; blurring the active element closes it.
  (document.activeElement as HTMLElement | null)?.blur();
}
</script>

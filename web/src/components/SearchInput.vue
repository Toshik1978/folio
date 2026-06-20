<template>
  <div class="relative w-full">
    <!-- Container mimics a DaisyUI input; chips + native input stay on one line
         and scroll horizontally when they overflow. -->
    <div
      class="input input-sm flex w-full flex-nowrap items-center gap-1.5 overflow-x-auto focus-within:border-primary focus-within:ring-1"
      @click="focusInput"
    >
      <!-- Active filter chips (one per active route param). -->
      <span
        v-for="chip in chips"
        :key="chip.key"
        data-testid="chip"
        class="badge badge-sm shrink-0 gap-1 whitespace-nowrap"
        :class="chip.badgeClass"
      >
        {{ chip.label }}
        <button
          type="button"
          class="cursor-pointer leading-none"
          :aria-label="`Remove ${chip.label}`"
          @click.stop="removeChip(chip.key)"
        >
          ✕
        </button>
      </span>

      <!-- Mini-label shows which facet the next value is being entered for. -->
      <span v-if="activeFacet" class="text-base-content/60 shrink-0 text-xs font-medium">
        {{ facetLabel(activeFacet) }}:
      </span>

      <input
        ref="inputEl"
        v-model="text"
        type="text"
        :placeholder="activeFacet ? `Enter ${facetLabel(activeFacet)}…` : 'Search the library…'"
        class="min-w-24 grow border-none bg-transparent p-0 text-sm outline-none focus:outline-none"
        @focus="onFocus"
        @blur="onBlur"
        @keydown.enter="commit"
        @keydown.esc="resetEntry"
      />
    </div>

    <!-- Facet menu: text facets plus value facets (Format/Language). -->
    <ul
      v-if="showMenu && !activeFacet && !valueFacet"
      data-testid="facet-menu"
      class="menu bg-base-100 rounded-box absolute z-50 mt-1 w-56 p-1 shadow"
    >
      <li v-for="facet in FACETS" :key="facet.field">
        <button
          type="button"
          data-testid="facet-option"
          @mousedown.prevent="selectFacet(facet.field)"
        >
          {{ facet.label }}
        </button>
      </li>
      <li v-for="vf in VALUE_FACETS" :key="vf.field">
        <button
          type="button"
          data-testid="value-facet-option"
          @mousedown.prevent="selectValueFacet(vf.field)"
        >
          {{ vf.label }}
        </button>
      </li>
    </ul>

    <!-- Value submenu: pick a known format/language value. -->
    <ul
      v-if="showMenu && valueFacet"
      data-testid="value-menu"
      class="menu bg-base-100 rounded-box absolute z-50 mt-1 max-h-64 w-56 flex-nowrap overflow-y-auto p-1 shadow"
    >
      <li v-for="opt in valueOptions" :key="opt">
        <button type="button" data-testid="value-option" @mousedown.prevent="applyValue(opt)">
          {{ valueFacet === 'lang' ? languageLabel(opt) : opt }}
        </button>
      </li>
      <li v-if="valueOptions.length === 0" class="menu-disabled">
        <span class="text-base-content/50">No values</span>
      </li>
    </ul>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue';
import { type LocationQuery, useRoute, useRouter } from 'vue-router';

import { useFacetValues } from '@/composables/useFacetValues';
import { languageLabel, sortLanguageCodes } from '@/utils/language';

// FacetField names the FTS-searchable facets selectable from the menu. tag and
// publisher are exact-only (they arrive via nav links) and are not in the menu.
type FacetField = 'author' | 'title' | 'series';
// ValueField names facets chosen from a list of known values (not free text).
type ValueField = 'format' | 'lang';

// Menu entries, in display order. label is the human name; field is the URL key.
const FACETS: { field: FacetField; label: string }[] = [
  { field: 'author', label: 'Author' },
  { field: 'title', label: 'Book Title' },
  { field: 'series', label: 'Book Series' },
];

const VALUE_FACETS: { field: ValueField; label: string }[] = [
  { field: 'format', label: 'Format' },
  { field: 'lang', label: 'Language' },
];

// All route params the search bar owns, with chip presentation metadata.
const CHIP_META: Record<string, { label: string; badgeClass: string; exactable: boolean }> = {
  q: { label: 'Search', badgeClass: 'badge-ghost', exactable: false },
  title: { label: 'Title', badgeClass: 'badge-accent', exactable: true },
  author: { label: 'Author', badgeClass: 'badge-primary', exactable: true },
  series: { label: 'Series', badgeClass: 'badge-secondary', exactable: true },
  tag: { label: 'Tag', badgeClass: 'badge-neutral', exactable: false },
  publisher: { label: 'Publisher', badgeClass: 'badge-neutral', exactable: false },
  format: { label: 'Format', badgeClass: 'badge-info', exactable: false },
  lang: { label: 'Language', badgeClass: 'badge-info', exactable: false },
};
const OWNED_KEYS = Object.keys(CHIP_META);

const router = useRouter();
const route = useRoute();
const { formats, languages, load } = useFacetValues();

const inputEl = ref<HTMLInputElement | null>(null);
const text = ref('');
const activeFacet = ref<FacetField | null>(null);
const valueFacet = ref<ValueField | null>(null);
const showMenu = ref(false);

const valueOptions = computed(() =>
  valueFacet.value === 'lang' ? sortLanguageCodes(languages.value) : formats.value,
);

function facetLabel(field: FacetField): string {
  return FACETS.find((f) => f.field === field)!.label;
}

// chips derives the visible tokens from the route query (single source of truth).
// An exactable value beginning with "=" renders as "Field = value"; otherwise
// "Field: value". Reading from the route means navigation auto-syncs the chips.
const chips = computed(() => {
  const out: { key: string; label: string; badgeClass: string }[] = [];
  for (const key of OWNED_KEYS) {
    const raw = route.query[key];
    if (typeof raw !== 'string' || raw === '') continue;
    const meta = CHIP_META[key];
    const exact = meta.exactable && raw.startsWith('=');
    const value = exact ? raw.slice(1) : raw;
    // exact reads "Field = value"; partial/free reads "Field: value".
    const label = exact ? `${meta.label} = ${value}` : `${meta.label}: ${value}`;
    out.push({ key, label, badgeClass: meta.badgeClass });
  }
  return out;
});

function focusInput(): void {
  inputEl.value?.focus();
}

function onFocus(): void {
  showMenu.value = true;
  void load(); // lazily populate the value dropdowns on first open
}

// Delay hiding so a mousedown on a menu option still registers before blur.
function onBlur(): void {
  setTimeout(() => {
    showMenu.value = false;
  }, 100);
}

function selectFacet(field: FacetField): void {
  activeFacet.value = field;
  valueFacet.value = null;
  showMenu.value = false;
  // Focus must return to the input so the user can type the value immediately.
  void nextTick(() => focusInput());
}

function selectValueFacet(field: ValueField): void {
  valueFacet.value = field;
  activeFacet.value = null;
  // Keep the menu open so the value submenu renders.
}

function resetEntry(): void {
  activeFacet.value = null;
  valueFacet.value = null;
  showMenu.value = false;
}

// Build the next route query from the current chips, then apply one change.
// Always navigates to '/' (the books listing) and drops pagination so results
// reset. library scoping is handled separately by useLibrary, not here.
function nextQuery(): LocationQuery {
  const q: LocationQuery = {};
  for (const key of OWNED_KEYS) {
    const raw = route.query[key];
    if (typeof raw === 'string' && raw !== '') q[key] = raw;
  }
  // sort is owned by BooksPage's select, not the search bar — but committing
  // or removing a chip must not silently reset the user's chosen order.
  const sort = route.query.sort;
  if (typeof sort === 'string' && sort !== '') q.sort = sort;
  return q;
}

// commit turns the typed text into a chip: into the active facet if one is
// chosen (value keeps any leading "=" for exact), otherwise a free-text q chip.
function commit(): void {
  const value = text.value.trim();
  if (!value) return;

  const query = nextQuery();
  if (activeFacet.value) {
    query[activeFacet.value] = value; // one value per field → overwrite
  } else {
    query.q = value;
  }

  void router.push({ path: '/', query });
  text.value = '';
  resetEntry();
}

// applyValue commits a picked Format/Language value as its own route param.
function applyValue(value: string): void {
  if (!valueFacet.value) return;
  const query = nextQuery();
  query[valueFacet.value] = value; // one value per field → overwrite
  void router.push({ path: '/', query });
  resetEntry();
}

function removeChip(key: string): void {
  const query = nextQuery();
  delete query[key];
  void router.push({ path: '/', query });
}

// Reset the entry state whenever the route changes (e.g. navigating away).
watch(
  () => route.fullPath,
  () => {
    text.value = '';
    resetEntry();
  },
);
</script>

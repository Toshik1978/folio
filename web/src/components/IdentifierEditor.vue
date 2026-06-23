<script setup lang="ts">
import { ref, watch } from 'vue';

import type { IdentifierInput } from '@/types';

// IdentifierEditor edits a book's identifiers as add/change/remove rows. The
// parent sends the resulting list as the authoritative set, so removing every row
// deletes all identifiers on save. Type is a select (the four with known link-out
// URLs), but an existing uncommon type is preserved as an extra option so a save
// never silently drops it.
const props = defineProps<{ modelValue: IdentifierInput[] }>();
const emit = defineEmits<{ 'update:modelValue': [value: IdentifierInput[]] }>();

// --- Stable-key infrastructure -------------------------------------------
// `rows` is the source of truth for row identity. Each row carries a stable
// `_uid` so Vue keys by identity, not position: removing a middle row reuses the
// surviving DOM nodes, and editing a value keeps the same <input> node — both
// preserving focus and IME composition state.
//
// User actions mutate `rows` in place (same uid on edit, splice on remove, push
// on add) and then emit. The parent echoes every emit back as a new modelValue;
// the watch below recognises that echo and skips re-seeding, so our in-place
// rows survive. Only a genuine external change (e.g. loading a different book)
// re-seeds rows with fresh uids.

let nextUid = 0;
function uid(): number {
  return ++nextUid;
}

interface Row extends IdentifierInput {
  _uid: number;
}

// strip drops the internal `_uid`, yielding the public IdentifierInput shape.
function strip(rs: Row[]): IdentifierInput[] {
  return rs.map((row) => ({ type: row.type, value: row.value }));
}

// seed replaces rows wholesale, assigning a fresh uid to every item. Used on
// mount and on genuine external modelValue changes.
function seed(incoming: IdentifierInput[]): void {
  rows.value = incoming.map((item) => ({ type: item.type, value: item.value, _uid: uid() }));
}

// sameAsRows reports whether `incoming` deep-equals what we'd currently emit —
// i.e. it is the echo of our own change rather than an external edit.
function sameAsRows(incoming: IdentifierInput[]): boolean {
  const current = rows.value;
  if (incoming.length !== current.length) {
    return false;
  }
  return incoming.every(
    (item, i) => item.type === current[i].type && item.value === current[i].value,
  );
}

// Internal row list. Seeded from modelValue on mount and on external changes.
const rows = ref<Row[]>([]);
seed(props.modelValue);

// Re-seed only on external modelValue changes. The echo of our own emit
// deep-equals the current rows, so we skip it and keep the in-place rows (and
// thus stable uids / DOM nodes).
watch(
  () => props.modelValue,
  (incoming) => {
    if (!sameAsRows(incoming)) {
      seed(incoming);
    }
  },
  { deep: false },
);

// --- Known types -----------------------------------------------------------

const KNOWN_TYPES = [
  { value: 'isbn', label: 'ISBN' },
  { value: 'amazon', label: 'Amazon' },
  { value: 'goodreads', label: 'Goodreads' },
  { value: 'google', label: 'Google' },
];
const knownValues = KNOWN_TYPES.map((t) => t.value);

// The first known type not already used, so a new row defaults to something
// useful (identifiers are one-per-type) and falls back to ISBN when all are taken.
// Reads `rows` (the source of truth) so it is correct even before the echoed
// modelValue arrives.
function nextDefaultType(): string {
  const used = new Set(rows.value.map((r) => r.type));
  return knownValues.find((t) => !used.has(t)) ?? 'isbn';
}

// Options for a row: the known set, plus the row's own type when it is something
// uncommon (e.g. doi) so editing it does not force a switch to a known type.
function typeOptions(rowType: string): { value: string; label: string }[] {
  if (rowType && !knownValues.includes(rowType)) {
    return [...KNOWN_TYPES, { value: rowType, label: rowType.toUpperCase() }];
  }

  return KNOWN_TYPES;
}

// --- Mutations -------------------------------------------------------------
// Each handler mutates `rows` in place (preserving uids where identity is kept)
// then emits the stripped list. We never mutate the `modelValue` prop itself.

function patch(index: number, change: Partial<IdentifierInput>): void {
  // In place: same _uid, so the row's DOM node (and focus/IME) survives the edit.
  rows.value[index] = { ...rows.value[index], ...change };
  emit('update:modelValue', strip(rows.value));
}

function remove(index: number): void {
  // Splice keeps every surviving row's _uid, so their DOM nodes are reused.
  rows.value.splice(index, 1);
  emit('update:modelValue', strip(rows.value));
}

function add(): void {
  rows.value.push({ type: nextDefaultType(), value: '', _uid: uid() });
  emit('update:modelValue', strip(rows.value));
}
</script>

<template>
  <div class="flex flex-col gap-2">
    <div v-for="(row, i) in rows" :key="row._uid" class="flex items-center gap-2">
      <select
        :value="row.type"
        class="select w-36 shrink-0"
        :aria-label="`Identifier ${i + 1} type`"
        @change="patch(i, { type: ($event.target as HTMLSelectElement).value })"
      >
        <option v-for="opt in typeOptions(row.type)" :key="opt.value" :value="opt.value">
          {{ opt.label }}
        </option>
      </select>
      <input
        :value="row.value"
        type="text"
        class="input w-full flex-1"
        :aria-label="`Identifier ${i + 1} value`"
        @input="patch(i, { value: ($event.target as HTMLInputElement).value })"
      />
      <button
        type="button"
        class="btn btn-ghost btn-square"
        :aria-label="`Remove identifier ${i + 1}`"
        @click="remove(i)"
      >
        <i class="pi pi-trash" />
      </button>
    </div>

    <div>
      <button type="button" class="btn btn-ghost btn-sm gap-2" @click="add">
        <i class="pi pi-plus" />
        Add identifier
      </button>
    </div>
  </div>
</template>

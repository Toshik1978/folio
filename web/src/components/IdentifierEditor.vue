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
// Each row carries a uid so Vue keys by row identity, not position. Removing a
// middle row therefore reuses the surviving DOM nodes rather than destroying and
// recreating them, which preserves focus and IME composition state.

let nextUid = 0;
function uid(): number {
  return ++nextUid;
}

interface Row extends IdentifierInput {
  _uid: number;
}

// Internal row list. Reconciled from modelValue on every external change.
const rows = ref<Row[]>([]);

// Reconcile modelValue -> rows: assign a stable uid to each incoming item by
// matching on (type, value). Items already present keep their uid; genuinely
// new items get a fresh one. This preserves identity for items that haven't
// changed while correctly handling parent-driven additions.
function reconcile(incoming: IdentifierInput[]): void {
  const prev = rows.value;
  const used = new Set<number>();

  rows.value = incoming.map((item) => {
    // Try to find an existing row with the same type+value that hasn't been
    // matched yet (handles duplicates correctly).
    const match = prev.find(
      (r) => !used.has(r._uid) && r.type === item.type && r.value === item.value,
    );
    if (match) {
      used.add(match._uid);
      return match;
    }
    return { ...item, _uid: uid() };
  });
}

// Seed on mount, then track parent changes.
reconcile(props.modelValue);
watch(() => props.modelValue, reconcile, { deep: false });

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
function nextDefaultType(): string {
  const used = new Set(props.modelValue.map((r) => r.type));
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

// --- Mutations (props-down / events-up; never mutate the prop) -------------

function strip(rs: Row[]): IdentifierInput[] {
  return rs.map(({ _uid: _uid, ...item }) => item);
}

function patch(index: number, change: Partial<IdentifierInput>): void {
  const updated = rows.value.map((row, i) => (i === index ? { ...row, ...change } : row));
  emit('update:modelValue', strip(updated));
}

function remove(index: number): void {
  const updated = rows.value.filter((_, i) => i !== index);
  emit('update:modelValue', strip(updated));
}

function add(): void {
  emit('update:modelValue', [...strip(rows.value), { type: nextDefaultType(), value: '' }]);
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

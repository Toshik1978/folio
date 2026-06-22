<script setup lang="ts">
import { computed } from 'vue';

import type { IdentifierInput } from '@/types';

// IdentifierEditor edits a book's identifiers as add/change/remove rows. The
// parent sends the resulting list as the authoritative set, so removing every row
// deletes all identifiers on save. Type is a select (the four with known link-out
// URLs), but an existing uncommon type is preserved as an extra option so a save
// never silently drops it.
const props = defineProps<{ modelValue: IdentifierInput[] }>();
const emit = defineEmits<{ 'update:modelValue': [value: IdentifierInput[]] }>();

const KNOWN_TYPES = [
  { value: 'isbn', label: 'ISBN' },
  { value: 'amazon', label: 'Amazon' },
  { value: 'goodreads', label: 'Goodreads' },
  { value: 'google', label: 'Google' },
];
const knownValues = KNOWN_TYPES.map((t) => t.value);

// The first known type not already used, so a new row defaults to something
// useful (identifiers are one-per-type) and falls back to ISBN when all are taken.
const nextDefaultType = computed(() => {
  const used = new Set(props.modelValue.map((r) => r.type));

  return knownValues.find((t) => !used.has(t)) ?? 'isbn';
});

// Options for a row: the known set, plus the row's own type when it is something
// uncommon (e.g. doi) so editing it does not force a switch to a known type.
function typeOptions(rowType: string): { value: string; label: string }[] {
  if (rowType && !knownValues.includes(rowType)) {
    return [...KNOWN_TYPES, { value: rowType, label: rowType.toUpperCase() }];
  }

  return KNOWN_TYPES;
}

// All mutations emit a fresh array (props-down / events-up; never mutate the prop).
function patch(index: number, change: Partial<IdentifierInput>): void {
  emit(
    'update:modelValue',
    props.modelValue.map((row, i) => (i === index ? { ...row, ...change } : row)),
  );
}

function remove(index: number): void {
  emit(
    'update:modelValue',
    props.modelValue.filter((_, i) => i !== index),
  );
}

function add(): void {
  emit('update:modelValue', [...props.modelValue, { type: nextDefaultType.value, value: '' }]);
}
</script>

<template>
  <div class="flex flex-col gap-2">
    <div v-for="(row, i) in modelValue" :key="i" class="flex items-center gap-2">
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

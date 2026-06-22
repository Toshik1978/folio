<script setup lang="ts">
import { computed, ref } from 'vue';

// TagSelect is a select-only, multi-value tag picker: chosen tags render as
// removable chips and new ones can only be *added from the provided options*
// (typing filters that list, it never creates a free-text tag). Existing values
// outside the option list still render as chips and stay removable, so editing a
// book never silently drops a tag the taxonomy no longer offers.
const props = defineProps<{
  modelValue: string[];
  options: string[];
  placeholder?: string;
  inputId?: string;
}>();
const emit = defineEmits<{ 'update:modelValue': [value: string[]] }>();

const query = ref('');
const focused = ref(false);
const inputEl = ref<HTMLInputElement | null>(null);

// Addable options: those not already chosen, narrowed by the (case-insensitive)
// query. This is the sole source of new values, which is what keeps the control
// select-only.
const available = computed(() => {
  const chosen = new Set(props.modelValue.map((t) => t.toLowerCase()));
  const q = query.value.trim().toLowerCase();

  return props.options.filter(
    (o) => !chosen.has(o.toLowerCase()) && (q === '' || o.toLowerCase().includes(q)),
  );
});

const showDropdown = computed(() => focused.value && available.value.length > 0);

function add(tag: string): void {
  if (props.modelValue.some((t) => t.toLowerCase() === tag.toLowerCase())) return;
  emit('update:modelValue', [...props.modelValue, tag]);
  query.value = '';
  inputEl.value?.focus();
}

function remove(tag: string): void {
  emit(
    'update:modelValue',
    props.modelValue.filter((t) => t !== tag),
  );
}

// Backspace on an empty query removes the last chip — the conventional token-input
// shortcut so a misclick is quick to undo from the keyboard.
function onBackspace(): void {
  if (query.value === '' && props.modelValue.length > 0) {
    remove(props.modelValue[props.modelValue.length - 1]);
  }
}
</script>

<template>
  <div class="relative">
    <!--
      The .input class wraps a bare <input> (daisyUI v5's container pattern), so
      the border + focus-within ring match every other field; h-auto/flex-wrap let
      the chips grow the box onto multiple rows.
    -->
    <div
      class="input h-auto min-h-12 w-full flex-wrap items-center gap-1.5 py-2"
      @click="inputEl?.focus()"
    >
      <span v-for="tag in modelValue" :key="tag" class="badge badge-primary badge-sm gap-1">
        {{ tag }}
        <button
          type="button"
          class="opacity-70 hover:opacity-100"
          :aria-label="`Remove ${tag}`"
          @click.stop="remove(tag)"
        >
          ✕
        </button>
      </span>
      <input
        :id="inputId"
        ref="inputEl"
        v-model="query"
        data-testid="edit-tags"
        type="text"
        class="min-w-24 flex-1 border-none bg-transparent p-0 text-sm focus:outline-none"
        :placeholder="modelValue.length ? '' : (placeholder ?? 'Select tags…')"
        @focus="focused = true"
        @blur="focused = false"
        @keydown.backspace="onBackspace"
        @keydown.enter.prevent="available[0] && add(available[0])"
      />
    </div>

    <ul
      v-if="showDropdown"
      class="menu bg-base-100 rounded-box border-base-300 absolute z-10 mt-1 max-h-48 w-full flex-nowrap overflow-y-auto border p-1 shadow-lg"
    >
      <li v-for="opt in available" :key="opt">
        <button type="button" @mousedown.prevent="add(opt)">{{ opt }}</button>
      </li>
    </ul>
  </div>
</template>

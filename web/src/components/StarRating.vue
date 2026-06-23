<script setup lang="ts">
const props = defineProps<{ rating: number }>();
</script>
<template>
  <span :aria-label="`Rating: ${props.rating} of 5`" class="flex gap-0.5">
    <i v-for="i in 5" :key="i" :class="i <= props.rating ? 'pi pi-star-fill' : 'pi pi-star'" />
  </span>
</template>

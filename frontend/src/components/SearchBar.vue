<script setup lang="ts">
import { ref } from "vue";

const emit = defineEmits<{ submit: [query: string] }>();
const query = ref("");

function onSubmit() {
  const trimmed = query.value.trim();
  if (trimmed) emit("submit", trimmed);
}
</script>

<template>
  <form @submit.prevent="onSubmit" class="w-full">
    <input
      v-model="query"
      type="search"
      placeholder="Search for a song"
      autocomplete="off"
      class="w-full px-6 py-4 text-lg rounded-full bg-[var(--color-surface)] border border-[var(--color-border)] focus:border-[var(--color-accent)] focus:ring-2 focus:ring-[var(--color-accent)]/40 focus:outline-none text-[var(--color-text)] placeholder-[var(--color-text-faint)] transition-all"
    />
  </form>
</template>

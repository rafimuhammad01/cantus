<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from "vue";
import { useSearchStore } from "@/stores/search";
import SearchBar from "@/components/SearchBar.vue";
import SongRow from "@/components/SongRow.vue";

const search = useSearchStore();
const sentinel = ref<HTMLDivElement | null>(null);
let observer: IntersectionObserver | null = null;

const showHero = computed(
  () => search.query === "" && search.results.length === 0 && !search.loading,
);

function onSearchSubmit(q: string) {
  search.runSearch(q);
}

// Set up the IntersectionObserver once we have a sentinel.
// Watch sentinel.value because v-if hides it until we have results.
watch(sentinel, (el) => {
  if (observer) {
    observer.disconnect();
    observer = null;
  }
  if (!el) return;
  observer = new IntersectionObserver(
    (entries) => {
      for (const entry of entries) {
        if (entry.isIntersecting) {
          search.loadMore();
        }
      }
    },
    { rootMargin: "200px" },
  );
  observer.observe(el);
});

onUnmounted(() => {
  observer?.disconnect();
});
</script>

<template>
  <div class="min-h-screen flex flex-col">
    <!-- Hero state: centered logo + searchbar, no results yet -->
    <div
      v-if="showHero"
      class="flex-1 flex flex-col items-center justify-center px-4"
    >
      <h1 class="text-6xl font-bold text-white mb-12 tracking-tight">cantus</h1>
      <div class="w-full max-w-xl">
        <SearchBar @submit="onSearchSubmit" />
      </div>
    </div>

    <!-- Results state: searchbar at top, results below -->
    <div v-else class="max-w-2xl w-full mx-auto px-4 py-8">
      <div class="mb-8">
        <SearchBar @submit="onSearchSubmit" />
      </div>

      <!-- Loading skeleton on first fetch -->
      <div
        v-if="search.loading && search.results.length === 0"
        class="space-y-3"
      >
        <div
          v-for="i in 5"
          :key="i"
          class="h-20 rounded-xl bg-[#1a1822] border border-[#2a2730] animate-pulse"
        />
      </div>

      <!-- Error state -->
      <div
        v-if="search.error"
        class="p-4 rounded-xl bg-red-900/30 border border-red-800 text-red-200"
      >
        {{ search.error }}
      </div>

      <!-- Results list -->
      <div v-if="search.results.length > 0" class="space-y-3">
        <SongRow v-for="r in search.results" :key="r.video_id" :result="r" />
      </div>

      <!-- Empty state after a search that returned nothing -->
      <div
        v-if="
          !search.loading &&
          !search.error &&
          search.query !== '' &&
          search.results.length === 0
        "
        class="text-gray-500 text-center py-12"
      >
        No songs found.
      </div>

      <!-- Infinite scroll sentinel -->
      <div
        v-if="search.hasMore"
        ref="sentinel"
        class="py-8 flex justify-center"
      >
        <div v-if="search.loading" class="text-gray-500 text-sm">
          Loading...
        </div>
      </div>

      <!-- End-of-list indicator -->
      <div
        v-else-if="search.results.length > 0 && !search.loading"
        class="text-gray-600 text-xs text-center py-8"
      >
        End of results
      </div>
    </div>
  </div>
</template>

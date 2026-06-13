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

function goHome() {
  search.runSearch("");
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
  <div class="min-h-[100dvh] flex flex-col">
    <!-- Hero state: centered logo + searchbar, no results yet -->
    <div
      v-if="showHero"
      class="flex-1 flex flex-col items-center justify-center px-4"
    >
      <h1
        class="font-serif italic text-[64px] sm:text-[88px] md:text-[96px] leading-none text-[var(--color-text)] mb-2 tracking-tight"
      >
        cantus
      </h1>
      <p class="text-[14px] text-[var(--color-text-muted)] mb-12">
        Sing any song. In your key.
      </p>
      <div class="w-full max-w-xl">
        <SearchBar @submit="onSearchSubmit" />
      </div>
    </div>

    <!-- Results state: searchbar at top, results below -->
    <div v-else class="max-w-2xl w-full mx-auto px-4 py-8">
      <button
        @click="goHome"
        class="font-serif italic text-[28px] leading-none text-[var(--color-text)] hover:text-[var(--color-accent)] tracking-tight mb-6 transition-colors"
        aria-label="Back to home"
      >
        cantus
      </button>
      <div class="mb-8">
        <SearchBar @submit="onSearchSubmit" />
      </div>

      <!-- Loading skeleton on first fetch -->
      <div
        v-if="search.loading && search.results.length === 0"
        class="divide-y divide-[var(--color-border)]"
      >
        <div v-for="i in 5" :key="i" class="flex items-center gap-4 px-2 py-3">
          <div
            class="w-14 h-14 rounded-lg bg-[var(--color-surface-2)] animate-pulse shrink-0"
          />
          <div class="flex-1 space-y-2">
            <div
              class="h-4 w-2/3 rounded bg-[var(--color-surface-2)] animate-pulse"
            />
            <div
              class="h-3 w-1/2 rounded bg-[var(--color-surface-2)] animate-pulse"
            />
          </div>
        </div>
      </div>

      <!-- Error state -->
      <div
        v-if="search.error"
        class="p-4 rounded-xl bg-[var(--color-surface)] border border-[var(--color-danger)]/60 text-[var(--color-danger)]"
      >
        {{ search.error }}
      </div>

      <!-- Results list -->
      <div v-if="search.results.length > 0">
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
        Nothing matched. Try a different spelling.
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

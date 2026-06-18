import { defineStore } from "pinia";
import { ref } from "vue";
import { search as apiSearch, type SearchResult } from "@/services/api";

const PAGE_SIZE = 10;

export const useSearchStore = defineStore("search", () => {
  const query = ref("");
  const results = ref<SearchResult[]>([]);
  const hasMore = ref(false);
  const offset = ref(0);
  const loading = ref(false);
  const error = ref<string | null>(null);

  async function runSearch(q: string) {
    query.value = q;
    results.value = [];
    offset.value = 0;
    hasMore.value = false;
    error.value = null;
    if (!q.trim()) return;
    loading.value = true;
    try {
      const resp = await apiSearch(q, PAGE_SIZE, 0);
      results.value = resp.items;
      hasMore.value = resp.has_more;
      offset.value = resp.items.length;
    } catch (e) {
      error.value = String(e);
    } finally {
      loading.value = false;
    }
  }

  async function loadMore() {
    if (loading.value || !hasMore.value || !query.value.trim()) return;
    loading.value = true;
    try {
      const resp = await apiSearch(query.value, PAGE_SIZE, offset.value);
      results.value.push(...resp.items);
      hasMore.value = resp.has_more;
      offset.value += resp.items.length;
    } catch (e) {
      error.value = String(e);
    } finally {
      loading.value = false;
    }
  }

  return {
    query,
    results,
    hasMore,
    loading,
    error,
    runSearch,
    loadMore,
  };
});

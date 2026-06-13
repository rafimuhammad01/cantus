<script setup lang="ts">
import { useRouter } from "vue-router";
import { usePlayerStore } from "@/stores/player";
import type { SearchResult } from "@/services/api";

const props = defineProps<{ result: SearchResult }>();
const router = useRouter();
const player = usePlayerStore();

function formatDuration(sec: number): string {
  const m = Math.floor(sec / 60);
  const s = sec % 60;
  return `${m}:${s.toString().padStart(2, "0")}`;
}

function onClick() {
  player.selectSong(props.result);
  router.push(`/preview/${props.result.video_id}`);
}
</script>

<template>
  <button
    @click="onClick"
    class="group w-full flex items-center gap-4 px-2 py-3 border-b border-[var(--color-border)] hover:bg-[var(--color-surface)] transition-colors text-left"
  >
    <img
      :src="result.thumbnail_url"
      :alt="result.title"
      class="w-14 h-14 rounded-lg object-cover shrink-0"
      loading="lazy"
    />
    <div class="min-w-0 flex-1">
      <div
        class="text-[15px] font-medium text-[var(--color-text)] truncate group-hover:underline decoration-[var(--color-accent)] decoration-1 underline-offset-4"
      >
        {{ result.title }}
      </div>
      <div class="text-[13px] text-[var(--color-text-muted)] truncate mt-0.5">
        {{ result.artist }}
        <template v-if="result.album"> · {{ result.album }}</template>
      </div>
    </div>
    <div class="text-[13px] text-[var(--color-text-faint)] shrink-0 tnum">
      {{ formatDuration(result.duration_sec) }}
    </div>
  </button>
</template>

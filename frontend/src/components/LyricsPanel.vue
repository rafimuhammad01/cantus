<script setup lang="ts">
import { ref, watch, nextTick } from "vue";
import type { Cue } from "@/services/api";

const props = defineProps<{
  lines: Cue[];
  activeIndex: number;
  plain: string;
  available: boolean;
  loading?: boolean;
}>();

const lineRefs = ref<HTMLElement[]>([]);

// Scroll the active line into view whenever it changes.
watch(
  () => props.activeIndex,
  async (idx) => {
    if (idx < 0) return;
    await nextTick();
    const el = lineRefs.value[idx];
    if (el) {
      el.scrollIntoView({ behavior: "smooth", block: "center" });
    }
  },
);
</script>

<template>
  <!-- Loading -->
  <div
    v-if="loading"
    class="flex items-center justify-center h-full text-[var(--color-text-muted)] text-[13px]"
  >
    Loading lyrics…
  </div>

  <!-- Not available -->
  <div
    v-else-if="!available"
    class="flex items-center justify-center h-full text-[var(--color-text-muted)] text-[13px]"
  >
    Lyrics not available
  </div>

  <!-- Available but no synced cues: plain text fallback -->
  <div v-else-if="lines.length === 0" class="h-full overflow-y-auto px-4 py-6">
    <pre
      class="whitespace-pre-wrap font-sans text-[14px] leading-relaxed text-[var(--color-text-muted)]"
      >{{ plain }}</pre
    >
  </div>

  <!-- Synced karaoke display -->
  <div
    v-else
    class="h-full overflow-y-auto px-4 py-6 flex flex-col items-center gap-1 lyrics-fade"
  >
    <div
      v-for="(cue, i) in lines"
      :key="i"
      :ref="
        (el) => {
          if (el) lineRefs[i] = el as HTMLElement;
        }
      "
      class="text-center transition-all duration-300 w-full max-w-xl"
      :class="{
        // Active line
        'text-[var(--color-accent)] text-[18px] font-semibold opacity-100 scale-105':
          i === activeIndex,
        // One line away
        'text-[var(--color-text)] text-[15px] opacity-50':
          Math.abs(i - activeIndex) === 1,
        // Two lines away
        'text-[var(--color-text-muted)] text-[14px] opacity-25':
          Math.abs(i - activeIndex) >= 2,
      }"
    >
      <!-- Empty-text cue: preserve gap rhythm -->
      <span v-if="cue.text === ''" class="inline-block h-4 w-1">&nbsp;</span>
      <span v-else>{{ cue.text }}</span>
    </div>
  </div>
</template>

<style scoped>
.lyrics-fade {
  -webkit-mask-image: linear-gradient(
    to bottom,
    transparent 0,
    #000 24px,
    #000 calc(100% - 24px),
    transparent 100%
  );
  mask-image: linear-gradient(
    to bottom,
    transparent 0,
    #000 24px,
    #000 calc(100% - 24px),
    transparent 100%
  );
}
</style>

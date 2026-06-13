<script setup lang="ts">
import { ref, watch, onUnmounted, computed } from "vue";

const props = defineProps<{
  src: string;
  hidePlayButton?: boolean;
  variant?: "default" | "bottom-bar";
}>();
const audio = ref<HTMLAudioElement | null>(null);
const isPlaying = ref(false);
const currentTime = ref(0);
const duration = ref(0);

function togglePlay() {
  const el = audio.value;
  if (!el) return;
  if (el.paused) {
    el.play().catch(() => {});
  } else {
    el.pause();
  }
}

function onTimeUpdate() {
  if (audio.value) currentTime.value = audio.value.currentTime;
}
function onLoadedMeta() {
  if (audio.value) duration.value = audio.value.duration || 0;
}
function onPlay() {
  isPlaying.value = true;
}
function onPause() {
  isPlaying.value = false;
}
function onSeek(e: Event) {
  const target = e.target as HTMLInputElement;
  if (audio.value) audio.value.currentTime = Number(target.value);
}

function fmt(s: number): string {
  if (!isFinite(s)) return "0:00";
  const m = Math.floor(s / 60);
  const sec = Math.floor(s % 60);
  return `${m}:${sec.toString().padStart(2, "0")}`;
}

watch(
  () => props.src,
  (next) => {
    isPlaying.value = false;
    currentTime.value = 0;
    duration.value = 0;
    const el = audio.value;
    if (el && next) {
      el.load();
    }
  },
);

onUnmounted(() => {
  audio.value?.pause();
});

const isBottomBar = computed(() => props.variant === "bottom-bar");

defineExpose({ audio });
</script>

<template>
  <div
    :class="
      isBottomBar
        ? 'fixed bottom-0 inset-x-0 z-20 bg-[var(--color-surface)]/95 backdrop-blur border-t border-[var(--color-border)] px-4 py-3 pb-[max(0.75rem,env(safe-area-inset-bottom))]'
        : 'w-full'
    "
  >
    <audio
      ref="audio"
      :src="src"
      @timeupdate="onTimeUpdate"
      @loadedmetadata="onLoadedMeta"
      @play="onPlay"
      @pause="onPause"
      preload="metadata"
    />
    <div
      :class="
        isBottomBar
          ? 'max-w-4xl mx-auto flex items-center gap-4'
          : 'flex items-center gap-3'
      "
    >
      <button
        v-if="!hidePlayButton"
        @click="togglePlay"
        class="w-12 h-12 rounded-full bg-[var(--color-accent)] hover:bg-[var(--color-accent-hover)] flex items-center justify-center text-[#0a0a0b] text-xl shrink-0 transition-colors"
        :aria-label="isPlaying ? 'Pause' : 'Play'"
      >
        <span v-if="isPlaying">⏸</span>
        <span v-else>▶</span>
      </button>
      <div class="flex-1 flex items-center gap-3">
        <span
          class="text-[12px] tnum text-[var(--color-text-muted)] w-12 text-right"
        >
          {{ fmt(currentTime) }}
        </span>
        <input
          type="range"
          :max="duration || 0"
          :value="currentTime"
          step="0.1"
          @input="onSeek"
          class="flex-1 accent-[var(--color-accent)]"
          aria-label="Seek"
        />
        <span class="text-[12px] tnum text-[var(--color-text-muted)] w-12">
          {{ fmt(duration) }}
        </span>
      </div>
    </div>
  </div>
</template>

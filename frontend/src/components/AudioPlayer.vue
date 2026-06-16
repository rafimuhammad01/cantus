<script setup lang="ts">
import { ref, watch, onUnmounted, computed, useSlots } from "vue";

const props = defineProps<{
  src: string;
  hidePlayButton?: boolean;
  variant?: "default" | "bottom-bar";
  thumbnailUrl?: string;
  title?: string;
  subtitle?: string;
  badge?: string;
  // When provided, parent controls play state — icon reflects this value instead
  // of internal isPlaying. Click emits 'toggle' and skips internal togglePlay().
  playing?: boolean;
  // Greys out the play button (e.g. before stems/job are ready).
  disabled?: boolean;
}>();
const emit = defineEmits<{
  toggle: [];
}>();
const slots = useSlots();
const audio = ref<HTMLAudioElement | null>(null);
const hasCta = computed(() => !!slots.cta);
const isPlaying = ref(false);
const currentTime = ref(0);
const duration = ref(0);

// Resolved play indicator: prefer parent-controlled `playing` prop when present.
const effectivePlaying = computed(() =>
  props.playing !== undefined ? props.playing : isPlaying.value,
);

function togglePlay() {
  const el = audio.value;
  if (!el) return;
  if (el.paused) {
    el.play().catch(() => {});
  } else {
    el.pause();
  }
}

function onPlayButtonClick() {
  emit("toggle");
  // When parent owns play state (playing prop is bound), don't also run the
  // internal toggle — parent's handler drives the audio element directly.
  if (props.playing === undefined) {
    togglePlay();
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
        ? 'fixed bottom-0 inset-x-0 z-20 bg-[var(--color-surface)]/95 backdrop-blur border-t border-[var(--color-border)] px-4 pt-3 pb-[max(0.75rem,env(safe-area-inset-bottom))]'
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
          ? 'flex flex-wrap items-center gap-3 sm:gap-6 max-w-6xl mx-auto'
          : 'flex items-center gap-3'
      "
    >
      <!-- Play button -->
      <button
        v-if="!hidePlayButton"
        @click="onPlayButtonClick"
        :disabled="disabled"
        :class="[
          isBottomBar ? 'w-14 h-14 text-2xl' : 'w-12 h-12 text-xl',
          'rounded-full flex items-center justify-center shrink-0 transition-colors',
          disabled
            ? 'bg-[var(--color-surface-2)] text-[var(--color-text-faint)] cursor-not-allowed'
            : 'bg-[var(--color-accent)] hover:bg-[var(--color-accent-hover)] text-[#0a0a0b]',
        ]"
        :aria-label="effectivePlaying ? 'Pause' : 'Play'"
      >
        <span v-if="effectivePlaying">⏸</span>
        <span v-else>▶</span>
      </button>

      <!-- Middle: meta on top, scrubber below (bottom-bar) or just scrubber -->
      <div
        :class="
          isBottomBar
            ? 'flex-1 min-w-0 flex flex-col gap-1'
            : 'flex-1 flex items-center gap-3'
        "
      >
        <!-- Song meta row (bottom-bar only) -->
        <div
          v-if="isBottomBar && (thumbnailUrl || title)"
          class="flex items-center gap-3 min-w-0"
        >
          <img
            v-if="thumbnailUrl"
            :src="thumbnailUrl"
            :alt="title"
            class="w-12 h-12 rounded-md object-cover shrink-0"
          />
          <div class="min-w-0 flex-1">
            <div class="flex items-center gap-2 min-w-0">
              <span
                class="text-[15px] font-medium leading-tight truncate text-[var(--color-text)]"
              >
                {{ title }}
              </span>
              <span
                v-if="badge"
                class="hidden sm:inline-block shrink-0 rounded-full bg-[var(--color-surface-2)] text-[var(--color-text-faint)] text-[11px] px-2 py-0.5 leading-none"
              >
                {{ badge }}
              </span>
            </div>
            <div
              v-if="subtitle"
              class="text-[12px] text-[var(--color-text-muted)] truncate"
            >
              {{ subtitle }}
            </div>
          </div>
        </div>

        <!-- Scrubber: full-width track, time labels below at the edges -->
        <div v-if="isBottomBar" class="flex flex-col gap-0.5">
          <input
            type="range"
            :max="duration || 0"
            :value="currentTime"
            step="0.1"
            @input="onSeek"
            class="w-full accent-[var(--color-accent)]"
            aria-label="Seek"
          />
          <div
            class="flex justify-between text-[11px] tnum text-[var(--color-text-muted)] leading-none"
          >
            <span>{{ fmt(currentTime) }}</span>
            <span>{{ fmt(duration) }}</span>
          </div>
        </div>
        <!-- Default (non-bottom-bar) scrubber row -->
        <div v-else class="flex items-center gap-3">
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

      <!-- Right: CTA slot (bottom-bar only). Wraps to its own row on mobile. -->
      <div
        v-if="isBottomBar && hasCta"
        class="order-last w-full flex justify-center sm:order-none sm:w-auto sm:shrink-0 sm:block"
      >
        <slot name="cta" />
      </div>
    </div>
  </div>
</template>

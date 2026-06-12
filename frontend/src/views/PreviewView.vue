<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from "vue";
import { useRoute, useRouter } from "vue-router";
import { usePlayerStore } from "@/stores/player";
import { shortKey, transposeKey } from "@/utils/key";
import { midiToNoteName } from "@/utils/pitch";
import KeySelector from "@/components/KeySelector.vue";
import VocalOctaveSelector from "@/components/VocalOctaveSelector.vue";
import AudioPlayer from "@/components/AudioPlayer.vue";
import ProcessingStatus from "@/components/ProcessingStatus.vue";
import PitchDiagram from "@/components/PitchDiagram.vue";

const route = useRoute();
const router = useRouter();
const player = usePlayerStore();
const audioPlayerRef = ref<InstanceType<typeof AudioPlayer> | null>(null);

// Short debounce — long enough to collapse mash-clicks, short enough that the
// transition feels snappy. We pause audio + show "Shifting…" immediately on
// click, so 250ms doesn't read as lag.
const SHIFT_DEBOUNCE_MS = 250;

const routeVideoId = computed(() => {
  const v = route.params.videoId;
  return Array.isArray(v) ? v[0] : v;
});

const noContext = computed(
  () => !player.videoId || player.videoId !== routeVideoId.value,
);

const shiftPending = ref(false);
// Pending semitones — updates instantly on click for snappy UI feedback.
// The actual /api/preview-shift fires only after a 600ms debounce.
const pendingSemitones = ref(player.semitones);
let shiftTimer: ReturnType<typeof setTimeout> | null = null;

function fmtDuration(sec: number): string {
  const m = Math.floor(sec / 60);
  const s = sec % 60;
  return `${m}:${s.toString().padStart(2, "0")}`;
}

function onSemitonesChange(n: number) {
  pendingSemitones.value = n;
  if (n === player.semitones) {
    shiftPending.value = false;
    if (shiftTimer !== null) {
      clearTimeout(shiftTimer);
      shiftTimer = null;
    }
    return;
  }
  audioPlayerRef.value?.audio?.pause();
  shiftPending.value = true;
  if (shiftTimer !== null) clearTimeout(shiftTimer);
  shiftTimer = setTimeout(async () => {
    shiftTimer = null;
    try {
      await player.setSemitones(n);
    } finally {
      shiftPending.value = false;
    }
  }, SHIFT_DEBOUNCE_MS);
}

async function onGenerateClick() {
  // Flush any pending debounce — user intent is clear once they hit Generate.
  if (shiftTimer !== null) {
    clearTimeout(shiftTimer);
    shiftTimer = null;
  }
  // Use the displayed pendingSemitones value so the generated key matches what
  // the user just selected, even if the preview-shift hadn't committed yet.
  player.semitones = pendingSemitones.value;

  try {
    await player.startGenerate();
    router.push(`/play/${player.videoId}/${player.semitones}`);
  } catch (e) {
    alert(`Could not start generation: ${e}`);
  }
}

onMounted(() => {
  if (noContext.value) {
    // No song selected (refresh, deep link, or expired session) — bounce to search.
    router.replace("/");
    return;
  }
  pendingSemitones.value = player.semitones;
  // Fire loadPreviewStems without awaiting — UI reacts to reactive flags.
  // loadPreviewKey() is intentionally removed: previewMelody provides the key
  // once stems are ready, and keeping an extra CREPE-less key call would
  // require two separate sources of truth for the same value.
  void player.loadPreviewStems();
});

onUnmounted(() => {
  if (shiftTimer !== null) clearTimeout(shiftTimer);
});
</script>

<template>
  <div class="min-h-screen pb-32">
    <template v-if="!noContext">
      <!-- Backdrop + artwork header -->
      <div class="relative">
        <div
          v-if="player.song?.thumbnail_url"
          class="absolute inset-0 overflow-hidden -z-10"
        >
          <img
            :src="player.song.thumbnail_url"
            alt=""
            class="w-full h-full object-cover scale-110"
            style="filter: blur(40px) brightness(0.35)"
          />
          <div
            class="absolute inset-0 bg-gradient-to-b from-[var(--color-bg)]/40 via-[var(--color-bg)]/70 to-[var(--color-bg)]"
          />
        </div>
        <div class="max-w-3xl mx-auto px-4 pt-6 pb-12">
          <button
            @click="router.push('/')"
            class="text-[13px] text-[var(--color-text-muted)] hover:text-[var(--color-text)] transition-colors"
          >
            ← Back to search
          </button>
          <div
            class="mt-8 flex flex-col sm:flex-row items-center sm:items-end gap-6"
          >
            <img
              v-if="player.song?.thumbnail_url"
              :src="player.song.thumbnail_url"
              :alt="player.song.title"
              class="w-[240px] h-[240px] sm:w-[280px] sm:h-[280px] rounded-xl object-cover shrink-0"
              style="box-shadow: 0 16px 48px rgba(0, 0, 0, 0.5)"
            />
            <div class="text-center sm:text-left min-w-0 flex-1">
              <h1
                class="font-serif text-[36px] sm:text-[44px] leading-tight text-[var(--color-text)] tracking-tight"
              >
                {{ player.song?.title }}
              </h1>
              <div class="mt-2 text-[14px] text-[var(--color-text-muted)]">
                <span>by {{ player.song?.artist }}</span>
                <template v-if="player.song?.album">
                  · {{ player.song.album }}</template
                >
                <template v-if="player.song?.duration_sec">
                  · {{ fmtDuration(player.song.duration_sec) }}
                </template>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- Transpose (centered, the focus of this screen) -->
      <div class="max-w-3xl mx-auto px-4">
        <div class="flex flex-col items-center gap-3">
          <KeySelector
            :semitones="pendingSemitones"
            :disabled="!player.previewStemsReady"
            :original-key="
              player.previewOriginalKey
                ? shortKey(player.previewOriginalKey)
                : undefined
            "
            :transposed-key="
              player.previewOriginalKey
                ? shortKey(
                    transposeKey(player.previewOriginalKey, pendingSemitones),
                  )
                : undefined
            "
            @change="onSemitonesChange"
          />
          <Transition name="fade">
            <p
              v-if="pendingSemitones === 0 && player.previewStemsReady"
              class="text-[12px] text-[var(--color-text-faint)]"
            >
              Try lowering if the song feels too high to sing.
            </p>
          </Transition>
        </div>

        <!-- Player + pitch card -->
        <div class="mt-8 rounded-2xl bg-[var(--color-surface)] p-4">
          <!-- Loading: preview stems -->
          <div v-if="player.previewStemsLoading" class="py-8">
            <ProcessingStatus
              status="separating"
              message="Getting your accompaniment ready…"
            />
          </div>

          <!-- Error -->
          <div
            v-else-if="player.previewStemsError"
            class="p-4 rounded-xl bg-[var(--color-surface-2)] border border-[var(--color-danger)]/60 text-[var(--color-danger)]"
          >
            <p class="mb-3">{{ player.previewStemsError }}</p>
            <button
              @click="() => void player.loadPreviewStems()"
              class="px-4 py-2 rounded-full bg-[var(--color-accent)] hover:bg-[var(--color-accent-hover)] text-[#0a0a0b] text-sm transition-colors"
            >
              Retry
            </button>
          </div>

          <!-- Audio + pitch -->
          <div v-else class="relative">
            <div
              v-if="shiftPending"
              class="absolute inset-0 rounded-2xl bg-[var(--color-surface)]/85 backdrop-blur-sm flex flex-col items-center justify-center gap-2 z-10"
            >
              <svg class="animate-spin h-6 w-6" viewBox="0 0 24 24" fill="none">
                <circle
                  cx="12"
                  cy="12"
                  r="10"
                  stroke="#24242a"
                  stroke-width="3"
                />
                <path
                  d="M12 2a10 10 0 0 1 10 10"
                  stroke="#e8a87c"
                  stroke-width="3"
                  stroke-linecap="round"
                />
              </svg>
              <span class="text-sm text-[var(--color-text-muted)]"
                >Tuning to your key…</span
              >
            </div>
            <AudioPlayer
              ref="audioPlayerRef"
              :src="player.audioSrc"
              :hide-play-button="true"
            />
            <PitchDiagram
              v-if="
                player.previewStemsReady &&
                player.previewMelody &&
                audioPlayerRef?.audio
              "
              :key="`${player.semitones}-${player.vocalOctaveShift}`"
              :audio-el="audioPlayerRef.audio!"
              :melody="player.previewMelody"
              :vocal-octave-shift="player.vocalOctaveShift"
              class="mt-4"
            />
            <div class="mt-4 flex justify-center">
              <VocalOctaveSelector
                :current="player.vocalOctaveShift"
                :disabled="!player.previewStemsReady"
                :range="
                  player.vocalRange
                    ? `${midiToNoteName(player.vocalRange.minMidi)} – ${midiToNoteName(player.vocalRange.maxMidi)}`
                    : undefined
                "
                @change="player.setVocalOctaveShift"
              />
            </div>
          </div>
        </div>

        <p class="mt-6 text-[12px] text-[var(--color-text-faint)] text-center">
          This is a 30-second preview.
        </p>
      </div>

      <!-- Sticky bottom CTA -->
      <div
        class="fixed bottom-0 inset-x-0 z-20 bg-[var(--color-bg)]/90 backdrop-blur border-t border-[var(--color-border)] px-4 py-4"
      >
        <div class="max-w-md mx-auto flex flex-col items-center gap-1">
          <button
            @click="onGenerateClick"
            class="w-full px-6 py-3 rounded-full bg-[var(--color-accent)] hover:bg-[var(--color-accent-hover)] text-[#0a0a0b] font-medium transition-colors"
          >
            Practice full song →
          </button>
          <p class="text-[11px] text-[var(--color-text-faint)]">
            ~90 seconds to prepare
          </p>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.fade-enter-active,
.fade-leave-active {
  transition: opacity 200ms ease-out;
}
.fade-enter-from,
.fade-leave-to {
  opacity: 0;
}
</style>

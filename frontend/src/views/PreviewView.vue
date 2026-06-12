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

// Key display reads pendingSemitones so the user sees "A → D" immediately
// even while the audio is still catching up.
// Once previewMelody arrives we read its keys (backend already computed transposed_key);
// before that we fall back to transposeKey math on previewOriginalKey.
const originalShort = computed(() => shortKey(player.previewOriginalKey ?? ""));
const transposedShort = computed(() => {
  const base = player.previewOriginalKey ?? "";
  if (!base) return "";
  // When pending matches store, prefer the server-computed transposed_key
  if (pendingSemitones.value === player.semitones) {
    return shortKey(player.previewTransposedKey ?? "");
  }
  // During debounce, compute locally so the key display updates instantly
  return shortKey(transposeKey(base, pendingSemitones.value));
});
const showKeyLine = computed(() => player.previewOriginalKey !== null);
const keyDisplay = computed(() => {
  if (!originalShort.value) return "";
  if (pendingSemitones.value === 0) return `Key: ${originalShort.value}`;
  return `Key: ${originalShort.value} → ${transposedShort.value}`;
});

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
  <div class="max-w-3xl w-full mx-auto px-4 py-8 min-h-screen">
    <button
      @click="router.push('/')"
      class="mb-6 text-sm text-gray-400 hover:text-white transition-colors"
    >
      ← Back to search
    </button>

    <template v-if="!noContext">
      <!-- Song header -->
      <div class="mb-6">
        <h1 class="text-3xl font-bold text-white mb-1">
          {{ player.song?.title }}
        </h1>
        <div class="text-gray-400">
          <span>by {{ player.song?.artist }}</span>
          <template v-if="player.song?.album">
            · {{ player.song.album }}</template
          >
          <template v-if="player.song?.duration_sec">
            · {{ fmtDuration(player.song.duration_sec) }}
          </template>
        </div>
      </div>

      <!-- Transpose pill — disabled until stems are ready to prevent chipmunk shifts -->
      <div class="flex items-center gap-4 mb-3">
        <KeySelector
          :semitones="pendingSemitones"
          :disabled="!player.previewStemsReady"
          @change="onSemitonesChange"
        />
      </div>

      <!-- Key display -->
      <div v-if="showKeyLine" class="mb-3 text-gray-300">{{ keyDisplay }}</div>
      <div v-else class="mb-3 text-gray-600 text-sm">
        {{ player.previewStemsLoading ? "Preparing track…" : "&nbsp;" }}
      </div>

      <!-- Vocal octave selector — shifts only the displayed target line, not the audio -->
      <div class="flex items-center gap-4 mb-2">
        <VocalOctaveSelector
          :current="player.vocalOctaveShift"
          :disabled="!player.previewStemsReady"
          @change="player.setVocalOctaveShift"
        />
      </div>
      <div class="mb-6 text-gray-300 text-sm min-h-[1.25rem]">
        <template v-if="player.vocalRange !== null">
          Vocal: {{ midiToNoteName(player.vocalRange.minMidi) }} –
          {{ midiToNoteName(player.vocalRange.maxMidi) }}
        </template>
      </div>

      <!-- Preview-stems progress / error / audio player -->
      <div class="mb-6">
        <!-- Loading: show progress while Demucs + CREPE run on the clip -->
        <div
          v-if="player.previewStemsLoading"
          class="rounded-xl p-4 bg-[#1a1822] border border-[#2a2730]"
        >
          <ProcessingStatus
            status="separating"
            message="Preparing the instrumental track — about 14 seconds…"
          />
        </div>

        <!-- Error: show message and retry button -->
        <div
          v-else-if="player.previewStemsError"
          class="rounded-xl p-4 bg-red-900/30 border border-red-800 text-red-200"
        >
          <p class="mb-3">{{ player.previewStemsError }}</p>
          <button
            @click="() => void player.loadPreviewStems()"
            class="px-4 py-2 rounded-full bg-[#2ca02c] hover:bg-[#249027] text-white text-sm transition-colors"
          >
            Retry
          </button>
        </div>

        <!-- Audio player: always rendered once loading is done so audioPlayerRef is available -->
        <div
          v-else
          class="relative rounded-xl p-4 bg-[#1a1822] border border-[#2a2730]"
        >
          <div
            v-if="shiftPending"
            class="absolute inset-0 rounded-xl bg-[#1a1822]/85 backdrop-blur-sm flex flex-col items-center justify-center gap-2 z-10"
          >
            <svg class="animate-spin h-6 w-6" viewBox="0 0 24 24" fill="none">
              <circle
                cx="12"
                cy="12"
                r="10"
                stroke="#2a2730"
                stroke-width="3"
              />
              <path
                d="M12 2a10 10 0 0 1 10 10"
                stroke="#2ca02c"
                stroke-width="3"
                stroke-linecap="round"
              />
            </svg>
            <span class="text-sm text-gray-300">Shifting key…</span>
          </div>
          <AudioPlayer
            ref="audioPlayerRef"
            :src="player.audioSrc"
            :hide-play-button="true"
          />
        </div>
      </div>

      <!-- Pitch diagram: shown once stems are ready, melody is loaded, and audio element exists.
           :key on semitones forces a clean remount on transpose — the prototype's targetSeries +
           pitch store user-history are precomputed at setup, so without a remount the diagram
           would still show the previous-key target line and the user's stale singing trail. -->
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
      />

      <div class="text-xs text-gray-500 mb-4">
        This is a 30-second preview. Generate the full song to sing along.
      </div>

      <button
        @click="onGenerateClick"
        class="px-6 py-3 rounded-full bg-[#2ca02c] hover:bg-[#249027] text-white font-medium transition-colors"
      >
        Generate Full Song
      </button>
    </template>
  </div>
</template>

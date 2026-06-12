<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { usePlayerStore } from "@/stores/player";
import { useSSE } from "@/composables/useSSE";
import { statusURL, audioURL, getMelody } from "@/services/api";
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
const sse = useSSE();
const audioPlayerRef = ref<InstanceType<typeof AudioPlayer> | null>(null);

const NAV_DEBOUNCE_MS = 250;

const routeVideoId = computed(() => {
  const v = route.params.videoId;
  return Array.isArray(v) ? v[0] : v;
});
const routeSemitones = computed(() => {
  const v = route.params.semitones;
  const s = Array.isArray(v) ? v[0] : v;
  const n = Number.parseInt(s, 10);
  return Number.isNaN(n) ? 0 : n;
});

const noContext = computed(
  () => !player.videoId || player.videoId !== routeVideoId.value,
);

const isDone = computed(() => player.jobStatus === "done");

const fullAudioSrc = computed(() =>
  player.videoId && player.sig
    ? audioURL(player.videoId, player.sig, routeSemitones.value)
    : "",
);

// Pending semitones — updates instantly on click for snappy UI feedback.
// The actual URL navigation (and the generate it triggers) fires only after
// a 600ms debounce so a user who mashes − seven times doesn't queue 7 jobs.
const pendingSemitones = ref(routeSemitones.value);
let navTimer: ReturnType<typeof setTimeout> | null = null;

const originalShort = computed(() => shortKey(player.originalKey ?? ""));
// Use the pending value so the key display reads "A → D" the moment the user
// finishes clicking, not after the page navigates and reloads melody.
const transposedShort = computed(() => {
  const base = player.originalKey ?? "";
  if (!base) return "";
  // pendingSemitones already reflects the user's intent; transposedKey from
  // the store is for the route's current semitones which lags during debounce.
  if (pendingSemitones.value === routeSemitones.value) {
    return shortKey(player.transposedKey ?? "");
  }
  return shortKey(transposeKey(base, pendingSemitones.value));
});
const showKeyLine = computed(() => player.originalKey !== null);
const keyDisplay = computed(() => {
  if (!originalShort.value) return "";
  if (pendingSemitones.value === 0) return `Key: ${originalShort.value}`;
  return `Key: ${originalShort.value} → ${transposedShort.value}`;
});

async function loadDoneArtifacts() {
  if (!player.videoId || !player.sig) return;
  try {
    player.melody = await getMelody(
      player.videoId,
      player.sig,
      routeSemitones.value,
    );
  } catch (e) {
    player.applyStatus("error", `failed to load melody: ${e}`);
  }
}

function startSSE() {
  if (!player.jobId) return;
  sse.open(statusURL(player.jobId), (ev) => {
    player.applyStatus(ev.status, ev.message);
  });
}

// On semitone change via the pill, update pending value immediately and
// debounce the URL navigation so rapid clicks collapse into one nav.
function onSemitonesChange(n: number) {
  pendingSemitones.value = n;
  audioPlayerRef.value?.audio?.pause();
  if (navTimer !== null) clearTimeout(navTimer);
  navTimer = setTimeout(() => {
    navTimer = null;
    if (n !== routeSemitones.value) {
      router.push(`/play/${player.videoId}/${n}`);
    }
  }, NAV_DEBOUNCE_MS);
}

// Watch jobStatus → done: load melody once.
watch(
  () => player.jobStatus,
  async (next) => {
    if (next === "done") {
      await loadDoneArtifacts();
    }
  },
);

// On route param change (user transposed → URL changed), kick a new generate.
watch(routeSemitones, async (next) => {
  if (noContext.value) return;
  // Sync pending (catches direct URL edits / browser back-forward)
  pendingSemitones.value = next;
  // Ensure store semitones matches URL
  player.semitones = next;
  // Re-issue generate for this key. JobRunner skips cached stages — shift only ~5-15s.
  sse.close();
  player.jobStatus = "idle";
  player.jobMessage = "";
  try {
    await player.startGenerate();
    startSSE();
  } catch (e) {
    player.applyStatus("error", String(e));
  }
});

onMounted(() => {
  if (noContext.value) {
    // No song selected (refresh, deep link, or expired session) — bounce to search.
    router.replace("/");
    return;
  }
  // If we already have a jobId from PreviewView, just open the SSE.
  // Otherwise kick a generate.
  player.semitones = routeSemitones.value;
  if (
    player.jobId &&
    (player.jobStatus === "queued" ||
      player.jobStatus === "downloading" ||
      player.jobStatus === "separating" ||
      player.jobStatus === "melody" ||
      player.jobStatus === "shifting")
  ) {
    startSSE();
  } else if (player.jobStatus === "done") {
    // Came back to a done state — load artifacts.
    loadDoneArtifacts();
  } else {
    // Cold start — kick the generate.
    (async () => {
      try {
        await player.startGenerate();
        startSSE();
      } catch (e) {
        player.applyStatus("error", String(e));
      }
    })();
  }
});

function fmtDuration(sec: number): string {
  const m = Math.floor(sec / 60);
  const s = sec % 60;
  return `${m}:${s.toString().padStart(2, "0")}`;
}

onUnmounted(() => {
  if (navTimer !== null) clearTimeout(navTimer);
});
</script>

<template>
  <div class="max-w-3xl w-full mx-auto px-4 py-8 min-h-screen">
    <button
      @click="router.push(`/preview/${routeVideoId}`)"
      class="mb-6 text-sm text-gray-400 hover:text-white transition-colors"
    >
      ← Back to preview
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

      <!-- Transpose pill (rapid clicks debounce into one nav after 600ms) -->
      <div class="flex items-center gap-4 mb-3">
        <KeySelector
          :semitones="pendingSemitones"
          @change="onSemitonesChange"
        />
      </div>

      <!-- Key display -->
      <div v-if="showKeyLine" class="mb-3 text-gray-300">{{ keyDisplay }}</div>
      <div v-else class="mb-3 text-gray-600 text-sm">&nbsp;</div>

      <!-- Vocal octave selector — shifts only the displayed target line, not the audio -->
      <div class="flex items-center gap-4 mb-2">
        <VocalOctaveSelector
          :current="player.vocalOctaveShift"
          :disabled="player.jobStatus !== 'done'"
          @change="player.setVocalOctaveShift"
        />
      </div>
      <div class="mb-6 text-gray-300 text-sm min-h-[1.25rem]">
        <template v-if="player.melody && player.vocalRange !== null">
          Vocal: {{ midiToNoteName(player.vocalRange.minMidi) }} –
          {{ midiToNoteName(player.vocalRange.maxMidi) }}
        </template>
      </div>

      <!-- SSE progress while in-flight; audio player when done -->
      <div v-if="!isDone" class="mb-6">
        <ProcessingStatus
          :status="player.jobStatus"
          :message="player.jobMessage"
        />
      </div>

      <div
        v-else
        class="mb-6 rounded-xl p-4 bg-[#1a1822] border border-[#2a2730]"
      >
        <AudioPlayer
          ref="audioPlayerRef"
          :src="fullAudioSrc"
          :hide-play-button="true"
        />
      </div>

      <!-- Pitch diagram: shown once generate is done, melody is loaded, and audio element exists.
           :key on routeSemitones forces a clean remount on transpose so user-history + targetSeries reset. -->
      <PitchDiagram
        v-if="
          player.melody && player.jobStatus === 'done' && audioPlayerRef?.audio
        "
        :key="`${routeSemitones}-${player.vocalOctaveShift}`"
        :audio-el="audioPlayerRef.audio!"
        :melody="player.melody"
        :vocal-octave-shift="player.vocalOctaveShift"
      />
    </template>
  </div>
</template>

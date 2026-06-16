<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { usePlayerStore } from "@/stores/player";
import { useSSE } from "@/composables/useSSE";
import { statusURL, audioURL, getMelody } from "@/services/api";
import { shortKey, transposeKey } from "@/utils/key";
import { midiToNoteName, hzToMidi } from "@/utils/pitch";
import KeySelector from "@/components/KeySelector.vue";
import VocalOctaveSelector from "@/components/VocalOctaveSelector.vue";
import AudioPlayer from "@/components/AudioPlayer.vue";
import ProcessingStatus from "@/components/ProcessingStatus.vue";
import PitchDiagram from "@/components/PitchDiagram.vue";
import LyricsPanel from "@/components/LyricsPanel.vue";
import { useLyrics, type LyricsSongBundle } from "@/composables/useLyrics";

const route = useRoute();
const router = useRouter();
const player = usePlayerStore();
const sse = useSSE();
const audioPlayerRef = ref<InstanceType<typeof AudioPlayer> | null>(null);

const NAV_DEBOUNCE_MS = 600;

// Lyrics — currentTimeSec driven by the AudioPlayer's timeupdate event.
const currentTimeSec = ref(0);
const lyricsSong = computed<LyricsSongBundle | null>(() => {
  if (!player.song) return null;
  return {
    videoId: player.song.video_id,
    lyricsSig: player.song.lyrics_sig,
    title: player.song.title,
    artist: player.song.artist,
    album: player.song.album,
    durationSec: player.song.duration_sec,
  };
});
const {
  available: lyricsAvailable,
  lines: lyricsLines,
  plain: lyricsPlain,
  activeIndex: lyricsActiveIndex,
  loading: lyricsLoading,
} = useLyrics(currentTimeSec, lyricsSong);

function onAudioTimeUpdate() {
  const el = audioPlayerRef.value?.audio;
  if (el) currentTimeSec.value = el.currentTime;
}

// Attach timeupdate to the native audio element once the AudioPlayer mounts.
watch(
  () => audioPlayerRef.value?.audio,
  (audioEl, prevAudioEl) => {
    if (prevAudioEl) {
      prevAudioEl.removeEventListener("timeupdate", onAudioTimeUpdate);
    }
    if (audioEl) {
      audioEl.addEventListener("timeupdate", onAudioTimeUpdate);
    }
  },
);

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

// Voice range comes only from the full melody. player.vocalRange falls back to
// the 30s previewMelody when the full melody isn't loaded, which would leak
// the preview-clip range into PlayView while a generate is still running.
const fullVocalRange = computed<{ minMidi: number; maxMidi: number } | null>(
  () => {
    if (!player.melody) return null;
    const voiced = player.melody.frames
      .filter(([, hz]) => hz > 0)
      .map(([, hz]) => hzToMidi(hz));
    if (voiced.length === 0) return null;
    const shift = player.vocalOctaveShift;
    return {
      minMidi: Math.round(Math.min(...voiced) + shift),
      maxMidi: Math.round(Math.max(...voiced) + shift),
    };
  },
);

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
  // Clear stale melody — PitchDiagram captures props.melody at setup, so if it
  // mounts with a melody from a previous semitones value, the target line stays
  // wrong even after loadDoneArtifacts replaces player.melody. Forcing null gates
  // PitchDiagram on the fresh fetch.
  player.melody = null;
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
    // Clear melody left over from PreviewView's loadFullMelodyIfAvailable —
    // it was fetched at the previous semitones and would leak into PitchDiagram
    // before loadDoneArtifacts replaces it (PitchDiagram captures props.melody
    // at setup, doesn't re-read on prop change).
    player.melody = null;
    startSSE();
  } else if (player.jobStatus === "done") {
    // Came back to a done state — load artifacts.
    player.melody = null;
    loadDoneArtifacts();
  } else {
    // Cold start — kick the generate.
    // Clear any melody loaded for a different semitones value in PreviewView
    // (loadFullMelodyIfAvailable runs at player.semitones=0 on entry) so
    // PitchDiagram waits for the fresh fetch instead of mounting on a stale
    // melody whose targetSeries it'd capture at setup.
    player.melody = null;
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

onUnmounted(() => {
  if (navTimer !== null) clearTimeout(navTimer);
});
</script>

<template>
  <div class="min-h-[100svh] flex flex-col">
    <template v-if="!noContext">
      <!-- Slim top bar -->
      <header
        class="shrink-0 bg-[var(--color-surface)]/95 backdrop-blur border-b border-[var(--color-border)]"
      >
        <div class="max-w-6xl mx-auto px-4 py-3 flex items-center gap-4">
          <button
            @click="router.push(`/preview/${routeVideoId}`)"
            class="text-[13px] text-[var(--color-text-muted)] hover:text-[var(--color-text)] shrink-0"
            aria-label="Back to preview"
          >
            ←
          </button>
          <img
            v-if="player.song?.thumbnail_url"
            :src="player.song.thumbnail_url"
            :alt="player.song.title"
            class="w-10 h-10 rounded-md object-cover shrink-0"
          />
          <div class="min-w-0 flex-1">
            <div
              class="text-[15px] font-medium leading-tight truncate text-[var(--color-text)]"
            >
              {{ player.song?.title }}
            </div>
            <div class="text-[12px] text-[var(--color-text-muted)] truncate">
              {{ player.song?.artist }}
              <template v-if="player.song?.album">
                · {{ player.song.album }}</template
              >
            </div>
          </div>
          <div class="hidden md:flex items-end gap-6 shrink-0">
            <KeySelector
              :semitones="pendingSemitones"
              :original-key="
                player.originalKey ? shortKey(player.originalKey) : undefined
              "
              :transposed-key="
                player.originalKey
                  ? shortKey(transposeKey(player.originalKey, pendingSemitones))
                  : undefined
              "
              @change="onSemitonesChange"
            />
            <VocalOctaveSelector
              :current="player.vocalOctaveShift"
              :disabled="player.jobStatus !== 'done'"
              :range="
                fullVocalRange
                  ? `${midiToNoteName(fullVocalRange.minMidi)} – ${midiToNoteName(fullVocalRange.maxMidi)}`
                  : undefined
              "
              @change="player.setVocalOctaveShift"
            />
          </div>
        </div>
      </header>

      <!-- Mobile control row (below the slim top bar) -->
      <div
        class="md:hidden shrink-0 max-w-6xl mx-auto px-4 py-4 flex flex-wrap items-end justify-center gap-4"
      >
        <KeySelector
          :semitones="pendingSemitones"
          :original-key="
            player.originalKey ? shortKey(player.originalKey) : undefined
          "
          :transposed-key="
            player.originalKey
              ? shortKey(transposeKey(player.originalKey, pendingSemitones))
              : undefined
          "
          @change="onSemitonesChange"
        />
        <VocalOctaveSelector
          :current="player.vocalOctaveShift"
          :disabled="player.jobStatus !== 'done'"
          :range="
            fullVocalRange
              ? `${midiToNoteName(fullVocalRange.minMidi)} – ${midiToNoteName(fullVocalRange.maxMidi)}`
              : undefined
          "
          @change="player.setVocalOctaveShift"
        />
      </div>

      <!-- Main hero area -->
      <main
        class="flex-1 min-h-0 w-full max-w-6xl mx-auto px-4 pt-4 pb-24 flex flex-col"
      >
        <div class="flex-1 min-h-0 flex flex-col gap-3">
          <!-- PitchDiagram card — stable height regardless of job state -->
          <div class="relative flex-1 min-h-[320px]">
            <!-- Job still running -->
            <div
              v-if="!isDone"
              class="absolute inset-0 rounded-xl bg-[var(--color-surface)] border border-[var(--color-border)] flex items-center justify-center"
            >
              <ProcessingStatus
                :status="player.jobStatus"
                :message="player.jobMessage"
              />
            </div>
            <!-- Done + melody loaded -->
            <PitchDiagram
              v-else-if="player.melody && audioPlayerRef?.audio"
              :key="`${routeSemitones}-${player.vocalOctaveShift}`"
              :audio-el="audioPlayerRef.audio!"
              :melody="player.melody"
              :vocal-octave-shift="player.vocalOctaveShift"
              fill
              class="h-full w-full"
            />
            <!-- Done but melody not yet available — blank placeholder -->
            <div
              v-else
              class="absolute inset-0 rounded-xl bg-[var(--color-surface)] border border-[var(--color-border)]"
            />
          </div>

          <!-- Lyrics card -->
          <div
            class="h-32 sm:h-40 shrink-0 overflow-hidden rounded-xl bg-[var(--color-surface)] border border-[var(--color-border)]"
          >
            <LyricsPanel
              :lines="lyricsLines"
              :active-index="lyricsActiveIndex"
              :plain="lyricsPlain"
              :available="lyricsAvailable"
              :loading="lyricsLoading"
            />
          </div>
        </div>
      </main>

      <!-- Sticky bottom transport (scrubber only — Play & Sing lives in PitchDiagram) -->
      <AudioPlayer
        v-if="isDone"
        ref="audioPlayerRef"
        :src="fullAudioSrc"
        :hide-play-button="true"
        variant="bottom-bar"
      />
    </template>
  </div>
</template>

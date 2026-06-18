<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { usePlayerStore } from "@/stores/player";
import { useSearchStore } from "@/stores/search";
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
import { retryPolicy } from "@/lib/retryPolicy";

const route = useRoute();
const router = useRouter();
const player = usePlayerStore();
const search = useSearchStore();
const sse = useSSE();
const audioPlayerRef = ref<InstanceType<typeof AudioPlayer> | null>(null);
const pitchDiagramRef = ref<InstanceType<typeof PitchDiagram> | null>(null);

const headerQuery = ref("");
function onHeaderSearch() {
  const q = headerQuery.value.trim();
  if (!q) return;
  void search.runSearch(q);
  router.push("/");
}

const NAV_DEBOUNCE_MS = 600;

// Auto-retry state for transient generate / SSE-disconnect failures.
const autoRetryCount = ref(0);
const autoRetryExhausted = ref(false);
let autoRetryTimer: ReturnType<typeof setTimeout> | null = null;

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

/**
 * scheduleAutoRetry fires a generate retry after exponential backoff.
 * After retryPolicy.maxAttempts retries are exhausted it sets autoRetryExhausted
 * so the UI can show a manual Retry button.
 */
function scheduleAutoRetry() {
  if (autoRetryExhausted.value) return;
  if (autoRetryCount.value >= retryPolicy.maxAttempts) {
    autoRetryExhausted.value = true;
    return;
  }
  const delay = retryPolicy.backoffMs * Math.pow(2, autoRetryCount.value);
  autoRetryCount.value++;
  autoRetryTimer = setTimeout(async () => {
    autoRetryTimer = null;
    player.jobStatus = "queued";
    player.jobMessage = "";
    sse.close();
    try {
      await player.startGenerate();
      startSSE();
    } catch {
      scheduleAutoRetry();
    }
  }, delay);
}

/** Manual retry — resets the exhaustion counter so the user gets fresh attempts. */
function onManualRetry() {
  autoRetryCount.value = 0;
  autoRetryExhausted.value = false;
  player.jobStatus = "queued";
  player.jobMessage = "";
  sse.close();
  void (async () => {
    try {
      await player.startGenerate();
      startSSE();
    } catch (e) {
      player.applyStatus("error", String(e));
    }
  })();
}

// Watch sse.error: if the job hasn't finished, treat it as a transient disconnect
// and feed into the auto-retry policy.
watch(
  () => sse.error.value,
  (err) => {
    if (!err) return;
    if (player.jobStatus === "done" || player.jobStatus === "error") return;
    scheduleAutoRetry();
  },
);

// Watch player.jobStatus: if the SSE reports an error from the server, also
// trigger auto-retry (server-side pipeline error, not just a connection drop).
watch(
  () => player.jobStatus,
  (next, prev) => {
    if (next === "done") {
      // Reset retry counters on success.
      autoRetryCount.value = 0;
      autoRetryExhausted.value = false;
      void loadDoneArtifacts();
    }
    if (next === "error" && prev !== "error") {
      scheduleAutoRetry();
    }
  },
);

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
  // Reset retry state for new generate attempt.
  autoRetryCount.value = 0;
  autoRetryExhausted.value = false;
  if (autoRetryTimer !== null) {
    clearTimeout(autoRetryTimer);
    autoRetryTimer = null;
  }
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
  if (autoRetryTimer !== null) clearTimeout(autoRetryTimer);
});
</script>

<template>
  <div class="min-h-[100svh] sm:h-[100svh] flex flex-col">
    <template v-if="!noContext">
      <!-- Slim top bar: wordmark + back + search + desktop controls -->
      <header
        class="shrink-0 bg-[var(--color-surface)]/95 backdrop-blur border-b border-[var(--color-border)]"
      >
        <div class="max-w-6xl mx-auto px-4 py-3 flex items-center gap-4">
          <button
            @click="router.push('/')"
            class="font-serif italic text-[20px] leading-none text-[var(--color-text)] hover:text-[var(--color-accent)] tracking-tight shrink-0 transition-colors"
            aria-label="Back to home"
          >
            cantus
          </button>
          <button
            @click="router.push(`/preview/${routeVideoId}`)"
            class="text-[13px] text-[var(--color-text-muted)] hover:text-[var(--color-text)] shrink-0"
            aria-label="Back to preview"
          >
            ←
          </button>
          <!-- Search pinned to the right -->
          <form
            @submit.prevent="onHeaderSearch"
            class="ml-auto w-48 sm:w-64 max-w-full"
          >
            <input
              v-model="headerQuery"
              type="search"
              placeholder="Search another song"
              autocomplete="off"
              class="w-full px-4 py-2 text-sm rounded-full bg-[var(--color-surface-2)] border border-[var(--color-border)] focus:border-[var(--color-accent)] focus:ring-2 focus:ring-[var(--color-accent)]/40 focus:outline-none text-[var(--color-text)] placeholder-[var(--color-text-faint)] transition-all"
            />
          </form>
        </div>
      </header>

      <!-- Controls row: horizontal when there's room, wraps stacked when not -->
      <div
        class="shrink-0 max-w-6xl mx-auto w-full px-4 pt-4 pb-2 flex flex-wrap items-end justify-center gap-4 sm:gap-8"
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
        class="flex-1 min-h-0 w-full max-w-6xl mx-auto px-4 pt-4 pb-28 flex flex-col"
      >
        <div class="flex-1 min-h-0 flex flex-col gap-3">
          <!-- PitchDiagram card — stable height regardless of job state -->
          <div class="relative flex-1 min-h-[320px]">
            <!-- Job still running or errored -->
            <div
              v-if="!isDone"
              class="absolute inset-0 rounded-xl bg-[var(--color-surface)] border border-[var(--color-border)] flex flex-col items-center justify-center gap-3"
            >
              <ProcessingStatus
                :status="player.jobStatus"
                :message="player.jobMessage"
              />
              <!-- Manual retry button: shown only after auto-retries are exhausted -->
              <button
                v-if="autoRetryExhausted"
                @click="onManualRetry"
                class="mt-2 px-4 py-2 rounded-full bg-[var(--color-accent)] hover:bg-[var(--color-accent-hover)] text-[#0a0a0b] text-sm transition-colors"
              >
                Retry
              </button>
            </div>
            <!-- Done + melody loaded -->
            <PitchDiagram
              v-else-if="player.melody && audioPlayerRef?.audio"
              ref="pitchDiagramRef"
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

      <!-- Sticky bottom transport with song meta -->
      <AudioPlayer
        v-if="isDone"
        ref="audioPlayerRef"
        :src="fullAudioSrc"
        variant="bottom-bar"
        :thumbnail-url="player.song?.thumbnail_url"
        :title="player.song?.title"
        :subtitle="`${player.song?.artist ?? ''}${player.song?.album ? ' · ' + player.song.album : ''}`"
        :playing="pitchDiagramRef?.isActive ?? false"
        :disabled="player.jobStatus !== 'done'"
        @toggle="pitchDiagramRef?.togglePlayAndSing()"
      />
    </template>
  </div>
</template>

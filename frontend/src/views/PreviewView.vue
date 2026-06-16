<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { usePlayerStore } from "@/stores/player";
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
const audioPlayerRef = ref<InstanceType<typeof AudioPlayer> | null>(null);

// Lyrics — currentTimeSec driven by the AudioPlayer's timeupdate event.
// Preview clip is the first 30s of the song, so currentTime maps 1:1 to song time.
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

// Display key comes only from /api/preview-key, which re-reads melody.json's
// key (CREPE on isolated full-song vocals). When the full song hasn't been
// generated the endpoint returns "" and the KEY label hides.
const displayKey = computed(() => player.previewKey || null);

// Voice range follows the same rule as the key: only show when the full song
// has been analyzed. We derive it from player.melody (CREPE on full vocals)
// rather than player.vocalRange (which falls back to the 30s previewMelody)
// so that we don't display a range computed from too little audio.
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

const shiftPending = ref(false);
// Pending semitones — updates instantly on click for snappy UI feedback.
// The actual /api/preview-shift fires only after a 600ms debounce.
const pendingSemitones = ref(player.semitones);
let shiftTimer: ReturnType<typeof setTimeout> | null = null;

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
  // loadPreviewKey reads melody.json's key via /api/preview-key when the song
  // has been generated previously; returns "" otherwise so the KEY label hides.
  void player.loadPreviewKey();
  // loadFullMelodyIfAvailable tries /api/melody and 404s silently if the song
  // hasn't been generated. When it succeeds, player.melody populates and the
  // VOICE range subtitle below renders — same gating rule as the KEY label.
  void player.loadFullMelodyIfAvailable();
  void player.loadPreviewStems();
});

onUnmounted(() => {
  if (shiftTimer !== null) clearTimeout(shiftTimer);
});
</script>

<template>
  <div class="h-[100svh] flex flex-col">
    <template v-if="!noContext">
      <!-- Slim top bar -->
      <header
        class="shrink-0 bg-[var(--color-surface)]/95 backdrop-blur border-b border-[var(--color-border)]"
      >
        <div class="max-w-6xl mx-auto px-4 py-3 flex items-center gap-4">
          <button
            @click="router.push('/')"
            class="text-[13px] text-[var(--color-text-muted)] hover:text-[var(--color-text)] shrink-0"
            aria-label="Back to search"
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
            <div class="flex items-center gap-2 min-w-0">
              <div
                class="text-[15px] font-medium leading-tight truncate text-[var(--color-text)]"
              >
                {{ player.song?.title }}
              </div>
              <!-- Preview pill -->
              <span
                class="shrink-0 rounded-full bg-[var(--color-surface-2)] text-[var(--color-text-faint)] text-[11px] px-2 py-0.5 leading-none"
              >
                Preview · 30s
              </span>
            </div>
            <div class="text-[12px] text-[var(--color-text-muted)] truncate">
              {{ player.song?.artist
              }}<template v-if="player.song?.album">
                · {{ player.song.album }}</template
              >
            </div>
          </div>
          <!-- Desktop controls -->
          <div class="hidden md:flex items-end gap-6 shrink-0">
            <KeySelector
              :semitones="pendingSemitones"
              :disabled="!player.previewStemsReady"
              :original-key="displayKey ? shortKey(displayKey) : undefined"
              :transposed-key="
                displayKey
                  ? shortKey(transposeKey(displayKey, pendingSemitones))
                  : undefined
              "
              @change="onSemitonesChange"
            />
            <VocalOctaveSelector
              :current="player.vocalOctaveShift"
              :disabled="!fullVocalRange"
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

      <!-- Mobile controls row -->
      <div
        class="md:hidden shrink-0 max-w-6xl mx-auto px-4 py-4 flex flex-wrap items-end justify-center gap-4"
      >
        <KeySelector
          :semitones="pendingSemitones"
          :disabled="!player.previewStemsReady"
          :original-key="displayKey ? shortKey(displayKey) : undefined"
          :transposed-key="
            displayKey
              ? shortKey(transposeKey(displayKey, pendingSemitones))
              : undefined
          "
          @change="onSemitonesChange"
        />
        <VocalOctaveSelector
          :current="player.vocalOctaveShift"
          :disabled="!fullVocalRange"
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
        class="flex-1 min-h-0 w-full max-w-6xl mx-auto px-4 pt-4 pb-36 flex flex-col"
      >
        <div class="flex-1 min-h-0 flex flex-col gap-3">
          <!-- PitchDiagram card — stable height regardless of loading state -->
          <div class="relative flex-1 min-h-0">
            <!-- Loading state — same card footprint as PitchDiagram -->
            <div
              v-if="player.previewStemsLoading"
              class="absolute inset-0 rounded-xl bg-[var(--color-surface)] border border-[var(--color-border)] flex items-center justify-center"
            >
              <ProcessingStatus
                status="separating"
                message="Getting your accompaniment ready…"
              />
            </div>

            <!-- Error state -->
            <div
              v-else-if="player.previewStemsError"
              class="absolute inset-0 rounded-xl bg-[var(--color-surface)] border border-[var(--color-danger)]/60 flex flex-col items-center justify-center gap-3 p-4"
            >
              <p class="text-[var(--color-danger)] text-sm text-center">
                {{ player.previewStemsError }}
              </p>
              <button
                @click="() => void player.loadPreviewStems()"
                class="px-4 py-2 rounded-full bg-[var(--color-accent)] hover:bg-[var(--color-accent-hover)] text-[#0a0a0b] text-sm transition-colors"
              >
                Retry
              </button>
            </div>

            <!-- Ready state: pitch diagram with shift-pending overlay -->
            <template v-else>
              <!-- Shift pending overlay on the diagram card only -->
              <div
                v-if="shiftPending"
                class="absolute inset-0 rounded-xl bg-[var(--color-surface)]/85 backdrop-blur-sm flex flex-col items-center justify-center gap-2 z-10"
              >
                <svg
                  class="animate-spin h-6 w-6"
                  viewBox="0 0 24 24"
                  fill="none"
                >
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
                fill
                class="h-full w-full"
              />
              <!-- Placeholder when stems ready but diagram not yet (no melody) -->
              <div
                v-else-if="player.previewStemsReady"
                class="absolute inset-0 rounded-xl bg-[var(--color-surface)] border border-[var(--color-border)]"
              />
            </template>
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

      <!-- Bottom transport with CTA slot -->
      <AudioPlayer
        ref="audioPlayerRef"
        :src="player.audioSrc"
        :hide-play-button="true"
        variant="bottom-bar"
      >
        <template #cta>
          <button
            @click="onGenerateClick"
            class="w-full px-6 py-3 rounded-full bg-[var(--color-accent)] hover:bg-[var(--color-accent-hover)] text-[#0a0a0b] font-medium transition-colors"
          >
            Practice full song →
          </button>
        </template>
      </AudioPlayer>
    </template>
  </div>
</template>

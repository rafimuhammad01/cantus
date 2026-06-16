import { ref, computed, watch, type Ref } from "vue";
import { getLyrics, type Cue } from "@/services/api";

export interface LyricsSongBundle {
  videoId: string;
  lyricsSig: string;
  title: string;
  artist: string;
  album: string;
  durationSec: number;
}

export interface UseLyrics {
  available: Ref<boolean>;
  lines: Ref<Cue[]>;
  plain: Ref<string>;
  activeIndex: Ref<number>;
  loading: Ref<boolean>;
  error: Ref<string | null>;
}

/**
 * useLyrics fetches and manages timed lyrics for a given song bundle.
 *
 * @param currentTimeSec - reactive ref tracking the audio element's currentTime (in seconds)
 * @param song - reactive ref containing the song metadata needed to fetch lyrics
 */
export function useLyrics(
  currentTimeSec: Ref<number>,
  song: Ref<LyricsSongBundle | null>,
): UseLyrics {
  const available = ref(false);
  const lines = ref<Cue[]>([]);
  const plain = ref("");
  const loading = ref(false);
  const error = ref<string | null>(null);

  // Fetch lyrics whenever the song changes.
  watch(
    song,
    async (next) => {
      // Reset state for the new song.
      available.value = false;
      lines.value = [];
      plain.value = "";
      error.value = null;

      if (!next || !next.videoId || !next.lyricsSig) return;

      loading.value = true;
      try {
        const resp = await getLyrics(
          next.videoId,
          next.lyricsSig,
          next.title,
          next.artist,
          next.album,
          next.durationSec,
        );
        available.value = resp.available;
        lines.value = resp.synced ?? [];
        plain.value = resp.plain ?? "";
      } catch (e) {
        error.value = (e as Error).message ?? "Failed to load lyrics";
        available.value = false;
      } finally {
        loading.value = false;
      }
    },
    { immediate: true },
  );

  // Binary search: find the largest index where lines[i].start_ms <= currentMs.
  // Returns -1 if before the first cue or lines is empty.
  const activeIndex = computed<number>(() => {
    const cues = lines.value;
    if (cues.length === 0) return -1;
    const currentMs = currentTimeSec.value * 1000;
    if (currentMs < cues[0].start_ms) return -1;

    let lo = 0;
    let hi = cues.length - 1;
    while (lo < hi) {
      const mid = (lo + hi + 1) >> 1;
      if (cues[mid].start_ms <= currentMs) {
        lo = mid;
      } else {
        hi = mid - 1;
      }
    }
    return lo;
  });

  return { available, lines, plain, activeIndex, loading, error };
}

import { defineStore } from "pinia";
import { ref, computed, watch } from "vue";
import {
  previewURL,
  previewShift,
  prewarm as apiPrewarm,
  generate as apiGenerate,
  getMelody,
  getPreviewKey,
  audioURL,
  triggerPreviewStems,
  getPreviewMelody,
  previewAudioURL,
  type SearchResult,
  type MelodyResponse,
  type JobStatusName,
} from "@/services/api";
import { withRetry, retryPolicy } from "@/lib/retryPolicy";
import { hzToMidi } from "@/utils/pitch";

type PlayerMode = "idle" | "preview" | "preview-shift" | "full";

// Survives page refresh so /preview/:videoId and /play/:videoId/:semitones can
// rehydrate identity (sig + song metadata) without re-running the search. Scoped
// to sessionStorage so it auto-clears on tab close — refresh, not "permanent".
const PERSIST_KEY = "cantus.player.v1";

interface PersistedPlayer {
  videoId: string;
  sig: string;
  song: SearchResult | null;
  semitones: number;
  vocalOctaveShift: -12 | 0 | 12;
}

function loadPersisted(): PersistedPlayer | null {
  if (typeof sessionStorage === "undefined") return null;
  try {
    const raw = sessionStorage.getItem(PERSIST_KEY);
    if (!raw) return null;
    return JSON.parse(raw) as PersistedPlayer;
  } catch {
    return null;
  }
}

export const usePlayerStore = defineStore("player", () => {
  const persisted = loadPersisted();

  // Identity / song metadata
  const videoId = ref<string>(persisted?.videoId ?? "");
  const sig = ref<string>(persisted?.sig ?? "");
  const song = ref<SearchResult | null>(persisted?.song ?? null);

  // Transpose state
  const semitones = ref(persisted?.semitones ?? 0);

  // Vocal octave shift: -12, 0, or +12 semitones applied only to the displayed
  // target line — instrumental audio is unchanged.
  const vocalOctaveShift = ref<-12 | 0 | 12>(persisted?.vocalOctaveShift ?? 0);

  // Range of voiced MIDI notes in the active melody, with vocalOctaveShift applied.
  // Prefers previewMelody when in preview/preview-shift mode; falls back to full melody.
  const vocalRange = computed<{ minMidi: number; maxMidi: number } | null>(
    () => {
      const activeMelody =
        mode.value === "full"
          ? melody.value
          : (previewMelody.value ?? melody.value);
      if (!activeMelody) return null;
      const voiced = activeMelody.frames
        .filter(([, hz]) => hz > 0)
        .map(([, hz]) => hzToMidi(hz));
      if (voiced.length === 0) return null;
      const shift = vocalOctaveShift.value;
      return {
        minMidi: Math.round(Math.min(...voiced) + shift),
        maxMidi: Math.round(Math.max(...voiced) + shift),
      };
    },
  );

  // Audio source — rehydrate to the raw preview URL when we have identity from
  // sessionStorage but no in-memory blob/stem. loadPreviewStems() will swap it
  // to the cleaner instrumental stem once ready (same as a cold preview load).
  const audioSrc = ref<string>(
    persisted?.videoId && persisted?.sig
      ? previewURL(persisted.videoId, persisted.sig)
      : "",
  );
  const mode = ref<PlayerMode>(persisted?.videoId ? "preview" : "idle");

  // Preview key — loaded from /api/preview-key after song selection (no generate needed)
  const previewKey = ref<string>("");

  // Preview-stems state — populated by loadPreviewStems() after Demucs + CREPE
  const previewMelody = ref<MelodyResponse | null>(null);
  const previewStemsReady = ref(false);
  const previewStemsLoading = ref(false); // shown by PreviewView as a spinner
  const previewStemsError = ref<string>("");

  // Melody + key (populated after /api/melody fetches; key visible only after generate done)
  const melody = ref<MelodyResponse | null>(null);
  const originalKey = computed(() => melody.value?.key ?? null);

  // Generate job
  const jobId = ref<string | null>(null);
  const jobStatus = ref<JobStatusName | "idle">("idle");
  const jobMessage = ref<string>("");

  // Track blob URLs so we can revoke them when replaced
  let currentBlobUrl: string | null = null;
  function setAudioSrc(url: string, isBlob = false) {
    if (currentBlobUrl) {
      URL.revokeObjectURL(currentBlobUrl);
      currentBlobUrl = null;
    }
    audioSrc.value = url;
    if (isBlob) currentBlobUrl = url;
  }

  function setVocalOctaveShift(shift: -12 | 0 | 12): void {
    vocalOctaveShift.value = shift;
  }

  /** Called from SongCard.click — initializes the player for a given song. */
  function selectSong(result: SearchResult) {
    videoId.value = result.video_id;
    sig.value = result.sig;
    song.value = result;
    semitones.value = 0;
    vocalOctaveShift.value = 0;
    previewKey.value = "";
    // Reset preview-stems machinery so each new song starts fresh
    previewMelody.value = null;
    previewStemsReady.value = false;
    previewStemsLoading.value = false;
    previewStemsError.value = "";
    melody.value = null;
    jobId.value = null;
    jobStatus.value = "idle";
    jobMessage.value = "";
    setAudioSrc(previewURL(result.video_id, result.sig));
    mode.value = "preview";
  }

  /**
   * Load the original key from /api/preview-key. Idempotent — skips if
   * previewKey is already loaded for the current videoId.
   */
  async function loadPreviewKey() {
    if (!videoId.value || !sig.value) return;
    if (previewKey.value !== "") return; // already loaded
    try {
      const resp = await getPreviewKey(videoId.value, sig.value);
      previewKey.value = resp.key;
    } catch {
      // Non-fatal — key display will stay blank
    }
  }

  // Race guard for setSemitones — if the user transposes twice quickly past the
  // debounce, the second request must win regardless of HTTP completion order.
  // Each call bumps the seq, aborts the prior in-flight request, and discards
  // any response whose seq is no longer current before touching audioSrc.
  let setSemitonesSeq = 0;
  let setSemitonesAbort: AbortController | null = null;

  /** Fire /api/preview-shift and swap audioSrc to the returned blob. */
  async function setSemitones(n: number) {
    if (n === semitones.value) return;

    const mySeq = ++setSemitonesSeq;
    if (setSemitonesAbort) setSemitonesAbort.abort();
    const controller = new AbortController();
    setSemitonesAbort = controller;
    const isStale = () => mySeq !== setSemitonesSeq;

    // Full-song mode (after generate): unchanged.
    if (mode.value === "full" && melody.value !== null) {
      semitones.value = n;
      setAudioSrc(audioURL(videoId.value, sig.value, n));
      const m = await getMelody(videoId.value, sig.value, n, controller.signal);
      if (isStale()) return;
      melody.value = m;
      return;
    }

    // Preview mode with stems ready: fetch the transposed melody BEFORE flipping
    // semitones, since `:key="player.semitones"` on PitchDiagram triggers a
    // remount synchronously when the ref changes. If we flipped first the
    // diagram would remount with the previous (stale) melody and the const
    // targetSeries at component setup would lock in the wrong target line.
    if (previewStemsReady.value) {
      try {
        const pm = await getPreviewMelody(
          videoId.value,
          sig.value,
          n,
          controller.signal,
        );
        if (isStale()) return;
        previewMelody.value = pm;
      } catch {
        if (isStale()) return;
        // Non-fatal — pitch diagram will just stale until next successful fetch
      }
    }

    semitones.value = n;

    // n=0 → return to the unshifted source (stem when ready, raw preview otherwise)
    if (n === 0) {
      if (previewStemsReady.value) {
        // Stems ready: use the clean instrumental stem (no chipmunk artifacts)
        setAudioSrc(previewAudioURL(videoId.value, sig.value));
      } else {
        setAudioSrc(previewURL(videoId.value, sig.value));
      }
      mode.value = "preview";
      return;
    }

    // n≠0: fetch shifted preview blob (backend shifts clean stem when available)
    let blob: Blob;
    try {
      blob = await previewShift(videoId.value, sig.value, n, controller.signal);
    } catch (e) {
      if (isStale()) return;
      throw e;
    }
    if (isStale()) return;
    setAudioSrc(URL.createObjectURL(blob), true);
    mode.value = "preview-shift";
  }

  /**
   * Trigger Demucs + CREPE on the 30s preview clip, then fetch the preview melody.
   * Blocks ~14s warm-server / ~50s cold-server. Idempotent on the backend, so safe to call multiple times.
   * Once complete, swaps audioSrc to the clean instrumental stem URL.
   *
   * Each attempt uses an AbortController with a deadline of retryPolicy.stallTimeoutMs.
   * If the server goes silent for that long the fetch is aborted and withRetry retries.
   */
  async function loadPreviewStems(): Promise<void> {
    if (!videoId.value || !sig.value) return;
    if (previewStemsReady.value) return; // idempotent — stems already loaded
    if (previewStemsLoading.value) return; // in-flight; let it complete
    previewStemsLoading.value = true;
    previewStemsError.value = "";
    // Capture current videoId/sig in case the song changes mid-flight.
    const vid = videoId.value;
    const s = sig.value;
    try {
      await withRetry(() => {
        const controller = new AbortController();
        const stallTimer = setTimeout(
          () => controller.abort(),
          retryPolicy.stallTimeoutMs,
        );
        return triggerPreviewStems(vid, s, controller.signal).finally(() =>
          clearTimeout(stallTimer),
        );
      });
      // Fetch melody at the current semitones (usually 0 on first load)
      previewMelody.value = await getPreviewMelody(vid, s, semitones.value);
      // Swap audio source from the legacy fast preview to the clean stem
      setAudioSrc(previewAudioURL(vid, s));
      mode.value = "preview";
      previewStemsReady.value = true;
    } catch (e) {
      previewStemsError.value =
        (e as Error).message ?? "Failed to load preview stems";
    } finally {
      previewStemsLoading.value = false;
    }
  }

  /**
   * Fetch the full melody if the server has it cached (i.e. /api/generate has
   * already run for this song). Silently no-ops on 404 so PreviewView can call
   * it unconditionally — used to gate the displayed vocal range and key on
   * full-song availability without requiring the user to re-generate.
   */
  async function loadFullMelodyIfAvailable(): Promise<void> {
    if (!videoId.value || !sig.value) return;
    if (melody.value !== null) return;
    try {
      melody.value = await getMelody(videoId.value, sig.value, semitones.value);
    } catch {
      // 404 = not generated yet. Non-fatal; UI hides range/key.
    }
  }

  /** Fire-and-forget /api/prewarm. No UI state — purely a background optimization. */
  function startPrewarm(vid: string, s: string): void {
    if (!vid || !s) return;
    void apiPrewarm(vid, s).catch(() => {
      // Non-fatal — prewarm is an optimization; generate will run the full pipeline.
    });
  }

  // Race guard for startGenerate — if the user transposes again on PlayView
  // before the prior /api/generate POST resolves, we must NOT let the older
  // response clobber jobId (which would point the SSE at the wrong job and
  // play the wrong key). Returns null if superseded so callers skip startSSE.
  let startGenerateSeq = 0;
  let startGenerateAbort: AbortController | null = null;

  /** Kick off /api/generate. Caller drives the SSE via useSSE. Returns null if a newer call superseded this one. */
  async function startGenerate(): Promise<string | null> {
    const mySeq = ++startGenerateSeq;
    if (startGenerateAbort) startGenerateAbort.abort();
    const controller = new AbortController();
    startGenerateAbort = controller;
    let resp;
    try {
      resp = await apiGenerate(
        videoId.value,
        sig.value,
        semitones.value,
        controller.signal,
      );
    } catch (e) {
      if (mySeq !== startGenerateSeq) return null;
      throw e;
    }
    if (mySeq !== startGenerateSeq) return null;
    jobId.value = resp.job_id;
    jobStatus.value = "queued";
    jobMessage.value = "";
    return resp.job_id;
  }

  /** Called by the SSE consumer when status updates arrive. */
  function applyStatus(status: JobStatusName, message: string) {
    jobStatus.value = status;
    jobMessage.value = message;
  }

  if (typeof sessionStorage !== "undefined") {
    watch(
      [videoId, sig, song, semitones, vocalOctaveShift],
      ([v, s, sg, st, vo]) => {
        if (!v || !s) {
          sessionStorage.removeItem(PERSIST_KEY);
          return;
        }
        const payload: PersistedPlayer = {
          videoId: v,
          sig: s,
          song: sg,
          semitones: st,
          vocalOctaveShift: vo,
        };
        sessionStorage.setItem(PERSIST_KEY, JSON.stringify(payload));
      },
      { deep: true },
    );
  }

  return {
    videoId,
    sig,
    song,
    semitones,
    audioSrc,
    mode,
    previewKey,
    previewMelody,
    previewStemsReady,
    previewStemsLoading,
    previewStemsError,
    melody,
    originalKey,
    jobId,
    jobStatus,
    jobMessage,
    vocalOctaveShift,
    vocalRange,
    selectSong,
    setSemitones,
    setVocalOctaveShift,
    loadPreviewKey,
    loadPreviewStems,
    loadFullMelodyIfAvailable,
    startPrewarm,
    startGenerate,
    applyStatus,
  };
});

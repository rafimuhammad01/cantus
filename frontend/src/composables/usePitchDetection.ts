import { ref, type Ref } from "vue";
import * as Pitchfinder from "pitchfinder";
import { hzToMidi } from "@/utils/pitch";

// ─── YIN pitch detector via Pitchfinder ──────────────────────────────────────
//
// Replaces the SPICE/TensorFlow.js detector. YIN is a classical algorithm
// (de Cheveigné & Kawahara 2002) — pure JS, no model weights, no WebGL/WASM.
// Runs synchronously at ~1ms/frame on a 2048-sample Float32Array at native
// sample rate (44.1 or 48 kHz). Works identically on every browser including
// iOS Safari, where SPICE's WebGL weight-upload step hung indefinitely.

// ─── Constants ────────────────────────────────────────────────────────────────

// C2–C6: practical bass-profundo through soprano modal-voice.
const HZ_LOW = 65;
const HZ_HIGH = 1050;

// 5-frame trailing median. Noise spikes that pass the RMS gate tend to
// sustain across 2–3 frames; a 3-frame window can be overwhelmed by them.
// Widening to 5 reliably suppresses 2-frame bursts at the cost of small lag.
const SMOOTH_WINDOW = 5;

// RMS energy floor (linear, on [-1, 1] samples). ~-38 dBFS.
// Below this threshold the frame is silence — skip detection.
const SILENCE_RMS = 0.007;

// Reject frames whose detected MIDI differs more than this many semitones from
// the last stable detection. Bi-directional: YIN's classic failure mode is
// ½× (down an octave) on breathy frames, so we guard both directions.
const OCTAVE_JUMP_LIMIT = 12;

// How long to hold the last detected note after detection drops out. Smooths
// over brief sub-threshold gaps (consonants, vibrato dips).
const HOLD_MS = 140;

// ─── Interface ────────────────────────────────────────────────────────────────

export interface UsePitchDetection {
  currentMidi: Ref<number | null>;
  error: Ref<string | null>;
  isActive: Ref<boolean>;
  isLoading: Ref<boolean>; // true briefly during getUserMedia; always false after
  start(): Promise<void>;
  stop(): void;
}

// ─── Composable ───────────────────────────────────────────────────────────────

export function usePitchDetection(): UsePitchDetection {
  const currentMidi = ref<number | null>(null);
  const error = ref<string | null>(null);
  const isActive = ref(false);
  const isLoading = ref(false);

  let rafId: number | null = null;
  let audioCtx: AudioContext | null = null;
  let mediaStream: MediaStream | null = null;
  let sinkAudioEl: HTMLAudioElement | null = null;

  // Trailing window for median smoothing; holds MIDI values or null for silent frames.
  const smoothBuf: (number | null)[] = [];

  async function start(): Promise<void> {
    if (isActive.value) return;
    error.value = null;
    isLoading.value = true;

    let stream: MediaStream;
    try {
      // Call getUserMedia FIRST (before any other await) so Safari's
      // user-gesture authority is consumed immediately. Chaining awaits before
      // this can silently drop the gesture and Safari refuses the mic without
      // throwing. Disable browser audio processing — echoCancellation /
      // noiseSuppression / AGC filter the spectrum YIN relies on.
      stream = await navigator.mediaDevices.getUserMedia({
        audio: {
          echoCancellation: false,
          noiseSuppression: false,
          autoGainControl: false,
        },
      });
    } catch (e) {
      const name = (e as DOMException).name;
      if (name === "NotAllowedError" || name === "PermissionDeniedError") {
        error.value = "Microphone permission denied";
      } else if (name === "NotFoundError") {
        error.value = "No microphone found";
      } else {
        error.value = (e as Error).message ?? "Microphone error";
      }
      isLoading.value = false;
      return;
    }

    mediaStream = stream;

    // Safari workaround: some WebKit versions don't pump a MediaStream through
    // Web Audio unless the stream is also being consumed by an HTMLMediaElement.
    // Attach to a muted <audio> element and play() so Safari treats the stream
    // as active. Element is held on the closure so it isn't GC'd mid-session.
    const sinkEl = document.createElement("audio");
    sinkEl.srcObject = stream;
    sinkEl.muted = true;
    try {
      await sinkEl.play();
    } catch (e) {
      console.warn("[yin] sink <audio> play() failed:", e);
    }
    sinkAudioEl = sinkEl;

    audioCtx = new AudioContext({ latencyHint: "interactive" });
    // Safari/iOS often start the context in "suspended" state even when created
    // from a user gesture — explicit resume() is needed before audio flows.
    if (audioCtx.state === "suspended") {
      try {
        await audioCtx.resume();
      } catch (e) {
        console.warn("[yin] audioCtx.resume() failed:", e);
      }
    }

    // Instantiate YIN detector at native sample rate — no downsampling needed.
    // YIN works at 44.1 kHz or 48 kHz directly; higher rate = better resolution.
    const detectPitch = Pitchfinder.YIN({
      sampleRate: audioCtx.sampleRate,
      threshold: 0.1, // YIN internal cumulative mean threshold
      probabilityThreshold: 0.2, // tighter than default 0.1 — fewer noise-frame pitches
    });

    const analyser = audioCtx.createAnalyser();
    analyser.fftSize = 2048;
    analyser.smoothingTimeConstant = 0;

    const source = audioCtx.createMediaStreamSource(stream);
    source.connect(analyser);
    // Safari only processes nodes whose graph reaches destination. Without this
    // sink, getFloatTimeDomainData returns all zeros (Chrome processes "dangling"
    // graphs; Safari doesn't). Mute via gain=0 so the user never hears their mic.
    const silentSink = audioCtx.createGain();
    silentSink.gain.value = 0;
    analyser.connect(silentSink);
    silentSink.connect(audioCtx.destination);

    const buf = new Float32Array(2048);

    isActive.value = true;
    isLoading.value = false;

    let lastDetectedMidi: number | null = null;
    let lastDetectedAt = 0;

    function tick(): void {
      if (!isActive.value) return;

      analyser.getFloatTimeDomainData(buf);

      // RMS gate: skip silent frames before paying inference cost and before
      // they can poison the smoothing buffer with noise pitches.
      let sumSq = 0;
      for (let i = 0; i < buf.length; i++) {
        sumSq += buf[i] * buf[i];
      }
      const rms = Math.sqrt(sumSq / buf.length);

      let midi: number | null = null;

      if (rms >= SILENCE_RMS) {
        // YIN is synchronous — ~1ms per 2048-sample frame.
        const hz = detectPitch(buf);
        if (hz !== null && hz > HZ_LOW && hz < HZ_HIGH) {
          const candidate = hzToMidi(hz);
          // Bi-directional octave-jump guard: YIN's classic failure mode is
          // reporting ½× (down an octave) on breathy frames. Guard both
          // directions unlike the old SPICE code which only guarded upward.
          if (
            lastDetectedMidi !== null &&
            Math.abs(candidate - lastDetectedMidi) > OCTAVE_JUMP_LIMIT
          ) {
            midi = null;
          } else {
            midi = candidate;
          }
        }
      }

      // Trailing 5-frame median.
      smoothBuf.push(midi);
      while (smoothBuf.length > SMOOTH_WINDOW) smoothBuf.shift();
      const finite = smoothBuf.filter(
        (v): v is number => v !== null && isFinite(v),
      );
      let smoothed: number | null;
      if (finite.length === 0) {
        smoothed = null;
      } else {
        const sorted = finite.slice().sort((a, b) => a - b);
        const mid = Math.floor(sorted.length / 2);
        smoothed =
          sorted.length % 2 === 1
            ? sorted[mid]
            : (sorted[mid - 1] + sorted[mid]) / 2;
      }

      // Hold last detected note across brief sub-threshold gaps so the UI
      // doesn't strobe on/off during vibrato dips, consonants, etc.
      const nowMs = performance.now();
      if (smoothed !== null) {
        lastDetectedMidi = smoothed;
        lastDetectedAt = nowMs;
        currentMidi.value = smoothed;
      } else if (
        lastDetectedMidi !== null &&
        rms >= SILENCE_RMS &&
        nowMs - lastDetectedAt < HOLD_MS
      ) {
        currentMidi.value = lastDetectedMidi;
      } else {
        currentMidi.value = null;
        lastDetectedMidi = null;
      }

      rafId = requestAnimationFrame(tick);
    }

    rafId = requestAnimationFrame(tick);
  }

  function stop(): void {
    if (rafId !== null) {
      cancelAnimationFrame(rafId);
      rafId = null;
    }
    if (audioCtx !== null) {
      audioCtx.close().catch(() => {});
      audioCtx = null;
    }
    if (mediaStream !== null) {
      mediaStream.getTracks().forEach((t) => t.stop());
      mediaStream = null;
    }
    if (sinkAudioEl !== null) {
      sinkAudioEl.pause();
      sinkAudioEl.srcObject = null;
      sinkAudioEl = null;
    }
    smoothBuf.length = 0;
    currentMidi.value = null;
    isActive.value = false;
    isLoading.value = false;
  }

  return { currentMidi, error, isActive, isLoading, start, stop };
}

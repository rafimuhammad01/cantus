import { ref, type Ref } from "vue";
import * as tf from "@tensorflow/tfjs";
import { hzToMidi } from "@/utils/pitch";

// ─── Module-level ready flag — true once the model has loaded successfully ────

const _isModelReady = ref(false);
/** Reactive ref: true once the SPICE model has finished loading. */
export const isModelReady = _isModelReady;

// ─── Constants ────────────────────────────────────────────────────────────────

// SPICE output calibration from:
// https://blog.tensorflow.org/2020/06/estimating-pitch-with-spice-and-tensorflow-hub.html
const PT_OFFSET = 25.58;
const PT_SLOPE = 63.07;

// Reject frames where SPICE's uncertainty is high. TF blog suggests 0.9 for
// clean offline audio; live mic input has lower confidence on real singing
// (breath, vibrato, consonants) so we relax to 0.75. Lower = more notes
// detected, with occasional wrong notes sneaking in.
const CONF_THRESHOLD = 0.75;

// C2–F#6: practical bass-profundo through soprano modal-voice range.
const HZ_LOW = 65;
const HZ_HIGH = 1500;

// SPICE expects exactly 1024 samples at 16 kHz.
const SPICE_INPUT_SIZE = 1024;
const SPICE_INPUT_RATE = 16000;

// 3-frame trailing median to suppress single-frame jitter. SPICE doesn't have
// NSDF's subharmonic bias so we don't need the 9-frame window the old filter used.
const SMOOTH_WINDOW = 3;

// RMS energy floor (linear, on [-1, 1] samples). Below this the frame is
// almost certainly room tone / silence — skip SPICE entirely instead of
// letting it emit a low-confidence noise pitch. ~ -45 dBFS.
const SILENCE_RMS = 0.005;

// How long to hold the last detected note after detection drops out. Smooths
// over brief sub-threshold gaps (consonants, vibrato dips) so the displayed
// line doesn't strobe on/off. Tuned to feel responsive without lying after
// the user has actually stopped singing.
const HOLD_MS = 140;

// ─── Model URLs (tried in order; first success wins) ─────────────────────────

const SPICE_URLS = [
  "https://tfhub.dev/google/tfjs-model/spice/2/default/1",
  "https://tfhub.dev/google/tfjs-model/spice/1/default/1",
];

// ─── Module-level model cache — load once, reuse across mounts ───────────────

let modelPromise: Promise<tf.GraphModel> | null = null;

function getModel(): Promise<tf.GraphModel> {
  if (!modelPromise) {
    modelPromise = (async () => {
      let lastErr: unknown;
      for (const url of SPICE_URLS) {
        try {
          const m = await tf.loadGraphModel(url, { fromTFHub: true });
          _isModelReady.value = true;
          return m;
        } catch (e) {
          console.warn(`[spice] load failed for ${url}:`, e);
          lastErr = e;
        }
      }
      // Reset so the next mount can retry after a network blip.
      modelPromise = null;
      throw new Error(
        `SPICE load failed: ${(lastErr as Error)?.message ?? lastErr}`,
      );
    })();
  }
  return modelPromise;
}

/** Kick off SPICE model download without starting capture. Safe to call
 *  multiple times — the underlying promise is module-cached. */
export function preloadSPICE(): Promise<tf.GraphModel> {
  return getModel();
}

// ─── Interface ────────────────────────────────────────────────────────────────

export interface UsePitchDetectionSPICE {
  currentMidi: Ref<number | null>;
  error: Ref<string | null>;
  isActive: Ref<boolean>;
  isLoading: Ref<boolean>;
  start(): Promise<void>;
  stop(): void;
}

// ─── Composable ───────────────────────────────────────────────────────────────

export function usePitchDetectionSPICE(): UsePitchDetectionSPICE {
  const currentMidi = ref<number | null>(null);
  const error = ref<string | null>(null);
  const isActive = ref(false);
  const isLoading = ref(false);

  let rafId: number | null = null;
  let audioCtx: AudioContext | null = null;
  let mediaStream: MediaStream | null = null;
  let model: tf.GraphModel | null = null;

  // Trailing window for median smoothing; holds MIDI values or null for silent frames.
  const smoothBuf: (number | null)[] = [];

  function spiceToHz(normalizedPitch: number): number {
    const cqtBin = normalizedPitch * PT_SLOPE + PT_OFFSET;
    return 10.0 * Math.pow(2.0, cqtBin / 12.0);
  }

  async function start(): Promise<void> {
    if (isActive.value) return;
    error.value = null;
    isLoading.value = true;

    try {
      model = await getModel();
    } catch (e) {
      error.value = `Could not load pitch detector — check connection`;
      isLoading.value = false;
      return;
    }

    let stream: MediaStream;
    try {
      // Disable browser processing that destroys pitch accuracy.
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
    audioCtx = new AudioContext({ latencyHint: "interactive" });

    // Compute the smallest power-of-2 raw frame size such that after decimating
    // to 16 kHz we have at least SPICE_INPUT_SIZE samples.
    const decimationRatio = audioCtx.sampleRate / SPICE_INPUT_RATE;
    const rawFrameSize = Math.pow(
      2,
      Math.ceil(Math.log2(SPICE_INPUT_SIZE * decimationRatio)),
    );

    const analyser = audioCtx.createAnalyser();
    analyser.fftSize = rawFrameSize;
    analyser.smoothingTimeConstant = 0;

    const source = audioCtx.createMediaStreamSource(stream);
    // Intentionally not connecting analyser to destination — we don't want mic playback.
    source.connect(analyser);

    const rawBuf = new Float32Array(rawFrameSize);
    const downBuf = new Float32Array(SPICE_INPUT_SIZE);

    isActive.value = true;
    isLoading.value = false;

    let frameCount = 0;
    let lastDetectedMidi: number | null = null;
    let lastDetectedAt = 0;

    async function tick(): Promise<void> {
      if (!isActive.value) return;

      analyser.getFloatTimeDomainData(rawBuf);

      // Nearest-neighbor decimation from native sample rate to 16 kHz.
      for (let i = 0; i < SPICE_INPUT_SIZE; i++) {
        downBuf[i] = rawBuf[Math.round(i * decimationRatio)];
      }

      // RMS gate: skip silent frames before paying SPICE inference cost and
      // before they can poison the smoothing buffer with noise pitches.
      let sumSq = 0;
      for (let i = 0; i < SPICE_INPUT_SIZE; i++) {
        sumSq += downBuf[i] * downBuf[i];
      }
      const rms = Math.sqrt(sumSq / SPICE_INPUT_SIZE);

      let midi: number | null = null;

      if (rms >= SILENCE_RMS) {
        const inputTensor = tf.tensor(downBuf); // shape [1024]
        try {
          // executeAsync is the safe default — SPICE may include dynamic ops.
          // Named input matches magenta-js spice/pitch_utils.ts reference implementation.
          const outputs = (await model!.executeAsync({
            input_audio_samples: inputTensor,
          })) as tf.Tensor[];
          // SPICE output order from magenta-js spice/pitch_utils.ts: [uncertainty, pitch].
          const [uncData, pitchData] = await Promise.all([
            outputs[0].data() as Promise<Float32Array>, // SPICE: [0] = uncertainty
            outputs[1].data() as Promise<Float32Array>, // SPICE: [1] = pitch
          ]);
          tf.dispose([inputTensor, ...outputs]);

          // SPICE returns one value per chunk when given a single 1024-sample input.
          const confidence = 1 - uncData[0];
          const hz = spiceToHz(pitchData[0]);

          if (confidence >= CONF_THRESHOLD && hz > HZ_LOW && hz < HZ_HIGH) {
            midi = hzToMidi(hz);
          }

          if (frameCount === 0) {
            console.debug("[spice] first inference:", { confidence, hz, midi });
          }
          frameCount++;
        } catch (e) {
          tf.dispose(inputTensor);
          // Transient inference errors shouldn't kill the loop — just emit null for the frame.
          console.warn("[spice] inference error:", e);
        }
      }

      // Trailing 3-frame median.
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
      // doesn't strobe on/off during vibrato dips, consonants, etc. Only
      // applies while the mic still has energy — true silence drops out
      // immediately via the RMS gate above (midi stays null, smoothBuf
      // empties within SMOOTH_WINDOW frames, hold expires after HOLD_MS).
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

      rafId = requestAnimationFrame(() => void tick());
    }

    rafId = requestAnimationFrame(() => void tick());
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
    smoothBuf.length = 0;
    currentMidi.value = null;
    isActive.value = false;
    isLoading.value = false;
  }

  return { currentMidi, error, isActive, isLoading, start, stop };
}

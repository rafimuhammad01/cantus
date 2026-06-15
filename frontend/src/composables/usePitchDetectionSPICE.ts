import { ref, type Ref } from "vue";
import * as tf from "@tensorflow/tfjs";
import { hzToMidi } from "@/utils/pitch";

// ─── Module-level ready flag — true once the model has loaded successfully ────

const _isModelReady = ref(false);
/** Reactive ref: true once the SPICE model has finished loading. */
export const isModelReady = _isModelReady;

// Diagnostic: which phase of the load are we in? Surfaced to UI so we can
// tell where iOS Safari hangs without remote-inspecting.
const _loadStep = ref<string>("idle");
export const loadStep = _loadStep;

// ─── Constants ────────────────────────────────────────────────────────────────

// SPICE output calibration from:
// https://blog.tensorflow.org/2020/06/estimating-pitch-with-spice-and-tensorflow-hub.html
const PT_OFFSET = 25.58;
const PT_SLOPE = 63.07;

// Reject frames where SPICE's uncertainty is high. TF blog suggests 0.9 for
// clean offline audio; 0.75 let noise frames sneak through and produce sustained
// high-pitch spikes that the median couldn't kill. 0.85 trades a small drop in
// recall for substantially fewer noise pitches in real-room conditions.
const CONF_THRESHOLD = 0.78;

// C2–C6: practical bass-profundo through soprano modal-voice. The old upper
// bound of 1500 Hz (≈F#6) covered whistle-register / extreme belt — almost
// never the user, almost always a noise spike or octave-doubling error.
const HZ_LOW = 65;
const HZ_HIGH = 1050;

// SPICE expects exactly 1024 samples at 16 kHz.
const SPICE_INPUT_SIZE = 1024;
const SPICE_INPUT_RATE = 16000;

// 5-frame trailing median. Noise spikes that pass the confidence + RMS gates
// tend to sustain across 2–3 frames; a 3-frame window can be overwhelmed by
// them. Widening to 5 reliably suppresses 2-frame bursts at the cost of a
// small added lag (~50ms at SPICE's frame rate).
const SMOOTH_WINDOW = 5;

// RMS energy floor (linear, on [-1, 1] samples). ~-38 dBFS. The old -45 dBFS
// floor passed quiet room tone through, which SPICE then tried to fit a pitch
// to — producing the high-pitch jumps the user reported. Singing above the
// noise floor will easily clear -38 dBFS.
const SILENCE_RMS = 0.007;

// Reject frames whose detected MIDI is more than this many semitones above
// the last stable detection. SPICE octave-doubles on noisy / breathy frames
// (e.g. a sung A3 momentarily reads as A4 or higher). Real sung intervals
// within a phrase are almost always under an octave; anything larger is
// almost certainly a doubling error.
const OCTAVE_JUMP_LIMIT = 12;

// How long to hold the last detected note after detection drops out. Smooths
// over brief sub-threshold gaps (consonants, vibrato dips) so the displayed
// line doesn't strobe on/off. Tuned to feel responsive without lying after
// the user has actually stopped singing.
const HOLD_MS = 140;

// ─── Model URL ───────────────────────────────────────────────────────────────

// Self-hosted SPICE v2 in frontend/public/spice-model/. We vendored the model
// because Safari rejects tfhub.dev's CORS headers on the model fetch — Chrome
// is more permissive. Same-origin avoids the issue entirely and removes the
// network dependency at first run.
const SPICE_URL = "/spice-model/model.json";

// ─── Module-level model cache — load once, reuse across mounts ───────────────

let modelPromise: Promise<tf.GraphModel> | null = null;

function getModel(): Promise<tf.GraphModel> {
  if (!modelPromise) {
    modelPromise = (async () => {
      try {
        _loadStep.value = "tf-ready";
        console.log("[spice] step: tf.ready()");
        await withTimeout(tf.ready(), 10000, "tf.ready timeout");
        const backend = tf.getBackend();
        _loadStep.value = `backend:${backend}`;
        console.log("[spice] backend:", backend);

        _loadStep.value = "fetch-json";
        console.log("[spice] step: fetch model.json");
        const probe = await withTimeout(
          fetch(SPICE_URL, { method: "GET" }),
          10000,
          "model.json fetch timeout",
        );
        if (!probe.ok) {
          throw new Error(`model.json HTTP ${probe.status}`);
        }

        _loadStep.value = "load-graph";
        console.log("[spice] step: tf.loadGraphModel");
        const m = await withTimeout(
          tf.loadGraphModel(SPICE_URL),
          30000,
          "loadGraphModel timeout",
        );
        _loadStep.value = "ready";
        _isModelReady.value = true;
        return m;
      } catch (e) {
        const msg = (e as Error)?.message ?? String(e);
        console.warn(`[spice] load failed at step=${_loadStep.value}:`, e);
        _loadStep.value = `error:${msg}`;
        // Reset so the next mount can retry after a network blip.
        modelPromise = null;
        throw new Error(`SPICE load failed (${_loadStep.value}): ${msg}`);
      }
    })();
  }
  return modelPromise;
}

function withTimeout<T>(p: Promise<T>, ms: number, label: string): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const id = setTimeout(() => reject(new Error(label)), ms);
    p.then(
      (v) => {
        clearTimeout(id);
        resolve(v);
      },
      (e) => {
        clearTimeout(id);
        reject(e);
      },
    );
  });
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
  let sinkAudioEl: HTMLAudioElement | null = null;
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

    let stream: MediaStream;
    try {
      // Call getUserMedia FIRST (before any other await) so Safari's
      // user-gesture authority is consumed immediately. Chaining awaits before
      // this can silently drop the gesture and Safari refuses the mic without
      // throwing. Disable browser audio processing — echoCancellation /
      // noiseSuppression / AGC filter the spectrum SPICE relies on.
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

    // Now load the model. If preloaded (typical), this resolves instantly.
    try {
      model = await getModel();
    } catch (e) {
      stream.getTracks().forEach((t) => t.stop());
      error.value = `Could not load pitch detector — check connection`;
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
      console.warn("[spice] sink <audio> play() failed:", e);
    }
    sinkAudioEl = sinkEl;

    audioCtx = new AudioContext({ latencyHint: "interactive" });
    // Safari/iOS often start the context in "suspended" state even when created
    // from a user gesture — explicit resume() is needed before audio flows.
    if (audioCtx.state === "suspended") {
      try {
        await audioCtx.resume();
      } catch (e) {
        console.warn("[spice] audioCtx.resume() failed:", e);
      }
    }

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
    source.connect(analyser);
    // Safari only processes nodes whose graph reaches destination. Without this
    // sink, getFloatTimeDomainData returns all zeros (Chrome processes "dangling"
    // graphs; Safari doesn't). Mute via gain=0 so the user never hears their mic.
    const silentSink = audioCtx.createGain();
    silentSink.gain.value = 0;
    analyser.connect(silentSink);
    silentSink.connect(audioCtx.destination);

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
          // executeAsync is the safe default — Safari's TFJS backend doesn't
          // always honor synchronous execute() even on graphs without control
          // flow. The "model.execute() instead" warning is cosmetic here.
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
            const candidate = hzToMidi(hz);
            // Octave-jump guard: when we already have a recent stable note,
            // reject candidates more than OCTAVE_JUMP_LIMIT semitones above it.
            // Doesn't gate downward jumps since real phrases can drop fast
            // (sustained note → low consonant); upward sudden jumps are the
            // noise-doubling failure mode the user reported.
            if (
              lastDetectedMidi !== null &&
              candidate - lastDetectedMidi > OCTAVE_JUMP_LIMIT
            ) {
              midi = null;
            } else {
              midi = candidate;
            }
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

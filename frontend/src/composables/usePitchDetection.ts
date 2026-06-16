import { ref, type Ref } from "vue";
import * as ort from "onnxruntime-web/wasm";
import wasmUrl from "onnxruntime-web/ort-wasm-simd-threaded.wasm?url";
import { hzToMidi } from "@/utils/pitch";

// ─── SwiftF0 neural pitch detector via ONNX Runtime Web ──────────────────────
//
// SwiftF0 is a 95K-parameter CNN trained on diverse noisy speech/music data
// for monophonic F0 estimation (Nieradzik 2025, arXiv:2508.18440). The model
// takes raw 16 kHz mono audio and outputs pitch_hz + confidence per frame.
// All preprocessing (STFT with Hann window 1024 / hop 256, frequency-band
// trim) is baked INSIDE the ONNX graph — we only resample to 16 kHz.
//
// No external pre/post processing. The model is trained specifically for
// noise robustness; adding HZ guards, medians, octave guards, etc. before
// confirming behavior would mask whatever the model actually does.
//
// Runs on the WASM execution provider — no WebGL dependency, so unlike
// SPICE/TFJS it cannot hit the iOS Safari WebGL weight-upload hang.

// Vite hashes the .wasm filename in production, so we hand ORT the actual
// URL via Vite's ?url import. Map the standard logical name to the real
// hashed path.
ort.env.wasm.wasmPaths = { wasm: wasmUrl };
// Single-threaded to avoid SharedArrayBuffer / COOP-COEP requirements.
ort.env.wasm.numThreads = 1;

const MODEL_URL = "/swift-f0/model.onnx";
const TARGET_SR = 16000;

// SwiftF0 library defaults from lars76/swift-f0 core.py:
//   DEFAULT_CONFIDENCE_THRESHOLD = 0.9
//   MODEL_FMIN = 46.875  (model's trained lower bound)
//   MODEL_FMAX = 2093.75 (model's trained upper bound)
// Voicing logic in the library: voiced = conf > thresh AND fmin <= hz <= fmax
//
// Library default 0.9 is conservative — designed for batch processing of
// clean studio recordings. For real-time mic input we want fewer false
// negatives (missing voice) at the cost of a few more noisy frames.
const CONFIDENCE_THRESHOLD = 0.8;
const FMIN = 46.875;
const FMAX = 2093.75;
// Pull a healthy buffer per RAF tick. At 48 kHz native, 8192 samples
// ≈ 170 ms of audio, which after resampling to 16 kHz gives ~2731 samples
// (≈ 10 SwiftF0 frames at hop=256). We take the last frame's output.
const NATIVE_FRAME_SIZE = 8192;

// Module-cached session — load model + warm up once across mounts.
let sessionPromise: Promise<ort.InferenceSession> | null = null;
function loadSession(): Promise<ort.InferenceSession> {
  if (!sessionPromise) {
    sessionPromise = ort.InferenceSession.create(MODEL_URL, {
      executionProviders: ["wasm"],
      graphOptimizationLevel: "all",
    });
  }
  return sessionPromise;
}

export interface UsePitchDetection {
  currentMidi: Ref<number | null>;
  error: Ref<string | null>;
  isActive: Ref<boolean>;
  isLoading: Ref<boolean>;
  start(): Promise<void>;
  stop(): void;
}

export function usePitchDetection(): UsePitchDetection {
  const currentMidi = ref<number | null>(null);
  const error = ref<string | null>(null);
  const isActive = ref(false);
  const isLoading = ref(false);

  let rafId: number | null = null;
  let audioCtx: AudioContext | null = null;
  let mediaStream: MediaStream | null = null;
  let sinkAudioEl: HTMLAudioElement | null = null;

  async function start(): Promise<void> {
    if (isActive.value) return;
    error.value = null;
    isLoading.value = true;

    let stream: MediaStream;
    try {
      stream = await navigator.mediaDevices.getUserMedia({
        audio: {
          echoCancellation: true,
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

    let session: ort.InferenceSession;
    try {
      session = await loadSession();
    } catch (e) {
      stream.getTracks().forEach((t) => t.stop());
      error.value = `SwiftF0 failed to load: ${(e as Error)?.message ?? e}`;
      isLoading.value = false;
      return;
    }

    mediaStream = stream;

    // Safari MediaStream pump.
    const sinkEl = document.createElement("audio");
    sinkEl.srcObject = stream;
    sinkEl.muted = true;
    try {
      await sinkEl.play();
    } catch (e) {
      console.warn("[swiftf0] sink <audio> play() failed:", e);
    }
    sinkAudioEl = sinkEl;

    audioCtx = new AudioContext({ latencyHint: "interactive" });
    if (audioCtx.state === "suspended") {
      try {
        await audioCtx.resume();
      } catch (e) {
        console.warn("[swiftf0] audioCtx.resume() failed:", e);
      }
    }

    const analyser = audioCtx.createAnalyser();
    analyser.fftSize = NATIVE_FRAME_SIZE;
    analyser.smoothingTimeConstant = 0;

    const source = audioCtx.createMediaStreamSource(stream);
    source.connect(analyser);
    const silentSink = audioCtx.createGain();
    silentSink.gain.value = 0;
    analyser.connect(silentSink);
    silentSink.connect(audioCtx.destination);

    const nativeBuf = new Float32Array(NATIVE_FRAME_SIZE);

    // Precompute the decimation ratio. At 48 kHz native to 16 kHz target,
    // ratio is exactly 3 — naive nearest-neighbor decimation is fine for a
    // CNN trained on diverse noise.
    const decimRatio = audioCtx.sampleRate / TARGET_SR;
    const resampledLen = Math.floor(NATIVE_FRAME_SIZE / decimRatio);
    const resampled = new Float32Array(resampledLen);

    isActive.value = true;
    isLoading.value = false;

    // Inference is ~3 ms but we still don't want overlapping calls.
    let inflight = false;

    async function tick(): Promise<void> {
      if (!isActive.value) return;

      analyser.getFloatTimeDomainData(nativeBuf);

      if (!inflight) {
        inflight = true;

        // Nearest-neighbor decimation 48 kHz → 16 kHz.
        for (let i = 0; i < resampledLen; i++) {
          resampled[i] = nativeBuf[Math.round(i * decimRatio)];
        }

        const input = new ort.Tensor("float32", resampled, [1, resampledLen]);
        try {
          const outputs = await session.run({
            [session.inputNames[0]]: input,
          });
          // Model outputs: pitch_hz array, confidence array, both shape (1, N_frames).
          const pitchTensor = outputs[session.outputNames[0]];
          const confTensor = outputs[session.outputNames[1]];
          const pitchData = pitchTensor.data as Float32Array;
          const confData = confTensor.data as Float32Array;
          // Take the latest frame.
          const lastIdx = pitchData.length - 1;
          const hz = pitchData[lastIdx];
          const conf = confData[lastIdx];

          // Apply SwiftF0's own voicing logic verbatim.
          const voiced =
            conf > CONFIDENCE_THRESHOLD &&
            isFinite(hz) &&
            hz >= FMIN &&
            hz <= FMAX;
          currentMidi.value = voiced ? hzToMidi(hz) : null;
        } catch (e) {
          console.warn("[swiftf0] inference error:", e);
        } finally {
          inflight = false;
        }
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
    currentMidi.value = null;
    isActive.value = false;
    isLoading.value = false;
  }

  return { currentMidi, error, isActive, isLoading, start, stop };
}

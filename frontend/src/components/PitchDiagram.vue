<script setup lang="ts">
import { ref, computed, onMounted, onBeforeUnmount } from "vue";
import { usePitchDetection } from "@/composables/usePitchDetection";
import { usePitchStore } from "@/stores/pitch";
import { hzToMidi, midiToNoteName } from "@/utils/pitch";
import type { MelodyResponse } from "@/services/api";

const props = withDefaults(
  defineProps<{
    audioEl: HTMLAudioElement;
    melody: MelodyResponse;
    vocalOctaveShift?: number; // default 0; offsets target MIDI at setup time
    fill?: boolean; // when true the diagram fills its parent container vertically
  }>(),
  { fill: false },
);

// ─── Constants ───────────────────────────────────────────────────────────────

const WINDOW_SECONDS = 10;
const SVG_HEIGHT = ref(320);
const Y_AXIS_WIDTH = 44;
const TOP_PAD = 8;
const BOTTOM_PAD = 8;

// Live pitch bar: a thin vertical level meter between the y-axis labels and
// the diagram area, filled from the bottom up to the user's current pitch.
const PITCH_BAR_X = 32;
const PITCH_BAR_WIDTH = 8;

// ─── One-time precomputation from melody (stable for component lifetime) ─────

interface TargetFrame {
  t: number;
  midi: number | null;
}

// Bridge brief unvoiced gaps (consonants, breaths, transient confidence dips) so a
// continuous sung phrase renders as one line, not 5 disconnected fragments.
// Ports the prototype's `series.ffill(limit=12).rolling(window=3, min_periods=1, center=True).median()`
// (audio_renderer.py:36). 12 frames at melody.hop_ms ~= 600ms of bridge.
const FFILL_LIMIT_FRAMES = 12;
const SMOOTH_WINDOW = 3;

function ffillAndSmooth(series: TargetFrame[]): TargetFrame[] {
  const filled = series.map((f) => ({ t: f.t, midi: f.midi }));
  let lastValid: number | null = null;
  let gap = 0;
  for (let i = 0; i < filled.length; i++) {
    if (filled[i].midi !== null) {
      lastValid = filled[i].midi;
      gap = 0;
    } else if (lastValid !== null && gap < FFILL_LIMIT_FRAMES) {
      filled[i].midi = lastValid;
      gap++;
    }
  }

  // 3-frame centered median over the filled series — small bump-removal.
  const smoothed: TargetFrame[] = new Array(filled.length);
  const half = Math.floor(SMOOTH_WINDOW / 2);
  for (let i = 0; i < filled.length; i++) {
    const window: number[] = [];
    for (
      let j = Math.max(0, i - half);
      j <= Math.min(filled.length - 1, i + half);
      j++
    ) {
      if (filled[j].midi !== null) window.push(filled[j].midi!);
    }
    if (window.length === 0) {
      smoothed[i] = { t: filled[i].t, midi: null };
    } else {
      window.sort((a, b) => a - b);
      const mid = Math.floor(window.length / 2);
      const median =
        window.length % 2 === 1
          ? window[mid]
          : (window[mid - 1] + window[mid]) / 2;
      smoothed[i] = { t: filled[i].t, midi: median };
    }
  }
  return smoothed;
}

const shift = props.vocalOctaveShift ?? 0;
const targetSeries: TargetFrame[] = ffillAndSmooth(
  props.melody.frames
    .map(([t_ms, hz]) => ({
      t: t_ms / 1000,
      midi: hz > 0 ? hzToMidi(hz) + shift : null,
    }))
    .sort((a, b) => a.t - b.t),
);

const voicedTargets = targetSeries.filter(
  (f): f is { t: number; midi: number } => f.midi !== null,
);
const voicedTimes: number[] = voicedTargets.map((f) => f.t);
const voicedMidis: number[] = voicedTargets.map((f) => f.midi);

const yMin = ref<number>(
  voicedMidis.length === 0 ? 48 : Math.floor(Math.min(...voicedMidis)) - 1,
);
const yMax = ref<number>(
  voicedMidis.length === 0 ? 72 : Math.ceil(Math.max(...voicedMidis)) + 1,
);

const MIN_Y_SPAN = 24;
const LERP_RATE = 0.03;
const SNAP_THRESHOLD = 0.1;

function computeDesiredBounds(): { lo: number; hi: number } {
  const t0 = now.value - WINDOW_SECONDS / 2;
  const t1 = now.value + WINDOW_SECONDS / 2;
  let lo = Infinity,
    hi = -Infinity;

  for (const f of targetSeries) {
    if (f.midi === null) continue;
    if (f.t < t0 || f.t > t1) continue;
    if (f.midi < lo) lo = f.midi;
    if (f.midi > hi) hi = f.midi;
  }

  const times = pitchStore.userTimes;
  const midis = pitchStore.userMidis;
  for (let i = times.length - 1; i >= 0; i--) {
    if (times[i] < t0) break;
    const m = midis[i];
    if (m === null || !isFinite(m)) continue;
    if (m < lo) lo = m;
    if (m > hi) hi = m;
  }

  if (!isFinite(lo) || !isFinite(hi)) {
    return { lo: yMin.value, hi: yMax.value };
  }

  lo = Math.floor(lo) - 1;
  hi = Math.ceil(hi) + 1;
  const span = hi - lo;
  if (span < MIN_Y_SPAN) {
    const grow = (MIN_Y_SPAN - span) / 2;
    lo -= grow;
    hi += grow;
  }
  return { lo, hi };
}

// ─── Interpolation helper (mirrors np.interp with NaN edges) ─────────────────

function interpolateTargetMidi(t: number): number | null {
  if (voicedTimes.length === 0) return null;
  if (t < voicedTimes[0] || t > voicedTimes[voicedTimes.length - 1])
    return null;
  let lo = 0,
    hi = voicedTimes.length;
  while (lo < hi) {
    const mid = (lo + hi) >>> 1;
    if (voicedTimes[mid] < t) lo = mid + 1;
    else hi = mid;
  }
  if (voicedTimes[lo] === t) return voicedMidis[lo];
  const t1 = voicedTimes[lo - 1],
    t2 = voicedTimes[lo];
  const m1 = voicedMidis[lo - 1],
    m2 = voicedMidis[lo];
  return m1 + ((m2 - m1) * (t - t1)) / (t2 - t1);
}

// ─── Reactive state ───────────────────────────────────────────────────────────

const pitchDetection = usePitchDetection();
const pitchStore = usePitchStore();
const svgEl = ref<SVGSVGElement | null>(null);
const svgWrapEl = ref<HTMLDivElement | null>(null);
const svgWidth = ref(600);
const now = ref(0);
const showHeadphonesTip = ref(false);

// ─── Scaling helpers ──────────────────────────────────────────────────────────

function xScale(t: number): number {
  const tStart = now.value - WINDOW_SECONDS / 2;
  const tEnd = now.value + WINDOW_SECONDS / 2;
  return (
    Y_AXIS_WIDTH +
    ((t - tStart) / (tEnd - tStart)) * (svgWidth.value - Y_AXIS_WIDTH)
  );
}

function yScale(midi: number): number {
  const drawH = SVG_HEIGHT.value - TOP_PAD - BOTTOM_PAD;
  return (
    TOP_PAD + drawH * (1 - (midi - yMin.value) / (yMax.value - yMin.value))
  );
}

// ─── Y-axis ticks ─────────────────────────────────────────────────────────────

const yTicks = computed<number[]>(() => {
  const ticks: number[] = [];
  for (let m = Math.ceil(yMin.value); m <= Math.floor(yMax.value); m++)
    ticks.push(m);
  return ticks;
});

// ─── Polyline segment builder: splits on null into separate points strings ────

function polylineSegments(
  samples: TargetFrame[],
  nowTime: number,
  width: number,
): string[] {
  const segments: string[] = [];
  let current: string[] = [];
  for (const s of samples) {
    if (s.midi === null) {
      if (current.length >= 2) segments.push(current.join(" "));
      current = [];
    } else {
      current.push(`${xScale_w(s.t, nowTime, width)},${yScale(s.midi)}`);
    }
  }
  if (current.length >= 2) segments.push(current.join(" "));
  return segments;
}

// xScale variant that takes explicit now/width (computed context can't use reactive now directly in a helper)
function xScale_w(t: number, nowTime: number, width: number): number {
  const tStart = nowTime - WINDOW_SECONDS / 2;
  const tEnd = nowTime + WINDOW_SECONDS / 2;
  return (
    Y_AXIS_WIDTH + ((t - tStart) / (tEnd - tStart)) * (width - Y_AXIS_WIDTH)
  );
}

// ─── Target polyline segments (past / future), updated when now or width changes

const targetSegmentsPast = computed<string[]>(() => {
  const n = now.value;
  const w = svgWidth.value;
  const tStart = n - WINDOW_SECONDS / 2;
  const slice = targetSeries.filter((f) => f.t >= tStart && f.t <= n);
  return polylineSegments(slice, n, w);
});

const targetSegmentsFuture = computed<string[]>(() => {
  const n = now.value;
  const w = svgWidth.value;
  const tEnd = n + WINDOW_SECONDS / 2;
  const slice = targetSeries.filter((f) => f.t > n && f.t <= tEnd);
  return polylineSegments(slice, n, w);
});

// ─── User line segments, color-coded by pitch accuracy ───────────────────────

interface UserSegment {
  x1: number;
  y1: number;
  x2: number;
  y2: number;
  color: string;
}

// Color bands based on pitch perception research (Vurma & Ross 2006 J. Voice
// "in tune" perception ±20–30 cents; karaoke convention ±50 cents acceptable;
// >100 cents = audibly wrong note). The 1-semitone green band accounts for
// the 144ms median smoother's lag during a sung pitch transition.
function segmentColor(userMidi: number, t: number): string {
  const target = interpolateTargetMidi(t);
  if (target === null) return "#e8a87c";
  const diff = Math.abs(userMidi - target);
  if (diff <= 1.0) return "#7a9e7e"; // sage: on pitch
  if (diff <= 2.0) return "#e8a87c"; // amber: near
  return "#c98a8a"; // muted danger: off
}

const userSegments = computed<UserSegment[]>(() => {
  const n = now.value;
  const w = svgWidth.value;
  const tStart = n - WINDOW_SECONDS / 2;
  const tEnd = n + WINDOW_SECONDS / 2;
  const times = pitchStore.userTimes;
  const midis = pitchStore.userMidis;

  // Slice to visible window (store times are sorted ascending)
  let lo = 0;
  while (lo < times.length && times[lo] < tStart) lo++;
  let hi = times.length - 1;
  while (hi > lo && times[hi] > tEnd) hi--;

  const segments: UserSegment[] = [];
  for (let i = lo; i < hi; i++) {
    const m1 = midis[i],
      m2 = midis[i + 1];
    if (m1 === null || m2 === null || !isFinite(m1) || !isFinite(m2)) continue;
    segments.push({
      x1: xScale_w(times[i], n, w),
      y1: yScale(m1),
      x2: xScale_w(times[i + 1], n, w),
      y2: yScale(m2),
      color: segmentColor(m1, times[i]),
    });
  }
  return segments;
});

// ─── rAF loop ─────────────────────────────────────────────────────────────────

let rafId: number | null = null;

function tick(): void {
  now.value = props.audioEl.currentTime;

  if (!props.audioEl.paused && pitchDetection.isActive.value) {
    pitchStore.recordSample(
      props.audioEl.currentTime,
      pitchDetection.currentMidi.value,
      interpolateTargetMidi(props.audioEl.currentTime),
    );
  }

  const desired = computeDesiredBounds();

  const newYMin = yMin.value + (desired.lo - yMin.value) * LERP_RATE;
  yMin.value =
    Math.abs(newYMin - Math.round(newYMin)) < SNAP_THRESHOLD
      ? Math.round(newYMin)
      : newYMin;

  const newYMax = yMax.value + (desired.hi - yMax.value) * LERP_RATE;
  yMax.value =
    Math.abs(newYMax - Math.round(newYMax)) < SNAP_THRESHOLD
      ? Math.round(newYMax)
      : newYMax;

  rafId = requestAnimationFrame(tick);
}

// ─── Mic control ─────────────────────────────────────────────────────────────

const audioError = ref<string | null>(null);

async function startPlayAndSing(): Promise<void> {
  audioError.value = null;

  // Ask for mic FIRST. Modern browsers preserve user activation across an awaited
  // permission prompt, so we can still call .play() after — and the UX is much
  // better: no song blasting in the background while the user reads the prompt.
  // If the user denies (or the mic errors), we still play the song so they can
  // listen without singing.
  await pitchDetection.start();

  // The audio element can land in an error state if the src was swapped to a
  // URL that 404'd (e.g. transpose before generate completed). Reset it before
  // calling play() — load() clears the error and re-fetches.
  if (props.audioEl.error || props.audioEl.readyState === 0) {
    try {
      props.audioEl.load();
    } catch {
      // load() rarely throws, but if it does, fall through to play() which will
      // give us a clearer error message.
    }
  }

  try {
    await props.audioEl.play();
  } catch (e) {
    audioError.value = `Audio could not start: ${(e as Error).message ?? "unknown"}`;
    pitchDetection.stop();
    return;
  }

  pitchStore.setActive(true);
  if (!localStorage.getItem("cantus_headphones_seen")) {
    showHeadphonesTip.value = true;
    localStorage.setItem("cantus_headphones_seen", "1");
    setTimeout(() => {
      showHeadphonesTip.value = false;
    }, 6000);
  }
}

function stopPlayAndSing(): void {
  props.audioEl.pause();
  pitchDetection.stop();
  pitchStore.setActive(false);
}

async function togglePlayAndSing(): Promise<void> {
  if (pitchDetection.isActive.value) {
    stopPlayAndSing();
  } else {
    await startPlayAndSing();
  }
}

// Audio reaching the end → release the mic so we don't keep recording silence.
function onEnded(): void {
  stopPlayAndSing();
}

// ─── Lifecycle ────────────────────────────────────────────────────────────────

let resizeObserver: ResizeObserver | null = null;

onMounted(() => {
  props.audioEl.addEventListener("ended", onEnded);
  rafId = requestAnimationFrame(tick);

  const observeTarget = props.fill ? svgWrapEl.value : svgEl.value;
  if (observeTarget) {
    resizeObserver = new ResizeObserver((entries) => {
      const entry = entries[0];
      if (!entry) return;
      svgWidth.value = entry.contentRect.width || 600;
      if (props.fill) {
        SVG_HEIGHT.value = Math.max(1, Math.floor(entry.contentRect.height));
      }
    });
    resizeObserver.observe(observeTarget);
    const rect = observeTarget.getBoundingClientRect();
    svgWidth.value = rect.width || 600;
    if (props.fill) {
      SVG_HEIGHT.value = Math.max(1, Math.floor(rect.height));
    }
  }

  // Voice/key changes remount this component while audio keeps playing in the
  // parent. Re-arm the mic so the user doesn't have to hit Play & Sing again.
  if (!props.audioEl.paused) {
    void pitchDetection.start().then(() => {
      if (!pitchDetection.error.value) pitchStore.setActive(true);
    });
  }
});

onBeforeUnmount(() => {
  if (rafId !== null) cancelAnimationFrame(rafId);
  props.audioEl.removeEventListener("ended", onEnded);
  pitchDetection.stop();
  pitchStore.reset();
  resizeObserver?.disconnect();
});
</script>

<template>
  <div
    class="rounded-2xl p-4 bg-[var(--color-surface)] border border-[var(--color-border)] overflow-hidden"
    :class="props.fill ? 'flex flex-col h-full' : ''"
  >
    <div class="flex items-center justify-between mb-3">
      <button
        @click="togglePlayAndSing"
        class="px-5 py-2 rounded-full text-sm font-medium transition-colors"
        :class="
          pitchDetection.isActive.value
            ? 'bg-[var(--color-surface-2)] text-[var(--color-text)] hover:bg-[var(--color-border)]'
            : 'bg-[var(--color-accent)] text-[#0a0a0b] hover:bg-[var(--color-accent-hover)]'
        "
      >
        {{ pitchDetection.isActive.value ? "⏸ Pause" : "▶ Play & Sing" }}
      </button>
      <span
        v-if="pitchStore.hitRate !== null"
        class="text-sm text-[var(--color-text)] tnum"
      >
        Score: {{ Math.round(pitchStore.hitRate * 100) }}%
      </span>
    </div>

    <div
      v-if="pitchDetection.error.value || audioError"
      class="mb-3 flex items-center gap-3 text-sm text-[var(--color-danger)]"
    >
      <span>{{ pitchDetection.error.value ?? audioError }}</span>
      <button
        @click="startPlayAndSing"
        class="px-3 py-1 rounded-full bg-[var(--color-surface-2)] hover:bg-[var(--color-border)] text-[var(--color-text)] text-xs transition-colors"
      >
        Retry
      </button>
    </div>

    <div
      v-if="showHeadphonesTip"
      class="mb-3 text-xs text-[var(--color-text-muted)]"
    >
      Use headphones to keep the mic from picking up the backing track.
    </div>

    <div ref="svgWrapEl" :class="props.fill ? 'flex-1 min-h-0' : ''">
      <svg
        ref="svgEl"
        :height="SVG_HEIGHT"
        :width="svgWidth"
        class="block w-full"
      >
        <!-- Y-axis labels -->
        <text
          v-for="tick in yTicks"
          :key="tick"
          :x="4"
          :y="yScale(tick) + 4"
          fill="#a8a8a0"
          font-size="10"
          font-family="Fraunces, serif"
          font-style="italic"
        >
          {{ midiToNoteName(tick) }}
        </text>

        <!-- Horizontal grid lines -->
        <line
          v-for="tick in yTicks"
          :key="`g-${tick}`"
          :x1="Y_AXIS_WIDTH"
          :x2="svgWidth"
          :y1="yScale(tick)"
          :y2="yScale(tick)"
          stroke="#24242a"
          stroke-dasharray="2,2"
        />

        <!-- Pitch level bar (vertical, fills bottom→current pitch) -->
        <rect
          :x="PITCH_BAR_X"
          :width="PITCH_BAR_WIDTH"
          :y="yScale(yMax)"
          :height="yScale(yMin) - yScale(yMax)"
          fill="#24242a"
          rx="2"
        />
        <rect
          v-if="
            pitchDetection.currentMidi.value !== null &&
            isFinite(pitchDetection.currentMidi.value)
          "
          :x="PITCH_BAR_X"
          :width="PITCH_BAR_WIDTH"
          :y="yScale(pitchDetection.currentMidi.value)"
          :height="
            Math.max(0, yScale(yMin) - yScale(pitchDetection.currentMidi.value))
          "
          fill="#e8a87c"
          opacity="0.85"
          rx="2"
        />

        <!-- Target melody: past (bright cream — the "song" line) -->
        <polyline
          v-for="(seg, i) in targetSegmentsPast"
          :key="`tp-${i}`"
          :points="seg"
          fill="none"
          stroke="#fafaf7"
          stroke-opacity="0.85"
          stroke-width="3"
          stroke-linejoin="round"
        />

        <!-- Target melody: future (dim cream) -->
        <polyline
          v-for="(seg, i) in targetSegmentsFuture"
          :key="`tf-${i}`"
          :points="seg"
          fill="none"
          stroke="#fafaf7"
          stroke-opacity="0.28"
          stroke-width="2.5"
          stroke-linejoin="round"
        />

        <!-- User pitch: color-coded segments -->
        <line
          v-for="(s, i) in userSegments"
          :key="`u-${i}`"
          :x1="s.x1"
          :y1="s.y1"
          :x2="s.x2"
          :y2="s.y2"
          :stroke="s.color"
          stroke-width="3"
          stroke-linecap="round"
        />

        <!-- Red cursor at current time -->
        <line
          :x1="xScale(now)"
          :x2="xScale(now)"
          :y1="0"
          :y2="SVG_HEIGHT"
          stroke="#fafaf7"
          stroke-opacity="0.4"
          stroke-width="1.5"
        />

        <!-- Edge arrow: shown when user pitch is outside the visible Y range -->
        <template
          v-if="
            pitchDetection.currentMidi.value !== null &&
            isFinite(pitchDetection.currentMidi.value)
          "
        >
          <!-- Above yMax: chevron at top-right pointing up -->
          <template v-if="pitchDetection.currentMidi.value > yMax">
            <polygon
              :points="`${svgWidth - 28},${TOP_PAD + 14} ${svgWidth - 20},${TOP_PAD + 2} ${svgWidth - 12},${TOP_PAD + 14}`"
              fill="#e8a87c"
            />
            <text
              :x="svgWidth - 20"
              :y="TOP_PAD + 28"
              text-anchor="middle"
              fill="#e8a87c"
              font-size="10"
              font-family="monospace"
            >
              +{{ Math.round(pitchDetection.currentMidi.value - yMax) }}st
            </text>
          </template>

          <!-- Below yMin: chevron at bottom-right pointing down -->
          <template v-else-if="pitchDetection.currentMidi.value < yMin">
            <polygon
              :points="`${svgWidth - 28},${SVG_HEIGHT - BOTTOM_PAD - 14} ${svgWidth - 20},${SVG_HEIGHT - BOTTOM_PAD - 2} ${svgWidth - 12},${SVG_HEIGHT - BOTTOM_PAD - 14}`"
              fill="#e8a87c"
            />
            <text
              :x="svgWidth - 20"
              :y="SVG_HEIGHT - BOTTOM_PAD - 18"
              text-anchor="middle"
              fill="#e8a87c"
              font-size="10"
              font-family="monospace"
            >
              -{{ Math.round(yMin - pitchDetection.currentMidi.value) }}st
            </text>
          </template>
        </template>
      </svg>
    </div>
  </div>
</template>

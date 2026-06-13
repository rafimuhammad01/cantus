import { defineStore } from "pinia";
import { ref, computed } from "vue";

// Left-bound binary search, mirrors numpy searchsorted default.
function searchsorted(arr: number[], target: number): number {
  let lo = 0,
    hi = arr.length;
  while (lo < hi) {
    const mid = (lo + hi) >>> 1;
    if (arr[mid] < target) lo = mid + 1;
    else hi = mid;
  }
  return lo;
}

export const usePitchStore = defineStore("pitch", () => {
  // State
  const userTimes = ref<number[]>([]);
  const userMidis = ref<(number | null)[]>([]);
  const frameHits = ref(0);
  const frameTotal = ref(0);
  const currentMidi = ref<number | null>(null);
  const isActive = ref(false);

  // Null until frameTotal > 30 — avoids a junk score at song start (mirrors prototype audio_renderer.py:400).
  const hitRate = computed<number | null>(() => {
    if (frameTotal.value <= 30) return null;
    return frameHits.value / frameTotal.value;
  });

  // Samples within this many seconds of an existing sample are treated as the
  // same frame and overwrite it. rAF tick rate is ~16ms; SPICE frame rate ~32ms.
  // 25ms sits between, so a second pass through the same time region replaces
  // the prior pitch instead of inserting a near-duplicate.
  const SAMPLE_EPSILON = 0.025;

  /** smoothedUserMidi is the post-median value from PitchFilter. */
  function recordSample(
    t: number,
    smoothedUserMidi: number | null,
    targetMidi: number | null,
  ): void {
    currentMidi.value = smoothedUserMidi;

    const userFinite =
      smoothedUserMidi !== null && !Number.isNaN(smoothedUserMidi);
    const targetFinite = targetMidi !== null && !Number.isNaN(targetMidi);
    const countable = userFinite && targetFinite;
    // Hit = "right note" + "adjacent close" bands from the diagram (green or
    // yellow). Matches PitchDiagram's segmentColor logic for consistency.
    const isHit = countable && Math.abs(smoothedUserMidi! - targetMidi!) <= 2.0;

    const times = userTimes.value;
    const midis = userMidis.value;

    // Fast path: appending past the end (typical forward playback).
    if (times.length === 0 || t > times[times.length - 1] + SAMPLE_EPSILON) {
      times.push(t);
      midis.push(smoothedUserMidi);
      if (countable) {
        frameTotal.value++;
        if (isHit) frameHits.value++;
      }
      return;
    }

    // Out-of-order sample (user seeked back and resumed). Find the closest
    // existing slot; overwrite if within epsilon, else splice in.
    const i = searchsorted(times, t);
    const leftDist = i > 0 ? t - times[i - 1] : Infinity;
    const rightDist = i < times.length ? times[i] - t : Infinity;
    const overwriteIdx =
      leftDist <= rightDist
        ? leftDist <= SAMPLE_EPSILON
          ? i - 1
          : -1
        : rightDist <= SAMPLE_EPSILON
          ? i
          : -1;

    if (overwriteIdx >= 0) {
      midis[overwriteIdx] = smoothedUserMidi;
    } else {
      times.splice(i, 0, t);
      midis.splice(i, 0, smoothedUserMidi);
    }
    // Counters are cumulative across attempts — every recorded sample counts
    // toward hit rate, including overwrites. Aligns with the existing intent
    // that hit/total survive any array mutation.
    if (countable) {
      frameTotal.value++;
      if (isHit) frameHits.value++;
    }
  }

  function reset(): void {
    userTimes.value = [];
    userMidis.value = [];
    frameHits.value = 0;
    frameTotal.value = 0;
    currentMidi.value = null;
    // isActive intentionally not changed
  }

  function setActive(value: boolean): void {
    isActive.value = value;
  }

  return {
    userTimes,
    userMidis,
    frameHits,
    frameTotal,
    currentMidi,
    isActive,
    hitRate,
    recordSample,
    reset,
    setActive,
  };
});

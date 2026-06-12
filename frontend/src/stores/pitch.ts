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

  /** smoothedUserMidi is the post-median value from PitchFilter. */
  function recordSample(
    t: number,
    smoothedUserMidi: number | null,
    targetMidi: number | null,
  ): void {
    userTimes.value.push(t);
    userMidis.value.push(smoothedUserMidi);
    currentMidi.value = smoothedUserMidi;

    const userFinite =
      smoothedUserMidi !== null && !Number.isNaN(smoothedUserMidi);
    const targetFinite = targetMidi !== null && !Number.isNaN(targetMidi);

    if (userFinite && targetFinite) {
      frameTotal.value++;
      // Hit = "right note" + "adjacent close" bands from the diagram (green or
      // yellow). Matches PitchDiagram's segmentColor logic for consistency.
      frameHits.value +=
        Math.abs(smoothedUserMidi! - targetMidi!) <= 2.0 ? 1 : 0;
    }
  }

  function trimSinceSeek(t: number): void {
    if (userTimes.value.length === 0) return;
    const i = searchsorted(userTimes.value, t);
    userTimes.value = userTimes.value.slice(0, i);
    userMidis.value = userMidis.value.slice(0, i);
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
    trimSinceSeek,
    reset,
    setActive,
  };
});

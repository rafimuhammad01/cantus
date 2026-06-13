import { describe, it, expect, beforeEach } from "vitest";
import { setActivePinia, createPinia } from "pinia";
import { usePitchStore } from "./pitch";

beforeEach(() => {
  setActivePinia(createPinia());
});

// ---------------------------------------------------------------------------
// A. recordSample — hit/total table (absolute distance scoring, matches diagram)
// Hit = |userMidi - targetMidi| ≤ 2.0 (green ≤1.0 + yellow ≤2.0 bands).
// Octave-equivalent notes are NOT credited — absolute distance only.
// ---------------------------------------------------------------------------
describe("A. recordSample — hit/total", () => {
  it.each<[string, number | null, number | null, number, number]>([
    ["exact match", 60.0, 60.0, 1, 1],
    ["within green band (|diff|=1.0)", 60.0, 61.0, 1, 1],
    ["within yellow band (|diff|=1.5)", 60.0, 61.5, 1, 1],
    ["boundary |diff|=2.0 → hit", 60.0, 62.0, 1, 1],
    ["just outside |diff|=2.01 → miss", 60.0, 62.01, 0, 1],
    ["below boundary |diff|=1.5 (user=58.5, target=60)", 58.5, 60.0, 1, 1],
    ["octave: user=72, target=60 → miss (|diff|=12 > 2.0)", 72.0, 60.0, 0, 1],
    ["octave: user=48, target=60 → miss (|diff|=12 > 2.0)", 48.0, 60.0, 0, 1],
    ["miss: user=60, target=64 (|diff|=4 > 2.0)", 60.0, 64.0, 0, 1],
    ["user null", null, 60.0, 0, 0],
    ["target null", 60.0, null, 0, 0],
    ["user NaN", NaN, 60.0, 0, 0],
    ["target NaN", 60.0, NaN, 0, 0],
    ["both null", null, null, 0, 0],
  ])(
    "%s: user=%s target=%s → hitDelta=%s totalDelta=%s",
    (_label, userMidi, targetMidi, expectedHitDelta, expectedTotalDelta) => {
      const store = usePitchStore();
      store.recordSample(0, userMidi, targetMidi);
      expect(store.frameHits).toBe(expectedHitDelta);
      expect(store.frameTotal).toBe(expectedTotalDelta);
    },
  );
});

// ---------------------------------------------------------------------------
// B. recordSample always pushes to userTimes/userMidis
// ---------------------------------------------------------------------------
describe("B. recordSample always pushes arrays", () => {
  it("3 samples (voiced+target, voiced-no-target, unvoiced-no-target) → length 3", () => {
    const store = usePitchStore();
    store.recordSample(0.0, 60.0, 60.0); // voiced + target
    store.recordSample(0.1, 62.0, null); // voiced, no target
    store.recordSample(0.2, null, null); // unvoiced, no target
    expect(store.userTimes.length).toBe(3);
    expect(store.userMidis).toEqual([60.0, 62.0, null]);
  });
});

// ---------------------------------------------------------------------------
// C. currentMidi is updated to whatever was passed (including null)
// ---------------------------------------------------------------------------
describe("C. currentMidi tracking", () => {
  it("reflects last smoothedUserMidi, even when null", () => {
    const store = usePitchStore();
    store.recordSample(0.0, 60.0, null);
    expect(store.currentMidi).toBe(60.0);
    store.recordSample(0.1, null, null);
    expect(store.currentMidi).toBeNull();
    store.recordSample(0.2, 72.5, null);
    expect(store.currentMidi).toBe(72.5);
  });
});

// ---------------------------------------------------------------------------
// D. recordSample preserves sort order after seek-back (review-your-pitch UX)
// ---------------------------------------------------------------------------
describe("D. recordSample sort-order + overwrite on seek-back", () => {
  it("seek-back then resume: prior pitch survives, new samples splice in sorted", () => {
    const store = usePitchStore();
    const initial = [1.0, 2.0, 3.0, 4.0, 5.0];
    initial.forEach((t) => store.recordSample(t, 60.0, null));
    // User seeks back to 2.0 and sings at a fresh time slot between samples.
    store.recordSample(2.5, 65.0, null);
    expect(store.userTimes).toEqual([1.0, 2.0, 2.5, 3.0, 4.0, 5.0]);
    expect(store.userMidis).toEqual([60.0, 60.0, 65.0, 60.0, 60.0, 60.0]);
  });

  it("re-singing the same time slot overwrites within the rAF-frame epsilon", () => {
    const store = usePitchStore();
    store.recordSample(1.0, 60.0, null);
    store.recordSample(1.01, 70.0, null); // 10ms later = same frame
    expect(store.userTimes).toEqual([1.0]); // not spliced
    expect(store.userMidis).toEqual([70.0]); // overwritten
  });

  it("just outside the epsilon (30ms) inserts a new sample", () => {
    const store = usePitchStore();
    store.recordSample(1.0, 60.0, null);
    store.recordSample(1.03, 70.0, null);
    expect(store.userTimes).toEqual([1.0, 1.03]);
    expect(store.userMidis).toEqual([60.0, 70.0]);
  });
});

// ---------------------------------------------------------------------------
// E. counters: every recorded sample counts, including overwrites
// ---------------------------------------------------------------------------
describe("E. recordSample counters are cumulative across attempts", () => {
  it("overwriting an existing slot still bumps frameHits/frameTotal", () => {
    const store = usePitchStore();
    store.recordSample(1.0, 60.0, 60.0); // hit
    store.recordSample(1.0, 60.0, 60.0); // overwrite, also a hit
    expect(store.frameTotal).toBe(2);
    expect(store.frameHits).toBe(2);
    expect(store.userTimes.length).toBe(1); // array stays deduped
  });
});

// ---------------------------------------------------------------------------
// F. hitRate gating (null until frameTotal > 30)
// ---------------------------------------------------------------------------
describe("F. hitRate gating", () => {
  it.each<[string, number, number, number | null]>([
    ["frameTotal=0", 0, 0, null],
    ["frameTotal=30 (boundary, still null)", 30, 15, null],
    ["frameTotal=31", 31, 15, 15 / 31],
    ["frameTotal=100, hits=80", 100, 80, 0.8],
  ])("%s → hitRate=%s", (_label, total, hits, expected) => {
    const store = usePitchStore();
    // Directly drive frameTotal and frameHits via recordSample
    // Use target=60 for on-target hits and target=100 for misses
    for (let i = 0; i < hits; i++) {
      store.recordSample(i * 0.01, 60.0, 60.0); // hit
    }
    for (let i = hits; i < total; i++) {
      store.recordSample(i * 0.01, 60.0, 64.0); // miss (|diff|=4 > 2.0)
    }
    if (expected === null) {
      expect(store.hitRate).toBeNull();
    } else {
      expect(store.hitRate).toBeCloseTo(expected, 10);
    }
  });
});

// ---------------------------------------------------------------------------
// G. reset() clears state but not isActive
// ---------------------------------------------------------------------------
describe("G. reset()", () => {
  it("clears arrays/counts/currentMidi but leaves isActive=true", () => {
    const store = usePitchStore();
    store.setActive(true);
    for (let i = 0; i < 5; i++) {
      store.recordSample(i * 0.1, 60.0, 60.0);
    }
    store.reset();
    expect(store.userTimes).toEqual([]);
    expect(store.userMidis).toEqual([]);
    expect(store.frameHits).toBe(0);
    expect(store.frameTotal).toBe(0);
    expect(store.currentMidi).toBeNull();
    expect(store.isActive).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// H. setActive flips the flag
// ---------------------------------------------------------------------------
describe("H. setActive", () => {
  it("toggles isActive correctly", () => {
    const store = usePitchStore();
    expect(store.isActive).toBe(false);
    store.setActive(true);
    expect(store.isActive).toBe(true);
    store.setActive(false);
    expect(store.isActive).toBe(false);
  });
});

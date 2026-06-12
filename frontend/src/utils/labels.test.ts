import { describe, expect, it } from "vitest";
import { semitonesToLabel, octaveShiftToLabel } from "./labels";

describe("semitonesToLabel", () => {
  const cases: Array<[number, string]> = [
    [0, "Original key"],
    [1, "Higher 1 step"],
    [2, "Higher 2 steps"],
    [7, "Higher 7 steps"],
    [-1, "Lower 1 step"],
    [-2, "Lower 2 steps"],
    [-12, "Lower 12 steps"],
    [12, "Higher 12 steps"],
  ];
  it.each(cases)("semitonesToLabel(%i) → %s", (n, expected) => {
    expect(semitonesToLabel(n)).toBe(expected);
  });
});

describe("octaveShiftToLabel", () => {
  const cases: Array<[number, "Low" | "Mid" | "High"]> = [
    [-12, "Low"],
    [0, "Mid"],
    [12, "High"],
  ];
  it.each(cases)("octaveShiftToLabel(%i) → %s", (shift, expected) => {
    expect(octaveShiftToLabel(shift)).toBe(expected);
  });
});

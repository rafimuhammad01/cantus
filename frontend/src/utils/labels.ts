/** Plain-language label for a semitone offset. */
export function semitonesToLabel(n: number): string {
  if (n === 0) return "Original key";
  const abs = Math.abs(n);
  const word = abs === 1 ? "step" : "steps";
  return `${n > 0 ? "Higher" : "Lower"} ${abs} ${word}`;
}

/** Low / Mid / High label for the vocal-octave shift (-12 | 0 | 12). */
export function octaveShiftToLabel(shift: number): "Low" | "Mid" | "High" {
  if (shift < 0) return "Low";
  if (shift > 0) return "High";
  return "Mid";
}

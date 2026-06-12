# UI/UX Revamp — Design Spec

**Date:** 2026-06-13
**Scope:** Visual + interaction revamp of all three frontend views (Search, Preview, Play) and their shared components. No backend changes.

## Problem

The current UI is functional but generic: muddy purple-black background, default sans typography, boxy bordered cards, equal visual weight on every element, technical microcopy ("Generate Full Song", "−2 semitones", "Preparing the instrumental track"). It does not communicate that cantus is a premium, musically-literate practice tool, and it intimidates non-musician users with raw music vocabulary.

## Goals

1. **Premium music-app feel** — comparable to Spotify/Apple Music in polish, with a distinct warm/editorial identity rather than imitation.
2. **Minimal, focused-practice clarity** — one primary action per moment; the pitch diagram is the visual hero during practice.
3. **Quietly musical** — musicality is signalled through typography, note labels, and a secondary "musical truth" line, never by forcing non-musicians to learn theory.
4. **Accessible to non-musicians** — every primary control reads in plain language ("Lower 2 steps"); musical labels are always present but secondary.

Non-goals: changing backend behavior, adding new features, supporting light mode (dark only for v1), full design-system extraction (we keep utility classes inline via Tailwind 4).

## Design Identity

### Palette
| Token | Hex | Use |
|---|---|---|
| `bg` | `#0a0a0b` | Page background |
| `surface` | `#141416` | Cards, top bar, popovers |
| `surface-2` | `#1c1c1f` | Elevated / hover |
| `border` | `#24242a` | Hairline dividers, focus-ring base |
| `text` | `#fafaf7` | Warm white, primary text |
| `text-muted` | `#a8a8a0` | Secondary text |
| `text-faint` | `#5e5e58` | Tertiary / helper microcopy |
| `accent` | `#e8a87c` | Burnt amber — CTAs, focus ring, target pitch line, progress bar |
| `accent-hover` | `#efb892` | |
| `success` | `#7a9e7e` | Sage — user "on pitch" indicator |
| `danger` | `#c98a8a` | Quieter than the current red-500 |

Background gets a very subtle warm noise texture (1% opacity) to avoid flat-black banding.

### Typography
- **Fraunces** (variable, optical-size aware) — song titles, the wordmark, italic secondary "musical truth" lines, large display numerics.
- **Inter** (variable) — all other UI.
- **Tabular numerics** (Inter `font-variant-numeric: tabular-nums`) for time, durations, semitone numbers.
- Both loaded from Google Fonts at app boot. We accept the FOUT on first load to avoid invisible-text flicker; a `font-display: swap` declaration handles it.

### Motion
- 200ms `ease-out` for hover, focus, and color transitions.
- Page transitions: 180ms fade + 8px Y-slide (Vue `<Transition>` on `<RouterView>`).
- Transpose-change feedback: the displayed key text crossfades; the artwork (Preview view) does a soft 1.01× scale pulse.

## Dual-Layer Musical Labels (cross-cutting pattern)

Every musical control follows this pattern:

```
┌─────────────────────────────────────┐
│   [ – ]   Lower 2 steps   [ + ]    │  ← primary, Inter, large
│            A → G                    │  ← italic Fraunces, muted, smaller
└─────────────────────────────────────┘
```

- Primary label: plain language ("Original key", "Lower N steps", "Higher N steps", "Voice: low / mid / high"). Never uses "semitones."
- Secondary line: musical truth (key transposition arrow, note range, etc.) in italic Fraunces, `text-muted`. Always present once data is available — musicians read this; non-musicians ignore it.

Microcopy replacements:
| Before | After |
|---|---|
| "Generate Full Song" | "Practice full song" |
| "−2 semitones" / "+3 semitones" | "Lower 2 steps" / "Higher 3 steps" |
| "Preparing the instrumental track — about 14 seconds…" | "Getting your accompaniment ready…" |
| "Shifting key…" | "Tuning to your key…" |
| "Vocal: C3 – G4" (primary) | "Voice: mid" primary + "C3 – G4" italic secondary |
| "Back to search" / "Back to preview" | unchanged (already plain) |

## View Designs

### SearchView

**Hero state** (no query, no results):
- Centered "cantus" wordmark in Fraunces italic, ~96px, `text`.
- Tagline beneath, Inter 14px, `text-muted`: *"Sing any song. In your key."*
- Search bar: pill-shaped, `surface` fill, amber focus ring (2px). On focus, the wordmark fades to ~60% opacity to direct attention.

**Results state:**
- Search bar pins to the top of a max-width-2xl column (unchanged structurally).
- Results render as **rows, not cards**:
  - 56×56 album artwork (rounded 8px) on the left.
  - Title in Fraunces 18px on top, artist · album in Inter 13px `text-muted` below.
  - Duration right-aligned, tabular numerics, `text-faint`.
  - Row separator: 1px `border` hairline (no boxed card).
  - Hover: row background lifts to `surface`, title gets a 1px amber underline.
- Skeleton rows: warm shimmer (`surface` → `surface-2` → `surface`), not cold gray.
- Empty / error states: same structure, microcopy refined ("No songs found." → "Nothing matched. Try a different spelling.").

### PreviewView (split layout — "browsing this song")

**Top header (~45vh):**
- Full-bleed backdrop: blurred + darkened (`brightness(0.35) blur(40px)`) version of the album artwork.
- Foreground, centered horizontally with row layout (max-w-3xl):
  - 280×280 album artwork (rounded 12px, drop shadow `0 16px 48px rgba(0,0,0,0.5)`).
  - To the right (column): "← Back" tiny link, song title in Fraunces 44px, artist + album + duration in Inter 14px `text-muted`.

**Middle: dual-layer transpose** (the only primary interaction on this screen):
- Large horizontal pill, centered, max-w-md.
- `[ – ]` and `[ + ]` are 44×44 circular amber-outline buttons.
- Center label updates live: "Original key" / "Lower N steps" / "Higher N steps", Inter 18px medium.
- Italic Fraunces line under it: `A → G` (uses live `transposeKey` math during debounce, server-computed after).
- Tiny helper microcopy below (Inter 12px `text-faint`), shown only while `semitones === 0` and fades out after first user interaction: *"Try lowering if the song feels too high to sing."*

**Player + pitch card** (single calm `surface` card, 16px rounded, no border):
- Thin amber audio scrubber with tabular-numeric time on either side.
- Pitch diagram inline, compact (~140px tall) — a preview of what practice will look like.
- Below it, segmented control: `[ Low | Mid | High ]` labeled "Voice", with italic Fraunces `C3 – G4` underneath. Maps to existing `vocalOctaveShift` (-1 / 0 / +1).

**Sticky bottom CTA:** Amber filled button, full-width on mobile / fixed-width centered on desktop: **"Practice full song →"**. Helper microcopy beneath: *"~90 seconds to prepare"*. Sticky so it's always reachable while exploring transpose.

### PlayView (pitch diagram is hero — "practicing this song")

**Slim top bar (64px, `surface` with bottom hairline):**
- Left: "← Back" link, 40×40 album artwork, title in Fraunces 16px, artist Inter 12px `text-muted`.
- Right: compact transpose control — single button reading `Lower 2 steps · A → G` that opens a popover with `[ – ] [ + ]`. Voice menu collapses to a small icon (waveform glyph) that opens the same low/mid/high segmented control.

**Hero — Pitch diagram (55vh tall, full width minus 24px gutters):**
- Target pitch line in `accent` (amber), 2px stroke.
- User's sung line in `success` (sage) when within ±50¢ of target, lerping toward `text-faint` as it drifts off — visual feedback without scary red.
- Left edge: vertical column of italic Fraunces note names (A4, B4, C5…) at 50% opacity — the quiet musicality cue.
- Currently-active target note gets a soft amber halo (`box-shadow: 0 0 24px #e8a87c66`).

**Loading state** (replaces the diagram area while generate runs):
- Centered card, `surface`, max-w-md.
- Stepped indicator (vertical, not horizontal):
  ```
  ●  Downloading the song               (downloading)
  ●  Separating vocals from music       (separating)
  ●  Reading the melody                 (melody)
  ●  Tuning to your key                 (shifting)
  ○  Ready
  ```
- Each step: plain-language label in Inter, technical name in tiny italic Fraunces underneath (e.g., *"demucs"*, *"crepe"*, *"rubberband"*) — invisible musicality flex for curious users.
- Current step has the amber filled dot and a subtle pulse; completed steps go sage; pending steps stay `text-faint`.

**Bottom transport bar (sticky, 80px, `surface` with top hairline):**
- Large amber play/pause circle (56px) centered.
- Scrubber to its right with tabular numeric time on both ends.
- Mute icon on the far right.
- No other chrome.

## Component Inventory (what changes)

| File | Change |
|---|---|
| `src/style.css` | Add CSS custom properties for the palette, font imports, base text/background. Remove green and purple-black. |
| `src/App.vue` | Add `<Transition>` wrapping `<RouterView>` for 180ms fade+slide. |
| `src/views/SearchView.vue` | Wordmark + tagline; search bar restyle; remove card chrome from results region. |
| `src/views/PreviewView.vue` | Full restructure: backdrop + artwork header; transpose as the central control; sticky CTA. |
| `src/views/PlayView.vue` | Full restructure: slim top bar; pitch diagram as hero; new loading card; bottom transport. |
| `src/components/SearchBar.vue` | Pill shape, amber focus ring, refined microcopy. |
| `src/components/SongCard.vue` → **rename to `SongRow.vue`** | Row layout with artwork + meta + duration; hover behavior. |
| `src/components/KeySelector.vue` | Dual-layer label render; new visual treatment; emit unchanged. |
| `src/components/VocalOctaveSelector.vue` | Segmented "Low / Mid / High" + italic range; emit unchanged. |
| `src/components/AudioPlayer.vue` | Thin amber scrubber; transport bar variant for PlayView (prop `variant="bottom-bar"`). |
| `src/components/ProcessingStatus.vue` | New stepped vertical indicator with plain-language + italic technical labels. |
| `src/components/PitchDiagram.vue` | New note-name labels on left edge; amber target / sage user; active-note halo. No data-model changes. |

## Constraints / contracts preserved

- All Pinia store fields, API calls, SSE handling, route shape, debounce timing, and HMAC-sig flow are unchanged.
- Component emits and props that views currently consume stay the same (additions only, no breaks).
- `vocalOctaveShift` remains a number (-1 / 0 / +1) under the hood; "Low / Mid / High" is purely display.
- `semitones` remains a number; "Lower N steps" / "Higher N steps" is purely display.

## Testing

- Unit tests for any new presentational helpers (e.g., `semitonesToLabel(n)`, `octaveShiftToLabel(n)`) — table-driven, per project convention.
- Existing component tests updated to match new DOM (selectors, labels).
- No E2E changes required; routes and store shape are unchanged.
- Manual visual check on all three views at desktop (1440px), tablet (768px), and mobile (375px) widths.

## Out of scope

- Light mode.
- New features (lyrics, recording, social).
- Replacing Tailwind or adding a component library.
- Reworking the pitch detection / audio pipeline.
- Extracting a formal design system or token export.

## Open questions (none blocking)

None — all design decisions resolved during brainstorming.

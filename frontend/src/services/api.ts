// Types match the wire JSON exactly (snake_case)
export interface SearchResult {
  video_id: string;
  sig: string;
  lyrics_sig: string;
  title: string;
  artist: string;
  album: string;
  duration_sec: number;
  thumbnail_url: string;
}

export interface Cue {
  start_ms: number;
  text: string;
}

export interface LyricsResponse {
  available: boolean;
  synced: Cue[];
  plain: string;
}

export interface SearchResponse {
  items: SearchResult[];
  has_more: boolean;
}

export interface MelodyResponse {
  hop_ms: number;
  min_hz: number;
  max_hz: number;
  key: string; // original key: "A major" or "" if unknown
  transposed_key: string; // server-computed for the requested semitones
  frames: [number, number][]; // [t_ms, hz]
}

export interface GenerateResponse {
  job_id: string;
}

export type JobStatusName =
  | "queued"
  | "downloading"
  | "separating"
  | "melody"
  | "shifting"
  | "done"
  | "error";

export interface StatusEvent {
  status: JobStatusName;
  message: string;
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

async function checkOk(resp: Response): Promise<Response> {
  if (resp.ok) return resp;
  let msg = resp.statusText;
  try {
    const body = (await resp.json()) as { error?: string };
    if (body.error) msg = body.error;
  } catch {
    // ignore parse error; fall back to statusText
  }
  throw new Error(msg);
}

// ---------------------------------------------------------------------------
// API functions
// ---------------------------------------------------------------------------

export async function search(
  query: string,
  limit = 10,
  offset = 0,
): Promise<SearchResponse> {
  const resp = await fetch(
    `/api/songs/search?q=${encodeURIComponent(query)}&limit=${limit}&offset=${offset}`,
  );
  await checkOk(resp);
  return resp.json() as Promise<SearchResponse>;
}

/** Returns the GET URL string for direct <audio src=...> binding. */
export function previewURL(videoId: string, sig: string): string {
  return `/api/preview/${videoId}?sig=${sig}`;
}

export async function previewShift(
  videoId: string,
  sig: string,
  semitones: number,
): Promise<Blob> {
  const resp = await fetch("/api/preview-shift", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ video_id: videoId, sig, semitones }),
  });
  await checkOk(resp);
  return resp.blob();
}

export async function prewarm(
  videoId: string,
  sig: string,
): Promise<GenerateResponse> {
  const resp = await fetch("/api/prewarm", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ video_id: videoId, sig }),
  });
  await checkOk(resp);
  return resp.json() as Promise<GenerateResponse>;
}

export async function generate(
  videoId: string,
  sig: string,
  semitones: number,
): Promise<GenerateResponse> {
  const resp = await fetch("/api/generate", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ video_id: videoId, sig, semitones }),
  });
  await checkOk(resp);
  return resp.json() as Promise<GenerateResponse>;
}

export async function getMelody(
  videoId: string,
  sig: string,
  semitones: number,
): Promise<MelodyResponse> {
  const resp = await fetch(`/api/melody/${videoId}/${semitones}?sig=${sig}`);
  await checkOk(resp);
  return resp.json() as Promise<MelodyResponse>;
}

export function audioURL(
  videoId: string,
  sig: string,
  semitones: number,
): string {
  return `/api/audio/${videoId}/${semitones}?sig=${sig}`;
}

export function statusURL(jobId: string): string {
  return `/api/status/${jobId}`;
}

export interface PreviewKeyResponse {
  key: string;
}

export async function getPreviewKey(
  videoId: string,
  sig: string,
): Promise<PreviewKeyResponse> {
  const resp = await fetch(`/api/preview-key/${videoId}?sig=${sig}`);
  if (!resp.ok) {
    const body = await resp.text();
    throw new Error(`preview-key failed: ${resp.status} ${body}`);
  }
  return (await resp.json()) as PreviewKeyResponse;
}

/**
 * POST /api/preview-stems — triggers Demucs + CREPE on the 30s clip.
 * Streams a keepalive-padded response; final body is {"ready":true} or {"error":"..."}.
 */
export async function triggerPreviewStems(
  videoId: string,
  sig: string,
): Promise<void> {
  const resp = await fetch("/api/preview-stems", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ video_id: videoId, sig }),
  });
  await checkOk(resp); // handles 4xx/5xx from validation phase
  const text = await resp.text(); // may include leading whitespace keepalive bytes
  const data = JSON.parse(text);
  if (data.error) throw new Error(`preview-stems failed: ${data.error}`);
  // data.ready === true
}

/**
 * GET /api/preview-melody/:videoId/:semitones?sig= — returns math-transposed
 * MelodyResponse for the preview clip. 404 if preview-stems not yet generated.
 */
export async function getPreviewMelody(
  videoId: string,
  sig: string,
  semitones: number,
): Promise<MelodyResponse> {
  const resp = await fetch(
    `/api/preview-melody/${videoId}/${semitones}?sig=${sig}`,
  );
  await checkOk(resp);
  return resp.json() as Promise<MelodyResponse>;
}

/** Returns the GET URL for the clean instrumental stem (no_vocals.mp3). */
export function previewAudioURL(videoId: string, sig: string): string {
  return `/api/preview-audio/${videoId}?sig=${sig}`;
}

export async function getLyrics(
  videoId: string,
  lyricsSig: string,
  title: string,
  artist: string,
  album: string,
  durationSec: number,
): Promise<LyricsResponse> {
  const params = new URLSearchParams({
    lyrics_sig: lyricsSig,
    title,
    artist,
    album,
    duration_sec: String(durationSec),
  });
  const resp = await fetch(`/api/lyrics/${videoId}?${params.toString()}`);
  await checkOk(resp);
  return resp.json() as Promise<LyricsResponse>;
}

/**
 * retryPolicy — simple exponential-backoff retry for transient pipeline errors.
 *
 * maxAttempts is read from VITE_PIPELINE_MAX_RETRIES (default 2).
 * backoffMs is the base delay in milliseconds; each successive attempt doubles it.
 * stallTimeoutMs is the silence threshold before treating a job as stalled
 *   (read from VITE_PIPELINE_STALL_TIMEOUT_MS, default 90000).
 */

const MAX_ATTEMPTS = (() => {
  const env = import.meta.env.VITE_PIPELINE_MAX_RETRIES;
  const parsed = Number.parseInt(env as string, 10);
  return Number.isFinite(parsed) && parsed >= 1 ? parsed : 2;
})();

const BASE_BACKOFF_MS = 1500;

const STALL_TIMEOUT_MS = (() => {
  const env = import.meta.env.VITE_PIPELINE_STALL_TIMEOUT_MS;
  const parsed = Number.parseInt(env as string, 10);
  return Number.isFinite(parsed) && parsed >= 1000 ? parsed : 90000;
})();

export const retryPolicy = {
  maxAttempts: MAX_ATTEMPTS,
  backoffMs: BASE_BACKOFF_MS,
  stallTimeoutMs: STALL_TIMEOUT_MS,
} as const;

/**
 * withRetry calls fn up to retryPolicy.maxAttempts times with exponential backoff.
 * If all attempts fail it rethrows the last error.
 *
 * @param fn - async function to retry
 * @param onRetry - optional callback called before each retry with (attempt, error)
 */
export async function withRetry<T>(
  fn: () => Promise<T>,
  onRetry?: (attempt: number, error: Error) => void,
): Promise<T> {
  let lastError: Error = new Error("unknown error");
  let delay = retryPolicy.backoffMs;

  for (let attempt = 1; attempt <= retryPolicy.maxAttempts; attempt++) {
    try {
      return await fn();
    } catch (e) {
      lastError = e instanceof Error ? e : new Error(String(e));
      if (attempt < retryPolicy.maxAttempts) {
        onRetry?.(attempt, lastError);
        await sleep(delay);
        delay *= 2;
      }
    }
  }
  throw lastError;
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

export interface RemoteSyncRetryOptions<T> {
  label: string;
  attempts: number;
  retryDelayMs: number;
  run: () => Promise<T>;
  reset: () => Promise<void> | void;
}

const RETRYABLE_REMOTE_SYNC_PATTERNS = [
  'read aborted',
  'write aborted',
  'failed to dial',
  'stream reset',
  'reset by peer',
  'connection reset',
  'connection refused',
  'connection closed',
  'transport closed',
  'dial backoff',
  'timed out',
  'timeout',
  'networkerror',
  'websocket',
  'yamux',
];

export async function retryRemoteSyncOperation<T>(options: RemoteSyncRetryOptions<T>): Promise<T> {
  const attempts = Math.max(1, Math.floor(options.attempts));
  let lastError: unknown;

  for (let attempt = 1; attempt <= attempts; attempt += 1) {
    try {
      return await options.run();
    } catch (error) {
      lastError = error;
      if (attempt >= attempts || !isRetryableRemoteSyncError(error)) throw error;
      await Promise.resolve(options.reset()).catch(() => undefined);
      await delay(options.retryDelayMs * attempt);
    }
  }

  throw lastError instanceof Error ? lastError : new Error(`${options.label} failed`);
}

export function isRetryableRemoteSyncError(error: unknown): boolean {
  const message = error instanceof Error ? error.message : String(error);
  const normalized = message.toLowerCase();
  return RETRYABLE_REMOTE_SYNC_PATTERNS.some((pattern) => normalized.includes(pattern));
}

async function delay(milliseconds: number): Promise<void> {
  if (milliseconds <= 0) return;
  await new Promise((resolve) => setTimeout(resolve, milliseconds));
}

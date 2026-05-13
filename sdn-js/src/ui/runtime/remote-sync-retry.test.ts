import { describe, expect, it } from 'vitest';
import { isRetryableRemoteSyncError, retryRemoteSyncOperation } from './remote-sync-retry';

describe('remote sync retry', () => {
  it('retries transient libp2p stream aborts after resetting the transport cache', async () => {
    let attempts = 0;
    let resets = 0;

    const result = await retryRemoteSyncOperation({
      label: 'Remote sync stream',
      attempts: 3,
      retryDelayMs: 0,
      reset: async () => {
        resets += 1;
      },
      run: async () => {
        attempts += 1;
        if (attempts === 1) throw new Error('Read aborted');
        return 'ok';
      },
    });

    expect(result).toBe('ok');
    expect(attempts).toBe(2);
    expect(resets).toBe(1);
  });

  it('does not retry non-transient protocol errors', async () => {
    let attempts = 0;

    await expect(retryRemoteSyncOperation({
      label: 'Remote sync scan',
      attempts: 3,
      retryDelayMs: 0,
      reset: async () => undefined,
      run: async () => {
        attempts += 1;
        throw new Error('scan hash does not match requested record refs');
      },
    })).rejects.toThrow('scan hash does not match requested record refs');

    expect(attempts).toBe(1);
  });

  it('classifies dial failures and timeouts as retryable remote sync errors', () => {
    expect(isRetryableRemoteSyncError(new Error('failed to dial /space-data-network/flatsql-sync/1.0.0: Read aborted'))).toBe(true);
    expect(isRetryableRemoteSyncError(new Error('Remote sync scan timed out after 60s'))).toBe(true);
    expect(isRetryableRemoteSyncError(new Error("Failed to construct 'URL': Invalid URL"))).toBe(true);
    expect(isRetryableRemoteSyncError(new Error('No FlatSQL schema registered for OMM'))).toBe(false);
  });
});

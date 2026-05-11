import { describe, expect, it } from 'vitest';
import { TimeoutError, withTimeout } from './async-timeout';

describe('async timeout helper', () => {
  it('resolves the wrapped operation when it completes before the timeout', async () => {
    await expect(withTimeout(Promise.resolve('ok'), 100, 'operation timed out')).resolves.toBe('ok');
  });

  it('rejects with the supplied message when an operation hangs', async () => {
    await expect(withTimeout(new Promise(() => undefined), 1, 'remote FlatSQL sync timed out')).rejects.toThrow(TimeoutError);
    await expect(withTimeout(new Promise(() => undefined), 1, 'remote FlatSQL sync timed out')).rejects.toThrow('remote FlatSQL sync timed out');
  });
});

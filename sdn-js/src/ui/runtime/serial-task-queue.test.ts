import { describe, expect, it } from 'vitest';
import { SerialTaskQueue } from './serial-task-queue';

describe('serial task queue', () => {
  it('runs queued tasks one at a time in enqueue order', async () => {
    const queue = new SerialTaskQueue();
    const events: string[] = [];
    let releaseFirst: (() => void) | null = null;

    const first = queue.enqueue(async () => {
      events.push('first:start');
      await new Promise<void>((resolve) => {
        releaseFirst = resolve;
      });
      events.push('first:end');
      return 'first';
    });

    const second = queue.enqueue(async () => {
      events.push('second:start');
      return 'second';
    });

    await Promise.resolve();
    expect(events).toEqual(['first:start']);

    releaseFirst?.();
    await expect(first).resolves.toBe('first');
    await expect(second).resolves.toBe('second');
    expect(events).toEqual(['first:start', 'first:end', 'second:start']);
  });

  it('continues running queued work after a task rejects', async () => {
    const queue = new SerialTaskQueue();
    const events: string[] = [];

    const first = queue.enqueue(async () => {
      events.push('first');
      throw new Error('first failed');
    });
    const second = queue.enqueue(async () => {
      events.push('second');
      return 'second';
    });

    await expect(first).rejects.toThrow('first failed');
    await expect(second).resolves.toBe('second');
    expect(events).toEqual(['first', 'second']);
  });
});

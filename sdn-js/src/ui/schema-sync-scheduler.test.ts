import { describe, expect, it } from 'vitest';
import { createSchemaSyncScheduler } from '../../ui/src/lib/schema-sync-scheduler';

describe('schema sync scheduler', () => {
  it('starts the largest subscribed schema first and waits before starting the next schema', async () => {
    const started: string[] = [];
    let releaseOmm: (() => void) | null = null;
    const scheduler = createSchemaSyncScheduler({
      syncSchema: async (standardId) => {
        started.push(standardId);
        if (standardId === 'OMM') {
          await new Promise<void>((resolve) => {
            releaseOmm = resolve;
          });
        }
      },
    });

    const run = scheduler.schedule([
      row('MPE', 0, 542_457),
      row('OMM', 10_000, 2_005_702),
      row('PNM', 0, 4),
    ], 'source');

    await Promise.resolve();
    expect(started).toEqual(['OMM']);

    releaseOmm?.();
    await run;
    expect(started).toEqual(['OMM', 'MPE', 'PNM']);
  });

  it('does not start a second stream while a schema is already syncing', async () => {
    const started: string[] = [];
    let activeCount = 0;
    let maxActiveCount = 0;
    let shouldBlockOmm = true;
    let releaseOmm: (() => void) | null = null;
    const scheduler = createSchemaSyncScheduler({
      syncSchema: async (standardId) => {
        activeCount += 1;
        maxActiveCount = Math.max(maxActiveCount, activeCount);
        started.push(standardId);
        if (standardId === 'OMM' && shouldBlockOmm) {
          shouldBlockOmm = false;
          await new Promise<void>((resolve) => {
            releaseOmm = resolve;
          });
        }
        activeCount -= 1;
      },
    });

    const firstRun = scheduler.schedule([
      row('OMM', 10_000, 2_005_702),
      row('MPE', 0, 542_457),
    ], 'source');

    await Promise.resolve();
    expect(started).toEqual(['OMM']);

    const secondRun = scheduler.schedule([
      row('OMM', 20_000, 2_005_702),
      row('MPE', 0, 542_457),
    ], 'source');
    await Promise.resolve();
    expect(started).toEqual(['OMM']);

    releaseOmm?.();
    await Promise.all([firstRun, secondRun]);
    expect(maxActiveCount).toBe(1);
  });

  it('reschedules a subscription when its sync filter changes', async () => {
    const calls: Array<[string, string, string | undefined]> = [];
    const scheduler = createSchemaSyncScheduler({
      syncSchema: (standardId, dataSourceId, subscriptionId) => {
        calls.push([standardId, dataSourceId, subscriptionId]);
      },
    });
    const baseRow = {
      id: 'OMM',
      subscriptionId: 'configured:celestrak.eth:OMM',
      datastoreKey: 'sdn-ds-v1-history',
      localRows: 0,
      remoteRows: 10,
      preference: {
        mode: 'sync',
        storageCap: 1,
        storageUnit: 'GB',
      },
    } as const;

    await scheduler.schedule([baseRow], 'configured:celestrak.eth');
    await scheduler.idle();
    await scheduler.schedule([
      {
        ...baseRow,
        syncFilter: "EPOCH_DAY = '2026-05-12'",
      },
    ], 'configured:celestrak.eth');
    await scheduler.idle();

    expect(calls).toEqual([
      ['OMM', 'configured:celestrak.eth', 'configured:celestrak.eth:OMM'],
      ['OMM', 'configured:celestrak.eth', 'configured:celestrak.eth:OMM'],
    ]);
  });

  it('reschedules a subscription when its query profile changes', async () => {
    const calls: Array<[string, string, string | undefined]> = [];
    const scheduler = createSchemaSyncScheduler({
      syncSchema: (standardId, dataSourceId, subscriptionId) => {
        calls.push([standardId, dataSourceId, subscriptionId]);
      },
    });
    const baseRow = {
      id: 'OMM',
      subscriptionId: 'configured:celestrak.eth:OMM',
      datastoreKey: 'sdn-ds-v1-history',
      localRows: 0,
      remoteRows: 10,
      syncFilter: '',
      queryProfile: 'ordered-offset-v1',
      preference: {
        mode: 'sync',
        storageCap: 1,
        storageUnit: 'GB',
      },
    } as const;

    await scheduler.schedule([baseRow], 'configured:celestrak.eth');
    await scheduler.idle();
    await scheduler.schedule([
      {
        ...baseRow,
        queryProfile: 'dataset-publication-offset-v1',
      },
    ], 'configured:celestrak.eth');
    await scheduler.idle();

    expect(calls).toEqual([
      ['OMM', 'configured:celestrak.eth', 'configured:celestrak.eth:OMM'],
      ['OMM', 'configured:celestrak.eth', 'configured:celestrak.eth:OMM'],
    ]);
  });
});

function row(id: string, localRows: number, remoteRows: number) {
  return {
    id,
    localRows,
    remoteRows,
    preference: {
      mode: 'sync' as const,
      storageCap: 1,
      storageUnit: 'GB',
    },
  };
}

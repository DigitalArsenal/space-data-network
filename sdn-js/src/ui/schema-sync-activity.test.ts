import { describe, expect, it } from 'vitest';

import {
  effectivePublishedRemoteRows,
  shouldRunPublishedSnapshotProbe,
} from '../../ui/src/lib/schema-sync-activity';

describe('schema sync activity planning', () => {
  it('treats completed published replace-snapshot feeds as manifest probes', () => {
    expect(shouldRunPublishedSnapshotProbe({
      queryProfile: 'dataset-publication-offset-v1',
      retentionPolicy: 'replace-snapshot',
      progressQueryProfile: 'dataset-publication-offset-v1',
      progressStatus: 'synced',
      localRows: 332_402,
      totalRows: 332_402,
    })).toBe(true);

    expect(shouldRunPublishedSnapshotProbe({
      queryProfile: 'dataset-publication-offset-v1',
      retentionPolicy: 'replace-snapshot',
      progressQueryProfile: 'dataset-publication-offset-v1',
      progressStatus: 'synced',
      localRows: 332_401,
      totalRows: 332_402,
    })).toBe(false);

    expect(shouldRunPublishedSnapshotProbe({
      queryProfile: 'dataset-publication-offset-v1',
      retentionPolicy: 'append-only',
      progressQueryProfile: 'dataset-publication-offset-v1',
      progressStatus: 'synced',
      localRows: 332_402,
      totalRows: 332_402,
    })).toBe(false);
  });

  it('uses the published manifest total instead of stale advertised rows', () => {
    expect(effectivePublishedRemoteRows({
      queryProfile: 'dataset-publication-offset-v1',
      progressQueryProfile: 'dataset-publication-offset-v1',
      advertisedRemoteRows: 338_649,
      progressTotalRows: 332_402,
    })).toBe(332_402);

    expect(effectivePublishedRemoteRows({
      queryProfile: 'ordered-offset-v1',
      progressQueryProfile: 'ordered-offset-v1',
      advertisedRemoteRows: 338_649,
      progressTotalRows: 332_402,
    })).toBe(338_649);
  });
});

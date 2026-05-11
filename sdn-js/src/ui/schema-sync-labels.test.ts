import { describe, expect, it } from 'vitest';
import { schemaSyncStatusLabel } from '../../ui/src/lib/schema-sync-labels';

describe('schema sync status labels', () => {
  it('does not show active syncing for an idle subscription with no worker progress', () => {
    expect(schemaSyncStatusLabel({
      preferenceMode: 'sync',
      progressStatus: 'idle',
      localRows: 10,
      remoteRows: 1_999_559,
    })).toBe('Queued');
  });

  it('shows terminal and active statuses directly', () => {
    expect(schemaSyncStatusLabel({
      preferenceMode: 'preview',
      progressStatus: 'idle',
      localRows: 0,
      remoteRows: 1,
    })).toBe('Preview only');
    expect(schemaSyncStatusLabel({
      preferenceMode: 'sync',
      progressStatus: 'syncing',
      localRows: 10,
      remoteRows: 1_999_559,
    })).toBe('Syncing');
    expect(schemaSyncStatusLabel({
      preferenceMode: 'sync',
      progressStatus: 'synced',
      localRows: 1_999_559,
      remoteRows: 1_999_559,
    })).toBe('Synced');
    expect(schemaSyncStatusLabel({
      preferenceMode: 'sync',
      progressStatus: 'capped',
      localRows: 100,
      remoteRows: 1_999_559,
    })).toBe('Storage cap reached');
    expect(schemaSyncStatusLabel({
      preferenceMode: 'sync',
      progressStatus: 'error',
      localRows: 10,
      remoteRows: 1_999_559,
    })).toBe('Sync error');
  });
});

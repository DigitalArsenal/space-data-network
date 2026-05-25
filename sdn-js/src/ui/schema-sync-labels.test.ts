import { describe, expect, it } from 'vitest';
import { effectiveSchemaSyncStatus, schemaSyncStatusLabel } from '../../ui/src/lib/schema-sync-labels';

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

  it('does not revive persisted syncing state when the worker is not active', () => {
    expect(effectiveSchemaSyncStatus({
      active: false,
      complete: false,
      persistedStatus: 'syncing',
    })).toBe('idle');
    expect(effectiveSchemaSyncStatus({
      active: false,
      complete: false,
      persistedStatus: 'synced',
    })).toBe('idle');
  });

  it('keeps active and complete states authoritative', () => {
    expect(effectiveSchemaSyncStatus({
      active: true,
      complete: false,
      persistedStatus: 'idle',
    })).toBe('syncing');
    expect(effectiveSchemaSyncStatus({
      active: false,
      complete: true,
      persistedStatus: 'error',
    })).toBe('synced');
  });
});

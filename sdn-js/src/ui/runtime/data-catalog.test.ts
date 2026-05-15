import { describe, expect, it } from 'vitest';
import {
  buildDataBillingProviderRows,
  buildDataCatalogRows,
  buildDataOverviewVisuals,
  catalogRowHasBillingData,
  filterDataCatalogRows,
  summarizeDataCatalog,
} from './data-catalog';

describe('paid-aware data catalog projection', () => {
  it('projects free synced data without pretending billing exists', () => {
    const rows = buildDataCatalogRows([{
      standardId: 'OMM',
      providerName: 'CelesTrak Provider',
      providerPeerId: '12D3KooWProvider',
      providerPublicKey: 'provider-public-key',
      remoteRows: 200,
      localRows: 100,
      pinnedRows: 50,
      cachedBytes: 1024,
      storageCap: 1,
      storageUnit: 'GB',
      syncStatus: 'syncing',
      nextSyncAttempt: 'Syncing now',
      lastSyncedAt: null,
      syncFilter: '',
    }]);

    expect(rows[0]).toMatchObject({
      provider: 'CelesTrak Provider',
      product: 'OMM Feed',
      messageTypes: ['OMM'],
      access: { state: 'free', label: 'Free' },
      plan: { label: 'No paid plan', priceLabel: 'Free' },
      storage: { localRows: 100, remoteRows: 200, pinnedRows: 50, cachedBytes: 1024 },
      sync: { status: 'syncing', label: 'Syncing', nextAttempt: 'Syncing now' },
    });
  });

  it('summarizes active local storage without adding fake spend', () => {
    const summary = summarizeDataCatalog(buildDataCatalogRows([{
      standardId: 'OMM',
      providerName: 'CelesTrak Provider',
      providerPeerId: 'peer',
      providerPublicKey: null,
      remoteRows: 200,
      localRows: 200,
      pinnedRows: 200,
      cachedBytes: 2048,
      storageCap: 2,
      storageUnit: 'GB',
      syncStatus: 'synced',
      nextSyncAttempt: 'When remote rows advance',
      lastSyncedAt: '2026-05-13T12:00:00.000Z',
      syncFilter: '',
    }]));

    expect(summary.localStorageBytes).toBe(2048);
    expect(summary.freeProducts).toBe(1);
    expect(summary.activePaidSubscriptions).toBe(0);
    expect(summary.hasBillingData).toBe(false);
    expect(summary.billingMetricTitle).toBe('Paid subscriptions');
    expect(summary.billingMetricValue).toBe('No paid subscriptions');
    expect(summary.monthlySpendLabel).toBe('No paid subscriptions');
  });

  it('separates paid entitlement from backend billing facts', () => {
    const rows = buildDataCatalogRows([
      {
        standardId: 'MPE',
        providerName: 'Commercial Provider',
        providerPeerId: 'peer',
        providerPublicKey: 'public-key',
        remoteRows: 10,
        localRows: 0,
        pinnedRows: 0,
        cachedBytes: 0,
        storageCap: 500,
        storageUnit: 'MB',
        syncStatus: 'idle',
        nextSyncAttempt: 'Not scheduled',
        lastSyncedAt: null,
        syncFilter: '',
        accessState: 'paid-active',
        planLabel: 'Orbit Pro',
      },
    ]);

    const summary = summarizeDataCatalog(rows);

    expect(rows[0].access.state).toBe('paid-active');
    expect(catalogRowHasBillingData(rows[0])).toBe(false);
    expect(summary.activePaidSubscriptions).toBe(1);
    expect(summary.hasBillingData).toBe(false);
    expect(summary.billingMetricTitle).toBe('Paid subscriptions');
    expect(summary.billingMetricValue).toBe('1 active paid · billing unavailable');
    expect(summary.monthlySpendLabel).toBe('Billing data unavailable');
  });

  it('uses backend billing facts before showing current period spend', () => {
    const rows = buildDataCatalogRows([
      {
        standardId: 'MPE',
        providerName: 'Commercial Provider',
        providerPeerId: 'peer',
        providerPublicKey: 'public-key',
        remoteRows: 10,
        localRows: 0,
        pinnedRows: 0,
        cachedBytes: 0,
        storageCap: 500,
        storageUnit: 'MB',
        syncStatus: 'idle',
        nextSyncAttempt: 'Not scheduled',
        lastSyncedAt: null,
        syncFilter: '',
        accessState: 'paid-active',
        planLabel: 'Orbit Pro',
        priceLabel: '$49/mo',
        renewalLabel: 'Jun 13',
      },
    ]);

    const summary = summarizeDataCatalog(rows);

    expect(catalogRowHasBillingData(rows[0])).toBe(true);
    expect(summary.hasBillingData).toBe(true);
    expect(summary.billingMetricTitle).toBe('Current period spend');
    expect(summary.billingMetricValue).toBe('$49/mo');
    expect(summary.monthlySpendLabel).toBe('$49/mo');
  });

  it('groups backend billing facts by provider for billing tables', () => {
    const rows = buildDataCatalogRows([
      {
        standardId: 'OMM',
        providerName: 'CelesTrak Provider',
        providerPeerId: 'peer-a',
        providerPublicKey: 'public-a',
        remoteRows: 10,
        localRows: 10,
        pinnedRows: 10,
        cachedBytes: 1024,
        storageCap: 1,
        storageUnit: 'GB',
        syncStatus: 'synced',
        nextSyncAttempt: 'When remote rows advance',
        lastSyncedAt: null,
        syncFilter: '',
        accessState: 'free',
      },
      {
        standardId: 'MPE',
        providerName: 'CelesTrak Provider',
        providerPeerId: 'peer-a',
        providerPublicKey: 'public-a',
        remoteRows: 10,
        localRows: 0,
        pinnedRows: 0,
        cachedBytes: 0,
        storageCap: 1,
        storageUnit: 'GB',
        syncStatus: 'idle',
        nextSyncAttempt: 'Not scheduled',
        lastSyncedAt: null,
        syncFilter: '',
        accessState: 'paid-active',
        planLabel: 'Orbit Pro',
        priceLabel: '$49/mo',
        renewalLabel: 'Jun 13',
        quotaLabel: 'No overage',
      },
      {
        standardId: 'CAT',
        providerName: 'Commercial Provider',
        providerPeerId: 'peer-b',
        providerPublicKey: 'public-b',
        remoteRows: 10,
        localRows: 0,
        pinnedRows: 0,
        cachedBytes: 0,
        storageCap: 500,
        storageUnit: 'MB',
        syncStatus: 'idle',
        nextSyncAttempt: 'Not scheduled',
        lastSyncedAt: null,
        syncFilter: '',
        accessState: 'paid-active',
        planLabel: 'Catalog Pro',
      },
    ]);

    expect(buildDataBillingProviderRows(rows)).toEqual([
      expect.objectContaining({
        provider: 'CelesTrak Provider',
        productCount: 1,
        productLabel: '1 billed product',
        priceLabel: '$49/mo',
        renewalLabel: 'Jun 13',
        quotaLabel: 'No overage',
      }),
    ]);
  });

  it('keeps paid entitlement fields distinct when future backend data is present', () => {
    const rows = buildDataCatalogRows([{
      standardId: 'MPE',
      providerName: 'Example Provider',
      providerPeerId: 'peer',
      providerPublicKey: 'public-key',
      remoteRows: 10,
      localRows: 0,
      pinnedRows: 0,
      cachedBytes: 0,
      storageCap: 500,
      storageUnit: 'MB',
      syncStatus: 'idle',
      nextSyncAttempt: 'Not scheduled',
      lastSyncedAt: null,
      syncFilter: 'EPOCH >= 2026-05-01',
      accessState: 'paid-active',
      planLabel: 'Orbit Pro',
      priceLabel: '$49/mo',
      renewalLabel: 'Jun 13',
      quotaLabel: 'No overage',
    }]);

    expect(rows[0]).toMatchObject({
      access: { state: 'paid-active', label: 'Active paid' },
      plan: { label: 'Orbit Pro', priceLabel: '$49/mo', renewalLabel: 'Jun 13', quotaLabel: 'No overage' },
      storage: { policyLabel: '500 MB cap', filterLabel: 'Filtered' },
      sync: { status: 'idle', label: 'Ready' },
    });
  });

  it('builds overview visuals from real catalog rows without fake billing data', () => {
    const rows = buildDataCatalogRows([
      {
        standardId: 'OMM',
        providerName: 'CelesTrak Provider',
        providerPeerId: 'peer-a',
        providerPublicKey: 'public-a',
        remoteRows: 200,
        localRows: 100,
        pinnedRows: 80,
        cachedBytes: 1_000,
        storageCap: 1,
        storageUnit: 'GB',
        syncStatus: 'synced',
        nextSyncAttempt: 'When remote rows advance',
        lastSyncedAt: '2026-05-13T12:00:00.000Z',
        syncFilter: '',
        accessState: 'free',
      },
      {
        standardId: 'MPE',
        providerName: 'CelesTrak Provider',
        providerPeerId: 'peer-a',
        providerPublicKey: 'public-a',
        remoteRows: 50,
        localRows: 10,
        pinnedRows: 5,
        cachedBytes: 3_000,
        storageCap: 1,
        storageUnit: 'GB',
        syncStatus: 'syncing',
        nextSyncAttempt: 'Syncing now',
        lastSyncedAt: null,
        syncFilter: '',
        accessState: 'paid-active',
        planLabel: 'Orbit Pro',
        priceLabel: '$49/mo',
      },
      {
        standardId: 'CAT',
        providerName: 'NOAA-compatible Provider',
        providerPeerId: 'peer-b',
        providerPublicKey: 'public-b',
        remoteRows: 30,
        localRows: 0,
        pinnedRows: 0,
        cachedBytes: 0,
        storageCap: 500,
        storageUnit: 'MB',
        syncStatus: 'idle',
        nextSyncAttempt: 'Not scheduled',
        lastSyncedAt: null,
        syncFilter: '',
        accessState: 'trial',
        planLabel: 'Trial',
      },
    ]);

    const visuals = buildDataOverviewVisuals(rows);

    expect(visuals.storageTotalBytes).toBe(4_000);
    expect(visuals.storageSegments).toEqual([
      expect.objectContaining({ key: 'provider:CelesTrak Provider', label: 'CelesTrak Provider', bytes: 4_000, percent: 100 }),
    ]);
    expect(visuals.storageSegmentsByGroup.access).toEqual([
      expect.objectContaining({ key: 'paid-active', label: 'Active paid', bytes: 3_000, percent: 75 }),
      expect.objectContaining({ key: 'free', label: 'Free', bytes: 1_000, percent: 25 }),
    ]);
    expect(visuals.storageSegmentsByGroup.messageType).toEqual([
      expect.objectContaining({ key: 'MPE', label: 'MPE', bytes: 3_000, percent: 75 }),
      expect.objectContaining({ key: 'OMM', label: 'OMM', bytes: 1_000, percent: 25 }),
    ]);
    expect(visuals.coverageRows).toEqual([
      expect.objectContaining({
        provider: 'CelesTrak Provider',
        cells: [
          expect.objectContaining({ messageType: 'MPE', accessLabel: 'Active paid', syncLabel: 'Syncing' }),
          expect.objectContaining({ messageType: 'OMM', accessLabel: 'Free', syncLabel: 'Synced' }),
        ],
      }),
      expect.objectContaining({
        provider: 'NOAA-compatible Provider',
        cells: [
          expect.objectContaining({ messageType: 'CAT', accessLabel: 'Trial', syncLabel: 'Ready' }),
        ],
      }),
    ]);
    expect(visuals.providerBars).toEqual([
      expect.objectContaining({
        provider: 'CelesTrak Provider',
        localBytes: 4_000,
        pinnedRows: 85,
        planLabels: ['Orbit Pro', 'No paid plan'],
        percent: 100,
      }),
      expect.objectContaining({
        provider: 'NOAA-compatible Provider',
        localBytes: 0,
        pinnedRows: 0,
        planLabels: ['Trial'],
        percent: 0,
      }),
    ]);
    expect(visuals.monthlySpendLabel).toBe('$49/mo');
  });

  it('filters catalog rows by query, access, sync, and storage state', () => {
    const rows = buildDataCatalogRows([
      {
        standardId: 'OMM',
        providerName: 'CelesTrak Provider',
        providerPeerId: 'peer-a',
        providerPublicKey: 'public-a',
        remoteRows: 200,
        localRows: 200,
        pinnedRows: 100,
        cachedBytes: 2_000,
        storageCap: 1,
        storageUnit: 'GB',
        syncStatus: 'synced',
        nextSyncAttempt: 'When remote rows advance',
        lastSyncedAt: null,
        syncFilter: '',
        accessState: 'free',
      },
      {
        standardId: 'MPE',
        providerName: 'Commercial Provider',
        providerPeerId: 'peer-b',
        providerPublicKey: 'public-b',
        remoteRows: 100,
        localRows: 0,
        pinnedRows: 0,
        cachedBytes: 0,
        storageCap: 1,
        storageUnit: 'GB',
        syncStatus: 'failed',
        nextSyncAttempt: 'On next scheduler pass',
        lastSyncedAt: null,
        syncFilter: '',
        accessState: 'paid-active',
        planLabel: 'Orbit Pro',
      },
    ]);

    expect(filterDataCatalogRows(rows, { query: 'celestrak omm' }).map((row) => row.product)).toEqual(['OMM Feed']);
    expect(filterDataCatalogRows(rows, { access: 'paid-active' }).map((row) => row.provider)).toEqual(['Commercial Provider']);
    expect(filterDataCatalogRows(rows, { access: 'paid' }).map((row) => row.provider)).toEqual(['Commercial Provider']);
    expect(filterDataCatalogRows(rows, { sync: 'failed' }).map((row) => row.product)).toEqual(['MPE Feed']);
    expect(filterDataCatalogRows(rows, { storage: 'stored' }).map((row) => row.product)).toEqual(['OMM Feed']);
    expect(filterDataCatalogRows(rows, { storage: 'not-stored' }).map((row) => row.product)).toEqual(['MPE Feed']);
  });
});

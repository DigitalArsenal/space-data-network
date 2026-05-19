import { expect, test, type Page } from '@playwright/test';
import * as flatbuffers from 'flatbuffers';
import { OMM } from 'spacedatastandards.org/lib/js/OMM/OMM.js';
import { PNM } from 'spacedatastandards.org/lib/js/PNM/PNM.js';

const realPeerId = '16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4';

test.beforeEach(async ({ page }) => {
  await installSdnFixtures(page);
});

test('node route renders the three SDN product navigation items only', async ({ page }) => {
  await page.goto('/?api=http://127.0.0.1:5174&gateway=http%3A%2F%2F127.0.0.1%3A8081#/node');

  const nav = page.getByRole('navigation', { name: 'Primary' });
  await expect(nav.getByRole('link', { name: 'Node' })).toBeVisible();
  await expect(nav.getByRole('link', { name: 'Peers' })).toBeVisible();
  await expect(nav.getByRole('link', { name: 'Data' })).toBeVisible();
  await expect(nav.getByText('Local Data')).toHaveCount(0);
  await expect(nav.getByText('Status')).toHaveCount(0);
  await expect(nav.getByText('Files')).toHaveCount(0);
});

test('peers route renders SDN peer fixtures through the backend adapter', async ({ page }) => {
  await page.goto('/?api=http://127.0.0.1:5174&gateway=http%3A%2F%2F127.0.0.1%3A8081#/peers');

  await expect(page.getByRole('heading', { level: 1, name: 'Peers' })).toBeVisible();
  await expect(page.getByRole('button', { name: /Observed Peers 1/ })).toBeVisible();
  await expect(page.getByRole('button', { name: /Data Feeds 0/ })).toBeVisible();
  await page.getByRole('button', { name: /Observed Peers 1/ }).click();
  await expect(page.getByText('CelesTrak Provider')).toBeVisible();
  await expect(page.getByText(realPeerId)).toBeVisible();
});

test('data route renders subscribed local datastore preview without workbench status chrome', async ({ page }) => {
  await page.addInitScript((peerId) => {
    window.localStorage.setItem('sdn:data-directory:v1', JSON.stringify({
      peerTrust: { [peerId]: 'marginal' },
      subscriptions: [{
        id: 'local:PNM',
        dataSourceId: 'local',
        peerId: 'local-node',
        standardId: 'PNM',
        providerName: 'CelesTrak Provider',
        providerPublicKey: peerId,
        remoteRows: 12,
        storageCap: 2.5,
        storageUnit: 'MB',
        syncFilter: '',
        createdAt: '2026-05-12T00:00:00.000Z',
        updatedAt: '2026-05-12T00:00:00.000Z',
      }],
    }));
    window.localStorage.setItem('sdn:data-schema-sync:v1', JSON.stringify({
      'local:PNM': { mode: 'sync', storageCap: 2.5, storageUnit: 'MB' },
    }));
  }, realPeerId);

  await page.goto('/?api=http://127.0.0.1:5174&gateway=http%3A%2F%2F127.0.0.1%3A8081#/data');

  await expect(page.getByRole('heading', { name: 'Data' })).toBeVisible();
  await expect(page.getByText('SQL Workbench')).toHaveCount(0);
  await expect(page.getByText('backend ready')).toHaveCount(0);
  await expect(page.getByText(/available .* total/)).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Refresh' })).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Overview' })).toHaveClass(/active/);
  await expect(page.getByRole('table', { name: 'Data products' })).toContainText('PNM Feed');
  await page.getByRole('textbox', { name: 'Search data products' }).fill('does-not-match');
  await expect(page.getByRole('table', { name: 'Data products' })).toContainText('No matching data products.');
  await page.getByRole('textbox', { name: 'Search data products' }).fill('pnm');
  await expect(page.getByRole('table', { name: 'Data products' })).toContainText('PNM Feed');

  await page.getByRole('button', { name: 'Sources' }).click();
  const dataSources = page.getByRole('table', { name: 'Data sources' });
  await expect(dataSources).toContainText('CelesTrak Provider');
  await expect(dataSources).toContainText('local-node');
  await expect(dataSources).toContainText(realPeerId.slice(0, 10));
  await expect(dataSources).toContainText('Local node');
  await expect(dataSources).toContainText('Subscribed source');
  await expect(dataSources).toContainText('PNM');
  await expect(dataSources).toContainText('1 product');
  await expect(dataSources.getByText('Search')).toHaveCount(0);
  await expect(dataSources.getByText('Directory')).toHaveCount(0);

  await page.getByRole('button', { name: 'Storage' }).click();
  await expect(page.getByLabel('Local storage state')).toBeVisible();
  await expect(page.getByLabel('Local storage state')).toContainText('CelesTrak Provider');
  await expect(page.getByLabel('Local storage state')).toContainText('PNM');
  await expect(page.getByLabel('Local storage state')).toContainText('Synced 10/12');
  await expect(page.getByLabel('Local storage state')).toContainText('Pinned rows');
  await expect(page.getByLabel('Local storage state')).toContainText('Next');
  await expect(page.getByLabel('Local storage state')).toContainText('Last');
  await expect(page.getByLabel('Local storage state').getByRole('button', { name: 'Verify pins' })).toBeVisible();
  await expect(page.getByLabel('Local storage state').getByRole('button', { name: 'Reset row' })).toBeVisible();

  await page.getByRole('button', { name: 'My Subscriptions' }).click();
  const syncSettings = page.getByLabel('Sync settings');
  await expect(syncSettings).toBeVisible();
  await expect(syncSettings.getByRole('button', { name: 'Active' })).toBeVisible();
  await expect(syncSettings.getByRole('button', { name: 'Trials' })).toBeVisible();
  await expect(syncSettings.getByRole('button', { name: 'Payment issues' })).toBeVisible();
  await syncSettings.getByRole('button', { name: 'Free' }).click();
  await expect(syncSettings).toContainText('PNM');
  await expect(syncSettings.locator('article').filter({ hasText: 'PNM Feed' })).toBeVisible();
  await syncSettings.getByRole('button', { name: 'All' }).click();
  await expect(syncSettings).toContainText('12');
  await expect(page.getByRole('spinbutton', { name: 'PNM storage cap' })).toHaveValue('2.5');
  await expect(page.getByRole('combobox', { name: 'PNM storage unit' })).toHaveValue('MB');
  await syncSettings.locator('article').filter({ hasText: 'PNM Feed' }).getByRole('button', { name: 'Details' }).click();
  const subscriptionDetails = page.getByLabel('PNM subscription details');
  await expect(subscriptionDetails).toContainText('Access');
  await expect(subscriptionDetails).toContainText('Storage');
  await expect(subscriptionDetails).toContainText('Pinning');
  await expect(subscriptionDetails).toContainText('Sync');
  await page.getByRole('button', { name: 'Pause' }).click();
  await expect(page.getByRole('button', { name: 'Resume' })).toBeVisible();
  await page.getByRole('button', { name: 'Resume' }).click();
  await expect(page.getByRole('button', { name: 'Pause' })).toBeVisible();
  await page.getByRole('button', { name: 'Verify pins' }).click();
  await expect(page.getByText(/verified pinned PNM shard artifacts|Verified .* PNM shard artifacts/i)).toBeVisible();
  await expect(syncSettings.getByRole('button', { name: 'Query' })).toHaveCount(0);
  await expect(syncSettings.getByRole('button', { name: /retry sync/i })).toBeVisible();

  await page.getByRole('button', { name: 'Explorer', exact: true }).click();
  await expect(page.getByRole('combobox', { name: 'Data type' })).toHaveValue('PNM');
  const dataRows = page.getByRole('table', { name: 'Data rows' });
  await expect(dataRows.getByRole('cell', { name: 'bafy-pnm-cid', exact: true })).toBeVisible();
  await dataRows.getByRole('cell', { name: 'bafy-pnm-cid', exact: true }).click();
  await expect(page.getByLabel('PNM detail')).toContainText('celestrak:gp:OMM.fbs:2026-05-11T03:00:00Z');
  await expect(page.getByText('Reconstituted signature payload')).toBeVisible();
  await page.getByRole('button', { name: 'Verify signature' }).click();
  await expect(page.getByText('Signature not present on this PNM.')).toBeVisible();
  await expect(page.getByRole('table', { name: 'PNM FILE_ID results' })).toContainText('bafy-pnm-cid');
  await expect(dataRows.getByRole('columnheader', { name: 'SIGNATURE' })).toHaveCount(0);
  await expect(page.getByRole('table', { name: 'SQL results' })).toHaveCount(0);
  await expect(page.getByRole('columnheader', { name: 'Bytes' })).toHaveCount(0);
  await expect(dataRows.getByRole('columnheader', { name: 'SEMI_MAJOR_AXIS' })).toHaveCount(0);
  await expect(dataRows.getByRole('columnheader', { name: 'MASS' })).toHaveCount(0);
  await expect(dataRows.getByRole('columnheader', { name: '_rowid' })).toHaveCount(0);
  await expect(dataRows.getByRole('columnheader', { name: '_data' })).toHaveCount(0);

  await page.getByRole('button', { name: 'SQL' }).click();
  const sql = page.getByRole('textbox', { name: 'Master search' });
  await expect(sql).toHaveValue('SELECT * FROM PNM LIMIT 10');
  await sql.fill('SELECT FILE_ID, CID FROM PNM LIMIT 10');
  await page.getByRole('button', { name: 'Run' }).click();
  await expect(dataRows.getByRole('columnheader', { name: 'FILE ID' })).toBeVisible();
  await expect(dataRows.getByRole('cell', { name: 'bafy-pnm-cid', exact: true })).toBeVisible();
  await page.getByRole('textbox', { name: 'Name', exact: true }).fill('PNM ID lookup');
  await page.getByRole('button', { name: 'Save view' }).click();
  const savedViews = page.getByRole('combobox', { name: 'Saved views' });
  await expect(savedViews).toContainText('PNM ID lookup');
  await sql.fill('SELECT * FROM PNM LIMIT 1');
  await page.getByRole('button', { name: 'Apply view' }).click();
  await expect(sql).toHaveValue('SELECT FILE_ID, CID FROM PNM LIMIT 10');
  await page.getByRole('button', { name: 'Delete view' }).click();
  await expect(savedViews).not.toContainText('PNM ID lookup');

  await page.reload();
  await page.getByRole('button', { name: 'My Subscriptions' }).click();
  await expect(page.getByRole('button', { name: 'Pause' })).toBeVisible();
  await expect(page.getByRole('spinbutton', { name: 'PNM storage cap' })).toHaveValue('2.5');
  await expect(page.getByRole('combobox', { name: 'PNM storage unit' })).toHaveValue('MB');
});

test('data route keeps first-load counts as loading when remote counts are not known yet', async ({ page }) => {
  await page.addInitScript(() => {
    window.localStorage.setItem('sdn:data-directory:v1', JSON.stringify({
      peerTrust: { 'local-node': 'marginal' },
      subscriptions: [{
        id: 'local:PNM',
        dataSourceId: 'local',
        peerId: 'local-node',
        standardId: 'PNM',
        providerName: 'CelesTrak Provider',
        providerPublicKey: 'local-node',
        remoteRows: 0,
        storageCap: 1,
        storageUnit: 'GB',
        syncFilter: '',
        createdAt: '2026-05-12T00:00:00.000Z',
        updatedAt: '2026-05-12T00:00:00.000Z',
      }],
    }));
    window.localStorage.setItem('sdn:data-schema-sync:v1', JSON.stringify({
      'local:PNM': { mode: 'sync', storageCap: 1, storageUnit: 'GB' },
    }));
  });

  await page.context().unroute('**/api/v1/data/summary');
  await page.context().unroute('**/api/v1/data/scan');
  await page.context().unroute('**/api/v1/data/query');
  await page.context().route('**/api/v1/data/summary', async (route) => {
    if (new URL(route.request().url()).pathname !== '/api/v1/data/summary') {
      await route.fallback();
      return;
    }
    await route.fulfill({ status: 503, body: 'summary warming up' });
  });
  await page.context().route('**/api/v1/data/scan', async (route) => {
    if (new URL(route.request().url()).pathname !== '/api/v1/data/scan') {
      await route.fallback();
      return;
    }
    await route.fulfill({ status: 503, body: 'scan warming up' });
  });
  await page.context().route('**/api/v1/data/query', async (route) => {
    if (new URL(route.request().url()).pathname !== '/api/v1/data/query') {
      await route.fallback();
      return;
    }
    await route.fulfill({ status: 503, body: 'query warming up' });
  });

  await page.goto('/?api=http://127.0.0.1:5174&gateway=http%3A%2F%2F127.0.0.1%3A8081#/data');
  const overview = page.getByRole('region', { name: 'Data overview' });
  await expect(overview).toBeVisible();
  await expect(page.getByLabel('Data overview summary')).toContainText('Loading');
  await expect(page.getByRole('table', { name: 'Data products' })).toContainText('Loading');
  await expect(overview.getByText('0 local / 0 remote')).toHaveCount(0);

  await page.getByRole('button', { name: 'Storage' }).click();
  const storageState = page.getByLabel('Local storage state');
  await expect(storageState).toBeVisible();
  await expect(storageState).toContainText('Loading');
  await expect(storageState.getByLabel('Remote rows 0')).toHaveCount(0);
  await expect(storageState.getByLabel('Local rows 0')).toHaveCount(0);
  await expect(storageState.getByText('0 local / 0 remote')).toHaveCount(0);
  await expect(storageState.getByText('No remote rows')).toHaveCount(0);
});

test('data route shows retry instead of query for sync-error subscriptions', async ({ page }) => {
  await page.addInitScript(() => {
    window.localStorage.setItem('sdn:data-directory:v1', JSON.stringify({
      peerTrust: { 'local-node': 'marginal' },
      subscriptions: [{
        id: 'local:PNM',
        dataSourceId: 'local',
        peerId: 'local-node',
        standardId: 'PNM',
        providerName: 'CelesTrak Provider',
        providerPublicKey: 'local-node',
        remoteRows: 12,
        storageCap: 1,
        storageUnit: 'GB',
        syncFilter: 'FILE_ID LIKE celestrak:%',
        createdAt: '2026-05-12T00:00:00.000Z',
        updatedAt: '2026-05-12T00:00:00.000Z',
      }],
    }));
    window.localStorage.setItem('sdn:data-schema-sync:v1', JSON.stringify({
      'local:PNM': { mode: 'sync', storageCap: 1, storageUnit: 'GB' },
    }));
    window.localStorage.setItem('sdn:data-schema-sync-state:v1', JSON.stringify({
      'local:PNM': {
        status: 'error',
        syncedRows: 10,
        totalRows: 12,
        localRows: 10,
        pinnedRows: 10,
        cachedBytes: 4096,
        error: 'failed to dial remote FlatSQL sync peer',
      },
    }));
  });

  await page.goto('/?api=http://127.0.0.1:5174&gateway=http%3A%2F%2F127.0.0.1%3A8081#/data');
  await page.getByRole('button', { name: 'My Subscriptions' }).click();

  const syncSettings = page.getByLabel('Sync settings');
  const row = syncSettings.locator('article').filter({ hasText: 'CelesTrak Provider' });
  await expect(row.getByLabel(/Status: Sync error/)).toBeVisible();
  await expect(row).toContainText('failed to dial remote FlatSQL sync peer');
  await expect(row.getByRole('button', { name: 'Query' })).toHaveCount(0);
  await expect(row.getByRole('button', { name: /retry sync/i })).toBeVisible();
  await expect(page.getByRole('textbox', { name: 'PNM sync filter' })).toHaveValue('FILE_ID LIKE celestrak:%');

  await row.getByRole('button', { name: /retry sync/i }).click();
  await expect(row.getByLabel(/Status: Queued/)).toBeVisible();
});

test('data catalog rows highlight and expand actions when clicked', async ({ page }) => {
  await page.addInitScript(() => {
    window.localStorage.setItem('sdn:data-directory:v1', JSON.stringify({
      peerTrust: { 'local-node': 'marginal' },
      subscriptions: [{
        id: 'local:OMM',
        dataSourceId: 'local',
        peerId: 'local-node',
        standardId: 'OMM',
        providerName: 'CelesTrak Provider',
        providerPublicKey: 'local-node',
        remoteRows: 2,
        storageCap: 1,
        storageUnit: 'GB',
        syncFilter: '',
        createdAt: '2026-05-12T00:00:00.000Z',
        updatedAt: '2026-05-12T00:00:00.000Z',
      }],
    }));
    window.localStorage.setItem('sdn:data-schema-sync:v1', JSON.stringify({
      'local:OMM': { mode: 'sync', storageCap: 1, storageUnit: 'GB' },
    }));
  });
  await page.context().unroute('**/api/v1/data/scan');
  await page.context().route('**/api/v1/data/scan', async (route) => {
    if (new URL(route.request().url()).pathname !== '/api/v1/data/scan') {
      await route.fallback();
      return;
    }
    const body = route.request().postDataJSON();
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({
        schema: body.schema ?? 'OMM.fbs',
        total_count: 0,
        count: 0,
        limit: body.limit ?? 10,
        offset: body.offset ?? 0,
        cursor: '',
        next_cursor: '',
        snapshot_id: 'catalog-row-expansion-snapshot',
        head: 'catalog-row-expansion-snapshot',
        high_water_mark: '',
        scan_hash: 'catalog-row-expansion-scan',
        chunk_hash: 'catalog-row-expansion-scan',
        query_profile: 'ordered-offset-v1',
        sync_protocol: '/space-data-network/flatsql-sync/1.0.0',
        max_chunk_size: 50000,
        transports: ['libp2p-websocket', 'libp2p-webrtc'],
        results: [],
      }),
    });
  });

  await page.goto('/?api=http://127.0.0.1:5174&gateway=http%3A%2F%2F127.0.0.1%3A8081#/data');
  await page.getByRole('button', { name: 'Catalog' }).click();

  const catalogTable = page.getByRole('table', { name: 'Catalog data products' });
  const row = catalogTable.locator('tbody tr.sdn-catalog-row').filter({ hasText: 'CelesTrak Provider' }).first();
  await expect(row).toBeVisible();
  await expect(row).toHaveAttribute('aria-expanded', 'false');

  const providerCell = row.locator('td').first().getByRole('button');
  if (test.info().project.name.includes('mobile')) {
    await providerCell.tap();
  } else {
    await providerCell.click();
  }

  await expect(row).toHaveAttribute('aria-expanded', 'true');
  await expect(row).toHaveClass(/active/);
  await expect(row).toHaveClass(/sdn-catalog-expanded/);
  await expect(catalogTable.locator('.sdn-catalog-action-panel')).toBeVisible();
  await expect(catalogTable.locator('.sdn-catalog-detail-grid')).toBeVisible();
  await expect(catalogTable.locator('.sdn-catalog-detail-grid')).toContainText('Provider');
  await expect(catalogTable.locator('.sdn-catalog-detail-grid')).toContainText('Message types');
  await expect(catalogTable.locator('.sdn-catalog-detail-grid')).toContainText('Storage estimate');
  await expect(catalogTable.locator('.sdn-catalog-detail-grid')).toContainText('PGP: Limited trust');
  await expect(catalogTable.getByRole('button', { name: 'Manage storage' })).toBeVisible();
  await expect(catalogTable.getByRole('button', { name: 'Manage', exact: true })).toBeVisible();
  await expect(catalogTable.getByRole('button', { name: 'Open Explorer' })).toBeVisible();

  await page.waitForTimeout(200);
  await page.getByRole('textbox', { name: 'Search' }).click();
  await expect(row).toHaveAttribute('aria-expanded', 'false');
  await expect(catalogTable.locator('.sdn-catalog-action-panel')).toHaveCount(0);
});

test('message types sort by remote rows and expose schema health actions', async ({ page }) => {
  await page.addInitScript(() => {
    window.localStorage.setItem('sdn:data-directory:v1', JSON.stringify({
      peerTrust: { 'local-node': 'marginal' },
      subscriptions: [
        {
          id: 'local:OMM',
          dataSourceId: 'local',
          peerId: 'local-node',
          standardId: 'OMM',
          providerName: 'CelesTrak Provider',
          providerPublicKey: 'local-node',
          remoteRows: 2,
          storageCap: 1,
          storageUnit: 'GB',
          syncFilter: '',
          createdAt: '2026-05-12T00:00:00.000Z',
          updatedAt: '2026-05-12T00:00:00.000Z',
        },
        {
          id: 'local:CAT',
          dataSourceId: 'local',
          peerId: 'local-node',
          standardId: 'CAT',
          providerName: 'CelesTrak Provider',
          providerPublicKey: 'local-node',
          remoteRows: 2500,
          storageCap: 1,
          storageUnit: 'GB',
          syncFilter: '',
          createdAt: '2026-05-12T00:00:00.000Z',
          updatedAt: '2026-05-12T00:00:00.000Z',
        },
      ],
    }));
    window.localStorage.setItem('sdn:data-schema-sync:v1', JSON.stringify({
      'local:OMM': { mode: 'sync', storageCap: 1, storageUnit: 'GB' },
      'local:CAT': { mode: 'sync', storageCap: 1, storageUnit: 'GB' },
    }));
  });
  await page.context().unroute('**/api/v1/data/scan');
  await page.context().route('**/api/v1/data/scan', async (route) => {
    if (new URL(route.request().url()).pathname !== '/api/v1/data/scan') {
      await route.fallback();
      return;
    }
    const body = route.request().postDataJSON();
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({
        schema: body.schema ?? 'CAT.fbs',
        total_count: 0,
        count: 0,
        limit: body.limit ?? 10,
        offset: body.offset ?? 0,
        cursor: '',
        next_cursor: '',
        snapshot_id: 'message-type-snapshot',
        head: 'message-type-snapshot',
        high_water_mark: '',
        scan_hash: 'message-type-scan',
        chunk_hash: 'message-type-scan',
        query_profile: 'ordered-offset-v1',
        sync_protocol: '/space-data-network/flatsql-sync/1.0.0',
        max_chunk_size: 50000,
        transports: ['libp2p-websocket', 'libp2p-webrtc'],
        results: [],
      }),
    });
  });

  await page.goto('/?api=http://127.0.0.1:5174&gateway=http%3A%2F%2F127.0.0.1%3A8081#/data');
  await page.getByRole('button', { name: 'Message Types' }).click();

  const messageTypes = page.getByRole('table', { name: 'Message types' });
  const firstRow = messageTypes.locator('tbody tr').first();
  await expect(firstRow).toContainText('CAT');
  await expect(firstRow).toContainText('2,500');
  await expect(messageTypes.getByRole('columnheader', { name: 'Remote' })).toBeVisible();
  await expect(messageTypes.getByRole('columnheader', { name: 'Local' })).toBeVisible();
  await expect(messageTypes.getByRole('columnheader', { name: 'Pinned' })).toBeVisible();
  await expect(messageTypes.getByRole('columnheader', { name: 'Freshness' })).toBeVisible();
  await expect(firstRow.getByRole('button', { name: 'Explorer' })).toBeVisible();
  await expect(firstRow.getByRole('button', { name: 'Manage' })).toBeVisible();
  await expect(firstRow.getByRole('button', { name: 'Retry' })).toBeVisible();
});

test('data route keeps same-schema subscriptions separated by datastore namespace', async ({ page }) => {
  await page.addInitScript(() => {
    window.localStorage.setItem('sdn:data-directory:v1', JSON.stringify({
      peerTrust: { 'local-node': 'marginal' },
      subscriptions: [
        {
          id: 'local:OMM:sdn-ds-history',
          dataSourceId: 'local',
          peerId: 'local-node',
          datastoreKey: 'sdn-ds-history',
          standardId: 'OMM',
          providerName: 'CelesTrak Historical',
          providerPublicKey: 'local-node',
          remoteRows: 44_349_135,
          storageCap: 5,
          storageUnit: 'GB',
          syncFilter: '',
          createdAt: '2026-05-12T00:00:00.000Z',
          updatedAt: '2026-05-12T00:00:00.000Z',
        },
        {
          id: 'local:OMM:sdn-ds-live',
          dataSourceId: 'local',
          peerId: 'local-node',
          datastoreKey: 'sdn-ds-live',
          standardId: 'OMM',
          providerName: 'CelesTrak Live',
          providerPublicKey: 'local-node',
          remoteRows: 2_287_018,
          storageCap: 1,
          storageUnit: 'GB',
          syncFilter: '',
          createdAt: '2026-05-12T00:00:00.000Z',
          updatedAt: '2026-05-12T00:00:00.000Z',
        },
      ],
    }));
    window.localStorage.setItem('sdn:data-schema-sync:v1', JSON.stringify({
      'local:OMM': { mode: 'preview', storageCap: 1, storageUnit: 'GB' },
      'local:OMM:sdn-ds-history': { mode: 'preview', storageCap: 5, storageUnit: 'GB' },
      'local:OMM:sdn-ds-live': { mode: 'preview', storageCap: 1, storageUnit: 'GB' },
    }));
  });

  await page.context().unroute('**/api/v1/data/summary');
  await page.context().unroute('**/api/v1/data/scan');
  await page.context().unroute('**/api/v1/data/stream');

  await page.context().route('**/api/v1/data/summary', async (route) => {
    if (new URL(route.request().url()).pathname !== '/api/v1/data/summary') {
      await route.fallback();
      return;
    }
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({
        total_records: 46_636_153,
        total_bytes: 1_337_000_000,
        schemas: [{ schema_name: 'OMM.fbs', count: 46_636_153, total_bytes: 1_337_000_000 }],
        sources: [
          {
            datastore_key: 'sdn-ds-history',
            schema_name: 'OMM.fbs',
            provider_id: 'local',
            source_name: 'celestrak-gp-historical',
            batch_id: 'history',
            count: 44_349_135,
            total_bytes: 1_000_000_000,
          },
          {
            datastore_key: 'sdn-ds-live',
            schema_name: 'OMM.fbs',
            provider_id: 'local',
            source_name: 'celestrak-gp',
            batch_id: 'live',
            count: 2_287_018,
            total_bytes: 337_000_000,
          },
        ],
      }),
    });
  });

  const scanDatastoreKeys: string[] = [];
  await page.context().route('**/api/v1/data/scan', async (route) => {
    if (new URL(route.request().url()).pathname !== '/api/v1/data/scan') {
      await route.fallback();
      return;
    }
    const body = route.request().postDataJSON();
    expect(body.schema).toBe('OMM.fbs');
    const datastoreKey = body.datastore_key ?? body.datastoreKey ?? '';
    scanDatastoreKeys.push(datastoreKey);
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({
        schema: 'OMM.fbs',
        total_count: datastoreKey === 'sdn-ds-live' ? 2_287_018 : 44_349_135,
        count: 1,
        limit: 10,
        offset: 0,
        cursor: 'MA',
        next_cursor: '',
        snapshot_id: `${datastoreKey}-snapshot`,
        head: `${datastoreKey}-head`,
        high_water_mark: `${datastoreKey}:1`,
        scan_hash: `${datastoreKey}-scan`,
        chunk_hash: `${datastoreKey}-scan`,
        query_profile: 'ordered-offset-v1',
        sync_protocol: '/space-data-network/flatsql-sync/1.0.0',
        max_chunk_size: 50000,
        transports: ['http', 'libp2p-websocket', 'libp2p-webrtc'],
        results: [{
          schema_name: 'OMM.fbs',
          cid: `${datastoreKey}-cid`,
          peer_id: 'source:celestrak',
          provider_id: 'local',
          source_name: datastoreKey === 'sdn-ds-live' ? 'celestrak-gp' : 'celestrak-gp-historical',
          batch_id: datastoreKey === 'sdn-ds-live' ? 'live' : 'history',
          timestamp: '2026-05-11T04:02:25Z',
          size_bytes: 288,
        }],
      }),
    });
  });
  await page.context().route('**/api/v1/data/stream', async (route) => {
    if (new URL(route.request().url()).pathname !== '/api/v1/data/stream') {
      await route.fallback();
      return;
    }
    await route.fulfill({
      contentType: 'application/vnd.sdn.flatbuffers.stream',
      body: rawFlatbufferStream([STARLINK_6292_OMM_BYTES]),
    });
  });

  await page.goto('/?api=http://127.0.0.1:5174&gateway=http%3A%2F%2F127.0.0.1%3A8081#/data');
  await page.getByRole('button', { name: 'My Subscriptions' }).click();
  const liveRow = page.getByLabel('Sync settings').locator('article').filter({ hasText: 'CelesTrak Live' });
  await expect(liveRow).toContainText('2,287,018 remote');
  await expect(liveRow.getByRole('button', { name: 'Query' })).toHaveCount(0);
  await expect(liveRow.getByRole('button', { name: /retry sync/i })).toBeVisible();

  scanDatastoreKeys.length = 0;
  await page.getByRole('button', { name: 'Explorer' }).click();
  await page.getByRole('combobox', { name: 'Source' }).selectOption('local:datastore:sdn-ds-live');
  await page.getByRole('combobox', { name: 'Data type' }).selectOption('OMM');

  await expect(page.getByRole('combobox', { name: 'Data type' })).toHaveValue('OMM');
  await expect.poll(() => scanDatastoreKeys.at(-1) ?? '').toBe('sdn-ds-live');
});

test('data route runs local SQL against locally synced CelesTrak rows without remote SQL', async ({ page }) => {
  await page.addInitScript(() => {
    window.localStorage.setItem('sdn:data-directory:v1', JSON.stringify({
      peerTrust: { 'local-node': 'marginal' },
      subscriptions: [{
        id: 'local:OMM:sdn-ds-live',
        dataSourceId: 'local',
        peerId: 'local-node',
        datastoreKey: 'sdn-ds-live',
        standardId: 'OMM',
        providerName: 'CelesTrak Live',
        providerPublicKey: 'local-node',
        remoteRows: 2_287_018,
        storageCap: 1,
        storageUnit: 'GB',
        syncFilter: '',
        createdAt: '2026-05-12T00:00:00.000Z',
        updatedAt: '2026-05-12T00:00:00.000Z',
      }],
    }));
    window.localStorage.setItem('sdn:data-schema-sync:v1', JSON.stringify({
      'local:OMM:sdn-ds-live': { mode: 'preview', storageCap: 1, storageUnit: 'GB' },
    }));
  });

  await page.context().unroute('**/api/v1/data/summary');
  await page.context().unroute('**/api/v1/data/scan');
  await page.context().unroute('**/api/v1/data/stream');
  await page.context().unroute('**/api/v1/data/query');

  const remoteQueryBodies: string[] = [];
  await page.context().route('**/api/v1/data/query**', async (route) => {
    remoteQueryBodies.push(route.request().postData() ?? route.request().url());
    await route.fulfill({ status: 418, body: 'remote SQL is not allowed for local LLM queries' });
  });

  await page.context().route('**/api/v1/data/summary', async (route) => {
    if (new URL(route.request().url()).pathname !== '/api/v1/data/summary') {
      await route.fallback();
      return;
    }
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({
        total_records: 2_287_018,
        total_bytes: 337_000_000,
        schemas: [{ schema_name: 'OMM.fbs', count: 2_287_018, total_bytes: 337_000_000 }],
        sources: [{
          datastore_key: 'sdn-ds-live',
          schema_name: 'OMM.fbs',
          provider_id: 'local',
          source_name: 'celestrak-gp',
          batch_id: 'live',
          count: 2_287_018,
          total_bytes: 337_000_000,
        }],
      }),
    });
  });

  await page.context().route('**/api/v1/data/scan', async (route) => {
    if (new URL(route.request().url()).pathname !== '/api/v1/data/scan') {
      await route.fallback();
      return;
    }
    const body = route.request().postDataJSON();
    expect(body).toMatchObject({ schema: 'OMM.fbs', datastore_key: 'sdn-ds-live' });
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({
        schema: 'OMM.fbs',
        total_count: 2_287_018,
        count: 2,
        limit: 10,
        offset: 0,
        cursor: 'MA',
        next_cursor: '',
        snapshot_id: 'sdn-ds-live-snapshot',
        head: 'sdn-ds-live-head',
        high_water_mark: 'sdn-ds-live:1',
        scan_hash: 'sdn-ds-live-scan',
        chunk_hash: 'sdn-ds-live-scan',
        query_profile: 'ordered-offset-v1',
        sync_protocol: '/space-data-network/flatsql-sync/1.0.0',
        max_chunk_size: 50000,
        transports: ['libp2p-websocket', 'libp2p-webrtc'],
        results: [
          {
            schema_name: 'OMM.fbs',
            cid: 'sdn-ds-live-cid',
            peer_id: 'source:celestrak',
            provider_id: 'local',
            source_name: 'celestrak-gp',
            batch_id: 'live',
            timestamp: '2026-05-11T04:02:25Z',
            size_bytes: 288,
          },
          {
            schema_name: 'OMM.fbs',
            cid: 'sdn-ds-live-high-orbit-cid',
            peer_id: 'source:celestrak',
            provider_id: 'local',
            source_name: 'celestrak-gp',
            batch_id: 'live',
            timestamp: '2026-05-11T04:02:26Z',
            size_bytes: 288,
          },
        ],
      }),
    });
  });

  await page.context().route('**/api/v1/data/stream', async (route) => {
    if (new URL(route.request().url()).pathname !== '/api/v1/data/stream') {
      await route.fallback();
      return;
    }
    await route.fulfill({
      contentType: 'application/vnd.sdn.flatbuffers.stream',
      body: rawFlatbufferStream([STARLINK_6292_OMM_BYTES, GEO_SOVIET_TEST_OMM_BYTES]),
    });
  });

  await page.goto('/?api=http://127.0.0.1:5174&gateway=http%3A%2F%2F127.0.0.1%3A8081#/data');
  await page.getByRole('button', { name: 'Explorer' }).click();
  await page.getByRole('combobox', { name: 'Data type' }).selectOption('OMM');

  const dataRows = page.getByRole('table', { name: 'Data rows' });
  await expect(dataRows.getByRole('cell', { name: 'STARLINK-6292', exact: true })).toBeVisible();

  await page.getByRole('button', { name: 'SQL' }).click();
  const masterSearch = page.getByRole('textbox', { name: 'Master search' });
  await masterSearch.fill("SELECT * FROM OMM WHERE EPOCH >= '2026-05-10T00:00:00Z' AND EPOCH < '2026-05-11T00:00:00Z' AND NORAD_CAT_ID = 56775 ORDER BY EPOCH ASC, NORAD_CAT_ID ASC LIMIT 10");
  await page.getByRole('button', { name: 'Run' }).click();
  await expect(masterSearch).toHaveValue("SELECT * FROM OMM WHERE EPOCH >= '2026-05-10T00:00:00Z' AND EPOCH < '2026-05-11T00:00:00Z' AND NORAD_CAT_ID = 56775 ORDER BY EPOCH ASC, NORAD_CAT_ID ASC LIMIT 10");
  await expect(dataRows.getByRole('cell', { name: 'STARLINK-6292', exact: true })).toBeVisible();
  await expect(dataRows.getByRole('cell', { name: '56775', exact: true })).toBeVisible();

  await masterSearch.fill('SELECT * FROM OMM WHERE MEAN_MOTION < 1 LIMIT 10');
  await page.getByRole('button', { name: 'Run' }).click();
  await expect(dataRows.getByRole('cell', { name: 'GEO-SOVIET-TEST', exact: true })).toBeVisible();
  expect(remoteQueryBodies.join('\n')).not.toContain('SELECT * FROM OMM WHERE MEAN_MOTION < 1');
});

test('data route keeps the shell fixed while the content pane scrolls', async ({ page }) => {
  await page.goto('/?api=http://127.0.0.1:5174&gateway=http%3A%2F%2F127.0.0.1%3A8081#/data');

  const layout = await page.evaluate(() => {
    const content = document.querySelector<HTMLElement>('.sdn-content');
    const topBar = document.querySelector<HTMLElement>('.sdn-top-bar');
    if (!content || !topBar) throw new Error('missing app shell');
    const filler = document.createElement('div');
    filler.style.height = '1600px';
    filler.setAttribute('data-test-filler', 'true');
    content.append(filler);
    const beforeTop = topBar.getBoundingClientRect().top;
    content.scrollTop = 480;
    window.scrollTo(0, 480);
    const afterTop = topBar.getBoundingClientRect().top;
    return {
      bodyOverflow: getComputedStyle(document.body).overflow,
      bodyClientHeight: document.body.clientHeight,
      bodyScrollHeight: document.body.scrollHeight,
      contentOverflow: getComputedStyle(content).overflow,
      contentClientHeight: content.clientHeight,
      contentScrollHeight: content.scrollHeight,
      contentScrollTop: content.scrollTop,
      windowScrollY: window.scrollY,
      beforeTop,
      afterTop,
    };
  });

  expect(layout.bodyOverflow).toBe('hidden');
  expect(layout.bodyScrollHeight).toBe(layout.bodyClientHeight);
  expect(layout.contentOverflow).toContain('auto');
  expect(layout.contentScrollHeight).toBeGreaterThan(layout.contentClientHeight);
  expect(layout.contentScrollTop).toBeGreaterThan(0);
  expect(layout.windowScrollY).toBe(0);
  expect(layout.afterTop).toBe(layout.beforeTop);
});

test('explore route renders CID inspection with the configured gateway', async ({ page }) => {
  await page.goto('/?api=http://127.0.0.1:5174&gateway=http%3A%2F%2F127.0.0.1%3A8081#/explore/bafySdnFixture');

  await expect(page.getByRole('heading', { level: 1, name: 'Data' })).toBeVisible();
  await expect(page.getByText('bafySdnFixture')).toBeVisible();
  await expect(page.getByRole('link', { name: 'Open Gateway' })).toHaveAttribute(
    'href',
    'http://127.0.0.1:8081/ipfs/bafySdnFixture',
  );
});

test('command buttons use operational radii rather than fully rounded bubbles', async ({ page }) => {
  await page.goto('/?api=http://127.0.0.1:5174&gateway=http%3A%2F%2F127.0.0.1%3A8081#/node');

  const radii = await page.locator('button').evaluateAll((buttons) => buttons.map((button) => {
    const radius = window.getComputedStyle(button).borderRadius.split(' ')[0] ?? '0px';
    return Number.parseFloat(radius);
  }));

  expect(radii.length).toBeGreaterThan(0);
  expect(radii.every((radius) => radius <= 12)).toBe(true);
});

test('SDN product routes do not navigate into upstream /webui', async ({ page }) => {
  await page.goto('/?api=http://127.0.0.1:5174&gateway=http%3A%2F%2F127.0.0.1%3A8081#/node');

  await page.getByRole('link', { name: 'Peers' }).click();
  await expect(page).not.toHaveURL(/\/webui/);
  await page.getByRole('link', { name: 'Data' }).click();
  await expect(page).not.toHaveURL(/\/webui/);
});

test('node self EPM email stays visible after the persisted EPM reloads', async ({ page }) => {
  await page.route('**/api/identity/epms', async (route) => {
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({
        epms: [
          {
            id: 'self',
            kind: 'node-self',
            epm_json: {
              dn: 'Space Data Network Desktop',
              peer_id: '12D3KooWNZMVqKBHke7bQJ6JTs2zp13DTZu441UNs6hZcZ3bUwMs',
              agent_version: 'kubo/0.39.0/sdn-desktop',
              email: 'persisted-node@example.invalid',
            },
          },
        ],
      }),
    });
  });

  await page.goto('/?api=http://127.0.0.1:5174&gateway=http%3A%2F%2F127.0.0.1%3A8081#/node');

  await expect(page.getByText('persisted-node@example.invalid')).toBeVisible();
});

test('captures desktop and mobile SDN UI screenshots', async ({ page }, testInfo) => {
  for (const route of ['node', 'peers', 'data']) {
    await page.goto(`/?api=http://127.0.0.1:5174&gateway=http%3A%2F%2F127.0.0.1%3A8081#/${route}`);
    await assertVisualGuardrails(page);
    const screenshot = await page.screenshot({
      path: testInfo.outputPath(`${route}-${testInfo.project.name}.png`),
      fullPage: true,
    });
    expect(screenshot.length).toBeGreaterThan(10_000);
  }
});

async function assertVisualGuardrails(page: Page): Promise<void> {
  const metrics = await page.evaluate(() => {
    const root = window.getComputedStyle(document.documentElement);
    const cards = Array.from(document.querySelectorAll<HTMLElement>('.sdn-card'));
    const controls = Array.from(document.querySelectorAll<HTMLElement>('button, a, input, .sdn-button, .sdn-input'));
    return {
      scrollWidth: document.documentElement.scrollWidth,
      viewportWidth: window.innerWidth,
      bodyTextLength: document.body.innerText.trim().length,
      tokens: {
        bg: root.getPropertyValue('--sdn-bg').trim(),
        surface: root.getPropertyValue('--sdn-surface').trim(),
        blue: root.getPropertyValue('--sdn-blue').trim(),
        green: root.getPropertyValue('--sdn-green').trim(),
        amber: root.getPropertyValue('--sdn-amber').trim(),
        red: root.getPropertyValue('--sdn-red').trim(),
        purple: root.getPropertyValue('--sdn-purple').trim(),
      },
      cardRadii: cards.map((card) => Number.parseFloat(window.getComputedStyle(card).borderRadius)),
      controlRadii: controls.map((control) => Number.parseFloat(window.getComputedStyle(control).borderRadius)),
    };
  });

  expect(metrics.bodyTextLength).toBeGreaterThan(100);
  expect(metrics.scrollWidth).toBeLessThanOrEqual(metrics.viewportWidth + 1);
  expect(metrics.tokens).toEqual({
    bg: '#000000',
    surface: '#0b0d10',
    blue: '#0a84ff',
    green: '#30d158',
    amber: '#ffd60a',
    red: '#ff453a',
    purple: '#bf5af2',
  });
  expect(metrics.cardRadii.length).toBeGreaterThan(0);
  expect(metrics.cardRadii.every((radius) => radius <= 12)).toBe(true);
  expect(metrics.controlRadii.length).toBeGreaterThan(0);
  expect(metrics.controlRadii.every((radius) => radius <= 12)).toBe(true);
}

async function installSdnFixtures(page: Page): Promise<void> {
  await page.route('**/api/node/epm/json', async (route) => {
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({
        dn: 'Space Data Network Desktop',
        peer_id: '12D3KooWNZMVqKBHke7bQJ6JTs2zp13DTZu441UNs6hZcZ3bUwMs',
        agent_version: 'kubo/0.39.0/sdn-desktop',
      }),
    });
  });
  await page.route('**/api/peers/sdn', async (route) => {
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify([
        {
          id: realPeerId,
          name: 'CelesTrak Provider',
          addrs: [`/ip4/167.172.219.213/tcp/4001/p2p/${realPeerId}`],
          trust_level: 'trusted',
          metadata: {
            agent_version: 'spacedatanetwork/1.0.3',
            protocols: '/space-data-network/module-delivery/1.0.0',
          },
        },
      ]),
    });
  });
  await page.route('**/api/local/sdn-nodes', async (route) => {
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({
        nodes: [
          {
            id: 'space-data-network-02',
            name: 'CelesTrak Provider',
            addrs: [],
            trust_level: 'trusted',
            metadata: {
              agent_version: 'sdn-configured-node',
              admin_proxy_path: '/api/local/sdn-nodes/space-data-network-02',
              host_name: '167.172.219.213',
              peer_id: realPeerId,
              public_key: realPeerId,
              protocols: '/space-data-network/configured-node/1.0.0',
            },
          },
        ],
      }),
    });
  });
  await page.route('**/api/local/sdn-nodes/space-data-network-02/api/v1/data/summary', async (route) => {
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({
        total_records: 16,
        total_bytes: 3456,
        schemas: [
          { schema_name: 'CAT.fbs', count: 1, total_bytes: 144 },
          { schema_name: 'EPM.fbs', count: 1, total_bytes: 144 },
          { schema_name: 'OMM.fbs', count: 2, total_bytes: 576 },
          { schema_name: 'PNM.fbs', count: 12, total_bytes: 1728 },
        ],
        sources: [
          {
            schema_name: 'PNM.fbs',
            provider_id: 'space-data-network-02',
            source_name: 'celestrak-publication-log',
            batch_id: 'fixture-pnm-batch',
            count: 12,
            total_bytes: 1728,
          },
          {
            schema_name: 'OMM.fbs',
            provider_id: 'space-data-network-02',
            source_name: 'celestrak-gp',
            batch_id: 'fixture-batch',
            count: 2,
            total_bytes: 576,
          },
        ],
      }),
    });
  });
  await page.context().route('**/api/v1/data/summary', async (route) => {
    if (new URL(route.request().url()).pathname !== '/api/v1/data/summary') {
      await route.fallback();
      return;
    }
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({
        total_records: 16,
        total_bytes: 3456,
        schemas: [
          { schema_name: 'CAT.fbs', count: 1, total_bytes: 144 },
          { schema_name: 'EPM.fbs', count: 1, total_bytes: 144 },
          { schema_name: 'OMM.fbs', count: 2, total_bytes: 576 },
          { schema_name: 'PNM.fbs', count: 12, total_bytes: 1728 },
        ],
        sources: [
          {
            schema_name: 'PNM.fbs',
            provider_id: 'local',
            source_name: 'celestrak-publication-log',
            batch_id: 'fixture-pnm-batch',
            count: 12,
            total_bytes: 1728,
          },
          {
            schema_name: 'OMM.fbs',
            provider_id: 'local',
            source_name: 'celestrak-gp',
            batch_id: 'fixture-batch',
            count: 2,
            total_bytes: 576,
          },
        ],
      }),
    });
  });
  await page.route('**/api/local/sdn-nodes/space-data-network-02/api/v1/data/scan', async (route) => {
    const body = route.request().postDataJSON();
    expect(body.schema).toBe('PNM.fbs');
    expect(body.include_data).toBe(false);
    const cursorOffset = typeof body.cursor === 'string' && body.cursor
      ? Number(Buffer.from(body.cursor, 'base64url').toString('utf8'))
      : 0;
    const offset = Number(body.offset ?? cursorOffset);
    const limit = Number(body.limit ?? 10);
    if (offset > 0) expect(limit).toBe(25_000);
    const pageRefs = PNM_FIXTURE_REFS.slice(offset, offset + limit);
    const nextOffset = offset + pageRefs.length;
    const nextCursor = nextOffset < PNM_FIXTURE_REFS.length
      ? Buffer.from(String(nextOffset), 'utf8').toString('base64url')
      : '';
    await new Promise((resolve) => setTimeout(resolve, 100));
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({
        schema: 'PNM.fbs',
        total_count: PNM_FIXTURE_REFS.length,
        count: pageRefs.length,
        limit,
        offset,
        cursor: Buffer.from(String(offset), 'utf8').toString('base64url'),
        next_cursor: nextCursor,
        snapshot_id: 'fixture-snapshot',
        head: 'fixture-snapshot',
        high_water_mark: '1:2:3:2',
        scan_hash: 'fixture-scan-hash',
        chunk_hash: 'fixture-scan-hash',
        query_profile: 'ordered-offset-v1',
        sync_protocol: '/space-data-network/flatsql-sync/1.0.0',
        max_chunk_size: 50000,
        transports: ['http', 'libp2p-websocket', 'libp2p-webrtc'],
        results: pageRefs.map(({ cid }, index) => ({
          schema_name: 'PNM.fbs',
          cid,
          peer_id: 'source:celestrak',
          provider_id: 'space-data-network-02',
          source_name: 'celestrak-publication-log',
          batch_id: 'fixture-pnm-batch',
          timestamp: `2026-05-11T04:${String(offset + index).padStart(2, '0')}:25Z`,
          size_bytes: 144,
        })),
      }),
    });
  });
  await page.context().route('**/api/v1/data/scan', async (route) => {
    if (new URL(route.request().url()).pathname !== '/api/v1/data/scan') {
      await route.fallback();
      return;
    }
    const body = route.request().postDataJSON();
    expect(body.schema).toBe('PNM.fbs');
    expect(body.include_data).toBe(false);
    const cursorOffset = typeof body.cursor === 'string' && body.cursor
      ? Number(Buffer.from(body.cursor, 'base64url').toString('utf8'))
      : 0;
    const offset = Number(body.offset ?? cursorOffset);
    const limit = Number(body.limit ?? 10);
    const pageRefs = PNM_FIXTURE_REFS.slice(offset, offset + limit);
    const nextOffset = offset + pageRefs.length;
    const nextCursor = nextOffset < PNM_FIXTURE_REFS.length
      ? Buffer.from(String(nextOffset), 'utf8').toString('base64url')
      : '';
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({
        schema: 'PNM.fbs',
        total_count: PNM_FIXTURE_REFS.length,
        count: pageRefs.length,
        limit,
        offset,
        cursor: Buffer.from(String(offset), 'utf8').toString('base64url'),
        next_cursor: nextCursor,
        snapshot_id: 'fixture-snapshot',
        head: 'fixture-snapshot',
        high_water_mark: '1:2:3:2',
        scan_hash: 'fixture-scan-hash',
        chunk_hash: 'fixture-scan-hash',
        query_profile: 'ordered-offset-v1',
        sync_protocol: '/space-data-network/flatsql-sync/1.0.0',
        max_chunk_size: 50000,
        transports: ['http', 'libp2p-websocket', 'libp2p-webrtc'],
        results: pageRefs.map(({ cid }, index) => ({
          schema_name: 'PNM.fbs',
          cid,
          peer_id: 'source:celestrak',
          provider_id: 'local',
          source_name: 'celestrak-publication-log',
          batch_id: 'fixture-pnm-batch',
          timestamp: `2026-05-11T04:${String(offset + index).padStart(2, '0')}:25Z`,
          size_bytes: 144,
        })),
      }),
    });
  });
  await page.route('**/api/local/sdn-nodes/space-data-network-02/api/v1/data/stream', async (route) => {
    const body = route.request().postDataJSON();
    expect(body.schema).toBe('PNM.fbs');
    expect(body.scan_hash).toBe('fixture-scan-hash');
    expect(body.chunk_hash).toBe('fixture-scan-hash');
    expect(body.snapshot_id).toBe('fixture-snapshot');
    expect(route.request().headers().accept).toContain('application/vnd.sdn.flatbuffers.stream');
    const records = Array.isArray(body.records) ? body.records : [];
    const buffers = records.map((record: { cid: string }) => PNM_FIXTURE_REFS.find((fixture) => fixture.cid === record.cid)?.bytes ?? PNM_BYTES);
    await route.fulfill({
      contentType: 'application/vnd.sdn.flatbuffers.stream',
      body: rawFlatbufferStream(buffers),
    });
  });
  await page.context().route('**/api/v1/data/stream', async (route) => {
    if (new URL(route.request().url()).pathname !== '/api/v1/data/stream') {
      await route.fallback();
      return;
    }
    const body = route.request().postDataJSON();
    expect(body.schema).toBe('PNM.fbs');
    expect(body.scan_hash).toBe('fixture-scan-hash');
    expect(body.chunk_hash).toBe('fixture-scan-hash');
    expect(body.snapshot_id).toBe('fixture-snapshot');
    expect(route.request().headers().accept).toContain('application/vnd.sdn.flatbuffers.stream');
    const records = Array.isArray(body.records) ? body.records : [];
    const buffers = records.map((record: { cid: string }) => PNM_FIXTURE_REFS.find((fixture) => fixture.cid === record.cid)?.bytes ?? PNM_BYTES);
    await route.fulfill({
      contentType: 'application/vnd.sdn.flatbuffers.stream',
      body: rawFlatbufferStream(buffers),
    });
  });
  await page.route('**/api/local/sdn-nodes/space-data-network-02/api/v1/data/query', async (route) => {
    const body = route.request().postDataJSON();
    if (body.schema === 'PNM.fbs') {
      const offset = Number(body.offset ?? 0);
      const limit = Number(body.limit ?? 10);
      const pageRefs = PNM_FIXTURE_REFS.slice(offset, offset + limit);
      if (route.request().headers().accept?.includes('application/vnd.sdn.flatbuffers.stream')) {
        await route.fulfill({
          contentType: 'application/vnd.sdn.flatbuffers.stream',
          body: rawFlatbufferStream(pageRefs.map((record) => record.bytes)),
        });
        return;
      }
      await route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify({
          results: pageRefs.map(({ cid }, index) => ({
            schema_name: 'PNM.fbs',
            cid,
            peer_id: 'source:celestrak',
            provider_id: 'space-data-network-02',
            source_name: 'celestrak-publication-log',
            batch_id: 'fixture-pnm-batch',
            timestamp: `2026-05-11T04:${String(offset + index).padStart(2, '0')}:25Z`,
            size_bytes: 144,
            data_base64: pageRefs[index].bytes.toString('base64'),
          })),
        }),
      });
      return;
    }
    expect(body).toMatchObject({ schema: 'OMM.fbs', limit: 10, offset: 0 });
    if (route.request().headers().accept?.includes('application/vnd.sdn.flatbuffers.stream')) {
      await route.fulfill({
        contentType: 'application/vnd.sdn.flatbuffers.stream',
        body: rawFlatbufferStream([STARLINK_6292_OMM_BYTES]),
      });
      return;
    }
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({
        results: [
          {
            schema_name: 'OMM.fbs',
            cid: 'celestrak-omm-1',
            peer_id: 'source:celestrak',
            provider_id: 'space-data-network-02',
            source_name: 'celestrak-gp',
            batch_id: 'fixture-batch',
            timestamp: '2026-05-11T04:02:25Z',
            size_bytes: 288,
          },
        ],
      }),
    });
  });
  await page.context().route('**/api/v1/data/query', async (route) => {
    if (new URL(route.request().url()).pathname !== '/api/v1/data/query') {
      await route.fallback();
      return;
    }
    const body = route.request().postDataJSON();
    expect(body.query).toBeUndefined();
    if (body.schema === 'PNM.fbs') {
      const offset = Number(body.offset ?? 0);
      const limit = Number(body.limit ?? 10);
      const pageRefs = PNM_FIXTURE_REFS.slice(offset, offset + limit);
      if (route.request().headers().accept?.includes('application/vnd.sdn.flatbuffers.stream')) {
        await route.fulfill({
          contentType: 'application/vnd.sdn.flatbuffers.stream',
          body: rawFlatbufferStream(pageRefs.map((record) => record.bytes)),
        });
        return;
      }
      await route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify({
          results: pageRefs.map(({ cid }, index) => ({
            schema_name: 'PNM.fbs',
            cid,
            peer_id: 'source:celestrak',
            provider_id: 'local',
            source_name: 'celestrak-publication-log',
            batch_id: 'fixture-pnm-batch',
            timestamp: `2026-05-11T04:${String(offset + index).padStart(2, '0')}:25Z`,
            size_bytes: 144,
            data_base64: pageRefs[index].bytes.toString('base64'),
          })),
        }),
      });
      return;
    }
    expect(body).toMatchObject({ schema: 'OMM.fbs', limit: 10, offset: 0 });
    if (route.request().headers().accept?.includes('application/vnd.sdn.flatbuffers.stream')) {
      await route.fulfill({
        contentType: 'application/vnd.sdn.flatbuffers.stream',
        body: rawFlatbufferStream([STARLINK_6292_OMM_BYTES]),
      });
      return;
    }
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({
        results: [
          {
            schema_name: 'OMM.fbs',
            cid: 'celestrak-omm-1',
            peer_id: 'source:celestrak',
            provider_id: 'local',
            source_name: 'celestrak-gp',
            batch_id: 'fixture-batch',
            timestamp: '2026-05-11T04:02:25Z',
            size_bytes: 288,
          },
        ],
      }),
    });
  });
  await page.route('**/api/v0/repo/stat', async (route) => {
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({ RepoSize: 1_200_000_000, StorageMax: 10_000_000_000 }),
    });
  });
  await page.route('**/api/v1/data/objects', async (route) => {
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({
        objects: [
          {
            id: 'omm-starlink-34967',
            label: 'STARLINK-34967',
            schema: 'OMM.fbs',
            source: 'celestrak-gp',
            state: 'stored',
          },
        ],
      }),
    });
  });
}

const STARLINK_6292_OMM_BYTES = Buffer.from('HAEAAEgAAAAkT01NAAAAADwAVAAAAAwACABQAEwAEAAAAAAAAAAAAAAARAAAADwANAAsACQAHAAUAAAAAAAAAAAAAAAAAAAABABIADwAAABQAAAAVAAAAGAAAAB4AAAAxEKtad4BV0DByqFFtsBwQGZmZmZmnGJAXf5D+u1/UUCej3xvHS04P22KKnBw9y1AUAAAAMfdAABkAAAAcAAAAAEAAABVAAAACAAAAFNETi1URVNUAAAAABQAAAAyMDI2LTA1LTExVDEwOjI2OjQxWgAAAAAFAAAARUFSVEgAAAAUAAAAMjAyNi0wNS0xMFQxMDo0NTozMVoAAAAACQAAADIwMjMtMDc4SgAAAA0AAABTVEFSTElOSy02MjkyAAAA', 'base64');
const GEO_SOVIET_TEST_OMM_BYTES = buildOmmBytes({
  objectName: 'GEO-SOVIET-TEST',
  objectId: '1979-001A',
  noradCatId: 90001,
  epoch: '2026-05-10T11:45:31Z',
  meanMotion: 0.99,
});
const PNM_BYTES = buildPnmBytes('bafy-pnm-cid', 'celestrak:gp:OMM.fbs:2026-05-11T03:00:00Z');
const PNM_FIXTURE_REFS = Array.from({ length: 12 }, (_, index) => {
  const ordinal = index + 1;
  const cid = ordinal === 1 ? 'bafy-pnm-cid' : `bafy-pnm-cid-${ordinal}`;
  return {
    cid,
    bytes: buildPnmBytes(cid, `celestrak:gp:OMM.fbs:2026-05-11T03:${String(index).padStart(2, '0')}:00Z`),
  };
});

function rawFlatbufferStream(records: Buffer[]): Buffer {
  const chunks: Buffer[] = [];
  for (const record of records) {
    const header = Buffer.alloc(4);
    header.writeUInt32BE(record.byteLength, 0);
    chunks.push(header, record);
  }
  return Buffer.concat(chunks);
}

function buildOmmBytes(fields: {
  objectName: string;
  objectId: string;
  noradCatId: number;
  epoch: string;
  meanMotion: number;
}): Buffer {
  const builder = new flatbuffers.Builder(256);
  const creationDate = builder.createString('2026-05-11T10:26:41Z');
  const originator = builder.createString('SDN-TEST');
  const objectName = builder.createString(fields.objectName);
  const objectId = builder.createString(fields.objectId);
  const centerName = builder.createString('EARTH');
  const epoch = builder.createString(fields.epoch);
  OMM.startOMM(builder);
  OMM.addCcsdsOmmVers(builder, 3);
  OMM.addCreationDate(builder, creationDate);
  OMM.addOriginator(builder, originator);
  OMM.addObjectName(builder, objectName);
  OMM.addObjectId(builder, objectId);
  OMM.addCenterName(builder, centerName);
  OMM.addEpoch(builder, epoch);
  OMM.addMeanMotion(builder, fields.meanMotion);
  OMM.addEccentricity(builder, 0.0001);
  OMM.addInclination(builder, 63.4);
  OMM.addNoradCatId(builder, fields.noradCatId);
  const omm = OMM.endOMM(builder);
  OMM.finishSizePrefixedOMMBuffer(builder, omm);
  return Buffer.from(builder.asUint8Array());
}

function buildPnmBytes(cidValue: string, fileIdValue: string): Buffer {
  const builder = new flatbuffers.Builder(256);
  const multiaddr = builder.createString('/dns4/celestrak.eth/tcp/443/wss');
  const published = builder.createString('2026-05-11T03:00:00Z');
  const cid = builder.createString(cidValue);
  const fileName = builder.createString('OMM.fbs');
  const fileId = builder.createString(fileIdValue);
  PNM.startPNM(builder);
  PNM.addMultiformatAddress(builder, multiaddr);
  PNM.addPublishTimestamp(builder, published);
  PNM.addCid(builder, cid);
  PNM.addFileName(builder, fileName);
  PNM.addFileId(builder, fileId);
  const pnm = PNM.endPNM(builder);
  PNM.finishSizePrefixedPNMBuffer(builder, pnm);
  return Buffer.from(builder.asUint8Array());
}

import { expect, test, type Page } from '@playwright/test';
import * as flatbuffers from 'flatbuffers';
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
  await expect(page.getByText('CelesTrak Provider')).toBeVisible();
  await expect(page.getByText(realPeerId)).toBeVisible();
});

test('data route renders a searchable remote data source without workbench status chrome', async ({ page }) => {
  await page.goto('/?api=http://127.0.0.1:5174&gateway=http%3A%2F%2F127.0.0.1%3A8081#/data');

  await expect(page.getByRole('heading', { name: 'Data' })).toBeVisible();
  await expect(page.getByText('SQL Workbench')).toHaveCount(0);
  await expect(page.getByText('backend ready')).toHaveCount(0);
  await expect(page.getByText(/available .* total/)).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Refresh' })).toHaveCount(0);

  const dataSourceSearch = page.getByRole('searchbox', { name: 'Data source' });
  await expect(dataSourceSearch).toBeVisible();
  await dataSourceSearch.fill('celes');
  const sourceTable = page.getByRole('table', { name: 'Data sources' });
  await expect(sourceTable).toBeVisible();
  await expect(sourceTable.getByText('CelesTrak Provider')).toBeVisible();
  await expect(sourceTable.getByText(realPeerId)).toBeVisible();
  await sourceTable.getByRole('button', { name: /CelesTrak Provider/ }).click();
  await expect(page.getByText('Loading')).toBeVisible();

  await expect(page.getByRole('button', { name: 'Storage' })).toHaveAttribute('aria-pressed', 'true');
  await expect(page.getByLabel('Local storage state')).toBeVisible();
  await page.getByRole('button', { name: 'Subscriptions' }).click();
  const schemaSync = page.getByRole('table', { name: 'Schema sync' });
  await expect(schemaSync).toBeVisible();
  await expect(schemaSync.getByRole('row').nth(1)).toContainText('PNM');
  await expect(schemaSync.getByRole('row').nth(1)).toContainText('12');
  await expect(schemaSync.getByRole('combobox', { name: 'PNM sync' })).toHaveValue('preview');
  await expect(schemaSync.getByRole('spinbutton', { name: 'PNM storage cap' })).toBeDisabled();
  await schemaSync.getByRole('combobox', { name: 'PNM sync' }).selectOption('sync');
  await expect(schemaSync.getByRole('spinbutton', { name: 'PNM storage cap' })).toBeEnabled();
  await schemaSync.getByRole('spinbutton', { name: 'PNM storage cap' }).fill('2.5');
  await schemaSync.getByRole('combobox', { name: 'PNM storage unit' }).selectOption('MB');
  await expect(schemaSync.getByRole('spinbutton', { name: 'PNM storage cap' })).toHaveValue('2.5');
  await expect(schemaSync.getByRole('row').nth(1)).toContainText('Syncing');
  await expect(schemaSync.getByRole('row').nth(1)).toContainText('Synced 12/12');
  await page.getByRole('button', { name: 'Explorer' }).click();
  await expect(page.getByRole('combobox', { name: 'Table' })).toHaveValue('PNM');
  await expect(page.getByRole('combobox', { name: 'Table' }).locator('option')).toHaveText([
    'PNM (12)',
    'OMM (2)',
    'CAT (1)',
    'EPM (1)',
  ]);
  await expect(page.getByRole('combobox', { name: 'Page size' })).toHaveValue('10');
  await expect(page.getByLabel('Remote rows 12')).toBeVisible();
  await expect(page.getByLabel('Local rows 12')).toBeVisible();
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

  const sql = page.getByRole('textbox', { name: 'SQL' });
  await expect(sql).toHaveValue('SELECT * FROM PNM LIMIT 10');
  await sql.fill('SELECT FILE_ID, CID FROM PNM LIMIT 10');
  await page.getByRole('button', { name: 'Run SQL' }).click();
  await expect(dataRows.getByRole('columnheader', { name: 'FILE_ID' })).toBeVisible();
  await expect(dataRows.getByRole('cell', { name: 'bafy-pnm-cid', exact: true })).toBeVisible();

  await page.reload();
  await page.getByRole('searchbox', { name: 'Data source' }).fill('celes');
  await page.getByRole('table', { name: 'Data sources' }).getByRole('button', { name: /CelesTrak Provider/ }).click();
  await page.getByRole('button', { name: 'Subscriptions' }).click();
  const reloadedSchemaSync = page.getByRole('table', { name: 'Schema sync' });
  await expect(reloadedSchemaSync.getByRole('combobox', { name: 'PNM sync' })).toHaveValue('sync');
  await expect(reloadedSchemaSync.getByRole('spinbutton', { name: 'PNM storage cap' })).toHaveValue('2.5');
  await expect(reloadedSchemaSync.getByRole('combobox', { name: 'PNM storage unit' })).toHaveValue('MB');
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
  await page.route('**/api/local/sdn-nodes/space-data-network-02/api/v1/data/scan', async (route) => {
    const body = route.request().postDataJSON();
    expect(body.schema).toBe('PNM.fbs');
    expect(body.include_data).toBe(false);
    const offset = Number(body.offset ?? 0);
    const limit = Number(body.limit ?? 10);
    if (offset > 0) expect(limit).toBe(25_000);
    const pageRefs = PNM_FIXTURE_REFS.slice(offset, offset + limit);
    await new Promise((resolve) => setTimeout(resolve, 100));
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({
        schema: 'PNM.fbs',
        total_count: PNM_FIXTURE_REFS.length,
        count: pageRefs.length,
        limit,
        offset,
        cursor: 'MA',
        next_cursor: offset + pageRefs.length < PNM_FIXTURE_REFS.length ? String(offset + pageRefs.length) : '',
        scan_hash: 'fixture-scan-hash',
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
  await page.route('**/api/local/sdn-nodes/space-data-network-02/api/v1/data/stream', async (route) => {
    const body = route.request().postDataJSON();
    expect(body.schema).toBe('PNM.fbs');
    expect(body.scan_hash).toBe('fixture-scan-hash');
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

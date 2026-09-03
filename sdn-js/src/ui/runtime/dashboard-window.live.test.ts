/**
 * Live window-runtime check against a running node.
 *
 * Skipped unless `SDN_LIVE_NODE` names the node (`http://127.0.0.1:7173`).
 * Uses the in-thread engine store (no Worker in Node) and the real fetch, and
 * proves the dashboard contract end to end: every standard opens from the
 * node's OWN engine DDL, a page ingests the node's raw frames, the stored
 * count matches the `$NDS` feed, and no JSON projection lane is ever called.
 */
import { afterAll, beforeAll, describe, expect, it } from 'vitest';

import { decodeDashboardStats } from '../../status/dashboard-stats';
import { createDashboardWindow, type DashboardWindowRuntime } from './dashboard-window';
import { createLocalFlatSqlStore, type LocalFlatSqlStore } from './local-flatsql';
import { joinUrl, type FetchLike } from './sdn-backend-adapter-utils';

const LIVE_NODE = (process.env.SDN_LIVE_NODE ?? '').trim().replace(/\/+$/, '');
const STANDARDS = ['OMM', 'CNP', 'TBS', 'SPW', 'EPM'] as const;
const ALLOWED_PATH_PREFIXES = ['/api/v1/standards/', '/api/v1/data/query'];
const LIVE_TIMEOUT_MS = 120_000;

describe.skipIf(!LIVE_NODE)('dashboard window runtime (live node)', () => {
  let store: LocalFlatSqlStore;
  let runtime: DashboardWindowRuntime;
  const requestedUrls: string[] = [];
  const spyFetch: FetchLike = (url, init) => {
    requestedUrls.push(url);
    return globalThis.fetch(url, init);
  };

  beforeAll(async () => {
    store = await createLocalFlatSqlStore({ schemas: [] });
    runtime = createDashboardWindow({ store, fetch: spyFetch, baseUrl: LIVE_NODE });
  }, LIVE_TIMEOUT_MS);

  afterAll(() => {
    runtime?.destroy();
    store?.destroy();
  });

  it('opens every standard from the node engine DDL with engine columns', async () => {
    for (const code of STANDARDS) {
      const standard = await runtime.ensureStandard(code);
      expect(standard.standardId, code).toBe(code);
      expect(standard.columns.length, `${code} columns`).toBeGreaterThan(0);
      expect(standard.columns, `${code} columns`).toEqual(expect.arrayContaining(['_source', '_rowid', '_offset', '_data']));
    }
  }, LIVE_TIMEOUT_MS);

  it('pages OMM and reports the node stored count from the $NDS feed', async () => {
    const statsResponse = await globalThis.fetch(joinUrl(LIVE_NODE, '/api/v1/dashboard/stats'), {
      headers: { accept: 'application/vnd.sdn.flatbuffers.stream' },
    });
    expect(statsResponse.ok).toBe(true);
    const stats = decodeDashboardStats(new Uint8Array(await statsResponse.arrayBuffer()));
    const ommStat = stats.schemas.find((entry) => entry.schema.replace(/\.fbs$/i, '').toUpperCase() === 'OMM');
    expect(ommStat, 'OMM in $NDS schemas').toBeDefined();

    const load = await runtime.loadPage({ schema: 'OMM' }, { page: 1, limit: 100 });
    expect(load).toMatchObject({ mode: 'page', partial: false, serverOrder: 'newest-first', batches: 1 });
    expect(load.windowRows).toBe(100);
    expect(load.storedRows).toBe(ommStat!.recordCount);

    const rows = await runtime.query('SELECT _source, NORAD_CAT_ID, EPOCH FROM OMM ORDER BY _rowid ASC', 'OMM', { maxLimit: 100 });
    expect(rows.records).toHaveLength(100);
    expect(rows.records.every((row) => typeof row._source === 'string' && (row._source as string).startsWith('OMM@'))).toBe(true);
  }, LIVE_TIMEOUT_MS);

  it('pages CNP, TBS, SPW and EPM through the local engine', async () => {
    for (const code of ['CNP', 'TBS', 'SPW', 'EPM'] as const) {
      const load = await runtime.loadPage({ schema: code }, { page: 1, limit: 5 });
      expect(load.windowRows, `${code} window rows`).toBeGreaterThanOrEqual(1);
      expect(load.storedRows, `${code} stored rows`).toBeGreaterThanOrEqual(load.windowRows);
      const rows = await runtime.query(`SELECT * FROM ${load.tableName} LIMIT 1`, code, { maxLimit: 1, maxBytes: 4 * 1024 * 1024 });
      expect(rows.records.length, `${code} rows`).toBeGreaterThanOrEqual(1);
      expect(rows.columns, `${code} columns`).toContain('_source');
    }
  }, LIVE_TIMEOUT_MS);

  it('never calls a JSON projection lane', () => {
    expect(requestedUrls.length).toBeGreaterThan(0);
    const disallowed = requestedUrls.filter((url) => {
      const path = new URL(url).pathname;
      return !ALLOWED_PATH_PREFIXES.some((prefix) => path.startsWith(prefix));
    });
    expect(disallowed).toEqual([]);
    expect(requestedUrls.some((url) => new URL(url).pathname === '/api/v1/data/table')).toBe(false);
  });
});

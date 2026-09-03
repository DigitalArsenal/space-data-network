import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import * as flatbuffers from 'flatbuffers';
import { describe, expect, it } from 'vitest';
import { OMM, OMMT } from 'spacedatastandards.org/lib/js/OMM/OMM.js';

import {
  DASHBOARD_WINDOW_RANGE_PROBE_FILTER,
  createDashboardWindow,
  dashboardWindowStateKey,
  isDashboardWindowSuperseded,
  parseSourceRunsHeader,
  sourceNameForState,
  type WindowStore,
} from './dashboard-window';
import { decodeWindowStatus, startDashboardDataRuntime } from './dashboard-data-runtime';
import { createLocalFlatSqlStore, type LocalFlatSqlSchema, type LocalFlatSqlStore } from './local-flatsql';
import { RAW_FLATBUFFER_STREAM_CONTENT_TYPE, type FetchLike } from './sdn-backend-adapter-utils';

const require = createRequire(import.meta.url);
const OMM_SCHEMA = readFileSync(require.resolve('spacedatastandards.org/schema/OMM/main.fbs'), 'utf8');

interface CatalogueRecord {
  index: number;
  source: string;
  noradCatId: number;
  epoch: string;
  frame: Uint8Array;
}

/** 12 distinct OMM frames, newest first, under ['celestrak-gp' x 7, 'supplemental' x 5]. */
function buildCatalogue(): CatalogueRecord[] {
  return Array.from({ length: 12 }, (_, index) => {
    const source = index < 7 ? 'celestrak-gp' : 'supplemental';
    const noradCatId = 1000 + index;
    const epoch = `2026-05-${String(28 - index).padStart(2, '0')}T10:45:31Z`;
    return { index, source, noradCatId, epoch, frame: ommFrame(noradCatId, `SAT-${index}`, epoch) };
  });
}

function ommFrame(noradCatId: number, objectName: string, epoch: string): Uint8Array {
  const builder = new flatbuffers.Builder(512);
  const record = new OMMT();
  record.OBJECT_NAME = objectName;
  record.OBJECT_ID = `2026-00${noradCatId}`;
  record.NORAD_CAT_ID = noradCatId;
  record.EPOCH = epoch;
  record.MEAN_MOTION = 14.9 + noradCatId / 10_000;
  record.ECCENTRICITY = 0.0003;
  record.INCLINATION = 53.05;
  OMM.finishSizePrefixedOMMBuffer(builder, record.pack(builder));
  return builder.asUint8Array().slice();
}

function concatFrames(frames: Uint8Array[]): Uint8Array<ArrayBuffer> {
  const out = new Uint8Array(new ArrayBuffer(frames.reduce((total, frame) => total + frame.byteLength, 0)));
  let offset = 0;
  for (const frame of frames) {
    out.set(frame, offset);
    offset += frame.byteLength;
  }
  return out;
}

function sourceRunsHeader(sources: string[]): string {
  const runs: Array<{ source: string; count: number }> = [];
  for (const source of sources) {
    const last = runs[runs.length - 1];
    if (last && last.source === source) last.count += 1;
    else runs.push({ source, count: 1 });
  }
  return runs.map((run) => `${run.source ? encodeURIComponent(run.source) : '-'}:${run.count}`).join(',');
}

interface FakeNodeOptions {
  rangeSupported?: boolean;
  status?: number;
  beforeQuery?: () => Promise<void>;
  schemas?: Record<string, string>;
}

interface RecordedRequest {
  url: string;
  method: string;
  body: Record<string, unknown> | null;
}

/** A node that only knows the DDL lane and the raw FlatBuffer lane. */
function createFakeNode(catalogue: CatalogueRecord[], options: FakeNodeOptions = {}) {
  const requests: RecordedRequest[] = [];
  const schemas: Record<string, string> = { OMM: OMM_SCHEMA, ...(options.schemas ?? {}) };
  const fetch: FetchLike = async (url, init) => {
    const path = new URL(url, 'http://node.test').pathname;
    const method = (init?.method ?? 'GET').toUpperCase();
    const body = typeof init?.body === 'string' ? JSON.parse(init.body) as Record<string, unknown> : null;
    requests.push({ url, method, body });

    const standardMatch = /^\/api\/v1\/standards\/([A-Z0-9]+)\.fbs$/.exec(path);
    if (standardMatch) {
      const code = standardMatch[1];
      const text = schemas[code];
      if (!text) return new Response('unknown standard', { status: 404 });
      return new Response(text, {
        status: 200,
        headers: {
          'content-type': 'text/plain; charset=utf-8',
          'x-sdn-engine-table': code,
          'x-sdn-file-id': `$${code}`,
        },
      });
    }

    if (path === '/api/v1/data/query' && method === 'POST' && body) {
      if (options.beforeQuery) await options.beforeQuery();
      if (options.status && options.status !== 200) return new Response('unavailable', { status: options.status });
      const headers = new Headers(init?.headers as Record<string, string>);
      expect(headers.get('accept')).toBe(RAW_FLATBUFFER_STREAM_CONTENT_TYPE);
      expect(body.include_data).toBe(false);
      expect(body.schema).toBe('OMM.fbs');
      let rows = catalogue;
      if (typeof body.source_name === 'string') rows = rows.filter((row) => row.source === body.source_name);
      if (typeof body.sync_filter === 'string') {
        rows = options.rangeSupported ? [...rows].sort((left, right) => left.epoch.localeCompare(right.epoch)) : [];
      }
      const offset = Number(body.offset ?? 0);
      const limit = Number(body.limit ?? rows.length);
      const page = rows.slice(offset, offset + limit);
      return new Response(concatFrames(page.map((row) => row.frame)), {
        status: 200,
        headers: {
          'content-type': RAW_FLATBUFFER_STREAM_CONTENT_TYPE,
          'x-sdn-schema': 'OMM.fbs',
          'x-sdn-stream-format': 'flatsql-size-prefixed-le-u32',
          'x-sdn-record-count': String(page.length),
          'x-sdn-total-count': String(rows.length),
          'x-sdn-offset': String(offset),
          'x-sdn-limit': String(limit),
          'x-sdn-source-runs': sourceRunsHeader(page.map((row) => row.source)),
        },
      });
    }

    return new Response('not found', { status: 404 });
  };
  const queryRequests = () => requests.filter((request) => request.url.endsWith('/api/v1/data/query'));
  return { fetch, requests, queryRequests };
}

async function createOmmStore(): Promise<LocalFlatSqlStore> {
  return createLocalFlatSqlStore({
    schemas: [{ standardId: 'OMM', tableName: 'OMM', fileId: '$OMM', schema: OMM_SCHEMA }],
  });
}

/** The same engine store seen through the window contract WITHOUT on-demand registration. */
function withoutAddStandard(store: LocalFlatSqlStore): WindowStore {
  return {
    ingestFlatBufferStream: (standardId, bytes, options) => store.ingestFlatBufferStream(standardId, bytes, options),
    clearStandard: (standardId, options) => store.clearStandard(standardId, options),
    query: (sql, standardId, options) => store.query(sql, standardId, options),
    getStats: (options) => store.getStats(options),
    listSources: (standardId) => store.listSources?.(standardId) ?? [],
  };
}

/**
 * A store that registers standards on demand — the shape a worker store
 * created with `schemas: []` exposes — built from real in-thread engines.
 */
function createRegistrableStore(): WindowStore & { added: LocalFlatSqlSchema[]; destroy(): void } {
  const engines = new Map<string, LocalFlatSqlStore>();
  const engineFor = (standardId?: string): LocalFlatSqlStore => {
    const engine = standardId ? engines.get(standardId.toUpperCase()) : engines.values().next().value;
    if (!engine) throw new Error(`No FlatSQL schema registered for ${standardId}`);
    return engine;
  };
  const added: LocalFlatSqlSchema[] = [];
  return {
    added,
    async addStandard(schema) {
      added.push(schema);
      engines.set(schema.standardId.toUpperCase(), await createLocalFlatSqlStore({ schemas: [schema] }));
    },
    ingestFlatBufferStream: (standardId, bytes, options) => engineFor(standardId).ingestFlatBufferStream(standardId, bytes, options),
    clearStandard: (standardId, options) => engineFor(standardId).clearStandard(standardId, options),
    query: (sql, standardId, options) => engineFor(standardId).query(sql, standardId, options),
    getStats: async (options) => {
      const stats = [];
      for (const engine of engines.values()) stats.push(...await engine.getStats(options));
      return stats;
    },
    listSources: (standardId) => engineFor(standardId).listSources?.(standardId) ?? [],
    destroy: () => {
      for (const engine of engines.values()) engine.destroy();
      engines.clear();
    },
  };
}

function deferred(): { promise: Promise<void>; resolve: () => void } {
  let resolve!: () => void;
  const promise = new Promise<void>((done) => { resolve = done; });
  return { promise, resolve };
}

async function until(predicate: () => boolean, attempts = 200): Promise<void> {
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    if (predicate()) return;
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
  throw new Error('condition not met');
}

describe('dashboard window state helpers', () => {
  it('keys a state by standard, lane and range only', () => {
    expect(dashboardWindowStateKey({ schema: 'OMM' })).toBe('OMM||||');
    expect(dashboardWindowStateKey({ schema: 'omm.fbs', source: 'OMM@celestrak-gp' })).toBe('OMM|OMM@celestrak-gp|||');
    expect(dashboardWindowStateKey({
      schema: 'OMM',
      source: '',
      range: { column: 'epoch', from: '2026-05-01', to: '2026-05-31' },
    })).toBe('OMM||EPOCH|2026-05-01|2026-05-31');
    expect(dashboardWindowStateKey({ schema: 'OMM', range: { column: 'EPOCH', from: '', to: '' } })).toBe('OMM||||');
  });

  it('resolves the source name the node is asked for', () => {
    expect(sourceNameForState({ schema: 'OMM', source: 'OMM@celestrak-gp' })).toBe('celestrak-gp');
    expect(sourceNameForState({ schema: 'OMM', source: 'celestrak-gp' })).toBe('celestrak-gp');
    expect(sourceNameForState({ schema: 'OMM', source: '' })).toBe('');
    expect(sourceNameForState({ schema: 'OMM' })).toBe('');
  });

  it('parses X-SDN-Source-Runs in frame order', () => {
    expect(parseSourceRunsHeader('celestrak-gp:2,supplemental:3')).toEqual([
      { source: 'celestrak-gp', count: 2 },
      { source: 'supplemental', count: 3 },
    ]);
    expect(parseSourceRunsHeader('-:4,space%20track:1')).toEqual([
      { source: '', count: 4 },
      { source: 'space track', count: 1 },
    ]);
    expect(parseSourceRunsHeader('')).toBeNull();
    expect(parseSourceRunsHeader('broken')).toBeNull();
  });
});

describe('dashboard window runtime', () => {
  it('registers a standard from the engine DDL lane and exposes the engine columns', async () => {
    const catalogue = buildCatalogue();
    const node = createFakeNode(catalogue);
    const runtime = createDashboardWindow({ store: await createOmmStore(), fetch: node.fetch, baseUrl: 'http://node.test' });

    const standard = await runtime.ensureStandard('OMM');
    expect(standard).toMatchObject({ standardId: 'OMM', tableName: 'OMM', fileId: '$OMM' });
    expect(standard.columns).toEqual(expect.arrayContaining(['NORAD_CAT_ID', 'EPOCH', '_source', '_rowid', '_offset', '_data']));
    expect(node.requests.map((request) => request.url)).toEqual(['http://node.test/api/v1/standards/OMM.fbs']);

    await runtime.ensureStandard('omm.fbs');
    expect(node.requests).toHaveLength(1);
  });

  it('refuses a standard the engine does not hold when the store cannot add one', async () => {
    const node = createFakeNode(buildCatalogue(), { schemas: { CNP: 'table CNP { ID: string; }\nroot_type CNP;\nfile_identifier "$CNP";' } });
    const runtime = createDashboardWindow({ store: withoutAddStandard(await createOmmStore()), fetch: node.fetch, baseUrl: 'http://node.test' });

    await expect(runtime.ensureStandard('CNP')).rejects.toThrow('standard CNP is not registered in the local engine');
    await expect(runtime.ensureStandard('XYZ')).rejects.toThrow('/api/v1/standards/XYZ.fbs returned HTTP 404');
    // A standard the engine already holds needs no registration.
    expect((await runtime.ensureStandard('OMM')).columns).toContain('_source');
  });

  it('opens standards on demand in an engine store created with no schemas', async () => {
    const catalogue = buildCatalogue();
    const node = createFakeNode(catalogue);
    const store = await createLocalFlatSqlStore({ schemas: [] });
    const runtime = createDashboardWindow({ store, fetch: node.fetch, baseUrl: 'http://node.test' });

    const standard = await runtime.ensureStandard('OMM');
    expect(standard.columns).toEqual(expect.arrayContaining(['NORAD_CAT_ID', 'EPOCH', '_source', '_rowid', '_offset', '_data']));
    const load = await runtime.loadPage({ schema: 'OMM' }, { page: 1, limit: 3 });
    expect(load).toMatchObject({ windowRows: 3, storedRows: 12 });
    const rows = await runtime.query('SELECT NORAD_CAT_ID FROM OMM ORDER BY _rowid ASC', 'OMM');
    expect(rows.records.map((row) => row.NORAD_CAT_ID)).toEqual([1000, 1001, 1002]);
    store.destroy();
  });

  it('adds a standard through the store when the engine DDL lane serves it', async () => {
    const catalogue = buildCatalogue();
    const node = createFakeNode(catalogue);
    const store = createRegistrableStore();
    const runtime = createDashboardWindow({ store, fetch: node.fetch, baseUrl: 'http://node.test' });

    const standard = await runtime.ensureStandard('OMM');
    expect(store.added).toEqual([{ standardId: 'OMM', tableName: 'OMM', fileId: '$OMM', schema: OMM_SCHEMA }]);
    expect(standard.columns).toContain('_source');

    const load = await runtime.loadPage({ schema: 'OMM' }, { page: 1, limit: 4 });
    expect(load).toMatchObject({ mode: 'page', windowRows: 4, storedRows: 12 });
    store.destroy();
  });

  it('loads exactly one server page per state and reproduces server order + source partitions', async () => {
    const catalogue = buildCatalogue();
    const node = createFakeNode(catalogue);
    const store = await createOmmStore();
    const runtime = createDashboardWindow({ store, fetch: node.fetch, baseUrl: 'http://node.test' });

    const load = await runtime.loadPage({ schema: 'OMM' }, { page: 2, limit: 5 });
    expect(load).toMatchObject({
      key: 'OMM||||',
      standardId: 'OMM',
      tableName: 'OMM',
      mode: 'page',
      source: '',
      windowRows: 5,
      storedRows: 12,
      partial: false,
      serverOrder: 'newest-first',
      serverRange: false,
      offset: 5,
      page: 2,
      pageSize: 5,
      batches: 1,
    });
    expect(load.bytes).toBe(concatFrames(catalogue.slice(5, 10).map((row) => row.frame)).byteLength);

    const queries = node.queryRequests();
    expect(queries).toHaveLength(1);
    expect(queries[0].body).toEqual({ schema: 'OMM.fbs', include_data: false, limit: 5, offset: 5 });

    const rows = await runtime.query('SELECT _source, NORAD_CAT_ID FROM OMM ORDER BY _rowid ASC', 'OMM');
    expect(rows.records).toEqual(catalogue.slice(5, 10).map((row) => ({ _source: `OMM@${row.source}`, NORAD_CAT_ID: row.noradCatId })));
    expect(rows.records.map((row) => row._source)).toEqual([
      'OMM@celestrak-gp', 'OMM@celestrak-gp', 'OMM@supplemental', 'OMM@supplemental', 'OMM@supplemental',
    ]);

    const grouped = await runtime.query('SELECT _source, COUNT(*) AS n FROM OMM GROUP BY _source ORDER BY _source', 'OMM');
    expect(grouped.records).toEqual([{ _source: 'OMM@celestrak-gp', n: 2 }, { _source: 'OMM@supplemental', n: 3 }]);
    expect(await runtime.listSources('OMM')).toEqual(['celestrak-gp', 'supplemental']);

    const epochs = await runtime.query('SELECT EPOCH FROM OMM ORDER BY _rowid ASC', 'OMM');
    expect(epochs.records.map((row) => row.EPOCH)).toEqual(catalogue.slice(5, 10).map((row) => row.epoch));

    const status = decodeWindowStatus(runtime.status('OMM'));
    expect(status).toMatchObject({
      status: 'synced',
      localRows: 5,
      syncedRows: 5,
      totalRows: 12,
      missingRows: 7,
      cursor: 'OMM||||',
      queryProfile: 'page',
      syncProtocol: 'http:/api/v1/data/query',
    });
  });

  it('clears the previous page before ingesting the next one', async () => {
    const catalogue = buildCatalogue();
    const node = createFakeNode(catalogue);
    const store = await createOmmStore();
    const runtime = createDashboardWindow({ store, fetch: node.fetch, baseUrl: 'http://node.test' });

    await runtime.loadPage({ schema: 'OMM' }, { page: 1, limit: 5 });
    const second = await runtime.loadPage({ schema: 'OMM' }, { page: 3, limit: 5 });
    expect(second.windowRows).toBe(2);
    expect((await store.getStats())[0]?.recordCount).toBe(2);

    const third = await runtime.loadPage({ schema: 'OMM' }, { page: 2, limit: 5 });
    expect(third.windowRows).toBe(5);
    expect((await store.getStats())[0]?.recordCount).toBe(5);
    const ids = await runtime.query('SELECT NORAD_CAT_ID FROM OMM ORDER BY _rowid ASC', 'OMM');
    expect(ids.records.map((row) => row.NORAD_CAT_ID)).toEqual([1005, 1006, 1007, 1008, 1009]);

    await runtime.clear('OMM');
    expect((await store.getStats())[0]?.recordCount).toBe(0);
    expect(runtime.currentLoad('OMM')).toBeNull();
    expect(decodeWindowStatus(runtime.status('OMM'))).toMatchObject({ status: 'idle', localRows: 0, totalRows: 0 });
  });

  it('asks the node for one source lane only', async () => {
    const catalogue = buildCatalogue();
    const node = createFakeNode(catalogue);
    const runtime = createDashboardWindow({ store: await createOmmStore(), fetch: node.fetch, baseUrl: 'http://node.test' });

    const load = await runtime.loadPage({ schema: 'OMM', source: 'OMM@supplemental' }, { page: 1, limit: 50 });
    expect(load).toMatchObject({ key: 'OMM|OMM@supplemental|||', source: 'supplemental', windowRows: 5, storedRows: 5 });
    expect(node.queryRequests()[0].body).toMatchObject({ source_name: 'supplemental', limit: 50, offset: 0 });
    const rows = await runtime.query('SELECT DISTINCT _source FROM OMM', 'OMM');
    expect(rows.records).toEqual([{ _source: 'OMM@supplemental' }]);
  });

  it('loads a capped window in batches and reports the gap as a $DSS frame', async () => {
    const catalogue = buildCatalogue();
    const node = createFakeNode(catalogue);
    const store = await createOmmStore();
    const runtime = createDashboardWindow({ store, fetch: node.fetch, baseUrl: 'http://node.test' });

    const load = await runtime.loadWindow({ schema: 'OMM' }, { maxRows: 8, batchRows: 5 });
    expect(load).toMatchObject({
      mode: 'window',
      windowRows: 8,
      storedRows: 12,
      partial: true,
      serverOrder: 'newest-first',
      serverRange: false,
      syncFilter: '',
      batches: 2,
    });
    const queries = node.queryRequests();
    expect(queries).toHaveLength(2);
    expect(queries.map((request) => request.body)).toEqual([
      { schema: 'OMM.fbs', include_data: false, limit: 5, offset: 0 },
      { schema: 'OMM.fbs', include_data: false, limit: 3, offset: 5 },
    ]);

    const ids = await runtime.query('SELECT NORAD_CAT_ID FROM OMM ORDER BY _rowid ASC', 'OMM');
    expect(ids.records.map((row) => row.NORAD_CAT_ID)).toEqual([1000, 1001, 1002, 1003, 1004, 1005, 1006, 1007]);

    const status = decodeWindowStatus(runtime.status('OMM'));
    expect(status).toMatchObject({ status: 'capped', localRows: 8, totalRows: 12, missingRows: 4, queryProfile: 'window' });
    expect(status.cachedBytes).toBe(load.bytes);

    const full = await runtime.loadWindow({ schema: 'OMM' }, { maxRows: 100, batchRows: 5 });
    expect(full).toMatchObject({ windowRows: 12, storedRows: 12, partial: false, batches: 3 });
    expect(decodeWindowStatus(runtime.status('OMM'))).toMatchObject({ status: 'synced', missingRows: 0 });
  });

  it('sends no sync_filter when the probe shows the node has no epochs for the standard', async () => {
    const catalogue = buildCatalogue();
    const node = createFakeNode(catalogue, { rangeSupported: false });
    const runtime = createDashboardWindow({ store: await createOmmStore(), fetch: node.fetch, baseUrl: 'http://node.test' });
    const state = { schema: 'OMM', range: { column: 'EPOCH', from: '2026-05-20T00:00:00Z', to: '2026-05-31T00:00:00Z' } };

    const load = await runtime.loadWindow(state, { batchRows: 5 });
    expect(load).toMatchObject({
      key: 'OMM||EPOCH|2026-05-20T00:00:00Z|2026-05-31T00:00:00Z',
      serverRange: false,
      serverOrder: 'newest-first',
      syncFilter: '',
      rangeText: 'EPOCH >= 2026-05-20T00:00:00Z AND EPOCH <= 2026-05-31T00:00:00Z',
      windowRows: 12,
      storedRows: 12,
    });
    const queries = node.queryRequests();
    expect(queries[0].body).toMatchObject({ limit: 1, offset: 0, sync_filter: DASHBOARD_WINDOW_RANGE_PROBE_FILTER });
    expect(queries.slice(1).every((request) => !('sync_filter' in (request.body ?? {})))).toBe(true);
    expect(queries).toHaveLength(1 + 3);

    await runtime.loadWindow(state, { batchRows: 5 });
    expect(node.queryRequests().filter((request) => request.body?.sync_filter === DASHBOARD_WINDOW_RANGE_PROBE_FILTER)).toHaveLength(1);
    expect(decodeWindowStatus(runtime.status('OMM')).syncFilter).toBe('EPOCH >= 2026-05-20T00:00:00Z AND EPOCH <= 2026-05-31T00:00:00Z');
  });

  it('sends the epoch range server-side once the probe confirms an epoch index', async () => {
    const catalogue = buildCatalogue();
    const node = createFakeNode(catalogue, { rangeSupported: true });
    const runtime = createDashboardWindow({ store: await createOmmStore(), fetch: node.fetch, baseUrl: 'http://node.test' });

    const load = await runtime.loadWindow({
      schema: 'OMM',
      range: { column: 'EPOCH', from: '2026-05-20T00:00:00Z', to: '2026-05-31T00:00:00Z' },
    }, { batchRows: 5 });
    expect(load).toMatchObject({
      serverRange: true,
      serverOrder: 'epoch-asc',
      syncFilter: 'EPOCH >= 2026-05-20T00:00:00Z AND EPOCH <= 2026-05-31T00:00:00Z',
    });
    const batches = node.queryRequests().slice(1);
    expect(batches.every((request) => request.body?.sync_filter === load.syncFilter)).toBe(true);
    const epochs = await runtime.query('SELECT EPOCH FROM OMM ORDER BY _rowid ASC', 'OMM', { maxLimit: 100 });
    expect(epochs.records.map((row) => row.EPOCH)).toEqual([...catalogue].reverse().map((row) => row.epoch));

    const other = await runtime.loadWindow({ schema: 'OMM', range: { column: 'CREATION_DATE', from: '2026-01-01', to: '' } }, { batchRows: 5 });
    expect(other).toMatchObject({ serverRange: false, syncFilter: '', rangeText: 'CREATION_DATE >= 2026-01-01' });
  });

  it('stops a superseded load before its ingest', async () => {
    const catalogue = buildCatalogue();
    const gate = deferred();
    let gated = true;
    const node = createFakeNode(catalogue, { beforeQuery: () => (gated ? gate.promise : Promise.resolve()) });
    const store = await createOmmStore();
    const runtime = createDashboardWindow({ store, fetch: node.fetch, baseUrl: 'http://node.test' });

    const first = runtime.loadPage({ schema: 'OMM' }, { page: 1, limit: 5 });
    first.catch(() => undefined);
    await until(() => node.queryRequests().length === 1);
    const second = runtime.loadPage({ schema: 'OMM', source: 'OMM@supplemental' }, { page: 1, limit: 5 });
    gated = false;
    gate.resolve();

    await expect(first).rejects.toSatisfy(isDashboardWindowSuperseded);
    const load = await second;
    expect(load).toMatchObject({ key: 'OMM|OMM@supplemental|||', windowRows: 5, storedRows: 5 });
    expect((await store.getStats())[0]?.recordCount).toBe(5);
    const rows = await runtime.query('SELECT DISTINCT _source FROM OMM', 'OMM');
    expect(rows.records).toEqual([{ _source: 'OMM@supplemental' }]);
  });

  it('propagates the HTTP status of a failed raw-lane request', async () => {
    const node = createFakeNode(buildCatalogue(), { status: 503 });
    const runtime = createDashboardWindow({ store: await createOmmStore(), fetch: node.fetch, baseUrl: 'http://node.test' });

    await expect(runtime.loadPage({ schema: 'OMM' }, { page: 1, limit: 5 })).rejects.toThrow('/api/v1/data/query returned HTTP 503');
    expect(runtime.currentLoad('OMM')).toBeNull();
  });
});

describe('startDashboardDataRuntime', () => {
  it('publishes SDN_DATA_WINDOW and drives the window through the store it booted', async () => {
    const catalogue = buildCatalogue();
    const node = createFakeNode(catalogue);
    const store = createRegistrableStore();
    const handle = startDashboardDataRuntime({
      baseUrl: 'http://node.test',
      wasmPath: 'http://node.test/sdn-js',
      fetch: node.fetch,
      createStore: async (options) => {
        expect(options.schemas).toEqual([]);
        expect(options.engine?.wasmPath).toBe('http://node.test/sdn-js');
        expect(typeof options.engine?.computeSHA384).toBe('function');
        return store;
      },
    });
    expect(globalThis.SDN_DATA_WINDOW).toBe(handle);
    expect(decodeWindowStatus(handle.status('OMM'))).toMatchObject({ status: 'idle' });
    await handle.ready;

    const load = await handle.loadPage({ schema: 'OMM' }, { page: 1, limit: 3 });
    expect(load).toMatchObject({ windowRows: 3, storedRows: 12 });
    expect(handle.currentLoad('OMM')).toMatchObject({ key: 'OMM||||' });
    expect(decodeWindowStatus(handle.status('OMM'))).toMatchObject({ status: 'synced', localRows: 3, totalRows: 12, missingRows: 9 });
    expect(handle.stateKey({ schema: 'OMM', source: 'OMM@celestrak-gp' })).toBe('OMM|OMM@celestrak-gp|||');

    handle.destroy();
    expect(globalThis.SDN_DATA_WINDOW).toBeUndefined();
  });
});

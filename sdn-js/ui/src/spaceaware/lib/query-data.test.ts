import { describe, expect, it } from 'vitest';
import { SdnApiError, type RequestOptions } from '../../lib/auth/sdn-api-client';
import {
  QUERY_DEFAULT_MODE,
  QUERY_FORBIDDEN_MESSAGE,
  QUERY_G5_CAPTION,
  QUERY_LOADING_LABEL,
  QUERY_METADATA_CAPTION,
  QUERY_OUTPUT_MODES,
  QUERY_ROW_LIMIT,
  QUERY_SIGN_IN_MESSAGE,
  buildDecodedQuerySql,
  buildQueryCsvOutput,
  buildQueryJsonOutput,
  buildQueryTableColumns,
  fetchDecodedQueryRows,
  fetchRecordMetadataRows,
  loadQueryTabData,
  metadataQueryRequestBody,
  metadataQuerySchemaName,
  parseDecodedQueryRows,
  parseMetadataQueryResponse,
  queryEmptyStateLabel,
  queryEngineCaption,
  queryOutputModeStyle,
  queryTableCellText,
  resolveQueryEngine,
  type QueryApiClient,
} from './query-data';

// ---------------------------------------------------------------------------
// Output mode toggle
// ---------------------------------------------------------------------------

describe('QUERY_OUTPUT_MODES / queryOutputModeStyle', () => {
  it('has exactly TABLE, JSON, CSV in order', () => {
    expect(QUERY_OUTPUT_MODES.map((m) => m.label)).toEqual(['TABLE', 'JSON', 'CSV']);
  });

  it('defaults to JSON, matching the mock', () => {
    expect(QUERY_DEFAULT_MODE).toBe('json');
  });

  it('styles the active mode distinctly from inactive modes', () => {
    const active = queryOutputModeStyle('json', 'json');
    const inactive = queryOutputModeStyle('table', 'json');
    expect(active.color).not.toBe(inactive.color);
    expect(active.background).not.toBe(inactive.background);
    expect(active.border).not.toBe(inactive.border);
  });
});

// ---------------------------------------------------------------------------
// Schema name / request body contract
// ---------------------------------------------------------------------------

describe('metadataQuerySchemaName', () => {
  it('appends .fbs (server does a literal match — "EPM" alone returns count:0)', () => {
    expect(metadataQuerySchemaName('EPM')).toBe('EPM.fbs');
  });

  it('uppercases and trims the code', () => {
    expect(metadataQuerySchemaName(' cat ')).toBe('CAT.fbs');
  });
});

describe('metadataQueryRequestBody', () => {
  it('builds the exact {schema, limit, offset} contract', () => {
    expect(metadataQueryRequestBody('EPM', 50, 0)).toEqual({ schema: 'EPM.fbs', limit: 50, offset: 0 });
  });

  it('defaults limit to QUERY_ROW_LIMIT and offset to 0', () => {
    expect(metadataQueryRequestBody('CAT')).toEqual({ schema: 'CAT.fbs', limit: QUERY_ROW_LIMIT, offset: 0 });
  });
});

// ---------------------------------------------------------------------------
// parseMetadataQueryResponse
// ---------------------------------------------------------------------------

describe('parseMetadataQueryResponse', () => {
  it('parses the real {schema,count,results} envelope, keys untouched', () => {
    const payload = {
      schema: 'EPM.fbs',
      count: 2,
      results: [
        { schema_name: 'EPM.fbs', cid: 'bafy1', peer_id: '12D3Koo1', timestamp: '2026-01-01T00:00:00Z', size_bytes: 128, flatbuffer_uri: '/api/v1/data/records/EPM.fbs/bafy1' },
        { schema_name: 'EPM.fbs', cid: 'bafy2', peer_id: '12D3Koo2', timestamp: '2026-01-02T00:00:00Z', size_bytes: 256, flatbuffer_uri: '/api/v1/data/records/EPM.fbs/bafy2', batch_id: 'batch-9', provider_id: 'celestrak' },
      ],
    };
    const result = parseMetadataQueryResponse(payload);
    expect(result.schema).toBe('EPM.fbs');
    expect(result.count).toBe(2);
    expect(result.rows).toHaveLength(2);
    // exact key passthrough — no camelCase renaming
    expect(result.rows[1]).toEqual(payload.results[1]);
    expect(Object.keys(result.rows[1] as object)).toContain('batch_id');
    expect(Object.keys(result.rows[1] as object)).toContain('provider_id');
  });

  it('honestly renders count:0 for an empty store', () => {
    const result = parseMetadataQueryResponse({ schema: 'PRR.fbs', count: 0, results: [] });
    expect(result.count).toBe(0);
    expect(result.rows).toEqual([]);
  });

  it('falls back count to rows.length when the count field is missing', () => {
    const result = parseMetadataQueryResponse({ results: [{ a: 1 }, { a: 2 }] });
    expect(result.count).toBe(2);
  });

  it('degrades to an empty result for a malformed payload', () => {
    expect(parseMetadataQueryResponse(null)).toEqual({ schema: null, count: 0, rows: [] });
    expect(parseMetadataQueryResponse([1, 2, 3])).toEqual({ schema: null, count: 0, rows: [] });
  });

  it('drops non-object entries from results', () => {
    const result = parseMetadataQueryResponse({ results: [{ a: 1 }, 'not an object', null, 42] });
    expect(result.rows).toEqual([{ a: 1 }]);
  });
});

// ---------------------------------------------------------------------------
// SCHEMA-EXACT KEY RULE — the fixture the loop task spec explicitly asks for
// ---------------------------------------------------------------------------

describe('schema-exact key passthrough (NORAD_CAT_ID / OBJECT_NAME fixture)', () => {
  const decodedFixture = [
    { NORAD_CAT_ID: 25544, OBJECT_NAME: 'ISS (ZARYA)', OBJECT_TYPE: 'PAYLOAD' },
    { NORAD_CAT_ID: 43013, OBJECT_NAME: 'CALSPHERE 1', OBJECT_TYPE: 'DEBRIS' },
  ];

  it('TABLE columns carry decoded-style keys verbatim', () => {
    expect(buildQueryTableColumns(decodedFixture)).toEqual(['NORAD_CAT_ID', 'OBJECT_NAME', 'OBJECT_TYPE']);
  });

  it('CSV header carries decoded-style keys verbatim', () => {
    const csv = buildQueryCsvOutput(decodedFixture);
    const [header] = csv.split('\n');
    expect(header).toBe('NORAD_CAT_ID,OBJECT_NAME,OBJECT_TYPE');
  });

  it('JSON output carries decoded-style keys verbatim', () => {
    const json = buildQueryJsonOutput(decodedFixture);
    expect(JSON.parse(json)).toEqual(decodedFixture);
    expect(json).toContain('"NORAD_CAT_ID"');
    expect(json).toContain('"OBJECT_NAME"');
  });

  it('also passes real metadata keys (batch_id, cid) through unmodified', () => {
    const metadataFixture = [{ batch_id: 'b-1', cid: 'bafy1', peer_id: '12D3Koo1' }];
    expect(buildQueryTableColumns(metadataFixture)).toEqual(['batch_id', 'cid', 'peer_id']);
    expect(buildQueryCsvOutput(metadataFixture).split('\n')[0]).toBe('batch_id,cid,peer_id');
  });
});

// ---------------------------------------------------------------------------
// buildQueryTableColumns / queryTableCellText
// ---------------------------------------------------------------------------

describe('buildQueryTableColumns', () => {
  it('unions keys across rows in first-seen order (optional metadata fields)', () => {
    const rows = [{ a: 1, b: 2 }, { a: 1, c: 3 }];
    expect(buildQueryTableColumns(rows)).toEqual(['a', 'b', 'c']);
  });

  it('returns [] for no rows', () => {
    expect(buildQueryTableColumns([])).toEqual([]);
  });
});

describe('queryTableCellText', () => {
  it('renders scalars as-is', () => {
    expect(queryTableCellText('hello')).toBe('hello');
    expect(queryTableCellText(42)).toBe('42');
    expect(queryTableCellText(true)).toBe('true');
  });

  it('renders null/undefined as an empty string', () => {
    expect(queryTableCellText(null)).toBe('');
    expect(queryTableCellText(undefined)).toBe('');
  });

  it('renders nested objects/arrays as compact JSON', () => {
    expect(queryTableCellText({ x: 1 })).toBe('{"x":1}');
    expect(queryTableCellText([1, 2])).toBe('[1,2]');
  });
});

// ---------------------------------------------------------------------------
// buildQueryCsvOutput
// ---------------------------------------------------------------------------

describe('buildQueryCsvOutput', () => {
  it('returns "" for zero rows', () => {
    expect(buildQueryCsvOutput([])).toBe('');
  });

  it('quotes fields containing commas/quotes/newlines', () => {
    const csv = buildQueryCsvOutput([{ name: 'a, "b"\nc' }]);
    expect(csv).toBe('name\n"a, ""b""\nc"');
  });
});

// ---------------------------------------------------------------------------
// buildDecodedQuerySql / parseDecodedQueryRows
// ---------------------------------------------------------------------------

describe('buildDecodedQuerySql', () => {
  it('builds a SELECT * ... LIMIT ... OFFSET ... statement against the quoted, uppercased code', () => {
    expect(buildDecodedQuerySql('epm', 50, 0)).toBe('SELECT * FROM "EPM" LIMIT 50 OFFSET 0');
  });

  it('escapes embedded double quotes defensively', () => {
    expect(buildDecodedQuerySql('a"b', 10, 5)).toBe('SELECT * FROM "A""B" LIMIT 10 OFFSET 5');
  });
});

describe('parseDecodedQueryRows', () => {
  it('accepts a bare array', () => {
    expect(parseDecodedQueryRows([{ a: 1 }])).toEqual([{ a: 1 }]);
  });

  it('accepts {rows:[...]}, {records:[...]}, {results:[...]}', () => {
    expect(parseDecodedQueryRows({ rows: [{ a: 1 }] })).toEqual([{ a: 1 }]);
    expect(parseDecodedQueryRows({ records: [{ a: 1 }] })).toEqual([{ a: 1 }]);
    expect(parseDecodedQueryRows({ results: [{ a: 1 }] })).toEqual([{ a: 1 }]);
  });

  it('degrades to [] for an unrecognized shape', () => {
    expect(parseDecodedQueryRows({ foo: 'bar' })).toEqual([]);
    expect(parseDecodedQueryRows(null)).toEqual([]);
    expect(parseDecodedQueryRows('nope')).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// resolveQueryEngine — feature detection (404 = confirmed gap on this build)
// ---------------------------------------------------------------------------

function fakeClient(fn: (path: string, opts?: RequestOptions) => unknown): QueryApiClient {
  return {
    requestJson: async <T,>(path: string, opts?: RequestOptions) => {
      const value = fn(path, opts);
      if (value instanceof Error) throw value;
      return { status: 200, data: value as T, etag: null, notModified: false };
    },
  } as unknown as QueryApiClient;
}

describe('resolveQueryEngine', () => {
  it('resolves "unavailable" on a 404 (the real, current state of this build)', async () => {
    const client = fakeClient(() => new SdnApiError(404, { code: 'not_found', message: 'not found' }, '/query'));
    expect(await resolveQueryEngine(client, 'SELECT 1')).toBe('unavailable');
  });

  it('resolves "available" on any 2xx response', async () => {
    const client = fakeClient(() => ({ rows: [] }));
    expect(await resolveQueryEngine(client, 'SELECT 1')).toBe('available');
  });

  it('resolves "unavailable" (not stuck) on other errors too — a flaky G.5 fails open', async () => {
    const client = fakeClient(() => new TypeError('network down'));
    expect(await resolveQueryEngine(client, 'SELECT 1')).toBe('unavailable');
  });

  it('sends the {sql} call shape', async () => {
    let capturedBody: unknown;
    const client: QueryApiClient = {
      requestJson: async <T,>(_path: string, opts?: RequestOptions) => {
        capturedBody = opts?.body;
        return { status: 200, data: {} as T, etag: null, notModified: false };
      },
    };
    await resolveQueryEngine(client, 'SELECT * FROM "EPM" LIMIT 50 OFFSET 0');
    expect(capturedBody).toEqual({ sql: 'SELECT * FROM "EPM" LIMIT 50 OFFSET 0' });
  });
});

// ---------------------------------------------------------------------------
// fetchRecordMetadataRows
// ---------------------------------------------------------------------------

describe('fetchRecordMetadataRows', () => {
  it('parses a real 200 response', async () => {
    const client = fakeClient(() => ({ schema: 'EPM.fbs', count: 1, results: [{ cid: 'bafy1' }] }));
    const result = await fetchRecordMetadataRows(client, 'EPM');
    expect(result).toEqual({ engine: 'metadata', rows: [{ cid: 'bafy1' }], schema: 'EPM.fbs', count: 1, errorKind: null, errorMessage: null });
  });

  it('sends the exact {schema,limit,offset} body with the .fbs suffix', async () => {
    let capturedBody: unknown;
    const client: QueryApiClient = {
      requestJson: async <T,>(_path: string, opts?: RequestOptions) => {
        capturedBody = opts?.body;
        return { status: 200, data: { results: [] } as T, etag: null, notModified: false };
      },
    };
    await fetchRecordMetadataRows(client, 'EPM', 25, 10);
    expect(capturedBody).toEqual({ schema: 'EPM.fbs', limit: 25, offset: 10 });
  });

  it('hits the /data/query path', async () => {
    let capturedPath = '';
    const client: QueryApiClient = {
      requestJson: async <T,>(path: string) => {
        capturedPath = path;
        return { status: 200, data: { results: [] } as T, etag: null, notModified: false };
      },
    };
    await fetchRecordMetadataRows(client, 'EPM');
    expect(capturedPath).toBe('/data/query');
  });

  it('gives an honest sign-in message on 401', async () => {
    const client = fakeClient(() => new SdnApiError(401, { code: 'unauthorized', message: 'not authenticated' }, '/data/query'));
    const result = await fetchRecordMetadataRows(client, 'EPM');
    expect(result.errorKind).toBe('unauthenticated');
    expect(result.errorMessage).toBe(QUERY_SIGN_IN_MESSAGE);
    expect(result.rows).toEqual([]);
  });

  it('gives an honest trust-level message on 403', async () => {
    const client = fakeClient(() => new SdnApiError(403, { code: 'forbidden', message: 'insufficient trust' }, '/data/query'));
    const result = await fetchRecordMetadataRows(client, 'EPM');
    expect(result.errorKind).toBe('forbidden');
    expect(result.errorMessage).toBe(QUERY_FORBIDDEN_MESSAGE);
  });

  it('never throws — a network error degrades to an honest result', async () => {
    const client = fakeClient(() => new TypeError('Failed to fetch'));
    const result = await fetchRecordMetadataRows(client, 'EPM');
    expect(result.errorKind).toBe('other');
    expect(result.rows).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// fetchDecodedQueryRows
// ---------------------------------------------------------------------------

describe('fetchDecodedQueryRows', () => {
  it('parses a hypothetical G.5 {rows:[...]} response', async () => {
    const client = fakeClient(() => ({ rows: [{ NORAD_CAT_ID: 25544 }] }));
    const result = await fetchDecodedQueryRows(client, 'CAT');
    expect(result.engine).toBe('g5');
    expect(result.rows).toEqual([{ NORAD_CAT_ID: 25544 }]);
    expect(result.count).toBe(1);
  });

  it('degrades honestly on a 404', async () => {
    const client = fakeClient(() => new SdnApiError(404, null, '/query'));
    const result = await fetchDecodedQueryRows(client, 'CAT');
    expect(result.errorKind).toBe('other');
    expect(result.rows).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// loadQueryTabData — orchestration
// ---------------------------------------------------------------------------

describe('loadQueryTabData', () => {
  it('goes straight to the metadata fallback when engine availability is "unknown"', async () => {
    let capturedPath = '';
    const client: QueryApiClient = {
      requestJson: async <T,>(path: string) => {
        capturedPath = path;
        return { status: 200, data: { schema: 'EPM.fbs', count: 0, results: [] } as T, etag: null, notModified: false };
      },
    };
    const result = await loadQueryTabData(client, 'EPM', 'unknown');
    expect(result.engine).toBe('metadata');
    expect(capturedPath).toBe('/data/query');
  });

  it('goes straight to the metadata fallback when engine availability is "unavailable"', async () => {
    const client = fakeClient(() => ({ schema: 'EPM.fbs', count: 0, results: [] }));
    const result = await loadQueryTabData(client, 'EPM', 'unavailable');
    expect(result.engine).toBe('metadata');
  });

  it('queries G.5 directly when engine availability is "available"', async () => {
    let capturedPath = '';
    const client: QueryApiClient = {
      requestJson: async <T,>(path: string) => {
        capturedPath = path;
        return { status: 200, data: { rows: [{ NORAD_CAT_ID: 1 }] } as T, etag: null, notModified: false };
      },
    };
    const result = await loadQueryTabData(client, 'CAT', 'available');
    expect(result.engine).toBe('g5');
    expect(capturedPath).toBe('/query');
  });

  it('falls back to metadata when the G.5 call itself fails mid-session', async () => {
    let calls = 0;
    const client: QueryApiClient = {
      requestJson: async <T,>(path: string) => {
        calls += 1;
        if (path === '/query') throw new SdnApiError(500, null, '/query');
        return { status: 200, data: { schema: 'CAT.fbs', count: 0, results: [] } as T, etag: null, notModified: false };
      },
    };
    const result = await loadQueryTabData(client, 'CAT', 'available');
    expect(result.engine).toBe('metadata');
    expect(calls).toBe(2);
  });

  it('passes limit/offset through to the metadata fallback', async () => {
    let capturedBody: unknown;
    const client: QueryApiClient = {
      requestJson: async <T,>(_path: string, opts?: RequestOptions) => {
        capturedBody = opts?.body;
        return { status: 200, data: { results: [] } as T, etag: null, notModified: false };
      },
    };
    await loadQueryTabData(client, 'EPM', 'unavailable', 25, 5);
    expect(capturedBody).toEqual({ schema: 'EPM.fbs', limit: 25, offset: 5 });
  });
});

// ---------------------------------------------------------------------------
// Empty state / caption copy
// ---------------------------------------------------------------------------

describe('queryEmptyStateLabel', () => {
  it('shows a loading label before the query resolves', () => {
    expect(queryEmptyStateLabel('EPM', false, 0)).toBe(QUERY_LOADING_LABEL);
  });

  it('shows the honest "NO ROWS STORED FOR <CODE>" panel for a zero-row result', () => {
    expect(queryEmptyStateLabel('epm', true, 0)).toBe('NO ROWS STORED FOR EPM');
  });

  it('shows "" once rows exist', () => {
    expect(queryEmptyStateLabel('EPM', true, 3)).toBe('');
  });
});

describe('queryEngineCaption', () => {
  it('is honest about the metadata-only surface for the "metadata" engine', () => {
    expect(queryEngineCaption('metadata')).toBe(QUERY_METADATA_CAPTION);
    expect(queryEngineCaption('metadata')).toContain('G.5');
  });

  it('is distinct for the (hypothetical) "g5" engine', () => {
    expect(queryEngineCaption('g5')).toBe(QUERY_G5_CAPTION);
    expect(queryEngineCaption('g5')).not.toBe(queryEngineCaption('metadata'));
  });
});

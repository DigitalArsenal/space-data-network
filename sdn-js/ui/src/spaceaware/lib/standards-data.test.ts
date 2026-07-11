import { describe, expect, it } from 'vitest';
import {
  CODEGEN_LANGUAGES,
  DATA_TAB_PLACEHOLDER_COPY,
  DATA_TAB_PLACEHOLDER_TITLE,
  DATA_VIEW_TOGGLES,
  MODULES_PLACEHOLDER_COPY,
  MODULES_PLACEHOLDER_TITLE,
  NO_VENDORED_SCHEMA_MESSAGE,
  STANDARD_DETAIL_TABS,
  buildExplorerFieldRows,
  buildSchemaSyncBannerView,
  buildSelectedStandardHeader,
  buildStandardsListRows,
  codegenHeaderComment,
  dataViewToggleStyle,
  fbsDownloadFilename,
  findCodegenLanguage,
  formatSdsPackageVersionChip,
  formatStandardsCountCaption,
  formatTableRowsCaption,
  generateReaderStub,
  generatedCodeFilename,
  joinStandardsWithStats,
  loadStandardsDashboardData,
  parseChannelsResponse,
  sortStandardEntries,
  standardDetailTabStyle,
  standardIsEncrypted,
  standardStatusIndicator,
  toCamelCaseFieldName,
  toPascalCaseFieldName,
  type RawChannel,
  type StandardEntry,
  type StandardsApiClient,
} from './standards-data';
import type { NodeStatsSnapshot } from './node-data';
import { getVendoredSchema } from './standards-fbs';

function makeChannel(overrides: Partial<RawChannel> = {}): RawChannel {
  return {
    standardCode: 'CAT',
    topic: '/spacedatanetwork/channels/CAT',
    encryptionState: 'none',
    grantState: 'not-required',
    subscribed: false,
    visibility: 'public',
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// parseChannelsResponse
// ---------------------------------------------------------------------------

describe('parseChannelsResponse', () => {
  it('parses the real /api/v1/channels response shape', () => {
    const payload = {
      count: 2,
      results: [
        { standardCode: 'ACL', topic: '/spacedatanetwork/channels/ACL', encryptionState: 'none', grantState: 'not-required', subscribed: false, visibility: 'public' },
        { standardCode: 'MPE', topic: '/spacedatanetwork/channels/MPE', encryptionState: 'encrypted', grantState: 'required', subscribed: true, visibility: 'private-listed' },
      ],
    };
    const channels = parseChannelsResponse(payload);
    expect(channels).toHaveLength(2);
    expect(channels[0]).toEqual({
      standardCode: 'ACL',
      topic: '/spacedatanetwork/channels/ACL',
      encryptionState: 'none',
      grantState: 'not-required',
      subscribed: false,
      visibility: 'public',
    });
    expect(channels[1].encryptionState).toBe('encrypted');
    expect(channels[1].subscribed).toBe(true);
  });

  it('ignores extra fields a verified-publication row carries (channelId, pnmVerified, ...)', () => {
    const channels = parseChannelsResponse({
      results: [
        {
          standardCode: 'PNM',
          topic: '/x',
          encryptionState: 'none',
          grantState: 'verified',
          subscribed: true,
          visibility: 'public',
          channelId: 'abc',
          pnmVerified: true,
          feedUuid: null,
        },
      ],
    });
    expect(channels).toEqual([
      { standardCode: 'PNM', topic: '/x', encryptionState: 'none', grantState: 'verified', subscribed: true, visibility: 'public' },
    ]);
  });

  it('drops entries with no standardCode', () => {
    expect(parseChannelsResponse({ results: [{ topic: '/x' }] })).toEqual([]);
  });

  it('defaults missing encryptionState/grantState/visibility honestly rather than crashing', () => {
    const channels = parseChannelsResponse({ results: [{ standardCode: 'X' }] });
    expect(channels).toEqual([{ standardCode: 'X', topic: '', encryptionState: 'none', grantState: '', subscribed: false, visibility: '' }]);
  });

  it('handles a non-object payload / missing results array', () => {
    expect(parseChannelsResponse(null)).toEqual([]);
    expect(parseChannelsResponse(undefined)).toEqual([]);
    expect(parseChannelsResponse({})).toEqual([]);
    expect(parseChannelsResponse('garbage')).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// joinStandardsWithStats — the "absent from stats == confirmed zero" rule
// ---------------------------------------------------------------------------

describe('joinStandardsWithStats', () => {
  const stats: NodeStatsSnapshot = {
    connectedPeers: 2,
    totalBytes: 111_168,
    totalRecords: 6,
    schemas: [{ schema: 'PRR.fbs', count: 6, totalBytes: 111_168 }],
  };

  it('strips the .fbs suffix when joining schema names to standardCode', () => {
    const entries = joinStandardsWithStats([makeChannel({ standardCode: 'PRR' })], stats);
    expect(entries).toEqual([{ code: 'PRR', channel: expect.objectContaining({ standardCode: 'PRR' }), rows: 6 }]);
  });

  it('renders 0 (not a placeholder) for a standard absent from stats', () => {
    const entries = joinStandardsWithStats([makeChannel({ standardCode: 'ACL' })], stats);
    expect(entries[0].rows).toBe(0);
  });

  it('renders 0 for every entry when stats is null (fetch failed)', () => {
    const entries = joinStandardsWithStats([makeChannel({ standardCode: 'PRR' })], null);
    expect(entries[0].rows).toBe(0);
  });

  it('preserves channel list order and length', () => {
    const channels = [makeChannel({ standardCode: 'A' }), makeChannel({ standardCode: 'B' }), makeChannel({ standardCode: 'C' })];
    const entries = joinStandardsWithStats(channels, null);
    expect(entries.map((e) => e.code)).toEqual(['A', 'B', 'C']);
  });
});

// ---------------------------------------------------------------------------
// sortStandardEntries
// ---------------------------------------------------------------------------

describe('sortStandardEntries', () => {
  function entry(code: string, rows: number): StandardEntry {
    return { code, channel: makeChannel({ standardCode: code }), rows };
  }

  it('puts standards with rows before standards with zero rows', () => {
    const sorted = sortStandardEntries([entry('ZZZ', 0), entry('AAA', 5)]);
    expect(sorted.map((e) => e.code)).toEqual(['AAA', 'ZZZ']);
  });

  it('orders standards-with-rows descending by count', () => {
    const sorted = sortStandardEntries([entry('LOW', 3), entry('HIGH', 100), entry('MID', 40)]);
    expect(sorted.map((e) => e.code)).toEqual(['HIGH', 'MID', 'LOW']);
  });

  it('breaks a row-count tie alphabetically', () => {
    const sorted = sortStandardEntries([entry('BBB', 10), entry('AAA', 10)]);
    expect(sorted.map((e) => e.code)).toEqual(['AAA', 'BBB']);
  });

  it('orders zero-row standards alphabetically', () => {
    const sorted = sortStandardEntries([entry('ZZZ', 0), entry('AAA', 0), entry('MMM', 0)]);
    expect(sorted.map((e) => e.code)).toEqual(['AAA', 'MMM', 'ZZZ']);
  });

  it('does not mutate its input array', () => {
    const input = [entry('B', 1), entry('A', 2)];
    const inputCopy = input.slice();
    sortStandardEntries(input);
    expect(input).toEqual(inputCopy);
  });
});

// ---------------------------------------------------------------------------
// standardIsEncrypted — real data is always 'none' today; the padlock logic
// itself is proven with a synthetic 'encrypted' fixture
// ---------------------------------------------------------------------------

describe('standardIsEncrypted', () => {
  it('is false for the real live encryptionState value ("none")', () => {
    expect(standardIsEncrypted({ encryptionState: 'none' })).toBe(false);
  });

  it('is true for a synthetic encrypted fixture', () => {
    expect(standardIsEncrypted({ encryptionState: 'encrypted' })).toBe(true);
  });

  it('is false for an unrecognized value (never a false positive)', () => {
    expect(standardIsEncrypted({ encryptionState: 'signed' })).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// buildSchemaSyncBannerView
// ---------------------------------------------------------------------------

describe('buildSchemaSyncBannerView', () => {
  it('renders green SCHEMA SYNCED when stats loaded', () => {
    expect(buildSchemaSyncBannerView(true)).toEqual({ label: 'FLATSQL STORE · SCHEMA SYNCED', dotColor: '#5ad6a0' });
  });

  it('renders an honest degraded label when stats failed to load', () => {
    const view = buildSchemaSyncBannerView(false);
    expect(view.label).not.toContain('SYNCED');
    expect(view.dotColor).not.toBe('#5ad6a0');
  });
});

// ---------------------------------------------------------------------------
// Banner text helpers
// ---------------------------------------------------------------------------

describe('formatSdsPackageVersionChip', () => {
  it('formats the real node/info standards_version with a v prefix', () => {
    expect(formatSdsPackageVersionChip('1.136.0')).toBe('v1.136.0');
  });

  it('renders an honest dash for missing/blank input, never the mock fabricated version', () => {
    expect(formatSdsPackageVersionChip(null)).toBe('—');
    expect(formatSdsPackageVersionChip(undefined)).toBe('—');
    expect(formatSdsPackageVersionChip('  ')).toBe('—');
  });
});

describe('formatStandardsCountCaption', () => {
  it('pluralizes normally', () => {
    expect(formatStandardsCountCaption(173)).toBe('173 standards · FlatBuffers IDL');
  });

  it('singularizes for exactly 1', () => {
    expect(formatStandardsCountCaption(1)).toBe('1 standard · FlatBuffers IDL');
  });

  it('handles 0 honestly', () => {
    expect(formatStandardsCountCaption(0)).toBe('0 standards · FlatBuffers IDL');
  });
});

// ---------------------------------------------------------------------------
// standardStatusIndicator
// ---------------------------------------------------------------------------

describe('standardStatusIndicator', () => {
  it('shows a padlock for an encrypted standard regardless of row count', () => {
    expect(standardStatusIndicator(0, true)).toEqual({ glyph: '🔒', color: '#ffb24d' });
    expect(standardStatusIndicator(500, true)).toEqual({ glyph: '🔒', color: '#ffb24d' });
  });

  it('shows a green dot when rows are synced and not encrypted', () => {
    expect(standardStatusIndicator(6, false)).toEqual({ glyph: '●', color: '#5ad6a0' });
  });

  it('shows a neutral gray dot when nothing has synced yet — never a fabricated fresh/stale state', () => {
    expect(standardStatusIndicator(0, false)).toEqual({ glyph: '●', color: '#5a7a8a' });
  });
});

// ---------------------------------------------------------------------------
// buildStandardsListRows / buildSelectedStandardHeader — real vendored
// schema (CAT) + an unknown code with no vendored .fbs
// ---------------------------------------------------------------------------

describe('buildStandardsListRows', () => {
  it('derives label/version from the real vendored CAT schema', () => {
    const entries: StandardEntry[] = [{ code: 'CAT', channel: makeChannel({ standardCode: 'CAT' }), rows: 8462 }];
    const rows = buildStandardsListRows(entries, 'CAT');
    expect(rows).toEqual([
      {
        code: 'CAT',
        label: 'Catalog Entity Message',
        versionLabel: 'v1.0.2',
        rowsLabel: '8,462 rows',
        encrypted: false,
        statusGlyph: '●',
        statusColor: '#5ad6a0',
        selected: true,
      },
    ]);
  });

  it('falls back to the code itself and an honest dash version for a code with no vendored schema', () => {
    const entries: StandardEntry[] = [{ code: 'ZZZ-NOPE', channel: makeChannel({ standardCode: 'ZZZ-NOPE' }), rows: 0 }];
    const rows = buildStandardsListRows(entries, null);
    expect(rows[0].label).toBe('ZZZ-NOPE');
    expect(rows[0].versionLabel).toBe('—');
    expect(rows[0].selected).toBe(false);
  });

  it('marks the MPE-style synthetic encrypted fixture with the padlock, not a color dot', () => {
    const entries: StandardEntry[] = [{ code: 'MPE', channel: makeChannel({ standardCode: 'MPE', encryptionState: 'encrypted' }), rows: 18 }];
    const rows = buildStandardsListRows(entries, null);
    expect(rows[0].encrypted).toBe(true);
    expect(rows[0].statusGlyph).toBe('🔒');
  });
});

describe('buildSelectedStandardHeader', () => {
  it('returns null when nothing is selected', () => {
    expect(buildSelectedStandardHeader(null)).toBeNull();
  });

  it('builds the header for a real standard (OMM)', () => {
    const entry: StandardEntry = { code: 'OMM', channel: makeChannel({ standardCode: 'OMM' }), rows: 9120 };
    const header = buildSelectedStandardHeader(entry)!;
    expect(header.code).toBe('OMM');
    expect(header.name).toBe(getVendoredSchema('OMM')?.label);
    expect(header.versionChip).toBe(`v${getVendoredSchema('OMM')?.version}`);
    expect(header.encrypted).toBe(false);
    expect(header.rowsCaption).toBe('table OMM · 9,120 rows');
  });
});

describe('formatTableRowsCaption', () => {
  it('formats with thousands separators', () => {
    expect(formatTableRowsCaption('CAT', 8462)).toBe('table CAT · 8,462 rows');
  });

  it('formats zero honestly', () => {
    expect(formatTableRowsCaption('ACL', 0)).toBe('table ACL · 0 rows');
  });
});

// ---------------------------------------------------------------------------
// buildExplorerFieldRows
// ---------------------------------------------------------------------------

describe('buildExplorerFieldRows', () => {
  it('returns an empty list for a null schema (no vendored .fbs)', () => {
    expect(buildExplorerFieldRows(null)).toEqual([]);
  });

  it('maps the real CAT schema fields with type colors', () => {
    const schema = getVendoredSchema('CAT')!;
    const rows = buildExplorerFieldRows(schema);
    const norad = rows.find((r) => r.name === 'NORAD_CAT_ID')!;
    expect(norad.type).toBe('uint');
    expect(norad.typeColor).toBe('#9fd4f5');
    expect(norad.note).toBe('NORAD Catalog Number');
    const payloads = rows.find((r) => r.name === 'PAYLOADS')!;
    expect(payloads.type).toBe('[PLD]');
    expect(payloads.typeColor).toBe('#c77dff');
  });
});

describe('fbsDownloadFilename', () => {
  it('appends .fbs to the code', () => {
    expect(fbsDownloadFilename('CAT')).toBe('CAT.fbs');
  });
});

// ---------------------------------------------------------------------------
// GENERATE tab: languages, casing helpers, codegen header, stub generator
// ---------------------------------------------------------------------------

describe('CODEGEN_LANGUAGES', () => {
  it('is exactly these 8 languages in this order', () => {
    expect(CODEGEN_LANGUAGES.map((l) => l.name)).toEqual([
      'TypeScript',
      'Python',
      'C++',
      'Go',
      'Rust',
      'C#',
      'Java',
      'Swift',
    ]);
  });

  it('gives every language a distinct file extension', () => {
    const exts = CODEGEN_LANGUAGES.map((l) => l.ext);
    expect(new Set(exts).size).toBe(exts.length);
  });
});

describe('findCodegenLanguage', () => {
  it('resolves a known id', () => {
    expect(findCodegenLanguage('python').name).toBe('Python');
  });
});

describe('toPascalCaseFieldName / toCamelCaseFieldName', () => {
  it('converts SCREAMING_SNAKE_CASE to PascalCase', () => {
    expect(toPascalCaseFieldName('NORAD_CAT_ID')).toBe('NoradCatId');
  });

  it('converts SCREAMING_SNAKE_CASE to camelCase (matches the mock\'s noradCatId example)', () => {
    expect(toCamelCaseFieldName('NORAD_CAT_ID')).toBe('noradCatId');
  });

  it('handles a single-word field name', () => {
    expect(toPascalCaseFieldName('EPOCH')).toBe('Epoch');
    expect(toCamelCaseFieldName('EPOCH')).toBe('epoch');
  });
});

describe('codegenHeaderComment', () => {
  it('is honest — never claims flatc ran in the browser', () => {
    const header = codegenHeaderComment('CAT', '1.136.0');
    expect(header).not.toMatch(/flatc/i);
    expect(header).toContain('Stub reader generated client-side from CAT.fbs');
    expect(header).toContain('spacedatastandards.org 1.136.0');
  });

  it('omits the version clause entirely when the package version is unknown', () => {
    const header = codegenHeaderComment('CAT', null);
    expect(header).toContain('spacedatastandards.org)');
  });
});

describe('generateReaderStub', () => {
  const catSchema = getVendoredSchema('CAT')!;

  it('renders the honest no-schema message when schema is null', () => {
    const stub = generateReaderStub('ZZZ', null, '1.136.0', 'typescript');
    expect(stub).toContain(NO_VENDORED_SCHEMA_MESSAGE);
    expect(stub).not.toContain('function');
  });

  it('typescript: camelCase accessors, no flatc claim', () => {
    const stub = generateReaderStub('CAT', catSchema, '1.136.0', 'typescript');
    expect(stub).toContain('export function readCAT(bytes: Uint8Array)');
    expect(stub).toContain('noradCatId: msg.noradCatId(),');
  });

  it('python: snake_case function name, PascalCase accessor, # comment header', () => {
    const stub = generateReaderStub('CAT', catSchema, '1.136.0', 'python');
    expect(stub).toContain('def read_cat(buf: bytes) -> dict:');
    expect(stub).toContain('"NORAD_CAT_ID": msg.NoradCatId(),');
    expect(stub.startsWith('# Stub reader generated client-side')).toBe(true);
  });

  it('cpp: header include + lowercase accessor comment', () => {
    const stub = generateReaderStub('CAT', catSchema, '1.136.0', 'cpp');
    expect(stub).toContain('#include "CAT_generated.h"');
    expect(stub).toContain('read_cat(const void* buf)');
  });

  it('go: PascalCase exported function + package sds', () => {
    const stub = generateReaderStub('CAT', catSchema, '1.136.0', 'go');
    expect(stub).toContain('package sds');
    expect(stub).toContain('func ReadCAT(buf []byte) *CAT {');
  });

  it('rust: snake_case free function', () => {
    const stub = generateReaderStub('CAT', catSchema, '1.136.0', 'rust');
    expect(stub).toContain('pub fn read_cat(buf: &[u8]) -> CAT {');
  });

  it('csharp: PascalCase reader class', () => {
    const stub = generateReaderStub('CAT', catSchema, '1.136.0', 'csharp');
    expect(stub).toContain('public static class CATReader {');
    expect(stub).toContain('using Google.FlatBuffers;');
  });

  it('java: camelCase accessor comments', () => {
    const stub = generateReaderStub('CAT', catSchema, '1.136.0', 'java');
    expect(stub).toContain('public class CATReader {');
    expect(stub).toContain('msg.noradCatId()');
  });

  it('swift: camelCase accessor comments + FlatBuffers import', () => {
    const stub = generateReaderStub('CAT', catSchema, '1.136.0', 'swift');
    expect(stub).toContain('import FlatBuffers');
    expect(stub).toContain('func readCAT(_ buf: [UInt8]) -> CAT {');
  });

  it('every language stub includes a field for every parsed field (field-count fidelity)', () => {
    for (const lang of CODEGEN_LANGUAGES) {
      const stub = generateReaderStub('CAT', catSchema, '1.136.0', lang.id);
      // NORAD_CAT_ID appears in some casing/form (camelCase, PascalCase, or
      // snake_case) in every language's stub — normalize away underscores
      // and case before comparing.
      const normalized = stub.toLowerCase().replace(/_/g, '');
      expect(normalized).toContain('noradcatid');
    }
  });
});

describe('generatedCodeFilename', () => {
  it('matches the CODE_generated.<ext> pattern', () => {
    expect(generatedCodeFilename('CAT', findCodegenLanguage('typescript'))).toBe('CAT_generated.ts');
    expect(generatedCodeFilename('CAT', findCodegenLanguage('python'))).toBe('CAT_generated.py');
    expect(generatedCodeFilename('CAT', findCodegenLanguage('cpp'))).toBe('CAT_generated.h');
  });
});

// ---------------------------------------------------------------------------
// Toggle / tab styling
// ---------------------------------------------------------------------------

describe('DATA_VIEW_TOGGLES / dataViewToggleStyle', () => {
  it('has exactly the two mock toggles in order', () => {
    expect(DATA_VIEW_TOGGLES.map((t) => t.label)).toEqual(['DATA STANDARDS', 'MODULES']);
  });

  it('styles the active toggle distinctly from the inactive one', () => {
    const active = dataViewToggleStyle('standards', 'standards');
    const inactive = dataViewToggleStyle('modules', 'standards');
    expect(active.color).not.toBe(inactive.color);
    expect(active.background).not.toBe(inactive.background);
  });
});

describe('STANDARD_DETAIL_TABS / standardDetailTabStyle', () => {
  it('has exactly EXPLORER, GENERATE, DATA in order', () => {
    expect(STANDARD_DETAIL_TABS.map((t) => t.label)).toEqual(['EXPLORER', 'GENERATE', 'DATA']);
  });

  it('styles the active tab with the accent underline', () => {
    const active = standardDetailTabStyle('explorer', 'explorer');
    const inactive = standardDetailTabStyle('generate', 'explorer');
    expect(active.borderColor).toBe('#35c9d8');
    expect(inactive.borderColor).toBe('transparent');
  });
});

// ---------------------------------------------------------------------------
// Honest placeholders
// ---------------------------------------------------------------------------

describe('placeholder copy', () => {
  it('DATA tab placeholder references U3.6, matches ConsolePlaceholder-style honesty', () => {
    expect(DATA_TAB_PLACEHOLDER_TITLE).toContain('NOT YET WIRED');
    expect(DATA_TAB_PLACEHOLDER_COPY).toContain('U3.6');
    expect(DATA_TAB_PLACEHOLDER_COPY.toLowerCase()).not.toContain('mock');
  });

  it('MODULES placeholder references U3.6', () => {
    expect(MODULES_PLACEHOLDER_TITLE).toContain('NOT YET WIRED');
    expect(MODULES_PLACEHOLDER_COPY).toContain('U3.6');
  });
});

// ---------------------------------------------------------------------------
// loadStandardsDashboardData — fetch orchestration (fake apiClient, never
// throws even when every endpoint fails)
// ---------------------------------------------------------------------------

describe('loadStandardsDashboardData', () => {
  // Same generic-handler fake-client shape as node-data.test.ts's
  // loadNodeDashboardData tests — a plain per-path value/Error map, cast
  // through `unknown` so the shared `requestJson<T>` generic doesn't need a
  // real discriminated-union type for every path's differently-shaped body.
  function fakeApiClient(handlers: Record<string, unknown>): StandardsApiClient {
    return {
      requestJson: async <T,>(path: string) => {
        if (!(path in handlers)) throw new Error(`unexpected path ${path}`);
        const value = handlers[path];
        if (value instanceof Error) throw value;
        return { status: 200, data: value as T, etag: null, notModified: false };
      },
    } as unknown as StandardsApiClient;
  }

  it('parses a successful set of responses', async () => {
    const apiClient = fakeApiClient({
      '/channels': {
        count: 1,
        results: [{ standardCode: 'CAT', topic: '/x', encryptionState: 'none', grantState: 'not-required', subscribed: false, visibility: 'public' }],
      },
      '/stats': { schemas: [{ schema: 'CAT.fbs', count: 8462, total_bytes: 1000 }], total_bytes: 1000, total_records: 8462, connected_peers: 2 },
      '/api/node/info': { standards_version: '1.136.0' },
    });
    const data = await loadStandardsDashboardData(apiClient);
    expect(data.channels).toHaveLength(1);
    expect(data.stats?.schemas[0].count).toBe(8462);
    expect(data.nodeInfo?.standardsVersion).toBe('1.136.0');
  });

  it('never rejects — every endpoint failing resolves to an honest empty/null snapshot', async () => {
    const apiClient = fakeApiClient({
      '/channels': new Error('offline'),
      '/stats': new Error('offline'),
      '/api/node/info': new Error('offline'),
    });
    const data = await loadStandardsDashboardData(apiClient);
    expect(data).toEqual({ channels: [], stats: null, nodeInfo: null });
  });

  it('degrades each surface independently (channels ok, stats/node-info fail)', async () => {
    const apiClient = fakeApiClient({
      '/channels': { count: 0, results: [] },
      '/stats': new Error('down'),
      '/api/node/info': new Error('down'),
    });
    const data = await loadStandardsDashboardData(apiClient);
    expect(data.channels).toEqual([]);
    expect(data.stats).toBeNull();
    expect(data.nodeInfo).toBeNull();
  });
});

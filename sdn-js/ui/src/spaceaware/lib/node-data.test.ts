import { describe, expect, it } from 'vitest';
import {
  EPM_CID_NOT_PUBLISHED,
  NODE_PEER_SUMMARY_EMPTY_LABEL,
  buildEpmDownloadFilename,
  buildNodeHealthView,
  buildNodeIdentityView,
  buildNodeNetmapView,
  buildNodePeerSummary,
  buildNodeServiceView,
  buildNodeStorageView,
  composeServiceVersionLine,
  deriveIdentitySubtitle,
  deriveListenAddressRows,
  encodeQrDataUrl,
  extractHostPort,
  fetchVCardText,
  flattenJsonToCsv,
  formatAddrCount,
  formatBytes,
  formatConnectedPeersCount,
  formatModeLabel,
  loadNodeDashboardData,
  parseEpmIdentity,
  parseNodeInfo,
  parseNodePeers,
  parseNodeStats,
  parseVCardFn,
  serviceStatusDotColor,
  slugifyForFilename,
  truncateMiddle,
  type NodeInfoApiClient,
} from './node-data';

// ---------------------------------------------------------------------------
// formatBytes
// ---------------------------------------------------------------------------

describe('formatBytes', () => {
  it('renders an honest "—" for missing/invalid input', () => {
    expect(formatBytes(null)).toBe('—');
    expect(formatBytes(undefined)).toBe('—');
    expect(formatBytes(Number.NaN)).toBe('—');
    expect(formatBytes(-5)).toBe('—');
  });

  it('renders 0 bytes explicitly rather than a blank/dash', () => {
    expect(formatBytes(0)).toBe('0 B');
  });

  it('formats sub-1024 values as whole bytes', () => {
    expect(formatBytes(512)).toBe('512 B');
    expect(formatBytes(1023)).toBe('1023 B');
  });

  it('matches the real /api/v1/stats sample (117832 bytes -> "115.1 KB")', () => {
    expect(formatBytes(117_832)).toBe('115.1 KB');
  });

  it('formats MB and GB ranges with one decimal', () => {
    expect(formatBytes(5_242_880)).toBe('5.0 MB'); // 5 * 1024^2
    expect(formatBytes(5_153_960_755)).toBe('4.8 GB');
  });

  it('caps at TB rather than overflowing further', () => {
    const twoTb = 2 * 1024 ** 4;
    expect(formatBytes(twoTb)).toBe('2.0 TB');
  });
});

// ---------------------------------------------------------------------------
// truncateMiddle
// ---------------------------------------------------------------------------

describe('truncateMiddle', () => {
  it('renders "—" for blank/missing input', () => {
    expect(truncateMiddle(null)).toBe('—');
    expect(truncateMiddle(undefined)).toBe('—');
    expect(truncateMiddle('   ')).toBe('—');
  });

  it('passes short values through untouched', () => {
    expect(truncateMiddle('12D3Koo')).toBe('12D3Koo');
  });

  it('truncates long values with an ellipsis, keeping head/tail lengths', () => {
    const id = '12D3KooWAbCdEfGh1234567890AbCdEfGh1234567890Ab';
    const result = truncateMiddle(id);
    expect(result).toBe('12D3KooW…7890Ab');
    expect(result.startsWith(id.slice(0, 8))).toBe(true);
    expect(result.endsWith(id.slice(-6))).toBe(true);
  });

  it('respects custom head/tail lengths', () => {
    expect(truncateMiddle('abcdefghijklmnop', 3, 3)).toBe('abc…nop');
  });
});

// ---------------------------------------------------------------------------
// extractHostPort / deriveListenAddressRows
// ---------------------------------------------------------------------------

describe('extractHostPort', () => {
  it('parses an ip4 + tcp multiaddr', () => {
    expect(extractHostPort('/ip4/127.0.0.1/tcp/5001')).toBe('127.0.0.1:5001');
  });

  it('ignores a trailing /p2p/<id> component', () => {
    expect(extractHostPort('/ip4/10.0.0.5/tcp/4001/p2p/12D3KooWabc')).toBe('10.0.0.5:4001');
  });

  it('brackets a literal ip6 host', () => {
    expect(extractHostPort('/ip6/::1/tcp/4001')).toBe('[::1]:4001');
  });

  it('does not bracket a dns6 hostname', () => {
    expect(extractHostPort('/dns6/example.com/tcp/443')).toBe('example.com:443');
  });

  it('parses dns4 hosts', () => {
    expect(extractHostPort('/dns4/relay.spacedatanetwork.org/tcp/443')).toBe('relay.spacedatanetwork.org:443');
  });

  it('returns null when there is no port segment', () => {
    expect(extractHostPort('/ip4/127.0.0.1')).toBeNull();
  });

  it('returns null when there is no host segment (e.g. a unix socket)', () => {
    expect(extractHostPort('/unix/tmp/sdn.sock')).toBeNull();
  });

  it('returns null for non-string input', () => {
    expect(extractHostPort(null)).toBeNull();
    expect(extractHostPort(42)).toBeNull();
    expect(extractHostPort(undefined)).toBeNull();
  });

  it('returns null for an empty string', () => {
    expect(extractHostPort('')).toBeNull();
  });
});

describe('deriveListenAddressRows', () => {
  it('renders both rows as "—" for an empty/missing list', () => {
    expect(deriveListenAddressRows([])).toEqual({ api: '—', gateway: '—' });
    expect(deriveListenAddressRows(null)).toEqual({ api: '—', gateway: '—' });
    expect(deriveListenAddressRows(undefined)).toEqual({ api: '—', gateway: '—' });
  });

  it('fills only API when there is a single parseable address', () => {
    expect(deriveListenAddressRows(['/ip4/127.0.0.1/tcp/4001'])).toEqual({ api: '127.0.0.1:4001', gateway: '—' });
  });

  it('fills both rows when there are two distinct addresses', () => {
    expect(deriveListenAddressRows(['/ip4/127.0.0.1/tcp/4001', '/ip4/0.0.0.0/tcp/8080'])).toEqual({
      api: '127.0.0.1:4001',
      gateway: '0.0.0.0:8080',
    });
  });

  it('collapses duplicate host:port pairs to a single unique entry', () => {
    expect(
      deriveListenAddressRows(['/ip4/127.0.0.1/tcp/4001/p2p/abc', '/ip4/127.0.0.1/tcp/4001/p2p/xyz']),
    ).toEqual({ api: '127.0.0.1:4001', gateway: '—' });
  });

  it('skips unparseable entries entirely rather than surfacing a partial address', () => {
    expect(deriveListenAddressRows(['/unix/tmp/sdn.sock', '/ip4/127.0.0.1/tcp/4001'])).toEqual({
      api: '127.0.0.1:4001',
      gateway: '—',
    });
  });

  it("excludes /p2p-circuit relay reservations — their host:port is the RELAY's address, not this node's", () => {
    // Real shape from a live node: relay circuits through bootstrap peers
    // precede the node's own listen addrs in listen_addresses.
    expect(
      deriveListenAddressRows([
        '/ip4/104.131.11.220/tcp/4001/p2p/16Uiu2HAm1Lbv/p2p-circuit',
        '/ip4/104.131.11.220/tcp/8080/ws/p2p/16Uiu2HAm1Lbv/p2p-circuit',
        '/ip4/127.0.0.1/tcp/14001',
        '/ip4/127.0.0.1/tcp/18080/ws',
      ]),
    ).toEqual({ api: '127.0.0.1:14001', gateway: '127.0.0.1:18080' });
  });

  it('renders honest dashes when ONLY relay circuits are present', () => {
    expect(deriveListenAddressRows(['/ip4/104.131.11.220/tcp/4001/p2p/abc/p2p-circuit'])).toEqual({
      api: '—',
      gateway: '—',
    });
  });
});

// ---------------------------------------------------------------------------
// Small formatters
// ---------------------------------------------------------------------------

describe('formatModeLabel', () => {
  it('uppercases and prefixes a real mode value', () => {
    expect(formatModeLabel('desktop-local')).toBe('MODE · DESKTOP-LOCAL');
  });

  it('renders an honest dash for missing/blank mode', () => {
    expect(formatModeLabel(null)).toBe('MODE · —');
    expect(formatModeLabel(undefined)).toBe('MODE · —');
    expect(formatModeLabel('  ')).toBe('MODE · —');
  });
});

describe('formatAddrCount', () => {
  it('pluralizes correctly', () => {
    expect(formatAddrCount(0)).toBe('0 ADDRS');
    expect(formatAddrCount(1)).toBe('1 ADDR');
    expect(formatAddrCount(5)).toBe('5 ADDRS');
  });

  it('clamps negative/non-finite input to 0 rather than throwing', () => {
    expect(formatAddrCount(-3)).toBe('0 ADDRS');
    expect(formatAddrCount(Number.NaN)).toBe('0 ADDRS');
  });
});

describe('formatConnectedPeersCount', () => {
  it('renders an honest dash for missing/invalid input', () => {
    expect(formatConnectedPeersCount(null)).toBe('—');
    expect(formatConnectedPeersCount(undefined)).toBe('—');
    expect(formatConnectedPeersCount(Number.NaN)).toBe('—');
  });

  it('renders the real connected-peer count, truncated to an integer', () => {
    expect(formatConnectedPeersCount(117)).toBe('117');
    expect(formatConnectedPeersCount(3.9)).toBe('3');
  });
});

describe('composeServiceVersionLine', () => {
  it('composes all three fields honestly when present', () => {
    expect(
      composeServiceVersionLine({ version: '0.47.0', suiteVersion: '2.1.0', agentVersion: 'spacedatanetwork/1.0.4' }),
    ).toBe('v0.47.0 · suite 2.1.0 · spacedatanetwork/1.0.4');
  });

  it('omits missing fields rather than fabricating them', () => {
    expect(composeServiceVersionLine({ version: '0.47.0' })).toBe('v0.47.0');
  });

  it('renders an honest dash when nothing is available', () => {
    expect(composeServiceVersionLine({})).toBe('—');
  });

  it('strips the "spacedatanetwork/" product prefix before the v-prefix and drops a duplicated agent_version', () => {
    // Real shape from a live node: version === agent_version === "spacedatanetwork/1.0.4".
    expect(
      composeServiceVersionLine({
        version: 'spacedatanetwork/1.0.4',
        suiteVersion: '1.0.4',
        agentVersion: 'spacedatanetwork/1.0.4',
      }),
    ).toBe('v1.0.4 · suite 1.0.4');
  });
});

describe('serviceStatusDotColor', () => {
  it('is green only for a confirmed RUNNING state', () => {
    expect(serviceStatusDotColor('RUNNING')).toBe('#5ad6a0');
    expect(serviceStatusDotColor('—')).toBe('#5a7a8a');
  });
});

describe('deriveIdentitySubtitle', () => {
  it('appends a real entity type, uppercased', () => {
    expect(deriveIdentitySubtitle('operator')).toBe('Entity Profile Metadata · OPERATOR');
  });

  it('drops the mock-only "self-issued" claim when entity_type is absent', () => {
    expect(deriveIdentitySubtitle(null)).toBe('Entity Profile Metadata');
    expect(deriveIdentitySubtitle(undefined)).toBe('Entity Profile Metadata');
  });
});

describe('parseVCardFn', () => {
  it('extracts a simple FN line', () => {
    const vcard = 'BEGIN:VCARD\nVERSION:3.0\nFN:SDN Operator\nEND:VCARD';
    expect(parseVCardFn(vcard)).toBe('SDN Operator');
  });

  it('unescapes vCard-spec escape sequences', () => {
    const vcard = 'BEGIN:VCARD\nFN:Smith\\, Jane\\; Node Ops\\\\Team\nEND:VCARD';
    expect(parseVCardFn(vcard)).toBe('Smith, Jane; Node Ops\\Team');
  });

  it('returns null for missing/blank FN or empty input', () => {
    expect(parseVCardFn('BEGIN:VCARD\nVERSION:3.0\nEND:VCARD')).toBeNull();
    expect(parseVCardFn('')).toBeNull();
    expect(parseVCardFn(null)).toBeNull();
    expect(parseVCardFn(undefined)).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// Raw endpoint parsers
// ---------------------------------------------------------------------------

describe('parseNodeInfo', () => {
  it('parses a full /api/node/info payload', () => {
    const payload = {
      peer_id: '12D3KooWExample',
      mode: 'desktop-local',
      version: '0.47.0',
      suite_version: '2.1.0',
      standards_version: '1.0.3',
      agent_version: 'spacedatanetwork/1.0.4',
      listen_addresses: ['/ip4/127.0.0.1/tcp/4001'],
      dn: 'SDN Operator',
      entity_type: 'operator',
      xpub: 'xpub123',
      multiformat_address: 'f01701220...',
      signature_timestamp: 1700000000,
    };
    expect(parseNodeInfo(payload)).toEqual({
      peerId: '12D3KooWExample',
      mode: 'desktop-local',
      version: '0.47.0',
      suiteVersion: '2.1.0',
      standardsVersion: '1.0.3',
      agentVersion: 'spacedatanetwork/1.0.4',
      listenAddresses: ['/ip4/127.0.0.1/tcp/4001'],
      dn: 'SDN Operator',
      entityType: 'operator',
    });
  });

  it('degrades every field to null/[] for a non-object payload', () => {
    expect(parseNodeInfo(null)).toEqual({
      peerId: null,
      mode: null,
      version: null,
      suiteVersion: null,
      standardsVersion: null,
      agentVersion: null,
      listenAddresses: [],
      dn: null,
      entityType: null,
    });
    expect(parseNodeInfo('not an object').peerId).toBeNull();
    expect(parseNodeInfo([1, 2, 3]).listenAddresses).toEqual([]);
  });

  it('filters non-string entries out of listen_addresses', () => {
    expect(parseNodeInfo({ listen_addresses: ['/ip4/1.2.3.4/tcp/1', 42, null] }).listenAddresses).toEqual([
      '/ip4/1.2.3.4/tcp/1',
    ]);
  });
});

describe('parseEpmIdentity', () => {
  it('parses dn/entity_type and ignores everything else', () => {
    expect(parseEpmIdentity({ dn: 'SDN Operator', entity_type: 'operator', xpub: 'xpub123' })).toEqual({
      dn: 'SDN Operator',
      entityType: 'operator',
    });
  });

  it('degrades to nulls for a malformed payload', () => {
    expect(parseEpmIdentity(undefined)).toEqual({ dn: null, entityType: null });
  });
});

describe('parseNodeStats', () => {
  it('parses the real /api/v1/stats sample shape', () => {
    const payload = {
      connected_peers: 117,
      schemas: [{ count: 6, schema: 'PRR.fbs', total_bytes: 111_168 }],
      total_bytes: 117_832,
      total_records: 8,
    };
    expect(parseNodeStats(payload)).toEqual({
      connectedPeers: 117,
      totalBytes: 117_832,
      totalRecords: 8,
      schemas: [{ schema: 'PRR.fbs', count: 6, totalBytes: 111_168 }],
    });
  });

  it('defaults schemas to [] when absent, and degrades a non-object payload entirely', () => {
    expect(parseNodeStats({ total_bytes: 5 }).schemas).toEqual([]);
    expect(parseNodeStats(null)).toEqual({ connectedPeers: null, totalBytes: null, totalRecords: null, schemas: [] });
  });

  it('fills honest fallbacks for a malformed schema entry', () => {
    expect(parseNodeStats({ schemas: [{}] }).schemas).toEqual([{ schema: 'UNKNOWN', count: 0, totalBytes: 0 }]);
  });
});

describe('parseNodePeers', () => {
  it('parses the real /api/v1/peers object shape (not a bare array)', () => {
    const payload = {
      peers: [
        { peer_id: '12D3KooWAAA', addrs: ['/ip4/1.1.1.1/tcp/4001'] },
        { peer_id: '12D3KooWBBB', addrs: [] },
      ],
    };
    expect(parseNodePeers(payload)).toEqual([
      { peerId: '12D3KooWAAA', addrs: ['/ip4/1.1.1.1/tcp/4001'] },
      { peerId: '12D3KooWBBB', addrs: [] },
    ]);
  });

  it('drops entries with no peer_id', () => {
    expect(parseNodePeers({ peers: [{ addrs: [] }] })).toEqual([]);
  });

  it('returns [] for a bare array payload (would be a server-shape regression) or missing peers key', () => {
    expect(parseNodePeers([{ peer_id: 'x', addrs: [] }])).toEqual([]);
    expect(parseNodePeers({})).toEqual([]);
    expect(parseNodePeers(null)).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// View-model builders
// ---------------------------------------------------------------------------

describe('buildNodeHealthView', () => {
  it('builds a full view from real info + stats bytes', () => {
    const info = parseNodeInfo({
      peer_id: '12D3KooWExample',
      mode: 'desktop-local',
      listen_addresses: ['/ip4/127.0.0.1/tcp/4001', '/ip4/0.0.0.0/tcp/8080'],
    });
    expect(buildNodeHealthView(info, 117_832)).toEqual({
      mode: 'MODE · DESKTOP-LOCAL',
      peerId: '12D3KooWExample',
      api: '127.0.0.1:4001',
      gateway: '0.0.0.0:8080',
      storageUsed: '115.1 KB',
      storageTotal: '— capacity unknown',
      storagePercent: 0,
    });
  });

  it('renders every honest fallback when info/bytes are unavailable', () => {
    expect(buildNodeHealthView(null, null)).toEqual({
      mode: 'MODE · —',
      peerId: '—',
      api: '—',
      gateway: '—',
      storageUsed: '—',
      storageTotal: '— capacity unknown',
      storagePercent: 0,
    });
  });
});

describe('buildNodeIdentityView', () => {
  it('prefers the vCard FN over dn for the vCARD row, and always marks CID unpublished', () => {
    const identity = parseEpmIdentity({ dn: 'SDN Operator', entity_type: 'operator' });
    const view = buildNodeIdentityView(identity, 'BEGIN:VCARD\nFN:Operator Display Name\nEND:VCARD');
    expect(view).toEqual({
      name: 'SDN Operator',
      subtitle: 'Entity Profile Metadata · OPERATOR',
      epmCid: EPM_CID_NOT_PUBLISHED,
      vcard: 'Operator Display Name',
    });
  });

  it('falls back to dn when there is no vCard FN, and to "—" when there is neither', () => {
    const identity = parseEpmIdentity({ dn: 'SDN Operator' });
    expect(buildNodeIdentityView(identity, null).vcard).toBe('SDN Operator');
    expect(buildNodeIdentityView(null, null)).toEqual({
      name: '—',
      subtitle: 'Entity Profile Metadata',
      epmCid: EPM_CID_NOT_PUBLISHED,
      vcard: '—',
    });
  });
});

describe('buildNodeServiceView', () => {
  it('reports RUNNING with an honest version line when node/info succeeded', () => {
    const info = parseNodeInfo({ version: '0.47.0', suite_version: '2.1.0', agent_version: 'spacedatanetwork/1.0.4' });
    expect(buildNodeServiceView(info)).toEqual({
      state: 'RUNNING',
      version: 'v0.47.0 · suite 2.1.0 · spacedatanetwork/1.0.4',
      autostart: '—',
      uptime: '—',
    });
  });

  it('renders every honest fallback when node/info is unavailable', () => {
    expect(buildNodeServiceView(null)).toEqual({ state: '—', version: '—', autostart: '—', uptime: '—' });
  });
});

describe('buildNodeNetmapView', () => {
  it('uses the real connected_peers count and an honest country dash', () => {
    expect(buildNodeNetmapView(parseNodeStats({ connected_peers: 117 }))).toEqual({
      connectionCount: '117',
      countryCount: '—',
    });
  });

  it('renders an honest dash for both fields when stats are unavailable', () => {
    expect(buildNodeNetmapView(null)).toEqual({ connectionCount: '—', countryCount: '—' });
  });
});

describe('buildNodePeerSummary', () => {
  const peers = [
    { peerId: '12D3KooWAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA', addrs: ['/ip4/1.1.1.1/tcp/1'] },
    { peerId: '12D3KooWBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB', addrs: [] },
    { peerId: '12D3KooWCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC', addrs: ['/ip4/2.2.2.2/tcp/1', '/ip4/3.3.3.3/tcp/1'] },
    { peerId: '12D3KooWDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD', addrs: [] },
  ];

  it('caps at 3 rows and never fabricates a TRUSTED badge', () => {
    const rows = buildNodePeerSummary(peers);
    expect(rows).toHaveLength(3);
    for (const row of rows) {
      expect(row.trust).toBe('OBSERVED');
      expect(row.trustColor).not.toBe('#5ad6a0'); // the TRUSTED green
    }
  });

  it('renders address counts honestly per peer', () => {
    const rows = buildNodePeerSummary(peers);
    expect(rows[0]!.feeds).toBe('1 ADDR');
    expect(rows[1]!.feeds).toBe('0 ADDRS');
    expect(rows[2]!.feeds).toBe('2 ADDRS');
  });

  it('returns [] for an empty peer list (caller renders NODE_PEER_SUMMARY_EMPTY_LABEL)', () => {
    expect(buildNodePeerSummary([])).toEqual([]);
    expect(NODE_PEER_SUMMARY_EMPTY_LABEL).toBe('NO PEERS');
  });
});

describe('buildNodeStorageView', () => {
  it('builds a full view from real stats + standards_version', () => {
    const stats = parseNodeStats({
      total_bytes: 117_832,
      total_records: 8,
      schemas: [{ schema: 'PRR.fbs', count: 6, total_bytes: 111_168 }],
    });
    const view = buildNodeStorageView(stats, '1.0.3');
    expect(view).toEqual({
      used: '115.1 KB',
      total: '8 RECORDS',
      percent: 0,
      standardsSynced: 'STANDARDS 1.0.3',
      freshness: '—',
      freshnessKnown: false,
      schemaRows: [{ label: 'PRR.fbs', value: '6 RECORDS · 108.6 KB' }],
    });
  });

  it('renders every honest fallback when stats/standards_version are unavailable', () => {
    expect(buildNodeStorageView(null, null)).toEqual({
      used: '—',
      total: '— RECORDS',
      percent: 0,
      standardsSynced: '—',
      freshness: '—',
      freshnessKnown: false,
      schemaRows: [],
    });
  });
});

// ---------------------------------------------------------------------------
// Identity export payloads
// ---------------------------------------------------------------------------

describe('flattenJsonToCsv', () => {
  it('flattens scalar top-level fields into a 2-row CSV', () => {
    expect(flattenJsonToCsv({ dn: 'SDN Operator', entity_type: 'operator', active: true, rank: 3 })).toBe(
      'dn,entity_type,active,rank\nSDN Operator,operator,true,3',
    );
  });

  it('skips nested objects/arrays', () => {
    expect(flattenJsonToCsv({ dn: 'X', keys: [{ a: 1 }], meta: { nested: true } })).toBe('dn\nX');
  });

  it('quotes and escapes fields containing commas/quotes', () => {
    expect(flattenJsonToCsv({ dn: 'Smith, "Jane"' })).toBe('dn\n"Smith, ""Jane"""');
  });

  it('returns "" when there is nothing scalar to export', () => {
    expect(flattenJsonToCsv({})).toBe('');
    expect(flattenJsonToCsv(null)).toBe('');
    expect(flattenJsonToCsv({ nested: { a: 1 } })).toBe('');
  });
});

describe('slugifyForFilename / buildEpmDownloadFilename', () => {
  it('slugifies a display name', () => {
    expect(slugifyForFilename('SDN Operator')).toBe('sdn-operator');
    expect(slugifyForFilename('Node #1 (west)')).toBe('node-1-west');
  });

  it('falls back to "identity" for blank/unsafe input', () => {
    expect(slugifyForFilename(null)).toBe('identity');
    expect(slugifyForFilename('   ')).toBe('identity');
    expect(slugifyForFilename('***')).toBe('identity');
  });

  it('builds the download filename with the given extension', () => {
    expect(buildEpmDownloadFilename('SDN Operator', 'json')).toBe('epm-sdn-operator.json');
    expect(buildEpmDownloadFilename(null, 'vcf')).toBe('epm-identity.vcf');
  });
});

// ---------------------------------------------------------------------------
// QR encoding
// ---------------------------------------------------------------------------

describe('encodeQrDataUrl', () => {
  it('encodes real payload text into a deterministic PNG data URL', async () => {
    const payload = 'BEGIN:VCARD\nVERSION:3.0\nFN:Test Node\nEND:VCARD';
    const a = await encodeQrDataUrl(payload);
    const b = await encodeQrDataUrl(payload);
    expect(a).toMatch(/^data:image\/png;base64,/);
    expect(a).toBe(b);
  });

  it('returns null for blank/missing payload without touching the qrcode module', async () => {
    expect(await encodeQrDataUrl('')).toBeNull();
    expect(await encodeQrDataUrl('   ')).toBeNull();
    expect(await encodeQrDataUrl(null)).toBeNull();
    expect(await encodeQrDataUrl(undefined)).toBeNull();
  });

  it('degrades to level L for a real-node-sized vCard that exceeds level-M capacity (~2.3KB)', async () => {
    // A live node's vCard (chain proofs included) measured 2587 bytes —
    // level M throws "data is too big" on it; level L fits up to ~2.9KB.
    const payload = `BEGIN:VCARD\nVERSION:3.0\nFN:Big Node\n${'X'.repeat(2500)}\nEND:VCARD`;
    const out = await encodeQrDataUrl(payload);
    expect(out).toMatch(/^data:image\/png;base64,/);
  });

  it('returns null (honest fallback) when the payload exceeds even level-L capacity', async () => {
    // Lowercase forces QR byte mode (level-L cap 2953 bytes) — uppercase
    // would slip into the roomier alphanumeric mode and still encode.
    const payload = 'y'.repeat(4000);
    expect(await encodeQrDataUrl(payload)).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// Fetch orchestration
// ---------------------------------------------------------------------------

function fakeFetch(status: number, body: string): typeof fetch {
  return (async () =>
    ({
      ok: status >= 200 && status < 300,
      status,
      text: async () => body,
    }) as unknown as Response) as unknown as typeof fetch;
}

function throwingFetch(): typeof fetch {
  return (async () => {
    throw new Error('network unreachable');
  }) as unknown as typeof fetch;
}

describe('fetchVCardText', () => {
  it('returns the raw vCard text on a 200', async () => {
    const text = 'BEGIN:VCARD\nFN:Test\nEND:VCARD';
    expect(await fetchVCardText(fakeFetch(200, text))).toBe(text);
  });

  it('returns null on a non-2xx status rather than throwing', async () => {
    expect(await fetchVCardText(fakeFetch(500, 'content too long to encode'))).toBeNull();
  });

  it('returns null when the fetch itself throws (offline)', async () => {
    expect(await fetchVCardText(throwingFetch())).toBeNull();
  });
});

describe('loadNodeDashboardData', () => {
  function fakeApiClient(handlers: Record<string, unknown>): NodeInfoApiClient {
    return {
      requestJson: async <T,>(path: string) => {
        if (!(path in handlers)) throw new Error(`unexpected path ${path}`);
        const value = handlers[path];
        if (value instanceof Error) throw value;
        return { status: 200, data: value as T, etag: null, notModified: false };
      },
    } as unknown as NodeInfoApiClient;
  }

  it('assembles every surface into one snapshot on success', async () => {
    const apiClient = fakeApiClient({
      '/api/node/info': { peer_id: '12D3KooWExample', mode: 'desktop-local' },
      '/api/node/epm/json': { dn: 'SDN Operator', entity_type: 'operator' },
      '/stats': { connected_peers: 117, total_bytes: 117_832, total_records: 8 },
      '/peers': { peers: [{ peer_id: '12D3KooWAAA', addrs: [] }] },
    });
    const data = await loadNodeDashboardData(apiClient, fakeFetch(200, 'BEGIN:VCARD\nFN:Test\nEND:VCARD'));
    expect(data.nodeInfo?.peerId).toBe('12D3KooWExample');
    expect(data.identity).toEqual({ dn: 'SDN Operator', entityType: 'operator' });
    expect(data.epmJsonRaw).toEqual({ dn: 'SDN Operator', entity_type: 'operator' });
    expect(data.vcardText).toBe('BEGIN:VCARD\nFN:Test\nEND:VCARD');
    expect(data.stats?.connectedPeers).toBe(117);
    expect(data.peers).toEqual([{ peerId: '12D3KooWAAA', addrs: [] }]);
  });

  it('degrades only the failing surface to null/[] when one endpoint is unreachable, never throwing', async () => {
    const apiClient = fakeApiClient({
      '/api/node/info': new Error('offline'),
      '/api/node/epm/json': { dn: 'SDN Operator' },
      '/stats': new Error('offline'),
      '/peers': new Error('offline'),
    });
    const data = await loadNodeDashboardData(apiClient, throwingFetch());
    expect(data.nodeInfo).toBeNull();
    expect(data.identity).toEqual({ dn: 'SDN Operator', entityType: null });
    expect(data.stats).toBeNull();
    expect(data.peers).toEqual([]);
    expect(data.vcardText).toBeNull();
  });
});

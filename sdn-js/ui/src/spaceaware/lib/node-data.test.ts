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
  buildNodeThroughputView,
  buildThroughputBars,
  buildThroughputRateView,
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
  formatRate,
  formatThroughputAxisLabel,
  formatUptime,
  loadNodeDashboardData,
  parseEpmIdentity,
  parseNodeInfo,
  parseNodePeers,
  parseNodeStats,
  parseNodeStatus,
  parseVCardFn,
  serviceStatusDotColor,
  slugifyForFilename,
  truncateMiddle,
  type NodeBandwidthHistorySample,
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

describe('formatUptime', () => {
  it('renders an honest "—" for missing/invalid input', () => {
    expect(formatUptime(null)).toBe('—');
    expect(formatUptime(undefined)).toBe('—');
    expect(formatUptime(Number.NaN)).toBe('—');
    expect(formatUptime(-1)).toBe('—');
  });

  it('renders "00:00" for zero uptime', () => {
    expect(formatUptime(0)).toBe('00:00');
  });

  it('drops the day segment entirely under 24h, rendering plain "HH:MM"', () => {
    expect(formatUptime(45)).toBe('00:00'); // sub-minute
    expect(formatUptime(60)).toBe('00:01');
    expect(formatUptime(3600)).toBe('01:00');
    expect(formatUptime(86399)).toBe('23:59'); // one second under a day
  });

  it('adds the day segment starting at exactly 24h', () => {
    expect(formatUptime(86400)).toBe('1d 00:00');
  });

  it('matches the mock\'s "4d 02:11" style for a real multi-day sample (355860s)', () => {
    expect(formatUptime(355860)).toBe('4d 02:51');
  });

  it('keeps HH:MM zero-padded for a large day count', () => {
    expect(formatUptime(864000)).toBe('10d 00:00'); // exactly 10 days
  });

  it('truncates fractional seconds rather than rounding', () => {
    expect(formatUptime(119.9)).toBe('00:01'); // 119s -> 1m59s, not rounded up to 2m
  });
});

describe('formatRate', () => {
  it('renders an honest "—" for missing/invalid input', () => {
    expect(formatRate(null)).toBe('—');
    expect(formatRate(undefined)).toBe('—');
    expect(formatRate(Number.NaN)).toBe('—');
    expect(formatRate(-5)).toBe('—');
  });

  it('renders 0 bytes/s explicitly rather than a blank/dash', () => {
    expect(formatRate(0)).toBe('0 B/s');
  });

  it('formats sub-1024 values as whole bytes/s', () => {
    expect(formatRate(512)).toBe('512 B/s');
    expect(formatRate(1023)).toBe('1023 B/s');
  });

  it('formats the loop U4.1 sample inputs (3452 bps -> KB/s, 3452000 bps -> MB/s)', () => {
    expect(formatRate(3452)).toBe('3.37 KB/s');
    expect(formatRate(3_452_000)).toBe('3.29 MB/s');
  });

  it('formats GB/s with two-decimal precision (unlike formatBytes\' one-decimal style)', () => {
    expect(formatRate(5 * 1024 ** 3)).toBe('5.00 GB/s');
  });

  it('caps at TB/s rather than overflowing further', () => {
    expect(formatRate(2 * 1024 ** 4)).toBe('2.00 TB/s');
  });
});

describe('formatThroughputAxisLabel', () => {
  it('renders a seconds label under a minute', () => {
    expect(formatThroughputAxisLabel(2)).toBe('−10s'); // 2 samples * 5s
  });

  it('switches to a rounded minutes label at/above 60s', () => {
    expect(formatThroughputAxisLabel(12)).toBe('−1m'); // exactly 60s
    expect(formatThroughputAxisLabel(24)).toBe('−2m'); // a full 24-sample buffer, ~2 min per the daemon's own doc
  });

  it('renders "−0s" for zero/negative/non-finite sample counts rather than throwing', () => {
    expect(formatThroughputAxisLabel(0)).toBe('−0s');
    expect(formatThroughputAxisLabel(-3)).toBe('−0s');
    expect(formatThroughputAxisLabel(Number.NaN)).toBe('−0s');
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

describe('parseNodeStatus', () => {
  it('parses a full /api/v1/node/status payload (disk + service + bandwidth + history all present)', () => {
    const payload = {
      uptime_seconds: 355_860,
      started_at: '2026-07-06T12:00:00Z',
      store: { total_bytes: 117_832, total_records: 8, storage_path: '/var/lib/sdn/flatsql' },
      disk: { capacity_bytes: 32_000_000_000, free_bytes: 20_000_000_000, available_bytes: 19_500_000_000 },
      service: { state: 'running', mode: 'desktop-local', autostart_known: false },
      bandwidth: {
        total_in_bytes: 1_000_000,
        total_out_bytes: 500_000,
        rate_in_bps: 3_452_000,
        rate_out_bps: 922_000,
        history: [
          { ts: '2026-07-06T12:00:00Z', total_in_bytes: 900_000, total_out_bytes: 450_000, rate_in_bps: 3_000_000, rate_out_bps: 800_000 },
          { ts: '2026-07-06T12:00:05Z', total_in_bytes: 1_000_000, total_out_bytes: 500_000, rate_in_bps: 3_452_000, rate_out_bps: 922_000 },
        ],
      },
    };
    expect(parseNodeStatus(payload)).toEqual({
      uptimeSeconds: 355_860,
      startedAt: '2026-07-06T12:00:00Z',
      store: { totalBytes: 117_832, totalRecords: 8, storagePath: '/var/lib/sdn/flatsql' },
      disk: { capacityBytes: 32_000_000_000, freeBytes: 20_000_000_000, availableBytes: 19_500_000_000 },
      service: { state: 'running', mode: 'desktop-local', autostartKnown: false },
      bandwidth: {
        totalInBytes: 1_000_000,
        totalOutBytes: 500_000,
        rateInBps: 3_452_000,
        rateOutBps: 922_000,
        history: [
          { ts: '2026-07-06T12:00:00Z', totalInBytes: 900_000, totalOutBytes: 450_000, rateInBps: 3_000_000, rateOutBps: 800_000 },
          { ts: '2026-07-06T12:00:05Z', totalInBytes: 1_000_000, totalOutBytes: 500_000, rateInBps: 3_452_000, rateOutBps: 922_000 },
        ],
      },
    });
  });

  it('preserves a real "disk: null" / "bandwidth: null" as an honest null, not a parse failure', () => {
    const result = parseNodeStatus({ uptime_seconds: 10, disk: null, bandwidth: null, service: { state: 'running', mode: 'desktop-local', autostart_known: false } });
    expect(result.disk).toBeNull();
    expect(result.bandwidth).toBeNull();
  });

  it('degrades every field to null/empty for a malformed/non-object payload', () => {
    expect(parseNodeStatus(null)).toEqual({
      uptimeSeconds: null,
      startedAt: null,
      store: null,
      disk: null,
      service: null,
      bandwidth: null,
    });
    expect(parseNodeStatus('not an object').uptimeSeconds).toBeNull();
    expect(parseNodeStatus([1, 2, 3]).bandwidth).toBeNull();
  });

  it('drops malformed history entries rather than crashing on them', () => {
    const result = parseNodeStatus({
      bandwidth: { rate_in_bps: 1, rate_out_bps: 1, history: [{ rate_in_bps: 5 }, null, 'garbage', 42] },
    });
    expect(result.bandwidth?.history).toEqual([
      { ts: null, totalInBytes: null, totalOutBytes: null, rateInBps: 5, rateOutBps: null },
    ]);
  });

  it('parses service.autostart_known as a real boolean in either direction', () => {
    expect(parseNodeStatus({ service: { autostart_known: true } }).service?.autostartKnown).toBe(true);
    expect(parseNodeStatus({ service: { autostart_known: false } }).service?.autostartKnown).toBe(false);
    expect(parseNodeStatus({ service: {} }).service?.autostartKnown).toBe(false);
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

  it('renders the mock\'s "used / capacity" pattern with a real (un-clamped, tiny) percent when disk capacity is real (loop U4.1)', () => {
    const view = buildNodeHealthView(null, 117_832, 34_359_738_368 /* 32 GiB */);
    expect(view.storageUsed).toBe('115.1 KB');
    expect(view.storageTotal).toBe('32.0 GB');
    expect(view.storagePercent).toBeGreaterThan(0);
    expect(view.storagePercent).toBeLessThan(0.001); // honest sliver, not artificially clamped up
  });

  it('shows the real capacity even when used bytes are unavailable, with a 0% bar', () => {
    const view = buildNodeHealthView(null, null, 34_359_738_368);
    expect(view.storageUsed).toBe('—');
    expect(view.storageTotal).toBe('32.0 GB');
    expect(view.storagePercent).toBe(0);
  });

  it('treats a null/zero/negative disk capacity as "capacity unknown", not a fabricated total', () => {
    expect(buildNodeHealthView(null, 117_832, null).storageTotal).toBe('— capacity unknown');
    expect(buildNodeHealthView(null, 117_832, 0).storageTotal).toBe('— capacity unknown');
    expect(buildNodeHealthView(null, 117_832, -5).storageTotal).toBe('— capacity unknown');
  });

  it('clamps a used-over-capacity percent at 100 rather than overflowing', () => {
    expect(buildNodeHealthView(null, 200, 100).storagePercent).toBe(100);
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
  it('reports RUNNING with an honest version line when node/info succeeded, uptime defaulting to "—" when omitted', () => {
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

  it('renders a real formatUptime-style uptime when /node/status uptime_seconds is real (loop U4.1)', () => {
    const info = parseNodeInfo({ version: '0.47.0' });
    expect(buildNodeServiceView(info, 355_860).uptime).toBe('4d 02:51');
  });

  it('keeps AUTOSTART an honest "—" no matter the uptime value — no daemon surface has ever backed it', () => {
    const info = parseNodeInfo({ version: '0.47.0' });
    expect(buildNodeServiceView(info, 355_860).autostart).toBe('—');
    expect(buildNodeServiceView(info, 0).autostart).toBe('—');
    expect(buildNodeServiceView(info, null).autostart).toBe('—');
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

describe('buildThroughputRateView', () => {
  it('renders the down figure in its own adaptive unit and the up figure at that SAME unit', () => {
    expect(buildThroughputRateView(3_452_000, 922_000)).toEqual({
      downValue: '3.29',
      downUnit: 'MB/s',
      upValue: '0.88', // 922000 / 1024^2, expressed at the down figure's MB/s tier, not its own adaptive KB/s
    });
  });

  it('renders an honest dash pair when rate_in_bps is missing/invalid, regardless of rate_out_bps', () => {
    expect(buildThroughputRateView(null, 922_000)).toEqual({ downValue: '—', downUnit: '', upValue: '—' });
    expect(buildThroughputRateView(Number.NaN, 922_000)).toEqual({ downValue: '—', downUnit: '', upValue: '—' });
    expect(buildThroughputRateView(-1, 922_000)).toEqual({ downValue: '—', downUnit: '', upValue: '—' });
  });

  it('renders an honest dash for just the up figure when only rate_out_bps is missing', () => {
    const view = buildThroughputRateView(3_452_000, null);
    expect(view.downValue).toBe('3.29');
    expect(view.upValue).toBe('—');
  });

  it('renders whole-byte precision at the B/s tier for a small rate_in_bps', () => {
    expect(buildThroughputRateView(512, 256)).toEqual({ downValue: '512', downUnit: 'B/s', upValue: '256' });
  });
});

describe('buildThroughputBars', () => {
  function sample(rateInBps: number | null): NodeBandwidthHistorySample {
    return { ts: null, totalInBytes: null, totalOutBytes: null, rateInBps, rateOutBps: null };
  }

  it('returns [] for an empty history', () => {
    expect(buildThroughputBars([])).toEqual([]);
  });

  it('normalizes a single sample to 100% of itself', () => {
    const bars = buildThroughputBars([sample(1000)]);
    expect(bars).toHaveLength(1);
    expect(bars[0]!.percent).toBe(100);
  });

  it('normalizes multiple samples to the max sample in the window', () => {
    const bars = buildThroughputBars([sample(50), sample(100), sample(25)]);
    expect(bars.map((b) => b.percent)).toEqual([50, 100, 25]);
  });

  it('renders every bar at 0% for a genuinely idle link (all-zero samples) rather than dividing by zero', () => {
    const bars = buildThroughputBars([sample(0), sample(0)]);
    expect(bars.map((b) => b.percent)).toEqual([0, 0]);
  });

  it('clamps a negative/malformed sample rate to 0 rather than a negative bar height', () => {
    const bars = buildThroughputBars([sample(-50), sample(null), sample(100)]);
    expect(bars.map((b) => b.percent)).toEqual([0, 0, 100]);
  });

  it('pairs each bar with a real lib/console.ts throughputBarGradient by index', () => {
    const bars = buildThroughputBars([sample(1), sample(2), sample(3)]);
    expect(bars.every((b) => typeof b.gradient === 'string' && b.gradient.includes('gradient'))).toBe(true);
  });
});

describe('buildNodeThroughputView', () => {
  function sample(rateInBps: number, tsOffsetSec: number): NodeBandwidthHistorySample {
    return { ts: `t+${tsOffsetSec}`, totalInBytes: null, totalOutBytes: null, rateInBps, rateOutBps: null };
  }

  it('renders hasData:false (the pre-U4.1 NO TELEMETRY state) when bandwidth is null', () => {
    expect(buildNodeThroughputView(null)).toEqual({
      hasData: false,
      downValue: '—',
      downUnit: '',
      upValue: '—',
      collecting: false,
      bars: [],
      axisStart: '',
      axisEnd: '',
    });
  });

  it('renders the headline pair but collecting:true with no bars for 0-1 history samples', () => {
    const view = buildNodeThroughputView({
      totalInBytes: null,
      totalOutBytes: null,
      rateInBps: 3_452_000,
      rateOutBps: 922_000,
      history: [],
    });
    expect(view.hasData).toBe(true);
    expect(view.collecting).toBe(true);
    expect(view.downValue).toBe('3.29');
    expect(view.bars).toEqual([]);
    expect(view.axisStart).toBe('');
  });

  it('renders real bars + a real axis span for ≥2 history samples', () => {
    const history = [sample(10, 0), sample(20, 5), sample(15, 10)];
    const view = buildNodeThroughputView({
      totalInBytes: null,
      totalOutBytes: null,
      rateInBps: 3_452_000,
      rateOutBps: 922_000,
      history,
    });
    expect(view.hasData).toBe(true);
    expect(view.collecting).toBe(false);
    expect(view.bars).toHaveLength(3);
    expect(view.axisStart).toBe('−15s'); // 3 samples * 5s
    expect(view.axisEnd).toBe('NOW');
  });

  it('computes the real "−2m"/"NOW" axis span for a full 24-sample buffer', () => {
    const history = Array.from({ length: 24 }, (_, i) => sample(i + 1, i * 5));
    const view = buildNodeThroughputView({ totalInBytes: null, totalOutBytes: null, rateInBps: 1, rateOutBps: 1, history });
    expect(view.axisStart).toBe('−2m');
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

  it('assembles every surface into one snapshot on success, including the loop U4.1 /node/status surface', async () => {
    const apiClient = fakeApiClient({
      '/api/node/info': { peer_id: '12D3KooWExample', mode: 'desktop-local' },
      '/api/node/epm/json': { dn: 'SDN Operator', entity_type: 'operator' },
      '/stats': { connected_peers: 117, total_bytes: 117_832, total_records: 8 },
      '/peers': { peers: [{ peer_id: '12D3KooWAAA', addrs: [] }] },
      '/node/status': { uptime_seconds: 355_860, disk: { capacity_bytes: 34_359_738_368 }, service: { autostart_known: false } },
    });
    const data = await loadNodeDashboardData(apiClient, fakeFetch(200, 'BEGIN:VCARD\nFN:Test\nEND:VCARD'));
    expect(data.nodeInfo?.peerId).toBe('12D3KooWExample');
    expect(data.identity).toEqual({ dn: 'SDN Operator', entityType: 'operator' });
    expect(data.epmJsonRaw).toEqual({ dn: 'SDN Operator', entity_type: 'operator' });
    expect(data.vcardText).toBe('BEGIN:VCARD\nFN:Test\nEND:VCARD');
    expect(data.stats?.connectedPeers).toBe(117);
    expect(data.peers).toEqual([{ peerId: '12D3KooWAAA', addrs: [] }]);
    expect(data.status?.uptimeSeconds).toBe(355_860);
    expect(data.status?.disk?.capacityBytes).toBe(34_359_738_368);
  });

  it('degrades only the failing surface to null/[] when one endpoint is unreachable, never throwing', async () => {
    const apiClient = fakeApiClient({
      '/api/node/info': new Error('offline'),
      '/api/node/epm/json': { dn: 'SDN Operator' },
      '/stats': new Error('offline'),
      '/peers': new Error('offline'),
      '/node/status': new Error('offline'),
    });
    const data = await loadNodeDashboardData(apiClient, throwingFetch());
    expect(data.nodeInfo).toBeNull();
    expect(data.identity).toEqual({ dn: 'SDN Operator', entityType: null });
    expect(data.stats).toBeNull();
    expect(data.peers).toEqual([]);
    expect(data.vcardText).toBeNull();
    expect(data.status).toBeNull();
  });

  it('degrades ONLY /node/status to null (e.g. an anonymous session\'s 401) while every other surface still succeeds', async () => {
    const apiClient = fakeApiClient({
      '/api/node/info': { peer_id: '12D3KooWExample' },
      '/api/node/epm/json': { dn: 'SDN Operator' },
      '/stats': { total_bytes: 117_832 },
      '/peers': { peers: [] },
      '/node/status': new Error('401 anonymous'),
    });
    const data = await loadNodeDashboardData(apiClient, fakeFetch(200, ''));
    expect(data.status).toBeNull();
    expect(data.nodeInfo?.peerId).toBe('12D3KooWExample');
    expect(data.stats?.totalBytes).toBe(117_832);
  });
});

import { describe, expect, it } from 'vitest';
import {
  NETMAP_HOME_COLOR,
  NETMAP_PEER_COLOR,
  NETMAP_UNRESOLVED_TOOLTIP,
  SDN_NETMAP_GEO_TABLE,
  buildNetmapPoints,
  countResolvedCountries,
  extractPublicIp,
  formatNetmapCountryCount,
  hashPeerIdToLatLon,
  netmapPointColor,
  resolveGeo,
  type NetmapGeoEntry,
  type NetmapPoint,
} from './netmap-data';
import type { RawPeer } from './node-data';

// ---------------------------------------------------------------------------
// extractPublicIp
// ---------------------------------------------------------------------------

describe('extractPublicIp', () => {
  it('returns null for missing/empty input', () => {
    expect(extractPublicIp(null)).toBeNull();
    expect(extractPublicIp(undefined)).toBeNull();
    expect(extractPublicIp([])).toBeNull();
  });

  it('extracts a public ip4 address from a real bootstrap-style multiaddr', () => {
    expect(extractPublicIp(['/ip4/159.203.150.8/tcp/4001/p2p/16Uiu2HAm1Lb'])).toBe('159.203.150.8');
  });

  it('extracts a public ip6 address', () => {
    expect(extractPublicIp(['/ip6/2604:a880:400:d1::82f:6001/tcp/4001'])).toBe('2604:a880:400:d1::82f:6001');
  });

  it('strips an ip6 zone id before returning', () => {
    expect(extractPublicIp(['/ip6/2604:a880:400:d1::82f:6001%eth0/tcp/4001'])).toBe('2604:a880:400:d1::82f:6001');
  });

  it('skips a loopback ip4 address', () => {
    expect(extractPublicIp(['/ip4/127.0.0.1/tcp/4001'])).toBeNull();
  });

  it('skips RFC1918 private ip4 ranges', () => {
    expect(extractPublicIp(['/ip4/10.0.0.5/tcp/4001'])).toBeNull();
    expect(extractPublicIp(['/ip4/172.16.4.4/tcp/4001'])).toBeNull();
    expect(extractPublicIp(['/ip4/192.168.1.20/tcp/4001'])).toBeNull();
  });

  it('skips link-local ip4', () => {
    expect(extractPublicIp(['/ip4/169.254.1.1/tcp/4001'])).toBeNull();
  });

  it('skips CGNAT (RFC6598) ip4', () => {
    expect(extractPublicIp(['/ip4/100.64.0.1/tcp/4001'])).toBeNull();
  });

  it('skips RFC5737 documentation ip4 ranges', () => {
    expect(extractPublicIp(['/ip4/203.0.113.10/tcp/4001'])).toBeNull();
    expect(extractPublicIp(['/ip4/198.51.100.20/tcp/4001'])).toBeNull();
    expect(extractPublicIp(['/ip4/192.0.2.1/tcp/4001'])).toBeNull();
  });

  it('skips ip6 loopback and unspecified', () => {
    expect(extractPublicIp(['/ip6/::1/tcp/4001'])).toBeNull();
    expect(extractPublicIp(['/ip6/::/tcp/4001'])).toBeNull();
  });

  it('skips ip6 link-local (fe80::/10)', () => {
    expect(extractPublicIp(['/ip6/fe80::1/tcp/4001'])).toBeNull();
  });

  it('skips ip6 unique-local (fc00::/7)', () => {
    expect(extractPublicIp(['/ip6/fd00::1/tcp/4001'])).toBeNull();
    expect(extractPublicIp(['/ip6/fc12::1/tcp/4001'])).toBeNull();
  });

  it('skips dns4/dns6/dnsaddr/dns segments — no literal IP to classify', () => {
    expect(extractPublicIp(['/dnsaddr/bootstrap.spacedatanetwork.org/p2p/16Uiu2HAm1Lb'])).toBeNull();
    expect(extractPublicIp(['/dns4/example.org/tcp/4001'])).toBeNull();
    expect(extractPublicIp(['/dns6/example.org/tcp/4001'])).toBeNull();
  });

  it('skips /p2p-circuit relay reservations even when they contain an ip4 segment', () => {
    expect(extractPublicIp(['/ip4/159.203.150.8/tcp/4001/p2p/16Uiu2HAm1Lb/p2p-circuit/p2p/12D3KooWTarget'])).toBeNull();
  });

  it('returns the first PUBLIC address, skipping a private one that comes first', () => {
    expect(extractPublicIp(['/ip4/192.168.1.5/tcp/4001', '/ip4/159.203.150.8/tcp/4001'])).toBe('159.203.150.8');
  });

  it('never throws on malformed/garbage addr strings and returns null', () => {
    expect(() => extractPublicIp(['not a multiaddr', '/ip4/999.999.999.999/tcp/4001'])).not.toThrow();
    expect(extractPublicIp(['not a multiaddr', '/ip4/999.999.999.999/tcp/4001'])).toBeNull();
  });

  it('ignores non-string entries in the array without throwing', () => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    expect(extractPublicIp([null, 42, '/ip4/159.203.150.8/tcp/4001'] as any)).toBe('159.203.150.8');
  });

  it('skips a socket-style multiaddr with no ip4/ip6 segment at all', () => {
    expect(extractPublicIp(['/unix/tmp/sdn.sock'])).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// resolveGeo
// ---------------------------------------------------------------------------

describe('resolveGeo', () => {
  it('resolves the primary bootstrap host to New York, US', () => {
    const result = resolveGeo('159.203.150.8');
    expect(result).toEqual({ lat: 40.7128, lon: -74.006, country: 'US', place: 'New York, US' });
  });

  it('resolves the celestrak bootstrap host to San Francisco, US', () => {
    const result = resolveGeo('167.172.219.213');
    expect(result).toEqual({ lat: 37.7749, lon: -122.4194, country: 'US', place: 'San Francisco, US' });
  });

  it('returns null for an unlisted ip4 address', () => {
    expect(resolveGeo('8.8.8.8')).toBeNull();
  });

  it('returns null for blank/missing input', () => {
    expect(resolveGeo(null)).toBeNull();
    expect(resolveGeo(undefined)).toBeNull();
    expect(resolveGeo('')).toBeNull();
  });

  it('never matches an ip6 lookup against the ip4-only default table', () => {
    expect(resolveGeo('2604:a880:400:d1::82f:6001')).toBeNull();
  });

  it('respects family when a custom table has both ip4 and ip6 entries', () => {
    const table: NetmapGeoEntry[] = [
      { prefix: '10.1.2.3/32', family: 'ip4', lat: 1, lon: 1, country: 'ZZ', place: 'IPv4 Test' },
      { prefix: 'abcd::1/128', family: 'ip6', lat: 2, lon: 2, country: 'YY', place: 'IPv6 Test' },
    ];
    expect(resolveGeo('10.1.2.3', table)?.country).toBe('ZZ');
    expect(resolveGeo('abcd::1', table)?.country).toBe('YY');
    // cross-family: an ip6-shaped lookup never matches an ip4 entry and vice versa
    expect(resolveGeo('10.1.2.3'.replace('.', ':'), table)).not.toEqual(
      expect.objectContaining({ country: 'ZZ' }),
    );
  });

  it('picks the longest (most specific) matching prefix', () => {
    const table: NetmapGeoEntry[] = [
      { prefix: '159.203.0.0/16', family: 'ip4', lat: 0, lon: 0, country: 'AA', place: 'Broad' },
      { prefix: '159.203.150.8/32', family: 'ip4', lat: 40.7128, lon: -74.006, country: 'US', place: 'Specific' },
    ];
    expect(resolveGeo('159.203.150.8', table)?.place).toBe('Specific');
    expect(resolveGeo('159.203.99.99', table)?.place).toBe('Broad');
  });

  it('supports only exact (/128) ipv6 entries, never a broader ipv6 prefix', () => {
    const table: NetmapGeoEntry[] = [{ prefix: 'abcd::/64', family: 'ip6', lat: 9, lon: 9, country: 'XX', place: 'Nope' }];
    expect(resolveGeo('abcd::1', table)).toBeNull();
  });

  it('the default vendored table only exposes documented production hosts', () => {
    expect(SDN_NETMAP_GEO_TABLE.length).toBeGreaterThan(0);
    expect(SDN_NETMAP_GEO_TABLE.every((e) => e.family === 'ip4')).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// hashPeerIdToLatLon
// ---------------------------------------------------------------------------

describe('hashPeerIdToLatLon', () => {
  it('is deterministic — same peer id always yields the same coordinates', () => {
    const a = hashPeerIdToLatLon('12D3KooWAbCdEfGh1234567890');
    const b = hashPeerIdToLatLon('12D3KooWAbCdEfGh1234567890');
    expect(a).toEqual(b);
  });

  it('produces coordinates within valid bounds', () => {
    for (const id of ['12D3KooWAAA', '16Uiu2HAmBBB', 'x', '']) {
      const { lat, lon } = hashPeerIdToLatLon(id);
      expect(lat).toBeGreaterThanOrEqual(-60);
      expect(lat).toBeLessThan(60);
      expect(lon).toBeGreaterThanOrEqual(-180);
      expect(lon).toBeLessThan(180);
    }
  });

  it('different peer ids generally land at different coordinates', () => {
    const a = hashPeerIdToLatLon('12D3KooWAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA');
    const b = hashPeerIdToLatLon('16Uiu2HAmBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB');
    expect(a).not.toEqual(b);
  });

  it('never throws on blank input and still returns a valid pair', () => {
    expect(() => hashPeerIdToLatLon('')).not.toThrow();
    const { lat, lon } = hashPeerIdToLatLon('');
    expect(Number.isFinite(lat)).toBe(true);
    expect(Number.isFinite(lon)).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// buildNetmapPoints
// ---------------------------------------------------------------------------

function peer(peerId: string, addrs: string[]): RawPeer {
  return { peerId, addrs };
}

describe('buildNetmapPoints', () => {
  it('builds a resolved HOME point when selfInfo has a matching listen address', () => {
    const model = buildNetmapPoints(
      { peerId: '16Uiu2HAm1Lb', listenAddresses: ['/ip4/159.203.150.8/tcp/4001'] },
      [],
    );
    expect(model.home.kind).toBe('home');
    expect(model.home.city).toBe('THIS NODE');
    expect(model.home.resolved).toBe(true);
    expect(model.home.country).toBe('US');
    expect(model.home.sublabel).toBe('New York, US');
  });

  it('falls back to a hashed HOME placement when selfInfo is null', () => {
    const model = buildNetmapPoints(null, []);
    expect(model.home.resolved).toBe(false);
    expect(model.home.country).toBeNull();
    expect(model.home.sublabel).toBe(NETMAP_UNRESOLVED_TOOLTIP);
  });

  it('falls back to a hashed HOME placement when listenAddresses has no public ip', () => {
    const model = buildNetmapPoints({ peerId: '12D3KooWSelf', listenAddresses: ['/ip4/127.0.0.1/tcp/4001'] }, []);
    expect(model.home.resolved).toBe(false);
    expect(model.home.country).toBeNull();
  });

  it('gives every remote peer kind "peer" — never a fabricated provider/client split', () => {
    const model = buildNetmapPoints(null, [
      peer('12D3KooWAAA', ['/ip4/159.203.150.8/tcp/4001']),
      peer('12D3KooWBBB', ['/ip4/8.8.8.8/tcp/4001']),
    ]);
    expect(model.points.every((p) => p.kind === 'peer')).toBe(true);
  });

  it('resolves a peer whose address matches the vendored table', () => {
    const model = buildNetmapPoints(null, [peer('16Uiu2HAm9oK', ['/ip4/167.172.219.213/tcp/4001'])]);
    const [point] = model.points;
    expect(point.resolved).toBe(true);
    expect(point.country).toBe('US');
    expect(point.sublabel).toBe('San Francisco, US');
    expect(point.ip).toBe('167.172.219.213');
  });

  it('marks a peer with an unlisted public ip as unresolved with an honest tooltip', () => {
    const model = buildNetmapPoints(null, [peer('12D3KooWCCC', ['/ip4/8.8.8.8/tcp/4001'])]);
    const [point] = model.points;
    expect(point.resolved).toBe(false);
    expect(point.country).toBeNull();
    expect(point.sublabel).toBe(NETMAP_UNRESOLVED_TOOLTIP);
    expect(point.ip).toBe('8.8.8.8');
  });

  it('marks a peer with only private/circuit addrs as unresolved with no extracted ip', () => {
    const model = buildNetmapPoints(null, [
      peer('12D3KooWDDD', ['/ip4/192.168.1.5/tcp/4001', '/ip4/159.203.150.8/p2p-circuit/tcp/4001']),
    ]);
    const [point] = model.points;
    expect(point.resolved).toBe(false);
    expect(point.ip).toBeUndefined();
    expect(point.country).toBeNull();
  });

  it('sets city to the truncated peer id for a remote peer', () => {
    const longId = '12D3KooWAbCdEfGh1234567890AbCdEfGh1234567890Ab';
    const model = buildNetmapPoints(null, [peer(longId, [])]);
    expect(model.points[0]!.city).toBe('12D3KooW…7890Ab');
  });

  it('caps plotted peer points at 200', () => {
    const many = Array.from({ length: 250 }, (_, i) => peer(`peer-${i}`, []));
    const model = buildNetmapPoints(null, many);
    expect(model.points.length).toBe(200);
  });

  it('produces an empty points array for an empty peer list', () => {
    const model = buildNetmapPoints(null, []);
    expect(model.points).toEqual([]);
  });

  it('is stable across repeated calls with the same input (deterministic hashing)', () => {
    const peers = [peer('12D3KooWStable', ['/ip4/8.8.8.8/tcp/4001'])];
    const a = buildNetmapPoints(null, peers);
    const b = buildNetmapPoints(null, peers);
    expect(a.points).toEqual(b.points);
    expect(a.home).toEqual(b.home);
  });

  it('never throws on a peer with an empty addrs array', () => {
    expect(() => buildNetmapPoints(null, [peer('12D3KooWNoAddrs', [])])).not.toThrow();
  });
});

// ---------------------------------------------------------------------------
// countResolvedCountries / formatNetmapCountryCount
// ---------------------------------------------------------------------------

function resolvedPoint(country: string | null, resolved = true): NetmapPoint {
  return {
    id: `id-${country ?? 'x'}-${Math.random()}`,
    kind: 'peer',
    lat: 0,
    lon: 0,
    city: 'x',
    sublabel: 'x',
    resolved,
    country,
  };
}

describe('countResolvedCountries', () => {
  it('returns 0 for an empty points array', () => {
    expect(countResolvedCountries([])).toBe(0);
  });

  it('returns 0 when nothing is resolved', () => {
    expect(countResolvedCountries([resolvedPoint(null, false), resolvedPoint(null, false)])).toBe(0);
  });

  it('counts distinct countries once each', () => {
    expect(countResolvedCountries([resolvedPoint('US'), resolvedPoint('US'), resolvedPoint('DE')])).toBe(2);
  });

  it('never counts a resolved:false point even if a country string is somehow present', () => {
    const points: NetmapPoint[] = [{ ...resolvedPoint('US', true), resolved: false }];
    expect(countResolvedCountries(points)).toBe(0);
  });

  it('ignores a resolved:true point with a null country (defensive)', () => {
    expect(countResolvedCountries([resolvedPoint(null, true)])).toBe(0);
  });
});

describe('formatNetmapCountryCount', () => {
  it('renders the honest "—" when zero countries resolved', () => {
    expect(formatNetmapCountryCount([])).toBe('—');
    expect(formatNetmapCountryCount([resolvedPoint(null, false)])).toBe('—');
  });

  it('renders the real distinct-country count as a plain string', () => {
    expect(formatNetmapCountryCount([resolvedPoint('US'), resolvedPoint('GB'), resolvedPoint('US')])).toBe('2');
  });
});

// ---------------------------------------------------------------------------
// netmapPointColor
// ---------------------------------------------------------------------------

describe('netmapPointColor', () => {
  it('returns the THIS NODE legend color for kind "home"', () => {
    expect(netmapPointColor('home')).toBe(NETMAP_HOME_COLOR);
  });

  it('returns the PEERS legend color for kind "peer" and anything else', () => {
    expect(netmapPointColor('peer')).toBe(NETMAP_PEER_COLOR);
    expect(netmapPointColor(undefined)).toBe(NETMAP_PEER_COLOR);
  });
});

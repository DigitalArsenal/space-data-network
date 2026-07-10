/**
 * PEER MAP widget data pipeline (loop task U3.4) — turns the real
 * `GET /api/v1/peers` swarm list (`{"peers":[{"peer_id","addrs"}]}`, no
 * `dn`/trust/standards fields on this build — see `node-data.ts`'s
 * `parseNodePeers` doc comment) into the point set the ported canvas globe
 * (`../../../lib/globe/SdnGlobe.ts`) renders.
 *
 * Honesty rules this file enforces (decision D8 v1 — no runtime GeoIP calls,
 * no CDN tiles, zero external network requests from the UI):
 *
 *   - Every remote peer is classified `kind: 'peer'` — there is no
 *     trust/standards surface to distinguish PROVIDERS/CLIENTS, so this
 *     never fabricates that split (matches `peers-data.ts`'s "every real
 *     peer is honestly `'observed'`" rule).
 *   - Location comes from a SMALL vendored static IP table of individually
 *     DOCUMENTED production hosts (`SDN_NETMAP_GEO_TABLE` below) — never a
 *     guessed cloud-provider CIDR block. A peer whose first public ip4/ip6
 *     address (see `extractPublicIp`) isn't in that table gets a
 *     deterministic peer-id-hashed placement instead (`hashPeerIdToLatLon`)
 *     so the map is stable across renders, rendered dimmer by the engine and
 *     tooltipped "location unresolved" — NEVER a claimed country.
 *   - Private/loopback/link-local/CGNAT/documentation-range ip4, and
 *     loopback/unique-local/link-local ip6, are treated exactly like "no
 *     address" — `extractPublicIp` skips them. `dns4`/`dns6`/`dnsaddr`/`dns`
 *     segments and `/p2p-circuit` reservations (the RELAY's address, not the
 *     peer's own — same rationale as `node-data.ts`'s
 *     `deriveListenAddressRows`) are skipped for the same reason: nothing
 *     honest to resolve without a live DNS/relay lookup, which this UI never
 *     performs.
 */

import type { NodeInfoSnapshot, RawPeer } from './node-data';
import { truncateMiddle } from './node-data';

// ---------------------------------------------------------------------------
// IPv4/IPv6 parsing + private-range classification
// ---------------------------------------------------------------------------

function ip4ToInt(ip: string): number | null {
  const parts = ip.split('.');
  if (parts.length !== 4) return null;
  let value = 0;
  for (const part of parts) {
    if (!/^\d{1,3}$/.test(part)) return null;
    const n = Number(part);
    if (n < 0 || n > 255) return null;
    value = (value << 8) | n;
  }
  return value >>> 0;
}

function isValidIp4(ip: string): boolean {
  return ip4ToInt(ip) !== null;
}

function ip4PrefixBits(cidr: string): number | null {
  const bits = Number(cidr.split('/')[1]);
  return Number.isInteger(bits) && bits >= 0 && bits <= 32 ? bits : null;
}

function ip4InCidr(ip: string, cidr: string): boolean {
  const [base] = cidr.split('/');
  const bits = ip4PrefixBits(cidr);
  if (bits === null || !base) return false;
  const ipInt = ip4ToInt(ip);
  const baseInt = ip4ToInt(base);
  if (ipInt === null || baseInt === null) return false;
  const mask = bits === 0 ? 0 : (0xffffffff << (32 - bits)) >>> 0;
  return (ipInt & mask) === (baseInt & mask);
}

/**
 * RFC1918 private ranges, loopback, link-local, CGNAT (RFC6598), the
 * IETF/RFC5737 documentation ranges (TEST-NET-1/2/3 — never a real peer,
 * but classifying them keeps this list honest rather than silently treating
 * them as public), multicast, and the reserved `240.0.0.0/4` block.
 */
const IP4_PRIVATE_OR_RESERVED_RANGES: readonly string[] = [
  '0.0.0.0/8',
  '10.0.0.0/8',
  '100.64.0.0/10',
  '127.0.0.0/8',
  '169.254.0.0/16',
  '172.16.0.0/12',
  '192.0.0.0/24',
  '192.0.2.0/24',
  '192.168.0.0/16',
  '198.18.0.0/15',
  '198.51.100.0/24',
  '203.0.113.0/24',
  '224.0.0.0/4',
  '240.0.0.0/4',
  '255.255.255.255/32',
];

function isPrivateOrReservedIp4(ip: string): boolean {
  return IP4_PRIVATE_OR_RESERVED_RANGES.some((cidr) => ip4InCidr(ip, cidr));
}

function stripIp6Zone(host: string): string {
  const percent = host.indexOf('%');
  return percent === -1 ? host : host.slice(0, percent);
}

/** Loose but never-throws IPv6 literal check — real multiaddr ip6 segments are always well-formed, this only guards against garbage input. */
function isValidIp6(ip: string): boolean {
  return ip.length > 0 && ip.length <= 45 && ip.includes(':') && /^[0-9a-fA-F:]+$/.test(ip);
}

function isPrivateOrReservedIp6(ip: string): boolean {
  const lower = ip.toLowerCase();
  if (lower === '::1' || lower === '::') return true;
  if (/^fe[89ab][0-9a-f]:/.test(lower)) return true; // fe80::/10 link-local
  if (/^f[cd][0-9a-f]{2}:/.test(lower)) return true; // fc00::/7 unique-local (ULA)
  return false;
}

const IP_SEGMENT_RE = /\/(ip4|ip6)\/([^/]+)/;

/**
 * Extracts the FIRST public ip4/ip6 address from a peer's multiaddr list —
 * skipping `/p2p-circuit` relay reservations, `dns4`/`dns6`/`dnsaddr`/`dns`
 * hostnames (no literal IP to classify), and any private/loopback/
 * link-local/CGNAT/documentation-range literal. Returns `null` (never
 * throws) when nothing usable is found — that peer falls through to the
 * deterministic hash-based placement in `buildNetmapPoints`.
 */
export function extractPublicIp(multiaddrs: readonly string[] | null | undefined): string | null {
  const list = Array.isArray(multiaddrs) ? multiaddrs : [];
  for (const addr of list) {
    if (typeof addr !== 'string') continue;
    if (addr.includes('/p2p-circuit')) continue;
    const match = IP_SEGMENT_RE.exec(addr);
    if (!match) continue;
    const [, family, rawHost] = match;
    if (family === 'ip4') {
      if (isValidIp4(rawHost) && !isPrivateOrReservedIp4(rawHost)) return rawHost;
      continue;
    }
    const host = stripIp6Zone(rawHost);
    if (isValidIp6(host) && !isPrivateOrReservedIp6(host)) return host;
  }
  return null;
}

// ---------------------------------------------------------------------------
// Vendored static geo table (decision D8 v1 — see file doc comment)
// ---------------------------------------------------------------------------

export interface NetmapGeoEntry {
  /** `a.b.c.d/n` for ip4 entries; `addr/128` (exact match only — see `resolveGeo`) for ip6. */
  prefix: string;
  family: 'ip4' | 'ip6';
  lat: number;
  lon: number;
  /** ISO-3166-1 alpha-2 country code. */
  country: string;
  /** Human-readable "City, CC" tooltip text. */
  place: string;
}

/**
 * Every entry here is a SPECIFIC, individually-documented production host
 * from this repo — not a guessed cloud-provider CIDR block. An IP not
 * listed simply falls through to `hashPeerIdToLatLon`'s deterministic
 * fallback rather than a wrong or fabricated location. Sources:
 *
 *   - `docs/benchmarks-wan.md` ("Topology" table): `sdn.spaceaware.io` ==
 *     159.203.150.8 (primary IP 104.131.11.220) on DigitalOcean NYC3;
 *     `celestrak.eth` == 167.172.219.213 on DigitalOcean SFO2.
 *   - `Agents.md` ("Runtime Contract"): confirms both hosts/peer ids as the
 *     two production bootstrap nodes clients seed discovery from.
 *   - `sdn-server/internal/bootstrap/bootstrap.go`: same two hosts wired as
 *     this daemon's default bootstrap multiaddrs.
 *
 * Coordinates are the well-known public city-center coordinates for New
 * York, NY and San Francisco, CA (city-level precision, matching a
 * DigitalOcean *region* — not a per-rack lookup, which this table
 * deliberately doesn't claim to have).
 */
export const SDN_NETMAP_GEO_TABLE: readonly NetmapGeoEntry[] = [
  { prefix: '159.203.150.8/32', family: 'ip4', lat: 40.7128, lon: -74.006, country: 'US', place: 'New York, US' },
  { prefix: '104.131.11.220/32', family: 'ip4', lat: 40.7128, lon: -74.006, country: 'US', place: 'New York, US' },
  {
    prefix: '167.172.219.213/32',
    family: 'ip4',
    lat: 37.7749,
    lon: -122.4194,
    country: 'US',
    place: 'San Francisco, US',
  },
];

function ip6MatchesExact(ip: string, prefix: string): boolean {
  // Only exact (/128) IPv6 entries are supported today — there is no
  // vendored IPv6 range yet, so this keeps the mechanism honest rather than
  // pretending to support arbitrary IPv6 CIDR prefixes it was never tested
  // against.
  const [base, bits] = prefix.split('/');
  if (bits !== '128' || !base) return false;
  return ip.toLowerCase() === base.toLowerCase();
}

export interface NetmapGeoResult {
  lat: number;
  lon: number;
  country: string;
  place: string;
}

/**
 * Looks up `ip` (a plain dotted-quad or IPv6 literal — NOT a multiaddr)
 * against `table` (defaults to the vendored `SDN_NETMAP_GEO_TABLE`),
 * longest-prefix-match first. Returns `null` for a blank `ip` or no table
 * hit — callers must treat that as "unresolved", never guess.
 */
export function resolveGeo(
  ip: string | null | undefined,
  table: readonly NetmapGeoEntry[] = SDN_NETMAP_GEO_TABLE,
): NetmapGeoResult | null {
  if (!ip) return null;
  const family: 'ip4' | 'ip6' = ip.includes(':') ? 'ip6' : 'ip4';
  let best: NetmapGeoEntry | null = null;
  let bestBits = -1;
  for (const entry of table) {
    if (entry.family !== family) continue;
    if (family === 'ip4') {
      const bits = ip4PrefixBits(entry.prefix);
      if (bits !== null && ip4InCidr(ip, entry.prefix) && bits > bestBits) {
        best = entry;
        bestBits = bits;
      }
    } else if (ip6MatchesExact(ip, entry.prefix)) {
      best = entry;
      break; // exact match only — no ambiguity to resolve further
    }
  }
  if (!best) return null;
  return { lat: best.lat, lon: best.lon, country: best.country, place: best.place };
}

// ---------------------------------------------------------------------------
// Deterministic hash fallback (stable across renders — never `Math.random`)
// ---------------------------------------------------------------------------

/** FNV-1a-style 32-bit string hash — same technique as `console.ts`'s `generateQrPlaceholderPattern` multiplier trick, kept deterministic on purpose. */
function hashString(input: string, seed: number): number {
  let h = seed >>> 0;
  for (let i = 0; i < input.length; i += 1) {
    h ^= input.charCodeAt(i);
    h = Math.imul(h, 16777619) >>> 0;
  }
  return h >>> 0;
}

/**
 * Deterministically hashes `peerId` to a repeatable lat/lon — the same peer
 * id always lands at the same point, so the map doesn't jitter across
 * re-renders, but this is NEVER presented as a real location (see
 * `buildNetmapPoints`, which marks these points `resolved:false`).
 */
export function hashPeerIdToLatLon(peerId: string): { lat: number; lon: number } {
  const id = (peerId ?? '').trim() || 'unknown-peer';
  const latHash = hashString(id, 0x811c9dc5);
  const lonHash = hashString(`${id}|lon`, 0x811c9dc5);
  const lat = (latHash % 12_000) / 100 - 60; // deterministic, in [-60, 60)
  const lon = (lonHash % 36_000) / 100 - 180; // deterministic, in [-180, 180)
  return { lat, lon };
}

// ---------------------------------------------------------------------------
// Point model + builder
// ---------------------------------------------------------------------------

export const NETMAP_UNRESOLVED_TOOLTIP = 'location unresolved';

/** Hard cap on plotted peer dots — this widget renders alongside every other NODE dashboard widget, so an unbounded swarm list never turns into an unbounded canvas draw. */
const NETMAP_POINT_CAP = 200;

/** THIS NODE legend color (`#ffd089`) — matches `NodeView.svelte`'s legend dot and the globe engine's own hardcoded home-marker color. */
export const NETMAP_HOME_COLOR = '#ffd089';
/** PEERS legend color (`#9fd4f5`) — matches `NodeView.svelte`'s legend dot. Every remote peer uses this; see the file doc comment for why PROVIDERS/CLIENTS are never fabricated. */
export const NETMAP_PEER_COLOR = '#9fd4f5';

/** `colorFor` callback for the ported globe engine (`SdnGlobeOptions.colorFor`). */
export function netmapPointColor(kind: string | undefined): string {
  return kind === 'home' ? NETMAP_HOME_COLOR : NETMAP_PEER_COLOR;
}

export interface NetmapPoint {
  id: string;
  kind: 'home' | 'peer';
  lat: number;
  lon: number;
  /** Tooltip line 1 (bold) — truncated peer id, or `'THIS NODE'` for home. */
  city: string;
  /** Tooltip line 2 — resolved `'City, CC'` or the honest `'location unresolved'` string. */
  sublabel: string;
  /** First extracted public ip4/ip6 address, when one was found (even if it didn't resolve against the geo table). */
  ip?: string;
  resolved: boolean;
  /** ISO-3166-1 alpha-2 country code. Always `null` when `resolved` is `false` — never a fabricated country. */
  country: string | null;
}

interface Placement {
  lat: number;
  lon: number;
  ip?: string;
  resolved: boolean;
  country: string | null;
  sublabel: string;
}

function placePoint(seedId: string, addrs: readonly string[]): Placement {
  const ip = extractPublicIp(addrs);
  const geo = resolveGeo(ip);
  if (ip && geo) {
    return { lat: geo.lat, lon: geo.lon, ip, resolved: true, country: geo.country, sublabel: geo.place };
  }
  const hashed = hashPeerIdToLatLon(seedId);
  return {
    lat: hashed.lat,
    lon: hashed.lon,
    ip: ip ?? undefined,
    resolved: false,
    country: null,
    sublabel: NETMAP_UNRESOLVED_TOOLTIP,
  };
}

export interface NetmapModel {
  home: NetmapPoint;
  points: NetmapPoint[];
}

/**
 * Builds the PEER MAP widget's full point set: `selfInfo` (this node's
 * `node/info` snapshot — `peerId`/`listenAddresses`, both already parsed by
 * `node-data.ts`'s `parseNodeInfo`) becomes the HOME point via the exact
 * same honest placement pipeline as every other point (real geo table hit,
 * else a stable hashed placement — HOME gets no special-cased fake
 * location). `peers` (the real `/api/v1/peers` list) becomes up to
 * `NETMAP_POINT_CAP` `kind:'peer'` points, capped in list order.
 */
export function buildNetmapPoints(
  selfInfo: Pick<NodeInfoSnapshot, 'peerId' | 'listenAddresses'> | null,
  peers: readonly RawPeer[],
): NetmapModel {
  const selfSeedId = selfInfo?.peerId?.trim() || 'this-node';
  const homePlacement = placePoint(selfSeedId, selfInfo?.listenAddresses ?? []);
  const home: NetmapPoint = {
    id: selfSeedId,
    kind: 'home',
    lat: homePlacement.lat,
    lon: homePlacement.lon,
    city: 'THIS NODE',
    sublabel: homePlacement.sublabel,
    ip: homePlacement.ip,
    resolved: homePlacement.resolved,
    country: homePlacement.country,
  };
  const points: NetmapPoint[] = peers.slice(0, NETMAP_POINT_CAP).map((peer) => {
    const placement = placePoint(peer.peerId, peer.addrs);
    return {
      id: peer.peerId,
      kind: 'peer',
      lat: placement.lat,
      lon: placement.lon,
      city: truncateMiddle(peer.peerId),
      sublabel: placement.sublabel,
      ip: placement.ip,
      resolved: placement.resolved,
      country: placement.country,
    };
  });
  return { home, points };
}

/** Distinct countries among RESOLVED points only (`home` is intentionally excluded by callers — see `NodeView.svelte` — this node's own location isn't a "connected country"). */
export function countResolvedCountries(points: readonly NetmapPoint[]): number {
  const countries = new Set<string>();
  for (const point of points) {
    if (point.resolved && point.country) countries.add(point.country);
  }
  return countries.size;
}

/** PEER MAP caption's `N COUNTRIES` text — the honest `'—'` when nothing has resolved yet. */
export function formatNetmapCountryCount(points: readonly NetmapPoint[]): string {
  const count = countResolvedCountries(points);
  return count > 0 ? String(count) : '—';
}

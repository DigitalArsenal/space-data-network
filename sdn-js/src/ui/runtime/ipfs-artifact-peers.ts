import { normalizeHttpEndpointUrl } from './endpoint-url';

export interface IpfsArtifactPeeringOptions {
  /**
   * Register each artifact peer with Kubo's peering service before dialing it.
   * Kubo 0.43's Bitswap broadcast control only broadcasts wants to peers that
   * previously served blocks, LAN peers and peered peers; a merely connected
   * provider is never asked for a CID and the gateway read times out
   * (node-transfer tests, 2026-09-10: 0/13 connected-only, 13/13 peered).
   * Default: enabled.
   */
  enabled?: boolean;
  /**
   * How long the peering entry is retained after connect before it is removed
   * again; `swarm/peering/add` persists to the Kubo config, so retention is
   * bounded (default 120 s) and callers may release earlier.
   */
  retainMs?: number;
}

export interface IpfsArtifactPeerConnectOptions {
  ipfsApiUrl?: string | null;
  artifactPeerAddrs?: unknown;
  timeoutMs?: number;
  fetch?: typeof fetch;
  peering?: IpfsArtifactPeeringOptions;
}

export interface IpfsArtifactPeerConnectSummary {
  attempted: number;
  connected: number;
  failed: number;
  /** Peer ids registered with the peering service for this connect. */
  peered?: string[];
  /** Remove the bounded peering entries now instead of at the retention deadline. */
  release?: () => Promise<void>;
}

export interface IpfsArtifactProviderConnectOptions extends Omit<IpfsArtifactPeerConnectOptions, 'artifactPeerAddrs'> {
  cids?: unknown;
  numProviders?: number;
}

export interface IpfsArtifactProviderConnectSummary extends IpfsArtifactPeerConnectSummary {
  discovered: number;
}

const DEFAULT_CONNECT_TIMEOUT_MS = 5000;
const DEFAULT_PEERING_RETAIN_MS = 120_000;
const DEFAULT_PROVIDER_DISCOVERY_COUNT = 20;
const TRUSTED_ARTIFACT_SEED_LEVELS = new Set([
  'marginal',
  'limited',
  'full',
  'ultimate',
  'trusted',
  'admin',
  'configured',
]);

export function normalizeIpfsArtifactPeerAddrs(value: unknown): string[] {
  const rawValues = Array.isArray(value)
    ? value
    : typeof value === 'string'
      ? value.split(',')
      : [];
  const normalized: string[] = [];
  const seen = new Set<string>();
  for (const raw of rawValues) {
    if (typeof raw !== 'string') continue;
    const addr = raw.trim();
    if (!addr || seen.has(addr)) continue;
    seen.add(addr);
    normalized.push(addr);
  }
  return normalized;
}

export function prioritizeIpfsArtifactPeerAddrs(primary: unknown, candidates: unknown): string[] {
  return normalizeIpfsArtifactPeerAddrs([
    ...normalizeIpfsArtifactPeerAddrs(primary),
    ...normalizeIpfsArtifactPeerAddrs(candidates),
  ]);
}

export function artifactPeerAddrsForTrustedPeers(value: unknown): string[] {
  const peers = Array.isArray(value) ? value : [];
  return normalizeIpfsArtifactPeerAddrs(peers.flatMap((peer) => {
    const record = asRecord(peer);
    if (!record || !isTrustedArtifactSeedPeer(record)) return [];
    const metadata = asRecord(record.metadata) ?? {};
    return normalizeIpfsArtifactPeerAddrs(
      artifactAddrValue(record) ?? artifactAddrValue(metadata),
    );
  }));
}

export async function connectIpfsArtifactPeers(options: IpfsArtifactPeerConnectOptions): Promise<IpfsArtifactPeerConnectSummary> {
  const apiBase = normalizeApiBase(options.ipfsApiUrl);
  const artifactPeerAddrs = normalizeIpfsArtifactPeerAddrs(options.artifactPeerAddrs);
  if (!apiBase || artifactPeerAddrs.length === 0) {
    return { attempted: 0, connected: 0, failed: 0 };
  }
  const fetchLike = options.fetch ?? globalThis.fetch;
  if (typeof fetchLike !== 'function') {
    return { attempted: 0, connected: 0, failed: artifactPeerAddrs.length };
  }

  const timeoutMs = Math.max(1, Math.floor(options.timeoutMs ?? DEFAULT_CONNECT_TIMEOUT_MS));
  const peeringEnabled = options.peering?.enabled !== false;
  const retainMs = Math.max(1, Math.floor(options.peering?.retainMs ?? DEFAULT_PEERING_RETAIN_MS));
  const peered: string[] = [];

  let connected = 0;
  let failed = 0;
  for (const addr of artifactPeerAddrs) {
    if (peeringEnabled) {
      const peerId = peerIdFromMultiaddr(addr);
      if (peerId) {
        const peering = new URL(`${apiBase}/api/v0/swarm/peering/add`);
        peering.searchParams.set('arg', addr);
        try {
          const response = await fetchWithTimeout(fetchLike, peering.toString(), { method: 'POST' }, timeoutMs, `IPFS peering add ${addr}`);
          if (response.ok) peered.push(peerId);
        } catch {
          // A provider that cannot be peered is still dialed below; the
          // broadcast-control limitation then applies to it alone.
        }
      }
    }
    const url = new URL(`${apiBase}/api/v0/swarm/connect`);
    url.searchParams.set('arg', addr);
    url.searchParams.set('timeout', `${timeoutMs}ms`);
    try {
      const response = await fetchWithTimeout(
        fetchLike,
        url.toString(),
        { method: 'POST' },
        timeoutMs,
        `IPFS swarm connect ${addr}`,
      );
      if (response.ok) {
        connected += 1;
      } else {
        failed += 1;
      }
    } catch {
      failed += 1;
    }
  }

  const summary: IpfsArtifactPeerConnectSummary = {
    attempted: artifactPeerAddrs.length,
    connected,
    failed,
  };
  if (peered.length > 0) {
    let released = false;
    const release = async () => {
      if (released) return;
      released = true;
      clearTimeout(timer);
      for (const peerId of peered) {
        const removal = new URL(`${apiBase}/api/v0/swarm/peering/rm`);
        removal.searchParams.set('arg', peerId);
        try {
          await fetchWithTimeout(fetchLike, removal.toString(), { method: 'POST' }, timeoutMs, `IPFS peering rm ${peerId}`);
        } catch {
          // Best effort: the entry is bounded by the next connect's release.
        }
      }
    };
    const timer = setTimeout(() => { void release(); }, retainMs);
    (timer as { unref?: () => void }).unref?.();
    summary.peered = [...peered];
    summary.release = release;
  }
  return summary;
}

/** Peer id from a multiaddr's trailing /p2p/<id> (or legacy /ipfs/<id>). */
export function peerIdFromMultiaddr(addr: string): string | null {
  const match = /\/(?:p2p|ipfs)\/([^/]+)\/?$/.exec(addr.trim());
  return match ? match[1] : null;
}

export async function connectIpfsArtifactProviders(options: IpfsArtifactProviderConnectOptions): Promise<IpfsArtifactProviderConnectSummary> {
  const apiBase = normalizeApiBase(options.ipfsApiUrl);
  const cids = normalizeCidValues(options.cids);
  if (!apiBase || cids.length === 0) {
    return { attempted: 0, connected: 0, failed: 0, discovered: 0 };
  }
  const fetchLike = options.fetch ?? globalThis.fetch;
  if (typeof fetchLike !== 'function') {
    return { attempted: 0, connected: 0, failed: 0, discovered: 0 };
  }

  const providerAddrs: string[] = [];
  const seen = new Set<string>();
  const discovered = await Promise.all(cids.map(async (cid) => {
    const url = new URL(`${apiBase}/api/v0/routing/findprovs`);
    url.searchParams.set('arg', cid);
    url.searchParams.set('num-providers', String(normalizeProviderCount(options.numProviders)));
    try {
      const response = await fetchWithTimeout(
        fetchLike,
        url.toString(),
        { method: 'POST' },
        Math.max(1, Math.floor(options.timeoutMs ?? DEFAULT_CONNECT_TIMEOUT_MS)),
        `IPFS provider discovery ${cid}`,
      );
      if (!response.ok) return [];
      const payload = await readResponseTextWithTimeout(
        response,
        Math.max(1, Math.floor(options.timeoutMs ?? DEFAULT_CONNECT_TIMEOUT_MS)),
        `IPFS provider discovery ${cid}`,
      );
      return providerAddrsFromFindProvidersPayload(payload);
    } catch {
      return [];
    }
  }));

  for (const addr of discovered.flat()) {
    if (seen.has(addr)) continue;
    seen.add(addr);
    providerAddrs.push(addr);
  }

  const summary = await connectIpfsArtifactPeers({
    ipfsApiUrl: apiBase,
    artifactPeerAddrs: providerAddrs,
    timeoutMs: options.timeoutMs,
    fetch: fetchLike,
  });
  return {
    ...summary,
    discovered: providerAddrs.length,
  };
}

async function readResponseTextWithTimeout(response: Response, timeoutMs: number, label: string): Promise<string> {
  if (!response.body) {
    return await response.text();
  }
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let timeout: ReturnType<typeof setTimeout> | null = null;
  let timedOut = false;
  const request = (async () => {
    let text = '';
    for (;;) {
      const chunk = await reader.read();
      if (chunk.done) break;
      if (chunk.value) text += decoder.decode(chunk.value, { stream: true });
    }
    text += decoder.decode();
    return text;
  })();
  const timer = new Promise<never>((_, reject) => {
    timeout = setTimeout(() => {
      timedOut = true;
      const error = new Error(`${label} timed out after ${timeoutMs} ms`);
      reader.cancel(error).catch(() => undefined);
      reject(error);
    }, timeoutMs);
  });
  try {
    return await Promise.race([request, timer]);
  } finally {
    if (timeout) clearTimeout(timeout);
    if (timedOut) request.catch(() => undefined);
    try {
      reader.releaseLock();
    } catch {
      // The reader may still be settling after cancellation.
    }
  }
}

async function fetchWithTimeout(
  fetchLike: typeof fetch,
  url: string,
  init: RequestInit,
  timeoutMs: number,
  label: string,
): Promise<Response> {
  const controller = new AbortController();
  let timeout: ReturnType<typeof setTimeout> | null = null;
  let timedOut = false;
  const request = fetchLike(url, { ...init, signal: controller.signal });
  const timer = new Promise<never>((_, reject) => {
    timeout = setTimeout(() => {
      timedOut = true;
      const error = new Error(`${label} timed out after ${timeoutMs} ms`);
      controller.abort(error);
      reject(error);
    }, timeoutMs);
  });
  try {
    return await Promise.race([request, timer]);
  } finally {
    if (timeout) clearTimeout(timeout);
    if (timedOut) request.catch(() => undefined);
  }
}

function normalizeApiBase(value: string | null | undefined): string | null {
  return normalizeHttpEndpointUrl(value);
}

function normalizeCidValues(value: unknown): string[] {
  const rawValues = Array.isArray(value)
    ? value
    : typeof value === 'string'
      ? value.split(',')
      : [];
  const normalized: string[] = [];
  const seen = new Set<string>();
  for (const raw of rawValues) {
    if (typeof raw !== 'string') continue;
    const cid = raw.trim();
    if (!cid || seen.has(cid)) continue;
    seen.add(cid);
    normalized.push(cid);
  }
  return normalized;
}

function normalizeProviderCount(value: number | undefined): number {
  const numeric = Math.floor(Number(value ?? DEFAULT_PROVIDER_DISCOVERY_COUNT));
  return Number.isFinite(numeric) && numeric > 0 ? numeric : DEFAULT_PROVIDER_DISCOVERY_COUNT;
}

function providerAddrsFromFindProvidersPayload(text: string): string[] {
  const records = parseFindProvidersRecords(text);
  const normalized: string[] = [];
  const seen = new Set<string>();
  for (const record of records) {
    for (const addr of providerAddrsFromFindProvidersRecord(record)) {
      if (seen.has(addr)) continue;
      seen.add(addr);
      normalized.push(addr);
    }
  }
  return normalized;
}

function parseFindProvidersRecords(text: string): unknown[] {
  const trimmed = text.trim();
  if (!trimmed) return [];
  const lineRecords: unknown[] = [];
  for (const line of trimmed.split(/\r?\n/)) {
    const lineText = line.trim();
    if (!lineText) continue;
    try {
      lineRecords.push(JSON.parse(lineText) as unknown);
    } catch {
      continue;
    }
  }
  if (lineRecords.length > 0) return flattenRecordValues(lineRecords);
  try {
    return flattenRecordValues([JSON.parse(trimmed) as unknown]);
  } catch {
    return [];
  }
}

function flattenRecordValues(records: unknown[]): unknown[] {
  const flattened: unknown[] = [];
  for (const record of records) {
    if (Array.isArray(record)) {
      flattened.push(...flattenRecordValues(record));
    } else {
      flattened.push(record);
    }
  }
  return flattened;
}

function providerAddrsFromFindProvidersRecord(record: unknown): string[] {
  const root = asRecord(record);
  if (!root) return [];
  const responses = arrayValue(root, ['Responses', 'responses', 'Providers', 'providers']);
  const peerRecords = responses.length > 0 ? responses : [root];
  const normalized: string[] = [];
  for (const peerRecord of peerRecords) {
    const peer = asRecord(peerRecord);
    if (!peer) continue;
    const peerId = stringValue(peer, ['ID', 'Id', 'id', 'Peer', 'peer', 'PeerID', 'peerId']);
    const addrs = arrayValue(peer, ['Addrs', 'addrs', 'Multiaddrs', 'multiaddrs', 'Addresses', 'addresses']);
    for (const rawAddr of addrs) {
      const addr = stringFromMultiaddrValue(rawAddr);
      if (!addr) continue;
      normalized.push(appendPeerIdToMultiaddr(addr, peerId));
    }
  }
  return normalized;
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return value && typeof value === 'object' && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null;
}

function arrayValue(record: Record<string, unknown>, keys: string[]): unknown[] {
  for (const key of keys) {
    const value = record[key];
    if (Array.isArray(value)) return value;
  }
  return [];
}

function stringValue(record: Record<string, unknown>, keys: string[]): string | null {
  for (const key of keys) {
    const value = record[key];
    if (typeof value === 'string' && value.trim()) return value.trim();
  }
  return null;
}

function stringFromMultiaddrValue(value: unknown): string | null {
  if (typeof value === 'string') {
    const trimmed = value.trim();
    return trimmed || null;
  }
  const record = asRecord(value);
  if (!record) return null;
  return stringValue(record, ['String', 'string', 'multiaddr', 'Multiaddr', '/']);
}

function appendPeerIdToMultiaddr(addr: string, peerId: string | null): string {
  if (!peerId || addr.includes('/p2p/') || addr.includes('/ipfs/')) return addr;
  return `${addr.replace(/\/+$/, '')}/p2p/${peerId}`;
}

function isTrustedArtifactSeedPeer(record: Record<string, unknown>): boolean {
  const metadata = asRecord(record.metadata) ?? {};
  const trustLevel = stringValue(record, ['trustLevel', 'trust_level', 'trust'])
    ?? stringValue(metadata, ['trustLevel', 'trust_level', 'trust']);
  return TRUSTED_ARTIFACT_SEED_LEVELS.has((trustLevel ?? '').trim().toLowerCase());
}

function artifactAddrValue(record: Record<string, unknown>): unknown {
  return record.ipfs_artifact_addrs
    ?? record.ipfsArtifactAddrs
    ?? record.artifact_peer_addrs
    ?? record.artifactPeerAddrs
    ?? record.artifact_addrs
    ?? record.artifactAddrs;
}

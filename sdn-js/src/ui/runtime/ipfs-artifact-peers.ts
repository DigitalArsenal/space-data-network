import { normalizeHttpEndpointUrl } from './endpoint-url';

export interface IpfsArtifactPeerConnectOptions {
  ipfsApiUrl?: string | null;
  artifactPeerAddrs?: unknown;
  timeoutMs?: number;
  fetch?: typeof fetch;
}

export interface IpfsArtifactPeerConnectSummary {
  attempted: number;
  connected: number;
  failed: number;
}

export interface IpfsArtifactProviderConnectOptions extends Omit<IpfsArtifactPeerConnectOptions, 'artifactPeerAddrs'> {
  cids?: unknown;
  numProviders?: number;
}

export interface IpfsArtifactProviderConnectSummary extends IpfsArtifactPeerConnectSummary {
  discovered: number;
}

const DEFAULT_CONNECT_TIMEOUT_MS = 5000;
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

  let connected = 0;
  let failed = 0;
  for (const addr of artifactPeerAddrs) {
    const url = new URL(`${apiBase}/api/v0/swarm/connect`);
    url.searchParams.set('arg', addr);
    url.searchParams.set('timeout', `${Math.max(1, Math.floor(options.timeoutMs ?? DEFAULT_CONNECT_TIMEOUT_MS))}ms`);
    try {
      const timeoutMs = Math.max(1, Math.floor(options.timeoutMs ?? DEFAULT_CONNECT_TIMEOUT_MS));
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

  return {
    attempted: artifactPeerAddrs.length,
    connected,
    failed,
  };
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
  for (const cid of cids) {
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
      if (!response.ok) continue;
      const payload = await readResponseTextWithTimeout(
        response,
        Math.max(1, Math.floor(options.timeoutMs ?? DEFAULT_CONNECT_TIMEOUT_MS)),
        `IPFS provider discovery ${cid}`,
      );
      for (const addr of providerAddrsFromFindProvidersPayload(payload)) {
        if (seen.has(addr)) continue;
        seen.add(addr);
        providerAddrs.push(addr);
      }
    } catch {
      continue;
    }
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
  let timeout: ReturnType<typeof setTimeout> | null = null;
  let timedOut = false;
  const request = response.text();
  const timer = new Promise<never>((_, reject) => {
    timeout = setTimeout(() => {
      timedOut = true;
      reject(new Error(`${label} timed out after ${timeoutMs} ms`));
    }, timeoutMs);
  });
  try {
    return await Promise.race([request, timer]);
  } finally {
    if (timeout) clearTimeout(timeout);
    if (timedOut) request.catch(() => undefined);
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

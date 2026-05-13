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

const DEFAULT_CONNECT_TIMEOUT_MS = 5000;

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
      const response = await fetchLike(url.toString(), { method: 'POST' });
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

function normalizeApiBase(value: string | null | undefined): string | null {
  const trimmed = value?.trim();
  if (!trimmed) return null;
  return trimmed.replace(/\/+$/, '');
}

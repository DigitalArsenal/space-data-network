/**
 * Node Resolver — multi-identifier resolution for SDN nodes.
 *
 * Resolves various identifier formats to a usable node endpoint:
 * - PeerID (12D3KooW...)
 * - .onion address
 * - IPFS CID (bafy... / Qm...)
 * - IPNS name (k51...)
 * - HTTP(S) URL
 * - Multiaddr (/ip4/... /dns4/...)
 * - ENS name (*.eth)
 * - EPM FlatBuffer bytes
 */

import type { ParsedEPM } from './epm-resolver';

/** Identifier types the resolver can detect. */
export type IdentifierType =
  | 'peerId'
  | 'onion'
  | 'cid'
  | 'ipns'
  | 'ens'
  | 'http'
  | 'multiaddr'
  | 'epm';

/** Result of resolving a node identifier. */
export interface ResolvedNode {
  /** Libp2p PeerID (12D3KooW...) if known. */
  peerId?: string;
  /** HTTP(S) base URL for the node's API (no trailing slash). */
  httpUrl?: string;
  /** Multiaddrs discovered for the node. */
  multiaddrs?: string[];
  /** Parsed EPM if available. */
  epm?: ParsedEPM;
  /** What kind of identifier was provided. */
  identifierType: IdentifierType;
}

/** Options for resolveNode(). */
export interface ResolveOptions {
  /** IPFS HTTP gateway URL for CID/IPNS resolution (default: https://dweb.link). */
  ipfsGateway?: string;
  /** Timeout in ms for HTTP fetches (default: 10000). */
  timeout?: number;
}

const DEFAULT_IPFS_GATEWAY = 'https://dweb.link';
const DEFAULT_TIMEOUT = 10_000;

/**
 * Detect the type of a node identifier string.
 */
export function detectIdentifierType(id: string): IdentifierType {
  const trimmed = id.trim();

  // PeerID: base58 starting with 12D3KooW (Ed25519) or Qm (RSA legacy)
  if (/^12D3KooW[a-zA-Z0-9]{44,}$/.test(trimmed)) return 'peerId';

  // .onion address
  if (/\.onion(:\d+)?$/i.test(trimmed)) return 'onion';

  // HTTP(S) URL
  if (/^https?:\/\//i.test(trimmed)) return 'http';

  // Multiaddr
  if (trimmed.startsWith('/ip4/') || trimmed.startsWith('/ip6/') ||
      trimmed.startsWith('/dns4/') || trimmed.startsWith('/dns6/') ||
      trimmed.startsWith('/dnsaddr/')) return 'multiaddr';

  // IPFS CID v1 (bafy...) or v0 (Qm...)
  if (/^bafy[a-z2-7]{50,}$/i.test(trimmed) || /^Qm[a-zA-Z0-9]{44}$/.test(trimmed)) return 'cid';

  // IPNS key (k51...)
  if (/^k51[a-z0-9]{50,}$/i.test(trimmed) || trimmed.startsWith('/ipns/')) return 'ipns';

  // ENS name
  if (/\.eth$/i.test(trimmed)) return 'ens';

  // Fallback: assume it's an HTTP URL if it looks like a domain
  if (/^[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}(:\d+)?$/.test(trimmed)) return 'http';

  return 'http';
}

/**
 * Resolve a node identifier to connection details.
 *
 * @param id - A PeerID, .onion address, CID, IPNS name, HTTP URL, multiaddr, or ENS name.
 * @param opts - Resolution options.
 * @returns Resolved node details including HTTP URL and/or multiaddrs.
 */
export async function resolveNode(
  id: string,
  opts?: ResolveOptions,
): Promise<ResolvedNode> {
  const trimmed = id.trim();
  const type = detectIdentifierType(trimmed);

  switch (type) {
    case 'peerId':
      return { peerId: trimmed, identifierType: 'peerId' };

    case 'onion':
      return resolveOnion(trimmed);

    case 'http':
      return resolveHttp(trimmed);

    case 'multiaddr':
      return resolveMultiaddr(trimmed);

    case 'cid':
      return resolveCID(trimmed, opts);

    case 'ipns':
      return resolveIPNS(trimmed, opts);

    case 'ens':
      return resolveENS(trimmed, opts);

    default:
      return { httpUrl: ensureProtocol(trimmed), identifierType: type };
  }
}

function resolveOnion(addr: string): ResolvedNode {
  // Strip any protocol prefix
  let host = addr.replace(/^https?:\/\//, '');
  // Ensure .onion gets http:// (tor proxy handles it)
  const url = `http://${host}`;
  return { httpUrl: url, identifierType: 'onion' };
}

function resolveHttp(url: string): ResolvedNode {
  return { httpUrl: ensureProtocol(url).replace(/\/+$/, ''), identifierType: 'http' };
}

function resolveMultiaddr(ma: string): ResolvedNode {
  // Extract host and port from multiaddr for HTTP
  const parts = ma.split('/').filter(Boolean);

  let host: string | undefined;
  let port: string | undefined;
  let peerId: string | undefined;

  for (let i = 0; i < parts.length; i++) {
    if ((parts[i] === 'ip4' || parts[i] === 'ip6' || parts[i] === 'dns4' || parts[i] === 'dns6' || parts[i] === 'dnsaddr') && parts[i + 1]) {
      host = parts[i + 1];
    }
    if ((parts[i] === 'tcp' || parts[i] === 'udp') && parts[i + 1]) {
      port = parts[i + 1];
    }
    if (parts[i] === 'p2p' && parts[i + 1]) {
      peerId = parts[i + 1];
    }
  }

  const result: ResolvedNode = {
    multiaddrs: [ma],
    identifierType: 'multiaddr',
  };

  if (peerId) result.peerId = peerId;

  if (host) {
    const scheme = port === '443' ? 'https' : 'http';
    result.httpUrl = port ? `${scheme}://${host}:${port}` : `https://${host}`;
  }

  return result;
}

async function resolveCID(
  cid: string,
  opts?: ResolveOptions,
): Promise<ResolvedNode> {
  // CID could be an EPM binary — try to fetch from IPFS gateway
  const gateway = opts?.ipfsGateway || DEFAULT_IPFS_GATEWAY;
  const timeout = opts?.timeout || DEFAULT_TIMEOUT;

  try {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), timeout);

    const resp = await fetch(`${gateway}/ipfs/${cid}`, {
      signal: controller.signal,
      headers: { Accept: 'application/json, application/octet-stream' },
    });
    clearTimeout(timer);

    if (resp.ok) {
      const contentType = resp.headers.get('content-type') || '';

      // If JSON, might be node info
      if (contentType.includes('json')) {
        const json = await resp.json() as Record<string, unknown>;
        const result: ResolvedNode = { identifierType: 'cid' };
        if (json.peer_id) result.peerId = json.peer_id as string;
        if (json.listen_addresses) result.multiaddrs = json.listen_addresses as string[];
        return result;
      }
    }
  } catch {
    // Gateway fetch failed — return CID-only result
  }

  return { identifierType: 'cid' };
}

async function resolveIPNS(
  name: string,
  opts?: ResolveOptions,
): Promise<ResolvedNode> {
  const ipnsName = name.startsWith('/ipns/') ? name.replace('/ipns/', '') : name;
  const gateway = opts?.ipfsGateway || DEFAULT_IPFS_GATEWAY;
  const timeout = opts?.timeout || DEFAULT_TIMEOUT;

  try {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), timeout);

    const resp = await fetch(`${gateway}/ipns/${ipnsName}`, {
      signal: controller.signal,
      headers: { Accept: 'application/json' },
    });
    clearTimeout(timer);

    if (resp.ok) {
      const json = await resp.json() as Record<string, unknown>;
      const result: ResolvedNode = { identifierType: 'ipns' };
      if (json.peer_id) result.peerId = json.peer_id as string;
      if (json.listen_addresses) result.multiaddrs = json.listen_addresses as string[];
      return result;
    }
  } catch {
    // IPNS resolution failed
  }

  return { identifierType: 'ipns' };
}

async function resolveENS(
  name: string,
  opts?: ResolveOptions,
): Promise<ResolvedNode> {
  const timeout = opts?.timeout || DEFAULT_TIMEOUT;

  // Try public ENS content-hash resolution
  try {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), timeout);

    // Use eth.limo as a gateway to resolve ENS content hashes
    const resp = await fetch(`https://${name}.limo`, {
      signal: controller.signal,
      method: 'HEAD',
      redirect: 'manual',
    });
    clearTimeout(timer);

    // eth.limo resolves ENS → IPFS content hash; the redirect location has the CID
    const location = resp.headers.get('location');
    if (location) {
      return { httpUrl: location, identifierType: 'ens' };
    }

    // If 200, the ENS name resolved to a website
    if (resp.ok) {
      return { httpUrl: `https://${name}.limo`, identifierType: 'ens' };
    }
  } catch {
    // ENS resolution failed
  }

  return { identifierType: 'ens' };
}

function ensureProtocol(url: string): string {
  if (/^https?:\/\//i.test(url)) return url;
  return `https://${url}`;
}

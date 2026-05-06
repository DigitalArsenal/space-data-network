const DEFAULT_STALE_MS = 10_000;
const DEFAULT_KUBO_API_BASE_URL = 'http://127.0.0.1:5001';
const SDN_PROTOCOL_PREFIXES = ['/space-data-network/', '/spacedatanetwork/'];

/**
 * Normalize a trusted SDN peer into the minimal shape expected by the upstream
 * peers bundles.
 *
 * @param {any} peer
 * @returns {any}
 */
export function normalizeTrustedPeerToSwarmPeer (peer) {
  const addrs = Array.isArray(peer?.addrs) ? peer.addrs.filter(Boolean) : [];
  const protocols = splitProtocols(peer?.metadata?.protocols);
  const agentVersion = inferAgentVersion(peer?.metadata);

  return {
    peer: String(peer?.id ?? '').trim(),
    addr: addrs[0] ?? '',
    latency: null,
    direction: null,
    streams: protocols.map((protocol) => ({ protocol })),
    identify: agentVersion ? { AgentVersion: agentVersion } : undefined,
    sdnSources: ['registry'],
    sdnMeta: {
      trustLevel: stringOrNull(peer?.trust_level),
      name: stringOrNull(peer?.name),
      organization: stringOrNull(peer?.organization),
    },
  };
}

/**
 * Format an SDN multiaddr into the short upstream "connection" label used by
 * the WebUI peers table.
 *
 * @param {string} address
 * @returns {string}
 */
export function formatPeerConnection (address) {
  const parts = String(address ?? '').split('/').filter(Boolean);
  for (const preferred of ['wss', 'ws', 'webrtc', 'webtransport', 'quic-v1', 'quic', 'tcp', 'udp']) {
    if (parts.includes(preferred)) {
      return preferred;
    }
  }
  return parts[parts.length - 1] ?? '';
}

/**
 * Project SDN trusted peers into the exact row shape consumed by the upstream
 * peers table selector.
 *
 * @param {any[]} peers
 * @returns {any[]}
 */
export function trustedPeerListToPeerLocationsForSwarm (peers) {
  return (Array.isArray(peers) ? peers : [])
    .map(normalizeTrustedPeerToSwarmPeer)
    .filter((peer) => peer.peer)
    .map((peer) => ({
      peerId: peer.peer,
      flagCode: null,
      location: inferPeerLocation(peer.addr),
      coordinates: null,
      connection: formatPeerConnection(peer.addr),
      address: peer.addr,
      latency: null,
      direction: peer.direction ?? null,
      isPrivate: isPrivateAddress(peer.addr),
      isNearby: false,
      protocols: formatProtocols(peer.streams),
      agentVersion: peer.identify?.AgentVersion ?? null,
    }));
}

/**
 * Project the live peer graph plus registry metadata into the trusted peer
 * shape expected by the upstream peer selectors. This keeps `/` scoped to live
 * SDN peers while excluding generic IPFS-only connections.
 *
 * @param {any} snapshot
 * @param {any[]} registryPeers
 * @returns {any[]}
 */
export function buildObservedSdnPeers (snapshot, registryPeers = []) {
  const localPeerId = stringOrNull(snapshot?.local_peer_id);
  const edgeProtocols = buildEdgeProtocolMap(snapshot?.edges, localPeerId);
  const registryById = new Map(
    (Array.isArray(registryPeers) ? registryPeers : [])
      .map((peer) => [stringOrNull(peer?.id), peer])
      .filter(([peerId]) => peerId),
  );

  return (Array.isArray(snapshot?.nodes) ? snapshot.nodes : [])
    .map((node) => {
      const peerId = stringOrNull(node?.peer_id);
      if (!peerId || peerId === localPeerId || !node?.is_online) {
        return null;
      }

      const registryPeer = registryById.get(peerId);
      const protocols = uniqueStrings([
        ...splitProtocols(registryPeer?.metadata?.protocols),
        ...(edgeProtocols.get(peerId) ?? []),
      ]);
      const trustLevel = stringOrNull(node?.trust_level) ?? stringOrNull(registryPeer?.trust_level);
      const name = stringOrNull(node?.dn) ?? stringOrNull(registryPeer?.name);
      const organization = stringOrNull(node?.organization) ?? stringOrNull(registryPeer?.organization);

      if (!isObservedSdnPeer({ trustLevel, name, organization, protocols })) {
        return null;
      }

      const addrs = uniqueStrings([
        ...asStringArray(node?.multiformat_address),
        ...asStringArray(registryPeer?.addrs),
      ]);
      const metadata = {};
      const agentVersion = inferAgentVersion(registryPeer?.metadata);
      if (agentVersion) {
        metadata.agent_version = agentVersion;
      }
      if (protocols.length > 0) {
        metadata.protocols = protocols.join(',');
      }

      const peer = { id: peerId };
      if (addrs.length > 0) {
        peer.addrs = addrs;
      }
      if (trustLevel) {
        peer.trust_level = trustLevel;
      }
      if (name) {
        peer.name = name;
      }
      if (organization) {
        peer.organization = organization;
      }
      if (Object.keys(metadata).length > 0) {
        peer.metadata = metadata;
      }

      return peer;
    })
    .filter(Boolean);
}

/**
 * Fetch live SDN peers from a hosted node. Prefer the observed peer graph and
 * fall back to the registry for older servers that do not expose it yet.
 *
 * @param {{ baseUrl?: string | null, fetchImpl?: typeof fetch }} [options]
 * @returns {{ listPeers: () => Promise<any[]>, staleMs: number }}
 */
export function createHostedRegistryPeerSource (options = {}) {
  const baseUrl = normalizeBaseUrl(options.baseUrl ?? inferCurrentBaseUrl());
  const kuboApiBaseUrl = normalizeBaseUrl(options.kuboApiBaseUrl ?? inferKuboApiBaseUrl());
  const fetchImpl = options.fetchImpl ?? globalThis.fetch?.bind(globalThis);

  return {
    staleMs: DEFAULT_STALE_MS,
    async listPeers () {
      if (!baseUrl || typeof fetchImpl !== 'function') {
        return [];
      }

      const observedSdnResponse = await fetchImpl(`${baseUrl}/api/peers/sdn`);
      if (observedSdnResponse.ok) {
        return await observedSdnResponse.json();
      }
      if (observedSdnResponse.status !== 404) {
        throw new Error(`failed to fetch observed SDN peers: ${observedSdnResponse.status}`);
      }

      const graphResponse = await fetchImpl(`${baseUrl}/api/peers/graph`);
      if (graphResponse.ok) {
        const [snapshot, registryPeers] = await Promise.all([
          graphResponse.json(),
          fetchRegistryPeers(baseUrl, fetchImpl).catch(() => []),
        ]);
        return buildObservedSdnPeers(snapshot, registryPeers);
      }
      if (graphResponse.status !== 404) {
        throw new Error(`failed to fetch SDN peer graph: ${graphResponse.status}`);
      }

      const registryPeers = await fetchRegistryPeers(baseUrl, fetchImpl).catch((error) => {
        if (error?.status !== 404) {
          throw error;
        }
        return null;
      });
      if (registryPeers) {
        return registryPeers;
      }

      const configuredDesktopPeers = await fetchConfiguredDesktopSdnNodes(baseUrl, fetchImpl).catch(() => []);
      if (configuredDesktopPeers.length > 0) {
        return configuredDesktopPeers;
      }

      return fetchKuboSdnPeers(kuboApiBaseUrl, fetchImpl);
    }
  };
}

function splitProtocols (value) {
  if (Array.isArray(value)) {
    return value
      .map((entry) => String(entry ?? '').trim())
      .filter(Boolean);
  }
  return String(value ?? '')
    .split(',')
    .map((entry) => entry.trim())
    .filter(Boolean);
}

function inferAgentVersion (metadata) {
  const explicit = stringOrNull(metadata?.agent_version);
  if (explicit) {
    return explicit;
  }

  return splitProtocols(metadata?.advertisement_flags)[0] ?? null;
}

function isSdnAgentVersion (agentVersion) {
  const value = String(agentVersion ?? '').toLowerCase();
  return value.includes('spacedatanetwork') || value.includes('space-data-network') || value.startsWith('sdn');
}

function buildEdgeProtocolMap (edges, localPeerId) {
  const protocolsByPeer = new Map();

  for (const edge of Array.isArray(edges) ? edges : []) {
    const sourcePeerId = stringOrNull(edge?.source_peer_id);
    const targetPeerId = stringOrNull(edge?.target_peer_id);
    if (!targetPeerId || sourcePeerId !== localPeerId) {
      continue;
    }

    const protocols = uniqueStrings(edge?.protocols);
    if (protocols.length === 0) {
      continue;
    }

    const existing = protocolsByPeer.get(targetPeerId) ?? [];
    protocolsByPeer.set(targetPeerId, uniqueStrings([...existing, ...protocols]));
  }

  return protocolsByPeer;
}

function isObservedSdnPeer ({ trustLevel, name, organization, protocols }) {
  return Boolean(trustLevel || name || organization || protocols.some(isSdnProtocol));
}

function isSdnProtocol (protocol) {
  return SDN_PROTOCOL_PREFIXES.some((prefix) => String(protocol ?? '').startsWith(prefix));
}

function formatProtocols (streams) {
  const protocols = Array.isArray(streams)
    ? streams
      .map((entry) => String(entry?.protocol ?? '').trim())
      .filter(Boolean)
    : [];
  return protocols.join(', ');
}

function inferPeerLocation (address) {
  return isPrivateAddress(address) ? 'Private SDN link' : null;
}

function isPrivateAddress (address) {
  const value = String(address ?? '');
  return value.includes('/ip4/127.') ||
    value.includes('/ip4/10.') ||
    value.includes('/ip4/192.168.') ||
    value.includes('/ip4/172.16.') ||
    value.includes('/ip4/172.17.') ||
    value.includes('/ip4/172.18.') ||
    value.includes('/ip4/172.19.') ||
    value.includes('/ip4/172.2') ||
    value.includes('/ip6/::1');
}

function stringOrNull (value) {
  const normalized = String(value ?? '').trim();
  return normalized || null;
}

function asStringArray (value) {
  return (Array.isArray(value) ? value : [])
    .map((entry) => stringOrNull(entry))
    .filter(Boolean);
}

function uniqueStrings (values) {
  return [...new Set(asStringArray(values))];
}

async function fetchRegistryPeers (baseUrl, fetchImpl) {
  const response = await fetchImpl(`${baseUrl}/api/peers`);
  if (!response.ok) {
    const error = new Error(`failed to fetch SDN peers: ${response.status}`);
    error.status = response.status;
    throw error;
  }
  const payload = await response.json();
  return Array.isArray(payload) ? payload : [];
}

async function fetchKuboSdnPeers (baseUrl, fetchImpl) {
  if (!baseUrl) {
    return [];
  }

  const response = await fetchImpl(`${baseUrl}/api/v0/swarm/peers?verbose=true&identify=true&timeout=10000ms`, {
    method: 'POST',
  });
  if (!response.ok) {
    return [];
  }

  const payload = await response.json();
  return (Array.isArray(payload?.Peers) ? payload.Peers : [])
    .map(kuboPeerToTrustedPeer)
    .filter(Boolean);
}

async function fetchConfiguredDesktopSdnNodes (baseUrl, fetchImpl) {
  if (!isLocalDesktopBaseUrl(baseUrl)) {
    return [];
  }

  const response = await fetchImpl(`${baseUrl}/api/local/sdn-nodes`);
  if (!response.ok) {
    return [];
  }

  const payload = await response.json();
  return Array.isArray(payload?.nodes) ? payload.nodes : [];
}

function kuboPeerToTrustedPeer (peer) {
  const peerId = stringOrNull(peer?.Identify?.ID) ?? stringOrNull(peer?.Peer);
  if (!peerId) {
    return null;
  }

  const agentVersion = stringOrNull(peer?.Identify?.AgentVersion);
  const protocols = uniqueStrings(peer?.Identify?.Protocols);
  if (!isSdnAgentVersion(agentVersion) && !protocols.some(isSdnProtocol)) {
    return null;
  }

  const address = normalizePeerAddress(peer?.Addr, peerId);
  const metadata = {};
  if (agentVersion) {
    metadata.agent_version = agentVersion;
  }
  if (protocols.length > 0) {
    metadata.protocols = protocols.join(',');
  }

  const trustedPeer = { id: peerId };
  if (address) {
    trustedPeer.addrs = [address];
  }
  if (Object.keys(metadata).length > 0) {
    trustedPeer.metadata = metadata;
  }
  return trustedPeer;
}

function normalizePeerAddress (address, peerId) {
  const normalized = stringOrNull(address);
  if (!normalized) {
    return null;
  }
  if (normalized.includes('/p2p/')) {
    return normalized;
  }
  return `${normalized}/p2p/${peerId}`;
}

function isLocalDesktopBaseUrl (baseUrl) {
  try {
    const parsed = new URL(baseUrl);
    return parsed.protocol === 'http:' &&
      ['127.0.0.1', 'localhost', '[::1]'].includes(parsed.hostname);
  } catch {
    return false;
  }
}

function inferCurrentBaseUrl () {
  if (typeof window === 'undefined') {
    return null;
  }
  return window.location.origin;
}

function inferKuboApiBaseUrl () {
  if (typeof window === 'undefined') {
    return null;
  }

  try {
    const storedApi = window.localStorage?.getItem('ipfsApi');
    const fromStorage = httpUrlFromKuboApiAddress(storedApi);
    if (fromStorage) {
      return fromStorage;
    }
  } catch {
    // Ignore inaccessible storage and fall back to the desktop default below.
  }

  const host = window.location.hostname;
  if (host === '127.0.0.1' || host === 'localhost' || host === '::1') {
    return DEFAULT_KUBO_API_BASE_URL;
  }
  return null;
}

function httpUrlFromKuboApiAddress (value) {
  const candidate = String(value ?? '').trim();
  if (!candidate) {
    return null;
  }
  if (/^https?:\/\//i.test(candidate)) {
    return candidate;
  }

  const match = candidate.match(/^\/ip4\/([^/]+)\/tcp\/(\d+)$/);
  if (match) {
    return `http://${match[1]}:${match[2]}`;
  }
  return null;
}

function normalizeBaseUrl (value) {
  const candidate = String(value ?? '').trim();
  if (!candidate) {
    return null;
  }
  return candidate.replace(/\/+$/, '');
}

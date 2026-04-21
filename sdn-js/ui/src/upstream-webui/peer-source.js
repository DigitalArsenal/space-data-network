const DEFAULT_STALE_MS = 10_000;
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
  const agentVersion = stringOrNull(peer?.metadata?.agent_version);

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
      const agentVersion = stringOrNull(registryPeer?.metadata?.agent_version);
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

      return fetchRegistryPeers(baseUrl, fetchImpl);
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
    throw new Error(`failed to fetch SDN peers: ${response.status}`);
  }
  const payload = await response.json();
  return Array.isArray(payload) ? payload : [];
}

function inferCurrentBaseUrl () {
  if (typeof window === 'undefined') {
    return null;
  }
  return window.location.origin;
}

function normalizeBaseUrl (value) {
  const candidate = String(value ?? '').trim();
  if (!candidate) {
    return null;
  }
  return candidate.replace(/\/+$/, '');
}

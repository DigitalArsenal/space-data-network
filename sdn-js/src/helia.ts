/**
 * Helia-based SDN node factory.
 *
 * Creates a Helia node that wraps libp2p with IPFS content-routing, giving
 * every SDN node full IPFS + p2p capabilities in both browser and Node.js.
 *
 * The internal libp2p instance uses the same transport/security/pubsub config
 * as SDNNode so both can interoperate on the same network.
 *
 * Usage:
 *
 *   const { helia, libp2p } = await createHeliaSDNNode({ edgeRelays: [...] });
 *
 *   // IPFS content operations
 *   const fs = unixfs(helia);
 *   const cid = await fs.addBytes(myData);
 *
 *   // p2p stream protocol
 *   libp2p.handle('/my/protocol/1.0.0', handler);
 *
 *   await helia.stop();
 */

import { createHelia, type Helia } from 'helia';
import { unixfs } from '@helia/unixfs';
import { bitswap } from '@helia/block-brokers';
import { libp2pRouting } from '@helia/routers';
import { createLibp2p, type Libp2p } from 'libp2p';
import { serviceCapabilities } from '@libp2p/interface';
import { webSockets } from '@libp2p/websockets';
import { all as wsFilters } from '@libp2p/websockets/filters';
import { webTransport } from '@libp2p/webtransport';
import { webRTCDirect } from '@spacedatanetwork/libp2p-webrtc-v1';
import { circuitRelayTransport } from '@libp2p/circuit-relay-v2';
import { bootstrap } from '@libp2p/bootstrap';
import { identify } from '@libp2p/identify';
import { gossipsub } from '@chainsafe/libp2p-gossipsub';
import { noise } from '@chainsafe/libp2p-noise';
import { yamux } from '@chainsafe/libp2p-yamux';
import { kadDHT } from '@libp2p/kad-dht';
import { keys } from '@libp2p/crypto';
import { peerIdFromString } from '@libp2p/peer-id';
import { CID } from 'multiformats/cid';
import { multiaddr } from '@multiformats/multiaddr';

import type { SDNConfig } from './node';
import { dhtEnabled } from './node';
import { getBootstrapRelays } from './edge-discovery';
import { resolveConnectionMonitorInit } from './connection-monitor-policy';
import { initHDWallet } from './crypto/hd-wallet';
import type { DerivedIdentity } from './crypto/types';

const BOOTSTRAP_DIAL_TIMEOUT_MS = 3_000;

// secp256k1 marshalling needed for identity key → PeerID derivation
function marshalSecp256k1PrivateKey(rawKey: Uint8Array): Uint8Array {
  // secp256k1 protobuf prefix (type 2) + 32-byte key
  const prefix = new Uint8Array([8, 2, 18, 32]);
  const out = new Uint8Array(prefix.length + rawKey.length);
  out.set(prefix);
  out.set(rawKey, prefix.length);
  return out;
}

export interface HeliaSDNNode {
  /** The Helia node (IPFS + libp2p). */
  helia: Helia;
  /** The underlying libp2p instance (also accessible via helia.libp2p). */
  libp2p: Libp2p;
  /** Stop both Helia and libp2p. */
  stop(): Promise<void>;
}

export interface FetchCIDBytesFromHeliaOptions {
  signal?: AbortSignal;
  providers?: unknown[];
  providerAddrs?: string[];
  maxProviders?: number;
  onProgress?: (event: unknown) => void;
}

type IncomingStreamData = {
  stream: unknown;
  connection: unknown;
};

type HeliaLibp2pCreateOptions = NonNullable<Parameters<typeof createLibp2p>[0]>;

type CompatLibp2p = Libp2p & {
  __heliaStreamHandlerCompatApplied?: boolean;
  __heliaDialProtocolStreamCompatApplied?: boolean;
};

function identifyCapabilityOnly() {
  return () => ({
    [serviceCapabilities]: ['@libp2p/identify'],
    [Symbol.toStringTag]: 'sdn-js-identify-capability',
  });
}

type LegacyWritableStreamCompat = {
  __heliaLegacyWriteCompatApplied?: boolean;
  send?: (chunk: Uint8Array) => boolean;
  onDrain?: (options?: { signal?: AbortSignal }) => Promise<void>;
  sink?: (source: AsyncIterable<Uint8Array>) => Promise<void>;
  close?: (options?: unknown) => Promise<void>;
  closeWrite?: (options?: unknown) => Promise<void>;
};

type LegacyEventedStreamCompat = {
  __heliaLegacyEventCompatApplied?: boolean;
  source?: AsyncIterable<unknown> | (() => AsyncIterable<unknown>);
  close?: (options?: unknown) => Promise<void>;
  addEventListener?: (type: string, listener: (event: any) => void) => void;
  removeEventListener?: (type: string, listener: (event: any) => void) => void;
};

// Helia 6 registers some stream handlers as `(stream, connection)`, while the
// libp2p instance resolved in this workspace invokes handlers with a single
// `{ stream, connection }` object. Adapt only the two-argument form.
function withHeliaStreamHandlerCompat(libp2p: Libp2p): Libp2p {
  const candidate = libp2p as CompatLibp2p;
  if (candidate.__heliaStreamHandlerCompatApplied === true) {
    return candidate;
  }

  const originalHandle = candidate.handle?.bind(candidate);
  if (typeof originalHandle !== 'function') {
    return candidate;
  }

  candidate.handle = (async (protocols, handler, options) => {
    if (typeof handler === 'function' && handler.length >= 2) {
      const twoArgHandler = handler as any;
      return originalHandle(
        protocols,
        (incoming: IncomingStreamData) =>
          twoArgHandler(
            addLegacyEventedStreamCompat(incoming?.stream),
            incoming?.connection,
          ),
        options,
      );
    }
    return originalHandle(protocols, handler, options);
  }) as Libp2p['handle'];
  candidate.__heliaStreamHandlerCompatApplied = true;

  return candidate;
}

function addLegacyEventedStreamCompat<T>(stream: T): T {
  const candidate = stream as T & LegacyEventedStreamCompat;
  if (
    stream == null ||
    candidate.__heliaLegacyEventCompatApplied === true ||
    candidate.source == null ||
    typeof candidate.addEventListener === 'function'
  ) {
    return stream;
  }

  const listeners = new Map<string, Set<(event: any) => void>>();
  let pumpStarted = false;

  const dispatchEvent = (type: string, event: any = {}) => {
    for (const listener of listeners.get(type) ?? []) {
      listener(event);
    }
  };

  const streamSource = () => {
    const source = candidate.source;
    return typeof source === 'function' ? source.call(candidate) : source;
  };

  const startPump = () => {
    if (pumpStarted) {
      return;
    }
    pumpStarted = true;
    queueMicrotask(() => {
      void Promise.resolve()
        .then(async () => {
          const source = streamSource();
          if (!source) {
            return;
          }
          for await (const chunk of source) {
            dispatchEvent('message', { data: chunk });
          }
          dispatchEvent('remoteCloseWrite');
          dispatchEvent('close', { error: null });
        })
        .catch((error) => {
          dispatchEvent('close', { error });
        });
    });
  };

  candidate.addEventListener = (type: string, listener: (event: any) => void) => {
    const typeListeners = listeners.get(type) ?? new Set<(event: any) => void>();
    typeListeners.add(listener);
    listeners.set(type, typeListeners);
    if (type === 'message') {
      startPump();
    }
  };
  candidate.removeEventListener = (type: string, listener: (event: any) => void) => {
    listeners.get(type)?.delete(listener);
  };
  candidate.close = async () => undefined;
  candidate.__heliaLegacyEventCompatApplied = true;

  return candidate;
}

function addLegacyWritableStreamCompat<T>(stream: T): T {
  const candidate = stream as T & LegacyWritableStreamCompat;
  if (
    candidate.__heliaLegacyWriteCompatApplied === true ||
    typeof candidate.send === 'function' ||
    typeof candidate.sink !== 'function'
  ) {
    return stream;
  }

  const originalClose = candidate.close?.bind(candidate);
  const originalCloseWrite = candidate.closeWrite?.bind(candidate);
  const chunks: Uint8Array[] = [];
  const waiters: Array<() => void> = [];
  let sourceClosed = false;
  let sinkPromise: Promise<void> | undefined;

  const wakeSource = () => {
    for (const resolve of waiters.splice(0)) {
      resolve();
    }
  };

  async function* queuedSource(): AsyncGenerator<Uint8Array, void, unknown> {
    while (true) {
      const chunk = chunks.shift();
      if (chunk) {
        yield chunk;
        continue;
      }
      if (sourceClosed) {
        return;
      }
      await new Promise<void>((resolve) => {
        waiters.push(resolve);
      });
    }
  }

  const ensureSinkStarted = () => {
    if (!sinkPromise) {
      sinkPromise = candidate.sink?.(queuedSource()) ?? Promise.resolve();
    }
    return sinkPromise;
  };

  const closeQueuedSource = async () => {
    sourceClosed = true;
    wakeSource();
    if (sinkPromise) {
      await sinkPromise;
    }
  };

  candidate.send = (chunk: Uint8Array) => {
    if (sourceClosed) {
      throw new Error('Cannot send on a closed stream.');
    }
    chunks.push(chunk.slice());
    ensureSinkStarted();
    wakeSource();
    return true;
  };
  candidate.onDrain = async (options?: { signal?: AbortSignal }) => {
    if (options?.signal?.aborted === true) {
      throw options.signal.reason instanceof Error
        ? options.signal.reason
        : new Error(String(options.signal.reason ?? 'Stream drain aborted'));
    }
  };
  candidate.closeWrite = async (options?: unknown) => {
    if (!sinkPromise) {
      await originalCloseWrite?.(options);
      return;
    }
    await closeQueuedSource();
  };
  candidate.close = async (options?: unknown) => {
    await closeQueuedSource();
    await originalClose?.(options);
  };
  candidate.__heliaLegacyWriteCompatApplied = true;

  return candidate;
}

function withHeliaDialProtocolStreamCompat(libp2p: Libp2p): Libp2p {
  const candidate = libp2p as CompatLibp2p;
  if (candidate.__heliaDialProtocolStreamCompatApplied === true) {
    return candidate;
  }

  const originalDialProtocol = candidate.dialProtocol?.bind(candidate);
  if (typeof originalDialProtocol !== 'function') {
    return candidate;
  }

  candidate.dialProtocol = (async (...args: Parameters<Libp2p['dialProtocol']>) => {
    const stream = await originalDialProtocol(...args);
    return addLegacyWritableStreamCompat(stream);
  }) as Libp2p['dialProtocol'];
  candidate.__heliaDialProtocolStreamCompatApplied = true;

  return candidate;
}

export async function createHeliaFromLibp2p(libp2p: Libp2p): Promise<Helia> {
  const compatibleLibp2p = withHeliaDialProtocolStreamCompat(
    withHeliaStreamHandlerCompat(libp2p),
  );
  return createHelia({
    libp2p: compatibleLibp2p,
    blockBrokers: [bitswap()],
    routers: [libp2pRouting(compatibleLibp2p as never)],
  } as never);
}

async function dialBootstrapAddrs(
  libp2p: Libp2p,
  addrs: readonly string[],
): Promise<void> {
  if (addrs.length === 0 || typeof libp2p.dial !== 'function') {
    return;
  }

  await Promise.allSettled(
    addrs.map(async (addr) => {
      let timer: ReturnType<typeof setTimeout> | undefined;
      let signal: AbortSignal | undefined;
      if (typeof AbortSignal !== 'undefined' && typeof AbortController !== 'undefined') {
        if (typeof AbortSignal.timeout === 'function') {
          signal = AbortSignal.timeout(BOOTSTRAP_DIAL_TIMEOUT_MS);
        } else {
          const controller = new AbortController();
          signal = controller.signal;
          timer = setTimeout(() => controller.abort(), BOOTSTRAP_DIAL_TIMEOUT_MS);
        }
      }

      try {
        await libp2p.dial(
          multiaddr(addr),
          signal ? ({ signal } as never) : undefined,
        );
      } finally {
        if (timer) {
          clearTimeout(timer);
        }
      }
    }),
  );
}

type ProviderHint = {
  peerId: unknown;
  multiaddr: ReturnType<typeof multiaddr>;
};

type PeerIdWithMultihashCompat = {
  multihash?: unknown;
  toMultihash?: () => unknown;
};

async function seedProviderAddrs(
  helia: Helia,
  providers: readonly ProviderHint[],
): Promise<void> {
  const peerStore = (helia as Helia & {
    libp2p?: { peerStore?: { merge?: (peerId: unknown, data: unknown) => Promise<unknown> } };
  }).libp2p?.peerStore;
  if (providers.length === 0 || typeof peerStore?.merge !== 'function') {
    return;
  }

  await Promise.allSettled(
    providers.map(({ peerId, multiaddr: addr }) =>
      peerStore.merge(peerId, { multiaddrs: [addr] }),
    ),
  );
}

function providerHintsFromAddrs(addrs: readonly ReturnType<typeof multiaddr>[]): ProviderHint[] {
  return addrs.map((addr) => {
    const peerId = addr.getPeerId();
    if (!peerId) {
      throw new Error('Provider bootstrap multiaddrs must include /p2p/<peer-id>.');
    }
    const parsedPeerId = peerIdFromString(peerId) as PeerIdWithMultihashCompat;
    if (
      typeof parsedPeerId.toMultihash !== 'function' &&
      parsedPeerId.multihash != null
    ) {
      parsedPeerId.toMultihash = () => parsedPeerId.multihash;
    }
    return {
      peerId: parsedPeerId,
      multiaddr: addr,
    };
  });
}

function providerSessionMaxProviders(
  providerCount: number,
  configuredMaxProviders: number | undefined,
): number {
  if (!Number.isFinite(configuredMaxProviders)) {
    return providerCount;
  }
  return Math.max(providerCount, configuredMaxProviders ?? providerCount);
}

export async function fetchCIDBytesFromHelia(
  helia: Helia,
  cid: string,
  options: FetchCIDBytesFromHeliaOptions = {},
): Promise<Uint8Array> {
  const rootCid = CID.parse(cid);
  let blockstoreSession: { close?: () => void } | undefined;
  let unixFsTarget: Parameters<typeof unixfs>[0] = helia;
  const bytes: Uint8Array[] = [];
  const { providerAddrs = [], ...catOptions } = options;
  if (providerAddrs.length > 0) {
    const providerMultiaddrs = providerAddrs.map((addr) => multiaddr(addr));
    const providerHints = providerHintsFromAddrs(providerMultiaddrs);
    await seedProviderAddrs(helia, providerHints);
    catOptions.providers = [
      ...(catOptions.providers ?? []),
      ...providerHints.map(({ peerId }) => peerId),
    ];
    const blockstore = (helia as Helia & {
      blockstore?: {
        createSession?: (root: CID, options?: unknown) => { close?: () => void };
      };
    }).blockstore;
    if (typeof blockstore?.createSession === 'function') {
      blockstoreSession = blockstore.createSession(rootCid, {
        ...catOptions,
        maxProviders: providerSessionMaxProviders(
          providerHints.length,
          catOptions.maxProviders,
        ),
      });
      unixFsTarget = { blockstore: blockstoreSession } as Parameters<typeof unixfs>[0];
    }
  }

  try {
    const fs = unixfs(unixFsTarget);
    for await (const chunk of fs.cat(rootCid, catOptions as never)) {
      bytes.push(chunk.slice());
    }
  } finally {
    blockstoreSession?.close?.();
  }

  return concatBytes(bytes);
}

/**
 * Create a Helia node configured for the Space Data Network.
 *
 * The libp2p config mirrors SDNNode.init() so both node types are
 * compatible peers on the same network.
 *
 * @param config  SDNConfig — same options accepted by SDNNode.create()
 */
export async function createHeliaSDNNode(config: SDNConfig = {}): Promise<HeliaSDNNode> {
  await initHDWallet();

  const rawRelays = config.edgeRelays ?? await getBootstrapRelays();
  const bootstrapList = rawRelays.length > 0 ? rawRelays : [];

  const services: NonNullable<Parameters<typeof createLibp2p>[0]>['services'] = {
    pubsub: gossipsub({
      allowPublishToZeroTopicPeers: true,
      emitSelf: false,
    }),
  };
  // Opt-in, for the reason documented on `dhtEnabled` in ./node: a browser
  // Kad-DHT client starves the renderer's event loop and wedges it for good.
  // Helia does not need it here — blocks come from the peers this node dials
  // directly (bitswap over the configured relays), which is exactly how
  // spaceaware.io/beta reads the live catalog.
  if (dhtEnabled(config)) {
    services.dht = kadDHT({ clientMode: true });
  }
  services.identify =
    config.enableIdentify === true ? identify() : identifyCapabilityOnly();

  const libp2pOpts: HeliaLibp2pCreateOptions = {
    transports: [
      webSockets({ filter: wsFilters }),
      webTransport(),
      webRTCDirect() as unknown as ReturnType<typeof webTransport>,
      circuitRelayTransport({ discoverRelays: 100 }),
    ],
    connectionEncryption: [noise()],
    streamMuxers: [yamux()],
    peerDiscovery: bootstrapList.length
      ? [bootstrap({ list: bootstrapList })]
      : [],
    // Same policy as SDNNode: libp2p's stock heartbeat aborts the whole
    // connection after a fixed 2000 ms, which a busy browser misses while the
    // peer is healthy. See connection-monitor-policy.ts.
    connectionMonitor: resolveConnectionMonitorInit(config.connectionMonitor),
    services,
  };
  if (config.enableAutoDial === false) {
    libp2pOpts.connectionManager = {
      minConnections: 0,
    };
  }

  if (config.identity?.identityKey) {
    const rawKey = (config.identity as DerivedIdentity).identityKey.privateKey;
    libp2pOpts.privateKey = await keys.unmarshalPrivateKey(
      marshalSecp256k1PrivateKey(rawKey),
    );
  }

  const libp2p = await createLibp2p(libp2pOpts);
  const helia = await createHeliaFromLibp2p(libp2p);
  await dialBootstrapAddrs(helia.libp2p as unknown as Libp2p, bootstrapList);

  return {
    helia,
    libp2p: helia.libp2p as unknown as Libp2p,
    async stop() {
      await helia.stop();
    },
  };
}

function concatBytes(chunks: Uint8Array[]): Uint8Array {
  if (chunks.length === 0) {
    return new Uint8Array(0);
  }
  if (chunks.length === 1) {
    return chunks[0];
  }

  const totalLength = chunks.reduce((sum, chunk) => sum + chunk.length, 0);
  const out = new Uint8Array(totalLength);
  let offset = 0;
  for (const chunk of chunks) {
    out.set(chunk, offset);
    offset += chunk.length;
  }
  return out;
}

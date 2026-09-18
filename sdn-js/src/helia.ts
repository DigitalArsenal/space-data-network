/**
 * Helia-based SDN node factory.
 *
 * Creates a Helia node that wraps libp2p with IPFS content-routing, giving
 * every SDN node full IPFS + p2p capabilities in both browser and Node.js.
 *
 * The internal libp2p instance uses the same transport/security/pubsub config
 * as SDNNode so both can interoperate on the same network.
 *
 * ONE LIBP2P MAJOR. This file used to carry ~370 lines of hand-written
 * compatibility shims because sdn-js built its node with libp2p 1.9.4 while
 * helia@6 nested its own libp2p 3.2.0, and the two disagreed about stream
 * handler arity and the stream read/write API. Both halves now resolve to a
 * single hoisted libp2p 3.x, so helia calls the same object we build and every
 * one of those shims is gone.
 *
 * The one integration that remains hand-written is `libp2pRouter` below: helia 7
 * moved `libp2pRouting()` inside `@helia/libp2p`, which does not export it and
 * builds its own libp2p rather than accepting ours. That is an adapter over a
 * public libp2p API, not a bridge between two majors.
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

import { createHeliaLight, type Helia } from 'helia';
import { unixfs } from '@helia/unixfs';
import { withBitswap } from '@helia/bitswap';
import { trustlessGatewayBlockBroker } from '@helia/trustless-gateway-client';
import { fallbackRouter } from '@helia/fallback-router';
import { createLibp2p, type Libp2p } from 'libp2p';
import { serviceCapabilities } from '@libp2p/interface';
import { webSockets } from '@libp2p/websockets';
import { webTransport } from '@libp2p/webtransport';
import { webRTCDirect } from '@libp2p/webrtc';
import { circuitRelayTransport } from '@libp2p/circuit-relay-v2';
import { bootstrap } from '@libp2p/bootstrap';
import { identify } from '@libp2p/identify';
import { ping } from '@libp2p/ping';
import { gossipsub } from '@libp2p/gossipsub';
import { noise } from '@libp2p/noise';
import { yamux } from '@libp2p/yamux';
import { kadDHT } from '@libp2p/kad-dht';
import { privateKeyFromProtobuf } from '@libp2p/crypto/keys';
import { peerIdFromCID, peerIdFromString } from '@libp2p/peer-id';
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
  /** Maximum object size. Whole-object reads default to 128 MiB; streaming has no default total-size limit. */
  maxBytes?: number;
}

type HeliaLibp2pCreateOptions = NonNullable<Parameters<typeof createLibp2p>[0]>;

/**
 * Advertise the `@libp2p/identify` capability without running identify.
 *
 * NOT a version shim, and not optional: gossipsub declares
 * `serviceDependencies = ['@libp2p/identify']`, and libp2p throws
 * `UnmetServiceDependenciesError` at construction when a declared capability is
 * unmet. Running the real `identify()` instead would make browsers advertise
 * their protocol list to the Go node, which is the behaviour
 * sdn-server/internal/node/peer_admission_policy.go was rewritten to stop
 * depending on after the 2026-08-06 outage.
 */
function identifyCapabilityOnly() {
  return () => ({
    [serviceCapabilities]: ['@libp2p/identify'],
    [Symbol.toStringTag]: 'sdn-js-identify-capability',
  });
}

function normalizeTrustlessGateways(value: SDNConfig['ipfsTrustlessGateways']): string[] {
  if (value === undefined) return [];
  const message = 'ipfsTrustlessGateways must contain only HTTPS origins without credentials, paths, queries, or fragments.';
  if (!Array.isArray(value)) throw new Error(message);
  const gateways = new Set<string>();
  for (const entry of value) {
    let url: URL;
    try {
      if (typeof entry !== 'string' || !entry.trim()) throw new Error(message);
      url = new URL(entry);
    } catch {
      throw new Error(message);
    }
    if (url.protocol !== 'https:' || url.username || url.password || url.pathname !== '/' || url.search || url.hash) {
      throw new Error(message);
    }
    gateways.add(url.origin);
  }
  return [...gateways];
}

type AnyRouter = Record<string, any>;

/**
 * Expose a libp2p instance to Helia's routing layer.
 *
 * helia 6 shipped this as `libp2pRouting()` in `@helia/routers`. helia 7 moved
 * it into `@helia/libp2p`, which neither exports it nor accepts a pre-built
 * libp2p — `withLibp2pLight()` always calls `createLibp2p()` itself. sdn-js
 * hands Helia the SAME libp2p instance SDNNode uses, so the adapter lives here.
 * It only forwards to libp2p's public contentRouting/peerRouting APIs.
 */
function libp2pRouter(libp2p: Libp2p): AnyRouter {
  const anyLibp2p = libp2p as unknown as AnyRouter;
  const toPeer = (info: any) => ({ ...info, id: info.id.toCID(), router: 'libp2p-router' });
  return {
    name: 'libp2p-router',
    async provide(cid: CID, options?: unknown) {
      await anyLibp2p.contentRouting.provide(cid, options);
    },
    async cancelReprovide(key: unknown, options?: unknown) {
      await anyLibp2p.contentRouting.cancelReprovide(key, options);
    },
    async *findProviders(cid: CID, options?: unknown) {
      for await (const provider of anyLibp2p.contentRouting.findProviders(cid, options)) {
        yield { ...toPeer(provider), fallback: false };
      }
    },
    async put(key: Uint8Array, value: Uint8Array, options?: unknown) {
      await anyLibp2p.contentRouting.put(key, value, options);
    },
    async get(key: Uint8Array, options?: unknown) {
      return anyLibp2p.contentRouting.get(key, options);
    },
    async findPeer(peerId: unknown, options?: unknown) {
      return toPeer(await anyLibp2p.peerRouting.findPeer(peerIdFromCID(peerId as never), options));
    },
    async *getClosestPeers(key: Uint8Array, options?: unknown) {
      for await (const peer of anyLibp2p.peerRouting.getClosestPeers(key, options)) {
        yield toPeer(peer);
      }
    },
    toString() {
      return 'SdnLibp2pRouter()';
    },
  };
}

/**
 * Attach an already-constructed libp2p to a Helia node.
 *
 * `helia.libp2p` becomes the passed instance, Helia routes content lookups
 * through it, and `helia.stop()` stops it — the same lifecycle helia 6 gave a
 * caller-supplied libp2p through `createHelia({ libp2p })`.
 */
function attachLibp2p(helia: Helia, libp2p: Libp2p): Helia {
  const target = helia as unknown as {
    hasRouter(name: string): boolean;
    addRouter(router: unknown): void;
    addMixin(mixin: unknown): void;
  };
  Object.defineProperty(helia, 'libp2p', {
    configurable: true,
    enumerable: true,
    get: () => libp2p,
  });
  target.addMixin({
    name: 'libp2p',
    start: async () => {
      if (!target.hasRouter('libp2p-router')) {
        target.addRouter(libp2pRouter(libp2p));
      }
      // createLibp2p() returns a started node, and start() on a started node
      // throws, so only start one the caller created with `start: false`.
      if ((libp2p as unknown as { status?: string }).status === 'stopped') {
        await libp2p.start();
      }
    },
    stop: async () => {
      await libp2p.stop();
    },
  });
  return helia;
}

/** The libp2p instance attached to a Helia node by attachLibp2p(). */
export function heliaLibp2p(helia: Helia): Libp2p {
  return (helia as unknown as { libp2p: Libp2p }).libp2p;
}

export async function createHeliaFromLibp2p(
  libp2p: Libp2p,
  config: Pick<SDNConfig, 'ipfsTrustlessGateways'> = {},
): Promise<Helia> {
  const gateways = normalizeTrustlessGateways(config.ipfsTrustlessGateways);
  const helia = createHeliaLight({
    blockBrokers: gateways.length ? [trustlessGatewayBlockBroker()] : [],
    routers: gateways.length ? [fallbackRouter({ gateways, shuffle: false })] : [],
  });
  attachLibp2p(helia, libp2p);
  // withBitswap types its argument as HeliaWithLibp2p; attachLibp2p just defined
  // that property, which the structural Helia type cannot express.
  withBitswap(helia as never);
  await helia.start();
  return helia;
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

async function seedProviderAddrs(
  helia: Helia,
  providers: readonly ProviderHint[],
): Promise<void> {
  const peerStore = (helia as unknown as {
    libp2p?: { peerStore?: { merge?: (peerId: unknown, data: unknown) => Promise<unknown> } };
  }).libp2p?.peerStore;
  if (providers.length === 0 || typeof peerStore?.merge !== 'function') {
    return;
  }

  const merge = peerStore.merge;
  await Promise.allSettled(
    providers.map(({ peerId, multiaddr: addr }) =>
      merge.call(peerStore, peerId, { multiaddrs: [addr] }),
    ),
  );
}

/**
 * The /p2p component of a multiaddr.
 *
 * `Multiaddr.getPeerId()` was removed in @multiformats/multiaddr 13 ("lightweight
 * multiaddrs"); `getComponents()` is the replacement. The LAST p2p component wins,
 * which is what a `/…/p2p/<relay>/p2p-circuit/p2p/<target>` address needs.
 */
export function multiaddrPeerId(addr: ReturnType<typeof multiaddr>): string | null {
  let found: string | null = null;
  for (const component of addr.getComponents()) {
    if (component.name === 'p2p' && typeof component.value === 'string') {
      found = component.value;
    }
  }
  return found;
}

function providerHintsFromAddrs(addrs: readonly ReturnType<typeof multiaddr>[]): ProviderHint[] {
  return addrs.map((addr) => {
    const peerId = multiaddrPeerId(addr);
    if (!peerId) {
      throw new Error('Provider bootstrap multiaddrs must include /p2p/<peer-id>.');
    }
    return {
      peerId: peerIdFromString(peerId),
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

/**
 * Stream verified UnixFS content on demand. Early return closes the iterator
 * and provider session. Copy a chunk if it must outlive the next iteration.
 */
export async function* streamCIDFromHelia(
  helia: Helia,
  cid: string,
  options: FetchCIDBytesFromHeliaOptions = {},
): AsyncGenerator<Uint8Array, void, undefined> {
  options.signal?.throwIfAborted();
  const maxBytes = options.maxBytes ?? Number.MAX_SAFE_INTEGER;
  if (!Number.isSafeInteger(maxBytes) || maxBytes < 0) throw new Error('maxBytes must be a non-negative safe integer');
  const rootCid = CID.parse(cid);
  let blockstoreSession: { close?: () => void | Promise<void> } | undefined;
  let unixFsTarget: Parameters<typeof unixfs>[0] = helia;
  let totalBytes = 0;
  const { providerAddrs = [], maxBytes: _maxBytes, ...catOptions } = options;
  if (providerAddrs.length > 0) {
    const providerMultiaddrs = providerAddrs.map((addr) => multiaddr(addr));
    const providerHints = providerHintsFromAddrs(providerMultiaddrs);
    await seedProviderAddrs(helia, providerHints);
    // helia 7 types a session provider as `CID | Multiaddr | Multiaddr[]` and
    // its trustless-gateway session calls `ma.getComponents()` on each one, so
    // a PeerId - which helia 6 accepted - now throws a TypeError that aborts the
    // whole provider search and the session never becomes ready. Hand it the
    // multiaddrs the caller supplied; bitswap dials them directly and the
    // gateway session correctly ignores the non-HTTP ones. The PeerStore is
    // still seeded with the PeerId above so a later dial-by-peer-id resolves.
    catOptions.providers = [
      ...(catOptions.providers ?? []),
      ...providerHints.map(({ multiaddr: addr }) => addr),
    ];
    const blockstore = (helia as Helia & {
      blockstore?: {
        createSession?: (root: CID, options?: unknown) => { close?: () => void | Promise<void> };
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
    options.signal?.throwIfAborted();
    const fs = unixfs(unixFsTarget);
    for await (const chunk of fs.cat(rootCid, catOptions as never)) {
      options.signal?.throwIfAborted();
      if (chunk.byteLength > maxBytes - totalBytes) throw new Error(`CID byte limit ${maxBytes} exceeded; use streaming or an explicit larger limit`);
      totalBytes += chunk.byteLength;
      yield chunk;
      options.signal?.throwIfAborted();
    }
  } finally {
    await blockstoreSession?.close?.();
  }
}

/** Buffer an object up to maxBytes (default 128 MiB); prefer streaming for datasets. */
export async function fetchCIDBytesFromHelia(
  helia: Helia,
  cid: string,
  options: FetchCIDBytesFromHeliaOptions = {},
): Promise<Uint8Array> {
  const bytes: Uint8Array[] = [];
  for await (const chunk of streamCIDFromHelia(helia, cid, { ...options, maxBytes: options.maxBytes ?? 128 * 1024 * 1024 })) {
    bytes.push(chunk.slice());
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
  // Reject invalid opt-ins before wallet or network initialization.
  const ipfsTrustlessGateways = normalizeTrustlessGateways(config.ipfsTrustlessGateways);
  await initHDWallet();

  const rawRelays = config.edgeRelays ?? await getBootstrapRelays();
  const bootstrapList = rawRelays.length > 0 ? rawRelays : [];

  const services: Record<string, unknown> = {
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
    // kad-dht 16 declares `['@libp2p/identify', '@libp2p/ping']` and CALLS
    // ping.ping() during routing-table eviction, so a capability-only stub
    // would crash there rather than at construction. Only registered when the
    // DHT is on, so the default browser node still advertises nothing.
    services.ping = ping();
    services.dht = kadDHT({ clientMode: true });
  }
  services.identify =
    config.enableIdentify === true ? identify() : identifyCapabilityOnly();

  const libp2pOpts: HeliaLibp2pCreateOptions = {
    // circuit-relay-v2 4.x removed `discoverRelays`; a `/p2p-circuit` listen
    // address is how a node now asks for relay reservations.
    addresses: {
      listen: ['/p2p-circuit'],
    },
    transports: [
      webSockets(),
      webTransport(),
      webRTCDirect(),
      circuitRelayTransport(),
    ],
    connectionEncrypters: [noise()],
    streamMuxers: [yamux()],
    peerDiscovery: bootstrapList.length
      ? [bootstrap({ list: bootstrapList })]
      : [],
    // Same policy as SDNNode: libp2p's stock heartbeat aborts the whole
    // connection after a deadline whose floor is 5000 ms, which a busy browser
    // misses while the peer is healthy. See connection-monitor-policy.ts.
    connectionMonitor: resolveConnectionMonitorInit(config.connectionMonitor),
    services: services as HeliaLibp2pCreateOptions['services'],
  };

  // See `allowPrivateAddressDial` in SDNConfig: the browser gater's default
  // denies private-IP dials outright, so a loopback publisher is unreachable
  // until the caller says otherwise. Only `denyDialMultiaddr` is replaced —
  // every other gate keeps libp2p's default.
  if (config.allowPrivateAddressDial === true) {
    libp2pOpts.connectionGater = {
      ...(libp2pOpts.connectionGater ?? {}),
      denyDialMultiaddr: async () => false,
    };
  }

  if (config.identity?.identityKey) {
    const rawKey = (config.identity as DerivedIdentity).identityKey.privateKey;
    libp2pOpts.privateKey = privateKeyFromProtobuf(
      marshalSecp256k1PrivateKey(rawKey),
    );
  }

  const libp2p = await createLibp2p(libp2pOpts);
  const helia = await createHeliaFromLibp2p(libp2p, { ipfsTrustlessGateways });
  await dialBootstrapAddrs(heliaLibp2p(helia), bootstrapList);

  return {
    helia,
    libp2p: heliaLibp2p(helia),
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

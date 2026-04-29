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
import { createLibp2p, type Libp2p } from 'libp2p';
import { serviceCapabilities } from '@libp2p/interface';
import { webSockets } from '@libp2p/websockets';
import { all as wsFilters } from '@libp2p/websockets/filters';
import { webTransport } from '@libp2p/webtransport';
import { circuitRelayTransport } from '@libp2p/circuit-relay-v2';
import { bootstrap } from '@libp2p/bootstrap';
import { identify } from '@libp2p/identify';
import { gossipsub } from '@chainsafe/libp2p-gossipsub';
import { noise } from '@chainsafe/libp2p-noise';
import { yamux } from '@chainsafe/libp2p-yamux';
import { kadDHT } from '@libp2p/kad-dht';
import { keys } from '@libp2p/crypto';
import { CID } from 'multiformats/cid';

import type { SDNConfig } from './node';
import { getBootstrapRelays } from './edge-discovery';
import { initHDWallet } from './crypto/hd-wallet';
import type { DerivedIdentity } from './crypto/types';

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

type IncomingStreamData = {
  stream: unknown;
  connection: unknown;
};

type CompatLibp2p = Libp2p & {
  __heliaStreamHandlerCompatApplied?: boolean;
};

function identifyCapabilityOnly() {
  return () => ({
    [serviceCapabilities]: ['@libp2p/identify'],
    [Symbol.toStringTag]: 'sdn-js-identify-capability',
  });
}

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
          twoArgHandler(incoming?.stream, incoming?.connection),
        options,
      );
    }
    return originalHandle(protocols, handler, options);
  }) as Libp2p['handle'];
  candidate.__heliaStreamHandlerCompatApplied = true;

  return candidate;
}

export async function createHeliaFromLibp2p(libp2p: Libp2p): Promise<Helia> {
  return createHelia({ libp2p: withHeliaStreamHandlerCompat(libp2p) } as never);
}

export async function fetchCIDBytesFromHelia(helia: Helia, cid: string): Promise<Uint8Array> {
  const fs = unixfs(helia);
  const bytes: Uint8Array[] = [];

  for await (const chunk of fs.cat(CID.parse(cid))) {
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
  await initHDWallet();

  const rawRelays = config.edgeRelays ?? await getBootstrapRelays();
  const bootstrapList = rawRelays.length > 0 ? rawRelays : [];

  const services: NonNullable<Parameters<typeof createLibp2p>[0]>['services'] = {
    pubsub: gossipsub({
      allowPublishToZeroTopicPeers: true,
      emitSelf: false,
    }),
    dht: kadDHT({ clientMode: true }),
  };
  services.identify =
    config.enableIdentify === true ? identify() : identifyCapabilityOnly();

  const libp2pOpts: Parameters<typeof createLibp2p>[0] = {
    transports: [
      webSockets({ filter: wsFilters }),
      webTransport(),
      circuitRelayTransport({ discoverRelays: 100 }),
    ],
    connectionEncryption: [noise()],
    streamMuxers: [yamux()],
    peerDiscovery: bootstrapList.length
      ? [bootstrap({ list: bootstrapList })]
      : [],
    services,
  };

  if (config.identity?.identityKey) {
    const rawKey = (config.identity as DerivedIdentity).identityKey.privateKey;
    libp2pOpts.privateKey = await keys.unmarshalPrivateKey(
      marshalSecp256k1PrivateKey(rawKey),
    );
  }

  const libp2p = await createLibp2p(libp2pOpts);
  const helia = await createHeliaFromLibp2p(libp2p);

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

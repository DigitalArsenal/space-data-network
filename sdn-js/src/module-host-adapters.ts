/**
 * Module-host capability adapters (WS6.2).
 *
 * Bridges the sdn-js browser node's services — Helia (ipfs), the record
 * store (storage_*), libp2p gossipsub (pubsub), and wallet key material
 * (wallet_sign / keyslot) — into the space-data-module-sdk BrowserHost
 * adapter contract, so guest WASM modules loaded through the browser module
 * harness can call the same host capabilities the Go node grants
 * (sdn-server internal/modulert/caps):
 *
 *   ipfs.add {base64|content|data}    -> { Hash, Size }
 *   ipfs.cat {cid}                    -> bytes
 *   storage.write {schema, data:b64}  -> { cid }
 *   storage.query {schema, limit,...} -> records
 *   storage.delete {schema, cid}      -> true
 *   pubsub.publish {topic, data}      -> true
 *   pubsub.subscribe/unsubscribe {topic}; pubsub.list_topics {} -> { topics }
 *   keyslot.get {slotId}              -> key bytes
 *
 * Pass the result as `hostOptions.capabilityAdapters` to
 * createBrowserModuleHarness (see loadDecryptedModule in
 * ui/runtime/live-delivery.ts).
 */

import { unixfs } from '@helia/unixfs';
import { CID } from 'multiformats/cid';
import type { Helia } from 'helia';
import type { Libp2p } from 'libp2p';
import type { GossipSub } from '@chainsafe/libp2p-gossipsub';

/** Minimal record-store contract (satisfied by the FlatSQL-WASM engine record store — THE SDNNode store — and the legacy SDNStorage). */
export interface ModuleHostRecordStore {
  store(
    schema: string,
    data: Uint8Array,
    peerId: string,
    signature: Uint8Array,
  ): Promise<string>;
  query(
    schema: string,
    filter?: { peerId?: string; limit?: number },
  ): Promise<unknown[]>;
  delete(cid: string): Promise<void>;
}

export type ModuleHostKeySlotResolver =
  | Uint8Array
  | (() => Uint8Array | Promise<Uint8Array>);

export interface ModuleHostAdapterOptions {
  /** Helia instance backing ipfs.add / ipfs.cat. */
  helia?: Helia;
  /** libp2p node whose services.pubsub (gossipsub) backs pubsub.*. */
  libp2p?: Libp2p;
  /** Record store backing storage.* (e.g. SDNStorage). */
  storage?: ModuleHostRecordStore;
  /** Publisher peer id recorded on storage.write records. */
  peerId?: string;
  /** Named signing-key slots served by keyslot.get. */
  keySlots?: Record<string, ModuleHostKeySlotResolver>;
  /** Optional callback for messages on topics subscribed via pubsub.subscribe. */
  onPubsubMessage?: (message: {
    topic: string;
    data: Uint8Array;
    from?: string;
  }) => void;
}

export interface ModuleHostCapabilityAdapters {
  ipfs?: Record<string, (params: Record<string, unknown>) => Promise<unknown>>;
  storage?: Record<string, (params: Record<string, unknown>) => Promise<unknown>>;
  pubsub?: Record<string, (params: Record<string, unknown>) => Promise<unknown>>;
  walletSign?: Record<string, (params: Record<string, unknown>) => Promise<unknown>>;
}

function decodeBase64(value: string): Uint8Array {
  const binary = atob(value);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes;
}

function toBytes(value: unknown, label: string): Uint8Array {
  if (value instanceof Uint8Array) return value;
  if (value instanceof ArrayBuffer) return new Uint8Array(value);
  if (typeof value === 'string') return decodeBase64(value);
  throw new Error(`${label} must be base64, Uint8Array, or ArrayBuffer`);
}

function requireString(value: unknown, label: string): string {
  const text = String(value ?? '').trim();
  if (!text) throw new Error(`missing ${label}`);
  return text;
}

function pubsubService(libp2p: Libp2p): GossipSub {
  const service = (libp2p.services as Record<string, unknown>)?.pubsub;
  if (!service) {
    throw new Error('libp2p node has no pubsub service configured');
  }
  return service as GossipSub;
}

/**
 * Build BrowserHost capabilityAdapters from sdn-js node services. Only the
 * capabilities whose backing service was provided are included.
 */
export function createModuleHostCapabilityAdapters(
  options: ModuleHostAdapterOptions = {},
): ModuleHostCapabilityAdapters {
  const adapters: ModuleHostCapabilityAdapters = {};
  const textEncoder = new TextEncoder();

  if (options.helia) {
    const helia = options.helia;
    adapters.ipfs = {
      add: async (params) => {
        const source =
          params.base64 ?? params.content ?? params.data ?? null;
        const bytes =
          typeof params.content === 'string' && params.base64 === undefined
            ? textEncoder.encode(params.content as string)
            : toBytes(source, 'ipfs.add data');
        const fs = unixfs(helia);
        const cid = await fs.addBytes(bytes);
        return { Hash: cid.toString(), Size: bytes.length };
      },
      cat: async (params) => {
        const cid = requireString(params.cid, 'cid');
        const fs = unixfs(helia);
        const chunks: Uint8Array[] = [];
        let total = 0;
        for await (const chunk of fs.cat(CID.parse(cid))) {
          chunks.push(chunk.slice());
          total += chunk.length;
        }
        const bytes = new Uint8Array(total);
        let offset = 0;
        for (const chunk of chunks) {
          bytes.set(chunk, offset);
          offset += chunk.length;
        }
        return bytes;
      },
    };
  }

  if (options.storage) {
    const store = options.storage;
    const peerId = options.peerId ?? 'browser-module-host';
    adapters.storage = {
      write: async (params) => {
        const schema = requireString(params.schema, 'schema');
        const bytes = toBytes(params.data, 'storage.write data');
        const signature =
          params.signature === undefined
            ? new Uint8Array(0)
            : toBytes(params.signature, 'storage.write signature');
        const cid = await store.store(schema, bytes, peerId, signature);
        return { cid };
      },
      query: async (params) => {
        const schema = requireString(params.schema, 'schema');
        const limitRaw = Number(params.limit ?? 100);
        const limit =
          Number.isFinite(limitRaw) && limitRaw > 0 ? Math.floor(limitRaw) : 100;
        const peer =
          typeof params.peer_id === 'string' && params.peer_id
            ? (params.peer_id as string)
            : undefined;
        return store.query(schema, { peerId: peer, limit });
      },
      delete: async (params) => {
        const cid = requireString(params.cid, 'cid');
        await store.delete(cid);
        return true;
      },
    };
  }

  if (options.libp2p) {
    const libp2p = options.libp2p;
    const onMessage = options.onPubsubMessage;
    let messageListenerInstalled = false;
    const subscribedTopics = new Set<string>();
    adapters.pubsub = {
      publish: async (params) => {
        const topic = requireString(params.topic, 'topic');
        const data =
          typeof params.data === 'string'
            ? textEncoder.encode(params.data as string)
            : toBytes(params.data ?? new Uint8Array(0), 'pubsub.publish data');
        await pubsubService(libp2p).publish(topic, data);
        return true;
      },
      subscribe: async (params) => {
        const topic = requireString(params.topic, 'topic');
        const pubsub = pubsubService(libp2p);
        pubsub.subscribe(topic);
        subscribedTopics.add(topic);
        if (onMessage && !messageListenerInstalled) {
          messageListenerInstalled = true;
          pubsub.addEventListener('message', (event) => {
            const detail = (event as CustomEvent<{
              topic: string;
              data: Uint8Array;
              from?: { toString(): string };
            }>).detail;
            if (!subscribedTopics.has(detail.topic)) return;
            onMessage({
              topic: detail.topic,
              data: detail.data,
              from: detail.from?.toString(),
            });
          });
        }
        return true;
      },
      unsubscribe: async (params) => {
        const topic = requireString(params.topic, 'topic');
        pubsubService(libp2p).unsubscribe(topic);
        subscribedTopics.delete(topic);
        return true;
      },
      list_topics: async () => ({
        topics: pubsubService(libp2p).getTopics(),
      }),
    };
  }

  if (options.keySlots) {
    const keySlots = options.keySlots;
    adapters.walletSign = {
      get: async (params) => {
        const slotId = requireString(params.slotId, 'slotId');
        const resolver = keySlots[slotId];
        if (resolver === undefined) {
          throw new Error(`unknown key slot: ${slotId}`);
        }
        const key =
          typeof resolver === 'function' ? await resolver() : resolver;
        return key instanceof Uint8Array ? key : new Uint8Array(key);
      },
    };
  }

  return adapters;
}

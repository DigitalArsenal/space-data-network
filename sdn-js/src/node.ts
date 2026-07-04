/**
 * SDN Node - Main P2P node implementation for browsers
 */

import { createLibp2p, Libp2p } from "libp2p";
import { serviceCapabilities } from "@libp2p/interface";
import { webSockets } from "@libp2p/websockets";
import { all as wsFilters } from "@libp2p/websockets/filters";
import { webTransport } from "@libp2p/webtransport";
import { webRTC } from "@spacedatanetwork/libp2p-webrtc-v1";
import { circuitRelayTransport } from "@libp2p/circuit-relay-v2";
import { bootstrap } from "@libp2p/bootstrap";
import { identify } from "@libp2p/identify";
import { gossipsub, GossipSub } from "@chainsafe/libp2p-gossipsub";
import { noise } from "@chainsafe/libp2p-noise";
import { yamux } from "@chainsafe/libp2p-yamux";
import { kadDHT } from "@libp2p/kad-dht";
import { multiaddr } from "@multiformats/multiaddr";
import { CID } from "multiformats/cid";
import { peerIdFromKeys, peerIdFromString } from "@libp2p/peer-id";
import { isUint8ArrayList, Uint8ArrayList } from "uint8arraylist";

import { unmarshalPrivateKey } from "@libp2p/crypto/keys";

import { SDNStorage, StoredRecord, QueryFilter } from "./storage";
import {
  FlatSQLEngineRecordStore,
  type SnapshotPersistence,
} from "./engine-record-store";
import type { LocalFlatSqlSchema } from "./local-flatsql";
import { getBootstrapRelays, EdgeDiscovery } from "./edge-discovery";
import { SchemaName, SUPPORTED_SCHEMAS } from "./schemas";
import { initHDWallet } from "./crypto/hd-wallet";
import type { DerivedIdentity } from "./crypto/types";
import {
  requestEncryptedModuleBundle,
  requestModuleGrant,
  type DiscoveredProvider,
  type ModuleDeliveryEvent,
  type ModuleDeliveryObserver,
  type ModuleGrantRequestOptions,
  type ModuleGrantResult,
  type ModuleDeliveryTransport,
  type EncryptedModuleBundleResult,
  MODULE_DELIVERY_PROTOCOL_ID,
} from "./module-delivery";
import {
  requestFlatSqlSyncChunk,
  requestFlatSqlSyncManifest,
  type FlatSqlSyncChunk,
  type FlatSqlSyncManifest,
  type FlatSqlSyncQuery,
} from "./flatsql-sync";

const TOPIC_PREFIX = "/spacedatanetwork/sds/";
type Libp2pCreateOptions = NonNullable<Parameters<typeof createLibp2p>[0]>;
export const LEGACY_ID_EXCHANGE_PROTOCOL =
  "/space-data-network/id-exchange/1.0.0";
// Public IPFS bootstrap peers + SDN relay can be combined for browser interop.
export const IPFS_BOOTSTRAP_PEERS = [
  "/dnsaddr/bootstrap.libp2p.io/p2p/QmNnooDu7bfjPFoTZYxMNLWUQJyrVwtbZg5gBMjTezGAJN",
  "/dnsaddr/bootstrap.libp2p.io/p2p/QmbLHAnMoJPWSCR5Zhtx6BHJX9KiKNN6tpvbUcqanj75Nb",
  "/dnsaddr/bootstrap.libp2p.io/p2p/QmZa1sAxajnQjVM8WjWXoMbmPd7NsWhfKsPkErzpm9wGkp",
  "/dnsaddr/bootstrap.libp2p.io/p2p/QmQCU2EcMqAqQPR2i9bChDtGNJchTbq5TbXJJ16u19uLTa",
  "/dnsaddr/bootstrap.libp2p.io/p2p/QmcZf59bWwK5XFi76CZX8cbJ4BhTzzA3gU1ZjYZcYW3dwt",
] as const;

function identifyCapabilityOnly() {
  return () => ({
    [serviceCapabilities]: ["@libp2p/identify"],
    [Symbol.toStringTag]: "sdn-js-identify-capability",
  });
}

type StreamChunk = Uint8Array | Uint8ArrayList | ArrayBufferView | ArrayBuffer;

export interface SDNConfig {
  edgeRelays?: string[];
  bootstrapPeers?: string[];
  includeIPFSBootstrap?: boolean;
  /** Enable libp2p Identify service. Disabled by default for lean browser module delivery. */
  enableIdentify?: boolean;
  ipfsApiBaseUrl?: string;
  ipfsGatewayBaseUrl?: string;
  ipfsFetchTimeoutMs?: number;
  idExchangeProtocol?: string;
  enableStorage?: boolean;
  storeName?: string;
  /**
   * Record store backend. 'flatsql' (default) is THE node store: the
   * FlatSQL-WASM engine record store (loop D.1 — same engine, SQL, and
   * source-partition layout as sdn-server), journal-persisted to IndexedDB
   * under storeName by default or through storagePersistence when given;
   * 'indexeddb' keeps the legacy SDNStorage (slated for removal in D.5);
   * or pass a ready store instance.
   */
  storageBackend?: 'flatsql' | 'indexeddb' | NodeRecordStorage;
  /** Envelope journal persistence for the engine store (e.g. HeliaSnapshotPersistence). */
  storagePersistence?: SnapshotPersistence;
  /**
   * SDS standards mirrored into per-standard engine databases
   * (per-provider `registerSource` shadow tables + unified views, mirroring
   * the server layout). Optional: the envelope store works without them.
   */
  storageSchemas?: LocalFlatSqlSchema[];
  /** Private key for auth challenge signing (32 bytes Ed25519 seed) */
  privateKey?: Uint8Array;
  /** Full HD wallet-derived identity (secp256k1 for PeerID + Ed25519 for auth) */
  identity?: DerivedIdentity;
  /** Enable relay load probing for load balancing (default: true) */
  enableRelayProbing?: boolean;
  /** Interval between relay probes in ms (default: 30000) */
  relayProbeIntervalMs?: number;
}

export interface SDNNodeEvents {
  onMessage?: (schema: SchemaName, data: unknown, from: string) => void;
  onPeerConnected?: (peerId: string) => void;
  onPeerDisconnected?: (peerId: string) => void;
  onModuleDeliveryEvent?: (event: ModuleDeliveryEvent) => void;
}

/** The record-store surface SDNNode drives (SDNStorage and FlatSQLStorage). */
export interface NodeRecordStorage {
  store(
    schema: SchemaName | string,
    data: Uint8Array,
    peerId: string,
    signature: Uint8Array,
  ): Promise<string>;
  get(schema: SchemaName | string, cid: string): Promise<StoredRecord | null>;
  query(schema: SchemaName | string, filter?: QueryFilter): Promise<StoredRecord[]>;
  close(): Promise<void>;
}

export class SDNNode {
  private libp2p: Libp2p | null = null;
  private storage: NodeRecordStorage | null = null;
  private config: SDNConfig;
  private events: SDNNodeEvents;
  private subscriptions: Map<string, AbortController> = new Map();
  private privateKey: Uint8Array | null = null;
  private cryptoReady = false;
  private discovery: EdgeDiscovery | null = null;

  private constructor(config: SDNConfig, events: SDNNodeEvents = {}) {
    this.config = config;
    this.events = events;
    this.privateKey = config.privateKey ?? null;
  }

  /**
   * Create and start a new SDN node
   */
  static async create(
    config: SDNConfig = {},
    events: SDNNodeEvents = {},
  ): Promise<SDNNode> {
    const node = new SDNNode(config, events);

    // Try to load HD wallet module for signing
    node.cryptoReady = await initHDWallet();
    if (!node.cryptoReady) {
      console.warn(
        "HD Wallet WASM not loaded - auth challenge signing unavailable",
      );
    }

    await node.init();
    return node;
  }

  private async init(): Promise<void> {
    const explicitRelays = normalizeConfiguredRelays(this.config.edgeRelays);
    const hasExplicitRelays = explicitRelays.length > 0;

    // Build bootstrap list from SDN relays and IPFS public bootstrappers.
    const rawRelays = hasExplicitRelays
      ? explicitRelays
      : await getBootstrapRelays();

    // Create discovery instance for load-balanced relay selection
    this.discovery = new EdgeDiscovery(rawRelays);
    if (shouldProbeRelays(this.config, hasExplicitRelays)) {
      try {
        await this.discovery.probeAllRelays();
      } catch {
        // Non-fatal: fall back to unprobed scoring
      }
      this.discovery.startProbing(this.config.relayProbeIntervalMs ?? 30_000);
    }

    const relays = shouldProbeRelays(this.config, hasExplicitRelays)
      ? this.discovery.getBestRelays(rawRelays.length)
      : rawRelays;
    const bootstrapList = resolveBootstrapList(
      relays,
      this.config,
      hasExplicitRelays,
    );

    // Build libp2p options
    const services: NonNullable<
      Parameters<typeof createLibp2p>[0]
    >["services"] = {
      pubsub: gossipsub({
        allowPublishToZeroTopicPeers: true,
        emitSelf: false,
      }),
      dht: kadDHT({
        clientMode: true,
      }),
    };
    services.identify =
      this.config.enableIdentify === true
        ? identify()
        : identifyCapabilityOnly();

    const libp2pOpts: Libp2pCreateOptions = {
      transports: [
        webSockets({ filter: wsFilters }),
        webTransport(),
        webRTC() as unknown as ReturnType<typeof webTransport>,
        circuitRelayTransport({
          discoverRelays: 100,
        }),
      ],
      connectionEncryption: [noise()],
      streamMuxers: [yamux()],
      peerDiscovery: [bootstrap({ list: bootstrapList })],
      services,
    };

    // If an HD wallet identity is provided, use its secp256k1 key for deterministic PeerID
    if (this.config.identity?.identityKey) {
      const rawKey = this.config.identity.identityKey.privateKey;
      const privateKeyBytes = marshalSecp256k1PrivateKey(rawKey);
      const publicKeyBytes = marshalSecp256k1PublicKey(
        this.config.identity.identityKey.publicKey,
      );
      libp2pOpts.peerId = await peerIdFromKeys(publicKeyBytes, privateKeyBytes);
      libp2pOpts.privateKey = await unmarshalPrivateKey(privateKeyBytes);
      // Also set the Ed25519 signing key for message auth
      if (!this.privateKey) {
        this.privateKey = this.config.identity.signingKey.privateKey;
      }
    }

    // Initialize libp2p
    this.libp2p = await createLibp2p(libp2pOpts);

    // Initialize storage if enabled. The FlatSQL-WASM engine record store is
    // THE node store (loop D.1) — no opt-in flag; pass storageBackend:
    // 'indexeddb' for the legacy SDNStorage (until D.5) or a ready
    // NodeRecordStorage instance.
    if (this.config.enableStorage !== false) {
      const backend = this.config.storageBackend ?? "flatsql";
      if (backend === "indexeddb") {
        this.storage = await SDNStorage.open(
          this.config.storeName || "sdn-store",
        );
      } else if (backend === "flatsql") {
        this.storage = await FlatSQLEngineRecordStore.open({
          persistence: this.config.storagePersistence,
          persistenceKey: this.config.storeName || "sdn-store",
          schemas: this.config.storageSchemas,
        });
      } else {
        this.storage = backend;
      }
    }

    // Setup event handlers
    this.libp2p.addEventListener("peer:connect", (evt) => {
      const peerId = evt.detail.toString();
      this.events.onPeerConnected?.(peerId);
    });

    this.libp2p.addEventListener("peer:disconnect", (evt) => {
      const peerId = evt.detail.toString();
      this.events.onPeerDisconnected?.(peerId);
    });

    // Start the node
    await this.libp2p.start();
  }

  /**
   * Get the node's peer ID
   */
  get peerId(): string {
    return this.libp2p?.peerId.toString() ?? "";
  }

  /**
   * Get list of connected peers
   */
  get peers(): string[] {
    return this.libp2p?.getPeers().map((p) => p.toString()) ?? [];
  }

  /**
   * Publish data to a schema topic
   */
  /**
   * Publish raw binary payloads (e.g. signed PNM/SDS FlatBuffers) to a
   * schema topic — publish() JSON-wraps objects; FlatBuffer envelopes must
   * go out verbatim.
   */
  async publishRaw(schema: SchemaName | string, data: Uint8Array): Promise<string> {
    if (!this.libp2p) {
      throw new Error("Node not initialized");
    }
    const topicName = TOPIC_PREFIX + schema;
    const pubsub = this.libp2p.services.pubsub as GossipSub;
    await pubsub.publish(topicName, data);
    return topicName;
  }

  async publish(schema: SchemaName, data: object): Promise<string> {
    if (!this.libp2p) {
      throw new Error("Node not initialized");
    }

    // Convert to binary (in production, use FlatBuffers via WASM)
    const jsonStr = JSON.stringify(data);
    const binary = new TextEncoder().encode(jsonStr);

    // Publish to topic
    const topicName = TOPIC_PREFIX + schema;
    const pubsub = this.libp2p.services.pubsub as GossipSub;
    await pubsub.publish(topicName, binary);

    // Store locally
    let cid = "";
    if (this.storage) {
      cid = await this.storage.store(
        schema,
        binary,
        this.peerId,
        new Uint8Array(0),
      );
    }

    return cid;
  }

  /**
   * Set the private key for auth challenge signing
   */
  setPrivateKey(key: Uint8Array): void {
    if (key.length !== 32 && key.length !== 64) {
      throw new Error(
        "Invalid private key length - expected 32 (seed) or 64 bytes",
      );
    }
    this.privateKey = key.length === 64 ? key.slice(0, 32) : key;
  }

  /**
   * Check if auth challenge signing is available
   */
  get canSign(): boolean {
    return this.cryptoReady && this.privateKey !== null;
  }

  /**
   * Subscribe to a schema topic
   */
  async subscribe(
    schema: SchemaName,
    handler?: (data: unknown, from: string) => void,
  ): Promise<void> {
    if (!this.libp2p) {
      throw new Error("Node not initialized");
    }

    const topicName = TOPIC_PREFIX + schema;
    const pubsub = this.libp2p.services.pubsub as GossipSub;

    // Subscribe to the topic
    pubsub.subscribe(topicName);

    // Create abort controller for this subscription
    const controller = new AbortController();
    this.subscriptions.set(schema, controller);

    // Listen for messages
    pubsub.addEventListener(
      "message",
      (evt: CustomEvent) => {
        if (evt.detail.topic !== topicName) return;

        const msgData = evt.detail.data;
        if (msgData.length === 0) return;

        // Decode JSON (in production, use FlatBuffers via WASM)
        const jsonStr = new TextDecoder().decode(msgData);
        let parsed: unknown;
        try {
          parsed = JSON.parse(jsonStr);
        } catch {
          console.warn("Failed to parse message");
          return;
        }

        const from = evt.detail.from.toString();

        // Store locally. Generic SDS messages carry no detached payload
        // signature today; GossipSub StrictSign covers the message with the
        // publisher's libp2p identity key, so that transport signature is
        // recorded instead of an empty placeholder. Payload-level Ed25519
        // envelopes (as used by PNM and module delivery) are the upgrade
        // path for end-to-end provenance independent of the transport.
        if (this.storage) {
          const transportSignature =
            evt.detail.signature instanceof Uint8Array
              ? evt.detail.signature
              : new Uint8Array(0);
          this.storage
            .store(schema, msgData, from, transportSignature)
            .catch(console.error);
        }

        // Call handlers
        handler?.(parsed, from);
        this.events.onMessage?.(schema, parsed, from);
      },
      { signal: controller.signal },
    );
  }

  /**
   * Unsubscribe from a schema topic
   */
  async unsubscribe(schema: SchemaName): Promise<void> {
    if (!this.libp2p) return;

    const topicName = TOPIC_PREFIX + schema;
    const pubsub = this.libp2p.services.pubsub as GossipSub;

    pubsub.unsubscribe(topicName);

    const controller = this.subscriptions.get(schema);
    if (controller) {
      controller.abort();
      this.subscriptions.delete(schema);
    }
  }

  /**
   * Query local storage for records
   */
  async query(
    schema: SchemaName,
    filter?: { peerId?: string; since?: Date },
  ): Promise<StoredRecord[]> {
    if (!this.storage) {
      throw new Error("Storage not enabled");
    }

    return this.storage.query(schema, filter);
  }

  /**
   * Get a specific record by CID
   */
  async get(schema: SchemaName, cid: string): Promise<StoredRecord | null> {
    if (!this.storage) {
      throw new Error("Storage not enabled");
    }

    return this.storage.get(schema, cid);
  }

  /**
   * Connect to a specific peer
   */
  async dial(addr: string): Promise<void> {
    if (!this.libp2p) {
      throw new Error("Node not initialized");
    }

    const ma = multiaddr(addr);
    await this.libp2p.dial(ma);
  }

  /**
   * Dial through a relay to reach a peer behind a firewall
   */
  async dialThroughRelay(
    relayAddr: string,
    targetPeerId: string,
  ): Promise<void> {
    if (!this.libp2p) {
      throw new Error("Node not initialized");
    }

    const relayMa = multiaddr(relayAddr);
    const circuitAddr = relayMa.encapsulate(`/p2p-circuit/p2p/${targetPeerId}`);
    await this.libp2p.dial(circuitAddr);
  }

  /**
   * Dial a specific protocol through a relay circuit and return the first reply chunk.
   */
  async dialProtocol(
    targetPeerId: string,
    protocolId: string,
    payload: Uint8Array,
    candidateAddrs: string[] = [],
  ): Promise<Uint8Array> {
    if (!this.libp2p) {
      throw new Error("Node not initialized");
    }

    const payloadBytes = payload.slice();
    const dialTargets = candidateAddrs.map((addr) =>
      normalizeDialTarget(addr, targetPeerId),
    );
    let lastError: unknown = null;

    if (dialTargets.length === 0) {
      const stream = await this.libp2p.dialProtocol(
        peerIdFromString(targetPeerId) as unknown as Parameters<
          Libp2p["dialProtocol"]
        >[0],
        protocolId,
      );
      return exchangeStream(stream, payloadBytes);
    }

    for (const dialTarget of dialTargets) {
      try {
        const stream = await this.libp2p.dialProtocol(
          multiaddr(dialTarget),
          protocolId,
        );
        return await exchangeStream(stream, payloadBytes);
      } catch (error) {
        lastError = error;
      }
    }

    throw new Error(
      `failed to dial ${protocolId} for ${targetPeerId}: ${formatError(lastError)}`,
    );
  }

  async readFlatSqlSyncChunk(query: FlatSqlSyncQuery): Promise<FlatSqlSyncChunk> {
    return requestFlatSqlSyncChunk(this, query);
  }

  async openFlatSqlSyncManifest(query: FlatSqlSyncQuery): Promise<FlatSqlSyncManifest> {
    return requestFlatSqlSyncManifest(this, query);
  }

  async dialProtocolThroughRelay(
    relayAddr: string,
    targetPeerId: string,
    protocolId: string,
    payload: Uint8Array | string,
  ): Promise<Uint8Array> {
    const payloadBytes =
      typeof payload === "string" ? new TextEncoder().encode(payload) : payload;
    return this.dialProtocol(targetPeerId, protocolId, payloadBytes, [
      relayAddr,
    ]);
  }

  /**
   * Compatibility helper for the historical id-exchange relay probe script.
   */
  async idExchangeThroughRelay(
    relayAddr: string,
    targetPeerId: string,
    message = "ping",
  ): Promise<string> {
    const response = await this.dialProtocolThroughRelay(
      relayAddr,
      targetPeerId,
      this.config.idExchangeProtocol ?? LEGACY_ID_EXCHANGE_PROTOCOL,
      message,
    );
    return new TextDecoder().decode(response);
  }

  async discoverProviders(discoveryCID: string): Promise<DiscoveredProvider[]> {
    if (!this.libp2p) {
      throw new Error("Node not initialized");
    }

    const providers: DiscoveredProvider[] = [];
    const seen = new Set<string>();
    const timeout = AbortSignal.timeout(10_000);

    for await (const provider of this.libp2p.contentRouting.findProviders(
      CID.parse(discoveryCID),
      { signal: timeout },
    )) {
      const peerId = provider.id.toString();
      const multiaddrs = provider.multiaddrs.map((addr) => addr.toString());
      const key = `${peerId}:${multiaddrs.join(",")}`;
      if (seen.has(key)) {
        continue;
      }
      seen.add(key);
      providers.push({ peerId, multiaddrs });
    }

    return providers;
  }

  async fetchCIDBytes(cid: string): Promise<Uint8Array> {
    const ipfsApiBaseUrl = normalizeOptionalString(this.config.ipfsApiBaseUrl);
    const ipfsGatewayBaseUrl = normalizeOptionalString(
      this.config.ipfsGatewayBaseUrl,
    );
    let fetchPromise: Promise<Uint8Array>;
    if (ipfsApiBaseUrl) {
      fetchPromise = fetchCIDBytesFromIPFSApi(ipfsApiBaseUrl, cid);
    } else if (ipfsGatewayBaseUrl) {
      fetchPromise = fetchCIDBytesFromIPFSGateway(ipfsGatewayBaseUrl, cid);
    } else {
      throw new Error(
        "CID fetch requires ipfsApiBaseUrl or ipfsGatewayBaseUrl in the browser bundle",
      );
    }

    return withOptionalTimeout(
      fetchPromise,
      resolveHeliaFetchTimeoutMs(this.config),
    );
  }

  async requestModuleGrant(
    options: Omit<ModuleGrantRequestOptions, "requesterIdentity"> & {
      requesterIdentity?: ModuleGrantRequestOptions["requesterIdentity"];
    },
  ): Promise<ModuleGrantResult> {
    const requesterIdentity = options.requesterIdentity ?? this.config.identity;
    if (!requesterIdentity) {
      throw new Error("requester identity is required");
    }
    const observer = resolveModuleDeliveryObserver(
      options.observer,
      this.events,
    );
    return requestModuleGrant(this as ModuleDeliveryTransport, {
      ...options,
      requesterIdentity,
      observer,
    });
  }

  async requestEncryptedModuleBundle(
    options: Omit<ModuleGrantRequestOptions, "requesterIdentity"> & {
      requesterIdentity?: ModuleGrantRequestOptions["requesterIdentity"];
    },
  ): Promise<EncryptedModuleBundleResult> {
    const requesterIdentity = options.requesterIdentity ?? this.config.identity;
    if (!requesterIdentity) {
      throw new Error("requester identity is required");
    }
    const observer = resolveModuleDeliveryObserver(
      options.observer,
      this.events,
    );
    return requestEncryptedModuleBundle(this as ModuleDeliveryTransport, {
      ...options,
      requesterIdentity,
      observer,
    });
  }

  /**
   * Stop the node
   */
  async stop(): Promise<void> {
    // Stop relay probing
    this.discovery?.stopProbing();

    // Cancel all subscriptions
    for (const controller of this.subscriptions.values()) {
      controller.abort();
    }
    this.subscriptions.clear();

    // Close storage
    if (this.storage) {
      await this.storage.close();
    }

    // Stop libp2p
    if (this.libp2p) {
      await this.libp2p.stop();
    }
  }

  /**
   * Get the EdgeDiscovery instance for advanced relay management.
   */
  getDiscovery(): EdgeDiscovery | null {
    return this.discovery;
  }

  /**
   * Get supported schemas
   */
  static get schemas(): readonly SchemaName[] {
    return SUPPORTED_SCHEMAS;
  }

  static get ipfsBootstrapPeers(): readonly string[] {
    return IPFS_BOOTSTRAP_PEERS;
  }

  static get moduleDeliveryProtocolId(): string {
    return MODULE_DELIVERY_PROTOCOL_ID;
  }
}

function resolveBootstrapList(
  relays: string[],
  config: SDNConfig,
  hasExplicitRelays: boolean,
): string[] {
  const includeIPFS = hasExplicitRelays
    ? config.includeIPFSBootstrap === true
    : config.includeIPFSBootstrap !== false;
  const configured = config.bootstrapPeers ?? [];
  const combined = [
    ...(includeIPFS ? IPFS_BOOTSTRAP_PEERS : []),
    ...relays,
    ...configured,
  ];

  return Array.from(new Set(combined.filter((addr) => addr.trim().length > 0)));
}

function normalizeConfiguredRelays(relays?: string[]): string[] {
  if (!Array.isArray(relays)) {
    return [];
  }
  return relays
    .map((relay) => String(relay || "").trim())
    .filter((relay) => relay.length > 0);
}

function shouldProbeRelays(
  config: SDNConfig,
  hasExplicitRelays: boolean,
): boolean {
  if (hasExplicitRelays) {
    return config.enableRelayProbing === true;
  }
  return config.enableRelayProbing !== false;
}

function resolveHeliaFetchTimeoutMs(config: SDNConfig): number {
  const configuredTimeout = Number(config.ipfsFetchTimeoutMs);
  if (Number.isFinite(configuredTimeout) && configuredTimeout > 0) {
    return configuredTimeout;
  }
  return 0;
}

function normalizeOptionalString(value: unknown): string {
  return typeof value === "string" ? value.trim() : "";
}

async function fetchCIDBytesFromIPFSApi(
  baseUrl: string,
  cid: string,
): Promise<Uint8Array> {
  if (typeof fetch !== "function") {
    throw new Error("IPFS API CID fetch requires fetch().");
  }
  const endpoint = new URL(
    `${baseUrl.replace(/\/+$/g, "")}/cat`,
    typeof location !== "undefined" ? location.href : "http://localhost",
  );
  endpoint.searchParams.set("arg", cid);
  const response = await fetch(endpoint, {
    method: "POST",
    headers: { accept: "application/octet-stream" },
  });
  if (!response.ok) {
    throw new Error(
      `IPFS API CID fetch failed: ${response.status} ${response.statusText}`,
    );
  }
  return new Uint8Array(await response.arrayBuffer());
}

async function fetchCIDBytesFromIPFSGateway(
  baseUrl: string,
  cid: string,
): Promise<Uint8Array> {
  if (typeof fetch !== "function") {
    throw new Error("IPFS gateway CID fetch requires fetch().");
  }
  const endpoint = new URL(
    `${baseUrl.replace(/\/+$/g, "")}/${encodeURIComponent(cid)}`,
    typeof location !== "undefined" ? location.href : "http://localhost",
  );
  const response = await fetch(endpoint, {
    method: "GET",
    headers: { accept: "application/octet-stream" },
  });
  if (!response.ok) {
    throw new Error(
      `IPFS gateway CID fetch failed: ${response.status} ${response.statusText}`,
    );
  }
  return new Uint8Array(await response.arrayBuffer());
}

async function withOptionalTimeout<T>(
  promise: Promise<T>,
  timeoutMs: number,
): Promise<T> {
  if (!(timeoutMs > 0)) {
    return promise;
  }

  let timeoutId: ReturnType<typeof setTimeout> | null = null;
  try {
    return await Promise.race([
      promise,
      new Promise<T>((_, reject) => {
        timeoutId = setTimeout(() => {
          reject(new Error(`helia fetch timed out after ${timeoutMs}ms`));
        }, timeoutMs);
      }),
    ]);
  } finally {
    if (timeoutId !== null) {
      clearTimeout(timeoutId);
    }
  }
}

function cloneToLocalUint8Array(chunk: StreamChunk): Uint8Array {
  if (chunk instanceof Uint8Array) {
    return new Uint8Array(chunk);
  }
  if (isUint8ArrayList(chunk)) {
    return new Uint8Array(chunk.subarray());
  }
  if (chunk instanceof ArrayBuffer) {
    return new Uint8Array(chunk.slice(0));
  }
  if (ArrayBuffer.isView(chunk)) {
    return new Uint8Array(
      chunk.buffer.slice(chunk.byteOffset, chunk.byteOffset + chunk.byteLength),
    );
  }
  throw new Error("stream chunk must be Uint8Array-compatible bytes");
}

async function exchangeStream(
  stream: Awaited<ReturnType<Libp2p["dialProtocol"]>>,
  payloadBytes: Uint8Array,
): Promise<Uint8Array> {
  try {
    await stream.sink(
      (async function* source() {
        yield cloneToLocalUint8Array(payloadBytes);
      })(),
    );

    const chunks: Uint8Array[] = [];
    for await (const chunk of stream.source) {
      chunks.push(cloneToLocalUint8Array(chunk as StreamChunk));
    }
    return concatBytes(chunks);
  } finally {
    try {
      await stream.close();
    } catch {
      // Ignore close errors in probe/test path.
    }
  }
}

function normalizeDialTarget(addr: string, targetPeerId: string): string {
  const trimmed = addr.trim();
  if (!trimmed) {
    return trimmed;
  }
  if (trimmed.includes("/p2p-circuit")) {
    return trimmed.includes(`/p2p/${targetPeerId}`)
      ? trimmed
      : `${trimmed}/p2p/${targetPeerId}`;
  }
  if (trimmed.includes(`/p2p/${targetPeerId}`)) {
    return trimmed;
  }
  if (trimmed.includes("/p2p/")) {
    return `${trimmed}/p2p-circuit/p2p/${targetPeerId}`;
  }
  return `${trimmed}/p2p/${targetPeerId}`;
}

function resolveModuleDeliveryObserver(
  observer: ModuleDeliveryObserver | undefined,
  events: SDNNodeEvents,
): ModuleDeliveryObserver | undefined {
  if (observer) {
    return observer;
  }
  if (!events.onModuleDeliveryEvent) {
    return undefined;
  }
  return {
    onEvent(event) {
      events.onModuleDeliveryEvent?.(event);
    },
  };
}

function formatError(error: unknown): string {
  if (error instanceof Error) {
    return error.message;
  }
  return String(error);
}

/**
 * Marshal a 32-byte secp256k1 private key into the libp2p protobuf format.
 * KeyType=2 (Secp256k1), Data=32-byte raw key.
 */
function marshalSecp256k1PrivateKey(rawKey: Uint8Array): Uint8Array {
  const buf = new Uint8Array(36);
  buf[0] = 0x08; // field 1 tag
  buf[1] = 0x02; // KeyType.Secp256k1
  buf[2] = 0x12; // field 2 tag
  buf[3] = 0x20; // length = 32
  buf.set(rawKey, 4);
  return buf;
}

/**
 * Marshal a 33-byte compressed secp256k1 public key into the libp2p protobuf format.
 * KeyType=2 (Secp256k1), Data=33-byte compressed public key.
 */
function marshalSecp256k1PublicKey(rawKey: Uint8Array): Uint8Array {
  const buf = new Uint8Array(37);
  buf[0] = 0x08; // field 1 tag
  buf[1] = 0x02; // KeyType.Secp256k1
  buf[2] = 0x12; // field 2 tag
  buf[3] = 0x21; // length = 33
  buf.set(rawKey, 4);
  return buf;
}

function concatBytes(chunks: Uint8Array[]): Uint8Array {
  if (chunks.length === 0) {
    return new Uint8Array(0);
  }
  if (chunks.length === 1) {
    return chunks[0];
  }

  let totalLength = 0;
  for (const chunk of chunks) {
    totalLength += chunk.length;
  }

  const out = new Uint8Array(totalLength);
  let offset = 0;
  for (const chunk of chunks) {
    out.set(chunk, offset);
    offset += chunk.length;
  }
  return out;
}

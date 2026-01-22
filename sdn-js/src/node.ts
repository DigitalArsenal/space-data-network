/**
 * SDN Node - Main P2P node implementation for browsers
 */

import { createLibp2p, Libp2p } from 'libp2p';
import { webSockets } from '@libp2p/websockets';
import { all as wsFilters } from '@libp2p/websockets/filters';
import { webTransport } from '@libp2p/webtransport';
import { circuitRelayTransport } from '@libp2p/circuit-relay-v2';
import { bootstrap } from '@libp2p/bootstrap';
import { identify } from '@libp2p/identify';
import { gossipsub, GossipSub } from '@chainsafe/libp2p-gossipsub';
import { noise } from '@chainsafe/libp2p-noise';
import { yamux } from '@chainsafe/libp2p-yamux';
import { kadDHT } from '@libp2p/kad-dht';
import { multiaddr } from '@multiformats/multiaddr';

import { SDNStorage, StoredRecord } from './storage';
import { getBootstrapRelays } from './edge-discovery';
import { SchemaName, SUPPORTED_SCHEMAS } from './schemas';

const TOPIC_PREFIX = '/spacedatanetwork/sds/';

export interface SDNConfig {
  edgeRelays?: string[];
  enableStorage?: boolean;
  storeName?: string;
}

export interface SDNNodeEvents {
  onMessage?: (schema: SchemaName, data: unknown, from: string) => void;
  onPeerConnected?: (peerId: string) => void;
  onPeerDisconnected?: (peerId: string) => void;
}

export class SDNNode {
  private libp2p: Libp2p | null = null;
  private storage: SDNStorage | null = null;
  private config: SDNConfig;
  private events: SDNNodeEvents;
  private subscriptions: Map<string, AbortController> = new Map();

  private constructor(config: SDNConfig, events: SDNNodeEvents = {}) {
    this.config = config;
    this.events = events;
  }

  /**
   * Create and start a new SDN node
   */
  static async create(config: SDNConfig = {}, events: SDNNodeEvents = {}): Promise<SDNNode> {
    const node = new SDNNode(config, events);
    await node.init();
    return node;
  }

  private async init(): Promise<void> {
    // Get bootstrap relays
    const bootstrapList = this.config.edgeRelays || await getBootstrapRelays();

    // Initialize libp2p
    this.libp2p = await createLibp2p({
      transports: [
        webSockets({ filter: wsFilters }),
        webTransport(),
        circuitRelayTransport({
          discoverRelays: 100,
        }),
      ],
      connectionEncryption: [noise()],
      streamMuxers: [yamux()],
      peerDiscovery: [
        bootstrap({ list: bootstrapList }),
      ],
      services: {
        identify: identify(),
        pubsub: gossipsub({
          allowPublishToZeroTopicPeers: true,
          emitSelf: false,
        }),
        dht: kadDHT({
          clientMode: true,
        }),
      },
    });

    // Initialize storage if enabled
    if (this.config.enableStorage !== false) {
      this.storage = await SDNStorage.open(this.config.storeName || 'sdn-store');
    }

    // Setup event handlers
    this.libp2p.addEventListener('peer:connect', (evt) => {
      const peerId = evt.detail.toString();
      this.events.onPeerConnected?.(peerId);
    });

    this.libp2p.addEventListener('peer:disconnect', (evt) => {
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
    return this.libp2p?.peerId.toString() ?? '';
  }

  /**
   * Get list of connected peers
   */
  get peers(): string[] {
    return this.libp2p?.getPeers().map(p => p.toString()) ?? [];
  }

  /**
   * Publish data to a schema topic
   */
  async publish(schema: SchemaName, data: object): Promise<string> {
    if (!this.libp2p) {
      throw new Error('Node not initialized');
    }

    // Convert to binary (in production, use FlatBuffers via WASM)
    const jsonStr = JSON.stringify(data);
    const binary = new TextEncoder().encode(jsonStr);

    // Create message with signature placeholder
    // In production, sign with Ed25519 via WASM
    const signature = new Uint8Array(64); // Placeholder
    const message = new Uint8Array(binary.length + signature.length);
    message.set(binary, 0);
    message.set(signature, binary.length);

    // Publish to topic
    const topicName = TOPIC_PREFIX + schema;
    const pubsub = this.libp2p.services.pubsub as GossipSub;
    await pubsub.publish(topicName, message);

    // Store locally
    let cid = '';
    if (this.storage) {
      cid = await this.storage.store(schema, binary, this.peerId, signature);
    }

    return cid;
  }

  /**
   * Subscribe to a schema topic
   */
  async subscribe(schema: SchemaName, handler?: (data: unknown, from: string) => void): Promise<void> {
    if (!this.libp2p) {
      throw new Error('Node not initialized');
    }

    const topicName = TOPIC_PREFIX + schema;
    const pubsub = this.libp2p.services.pubsub as GossipSub;

    // Subscribe to the topic
    pubsub.subscribe(topicName);

    // Create abort controller for this subscription
    const controller = new AbortController();
    this.subscriptions.set(schema, controller);

    // Listen for messages
    pubsub.addEventListener('message', (evt: CustomEvent) => {
      if (evt.detail.topic !== topicName) return;

      const data = evt.detail.data;
      if (data.length < 65) return; // Too short (needs data + signature)

      // Extract data and signature
      const msgData = data.slice(0, data.length - 64);
      const signature = data.slice(data.length - 64);

      // Decode JSON (in production, use FlatBuffers via WASM)
      const jsonStr = new TextDecoder().decode(msgData);
      let parsed: unknown;
      try {
        parsed = JSON.parse(jsonStr);
      } catch {
        console.warn('Failed to parse message');
        return;
      }

      const from = evt.detail.from.toString();

      // Store locally
      if (this.storage) {
        this.storage.store(schema, msgData, from, signature).catch(console.error);
      }

      // Call handlers
      handler?.(parsed, from);
      this.events.onMessage?.(schema, parsed, from);
    }, { signal: controller.signal });
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
  async query(schema: SchemaName, filter?: { peerId?: string; since?: Date }): Promise<StoredRecord[]> {
    if (!this.storage) {
      throw new Error('Storage not enabled');
    }

    return this.storage.query(schema, filter);
  }

  /**
   * Get a specific record by CID
   */
  async get(schema: SchemaName, cid: string): Promise<StoredRecord | null> {
    if (!this.storage) {
      throw new Error('Storage not enabled');
    }

    return this.storage.get(schema, cid);
  }

  /**
   * Connect to a specific peer
   */
  async dial(addr: string): Promise<void> {
    if (!this.libp2p) {
      throw new Error('Node not initialized');
    }

    const ma = multiaddr(addr);
    await this.libp2p.dial(ma);
  }

  /**
   * Dial through a relay to reach a peer behind a firewall
   */
  async dialThroughRelay(relayAddr: string, targetPeerId: string): Promise<void> {
    if (!this.libp2p) {
      throw new Error('Node not initialized');
    }

    const relayMa = multiaddr(relayAddr);
    const circuitAddr = relayMa.encapsulate(`/p2p-circuit/p2p/${targetPeerId}`);
    await this.libp2p.dial(circuitAddr);
  }

  /**
   * Stop the node
   */
  async stop(): Promise<void> {
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
   * Get supported schemas
   */
  static get schemas(): readonly SchemaName[] {
    return SUPPORTED_SCHEMAS;
  }
}

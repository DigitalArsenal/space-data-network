/**
 * SDN Subscription Manager - Manages data type subscriptions with filtering
 *
 * Provides a high-level API for subscribing to specific SDS data types
 * from selected peers with optional encryption and streaming modes.
 */

import { SchemaName, SUPPORTED_SCHEMAS, getTopicName } from './schemas';

/**
 * Query filter for field-level filtering
 */
export interface QueryFilter {
  field: string;
  operator: 'eq' | 'ne' | 'gt' | 'gte' | 'lt' | 'lte' | 'contains' | 'startsWith' | 'endsWith' | 'in' | 'notIn';
  value: unknown;
}

/**
 * Subscription configuration for data types
 */
export interface SubscriptionConfig {
  /** Data types to subscribe to, e.g., ["OMM.fbs", "CDM.fbs", "EPM.fbs"] */
  dataTypes: SchemaName[];
  /** Peer IDs to receive data from, or ["all"] for all peers */
  sourcePeers: string[];
  /** Whether to receive encrypted or plaintext data */
  encrypted: boolean;
  /** Whether to use real-time streaming or batch mode */
  streaming: boolean;
  /** Optional field-level filters */
  filters?: QueryFilter[];
  /** Optional rate limit (messages per minute) */
  rateLimit?: number;
  /** Optional TTL for received messages (milliseconds) */
  ttl?: number;
}

/**
 * Streaming mode configuration
 */
export enum StreamingMode {
  /** Encrypted streaming using ECIES per-message or session key */
  Encrypted = 'encrypted',
  /** Unencrypted streaming for public data like TLEs */
  Unencrypted = 'unencrypted',
  /** Hybrid mode: headers unencrypted, payload encrypted */
  Hybrid = 'hybrid',
}

/**
 * Routing header for message routing (mirrors FlatBuffer schema)
 */
export interface RoutingHeader {
  /** Schema type, e.g., "OMM", "CDM" (unencrypted for routing) */
  schemaType: string;
  /** Destination peer IDs, empty for broadcast */
  destinationPeers: string[];
  /** Time-to-live (hop count) */
  ttl: number;
  /** Priority level (0-255, higher is more important) */
  priority: number;
  /** Whether the payload is encrypted */
  encrypted: boolean;
  /** Optional session key ID for encrypted streaming */
  sessionKeyId?: string;
}

/**
 * Active subscription state
 */
export interface ActiveSubscription {
  /** Unique subscription ID */
  id: string;
  /** Subscription configuration */
  config: SubscriptionConfig;
  /** Timestamp when subscription was created */
  createdAt: number;
  /** Message count received */
  messageCount: number;
  /** Last message timestamp */
  lastMessageAt: number | null;
  /** Subscription status */
  status: 'active' | 'paused' | 'error';
  /** Error message if status is 'error' */
  errorMessage?: string;
}

/**
 * Subscription event types
 */
export type SubscriptionEventType =
  | 'message'
  | 'error'
  | 'subscribed'
  | 'unsubscribed'
  | 'paused'
  | 'resumed'
  | 'rateLimit';

/**
 * Subscription event payload
 */
export interface SubscriptionEvent {
  type: SubscriptionEventType;
  subscriptionId: string;
  schema?: SchemaName;
  data?: unknown;
  from?: string;
  header?: RoutingHeader;
  error?: Error;
  timestamp: number;
}

/**
 * Subscription event handler
 */
export type SubscriptionEventHandler = (event: SubscriptionEvent) => void;

/**
 * Evaluates a query filter against a data object
 */
export function evaluateFilter(data: Record<string, unknown>, filter: QueryFilter): boolean {
  const fieldValue = getNestedValue(data, filter.field);

  switch (filter.operator) {
    case 'eq':
      return fieldValue === filter.value;
    case 'ne':
      return fieldValue !== filter.value;
    case 'gt':
      return typeof fieldValue === 'number' && typeof filter.value === 'number' && fieldValue > filter.value;
    case 'gte':
      return typeof fieldValue === 'number' && typeof filter.value === 'number' && fieldValue >= filter.value;
    case 'lt':
      return typeof fieldValue === 'number' && typeof filter.value === 'number' && fieldValue < filter.value;
    case 'lte':
      return typeof fieldValue === 'number' && typeof filter.value === 'number' && fieldValue <= filter.value;
    case 'contains':
      return typeof fieldValue === 'string' && typeof filter.value === 'string' && fieldValue.includes(filter.value);
    case 'startsWith':
      return typeof fieldValue === 'string' && typeof filter.value === 'string' && fieldValue.startsWith(filter.value);
    case 'endsWith':
      return typeof fieldValue === 'string' && typeof filter.value === 'string' && fieldValue.endsWith(filter.value);
    case 'in':
      return Array.isArray(filter.value) && filter.value.includes(fieldValue);
    case 'notIn':
      return Array.isArray(filter.value) && !filter.value.includes(fieldValue);
    default:
      return false;
  }
}

/**
 * Gets a nested value from an object using dot notation
 */
function getNestedValue(obj: Record<string, unknown>, path: string): unknown {
  const parts = path.split('.');
  let current: unknown = obj;

  for (const part of parts) {
    if (current === null || current === undefined) {
      return undefined;
    }
    if (typeof current === 'object' && current !== null) {
      current = (current as Record<string, unknown>)[part];
    } else {
      return undefined;
    }
  }

  return current;
}

/**
 * Evaluates all filters against data (AND logic)
 */
export function evaluateFilters(data: Record<string, unknown>, filters: QueryFilter[]): boolean {
  if (!filters || filters.length === 0) {
    return true;
  }
  return filters.every(filter => evaluateFilter(data, filter));
}

/**
 * Generates a unique subscription ID
 */
export function generateSubscriptionId(): string {
  const timestamp = Date.now().toString(36);
  const random = Math.random().toString(36).substring(2, 10);
  return `sub_${timestamp}_${random}`;
}

/**
 * Validates a subscription configuration
 */
export function validateSubscriptionConfig(config: SubscriptionConfig): string[] {
  const errors: string[] = [];

  // Validate data types
  if (!config.dataTypes || config.dataTypes.length === 0) {
    errors.push('At least one data type must be specified');
  } else {
    for (const dataType of config.dataTypes) {
      if (!SUPPORTED_SCHEMAS.includes(dataType)) {
        errors.push(`Unknown data type: ${dataType}`);
      }
    }
  }

  // Validate source peers
  if (!config.sourcePeers || config.sourcePeers.length === 0) {
    errors.push('At least one source peer must be specified (or "all")');
  }

  // Validate rate limit
  if (config.rateLimit !== undefined && (config.rateLimit < 0 || !Number.isInteger(config.rateLimit))) {
    errors.push('Rate limit must be a non-negative integer');
  }

  // Validate TTL
  if (config.ttl !== undefined && (config.ttl < 0 || !Number.isInteger(config.ttl))) {
    errors.push('TTL must be a non-negative integer');
  }

  // Validate filters
  if (config.filters) {
    for (let i = 0; i < config.filters.length; i++) {
      const filter = config.filters[i];
      if (!filter.field || typeof filter.field !== 'string') {
        errors.push(`Filter ${i}: field must be a non-empty string`);
      }
      const validOperators = ['eq', 'ne', 'gt', 'gte', 'lt', 'lte', 'contains', 'startsWith', 'endsWith', 'in', 'notIn'];
      if (!validOperators.includes(filter.operator)) {
        errors.push(`Filter ${i}: invalid operator "${filter.operator}"`);
      }
    }
  }

  return errors;
}

/**
 * Creates a default subscription configuration
 */
export function createDefaultConfig(): SubscriptionConfig {
  return {
    dataTypes: [],
    sourcePeers: ['all'],
    encrypted: true,
    streaming: true,
    filters: [],
    rateLimit: 1000,
    ttl: 24 * 60 * 60 * 1000, // 24 hours
  };
}

/**
 * Serializes a routing header to binary format
 * Format: [schemaTypeLen(1)][schemaType(n)][destCount(1)][destPeers...][ttl(1)][priority(1)][flags(1)]
 */
export function serializeRoutingHeader(header: RoutingHeader): Uint8Array {
  const encoder = new TextEncoder();
  const schemaBytes = encoder.encode(header.schemaType);
  const destBytes = header.destinationPeers.map(p => encoder.encode(p));

  // Calculate total size
  let size = 1 + schemaBytes.length; // schema type length + schema type
  size += 1; // destination count
  for (const dest of destBytes) {
    size += 1 + dest.length; // length + bytes for each destination
  }
  size += 3; // ttl + priority + flags

  if (header.sessionKeyId) {
    const sessionKeyBytes = encoder.encode(header.sessionKeyId);
    size += 1 + sessionKeyBytes.length;
  }

  const buffer = new Uint8Array(size);
  let offset = 0;

  // Schema type
  buffer[offset++] = schemaBytes.length;
  buffer.set(schemaBytes, offset);
  offset += schemaBytes.length;

  // Destination peers
  buffer[offset++] = destBytes.length;
  for (const dest of destBytes) {
    buffer[offset++] = dest.length;
    buffer.set(dest, offset);
    offset += dest.length;
  }

  // TTL and priority
  buffer[offset++] = Math.min(255, Math.max(0, header.ttl));
  buffer[offset++] = Math.min(255, Math.max(0, header.priority));

  // Flags: bit 0 = encrypted, bit 1 = has session key
  let flags = 0;
  if (header.encrypted) flags |= 0x01;
  if (header.sessionKeyId) flags |= 0x02;
  buffer[offset++] = flags;

  // Optional session key ID
  if (header.sessionKeyId) {
    const sessionKeyBytes = encoder.encode(header.sessionKeyId);
    buffer[offset++] = sessionKeyBytes.length;
    buffer.set(sessionKeyBytes, offset);
  }

  return buffer;
}

/**
 * Deserializes a routing header from binary format
 */
export function deserializeRoutingHeader(data: Uint8Array): RoutingHeader | null {
  if (data.length < 5) {
    return null;
  }

  const decoder = new TextDecoder();
  let offset = 0;

  // Schema type
  const schemaTypeLen = data[offset++];
  if (offset + schemaTypeLen > data.length) return null;
  const schemaType = decoder.decode(data.slice(offset, offset + schemaTypeLen));
  offset += schemaTypeLen;

  // Destination peers
  if (offset >= data.length) return null;
  const destCount = data[offset++];
  const destinationPeers: string[] = [];
  for (let i = 0; i < destCount; i++) {
    if (offset >= data.length) return null;
    const destLen = data[offset++];
    if (offset + destLen > data.length) return null;
    destinationPeers.push(decoder.decode(data.slice(offset, offset + destLen)));
    offset += destLen;
  }

  // TTL, priority, flags
  if (offset + 3 > data.length) return null;
  const ttl = data[offset++];
  const priority = data[offset++];
  const flags = data[offset++];

  const encrypted = (flags & 0x01) !== 0;
  const hasSessionKey = (flags & 0x02) !== 0;

  let sessionKeyId: string | undefined;
  if (hasSessionKey && offset < data.length) {
    const sessionKeyLen = data[offset++];
    if (offset + sessionKeyLen <= data.length) {
      sessionKeyId = decoder.decode(data.slice(offset, offset + sessionKeyLen));
    }
  }

  return {
    schemaType,
    destinationPeers,
    ttl,
    priority,
    encrypted,
    sessionKeyId,
  };
}

/**
 * Gets the topic name for schema-based routing
 */
export function getSchemaRoutingTopic(schemaType: string): string {
  return `/sdn/data/${schemaType.replace('.fbs', '')}`;
}

/**
 * Gets the topic name for peer-based routing
 */
export function getPeerRoutingTopic(peerId: string): string {
  return `/sdn/peer/${peerId}`;
}

/**
 * Subscription Manager class for managing multiple subscriptions
 */
export class SubscriptionManager {
  private subscriptions: Map<string, ActiveSubscription> = new Map();
  private handlers: Map<string, Set<SubscriptionEventHandler>> = new Map();
  private rateLimitCounters: Map<string, { count: number; resetAt: number }> = new Map();

  /**
   * Creates a new subscription
   */
  createSubscription(config: SubscriptionConfig): ActiveSubscription {
    const errors = validateSubscriptionConfig(config);
    if (errors.length > 0) {
      throw new Error(`Invalid subscription config: ${errors.join(', ')}`);
    }

    const subscription: ActiveSubscription = {
      id: generateSubscriptionId(),
      config,
      createdAt: Date.now(),
      messageCount: 0,
      lastMessageAt: null,
      status: 'active',
    };

    this.subscriptions.set(subscription.id, subscription);
    this.emitEvent({
      type: 'subscribed',
      subscriptionId: subscription.id,
      timestamp: Date.now(),
    });

    return subscription;
  }

  /**
   * Gets a subscription by ID
   */
  getSubscription(id: string): ActiveSubscription | undefined {
    return this.subscriptions.get(id);
  }

  /**
   * Gets all active subscriptions
   */
  getAllSubscriptions(): ActiveSubscription[] {
    return Array.from(this.subscriptions.values());
  }

  /**
   * Updates a subscription configuration
   */
  updateSubscription(id: string, config: Partial<SubscriptionConfig>): ActiveSubscription | undefined {
    const subscription = this.subscriptions.get(id);
    if (!subscription) {
      return undefined;
    }

    const newConfig = { ...subscription.config, ...config };
    const errors = validateSubscriptionConfig(newConfig);
    if (errors.length > 0) {
      throw new Error(`Invalid subscription config: ${errors.join(', ')}`);
    }

    subscription.config = newConfig;
    return subscription;
  }

  /**
   * Removes a subscription
   */
  removeSubscription(id: string): boolean {
    const subscription = this.subscriptions.get(id);
    if (!subscription) {
      return false;
    }

    this.subscriptions.delete(id);
    this.rateLimitCounters.delete(id);
    this.emitEvent({
      type: 'unsubscribed',
      subscriptionId: id,
      timestamp: Date.now(),
    });

    return true;
  }

  /**
   * Pauses a subscription
   */
  pauseSubscription(id: string): boolean {
    const subscription = this.subscriptions.get(id);
    if (!subscription) {
      return false;
    }

    subscription.status = 'paused';
    this.emitEvent({
      type: 'paused',
      subscriptionId: id,
      timestamp: Date.now(),
    });

    return true;
  }

  /**
   * Resumes a subscription
   */
  resumeSubscription(id: string): boolean {
    const subscription = this.subscriptions.get(id);
    if (!subscription) {
      return false;
    }

    subscription.status = 'active';
    this.emitEvent({
      type: 'resumed',
      subscriptionId: id,
      timestamp: Date.now(),
    });

    return true;
  }

  /**
   * Processes an incoming message against all subscriptions
   */
  processMessage(schema: SchemaName, data: unknown, from: string, header?: RoutingHeader): void {
    const now = Date.now();

    for (const subscription of this.subscriptions.values()) {
      if (subscription.status !== 'active') {
        continue;
      }

      const { config } = subscription;

      // Check schema match
      if (!config.dataTypes.includes(schema)) {
        continue;
      }

      // Check source peer match
      if (!config.sourcePeers.includes('all') && !config.sourcePeers.includes(from)) {
        continue;
      }

      // Check encryption preference
      if (header && config.encrypted !== header.encrypted) {
        continue;
      }

      // Check filters
      if (config.filters && config.filters.length > 0) {
        if (typeof data !== 'object' || data === null) {
          continue;
        }
        if (!evaluateFilters(data as Record<string, unknown>, config.filters)) {
          continue;
        }
      }

      // Check rate limit
      if (config.rateLimit && !this.checkRateLimit(subscription.id, config.rateLimit)) {
        this.emitEvent({
          type: 'rateLimit',
          subscriptionId: subscription.id,
          schema,
          from,
          timestamp: now,
        });
        continue;
      }

      // Update subscription stats
      subscription.messageCount++;
      subscription.lastMessageAt = now;

      // Emit message event
      this.emitEvent({
        type: 'message',
        subscriptionId: subscription.id,
        schema,
        data,
        from,
        header,
        timestamp: now,
      });
    }
  }

  /**
   * Checks rate limit for a subscription
   */
  private checkRateLimit(subscriptionId: string, limit: number): boolean {
    const now = Date.now();
    const minute = 60 * 1000;

    let counter = this.rateLimitCounters.get(subscriptionId);
    if (!counter || counter.resetAt <= now) {
      counter = { count: 0, resetAt: now + minute };
      this.rateLimitCounters.set(subscriptionId, counter);
    }

    if (counter.count >= limit) {
      return false;
    }

    counter.count++;
    return true;
  }

  /**
   * Adds an event handler
   */
  addEventListener(subscriptionId: string | '*', handler: SubscriptionEventHandler): void {
    const key = subscriptionId === '*' ? '*' : subscriptionId;
    let handlers = this.handlers.get(key);
    if (!handlers) {
      handlers = new Set();
      this.handlers.set(key, handlers);
    }
    handlers.add(handler);
  }

  /**
   * Removes an event handler
   */
  removeEventListener(subscriptionId: string | '*', handler: SubscriptionEventHandler): void {
    const key = subscriptionId === '*' ? '*' : subscriptionId;
    const handlers = this.handlers.get(key);
    if (handlers) {
      handlers.delete(handler);
    }
  }

  /**
   * Emits an event to all relevant handlers
   */
  private emitEvent(event: SubscriptionEvent): void {
    // Emit to specific subscription handlers
    const specificHandlers = this.handlers.get(event.subscriptionId);
    if (specificHandlers) {
      for (const handler of specificHandlers) {
        try {
          handler(event);
        } catch (err) {
          console.error('Subscription event handler error:', err);
        }
      }
    }

    // Emit to global handlers
    const globalHandlers = this.handlers.get('*');
    if (globalHandlers) {
      for (const handler of globalHandlers) {
        try {
          handler(event);
        } catch (err) {
          console.error('Subscription event handler error:', err);
        }
      }
    }
  }

  /**
   * Gets the topics to subscribe to based on all active subscriptions
   */
  getRequiredTopics(): Set<string> {
    const topics = new Set<string>();

    for (const subscription of this.subscriptions.values()) {
      if (subscription.status !== 'active') {
        continue;
      }

      for (const dataType of subscription.config.dataTypes) {
        topics.add(getTopicName(dataType));
        topics.add(getSchemaRoutingTopic(dataType));
      }

      // Add peer-specific topics if not subscribing to all
      if (!subscription.config.sourcePeers.includes('all')) {
        for (const peerId of subscription.config.sourcePeers) {
          topics.add(getPeerRoutingTopic(peerId));
        }
      }
    }

    return topics;
  }

  /**
   * Clears all subscriptions
   */
  clear(): void {
    for (const id of this.subscriptions.keys()) {
      this.removeSubscription(id);
    }
  }
}

/**
 * Default subscription manager instance
 */
export const defaultSubscriptionManager = new SubscriptionManager();

// --- Streaming Mode Support ---

/**
 * Encryption mode for streaming payloads
 */
export enum EncryptionMode {
  /** No encryption */
  None = 0,
  /** ECIES per-message encryption */
  ECIES = 1,
  /** Session key encryption (AES-GCM with pre-shared key) */
  SessionKey = 2,
  /** Hybrid: header unencrypted, payload encrypted */
  Hybrid = 3,
}

/**
 * Stream delivery mode
 */
export enum StreamDeliveryMode {
  /** Single message delivery */
  Single = 0,
  /** Real-time streaming */
  Streaming = 1,
  /** Batch delivery */
  Batch = 2,
}

/**
 * Streaming session state
 */
export interface StreamingSession {
  /** Unique session ID */
  id: string;
  /** Associated subscription ID */
  subscriptionId: string;
  /** Remote peer ID */
  peerId: string;
  /** Schema types this session handles */
  schemaTypes: string[];
  /** Delivery mode */
  mode: StreamDeliveryMode;
  /** Encryption mode */
  encryptionMode: EncryptionMode;
  /** Session key ID (for session-key encryption) */
  sessionKeyId?: string;
  /** Whether the session is active */
  active: boolean;
  /** Messages sent count */
  messagesSent: number;
  /** Bytes sent count */
  bytesSent: number;
  /** Creation timestamp */
  createdAt: number;
  /** Last activity timestamp */
  lastActivity: number;
}

/**
 * Extended routing header with encryption mode and streaming support
 */
export interface ExtendedRoutingHeader extends RoutingHeader {
  /** Encryption mode for the payload */
  encryptionMode: EncryptionMode;
  /** Source peer ID */
  sourcePeer?: string;
  /** Sequence number for ordering */
  sequence?: number;
  /** Timestamp (Unix milliseconds) */
  timestamp: number;
  /** Topic override */
  topicOverride?: string;
  /** Stream delivery mode */
  streamMode?: StreamDeliveryMode;
}

/**
 * Edge relay filter configuration
 */
export interface EdgeRelayFilter {
  /** Allowed schema types (empty = all) */
  allowedSchemas?: string[];
  /** Allowed destination peers (empty = all) */
  allowedPeers?: string[];
  /** Minimum priority to forward (0 = all) */
  minPriority: number;
  /** Maximum TTL accepted (0 = no limit) */
  maxTTL: number;
  /** Allow encrypted messages */
  allowEncrypted: boolean;
  /** Allow unencrypted messages */
  allowUnencrypted: boolean;
}

/**
 * Creates a default edge relay filter (permissive)
 */
export function createDefaultEdgeRelayFilter(): EdgeRelayFilter {
  return {
    minPriority: 0,
    maxTTL: 0,
    allowEncrypted: true,
    allowUnencrypted: true,
  };
}

/**
 * Evaluates an edge relay filter against a routing header
 */
export function evaluateEdgeRelayFilter(filter: EdgeRelayFilter, header: RoutingHeader): boolean {
  // Schema filter
  if (filter.allowedSchemas && filter.allowedSchemas.length > 0) {
    if (!filter.allowedSchemas.includes(header.schemaType)) {
      return false;
    }
  }

  // Peer filter
  if (filter.allowedPeers && filter.allowedPeers.length > 0 && header.destinationPeers.length > 0) {
    const hasAllowed = header.destinationPeers.some(p => filter.allowedPeers!.includes(p));
    if (!hasAllowed) {
      return false;
    }
  }

  // Priority filter
  if (header.priority < filter.minPriority) {
    return false;
  }

  // TTL filter
  if (filter.maxTTL > 0 && header.ttl > filter.maxTTL) {
    return false;
  }

  // Encryption filter
  if (header.encrypted && !filter.allowEncrypted) {
    return false;
  }
  if (!header.encrypted && !filter.allowUnencrypted) {
    return false;
  }

  return true;
}

/**
 * Creates an extended routing header with streaming support
 */
export function createRoutingHeader(
  schemaType: string,
  sourcePeer: string,
  options?: Partial<ExtendedRoutingHeader>,
): ExtendedRoutingHeader {
  return {
    schemaType,
    destinationPeers: [],
    ttl: 7,
    priority: 64, // Normal
    encrypted: true,
    encryptionMode: EncryptionMode.ECIES,
    sourcePeer,
    timestamp: Date.now(),
    streamMode: StreamDeliveryMode.Single,
    ...options,
  };
}

/**
 * Serializes an extended routing header to binary format
 * Compatible with Go SerializeRoutingHeader
 */
export function serializeExtendedRoutingHeader(header: ExtendedRoutingHeader): Uint8Array {
  const encoder = new TextEncoder();
  const schemaBytes = encoder.encode(header.schemaType);
  const destBytes = header.destinationPeers.map(p => encoder.encode(p));

  let size = 1 + schemaBytes.length; // schema type length + schema type
  size += 1; // destination count
  for (const dest of destBytes) {
    size += 1 + dest.length;
  }
  size += 3; // ttl + priority + flags

  const sessionKeyBytes = header.sessionKeyId ? encoder.encode(header.sessionKeyId) : null;
  const sourcePeerBytes = header.sourcePeer ? encoder.encode(header.sourcePeer) : null;
  const topicOverrideBytes = header.topicOverride ? encoder.encode(header.topicOverride) : null;

  if (sessionKeyBytes) size += 1 + sessionKeyBytes.length;
  if (sourcePeerBytes) size += 1 + sourcePeerBytes.length;
  if (topicOverrideBytes) size += 1 + topicOverrideBytes.length;

  size += 16; // sequence (8) + timestamp (8)

  const buffer = new Uint8Array(size);
  const view = new DataView(buffer.buffer);
  let offset = 0;

  // Schema type
  buffer[offset++] = schemaBytes.length;
  buffer.set(schemaBytes, offset);
  offset += schemaBytes.length;

  // Destination peers
  buffer[offset++] = destBytes.length;
  for (const dest of destBytes) {
    buffer[offset++] = dest.length;
    buffer.set(dest, offset);
    offset += dest.length;
  }

  // TTL and priority
  buffer[offset++] = Math.min(255, Math.max(0, header.ttl));
  buffer[offset++] = Math.min(255, Math.max(0, header.priority));

  // Flags
  let flags = 0;
  if (header.encrypted) flags |= 0x01;
  if (sessionKeyBytes) flags |= 0x02;
  if (sourcePeerBytes) flags |= 0x04;
  if (topicOverrideBytes) flags |= 0x08;
  flags |= (header.encryptionMode & 0x03) << 5;
  buffer[offset++] = flags;

  // Optional session key ID
  if (sessionKeyBytes) {
    buffer[offset++] = sessionKeyBytes.length;
    buffer.set(sessionKeyBytes, offset);
    offset += sessionKeyBytes.length;
  }

  // Optional source peer
  if (sourcePeerBytes) {
    buffer[offset++] = sourcePeerBytes.length;
    buffer.set(sourcePeerBytes, offset);
    offset += sourcePeerBytes.length;
  }

  // Optional topic override
  if (topicOverrideBytes) {
    buffer[offset++] = topicOverrideBytes.length;
    buffer.set(topicOverrideBytes, offset);
    offset += topicOverrideBytes.length;
  }

  // Sequence (8 bytes, big endian)
  const seq = header.sequence ?? 0;
  view.setUint32(offset, Math.floor(seq / 0x100000000));
  view.setUint32(offset + 4, seq >>> 0);
  offset += 8;

  // Timestamp (8 bytes, big endian)
  const ts = header.timestamp;
  view.setUint32(offset, Math.floor(ts / 0x100000000));
  view.setUint32(offset + 4, ts >>> 0);
  offset += 8;

  return buffer.slice(0, offset);
}

/**
 * Deserializes an extended routing header from binary format
 * Compatible with Go DeserializeRoutingHeader
 */
export function deserializeExtendedRoutingHeader(data: Uint8Array): ExtendedRoutingHeader | null {
  if (data.length < 5) return null;

  const decoder = new TextDecoder();
  const view = new DataView(data.buffer, data.byteOffset, data.byteLength);
  let offset = 0;

  // Schema type
  const schemaTypeLen = data[offset++];
  if (offset + schemaTypeLen > data.length) return null;
  const schemaType = decoder.decode(data.slice(offset, offset + schemaTypeLen));
  offset += schemaTypeLen;

  // Destination peers
  if (offset >= data.length) return null;
  const destCount = data[offset++];
  const destinationPeers: string[] = [];
  for (let i = 0; i < destCount; i++) {
    if (offset >= data.length) return null;
    const destLen = data[offset++];
    if (offset + destLen > data.length) return null;
    destinationPeers.push(decoder.decode(data.slice(offset, offset + destLen)));
    offset += destLen;
  }

  // TTL, priority, flags
  if (offset + 3 > data.length) return null;
  const ttl = data[offset++];
  const priority = data[offset++];
  const flags = data[offset++];

  const encrypted = (flags & 0x01) !== 0;
  const hasSessionKey = (flags & 0x02) !== 0;
  const hasSourcePeer = (flags & 0x04) !== 0;
  const hasTopicOverride = (flags & 0x08) !== 0;
  const encryptionMode: EncryptionMode = ((flags >> 5) & 0x03) as EncryptionMode;

  let sessionKeyId: string | undefined;
  if (hasSessionKey && offset < data.length) {
    const len = data[offset++];
    if (offset + len <= data.length) {
      sessionKeyId = decoder.decode(data.slice(offset, offset + len));
      offset += len;
    }
  }

  let sourcePeer: string | undefined;
  if (hasSourcePeer && offset < data.length) {
    const len = data[offset++];
    if (offset + len <= data.length) {
      sourcePeer = decoder.decode(data.slice(offset, offset + len));
      offset += len;
    }
  }

  let topicOverride: string | undefined;
  if (hasTopicOverride && offset < data.length) {
    const len = data[offset++];
    if (offset + len <= data.length) {
      topicOverride = decoder.decode(data.slice(offset, offset + len));
      offset += len;
    }
  }

  let sequence: number | undefined;
  let timestamp = 0;
  if (offset + 16 <= data.length) {
    sequence = view.getUint32(offset) * 0x100000000 + view.getUint32(offset + 4);
    offset += 8;
    timestamp = view.getUint32(offset) * 0x100000000 + view.getUint32(offset + 4);
    offset += 8;
  }

  return {
    schemaType,
    destinationPeers,
    ttl,
    priority,
    encrypted,
    encryptionMode,
    sessionKeyId,
    sourcePeer,
    sequence,
    timestamp,
    topicOverride,
  };
}

/**
 * Creates a message with routing header prepended (matches Go format)
 * Format: [headerLen(2)][header(n)][payload(m)]
 */
export function createMessageWithHeader(header: ExtendedRoutingHeader, payload: Uint8Array): Uint8Array {
  const headerBytes = serializeExtendedRoutingHeader(header);
  const message = new Uint8Array(2 + headerBytes.length + payload.length);
  const view = new DataView(message.buffer);
  view.setUint16(0, headerBytes.length);
  message.set(headerBytes, 2);
  message.set(payload, 2 + headerBytes.length);
  return message;
}

/**
 * Parses a message with routing header to extract header and payload
 */
export function parseMessageWithHeader(message: Uint8Array): { header: ExtendedRoutingHeader; payload: Uint8Array } | null {
  if (message.length < 2) return null;

  const view = new DataView(message.buffer, message.byteOffset, message.byteLength);
  const headerLen = view.getUint16(0);

  if (message.length < 2 + headerLen) return null;

  const header = deserializeExtendedRoutingHeader(message.slice(2, 2 + headerLen));
  if (!header) return null;

  const payload = message.slice(2 + headerLen);
  return { header, payload };
}

/**
 * Determines the routing topic for a message based on its header
 */
export function getRoutingTopicForHeader(header: RoutingHeader | ExtendedRoutingHeader): string {
  const extended = header as ExtendedRoutingHeader;
  if (extended.topicOverride) {
    return extended.topicOverride;
  }
  if (header.destinationPeers.length === 1) {
    return getPeerRoutingTopic(header.destinationPeers[0]);
  }
  return getSchemaRoutingTopic(header.schemaType);
}

/**
 * Desktop app subscription data model API.
 * Provides the data model and configuration interface for the desktop app.
 */
export class DesktopSubscriptionAPI {
  private manager: SubscriptionManager;
  private streamingSessions: Map<string, StreamingSession> = new Map();

  constructor(manager?: SubscriptionManager) {
    this.manager = manager ?? new SubscriptionManager();
  }

  /** Get the subscription manager */
  getManager(): SubscriptionManager {
    return this.manager;
  }

  /** Create a subscription with streaming session */
  createStreamingSubscription(
    config: SubscriptionConfig,
    peerId: string,
    mode: StreamDeliveryMode = StreamDeliveryMode.Streaming,
    encMode: EncryptionMode = EncryptionMode.ECIES,
  ): { subscription: ActiveSubscription; session: StreamingSession } {
    const subscription = this.manager.createSubscription(config);

    const session: StreamingSession = {
      id: `sess_${Date.now().toString(36)}_${Math.random().toString(36).substring(2, 10)}`,
      subscriptionId: subscription.id,
      peerId,
      schemaTypes: config.dataTypes,
      mode,
      encryptionMode: encMode,
      sessionKeyId: encMode === EncryptionMode.SessionKey
        ? `sk_${Date.now().toString(36)}_${Math.random().toString(36).substring(2, 10)}`
        : undefined,
      active: true,
      messagesSent: 0,
      bytesSent: 0,
      createdAt: Date.now(),
      lastActivity: Date.now(),
    };

    this.streamingSessions.set(session.id, session);
    return { subscription, session };
  }

  /** Close a streaming session */
  closeSession(sessionId: string): boolean {
    const session = this.streamingSessions.get(sessionId);
    if (!session) return false;
    session.active = false;
    this.streamingSessions.delete(sessionId);
    return true;
  }

  /** Get all streaming sessions */
  getSessions(): StreamingSession[] {
    return Array.from(this.streamingSessions.values());
  }

  /** Get sessions for a specific peer */
  getPeerSessions(peerId: string): StreamingSession[] {
    return this.getSessions().filter(s => s.peerId === peerId);
  }

  /** Get all required topics including streaming sessions */
  getRequiredTopics(): string[] {
    const topics = this.manager.getRequiredTopics();
    // Add session-specific peer topics
    for (const session of this.streamingSessions.values()) {
      if (session.active) {
        topics.add(getPeerRoutingTopic(session.peerId));
      }
    }
    return Array.from(topics);
  }
}

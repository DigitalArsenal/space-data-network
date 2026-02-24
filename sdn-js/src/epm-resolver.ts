/**
 * EPM Resolver - Entity Profile Manifest lookup and encryption key resolution
 *
 * Provides functionality to:
 * - Look up EPMs by Peer ID
 * - Cache EPMs with TTL
 * - Extract encryption public keys for ECIES
 * - Support multiple key types (X25519, secp256k1, P-256)
 */

import type { Libp2p } from 'libp2p';

// Key type enum matching FlatBuffers schema
export enum KeyType {
  Signing = 0,
  Encryption = 1,
}

// Supported key exchange algorithms
export type KeyExchangeAlgorithm = 'x25519' | 'secp256k1' | 'p256';

// EPM key information
export interface EPMKey {
  publicKey: Uint8Array;
  publicKeyHex: string;
  keyType: KeyType;
  keyAddress?: string;
  addressType?: string;
  algorithm?: KeyExchangeAlgorithm;
}

// Parsed EPM data
export interface ParsedEPM {
  dn?: string;
  legalName?: string;
  familyName?: string;
  givenName?: string;
  email?: string;
  telephone?: string;
  keys: EPMKey[];
  multiformatAddresses: string[];
  raw: Uint8Array;
  cid?: string;
  peerID?: string;
  timestamp: number;
}

// Cache entry with TTL
interface CacheEntry {
  epm: ParsedEPM;
  expiresAt: number;
}

// EPM resolver options
export interface EPMResolverOptions {
  /** Cache TTL in milliseconds (default: 5 minutes) */
  cacheTTL?: number;
  /** Maximum cache size (default: 1000) */
  maxCacheSize?: number;
  /** IPFS gateway URL for fallback fetching */
  ipfsGateway?: string;
  /** PubSub topic for PNM announcements */
  pnmTopic?: string;
}

// Default options
const DEFAULT_OPTIONS: Required<EPMResolverOptions> = {
  cacheTTL: 5 * 60 * 1000, // 5 minutes
  maxCacheSize: 1000,
  ipfsGateway: 'https://ipfs.io/ipfs/',
  pnmTopic: '/sdn/pnm/1.0.0',
};

/**
 * EPM File Identifier for FlatBuffers
 */
const EPM_FILE_ID = '$EPM';

/**
 * Parse hex string to Uint8Array
 */
function hexToBytes(hex: string): Uint8Array {
  const cleanHex = hex.startsWith('0x') ? hex.slice(2) : hex;
  const bytes = new Uint8Array(cleanHex.length / 2);
  for (let i = 0; i < bytes.length; i++) {
    bytes[i] = parseInt(cleanHex.substr(i * 2, 2), 16);
  }
  return bytes;
}

/**
 * Convert Uint8Array to hex string
 */
function bytesToHex(bytes: Uint8Array): string {
  return Array.from(bytes)
    .map(b => b.toString(16).padStart(2, '0'))
    .join('');
}

/**
 * Detect key algorithm from public key format/length
 * - X25519: 32 bytes
 * - secp256k1: 33 bytes (compressed) or 65 bytes (uncompressed)
 * - P-256: 33 bytes (compressed) or 65 bytes (uncompressed)
 *
 * Note: Distinguishing secp256k1 from P-256 requires additional metadata
 */
function detectKeyAlgorithm(publicKey: Uint8Array, hint?: string): KeyExchangeAlgorithm {
  if (hint) {
    const normalizedHint = hint.toLowerCase();
    if (normalizedHint.includes('x25519') || normalizedHint.includes('curve25519')) {
      return 'x25519';
    }
    if (normalizedHint.includes('secp256k1') || normalizedHint.includes('ethereum') || normalizedHint.includes('bitcoin')) {
      return 'secp256k1';
    }
    if (normalizedHint.includes('p256') || normalizedHint.includes('p-256') || normalizedHint.includes('nist')) {
      return 'p256';
    }
  }

  // Default detection by key length
  if (publicKey.length === 32) {
    return 'x25519';
  }
  // For 33/65 byte keys, default to secp256k1 (most common for blockchain)
  // Applications should use ADDRESS_TYPE hint for P-256
  return 'secp256k1';
}

/**
 * Parse EPM FlatBuffer data
 *
 * This is a minimal parser that extracts key information without
 * requiring the full FlatBuffers library dependency.
 */
function parseEPMBuffer(data: Uint8Array): ParsedEPM | null {
  try {
    // Check file identifier
    if (data.length < 8) return null;

    // For size-prefixed buffers, skip the 4-byte size prefix
    let offset = 0;
    const view = new DataView(data.buffer, data.byteOffset, data.byteLength);

    // Check if size-prefixed
    const possibleSize = view.getUint32(0, true);
    if (possibleSize === data.length - 4) {
      offset = 4;
    }

    // Check file identifier at offset+4
    const fileId = String.fromCharCode(
      data[offset + 4],
      data[offset + 5],
      data[offset + 6],
      data[offset + 7]
    );

    if (fileId !== EPM_FILE_ID) {
      console.warn('Invalid EPM file identifier:', fileId);
      return null;
    }

    // For proper parsing, we'd need FlatBuffers runtime
    // This is a placeholder that requires the full library
    // In production, import the generated EPM parser

    return {
      keys: [],
      multiformatAddresses: [],
      raw: data,
      timestamp: Date.now(),
    };
  } catch (err) {
    console.error('Failed to parse EPM buffer:', err);
    return null;
  }
}

/**
 * EPM Resolver - Looks up Entity Profile Manifests and extracts encryption keys
 */
export class EPMResolver {
  private cache: Map<string, CacheEntry> = new Map();
  private options: Required<EPMResolverOptions>;
  private libp2pNode?: Libp2p;
  private pnmSubscribed = false;

  constructor(options: EPMResolverOptions = {}) {
    this.options = { ...DEFAULT_OPTIONS, ...options };
  }

  /**
   * Set the libp2p node for P2P EPM resolution
   */
  setNode(node: Libp2p): void {
    this.libp2pNode = node;
  }

  /**
   * Subscribe to PNM (Publish Notification Message) topic for EPM announcements
   */
  async subscribeToPNM(): Promise<void> {
    if (!this.libp2pNode || this.pnmSubscribed) return;

    try {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const services = this.libp2pNode.services as any;
      const pubsub = services?.pubsub;
      if (!pubsub) {
        console.warn('PubSub not available on libp2p node');
        return;
      }

      await pubsub.subscribe(this.options.pnmTopic);

      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pubsub.addEventListener('message', (evt: any) => {
        if (evt.detail.topic === this.options.pnmTopic) {
          this.handlePNM(evt.detail.data);
        }
      });

      this.pnmSubscribed = true;
    } catch (err) {
      console.error('Failed to subscribe to PNM topic:', err);
    }
  }

  /**
   * Handle incoming PNM announcement
   */
  private async handlePNM(_data: Uint8Array): Promise<void> {
    try {
      // Parse PNM to extract CID and FILE_ID
      // If FILE_ID === "EPM", fetch and cache the EPM
      // This would use the PNM FlatBuffer parser
      console.debug('Received PNM announcement');
    } catch (err) {
      console.error('Failed to handle PNM:', err);
    }
  }

  /**
   * Resolve an EPM by Peer ID
   *
   * Resolution order:
   * 1. Check local cache
   * 2. Query IPNS (if libp2p available)
   * 3. Fetch from IPFS gateway (if CID known)
   */
  async resolveByPeerID(peerID: string): Promise<ParsedEPM | null> {
    // Check cache first
    const cached = this.getFromCache(peerID);
    if (cached) return cached;

    // Try IPNS resolution via libp2p
    if (this.libp2pNode) {
      try {
        const epm = await this.resolveIPNS(peerID);
        if (epm) {
          this.addToCache(peerID, epm);
          return epm;
        }
      } catch (err) {
        console.debug('IPNS resolution failed:', err);
      }
    }

    return null;
  }

  /**
   * Resolve EPM by CID (Content Identifier)
   */
  async resolveByCID(cid: string): Promise<ParsedEPM | null> {
    // Check cache first
    const cached = this.getFromCache(`cid:${cid}`);
    if (cached) return cached;

    try {
      // Try IPFS gateway
      const url = `${this.options.ipfsGateway}${cid}`;
      const response = await fetch(url);
      if (!response.ok) {
        throw new Error(`IPFS fetch failed: ${response.status}`);
      }

      const data = new Uint8Array(await response.arrayBuffer());
      const epm = parseEPMBuffer(data);

      if (epm) {
        epm.cid = cid;
        this.addToCache(`cid:${cid}`, epm);
        return epm;
      }
    } catch (err) {
      console.error('Failed to resolve EPM by CID:', err);
    }

    return null;
  }

  /**
   * Resolve EPM via IPNS
   */
  private async resolveIPNS(_peerID: string): Promise<ParsedEPM | null> {
    // This would use libp2p IPNS resolution
    // For now, return null to fall back to other methods
    return null;
  }

  /**
   * Add a manually fetched EPM to the cache
   */
  addEPM(key: string, epm: ParsedEPM): void {
    this.addToCache(key, epm);
  }

  /**
   * Add a raw EPM buffer and parse it
   */
  addEPMBuffer(key: string, data: Uint8Array): ParsedEPM | null {
    const epm = parseEPMBuffer(data);
    if (epm) {
      this.addToCache(key, epm);
      return epm;
    }
    return null;
  }

  /**
   * Add parsed EPM data directly (for when parsing is done externally)
   */
  addParsedEPM(key: string, data: {
    keys: Array<{
      publicKey: string | Uint8Array;
      keyType: KeyType;
      algorithm?: KeyExchangeAlgorithm;
      keyAddress?: string;
      addressType?: string;
    }>;
    multiformatAddresses?: string[];
    dn?: string;
    legalName?: string;
    email?: string;
    peerID?: string;
    cid?: string;
  }): ParsedEPM {
    const keys: EPMKey[] = data.keys.map(k => {
      const pubKeyBytes = typeof k.publicKey === 'string'
        ? hexToBytes(k.publicKey)
        : k.publicKey;

      return {
        publicKey: pubKeyBytes,
        publicKeyHex: typeof k.publicKey === 'string'
          ? (k.publicKey.startsWith('0x') ? k.publicKey.slice(2) : k.publicKey)
          : bytesToHex(pubKeyBytes),
        keyType: k.keyType,
        algorithm: k.algorithm || detectKeyAlgorithm(pubKeyBytes, k.addressType),
        keyAddress: k.keyAddress,
        addressType: k.addressType,
      };
    });

    const epm: ParsedEPM = {
      keys,
      multiformatAddresses: data.multiformatAddresses || [],
      raw: new Uint8Array(0),
      timestamp: Date.now(),
      dn: data.dn,
      legalName: data.legalName,
      email: data.email,
      peerID: data.peerID,
      cid: data.cid,
    };

    this.addToCache(key, epm);
    return epm;
  }

  /**
   * Get encryption public key for a peer
   *
   * @param peerID - The peer's identifier
   * @param preferredAlgorithm - Preferred key exchange algorithm
   * @returns The encryption key info or null if not found
   */
  async getEncryptionKey(
    peerID: string,
    preferredAlgorithm?: KeyExchangeAlgorithm
  ): Promise<EPMKey | null> {
    const epm = await this.resolveByPeerID(peerID);
    if (!epm) return null;

    return this.extractEncryptionKey(epm, preferredAlgorithm);
  }

  /**
   * Get encryption key from a cached/known EPM
   */
  extractEncryptionKey(
    epm: ParsedEPM,
    preferredAlgorithm?: KeyExchangeAlgorithm
  ): EPMKey | null {
    // Filter to encryption keys only
    const encryptionKeys = epm.keys.filter(k => k.keyType === KeyType.Encryption);

    if (encryptionKeys.length === 0) return null;

    // If preferred algorithm specified, try to find matching key
    if (preferredAlgorithm) {
      const matching = encryptionKeys.find(k => k.algorithm === preferredAlgorithm);
      if (matching) return matching;
    }

    // Return first encryption key (typically X25519 is preferred default)
    // Sort by algorithm preference: x25519 > secp256k1 > p256
    const sorted = encryptionKeys.sort((a, b) => {
      const order: Record<KeyExchangeAlgorithm, number> = {
        'x25519': 0,
        'secp256k1': 1,
        'p256': 2,
      };
      return (order[a.algorithm || 'secp256k1'] || 1) - (order[b.algorithm || 'secp256k1'] || 1);
    });

    return sorted[0];
  }

  /**
   * Get signing public key for a peer
   */
  async getSigningKey(peerID: string): Promise<EPMKey | null> {
    const epm = await this.resolveByPeerID(peerID);
    if (!epm) return null;

    return this.extractSigningKey(epm);
  }

  /**
   * Extract signing key from EPM
   */
  extractSigningKey(epm: ParsedEPM): EPMKey | null {
    const signingKeys = epm.keys.filter(k => k.keyType === KeyType.Signing);
    return signingKeys[0] || null;
  }

  /**
   * Get all keys from an EPM
   */
  async getAllKeys(peerID: string): Promise<EPMKey[]> {
    const epm = await this.resolveByPeerID(peerID);
    return epm?.keys || [];
  }

  // Cache management

  private getFromCache(key: string): ParsedEPM | null {
    const entry = this.cache.get(key);
    if (!entry) return null;

    if (Date.now() > entry.expiresAt) {
      this.cache.delete(key);
      return null;
    }

    return entry.epm;
  }

  private addToCache(key: string, epm: ParsedEPM): void {
    // Enforce max cache size with LRU-like behavior
    if (this.cache.size >= this.options.maxCacheSize) {
      // Remove oldest entry
      const oldestKey = this.cache.keys().next().value;
      if (oldestKey) this.cache.delete(oldestKey);
    }

    this.cache.set(key, {
      epm,
      expiresAt: Date.now() + this.options.cacheTTL,
    });
  }

  /**
   * Clear the EPM cache
   */
  clearCache(): void {
    this.cache.clear();
  }

  /**
   * Remove a specific entry from cache
   */
  invalidate(key: string): void {
    this.cache.delete(key);
  }

  /**
   * Get cache statistics
   */
  getCacheStats(): { size: number; maxSize: number; ttl: number } {
    return {
      size: this.cache.size,
      maxSize: this.options.maxCacheSize,
      ttl: this.options.cacheTTL,
    };
  }
}

/**
 * Create an EPM resolver instance
 */
export function createEPMResolver(options?: EPMResolverOptions): EPMResolver {
  return new EPMResolver(options);
}

/**
 * Helper: Create encryption context for a recipient using EPM
 *
 * This is a convenience function that combines EPM lookup with
 * EncryptionContext creation from the flatbuffers/wasm library.
 *
 * @example
 * ```typescript
 * import { createEPMResolver } from 'sdn-js';
 * import { EncryptionContext } from 'flatbuffers-encryption';
 *
 * const resolver = createEPMResolver();
 *
 * // Add known EPM (e.g., from PNM subscription)
 * resolver.addParsedEPM('peer123', {
 *   keys: [{
 *     publicKey: '0x...',
 *     keyType: KeyType.Encryption,
 *     algorithm: 'x25519',
 *   }],
 * });
 *
 * // Get encryption key
 * const key = await resolver.getEncryptionKey('peer123');
 * if (key) {
 *   const ctx = EncryptionContext.forEncryption(key.publicKey, {
 *     algorithm: key.algorithm,
 *   });
 *   const encrypted = ctx.encryptBuffer(data);
 *   const header = ctx.getHeader();
 *   // Send header + encrypted to recipient
 * }
 * ```
 */

/**
 * SDNClient — unified client for Space Data Network nodes.
 *
 * Resolves nodes by various identifiers (PeerID, .onion, CID, IPNS, ENS, HTTP URL),
 * queries their data catalog, fetches/publishes SDS records, and manages subscriptions.
 */

import { resolveNode } from './resolver';
import type { ResolvedNode, ResolveOptions, IdentifierType } from './resolver';
import { HttpTransport, SDNTransportError } from './transport/http';
import type {
  NodeCatalog,
  SchemaCatalogEntry,
  DataQueryOptions,
  DataQueryResponse,
  DataRecord,
  PublishResult,
  BatchPublishResult,
} from './transport/http';
import { SessionAuth } from './transport/auth';
import type { AuthProvider } from './transport/auth';
import type { DerivedIdentity } from './crypto/types';
import type { ParsedEPM } from './epm-resolver';

/** Options for creating an SDNClient. */
export interface SDNClientOptions extends ResolveOptions {
  /** Pre-resolved HTTP base URL (skip resolution). */
  baseUrl?: string;
  /** Auth provider for authenticated requests. */
  authProvider?: AuthProvider;
}

/**
 * SDNClient provides a unified interface to interact with an SDN node:
 * - Discover the node via various identifier types
 * - Query its data catalog (what schemas it publishes)
 * - Fetch data by schema, day, NORAD ID, entity ID
 * - Publish data (with authentication)
 *
 * @example
 * ```ts
 * // Resolve by domain
 * const client = await SDNClient.resolve('spaceaware.io');
 * const catalog = await client.catalog();
 * console.log(catalog.schemas);
 *
 * // Query OMM data
 * const omm = await client.query({ schema: 'OMM.fbs', noradCatId: 25544, day: '2026-02-24' });
 *
 * // Authenticate and publish
 * await client.authenticate(identity);
 * await client.publish('OMM.fbs', flatbufferBytes);
 * ```
 */
export class SDNClient {
  /** The resolved node info. */
  readonly resolved: ResolvedNode;
  private transport: HttpTransport;
  private _catalog?: NodeCatalog;

  private constructor(resolved: ResolvedNode, transport: HttpTransport) {
    this.resolved = resolved;
    this.transport = transport;
  }

  /**
   * Resolve a node by any supported identifier and return a client.
   *
   * Supported identifiers:
   * - PeerID: `12D3KooW...`
   * - .onion: `abc...xyz.onion`
   * - HTTP: `https://spaceaware.io` or `spaceaware.io`
   * - Multiaddr: `/dns4/spaceaware.io/tcp/443`
   * - IPFS CID: `bafy...`
   * - IPNS: `k51...`
   * - ENS: `mynode.eth`
   */
  static async resolve(identifier: string, opts?: SDNClientOptions): Promise<SDNClient> {
    const resolved = await resolveNode(identifier, opts);

    if (!resolved.httpUrl) {
      throw new Error(
        `Could not resolve HTTP endpoint for identifier: ${identifier} (type: ${resolved.identifierType})`,
      );
    }

    const transport = new HttpTransport(resolved.httpUrl, opts?.authProvider);
    return new SDNClient(resolved, transport);
  }

  /** Create a client from an explicit HTTP URL. */
  static fromUrl(url: string, opts?: SDNClientOptions): SDNClient {
    const resolved: ResolvedNode = {
      httpUrl: url.replace(/\/+$/, ''),
      identifierType: 'http',
    };
    const transport = new HttpTransport(resolved.httpUrl!, opts?.authProvider);
    return new SDNClient(resolved, transport);
  }

  /** Create a client from a parsed EPM. */
  static fromEPM(epm: ParsedEPM): SDNClient | null {
    // Find HTTP URL from multiformat addresses
    let httpUrl: string | undefined;
    for (const addr of epm.multiformatAddresses) {
      if (addr.startsWith('https://') || addr.startsWith('http://')) {
        httpUrl = addr;
        break;
      }
      // Try to extract from dns4/dns6 multiaddr
      if (addr.startsWith('/dns4/') || addr.startsWith('/dns6/')) {
        const parts = addr.split('/');
        const hostIdx = parts.indexOf('dns4') + 1 || parts.indexOf('dns6') + 1;
        if (hostIdx > 0 && parts[hostIdx]) {
          const tcpIdx = parts.indexOf('tcp');
          const port = tcpIdx >= 0 ? parts[tcpIdx + 1] : '443';
          const scheme = port === '443' ? 'https' : 'http';
          httpUrl = `${scheme}://${parts[hostIdx]}:${port}`;
        }
      }
    }

    if (!httpUrl) return null;

    const resolved: ResolvedNode = {
      httpUrl,
      peerId: epm.peerID,
      multiaddrs: epm.multiformatAddresses,
      epm,
      identifierType: 'epm',
    };
    return new SDNClient(resolved, new HttpTransport(httpUrl));
  }

  /** The node's HTTP base URL. */
  get baseUrl(): string {
    return this.resolved.httpUrl!;
  }

  /** The node's PeerID, if known. */
  get peerId(): string | undefined {
    return this.resolved.peerId;
  }

  /** Fetch the node's schema catalog. */
  async catalog(): Promise<NodeCatalog> {
    this._catalog = await this.transport.getCatalog();
    return this._catalog;
  }

  /** Get cached catalog (call catalog() first to populate). */
  get cachedCatalog(): NodeCatalog | undefined {
    return this._catalog;
  }

  /** List schema names this node publishes. Fetches catalog if not cached. */
  async listSchemas(): Promise<string[]> {
    const cat = this._catalog || (await this.catalog());
    return cat.schemas.map((s) => s.name);
  }

  /** Query data records. */
  async query(opts: DataQueryOptions): Promise<DataRecord[]> {
    const resp = await this.transport.queryData(opts);
    return resp.results;
  }

  /** Get a single record by schema and CID. */
  async get(schema: string, cid: string): Promise<Uint8Array | null> {
    return this.transport.getRecord(schema, cid);
  }

  /** Get node info (EPM + runtime metadata). */
  async nodeInfo(): Promise<Record<string, unknown>> {
    return this.transport.getNodeInfo();
  }

  /**
   * Authenticate with the node using an HD wallet identity.
   * Required before publishing data.
   */
  async authenticate(identity: DerivedIdentity): Promise<void> {
    const auth = new SessionAuth(this.resolved.httpUrl!, identity);
    await auth.authenticate();
    // Replace transport with authenticated version
    this.transport = new HttpTransport(this.resolved.httpUrl!, auth);
  }

  /** Publish a FlatBuffer record. Requires prior authenticate() call. */
  async publish(schema: string, data: Uint8Array): Promise<PublishResult> {
    return this.transport.publishData(schema, data);
  }

  /** Publish multiple records in a batch. Requires prior authenticate() call. */
  async publishBatch(schema: string, records: Uint8Array[]): Promise<BatchPublishResult> {
    return this.transport.publishBatch(schema, records);
  }
}

// Re-export types for convenience
export type {
  NodeCatalog,
  SchemaCatalogEntry,
  DataQueryOptions,
  DataQueryResponse,
  DataRecord,
  PublishResult,
  BatchPublishResult,
  ResolvedNode,
  ResolveOptions,
  IdentifierType,
  AuthProvider,
};
export { SDNTransportError };

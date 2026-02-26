/**
 * HTTP Transport — fetch-based client for SDN server API endpoints.
 */

import type { AuthProvider } from './auth';

/** A single schema entry in the catalog. */
export interface SchemaCatalogEntry {
  name: string;
  record_count: number;
  total_bytes: number;
  oldest_epoch?: string;
  newest_epoch?: string;
}

/** Catalog response from GET /api/v1/catalog. */
export interface NodeCatalog {
  peer_id: string;
  schemas: SchemaCatalogEntry[];
  capabilities: string[];
  rate_limits: Record<string, number>;
}

/** Options for querying data. */
export interface DataQueryOptions {
  schema: string;
  day?: string;
  noradCatId?: number;
  entityId?: string;
  limit?: number;
  includeData?: boolean;
  format?: 'json' | 'flatbuffers';
}

/** A data record returned by a query. */
export interface DataRecord {
  cid: string;
  peer_id: string;
  timestamp: string;
  data_base64?: string;
}

/** Query response envelope. */
export interface DataQueryResponse {
  schema: string;
  query: Record<string, unknown>;
  count: number;
  results: DataRecord[];
}

/** Result of a publish operation. */
export interface PublishResult {
  cid: string;
  schema: string;
  stored_at: string;
  bytes: number;
}

/** Batch publish result. */
export interface BatchPublishResult {
  schema: string;
  stored_at: string;
  count: number;
  results: Array<{ cid?: string; error?: string; bytes: number }>;
}

/** HTTP transport for SDN server APIs. */
export class HttpTransport {
  private baseUrl: string;
  private authProvider?: AuthProvider;

  constructor(baseUrl: string, authProvider?: AuthProvider) {
    this.baseUrl = baseUrl.replace(/\/+$/, '');
    this.authProvider = authProvider;
  }

  /** Fetch the node's schema catalog. */
  async getCatalog(): Promise<NodeCatalog> {
    const resp = await this.fetch('/api/v1/catalog');
    return resp.json();
  }

  /** Query data records for a schema. */
  async queryData(opts: DataQueryOptions): Promise<DataQueryResponse> {
    const params = new URLSearchParams();
    if (opts.day) params.set('day', opts.day);
    if (opts.noradCatId !== undefined) params.set('norad_cat_id', String(opts.noradCatId));
    if (opts.entityId) params.set('entity_id', opts.entityId);
    if (opts.limit) params.set('limit', String(opts.limit));
    if (opts.includeData) params.set('include_data', 'true');
    params.set('format', 'json');

    const qs = params.toString();
    const path = `/api/v1/data/query/${encodeURIComponent(opts.schema)}${qs ? '?' + qs : ''}`;
    const resp = await this.fetch(path);
    return resp.json();
  }

  /** Get a single record by CID. */
  async getRecord(schema: string, cid: string): Promise<Uint8Array | null> {
    const resp = await this.fetch(
      `/api/v1/data/query/${encodeURIComponent(schema)}?cid=${encodeURIComponent(cid)}&include_data=true&format=json`,
    );
    const data: DataQueryResponse = await resp.json();
    if (data.results.length === 0 || !data.results[0].data_base64) return null;

    // Decode base64
    const b64 = data.results[0].data_base64;
    const binary = atob(b64);
    const bytes = new Uint8Array(binary.length);
    for (let i = 0; i < binary.length; i++) {
      bytes[i] = binary.charCodeAt(i);
    }
    return bytes;
  }

  /** Publish a single FlatBuffer record. Requires authentication. */
  async publishData(schema: string, data: Uint8Array): Promise<PublishResult> {
    const resp = await this.fetch(`/api/v1/data/publish/${encodeURIComponent(schema)}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/octet-stream' },
      body: data.buffer.slice(data.byteOffset, data.byteOffset + data.byteLength) as ArrayBuffer,
    });
    return resp.json();
  }

  /** Publish multiple records as a uint32BE-length-prefixed stream. */
  async publishBatch(schema: string, records: Uint8Array[]): Promise<BatchPublishResult> {
    // Build length-prefixed stream
    let totalLen = 0;
    for (const rec of records) totalLen += 4 + rec.length;

    const stream = new Uint8Array(totalLen);
    const view = new DataView(stream.buffer);
    let offset = 0;
    for (const rec of records) {
      view.setUint32(offset, rec.length, false); // big-endian
      offset += 4;
      stream.set(rec, offset);
      offset += rec.length;
    }

    const resp = await this.fetch(`/api/v1/data/publish/batch/${encodeURIComponent(schema)}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/octet-stream' },
      body: stream.buffer as ArrayBuffer,
    });
    return resp.json();
  }

  /** Get node info. */
  async getNodeInfo(): Promise<Record<string, unknown>> {
    const resp = await this.fetch('/api/node/info');
    return resp.json();
  }

  /** Internal fetch with auth headers. */
  private async fetch(path: string, init?: RequestInit): Promise<Response> {
    const url = this.baseUrl + path;
    const headers: Record<string, string> = {
      Accept: 'application/json',
      ...(init?.headers as Record<string, string>),
    };

    if (this.authProvider) {
      const authHeaders = await this.authProvider.getAuthHeaders();
      Object.assign(headers, authHeaders);
    }

    const resp = await globalThis.fetch(url, {
      ...init,
      headers,
      credentials: 'include', // send cookies for session auth
    });

    if (!resp.ok) {
      const body = await resp.text().catch(() => '');
      throw new SDNTransportError(resp.status, body, url);
    }

    return resp;
  }
}

/** Error thrown by HttpTransport on non-2xx responses. */
export class SDNTransportError extends Error {
  status: number;
  body: string;
  url: string;

  constructor(status: number, body: string, url: string) {
    super(`HTTP ${status}: ${body.slice(0, 200)}`);
    this.name = 'SDNTransportError';
    this.status = status;
    this.body = body;
    this.url = url;
  }
}

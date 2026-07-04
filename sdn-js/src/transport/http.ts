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

/** Content type of the aligned size-prefixed FlatBuffer record stream. */
export const FLATBUFFER_STREAM_CONTENT_TYPE = 'application/vnd.sdn.flatbuffers.stream';

/**
 * Options for querying data via the flow-served bulk endpoint
 * (`GET /api/v1/data/<standard>/bulk`, loop C.4 — param parsing, profile
 * resolution, format selection and ETag/304 handling run inside the
 * data-retrieval WASM flow).
 */
export interface DataQueryOptions {
  /** SDS schema/standard identifier (`OMM.fbs`, `OMM`, or `omm`). */
  schema: string;
  /**
   * Epoch profile (`nearest` / `as_of` / `forward`; `mode` alias accepted
   * server-side). Absent = retrieval-module default (`nearest`).
   */
  profile?: string;
  /** Target epoch: unix seconds (fractional allowed) or RFC3339. Default: now (server-side). */
  epoch?: number | string;
  /** Row limit (positive integer). */
  limit?: number;
  /** Provider source partition (bare source name, e.g. `celestrak-gp`). */
  source?: string;
  /**
   * Wire format. DEFAULT `flatbuffers` — the aligned size-prefixed
   * FlatBuffer record stream, consumed zero-copy. `json` is the opt-in
   * edge adapter.
   */
  format?: 'flatbuffers' | 'json';
  /**
   * Cached entity tag for a conditional request. Sent as `If-None-Match`;
   * a 304 response comes back as `notModified: true` with an empty body —
   * serve the local engine store copy.
   */
  ifNoneMatch?: string | null;
}

/**
 * FlatBuffer-stream query result (the DEFAULT): the verbatim aligned
 * size-prefixed record stream plus the flow's caching/count headers.
 */
export interface DataQueryStreamResult {
  format: 'flatbuffers';
  /** HTTP status (200, or 304 for a conditional-request hit). */
  status: number;
  /** True when the server answered 304 Not Modified (stream is empty). */
  notModified: boolean;
  /** `ETag` header (`W/"fnv1a64-<hex>"`), for the next conditional request. */
  etag: string | null;
  /** `X-SDN-Record-Count` header (0 when absent / not modified). */
  recordCount: number;
  /** Aligned size-prefixed (u32 LE) FlatBuffer frames — the verbatim body. */
  stream: Uint8Array;
  /** Zero-copy per-record frame iterator (subarray views into `stream`). */
  frames(): Generator<Uint8Array, void, undefined>;
}

/** JSON edge-adapter query result (opt-in via `format: 'json'`). */
export interface DataQueryJsonResult {
  format: 'json';
  status: number;
  notModified: boolean;
  etag: string | null;
  count: number;
  records: Array<Record<string, unknown>>;
}

/**
 * Iterate the frames of an aligned size-prefixed FlatBuffer record stream
 * (u32 little-endian length prefixes — the engine/server wire format,
 * `X-SDN-Stream-Format: flatsql-size-prefixed-le-u32`). Every yielded frame
 * is a zero-copy subarray view into `stream`.
 */
export function* iterateSizePrefixedFrames(stream: Uint8Array): Generator<Uint8Array, void, undefined> {
  const view = new DataView(stream.buffer, stream.byteOffset, stream.byteLength);
  let offset = 0;
  let index = 0;
  while (offset < stream.byteLength) {
    if (stream.byteLength - offset < 4) {
      throw new Error(`Invalid FlatBuffer record stream: truncated frame header at offset ${offset}`);
    }
    const length = view.getUint32(offset, true);
    offset += 4;
    const next = offset + length;
    if (next > stream.byteLength) {
      throw new Error(`Invalid FlatBuffer record stream: truncated frame at index ${index}`);
    }
    yield stream.subarray(offset, next);
    offset = next;
    index += 1;
  }
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

/** Log head response from GET /api/v1/log/{schema}/head. */
export interface LogHeadResponse {
  schema_type: string;
  publisher_peer_id: string;
  head_sequence: number;
  head_entry_hash: string;
  record_count: number;
  oldest_epoch_day: string;
  newest_epoch_day: string;
}

/** A single publisher's log head info. */
export interface LogHeadInfo {
  publisher_peer_id: string;
  schema_type: string;
  head_sequence: number;
  head_entry_hash: string;
  timestamp: string;
}

/** Log heads response from GET /api/v1/log/{schema}/heads. */
export interface LogHeadsResponse {
  schema_type: string;
  count: number;
  heads: LogHeadInfo[];
}

export interface ChannelAccessOptions {
  subject?: string;
  grantId?: string;
  visibility?: string;
  encryptedStreamHeader?: string;
  encryptedRecordIndex?: number | string;
}

export interface ChannelListOptions extends ChannelAccessOptions {
  standardCode?: string;
}

export interface ChannelSummary {
  channelId: string;
  sourceId?: string;
  standardCode: string;
  feedUuid?: string | null;
  visibility?: string;
  subscribed?: boolean;
  pnmVerified?: boolean;
  dpmVerified?: boolean;
  grantState?: string;
  encryptionState?: string;
  [key: string]: unknown;
}

export interface ChannelMonitor extends ChannelSummary {
  channelHead?: string;
  providerPeer?: string;
  localRows?: number;
  remoteRows?: number;
  syncedRows?: number;
  missingRows?: number;
  pinnedCount?: number;
  pinnedRows?: number;
  pinnedBytes?: number;
  syncedBytes?: number;
  throughputBytesPerSecond?: number;
  wireSpeedUtilization?: number | null;
  timingsMs?: ChannelMonitorTimings;
  lastVerifiedUpdate?: string;
}

export interface ChannelMonitorTimings {
  discovery?: number;
  grantNegotiation?: number;
  pnmDpmVerification?: number;
  transfer?: number;
  decrypt?: number;
  hashVerification?: number;
  durableImport?: number;
}

export type ChannelActionResponse = Record<string, unknown>;
export type ChannelGrantRequest = Record<string, unknown>;

export interface ChannelKeyEnvelopeRequest {
  recipientKeyId: string;
  contentKeyId?: string;
}

export interface ChannelKeyEnvelopeResponse {
  channelId: string;
  sourceId?: string;
  standardCode: string;
  feedUuid?: string | null;
  grantState?: string;
  contentKeyId: string;
  recipientKeyId: string;
  keyEpoch?: string;
  algorithm?: string;
  envelopeCid?: string;
  wrappedKeyEnvelopeBase64?: string;
  [key: string]: unknown;
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

  /**
   * Query data records via the flow-served bulk endpoint
   * (`GET /api/v1/data/<standard>/bulk`).
   *
   * FLATBUFFERS-FIRST (loop D.3): the default is ONE request whose body is
   * the aligned size-prefixed FlatBuffer record stream, returned verbatim
   * for zero-copy consumption (`frames()` yields subarray views; feed
   * `stream` straight into `ingestFlatBufferStream` for persistence). JSON
   * is the opt-in edge adapter (`format: 'json'`). Pass `ifNoneMatch` with
   * a previously returned `etag` to make the request conditional; a 304
   * comes back as `notModified: true` — serve the local engine store copy.
   */
  async queryData(opts: DataQueryOptions & { format: 'json' }): Promise<DataQueryJsonResult>;
  async queryData(opts: DataQueryOptions & { format?: 'flatbuffers' }): Promise<DataQueryStreamResult>;
  async queryData(opts: DataQueryOptions): Promise<DataQueryStreamResult | DataQueryJsonResult>;
  async queryData(opts: DataQueryOptions): Promise<DataQueryStreamResult | DataQueryJsonResult> {
    const format = opts.format === 'json' ? 'json' : 'flatbuffers';
    const params = new URLSearchParams();
    if (opts.source) params.set('source', opts.source);
    if (opts.profile) params.set('profile', opts.profile);
    if (opts.epoch !== undefined && opts.epoch !== null) params.set('epoch', String(opts.epoch));
    if (opts.limit && opts.limit > 0) params.set('limit', String(Math.floor(opts.limit)));
    if (format === 'json') params.set('format', 'json');

    const qs = params.toString();
    const path = `${dataBulkPath(opts.schema)}${qs ? '?' + qs : ''}`;
    const headers: Record<string, string> = {
      Accept: format === 'json' ? 'application/json' : FLATBUFFER_STREAM_CONTENT_TYPE,
    };
    if (opts.ifNoneMatch) headers['If-None-Match'] = opts.ifNoneMatch;

    const resp = await this.fetch(path, { headers }, { allowStatuses: [304] });
    const etag = resp.headers?.get?.('etag') ?? null;
    const notModified = resp.status === 304;

    if (format === 'json') {
      const payload = notModified ? {} : ((await resp.json()) as Record<string, unknown>);
      const records = Array.isArray(payload.records)
        ? (payload.records as Array<Record<string, unknown>>)
        : [];
      return {
        format: 'json',
        status: resp.status,
        notModified,
        etag,
        count: typeof payload.count === 'number' ? payload.count : records.length,
        records,
      };
    }

    const stream = notModified ? new Uint8Array(0) : new Uint8Array(await resp.arrayBuffer());
    const recordCountHeader = resp.headers?.get?.('x-sdn-record-count');
    const recordCount = recordCountHeader ? Number.parseInt(recordCountHeader, 10) || 0 : 0;
    return {
      format: 'flatbuffers',
      status: resp.status,
      notModified,
      etag,
      recordCount,
      stream,
      frames: () => iterateSizePrefixedFrames(stream),
    };
  }

  /**
   * Get a single record's raw FlatBuffer bytes by CID
   * (`GET /api/v1/data/records/{schema}/{cid}`, `application/x-flatbuffers`
   * — no base64, no JSON envelope). Returns null when the record is absent.
   */
  async getRecord(schema: string, cid: string): Promise<Uint8Array | null> {
    const resp = await this.fetch(
      `/api/v1/data/records/${encodeURIComponent(schema)}/${encodeURIComponent(cid)}`,
      { headers: { Accept: 'application/x-flatbuffers' } },
      { allowStatuses: [404] },
    );
    if (resp.status === 404) return null;
    return new Uint8Array(await resp.arrayBuffer());
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

  /** Get log head for a publisher+schema. */
  async getLogHead(schema: string, publisherPeerID: string): Promise<LogHeadResponse> {
    const params = new URLSearchParams({ publisher: publisherPeerID });
    const resp = await this.fetch(`/api/v1/log/${encodeURIComponent(schema)}/head?${params}`);
    return resp.json();
  }

  /** Get all publishers' log heads for a schema. */
  async getLogHeads(schema: string): Promise<LogHeadsResponse> {
    const resp = await this.fetch(`/api/v1/log/${encodeURIComponent(schema)}/heads`);
    return resp.json();
  }

  /** Get node info. */
  async getNodeInfo(): Promise<Record<string, unknown>> {
    const resp = await this.fetch('/api/node/info');
    return resp.json();
  }

  async listChannels(options: ChannelListOptions = {}): Promise<ChannelSummary[]> {
    const query = new URLSearchParams();
    if (options.standardCode) query.set('standardCode', options.standardCode);
    appendChannelAccessQuery(query, options);
    const suffix = query.size > 0 ? `?${query.toString()}` : '';
    const resp = await this.fetch(`/api/v1/channels${suffix}`);
    const payload = await resp.json();
    return channelRowsFromPayload(payload);
  }

  async getChannel(channelId: string, options?: ChannelAccessOptions): Promise<ChannelSummary> {
    const query = new URLSearchParams();
    appendChannelActionAccessQuery(query, options);
    const suffix = query.size > 0 ? `?${query.toString()}` : '';
    const resp = await this.fetch(`/api/v1/channels/${encodeURIComponent(channelId)}${suffix}`);
    return resp.json();
  }

  async subscribeChannel(channelId: string, options?: ChannelAccessOptions): Promise<ChannelActionResponse> {
    return this.postChannelAction(channelId, 'subscribe', options);
  }

  async unsubscribeChannel(channelId: string, options?: ChannelAccessOptions): Promise<ChannelActionResponse> {
    return this.postChannelAction(channelId, 'unsubscribe', options);
  }

  async publishChannelStream(channelId: string, stream: Uint8Array, options?: ChannelAccessOptions): Promise<ChannelActionResponse> {
    const resp = await this.fetch(channelActionPath(channelId, 'publish', options), {
      method: 'POST',
      headers: channelStreamPublishHeaders(options),
      body: stream.buffer.slice(stream.byteOffset, stream.byteOffset + stream.byteLength) as ArrayBuffer,
    });
    return resp.json();
  }

  async issueChannelGrant(channelId: string, grant: ChannelGrantRequest, options?: ChannelAccessOptions): Promise<ChannelActionResponse> {
    const resp = await this.fetch(channelActionPath(channelId, 'grants', options), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(grant),
    });
    return resp.json();
  }

  async requestChannelKeyEnvelope(
    channelId: string,
    request: ChannelKeyEnvelopeRequest,
    options?: ChannelAccessOptions,
  ): Promise<ChannelKeyEnvelopeResponse> {
    const resp = await this.fetch(channelActionPath(channelId, 'key-unwrap', options), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        recipientKeyId: request.recipientKeyId,
        ...(request.contentKeyId ? { contentKeyId: request.contentKeyId } : {}),
      }),
    });
    return resp.json();
  }

  async openChannelStream(channelId: string, options?: ChannelAccessOptions): Promise<Uint8Array> {
    const resp = await this.fetch(channelActionPath(channelId, 'stream', options), {
      headers: { Accept: 'application/vnd.sdn.flatbuffers.stream' },
    });
    return new Uint8Array(await resp.arrayBuffer());
  }

  async openChannelModuleFeed(channelId: string, options?: ChannelAccessOptions): Promise<Uint8Array> {
    const resp = await this.fetch(channelActionPath(channelId, 'module-feed', options), {
      method: 'POST',
      headers: { Accept: 'application/vnd.sdn.flatbuffers.stream' },
    });
    return new Uint8Array(await resp.arrayBuffer());
  }

  async monitorChannel(channelId: string, options?: ChannelAccessOptions): Promise<ChannelMonitor> {
    const resp = await this.fetch(channelActionPath(channelId, 'monitor', options));
    return resp.json();
  }

  private async postChannelAction(channelId: string, action: string, options?: ChannelAccessOptions): Promise<ChannelActionResponse> {
    const resp = await this.fetch(channelActionPath(channelId, action, options), { method: 'POST' });
    return resp.json();
  }

  /** Internal fetch with auth headers. */
  private async fetch(
    path: string,
    init?: RequestInit,
    opts?: { allowStatuses?: number[] },
  ): Promise<Response> {
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

    if (!resp.ok && !opts?.allowStatuses?.includes(resp.status)) {
      const body = await resp.text().catch(() => '');
      throw new SDNTransportError(resp.status, body, url);
    }

    return resp;
  }
}

/** Flow-served bulk retrieval path for a schema (`OMM.fbs` → `/api/v1/data/omm/bulk`). */
function dataBulkPath(schema: string): string {
  const standard = schema.trim().split('.')[0]?.toLowerCase() ?? '';
  if (!standard) throw new Error(`invalid schema for bulk data query: ${JSON.stringify(schema)}`);
  return `/api/v1/data/${encodeURIComponent(standard)}/bulk`;
}

function channelActionPath(channelId: string, action: string, options?: ChannelAccessOptions): string {
  const query = new URLSearchParams();
  appendChannelActionAccessQuery(query, options);
  const suffix = query.size > 0 ? `?${query.toString()}` : '';
  return `/api/v1/channels/${encodeURIComponent(channelId)}/${action}${suffix}`;
}

function appendChannelAccessQuery(query: URLSearchParams, options?: ChannelAccessOptions): void {
  if (options?.visibility) query.set('visibility', options.visibility);
  if (options?.subject) query.set('subject', options.subject);
  if (options?.grantId) query.set('grantId', options.grantId);
}

function appendChannelActionAccessQuery(query: URLSearchParams, options?: ChannelAccessOptions): void {
  if (options?.subject) query.set('subject', options.subject);
  if (options?.grantId) query.set('grantId', options.grantId);
  if (options?.visibility) query.set('visibility', options.visibility);
}

function channelStreamPublishHeaders(options?: ChannelAccessOptions): Record<string, string> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/vnd.sdn.flatbuffers.stream',
  };
  if (isPrivateChannelVisibility(options?.visibility)) {
    headers['X-SDN-Encrypted-Stream'] = 'true';
    if (options?.encryptedStreamHeader?.trim()) {
      headers['X-SDN-Encrypted-Stream-Header'] = options.encryptedStreamHeader;
    }
    if (options?.encryptedRecordIndex !== undefined && options.encryptedRecordIndex !== null && String(options.encryptedRecordIndex).trim()) {
      headers['X-SDN-Encrypted-Record-Index'] = String(options.encryptedRecordIndex).trim();
    }
  }
  return headers;
}

function isPrivateChannelVisibility(visibility: string | undefined): boolean {
  const value = visibility?.trim().toLowerCase() ?? '';
  return value === 'private' || value.startsWith('private-');
}

function channelRowsFromPayload(payload: unknown): ChannelSummary[] {
  if (Array.isArray(payload)) return payload.filter(isRecord) as ChannelSummary[];
  if (isRecord(payload) && Array.isArray(payload.results)) return payload.results.filter(isRecord) as ChannelSummary[];
  return [];
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
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

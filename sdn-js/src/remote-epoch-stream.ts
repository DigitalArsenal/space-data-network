/** Conditional SDS retrieval with a bounded cache of exact response bytes. */
import { iterateSizePrefixedFrames } from './transport/http';
import type { DataQueryOptions, DataQueryStreamResult } from './transport/http';
import type { EngineEpochQueryRequest } from './epoch-query-sql';

export interface EpochStreamTransport {
  queryData(opts: DataQueryOptions & { format?: 'flatbuffers' }): Promise<DataQueryStreamResult>;
}

export interface EpochStreamLocalStore {
  ingestFlatBufferStream(
    standardId: string,
    streamBytes: Uint8Array,
    options?: { source?: string | null } | null,
  ): Promise<number>;
  /** Retained for compatibility; conditional responses do not query this store. */
  queryEpochRawStream(standardId: string, request?: EngineEpochQueryRequest | null): Uint8Array;
}

export interface RemoteEpochStreamRequest {
  /** SDS standard identifier (`OMM.fbs`, `OMM`, or `omm`). */
  schema: string;
  profile?: string;
  /** Target epoch (unix seconds or RFC3339). Absent = server default. */
  epoch?: number | string;
  limit?: number;
  /** Remote provider source partition. */
  source?: string;
  /** Local source partition. Defaults to `source`, then the store default. */
  ingestSource?: string;
}

export interface RemoteEpochStreamCacheOptions {
  /** Maximum retained responses (default 16). Zero disables the cache. */
  maxEntries?: number;
  /** Maximum retained payload bytes (default 32 MiB). Oversized bodies are not cached. */
  maxBytes?: number;
}

export interface RemoteEpochStreamResult {
  /** Caller-owned aligned size-prefixed FlatBuffer record stream. */
  stream: Uint8Array;
  /** Zero-copy frame views into this result's stream. */
  frames(): Generator<Uint8Array, void, undefined>;
  /** True when a 304 validated the retained response bytes. */
  fromCache: boolean;
  /** @deprecated Alias for fromCache; this no longer denotes a local database query. */
  fromLocalStore: boolean;
  etag: string | null;
  /** Record count derived from the validated framing. */
  recordCount: number;
  /** Newly materialized records (zero on cache replay). */
  ingested: number;
}

interface CachedResponse {
  etag: string;
  stream: Uint8Array;
  recordCount: number;
}

export class RemoteEpochStreamClient {
  private readonly cache = new Map<string, CachedResponse>();
  private cachedBytes = 0;
  private readonly maxEntries: number;
  private readonly maxBytes: number;
  private cacheGeneration = 0;

  constructor(
    private readonly transport: EpochStreamTransport,
    private readonly store: EpochStreamLocalStore,
    options: RemoteEpochStreamCacheOptions = {},
  ) {
    this.maxEntries = cacheLimit(options.maxEntries, 16, 'maxEntries');
    this.maxBytes = cacheLimit(options.maxBytes, 32 * 1024 * 1024, 'maxBytes');
  }

  async fetchEpochStream(request: RemoteEpochStreamRequest): Promise<RemoteEpochStreamResult> {
    const standardId = standardIdFor(request.schema);
    const key = this.cacheKey(request);
    const generation = this.cacheGeneration;
    const cached = this.cache.get(key);
    const options: DataQueryOptions & { format?: 'flatbuffers' } = {
      schema: request.schema,
      ...(request.profile ? { profile: request.profile } : {}),
      ...(request.epoch !== undefined ? { epoch: request.epoch } : {}),
      ...(request.limit !== undefined ? { limit: request.limit } : {}),
      ...(request.source ? { source: request.source } : {}),
    };
    let result = await this.transport.queryData({ ...options, ...(cached ? { ifNoneMatch: cached.etag } : {}) });
    if (result.notModified) {
      if (cached && generation === this.cacheGeneration &&
          (!result.etag || weakTag(result.etag) === weakTag(cached.etag))) {
        const etag = result.etag ?? cached.etag;
        // Do not overwrite a newer response inserted by a concurrent request.
        if (this.cache.get(key) === cached) {
          this.remove(key);
          if (!noStore(result.cacheControl)) this.remember(key, { ...cached, etag });
        }
        return response(cached.stream.slice(), true, etag, cached.recordCount, 0);
      }
      this.remove(key);
      result = await this.transport.queryData(options);
      if (result.notModified) throw new Error('HTTP 304 without a retained response after unconditional retry');
    }

    // Parse before ingestion; a missing or incorrect count header is not truth.
    if (noStore(result.cacheControl)) this.remove(key);
    let recordCount = 0;
    for (const _frame of iterateSizePrefixedFrames(result.stream)) recordCount++;
    const retained = result.etag && !noStore(result.cacheControl) &&
      this.maxEntries > 0 && this.maxBytes > 0 && result.stream.byteLength <= this.maxBytes
      ? result.stream.slice() : null;
    const ingestSource = request.ingestSource ?? request.source;
    const ingested = await this.store.ingestFlatBufferStream(
      standardId, result.stream, ingestSource ? { source: ingestSource } : null,
    );
    if (generation === this.cacheGeneration) {
      this.remove(key);
      if (retained && result.etag) this.remember(key, { etag: result.etag, stream: retained, recordCount });
    }
    return response(result.stream, false, result.etag, recordCount, ingested);
  }

  cachedEtag(request: RemoteEpochStreamRequest): string | null {
    return this.cache.get(this.cacheKey(request))?.etag ?? null;
  }

  /** Release cached responses, for example when the caller's session changes. */
  clearCache(): void {
    this.cacheGeneration++;
    this.cache.clear();
    this.cachedBytes = 0;
  }

  private cacheKey(request: RemoteEpochStreamRequest): string {
    return JSON.stringify([
      standardIdFor(request.schema), request.profile ?? '', request.epoch ?? null,
      request.limit ?? null, request.source ?? '', request.ingestSource ?? request.source ?? '',
    ]);
  }

  private remove(key: string): void {
    const previous = this.cache.get(key);
    if (previous) this.cachedBytes -= previous.stream.byteLength;
    this.cache.delete(key);
  }

  private remember(key: string, value: CachedResponse): void {
    this.cache.set(key, value);
    this.cachedBytes += value.stream.byteLength;
    while (this.cache.size > this.maxEntries || this.cachedBytes > this.maxBytes) {
      const oldest = this.cache.keys().next().value;
      if (oldest === undefined) break;
      this.remove(oldest);
    }
  }
}

function response(stream: Uint8Array, fromCache: boolean, etag: string | null,
  recordCount: number, ingested: number): RemoteEpochStreamResult {
  return { stream, frames: () => iterateSizePrefixedFrames(stream), fromCache,
    fromLocalStore: fromCache, etag, recordCount, ingested };
}

function cacheLimit(value: number | undefined, fallback: number, name: string): number {
  if (value === undefined) return fallback;
  if (!Number.isSafeInteger(value) || value < 0) throw new Error(`${name} must be a non-negative safe integer`);
  return value;
}

function weakTag(tag: string): string { return tag.trim().replace(/^W\//, ''); }
function noStore(value?: string | null): boolean { return /(?:^|,)\s*no-store\s*(?:,|$)/i.test(value ?? ''); }

function standardIdFor(schema: string): string {
  const standardId = schema.trim().split('.')[0]?.toUpperCase() ?? '';
  if (!standardId) throw new Error(`invalid schema for epoch stream: ${JSON.stringify(schema)}`);
  return standardId;
}

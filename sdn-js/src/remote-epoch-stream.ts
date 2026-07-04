/**
 * Remote epoch-stream client (loop D.3) — ONE conditional stream request.
 *
 * Pulls bulk SDS records from a flow-served SDN endpoint
 * (`GET /api/v1/data/<standard>/bulk`, default Content-Type
 * `application/vnd.sdn.flatbuffers.stream`) and feeds the aligned
 * size-prefixed FlatBuffer stream STRAIGHT into the local FlatSQL-WASM
 * engine store (`ingestFlatBufferStream`) — no base64, no JSON re-encode,
 * no per-record round-trips.
 *
 * Conditional requests: the client remembers the `ETag` (`W/"fnv1a64-…"`)
 * per query shape and sends `If-None-Match` once it holds a local copy.
 * A 304 answer is served from the local engine store via
 * `queryEpochRawStream` — the same profile SQL the server ran, byte-parity
 * proven by the D.2 isomorphism harness.
 */

import type {
  DataQueryOptions,
  DataQueryStreamResult,
} from './transport/http';
import type { EngineEpochQueryRequest } from './epoch-query-sql';

/** The transport surface this client needs (HttpTransport satisfies it). */
export interface EpochStreamTransport {
  queryData(opts: DataQueryOptions & { format?: 'flatbuffers' }): Promise<DataQueryStreamResult>;
}

/**
 * The local engine store surface this client needs
 * (FlatSQLEngineRecordStore satisfies it).
 */
export interface EpochStreamLocalStore {
  ingestFlatBufferStream(
    standardId: string,
    streamBytes: Uint8Array,
    options?: { source?: string | null } | null,
  ): Promise<number>;
  queryEpochRawStream(standardId: string, request?: EngineEpochQueryRequest | null): Uint8Array;
}

export interface RemoteEpochStreamRequest {
  /** SDS schema/standard identifier (`OMM.fbs`, `OMM`, or `omm`). */
  schema: string;
  /** Epoch profile (`nearest` / `as_of` / `forward`). Absent = server default. */
  profile?: string;
  /**
   * Target epoch (unix seconds or RFC3339). Pin this when you rely on
   * 304-from-local-store replay — an absent epoch defaults to "now" on both
   * hosts, which drifts between the original fetch and the replay.
   */
  epoch?: number | string;
  /** Row limit (positive integer). */
  limit?: number;
  /** Remote provider source partition (bare name, e.g. `celestrak-gp`). */
  source?: string;
  /**
   * Local source partition the stream materializes into
   * (`<Table>@<source>` shadow table). Defaults to `source`, falling back
   * to the store default (`local`).
   */
  ingestSource?: string;
}

export interface RemoteEpochStreamResult {
  /** Aligned size-prefixed FlatBuffer record stream (u32 LE framing). */
  stream: Uint8Array;
  /** Zero-copy per-record frame iterator over `stream`. */
  frames(): Generator<Uint8Array, void, undefined>;
  /** True when the server answered 304 and the local engine store served the bytes. */
  fromLocalStore: boolean;
  /** Entity tag the response validated against (cached for the next request). */
  etag: string | null;
  /** Server record count (200) or local frame count (304 replay). */
  recordCount: number;
  /** Records newly materialized into the local store by this call (0 on 304). */
  ingested: number;
}

/**
 * Conditional bulk-stream fetcher: one HTTP request per call, `If-None-Match`
 * once a local copy exists, 304 served zero-copy from the local engine store.
 */
export class RemoteEpochStreamClient {
  private readonly etags = new Map<string, string>();

  constructor(
    private readonly transport: EpochStreamTransport,
    private readonly store: EpochStreamLocalStore,
  ) {}

  async fetchEpochStream(request: RemoteEpochStreamRequest): Promise<RemoteEpochStreamResult> {
    const standardId = standardIdFor(request.schema);
    const cacheKey = this.cacheKey(request);
    const cachedEtag = this.etags.get(cacheKey) ?? null;

    const result = await this.transport.queryData({
      schema: request.schema,
      ...(request.profile ? { profile: request.profile } : {}),
      ...(request.epoch !== undefined ? { epoch: request.epoch } : {}),
      ...(request.limit !== undefined ? { limit: request.limit } : {}),
      ...(request.source ? { source: request.source } : {}),
      ...(cachedEtag ? { ifNoneMatch: cachedEtag } : {}),
    });

    if (result.notModified) {
      const localStream = this.store.queryEpochRawStream(standardId, this.localReplayRequest(request));
      return {
        stream: localStream,
        frames: () => iterateFrames(localStream),
        fromLocalStore: true,
        etag: result.etag ?? cachedEtag,
        recordCount: countFrames(localStream),
        ingested: 0,
      };
    }

    const ingestSource = request.ingestSource ?? request.source;
    const ingested = await this.store.ingestFlatBufferStream(
      standardId,
      result.stream,
      ingestSource ? { source: ingestSource } : null,
    );
    if (result.etag) {
      this.etags.set(cacheKey, result.etag);
    } else {
      this.etags.delete(cacheKey);
    }
    return {
      stream: result.stream,
      frames: result.frames,
      fromLocalStore: false,
      etag: result.etag,
      recordCount: result.recordCount,
      ingested,
    };
  }

  /** The etag currently cached for a query shape (tests / introspection). */
  cachedEtag(request: RemoteEpochStreamRequest): string | null {
    return this.etags.get(this.cacheKey(request)) ?? null;
  }

  private cacheKey(request: RemoteEpochStreamRequest): string {
    return JSON.stringify([
      standardIdFor(request.schema),
      request.profile ?? '',
      request.epoch ?? null,
      request.limit ?? null,
      request.source ?? '',
    ]);
  }

  private localReplayRequest(request: RemoteEpochStreamRequest): EngineEpochQueryRequest {
    return {
      ...(request.profile ? { profile: request.profile } : {}),
      ...(request.epoch !== undefined ? { epoch: epochSeconds(request.epoch) } : {}),
      // The flow treats an absent/non-positive limit as unlimited; mirror it.
      limit: request.limit !== undefined && request.limit > 0 ? request.limit : -1,
      ...(request.source ? { source: request.source } : {}),
    };
  }
}

function standardIdFor(schema: string): string {
  const standardId = schema.trim().split('.')[0]?.toUpperCase() ?? '';
  if (!standardId) throw new Error(`invalid schema for epoch stream: ${JSON.stringify(schema)}`);
  return standardId;
}

function epochSeconds(epoch: number | string): number {
  if (typeof epoch === 'number') return epoch;
  const parsed = Date.parse(epoch);
  if (!Number.isFinite(parsed)) throw new Error(`invalid epoch: ${JSON.stringify(epoch)}`);
  return parsed / 1000;
}

function* iterateFrames(stream: Uint8Array): Generator<Uint8Array, void, undefined> {
  const view = new DataView(stream.buffer, stream.byteOffset, stream.byteLength);
  let offset = 0;
  while (offset < stream.byteLength) {
    const length = view.getUint32(offset, true);
    offset += 4;
    yield stream.subarray(offset, offset + length);
    offset += length;
  }
}

function countFrames(stream: Uint8Array): number {
  const view = new DataView(stream.buffer, stream.byteOffset, stream.byteLength);
  let offset = 0;
  let count = 0;
  while (offset < stream.byteLength) {
    offset += 4 + view.getUint32(offset, true);
    count += 1;
  }
  return count;
}

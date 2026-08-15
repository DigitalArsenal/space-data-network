/**
 * GeoPlaces ($GNP) local cache lane — cache-first gazetteer serving.
 *
 * The GeoNames-derived gazetteer is a bulk dataset: big, slow-moving, and
 * identified by a `DATASET_EPOCH`. This lane composes the EXISTING sdn-js
 * pieces rather than inventing a second client shape:
 *
 *   - `RemoteEpochStreamClient` (remote-epoch-stream.ts) does the ONE
 *     conditional bulk request — `If-None-Match` per query shape, 304 served
 *     byte-identically from the local FlatSQL engine store.
 *   - `FlatSQLEngineRecordStore` (engine-record-store.ts) is the local store:
 *     `ingestFlatBufferStream` in, `queryEpochRawStream` out.
 *   - A channel subscription (channels.ts topic grammar,
 *     `/spacedatanetwork/channels/<STANDARD>/<source>`) carries the
 *     dataset-epoch announce that triggers background revalidation.
 *
 * Semantics: stale-while-revalidate. Once ANY epoch is cached, reads are
 * answered from the local engine store immediately and never block on the
 * network; a new-epoch announce (or an explicit `revalidate()`) fetches in the
 * background and atomically swaps the served epoch only after the new bytes
 * are in the store. A failed revalidate never poisons the cache — the previous
 * epoch keeps serving and the error is surfaced on `lastError`.
 *
 * All transports/stores are injected ports (repo DI style) — this module opens
 * no sockets and loads no external-origin bytes of its own.
 */

import { CHANNEL_TOPIC_PREFIX } from './channels';
import {
  RemoteEpochStreamClient,
  type EpochStreamLocalStore,
  type EpochStreamTransport,
} from './remote-epoch-stream';

/** SDS standard code for the GeoPlaces gazetteer (FlatSQL table `sds_gnp`). */
export const GEONAMES_STANDARD_CODE = 'GNP';

/** Provider/source partition tag for the GeoNames-derived dataset. */
export const GEONAMES_SOURCE = 'geonames';

/**
 * Per-(standard, source) channel topic the dataset-epoch announce rides on.
 * Mirrors `channels.channelDiscoveryTopic` grammar with the source segment
 * appended — never a divergent prefix.
 */
export function geoPlacesEpochTopic(
  source: string = GEONAMES_SOURCE,
  standardCode: string = GEONAMES_STANDARD_CODE,
): string {
  const code = standardCode.trim().split('.')[0]?.toUpperCase() ?? '';
  const src = source.trim();
  if (!code) throw new Error(`invalid standardCode ${JSON.stringify(standardCode)}`);
  if (!src) throw new Error('channel source is required');
  return `${CHANNEL_TOPIC_PREFIX}${code}/${src}`;
}

/** Cache lifecycle state. */
export type GeoPlacesCacheState = 'empty' | 'fresh' | 'stale' | 'revalidating';

/** Unsubscribe handle returned by a channel subscription. */
export type GeoPlacesUnsubscribe = () => void | Promise<void>;

/** The pubsub surface this lane needs (a node/client channel subscription). */
export interface GeoPlacesAnnounceSubscriber {
  subscribe(
    topic: string,
    handler: (payload: unknown) => void,
  ): GeoPlacesUnsubscribe | Promise<GeoPlacesUnsubscribe>;
}

/**
 * Dataset-epoch announce payload. SDS JSON keys keep IDL capitalization
 * exactly — `$GNP` fields are UPPER_SNAKE.
 */
export interface GeoPlacesEpochAnnounce {
  DATASET_EPOCH: number | string;
  SOURCE?: string;
  RECORD_COUNT?: number;
}

export interface GeoPlacesCacheOptions {
  transport: EpochStreamTransport;
  store: EpochStreamLocalStore;
  /** Optional announce channel; absent = explicit `revalidate()` only. */
  subscriber?: GeoPlacesAnnounceSubscriber | null;
  /** Source partition (remote + local). Default `geonames`. */
  source?: string;
  /** Standard code. Default `GNP`. */
  standardCode?: string;
  /** Row limit forwarded to the bulk query (absent = unlimited). */
  limit?: number;
  /** Epoch profile forwarded to the bulk query. Default `nearest`. */
  profile?: string;
  /** Revalidate automatically on an announce. Default true. */
  autoRevalidate?: boolean;
}

export interface GeoPlacesReadResult {
  /** Aligned size-prefixed FlatBuffer record stream (u32 LE framing). */
  stream: Uint8Array;
  /** Zero-copy per-record frame iterator over `stream`. */
  frames(): Generator<Uint8Array, void, undefined>;
  /** Dataset epoch these bytes belong to. */
  datasetEpoch: number | string | null;
  /** Cache state at the moment the read was answered. */
  cacheState: GeoPlacesCacheState;
  /** True when the bytes came out of the local engine store (no network). */
  fromLocalStore: boolean;
  /** Frame count of `stream`. */
  recordCount: number;
}

/** Cache-first $GNP gazetteer lane with pubsub-driven background revalidation. */
export class GeoPlacesCache {
  private readonly client: RemoteEpochStreamClient;
  private readonly store: EpochStreamLocalStore;
  private readonly subscriber: GeoPlacesAnnounceSubscriber | null;
  private readonly source: string;
  private readonly standardCode: string;
  private readonly limit: number | undefined;
  private readonly profile: string;
  private readonly autoRevalidate: boolean;

  private epoch: number | string | null = null;
  private cached = false;
  private pendingEpoch: number | string | null = null;
  private state: GeoPlacesCacheState = 'empty';
  private inflight: Promise<GeoPlacesReadResult> | null = null;
  private unsubscribe: GeoPlacesUnsubscribe | null = null;
  private error: Error | null = null;

  constructor(options: GeoPlacesCacheOptions) {
    this.store = options.store;
    this.client = new RemoteEpochStreamClient(options.transport, options.store);
    this.subscriber = options.subscriber ?? null;
    this.source = (options.source ?? GEONAMES_SOURCE).trim() || GEONAMES_SOURCE;
    this.standardCode = (options.standardCode ?? GEONAMES_STANDARD_CODE).trim().toUpperCase();
    this.limit = options.limit;
    this.profile = options.profile ?? 'nearest';
    this.autoRevalidate = options.autoRevalidate !== false;
  }

  /** Dataset epoch currently served from the cache (null when empty). */
  get datasetEpoch(): number | string | null {
    return this.epoch;
  }

  /** Current cache state. */
  get cacheState(): GeoPlacesCacheState {
    return this.state;
  }

  /** Last revalidation error (cleared on the next success). */
  get lastError(): Error | null {
    return this.error;
  }

  /** The announce topic this cache listens on. */
  get topic(): string {
    return geoPlacesEpochTopic(this.source, this.standardCode);
  }

  /**
   * Attach to the announce channel. Lazy: does nothing when no subscriber
   * port was injected, and is idempotent.
   */
  async start(): Promise<void> {
    if (!this.subscriber || this.unsubscribe) return;
    this.unsubscribe = await this.subscriber.subscribe(this.topic, (payload) => {
      this.onAnnounce(payload);
    });
  }

  /** Detach from the announce channel. Cached bytes stay cached. */
  async stop(): Promise<void> {
    const off = this.unsubscribe;
    this.unsubscribe = null;
    if (off) await off();
  }

  /**
   * Cache-first read. Answers from the local engine store the instant any
   * epoch is cached (never awaits the network); an empty cache fetches once.
   */
  async read(): Promise<GeoPlacesReadResult> {
    const cached = this.readCached();
    if (cached) return cached;
    return this.revalidate();
  }

  /**
   * Synchronous cache hit, or null when nothing is cached yet. Serving a
   * stale epoch also kicks a background revalidate (fire-and-forget).
   */
  readCached(): GeoPlacesReadResult | null {
    if (!this.cached) return null;
    if (this.state === 'stale' && this.autoRevalidate) {
      void this.revalidate().catch(() => {
        /* surfaced on lastError */
      });
    }
    return this.localResult(this.epoch, this.state);
  }

  /**
   * Fetch (conditionally) and atomically swap the cached epoch. Concurrent
   * calls share ONE in-flight request.
   */
  revalidate(epoch?: number | string): Promise<GeoPlacesReadResult> {
    if (this.inflight) return this.inflight;
    const target = epoch ?? this.pendingEpoch ?? this.epoch ?? undefined;
    const previousState = this.state;
    this.state = 'revalidating';
    const run = this.fetchEpoch(target)
      .then((result) => {
        this.error = null;
        return result;
      })
      .catch((cause: unknown) => {
        this.error = cause instanceof Error ? cause : new Error(String(cause));
        // Never poison the cache: keep the previously served epoch.
        this.state = !this.cached ? 'empty' : previousState === 'revalidating' ? 'stale' : previousState;
        throw this.error;
      })
      .finally(() => {
        this.inflight = null;
      });
    this.inflight = run;
    return run;
  }

  private async fetchEpoch(epoch: number | string | undefined): Promise<GeoPlacesReadResult> {
    const result = await this.client.fetchEpochStream({
      schema: this.standardCode,
      profile: this.profile,
      ...(epoch !== undefined ? { epoch } : {}),
      ...(this.limit !== undefined ? { limit: this.limit } : {}),
      source: this.source,
      ingestSource: this.source,
    });
    // Atomic swap: the served epoch advances only once the bytes are stored.
    this.epoch = epoch ?? this.epoch ?? null;
    if (this.pendingEpoch !== null && epoch !== undefined && sameEpoch(epoch, this.pendingEpoch)) {
      this.pendingEpoch = null;
    }
    this.cached = true;
    this.state = 'fresh';
    return {
      stream: result.stream,
      frames: result.frames,
      datasetEpoch: this.epoch,
      cacheState: this.state,
      fromLocalStore: result.fromLocalStore,
      recordCount: result.recordCount,
    };
  }

  private localResult(
    epoch: number | string | null,
    state: GeoPlacesCacheState,
  ): GeoPlacesReadResult {
    const stream = this.store.queryEpochRawStream(this.standardCode, {
      profile: this.profile,
      ...(epoch !== null ? { epoch: epochSeconds(epoch) } : {}),
      limit: this.limit !== undefined && this.limit > 0 ? this.limit : -1,
      source: this.source,
    });
    return {
      stream,
      frames: () => iterateFrames(stream),
      datasetEpoch: epoch,
      cacheState: state,
      fromLocalStore: true,
      recordCount: countFrames(stream),
    };
  }

  /** Announce handler — marks stale and (by default) revalidates in background. */
  private onAnnounce(payload: unknown): void {
    const announced = parseAnnounce(payload);
    if (announced === null) return;
    if (announced.SOURCE && announced.SOURCE !== this.source) return;
    if (this.cached && this.epoch !== null && sameEpoch(announced.DATASET_EPOCH, this.epoch)) return;
    this.pendingEpoch = announced.DATASET_EPOCH;
    this.state = this.cached ? 'stale' : 'empty';
    if (!this.autoRevalidate) return;
    void this.revalidate(announced.DATASET_EPOCH).catch(() => {
      /* surfaced on lastError */
    });
  }
}

/** Parse a dataset-epoch announce from JSON text, bytes, or an object. */
export function parseAnnounce(payload: unknown): GeoPlacesEpochAnnounce | null {
  let value: unknown = payload;
  if (value instanceof Uint8Array) {
    value = new TextDecoder().decode(value);
  }
  if (typeof value === 'string') {
    try {
      value = JSON.parse(value);
    } catch {
      return null;
    }
  }
  if (!value || typeof value !== 'object') return null;
  const record = value as Record<string, unknown>;
  const epoch = record.DATASET_EPOCH;
  if (typeof epoch !== 'number' && typeof epoch !== 'string') return null;
  const source = typeof record.SOURCE === 'string' ? record.SOURCE : undefined;
  const count = typeof record.RECORD_COUNT === 'number' ? record.RECORD_COUNT : undefined;
  return {
    DATASET_EPOCH: epoch,
    ...(source !== undefined ? { SOURCE: source } : {}),
    ...(count !== undefined ? { RECORD_COUNT: count } : {}),
  };
}

function sameEpoch(a: number | string, b: number | string): boolean {
  try {
    return epochSeconds(a) === epochSeconds(b);
  } catch {
    return String(a) === String(b);
  }
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

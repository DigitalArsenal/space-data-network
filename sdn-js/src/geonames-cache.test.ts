/**
 * GeoPlaces ($GNP) cache lane — cache-first + pubsub revalidation.
 *
 * In-memory fakes for the injected ports (mirrors the remote-epoch-stream
 * pattern, minus the real engine store: this lane's contract is the cache
 * state machine, not FlatSQL byte parity, which remote-epoch-stream.test.ts
 * already pins).
 */
import { describe, expect, it } from 'vitest';

import {
  GeoPlacesCache,
  GEONAMES_SOURCE,
  GEONAMES_STANDARD_CODE,
  geoPlacesEpochTopic,
  parseAnnounce,
} from './geonames-cache';
import type {
  GeoPlacesAnnounceSubscriber,
  GeoPlacesUnsubscribe,
} from './geonames-cache';
import type { EpochStreamLocalStore, EpochStreamTransport } from './remote-epoch-stream';

/** Build an aligned size-prefixed (u32 LE) frame stream from record payloads. */
function frameStream(records: Uint8Array[]): Uint8Array {
  const total = records.reduce((sum, r) => sum + 4 + r.byteLength, 0);
  const out = new Uint8Array(total);
  const view = new DataView(out.buffer);
  let offset = 0;
  for (const record of records) {
    view.setUint32(offset, record.byteLength, true);
    offset += 4;
    out.set(record, offset);
    offset += record.byteLength;
  }
  return out;
}

function streamFor(epoch: number, count: number): Uint8Array {
  return frameStream(
    Array.from({ length: count }, (_, i) => Uint8Array.of(epoch % 251, i, 0, 0)),
  );
}

function* iterate(stream: Uint8Array): Generator<Uint8Array, void, undefined> {
  const view = new DataView(stream.buffer, stream.byteOffset, stream.byteLength);
  let offset = 0;
  while (offset < stream.byteLength) {
    const length = view.getUint32(offset, true);
    offset += 4;
    yield stream.subarray(offset, offset + length);
    offset += length;
  }
}

interface FakeCall {
  epoch: number | string | undefined;
  ifNoneMatch: string | null;
}

/** In-memory local FlatSQL engine store (ingest in, epoch query out). */
class FakeStore implements EpochStreamLocalStore {
  readonly ingested: Array<{ standardId: string; source: string | null; bytes: number }> = [];
  queries = 0;
  private stream = new Uint8Array(0);

  async ingestFlatBufferStream(
    standardId: string,
    streamBytes: Uint8Array,
    options?: { source?: string | null } | null,
  ): Promise<number> {
    this.ingested.push({
      standardId,
      source: options?.source ?? null,
      bytes: streamBytes.byteLength,
    });
    this.stream = streamBytes.slice();
    return [...iterate(this.stream)].length;
  }

  queryEpochRawStream(_standardId: string): Uint8Array {
    this.queries += 1;
    return this.stream;
  }
}

/** In-memory conditional bulk transport. */
class FakeTransport implements EpochStreamTransport {
  readonly calls: FakeCall[] = [];
  etag = 'W/"fnv1a64-epoch-one000"';
  body = streamFor(1, 3);
  failure: Error | null = null;

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  async queryData(opts: any): Promise<any> {
    this.calls.push({ epoch: opts.epoch, ifNoneMatch: opts.ifNoneMatch ?? null });
    if (this.failure) throw this.failure;
    if (opts.ifNoneMatch === this.etag) {
      return {
        format: 'flatbuffers',
        status: 304,
        notModified: true,
        etag: this.etag,
        recordCount: 0,
        stream: new Uint8Array(0),
        frames: () => iterate(new Uint8Array(0)),
      };
    }
    const body = this.body;
    return {
      format: 'flatbuffers',
      status: 200,
      notModified: false,
      etag: this.etag,
      recordCount: [...iterate(body)].length,
      stream: body,
      frames: () => iterate(body),
    };
  }
}

class FakeSubscriber implements GeoPlacesAnnounceSubscriber {
  readonly topics: string[] = [];
  handler: ((payload: unknown) => void) | null = null;
  unsubscribed = 0;

  subscribe(topic: string, handler: (payload: unknown) => void): GeoPlacesUnsubscribe {
    this.topics.push(topic);
    this.handler = handler;
    return () => {
      this.unsubscribed += 1;
      this.handler = null;
    };
  }

  announce(payload: unknown): void {
    this.handler?.(payload);
  }
}

const EPOCH_ONE = 1778500800;
const EPOCH_TWO = 1778587200;

function makeCache(overrides: Partial<{ subscriber: FakeSubscriber }> = {}) {
  const transport = new FakeTransport();
  const store = new FakeStore();
  const subscriber = overrides.subscriber ?? new FakeSubscriber();
  const cache = new GeoPlacesCache({ transport, store, subscriber });
  return { transport, store, subscriber, cache };
}

/** Let queued microtasks (background revalidate) settle. */
const settle = () => new Promise<void>((resolve) => setTimeout(resolve, 0));

describe('geoPlacesEpochTopic', () => {
  it('uses the per-(standard, source) channel topic grammar', () => {
    expect(geoPlacesEpochTopic()).toBe('/spacedatanetwork/channels/GNP/geonames');
    expect(GEONAMES_STANDARD_CODE).toBe('GNP');
    expect(GEONAMES_SOURCE).toBe('geonames');
    // A `.fbs`-suffixed standard name resolves to the same bare-code topic.
    expect(geoPlacesEpochTopic('geonames', 'GNP.fbs')).toBe(
      '/spacedatanetwork/channels/GNP/geonames',
    );
  });
});

describe('parseAnnounce', () => {
  it('reads DATASET_EPOCH with exact IDL capitalization from object/JSON/bytes', () => {
    const expected = { DATASET_EPOCH: EPOCH_TWO, SOURCE: 'geonames' };
    expect(parseAnnounce(expected)).toEqual(expected);
    expect(parseAnnounce(JSON.stringify(expected))).toEqual(expected);
    expect(parseAnnounce(new TextEncoder().encode(JSON.stringify(expected)))).toEqual(expected);
  });

  it('refuses lowercase / malformed payloads instead of guessing', () => {
    expect(parseAnnounce({ dataset_epoch: EPOCH_TWO })).toBeNull();
    expect(parseAnnounce('not json')).toBeNull();
    expect(parseAnnounce(null)).toBeNull();
  });
});

describe('GeoPlacesCache', () => {
  it('fetches once on an empty cache and ingests into the local store', async () => {
    const { cache, transport, store } = makeCache();
    expect(cache.cacheState).toBe('empty');
    expect(cache.readCached()).toBeNull();

    const first = await cache.read();
    expect(transport.calls).toHaveLength(1);
    expect(transport.calls[0].ifNoneMatch).toBeNull();
    expect(first.fromLocalStore).toBe(false);
    expect(first.recordCount).toBe(3);
    expect(store.ingested).toEqual([{ standardId: 'GNP', source: 'geonames', bytes: 24 }]);
    expect(cache.cacheState).toBe('fresh');
  });

  it('serves a cache hit from the local store without touching the network', async () => {
    const { cache, transport, store } = makeCache();
    await cache.revalidate(EPOCH_ONE);
    expect(transport.calls).toHaveLength(1);
    expect(cache.datasetEpoch).toBe(EPOCH_ONE);

    const hit = cache.readCached();
    expect(hit).not.toBeNull();
    expect(hit!.fromLocalStore).toBe(true);
    expect(hit!.cacheState).toBe('fresh');
    expect(hit!.datasetEpoch).toBe(EPOCH_ONE);
    expect([...hit!.frames()]).toHaveLength(3);
    expect(store.queries).toBe(1);

    const again = await cache.read();
    expect(again.fromLocalStore).toBe(true);
    expect(transport.calls).toHaveLength(1); // still no second request
  });

  it('serves the local store on a 304 revalidation', async () => {
    const { cache, transport, store } = makeCache();
    await cache.revalidate(EPOCH_ONE);
    const ingestsAfterFirst = store.ingested.length;

    const second = await cache.revalidate(EPOCH_ONE);
    expect(transport.calls).toHaveLength(2);
    expect(transport.calls[1].ifNoneMatch).toBe(transport.etag);
    expect(second.fromLocalStore).toBe(true);
    expect(second.recordCount).toBe(3);
    expect(store.ingested).toHaveLength(ingestsAfterFirst); // 304 ingests nothing
    expect(cache.cacheState).toBe('fresh');
  });

  it('swaps the epoch in the background when an announce arrives', async () => {
    const { cache, transport, subscriber } = makeCache();
    await cache.start();
    expect(subscriber.topics).toEqual(['/spacedatanetwork/channels/GNP/geonames']);

    await cache.revalidate(EPOCH_ONE);
    expect(cache.datasetEpoch).toBe(EPOCH_ONE);

    transport.etag = 'W/"fnv1a64-epoch-two000"';
    transport.body = streamFor(2, 5);
    subscriber.announce({ DATASET_EPOCH: EPOCH_TWO, SOURCE: 'geonames' });

    // Cache-first: the OLD epoch still serves while the fetch is in flight.
    const duringRevalidate = cache.readCached();
    expect(duringRevalidate!.fromLocalStore).toBe(true);
    expect(duringRevalidate!.recordCount).toBe(3);

    await settle();
    expect(cache.datasetEpoch).toBe(EPOCH_TWO);
    expect(cache.cacheState).toBe('fresh');
    expect(transport.calls.at(-1)!.epoch).toBe(EPOCH_TWO);
    expect(cache.readCached()!.recordCount).toBe(5);

    await cache.stop();
    expect(subscriber.unsubscribed).toBe(1);
  });

  it('ignores an announce for the epoch already cached or another source', async () => {
    const { cache, transport, subscriber } = makeCache();
    await cache.start();
    await cache.revalidate(EPOCH_ONE);
    const calls = transport.calls.length;

    subscriber.announce({ DATASET_EPOCH: EPOCH_ONE, SOURCE: 'geonames' });
    subscriber.announce({ DATASET_EPOCH: EPOCH_TWO, SOURCE: 'other-provider' });
    subscriber.announce({ nope: true });
    await settle();

    expect(transport.calls).toHaveLength(calls);
    expect(cache.cacheState).toBe('fresh');
  });

  it('does not poison the cache when a revalidation fails', async () => {
    const { cache, transport, subscriber } = makeCache();
    await cache.start();
    await cache.revalidate(EPOCH_ONE);

    transport.failure = new Error('gateway refused');
    subscriber.announce({ DATASET_EPOCH: EPOCH_TWO });
    await settle();

    expect(cache.lastError?.message).toBe('gateway refused');
    expect(cache.datasetEpoch).toBe(EPOCH_ONE); // old epoch kept
    expect(cache.cacheState).toBe('stale');
    const stillServing = cache.readCached();
    expect(stillServing!.recordCount).toBe(3);
    await settle(); // let the stale-read's background retry finish failing

    // Recovery: the retry lands the new epoch and clears the error.
    transport.failure = null;
    transport.etag = 'W/"fnv1a64-epoch-two000"';
    transport.body = streamFor(2, 4);
    await cache.revalidate();
    expect(cache.lastError).toBeNull();
    expect(cache.datasetEpoch).toBe(EPOCH_TWO);
    expect(cache.readCached()!.recordCount).toBe(4);
  });

  it('rejects an empty-cache read failure without marking anything cached', async () => {
    const { cache, transport } = makeCache();
    transport.failure = new Error('offline');
    await expect(cache.read()).rejects.toThrow('offline');
    expect(cache.cacheState).toBe('empty');
    expect(cache.readCached()).toBeNull();
    expect(cache.datasetEpoch).toBeNull();
  });

  it('coalesces concurrent revalidations into one request', async () => {
    const { cache, transport } = makeCache();
    const [a, b] = await Promise.all([cache.revalidate(EPOCH_ONE), cache.revalidate(EPOCH_ONE)]);
    expect(transport.calls).toHaveLength(1);
    expect(a.recordCount).toBe(b.recordCount);
  });
});

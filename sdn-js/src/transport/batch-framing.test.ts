import { describe, it, expect } from 'vitest';
import {
  encodeSizePrefixedFrames,
  iterateSizePrefixedFrames,
  iterateSizePrefixedFrameStream,
  HttpTransport,
} from './http.js';

/** Decode one prefix exactly as the server does (`binary.LittleEndian.Uint32`). */
const goLE = (b: Uint8Array, at: number) =>
  (b[at] | (b[at + 1] << 8) | (b[at + 2] << 16) | (b[at + 3] << 24)) >>> 0;

describe('size-prefixed frame stream', () => {
  const records = [new Uint8Array(200).fill(1), new Uint8Array(4).fill(2), new Uint8Array(65_536).fill(3)];

  it('encoder and iterator are inverses', () => {
    const back = [...iterateSizePrefixedFrames(encodeSizePrefixedFrames(records))];
    expect(back.map((f) => Array.from(f))).toEqual(records.map((r) => Array.from(r)));
  });

  it('prefix decodes with the server algorithm to the record length', () => {
    const stream = encodeSizePrefixedFrames(records);
    let off = 0;
    for (const rec of records) {
      expect(goLE(stream, off)).toBe(rec.byteLength);
      off += 4 + rec.byteLength;
    }
    expect(off).toBe(stream.byteLength);
  });

  it('never announces the byte-swapped length that broke batch publish', () => {
    // 200 written big-endian reads as 0xC8000000 little-endian — the bug.
    expect(goLE(encodeSizePrefixedFrames([new Uint8Array(200)]), 0)).toBe(200);
  });

  it('empty input is an empty stream', () => {
    expect(encodeSizePrefixedFrames([]).byteLength).toBe(0);
    expect([...iterateSizePrefixedFrames(new Uint8Array(0))]).toEqual([]);
  });
});

/** Split a byte stream into chunks at the given cut points (exclusive prefix sums). */
function* chop(stream: Uint8Array, sizes: number[]): Generator<Uint8Array> {
  let off = 0;
  for (const size of sizes) {
    yield stream.subarray(off, off + size);
    off += size;
  }
  if (off < stream.byteLength) yield stream.subarray(off);
}

async function collect(frames: AsyncIterable<Uint8Array>): Promise<number[][]> {
  const out: number[][] = [];
  for await (const f of frames) out.push(Array.from(f));
  return out;
}

describe('incremental frame stream decoder', () => {
  const records = [new Uint8Array(200).fill(1), new Uint8Array(4).fill(2), new Uint8Array(65_536).fill(3)];
  const stream = encodeSizePrefixedFrames(records);
  const want = records.map((r) => Array.from(r));

  it('decodes across hostile chunk boundaries', async () => {
    // Mid-prefix, prefix/payload split, 1-byte drip through a frame, empty chunks.
    const cuts: number[][] = [
      [stream.byteLength], // one chunk
      [2], // split inside the first prefix
      [4], // prefix/payload boundary
      [1, 1, 1, 1, 1, 1, 1], // byte drip through prefix + payload start
      [0, 3, 0, 205, 0], // empty chunks interleaved
      [204, 6], // frame boundary then split second prefix
    ];
    for (const sizes of cuts) {
      expect(await collect(iterateSizePrefixedFrameStream(chop(stream, sizes)))).toEqual(want);
    }
  });

  it('decodes a WHATWG ReadableStream via getReader', async () => {
    const rs = new ReadableStream<Uint8Array>({
      start(controller) {
        for (const chunk of chop(stream, [3, 100, 150])) controller.enqueue(chunk);
        controller.close();
      },
    });
    expect(await collect(iterateSizePrefixedFrameStream(rs))).toEqual(want);
  });

  it('throws on truncation, mid-prefix and mid-frame', async () => {
    await expect(collect(iterateSizePrefixedFrameStream(chop(stream.subarray(0, 2), [2])))).rejects.toThrow(
      /truncated frame header/,
    );
    await expect(collect(iterateSizePrefixedFrameStream(chop(stream.subarray(0, 100), [100])))).rejects.toThrow(
      /truncated frame/,
    );
  });

  it('clean end on a frame boundary, empty stream yields nothing', async () => {
    expect(await collect(iterateSizePrefixedFrameStream(chop(new Uint8Array(0), [])))).toEqual([]);
  });

  it('cancels the underlying ReadableStream when the consumer stops early', async () => {
    let cancelled = false;
    const rs = new ReadableStream<Uint8Array>({
      pull(controller) {
        controller.enqueue(encodeSizePrefixedFrames([new Uint8Array(8)]));
      },
      cancel() {
        cancelled = true;
      },
    });
    const frames = iterateSizePrefixedFrameStream(rs);
    const first = await frames.next();
    expect(first.done).toBe(false);
    await frames.return(undefined);
    expect(cancelled).toBe(true);
  });
});

describe('publishBatchStream', () => {
  it('chunks an async source into bounded batch posts and aggregates results', async () => {
    const bodies: Uint8Array[] = [];
    const realFetch = globalThis.fetch;
    globalThis.fetch = (async (_url: unknown, init?: RequestInit) => {
      const body = new Uint8Array(init?.body as ArrayBuffer);
      bodies.push(body);
      const frames = [...iterateSizePrefixedFrames(body)];
      return new Response(
        JSON.stringify({
          schema: 'OMM.fbs',
          stored_at: '2026-08-27T00:00:00Z',
          count: frames.length,
          results: frames.map((f) => ({ cid: `cid-${f.byteLength}`, bytes: f.byteLength })),
        }),
        { status: 201, headers: { 'Content-Type': 'application/json' } },
      );
    }) as typeof fetch;
    try {
      const transport = new HttpTransport('http://node.test');
      async function* source(): AsyncGenerator<Uint8Array> {
        for (let i = 0; i < 7; i++) yield new Uint8Array(100).fill(i);
      }
      // Budget fits two records (2 * 104 = 208 <= 240 < 3 * 104).
      const res = await transport.publishBatchStream('OMM.fbs', source(), { maxBytesPerRequest: 240 });
      expect(res.requests).toBe(4); // 2 + 2 + 2 + 1
      expect(bodies.map((b) => [...iterateSizePrefixedFrames(b)].length)).toEqual([2, 2, 2, 1]);
      expect(res.count).toBe(7);
      expect(res.results).toHaveLength(7);
      expect(res.results.every((r) => r.cid === 'cid-100' && r.bytes === 100)).toBe(true);
    } finally {
      globalThis.fetch = realFetch;
    }
  });

  it('respects the record-count budget with a sync iterable', async () => {
    const realFetch = globalThis.fetch;
    let posts = 0;
    globalThis.fetch = (async (_url: unknown, init?: RequestInit) => {
      posts += 1;
      const frames = [...iterateSizePrefixedFrames(new Uint8Array(init?.body as ArrayBuffer))];
      return new Response(
        JSON.stringify({ schema: 'OMM.fbs', stored_at: 't', count: frames.length, results: frames.map(() => ({ cid: 'c', bytes: 1 })) }),
        { status: 201 },
      );
    }) as typeof fetch;
    try {
      const transport = new HttpTransport('http://node.test');
      const records = Array.from({ length: 5 }, () => new Uint8Array(1));
      const res = await transport.publishBatchStream('OMM.fbs', records, { maxRecordsPerRequest: 2 });
      expect(posts).toBe(3); // 2 + 2 + 1
      expect(res.count).toBe(5);
    } finally {
      globalThis.fetch = realFetch;
    }
  });
});

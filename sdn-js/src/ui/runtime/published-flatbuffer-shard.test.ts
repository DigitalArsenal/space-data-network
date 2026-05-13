import { describe, expect, it } from 'vitest';
import {
  fetchCidBytesFromGateway,
  flatBufferStreamFromPublishedFlatSqlSegment,
  importCarBytesToKubo,
  importPublishedFlatSqlShardCar,
  timedFlatBufferStreamFromPublishedFlatSqlSegment,
  rawRecordsFromPublishedFlatSqlSegment,
} from './published-flatbuffer-shard';

const encoder = new TextEncoder();

describe('published FlatSQL shard reader', () => {
  it('returns verified native FlatSQL shard streams without requiring the JSON materialized index', async () => {
    const first = new Uint8Array([1, 2, 3]);
    const second = new Uint8Array([4, 5]);
    const shard = concatFrames([first, second]);

    const stream = await flatBufferStreamFromPublishedFlatSqlSegment({
      schema: 'OMM.fbs',
      providerPeerId: '16Uiu2HCelesTrak',
      cid: 'bafkshard',
      shardSha256: await sha256Hex(shard),
      fetchCidBytes: async (cid) => {
        if (cid === 'bafkshard') return shard;
        throw new Error(`unexpected CID ${cid}`);
      },
    });

    expect(stream).toEqual(shard);
  });

  it('reports published shard network and verification timing separately', async () => {
    const shard = concatFrames([new Uint8Array([1, 2, 3])]);

    const result = await timedFlatBufferStreamFromPublishedFlatSqlSegment({
      schema: 'OMM.fbs',
      providerPeerId: '16Uiu2HCelesTrak',
      cid: 'bafkshard',
      shardSha256: await sha256Hex(shard),
      fetchCidBytes: async (cid) => {
        if (cid !== 'bafkshard') throw new Error(`unexpected CID ${cid}`);
        await new Promise((resolve) => setTimeout(resolve, 1));
        return shard;
      },
    });

    expect(result.streamBytes).toEqual(shard);
    expect(result.networkTransferMs).toBeGreaterThanOrEqual(0);
    expect(result.verificationMs).toBeGreaterThanOrEqual(0);
  });

  it('bounds local gateway CID fetches', async () => {
    await expect(Promise.race([
      fetchCidBytesFromGateway('http://127.0.0.1:8081', 'bafkshard', {
        timeoutMs: 1,
        fetch: async () => new Promise<Response>(() => undefined),
      }),
      new Promise<never>((_, reject) => setTimeout(() => reject(new Error('gateway fetch did not time out')), 25)),
    ])).rejects.toThrow('fetch CID bafkshard timed out after 1 ms');
  });

  it('bounds local gateway CID response bodies', async () => {
    const body = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(new Uint8Array([1, 2, 3]));
      },
    });

    await expect(Promise.race([
      fetchCidBytesFromGateway('http://127.0.0.1:8081', 'bafkshard', {
        timeoutMs: 1,
        fetch: async () => new Response(body, { status: 200 }),
      }),
      new Promise<never>((_, reject) => setTimeout(() => reject(new Error('gateway body did not time out')), 25)),
    ])).rejects.toThrow('fetch CID bafkshard timed out after 1 ms');
  });

  it('normalizes desktop gateway multiaddrs before fetching published shards', async () => {
    const calls: string[] = [];
    const bytes = await fetchCidBytesFromGateway('/ip4/127.0.0.1/tcp/8081', 'bafkshard', {
      fetch: async (url) => {
        calls.push(String(url));
        return new Response(new Uint8Array([1, 2, 3]), { status: 200 });
      },
    });

    expect(bytes).toEqual(new Uint8Array([1, 2, 3]));
    expect(calls).toEqual(['http://127.0.0.1:8081/ipfs/bafkshard']);
  });

  it('normalizes desktop Kubo API multiaddrs before importing CAR bundles', async () => {
    const originalFetch = globalThis.fetch;
    const calls: string[] = [];
    globalThis.fetch = (async (url) => {
      calls.push(String(url));
      return new Response(JSON.stringify({ Root: { Cid: '/' } }), { status: 200 });
    }) as typeof fetch;
    try {
      await importCarBytesToKubo('/ip4/127.0.0.1/tcp/5001', new Uint8Array([1, 2, 3]));
    } finally {
      globalThis.fetch = originalFetch;
    }

    expect(calls).toEqual(['http://127.0.0.1:5001/api/v0/dag/import?pin-roots=true&stats=false']);
  });

  it('verifies and imports a published shard-group CAR before shard reads', async () => {
    const car = encoder.encode('car bytes for a complete shard group');
    const imported: Uint8Array[] = [];

    const result = await importPublishedFlatSqlShardCar({
      cid: 'bafkcar',
      sha256: await sha256Hex(car),
      fetchCidBytes: async (cid) => {
        if (cid !== 'bafkcar') throw new Error(`unexpected CID ${cid}`);
        return car;
      },
      importCarBytes: async (bytes) => {
        imported.push(bytes);
      },
    });

    expect(imported).toEqual([car]);
    expect(result.cid).toBe('bafkcar');
    expect(result.byteLength).toBe(car.byteLength);
    expect(result.networkTransferMs).toBeGreaterThanOrEqual(0);
    expect(result.verificationMs).toBeGreaterThanOrEqual(0);
    expect(result.importMs).toBeGreaterThanOrEqual(0);
  });

  it('hydrates raw records from a DPM shard and its materialized index', async () => {
    const first = new Uint8Array([1, 2, 3]);
    const second = new Uint8Array([4, 5]);
    const shard = concatFrames([first, second]);
    const index = encoder.encode(JSON.stringify({
      version: 1,
      schemaName: 'OMM.fbs',
      shardCid: 'bafkshard',
      shardSha256: await sha256Hex(shard),
      recordCount: 2,
      records: [
        {
          cid: 'cid-1',
          offset: 0,
          length: 3,
          sourceTags: {
            providerId: 'space-data-network-02',
            sourceName: 'celestrak-gp',
            batchId: 'batch-001',
            producerPeerId: 'producer-peer',
            producerPublicKey: 'producer-key',
          },
        },
        {
          cid: 'cid-2',
          offset: 7,
          length: 2,
          sourceTags: {
            providerId: 'space-data-network-02',
            sourceName: 'celestrak-gp',
          },
        },
      ],
    }));

    const records = await rawRecordsFromPublishedFlatSqlSegment({
      schema: 'OMM.fbs',
      providerPeerId: '16Uiu2HCelesTrak',
      cid: 'bafkshard',
      indexCid: 'bafkindex',
      shardSha256: await sha256Hex(shard),
      fetchCidBytes: async (cid) => {
        if (cid === 'bafkshard') return shard;
        if (cid === 'bafkindex') return index;
        throw new Error(`unexpected CID ${cid}`);
      },
    });

    expect(records).toEqual([
      expect.objectContaining({
        schemaName: 'OMM.fbs',
        cid: 'cid-1',
        peerId: '16Uiu2HCelesTrak',
        providerId: 'space-data-network-02',
        sourceName: 'celestrak-gp',
        batchId: 'batch-001',
        producerPeerId: 'producer-peer',
        producerPublicKey: 'producer-key',
        sizeBytes: 3,
        dataBytes: first,
      }),
      expect.objectContaining({
        cid: 'cid-2',
        sizeBytes: 2,
        dataBytes: second,
      }),
    ]);
  });

  it('rejects a shard whose bytes do not match the advertised SHA-256', async () => {
    const shard = concatFrames([new Uint8Array([1, 2, 3])]);
    const index = encoder.encode(JSON.stringify({
      version: 1,
      schemaName: 'OMM.fbs',
      recordCount: 1,
      records: [{ cid: 'cid-1', offset: 0, length: 3 }],
    }));

    await expect(rawRecordsFromPublishedFlatSqlSegment({
      schema: 'OMM.fbs',
      providerPeerId: '16Uiu2HCelesTrak',
      cid: 'bafkshard',
      indexCid: 'bafkindex',
      shardSha256: 'not-the-sha',
      fetchCidBytes: async (cid) => cid === 'bafkshard' ? shard : index,
    })).rejects.toThrow('shard SHA-256 mismatch');
  });
});

function concatFrames(frames: Uint8Array[]): Uint8Array {
  const totalLength = frames.reduce((sum, frame) => sum + 4 + frame.byteLength, 0);
  const out = new Uint8Array(totalLength);
  let offset = 0;
  for (const frame of frames) {
    new DataView(out.buffer).setUint32(offset, frame.byteLength, true);
    offset += 4;
    out.set(frame, offset);
    offset += frame.byteLength;
  }
  return out;
}

async function sha256Hex(bytes: Uint8Array): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', bytes);
  return Array.from(new Uint8Array(digest)).map((byte) => byte.toString(16).padStart(2, '0')).join('');
}

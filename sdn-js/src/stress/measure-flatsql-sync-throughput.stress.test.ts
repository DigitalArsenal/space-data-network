import { createHash } from 'node:crypto';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

import { describe, expect, it } from 'vitest';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const packageRoot = path.resolve(__dirname, '../..');
const scriptUrl = pathToFileURL(path.join(packageRoot, 'scripts/measure-flatsql-sync-throughput.mjs')).href;

describe('FlatSQL sync throughput harness', () => {
  it('splits a published shard into direct libp2p byte ranges across multiple sources', async () => {
    const { downloadPublishedSegments } = await import(scriptUrl);
    const shardBytes = buildFlatSqlStream([12, 17, 9, 23]);
    const shardSha256 = createHash('sha256').update(shardBytes).digest('hex');
    const calls: Array<{ source: string; byteOffset: number; byteLength: number }> = [];
    const clients = ['primary', 'mirror'].map((source) => ({
      peer: source,
      addrs: [`/ip4/127.0.0.1/tcp/0/p2p/${source}`],
      client: {
        async readFlatSqlPublishedShard(request: { byteOffset?: number; byteLength?: number }) {
          const byteOffset = request.byteOffset ?? 0;
          const byteLength = request.byteLength ?? shardBytes.byteLength - byteOffset;
          calls.push({ source, byteOffset, byteLength });
          return {
            header: {
              byteOffset,
              byteLength,
              byteCount: byteLength,
            },
            streamBytes: shardBytes.slice(byteOffset, byteOffset + byteLength),
          };
        },
      },
    }));

    const result = await downloadPublishedSegments({
      clients,
      segments: [{
        cid: 'bafy-test-shard',
        byteCount: shardBytes.byteLength,
        shardSha256,
      }],
      schema: 'OMM.fbs',
      providerId: 'space-data-network-02',
      sourceName: 'celestrak-gp',
      concurrency: 1,
      rangeBytes: 16,
      rangeConcurrency: 3,
      now: fixedClock([0, 40, 41, 42, 43]),
    });

    expect(result.downloadedBytes).toBe(shardBytes.byteLength);
    expect(result.networkTransferMs).toBe(42);
    expect(result.sourceStats).toEqual([
      {
        peer: 'primary',
        addrs: ['/ip4/127.0.0.1/tcp/0/p2p/primary'],
        bytes: 45,
        requests: 3,
        errors: 0,
      },
      {
        peer: 'mirror',
        addrs: ['/ip4/127.0.0.1/tcp/0/p2p/mirror'],
        bytes: 32,
        requests: 2,
        errors: 0,
      },
    ]);
    expect(new Set(calls.map((call) => call.source))).toEqual(new Set(['primary', 'mirror']));
    expect(calls.map((call) => call.byteOffset)).toEqual([0, 16, 32, 48, 64]);
    expect(calls.every((call) => call.byteLength > 0 && call.byteLength <= 16)).toBe(true);
  });
});

function buildFlatSqlStream(payloadLengths: number[]): Uint8Array {
  const total = payloadLengths.reduce((sum, length) => sum + 4 + length, 0);
  const out = new Uint8Array(total);
  const view = new DataView(out.buffer);
  let offset = 0;
  for (const [recordIndex, length] of payloadLengths.entries()) {
    view.setUint32(offset, length, true);
    offset += 4;
    for (let index = 0; index < length; index += 1) {
      out[offset + index] = (recordIndex * 29 + index * 11) & 0xff;
    }
    offset += length;
  }
  return out;
}

function fixedClock(values: number[]): () => number {
  let index = 0;
  return () => values[Math.min(index++, values.length - 1)];
}

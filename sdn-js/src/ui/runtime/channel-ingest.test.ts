import { describe, expect, it } from 'vitest';
import { createChannelFlatSqlIngestor } from './channel-ingest';

describe('channel FlatSQL durable ingest adapter', () => {
  it('imports native channel stream frames through the SDK FlatSQL ingestor', () => {
    const ingest = createChannelFlatSqlIngestor();
    const first = flatBufferPayload('OMM ', 24);
    const second = flatBufferPayload('CDM ', 32);
    const stream = concatBytes(sizePrefixedFrame(first), sizePrefixedFrame(second));

    expect(ingest.pushChunk(stream.subarray(0, 9))).toBe(0);
    expect(ingest.pushChunk(stream.subarray(9))).toBe(2);
    expect(ingest.finish()).toBe(0);

    expect(ingest.rows.listRows().map((row) => row.handle)).toEqual([
      { schemaFileId: 'OMM', rowId: 1 },
      { schemaFileId: 'CDM', rowId: 1 },
    ]);
    expect(ingest.rows.listRows()[0].payload).toEqual(first);
    expect(ingest.stats()).toEqual(expect.objectContaining({
      framesDecoded: 2,
      framesAppended: 2,
      framesRouted: 0,
    }));
  });
});

function flatBufferPayload(fileIdentifier: string, byteLength: number): Uint8Array {
  const encoded = new TextEncoder().encode(fileIdentifier);
  if (encoded.byteLength !== 4) {
    throw new Error('fileIdentifier must be four bytes');
  }
  const payload = new Uint8Array(Math.max(8, byteLength));
  payload[0] = 4;
  payload.set(encoded, 4);
  for (let i = 8; i < payload.byteLength; i += 1) {
    payload[i] = i % 251;
  }
  return payload;
}

function sizePrefixedFrame(payload: Uint8Array): Uint8Array {
  const frame = new Uint8Array(4 + payload.byteLength);
  new DataView(frame.buffer).setUint32(0, payload.byteLength, true);
  frame.set(payload, 4);
  return frame;
}

function concatBytes(...chunks: Uint8Array[]): Uint8Array {
  const out = new Uint8Array(chunks.reduce((total, chunk) => total + chunk.byteLength, 0));
  let offset = 0;
  for (const chunk of chunks) {
    out.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return out;
}

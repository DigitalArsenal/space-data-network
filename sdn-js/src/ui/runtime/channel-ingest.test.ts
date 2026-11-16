import * as flatbuffers from 'flatbuffers';
import { compileTestSchema } from '../../test-schema-helper';
import { describe, expect, it } from 'vitest';
import { createChannelFlatSqlIngestor } from './channel-ingest';

describe('channel FlatSQL durable ingest adapter', () => {
  it('imports native channel stream frames through the SDK FlatSQL ingestor', async () => {
    const ingest = createChannelFlatSqlIngestor({ store: { engine: { backend: 'memory' }, schemas: await channelSchemas('OMM ', 'CDM ') } });
    const first = flatBufferPayload('OMM ', 24);
    const second = flatBufferPayload('CDM ', 32);
    const stream = concatBytes(sizePrefixedFrame(first), sizePrefixedFrame(second));

    expect(await ingest.pushChunk(stream.subarray(0, 9))).toBe(0);
    expect(await ingest.pushChunk(stream.subarray(9))).toBe(2);
    expect(await ingest.finish()).toBe(0);

    // The SDK lists across schemas in schema order. Resolve by handle to
    // prove both frames survived ingest without assuming insertion order.
    expect(await ingest.rows.listRows()).toHaveLength(2);
    expect((await ingest.rows.resolveRow({ schemaFileId: 'OMM ', rowId: 1 }))?.payload).toEqual(first);
    expect((await ingest.rows.resolveRow({ schemaFileId: 'CDM ', rowId: 1 }))?.payload).toEqual(second);
    expect(ingest.stats()).toEqual(expect.objectContaining({
      framesDecoded: 2,
      framesAppended: 2,
      framesRouted: 0,
    }));
    await ingest.rows.close();
  });
});

function flatBufferPayload(fileIdentifier: string, value: number): Uint8Array {
  const builder = new flatbuffers.Builder(64);
  builder.startObject(1);
  builder.addFieldInt32(0, value, 0);
  builder.finish(builder.endObject(), fileIdentifier);
  return builder.asUint8Array().slice();
}

async function channelSchemas(...ids: string[]) {
  return Promise.all(ids.map(async fileIdentifier => ({
    name: fileIdentifier.trim(), fileIdentifier,
    bfbs: await compileTestSchema(`table Record { value:uint; } root_type Record; file_identifier "${fileIdentifier}";`),
  })));
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

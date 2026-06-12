import { describe, expect, it } from 'vitest';
import { createChannelModuleStreamPump } from './channel-module-pump';

describe('channel module stream pump adapter', () => {
  it('feeds native channel stream frames through the SDK module stream pump', async () => {
    const requests: Array<{ methodId: string; inputs: Array<Record<string, unknown>> }> = [];
    const pump = createChannelModuleStreamPump({
      methodId: 'upsert_records',
      portId: 'records',
      maxFramesPerInvoke: 8,
      invoke: async (request) => {
        requests.push(request as { methodId: string; inputs: Array<Record<string, unknown>> });
        return { statusCode: 0, outputs: [] };
      },
    });
    const stream = concatBytes(
      sizePrefixedFrame(flatBufferPayload('OMM ', 24)),
      sizePrefixedFrame(flatBufferPayload('CDM ', 32)),
    );

    expect(await pump.pushChunk(stream.subarray(0, 9))).toBe(0);
    expect(await pump.pushChunk(stream.subarray(9))).toBe(2);
    const response = await pump.finish();

    expect(response).toEqual({ statusCode: 0, outputs: [] });
    expect(requests).toHaveLength(1);
    expect(requests[0].methodId).toBe('upsert_records');
    expect(requests[0].inputs).toHaveLength(2);
    expect(requests[0].inputs[0]).toMatchObject({
      portId: 'records',
      sequence: 1,
      endOfStream: false,
      typeRef: {
        fileIdentifier: 'OMM',
        acceptsAnyFlatbuffer: true,
      },
    });
    expect(requests[0].inputs[1]).toMatchObject({
      portId: 'records',
      sequence: 2,
      endOfStream: true,
      typeRef: {
        fileIdentifier: 'CDM',
        acceptsAnyFlatbuffer: true,
      },
    });
    expect(pump.stats()).toEqual(expect.objectContaining({
      framesDecoded: 2,
      framesInvoked: 2,
      invokes: 1,
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

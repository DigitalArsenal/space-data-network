import { describe, expect, it } from 'vitest';
import { createAvailableResult, createDegradedResult, type ChannelBackend } from './sdn-backend';
import { ingestChannelStreamToFlatSql } from './channel-sync';
import { pumpChannelStreamToModule } from './channel-module-sync';

describe('channel stream sync adapters', () => {
  it('opens a channel stream and imports it through the SDK FlatSQL ingestor', async () => {
    const first = flatBufferPayload('OMM ', 24);
    const second = flatBufferPayload('CDM ', 32);
    const stream = concatBytes(sizePrefixedFrame(first), sizePrefixedFrame(second));
    const channels = channelBackendWithStream(stream);

    const result = await ingestChannelStreamToFlatSql(channels, 'spaceaware-OMM');

    expect(result).toEqual(expect.objectContaining({ ok: true }));
    expect(result.data?.channelId).toBe('spaceaware-OMM');
    expect(result.data?.rows.listRows().map((row) => row.handle)).toEqual([
      { schemaFileId: 'OMM', rowId: 1 },
      { schemaFileId: 'CDM', rowId: 1 },
    ]);
    expect(result.data?.rows.listRows()[0].payload).toEqual(first);
    expect(result.data?.stats).toEqual(expect.objectContaining({
      framesDecoded: 2,
      framesAppended: 2,
      framesRouted: 0,
    }));
  });

  it('returns a degraded result when the channel stream cannot be opened', async () => {
    const channels = {
      openStream: async () => createDegradedResult<Uint8Array>('channels.openStream', 'HTTP 403 verified channel grant required'),
    } as Pick<ChannelBackend, 'openStream'>;

    const result = await ingestChannelStreamToFlatSql(channels, 'spaceaware-OMM');

    expect(result).toEqual(expect.objectContaining({
      ok: false,
      capability: expect.objectContaining({
        id: 'channels.ingestFlatSql',
        state: 'degraded',
        reason: expect.stringContaining('HTTP 403'),
      }),
      data: null,
    }));
  });

  it('passes private grant context to FlatSQL stream opens', async () => {
    const stream = sizePrefixedFrame(flatBufferPayload('OMM ', 24));
    let access: unknown = null;
    const channels = {
      openStream: async (_channelId: string, options?: unknown) => {
        access = options;
        return createAvailableResult('channels.openStream', stream);
      },
    } as Pick<ChannelBackend, 'openStream'>;

    const result = await ingestChannelStreamToFlatSql(channels, 'spaceaware-OMM', {
      access: { subject: 'peer-alpha', grantId: 'grant-1', visibility: 'private-listed' },
    });

    expect(result).toEqual(expect.objectContaining({ ok: true }));
    expect(access).toEqual({ subject: 'peer-alpha', grantId: 'grant-1', visibility: 'private-listed' });
  });

  it('opens a channel stream and feeds it through the SDK module stream pump', async () => {
    const requests: Array<{ methodId: string; inputs: Array<Record<string, unknown>> }> = [];
    const channels = channelBackendWithStream(concatBytes(
      sizePrefixedFrame(flatBufferPayload('OMM ', 24)),
      sizePrefixedFrame(flatBufferPayload('CDM ', 32)),
    ));

    const result = await pumpChannelStreamToModule(channels, 'spaceaware-OMM', {
      methodId: 'upsert_records',
      portId: 'records',
      maxFramesPerInvoke: 8,
      invoke: async (request) => {
        requests.push(request as { methodId: string; inputs: Array<Record<string, unknown>> });
        return { statusCode: 0, outputs: [] };
      },
    });

    expect(result).toEqual(expect.objectContaining({ ok: true }));
    expect(result.data?.channelId).toBe('spaceaware-OMM');
    expect(result.data?.lastResponse).toEqual({ statusCode: 0, outputs: [] });
    expect(requests).toHaveLength(1);
    expect(requests[0].inputs).toEqual([
      expect.objectContaining({
        portId: 'records',
        sequence: 1,
        endOfStream: false,
        typeRef: expect.objectContaining({ fileIdentifier: 'OMM' }),
      }),
      expect.objectContaining({
        portId: 'records',
        sequence: 2,
        endOfStream: true,
        typeRef: expect.objectContaining({ fileIdentifier: 'CDM' }),
      }),
    ]);
    expect(result.data?.stats).toEqual(expect.objectContaining({
      framesDecoded: 2,
      framesInvoked: 2,
      invokes: 1,
    }));
  });

  it('passes private grant context to module feed stream opens', async () => {
    let access: unknown = null;
    const channels = {
      openStream: async (_channelId: string, options?: unknown) => {
        access = options;
        return createAvailableResult('channels.openStream', sizePrefixedFrame(flatBufferPayload('OMM ', 24)));
      },
    } as Pick<ChannelBackend, 'openStream'>;

    const result = await pumpChannelStreamToModule(channels, 'spaceaware-OMM', {
      access: { subject: 'peer-alpha', grantId: 'grant-1', visibility: 'private-listed' },
      methodId: 'upsert_records',
      portId: 'records',
      invoke: async () => ({ statusCode: 0, outputs: [] }),
    });

    expect(result).toEqual(expect.objectContaining({ ok: true }));
    expect(access).toEqual({ subject: 'peer-alpha', grantId: 'grant-1', visibility: 'private-listed' });
  });
});

function channelBackendWithStream(stream: Uint8Array): Pick<ChannelBackend, 'openStream'> {
  return {
    openStream: async () => createAvailableResult('channels.openStream', stream),
  };
}

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

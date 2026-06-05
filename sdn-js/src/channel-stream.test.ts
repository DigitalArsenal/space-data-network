import { describe, expect, it } from 'vitest';
import {
  createChannelStreamDispatcher,
  createEncryptedChannelStreamDispatcher,
} from './channel-stream';

describe('channel native stream dispatcher adapter', () => {
  it('forwards mixed native size-prefixed FlatBuffer stream bytes to the dispatcher', () => {
    const dispatcher = new RecordingDispatcher();
    const stream = createChannelStreamDispatcher({
      dispatcher,
      acceptedTypes: [
        { fileIdentifier: 'OMM1', messageSize: 64, capacity: 8 },
        { fileIdentifier: 'CDM1', messageSize: 128, capacity: 4 },
      ],
    });
    const mixed = concatBytes(
      nativeFrame('OMM1', new Uint8Array([1, 2, 3])),
      nativeFrame('CDM1', new Uint8Array([4, 5])),
    );

    stream.pushChunk(mixed);

    expect(dispatcher.registered).toEqual([
      ['OMM1', 64, 8],
      ['CDM1', 128, 4],
    ]);
    expect(dispatcher.pushed).toHaveLength(1);
    expect(dispatcher.pushed[0]).toEqual(mixed);
    expect(stream.stats()).toEqual({
      bytesReceived: mixed.byteLength,
      framesReceived: 2,
      fileIdentifiers: { OMM1: 1, CDM1: 1 },
      encrypted: false,
    });
  });

  it('rejects truncated native stream chunks before dispatching', () => {
    const dispatcher = new RecordingDispatcher();
    const stream = createChannelStreamDispatcher({
      dispatcher,
      acceptedTypes: [{ fileIdentifier: 'OMM1', messageSize: 64, capacity: 8 }],
    });
    const truncated = nativeFrame('OMM1', new Uint8Array([1, 2, 3])).slice(0, 7);

    expect(() => stream.pushChunk(truncated)).toThrow(/truncated/i);
    expect(dispatcher.pushed).toHaveLength(0);
  });

  it('uses dispatcher encryption contexts for private channel streams', () => {
    const dispatcher = new RecordingDispatcher();
    const chunk = nativeFrame('OMM1', new Uint8Array([9, 8, 7]));
    const encryptionContext = {
      context: 'spaceaware-OMM',
      decryptBuffer: () => chunk,
    };
    const stream = createEncryptedChannelStreamDispatcher({
      dispatcher,
      acceptedTypes: [{ fileIdentifier: 'OMM1', messageSize: 64, capacity: 8 }],
      encryptionContexts: { OMM1: encryptionContext },
    });

    stream.pushChunk(new Uint8Array([0x8f, 0x23, 0x91, 0x05]), { recordIndex: 0 });

    expect(dispatcher.encryptionContexts).toEqual([['OMM1', encryptionContext]]);
    expect(dispatcher.pushed[0]).toEqual(chunk);
    expect(stream.stats().encrypted).toBe(true);
  });

  it('rejects encrypted private chunks without a record index before dispatching', () => {
    const dispatcher = new RecordingDispatcher();
    const encryptionContext = {
      context: 'spaceaware-OMM',
      decryptBuffer: () => nativeFrame('OMM1', new Uint8Array([1])),
    };
    const stream = createEncryptedChannelStreamDispatcher({
      dispatcher,
      acceptedTypes: [{ fileIdentifier: 'OMM1', messageSize: 64, capacity: 8 }],
      encryptionContexts: { OMM1: encryptionContext },
    });
    const encryptedChunk = new Uint8Array([0x8f, 0x23, 0x91, 0x05, 0xaa, 0x70, 0x42, 0x19, 0x5d]);

    expect(() => stream.pushChunk(encryptedChunk)).toThrow(/record index/i);
    expect(dispatcher.pushed).toHaveLength(0);
  });

  it('decrypts indexed opaque private chunks before native dispatcher routing', () => {
    const dispatcher = new RecordingDispatcher();
    const encryptedIndexes: number[] = [];
    const decrypted = nativeFrame('OMM1', new Uint8Array([7, 6, 5]));
    const encryptionContext = {
      context: 'spaceaware-OMM',
      setRecordIndex: (recordIndex: number) => encryptedIndexes.push(recordIndex),
      decryptBuffer: (bytes: Uint8Array) => {
        expect(bytes).toEqual(new Uint8Array([0x8f, 0x23, 0x91, 0x05, 0xaa, 0x70, 0x42, 0x19, 0x5d]));
        return decrypted;
      },
    };
    const stream = createEncryptedChannelStreamDispatcher({
      dispatcher,
      acceptedTypes: [{ fileIdentifier: 'OMM1', messageSize: 64, capacity: 8 }],
      encryptionContexts: { OMM1: encryptionContext },
    });
    const encryptedChunk = new Uint8Array([0x8f, 0x23, 0x91, 0x05, 0xaa, 0x70, 0x42, 0x19, 0x5d]);

    stream.pushChunk(encryptedChunk, { recordIndex: 7 });

    expect(encryptedIndexes).toEqual([7]);
    expect(dispatcher.pushed).toEqual([decrypted]);
    expect(stream.stats()).toEqual({
      bytesReceived: encryptedChunk.byteLength,
      framesReceived: 1,
      fileIdentifiers: { OMM1: 1 },
      encrypted: true,
    });
  });

  it('rejects encrypted mixed-feed chunks without a target file identifier', () => {
    const dispatcher = new RecordingDispatcher();
    const encryptionContext = {
      context: 'spaceaware-OMM',
      decryptBuffer: () => nativeFrame('OMM1', new Uint8Array([1])),
    };
    const stream = createEncryptedChannelStreamDispatcher({
      dispatcher,
      acceptedTypes: [
        { fileIdentifier: 'OMM1', messageSize: 64, capacity: 8 },
        { fileIdentifier: 'CDM1', messageSize: 64, capacity: 8 },
      ],
      encryptionContexts: { OMM1: encryptionContext, CDM1: encryptionContext },
    });

    expect(() => stream.pushChunk(new Uint8Array([0x8f, 0x23]), { recordIndex: 1 })).toThrow(/file identifier/i);
    expect(dispatcher.pushed).toHaveLength(0);
  });

  it('rejects replayed encrypted record indexes before dispatching private stream bytes', () => {
    const dispatcher = new RecordingDispatcher();
    const encryptionContext = {
      context: 'spaceaware-OMM',
      decryptBuffer: () => nativeFrame('OMM1', new Uint8Array([1])),
    };
    const stream = createEncryptedChannelStreamDispatcher({
      dispatcher,
      acceptedTypes: [{ fileIdentifier: 'OMM1', messageSize: 64, capacity: 8 }],
      encryptionContexts: { OMM1: encryptionContext },
    });
    const first = new Uint8Array([0x8f, 0x23, 0x91, 0x05]);
    const replay = new Uint8Array([0x91, 0x05, 0x8f, 0x23]);
    const decrypted = nativeFrame('OMM1', new Uint8Array([1]));

    stream.pushChunk(first, { recordIndex: 12 });

    expect(() => stream.pushChunk(replay, { recordIndex: 12 })).toThrow(/replay/i);
    expect(dispatcher.pushed).toEqual([decrypted]);
  });
});

class RecordingDispatcher {
  registered: Array<[string, number, number]> = [];
  pushed: Uint8Array[] = [];
  encryptionContexts: Array<[string, unknown]> = [];

  registerType(fileIdentifier: string, messageSize: number, capacity: number): void {
    this.registered.push([fileIdentifier, messageSize, capacity]);
  }

  pushBytes(bytes: Uint8Array): void {
    this.pushed.push(bytes);
  }

  setEncryptionContext(fileIdentifier: string, context: unknown): void {
    this.encryptionContexts.push([fileIdentifier, context]);
  }
}

function nativeFrame(fileIdentifier: string, payload: Uint8Array): Uint8Array {
  const encodedFileIdentifier = new TextEncoder().encode(fileIdentifier);
  if (encodedFileIdentifier.byteLength !== 4) {
    throw new Error('fileIdentifier must be four bytes');
  }
  const frame = new Uint8Array(4 + 4 + payload.byteLength);
  new DataView(frame.buffer).setUint32(0, 4 + payload.byteLength, true);
  frame.set(encodedFileIdentifier, 4);
  frame.set(payload, 8);
  return frame;
}

function concatBytes(...chunks: Uint8Array[]): Uint8Array {
  const total = chunks.reduce((sum, chunk) => sum + chunk.byteLength, 0);
  const out = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    out.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return out;
}

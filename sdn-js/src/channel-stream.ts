export interface NativeStreamingDispatcher {
  registerType(fileIdentifier: string, messageSize: number, capacity: number): void;
  pushBytes(bytes: Uint8Array): void;
  setEncryptionContext?(fileIdentifier: string, context: unknown): void;
}

export interface ChannelStreamType {
  fileIdentifier: string;
  messageSize: number;
  capacity: number;
}

export interface ChannelStreamStats {
  bytesReceived: number;
  framesReceived: number;
  fileIdentifiers: Record<string, number>;
  encrypted: boolean;
}

export interface ChannelStreamDispatcher {
  pushChunk(chunk: Uint8Array | ArrayBuffer | ArrayBufferView, options?: ChannelStreamPushOptions): void;
  stats(): ChannelStreamStats;
}

export interface ChannelStreamPushOptions {
  recordIndex?: number;
}

export interface ChannelStreamDispatcherOptions {
  dispatcher: NativeStreamingDispatcher;
  acceptedTypes: ChannelStreamType[];
}

export interface EncryptedChannelStreamDispatcherOptions extends ChannelStreamDispatcherOptions {
  encryptionContexts: Record<string, unknown>;
}

export function createChannelStreamDispatcher(options: ChannelStreamDispatcherOptions): ChannelStreamDispatcher {
  return new NativeChannelStreamDispatcher(options, false);
}

export function createEncryptedChannelStreamDispatcher(
  options: EncryptedChannelStreamDispatcherOptions,
): ChannelStreamDispatcher {
  if (!options.dispatcher.setEncryptionContext) {
    throw new Error('native streaming dispatcher does not support encryption contexts');
  }
  for (const type of options.acceptedTypes) {
    const context = options.encryptionContexts[type.fileIdentifier];
    if (!context) {
      throw new Error(`missing encryption context for ${type.fileIdentifier}`);
    }
    options.dispatcher.setEncryptionContext(type.fileIdentifier, context);
  }
  return new NativeChannelStreamDispatcher(options, true);
}

class NativeChannelStreamDispatcher implements ChannelStreamDispatcher {
  private readonly dispatcher: NativeStreamingDispatcher;
  private readonly accepted: Set<string>;
  private readonly counters: Record<string, number> = {};
  private readonly encryptedRecordIndexes = new Set<number>();
  private bytesReceived = 0;
  private framesReceived = 0;

  constructor(options: ChannelStreamDispatcherOptions, private readonly encrypted: boolean) {
    if (!options.dispatcher) {
      throw new Error('native streaming dispatcher is required');
    }
    if (!options.acceptedTypes.length) {
      throw new Error('at least one accepted stream type is required');
    }
    this.dispatcher = options.dispatcher;
    this.accepted = new Set();
    for (const type of options.acceptedTypes) {
      assertFileIdentifier(type.fileIdentifier);
      this.accepted.add(type.fileIdentifier);
      this.counters[type.fileIdentifier] = 0;
      this.dispatcher.registerType(type.fileIdentifier, type.messageSize, type.capacity);
    }
  }

  pushChunk(chunk: Uint8Array | ArrayBuffer | ArrayBufferView, options: ChannelStreamPushOptions = {}): void {
    const bytes = asUint8Array(chunk);
    if (this.encrypted) {
      this.assertEncryptedRecordIndex(options.recordIndex);
      this.dispatcher.pushBytes(bytes);
      this.bytesReceived += bytes.byteLength;
      return;
    }
    const frameCounts = scanNativeFrames(bytes, this.accepted);
    this.dispatcher.pushBytes(bytes);
    this.bytesReceived += bytes.byteLength;
    for (const [fileIdentifier, count] of Object.entries(frameCounts)) {
      this.counters[fileIdentifier] = (this.counters[fileIdentifier] ?? 0) + count;
      this.framesReceived += count;
    }
  }

  stats(): ChannelStreamStats {
    return {
      bytesReceived: this.bytesReceived,
      framesReceived: this.framesReceived,
      fileIdentifiers: { ...this.counters },
      encrypted: this.encrypted,
    };
  }

  private assertEncryptedRecordIndex(recordIndex: number | undefined): void {
    if (typeof recordIndex !== 'number' || !Number.isSafeInteger(recordIndex) || recordIndex < 0) {
      throw new Error('encrypted channel stream chunks require a non-negative record index');
    }
    const index = recordIndex;
    if (this.encryptedRecordIndexes.has(index)) {
      throw new Error(`replayed encrypted channel stream record index ${index}`);
    }
    this.encryptedRecordIndexes.add(index);
  }
}

function scanNativeFrames(bytes: Uint8Array, accepted: Set<string>): Record<string, number> {
  const counts: Record<string, number> = {};
  let offset = 0;
  while (offset < bytes.byteLength) {
    if (offset + 4 > bytes.byteLength) {
      throw new Error(`truncated native FlatBuffer stream header at offset ${offset}`);
    }
    const size = new DataView(bytes.buffer, bytes.byteOffset + offset, 4).getUint32(0, true);
    if (size < 4) {
      throw new Error(`invalid native FlatBuffer stream frame size ${size} at offset ${offset}`);
    }
    const frameEnd = offset + 4 + size;
    if (frameEnd > bytes.byteLength) {
      throw new Error(`truncated native FlatBuffer stream frame at offset ${offset}`);
    }
    const fileIdentifier = new TextDecoder().decode(bytes.subarray(offset + 4, offset + 8));
    if (!accepted.has(fileIdentifier)) {
      throw new Error(`unregistered native FlatBuffer file identifier ${fileIdentifier}`);
    }
    counts[fileIdentifier] = (counts[fileIdentifier] ?? 0) + 1;
    offset = frameEnd;
  }
  return counts;
}

function assertFileIdentifier(fileIdentifier: string): void {
  if (new TextEncoder().encode(fileIdentifier).byteLength !== 4) {
    throw new Error(`fileIdentifier ${JSON.stringify(fileIdentifier)} must be exactly four bytes`);
  }
}

function asUint8Array(chunk: Uint8Array | ArrayBuffer | ArrayBufferView): Uint8Array {
  if (chunk instanceof Uint8Array) {
    return chunk;
  }
  if (chunk instanceof ArrayBuffer) {
    return new Uint8Array(chunk);
  }
  return new Uint8Array(chunk.buffer, chunk.byteOffset, chunk.byteLength);
}

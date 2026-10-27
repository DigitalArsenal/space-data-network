import {
  createFlatBufferStreamIngestor,
  createFlatSqlRuntimeStore,
  type FlatBufferStreamIngestStats,
  type FlatBufferStreamIngestor,
  type FlatSqlRuntimeStore,
  type FlatSqlRuntimeStoreOptions,
} from 'space-data-module-sdk/runtime-host';

export interface ChannelFlatSqlIngestor {
  rows: FlatSqlRuntimeStore;
  pushChunk(chunk: Uint8Array | ArrayBuffer | ArrayBufferView): Promise<number>;
  finish(): Promise<0>;
  stats(): FlatBufferStreamIngestStats;
}

export interface ChannelFlatSqlIngestorOptions {
  rows?: FlatSqlRuntimeStore;
  store?: FlatSqlRuntimeStoreOptions;
}

export function createChannelFlatSqlIngestor(options: ChannelFlatSqlIngestorOptions = {}): ChannelFlatSqlIngestor {
  const rows = options.rows ?? createFlatSqlRuntimeStore(options.store);
  const ingestor: FlatBufferStreamIngestor = createFlatBufferStreamIngestor({ rows });
  return {
    rows,
    pushChunk(chunk) {
      return ingestor.pushBytes(chunk);
    },
    finish() {
      return ingestor.finish();
    },
    stats() {
      return { ...ingestor.stats };
    },
  };
}

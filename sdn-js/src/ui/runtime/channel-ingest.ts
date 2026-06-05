import {
  createFlatBufferStreamIngestor,
  createFlatSqlRuntimeStore,
  type FlatBufferStreamIngestStats,
  type FlatBufferStreamIngestor,
  type FlatSqlRuntimeStore,
} from 'space-data-module-sdk/runtime-host';

export interface ChannelFlatSqlIngestor {
  rows: FlatSqlRuntimeStore;
  pushChunk(chunk: Uint8Array | ArrayBuffer | ArrayBufferView): number;
  finish(): 0;
  stats(): FlatBufferStreamIngestStats;
}

export interface ChannelFlatSqlIngestorOptions {
  rows?: FlatSqlRuntimeStore;
}

export function createChannelFlatSqlIngestor(options: ChannelFlatSqlIngestorOptions = {}): ChannelFlatSqlIngestor {
  const rows = options.rows ?? createFlatSqlRuntimeStore();
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

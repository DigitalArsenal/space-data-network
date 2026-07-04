/**
 * Datasync session (loop D.4): a bounded walk of a peer's flatsql-sync
 * cursor chain (`read_chunk` over the server's rowid-snapshot cursor — the
 * sdn_record_index rowid space, GROUND TRUTH: wire-stable for deployed
 * peers) that materializes every chunk into THE engine record store with
 * true provider/source/batch provenance.
 */
import type {
  FlatSQLEngineRecordStore,
  EngineSyncChunkIngestOptions,
} from './engine-record-store';
import type { FlatSqlSyncChunk, FlatSqlSyncQuery } from './flatsql-sync';

/** The transport surface a sync session drives (SDNNode satisfies it). */
export interface FlatSqlSyncChunkTransport {
  readFlatSqlSyncChunk(query: FlatSqlSyncQuery): Promise<FlatSqlSyncChunk>;
}

export interface FlatSqlSchemaSyncOptions extends EngineSyncChunkIngestOptions {
  /** Chunk budget for this session (bounded by design; default 32). */
  maxChunks?: number;
}

/** Result of a bounded datasync session (loop D.4 `syncFlatSqlSchema`). */
export interface FlatSqlSchemaSyncSummary {
  schema: string;
  standardId: string;
  /** Chunks fetched from the peer this session. */
  chunks: number;
  /** Record frames delivered by the peer (before dedupe). */
  totalRecords: number;
  /** New engine rows materialized (idempotent replay ingests 0). */
  ingestedRecords: number;
  /** New envelope index rows. */
  indexedEnvelopes: number;
  /** Engine source partitions touched (true provider/source provenance). */
  sources: string[];
  snapshotId: string;
  head: string;
  highWaterMark: string;
  /** Resume cursor (rowid-snapshot space) — empty when the walk completed. */
  nextCursor: string;
  complete: boolean;
}

/**
 * Walk `read_chunk` pages from `transport` and materialize each into
 * `store` (per-provider engine source partitions + envelope index rows +
 * ingested-keys dedupe — see FlatSQLEngineRecordStore.ingestSyncChunk).
 * Stops when the peer reports no next cursor, a chunk comes back empty, or
 * `maxChunks` is reached; the summary's `nextCursor` resumes the walk.
 */
export async function syncFlatSqlSchemaIntoStore(
  transport: FlatSqlSyncChunkTransport,
  store: FlatSQLEngineRecordStore,
  query: FlatSqlSyncQuery,
  options: FlatSqlSchemaSyncOptions = {},
): Promise<FlatSqlSchemaSyncSummary> {
  const { maxChunks, ...ingestOptions } = options;
  const chunkBudget = Math.max(1, Math.floor(maxChunks ?? 32));
  const summary: FlatSqlSchemaSyncSummary = {
    schema: query.schema,
    standardId: query.schema.trim().split('.')[0]?.toUpperCase() ?? '',
    chunks: 0,
    totalRecords: 0,
    ingestedRecords: 0,
    indexedEnvelopes: 0,
    sources: [],
    snapshotId: query.snapshotId ?? '',
    head: query.head ?? '',
    highWaterMark: '',
    nextCursor: query.cursor ?? '',
    complete: false,
  };
  const sources = new Set<string>();
  let cursor = query.cursor ?? '';
  let snapshotId = query.snapshotId ?? '';
  let head = query.head ?? '';

  while (summary.chunks < chunkBudget) {
    const chunk = await transport.readFlatSqlSyncChunk({
      ...query,
      op: 'read_chunk',
      ...(cursor
        ? { cursor, snapshotId: snapshotId || undefined, head: head || undefined }
        : {}),
    });
    summary.chunks += 1;
    const result = await store.ingestSyncChunk(chunk, {
      ...ingestOptions,
      providerId: ingestOptions.providerId ?? query.providerId ?? null,
      sourceName: ingestOptions.sourceName ?? query.sourceName ?? null,
    });
    summary.totalRecords += result.totalRecords;
    summary.ingestedRecords += result.ingestedRecords;
    summary.indexedEnvelopes += result.indexedEnvelopes;
    for (const source of result.sources) sources.add(source);
    snapshotId = chunk.header.snapshotId || snapshotId;
    head = chunk.header.head || head;
    summary.snapshotId = snapshotId;
    summary.head = head;
    summary.highWaterMark = chunk.header.highWaterMark || summary.highWaterMark;
    cursor = chunk.header.nextCursor ?? '';
    summary.nextCursor = cursor;
    if (!cursor || chunk.header.count === 0) {
      summary.complete = true;
      break;
    }
  }
  summary.sources = [...sources];
  return summary;
}

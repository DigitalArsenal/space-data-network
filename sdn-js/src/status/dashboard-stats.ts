/**
 * Dashboard stats view model.
 *
 * Decodes a size-prefixed `DashboardStatsSet` FlatBuffer (file identifier
 * `$NDS`) into a plain, JSON-friendly view model. The node builds these frames
 * on a background lane and serves them from RAM at
 * `GET /api/v1/dashboard/stats`, and pushes the same bytes on `/ws/status`
 * beside the `$NST` node-status frames — so a client reading that socket must
 * dispatch on the file identifier, which is what
 * {@link isDashboardStatsFrame} is for.
 *
 * The generated code under `./generated` is produced by the schema task and is
 * never edited here.
 */

import * as flatbuffers from 'flatbuffers';

import { DashboardStatsSet } from './generated/nst.js';

/** One schema's record footprint in the node's store. */
export interface DashboardSchemaStatView {
  /** SDS schema name, e.g. 'OMM'. */
  schema: string;
  recordCount: number;
  totalBytes: number;
}

/** One (schema, provider, source, batch) ingest lane's progress. */
export interface DashboardSourceStatView {
  schema: string;
  providerId: string;
  sourceName: string;
  batchId: string;
  recordCount: number;
  totalBytes: number;
  /** Unix seconds; 0 = unknown. */
  firstIngestAt: number;
  lastIngestAt: number;
  updatedAt: number;
}

/** The decoded stats set as a plain view model. */
export interface DashboardStatsView {
  /** Unix seconds when the node assembled this snapshot. */
  generatedAt: number;
  /**
   * True when the node's read hit its store budget and these are
   * last-known-good numbers. A stale snapshot is still real data — render it
   * as stale, never as zero.
   */
  stale: boolean;
  /** Unix seconds the numbers were last true; 0 = never read. */
  asOf: number;
  totalRecords: number;
  totalBytes: number;
  schemas: DashboardSchemaStatView[];
  sources: DashboardSourceStatView[];
}

/** `$NDS`, the dashboard-stats file identifier. */
const DASHBOARD_STATS_IDENTIFIER = '$NDS';

/**
 * True when `bytes` is a size-prefixed `$NDS` frame. In a size-prefixed
 * buffer the 4-byte length comes first, then the root offset, then the
 * identifier at bytes 8..12.
 */
export function isDashboardStatsFrame(bytes: Uint8Array): boolean {
  if (bytes.length < 12) return false;
  for (let i = 0; i < 4; i += 1) {
    if (bytes[8 + i] !== DASHBOARD_STATS_IDENTIFIER.charCodeAt(i)) return false;
  }
  return true;
}

/**
 * Decode size-prefixed `DashboardStatsSet` bytes into the plain view model.
 *
 * @throws if the bytes are not a valid size-prefixed `$NDS` buffer.
 */
export function decodeDashboardStats(frame: Uint8Array): DashboardStatsView {
  const buffer = new flatbuffers.ByteBuffer(frame);
  const set = DashboardStatsSet.getSizePrefixedRootAsDashboardStatsSet(buffer);

  const schemas: DashboardSchemaStatView[] = [];
  const schemaCount = set.schemasLength();
  for (let i = 0; i < schemaCount; i += 1) {
    const row = set.SCHEMAS(i);
    if (!row) continue;
    schemas.push({
      schema: row.SCHEMA() ?? '',
      recordCount: Number(row.RECORD_COUNT()),
      totalBytes: Number(row.TOTAL_BYTES()),
    });
  }

  const sources: DashboardSourceStatView[] = [];
  const sourceCount = set.sourcesLength();
  for (let i = 0; i < sourceCount; i += 1) {
    const row = set.SOURCES(i);
    if (!row) continue;
    sources.push({
      schema: row.SCHEMA() ?? '',
      providerId: row.PROVIDER_ID() ?? '',
      sourceName: row.SOURCE_NAME() ?? '',
      batchId: row.BATCH_ID() ?? '',
      recordCount: Number(row.RECORD_COUNT()),
      totalBytes: Number(row.TOTAL_BYTES()),
      firstIngestAt: Number(row.FIRST_INGEST_AT()),
      lastIngestAt: Number(row.LAST_INGEST_AT()),
      updatedAt: Number(row.UPDATED_AT()),
    });
  }

  return {
    generatedAt: Number(set.GENERATED_AT()),
    stale: set.STALE(),
    asOf: Number(set.AS_OF()),
    totalRecords: Number(set.TOTAL_RECORDS()),
    totalBytes: Number(set.TOTAL_BYTES()),
    schemas,
    sources,
  };
}

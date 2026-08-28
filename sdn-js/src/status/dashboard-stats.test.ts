import * as flatbuffers from 'flatbuffers';
import { describe, expect, it } from 'vitest';

import { decodeDashboardStats, isDashboardStatsFrame } from './dashboard-stats';
import {
  DashboardSchemaStat,
  DashboardSourceStat,
  DashboardStatsSet,
  NodeStatusSet,
} from './generated/nst.js';

/** Build the same frame shape the node emits, with the generated builders. */
function buildFrame(): Uint8Array {
  const b = new flatbuffers.Builder(1024);

  const schemaName = b.createString('OMM');
  DashboardSchemaStat.startDashboardSchemaStat(b);
  DashboardSchemaStat.addSchema(b, schemaName);
  DashboardSchemaStat.addRecordCount(b, BigInt(10847));
  DashboardSchemaStat.addTotalBytes(b, BigInt(4200000));
  const schema = DashboardSchemaStat.endDashboardSchemaStat(b);
  const schemas = DashboardStatsSet.createSchemasVector(b, [schema]);

  const srcSchema = b.createString('OMM');
  const provider = b.createString('celestrak');
  const sourceName = b.createString('gp');
  const batchId = b.createString('b-1');
  DashboardSourceStat.startDashboardSourceStat(b);
  DashboardSourceStat.addSchema(b, srcSchema);
  DashboardSourceStat.addProviderId(b, provider);
  DashboardSourceStat.addSourceName(b, sourceName);
  DashboardSourceStat.addBatchId(b, batchId);
  DashboardSourceStat.addRecordCount(b, BigInt(10847));
  DashboardSourceStat.addTotalBytes(b, BigInt(4200000));
  DashboardSourceStat.addFirstIngestAt(b, BigInt(1755999000));
  DashboardSourceStat.addLastIngestAt(b, BigInt(1756000000));
  DashboardSourceStat.addUpdatedAt(b, BigInt(1756000000));
  const source = DashboardSourceStat.endDashboardSourceStat(b);
  const sources = DashboardStatsSet.createSourcesVector(b, [source]);

  DashboardStatsSet.startDashboardStatsSet(b);
  DashboardStatsSet.addGeneratedAt(b, BigInt(1756000123));
  DashboardStatsSet.addSchemas(b, schemas);
  DashboardStatsSet.addSources(b, sources);
  DashboardStatsSet.addTotalRecords(b, BigInt(10847));
  DashboardStatsSet.addTotalBytes(b, BigInt(4200000));
  DashboardStatsSet.addStale(b, true);
  DashboardStatsSet.addAsOf(b, BigInt(1756000000));
  const set = DashboardStatsSet.endDashboardStatsSet(b);

  DashboardStatsSet.finishSizePrefixedDashboardStatsSetBuffer(b, set);
  return b.asUint8Array();
}

/** A size-prefixed $NST frame, to prove the two are distinguishable. */
function buildNodeStatusFrame(): Uint8Array {
  const b = new flatbuffers.Builder(256);
  const peerId = b.createString('12D3KooTest');
  NodeStatusSet.startNodeStatusSet(b);
  NodeStatusSet.addGeneratedAt(b, BigInt(1756000123000));
  NodeStatusSet.addSourcePeerId(b, peerId);
  const set = NodeStatusSet.endNodeStatusSet(b);
  NodeStatusSet.finishSizePrefixedNodeStatusSetBuffer(b, set);
  return b.asUint8Array();
}

describe('dashboard stats frames', () => {
  it('decodes a size-prefixed $NDS frame into the view model', () => {
    const view = decodeDashboardStats(buildFrame());

    expect(view).toEqual({
      generatedAt: 1756000123,
      stale: true,
      asOf: 1756000000,
      totalRecords: 10847,
      totalBytes: 4200000,
      schemas: [{ schema: 'OMM', recordCount: 10847, totalBytes: 4200000 }],
      sources: [
        {
          schema: 'OMM',
          providerId: 'celestrak',
          sourceName: 'gp',
          batchId: 'b-1',
          recordCount: 10847,
          totalBytes: 4200000,
          firstIngestAt: 1755999000,
          lastIngestAt: 1756000000,
          updatedAt: 1756000000,
        },
      ],
    });
  });

  it('tells $NDS frames apart from the $NST frames on the same socket', () => {
    expect(isDashboardStatsFrame(buildFrame())).toBe(true);
    expect(isDashboardStatsFrame(buildNodeStatusFrame())).toBe(false);
    expect(isDashboardStatsFrame(new Uint8Array([1, 2, 3]))).toBe(false);
  });
});

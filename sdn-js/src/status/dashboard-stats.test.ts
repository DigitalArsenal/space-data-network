import * as flatbuffers from 'flatbuffers';
import { describe, expect, it } from 'vitest';

import { decodeDashboardStats, isDashboardStatsFrame } from './dashboard-stats';
import {
  DashboardIngestEvent,
  DashboardIngestEventKind,
  DashboardSchemaStat,
  DashboardSourceStat,
  DashboardStatsSet,
  DashboardTopicStat,
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
  DashboardSourceStat.addWindowRecords(b, BigInt(17));
  DashboardSourceStat.addPriorWindowRecords(b, BigInt(11));
  DashboardSourceStat.addWindowMs(b, BigInt(300000));
  const source = DashboardSourceStat.endDashboardSourceStat(b);
  const sources = DashboardStatsSet.createSourcesVector(b, [source]);

  const eventSchema = b.createString('OMM');
  const eventProvider = b.createString('celestrak');
  const eventSource = b.createString('gp');
  const eventMessage = b.createString('Ingest resumed after a quiet period.');
  const event = DashboardIngestEvent.createDashboardIngestEvent(
    b,
    DashboardIngestEventKind.Recover,
    eventSchema,
    eventProvider,
    eventSource,
    eventMessage,
    BigInt(1),
    BigInt(1756000010),
  );
  const events = DashboardStatsSet.createEventsVector(b, [event]);

  const topicName = b.createString('/sdn/OMM/v1');
  const topic = DashboardTopicStat.createDashboardTopicStat(
    b,
    topicName,
    2.5,
    BigInt(1756000005),
    true,
  );
  const topics = DashboardStatsSet.createTopicsVector(b, [topic]);

  DashboardStatsSet.startDashboardStatsSet(b);
  DashboardStatsSet.addGeneratedAt(b, BigInt(1756000123));
  DashboardStatsSet.addSchemas(b, schemas);
  DashboardStatsSet.addSources(b, sources);
  DashboardStatsSet.addTotalRecords(b, BigInt(10847));
  DashboardStatsSet.addTotalBytes(b, BigInt(4200000));
  DashboardStatsSet.addStale(b, true);
  DashboardStatsSet.addAsOf(b, BigInt(1756000000));
  DashboardStatsSet.addEvents(b, events);
  DashboardStatsSet.addTopics(b, topics);
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
          windowRecords: 17,
          priorWindowRecords: 11,
          windowMs: 300000,
        },
      ],
      events: [
        {
          kind: 'recover',
          schema: 'OMM',
          providerId: 'celestrak',
          sourceName: 'gp',
          message: 'Ingest resumed after a quiet period.',
          count: 1,
          at: 1756000010,
        },
      ],
      topics: [
        {
          topic: '/sdn/OMM/v1',
          ratePerMin: 2.5,
          lastSeenAt: 1756000005,
          subscribed: true,
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

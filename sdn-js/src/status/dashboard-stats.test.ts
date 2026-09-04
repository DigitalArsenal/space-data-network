import * as flatbuffers from 'flatbuffers';
import { describe, expect, it } from 'vitest';

import { decodeDashboardStats, isDashboardStatsFrame } from './dashboard-stats';
import {
  NodeStatusSet,
} from './generated/nst.js';
import {
  NDS,
  NDSIngestEvent,
  NDSSchemaStat,
  NDSSourceStat,
  NDSTopicStat,
  ndsIngestEventKind,
} from './generated/nst/main.js';

/** Build the same frame shape the node emits, with the generated builders. */
function buildFrame(includeStorage = true): Uint8Array {
  const b = new flatbuffers.Builder(1024);

  const schemaName = b.createString('OMM');
  NDSSchemaStat.startNDSSchemaStat(b);
  NDSSchemaStat.addSchema(b, schemaName);
  NDSSchemaStat.addRecordCount(b, BigInt(10847));
  NDSSchemaStat.addTotalBytes(b, BigInt(4200000));
  const schema = NDSSchemaStat.endNDSSchemaStat(b);
  const schemas = NDS.createSchemasVector(b, [schema]);

  const srcSchema = b.createString('OMM');
  const provider = b.createString('celestrak');
  const sourceName = b.createString('gp');
  const batchId = b.createString('b-1');
  NDSSourceStat.startNDSSourceStat(b);
  NDSSourceStat.addSchema(b, srcSchema);
  NDSSourceStat.addProviderId(b, provider);
  NDSSourceStat.addSourceName(b, sourceName);
  NDSSourceStat.addBatchId(b, batchId);
  NDSSourceStat.addRecordCount(b, BigInt(10847));
  NDSSourceStat.addTotalBytes(b, BigInt(4200000));
  NDSSourceStat.addFirstIngestAt(b, BigInt(1755999000));
  NDSSourceStat.addLastIngestAt(b, BigInt(1756000000));
  NDSSourceStat.addUpdatedAt(b, BigInt(1756000000));
  NDSSourceStat.addWindowRecords(b, BigInt(17));
  NDSSourceStat.addPriorWindowRecords(b, BigInt(11));
  NDSSourceStat.addWindowMs(b, BigInt(300000));
  const source = NDSSourceStat.endNDSSourceStat(b);
  const sources = NDS.createSourcesVector(b, [source]);

  const eventSchema = b.createString('OMM');
  const eventProvider = b.createString('celestrak');
  const eventSource = b.createString('gp');
  const eventMessage = b.createString('Ingest resumed after a quiet period.');
  const event = NDSIngestEvent.createNDSIngestEvent(
    b,
    ndsIngestEventKind.Recover,
    eventSchema,
    eventProvider,
    eventSource,
    eventMessage,
    BigInt(1),
    BigInt(1756000010),
  );
  const events = NDS.createEventsVector(b, [event]);

  const topicName = b.createString('/sdn/OMM/v1');
  const topic = NDSTopicStat.createNDSTopicStat(
    b,
    topicName,
    2.5,
    BigInt(1756000005),
    true,
  );
  const topics = NDS.createTopicsVector(b, [topic]);

  NDS.startNDS(b);
  NDS.addGeneratedAt(b, BigInt(1756000123));
  NDS.addSchemas(b, schemas);
  NDS.addSources(b, sources);
  NDS.addTotalRecords(b, BigInt(10847));
  NDS.addTotalBytes(b, BigInt(4200000));
  NDS.addStale(b, true);
  NDS.addAsOf(b, BigInt(1756000000));
  NDS.addEvents(b, events);
  NDS.addTopics(b, topics);
  if (includeStorage) {
    NDS.addStorageFreeBytes(b, BigInt(274877906944));
    NDS.addStorageCapacityBytes(b, BigInt(549755813888));
  }
  const set = NDS.endNDS(b);

  NDS.finishSizePrefixedNDSBuffer(b, set);
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
      storageFreeBytes: 274877906944,
      storageCapacityBytes: 549755813888,
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

  it('defaults storage sizes to zero when decoding a pre-1.212 NDS frame', () => {
    const view = decodeDashboardStats(buildFrame(false));

    expect(view.storageFreeBytes).toBe(0);
    expect(view.storageCapacityBytes).toBe(0);
  });

  it('tells $NDS frames apart from the $NST frames on the same socket', () => {
    expect(isDashboardStatsFrame(buildFrame())).toBe(true);
    expect(isDashboardStatsFrame(buildNodeStatusFrame())).toBe(false);
    expect(isDashboardStatsFrame(new Uint8Array([1, 2, 3]))).toBe(false);
  });
});

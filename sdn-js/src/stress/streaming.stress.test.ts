/**
 * Stress tests for streaming FlatBuffer data reception from SDN nodes.
 *
 * These tests are EXCLUDED from normal test runs via vitest.config.mts.
 *
 * Run explicitly with:
 *   npx vitest run --config vitest.stress.config.mts
 *
 * Environment variables:
 *   - STRESS_TARGET_MB: Target data volume in MB for the in-memory streaming
 *     test (default: 64)
 *
 * All tests run fully offline and deterministically. They exercise the real
 * FlatSQL sync chunk codec (requestFlatSqlSyncChunk / decodeFlatSqlSyncChunk)
 * and the real SubscriptionManager delivery pipeline over an in-memory
 * loopback transport that implements FlatSqlSyncTransport.
 *
 * A full end-to-end network test additionally requires a live SDN node that
 * serves the FlatSQL sync protocol behind a reachable websocket relay
 * multiaddr. SDNNode only bundles browser dial transports (websockets,
 * webtransport, webrtc, circuit-relay) and exposes no listen addresses, so
 * two in-process SDNNode instances cannot connect to each other inside
 * vitest. Use `npm run measure:flatsql-sync` or `npm run chaos:local` for
 * live-network runs against a real node.
 */

import { describe, it, expect, vi } from 'vitest';

import {
  FLATSQL_SYNC_PROTOCOL_ID,
  requestFlatSqlSyncChunk,
  type FlatSqlSyncTransport,
} from '../flatsql-sync';
import { SubscriptionManager } from '../subscription';

// Configuration from environment
const TARGET_MB = parseInt(process.env.STRESS_TARGET_MB || '64', 10);
const RECORD_BYTES = 256;
const RECORDS_PER_CHUNK = 512;
const STREAM_TEST_TIMEOUT_MS = 120_000;

const encoder = new TextEncoder();
const decoder = new TextDecoder();

// Statistics tracking
interface StreamStats {
  receivedRecords: number;
  receivedBytes: number;
  startTime: number;
  lastLogTime: number;
  uniqueCIDs: Set<string>;
}

/**
 * Format bytes as human-readable string
 */
function formatBytes(bytes: number): string {
  if (bytes >= 1024 * 1024 * 1024) {
    return `${(bytes / (1024 * 1024 * 1024)).toFixed(2)} GB`;
  }
  if (bytes >= 1024 * 1024) {
    return `${(bytes / (1024 * 1024)).toFixed(2)} MB`;
  }
  if (bytes >= 1024) {
    return `${(bytes / 1024).toFixed(2)} KB`;
  }
  return `${bytes} bytes`;
}

/**
 * Log progress statistics
 */
function logProgress(stats: StreamStats, targetBytes: number): void {
  const elapsed = (Date.now() - stats.startTime) / 1000;
  const rate = stats.receivedBytes / elapsed;
  const progress = (stats.receivedBytes / targetBytes) * 100;

  console.log(
    `Progress: ${progress.toFixed(1)}% | ` +
      `${stats.receivedRecords.toLocaleString()} records | ` +
      `${formatBytes(stats.receivedBytes)} | ` +
      `${formatBytes(rate)}/s | ` +
      `${stats.uniqueCIDs.size.toLocaleString()} unique CIDs`
  );
}

/**
 * Builds a deterministic OMM-like JSON record padded to exactly `size` bytes.
 */
function makeOmmRecordBytes(index: number, size: number): Uint8Array {
  const record = {
    OBJECT_NAME: `STRESS-SAT-${index}`,
    NORAD_CAT_ID: index,
    EPOCH: '2026-01-01T00:00:00.000Z',
    MEAN_MOTION: 15.5 + (index % 100) / 1000,
    ECCENTRICITY: 0.0001 + (index % 10) / 100000,
    INCLINATION: 51.6,
    PAD: '',
  };
  const baseLength = encoder.encode(JSON.stringify(record)).byteLength;
  if (baseLength > size) {
    throw new Error(`record ${index} base size ${baseLength} exceeds ${size}`);
  }
  record.PAD = 'x'.repeat(size - baseLength);
  return encoder.encode(JSON.stringify(record));
}

/**
 * Decodes a length-prefixed JSON request frame (matches encodeFlatSqlSyncRequest).
 */
function decodeSyncRequestFrame(frame: Uint8Array): Record<string, unknown> {
  const view = new DataView(frame.buffer, frame.byteOffset, frame.byteLength);
  const length = view.getUint32(0, false);
  return JSON.parse(decoder.decode(frame.slice(4, 4 + length))) as Record<string, unknown>;
}

/**
 * Builds a FlatSQL sync chunk response: a big-endian length-prefixed JSON
 * header frame followed by a little-endian size-prefixed record stream
 * (the wire format consumed by decodeFlatSqlSyncChunk).
 */
function buildSyncChunkResponse(header: Record<string, unknown>, records: Uint8Array[]): Uint8Array {
  const headerBytes = encoder.encode(JSON.stringify(header));
  const streamLength = records.reduce((sum, record) => sum + 4 + record.byteLength, 0);
  const out = new Uint8Array(4 + headerBytes.byteLength + streamLength);
  const view = new DataView(out.buffer);
  view.setUint32(0, headerBytes.byteLength, false);
  out.set(headerBytes, 4);
  let offset = 4 + headerBytes.byteLength;
  for (const record of records) {
    view.setUint32(offset, record.byteLength, true);
    out.set(record, offset + 4);
    offset += 4 + record.byteLength;
  }
  return out;
}

/**
 * In-memory loopback producer implementing the real FlatSqlSyncTransport
 * interface. Serves `totalRecords` OMM-like records in resumable chunks,
 * paginated via cursor/next_cursor exactly like a live SDN node.
 */
function createLoopbackSyncTransport(totalRecords: number): FlatSqlSyncTransport {
  return {
    async dialProtocol(_targetPeerId, protocolId, payload) {
      if (protocolId !== FLATSQL_SYNC_PROTOCOL_ID) {
        throw new Error(`unexpected protocol ${protocolId}`);
      }
      const request = decodeSyncRequestFrame(payload);
      if (request.op !== 'read_chunk') {
        throw new Error(`unexpected op ${String(request.op)}`);
      }

      const offset = typeof request.cursor === 'string' ? parseInt(request.cursor, 10) : 0;
      const count = Math.min(RECORDS_PER_CHUNK, totalRecords - offset);
      const records: Uint8Array[] = [];
      const results: Record<string, unknown>[] = [];
      for (let i = 0; i < count; i++) {
        const index = offset + i;
        records.push(makeOmmRecordBytes(index, RECORD_BYTES));
        results.push({
          schema_name: 'OMM.fbs',
          cid: `bafybei${index.toString(16).padStart(40, '0')}`,
          peer_id: 'loopback-producer',
          size_bytes: RECORD_BYTES,
        });
      }

      const nextOffset = offset + count;
      return buildSyncChunkResponse(
        {
          schema: String(request.schema ?? 'OMM.fbs'),
          total_count: totalRecords,
          count,
          limit: RECORDS_PER_CHUNK,
          offset,
          cursor: String(offset),
          next_cursor: nextOffset < totalRecords ? String(nextOffset) : '',
          sync_protocol: FLATSQL_SYNC_PROTOCOL_ID,
          results,
        },
        records
      );
    },
  };
}

describe('FlatBuffer Streaming Stress Tests', () => {
  it(
    'should stream and process large volume of OMM records',
    async () => {
      const targetBytes = TARGET_MB * 1024 * 1024;
      const totalRecords = Math.ceil(targetBytes / RECORD_BYTES);
      const stats: StreamStats = {
        receivedRecords: 0,
        receivedBytes: 0,
        startTime: Date.now(),
        lastLogTime: Date.now(),
        uniqueCIDs: new Set(),
      };

      console.log('');
      console.log('='.repeat(60));
      console.log('Starting FlatBuffer Streaming Stress Test');
      console.log(`Target: ${formatBytes(targetBytes)} (${totalRecords.toLocaleString()} records)`);
      console.log('Transport: in-memory loopback (real FlatSQL sync codec)');
      console.log('='.repeat(60));
      console.log('');

      const transport = createLoopbackSyncTransport(totalRecords);
      const manager = new SubscriptionManager();
      const sub = manager.createSubscription({
        dataTypes: ['OMM'],
        sourcePeers: ['all'],
        encrypted: false,
        streaming: true,
      });

      let deliveredRecords = 0;
      manager.addEventListener(sub.id, (event) => {
        if (event.type === 'message') {
          deliveredRecords++;
        }
      });

      // Stream chunks through the real protocol codec, following the
      // resumable cursor exactly like a live sync against an SDN node.
      let cursor = '';
      do {
        const chunk = await requestFlatSqlSyncChunk(transport, {
          targetPeerId: 'loopback-producer',
          schema: 'OMM.fbs',
          limit: RECORDS_PER_CHUNK,
          ...(cursor ? { cursor } : {}),
        });

        expect(chunk.header.schema).toBe('OMM.fbs');
        expect(chunk.records.length).toBe(chunk.header.count);

        for (let i = 0; i < chunk.records.length; i++) {
          const recordBytes = chunk.records[i];
          stats.receivedBytes += recordBytes.byteLength;
          stats.receivedRecords++;

          const ref = chunk.header.results[i];
          if (ref?.cid) {
            stats.uniqueCIDs.add(ref.cid);
          }

          // Push each decoded record through the real subscription pipeline.
          const parsed = JSON.parse(decoder.decode(recordBytes)) as Record<string, unknown>;
          manager.processMessage('OMM.fbs', parsed, ref?.peerId ?? 'loopback-producer');
        }

        // Log every 2 seconds
        if (Date.now() - stats.lastLogTime > 2000) {
          logProgress(stats, targetBytes);
          stats.lastLogTime = Date.now();
        }

        cursor = chunk.header.nextCursor;
      } while (cursor);

      expect(stats.receivedRecords).toBe(totalRecords);
      expect(stats.receivedBytes).toBe(totalRecords * RECORD_BYTES);
      expect(stats.uniqueCIDs.size).toBe(totalRecords);
      expect(deliveredRecords).toBe(totalRecords);
      expect(manager.getSubscription(sub.id)?.messageCount).toBe(totalRecords);

      console.log('');
      console.log('='.repeat(60));
      console.log('Test Complete');
      console.log(`Records received: ${stats.receivedRecords.toLocaleString()}`);
      console.log(`Bytes received: ${formatBytes(stats.receivedBytes)}`);
      console.log(`Unique CIDs: ${stats.uniqueCIDs.size.toLocaleString()}`);
      console.log('='.repeat(60));
    },
    STREAM_TEST_TIMEOUT_MS
  );

  it('should handle backpressure during high-volume reception', () => {
    const RATE_LIMIT = 1_000; // messages per minute
    const PRODUCED_PER_WINDOW = 5_000;
    const WINDOWS = 2;
    const QUEUE_CAPACITY = RATE_LIMIT; // slow consumer's bounded queue

    console.log('');
    console.log('Testing backpressure handling...');
    console.log(
      `Producing ${(PRODUCED_PER_WINDOW * WINDOWS).toLocaleString()} messages against a ` +
        `${RATE_LIMIT.toLocaleString()}/minute rate limit`
    );

    // Fake timers make the rate-limit window deterministic.
    vi.useFakeTimers();
    try {
      const manager = new SubscriptionManager();
      const sub = manager.createSubscription({
        dataTypes: ['OMM'],
        sourcePeers: ['all'],
        encrypted: false,
        streaming: true,
        rateLimit: RATE_LIMIT,
      });

      const queue: unknown[] = [];
      let delivered = 0;
      let rateLimited = 0;
      let maxQueueDepth = 0;

      manager.addEventListener(sub.id, (event) => {
        if (event.type === 'message') {
          delivered++;
          // Slow consumer: messages pile up in a bounded queue until drained.
          queue.push(event.data);
          maxQueueDepth = Math.max(maxQueueDepth, queue.length);
        } else if (event.type === 'rateLimit') {
          rateLimited++;
        }
      });

      for (let windowIndex = 0; windowIndex < WINDOWS; windowIndex++) {
        // Fast producer: floods the subscription far beyond the rate limit.
        for (let i = 0; i < PRODUCED_PER_WINDOW; i++) {
          manager.processMessage('OMM.fbs', { NORAD_CAT_ID: i, WINDOW: windowIndex }, 'loopback-producer');
        }

        // The producer is throttled: only RATE_LIMIT messages reach the
        // consumer this window, the overflow is shed as rateLimit events.
        expect(delivered).toBe(RATE_LIMIT * (windowIndex + 1));
        expect(queue.length).toBeLessThanOrEqual(QUEUE_CAPACITY);

        // Slow consumer drains its queue, then the rate window rolls over.
        queue.length = 0;
        vi.advanceTimersByTime(61_000);
      }

      expect(delivered).toBe(RATE_LIMIT * WINDOWS);
      expect(rateLimited).toBe((PRODUCED_PER_WINDOW - RATE_LIMIT) * WINDOWS);
      // The consumer-side queue never grew beyond its bound, so memory use
      // stays constant no matter how fast the producer pushes.
      expect(maxQueueDepth).toBeLessThanOrEqual(QUEUE_CAPACITY);
      expect(manager.getSubscription(sub.id)?.messageCount).toBe(RATE_LIMIT * WINDOWS);

      console.log(
        `Delivered ${delivered.toLocaleString()} | rate-limited ${rateLimited.toLocaleString()} | ` +
          `max queue depth ${maxQueueDepth.toLocaleString()}`
      );
    } finally {
      vi.useRealTimers();
    }
  });

  it('should correctly reassemble chunked messages', async () => {
    // This test doesn't require a running node - tests chunking logic
    const CHUNK_SIZE = 256 * 1024; // 256KB chunks
    const totalSize = 1024 * 1024; // 1MB test message

    // Create original data
    const originalData = new Uint8Array(totalSize);
    for (let i = 0; i < totalSize; i++) {
      originalData[i] = i % 256;
    }

    // Split into chunks (simulating network transfer)
    const chunks: Uint8Array[] = [];
    for (let offset = 0; offset < totalSize; offset += CHUNK_SIZE) {
      const end = Math.min(offset + CHUNK_SIZE, totalSize);
      chunks.push(originalData.slice(offset, end));
    }

    console.log(`Split ${formatBytes(totalSize)} into ${chunks.length} chunks`);

    // Reassemble chunks
    const reassembled = new Uint8Array(totalSize);
    let writeOffset = 0;
    for (const chunk of chunks) {
      reassembled.set(chunk, writeOffset);
      writeOffset += chunk.length;
    }

    // Verify integrity
    expect(reassembled.length).toBe(originalData.length);

    let mismatchCount = 0;
    for (let i = 0; i < totalSize; i++) {
      if (reassembled[i] !== originalData[i]) {
        mismatchCount++;
      }
    }

    expect(mismatchCount).toBe(0);
    console.log('Chunk reassembly verified successfully');
  });

  it('should track CID uniqueness efficiently', async () => {
    // Test that we can efficiently track millions of unique CIDs
    const cidSet = new Set<string>();
    const numCIDs = 1_000_000;

    console.log(`Testing CID tracking with ${numCIDs.toLocaleString()} CIDs...`);
    const startTime = Date.now();

    for (let i = 0; i < numCIDs; i++) {
      // Generate deterministic CID-like string
      const cid = `bafybei${i.toString(16).padStart(40, '0')}`;
      cidSet.add(cid);
    }

    const elapsed = Date.now() - startTime;
    console.log(`Added ${cidSet.size.toLocaleString()} unique CIDs in ${elapsed}ms`);

    expect(cidSet.size).toBe(numCIDs);

    // Test lookup performance
    const lookupStart = Date.now();
    let found = 0;
    for (let i = 0; i < numCIDs; i += 100) {
      const cid = `bafybei${i.toString(16).padStart(40, '0')}`;
      if (cidSet.has(cid)) found++;
    }
    const lookupElapsed = Date.now() - lookupStart;

    console.log(`Performed ${(numCIDs / 100).toLocaleString()} lookups in ${lookupElapsed}ms`);
    expect(found).toBe(numCIDs / 100);
  });

  it('should handle memory efficiently during long-running streams', async () => {
    // Test that we don't accumulate memory during streaming
    // This is a simplified test - in production we'd use actual memory profiling

    const iterations = 100_000;
    const records: { cid: string; size: number }[] = [];

    console.log(`Testing memory with ${iterations.toLocaleString()} records...`);

    // Simulate streaming records and processing them
    for (let i = 0; i < iterations; i++) {
      // Create record (simulates receiving from network)
      const record = {
        cid: `bafybei${i.toString(16).padStart(40, '0')}`,
        size: 256 + (i % 100),
      };

      // Process and discard (don't accumulate in array for real streaming)
      // In this test we do keep them to verify, but a real implementation
      // would process and discard or use a rolling buffer

      if (records.length < 1000) {
        // Only keep first 1000 for verification
        records.push(record);
      }

      // Periodically log to show progress
      if (i > 0 && i % 25000 === 0) {
        console.log(`Processed ${i.toLocaleString()} records`);
      }
    }

    expect(records.length).toBe(1000); // Verify we only kept 1000
    console.log('Memory efficiency test passed');
  });
});

describe('Stress Test Utilities', () => {
  it('should format bytes correctly', () => {
    expect(formatBytes(500)).toBe('500 bytes');
    expect(formatBytes(1024)).toBe('1.00 KB');
    expect(formatBytes(1024 * 1024)).toBe('1.00 MB');
    expect(formatBytes(1024 * 1024 * 1024)).toBe('1.00 GB');
    expect(formatBytes(10.5 * 1024 * 1024 * 1024)).toBe('10.50 GB');
  });
});

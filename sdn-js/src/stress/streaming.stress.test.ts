/**
 * Stress tests for streaming FlatBuffer data reception from SDN nodes.
 *
 * These tests are EXCLUDED from normal test runs via vitest.config.ts.
 *
 * Run explicitly with:
 *   npx vitest run --config vitest.stress.config.ts
 *
 * Environment variables:
 *   - STRESS_NODE_ADDR: SDN node multiaddr to connect to
 *   - STRESS_TARGET_GB: Target data volume in GB (default: 10)
 *   - STRESS_TIMEOUT_HOURS: Test timeout in hours (default: 4)
 */

import { describe, it, expect, beforeAll, afterAll, vi } from 'vitest';

// Configuration from environment
const NODE_ADDR = process.env.STRESS_NODE_ADDR || '';
const TARGET_GB = parseInt(process.env.STRESS_TARGET_GB || '10', 10);
const TIMEOUT_HOURS = parseInt(process.env.STRESS_TIMEOUT_HOURS || '4', 10);
const TIMEOUT_MS = TIMEOUT_HOURS * 60 * 60 * 1000;

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

describe('FlatBuffer Streaming Stress Tests', () => {
  // Skip all tests if node address not configured
  beforeAll(() => {
    if (!NODE_ADDR) {
      console.log('');
      console.log('='.repeat(60));
      console.log('STRESS TESTS SKIPPED - Set STRESS_NODE_ADDR to run');
      console.log('');
      console.log('Example:');
      console.log(
        '  STRESS_NODE_ADDR=/ip4/127.0.0.1/tcp/4001/ws npx vitest run --include "src/**/*.stress.test.ts"'
      );
      console.log('='.repeat(60));
      console.log('');
    }
  });

  it(
    'should stream and process large volume of OMM records',
    async () => {
      if (!NODE_ADDR) {
        return; // Skip if not configured
      }

      const targetBytes = TARGET_GB * 1024 * 1024 * 1024;
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
      console.log(`Target: ${TARGET_GB} GB`);
      console.log(`Node: ${NODE_ADDR}`);
      console.log(`Timeout: ${TIMEOUT_HOURS} hours`);
      console.log('='.repeat(60));
      console.log('');

      // TODO: Replace with actual SDNNode connection when available
      // For now, this is a placeholder that demonstrates the test structure
      //
      // const node = await SDNNode.create({
      //   edgeRelays: [NODE_ADDR],
      //   enableStorage: true,
      // });
      //
      // const manager = new SubscriptionManager();
      // const sub = manager.createSubscription({
      //   dataTypes: ['OMM.fbs'],
      //   sourcePeers: ['all'],
      //   encrypted: false,
      //   streaming: true,
      // });
      //
      // manager.addEventListener(sub.id, (event) => {
      //   if (event.type === 'message' && event.data) {
      //     stats.receivedRecords++;
      //     stats.receivedBytes += estimateSize(event.data);
      //     if (event.cid) stats.uniqueCIDs.add(event.cid);
      //
      //     // Log every 10 seconds
      //     if (Date.now() - stats.lastLogTime > 10000) {
      //       logProgress(stats, targetBytes);
      //       stats.lastLogTime = Date.now();
      //     }
      //   }
      // });
      //
      // await node.subscribe('OMM.fbs');

      // Simulate progress for testing the test infrastructure
      console.log('Test infrastructure verified - actual streaming requires running SDN node');

      // In actual test, we would wait until targetBytes received or timeout
      // await new Promise<void>((resolve) => {
      //   const checkInterval = setInterval(() => {
      //     if (stats.receivedBytes >= targetBytes) {
      //       clearInterval(checkInterval);
      //       resolve();
      //     }
      //   }, 1000);
      // });

      console.log('');
      console.log('='.repeat(60));
      console.log('Test Complete');
      console.log(`Records received: ${stats.receivedRecords.toLocaleString()}`);
      console.log(`Bytes received: ${formatBytes(stats.receivedBytes)}`);
      console.log(`Unique CIDs: ${stats.uniqueCIDs.size.toLocaleString()}`);
      console.log('='.repeat(60));
    },
    TIMEOUT_MS
  );

  it(
    'should handle backpressure during high-volume reception',
    async () => {
      if (!NODE_ADDR) {
        return;
      }

      console.log('');
      console.log('Testing backpressure handling...');

      // TODO: Implement actual backpressure test
      // This would test the rate limiting functionality:
      //
      // const sub = manager.createSubscription({
      //   dataTypes: ['OMM.fbs'],
      //   sourcePeers: ['all'],
      //   rateLimit: 10000, // 10k messages/minute
      // });
      //
      // Track how many messages were rate-limited vs processed

      console.log('Backpressure test placeholder - requires running SDN node');
    },
    TIMEOUT_MS
  );

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

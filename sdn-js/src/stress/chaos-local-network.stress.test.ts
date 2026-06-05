import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';

import { describe, expect, it } from 'vitest';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const packageRoot = path.resolve(__dirname, '../..');
const scriptPath = path.join(packageRoot, 'scripts/chaos-local-network.mjs');

describe('local virtual SDN chaos network', () => {
  it('converges every consumer after deterministic drops, corruption, partitions, and restarts', () => {
    const tempDir = mkdtempSync(path.join(tmpdir(), 'sdn-chaos-test-'));
    try {
      const checkpointPath = path.join(tempDir, 'checkpoint.json');
      const result = spawnSync(process.execPath, [
        scriptPath,
        '--json',
        '--shards', '64',
        '--shard-bytes', '4096',
        '--rows-per-shard', '250',
        '--consumers', '4',
        '--concurrency', '8',
        '--bandwidth-mbps', '2000',
        '--drop-every', '11',
        '--corrupt-every', '17',
        '--partition-every', '19',
        '--restart-every', '23',
        '--checkpoint-file', checkpointPath,
      ], {
        cwd: packageRoot,
        encoding: 'utf8',
        maxBuffer: 20 * 1024 * 1024,
      });

      expect(result.stderr).toBe('');
      expect(result.status).toBe(0);
      const report = JSON.parse(result.stdout) as {
        summary: {
          allVerified: boolean;
          completedConsumers: number;
          totalConsumers: number;
          totalRows: number;
          expectedRowsPerConsumer: number;
        };
        transport: {
          protocol: string;
          remoteHttpFallback: boolean;
          sshFallback: boolean;
        };
        manifest: {
          feedHead: string;
          segmentCount: number;
          rowCount: number;
          byteCount: number;
        };
        chaos: {
          droppedRequests: number;
          corruptedResponses: number;
          partitionFailures: number;
          restartEvents: number;
          retryCount: number;
          verificationFailures: number;
        };
        replication: {
          downloadedBytes: number;
          duplicateBytes: number;
          providerBytes: number;
          peerBytes: number;
          persistedCheckpoints: number;
          wireSpeedTarget: number;
          targetMet: boolean;
        };
        timingMs: {
          discovery: number;
          grantNegotiation: number;
          pnmDpmVerification: number;
          transfer: number;
          decrypt: number;
          hashVerification: number;
          durableImport: number;
        };
      };

      expect(report.summary).toMatchObject({
        allVerified: true,
        completedConsumers: 4,
        totalConsumers: 4,
        totalRows: 64_000,
        expectedRowsPerConsumer: 16_000,
      });
      expect(report.transport).toMatchObject({
        protocol: '/space-data-network/flatsql-sync/1.0.0',
        remoteHttpFallback: false,
        sshFallback: false,
      });
      expect(report.manifest.feedHead).toMatch(/^[a-f0-9]{64}$/);
      expect(report.manifest).toMatchObject({
        segmentCount: 64,
        rowCount: 16_000,
      });
      expect(report.chaos.droppedRequests).toBeGreaterThan(0);
      expect(report.chaos.corruptedResponses).toBeGreaterThan(0);
      expect(report.chaos.partitionFailures).toBeGreaterThan(0);
      expect(report.chaos.restartEvents).toBeGreaterThan(0);
      expect(report.chaos.retryCount).toBeGreaterThan(0);
      expect(report.chaos.verificationFailures).toBeGreaterThan(0);
      expect(report.replication.duplicateBytes).toBe(0);
      expect(report.replication.persistedCheckpoints).toBeGreaterThan(0);
      expect(report.replication.providerBytes + report.replication.peerBytes).toBeGreaterThan(0);
      expect(report.replication.bytesPerSecond).toBeLessThanOrEqual(report.replication.measuredWireSpeedBytesPerSecond);
      expect(report.replication.wireSpeedTarget).toBe(0.9);
      expect(typeof report.replication.targetMet).toBe('boolean');
      for (const key of ['discovery', 'grantNegotiation', 'pnmDpmVerification', 'transfer', 'decrypt', 'hashVerification', 'durableImport'] as const) {
        expect(Number.isFinite(report.timingMs[key])).toBe(true);
      }
      expect(report.timingMs.transfer).toBeGreaterThan(0);
      expect(report.timingMs.hashVerification).toBeGreaterThan(0);
    } finally {
      rmSync(tempDir, { recursive: true, force: true });
    }
  });

  it('resumes a completed checkpoint without redownloading verified feed-head shards', () => {
    const tempDir = mkdtempSync(path.join(tmpdir(), 'sdn-chaos-resume-test-'));
    try {
      const checkpointPath = path.join(tempDir, 'checkpoint.json');
      const args = [
        scriptPath,
        '--json',
        '--shards', '16',
        '--shard-bytes', '4096',
        '--rows-per-shard', '100',
        '--consumers', '3',
        '--concurrency', '6',
        '--bandwidth-mbps', '2000',
        '--drop-every', '7',
        '--corrupt-every', '11',
        '--partition-every', '13',
        '--restart-every', '17',
        '--checkpoint-file', checkpointPath,
      ];
      const first = spawnSync(process.execPath, args, {
        cwd: packageRoot,
        encoding: 'utf8',
        maxBuffer: 20 * 1024 * 1024,
      });

      expect(first.stderr).toBe('');
      expect(first.status).toBe(0);
      const firstReport = JSON.parse(first.stdout) as {
        manifest: { feedHead: string };
        summary: { allVerified: boolean; totalRows: number };
        replication: { downloadedBytes: number };
      };
      expect(firstReport.summary).toMatchObject({
        allVerified: true,
        totalRows: 4_800,
      });
      expect(firstReport.replication.downloadedBytes).toBeGreaterThan(0);

      const second = spawnSync(process.execPath, args, {
        cwd: packageRoot,
        encoding: 'utf8',
        maxBuffer: 20 * 1024 * 1024,
      });

      expect(second.stderr).toBe('');
      expect(second.status).toBe(0);
      const secondReport = JSON.parse(second.stdout) as {
        manifest: { feedHead: string };
        summary: { allVerified: boolean; totalRows: number };
        replication: {
          downloadedBytes: number;
          providerBytes: number;
          peerBytes: number;
          duplicateBytes: number;
          persistedCheckpoints: number;
        };
      };
      expect(secondReport.manifest.feedHead).toBe(firstReport.manifest.feedHead);
      expect(secondReport.summary).toMatchObject({
        allVerified: true,
        totalRows: 4_800,
      });
      expect(secondReport.replication).toMatchObject({
        downloadedBytes: 0,
        providerBytes: 0,
        peerBytes: 0,
        duplicateBytes: 0,
        persistedCheckpoints: 0,
      });
    } finally {
      rmSync(tempDir, { recursive: true, force: true });
    }
  });

  it('reuses verified checkpoint shards when the provider feed advances', () => {
    const tempDir = mkdtempSync(path.join(tmpdir(), 'sdn-chaos-advance-test-'));
    try {
      const checkpointPath = path.join(tempDir, 'checkpoint.json');
      const baseArgs = [
        scriptPath,
        '--json',
        '--shard-bytes', '4096',
        '--rows-per-shard', '100',
        '--consumers', '2',
        '--concurrency', '4',
        '--bandwidth-mbps', '2000',
        '--checkpoint-file', checkpointPath,
      ];
      const first = spawnSync(process.execPath, [...baseArgs, '--shards', '8'], {
        cwd: packageRoot,
        encoding: 'utf8',
        maxBuffer: 20 * 1024 * 1024,
      });

      expect(first.stderr).toBe('');
      expect(first.status).toBe(0);
      const firstReport = JSON.parse(first.stdout) as {
        manifest: { feedHead: string };
        summary: { allVerified: boolean; totalRows: number };
        replication: {
          newlyPinnedShards: number;
          reusedVerifiedShards: number;
        };
      };
      expect(firstReport.summary).toMatchObject({
        allVerified: true,
        totalRows: 1_600,
      });
      expect(firstReport.replication).toMatchObject({
        newlyPinnedShards: 16,
        reusedVerifiedShards: 0,
      });

      const second = spawnSync(process.execPath, [...baseArgs, '--shards', '12'], {
        cwd: packageRoot,
        encoding: 'utf8',
        maxBuffer: 20 * 1024 * 1024,
      });

      expect(second.stderr).toBe('');
      expect(second.status).toBe(0);
      const secondReport = JSON.parse(second.stdout) as {
        manifest: { feedHead: string };
        summary: { allVerified: boolean; totalRows: number };
        replication: {
          downloadedBytes: number;
          newlyPinnedShards: number;
          reusedVerifiedShards: number;
        };
      };
      expect(secondReport.manifest.feedHead).not.toBe(firstReport.manifest.feedHead);
      expect(secondReport.summary).toMatchObject({
        allVerified: true,
        totalRows: 2_400,
      });
      expect(secondReport.replication).toMatchObject({
        newlyPinnedShards: 8,
        reusedVerifiedShards: 16,
      });
      expect(secondReport.replication.downloadedBytes).toBeGreaterThan(0);
    } finally {
      rmSync(tempDir, { recursive: true, force: true });
    }
  });
});

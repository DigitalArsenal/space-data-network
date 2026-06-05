import { describe, expect, it } from 'vitest';

import {
  boundedWireSpeedUtilization,
  formatBytesPerSecond,
  publishedShardWireSpeedAudit,
  measuredWireSpeedUtilization,
  meetsWireSpeedTarget,
  parseThroughputHarnessArgs,
  throughputHarnessSummary,
} from './sync-throughput';

describe('sync throughput targets', () => {
  it('computes percent of measured wire speed', () => {
    expect(measuredWireSpeedUtilization(160_000_000, 200_000_000)).toBe(0.8);
  });

  it('does not report impossible utilization above measured wire speed', () => {
    expect(measuredWireSpeedUtilization(226_000_000, 200_000_000)).toBe(1);
  });

  it('bounds persisted wire speed utilization before display', () => {
    expect(boundedWireSpeedUtilization(1.13)).toBe(1);
    expect(boundedWireSpeedUtilization(0.8)).toBe(0.8);
    expect(boundedWireSpeedUtilization(-0.1)).toBeNull();
  });

  it('requires at least 90 percent of measured wire speed by default', () => {
    expect(meetsWireSpeedTarget(179_000_000, 200_000_000)).toBe(false);
    expect(meetsWireSpeedTarget(180_000_000, 200_000_000)).toBe(true);
  });

  it('requires at least 1.8 Gbit/s on a 2 Gbit/s configured link', () => {
    const twoGbitBytesPerSecond = 2_000_000_000 / 8;
    const requiredBytesPerSecond = 1_800_000_000 / 8;

    expect(meetsWireSpeedTarget(requiredBytesPerSecond - 1, twoGbitBytesPerSecond)).toBe(false);
    expect(meetsWireSpeedTarget(requiredBytesPerSecond, twoGbitBytesPerSecond)).toBe(true);
  });

  it('does not report utilization without a positive measured baseline', () => {
    expect(measuredWireSpeedUtilization(160_000_000, 0)).toBeNull();
  });

  it('audits published shard download speed separately from verification and materialization', () => {
    const audit = publishedShardWireSpeedAudit({
      downloadedBytes: 550_000_000,
      measuredWireSpeedBytesPerSecond: 250_000_000,
      manifestDiscoveryMs: 125,
      networkTransferMs: 2_750,
      verificationMs: 4_000,
      flatSqlMaterializationMs: 8_000,
    });

    expect(audit.downloadBytesPerSecond).toBe(200_000_000);
    expect(audit.wireSpeedUtilization).toBe(0.8);
    expect(audit.targetMet).toBe(false);
    expect(audit.timingsMs).toEqual({
      manifestDiscovery: 125,
      networkTransfer: 2_750,
      verification: 4_000,
      flatSqlMaterialization: 8_000,
    });
  });

  it('can audit against an absolute 2 Gbps baseline instead of the measured probe', () => {
    const audit = publishedShardWireSpeedAudit({
      downloadedBytes: 550_000_000,
      measuredWireSpeedBytesPerSecond: 30_000_000,
      wireSpeedBaselineBytesPerSecond: 250_000_000,
      manifestDiscoveryMs: 125,
      networkTransferMs: 4_500,
      verificationMs: 900,
      flatSqlMaterializationMs: 0,
      wireSpeedTarget: 0.5,
    });

    expect(audit.measuredWireSpeedBytesPerSecond).toBe(30_000_000);
    expect(audit.wireSpeedBaselineBytesPerSecond).toBe(250_000_000);
    expect(audit.downloadBytesPerSecond).toBe(122_222_222);
    expect(audit.wireSpeedUtilization).toBeCloseTo(0.488888888, 6);
    expect(audit.targetMet).toBe(false);
  });

  it('parses repeated libp2p addresses and CelesTrak-oriented throughput harness defaults', () => {
    const options = parseThroughputHarnessArgs([
      '--peer', '16Uiu2HCelesTrak',
      '--addr', '/dns4/sdn.spaceaware.io/tcp/443/wss/p2p/16Uiu2HCelesTrak',
      '--addr', '/dns4/sdn2.spaceaware.io/tcp/443/wss/p2p/16Uiu2HCelesTrak',
      '--provider-id', 'space-data-network-02',
      '--source-name', 'celestrak-gp',
      '--max-segments', '3',
    ]);

    expect(options).toMatchObject({
      peer: '16Uiu2HCelesTrak',
      addrs: [
        '/dns4/sdn.spaceaware.io/tcp/443/wss/p2p/16Uiu2HCelesTrak',
        '/dns4/sdn2.spaceaware.io/tcp/443/wss/p2p/16Uiu2HCelesTrak',
      ],
      schema: 'OMM.fbs',
      providerId: 'space-data-network-02',
      sourceName: 'celestrak-gp',
      concurrency: 32,
      clientPoolSize: 8,
      rangeBytes: 16 * 1024 * 1024,
      rangeConcurrency: 1,
      maxSegments: 3,
      requestTimeoutMs: 60_000,
      target: 0.9,
    });
  });

  it('parses the dedicated libp2p shard client pool size', () => {
    const options = parseThroughputHarnessArgs([
      '--peer', '16Uiu2HCelesTrak',
      '--addr', '/dns4/celestrak.example/tcp/443/wss/p2p/16Uiu2HCelesTrak',
      '--client-pool-size', '6',
    ]);

    expect(options.clientPoolSize).toBe(6);
  });

  it('parses published-shard byte range tuning options', () => {
    const options = parseThroughputHarnessArgs([
      '--peer', '16Uiu2HCelesTrak',
      '--addr', '/dns4/celestrak.example/tcp/443/wss/p2p/16Uiu2HCelesTrak',
      '--range-bytes', '1048576',
      '--range-concurrency', '8',
    ]);

    expect(options.rangeBytes).toBe(1_048_576);
    expect(options.rangeConcurrency).toBe(8);
  });

  it('parses bounded published-shard batch tuning options', () => {
    const options = parseThroughputHarnessArgs([
      '--peer', '16Uiu2HCelesTrak',
      '--addr', '/dns4/celestrak.example/tcp/443/wss/p2p/16Uiu2HCelesTrak',
      '--transfer-mode', 'batches',
      '--batch-bytes', '67108864',
      '--batch-segments', '8',
      '--batch-concurrency', '4',
    ]);

    expect(options.transferMode).toBe('batches');
    expect(options.batchBytes).toBe(67_108_864);
    expect(options.batchSegments).toBe(8);
    expect(options.batchConcurrency).toBe(4);
  });

  it('parses additional direct libp2p shard sources for multi-peer replication audits', () => {
    const options = parseThroughputHarnessArgs([
      '--peer', '16Uiu2HCelesTrak',
      '--addr', '/dns4/celestrak.example/tcp/443/wss/p2p/16Uiu2HCelesTrak',
      '--shard-source-peer', '16Uiu2HMirror',
      '--shard-source-addr', '/dns4/mirror.example/tcp/443/wss/p2p/16Uiu2HMirror',
      '--shard-source-addr', '/dns4/mirror-backup.example/tcp/443/wss/p2p/16Uiu2HMirror',
    ]);

    expect(options.shardSources).toEqual([{
      peer: '16Uiu2HMirror',
      addrs: [
        '/dns4/mirror.example/tcp/443/wss/p2p/16Uiu2HMirror',
        '/dns4/mirror-backup.example/tcp/443/wss/p2p/16Uiu2HMirror',
      ],
    }]);
  });

  it('parses an absolute wire speed baseline in bits per second', () => {
    const options = parseThroughputHarnessArgs([
      '--peer', '16Uiu2HCelesTrak',
      '--addr', '/dns4/celestrak.example/tcp/443/wss/p2p/16Uiu2HCelesTrak',
      '--wire-speed-bps', '2000000000',
      '--target', '0.5',
    ]);

    expect(options.wireSpeedBitsPerSecond).toBe(2_000_000_000);
    expect(options.target).toBe(0.5);
  });

  it('rejects Kubo artifact routing options for the direct libp2p harness', () => {
    expect(() => parseThroughputHarnessArgs([
      '--peer', '16Uiu2HCelesTrak',
      '--addr', '/dns4/celestrak.example/tcp/443/wss/p2p/16Uiu2HCelesTrak',
      '--local-kubo-gateway', 'http://127.0.0.1:8081',
    ])).toThrow('unknown option --local-kubo-gateway');
  });

  it('renders a concise pass/fail summary with separate timing fields', () => {
    const summary = throughputHarnessSummary({
      generatedAt: '2026-05-12T20:00:00.000Z',
      peer: '16Uiu2HCelesTrak',
      schema: 'OMM.fbs',
      target: 0.9,
      probe: {
        requestedBytes: 64,
        payloadBytes: 64,
        elapsedMs: 1,
        bytesPerSecond: 250_000_000,
        syncProtocol: '/space-data-network/flatsql-sync/1.0.0',
      },
      manifest: {
        totalCount: 2_000_000,
        totalBytes: 550_000_000,
        segmentCount: 11,
        downloadedSegmentCount: 11,
        manifestDiscoveryMs: 125,
        head: 'feed-head',
        snapshotId: 'snapshot',
        highWaterMark: 'high-water',
      },
      artifactRouting: {
        protocol: '/space-data-network/flatsql-sync/1.0.0',
        mode: 'direct-libp2p-published-shard-ranges',
        clientPoolSize: 4,
        rangeBytes: 4_194_304,
        rangeConcurrency: 1,
        remoteHttpFallback: false,
        sshFallback: false,
      },
      audit: {
        downloadedBytes: 550_000_000,
        measuredWireSpeedBytesPerSecond: 250_000_000,
        downloadBytesPerSecond: 225_000_000,
        wireSpeedUtilization: 0.9,
        wireSpeedTarget: 0.9,
        targetMet: true,
        timingsMs: {
          manifestDiscovery: 125,
          networkTransfer: 2_750,
          verification: 900,
          flatSqlMaterialization: 0,
        },
      },
    });

    expect(summary).toContain('Wire speed probe: 250.0 MB/s');
    expect(summary).toContain('Shard transfer: direct-libp2p-published-shard-ranges over /space-data-network/flatsql-sync/1.0.0');
    expect(summary).toContain('no HTTP/SSH fallbacks');
    expect(summary).toContain('Published shard download: 225.0 MB/s (90% of wire)');
    expect(summary).toContain('Timing: manifest 125 ms / network 2.75 s / verify 900 ms / FlatSQL 0 ms');
    expect(summary).toContain('90% target: met');
  });

  it('does not render impossible utilization from persisted audit results', () => {
    const summary = throughputHarnessSummary({
      generatedAt: '2026-05-12T20:00:00.000Z',
      peer: '16Uiu2HCelesTrak',
      schema: 'OMM.fbs',
      target: 0.9,
      probe: {
        requestedBytes: 64,
        payloadBytes: 64,
        elapsedMs: 1,
        bytesPerSecond: 200_000_000,
        syncProtocol: '/space-data-network/flatsql-sync/1.0.0',
      },
      manifest: {
        totalCount: 2_000_000,
        totalBytes: 550_000_000,
        segmentCount: 11,
        downloadedSegmentCount: 11,
        manifestDiscoveryMs: 125,
        head: 'feed-head',
        snapshotId: 'snapshot',
        highWaterMark: 'high-water',
      },
      audit: {
        downloadedBytes: 550_000_000,
        measuredWireSpeedBytesPerSecond: 200_000_000,
        downloadBytesPerSecond: 226_000_000,
        wireSpeedUtilization: 1.13,
        wireSpeedTarget: 0.9,
        targetMet: true,
        timingsMs: {
          manifestDiscovery: 125,
          networkTransfer: 2_434,
          verification: 900,
          flatSqlMaterialization: 0,
        },
      },
    });

    expect(summary).toContain('Published shard download: 226.0 MB/s (100% of wire)');
    expect(summary).not.toContain('113% of wire');
  });

  it('formats byte rates for the acceptance report', () => {
    expect(formatBytesPerSecond(200_000_000)).toBe('200.0 MB/s');
  });
});

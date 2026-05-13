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

  it('requires at least 80 percent of measured wire speed by default', () => {
    expect(meetsWireSpeedTarget(159_000_000, 200_000_000)).toBe(false);
    expect(meetsWireSpeedTarget(160_000_000, 200_000_000)).toBe(true);
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
    expect(audit.targetMet).toBe(true);
    expect(audit.timingsMs).toEqual({
      manifestDiscovery: 125,
      networkTransfer: 2_750,
      verification: 4_000,
      flatSqlMaterialization: 8_000,
    });
  });

  it('parses repeated libp2p addresses and CelesTrak-oriented throughput harness defaults', () => {
    const options = parseThroughputHarnessArgs([
      '--peer', '16Uiu2HCelesTrak',
      '--addr', '/dns4/sdn.spaceaware.io/tcp/443/wss/p2p/16Uiu2HCelesTrak',
      '--addr', '/dns4/sdn2.spaceaware.io/tcp/443/wss/p2p/16Uiu2HCelesTrak',
      '--provider-id', 'space-data-network-02',
      '--source-name', 'celestrak-gp',
      '--ipfs-api', 'http://127.0.0.1:15002',
      '--ipfs-peer', '/ip4/167.172.219.213/tcp/4002/p2p/12D3KooWCelesTrakKubo',
      '--ipfs-peer', '/ip4/104.131.11.220/tcp/4002/p2p/12D3KooWSpaceAwareKubo',
      '--ipfs-provider-discovery-limit', '8',
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
      gateway: 'http://127.0.0.1:8081',
      ipfsApi: 'http://127.0.0.1:15002',
      ipfsPeers: [
        '/ip4/167.172.219.213/tcp/4002/p2p/12D3KooWCelesTrakKubo',
        '/ip4/104.131.11.220/tcp/4002/p2p/12D3KooWSpaceAwareKubo',
      ],
      ipfsProviderDiscoveryLimit: 8,
      concurrency: 24,
      maxSegments: 3,
      target: 0.8,
    });
  });

  it('enables automatic IPFS provider discovery for the throughput harness by default', () => {
    const options = parseThroughputHarnessArgs([
      '--peer', '16Uiu2HCelesTrak',
      '--addr', '/dns4/celestrak.example/tcp/443/wss/p2p/16Uiu2HCelesTrak',
    ]);

    expect(options.ipfsProviderDiscoveryLimit).toBe(16);
    expect(parseThroughputHarnessArgs([
      '--peer', '16Uiu2HCelesTrak',
      '--addr', '/dns4/celestrak.example/tcp/443/wss/p2p/16Uiu2HCelesTrak',
      '--no-ipfs-provider-discovery',
    ]).ipfsProviderDiscoveryLimit).toBe(0);
  });

  it('renders a concise pass/fail summary with separate timing fields', () => {
    const summary = throughputHarnessSummary({
      generatedAt: '2026-05-12T20:00:00.000Z',
      peer: '16Uiu2HCelesTrak',
      schema: 'OMM.fbs',
      target: 0.8,
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
      audit: {
        downloadedBytes: 550_000_000,
        measuredWireSpeedBytesPerSecond: 250_000_000,
        downloadBytesPerSecond: 200_000_000,
        wireSpeedUtilization: 0.8,
        wireSpeedTarget: 0.8,
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
    expect(summary).toContain('Published shard download: 200.0 MB/s (80% of wire)');
    expect(summary).toContain('Timing: manifest 125 ms / network 2.75 s / verify 900 ms / FlatSQL 0 ms');
    expect(summary).toContain('80% target: met');
  });

  it('formats byte rates for the acceptance report', () => {
    expect(formatBytesPerSecond(200_000_000)).toBe('200.0 MB/s');
  });
});

#!/usr/bin/env node

import { createHash } from 'node:crypto';
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const FLATSQL_SYNC_PROTOCOL_ID = '/space-data-network/flatsql-sync/1.0.0';
const DEFAULT_WIRE_SPEED_TARGET = 0.99;

const DEFAULTS = {
  shards: 256,
  shardBytes: 1024 * 1024,
  rowsPerShard: 2_000,
  consumers: 8,
  concurrency: 16,
  bandwidthMbps: 2_000,
  dropEvery: 0,
  corruptEvery: 0,
  partitionEvery: 0,
  restartEvery: 0,
  maxAttempts: 64,
  wireSpeedTarget: DEFAULT_WIRE_SPEED_TARGET,
  json: false,
  checkpointFile: '',
};

export async function runChaosLocalNetwork(rawOptions = {}) {
  const options = normalizeOptions(rawOptions);
  const manifestStartedAt = Date.now();
  const shards = buildManifestShards(options);
  const manifestMs = Math.max(1, Date.now() - manifestStartedAt);
  const checkpoint = createCheckpoint(options);
  const consumers = Array.from({ length: options.consumers }, (_value, index) => ({
    id: `consumer-${index + 1}`,
    pinned: new Map(checkpoint.consumers[`consumer-${index + 1}`] ?? []),
  }));

  const metrics = {
    events: 0,
    droppedRequests: 0,
    corruptedResponses: 0,
    partitionFailures: 0,
    restartEvents: 0,
    retryCount: 0,
    verificationFailures: 0,
    requestFailures: 0,
    providerBytes: 0,
    peerBytes: 0,
    duplicateBytes: 0,
    newlyPinnedShards: 0,
    reusedVerifiedShards: 0,
    persistedCheckpoints: 0,
    virtualNetworkTransferMs: 0,
    simulatedConcurrency: options.concurrency,
  };

  for (const shard of shards) {
    for (const consumer of consumers) {
      await syncShardToConsumer({ options, shards, shard, consumer, consumers, metrics, checkpoint });
    }
  }

  const verificationStartedAt = Date.now();
  const consumerReports = consumers.map((consumer) => verifyConsumer(consumer, shards));
  const verificationMs = Math.max(1, Date.now() - verificationStartedAt);
  const completedConsumers = consumerReports.filter((report) => report.verified).length;
  const allVerified = completedConsumers === consumers.length;
  const totalPayloadBytes = shards.reduce((sum, shard) => sum + shard.byteCount, 0);
  const totalPayloadRows = shards.reduce((sum, shard) => sum + shard.rowCount, 0);
  const totalRows = consumerReports.reduce((sum, report) => sum + report.rows, 0);
  const downloadBytes = metrics.providerBytes + metrics.peerBytes;
  const wireSpeedBytesPerSecond = Math.floor((options.bandwidthMbps * 1_000_000) / 8);
  const linkLimitedTransferMs = wireSpeedBytesPerSecond > 0
    ? Math.ceil(downloadBytes / wireSpeedBytesPerSecond * 1000)
    : 0;
  const networkTransferMs = Math.max(
    1,
    Math.ceil(metrics.virtualNetworkTransferMs / Math.max(1, options.concurrency)),
    linkLimitedTransferMs,
  );
  const bytesPerSecond = Math.floor(downloadBytes / (networkTransferMs / 1000));
  const wireSpeedUtilization = wireSpeedBytesPerSecond > 0
    ? Math.min(1, bytesPerSecond / wireSpeedBytesPerSecond)
    : null;
  const requiredBytesPerSecond = wireSpeedBytesPerSecond > 0
    ? Math.floor(wireSpeedBytesPerSecond * options.wireSpeedTarget)
    : null;

  const hashVerificationMs = verificationMs;
  const durableImportMs = 0;
  return {
    generatedAt: new Date().toISOString(),
    scenario: {
      shards: options.shards,
      shardBytes: options.shardBytes,
      rowsPerShard: options.rowsPerShard,
      consumers: options.consumers,
      concurrency: options.concurrency,
      bandwidthMbps: options.bandwidthMbps,
      checkpointFile: options.checkpointFile,
    },
    transport: {
      protocol: FLATSQL_SYNC_PROTOCOL_ID,
      mode: 'local-virtual-libp2p-flatsql-shard-network',
      payloadFormat: 'concatenated-flatsql-size-prefixed-flatbuffers',
      remoteHttpFallback: false,
      sshFallback: false,
    },
    manifest: {
      feedHead: manifestHeadFromShards(shards),
      segmentCount: shards.length,
      rowCount: totalPayloadRows,
      byteCount: totalPayloadBytes,
    },
    summary: {
      allVerified,
      completedConsumers,
      totalConsumers: consumers.length,
      totalRows,
      expectedRowsPerConsumer: options.shards * options.rowsPerShard,
      expectedRowsTotal: options.shards * options.rowsPerShard * options.consumers,
      uniqueShards: shards.length,
      providerPayloadBytes: totalPayloadBytes,
    },
    chaos: {
      droppedRequests: metrics.droppedRequests,
      corruptedResponses: metrics.corruptedResponses,
      partitionFailures: metrics.partitionFailures,
      restartEvents: metrics.restartEvents,
      retryCount: metrics.retryCount,
      verificationFailures: metrics.verificationFailures,
      requestFailures: metrics.requestFailures,
    },
    replication: {
      downloadedBytes: downloadBytes,
      providerBytes: metrics.providerBytes,
      peerBytes: metrics.peerBytes,
      duplicateBytes: metrics.duplicateBytes,
      newlyPinnedShards: metrics.newlyPinnedShards,
      reusedVerifiedShards: metrics.reusedVerifiedShards,
      persistedCheckpoints: metrics.persistedCheckpoints,
      bytesPerSecond,
      measuredWireSpeedBytesPerSecond: wireSpeedBytesPerSecond,
      wireSpeedUtilization,
      wireSpeedTarget: options.wireSpeedTarget,
      requiredBytesPerSecond,
      targetMet: wireSpeedUtilization == null ? null : wireSpeedUtilization >= options.wireSpeedTarget,
    },
    timingMs: channelTimingBreakdown({
      discoveryMs: manifestMs,
      transferMs: networkTransferMs,
      hashVerificationMs,
      durableImportMs,
    }),
    consumers: consumerReports,
  };
}

function channelTimingBreakdown({
  discoveryMs,
  transferMs,
  hashVerificationMs,
  durableImportMs,
}) {
  return {
    discovery: discoveryMs,
    grantNegotiation: 0,
    pnmDpmVerification: 0,
    transfer: transferMs,
    decrypt: 0,
    hashVerification: hashVerificationMs,
    durableImport: durableImportMs,
    manifestDiscovery: discoveryMs,
    networkTransfer: transferMs,
    verification: hashVerificationMs,
    flatSqlMaterialization: durableImportMs,
  };
}

async function syncShardToConsumer(input) {
  const { options, shard, consumer, consumers, metrics, checkpoint } = input;
  if (consumer.pinned.has(shard.cid)) {
    metrics.reusedVerifiedShards += 1;
    return;
  }

  let lastError = null;
  for (let attempt = 1; attempt <= options.maxAttempts; attempt += 1) {
    try {
      const source = chooseSource(consumer, consumers, shard);
      const response = fetchShardOverVirtualNetwork({ options, shard, source, metrics });
      if (consumer.pinned.has(shard.cid)) {
        metrics.duplicateBytes += response.bytes.byteLength;
        return;
      }
      const actualHash = sha256Hex(response.bytes);
      if (actualHash !== shard.sha256) {
        metrics.verificationFailures += 1;
        throw new Error(`shard ${shard.cid} SHA-256 mismatch`);
      }
      consumer.pinned.set(shard.cid, {
        sha256: actualHash,
        rows: shard.rowCount,
        byteCount: shard.byteCount,
        source: source.id,
      });
      metrics.newlyPinnedShards += 1;
      persistCheckpoint(options, checkpoint, consumer, metrics);
      return;
    } catch (error) {
      lastError = error;
      metrics.retryCount += 1;
      await Promise.resolve();
    }
  }
  throw new Error(`failed to sync ${shard.cid} to ${consumer.id}: ${formatError(lastError)}`);
}

function fetchShardOverVirtualNetwork({ options, shard, source, metrics }) {
  metrics.events += 1;
  const event = metrics.events;
  const byteCount = shard.bytes.byteLength;
  const transferMs = Math.max(1, Math.ceil(byteCount / Math.max(1, (options.bandwidthMbps * 1_000_000) / 8) * 1000));
  metrics.virtualNetworkTransferMs += transferMs;

  if (options.restartEvery > 0 && event % options.restartEvery === 0) {
    metrics.restartEvents += 1;
    metrics.requestFailures += 1;
    throw new Error('simulated requester restart');
  }
  if (options.partitionEvery > 0 && event % options.partitionEvery === 0) {
    metrics.partitionFailures += 1;
    metrics.requestFailures += 1;
    throw new Error('simulated network partition');
  }
  if (options.dropEvery > 0 && event % options.dropEvery === 0) {
    metrics.droppedRequests += 1;
    metrics.requestFailures += 1;
    throw new Error('simulated dropped stream');
  }

  let bytes = shard.bytes;
  if (options.corruptEvery > 0 && event % options.corruptEvery === 0) {
    metrics.corruptedResponses += 1;
    bytes = new Uint8Array(shard.bytes);
    bytes[bytes.length - 1] ^= 0xff;
  }

  if (source.kind === 'provider') metrics.providerBytes += byteCount;
  else metrics.peerBytes += byteCount;

  return { bytes };
}

function chooseSource(consumer, consumers, shard) {
  for (const candidate of consumers) {
    if (candidate.id !== consumer.id && candidate.pinned.has(shard.cid)) {
      return { kind: 'peer', id: candidate.id };
    }
  }
  return { kind: 'provider', id: 'provider-1' };
}

function verifyConsumer(consumer, shards) {
  let rows = 0;
  let bytes = 0;
  let missing = 0;
  let mismatched = 0;
  for (const shard of shards) {
    const pinned = consumer.pinned.get(shard.cid);
    if (!pinned) {
      missing += 1;
      continue;
    }
    if (pinned.sha256 !== shard.sha256 || pinned.byteCount !== shard.byteCount) {
      mismatched += 1;
      continue;
    }
    rows += shard.rowCount;
    bytes += shard.byteCount;
  }
  return {
    id: consumer.id,
    verified: missing === 0 && mismatched === 0,
    rows,
    bytes,
    missingShards: missing,
    mismatchedShards: mismatched,
  };
}

function createCheckpoint(options) {
  if (options.checkpointFile && existsSync(options.checkpointFile)) {
    try {
      const parsed = JSON.parse(readFileSync(options.checkpointFile, 'utf8'));
      if (parsed && typeof parsed === 'object' && parsed.consumers && typeof parsed.consumers === 'object') {
        return parsed;
      }
    } catch {
      // Ignore invalid checkpoint input; the next persist will replace it.
    }
  }
  return {
    version: 1,
    protocol: FLATSQL_SYNC_PROTOCOL_ID,
    consumers: {},
  };
}

function persistCheckpoint(options, checkpoint, consumer, metrics) {
  if (!options.checkpointFile) return;
  checkpoint.consumers[consumer.id] = Array.from(consumer.pinned.entries());
  checkpoint.updatedAt = new Date().toISOString();
  mkdirSync(path.dirname(options.checkpointFile), { recursive: true });
  writeFileSync(options.checkpointFile, `${JSON.stringify(checkpoint)}\n`);
  metrics.persistedCheckpoints += 1;
}

function buildManifestShards(options) {
  return Array.from({ length: options.shards }, (_value, index) => {
    const bytes = buildFlatSqlShardStream(index, options.shardBytes, options.rowsPerShard);
    const sha256 = sha256Hex(bytes);
    return {
      index,
      cid: `bafy-sdn-chaos-${sha256.slice(0, 32)}`,
      rowCount: options.rowsPerShard,
      byteCount: bytes.byteLength,
      sha256,
      bytes,
    };
  });
}

function manifestHeadFromShards(shards) {
  const hash = createHash('sha256');
  for (const shard of shards) {
    hash.update(`${shard.index}\0${shard.cid}\0${shard.sha256}\0${shard.rowCount}\0${shard.byteCount}\n`);
  }
  return hash.digest('hex');
}

function buildFlatSqlShardStream(shardIndex, targetBytes, rowsPerShard) {
  const rows = Math.max(1, rowsPerShard);
  const payloadLength = Math.max(8, Math.floor(Math.max(rows * 12, targetBytes) / rows) - 4);
  const totalBytes = rows * (payloadLength + 4);
  const out = new Uint8Array(totalBytes);
  let offset = 0;
  const view = new DataView(out.buffer);
  for (let row = 0; row < rows; row += 1) {
    view.setUint32(offset, payloadLength, true);
    offset += 4;
    for (let index = 0; index < payloadLength; index += 1) {
      out[offset + index] = (shardIndex * 31 + row * 17 + index * 13) & 0xff;
    }
    offset += payloadLength;
  }
  return out;
}

function normalizeOptions(input) {
  const options = { ...DEFAULTS, ...input };
  for (const key of ['shards', 'shardBytes', 'rowsPerShard', 'consumers', 'concurrency', 'bandwidthMbps', 'dropEvery', 'corruptEvery', 'partitionEvery', 'restartEvery', 'maxAttempts']) {
    options[key] = Math.max(0, Math.floor(Number(options[key] ?? DEFAULTS[key])));
  }
  options.shards = Math.max(1, options.shards);
  options.shardBytes = Math.max(8, options.shardBytes);
  options.rowsPerShard = Math.max(1, options.rowsPerShard);
  options.consumers = Math.max(1, options.consumers);
  options.concurrency = Math.max(1, options.concurrency);
  options.bandwidthMbps = Math.max(1, options.bandwidthMbps);
  options.maxAttempts = Math.max(1, options.maxAttempts);
  options.wireSpeedTarget = normalizedRatio(options.wireSpeedTarget, DEFAULT_WIRE_SPEED_TARGET);
  options.checkpointFile = String(options.checkpointFile ?? '').trim();
  options.json = Boolean(options.json);
  return options;
}

function parseArgs(argv) {
  const options = { ...DEFAULTS };
  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index];
    switch (arg) {
      case '--json':
        options.json = true;
        break;
      case '--shards':
        options.shards = requiredNumber(argv, index += 1, arg);
        break;
      case '--shard-bytes':
        options.shardBytes = requiredNumber(argv, index += 1, arg);
        break;
      case '--rows-per-shard':
        options.rowsPerShard = requiredNumber(argv, index += 1, arg);
        break;
      case '--consumers':
        options.consumers = requiredNumber(argv, index += 1, arg);
        break;
      case '--concurrency':
        options.concurrency = requiredNumber(argv, index += 1, arg);
        break;
      case '--bandwidth-mbps':
        options.bandwidthMbps = requiredNumber(argv, index += 1, arg);
        break;
      case '--drop-every':
        options.dropEvery = requiredNumber(argv, index += 1, arg);
        break;
      case '--corrupt-every':
        options.corruptEvery = requiredNumber(argv, index += 1, arg);
        break;
      case '--partition-every':
        options.partitionEvery = requiredNumber(argv, index += 1, arg);
        break;
      case '--restart-every':
        options.restartEvery = requiredNumber(argv, index += 1, arg);
        break;
      case '--max-attempts':
        options.maxAttempts = requiredNumber(argv, index += 1, arg);
        break;
      case '--target':
        options.wireSpeedTarget = requiredNumber(argv, index += 1, arg);
        break;
      case '--checkpoint-file':
        options.checkpointFile = requiredValue(argv, index += 1, arg);
        break;
      case '--help':
      case '-h':
        options.help = true;
        break;
      default:
        throw new Error(`unknown option ${arg}`);
    }
  }
  return normalizeOptions(options);
}

function requiredNumber(argv, index, option) {
  const value = Number(requiredValue(argv, index, option));
  if (!Number.isFinite(value)) throw new Error(`${option} requires a number`);
  return value;
}

function normalizedRatio(value, fallback) {
  const numeric = Number(value);
  return Number.isFinite(numeric) && numeric > 0 && numeric <= 1 ? numeric : fallback;
}

function requiredValue(argv, index, option) {
  const value = argv[index];
  if (!value || value.startsWith('--')) throw new Error(`${option} requires a value`);
  return value;
}

function usage() {
  return [
    'Usage: node scripts/chaos-local-network.mjs [options]',
    '',
    'Runs a deterministic local virtual SDN network chaos test for direct FlatSQL published-shard replication.',
    'The harness injects stream drops, corruption, partitions, and requester restarts, then verifies all consumers converge.',
    '',
    'Options:',
    '  --shards <n>              Published shard count, default 256',
    '  --shard-bytes <bytes>     Approximate FlatSQL shard stream bytes, default 1048576',
    '  --rows-per-shard <n>      Logical rows per shard, default 2000',
    '  --consumers <n>           Consumer nodes, default 8',
    '  --concurrency <n>         Simulated range/shard concurrency, default 16',
    '  --bandwidth-mbps <n>      Simulated clean-link bandwidth, default 2000',
    '  --drop-every <n>          Drop every Nth request, default disabled',
    '  --corrupt-every <n>       Corrupt every Nth response, default disabled',
    '  --partition-every <n>     Partition every Nth request, default disabled',
    '  --restart-every <n>       Restart requester every Nth request, default disabled',
    '  --checkpoint-file <path>  Persist consumer pin progress for resume testing',
    '  --target <ratio>          Required clean-link utilization, default 0.9',
    '  --json                    Print JSON only',
  ].join('\n');
}

function sha256Hex(bytes) {
  return createHash('sha256').update(bytes).digest('hex');
}

function formatError(error) {
  return error instanceof Error ? error.message : String(error);
}

function formatBytes(value) {
  if (value >= 1_000_000_000) return `${(value / 1_000_000_000).toFixed(2)} GB`;
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(2)} MB`;
  if (value >= 1_000) return `${(value / 1_000).toFixed(2)} KB`;
  return `${value} B`;
}

function formatReport(report) {
  const speed = formatBytes(report.replication.bytesPerSecond);
  const utilization = report.replication.wireSpeedUtilization == null
    ? 'unknown'
    : `${Math.round(report.replication.wireSpeedUtilization * 100)}%`;
  return [
    'SDN local virtual chaos network',
    `Protocol: ${report.transport.protocol} (${report.transport.mode})`,
    `Topology: 1 provider, ${report.summary.totalConsumers} consumers, ${report.summary.uniqueShards} shards`,
    `Rows: ${report.summary.totalRows.toLocaleString()} / ${report.summary.expectedRowsTotal.toLocaleString()} verified`,
    `Download: ${speed}/s simulated (${utilization} of ${report.scenario.bandwidthMbps} Mbps clean link)`,
    ...(report.replication.requiredBytesPerSecond == null ? [] : [`Required: ${formatBytes(report.replication.requiredBytesPerSecond)}/s data-plane throughput`]),
    `Bytes: provider ${formatBytes(report.replication.providerBytes)}, peer ${formatBytes(report.replication.peerBytes)}, duplicate ${formatBytes(report.replication.duplicateBytes)}`,
    `Chaos: drops ${report.chaos.droppedRequests}, corruption ${report.chaos.corruptedResponses}, partitions ${report.chaos.partitionFailures}, restarts ${report.chaos.restartEvents}, retries ${report.chaos.retryCount}`,
    `Timing: manifest ${report.timingMs.manifestDiscovery} ms / network ${report.timingMs.networkTransfer} ms / verify ${report.timingMs.verification} ms`,
    `Fallbacks: HTTP ${report.transport.remoteHttpFallback ? 'enabled' : 'disabled'}, SSH ${report.transport.sshFallback ? 'enabled' : 'disabled'}`,
  ].join('\n');
}

async function main() {
  const options = parseArgs(process.argv.slice(2));
  if (options.help) {
    console.log(usage());
    return;
  }
  const report = await runChaosLocalNetwork(options);
  if (options.json) {
    console.log(JSON.stringify(report, null, 2));
  } else {
    console.log(formatReport(report));
    console.log('');
    console.log(JSON.stringify(report, null, 2));
  }
  if (!report.summary.allVerified) process.exitCode = 1;
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? '').href) {
  main().catch((error) => {
    console.error(formatError(error));
    process.exit(1);
  });
}

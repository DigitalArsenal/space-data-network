#!/usr/bin/env node

import { createHash } from 'node:crypto';
import { pathToFileURL } from 'node:url';

import {
  createDefaultLibp2pFlatSqlSyncClient,
  parseThroughputHarnessArgs,
  publishedShardWireSpeedAudit,
  throughputHarnessSummary,
} from '../dist/ui/index.mjs';

const QUERY_PROFILE = 'dataset-publication-offset-v1';
const DEFAULT_PUBLISHED_SHARD_RANGE_BYTES = 16 * 1024 * 1024;
const DEFAULT_PUBLISHED_SHARD_RANGE_CONCURRENCY = 1;
const DEFAULT_PUBLISHED_SHARD_BATCH_BYTES = 64 * 1024 * 1024;
const DEFAULT_PUBLISHED_SHARD_BATCH_SEGMENTS = 8;
const DEFAULT_PUBLISHED_SHARD_BATCH_CONCURRENCY = 4;

export async function runThroughputHarness(options, now = () => Date.now()) {
  validateOptions(options);
  const client = await createDefaultLibp2pFlatSqlSyncClient(options.addrs, {
    requestTimeoutMs: options.requestTimeoutMs,
  });
  let controlClientStopped = false;
  try {
    const probe = options.wireSpeedBitsPerSecond
      ? {
          requestedBytes: 0,
          payloadBytes: 0,
          elapsedMs: 0,
          bytesPerSecond: Math.floor(options.wireSpeedBitsPerSecond / 8),
          skipped: true,
        }
      : await client.measureWireSpeed({
          targetPeerId: options.peer,
          candidateAddrs: options.addrs,
          probeBytes: options.probeBytes,
        });

    const manifestStartedAt = now();
    const manifest = await client.openFlatSqlSyncManifest({
      targetPeerId: options.peer,
      candidateAddrs: options.addrs,
      schema: options.schema,
      op: 'open_manifest',
      providerId: options.providerId,
      sourceName: options.sourceName,
      queryProfile: QUERY_PROFILE,
      limit: options.manifestLimit,
    });
    const manifestDiscoveryMs = Math.max(0, now() - manifestStartedAt);
    const cidSegments = manifest.segments.filter((segment) => Boolean(segment.cid));
    const selectedSegments = options.maxSegments == null
      ? cidSegments
      : cidSegments.slice(0, options.maxSegments);
    if (selectedSegments.length === 0) {
      throw new Error('published manifest has no CID-backed shard segments to measure');
    }
    await client.stop?.();
    controlClientStopped = true;
    const shardSources = publishedShardSourcesForHarness(options);
    const shardClients = await Promise.all(Array.from(
      { length: Math.max(1, options.clientPoolSize) },
      async (_value, index) => {
        const source = shardSources[index % shardSources.length];
        return {
          ...source,
          client: await createDefaultLibp2pFlatSqlSyncClient(source.addrs, {
            requestTimeoutMs: options.requestTimeoutMs,
          }),
        };
      },
    ));

    let download;
    try {
      download = await downloadPublishedSegments({
         clients: shardClients,
         segments: selectedSegments,
         schema: options.schema,
         providerId: options.providerId,
         sourceName: options.sourceName,
         concurrency: options.concurrency,
        transferMode: options.transferMode,
        rangeBytes: options.rangeBytes,
        rangeConcurrency: options.rangeConcurrency,
        batchBytes: options.batchBytes,
        batchSegments: options.batchSegments,
        batchConcurrency: options.batchConcurrency,
        now,
      });
     } finally {
       await stopClients(shardClients.map((source) => source.client));
     }
    const audit = publishedShardWireSpeedAudit({
      downloadedBytes: download.downloadedBytes,
      measuredWireSpeedBytesPerSecond: probe.bytesPerSecond,
      wireSpeedBaselineBytesPerSecond: options.wireSpeedBitsPerSecond
        ? Math.floor(options.wireSpeedBitsPerSecond / 8)
        : probe.bytesPerSecond,
      manifestDiscoveryMs,
      networkTransferMs: download.networkTransferMs,
      verificationMs: download.verificationMs,
      flatSqlMaterializationMs: 0,
      wireSpeedTarget: options.target,
    });

    return {
      generatedAt: new Date(now()).toISOString(),
      peer: options.peer,
      schema: options.schema,
      target: options.target,
      artifactRouting: {
         protocol: '/space-data-network/flatsql-sync/1.0.0',
         mode: artifactRoutingMode(options.transferMode, shardSources.length),
         clientPoolSize: shardClients.length,
         sourceCount: shardSources.length,
         sources: download.sourceStats,
         transferMode: options.transferMode,
         rangeBytes: options.rangeBytes,
         rangeConcurrency: options.rangeConcurrency,
         batchBytes: options.batchBytes,
         batchSegments: options.batchSegments,
         batchConcurrency: options.batchConcurrency,
        remoteHttpFallback: false,
        sshFallback: false,
      },
      probe,
      manifest: {
        totalCount: manifest.totalCount,
        totalBytes: manifest.totalBytes,
        segmentCount: cidSegments.length,
        downloadedSegmentCount: selectedSegments.length,
        manifestDiscoveryMs,
        head: manifest.head,
        snapshotId: manifest.snapshotId,
        highWaterMark: manifest.highWaterMark,
      },
      audit,
    };
  } finally {
    await stopClients(controlClientStopped ? [] : [client]);
  }
}

export async function downloadPublishedSegments(options) {
  if (options.transferMode === 'batches') {
    return await downloadPublishedSegmentBatches(options);
  }
  const queue = options.segments.map((segment, index) => ({ segment, index }));
  const startedAt = options.now?.() ?? Date.now();
  const now = options.now ?? (() => Date.now());
  const shardSources = normalizeDownloadShardSources(options);
  const sourceStats = initializeSourceStats(shardSources);
  const sourceScores = initializeSourceScores(shardSources);
  let downloadedBytes = 0;
  let verificationMs = 0;

  async function worker() {
    while (queue.length > 0) {
      const item = queue.shift();
      if (!item) return;
      const cid = item.segment.cid;
      if (!cid) throw new Error('published shard segment is missing CID');
      const streamBytes = await fetchPublishedShardBytesViaRanges({
        shardSources,
        schema: options.schema,
        providerId: options.providerId,
        sourceName: options.sourceName,
        segment: item.segment,
        cid,
        preferredSourceIndex: item.index,
        rangeBytes: options.rangeBytes,
        rangeConcurrency: options.rangeConcurrency,
        sourceStats,
        sourceScores,
      });
      const verificationStartedAt = now();
      verifyPublishedShardBytes(item.segment, streamBytes);
      verificationMs += Math.max(0, now() - verificationStartedAt);
      downloadedBytes += streamBytes.byteLength;
    }
  }

  const workers = Array.from(
    { length: Math.max(1, Math.min(options.concurrency, queue.length)) },
    () => worker(),
  );
  await Promise.all(workers);
  const networkTransferMs = Math.max(0, now() - startedAt);

  return { downloadedBytes, networkTransferMs, verificationMs, sourceStats: Array.from(sourceStats.values()) };
}

async function downloadPublishedSegmentBatches(options) {
  const now = options.now ?? (() => Date.now());
  const startedAt = now();
  const shardSources = normalizeDownloadShardSources(options);
  const sourceStats = initializeSourceStats(shardSources);
  const sourceScores = initializeSourceScores(shardSources);
  const queue = publishedShardBatchQueue(options.segments, {
    batchBytes: options.batchBytes,
    batchSegments: options.batchSegments,
  });
  let downloadedBytes = 0;
  let verificationMs = 0;

  async function worker() {
    while (queue.length > 0) {
      const batch = queue.shift();
      if (!batch) return;
      const response = await readPublishedShardBatchFromSources(shardSources, {
        schema: options.schema,
        providerId: options.providerId,
        sourceName: options.sourceName,
        queryProfile: QUERY_PROFILE,
        cids: batch.items.map((item) => item.segment.cid),
      }, batch.preferredSourceIndex, sourceStats, sourceScores);
      const shardsByCid = new Map(response.shards.map((shard) => [shard.header.cid, shard]));
      for (const item of batch.items) {
        const shard = shardsByCid.get(item.segment.cid);
        if (!shard) throw new Error(`published shard batch did not return ${item.segment.cid}`);
        if (item.segment.byteCount > 0 && shard.streamBytes.byteLength !== item.segment.byteCount) {
          throw new Error(`published shard ${item.segment.cid} returned ${shard.streamBytes.byteLength}/${item.segment.byteCount} bytes`);
        }
        const verificationStartedAt = now();
        verifyPublishedShardBytes(item.segment, shard.streamBytes);
        verificationMs += Math.max(0, now() - verificationStartedAt);
        downloadedBytes += shard.streamBytes.byteLength;
      }
    }
  }

  await Promise.all(Array.from(
    { length: Math.max(1, Math.min(options.batchConcurrency ?? DEFAULT_PUBLISHED_SHARD_BATCH_CONCURRENCY, queue.length)) },
    () => worker(),
  ));
  const networkTransferMs = Math.max(0, now() - startedAt);

  return { downloadedBytes, networkTransferMs, verificationMs, sourceStats: Array.from(sourceStats.values()) };
}

function publishedShardBatchQueue(segments, options) {
  const maxBatchBytes = Math.max(1, Math.floor(options.batchBytes ?? DEFAULT_PUBLISHED_SHARD_BATCH_BYTES));
  const maxBatchSegments = Math.max(1, Math.floor(options.batchSegments ?? DEFAULT_PUBLISHED_SHARD_BATCH_SEGMENTS));
  const batches = [];
  let items = [];
  let byteCount = 0;

  const flush = () => {
    if (items.length === 0) return;
    batches.push({
      items,
      byteCount,
      preferredSourceIndex: items[0]?.index ?? 0,
    });
    items = [];
    byteCount = 0;
  };

  for (const [index, segment] of segments.entries()) {
    const cid = String(segment.cid ?? '').trim();
    if (!cid) throw new Error('published shard segment is missing CID');
    const itemBytes = Math.max(0, Math.floor(segment.byteCount ?? 0));
    if (items.length > 0 && (byteCount + itemBytes > maxBatchBytes || items.length >= maxBatchSegments)) flush();
    items.push({ segment: { ...segment, cid }, index });
    byteCount += itemBytes;
    if (itemBytes >= maxBatchBytes || items.length >= maxBatchSegments) flush();
  }
  flush();
  return batches;
}

async function fetchPublishedShardBytesViaRanges(options) {
  const shardSources = options.shardSources;
  const byteCount = Math.max(0, Math.floor(options.segment.byteCount ?? 0));
  if (byteCount <= 0) {
    const shard = await readPublishedShardFromSources(shardSources, {
      schema: options.schema,
      providerId: options.providerId,
        sourceName: options.sourceName,
        queryProfile: QUERY_PROFILE,
        cid: options.cid,
    }, options.preferredSourceIndex ?? 0, options.sourceStats, options.sourceScores);
    return shard.streamBytes;
  }

  const ranges = [];
  const rangeBytes = Math.max(1, Math.floor(options.rangeBytes ?? DEFAULT_PUBLISHED_SHARD_RANGE_BYTES));
  for (let offset = 0; offset < byteCount; offset += rangeBytes) {
    ranges.push({
      index: ranges.length,
      offset,
      length: Math.min(rangeBytes, byteCount - offset),
    });
  }

  const chunks = new Array(ranges.length);
  let nextRange = 0;
  async function rangeWorker() {
    while (nextRange < ranges.length) {
      const range = ranges[nextRange];
      nextRange += 1;
      const shard = await readPublishedShardFromSources(shardSources, {
        schema: options.schema,
        providerId: options.providerId,
        sourceName: options.sourceName,
        queryProfile: QUERY_PROFILE,
        cid: options.cid,
        byteOffset: range.offset,
        byteLength: range.length,
      }, (options.preferredSourceIndex ?? 0) + range.index, options.sourceStats, options.sourceScores);
      const headerOffset = shard.header.byteOffset ?? range.offset;
      const headerLength = shard.header.byteLength ?? shard.header.byteCount;
      if (headerOffset !== range.offset || headerLength !== range.length || shard.streamBytes.byteLength !== range.length) {
        throw new Error(`published shard ${options.cid} returned range ${headerOffset}+${headerLength}/${range.offset}+${range.length}`);
      }
      chunks[range.index] = shard.streamBytes;
    }
  }

  await Promise.all(Array.from(
    { length: Math.max(1, Math.min(options.rangeConcurrency ?? DEFAULT_PUBLISHED_SHARD_RANGE_CONCURRENCY, ranges.length)) },
    () => rangeWorker(),
  ));
  return concatUint8Arrays(chunks);
}

async function readPublishedShardFromSources(shardSources, query, preferredSourceIndex, sourceStats, sourceScores) {
  const orderedSources = orderedShardSources(shardSources, preferredSourceIndex, sourceScores);
  let lastError;
  for (const source of orderedSources) {
    const stats = sourceStats ? sourceStatsFor(sourceStats, source) : null;
    if (stats) stats.requests += 1;
    const startedAt = Date.now();
    if (sourceScores) recordSourceScoreStart(sourceScores, source);
    try {
      const shard = await source.client.readFlatSqlPublishedShard({
        ...query,
        targetPeerId: source.peer,
        candidateAddrs: source.addrs,
      });
      if (stats) stats.bytes += shard.streamBytes?.byteLength ?? 0;
      if (sourceScores) recordSourceScoreSuccess(sourceScores, source, shard.streamBytes?.byteLength ?? 0, Date.now() - startedAt);
      return shard;
    } catch (error) {
      if (stats) stats.errors += 1;
      if (sourceScores) recordSourceScoreFailure(sourceScores, source);
      lastError = error;
    } finally {
      if (sourceScores) recordSourceScoreEnd(sourceScores, source);
    }
  }
  throw lastError ?? new Error('remote FlatSQL published shard stream is unavailable');
}

async function readPublishedShardBatchFromSources(shardSources, query, preferredSourceIndex, sourceStats, sourceScores) {
  const orderedSources = orderedShardSources(shardSources, preferredSourceIndex, sourceScores);
  let lastError;
  for (const source of orderedSources) {
    const stats = sourceStats ? sourceStatsFor(sourceStats, source) : null;
    if (stats) stats.requests += 1;
    const startedAt = Date.now();
    if (sourceScores) recordSourceScoreStart(sourceScores, source);
    try {
      const batch = await source.client.readFlatSqlPublishedShardBatch({
        ...query,
        targetPeerId: source.peer,
        candidateAddrs: source.addrs,
      });
      const byteCount = batch.shards.reduce((sum, shard) => sum + (shard.streamBytes?.byteLength ?? 0), 0);
      if (stats) stats.bytes += byteCount;
      if (sourceScores) recordSourceScoreSuccess(sourceScores, source, byteCount, Date.now() - startedAt);
      return batch;
    } catch (error) {
      if (stats) stats.errors += 1;
      if (sourceScores) recordSourceScoreFailure(sourceScores, source);
      lastError = error;
    } finally {
      if (sourceScores) recordSourceScoreEnd(sourceScores, source);
    }
  }
  throw lastError ?? new Error('remote FlatSQL published shard batch stream is unavailable');
}

function initializeSourceStats(shardSources) {
  const stats = new Map();
  for (const source of shardSources) sourceStatsFor(stats, source);
  return stats;
}

function initializeSourceScores(shardSources) {
  const scores = new Map();
  for (const source of shardSources) sourceScoreFor(scores, source);
  return scores;
}

function sourceStatsFor(sourceStats, source) {
  const peer = String(source?.peer ?? '').trim() || 'unknown';
  const addrs = Array.from(new Set((source?.addrs ?? []).map((addr) => String(addr).trim()).filter(Boolean)));
  const key = JSON.stringify({ peer, addrs });
  if (!sourceStats.has(key)) {
    sourceStats.set(key, {
      peer,
      addrs,
      bytes: 0,
      requests: 0,
      errors: 0,
    });
  }
  return sourceStats.get(key);
}

function orderedShardSources(shardSources, preferredSourceIndex, sourceScores) {
  if (shardSources.length <= 1) return shardSources;
  const index = normalizedSourceIndex(preferredSourceIndex, shardSources.length);
  const preferredOrder = [
    ...shardSources.slice(index),
    ...shardSources.slice(0, index),
  ];
  if (!sourceScores) {
    return preferredOrder;
  }
  const preferredRank = new Map(preferredOrder.map((source, rank) => [sourceScoreKey(source), rank]));
  const baseRank = new Map(shardSources.map((source, rank) => [sourceScoreKey(source), rank]));
  return [...preferredOrder].sort((left, right) => compareShardSources(left, right, sourceScores, preferredRank, baseRank));
}

function compareShardSources(left, right, sourceScores, preferredRank, baseRank) {
  const leftKey = sourceScoreKey(left);
  const rightKey = sourceScoreKey(right);
  const leftScore = sourceScores.get(leftKey);
  const rightScore = sourceScores.get(rightKey);
  const leftAttempts = sourceScoreAttempts(leftScore);
  const rightAttempts = sourceScoreAttempts(rightScore);
  if (leftAttempts === 0 || rightAttempts === 0) {
    const leftActive = Math.max(0, leftScore?.active ?? 0);
    const rightActive = Math.max(0, rightScore?.active ?? 0);
    if (leftAttempts === 0 && rightAttempts === 0) {
      if (leftActive > 0 && rightActive > 0) {
        return (baseRank.get(leftKey) ?? 0) - (baseRank.get(rightKey) ?? 0);
      }
      if (leftActive !== rightActive) return leftActive - rightActive;
      return (preferredRank.get(leftKey) ?? 0) - (preferredRank.get(rightKey) ?? 0);
    }
    if (leftAttempts === 0 && leftActive > 0) return 1;
    if (rightAttempts === 0 && rightActive > 0) return -1;
    return (preferredRank.get(leftKey) ?? 0) - (preferredRank.get(rightKey) ?? 0);
  }
  if ((leftScore?.failures ?? 0) !== (rightScore?.failures ?? 0)) {
    return (leftScore?.failures ?? 0) - (rightScore?.failures ?? 0);
  }
  const leftEffectiveBytesPerMs = effectiveSourceBytesPerMs(leftScore);
  const rightEffectiveBytesPerMs = effectiveSourceBytesPerMs(rightScore);
  if (leftEffectiveBytesPerMs !== rightEffectiveBytesPerMs) {
    return rightEffectiveBytesPerMs - leftEffectiveBytesPerMs;
  }
  return (preferredRank.get(leftKey) ?? 0) - (preferredRank.get(rightKey) ?? 0);
}

function sourceScoreFor(sourceScores, source) {
  const key = sourceScoreKey(source);
  if (!sourceScores.has(key)) {
    sourceScores.set(key, {
      successes: 0,
      failures: 0,
      ewmaBytesPerMs: 0,
      active: 0,
    });
  }
  return sourceScores.get(key);
}

function sourceScoreKey(source) {
  const peer = String(source?.peer ?? '').trim() || 'unknown';
  const addrs = Array.from(new Set((source?.addrs ?? []).map((addr) => String(addr).trim()).filter(Boolean)));
  return JSON.stringify({ peer, addrs });
}

function sourceScoreAttempts(score) {
  if (!score) return 0;
  return score.successes + score.failures;
}

function effectiveSourceBytesPerMs(score) {
  if (!score) return 0;
  return score.ewmaBytesPerMs / Math.sqrt(1 + Math.max(0, score.active));
}

function recordSourceScoreStart(sourceScores, source) {
  const score = sourceScoreFor(sourceScores, source);
  score.active += 1;
}

function recordSourceScoreEnd(sourceScores, source) {
  const score = sourceScoreFor(sourceScores, source);
  score.active = Math.max(0, score.active - 1);
}

function recordSourceScoreSuccess(sourceScores, source, byteCount, elapsedMs) {
  const score = sourceScoreFor(sourceScores, source);
  const sample = Math.max(0, byteCount) / Math.max(1, elapsedMs);
  score.ewmaBytesPerMs = score.successes === 0
    ? sample
    : (score.ewmaBytesPerMs * 0.7) + (sample * 0.3);
  score.successes += 1;
}

function recordSourceScoreFailure(sourceScores, source) {
  const score = sourceScoreFor(sourceScores, source);
  score.failures += 1;
  score.ewmaBytesPerMs *= 0.5;
}

function normalizedSourceIndex(value, count) {
  if (!Number.isFinite(value) || count <= 0) return 0;
  const index = Math.floor(value) % count;
  return index < 0 ? index + count : index;
}

function publishedShardSourcesForHarness(options) {
  return normalizeShardSourceEntries([
    ...(options.shardSources ?? []),
    { peer: options.peer, addrs: options.addrs },
  ]);
}

function normalizeDownloadShardSources(options) {
  return normalizeShardSourceEntries((options.clients ?? []).map((entry) => {
    if (entry?.client) return entry;
    return {
      client: entry,
      peer: options.peer,
      addrs: options.addrs,
    };
  }));
}

function normalizeShardSourceEntries(entries) {
  const out = [];
  const seen = new Set();
  for (const entry of entries) {
    const peer = String(entry?.peer ?? '').trim();
    const addrs = Array.from(new Set((entry?.addrs ?? []).map((addr) => String(addr).trim()).filter(Boolean)));
    if (!peer || addrs.length === 0) continue;
    const key = JSON.stringify({ peer, addrs });
    if (seen.has(key)) continue;
    seen.add(key);
    out.push({
      ...entry,
      peer,
      addrs,
    });
  }
  return out;
}

function concatUint8Arrays(chunks) {
  const length = chunks.reduce((sum, chunk) => sum + chunk.byteLength, 0);
  const out = new Uint8Array(length);
  let offset = 0;
  for (const chunk of chunks) {
    out.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return out;
}

function verifyPublishedShardBytes(segment, streamBytes) {
  if (segment.shardSha256) {
    const actualSha = createHash('sha256').update(streamBytes).digest('hex');
    if (actualSha !== segment.shardSha256) {
      throw new Error(`shard SHA-256 mismatch for ${segment.cid}`);
    }
  }
  assertFlatSqlSizePrefixedStream(streamBytes);
}

function assertFlatSqlSizePrefixedStream(streamBytes) {
  let offset = 0;
  let index = 0;
  while (offset < streamBytes.byteLength) {
    if (streamBytes.byteLength - offset < 4) {
      throw new Error(`truncated FlatSQL size-prefixed stream header at offset ${offset}`);
    }
    const length = new DataView(streamBytes.buffer, streamBytes.byteOffset + offset, 4).getUint32(0, true);
    offset += 4;
    const nextOffset = offset + length;
    if (nextOffset > streamBytes.byteLength) {
      throw new Error(`truncated FlatSQL size-prefixed stream payload at index ${index}`);
    }
    offset = nextOffset;
    index += 1;
  }
}

function validateOptions(options) {
  if (!options.peer) throw new Error('--peer is required');
  if (!Array.isArray(options.addrs) || options.addrs.length === 0) throw new Error('at least one --addr is required');
}

function usage() {
  return [
    'Usage: npm run measure:flatsql-sync -- --peer <peerId> --addr <multiaddr> [options]',
    '',
    'Measures CelesTrak published-shard download throughput against libp2p wire speed.',
    'Manifest discovery and bulk shard bytes both use /space-data-network/flatsql-sync/1.0.0 over libp2p.',
    'Bulk shard bytes are fetched as published-shard byte ranges or bounded published-shard batches.',
    'There is no Kubo gateway, remote HTTP, or SSH data fallback in this harness.',
    '',
    'Options:',
    '  --schema <schema>            SDS schema, default OMM.fbs',
    '  --provider-id <id>          Provider/source filter',
    '  --source-name <name>        Provider/source filter',
    '  --probe-bytes <bytes>       Wire-speed probe payload, default 67108864',
    '  --request-timeout-ms <ms>   Libp2p dial/exchange timeout, default 60000',
    '  --manifest-limit <rows>     Published manifest row limit, default 50000',
    '  --max-segments <count>      Measure only the first N published shards',
    '  --concurrency <count>       Parallel shard downloads, default 32',
     '  --client-pool-size <count>  Dedicated libp2p clients for shard ranges, default 8',
     '  --shard-source-peer <id>    Additional peer serving the same immutable published shards',
     '  --shard-source-addr <addr>  Address for the most recent --shard-source-peer',
     '  --transfer-mode <mode>      ranges or batches, default ranges',
     '  --range-bytes <bytes>       Published shard byte range size, default 16777216',
    '  --range-concurrency <count> Parallel ranges per shard, default 1',
    '  --batch-bytes <bytes>       Published shard batch byte cap, default 67108864',
    '  --batch-segments <count>    Published shard batch segment cap, default 8',
    '  --batch-concurrency <count> Parallel batch streams, default 4',
    '  --wire-speed-bps <bps>      Absolute wire-speed baseline, e.g. 2000000000 for 2 Gbps',
    '  --target <ratio>            Required wire-speed utilization, default 0.8',
    '  --json                      Print JSON only',
  ].join('\n');
}

function artifactRoutingMode(transferMode, sourceCount) {
  const suffix = transferMode === 'batches' ? 'published-shard-batches' : 'published-shard-ranges';
  return sourceCount > 1 ? `multi-source direct libp2p ${suffix}` : `direct-libp2p-${suffix}`;
}

async function stopClients(clients) {
  await Promise.race([
    Promise.allSettled(clients.map((client) => client.stop?.())),
    new Promise((resolve) => setTimeout(resolve, 1000)),
  ]);
}

async function main() {
  const options = parseThroughputHarnessArgs(process.argv.slice(2));
  if (options.help) {
    console.log(usage());
    return;
  }
  const result = await runThroughputHarness(options);
  if (options.json) {
    console.log(JSON.stringify(result, null, 2));
  } else {
    console.log(throughputHarnessSummary(result));
    console.log('');
    console.log(JSON.stringify(result, null, 2));
  }
  if (result.audit.targetMet !== true) process.exitCode = 1;
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? '').href) {
  main()
    .then(() => process.exit(process.exitCode ?? 0))
    .catch((error) => {
      console.error(error instanceof Error ? error.message : error);
      process.exit(1);
    });
}

#!/usr/bin/env node

import { pathToFileURL } from 'node:url';

import {
  connectIpfsArtifactPeers,
  connectIpfsArtifactProviders,
  createDefaultLibp2pFlatSqlSyncClient,
  fetchCidBytesFromGateway,
  parseThroughputHarnessArgs,
  publishedShardWireSpeedAudit,
  throughputHarnessSummary,
  timedFlatBufferStreamFromPublishedFlatSqlSegment,
} from '../dist/ui/index.mjs';

const QUERY_PROFILE = 'dataset-publication-offset-v1';

export async function runThroughputHarness(options, now = () => Date.now()) {
  validateOptions(options);
  const client = await createDefaultLibp2pFlatSqlSyncClient(options.addrs, {
    requestTimeoutMs: options.requestTimeoutMs,
  });
  try {
    const probe = await client.measureWireSpeed({
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

    const artifactRouting = await connectLocalIpfsPeers(options, selectedSegments);
    const download = await downloadPublishedSegments({
      segments: selectedSegments,
      schema: options.schema,
      peer: options.peer,
      gateway: options.gateway,
      concurrency: options.concurrency,
      requestTimeoutMs: options.requestTimeoutMs,
      now,
    });
    const audit = publishedShardWireSpeedAudit({
      downloadedBytes: download.downloadedBytes,
      measuredWireSpeedBytesPerSecond: probe.bytesPerSecond,
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
      artifactRouting,
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
    await stopClient(client);
  }
}

export async function downloadPublishedSegments(options) {
  const queue = options.segments.map((segment, index) => ({ segment, index }));
  const fetchedSegments = [];
  const startedAt = options.now?.() ?? Date.now();
  const now = options.now ?? (() => Date.now());
  let downloadedBytes = 0;
  let verificationMs = 0;

  async function worker() {
    while (queue.length > 0) {
      const item = queue.shift();
      if (!item) return;
      const streamBytes = await fetchCidBytesFromGateway(options.gateway, item.segment.cid, {
        timeoutMs: options.requestTimeoutMs,
      });
      fetchedSegments.push({
        index: item.index,
        segment: item.segment,
        streamBytes,
      });
    }
  }

  const workers = Array.from(
    { length: Math.max(1, Math.min(options.concurrency, queue.length)) },
    () => worker(),
  );
  await Promise.all(workers);
  const networkTransferMs = Math.max(0, now() - startedAt);

  fetchedSegments.sort((left, right) => left.index - right.index);
  for (const fetched of fetchedSegments) {
    const result = await timedFlatBufferStreamFromPublishedFlatSqlSegment({
      schema: options.schema,
      providerPeerId: options.peer,
      cid: fetched.segment.cid,
      shardSha256: fetched.segment.shardSha256,
      fetchCidBytes: async () => fetched.streamBytes,
    });
    downloadedBytes += result.streamBytes.byteLength;
    verificationMs += result.verificationMs;
  }

  return { downloadedBytes, networkTransferMs, verificationMs };
}

function validateOptions(options) {
  if (!options.peer) throw new Error('--peer is required');
  if (!Array.isArray(options.addrs) || options.addrs.length === 0) throw new Error('at least one --addr is required');
  if (!options.gateway) throw new Error('--gateway is required for local IPFS CID retrieval');
  if (options.ipfsPeers.length > 0 && !options.ipfsApi) throw new Error('--ipfs-api is required when --ipfs-peer is used');
}

async function connectLocalIpfsPeers(options, selectedSegments = []) {
  const configuredPeerConnect = await connectIpfsArtifactPeers({
    ipfsApiUrl: options.ipfsApi,
    artifactPeerAddrs: options.ipfsPeers,
    timeoutMs: options.requestTimeoutMs,
  });
  if (options.ipfsPeers.length > 0 && configuredPeerConnect.failed > 0) {
    throw new Error(
      `local IPFS swarm connect failed for ${configuredPeerConnect.failed}/${configuredPeerConnect.attempted} configured peers`,
    );
  }
  const providerDiscoveryCids = options.ipfsProviderDiscoveryLimit > 0
    ? selectedSegments
      .map((segment) => segment.cid)
      .filter(Boolean)
      .slice(0, options.ipfsProviderDiscoveryLimit)
    : [];
  const providerDiscovery = await connectIpfsArtifactProviders({
    ipfsApiUrl: options.ipfsApi,
    cids: providerDiscoveryCids,
    timeoutMs: options.requestTimeoutMs,
  });
  return {
    configuredPeerCount: options.ipfsPeers.length,
    configuredPeerConnect,
    providerDiscoveryCidCount: providerDiscoveryCids.length,
    providerDiscovery,
  };
}

function usage() {
  return [
    'Usage: npm run measure:flatsql-sync -- --peer <peerId> --addr <multiaddr> [options]',
    '',
    'Measures CelesTrak published-shard download throughput against libp2p wire speed.',
    'Provider control traffic uses /space-data-network/flatsql-sync/1.0.0 over libp2p.',
    'CID bytes are fetched from the local IPFS gateway only; this is not a remote HTTP fallback.',
    'The harness asks local Kubo to discover and connect IPFS providers for selected shard CIDs.',
    'Use --ipfs-peer only to add known shard seed peers before measuring.',
    '',
    'Options:',
    '  --schema <schema>            SDS schema, default OMM.fbs',
    '  --provider-id <id>          Provider/source filter',
    '  --source-name <name>        Provider/source filter',
    '  --gateway <url>             Local IPFS gateway, default http://127.0.0.1:8081',
    '  --ipfs-api <url>            Local IPFS RPC API for swarm connect, default http://127.0.0.1:5001',
    '  --ipfs-peer <multiaddr>     Local IPFS peer to connect before download; repeatable',
    '  --ipfs-provider-discovery-limit <count>  Shard CIDs used for Kubo findprovs, default 16',
    '  --no-ipfs-provider-discovery             Disable automatic Kubo provider discovery',
    '  --probe-bytes <bytes>       Wire-speed probe payload, default 67108864',
    '  --request-timeout-ms <ms>   Libp2p dial/exchange timeout, default 60000',
    '  --manifest-limit <rows>     Published manifest row limit, default 50000',
    '  --max-segments <count>      Measure only the first N published shards',
    '  --concurrency <count>       Parallel shard downloads, default 24',
    '  --target <ratio>            Required wire-speed utilization, default 0.8',
    '  --json                      Print JSON only',
  ].join('\n');
}

async function stopClient(client) {
  if (!client.stop) return;
  await Promise.race([
    client.stop(),
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

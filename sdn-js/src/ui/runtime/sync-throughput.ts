export const DEFAULT_WIRE_SPEED_TARGET = 0.8;

export interface PublishedShardWireSpeedAuditInput {
  downloadedBytes: number;
  measuredWireSpeedBytesPerSecond: number | null | undefined;
  wireSpeedBaselineBytesPerSecond?: number | null | undefined;
  manifestDiscoveryMs: number;
  networkTransferMs: number;
  verificationMs: number;
  flatSqlMaterializationMs: number;
  wireSpeedTarget?: number;
}

export interface PublishedShardWireSpeedAudit {
  downloadedBytes: number;
  measuredWireSpeedBytesPerSecond: number | null;
  wireSpeedBaselineBytesPerSecond: number | null;
  downloadBytesPerSecond: number;
  wireSpeedUtilization: number | null;
  wireSpeedTarget: number;
  targetMet: boolean | null;
  timingsMs: {
    manifestDiscovery: number;
    networkTransfer: number;
    verification: number;
    flatSqlMaterialization: number;
  };
}

export interface ThroughputHarnessOptions {
  peer: string;
  addrs: string[];
  shardSources: ThroughputHarnessShardSource[];
  schema: string;
  providerId?: string;
  sourceName?: string;
  probeBytes: number;
  requestTimeoutMs: number;
  manifestLimit: number;
  maxSegments: number | null;
  concurrency: number;
  clientPoolSize: number;
  rangeBytes: number;
  rangeConcurrency: number;
  wireSpeedBitsPerSecond: number | null;
  target: number;
  json: boolean;
  help: boolean;
}

export interface ThroughputHarnessShardSource {
  peer: string;
  addrs: string[];
}

export interface ThroughputHarnessResult {
  generatedAt: string;
  peer: string;
  schema: string;
  target: number;
  artifactRouting?: {
    protocol: string;
    mode: string;
    clientPoolSize: number;
    sourceCount?: number;
    sources?: Array<{
      peer: string;
      addrs?: string[];
      bytes: number;
      requests: number;
      errors: number;
    }>;
    rangeBytes: number;
    rangeConcurrency: number;
    remoteHttpFallback: boolean;
    sshFallback: boolean;
  };
  probe: {
    requestedBytes: number;
    payloadBytes: number;
    elapsedMs: number;
    bytesPerSecond: number;
    syncProtocol: string;
  };
  manifest: {
    totalCount: number;
    totalBytes: number;
    segmentCount: number;
    downloadedSegmentCount: number;
    manifestDiscoveryMs: number;
    head: string;
    snapshotId: string;
    highWaterMark: string;
  };
  audit: PublishedShardWireSpeedAudit;
}

export function boundedWireSpeedUtilization(value: number | null | undefined): number | null {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < 0) return null;
  return Math.min(1, value);
}

export function measuredWireSpeedUtilization(
  downloadSpeedBytesPerSecond: number,
  measuredWireSpeedBytesPerSecond: number | null | undefined,
): number | null {
  const download = finitePositive(downloadSpeedBytesPerSecond);
  const baseline = finitePositive(measuredWireSpeedBytesPerSecond);
  if (download == null || baseline == null) return null;
  return boundedWireSpeedUtilization(download / baseline);
}

export function meetsWireSpeedTarget(
  downloadSpeedBytesPerSecond: number,
  measuredWireSpeedBytesPerSecond: number | null | undefined,
  target = DEFAULT_WIRE_SPEED_TARGET,
): boolean {
  const utilization = measuredWireSpeedUtilization(downloadSpeedBytesPerSecond, measuredWireSpeedBytesPerSecond);
  return utilization != null && utilization >= target;
}

export function publishedShardWireSpeedAudit(
  input: PublishedShardWireSpeedAuditInput,
): PublishedShardWireSpeedAudit {
  const downloadedBytes = nonNegativeInteger(input.downloadedBytes);
  const networkTransferMs = nonNegativeInteger(input.networkTransferMs);
  const downloadBytesPerSecond = networkTransferMs > 0
    ? Math.floor(downloadedBytes / (networkTransferMs / 1000))
    : 0;
  const measuredWireSpeedBytesPerSecond = finitePositive(input.measuredWireSpeedBytesPerSecond);
  const wireSpeedBaselineBytesPerSecond = finitePositive(input.wireSpeedBaselineBytesPerSecond)
    ?? measuredWireSpeedBytesPerSecond;
  const wireSpeedTarget = finitePositive(input.wireSpeedTarget) ?? DEFAULT_WIRE_SPEED_TARGET;
  const wireSpeedUtilization = measuredWireSpeedUtilization(downloadBytesPerSecond, wireSpeedBaselineBytesPerSecond);
  return {
    downloadedBytes,
    measuredWireSpeedBytesPerSecond,
    wireSpeedBaselineBytesPerSecond,
    downloadBytesPerSecond,
    wireSpeedUtilization,
    wireSpeedTarget,
    targetMet: wireSpeedUtilization == null ? null : wireSpeedUtilization >= wireSpeedTarget,
    timingsMs: {
      manifestDiscovery: nonNegativeInteger(input.manifestDiscoveryMs),
      networkTransfer: networkTransferMs,
      verification: nonNegativeInteger(input.verificationMs),
      flatSqlMaterialization: nonNegativeInteger(input.flatSqlMaterializationMs),
    },
  };
}

export function parseThroughputHarnessArgs(argv: string[]): ThroughputHarnessOptions {
  const options: ThroughputHarnessOptions = {
    peer: '',
    addrs: [],
    shardSources: [],
    schema: 'OMM.fbs',
    probeBytes: 64 * 1024 * 1024,
    requestTimeoutMs: 60_000,
    manifestLimit: 50_000,
    maxSegments: null,
    concurrency: 16,
    clientPoolSize: 4,
    rangeBytes: 4 * 1024 * 1024,
    rangeConcurrency: 1,
    wireSpeedBitsPerSecond: null,
    target: DEFAULT_WIRE_SPEED_TARGET,
    json: false,
    help: false,
  };
  let pendingShardSource: ThroughputHarnessShardSource | null = null;

  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index];
    switch (arg) {
      case '--help':
      case '-h':
        options.help = true;
        break;
      case '--peer':
        options.peer = requiredArg(argv, index += 1, arg);
        break;
      case '--addr':
        options.addrs.push(requiredArg(argv, index += 1, arg));
        break;
      case '--shard-source-peer':
        pendingShardSource = {
          peer: requiredArg(argv, index += 1, arg),
          addrs: [],
        };
        options.shardSources.push(pendingShardSource);
        break;
      case '--shard-source-addr':
        if (!pendingShardSource) throw new Error('--shard-source-addr requires a preceding --shard-source-peer');
        pendingShardSource.addrs.push(requiredArg(argv, index += 1, arg));
        break;
      case '--schema':
        options.schema = requiredArg(argv, index += 1, arg);
        break;
      case '--provider-id':
        options.providerId = requiredArg(argv, index += 1, arg);
        break;
      case '--source-name':
        options.sourceName = requiredArg(argv, index += 1, arg);
        break;
      case '--probe-bytes':
        options.probeBytes = positiveIntegerArg(argv, index += 1, arg);
        break;
      case '--request-timeout-ms':
        options.requestTimeoutMs = positiveIntegerArg(argv, index += 1, arg);
        break;
      case '--manifest-limit':
        options.manifestLimit = positiveIntegerArg(argv, index += 1, arg);
        break;
      case '--max-segments':
        options.maxSegments = positiveIntegerArg(argv, index += 1, arg);
        break;
      case '--concurrency':
        options.concurrency = positiveIntegerArg(argv, index += 1, arg);
        break;
      case '--client-pool-size':
        options.clientPoolSize = positiveIntegerArg(argv, index += 1, arg);
        break;
      case '--range-bytes':
        options.rangeBytes = positiveIntegerArg(argv, index += 1, arg);
        break;
      case '--range-concurrency':
        options.rangeConcurrency = positiveIntegerArg(argv, index += 1, arg);
        break;
      case '--wire-speed-bps':
        options.wireSpeedBitsPerSecond = positiveIntegerArg(argv, index += 1, arg);
        break;
      case '--target':
        options.target = positiveRatioArg(argv, index += 1, arg);
        break;
      case '--json':
        options.json = true;
        break;
      default:
        throw new Error(`unknown option ${arg}`);
    }
  }

  options.addrs = Array.from(new Set(options.addrs.map((addr) => addr.trim()).filter(Boolean)));
  options.shardSources = normalizeShardSources(options.shardSources);
  options.peer = options.peer.trim();
  options.schema = options.schema.trim() || 'OMM.fbs';
  return options;
}

function normalizeShardSources(sources: ThroughputHarnessShardSource[]): ThroughputHarnessShardSource[] {
  return sources
    .map((source) => ({
      peer: source.peer.trim(),
      addrs: Array.from(new Set(source.addrs.map((addr) => addr.trim()).filter(Boolean))),
    }))
    .filter((source) => source.peer && source.addrs.length > 0);
}

export function throughputHarnessSummary(result: ThroughputHarnessResult): string {
  const boundedUtilization = boundedWireSpeedUtilization(result.audit.wireSpeedUtilization);
  const utilization = boundedUtilization == null
    ? 'unknown'
    : `${Math.round(boundedUtilization * 100)}% of wire`;
  const targetStatus = result.audit.targetMet === true ? 'met' : result.audit.targetMet === false ? 'not met' : 'unknown';
  const baseline = result.audit.wireSpeedBaselineBytesPerSecond;
  return [
    `FlatSQL sync throughput audit (${result.schema})`,
    `Peer: ${result.peer}`,
    `Rows: ${result.manifest.totalCount.toLocaleString()} remote across ${result.manifest.segmentCount.toLocaleString()} published segments`,
    ...(result.artifactRouting ? [artifactRoutingSummary(result.artifactRouting)] : []),
    `Wire speed probe: ${formatBytesPerSecond(result.probe.bytesPerSecond)}`,
    ...(baseline && baseline !== result.probe.bytesPerSecond ? [`Wire speed target baseline: ${formatBytesPerSecond(baseline)}`] : []),
    `Published shard download: ${formatBytesPerSecond(result.audit.downloadBytesPerSecond)} (${utilization})`,
    `Timing: manifest ${formatDuration(result.audit.timingsMs.manifestDiscovery)} / network ${formatDuration(result.audit.timingsMs.networkTransfer)} / verify ${formatDuration(result.audit.timingsMs.verification)} / FlatSQL ${formatDuration(result.audit.timingsMs.flatSqlMaterialization)}`,
    `${Math.round(result.target * 100)}% target: ${targetStatus}`,
  ].join('\n');
}

export function formatBytesPerSecond(bytesPerSecond: number): string {
  const value = finitePositive(bytesPerSecond) ?? 0;
  if (value >= 1_000_000_000) return `${(value / 1_000_000_000).toFixed(1)} GB/s`;
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)} MB/s`;
  if (value >= 1_000) return `${(value / 1_000).toFixed(1)} KB/s`;
  return `${Math.floor(value)} B/s`;
}

function finitePositive(value: number | null | undefined): number | null {
  if (typeof value !== 'number' || !Number.isFinite(value) || value <= 0) return null;
  return value;
}

function nonNegativeInteger(value: number): number {
  return Number.isFinite(value) && value > 0 ? Math.floor(value) : 0;
}

function requiredArg(argv: string[], index: number, option: string): string {
  const value = argv[index];
  if (!value || value.startsWith('--')) throw new Error(`${option} requires a value`);
  return value;
}

function positiveIntegerArg(argv: string[], index: number, option: string): number {
  const value = Number(requiredArg(argv, index, option));
  if (!Number.isFinite(value) || value <= 0) throw new Error(`${option} must be a positive number`);
  return Math.floor(value);
}

function positiveRatioArg(argv: string[], index: number, option: string): number {
  const value = Number(requiredArg(argv, index, option));
  if (!Number.isFinite(value) || value <= 0 || value > 1) throw new Error(`${option} must be a ratio from 0 to 1`);
  return value;
}

function formatDuration(milliseconds: number): string {
  const value = nonNegativeInteger(milliseconds);
  if (value >= 1000) return `${(value / 1000).toFixed(2)} s`;
  return `${value} ms`;
}

function artifactRoutingSummary(routing: NonNullable<ThroughputHarnessResult['artifactRouting']>): string {
  const fallbacks = routing.remoteHttpFallback || routing.sshFallback ? 'fallbacks enabled' : 'no HTTP/SSH fallbacks';
  return `Shard transfer: ${routing.mode} over ${routing.protocol}; ${routing.clientPoolSize} clients, ${formatByteCount(routing.rangeBytes)} ranges x${routing.rangeConcurrency}; ${fallbacks}`;
}

function formatByteCount(bytes: number): string {
  const value = nonNegativeInteger(bytes);
  if (value >= 1_000_000_000) return `${(value / 1_000_000_000).toFixed(1)} GB`;
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)} MB`;
  if (value >= 1_000) return `${(value / 1_000).toFixed(1)} KB`;
  return `${value} B`;
}

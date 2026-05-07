export const BACKEND_MODES = ['desktop-local', 'remote-sdn', 'browser-node'] as const;

export type SdnBackendMode = (typeof BACKEND_MODES)[number];
export type CapabilityState =
  | 'available'
  | 'degraded'
  | 'unavailable'
  | 'permission-required'
  | 'remote-only'
  | 'local-only';

export interface BackendCapability {
  id: string;
  state: CapabilityState;
  reason?: string;
}

export interface BackendResult<T> {
  ok: boolean;
  capability: BackendCapability;
  data: T | null;
}

export interface SdnBackendConfig {
  mode: SdnBackendMode;
  kuboApiUrl: string | null;
  gatewayUrl: string | null;
  desktopProxyUrl: string | null;
  serverUrl: string | null;
}

export interface PartialSdnBackendConfig {
  mode?: string | null;
  kuboApiUrl?: string | null;
  gatewayUrl?: string | null;
  desktopProxyUrl?: string | null;
  serverUrl?: string | null;
}

export interface NodeSummary {
  displayName: string;
  peerId: string | null;
  agentVersion: string | null;
  online: boolean;
  runtime: SdnBackendMode;
}

export interface ObservedSdnPeer {
  id: string;
  name: string;
  addrs: string[];
  trustLevel: string;
  agentVersion?: string;
  protocols?: string[];
}

export interface StorageSummary {
  usedBytes: number | null;
  pinnedBytes: number | null;
  cacheBytes: number | null;
  quotaBytes: number | null;
}

export interface LocalObjectSummary {
  id: string;
  label: string;
  schema: string | null;
  source: string | null;
  sizeBytes: number | null;
  state: string;
  cid?: string;
}

export interface SdnBackend {
  readonly mode: SdnBackendMode;
  connect(): Promise<BackendResult<NodeSummary>>;
  getCapabilities(): Promise<BackendCapability[]>;
  getNodeSummary(): Promise<BackendResult<NodeSummary>>;
  getNodeProfile(): Promise<BackendResult<Record<string, unknown>>>;
  saveNodeProfile(profile: Record<string, unknown>): Promise<BackendResult<Record<string, unknown>>>;
  listObservedPeers(): Promise<BackendResult<ObservedSdnPeer[]>>;
  getStorageSummary(): Promise<BackendResult<StorageSummary>>;
  listObjects(): Promise<BackendResult<LocalObjectSummary[]>>;
  runSqlQuery(query: string): Promise<BackendResult<Array<Record<string, unknown>>>>;
  resolveCid(cid: string): Promise<BackendResult<{ cid: string; gatewayUrl: string }>>;
}

export function isBackendMode(value: string | null | undefined): value is SdnBackendMode {
  return BACKEND_MODES.includes(value as SdnBackendMode);
}

export function normalizeBackendConfig(input: PartialSdnBackendConfig): SdnBackendConfig {
  const mode = isBackendMode(input.mode) ? input.mode : 'desktop-local';
  return {
    mode,
    kuboApiUrl: trimTrailingSlash(input.kuboApiUrl) ?? (mode === 'desktop-local' ? 'http://127.0.0.1:5001' : null),
    gatewayUrl: trimTrailingSlash(input.gatewayUrl) ?? (mode === 'desktop-local' ? 'http://127.0.0.1:8081' : null),
    desktopProxyUrl: trimTrailingSlash(input.desktopProxyUrl) ?? null,
    serverUrl: trimTrailingSlash(input.serverUrl) ?? null,
  };
}

export function createCapability(id: string, state: CapabilityState, reason?: string): BackendCapability {
  return reason ? { id, state, reason } : { id, state };
}

export function createAvailableResult<T>(id: string, data: T): BackendResult<T> {
  return { ok: true, capability: createCapability(id, 'available'), data };
}

export function createUnavailableResult<T>(id: string, reason: string): BackendResult<T> {
  return { ok: false, capability: createCapability(id, 'unavailable', reason), data: null };
}

export function createDegradedResult<T>(id: string, reason: string, data: T | null = null): BackendResult<T> {
  return { ok: false, capability: createCapability(id, 'degraded', reason), data };
}

function trimTrailingSlash(value: string | null | undefined): string | null {
  const trimmed = value?.trim();
  if (!trimmed) return null;
  return trimmed.replace(/\/+$/, '');
}

export interface ModuleRuntimeFetchResponse {
  ok: boolean;
  status: number;
  redirected?: boolean;
  headers?: Pick<Headers, 'get'>;
  json(): Promise<unknown>;
}

export type ModuleRuntimeFetchLike = (
  input: string,
  init?: RequestInit,
) => Promise<ModuleRuntimeFetchResponse>;

export interface ModuleRuntimeStats {
  memoryPages: number;
  memoryBytes: number;
  maxMemoryPages: number;
  maxMemoryBytes: number;
  uptimeMs: number;
}

export interface ModuleRuntimeMethod {
  methodId: string;
  displayName?: string;
  description?: string;
}

export interface ModuleRuntimeProtocol {
  protocolId: string;
  methodId?: string;
  inputPortId?: string;
  outputPortId?: string;
  description?: string;
  wireId?: string;
  transportKind?: string;
  role?: string;
  autoInstall: boolean;
  advertise: boolean;
  discoveryKey?: string;
}

export interface ModuleRuntimeTimer {
  timerId: string;
  methodId?: string;
  defaultIntervalMs: number;
  description?: string;
}

export interface ModuleRuntimeManifest {
  pluginId: string;
  name?: string;
  version?: string;
  pluginFamily?: string;
  methods: ModuleRuntimeMethod[];
  capabilities: string[];
  protocols: ModuleRuntimeProtocol[];
  timers: ModuleRuntimeTimer[];
}

export interface ModuleRuntimeOption {
  key: string;
  label: string;
  type: string;
  value?: string;
  description?: string;
  readOnly: boolean;
}

export interface ModuleRuntimeCatalog {
  requiredScope?: string;
  contentType?: string;
  cacheControl?: string;
  bundleSha256?: string;
  sizeBytes?: number;
  signatureHex?: string;
  signerPubKeyHex?: string;
  uploadedAt?: string;
}

export interface ModuleRuntimeEntry {
  id: string;
  version?: string;
  status: string;
  statusMessage?: string;
  description?: string;
  manifest?: ModuleRuntimeManifest;
  stats: ModuleRuntimeStats;
  options: ModuleRuntimeOption[];
  catalog?: ModuleRuntimeCatalog;
}

export interface ModuleRuntimeSnapshot {
  generatedAt: string;
  count: number;
  modules: ModuleRuntimeEntry[];
}

export async function loadModuleRuntimeSnapshotFromServer(
  baseUrl: string,
  fetchImpl: ModuleRuntimeFetchLike = globalThis.fetch.bind(globalThis) as ModuleRuntimeFetchLike,
): Promise<ModuleRuntimeSnapshot> {
  const normalizedBaseUrl = baseUrl.replace(/\/+$/, '');
  const response = await fetchImpl(`${normalizedBaseUrl}/api/v1/modules/runtime`, {
    credentials: 'include',
  });
  if (!response.ok) {
    if (response.status === 404) {
      return emptyModuleRuntimeSnapshot();
    }
    throw new Error(`module runtime query failed (${response.status})`);
  }
  const contentType = response.headers?.get('content-type') ?? '';
  if (!contentType.toLowerCase().includes('json')) {
    if (response.redirected) {
      throw new Error('module runtime query was redirected');
    }
    return emptyModuleRuntimeSnapshot();
  }

  const payload = asRecord(await response.json());
  const modules = normalizeModuleEntries(payload?.modules);
  return {
    generatedAt: pickTrimmedString(payload, 'generatedAt') ?? new Date().toISOString(),
    count: modules.length,
    modules,
  };
}

export function emptyModuleRuntimeSnapshot(): ModuleRuntimeSnapshot {
  return {
    generatedAt: new Date(0).toISOString(),
    count: 0,
    modules: [],
  };
}

function normalizeModuleEntries(value: unknown): ModuleRuntimeEntry[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value
    .map((entry) => normalizeModuleEntry(entry))
    .filter((entry): entry is ModuleRuntimeEntry => Boolean(entry));
}

function normalizeModuleEntry(value: unknown): ModuleRuntimeEntry | null {
  const entry = asRecord(value);
  const id = pickTrimmedString(entry, 'id');
  if (!id) {
    return null;
  }
  return {
    id,
    version: pickTrimmedString(entry, 'version'),
    status: pickTrimmedString(entry, 'status') ?? 'unknown',
    statusMessage: pickTrimmedString(entry, 'statusMessage'),
    description: pickTrimmedString(entry, 'description'),
    manifest: normalizeManifest(entry?.manifest),
    stats: normalizeStats(entry?.stats),
    options: normalizeOptions(entry?.options),
    catalog: normalizeCatalog(entry?.catalog),
  };
}

function normalizeStats(value: unknown): ModuleRuntimeStats {
  const stats = asRecord(value);
  return {
    memoryPages: pickFiniteNumber(stats, 'memoryPages'),
    memoryBytes: pickFiniteNumber(stats, 'memoryBytes'),
    maxMemoryPages: pickFiniteNumber(stats, 'maxMemoryPages'),
    maxMemoryBytes: pickFiniteNumber(stats, 'maxMemoryBytes'),
    uptimeMs: pickFiniteNumber(stats, 'uptimeMs'),
  };
}

function normalizeManifest(value: unknown): ModuleRuntimeManifest | undefined {
  const manifest = asRecord(value);
  const pluginId = pickTrimmedString(manifest, 'pluginId');
  if (!pluginId) {
    return undefined;
  }
  return {
    pluginId,
    name: pickTrimmedString(manifest, 'name'),
    version: pickTrimmedString(manifest, 'version'),
    pluginFamily: pickTrimmedString(manifest, 'pluginFamily'),
    methods: normalizeMethods(manifest?.methods),
    capabilities: normalizeStringArray(manifest?.capabilities),
    protocols: normalizeProtocols(manifest?.protocols),
    timers: normalizeTimers(manifest?.timers),
  };
}

function normalizeMethods(value: unknown): ModuleRuntimeMethod[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.flatMap((method) => {
    const record = asRecord(method);
    const methodId = pickTrimmedString(record, 'methodId');
    if (!methodId) {
      return [];
    }
    return [{
      methodId,
      displayName: pickTrimmedString(record, 'displayName'),
      description: pickTrimmedString(record, 'description'),
    }];
  });
}

function normalizeProtocols(value: unknown): ModuleRuntimeProtocol[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.flatMap((protocol) => {
    const record = asRecord(protocol);
    const protocolId = pickTrimmedString(record, 'protocolId');
    if (!protocolId) {
      return [];
    }
    return [{
      protocolId,
      methodId: pickTrimmedString(record, 'methodId'),
      inputPortId: pickTrimmedString(record, 'inputPortId'),
      outputPortId: pickTrimmedString(record, 'outputPortId'),
      description: pickTrimmedString(record, 'description'),
      wireId: pickTrimmedString(record, 'wireId'),
      transportKind: pickTrimmedString(record, 'transportKind'),
      role: pickTrimmedString(record, 'role'),
      autoInstall: record?.autoInstall === true,
      advertise: record?.advertise === true,
      discoveryKey: pickTrimmedString(record, 'discoveryKey'),
    }];
  });
}

function normalizeTimers(value: unknown): ModuleRuntimeTimer[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.flatMap((timer) => {
    const record = asRecord(timer);
    const timerId = pickTrimmedString(record, 'timerId');
    if (!timerId) {
      return [];
    }
    return [{
      timerId,
      methodId: pickTrimmedString(record, 'methodId'),
      defaultIntervalMs: pickFiniteNumber(record, 'defaultIntervalMs'),
      description: pickTrimmedString(record, 'description'),
    }];
  });
}

function normalizeOptions(value: unknown): ModuleRuntimeOption[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.flatMap((option) => {
    const record = asRecord(option);
    const key = pickTrimmedString(record, 'key');
    const label = pickTrimmedString(record, 'label');
    const type = pickTrimmedString(record, 'type');
    if (!key || !label || !type) {
      return [];
    }
    return [{
      key,
      label,
      type,
      value: pickTrimmedString(record, 'value'),
      description: pickTrimmedString(record, 'description'),
      readOnly: record?.readOnly === true,
    }];
  });
}

function normalizeCatalog(value: unknown): ModuleRuntimeCatalog | undefined {
  const catalog = asRecord(value);
  if (!catalog) {
    return undefined;
  }
  return {
    requiredScope: pickTrimmedString(catalog, 'requiredScope'),
    contentType: pickTrimmedString(catalog, 'contentType'),
    cacheControl: pickTrimmedString(catalog, 'cacheControl'),
    bundleSha256: pickTrimmedString(catalog, 'bundleSha256'),
    sizeBytes: pickFiniteNumber(catalog, 'sizeBytes'),
    signatureHex: pickTrimmedString(catalog, 'signatureHex'),
    signerPubKeyHex: pickTrimmedString(catalog, 'signerPubKeyHex'),
    uploadedAt: pickTrimmedString(catalog, 'uploadedAt'),
  };
}

function normalizeStringArray(value: unknown): string[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value
    .filter((entry): entry is string => typeof entry === 'string')
    .map((entry) => entry.trim())
    .filter(Boolean);
}

function pickFiniteNumber(payload: Record<string, unknown> | null, key: string): number {
  const value = payload?.[key];
  if (typeof value !== 'number' || !Number.isFinite(value)) {
    return 0;
  }
  return Math.max(0, value);
}

function pickTrimmedString(
  payload: Record<string, unknown> | null,
  key: string,
): string | undefined {
  const value = payload?.[key];
  return typeof value === 'string' && value.trim() ? value.trim() : undefined;
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return Boolean(value) && typeof value === 'object' ? value as Record<string, unknown> : null;
}

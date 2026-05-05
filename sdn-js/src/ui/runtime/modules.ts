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
  hostRssBytes: number;
  invokeCount: number;
  errorCount: number;
  lastInvokeAt?: string;
  averageLatencyMs: number;
  timerRunCount: number;
  lastTimerStatus?: string;
}

export interface ModuleRuntimeMethod {
  methodId: string;
  displayName?: string;
  description?: string;
  inputPorts: ModuleRuntimePort[];
  outputPorts: ModuleRuntimePort[];
  maxBatch: number;
  drainPolicy?: string;
}

export interface ModuleRuntimePort {
  portId: string;
  displayName?: string;
  acceptedTypeSets: ModuleRuntimeAcceptedTypeSet[];
  minStreams: number;
  maxStreams: number;
  required: boolean;
  description?: string;
}

export interface ModuleRuntimeAcceptedTypeSet {
  setId?: string;
  allowedTypes: ModuleRuntimeTypeRef[];
  allowedWireFormats: string[];
  description?: string;
}

export interface ModuleRuntimeTypeRef {
  schemaName?: string;
  fileIdentifier?: string;
  schemaVersion?: string;
  rootType?: string;
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
  units?: string;
  min?: number;
  max?: number;
  defaultValue?: string;
  restartRequired: boolean;
  persistence?: string;
  mutable: boolean;
}

export interface ModuleRuntimeScheduleConfig {
  enabled: boolean;
  interval?: string;
  cronExpression?: string;
  timezone?: string;
  jitter?: string;
  backoff?: string;
  retryBudget?: number;
  maxRuntime?: string;
}

export interface ModuleRuntimeScheduleRun {
  id: string;
  methodId: string;
  trigger: string;
  startedAt: string;
  finishedAt?: string;
  status: string;
  message?: string;
  outputSize?: number;
}

export interface ModuleRuntimeSchedule extends ModuleRuntimeScheduleConfig {
  methodId: string;
  description?: string;
  interval: string;
  timezone: string;
  timezoneDisplay?: string;
  utcDisplay?: string;
  minInterval?: string;
  intervalPresets: string[];
  lastRunAt?: string;
  nextRunAt?: string;
  runHistory: ModuleRuntimeScheduleRun[];
}

export interface ModuleRuntimeAction {
  actionId: string;
  label: string;
  description?: string;
  enabled: boolean;
  destructive: boolean;
}

export interface ModuleRuntimeStatusEvent {
  status: string;
  message?: string;
  at?: string;
}

export interface ModuleRuntimeLinks {
  logsUrl?: string;
  eventsUrl?: string;
}

export interface ModuleRuntimeInputValue {
  methodId: string;
  portId: string;
  wireFormat?: string;
  encoding?: string;
  schemaName?: string;
  fileIdentifier?: string;
  schemaVersion?: string;
  rootType?: string;
  value?: string;
  updatedAt?: string;
}

export interface ModuleRuntimeCommandHistoryEntry {
  id: string;
  at: string;
  command: string;
  moduleId?: string;
  methodId?: string;
  portId?: string;
  status: string;
  summary?: string;
  inputValues: ModuleRuntimeInputValue[];
}

export interface SaveModuleRuntimeInputValuesResult {
  moduleId: string;
  restartPending: boolean;
  inputValues: ModuleRuntimeInputValue[];
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
  schedules: ModuleRuntimeSchedule[];
  actions: ModuleRuntimeAction[];
  statusHistory: ModuleRuntimeStatusEvent[];
  links?: ModuleRuntimeLinks;
  catalog?: ModuleRuntimeCatalog;
  inputValues: ModuleRuntimeInputValue[];
  restartPending: boolean;
  commandHistory: ModuleRuntimeCommandHistoryEntry[];
}

export interface ModuleRuntimeSnapshot {
  generatedAt: string;
  count: number;
  modules: ModuleRuntimeEntry[];
}

export async function loadModuleRuntimeSnapshotFromServer(
  baseUrl: string,
  fetchImpl: ModuleRuntimeFetchLike = globalThis.fetch.bind(
    globalThis,
  ) as ModuleRuntimeFetchLike,
): Promise<ModuleRuntimeSnapshot> {
  const normalizedBaseUrl = baseUrl.replace(/\/+$/, '');
  const response = await fetchImpl(
    `${normalizedBaseUrl}/api/v1/modules/runtime`,
    {
      credentials: 'include',
    },
  );
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
    generatedAt:
      pickTrimmedString(payload, 'generatedAt') ?? new Date().toISOString(),
    count: modules.length,
    modules,
  };
}

export async function updateModuleRuntimeOption(
  baseUrl: string,
  moduleId: string,
  optionKey: string,
  value: string,
  fetchImpl: ModuleRuntimeFetchLike = globalThis.fetch.bind(
    globalThis,
  ) as ModuleRuntimeFetchLike,
): Promise<ModuleRuntimeOption> {
  const normalizedBaseUrl = baseUrl.replace(/\/+$/, '');
  const response = await fetchImpl(
    `${normalizedBaseUrl}/api/v1/modules/runtime/${encodeURIComponent(moduleId)}/options/${encodeURIComponent(optionKey)}`,
    {
      method: 'PATCH',
      credentials: 'include',
      headers: {
        'content-type': 'application/json',
        'x-requested-with': 'XMLHttpRequest',
      },
      body: JSON.stringify({ value }),
    },
  );
  if (!response.ok) {
    throw new Error(`module option update failed (${response.status})`);
  }
  const option = normalizeOption(await response.json());
  if (!option) {
    throw new Error('module option update returned an invalid option');
  }
  return option;
}

export async function saveModuleRuntimeInputValues(
  baseUrl: string,
  moduleId: string,
  values: ModuleRuntimeInputValue[],
  fetchImpl: ModuleRuntimeFetchLike = globalThis.fetch.bind(
    globalThis,
  ) as ModuleRuntimeFetchLike,
): Promise<SaveModuleRuntimeInputValuesResult> {
  const normalizedBaseUrl = baseUrl.replace(/\/+$/, '');
  const response = await fetchImpl(
    `${normalizedBaseUrl}/api/v1/modules/runtime/${encodeURIComponent(moduleId)}/inputs`,
    {
      method: 'PATCH',
      credentials: 'include',
      headers: {
        'content-type': 'application/json',
        'x-requested-with': 'XMLHttpRequest',
      },
      body: JSON.stringify({ values }),
    },
  );
  if (!response.ok) {
    throw new Error(`module input update failed (${response.status})`);
  }
  const payload = asRecord(await response.json());
  return {
    moduleId: pickTrimmedString(payload, 'moduleId') ?? moduleId,
    restartPending: payload?.restartPending === true,
    inputValues: normalizeInputValues(payload?.inputValues),
  };
}

export async function saveModuleRuntimeSchedule(
  baseUrl: string,
  moduleId: string,
  methodId: string,
  config: ModuleRuntimeScheduleConfig,
  fetchImpl: ModuleRuntimeFetchLike = globalThis.fetch.bind(
    globalThis,
  ) as ModuleRuntimeFetchLike,
): Promise<ModuleRuntimeSchedule> {
  const normalizedBaseUrl = baseUrl.replace(/\/+$/, '');
  const response = await fetchImpl(
    `${normalizedBaseUrl}/api/v1/modules/runtime/${encodeURIComponent(moduleId)}/schedules/${encodeURIComponent(methodId)}`,
    {
      method: 'PATCH',
      credentials: 'include',
      headers: {
        'content-type': 'application/json',
        'x-requested-with': 'XMLHttpRequest',
      },
      body: JSON.stringify(config),
    },
  );
  if (!response.ok) {
    throw new Error(`module schedule update failed (${response.status})`);
  }
  const schedule = normalizeSchedule(await response.json());
  if (!schedule) {
    throw new Error('module schedule update returned an invalid schedule');
  }
  return schedule;
}

export async function runModuleRuntimeScheduleNow(
  baseUrl: string,
  moduleId: string,
  methodId: string,
  fetchImpl: ModuleRuntimeFetchLike = globalThis.fetch.bind(
    globalThis,
  ) as ModuleRuntimeFetchLike,
): Promise<ModuleRuntimeScheduleRun> {
  const normalizedBaseUrl = baseUrl.replace(/\/+$/, '');
  const response = await fetchImpl(
    `${normalizedBaseUrl}/api/v1/modules/runtime/${encodeURIComponent(moduleId)}/schedules/${encodeURIComponent(methodId)}/run`,
    {
      method: 'POST',
      credentials: 'include',
      headers: {
        'x-requested-with': 'XMLHttpRequest',
      },
    },
  );
  if (!response.ok) {
    throw new Error(`module schedule run failed (${response.status})`);
  }
  const run = normalizeScheduleRuns([await response.json()])[0];
  if (!run) {
    throw new Error('module schedule run returned an invalid result');
  }
  return run;
}

export async function runModuleRuntimeAction(
  baseUrl: string,
  moduleId: string,
  actionId: string,
  fetchImpl: ModuleRuntimeFetchLike = globalThis.fetch.bind(
    globalThis,
  ) as ModuleRuntimeFetchLike,
): Promise<{ ok: boolean; actionId: string }> {
  const normalizedBaseUrl = baseUrl.replace(/\/+$/, '');
  const response = await fetchImpl(
    `${normalizedBaseUrl}/api/v1/modules/runtime/${encodeURIComponent(moduleId)}/actions/${encodeURIComponent(actionId)}`,
    {
      method: 'POST',
      credentials: 'include',
      headers: {
        'x-requested-with': 'XMLHttpRequest',
      },
    },
  );
  if (!response.ok) {
    throw new Error(`module action failed (${response.status})`);
  }
  const payload = asRecord(await response.json());
  return {
    ok: payload?.ok === true,
    actionId: pickTrimmedString(payload, 'actionId') ?? actionId,
  };
}

export function emptyModuleRuntimeSnapshot(): ModuleRuntimeSnapshot {
  return {
    generatedAt: new Date(0).toISOString(),
    count: 0,
    modules: [],
  };
}

export function resolveSelectedModuleId(
  selectedId: string,
  modules: Array<Pick<ModuleRuntimeEntry, 'id'>>,
): string {
  if (modules.length === 0) {
    return '';
  }
  const normalizedSelectedId = selectedId.trim();
  if (
    normalizedSelectedId &&
    modules.some((module) => module.id === normalizedSelectedId)
  ) {
    return normalizedSelectedId;
  }
  return modules[0]?.id ?? '';
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
    schedules: normalizeSchedules(entry?.schedules),
    actions: normalizeActions(entry?.actions),
    statusHistory: normalizeStatusHistory(entry?.statusHistory),
    links: normalizeLinks(entry?.links),
    catalog: normalizeCatalog(entry?.catalog),
    inputValues: normalizeInputValues(entry?.inputValues),
    restartPending: entry?.restartPending === true,
    commandHistory: normalizeCommandHistory(entry?.commandHistory),
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
    hostRssBytes: pickFiniteNumber(stats, 'hostRssBytes'),
    invokeCount: pickFiniteNumber(stats, 'invokeCount'),
    errorCount: pickFiniteNumber(stats, 'errorCount'),
    lastInvokeAt: pickTrimmedString(stats, 'lastInvokeAt'),
    averageLatencyMs: pickFiniteNumber(stats, 'averageLatencyMs'),
    timerRunCount: pickFiniteNumber(stats, 'timerRunCount'),
    lastTimerStatus: pickTrimmedString(stats, 'lastTimerStatus'),
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
    return [
      {
        methodId,
        displayName: pickTrimmedString(record, 'displayName'),
        description: pickTrimmedString(record, 'description'),
        inputPorts: normalizePorts(record?.inputPorts),
        outputPorts: normalizePorts(record?.outputPorts),
        maxBatch: pickFiniteNumber(record, 'maxBatch'),
        drainPolicy: pickTrimmedString(record, 'drainPolicy'),
      },
    ];
  });
}

function normalizePorts(value: unknown): ModuleRuntimePort[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.flatMap((port) => {
    const record = asRecord(port);
    const portId = pickTrimmedString(record, 'portId');
    if (!portId) {
      return [];
    }
    return [
      {
        portId,
        displayName: pickTrimmedString(record, 'displayName'),
        acceptedTypeSets: normalizeAcceptedTypeSets(record?.acceptedTypeSets),
        minStreams: pickFiniteNumber(record, 'minStreams'),
        maxStreams: pickFiniteNumber(record, 'maxStreams'),
        required: record?.required === true,
        description: pickTrimmedString(record, 'description'),
      },
    ];
  });
}

function normalizeAcceptedTypeSets(
  value: unknown,
): ModuleRuntimeAcceptedTypeSet[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.flatMap((set) => {
    const record = asRecord(set);
    return [
      {
        setId: pickTrimmedString(record, 'setId'),
        allowedTypes: normalizeTypeRefs(record?.allowedTypes),
        allowedWireFormats: normalizeStringArray(record?.allowedWireFormats),
        description: pickTrimmedString(record, 'description'),
      },
    ];
  });
}

function normalizeTypeRefs(value: unknown): ModuleRuntimeTypeRef[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.flatMap((typeRef) => {
    const record = asRecord(typeRef);
    const normalized = {
      schemaName: pickTrimmedString(record, 'schemaName'),
      fileIdentifier: pickTrimmedString(record, 'fileIdentifier'),
      schemaVersion: pickTrimmedString(record, 'schemaVersion'),
      rootType: pickTrimmedString(record, 'rootType'),
    };
    if (
      !normalized.schemaName &&
      !normalized.fileIdentifier &&
      !normalized.schemaVersion &&
      !normalized.rootType
    ) {
      return [];
    }
    return [normalized];
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
    return [
      {
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
      },
    ];
  });
}

function normalizeInputValues(value: unknown): ModuleRuntimeInputValue[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.flatMap((inputValue) => {
    const record = asRecord(inputValue);
    const methodId = pickTrimmedString(record, 'methodId');
    const portId = pickTrimmedString(record, 'portId');
    if (!methodId || !portId) {
      return [];
    }
    return [
      {
        methodId,
        portId,
        wireFormat: pickTrimmedString(record, 'wireFormat'),
        encoding: pickTrimmedString(record, 'encoding'),
        schemaName: pickTrimmedString(record, 'schemaName'),
        fileIdentifier: pickTrimmedString(record, 'fileIdentifier'),
        schemaVersion: pickTrimmedString(record, 'schemaVersion'),
        rootType: pickTrimmedString(record, 'rootType'),
        value: pickTrimmedString(record, 'value'),
        updatedAt: pickTrimmedString(record, 'updatedAt'),
      },
    ];
  });
}

function normalizeCommandHistory(value: unknown): ModuleRuntimeCommandHistoryEntry[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.flatMap((entryValue) => {
    const record = asRecord(entryValue);
    const id = pickTrimmedString(record, 'id');
    const at = pickTrimmedString(record, 'at');
    const command = pickTrimmedString(record, 'command');
    const status = pickTrimmedString(record, 'status');
    if (!id || !at || !command || !status) {
      return [];
    }
    return [
      {
        id,
        at,
        command,
        moduleId: pickTrimmedString(record, 'moduleId'),
        methodId: pickTrimmedString(record, 'methodId'),
        portId: pickTrimmedString(record, 'portId'),
        status,
        summary: pickTrimmedString(record, 'summary'),
        inputValues: normalizeInputValues(record?.inputValues),
      },
    ];
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
    return [
      {
        timerId,
        methodId: pickTrimmedString(record, 'methodId'),
        defaultIntervalMs: pickFiniteNumber(record, 'defaultIntervalMs'),
        description: pickTrimmedString(record, 'description'),
      },
    ];
  });
}

function normalizeOptions(value: unknown): ModuleRuntimeOption[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.flatMap((option) => normalizeOption(option) ?? []);
}

function normalizeSchedules(value: unknown): ModuleRuntimeSchedule[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.flatMap((schedule) => normalizeSchedule(schedule) ?? []);
}

function normalizeSchedule(value: unknown): ModuleRuntimeSchedule | null {
  const record = asRecord(value);
  const methodId = pickTrimmedString(record, 'methodId');
  const interval = pickTrimmedString(record, 'interval');
  if (!methodId || !interval) {
    return null;
  }
  return {
    methodId,
    description: pickTrimmedString(record, 'description'),
    enabled: record?.enabled === true,
    interval,
    cronExpression: pickTrimmedString(record, 'cronExpression'),
    timezone: pickTrimmedString(record, 'timezone') ?? 'UTC',
    timezoneDisplay: pickTrimmedString(record, 'timezoneDisplay'),
    utcDisplay: pickTrimmedString(record, 'utcDisplay'),
    jitter: pickTrimmedString(record, 'jitter'),
    backoff: pickTrimmedString(record, 'backoff'),
    retryBudget: pickOptionalFiniteNumber(record, 'retryBudget'),
    maxRuntime: pickTrimmedString(record, 'maxRuntime'),
    minInterval: pickTrimmedString(record, 'minInterval'),
    intervalPresets: normalizeStringArray(record?.intervalPresets),
    lastRunAt: pickTrimmedString(record, 'lastRunAt'),
    nextRunAt: pickTrimmedString(record, 'nextRunAt'),
    runHistory: normalizeScheduleRuns(record?.runHistory),
  };
}

function normalizeScheduleRuns(value: unknown): ModuleRuntimeScheduleRun[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.flatMap((run) => {
    const record = asRecord(run);
    const id = pickTrimmedString(record, 'id');
    const methodId = pickTrimmedString(record, 'methodId');
    const trigger = pickTrimmedString(record, 'trigger');
    const startedAt = pickTrimmedString(record, 'startedAt');
    const status = pickTrimmedString(record, 'status');
    if (!id || !methodId || !trigger || !startedAt || !status) {
      return [];
    }
    return [
      {
        id,
        methodId,
        trigger,
        startedAt,
        finishedAt: pickTrimmedString(record, 'finishedAt'),
        status,
        message: pickTrimmedString(record, 'message'),
        outputSize: pickOptionalFiniteNumber(record, 'outputSize'),
      },
    ];
  });
}

function normalizeOption(value: unknown): ModuleRuntimeOption | null {
  const record = asRecord(value);
  const key = pickTrimmedString(record, 'key');
  const label = pickTrimmedString(record, 'label');
  const type = pickTrimmedString(record, 'type');
  if (!key || !label || !type) {
    return null;
  }
  const min = pickOptionalFiniteNumber(record, 'min');
  const max = pickOptionalFiniteNumber(record, 'max');
  return {
    key,
    label,
    type,
    value: pickTrimmedString(record, 'value'),
    description: pickTrimmedString(record, 'description'),
    readOnly: record?.readOnly === true,
    units: pickTrimmedString(record, 'units'),
    min,
    max,
    defaultValue: pickTrimmedString(record, 'defaultValue'),
    restartRequired: record?.restartRequired === true,
    persistence: pickTrimmedString(record, 'persistence'),
    mutable: record?.mutable === true || record?.readOnly !== true,
  };
}

function normalizeActions(value: unknown): ModuleRuntimeAction[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.flatMap((action) => {
    const record = asRecord(action);
    const actionId = pickTrimmedString(record, 'actionId');
    const label = pickTrimmedString(record, 'label');
    if (!actionId || !label) {
      return [];
    }
    return [
      {
        actionId,
        label,
        description: pickTrimmedString(record, 'description'),
        enabled: record?.enabled === true,
        destructive: record?.destructive === true,
      },
    ];
  });
}

function normalizeStatusHistory(value: unknown): ModuleRuntimeStatusEvent[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.flatMap((event) => {
    const record = asRecord(event);
    const status = pickTrimmedString(record, 'status');
    if (!status) {
      return [];
    }
    return [
      {
        status,
        message: pickTrimmedString(record, 'message'),
        at: pickTrimmedString(record, 'at'),
      },
    ];
  });
}

function normalizeLinks(value: unknown): ModuleRuntimeLinks | undefined {
  const links = asRecord(value);
  if (!links) {
    return undefined;
  }
  const out = {
    logsUrl: pickTrimmedString(links, 'logsUrl'),
    eventsUrl: pickTrimmedString(links, 'eventsUrl'),
  };
  return out.logsUrl || out.eventsUrl ? out : undefined;
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

function pickFiniteNumber(
  payload: Record<string, unknown> | null,
  key: string,
): number {
  const value = payload?.[key];
  if (typeof value !== 'number' || !Number.isFinite(value)) {
    return 0;
  }
  return Math.max(0, value);
}

function pickOptionalFiniteNumber(
  payload: Record<string, unknown> | null,
  key: string,
): number | undefined {
  const value = payload?.[key];
  if (typeof value !== 'number' || !Number.isFinite(value)) {
    return undefined;
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
  return Boolean(value) && typeof value === 'object'
    ? (value as Record<string, unknown>)
    : null;
}

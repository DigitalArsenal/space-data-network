<script lang="ts">
  import { decodeEpmFlatBuffer } from '../../../src/ui/runtime/epm-flatbuffer';
  import {
    loadDataDirectoryState,
    migrateSchemaSyncPreferencesToDataDirectory,
    persistDataDirectoryState,
    updateDataFeedSubscription,
    type DataDirectoryState,
    type DataDirectoryMigrationSource,
    type DataFeedSubscription,
  } from '../../../src/ui/runtime/data-directory';
  import {
    clearLocalFlatSqlStore,
    type LocalFlatSqlQueryResult,
    type LocalFlatSqlStandardStats,
  } from '../../../src/ui/runtime/local-flatsql';
  import {
    createWorkerLocalFlatSqlStore,
    type WorkerFlatSqlSyncBackendConfig,
    type WorkerLocalFlatSqlStore,
    type WorkerSchemaSyncUpdate,
  } from '../../../src/ui/runtime/local-flatsql-worker-client';
  import { decodeOmmFlatBuffer } from '../../../src/ui/runtime/omm-flatbuffer';
  import { decodePnmFlatBuffer } from '../../../src/ui/runtime/pnm-flatbuffer';
  import { createSchemaSyncScheduler } from '../lib/schema-sync-scheduler';
  import {
    effectiveSchemaSyncStatus,
    schemaSyncStatusLabel as formatSchemaSyncStatusLabel,
  } from '../lib/schema-sync-labels';
  import type {
    DataScanResult,
    DataSummary,
    ObservedSdnPeer,
    RawDataRecord,
    SdnBackend,
  } from '../../../src/ui/runtime/sdn-backend';
  import { verify as verifyEd25519Signature } from '../../../src/crypto/hd-wallet';
  import CAT_SCHEMA from '../../../node_modules/spacedatastandards.org/schema/CAT/main.fbs?raw';
  import EPM_SCHEMA from '../../../node_modules/spacedatastandards.org/schema/EPM/main.fbs?raw';
  import MPE_SCHEMA from '../../../node_modules/spacedatastandards.org/schema/MPE/main.fbs?raw';
  import OMM_SCHEMA from '../../../node_modules/spacedatastandards.org/schema/OMM/main.fbs?raw';
  import PNM_SCHEMA from '../../../node_modules/spacedatastandards.org/schema/PNM/main.fbs?raw';
  import SPW_SCHEMA from '../../../node_modules/spacedatastandards.org/schema/SPW/main.fbs?raw';

  type SortColumn = string;
  type SortDirection = 'asc' | 'desc';
  type ColumnSource = 'metadata' | 'standard';
  type SchemaSyncMode = 'preview' | 'sync';
  type SchemaSyncStatus = 'idle' | 'syncing' | 'synced' | 'capped' | 'error';
  type StorageUnit = 'MB' | 'GB' | 'TB';

  interface WorkbenchColumn {
    key: SortColumn;
    label: string;
    source: ColumnSource;
  }

  interface WorkbenchRow {
    record: RawDataRecord;
    decoded: Record<string, unknown>;
  }

  interface ConfiguredSdnNode {
    id: string;
    name: string;
    addrs: string[];
    trust_level?: string;
    trustLevel?: string;
    metadata?: Record<string, unknown>;
  }

  interface DataSourceOption {
    id: string;
    label: string;
    detail: string;
    peerId: string | null;
    publicKey: string | null;
    kind: 'local' | 'configured';
    syncAddrs?: string[];
    searchText: string;
  }

  interface StandardOption {
    id: string;
    remoteRows: number;
  }

  interface SchemaSyncPreference {
    mode: SchemaSyncMode;
    storageCap: number;
    storageUnit: StorageUnit;
  }

  interface SchemaSyncProgress {
    status: SchemaSyncStatus;
    syncedRows: number;
    totalRows: number;
    localRows: number;
    cachedBytes: number;
    pinnedBytes: number;
    providerPeerId: string | null;
    providerPublicKey: string | null;
    snapshotId: string | null;
    head: string | null;
    cursor: string | null;
    nextCursor: string | null;
    highWaterMark: string | null;
    queryProfile: string | null;
    chunkHash: string | null;
    syncProtocol: string | null;
    verifiedChunks: string[];
    lastSyncedAt: string | null;
    error: string | null;
  }

  interface SchemaSyncRow extends StandardOption {
    subscriptionId: string;
    dataSourceId: string;
    providerName: string;
    providerPeerId: string | null;
    providerPublicKey: string | null;
    syncFilter: string;
    localRows: number;
    cachedBytes: number;
    preference: SchemaSyncPreference;
    progress: SchemaSyncProgress;
  }

  export let backend: SdnBackend | null = null;
  export let peers: ObservedSdnPeer[] = [];
  export let route = '/data';

  const DEFAULT_STANDARD_ID = 'EPM';
  const LOCAL_DATA_SOURCE_ID = 'local';
  const SCHEMA_EXTENSION = 'fbs';
  const DEFAULT_PAGE_SIZE = 10;
  const SYNC_PAGE_SIZE = 50_000;
  const SYNC_PERSIST_RECORD_INTERVAL = 100_000;
  const PAGE_SIZE_OPTIONS = [10, 25, 50, 100];
  const SCHEMA_SYNC_STORAGE_KEY = 'sdn:data-schema-sync:v1';
  const SCHEMA_SYNC_STATE_STORAGE_KEY = 'sdn:data-schema-sync-state:v1';
  const STORAGE_CAP_UNITS: StorageUnit[] = ['MB', 'GB', 'TB'];
  const DEFAULT_SCHEMA_SYNC_PREFERENCE: SchemaSyncPreference = {
    mode: 'preview',
    storageCap: 1,
    storageUnit: 'GB',
  };
  const LOCAL_FLATSQL_SCHEMAS = [
    { standardId: 'CAT', tableName: 'CAT', fileId: '$CAT', schema: CAT_SCHEMA },
    { standardId: 'EPM', tableName: 'EPM', fileId: '$EPM', schema: EPM_SCHEMA },
    { standardId: 'MPE', tableName: 'MPE', fileId: '$MPE', schema: MPE_SCHEMA },
    { standardId: 'OMM', tableName: 'OMM', fileId: '$OMM', schema: OMM_SCHEMA },
    { standardId: 'PNM', tableName: 'PNM', fileId: '$PNM', schema: PNM_SCHEMA },
    { standardId: 'SPW', tableName: 'SPW', fileId: '$SPW', schema: SPW_SCHEMA },
  ];
  const METADATA_COLUMNS: WorkbenchColumn[] = [
    { key: 'schemaName', label: 'Message', source: 'metadata' },
    { key: 'cid', label: 'CID', source: 'metadata' },
    { key: 'peerId', label: 'Peer', source: 'metadata' },
    { key: 'providerId', label: 'Producer', source: 'metadata' },
    { key: 'sourceName', label: 'Source', source: 'metadata' },
    { key: 'batchId', label: 'Batch', source: 'metadata' },
    { key: 'timestamp', label: 'Timestamp', source: 'metadata' },
  ];
  const INTERNAL_COLUMN_KEYS = new Set(['bytes', 'dataBytes', 'data_base64', 'sizeBytes']);
  const INTERNAL_SQL_COLUMN_KEYS = new Set(['_data', '_offset', '_source', '_rowid']);
  const EPM_STANDARD_COLUMNS: WorkbenchColumn[] = [
    { key: 'dn', label: 'Display name', source: 'standard' },
    { key: 'legal_name', label: 'Legal name', source: 'standard' },
    { key: 'family_name', label: 'Family name', source: 'standard' },
    { key: 'given_name', label: 'Given name', source: 'standard' },
    { key: 'additional_name', label: 'Additional name', source: 'standard' },
    { key: 'honorific_prefix', label: 'Honorific prefix', source: 'standard' },
    { key: 'honorific_suffix', label: 'Honorific suffix', source: 'standard' },
    { key: 'job_title', label: 'Job title', source: 'standard' },
    { key: 'occupation', label: 'Occupation', source: 'standard' },
    { key: 'email', label: 'Email', source: 'standard' },
    { key: 'telephone', label: 'Telephone', source: 'standard' },
    { key: 'entity_type', label: 'Entity type', source: 'standard' },
    { key: 'directory_kind', label: 'Directory kind', source: 'standard' },
    { key: 'peer_id', label: 'EPM Peer ID', source: 'standard' },
    { key: 'signing_public_key', label: 'Signing public key', source: 'standard' },
    { key: 'encryption_public_key', label: 'Encryption public key', source: 'standard' },
    { key: 'alternate_names', label: 'Alternate names', source: 'standard' },
    { key: 'multiformat_address', label: 'Multiformat address', source: 'standard' },
    { key: 'keys', label: 'Keys', source: 'standard' },
    { key: 'signature', label: 'Signature', source: 'standard' },
    { key: 'signature_timestamp', label: 'Signature timestamp', source: 'standard' },
  ];
  const OMM_STANDARD_COLUMNS: WorkbenchColumn[] = [
    { key: 'OBJECT_NAME', label: 'Object name', source: 'standard' },
    { key: 'OBJECT_ID', label: 'Object ID', source: 'standard' },
    { key: 'NORAD_CAT_ID', label: 'NORAD catalog ID', source: 'standard' },
    { key: 'EPOCH', label: 'Epoch', source: 'standard' },
    { key: 'MEAN_MOTION', label: 'Mean motion', source: 'standard' },
    { key: 'ECCENTRICITY', label: 'Eccentricity', source: 'standard' },
    { key: 'INCLINATION', label: 'Inclination', source: 'standard' },
    { key: 'RA_OF_ASC_NODE', label: 'RA ascending node', source: 'standard' },
    { key: 'ARG_OF_PERICENTER', label: 'Argument of pericenter', source: 'standard' },
    { key: 'MEAN_ANOMALY', label: 'Mean anomaly', source: 'standard' },
    { key: 'BSTAR', label: 'BSTAR', source: 'standard' },
    { key: 'MEAN_ELEMENT_THEORY', label: 'Mean element theory', source: 'standard' },
    { key: 'TIME_SYSTEM', label: 'Time system', source: 'standard' },
    { key: 'EPHEMERIS_TYPE', label: 'Ephemeris type', source: 'standard' },
    { key: 'CLASSIFICATION_TYPE', label: 'Classification', source: 'standard' },
    { key: 'ORIGINATOR', label: 'Originator', source: 'standard' },
    { key: 'CREATION_DATE', label: 'Creation date', source: 'standard' },
    { key: 'CENTER_NAME', label: 'Center', source: 'standard' },
  ];
  const PNM_STANDARD_COLUMNS: WorkbenchColumn[] = [
    { key: 'FILE_ID', label: 'FILE_ID', source: 'standard' },
    { key: 'CID', label: 'CID', source: 'standard' },
    { key: 'FILE_NAME', label: 'FILE_NAME', source: 'standard' },
    { key: 'PUBLISH_TIMESTAMP', label: 'PUBLISH_TIMESTAMP', source: 'standard' },
    { key: 'MULTIFORMAT_ADDRESS', label: 'MULTIFORMAT_ADDRESS', source: 'standard' },
    { key: 'SIGNATURE', label: 'SIGNATURE', source: 'standard' },
    { key: 'SIGNATURE_TYPE', label: 'SIGNATURE_TYPE', source: 'standard' },
    { key: 'TIMESTAMP_SIGNATURE', label: 'TIMESTAMP_SIGNATURE', source: 'standard' },
    { key: 'TIMESTAMP_SIGNATURE_TYPE', label: 'TIMESTAMP_SIGNATURE_TYPE', source: 'standard' },
  ];
  const STANDARD_FIELD_COLUMNS: Record<string, WorkbenchColumn[]> = {
    EPM: EPM_STANDARD_COLUMNS,
    OMM: OMM_STANDARD_COLUMNS,
    PNM: PNM_STANDARD_COLUMNS,
  };

  let dataSummary: DataSummary | null = null;
  let selectedStandardId = DEFAULT_STANDARD_ID;
  let selectedDataSourceId = LOCAL_DATA_SOURCE_ID;
  let lastColumnStandardId = '';
  let columnMenuOpen = false;
  let visibleColumnKeys: string[] = [];
  let searchText = '';
  let pageSize = DEFAULT_PAGE_SIZE;
  let pageIndex = 0;
  let sortColumn: SortColumn = 'timestamp';
  let sortDirection: SortDirection = 'desc';
  let rawRecords: RawDataRecord[] = [];
  let dataScan: DataScanResult | null = null;
  let workbenchLoading = false;
  let lastBackend: SdnBackend | null = null;
  let configuredDataSources: ConfiguredSdnNode[] = [];
  let userSelectedDataSource = false;
  let userSelectedStandard = false;
  let inspectCid = '';
  let inspectGatewayUrl = '';
  let localFlatSqlStore: WorkerLocalFlatSqlStore | null = null;
  let localFlatSqlStoreKey = '';
  let localFlatSqlStorePromise: Promise<WorkerLocalFlatSqlStore | null> | null = null;
  let localFlatSqlStorePromiseKey = '';
  let localFlatSqlStats: LocalFlatSqlStandardStats[] = [];
  let sqlQueryText = defaultSqlQuery(DEFAULT_STANDARD_ID);
  let sqlResult: LocalFlatSqlQueryResult | null = null;
  let sqlError = '';
  let sqlRunning = false;
  let userEditedSql = false;
  let userEditedColumns = false;
  let dataDirectoryState: DataDirectoryState = loadDataDirectoryState();
  let schemaSyncPreferences: Record<string, SchemaSyncPreference> = loadSchemaSyncPreferences();
  let schemaSyncProgress: Record<string, SchemaSyncProgress> = loadSchemaSyncProgress();
  let activeSyncKeys = new Set<string>();
  let selectedPnmRow: WorkbenchRow | null = null;
  let pnmFileIdQuery = '';
  let pnmQueryResult: LocalFlatSqlQueryResult | null = null;
  let pnmQueryError = '';
  let pnmSignatureStatus = '';
  let pnmSignatureRunning = false;
  let resetSubscriptionId = '';
  let resetConfirmText = '';
  let resetStatus = '';
  let resetRunning = false;
  const schemaSyncScheduler = createSchemaSyncScheduler({
    syncSchema: (standardId, dataSourceId) => synchronizeSchema(standardId, dataSourceId),
  });
  const schemaSyncSchedulers = new Map<string, typeof schemaSyncScheduler>([[LOCAL_DATA_SOURCE_ID, schemaSyncScheduler]]);

  $: dataSourceOptions = buildDataSourceOptions(backend, configuredDataSources, peers);
  $: schemaSyncRows = buildSubscribedSchemaSyncRows(dataDirectoryState.subscriptions, selectedDataSourceId, localFlatSqlStats);
  $: decodedRows = rawRecords.map(decodeWorkbenchRecord);
  $: allColumns = workbenchColumnsForStandard(selectedStandardId, decodedRows);
  $: syncVisibleColumnKeys(allColumns);
  $: visibleColumns = allColumns.filter((column) => visibleColumnKeys.includes(column.key));
  $: filteredRows = filterRows(decodedRows, searchText);
  $: visibleRows = sortRows(filteredRows, sortColumn, sortDirection);
  $: estimatedTotalRows = scanTotalRowsForStandard(dataScan, selectedStandardId) ?? totalRowsForStandardId(dataSummary, selectedStandardId);
  $: totalPageCount = estimatedTotalRows === null ? Math.max(1, pageIndex + (canGoNext ? 2 : 1)) : Math.max(1, Math.ceil(estimatedTotalRows / normalizedPageSize()));
  $: canGoPrevious = pageIndex > 0;
  $: canGoNext = rawRecords.length >= pageSize && (estimatedTotalRows === null || ((pageIndex + 1) * pageSize) < estimatedTotalRows);
  $: pageLabel = `${pageIndex + 1}/${totalPageCount}`;
  $: selectedLocalFlatSqlStats = localFlatSqlStats.find((entry) => entry.standardId === selectedStandardId) ?? null;
  $: localRowCount = localRowsForStandard(localFlatSqlStats, selectedStandardId);
  $: cachedByteCount = selectedLocalFlatSqlStats?.cachedBytes ?? 0;
  $: scanHashLabel = dataScan?.scanHash ? shorten(dataScan.scanHash, 18) : 'none';
  $: sqlColumns = sqlResult?.columns ?? [];
  $: displaySqlColumns = visibleSqlColumns(sqlColumns, sqlRecords);
  $: sqlRecords = sqlResult?.records ?? [];
  $: pnmQueryColumns = visibleSqlColumns(pnmQueryResult?.columns ?? [], pnmQueryRows);
  $: pnmQueryRows = pnmQueryResult?.records ?? [];
  $: selectedPnmDetails = selectedPnmRow?.decoded ?? {};

  $: if (backend && backend !== lastBackend) {
    lastBackend = backend;
    void initializeDataExplorer();
  }

  $: scheduleSubscribedSchemaSyncs(schemaSyncRows);

  $: syncInspectRoute(route, backend);

  $: if (dataSourceOptions.length > 0 && !dataSourceOptions.some((source) => source.id === selectedDataSourceId)) {
    selectedDataSourceId = dataSourceOptions[0].id;
  }

  async function initializeDataExplorer(): Promise<void> {
    resetSchemaSyncSchedulers();
    configuredDataSources = [];
    dataDirectoryState = loadDataDirectoryState();
    selectedDataSourceId = LOCAL_DATA_SOURCE_ID;
    userSelectedDataSource = false;
    userSelectedStandard = false;
    await loadConfiguredDataSources();
    dataDirectoryState = migrateSchemaSyncPreferencesToDataDirectory(
      dataDirectoryState,
      schemaSyncPreferences,
      dataDirectoryMigrationSources(buildDataSourceOptions(backend, configuredDataSources, peers)),
      schemaSyncProgress,
    );
    persistDataDirectoryState(dataDirectoryState);
    if (!userSelectedDataSource) {
      selectedDataSourceId = preferredSubscribedDataSourceId(dataDirectoryState.subscriptions)
        ?? preferredDataSourceId(buildDataSourceOptions(backend, configuredDataSources, peers));
    }
    await initializeWorkbench();
  }

  async function initializeWorkbench(): Promise<void> {
    await loadDataSummary();
    await runWorkbenchQuery(0);
  }

  async function loadConfiguredDataSources(): Promise<void> {
    if (typeof fetch !== 'function') {
      configuredDataSources = [];
      return;
    }
    try {
      const response = await fetch('/api/local/sdn-nodes', {
        headers: { accept: 'application/json' },
      });
      if (!response.ok) {
        configuredDataSources = [];
        return;
      }
      configuredDataSources = normalizeConfiguredDataSources(await response.json());
    } catch {
      configuredDataSources = [];
    }
  }

  async function loadDataSummary(): Promise<void> {
    const source = currentDataSourceOption();
    const workerBackendConfig = backendConfigForDataSource(source);
    if (workerBackendConfig) {
      try {
        const store = await ensureLocalFlatSqlStore();
        dataSummary = store ? await store.getRemoteDataSummary(workerBackendConfig) : null;
        const nextStandardOptions = standardIdsFromSummary(dataSummary);
        const previousStandardId = selectedStandardId;
        if (!userSelectedStandard || !nextStandardOptions.includes(selectedStandardId)) {
          selectedStandardId = preferredStandardIdFromSummary(dataSummary);
        }
        if (previousStandardId !== selectedStandardId && !userEditedSql) {
          resetSqlForSelectedStandard();
        }
      } catch {
        dataSummary = null;
      }
      return;
    }

    const activeBackend = backendForSelectedDataSource();
    if (!activeBackend) {
      dataSummary = null;
      dataScan = null;
      return;
    }
    try {
      const result = await activeBackend.getDataSummary();
      dataSummary = result.data;
      const nextStandardOptions = standardIdsFromSummary(result.data);
      const previousStandardId = selectedStandardId;
      if (!userSelectedStandard || !nextStandardOptions.includes(selectedStandardId)) {
        selectedStandardId = preferredStandardIdFromSummary(result.data);
      }
      if (previousStandardId !== selectedStandardId && !userEditedSql) {
        resetSqlForSelectedStandard();
      }
    } catch {
      dataSummary = null;
    }
  }

  async function runWorkbenchQuery(targetPage = pageIndex): Promise<void> {
    const nextPage = Math.max(0, targetPage);
    const query = {
      schema: schemaNameForStandardId(selectedStandardId),
      limit: normalizedPageSize(),
      offset: nextPage * normalizedPageSize(),
    };
    workbenchLoading = true;
    try {
      const source = currentDataSourceOption();
      const workerBackendConfig = backendConfigForDataSource(source);
      if (workerBackendConfig) {
        const store = await ensureLocalFlatSqlStore();
        if (!store) throw new Error('FlatSQL initialization failed');
        const result = await store.queryRemotePage({
          standardId: selectedStandardId,
          query,
          backendConfig: workerBackendConfig,
          source: source?.publicKey ?? source?.peerId ?? source?.id ?? null,
        });
        dataScan = result.scan;
        rawRecords = result.records;
        localFlatSqlStats = result.stats;
        pageIndex = nextPage;
        resetPnmSelectionIfNeeded();
        if (rawRecords.length > 0 && !userEditedSql) {
          sqlQueryText = defaultSqlQuery(selectedStandardId);
        }
        return;
      }

      const activeBackend = backendForSelectedDataSource();
      if (!activeBackend) {
        rawRecords = [];
        dataScan = null;
        return;
      }

      try {
        const scanResult = await activeBackend.scanRawData(query);
        dataScan = scanResult.ok ? scanResult.data : null;
      } catch {
        dataScan = null;
      }

      let nextRecords: RawDataRecord[] = [];
      if (dataScan?.results.length) {
        const streamResult = await activeBackend.streamRawData({
          schema: dataScan.schema,
          scanHash: dataScan.scanHash,
          chunkHash: dataScan.chunkHash || dataScan.scanHash,
          snapshotId: dataScan.snapshotId,
          head: dataScan.head,
          cursor: dataScan.cursor,
          nextCursor: dataScan.nextCursor,
          totalCount: dataScan.totalCount,
          highWaterMark: dataScan.highWaterMark,
          queryProfile: dataScan.queryProfile,
          records: dataScan.results,
        });
        nextRecords = streamResult.ok ? streamResult.data ?? [] : [];
      }
      if (nextRecords.length === 0 && (!dataScan || dataScan.results.length > 0)) {
        const result = await activeBackend.queryRawData(query);
        nextRecords = result.data ?? [];
      }
      rawRecords = nextRecords;
      pageIndex = nextPage;
      resetPnmSelectionIfNeeded();
      await ingestDownloadedRecords(rawRecords);
      if (rawRecords.length > 0 && !userEditedSql) {
        sqlQueryText = defaultSqlQuery(selectedStandardId);
      }
    } catch {
      rawRecords = [];
      dataScan = null;
    } finally {
      workbenchLoading = false;
    }
  }

  function handleTableChange(): void {
    userSelectedStandard = true;
    userEditedColumns = false;
    resetSqlForSelectedStandard();
    clearPnmSelection();
    columnMenuOpen = false;
    dataScan = null;
    pageIndex = 0;
    void runWorkbenchQuery(0);
  }

  function handlePageSizeChange(): void {
    pageSize = normalizedPageSize();
    pageIndex = 0;
    void runWorkbenchQuery(0);
  }

  function selectDataSource(sourceId: string): void {
    if (!dataSourceOptions.some((source) => source.id === sourceId)) return;
    selectedDataSourceId = sourceId;
    pageIndex = 0;
    rawRecords = [];
    dataScan = null;
    clearPnmSelection();
    resetLocalFlatSqlStore();
    resetSqlForSelectedStandard();
    columnMenuOpen = false;
    userSelectedDataSource = true;
    userSelectedStandard = false;
    userEditedColumns = false;
    resetSchemaSyncSchedulers();
    void initializeWorkbench();
  }

  function goToPreviousPage(): void {
    if (canGoPrevious) void runWorkbenchQuery(pageIndex - 1);
  }

  function goToNextPage(): void {
    if (canGoNext) void runWorkbenchQuery(pageIndex + 1);
  }

  function handleSqlInput(): void {
    userEditedSql = true;
  }

  function selectSubscribedRow(schema: SchemaSyncRow): void {
    selectedStandardId = schema.id;
    if (selectedDataSourceId !== schema.dataSourceId) {
      selectDataSource(schema.dataSourceId);
      return;
    }
    userSelectedStandard = true;
    resetSqlForSelectedStandard();
    void runWorkbenchQuery(0);
  }

  function handleSubscriptionStorageCapInput(schema: SchemaSyncRow, event: Event): void {
    const storageCap = normalizedStorageCap((event.currentTarget as HTMLInputElement).value);
    updateSubscription(schema.subscriptionId, { storageCap });
    updateSchemaSyncPreference(schema.id, { mode: 'sync', storageCap }, schema.dataSourceId);
    scheduleSubscribedSchemaSyncs(schemaSyncRows);
  }

  function handleSubscriptionStorageUnitChange(schema: SchemaSyncRow, event: Event): void {
    const value = (event.currentTarget as HTMLSelectElement).value;
    const storageUnit = isStorageUnit(value) ? value : DEFAULT_SCHEMA_SYNC_PREFERENCE.storageUnit;
    updateSubscription(schema.subscriptionId, { storageUnit });
    updateSchemaSyncPreference(schema.id, { mode: 'sync', storageUnit }, schema.dataSourceId);
    scheduleSubscribedSchemaSyncs(schemaSyncRows);
  }

  function handleSubscriptionFilterInput(schema: SchemaSyncRow, event: Event): void {
    updateSubscription(schema.subscriptionId, {
      syncFilter: (event.currentTarget as HTMLInputElement).value,
    });
  }

  function updateSubscription(subscriptionId: string, patch: Partial<Pick<DataFeedSubscription, 'remoteRows' | 'storageCap' | 'storageUnit' | 'syncFilter'>>): void {
    dataDirectoryState = updateDataFeedSubscription(dataDirectoryState, subscriptionId, patch);
    persistDataDirectoryState(dataDirectoryState);
  }

  async function runSqlQuery(): Promise<void> {
    const query = sqlQueryText.trim();
    if (!query) {
      sqlResult = null;
      sqlError = '';
      return;
    }
    sqlRunning = true;
    sqlError = '';
    try {
      const store = await ensureLocalFlatSqlStore();
      if (!store) return;
      sqlResult = await store.query(query, selectedStandardId);
    } catch (error) {
      sqlResult = null;
      sqlError = error instanceof Error ? error.message : 'SQL query failed';
    } finally {
      sqlRunning = false;
    }
  }

  async function ingestDownloadedRecords(records: RawDataRecord[]): Promise<void> {
    if (records.length === 0) return;
    try {
      const store = await ensureLocalFlatSqlStore();
      if (!store) return;
      const source = currentDataSourceOption();
      await store.ingestRecords(selectedStandardId, records, source?.publicKey ?? source?.peerId ?? source?.id ?? null);
      await refreshLocalFlatSqlStats();
    } catch (error) {
      sqlError = error instanceof Error ? error.message : 'FlatSQL ingest failed';
    }
  }

  function scheduleSubscribedSchemaSyncs(rows: SchemaSyncRow[]): void {
    const bySource = new Map<string, SchemaSyncRow[]>();
    for (const row of rows) {
      if (row.preference.mode !== 'sync') continue;
      bySource.set(row.dataSourceId, [...bySource.get(row.dataSourceId) ?? [], row]);
    }
    for (const [dataSourceId, sourceRows] of bySource) {
      void schemaSyncSchedulerForDataSource(dataSourceId).schedule(sourceRows, dataSourceId);
    }
  }

  function schemaSyncSchedulerForDataSource(dataSourceId: string): typeof schemaSyncScheduler {
    const existing = schemaSyncSchedulers.get(dataSourceId);
    if (existing) return existing;
    const scheduler = createSchemaSyncScheduler({
      syncSchema: (standardId, sourceId) => synchronizeSchema(standardId, sourceId),
    });
    schemaSyncSchedulers.set(dataSourceId, scheduler);
    return scheduler;
  }

  function resetSchemaSyncSchedulers(): void {
    schemaSyncScheduler.reset();
    for (const scheduler of schemaSyncSchedulers.values()) scheduler.reset();
  }

  async function synchronizeSchema(standardId: string, dataSourceId = selectedDataSourceId): Promise<void> {
    const key = schemaSyncPreferenceKey(dataSourceId, standardId);
    const preference = schemaSyncPreferenceFor(dataSourceId, standardId);
    if (preference.mode !== 'sync' || activeSyncKeys.has(key)) return;

    const source = dataSourceOptionForId(dataSourceId);
    const backendConfig = backendConfigForDataSource(source);
    if (!backendConfig) return;
    const remoteRows = remoteRowsForSubscription(dataSourceId, standardId) ?? totalRowsForStandardId(dataSummary, standardId) ?? 0;
    const initialProgress = schemaSyncProgressFor(
      dataSourceId,
      standardId,
      remoteRows,
      localFlatSqlStats,
    );
    activeSyncKeys = new Set(activeSyncKeys).add(key);
    refreshSchemaSyncProgress(standardId, {
      status: 'syncing',
      error: null,
      totalRows: remoteRows,
      providerPeerId: source?.peerId ?? null,
      providerPublicKey: source?.publicKey ?? null,
    }, dataSourceId);

    let store: WorkerLocalFlatSqlStore | null = null;
    try {
      store = await ensureLocalFlatSqlStore(dataSourceId);
      if (!store) throw new Error('FlatSQL initialization failed');
      const update = await store.syncSchema({
        standardId,
        schema: schemaNameForStandardId(standardId),
        backendConfig,
        initialProgress,
        totalRows: remoteRows,
        capBytes: storageCapBytes(preference),
        pageSize: SYNC_PAGE_SIZE,
        persistRecordInterval: SYNC_PERSIST_RECORD_INTERVAL,
        source: source?.publicKey ?? source?.peerId ?? source?.id ?? null,
        syncFilter: syncFilterForSubscription(dataSourceId, standardId),
      }, (nextUpdate) => applyWorkerSchemaSyncUpdate(standardId, dataSourceId, nextUpdate));
      applyWorkerSchemaSyncUpdate(standardId, dataSourceId, update);
    } catch (error) {
      refreshSchemaSyncProgress(standardId, {
        status: 'error',
        error: error instanceof Error ? error.message : 'Schema sync failed',
      }, dataSourceId);
      if (store) {
        await store.flush(standardId);
        await refreshLocalFlatSqlStats();
      }
    } finally {
      const nextActive = new Set(activeSyncKeys);
      nextActive.delete(key);
      activeSyncKeys = nextActive;
    }
  }

  function applyWorkerSchemaSyncUpdate(standardId: string, dataSourceId: string, update: WorkerSchemaSyncUpdate): void {
    localFlatSqlStats = update.stats;
    refreshSchemaSyncProgress(standardId, update.progress, dataSourceId);
  }

  function refreshSchemaSyncProgress(standardId: string, patch: Partial<SchemaSyncProgress>, dataSourceId = selectedDataSourceId): void {
    const key = schemaSyncPreferenceKey(dataSourceId, standardId);
    const localRows = localRowsForStandard(localFlatSqlStats, standardId);
    const cachedBytes = cachedBytesForStandard(localFlatSqlStats, standardId);
    const current = schemaSyncProgressFor(dataSourceId, standardId, totalRowsForStandardId(dataSummary, standardId) ?? 0, localFlatSqlStats);
    schemaSyncProgress = {
      ...schemaSyncProgress,
      [key]: {
        ...current,
        localRows,
        cachedBytes,
        ...patch,
        syncedRows: Math.max(patch.syncedRows ?? current.syncedRows, localRows),
      },
    };
    persistSchemaSyncProgress(schemaSyncProgress);
  }

  async function ensureLocalFlatSqlStore(dataSourceId = selectedDataSourceId): Promise<WorkerLocalFlatSqlStore | null> {
    const nextKey = localFlatSqlPersistenceKey(dataSourceId);
    if (localFlatSqlStore && localFlatSqlStoreKey === nextKey) return localFlatSqlStore;
    if (localFlatSqlStorePromise && localFlatSqlStorePromiseKey === nextKey) return localFlatSqlStorePromise;
    resetLocalFlatSqlStore();
    localFlatSqlStorePromiseKey = nextKey;
    localFlatSqlStorePromise = (async () => {
      try {
        localFlatSqlStore = await createWorkerLocalFlatSqlStore({
          schemas: LOCAL_FLATSQL_SCHEMAS,
          persistenceKey: nextKey,
        });
        localFlatSqlStoreKey = nextKey;
        await refreshLocalFlatSqlStats();
        return localFlatSqlStore;
      } catch (error) {
        sqlError = error instanceof Error ? error.message : 'FlatSQL initialization failed';
        return null;
      } finally {
        localFlatSqlStorePromise = null;
        localFlatSqlStorePromiseKey = '';
      }
    })();
    return localFlatSqlStorePromise;
  }

  function resetLocalFlatSqlStore(): void {
    localFlatSqlStore?.destroy();
    localFlatSqlStore = null;
    localFlatSqlStoreKey = '';
    localFlatSqlStorePromise = null;
    localFlatSqlStorePromiseKey = '';
    localFlatSqlStats = [];
    sqlResult = null;
    sqlError = '';
  }

  function localFlatSqlPersistenceKey(dataSourceId: string): string {
    return `sdn-data:${dataSourceId}`;
  }

  function beginResetSubscriptionData(subscriptionId: string): void {
    resetSubscriptionId = subscriptionId;
    resetConfirmText = '';
    resetStatus = '';
  }

  function cancelResetSubscriptionData(): void {
    if (resetRunning) return;
    resetSubscriptionId = '';
    resetConfirmText = '';
    resetStatus = '';
  }

  async function confirmResetSubscriptionData(schema: SchemaSyncRow): Promise<void> {
    if (resetConfirmText.trim() !== 'RESET') {
      resetStatus = 'Type RESET to clear this row.';
      return;
    }
    resetRunning = true;
    resetStatus = '';
    const dataSourceId = schema.dataSourceId;
    const standardId = schema.id;
    try {
      resetLocalFlatSqlStore();
      await clearLocalFlatSqlStore({
        persistenceKey: localFlatSqlPersistenceKey(dataSourceId),
        standardIds: [standardId],
      });
      clearSchemaSyncProgressForSubscription(dataSourceId, standardId);
      const nextActive = new Set(activeSyncKeys);
      nextActive.delete(schemaSyncPreferenceKey(dataSourceId, standardId));
      activeSyncKeys = nextActive;
      schemaSyncSchedulerForDataSource(dataSourceId).reset();
      rawRecords = [];
      dataScan = null;
      clearPnmSelection();
      resetSqlForSelectedStandard();
      selectedDataSourceId = dataSourceId;
      selectedStandardId = standardId;
      await ensureLocalFlatSqlStore(dataSourceId);
      await refreshLocalFlatSqlStats();
      resetStatus = `${standardId} row reset. Sync will restart from the first remote row.`;
      resetSubscriptionId = '';
      resetConfirmText = '';
      void synchronizeSchema(standardId, dataSourceId);
    } catch (error) {
      resetStatus = error instanceof Error ? error.message : 'Row reset failed';
    } finally {
      resetRunning = false;
    }
  }

  async function refreshLocalFlatSqlStats(includeCachedBytes = true): Promise<void> {
    localFlatSqlStats = localFlatSqlStore ? await localFlatSqlStore.getStats({ includeCachedBytes }) : [];
  }

  function resetSqlForSelectedStandard(): void {
    sqlQueryText = defaultSqlQuery(selectedStandardId);
    userEditedSql = false;
    sqlResult = null;
    sqlError = '';
  }

  function setSort(column: SortColumn): void {
    if (sortColumn === column) {
      sortDirection = sortDirection === 'asc' ? 'desc' : 'asc';
      return;
    }
    sortColumn = column;
    sortDirection = column === 'timestamp' ? 'desc' : 'asc';
  }

  function sortableHeader(column: SortColumn, label: string): string {
    if (sortColumn !== column) return label;
    return `${label} ${sortDirection.toUpperCase()}`;
  }

  function sortAria(column: SortColumn): 'ascending' | 'descending' | 'none' {
    if (sortColumn !== column) return 'none';
    return sortDirection === 'asc' ? 'ascending' : 'descending';
  }

  function toggleColumn(key: string): void {
    if (visibleColumnKeys.includes(key)) {
      if (visibleColumnKeys.length <= 1) return;
      userEditedColumns = true;
      visibleColumnKeys = visibleColumnKeys.filter((candidate) => candidate !== key);
      return;
    }
    userEditedColumns = true;
    visibleColumnKeys = [...visibleColumnKeys, key];
  }

  function filterRows(rows: WorkbenchRow[], query: string): WorkbenchRow[] {
    const normalized = query.trim().toLowerCase();
    if (!normalized) return rows;
    return rows.filter((record) => rowText(record).includes(normalized));
  }

  function sortRows(rows: WorkbenchRow[], column: SortColumn, direction: SortDirection): WorkbenchRow[] {
    const multiplier = direction === 'asc' ? 1 : -1;
    return [...rows].sort((left, right) => compareRows(left, right, column) * multiplier);
  }

  function compareRows(left: WorkbenchRow, right: WorkbenchRow, column: SortColumn): number {
    if (column === 'timestamp') return String(left.record.timestamp ?? '').localeCompare(String(right.record.timestamp ?? ''));
    return tableValue(left, column).localeCompare(tableValue(right, column));
  }

  function rowText(record: WorkbenchRow): string {
    return allColumns
      .map((column) => tableValue(record, column.key))
      .concat(record.record.schemaName, record.record.timestamp ?? '')
      .join(' ')
      .toLowerCase();
  }

  function tableValue(row: WorkbenchRow, column: SortColumn): string {
    if (column === 'schemaName') return standardIdFromSchema(row.record.schemaName);
    if (column in row.record) return String(row.record[column as keyof RawDataRecord] ?? '');
    return stringifyCellValue(row.decoded[column]);
  }

  function displayCellValue(row: WorkbenchRow, column: WorkbenchColumn): string {
    return shorten(tableValue(row, column.key), column.source === 'standard' ? 40 : 34);
  }

  function fullCellValue(row: WorkbenchRow, column: WorkbenchColumn): string {
    return tableValue(row, column.key);
  }

  function sqlCellValue(row: Record<string, unknown>, column: string): string {
    return stringifyCellValue(row[column]);
  }

  function displaySqlCellValue(row: Record<string, unknown>, column: string): string {
    return shorten(sqlCellValue(row, column), 40);
  }

  function handleWorkbenchRowClick(row: WorkbenchRow): void {
    if (selectedStandardId === 'PNM' && !sqlResult) selectPnmRow(row);
  }

  function handleWorkbenchRowKeydown(row: WorkbenchRow, event: KeyboardEvent): void {
    if (event.key !== 'Enter' && event.key !== ' ') return;
    event.preventDefault();
    handleWorkbenchRowClick(row);
  }

  function selectPnmRow(row: WorkbenchRow): void {
    selectedPnmRow = row;
    pnmFileIdQuery = pnmValue(row.decoded, 'FILE_ID');
    pnmQueryResult = null;
    pnmQueryError = '';
    pnmSignatureStatus = '';
    void runPnmFileIdQuery();
  }

  function resetPnmSelectionIfNeeded(): void {
    if (selectedStandardId !== 'PNM') {
      clearPnmSelection();
      return;
    }
    if (!selectedPnmRow) return;
    const selectedCid = selectedPnmRow.record.cid;
    if (!rawRecords.some((record) => record.cid === selectedCid)) clearPnmSelection();
  }

  function clearPnmSelection(): void {
    selectedPnmRow = null;
    pnmFileIdQuery = '';
    pnmQueryResult = null;
    pnmQueryError = '';
    pnmSignatureStatus = '';
    pnmSignatureRunning = false;
  }

  async function runPnmFileIdQuery(): Promise<void> {
    const fileId = pnmFileIdQuery.trim();
    if (!fileId) {
      pnmQueryResult = null;
      pnmQueryError = 'FILE_ID is required';
      return;
    }
    pnmQueryError = '';
    try {
      const store = await ensureLocalFlatSqlStore();
      if (!store) return;
      pnmQueryResult = await store.query(`SELECT * FROM PNM WHERE FILE_ID = '${escapeSqlString(fileId)}' LIMIT 100`, 'PNM');
    } catch (error) {
      pnmQueryResult = null;
      pnmQueryError = error instanceof Error ? error.message : 'FILE_ID query failed';
    }
  }

  async function verifySelectedPnmSignature(): Promise<void> {
    if (!selectedPnmRow) return;
    pnmSignatureRunning = true;
    pnmSignatureStatus = '';
    try {
      pnmSignatureStatus = await verifyPnmSignature(selectedPnmRow.decoded, currentDataSourceOption()?.publicKey ?? null);
    } catch (error) {
      pnmSignatureStatus = error instanceof Error ? error.message : 'Signature verification failed';
    } finally {
      pnmSignatureRunning = false;
    }
  }

  async function verifyPnmSignature(decoded: Record<string, unknown>, publicKeyText: string | null): Promise<string> {
    const cid = pnmValue(decoded, 'CID');
    const signature = pnmValue(decoded, 'SIGNATURE');
    const signatureType = pnmValue(decoded, 'SIGNATURE_TYPE').toLowerCase();
    if (!cid) return 'CID is unavailable; cannot reconstitute the signed payload.';
    if (!signature) return 'Signature not present on this PNM.';
    if (!signatureType.includes('ed25519')) {
      return signatureType
        ? `Signature type ${pnmValue(decoded, 'SIGNATURE_TYPE')} is not supported in this verifier.`
        : 'Signature type is unavailable.';
    }
    const publicKey = bytesFromEncodedString(publicKeyText ?? '');
    const signatureBytes = bytesFromEncodedString(signature);
    if (!publicKey || publicKey.byteLength !== 32) {
      return 'Cannot verify: provider public key is not a 32-byte Ed25519 key.';
    }
    if (!signatureBytes || signatureBytes.byteLength !== 64) {
      return 'Cannot verify: PNM signature is not a 64-byte Ed25519 signature.';
    }
    const valid = await verifyEd25519Signature(publicKey, new TextEncoder().encode(cid), signatureBytes);
    return valid ? 'Signature valid for reconstituted CID payload.' : 'Signature invalid for reconstituted CID payload.';
  }

  function pnmValue(decoded: Record<string, unknown>, key: string): string {
    return stringifyCellValue(decoded[key]);
  }

  function pnmSignaturePayload(decoded: Record<string, unknown>): string {
    return pnmValue(decoded, 'CID') || '';
  }

  function escapeSqlString(value: string): string {
    return value.replace(/'/g, "''");
  }

  function bytesFromEncodedString(value: string): Uint8Array | null {
    const trimmed = value.trim();
    if (!trimmed) return null;
    const hex = trimmed.startsWith('0x') ? trimmed.slice(2) : trimmed;
    if (/^[0-9a-fA-F]+$/.test(hex) && hex.length % 2 === 0) {
      const bytes = new Uint8Array(hex.length / 2);
      for (let index = 0; index < bytes.length; index += 1) {
        bytes[index] = Number.parseInt(hex.slice(index * 2, index * 2 + 2), 16);
      }
      return bytes;
    }
    try {
      const normalized = trimmed.replace(/-/g, '+').replace(/_/g, '/');
      const padded = normalized.padEnd(Math.ceil(normalized.length / 4) * 4, '=');
      const binary = atob(padded);
      return Uint8Array.from(binary, (char) => char.charCodeAt(0));
    } catch {
      return null;
    }
  }

  function visibleSqlColumns(columns: string[], records: Array<Record<string, unknown>>): string[] {
    const visible = columns.filter((column) => !isInternalSqlColumn(column) && records.some((row) => hasDisplayValue(row[column])));
    if (visible.length > 0 || records.length > 0) return visible;
    return columns.filter((column) => !isInternalSqlColumn(column));
  }

  function isInternalSqlColumn(column: string): boolean {
    return INTERNAL_SQL_COLUMN_KEYS.has(column) || column.startsWith('_');
  }

  function defaultSqlQuery(standardId: string): string {
    return `SELECT * FROM ${standardIdFromSchema(standardId)} LIMIT ${DEFAULT_PAGE_SIZE}`;
  }

  function buildSubscribedSchemaSyncRows(
    subscriptions: DataFeedSubscription[],
    activeDataSourceId: string,
    stats: LocalFlatSqlStandardStats[],
  ): SchemaSyncRow[] {
    return subscriptions.map((subscription) => {
      const sourceStats = subscription.dataSourceId === activeDataSourceId ? stats : [];
      const progress = schemaSyncProgressFor(subscription.dataSourceId, subscription.standardId, subscription.remoteRows, sourceStats);
      return {
        id: subscription.standardId,
        subscriptionId: subscription.id,
        dataSourceId: subscription.dataSourceId,
        providerName: subscription.providerName,
        providerPeerId: subscription.peerId,
        providerPublicKey: subscription.providerPublicKey,
        syncFilter: subscription.syncFilter,
        remoteRows: Math.max(subscription.remoteRows, progress.totalRows),
        localRows: progress.localRows,
        cachedBytes: progress.cachedBytes,
        preference: subscriptionSchemaSyncPreference(subscription),
        progress,
      };
    }).sort((left, right) => {
      const delta = right.remoteRows - left.remoteRows;
      return delta === 0 ? left.id.localeCompare(right.id) : delta;
    });
  }

  function localRowsForStandard(stats: LocalFlatSqlStandardStats[], standardId: string): number {
    const stat = stats.find((entry) => entry.standardId === standardId);
    return Math.max(stat?.ingestedRecordCount ?? 0, stat?.recordCount ?? 0);
  }

  function cachedBytesForStandard(stats: LocalFlatSqlStandardStats[], standardId: string): number {
    return stats.find((entry) => entry.standardId === standardId)?.cachedBytes ?? 0;
  }

  function schemaSyncPreferenceFor(dataSourceId: string, standardId: string): SchemaSyncPreference {
    return schemaSyncPreferences[schemaSyncPreferenceKey(dataSourceId, standardId)] ?? DEFAULT_SCHEMA_SYNC_PREFERENCE;
  }

  function subscriptionSchemaSyncPreference(subscription: DataFeedSubscription): SchemaSyncPreference {
    const persisted = schemaSyncPreferences[schemaSyncPreferenceKey(subscription.dataSourceId, subscription.standardId)];
    return normalizeSchemaSyncPreference({
      mode: 'sync',
      storageCap: persisted?.storageCap ?? subscription.storageCap,
      storageUnit: persisted?.storageUnit ?? subscription.storageUnit,
    }) ?? {
      mode: 'sync',
      storageCap: subscription.storageCap,
      storageUnit: subscription.storageUnit,
    };
  }

  function remoteRowsForSubscription(dataSourceId: string, standardId: string): number | null {
    return dataDirectoryState.subscriptions.find((subscription) => (
      subscription.dataSourceId === dataSourceId && subscription.standardId === standardId
    ))?.remoteRows ?? null;
  }

  function syncFilterForSubscription(dataSourceId: string, standardId: string): string {
    return dataDirectoryState.subscriptions.find((subscription) => (
      subscription.dataSourceId === dataSourceId && subscription.standardId === standardId
    ))?.syncFilter ?? '';
  }

  function schemaSyncProgressFor(
    dataSourceId: string,
    standardId: string,
    remoteRows: number,
    stats: LocalFlatSqlStandardStats[],
  ): SchemaSyncProgress {
    const key = schemaSyncPreferenceKey(dataSourceId, standardId);
    const localRows = localRowsForStandard(stats, standardId);
    const cachedBytes = cachedBytesForStandard(stats, standardId);
    const persisted = schemaSyncProgress[key];
    const activePersisted = localRows === 0 && (persisted?.localRows ?? 0) > 0 ? null : persisted;
    const totalRows = Math.max(remoteRows, activePersisted?.totalRows ?? 0);
    const complete = totalRows > 0 && localRows >= totalRows;
    const active = activeSyncKeys.has(key);
    const status = effectiveSchemaSyncStatus({
      active,
      complete,
      persistedStatus: activePersisted?.status,
    });
    return {
      status,
      syncedRows: Math.max(localRows, activePersisted?.syncedRows ?? 0),
      totalRows,
      localRows,
      cachedBytes,
      pinnedBytes: activePersisted?.pinnedBytes ?? cachedBytes,
      providerPeerId: activePersisted?.providerPeerId ?? null,
      providerPublicKey: activePersisted?.providerPublicKey ?? null,
      snapshotId: activePersisted?.snapshotId ?? null,
      head: activePersisted?.head ?? null,
      cursor: activePersisted?.cursor ?? null,
      nextCursor: activePersisted?.nextCursor ?? null,
      highWaterMark: activePersisted?.highWaterMark ?? null,
      queryProfile: activePersisted?.queryProfile ?? null,
      chunkHash: activePersisted?.chunkHash ?? null,
      syncProtocol: activePersisted?.syncProtocol ?? null,
      verifiedChunks: activePersisted?.verifiedChunks ?? [],
      lastSyncedAt: activePersisted?.lastSyncedAt ?? null,
      error: activePersisted?.error ?? null,
    };
  }

  function updateSchemaSyncPreference(standardId: string, patch: Partial<SchemaSyncPreference>, dataSourceId = selectedDataSourceId): void {
    const key = schemaSyncPreferenceKey(dataSourceId, standardId);
    const current = schemaSyncPreferenceFor(dataSourceId, standardId);
    schemaSyncPreferences = {
      ...schemaSyncPreferences,
      [key]: normalizeSchemaSyncPreference({ ...current, ...patch }) ?? DEFAULT_SCHEMA_SYNC_PREFERENCE,
    };
    persistSchemaSyncPreferences(schemaSyncPreferences);
  }

  function schemaSyncPreferenceKey(dataSourceId: string, standardId: string): string {
    return `${dataSourceId}:${standardId.trim().toUpperCase() || DEFAULT_STANDARD_ID}`;
  }

  function loadSchemaSyncPreferences(): Record<string, SchemaSyncPreference> {
    if (typeof window === 'undefined') return {};
    try {
      const parsed = JSON.parse(window.localStorage.getItem(SCHEMA_SYNC_STORAGE_KEY) ?? '{}') as unknown;
      if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return {};
      const preferences: Record<string, SchemaSyncPreference> = {};
      for (const [key, value] of Object.entries(parsed as Record<string, unknown>)) {
        const preference = normalizeSchemaSyncPreference(value);
        if (preference) preferences[key] = preference;
      }
      return preferences;
    } catch {
      return {};
    }
  }

  function persistSchemaSyncPreferences(preferences: Record<string, SchemaSyncPreference>): void {
    if (typeof window === 'undefined') return;
    try {
      window.localStorage.setItem(SCHEMA_SYNC_STORAGE_KEY, JSON.stringify(preferences));
    } catch {
      // Storage quota or privacy settings should not block the data explorer.
    }
  }

  function loadSchemaSyncProgress(): Record<string, SchemaSyncProgress> {
    if (typeof window === 'undefined') return {};
    try {
      const parsed = JSON.parse(window.localStorage.getItem(SCHEMA_SYNC_STATE_STORAGE_KEY) ?? '{}') as unknown;
      if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return {};
      const progress: Record<string, SchemaSyncProgress> = {};
      for (const [key, value] of Object.entries(parsed as Record<string, unknown>)) {
        const normalized = normalizeSchemaSyncProgress(value);
        if (normalized) progress[key] = normalized;
      }
      return progress;
    } catch {
      return {};
    }
  }

  function persistSchemaSyncProgress(progress: Record<string, SchemaSyncProgress>): void {
    if (typeof window === 'undefined') return;
    try {
      window.localStorage.setItem(SCHEMA_SYNC_STATE_STORAGE_KEY, JSON.stringify(progress));
    } catch {
      // Storage quota or privacy settings should not block progress reporting.
    }
  }

  function clearSchemaSyncProgressForSubscription(dataSourceId: string, standardId: string): void {
    const key = schemaSyncPreferenceKey(dataSourceId, standardId);
    schemaSyncProgress = Object.fromEntries(
      Object.entries(schemaSyncProgress).filter(([candidate]) => candidate !== key),
    );
    persistSchemaSyncProgress(schemaSyncProgress);
  }

  function normalizeSchemaSyncPreference(value: unknown): SchemaSyncPreference | null {
    if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
    const candidate = value as Record<string, unknown>;
    return {
      mode: candidate.mode === 'sync' ? 'sync' : 'preview',
      storageCap: normalizedStorageCap(candidate.storageCap),
      storageUnit: isStorageUnit(candidate.storageUnit) ? candidate.storageUnit : DEFAULT_SCHEMA_SYNC_PREFERENCE.storageUnit,
    };
  }

  function normalizedStorageCap(value: unknown): number {
    const numeric = Number(value);
    if (!Number.isFinite(numeric)) return DEFAULT_SCHEMA_SYNC_PREFERENCE.storageCap;
    return Math.max(0.1, Math.min(1_000_000, Math.round(numeric * 10) / 10));
  }

  function normalizeSchemaSyncProgress(value: unknown): SchemaSyncProgress | null {
    if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
    const candidate = value as Record<string, unknown>;
    return {
      status: isSchemaSyncStatus(candidate.status) ? candidate.status : 'idle',
      syncedRows: normalizedRowCount(candidate.syncedRows),
      totalRows: normalizedRowCount(candidate.totalRows),
      localRows: normalizedRowCount(candidate.localRows),
      cachedBytes: normalizedRowCount(candidate.cachedBytes),
      pinnedBytes: normalizedRowCount(candidate.pinnedBytes),
      providerPeerId: normalizedOptionalString(candidate.providerPeerId),
      providerPublicKey: normalizedOptionalString(candidate.providerPublicKey),
      snapshotId: normalizedOptionalString(candidate.snapshotId),
      head: normalizedOptionalString(candidate.head),
      cursor: normalizedOptionalString(candidate.cursor),
      nextCursor: normalizedOptionalString(candidate.nextCursor),
      highWaterMark: normalizedOptionalString(candidate.highWaterMark),
      queryProfile: normalizedOptionalString(candidate.queryProfile),
      chunkHash: normalizedOptionalString(candidate.chunkHash),
      syncProtocol: normalizedOptionalString(candidate.syncProtocol),
      verifiedChunks: normalizedStringArray(candidate.verifiedChunks).slice(-256),
      lastSyncedAt: typeof candidate.lastSyncedAt === 'string' ? candidate.lastSyncedAt : null,
      error: typeof candidate.error === 'string' ? candidate.error : null,
    };
  }

  function normalizedOptionalString(value: unknown): string | null {
    return typeof value === 'string' && value.trim() ? value.trim() : null;
  }

  function normalizedStringArray(value: unknown): string[] {
    if (!Array.isArray(value)) return [];
    return value
      .filter((entry): entry is string => typeof entry === 'string' && entry.trim().length > 0)
      .map((entry) => entry.trim());
  }

  function normalizedRowCount(value: unknown): number {
    const numeric = Number(value);
    if (!Number.isFinite(numeric) || numeric < 0) return 0;
    return Math.floor(numeric);
  }

  function isSchemaSyncStatus(value: unknown): value is SchemaSyncStatus {
    return value === 'idle' || value === 'syncing' || value === 'synced' || value === 'capped' || value === 'error';
  }

  function isStorageUnit(value: unknown): value is StorageUnit {
    return typeof value === 'string' && STORAGE_CAP_UNITS.includes(value as StorageUnit);
  }

  function workbenchColumnsForStandard(standardId: string, rows: WorkbenchRow[]): WorkbenchColumn[] {
    const standardColumns = STANDARD_FIELD_COLUMNS[standardId] ?? [];
    const metadataColumns = standardId === 'PNM'
      ? METADATA_COLUMNS.filter((column) => column.key !== 'cid')
      : METADATA_COLUMNS;
    const knownKeys = new Set([...metadataColumns, ...standardColumns].map((column) => column.key));
    const dynamicColumns: WorkbenchColumn[] = [];
    for (const row of rows) {
      for (const key of Object.keys(row.decoded)) {
        if (INTERNAL_COLUMN_KEYS.has(key)) continue;
        if (knownKeys.has(key)) continue;
        knownKeys.add(key);
        dynamicColumns.push({ key, label: labelFromFieldKey(key), source: 'standard' });
      }
    }
    if (standardColumns.length === 0) return [...metadataColumns, ...dynamicColumns];
    return [...standardColumns, ...metadataColumns, ...dynamicColumns];
  }

  function syncVisibleColumnKeys(columns: WorkbenchColumn[]): void {
    const columnKeys = columns.map((column) => column.key);
    const validKeys = new Set(columnKeys);
    const defaultKeys = dataAwareDefaultColumnKeys(columns, decodedRows);
    if (selectedStandardId !== lastColumnStandardId || !userEditedColumns) {
      updateVisibleColumnKeys(defaultKeys.length > 0 ? defaultKeys : columnKeys);
      lastColumnStandardId = selectedStandardId;
      return;
    }
    if (visibleColumnKeys.length === 0 || visibleColumnKeys.some((key) => !validKeys.has(key))) {
      const nextKeys = visibleColumnKeys.filter((key) => validKeys.has(key));
      updateVisibleColumnKeys(nextKeys.length > 0 ? nextKeys : defaultKeys.length > 0 ? defaultKeys : columnKeys);
    }
  }

  function dataAwareDefaultColumnKeys(columns: WorkbenchColumn[], rows: WorkbenchRow[]): string[] {
    if (rows.length === 0) return columns.map((column) => column.key).filter((key) => !INTERNAL_COLUMN_KEYS.has(key));
    return columns
      .filter((column) => !INTERNAL_COLUMN_KEYS.has(column.key))
      .filter((column) => rows.some((row) => hasDisplayValue(tableValue(row, column.key))))
      .map((column) => column.key);
  }

  function updateVisibleColumnKeys(nextKeys: string[]): void {
    if (arraysEqual(visibleColumnKeys, nextKeys)) return;
    visibleColumnKeys = nextKeys;
  }

  function decodeWorkbenchRecord(record: RawDataRecord): WorkbenchRow {
    try {
      const bytes = recordBytes(record);
      const standardId = standardIdFromSchema(record.schemaName);
      if (standardId === 'EPM') return { record, decoded: decodeEpmFlatBuffer(bytes) };
      if (standardId === 'OMM') return { record, decoded: decodeOmmFlatBuffer(bytes) };
      if (standardId === 'PNM') return { record, decoded: decodePnmFlatBuffer(bytes) };
      return { record, decoded: {} };
    } catch {
      return { record, decoded: {} };
    }
  }

  function recordBytes(record: RawDataRecord): Uint8Array {
    if (record.dataBytes) return record.dataBytes;
    throw new Error('raw FlatBuffer bytes are unavailable');
  }

  function stringifyCellValue(value: unknown): string {
    if (value == null) return '';
    if (Array.isArray(value)) return value.map((entry) => stringifyCellValue(entry)).join(', ');
    if (typeof value === 'object') return JSON.stringify(value);
    return String(value);
  }

  function hasDisplayValue(value: unknown): boolean {
    if (value == null) return false;
    if (Array.isArray(value)) return value.some(hasDisplayValue);
    if (typeof value === 'string') return value.trim().length > 0;
    if (typeof value === 'object') return Object.values(value).some(hasDisplayValue);
    return true;
  }

  function arraysEqual(left: string[], right: string[]): boolean {
    return left.length === right.length && left.every((value, index) => value === right[index]);
  }

  function labelFromFieldKey(key: string): string {
    return key.split('_').filter(Boolean).map((part) => `${part.slice(0, 1).toUpperCase()}${part.slice(1)}`).join(' ');
  }

  function standardIdFromSchema(schemaName: string | null | undefined): string {
    const id = String(schemaName || '').split('.')[0]?.trim().toUpperCase();
    return id || DEFAULT_STANDARD_ID;
  }

  function standardOptionsFromSummary(summary: DataSummary | null): StandardOption[] {
    const ids = new Set<string>();
    for (const schema of summary?.schemas ?? []) ids.add(standardIdFromSchema(schema.schemaName));
    for (const source of summary?.sources ?? []) ids.add(standardIdFromSchema(source.schemaName));
    if (ids.size === 0) ids.add(DEFAULT_STANDARD_ID);
    return Array.from(ids)
      .map((id) => ({ id, remoteRows: totalRowsForStandardId(summary, id) ?? 0 }))
      .sort((left, right) => right.remoteRows - left.remoteRows || left.id.localeCompare(right.id));
  }

  function standardIdsFromSummary(summary: DataSummary | null): string[] {
    return standardOptionsFromSummary(summary).map((option) => option.id);
  }

  function preferredStandardIdFromSummary(summary: DataSummary | null): string {
    return standardOptionsFromSummary(summary)[0]?.id ?? DEFAULT_STANDARD_ID;
  }

  function schemaNameForStandardId(standardId: string): string {
    const id = standardId.trim().toUpperCase() || DEFAULT_STANDARD_ID;
    const exact = dataSummary?.schemas.find((schema) => standardIdFromSchema(schema.schemaName) === id);
    if (exact) return exact.schemaName;
    const source = dataSummary?.sources.find((entry) => standardIdFromSchema(entry.schemaName) === id);
    if (source) return source.schemaName;
    return `${id}.${SCHEMA_EXTENSION}`;
  }

  function totalRowsForStandardId(summary: DataSummary | null, standardId: string): number | null {
    if (!summary) return null;
    const schemaCount = summary.schemas.find((schema) => standardIdFromSchema(schema.schemaName) === standardId)?.count;
    if (typeof schemaCount === 'number') return schemaCount;
    const sourceCount = summary.sources
      .filter((source) => standardIdFromSchema(source.schemaName) === standardId)
      .reduce((total, source) => total + source.count, 0);
    return sourceCount || null;
  }

  function scanTotalRowsForStandard(scan: DataScanResult | null, standardId: string): number | null {
    if (!scan || standardIdFromSchema(scan.schema) !== standardId) return null;
    return Number.isFinite(scan.totalCount) ? scan.totalCount : null;
  }

  function formatNumber(value: number): string {
    return new Intl.NumberFormat('en-US').format(value);
  }

  function syncProgressLabel(schema: SchemaSyncRow): string {
    const syncedRows = Math.max(schema.localRows, schema.progress.syncedRows);
    const totalRows = Math.max(schema.remoteRows, schema.progress.totalRows);
    if (totalRows === 0) return 'No remote rows';
    return `Synced ${formatNumber(syncedRows)}/${formatNumber(totalRows)}`;
  }

  function syncStatusLabel(schema: SchemaSyncRow): string {
    return formatSchemaSyncStatusLabel({
      preferenceMode: schema.preference.mode,
      progressStatus: schema.progress.status,
      localRows: schema.localRows,
      remoteRows: schema.remoteRows,
    });
  }

  function nextSyncAttemptLabel(schema: SchemaSyncRow): string {
    const key = schemaSyncPreferenceKey(schema.dataSourceId, schema.id);
    if (activeSyncKeys.has(key)) return 'Syncing now';
    if (schema.progress.status === 'error') return 'On next scheduler pass';
    if (schema.preference.mode !== 'sync') return 'Not scheduled';
    if (schema.remoteRows > schema.localRows) return 'Queued';
    return 'When remote rows advance';
  }

  function formatDateTime(value: string): string {
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return value;
    return new Intl.DateTimeFormat(undefined, {
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
    }).format(date);
  }

  function formatBytes(value: number): string {
    if (!Number.isFinite(value) || value <= 0) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    let unitIndex = 0;
    let nextValue = value;
    while (nextValue >= 1024 && unitIndex < units.length - 1) {
      nextValue /= 1024;
      unitIndex += 1;
    }
    const digits = nextValue >= 10 || unitIndex === 0 ? 0 : 1;
    return `${nextValue.toFixed(digits)} ${units[unitIndex]}`;
  }

  function storageCapBytes(preference: SchemaSyncPreference): number {
    const unitMultiplier = preference.storageUnit === 'TB'
      ? 1024 ** 4
      : preference.storageUnit === 'GB'
        ? 1024 ** 3
        : 1024 ** 2;
    return Math.max(1, Math.floor(preference.storageCap * unitMultiplier));
  }

  function normalizedPageSize(): number {
    const value = Number(pageSize) || DEFAULT_PAGE_SIZE;
    return Math.max(1, Math.min(100, value));
  }

  function shorten(value: string | null | undefined, length = 28): string {
    if (!value) return '';
    if (value.length <= length) return value;
    return `${value.slice(0, Math.max(4, length - 5))}...${value.slice(-4)}`;
  }

  function backendForSelectedDataSource(): SdnBackend | null {
    const source = currentDataSourceOption();
    if (!backend || !source) return null;
    if (source.kind === 'local') return backend;
    return null;
  }

  function backendConfigForDataSource(source: DataSourceOption | null): WorkerFlatSqlSyncBackendConfig | null {
    if (!source || source.kind !== 'configured' || !source.peerId || !source.syncAddrs?.length) return null;
    return {
      targetPeerId: source.peerId,
      candidateAddrs: source.syncAddrs,
      providerId: configuredProviderIdFromSource(source),
      displayName: source.label,
      publicKey: source.publicKey,
      gatewayUrl: localGatewayUrl(),
    };
  }

  function localGatewayUrl(): string | null {
    const params = new URLSearchParams(window.location.search);
    const env = import.meta.env as ImportMetaEnv & { readonly SDN_UI_GATEWAY_URL?: string };
    return params.get('gateway') ?? env.SDN_UI_GATEWAY_URL ?? (backend?.mode === 'desktop-local' ? 'http://127.0.0.1:8081' : null);
  }

  function currentDataSourceOption(): DataSourceOption | null {
    const options = buildDataSourceOptions(backend, configuredDataSources, peers);
    return options.find((source) => source.id === selectedDataSourceId) ?? options[0] ?? null;
  }

  function dataSourceOptionForId(dataSourceId: string): DataSourceOption | null {
    const options = buildDataSourceOptions(backend, configuredDataSources, peers);
    return options.find((source) => source.id === dataSourceId) ?? null;
  }

  function buildDataSourceOptions(
    localBackend: SdnBackend | null,
    configuredNodes: ConfiguredSdnNode[],
    observedPeers: ObservedSdnPeer[],
  ): DataSourceOption[] {
    const options: DataSourceOption[] = [{
      id: LOCAL_DATA_SOURCE_ID,
      label: 'Local Desktop',
      detail: localBackend?.mode ?? 'desktop-local',
      peerId: null,
      publicKey: null,
      kind: 'local',
      searchText: 'local desktop local-node',
    }];
    const observedNames = new Map(observedPeers.map((peer) => [peer.id, peer.name]));

    for (const node of configuredNodes) {
      const peerId = configuredNodePeerId(node);
      const syncAddrs = configuredNodeSyncAddrs(node, peerId);
      if (!peerId || syncAddrs.length === 0) continue;
      const publicKey = configuredNodePublicKey(node) ?? peerId;
      const label = configuredNodeLabel(node, observedNames, peerId);
      const detail = [node.id, configuredNodeHostName(node)].filter(Boolean).join(' / ');
      options.push({
        id: `configured:${node.id}`,
        label,
        detail,
        peerId,
        publicKey,
        kind: 'configured',
        syncAddrs,
        searchText: [label, detail, publicKey, peerId, node.trustLevel, node.trust_level, syncAddrs.join(' ')].filter(Boolean).join(' ').toLowerCase(),
      });
    }

    return dedupeDataSourceOptions(options);
  }

  function dataDirectoryMigrationSources(options: DataSourceOption[]): DataDirectoryMigrationSource[] {
    return options
      .filter((source) => source.peerId)
      .map((source) => ({
        dataSourceId: source.id,
        peerId: source.peerId ?? source.id,
        providerName: source.label,
        providerPublicKey: source.publicKey,
      }));
  }

  function preferredDataSourceId(options: DataSourceOption[]): string {
    return options.find((source) => source.searchText.includes('celestrak'))?.id
      ?? options.find((source) => source.kind === 'configured')?.id
      ?? LOCAL_DATA_SOURCE_ID;
  }

  function preferredSubscribedDataSourceId(subscriptions: DataFeedSubscription[]): string | null {
    const firstConfigured = subscriptions.find((subscription) => subscription.dataSourceId !== LOCAL_DATA_SOURCE_ID);
    return firstConfigured?.dataSourceId ?? subscriptions[0]?.dataSourceId ?? null;
  }

  function normalizeConfiguredDataSources(payload: unknown): ConfiguredSdnNode[] {
    const records = recordsFromPayloadKey(payload, 'nodes');
    return records.map((record): ConfiguredSdnNode | null => {
      const id = readRecordString(record, 'id', 'peer_id', 'peerId');
      if (!id) return null;
      const addrs = Array.isArray(record.addrs) ? record.addrs.filter((entry): entry is string => typeof entry === 'string') : [];
      return {
        id,
        name: readRecordString(record, 'name', 'display_name', 'displayName', 'dn') ?? id,
        addrs,
        trust_level: readRecordString(record, 'trust_level', 'trustLevel') ?? undefined,
        metadata: isRecord(record.metadata) ? record.metadata : {},
      };
    }).filter((record): record is ConfiguredSdnNode => record !== null);
  }

  function configuredNodeSyncAddrs(node: ConfiguredSdnNode, peerId: string | null): string[] {
    if (!peerId) return [];
    return node.addrs
      .map((addr) => addr.trim())
      .filter((addr) => addr.includes('/p2p/') && addr.endsWith(`/p2p/${peerId}`) && /\/tcp\/\d+\/wss?\//.test(addr));
  }

  function configuredNodeHostName(node: ConfiguredSdnNode): string {
    return readRecordString(node.metadata ?? {}, 'host_name', 'hostName') ?? '';
  }

  function configuredNodePeerId(node: ConfiguredSdnNode): string | null {
    return readRecordString(node.metadata ?? {}, 'peer_id', 'peerId')
      ?? node.addrs.map((addr) => addr.split('/p2p/')[1]).find((value): value is string => Boolean(value))
      ?? null;
  }

  function configuredNodePublicKey(node: ConfiguredSdnNode): string | null {
    return readRecordString(node.metadata ?? {}, 'public_key', 'publicKey', 'signing_public_key', 'signingPublicKey');
  }

  function configuredNodeLabel(node: ConfiguredSdnNode, observedNames: Map<string, string>, peerId: string | null): string {
    if (node.name && node.name !== node.id && node.name !== peerId) return node.name;
    const observedPeerName = peerId ? observedNames.get(peerId) : null;
    if (observedPeerName && observedPeerName !== peerId) return observedPeerName;
    const observedNodeName = observedNames.get(node.id);
    if (observedNodeName && observedNodeName !== node.id) return observedNodeName;
    return node.name ?? peerId ?? node.id;
  }

  function configuredProviderIdFromSource(source: DataSourceOption): string | null {
    const id = source.id.startsWith('configured:') ? source.id.slice('configured:'.length) : source.id;
    return id || source.peerId;
  }

  function dedupeDataSourceOptions(options: DataSourceOption[]): DataSourceOption[] {
    const seen = new Set<string>();
    return options.filter((source) => {
      if (seen.has(source.id)) return false;
      seen.add(source.id);
      return true;
    });
  }

  function recordsFromPayloadKey(payload: unknown, key: string): Array<Record<string, unknown>> {
    if (Array.isArray(payload)) return payload.filter(isRecord);
    if (!isRecord(payload)) return [];
    const value = payload[key];
    if (Array.isArray(value)) return value.filter(isRecord);
    return [];
  }

  function readRecordString(record: Record<string, unknown>, ...keys: string[]): string | null {
    for (const key of keys) {
      const value = record[key];
      if (typeof value === 'string' && value.trim()) return value.trim();
    }
    return null;
  }

  function isRecord(value: unknown): value is Record<string, unknown> {
    return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
  }

  function syncInspectRoute(nextRoute: string, activeBackend: SdnBackend | null): void {
    const nextCid = inspectCidFromRoute(nextRoute);
    if (nextCid === inspectCid) return;
    inspectCid = nextCid;
    inspectGatewayUrl = '';
    if (nextCid && activeBackend) void loadInspectGateway(nextCid, activeBackend);
  }

  async function loadInspectGateway(cid: string, activeBackend: SdnBackend): Promise<void> {
    try {
      const result = await activeBackend.resolveCid(cid);
      if (inspectCid === cid) inspectGatewayUrl = result.data?.gatewayUrl ?? '';
    } catch {
      if (inspectCid === cid) inspectGatewayUrl = '';
    }
  }

  function inspectCidFromRoute(value: string): string {
    const query = value.split('?')[1] ?? '';
    const params = new URLSearchParams(query);
    return params.get('inspect') ?? '';
  }
</script>

<article class="sdn-card sdn-glass sdn-workbench">
  {#if inspectCid}
    <div class="sdn-inspect-strip">
      <code>{inspectCid}</code>
      <a class="sdn-button sdn-button-muted" href={inspectGatewayUrl || '#'} target="_blank" rel="noreferrer">Open Gateway</a>
    </div>
  {/if}

  <div class="sdn-workbench-main">
      <nav class="sdn-data-subnav" aria-label="Data state">
        <div class="sdn-breadcrumb">Data / Combined Storage / Subscriptions</div>
      </nav>

      {#if workbenchLoading}
        <p class="sdn-loading-inline" role="status">Loading</p>
      {/if}

        <section class="sdn-storage-state" aria-label="Local storage state">
          <div class="sdn-dataset-summary" aria-label="Dataset summary">
            <div class="sdn-dataset-metric" aria-label={`Remote rows ${formatNumber(schemaSyncRows.reduce((total, row) => total + row.remoteRows, 0))}`}>
              <span>Remote rows</span>
              <strong>{formatNumber(schemaSyncRows.reduce((total, row) => total + row.remoteRows, 0))}</strong>
            </div>
            <div class="sdn-dataset-metric" aria-label={`Local rows ${formatNumber(schemaSyncRows.reduce((total, row) => total + row.localRows, 0))}`}>
              <span>Local rows</span>
              <strong>{formatNumber(schemaSyncRows.reduce((total, row) => total + row.localRows, 0))}</strong>
            </div>
            <div class="sdn-dataset-metric" aria-label={`Cached ${formatBytes(schemaSyncRows.reduce((total, row) => total + row.cachedBytes, 0))}`}>
              <span>Cached</span>
              <strong>{formatBytes(schemaSyncRows.reduce((total, row) => total + row.cachedBytes, 0))}</strong>
            </div>
            <div class="sdn-dataset-metric" aria-label={`Scan ${scanHashLabel}`}>
              <span>Scan</span>
              <strong>{scanHashLabel}</strong>
            </div>
          </div>

          <div class="sdn-storage-grid">
            {#each schemaSyncRows as schema}
              <article class="sdn-storage-row" class:active={schema.id === selectedStandardId && schema.dataSourceId === selectedDataSourceId}>
                <div>
                  <strong>{schema.id}</strong>
                  <span>{schema.providerName}</span>
                  <span>{formatNumber(schema.localRows)} local / {formatNumber(schema.remoteRows)} remote</span>
                </div>
                <div>
                  <strong>{formatBytes(schema.cachedBytes)}</strong>
                  <span>{syncProgressLabel(schema)}</span>
                </div>
                <div>
                  <strong>{syncStatusLabel(schema)}</strong>
                  <span>Next sync attempt: {nextSyncAttemptLabel(schema)}</span>
                  <span>{schema.progress.lastSyncedAt ? formatDateTime(schema.progress.lastSyncedAt) : 'Never synced'}</span>
                </div>
                <button class="sdn-button sdn-button-muted sdn-button-compact" type="button" on:click={() => selectSubscribedRow(schema)}>Query</button>
              </article>
            {:else}
              <p class="sdn-empty-inline">No subscribed data feeds. Add a feed from Peers to start local sync.</p>
            {/each}
          </div>
        </section>

        <section class="sdn-schema-sync-wrap" aria-label="Sync subscriptions">
          <table class="sdn-table sdn-schema-sync-table" aria-label="Schema sync">
            <thead>
              <tr>
                <th>Provider</th>
                <th>Schema</th>
                <th>Remote rows</th>
                <th>Local rows</th>
                <th>Progress</th>
                <th>Status</th>
                <th>Storage cap</th>
                <th>Sync filter</th>
                <th>Next sync attempt</th>
                <th>Reset row</th>
              </tr>
            </thead>
            <tbody>
              {#each schemaSyncRows as schema}
                <tr class:active={schema.id === selectedStandardId && schema.dataSourceId === selectedDataSourceId}>
                  <td>
                    <strong>{schema.providerName}</strong>
                    <small>{schema.providerPublicKey ?? schema.providerPeerId ?? schema.dataSourceId}</small>
                  </td>
                  <td>{schema.id}</td>
                  <td>{formatNumber(schema.remoteRows)}</td>
                  <td>{formatNumber(schema.localRows)}</td>
                  <td>{syncProgressLabel(schema)}</td>
                  <td>{syncStatusLabel(schema)}</td>
                  <td>
                    <div class="sdn-storage-cap-controls">
                      <input
                        class="sdn-input"
                        type="number"
                        min="0.1"
                        step="0.1"
                        aria-label={`${schema.id} storage cap`}
                        value={schema.preference.storageCap}
                        on:input={(event) => handleSubscriptionStorageCapInput(schema, event)}
                      />
                      <select
                        class="sdn-input sdn-select"
                        aria-label={`${schema.id} storage unit`}
                        value={schema.preference.storageUnit}
                        on:change={(event) => handleSubscriptionStorageUnitChange(schema, event)}
                      >
                        {#each STORAGE_CAP_UNITS as unit}
                          <option value={unit}>{unit}</option>
                        {/each}
                      </select>
                    </div>
                  </td>
                  <td>
                    <input
                      class="sdn-input sdn-sync-filter"
                      aria-label={`${schema.id} sync filter`}
                      value={schema.syncFilter}
                      placeholder="Sync filter"
                      on:input={(event) => handleSubscriptionFilterInput(schema, event)}
                    />
                  </td>
                  <td>{nextSyncAttemptLabel(schema)}</td>
                  <td>
                    {#if resetSubscriptionId === schema.subscriptionId}
                      <div class="sdn-reset-confirm sdn-reset-row-confirm" role="group" aria-label={`${schema.id} row reset confirmation`}>
                        <label>
                          <span>Type RESET to clear this row.</span>
                          <input class="sdn-input" bind:value={resetConfirmText} autocomplete="off" />
                        </label>
                        <div class="sdn-toolbar">
                          <button class="sdn-button sdn-button-compact" type="button" on:click={() => void confirmResetSubscriptionData(schema)} disabled={resetRunning || resetConfirmText.trim() !== 'RESET'}>Clear</button>
                          <button class="sdn-button sdn-button-muted sdn-button-compact" type="button" on:click={cancelResetSubscriptionData} disabled={resetRunning}>Cancel</button>
                        </div>
                      </div>
                    {:else}
                      <button class="sdn-button sdn-button-muted sdn-button-compact" type="button" on:click={() => beginResetSubscriptionData(schema.subscriptionId)} disabled={resetRunning}>Reset row</button>
                    {/if}
                  </td>
                </tr>
              {:else}
                <tr><td colspan="10">No subscribed data feeds.</td></tr>
              {/each}
            </tbody>
          </table>
          {#if resetStatus}
            <p class="sdn-empty-inline" role="status">{resetStatus}</p>
          {/if}
        </section>

        <section class="sdn-explorer-panel" aria-label="Local SQL">
          <div class="sdn-workbench-controls">
            <label>
              <span>Table</span>
              <select class="sdn-input sdn-select" bind:value={selectedStandardId} on:change={handleTableChange}>
                {#each schemaSyncRows as standard}
                  <option value={standard.id}>{standard.id} ({formatNumber(standard.remoteRows)})</option>
                {/each}
              </select>
            </label>

            <label class="sdn-workbench-sql">
              <span>SQL</span>
              <textarea class="sdn-input sdn-sql-input" bind:value={sqlQueryText} on:input={handleSqlInput} rows="2" spellcheck="false"></textarea>
            </label>

            <label>
              <span>Page size</span>
              <select class="sdn-input sdn-select" bind:value={pageSize} on:change={handlePageSizeChange}>
                {#each PAGE_SIZE_OPTIONS as option}
                  <option value={option}>{option}</option>
                {/each}
              </select>
            </label>

            {#if !sqlResult}
              <div class="sdn-column-menu">
                <button class="sdn-button sdn-button-muted" type="button" on:click={() => columnMenuOpen = !columnMenuOpen}>Columns</button>
                {#if columnMenuOpen}
                  <div class="sdn-column-menu-panel">
                    {#each allColumns as column}
                      <label>
                        <input
                          type="checkbox"
                          checked={visibleColumnKeys.includes(column.key)}
                          on:change={() => toggleColumn(column.key)}
                        />
                        <span>{column.label}</span>
                      </label>
                    {/each}
                  </div>
                {/if}
              </div>
            {/if}

            <button class="sdn-button sdn-button-muted" type="button" on:click={() => void runSqlQuery()} disabled={sqlRunning}>Run SQL</button>
          </div>

          <div class="sdn-dataset-summary" aria-label="Dataset summary">
            <div class="sdn-dataset-metric" aria-label={`Remote rows ${formatNumber(estimatedTotalRows ?? 0)}`}>
              <span>Remote rows</span>
              <strong>{formatNumber(estimatedTotalRows ?? 0)}</strong>
            </div>
            <div class="sdn-dataset-metric" aria-label={`Local rows ${formatNumber(localRowCount)}`}>
              <span>Local rows</span>
              <strong>{formatNumber(localRowCount)}</strong>
            </div>
            <div class="sdn-dataset-metric" aria-label={`Cached ${formatBytes(cachedByteCount)}`}>
              <span>Cached</span>
              <strong>{formatBytes(cachedByteCount)}</strong>
            </div>
            <div class="sdn-dataset-metric" aria-label={`Scan ${scanHashLabel}`}>
              <span>Scan</span>
              <strong>{scanHashLabel}</strong>
            </div>
          </div>

          <div class="sdn-table-wrap sdn-workbench-table-wrap">
            <table class="sdn-table sdn-workbench-table" aria-label="Data rows">
              {#if sqlResult}
                <thead>
                  <tr>
                    {#each displaySqlColumns as column}
                      <th>{column}</th>
                    {/each}
                  </tr>
                </thead>
                <tbody>
                  {#each sqlRecords as row}
                    <tr>
                      {#each displaySqlColumns as column}
                        <td title={sqlCellValue(row, column)}>{displaySqlCellValue(row, column)}</td>
                      {/each}
                    </tr>
                  {:else}
                    <tr>
                      <td colspan={Math.max(1, displaySqlColumns.length)}>No rows loaded for {selectedStandardId}.</td>
                    </tr>
                  {/each}
                </tbody>
              {:else}
                <thead>
                  <tr>
                    {#each visibleColumns as column}
                      <th aria-sort={sortAria(column.key)}>
                        <button class="sdn-sort-button" type="button" on:click={() => setSort(column.key)}>
                          {sortableHeader(column.key, column.label)}
                        </button>
                      </th>
                    {/each}
                  </tr>
                </thead>
                <tbody>
                  {#each visibleRows as row}
                    <tr
                      class:sdn-clickable-row={selectedStandardId === 'PNM'}
                      class:active={selectedPnmRow?.record.cid === row.record.cid}
                      role={selectedStandardId === 'PNM' ? 'button' : undefined}
                      tabindex={selectedStandardId === 'PNM' ? 0 : undefined}
                      on:click={() => handleWorkbenchRowClick(row)}
                      on:keydown={(event) => handleWorkbenchRowKeydown(row, event)}
                    >
                      {#each visibleColumns as column}
                        <td title={fullCellValue(row, column)}>{displayCellValue(row, column)}</td>
                      {/each}
                    </tr>
                  {:else}
                    <tr>
                      <td colspan={Math.max(1, visibleColumns.length)}>No rows loaded for {selectedStandardId}.</td>
                    </tr>
                  {/each}
                </tbody>
              {/if}
            </table>
          </div>

          <div class="sdn-pagination">
            <button class="sdn-button sdn-button-muted" type="button" on:click={goToPreviousPage} disabled={!canGoPrevious}>Previous</button>
            <span class="sdn-page-count">{pageLabel}</span>
            <button class="sdn-button sdn-button-muted" type="button" on:click={goToNextPage} disabled={!canGoNext}>Next</button>
          </div>

          {#if selectedStandardId === 'PNM' && selectedPnmRow}
            <section class="sdn-pnm-detail" aria-label="PNM detail">
              <div class="sdn-pnm-detail-head">
                <div>
                  <strong>PNM publication</strong>
                  <span>{pnmValue(selectedPnmDetails, 'FILE_ID')}</span>
                </div>
                <button class="sdn-button sdn-button-muted" type="button" on:click={() => void verifySelectedPnmSignature()} disabled={pnmSignatureRunning}>Verify signature</button>
              </div>

              <div class="sdn-pnm-fields">
                {#each PNM_STANDARD_COLUMNS as column}
                  {#if hasDisplayValue(selectedPnmDetails[column.key])}
                    <div>
                      <span>{column.label}</span>
                      <code>{pnmValue(selectedPnmDetails, column.key)}</code>
                    </div>
                  {/if}
                {/each}
              </div>

              <label class="sdn-pnm-file-query">
                <span>FILE_ID query</span>
                <div>
                  <input class="sdn-input" bind:value={pnmFileIdQuery} />
                  <button class="sdn-button sdn-button-muted" type="button" on:click={() => void runPnmFileIdQuery()}>Query</button>
                </div>
              </label>

              <div class="sdn-pnm-signature-payload">
                <span>Reconstituted signature payload</span>
                <code>{pnmSignaturePayload(selectedPnmDetails)}</code>
              </div>

              {#if pnmSignatureStatus}
                <p class="sdn-empty-inline" role="status">{pnmSignatureStatus}</p>
              {/if}

              {#if pnmQueryError}
                <p class="sdn-empty-inline" role="alert">{pnmQueryError}</p>
              {:else if pnmQueryResult}
                <div class="sdn-table-wrap sdn-pnm-query-table-wrap">
                  <table class="sdn-table sdn-pnm-query-table" aria-label="PNM FILE_ID results">
                    <thead>
                      <tr>
                        {#each pnmQueryColumns as column}
                          <th>{column}</th>
                        {/each}
                      </tr>
                    </thead>
                    <tbody>
                      {#each pnmQueryRows as row}
                        <tr>
                          {#each pnmQueryColumns as column}
                            <td title={sqlCellValue(row, column)}>{displaySqlCellValue(row, column)}</td>
                          {/each}
                        </tr>
                      {:else}
                        <tr>
                          <td colspan={Math.max(1, pnmQueryColumns.length)}>No PNMs found for this FILE_ID.</td>
                        </tr>
                      {/each}
                    </tbody>
                  </table>
                </div>
              {/if}
            </section>
          {/if}

          {#if sqlError}
            <p class="sdn-empty-inline" role="alert">{sqlError}</p>
          {/if}
        </section>
  </div>
</article>

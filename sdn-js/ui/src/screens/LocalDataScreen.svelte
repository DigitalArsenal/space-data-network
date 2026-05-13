<script lang="ts">
  import { decodeEpmFlatBuffer } from '../../../src/ui/runtime/epm-flatbuffer';
  import {
    DEFAULT_DATA_FEED_QUERY_PROFILE,
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
  import { artifactPeerAddrsForTrustedPeers, normalizeIpfsArtifactPeerAddrs, prioritizeIpfsArtifactPeerAddrs } from '../../../src/ui/runtime/ipfs-artifact-peers';
  import {
    buildEpochProfileSql,
    EPOCH_SQL_PROFILES,
    type EpochSqlProfile,
  } from '../../../src/ui/runtime/epoch-query-sql';
  import { createDeterministicLocalLlmQueryAdapter } from '../../../src/ui/runtime/llm-query-adapter';
  import { buildLocalLlmQueryContext } from '../../../src/ui/runtime/llm-query-context';
  import { boundedWireSpeedUtilization } from '../../../src/ui/runtime/sync-throughput';
  import { syncRowCountSummary, syncRowCountSummaryLabel } from '../../../src/ui/runtime/sync-progress';
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
  type DataSection = 'storage' | 'subscriptions' | 'explorer';
  type SchemaSyncMode = 'preview' | 'sync';
  type SchemaSyncStatus = 'idle' | 'syncing' | 'synced' | 'capped' | 'error';
  type StorageUnit = 'MB' | 'GB' | 'TB';
  type DataQueryProfile = 'ordered-offset-v1' | 'dataset-publication-offset-v1';

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
    providerId?: string | null;
    sourceName?: string | null;
    kind: 'local' | 'configured';
    syncAddrs?: string[];
    artifactPeerAddrs?: string[];
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
    pinnedRows: number;
    missingRows: number;
    cachedBytes: number;
    pinnedBytes: number;
    downloadedBytes: number;
    downloadSpeedBytesPerSecond: number;
    measuredWireSpeedBytesPerSecond: number;
    wireSpeedUtilization: number | null;
    wireSpeedTarget: number;
    wireSpeedTargetMet: boolean | null;
    manifestDiscoveryMs: number;
    networkTransferMs: number;
    verificationMs: number;
    flatSqlMaterializationMs: number;
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
    syncFilter: string | null;
    verifiedChunks: string[];
    lastSyncedAt: string | null;
    error: string | null;
  }

  interface SchemaSyncRow extends StandardOption {
    subscriptionId: string;
    dataSourceId: string;
    datastoreKey: string | null;
    providerName: string;
    providerPeerId: string | null;
    providerPublicKey: string | null;
    syncFilter: string;
    queryProfile: DataQueryProfile;
    localRows: number;
    cachedBytes: number;
    preference: SchemaSyncPreference;
    progress: SchemaSyncProgress;
  }

  interface ExplorerSourceOption {
    id: string;
    dataSourceId: string;
    datastoreKey: string | null;
    label: string;
    detail: string;
  }

  export let backend: SdnBackend | null = null;
  export let peers: ObservedSdnPeer[] = [];
  export let trustedPeers: ObservedSdnPeer[] = [];
  export let route = '/data';

  const DEFAULT_STANDARD_ID = 'EPM';
  const LOCAL_DATA_SOURCE_ID = 'local';
  const SCHEMA_EXTENSION = 'fbs';
  const DEFAULT_PAGE_SIZE = 10;
  const SYNC_PAGE_SIZE = 50_000;
  const SYNC_PERSIST_RECORD_INTERVAL = 100_000;
  const PAGE_SIZE_OPTIONS = [10, 25, 50, 100];
  const DEFAULT_QUERY_PROFILE = DEFAULT_DATA_FEED_QUERY_PROFILE as DataQueryProfile;
  const DATA_QUERY_PROFILES: Array<{ id: DataQueryProfile; label: string }> = [
    { id: 'ordered-offset-v1', label: 'Ordered offset' },
    { id: 'dataset-publication-offset-v1', label: 'Published artifacts' },
  ];
  const DATA_SECTIONS: Array<{ id: DataSection; label: string; breadcrumb: string }> = [
    { id: 'storage', label: 'Storage', breadcrumb: 'Data / Storage' },
    { id: 'subscriptions', label: 'Sync settings', breadcrumb: 'Data / Sync Settings' },
    { id: 'explorer', label: 'Explorer', breadcrumb: 'Data / Explorer' },
  ];
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
  let selectedDataSection: DataSection = 'storage';
  let selectedStandardId = DEFAULT_STANDARD_ID;
  let selectedDataSourceId = LOCAL_DATA_SOURCE_ID;
  let selectedSubscriptionId = '';
  let selectedDatastoreKey: string | null = null;
  let selectedExplorerSourceKey = '';
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
  let llmAskText = '';
  let llmDraftRunning = false;
  let llmDraftError = '';
  let llmDraftRationale = '';
  let epochProfile: EpochSqlProfile = 'epoch.day';
  let epochDay = defaultEpochDay();
  let epochAt = `${epochDay}T00:00`;
  let epochFrom = `${epochDay}T00:00`;
  let epochTo = `${nextEpochDay(epochDay)}T00:00`;
  let epochMaxDeltaSeconds = 86400;
  let epochEntityId = '';
  let epochQueryError = '';
  let dataDirectoryState: DataDirectoryState = loadDataDirectoryState();
  let schemaSyncPreferences: Record<string, SchemaSyncPreference> = loadSchemaSyncPreferences();
  let schemaSyncProgress: Record<string, SchemaSyncProgress> = loadSchemaSyncProgress();
  let activeSyncKeys = new Set<string>();
  let pausedSyncKeys = new Set<string>();
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
  let pinVerifyStatus = '';
  let pinVerifyRunning = false;
  const schemaSyncScheduler = createSchemaSyncScheduler({
    syncSchema: (standardId, dataSourceId, subscriptionId) => synchronizeSchema(standardId, dataSourceId, subscriptionId),
  });
  const schemaSyncSchedulers = new Map<string, typeof schemaSyncScheduler>([[LOCAL_DATA_SOURCE_ID, schemaSyncScheduler]]);

  $: dataSourceOptions = buildDataSourceOptions(backend, configuredDataSources, peers);
  $: schemaSyncRows = buildSubscribedSchemaSyncRows(dataDirectoryState.subscriptions, selectedDataSourceId, selectedDatastoreKey, localFlatSqlStats, schemaSyncPreferences);
  $: subscribedSourceOptions = buildSubscribedSourceOptions(schemaSyncRows);
  $: selectedExplorerSourceKey = subscriptionSourceKey(selectedDataSourceId, selectedDatastoreKey);
  $: subscribedStandardOptions = schemaSyncRows.filter((row) => subscriptionSourceKey(row.dataSourceId, row.datastoreKey) === subscriptionSourceKey(selectedDataSourceId, selectedDatastoreKey));
  $: activeStorageRows = schemaSyncRows.filter((row) => row.preference.mode === 'sync');
  $: selectedSchemaSyncRow = schemaSyncRows.find((row) => selectedSubscriptionId && row.subscriptionId === selectedSubscriptionId)
    ?? schemaSyncRows.find((row) => row.id === selectedStandardId && row.dataSourceId === selectedDataSourceId && (selectedDatastoreKey ? row.datastoreKey === selectedDatastoreKey : true))
    ?? schemaSyncRows.find((row) => row.id === selectedStandardId && row.dataSourceId === selectedDataSourceId)
    ?? schemaSyncRows.find((row) => row.id === selectedStandardId)
    ?? null;
  $: selectedDataSectionMeta = DATA_SECTIONS.find((section) => section.id === selectedDataSection) ?? DATA_SECTIONS[0];
  $: syncSelectedStandardWithSubscriptions(schemaSyncRows);
  $: decodedRows = rawRecords.map(decodeWorkbenchRecord);
  $: allColumns = workbenchColumnsForStandard(selectedStandardId, decodedRows);
  $: syncVisibleColumnKeys(allColumns);
  $: visibleColumns = allColumns.filter((column) => visibleColumnKeys.includes(column.key));
  $: filteredRows = filterRows(decodedRows, searchText);
  $: visibleRows = sortRows(filteredRows, sortColumn, sortDirection);
  $: estimatedTotalRows = scanTotalRowsForStandard(dataScan, selectedStandardId)
    ?? selectedSchemaSyncRow?.remoteRows
    ?? totalRowsForStandardId(dataSummary, selectedStandardId)
    ?? null;
  $: totalPageCount = estimatedTotalRows === null ? Math.max(1, pageIndex + (canGoNext ? 2 : 1)) : Math.max(1, Math.ceil(estimatedTotalRows / normalizedPageSize()));
  $: canGoPrevious = pageIndex > 0;
  $: canGoNext = rawRecords.length >= pageSize && (estimatedTotalRows === null || ((pageIndex + 1) * pageSize) < estimatedTotalRows);
  $: pageLabel = `${pageIndex + 1}/${totalPageCount}`;
  $: selectedLocalFlatSqlStats = localFlatSqlStats.find((entry) => entry.standardId === selectedStandardId) ?? null;
  $: localRowCount = localRowsForStandard(localFlatSqlStats, selectedStandardId) || selectedSchemaSyncRow?.localRows || 0;
  $: cachedByteCount = selectedLocalFlatSqlStats?.cachedBytes ?? selectedSchemaSyncRow?.cachedBytes ?? 0;
  $: pinnedRowCount = selectedSchemaSyncRow?.progress.pinnedRows ?? selectedLocalFlatSqlStats?.pinnedRows ?? 0;
  $: lastSyncedLabel = selectedSchemaSyncRow?.progress.lastSyncedAt
    ? formatDateTime(selectedSchemaSyncRow.progress.lastSyncedAt)
    : selectedLocalFlatSqlStats?.lastSyncedAt
      ? formatDateTime(selectedLocalFlatSqlStats.lastSyncedAt)
      : 'Never synced';
  $: transportStateLabel = selectedSchemaSyncRow?.progress.syncProtocol
    ?? dataScan?.syncProtocol
    ?? (selectedDataSourceId === LOCAL_DATA_SOURCE_ID ? 'local' : 'pending');
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
        const store = await ensureLocalFlatSqlStore(selectedDataSourceId, selectedDatastoreKey);
        dataSummary = store ? await store.getRemoteDataSummary(workerBackendConfig) : null;
        refreshSubscriptionRemoteRowsFromSummary(source?.id ?? selectedDataSourceId, dataSummary);
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
      refreshSubscriptionRemoteRowsFromSummary(source?.id ?? selectedDataSourceId, result.data);
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
    const activeSelection = selectedSchemaSyncRowForSelection();
    const query = {
      schema: schemaNameForStandardId(selectedStandardId),
      ...(activeSelection?.datastoreKey ? { datastoreKey: activeSelection.datastoreKey } : {}),
      ...(activeSelection?.syncFilter ? { syncFilter: activeSelection.syncFilter } : {}),
      queryProfile: subscriptionQueryProfileFor(activeSelection),
      limit: normalizedPageSize(),
      offset: nextPage * normalizedPageSize(),
    };
    workbenchLoading = true;
    try {
      const source = currentDataSourceOption();
      const workerBackendConfig = backendConfigForDataSource(source, activeSelection?.datastoreKey ?? selectedDatastoreKey);
      if (workerBackendConfig) {
        const store = await ensureLocalFlatSqlStore(selectedDataSourceId, activeSelection?.datastoreKey ?? selectedDatastoreKey);
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
          ...(query.datastoreKey ? { datastoreKey: query.datastoreKey } : {}),
          scanHash: dataScan.scanHash,
          chunkHash: dataScan.chunkHash || dataScan.scanHash,
          snapshotId: dataScan.snapshotId,
          head: dataScan.head,
          cursor: dataScan.cursor,
          nextCursor: dataScan.nextCursor,
          totalCount: dataScan.totalCount,
          highWaterMark: dataScan.highWaterMark,
          queryProfile: dataScan.queryProfile,
          ...(activeSelection?.syncFilter ? { syncFilter: activeSelection.syncFilter } : {}),
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

  function handleExplorerSourceChange(event: Event): void {
    selectedExplorerSourceKey = (event.currentTarget as HTMLSelectElement).value;
    const sourceRows = schemaSyncRows.filter((row) => subscriptionSourceKey(row.dataSourceId, row.datastoreKey) === selectedExplorerSourceKey);
    const selected = sourceRows.find((row) => row.id === selectedStandardId) ?? sourceRows[0] ?? null;
    if (selected) selectExplorerSchemaRow(selected);
  }

  function handleExplorerStandardChange(event: Event): void {
    selectedStandardId = (event.currentTarget as HTMLSelectElement).value;
    const selected = subscribedStandardOptions.find((row) => row.id === selectedStandardId) ?? subscribedStandardOptions[0] ?? null;
    if (selected) selectExplorerSchemaRow(selected);
  }

  function selectExplorerSchemaRow(selected: SchemaSyncRow): void {
    selectedSubscriptionId = selected.subscriptionId;
    selectedStandardId = selected.id;
    selectedDataSourceId = selected.dataSourceId;
    selectedDatastoreKey = selected.datastoreKey;
    resetLocalFlatSqlStore();
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

  function goToPreviousPage(): void {
    if (canGoPrevious) void runWorkbenchQuery(pageIndex - 1);
  }

  function goToNextPage(): void {
    if (canGoNext) void runWorkbenchQuery(pageIndex + 1);
  }

  function handleSqlInput(): void {
    userEditedSql = true;
  }

  function setDataSection(section: DataSection): void {
    selectedDataSection = section;
  }

  function syncSelectedStandardWithSubscriptions(rows: SchemaSyncRow[]): void {
    if (rows.length === 0) return;
    if (selectedSubscriptionId && rows.some((row) => row.subscriptionId === selectedSubscriptionId)) return;
    if (userSelectedStandard && rows.some((row) => (
      row.id === selectedStandardId
      && row.dataSourceId === selectedDataSourceId
      && (!selectedDatastoreKey || row.datastoreKey === selectedDatastoreKey)
    ))) return;
    const next = rows[0];
    selectedSubscriptionId = next.subscriptionId;
    selectedStandardId = next.id;
    selectedDataSourceId = next.dataSourceId;
    selectedDatastoreKey = next.datastoreKey;
    resetSqlForSelectedStandard();
    clearPnmSelection();
    dataScan = null;
    pageIndex = 0;
  }

  function isSchemaRowSelected(schema: SchemaSyncRow): boolean {
    if (selectedSubscriptionId) return schema.subscriptionId === selectedSubscriptionId;
    return schema.id === selectedStandardId
      && schema.dataSourceId === selectedDataSourceId
      && (selectedDatastoreKey === null || schema.datastoreKey === selectedDatastoreKey);
  }

  function selectedSchemaSyncRowForSelection(): SchemaSyncRow | null {
    return schemaSyncRows.find((row) => selectedSubscriptionId && row.subscriptionId === selectedSubscriptionId)
      ?? schemaSyncRows.find((row) => (
        row.id === selectedStandardId
        && row.dataSourceId === selectedDataSourceId
        && (selectedDatastoreKey === null || row.datastoreKey === selectedDatastoreKey)
      ))
      ?? null;
  }

  function handleSubscriptionStorageCapInput(schema: SchemaSyncRow, event: Event): void {
    const storageCap = normalizedStorageCap((event.currentTarget as HTMLInputElement).value);
    updateSubscription(schema.subscriptionId, { storageCap });
    updateSchemaSyncPreference(schema.id, { mode: 'sync', storageCap }, schema.dataSourceId, schema.datastoreKey);
    scheduleSubscribedSchemaSyncs(schemaSyncRows);
  }

  function handleSubscriptionStorageUnitChange(schema: SchemaSyncRow, event: Event): void {
    const value = (event.currentTarget as HTMLSelectElement).value;
    const storageUnit = isStorageUnit(value) ? value : DEFAULT_SCHEMA_SYNC_PREFERENCE.storageUnit;
    updateSubscription(schema.subscriptionId, { storageUnit });
    updateSchemaSyncPreference(schema.id, { mode: 'sync', storageUnit }, schema.dataSourceId, schema.datastoreKey);
    scheduleSubscribedSchemaSyncs(schemaSyncRows);
  }

  function handleSubscriptionFilterInput(schema: SchemaSyncRow, event: Event): void {
    updateSubscription(schema.subscriptionId, {
      syncFilter: (event.currentTarget as HTMLInputElement).value,
    });
  }

  function handleSubscriptionQueryProfileChange(schema: SchemaSyncRow, event: Event): void {
    updateSubscription(schema.subscriptionId, {
      queryProfile: normalizeDataQueryProfile((event.currentTarget as HTMLSelectElement).value),
    });
    schemaSyncSchedulerForDataSource(schema.dataSourceId).reset();
  }

  function pauseSubscriptionSync(schema: SchemaSyncRow): void {
    const key = schemaSyncPreferenceKey(schema.dataSourceId, schema.id, schema.datastoreKey);
    pausedSyncKeys = new Set(pausedSyncKeys).add(key);
    updateSchemaSyncPreference(schema.id, { mode: 'preview' }, schema.dataSourceId, schema.datastoreKey);
    schemaSyncSchedulerForDataSource(schema.dataSourceId).reset();
    if (activeSyncKeys.has(key)) {
      const nextActive = new Set(activeSyncKeys);
      nextActive.delete(key);
      activeSyncKeys = nextActive;
      resetLocalFlatSqlStore();
    }
    refreshSchemaSyncProgress(schema.id, {
      status: 'idle',
      error: null,
      downloadSpeedBytesPerSecond: 0,
      wireSpeedUtilization: null,
    }, schema.dataSourceId, schema.datastoreKey);
  }

  function resumeSubscriptionSync(schema: SchemaSyncRow): void {
    const key = schemaSyncPreferenceKey(schema.dataSourceId, schema.id, schema.datastoreKey);
    const nextPaused = new Set(pausedSyncKeys);
    nextPaused.delete(key);
    pausedSyncKeys = nextPaused;
    updateSchemaSyncPreference(schema.id, { mode: 'sync' }, schema.dataSourceId, schema.datastoreKey);
    scheduleSubscribedSchemaSyncs(schemaSyncRows);
    void synchronizeSchema(schema.id, schema.dataSourceId, schema.subscriptionId);
  }

  function retrySubscriptionSync(schema: SchemaSyncRow): void {
    const key = schemaSyncPreferenceKey(schema.dataSourceId, schema.id, schema.datastoreKey);
    const nextPaused = new Set(pausedSyncKeys);
    nextPaused.delete(key);
    pausedSyncKeys = nextPaused;
    const nextActive = new Set(activeSyncKeys);
    nextActive.delete(key);
    activeSyncKeys = nextActive;
    updateSchemaSyncPreference(schema.id, { mode: 'sync' }, schema.dataSourceId, schema.datastoreKey);
    refreshSchemaSyncProgress(schema.id, {
      status: 'idle',
      error: null,
      downloadSpeedBytesPerSecond: 0,
      wireSpeedUtilization: null,
    }, schema.dataSourceId, schema.datastoreKey);
    schemaSyncSchedulerForDataSource(schema.dataSourceId).reset();
    void synchronizeSchema(schema.id, schema.dataSourceId, schema.subscriptionId);
  }

  async function verifyPinnedArtifacts(schema: SchemaSyncRow): Promise<void> {
    pinVerifyRunning = true;
    pinVerifyStatus = '';
    try {
      const store = await ensureLocalFlatSqlStore(schema.dataSourceId, schema.datastoreKey);
      if (!store) throw new Error('FlatSQL initialization failed');
      const entries = await store.listPinLedgerEntries({
        standardId: schema.id,
        role: 'shard',
        verificationState: 'verified',
      });
      const pinnedRows = entries.reduce((total, entry) => total + Math.max(0, entry.rowCount ?? 0), 0);
      const pinnedBytes = entries.reduce((total, entry) => total + Math.max(0, entry.byteCount ?? 0), 0);
      if (entries.length === 0) {
        pinVerifyStatus = `No verified pinned ${schema.id} shard artifacts for ${schema.providerName}.`;
        return;
      }
      pinVerifyStatus = `Verified ${formatNumber(entries.length)} ${schema.id} shard artifacts covering ${formatNumber(pinnedRows)} rows / ${formatBytes(pinnedBytes)}.`;
    } catch (error) {
      pinVerifyStatus = error instanceof Error ? error.message : 'Pinned artifact verification failed';
    } finally {
      pinVerifyRunning = false;
    }
  }

  function updateSubscription(subscriptionId: string, patch: Partial<Pick<DataFeedSubscription, 'remoteRows' | 'storageCap' | 'storageUnit' | 'syncFilter' | 'queryProfile'>>): void {
    dataDirectoryState = updateDataFeedSubscription(dataDirectoryState, subscriptionId, patch);
    persistDataDirectoryState(dataDirectoryState);
  }

  function refreshSubscriptionRemoteRowsFromSummary(dataSourceId: string, summary: DataSummary | null): void {
    if (!summary) return;
    let nextState = dataDirectoryState;
    for (const subscription of dataDirectoryState.subscriptions) {
      if (subscription.dataSourceId !== dataSourceId) continue;
      const remoteRows = remoteRowsForSummarySubscription(summary, subscription);
      if (remoteRows === null || remoteRows === subscription.remoteRows) continue;
      nextState = updateDataFeedSubscription(nextState, subscription.id, { remoteRows });
    }
    if (nextState === dataDirectoryState) return;
    dataDirectoryState = nextState;
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
      const store = await ensureLocalFlatSqlStore(selectedDataSourceId, selectedSchemaSyncRow?.datastoreKey ?? selectedDatastoreKey);
      if (!store) return;
      sqlResult = await store.query(query, selectedStandardId);
    } catch (error) {
      sqlResult = null;
      sqlError = error instanceof Error ? error.message : 'SQL query failed';
    } finally {
      sqlRunning = false;
    }
  }

  async function draftLocalLlmSql(): Promise<void> {
    const ask = llmAskText.trim();
    if (!ask || localRowCount <= 0) return;
    llmDraftRunning = true;
    llmDraftError = '';
    llmDraftRationale = '';
    try {
      const source = currentDataSourceOption();
      const schemaRow = selectedSchemaSyncRow;
      const context = buildLocalLlmQueryContext({
        standardId: selectedStandardId,
        schemaName: schemaNameForStandardId(selectedStandardId),
        tableName: standardIdFromSchema(selectedStandardId),
        columns: llmSqlColumnsForStandard(selectedStandardId),
        queryProfile: subscriptionQueryProfileFor(schemaRow),
        source: {
          dataSourceId: selectedDataSourceId,
          datastoreKey: selectedDatastoreKey,
          providerName: schemaRow?.providerName ?? source?.label ?? selectedDataSourceId,
          providerPeerId: schemaRow?.providerPeerId ?? source?.peerId ?? null,
          providerPublicKey: schemaRow?.providerPublicKey ?? source?.publicKey ?? null,
          providerId: source?.providerId ?? schemaRow?.dataSourceId ?? null,
          sourceName: source?.sourceName ?? null,
        },
        sampleRows: visibleRows.map((row) => row.decoded),
        maxRows: normalizedPageSize(),
      });
      const adapter = createDeterministicLocalLlmQueryAdapter();
      const draft = await adapter.draftSql({ ask, context });
      sqlQueryText = draft.sql;
      llmDraftRationale = draft.rationale;
      userEditedSql = true;
      sqlResult = null;
      sqlError = '';
    } catch (error) {
      llmDraftError = error instanceof Error ? error.message : 'Plaintext query draft failed';
    } finally {
      llmDraftRunning = false;
    }
  }

  async function applyEpochProfileQuery(): Promise<void> {
    epochQueryError = '';
    try {
      sqlQueryText = buildEpochProfileSql({
        standardId: selectedStandardId,
        profile: epochProfile,
        day: epochDay,
        at: epochAt,
        from: epochFrom,
        to: epochTo,
        maxDeltaSeconds: epochMaxDeltaSeconds,
        entityId: epochEntityId,
        limit: normalizedPageSize(),
      });
      userEditedSql = true;
      await runSqlQuery();
    } catch (error) {
      epochQueryError = error instanceof Error ? error.message : 'Epoch query failed';
    }
  }

  async function ingestDownloadedRecords(records: RawDataRecord[]): Promise<void> {
    if (records.length === 0) return;
    try {
      const store = await ensureLocalFlatSqlStore(selectedDataSourceId, selectedSchemaSyncRow?.datastoreKey ?? selectedDatastoreKey);
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
      syncSchema: (standardId, sourceId, subscriptionId) => synchronizeSchema(standardId, sourceId, subscriptionId),
    });
    schemaSyncSchedulers.set(dataSourceId, scheduler);
    return scheduler;
  }

  function resetSchemaSyncSchedulers(): void {
    schemaSyncScheduler.reset();
    for (const scheduler of schemaSyncSchedulers.values()) scheduler.reset();
  }

  async function synchronizeSchema(standardId: string, dataSourceId = selectedDataSourceId, subscriptionId = ''): Promise<void> {
    const subscription = subscriptionForSync(dataSourceId, standardId, subscriptionId);
    const datastoreKey = subscription?.datastoreKey ?? null;
    const key = schemaSyncPreferenceKey(dataSourceId, standardId, datastoreKey);
    const preference = schemaSyncPreferenceFor(dataSourceId, standardId, datastoreKey);
    if (preference.mode !== 'sync' || activeSyncKeys.has(key)) return;
    if (pausedSyncKeys.has(key)) return;

    const source = dataSourceOptionForId(dataSourceId);
    const backendConfig = backendConfigForDataSource(source, datastoreKey);
    if (!backendConfig) return;
    const remoteRows = subscription?.remoteRows ?? remoteRowsForSubscription(dataSourceId, standardId, datastoreKey) ?? totalRowsForStandardId(dataSummary, standardId) ?? 0;
    const syncFilter = subscription?.syncFilter ?? syncFilterForSubscription(dataSourceId, standardId, datastoreKey);
    const queryProfile = subscriptionQueryProfileFor(subscription);
    let initialProgress = schemaSyncProgressFor(
      dataSourceId,
      standardId,
      remoteRows,
      localFlatSqlStats,
      selectedDataSourceId === dataSourceId && selectedDatastoreKey === datastoreKey,
      datastoreKey,
    );
    if (syncFilterChangedRequiresReset(initialProgress, syncFilter)) {
      const persistenceKey = localFlatSqlPersistenceKey(dataSourceId, datastoreKey);
      if (localFlatSqlStoreKey === persistenceKey) resetLocalFlatSqlStore();
      await clearLocalFlatSqlStore({
        persistenceKey,
        standardIds: [standardId],
      });
      clearSchemaSyncProgressForSubscription(dataSourceId, standardId, datastoreKey);
      if (selectedDataSourceId === dataSourceId && selectedDatastoreKey === datastoreKey) {
        await refreshLocalFlatSqlStats();
      }
      initialProgress = schemaSyncProgressFor(
        dataSourceId,
        standardId,
        remoteRows,
        localFlatSqlStats,
        selectedDataSourceId === dataSourceId && selectedDatastoreKey === datastoreKey,
        datastoreKey,
      );
    }
    activeSyncKeys = new Set(activeSyncKeys).add(key);
    refreshSchemaSyncProgress(standardId, {
      status: 'syncing',
      error: null,
      totalRows: remoteRows,
      providerPeerId: source?.peerId ?? null,
      providerPublicKey: source?.publicKey ?? null,
      syncFilter: syncFilter || null,
    }, dataSourceId, datastoreKey);

    let store: WorkerLocalFlatSqlStore | null = null;
    try {
      store = await ensureLocalFlatSqlStore(dataSourceId, datastoreKey);
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
        syncFilter,
        queryProfile,
      }, (nextUpdate) => applyWorkerSchemaSyncUpdate(standardId, dataSourceId, datastoreKey, nextUpdate));
      applyWorkerSchemaSyncUpdate(standardId, dataSourceId, datastoreKey, update);
    } catch (error) {
      if (pausedSyncKeys.has(key)) {
        refreshSchemaSyncProgress(standardId, {
          status: 'idle',
          error: null,
          downloadSpeedBytesPerSecond: 0,
          wireSpeedUtilization: null,
        }, dataSourceId, datastoreKey);
        return;
      }
      refreshSchemaSyncProgress(standardId, {
        status: 'error',
        error: error instanceof Error ? error.message : 'Schema sync failed',
      }, dataSourceId, datastoreKey);
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

  function applyWorkerSchemaSyncUpdate(standardId: string, dataSourceId: string, datastoreKey: string | null, update: WorkerSchemaSyncUpdate): void {
    if (selectedDataSourceId === dataSourceId && selectedDatastoreKey === datastoreKey) {
      localFlatSqlStats = update.stats;
    }
    refreshSchemaSyncProgress(standardId, update.progress, dataSourceId, datastoreKey);
  }

  function refreshSchemaSyncProgress(standardId: string, patch: Partial<SchemaSyncProgress>, dataSourceId = selectedDataSourceId, datastoreKey: string | null = selectedDatastoreKey): void {
    const key = schemaSyncPreferenceKey(dataSourceId, standardId, datastoreKey);
    const statsAreSelected = selectedDataSourceId === dataSourceId && selectedDatastoreKey === datastoreKey;
    const persisted = schemaSyncProgress[key];
    const localRows = statsAreSelected ? localRowsForStandard(localFlatSqlStats, standardId) : patch.localRows ?? persisted?.localRows ?? 0;
    const cachedBytes = statsAreSelected ? cachedBytesForStandard(localFlatSqlStats, standardId) : patch.cachedBytes ?? persisted?.cachedBytes ?? 0;
    const current = schemaSyncProgressFor(dataSourceId, standardId, totalRowsForStandardId(dataSummary, standardId) ?? 0, localFlatSqlStats, statsAreSelected, datastoreKey);
    const nextProgress = {
      ...current,
      localRows,
      cachedBytes,
      ...patch,
      syncedRows: Math.max(patch.syncedRows ?? current.syncedRows, localRows),
    };
    const wireSpeedUtilization = boundedWireSpeedUtilization(nextProgress.wireSpeedUtilization);
    const rowCounts = syncRowCountSummary({
      localRows: nextProgress.localRows,
      syncedRows: nextProgress.syncedRows,
      pinnedRows: nextProgress.pinnedRows,
      remoteRows: nextProgress.totalRows,
      totalRows: nextProgress.totalRows,
    });
    schemaSyncProgress = {
      ...schemaSyncProgress,
      [key]: {
        ...nextProgress,
        ...rowCounts,
        wireSpeedUtilization,
      },
    };
    persistMeasuredWireSpeedBytesPerSecond(dataSourceId, schemaSyncProgress[key]?.measuredWireSpeedBytesPerSecond ?? 0);
    persistSchemaSyncProgress(schemaSyncProgress);
  }

  async function ensureLocalFlatSqlStore(dataSourceId = selectedDataSourceId, datastoreKey: string | null = selectedDatastoreKey): Promise<WorkerLocalFlatSqlStore | null> {
    const nextKey = localFlatSqlPersistenceKey(dataSourceId, datastoreKey);
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

  function localFlatSqlPersistenceKey(dataSourceId: string, datastoreKey: string | null = null): string {
    return datastoreKey ? `sdn-data:${dataSourceId}:${datastoreKey}` : `sdn-data:${dataSourceId}`;
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
        persistenceKey: localFlatSqlPersistenceKey(dataSourceId, schema.datastoreKey),
        standardIds: [standardId],
      });
      clearSchemaSyncProgressForSubscription(dataSourceId, standardId, schema.datastoreKey);
      const nextActive = new Set(activeSyncKeys);
      nextActive.delete(schemaSyncPreferenceKey(dataSourceId, standardId, schema.datastoreKey));
      activeSyncKeys = nextActive;
      schemaSyncSchedulerForDataSource(dataSourceId).reset();
      rawRecords = [];
      dataScan = null;
      clearPnmSelection();
      resetSqlForSelectedStandard();
      selectedDataSourceId = dataSourceId;
      selectedStandardId = standardId;
      selectedSubscriptionId = schema.subscriptionId;
      selectedDatastoreKey = schema.datastoreKey;
      await ensureLocalFlatSqlStore(dataSourceId, schema.datastoreKey);
      await refreshLocalFlatSqlStats();
      resetStatus = `${standardId} row reset. Sync will restart from the first remote row.`;
      resetSubscriptionId = '';
      resetConfirmText = '';
      void synchronizeSchema(standardId, dataSourceId, schema.subscriptionId);
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
      const store = await ensureLocalFlatSqlStore(selectedDataSourceId, selectedSchemaSyncRow?.datastoreKey ?? selectedDatastoreKey);
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

  function llmSqlColumnsForStandard(standardId: string): string[] {
    const standardColumns = STANDARD_FIELD_COLUMNS[standardIdFromSchema(standardId)] ?? [];
    if (standardColumns.length > 0) return standardColumns.map((column) => column.key);
    return allColumns
      .filter((column) => column.source === 'standard')
      .map((column) => column.key)
      .filter((key) => !INTERNAL_COLUMN_KEYS.has(key) && !isInternalSqlColumn(key));
  }

  function defaultEpochDay(): string {
    return new Date().toISOString().slice(0, 10);
  }

  function nextEpochDay(day: string): string {
    const date = new Date(`${day}T00:00:00Z`);
    if (Number.isNaN(date.getTime())) return defaultEpochDay();
    date.setUTCDate(date.getUTCDate() + 1);
    return date.toISOString().slice(0, 10);
  }

  function buildSubscribedSchemaSyncRows(
    subscriptions: DataFeedSubscription[],
    activeDataSourceId: string,
    activeDatastoreKey: string | null,
    stats: LocalFlatSqlStandardStats[],
    preferences: Record<string, SchemaSyncPreference>,
  ): SchemaSyncRow[] {
    return subscriptions.map((subscription) => {
      const sourceStats = subscription.dataSourceId === activeDataSourceId && (subscription.datastoreKey ?? null) === activeDatastoreKey ? stats : [];
      const progress = schemaSyncProgressFor(
        subscription.dataSourceId,
        subscription.standardId,
        subscription.remoteRows,
        sourceStats,
        subscription.dataSourceId === activeDataSourceId && (subscription.datastoreKey ?? null) === activeDatastoreKey,
        subscription.datastoreKey ?? null,
      );
      return {
        id: subscription.standardId,
        subscriptionId: subscription.id,
        dataSourceId: subscription.dataSourceId,
        datastoreKey: subscription.datastoreKey,
        providerName: subscription.providerName,
        providerPeerId: subscription.peerId,
        providerPublicKey: subscription.providerPublicKey,
        syncFilter: subscription.syncFilter,
        queryProfile: normalizeDataQueryProfile(subscription.queryProfile),
        remoteRows: Math.max(subscription.remoteRows, progress.totalRows),
        localRows: progress.localRows,
        cachedBytes: progress.cachedBytes,
        preference: subscriptionSchemaSyncPreference(subscription, preferences),
        progress,
      };
    }).sort((left, right) => {
      const delta = right.remoteRows - left.remoteRows;
      return delta === 0 ? left.id.localeCompare(right.id) : delta;
    });
  }

  function buildSubscribedSourceOptions(rows: SchemaSyncRow[]): ExplorerSourceOption[] {
    const options: ExplorerSourceOption[] = [];
    const seen = new Set<string>();
    for (const row of rows) {
      const key = subscriptionSourceKey(row.dataSourceId, row.datastoreKey);
      if (seen.has(key)) continue;
      seen.add(key);
      options.push({
        id: key,
        dataSourceId: row.dataSourceId,
        datastoreKey: row.datastoreKey,
        label: row.providerName,
        detail: row.datastoreKey ?? row.providerPeerId ?? row.providerPublicKey ?? row.dataSourceId,
      });
    }
    return options.sort((left, right) => left.label.localeCompare(right.label) || left.detail.localeCompare(right.detail));
  }

  function subscriptionSourceKey(dataSourceId: string, datastoreKey: string | null = null): string {
    return datastoreKey ? `${dataSourceId}:datastore:${datastoreKey}` : dataSourceId;
  }

  function localRowsForStandard(stats: LocalFlatSqlStandardStats[], standardId: string): number {
    const stat = stats.find((entry) => entry.standardId === standardId);
    return Math.max(stat?.ingestedRecordCount ?? 0, stat?.recordCount ?? 0);
  }

  function cachedBytesForStandard(stats: LocalFlatSqlStandardStats[], standardId: string): number {
    return stats.find((entry) => entry.standardId === standardId)?.cachedBytes ?? 0;
  }

  function localStatsForStandard(stats: LocalFlatSqlStandardStats[], standardId: string): LocalFlatSqlStandardStats | null {
    return stats.find((entry) => entry.standardId === standardId) ?? null;
  }

  function schemaSyncPreferenceFor(dataSourceId: string, standardId: string, datastoreKey: string | null = null): SchemaSyncPreference {
    return schemaSyncPreferences[schemaSyncPreferenceKey(dataSourceId, standardId, datastoreKey)]
      ?? schemaSyncPreferences[schemaSyncPreferenceKey(dataSourceId, standardId)]
      ?? DEFAULT_SCHEMA_SYNC_PREFERENCE;
  }

  function subscriptionSchemaSyncPreference(
    subscription: DataFeedSubscription,
    preferences = schemaSyncPreferences,
  ): SchemaSyncPreference {
    const persisted = preferences[schemaSyncPreferenceKey(subscription.dataSourceId, subscription.standardId, subscription.datastoreKey)]
      ?? preferences[schemaSyncPreferenceKey(subscription.dataSourceId, subscription.standardId)];
    return normalizeSchemaSyncPreference({
      mode: persisted?.mode ?? 'sync',
      storageCap: persisted?.storageCap ?? subscription.storageCap,
      storageUnit: persisted?.storageUnit ?? subscription.storageUnit,
    }) ?? {
      mode: 'sync',
      storageCap: subscription.storageCap,
      storageUnit: subscription.storageUnit,
    };
  }

  function remoteRowsForSubscription(dataSourceId: string, standardId: string, datastoreKey: string | null = null): number | null {
    return dataDirectoryState.subscriptions.find((subscription) => (
      subscription.dataSourceId === dataSourceId
      && subscription.standardId === standardId
      && (datastoreKey === null || subscription.datastoreKey === datastoreKey)
    ))?.remoteRows ?? null;
  }

  function syncFilterForSubscription(dataSourceId: string, standardId: string, datastoreKey: string | null = null): string {
    return dataDirectoryState.subscriptions.find((subscription) => (
      subscription.dataSourceId === dataSourceId
      && subscription.standardId === standardId
      && (datastoreKey === null || subscription.datastoreKey === datastoreKey)
    ))?.syncFilter ?? '';
  }

  function subscriptionQueryProfileFor(subscription: Pick<DataFeedSubscription, 'queryProfile'> | Pick<SchemaSyncRow, 'queryProfile'> | null | undefined): DataQueryProfile {
    return normalizeDataQueryProfile(subscription?.queryProfile);
  }

  function syncFilterChangedRequiresReset(progress: SchemaSyncProgress, nextSyncFilter: string): boolean {
    const previous = progress.syncFilter?.trim() ?? '';
    const next = nextSyncFilter.trim();
    if (previous === next) return false;
    return progress.localRows > 0
      || progress.syncedRows > 0
      || progress.cachedBytes > 0
      || progress.pinnedRows > 0
      || Boolean(progress.lastSyncedAt);
  }

  function subscriptionForSync(dataSourceId: string, standardId: string, subscriptionId = ''): DataFeedSubscription | null {
    return dataDirectoryState.subscriptions.find((subscription) => subscriptionId && subscription.id === subscriptionId)
      ?? dataDirectoryState.subscriptions.find((subscription) => (
        subscription.dataSourceId === dataSourceId
        && subscription.standardId === standardId
        && (selectedDatastoreKey === null || subscription.datastoreKey === selectedDatastoreKey)
      ))
      ?? dataDirectoryState.subscriptions.find((subscription) => (
        subscription.dataSourceId === dataSourceId && subscription.standardId === standardId
      ))
      ?? null;
  }

  function schemaSyncProgressFor(
    dataSourceId: string,
    standardId: string,
    remoteRows: number,
    stats: LocalFlatSqlStandardStats[],
    statsAreAuthoritative = true,
    datastoreKey: string | null = null,
  ): SchemaSyncProgress {
    const key = schemaSyncPreferenceKey(dataSourceId, standardId, datastoreKey);
    const localStats = localStatsForStandard(stats, standardId);
    const localRows = localRowsForStandard(stats, standardId);
    const cachedBytes = cachedBytesForStandard(stats, standardId);
    const persisted = schemaSyncProgress[key];
    const activePersisted = statsAreAuthoritative && localRows === 0 && (persisted?.localRows ?? 0) > 0 ? null : persisted;
    const totalRows = Math.max(remoteRows, activePersisted?.totalRows ?? 0);
    const complete = totalRows > 0 && localRows >= totalRows;
    const active = activeSyncKeys.has(key);
    const status = effectiveSchemaSyncStatus({
      active,
      complete,
      persistedStatus: activePersisted?.status,
    });
    const rowCounts = syncRowCountSummary({
      localRows,
      syncedRows: Math.max(localRows, activePersisted?.syncedRows ?? 0),
      pinnedRows: Math.max(localStats?.pinnedRows ?? 0, activePersisted?.pinnedRows ?? 0),
      remoteRows,
      totalRows,
    });
    return {
      status,
      syncedRows: rowCounts.syncedRows,
      totalRows: rowCounts.totalRows,
      localRows,
      pinnedRows: rowCounts.pinnedRows,
      missingRows: rowCounts.missingRows,
      cachedBytes,
      pinnedBytes: Math.max(localStats?.pinnedBytes ?? 0, activePersisted?.pinnedBytes ?? 0),
      downloadedBytes: activePersisted?.downloadedBytes ?? 0,
      downloadSpeedBytesPerSecond: active ? activePersisted?.downloadSpeedBytesPerSecond ?? 0 : 0,
      measuredWireSpeedBytesPerSecond: activePersisted?.measuredWireSpeedBytesPerSecond ?? measuredWireSpeedBytesPerSecondForSource(dataSourceId),
      wireSpeedUtilization: active ? activePersisted?.wireSpeedUtilization ?? null : null,
      wireSpeedTarget: activePersisted?.wireSpeedTarget ?? 0.8,
      wireSpeedTargetMet: activePersisted?.wireSpeedTargetMet ?? null,
      manifestDiscoveryMs: activePersisted?.manifestDiscoveryMs ?? 0,
      networkTransferMs: activePersisted?.networkTransferMs ?? 0,
      verificationMs: activePersisted?.verificationMs ?? 0,
      flatSqlMaterializationMs: activePersisted?.flatSqlMaterializationMs ?? 0,
      providerPeerId: activePersisted?.providerPeerId ?? null,
      providerPublicKey: activePersisted?.providerPublicKey ?? null,
      snapshotId: localStats?.snapshotId ?? activePersisted?.snapshotId ?? null,
      head: localStats?.head ?? activePersisted?.head ?? null,
      cursor: activePersisted?.cursor ?? null,
      nextCursor: activePersisted?.nextCursor ?? null,
      highWaterMark: localStats?.highWaterMark ?? activePersisted?.highWaterMark ?? null,
      queryProfile: activePersisted?.queryProfile ?? null,
      chunkHash: activePersisted?.chunkHash ?? null,
      syncProtocol: activePersisted?.syncProtocol ?? null,
      syncFilter: activePersisted?.syncFilter ?? null,
      verifiedChunks: activePersisted?.verifiedChunks ?? [],
      lastSyncedAt: localStats?.lastSyncedAt ?? activePersisted?.lastSyncedAt ?? null,
      error: activePersisted?.error ?? null,
    };
  }

  function updateSchemaSyncPreference(standardId: string, patch: Partial<SchemaSyncPreference>, dataSourceId = selectedDataSourceId, datastoreKey: string | null = selectedDatastoreKey): void {
    const key = schemaSyncPreferenceKey(dataSourceId, standardId, datastoreKey);
    const current = schemaSyncPreferenceFor(dataSourceId, standardId, datastoreKey);
    schemaSyncPreferences = {
      ...schemaSyncPreferences,
      [key]: normalizeSchemaSyncPreference({ ...current, ...patch }) ?? DEFAULT_SCHEMA_SYNC_PREFERENCE,
    };
    persistSchemaSyncPreferences(schemaSyncPreferences);
  }

  function schemaSyncPreferenceKey(dataSourceId: string, standardId: string, datastoreKey: string | null = null): string {
    const base = `${dataSourceId}:${standardId.trim().toUpperCase() || DEFAULT_STANDARD_ID}`;
    return datastoreKey ? `${base}:${datastoreKey}` : base;
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

  function persistMeasuredWireSpeedBytesPerSecond(dataSourceId: string, value: number): void {
    if (typeof window === 'undefined') return;
    const numeric = Number(value);
    if (!Number.isFinite(numeric) || numeric <= 0) return;
    try {
      window.localStorage.setItem(`sdn-data-wire-speed-bytes-per-second:${dataSourceId}`, String(Math.floor(numeric)));
    } catch {
      // Storage quota or privacy settings should not block sync.
    }
  }

  function clearSchemaSyncProgressForSubscription(dataSourceId: string, standardId: string, datastoreKey: string | null = null): void {
    const key = schemaSyncPreferenceKey(dataSourceId, standardId, datastoreKey);
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
      pinnedRows: normalizedRowCount(candidate.pinnedRows),
      missingRows: normalizedRowCount(candidate.missingRows),
      cachedBytes: normalizedRowCount(candidate.cachedBytes),
      pinnedBytes: normalizedRowCount(candidate.pinnedBytes),
      downloadedBytes: normalizedRowCount(candidate.downloadedBytes),
      downloadSpeedBytesPerSecond: normalizedRowCount(candidate.downloadSpeedBytesPerSecond),
      measuredWireSpeedBytesPerSecond: normalizedRowCount(candidate.measuredWireSpeedBytesPerSecond),
      wireSpeedUtilization: normalizedOptionalRatio(candidate.wireSpeedUtilization),
      wireSpeedTarget: normalizedOptionalRatio(candidate.wireSpeedTarget) ?? 0.8,
      wireSpeedTargetMet: typeof candidate.wireSpeedTargetMet === 'boolean' ? candidate.wireSpeedTargetMet : null,
      manifestDiscoveryMs: normalizedRowCount(candidate.manifestDiscoveryMs),
      networkTransferMs: normalizedRowCount(candidate.networkTransferMs),
      verificationMs: normalizedRowCount(candidate.verificationMs),
      flatSqlMaterializationMs: normalizedRowCount(candidate.flatSqlMaterializationMs),
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
      syncFilter: normalizedOptionalString(candidate.syncFilter),
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

  function normalizedOptionalRatio(value: unknown): number | null {
    if (value === null || value === undefined || value === '') return null;
    const numeric = Number(value);
    if (!Number.isFinite(numeric) || numeric < 0) return null;
    return boundedWireSpeedUtilization(numeric);
  }

  function isSchemaSyncStatus(value: unknown): value is SchemaSyncStatus {
    return value === 'idle' || value === 'syncing' || value === 'synced' || value === 'capped' || value === 'error';
  }

  function isStorageUnit(value: unknown): value is StorageUnit {
    return typeof value === 'string' && STORAGE_CAP_UNITS.includes(value as StorageUnit);
  }

  function normalizeDataQueryProfile(value: unknown): DataQueryProfile {
    const candidate = String(value ?? '').trim();
    return DATA_QUERY_PROFILES.some((profile) => profile.id === candidate)
      ? candidate as DataQueryProfile
      : DEFAULT_QUERY_PROFILE;
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

  function remoteRowsForSummarySubscription(summary: DataSummary | null, subscription: DataFeedSubscription): number | null {
    if (!summary) return null;
    const datastoreKey = subscription.datastoreKey ?? null;
    if (datastoreKey) {
      const sourceCount = summary.sources.find((source) => (
        source.datastoreKey === datastoreKey
        && standardIdFromSchema(source.schemaName) === subscription.standardId
      ))?.count;
      if (typeof sourceCount === 'number') return sourceCount;
    }
    return totalRowsForStandardId(summary, subscription.standardId);
  }

  function scanTotalRowsForStandard(scan: DataScanResult | null, standardId: string): number | null {
    if (!scan || standardIdFromSchema(scan.schema) !== standardId) return null;
    return Number.isFinite(scan.totalCount) ? scan.totalCount : null;
  }

  function formatNumber(value: number): string {
    return new Intl.NumberFormat('en-US').format(value);
  }

  function syncProgressLabel(schema: SchemaSyncRow): string {
    const rowCounts = syncRowCountSummary({
      localRows: schema.localRows,
      syncedRows: schema.progress.syncedRows,
      pinnedRows: schema.progress.pinnedRows,
      remoteRows: schema.remoteRows,
      totalRows: schema.progress.totalRows,
    });
    if (rowCounts.totalRows === 0) return 'No remote rows';
    return syncRowCountSummaryLabel(rowCounts);
  }

  function syncDownloadSpeedLabel(schema: SchemaSyncRow): string {
    return `Download ${formatBytesPerSecond(schema.progress.downloadSpeedBytesPerSecond)}`;
  }

  function syncTimingLabel(schema: SchemaSyncRow): string {
    const progress = schema.progress;
    return `Timing: manifest ${formatDuration(progress.manifestDiscoveryMs)} / network ${formatDuration(progress.networkTransferMs)} / verify ${formatDuration(progress.verificationMs)} / FlatSQL ${formatDuration(progress.flatSqlMaterializationMs)}`;
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
    const key = schemaSyncPreferenceKey(schema.dataSourceId, schema.id, schema.datastoreKey);
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

  function formatBytesPerSecond(value: number): string {
    return `${formatBytes(value)}/s`;
  }

  function formatDuration(milliseconds: number): string {
    const value = Math.max(0, Math.floor(milliseconds));
    if (value < 1000) return `${value} ms`;
    const seconds = value / 1000;
    if (seconds < 60) return `${seconds.toFixed(seconds < 10 ? 1 : 0)} s`;
    const minutes = Math.floor(seconds / 60);
    const remainingSeconds = Math.round(seconds % 60);
    return `${minutes}m ${remainingSeconds}s`;
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

  function backendConfigForDataSource(source: DataSourceOption | null, datastoreKey: string | null = null): WorkerFlatSqlSyncBackendConfig | null {
    if (!source || source.kind !== 'configured' || !source.peerId || !source.syncAddrs?.length) return null;
    return {
      targetPeerId: source.peerId,
      candidateAddrs: source.syncAddrs,
      datastoreKey,
      providerId: configuredProviderIdFromSource(source),
      sourceName: configuredSourceNameFromSource(source),
      displayName: source.label,
      publicKey: source.publicKey,
      gatewayUrl: localGatewayUrl(),
      ipfsApiUrl: localKuboApiUrl(),
      artifactPeerAddrs: artifactPeerAddrsForDataSource(source),
      measuredWireSpeedBytesPerSecond: measuredWireSpeedBytesPerSecondForSource(source.id),
    };
  }

  function measuredWireSpeedBytesPerSecondForSource(dataSourceId: string): number {
    const local = typeof window !== 'undefined' ? window.localStorage : null;
    const env = import.meta.env as ImportMetaEnv & {
      readonly SDN_UI_WIRE_SPEED_BYTES_PER_SECOND?: string;
      readonly SDN_UI_WIRE_SPEED_BPS?: string;
    };
    const byteCandidates = [
      local?.getItem(`sdn-data-wire-speed-bytes-per-second:${dataSourceId}`),
      local?.getItem('sdn-data-wire-speed-bytes-per-second'),
      env.SDN_UI_WIRE_SPEED_BYTES_PER_SECOND,
    ];
    for (const candidate of byteCandidates) {
      const numeric = Number(candidate);
      if (Number.isFinite(numeric) && numeric > 0) return Math.floor(numeric);
    }
    const bitCandidates = [
      local?.getItem(`sdn-data-wire-speed-bps:${dataSourceId}`),
      local?.getItem('sdn-data-wire-speed-bps'),
      env.SDN_UI_WIRE_SPEED_BPS,
    ];
    for (const candidate of bitCandidates) {
      const numeric = Number(candidate);
      if (Number.isFinite(numeric) && numeric > 0) return Math.floor(numeric / 8);
    }
    return 0;
  }

  function localGatewayUrl(): string | null {
    const params = new URLSearchParams(window.location.search);
    const env = import.meta.env as ImportMetaEnv & { readonly SDN_UI_GATEWAY_URL?: string };
    return params.get('gateway') ?? env.SDN_UI_GATEWAY_URL ?? (backend?.mode === 'desktop-local' ? 'http://127.0.0.1:8081' : null);
  }

  function localKuboApiUrl(): string | null {
    const params = new URLSearchParams(window.location.search);
    const env = import.meta.env as ImportMetaEnv & { readonly SDN_UI_API_URL?: string };
    return params.get('api') ?? env.SDN_UI_API_URL ?? (backend?.mode === 'desktop-local' ? 'http://127.0.0.1:5001' : null);
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
      const providerId = configuredNodeProviderId(node);
      const sourceName = configuredNodeSourceName(node);
      const label = configuredNodeLabel(node, observedNames, peerId);
      const detail = [node.id, configuredNodeHostName(node)].filter(Boolean).join(' / ');
      const artifactPeerAddrs = configuredNodeArtifactPeerAddrs(node);
      options.push({
        id: `configured:${node.id}`,
        label,
        detail,
        peerId,
        publicKey,
        providerId,
        sourceName,
        kind: 'configured',
        syncAddrs,
        artifactPeerAddrs,
        searchText: [label, detail, publicKey, peerId, providerId, sourceName, node.trustLevel, node.trust_level, syncAddrs.join(' '), artifactPeerAddrs.join(' ')].filter(Boolean).join(' ').toLowerCase(),
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

  function configuredNodeProviderId(node: ConfiguredSdnNode): string | null {
    return readRecordString(node.metadata ?? {}, 'provider_id', 'providerId');
  }

  function configuredNodeSourceName(node: ConfiguredSdnNode): string | null {
    return readRecordString(node.metadata ?? {}, 'source_name', 'sourceName');
  }

  function configuredNodeArtifactPeerAddrs(node: ConfiguredSdnNode): string[] {
    const metadata = node.metadata ?? {};
    return normalizeIpfsArtifactPeerAddrs(
      metadata.ipfs_artifact_addrs
        ?? metadata.ipfsArtifactAddrs
        ?? metadata.artifact_addrs
        ?? metadata.artifactAddrs,
    );
  }

  function artifactPeerAddrsForDataSource(source: DataSourceOption): string[] {
    const discoveredPeerAddrs = dataSourceOptions
      .filter((option) => option.kind === 'configured')
      .flatMap((option) => option.artifactPeerAddrs ?? []);
    return prioritizeIpfsArtifactPeerAddrs(source.artifactPeerAddrs ?? [], [
      ...discoveredPeerAddrs,
      ...artifactPeerAddrsForObservedPeers(peers),
      ...artifactPeerAddrsForTrustedPeers(trustedPeers),
    ]);
  }

  function artifactPeerAddrsForObservedPeers(observedPeers: ObservedSdnPeer[]): string[] {
    return observedPeers.flatMap((peer) => peer.artifactPeerAddrs ?? []);
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
    if (source.providerId) return source.providerId;
    const id = source.id.startsWith('configured:') ? source.id.slice('configured:'.length) : source.id;
    return id || source.peerId;
  }

  function configuredSourceNameFromSource(source: DataSourceOption): string | null {
    return source.sourceName ?? null;
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
        <div class="sdn-breadcrumb">{selectedDataSectionMeta.breadcrumb}</div>
        <div class="sdn-data-subnav-actions" role="group" aria-label="Data sections">
          {#each DATA_SECTIONS as section}
            <button
              class="sdn-button sdn-button-muted sdn-button-compact"
              class:active={selectedDataSection === section.id}
              type="button"
              on:click={() => setDataSection(section.id)}
            >
              {section.label}
            </button>
          {/each}
        </div>
      </nav>

      {#if workbenchLoading}
        <p class="sdn-loading-inline" role="status">Loading</p>
      {/if}

      {#if selectedDataSection === 'storage'}
        <section class="sdn-storage-state" aria-label="Local storage state">
          <div class="sdn-dataset-summary" aria-label="Dataset summary">
            <div class="sdn-dataset-metric" aria-label={`Remote rows ${formatNumber(activeStorageRows.reduce((total, row) => total + row.remoteRows, 0))}`}>
              <span>Remote rows</span>
              <strong>{formatNumber(activeStorageRows.reduce((total, row) => total + row.remoteRows, 0))}</strong>
            </div>
            <div class="sdn-dataset-metric" aria-label={`Local rows ${formatNumber(activeStorageRows.reduce((total, row) => total + row.localRows, 0))}`}>
              <span>Local rows</span>
              <strong>{formatNumber(activeStorageRows.reduce((total, row) => total + row.localRows, 0))}</strong>
            </div>
            <div class="sdn-dataset-metric" aria-label={`Cached ${formatBytes(activeStorageRows.reduce((total, row) => total + row.cachedBytes, 0))}`}>
              <span>Cached</span>
              <strong>{formatBytes(activeStorageRows.reduce((total, row) => total + row.cachedBytes, 0))}</strong>
            </div>
            <div class="sdn-dataset-metric" aria-label={`Pinned rows ${formatNumber(activeStorageRows.reduce((total, row) => total + row.progress.pinnedRows, 0))}`}>
              <span>Pinned rows</span>
              <strong>{formatNumber(activeStorageRows.reduce((total, row) => total + row.progress.pinnedRows, 0))}</strong>
            </div>
            <div class="sdn-dataset-metric" aria-label={`Scan ${scanHashLabel}`}>
              <span>Scan</span>
              <strong>{scanHashLabel}</strong>
            </div>
          </div>

          <div class="sdn-storage-grid">
            {#each activeStorageRows as schema}
              <article class="sdn-storage-row" class:active={isSchemaRowSelected(schema)}>
                <div>
                  <strong>{schema.id}</strong>
                  <span>{schema.providerName}</span>
                  <span>{formatNumber(schema.localRows)} local / {formatNumber(schema.remoteRows)} remote</span>
                  <span>{formatNumber(schema.progress.pinnedRows)} pinned</span>
                </div>
                <div>
                  <strong>{formatBytes(schema.cachedBytes)}</strong>
                  <span>{syncProgressLabel(schema)}</span>
                  <span>{syncDownloadSpeedLabel(schema)}</span>
                  <span>{syncTimingLabel(schema)}</span>
                </div>
                <div>
                  <strong>{syncStatusLabel(schema)}</strong>
                  <span>Next sync attempt: {nextSyncAttemptLabel(schema)}</span>
                  <span>{schema.progress.lastSyncedAt ? formatDateTime(schema.progress.lastSyncedAt) : 'Never synced'}</span>
                  {#if schema.progress.error}
                    <span class="sdn-sync-error" title={schema.progress.error}>{shorten(schema.progress.error, 120)}</span>
                  {/if}
                </div>
                <button class="sdn-button sdn-button-muted sdn-button-compact" type="button" aria-label={`${schema.id} retry sync`} on:click={() => retrySubscriptionSync(schema)} disabled={activeSyncKeys.has(schemaSyncPreferenceKey(schema.dataSourceId, schema.id, schema.datastoreKey))}>Retry</button>
              </article>
            {:else}
              <p class="sdn-empty-inline">No actively synced data feeds. Add a feed from Peers to start local sync.</p>
            {/each}
          </div>
        </section>
      {/if}

      {#if selectedDataSection === 'subscriptions'}
        <section class="sdn-storage-state" aria-label="Sync settings">
          <div class="sdn-storage-grid">
            {#each schemaSyncRows as schema}
              <article class="sdn-storage-row sdn-subscription-row" class:active={isSchemaRowSelected(schema)}>
                <div>
                  <strong>{schema.id}</strong>
                  <span>{schema.providerName}</span>
                  <span>{formatNumber(schema.localRows)} local / {formatNumber(schema.remoteRows)} remote</span>
                </div>
                <div>
                  <strong>{syncStatusLabel(schema)}</strong>
                  <span>{syncProgressLabel(schema)}</span>
                  <span>{syncDownloadSpeedLabel(schema)}</span>
                  <span>{syncTimingLabel(schema)}</span>
                  <span>Next sync attempt: {nextSyncAttemptLabel(schema)}</span>
                  {#if schema.progress.error}
                    <span class="sdn-sync-error" title={schema.progress.error}>{shorten(schema.progress.error, 120)}</span>
                  {/if}
                </div>
                <label>
                  <span>Storage cap</span>
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
                </label>
                <label>
                  <span>Sync profile</span>
                  <select
                    class="sdn-input sdn-select"
                    aria-label={`${schema.id} sync profile`}
                    value={schema.queryProfile}
                    on:change={(event) => handleSubscriptionQueryProfileChange(schema, event)}
                  >
                    {#each DATA_QUERY_PROFILES as profile}
                      <option value={profile.id}>{profile.label}</option>
                    {/each}
                  </select>
                </label>
                <label>
                  <span>Sync filter</span>
                  <input
                    class="sdn-input sdn-sync-filter"
                    aria-label={`${schema.id} sync filter`}
                    value={schema.syncFilter}
                    placeholder="Sync filter"
                    on:input={(event) => handleSubscriptionFilterInput(schema, event)}
                  />
                </label>
                <div class="sdn-subscription-actions">
                  <button class="sdn-button sdn-button-muted sdn-button-compact" type="button" aria-label={`${schema.id} retry sync`} on:click={() => retrySubscriptionSync(schema)} disabled={activeSyncKeys.has(schemaSyncPreferenceKey(schema.dataSourceId, schema.id, schema.datastoreKey))}>Retry</button>
                  {#if schema.preference.mode === 'sync'}
                    <button class="sdn-button sdn-button-muted sdn-button-compact" type="button" on:click={() => pauseSubscriptionSync(schema)}>Pause</button>
                  {:else}
                    <button class="sdn-button sdn-button-compact" type="button" on:click={() => resumeSubscriptionSync(schema)}>Resume</button>
                  {/if}
                  <button class="sdn-button sdn-button-muted sdn-button-compact" type="button" on:click={() => void verifyPinnedArtifacts(schema)} disabled={pinVerifyRunning}>Verify pins</button>
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
                </div>
              </article>
            {:else}
              <p class="sdn-empty-inline">No subscribed data feeds.</p>
            {/each}
          </div>
          {#if resetStatus}
            <p class="sdn-empty-inline" role="status">{resetStatus}</p>
          {/if}
          {#if pinVerifyStatus}
            <p class="sdn-empty-inline" role="status">{pinVerifyStatus}</p>
          {/if}
        </section>
      {/if}

      {#if selectedDataSection === 'explorer'}
        <section class="sdn-explorer-panel" aria-label="Local SQL">
          <div class="sdn-workbench-controls">
            <label>
              <span>Source</span>
              <select class="sdn-input sdn-select" bind:value={selectedExplorerSourceKey} on:change={handleExplorerSourceChange}>
                {#each subscribedSourceOptions as source}
                  <option value={source.id}>{source.label}</option>
                {/each}
              </select>
            </label>

            <label>
              <span>Table</span>
              <select class="sdn-input sdn-select" bind:value={selectedStandardId} on:change={handleExplorerStandardChange}>
                {#each subscribedStandardOptions as standard}
                  <option value={standard.id}>{standard.id} ({formatNumber(standard.remoteRows)})</option>
                {/each}
              </select>
            </label>

            {#if selectedStandardId === 'OMM'}
              <label>
                <span>Profile</span>
                <select class="sdn-input sdn-select" bind:value={epochProfile}>
                  {#each EPOCH_SQL_PROFILES as profile}
                    <option value={profile.id}>{profile.label}</option>
                  {/each}
                </select>
              </label>

              {#if epochProfile === 'epoch.day'}
                <label>
                  <span>Day</span>
                  <input class="sdn-input" type="date" bind:value={epochDay} />
                </label>
              {:else if epochProfile === 'epoch.window' || epochProfile === 'epoch.coverage'}
                <label>
                  <span>From</span>
                  <input class="sdn-input" type="datetime-local" bind:value={epochFrom} />
                </label>
                <label>
                  <span>To</span>
                  <input class="sdn-input" type="datetime-local" bind:value={epochTo} />
                </label>
              {:else}
                <label>
                  <span>At</span>
                  <input class="sdn-input" type="datetime-local" bind:value={epochAt} />
                </label>
                <label>
                  <span>Max delta</span>
                  <input class="sdn-input" type="number" min="0" step="60" bind:value={epochMaxDeltaSeconds} />
                </label>
              {/if}

              {#if epochProfile !== 'epoch.coverage'}
                <label>
                  <span>Entity</span>
                  <input class="sdn-input" inputmode="numeric" bind:value={epochEntityId} placeholder="NORAD catalog ID" />
                </label>
              {/if}

              <button class="sdn-button sdn-button-muted" type="button" on:click={() => void applyEpochProfileQuery()} disabled={sqlRunning}>Apply</button>
            {/if}

            <label class="sdn-workbench-ask">
              <span>Ask</span>
              <textarea
                class="sdn-input sdn-ask-input"
                bind:value={llmAskText}
                rows="2"
                spellcheck="true"
                placeholder="find all OMMs for satellites that belong to former soviet block nations that have periods greater than 1 day"
              ></textarea>
            </label>

            <button
              class="sdn-button sdn-button-muted"
              type="button"
              on:click={() => void draftLocalLlmSql()}
              disabled={llmDraftRunning || localRowCount <= 0 || !llmAskText.trim()}
            >{llmDraftRunning ? 'Drafting' : 'Draft SQL'}</button>

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

          {#if epochQueryError}
            <p class="sdn-empty-inline" role="alert">{epochQueryError}</p>
          {/if}

          {#if llmDraftError}
            <p class="sdn-empty-inline" role="alert">{llmDraftError}</p>
          {:else if llmDraftRationale}
            <p class="sdn-empty-inline" role="status">{llmDraftRationale}</p>
          {/if}

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
            <div class="sdn-dataset-metric" aria-label={`Pinned rows ${formatNumber(pinnedRowCount)}`}>
              <span>Pinned rows</span>
              <strong>{formatNumber(pinnedRowCount)}</strong>
            </div>
            <div class="sdn-dataset-metric" aria-label={`Last sync ${lastSyncedLabel}`}>
              <span>Last sync</span>
              <strong>{lastSyncedLabel}</strong>
            </div>
            <div class="sdn-dataset-metric" aria-label={`Transport ${transportStateLabel}`}>
              <span>Transport</span>
              <strong>{transportStateLabel}</strong>
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
                <span>FILE_ID</span>
                <div>
                  <input class="sdn-input" bind:value={pnmFileIdQuery} />
                  <button class="sdn-button sdn-button-muted" type="button" on:click={() => void runPnmFileIdQuery()}>Find</button>
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
      {/if}
  </div>
</article>

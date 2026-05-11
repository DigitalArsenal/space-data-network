<script lang="ts">
  import { decodeEpmFlatBuffer } from '../../../src/ui/runtime/epm-flatbuffer';
  import {
    createLocalFlatSqlStore,
    type LocalFlatSqlQueryResult,
    type LocalFlatSqlStandardStats,
    type LocalFlatSqlStore,
  } from '../../../src/ui/runtime/local-flatsql';
  import { decodeOmmFlatBuffer } from '../../../src/ui/runtime/omm-flatbuffer';
  import type {
    DataScanResult,
    DataSummary,
    ObservedSdnPeer,
    RawDataRecord,
    SdnBackend,
  } from '../../../src/ui/runtime/sdn-backend';
  import { createRemoteSdnBackend } from '../../../src/ui/runtime/sdn-backend-remote';
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
    serverUrl?: string;
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

  interface SchemaSyncRow extends StandardOption {
    localRows: number;
    preference: SchemaSyncPreference;
  }

  export let backend: SdnBackend | null = null;
  export let peers: ObservedSdnPeer[] = [];
  export let route = '/data';

  const DEFAULT_STANDARD_ID = 'EPM';
  const LOCAL_DATA_SOURCE_ID = 'local';
  const SCHEMA_EXTENSION = 'fbs';
  const DEFAULT_PAGE_SIZE = 10;
  const DATA_SOURCE_PAGE_SIZE = 6;
  const PAGE_SIZE_OPTIONS = [10, 25, 50, 100];
  const SCHEMA_SYNC_STORAGE_KEY = 'sdn:data-schema-sync:v1';
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
  const STANDARD_FIELD_COLUMNS: Record<string, WorkbenchColumn[]> = {
    EPM: EPM_STANDARD_COLUMNS,
    OMM: OMM_STANDARD_COLUMNS,
  };

  let dataSummary: DataSummary | null = null;
  let selectedStandardId = DEFAULT_STANDARD_ID;
  let selectedDataSourceId = LOCAL_DATA_SOURCE_ID;
  let lastColumnStandardId = '';
  let columnMenuOpen = false;
  let visibleColumnKeys: string[] = [];
  let searchText = '';
  let dataSourceSearchText = '';
  let dataSourcePageIndex = 0;
  let pageSize = DEFAULT_PAGE_SIZE;
  let pageIndex = 0;
  let sortColumn: SortColumn = 'timestamp';
  let sortDirection: SortDirection = 'desc';
  let rawRecords: RawDataRecord[] = [];
  let dataScan: DataScanResult | null = null;
  let workbenchLoading = false;
  let lastBackend: SdnBackend | null = null;
  let configuredDataSources: ConfiguredSdnNode[] = [];
  let dataSourceBackends = new Map<string, SdnBackend>();
  let userSelectedDataSource = false;
  let userSelectedStandard = false;
  let inspectCid = '';
  let inspectGatewayUrl = '';
  let localFlatSqlStore: LocalFlatSqlStore | null = null;
  let localFlatSqlStoreKey = '';
  let localFlatSqlStats: LocalFlatSqlStandardStats[] = [];
  let sqlQueryText = defaultSqlQuery(DEFAULT_STANDARD_ID);
  let sqlResult: LocalFlatSqlQueryResult | null = null;
  let sqlError = '';
  let sqlRunning = false;
  let userEditedSql = false;
  let userEditedColumns = false;
  let schemaSyncPreferences: Record<string, SchemaSyncPreference> = loadSchemaSyncPreferences();

  $: dataSourceOptions = buildDataSourceOptions(backend, configuredDataSources, peers);
  $: filteredDataSourceOptions = filterDataSourceOptions(dataSourceOptions, dataSourceSearchText);
  $: dataSourceTotalPageCount = Math.max(1, Math.ceil(filteredDataSourceOptions.length / DATA_SOURCE_PAGE_SIZE));
  $: if (dataSourcePageIndex >= dataSourceTotalPageCount) dataSourcePageIndex = dataSourceTotalPageCount - 1;
  $: paginatedDataSourceOptions = filteredDataSourceOptions.slice(
    dataSourcePageIndex * DATA_SOURCE_PAGE_SIZE,
    (dataSourcePageIndex + 1) * DATA_SOURCE_PAGE_SIZE,
  );
  $: dataSourcePageLabel = `${dataSourcePageIndex + 1}/${dataSourceTotalPageCount}`;
  $: standardOptions = standardOptionsFromSummary(dataSummary);
  $: schemaSyncRows = buildSchemaSyncRows(standardOptions, selectedDataSourceId, localFlatSqlStats);
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
  $: localRowCount = selectedLocalFlatSqlStats?.recordCount ?? 0;
  $: cachedByteCount = selectedLocalFlatSqlStats?.cachedBytes ?? 0;
  $: scanHashLabel = dataScan?.scanHash ? shorten(dataScan.scanHash, 18) : 'none';
  $: sqlColumns = sqlResult?.columns ?? [];
  $: displaySqlColumns = visibleSqlColumns(sqlColumns, sqlRecords);
  $: sqlRecords = sqlResult?.records ?? [];

  $: if (backend && backend !== lastBackend) {
    lastBackend = backend;
    void initializeDataExplorer();
  }

  $: syncInspectRoute(route, backend);

  $: if (dataSourceOptions.length > 0 && !dataSourceOptions.some((source) => source.id === selectedDataSourceId)) {
    selectedDataSourceId = dataSourceOptions[0].id;
  }

  async function initializeDataExplorer(): Promise<void> {
    configuredDataSources = [];
    dataSourceBackends = new Map();
    selectedDataSourceId = LOCAL_DATA_SOURCE_ID;
    userSelectedDataSource = false;
    userSelectedStandard = false;
    await loadConfiguredDataSources();
    if (!userSelectedDataSource) {
      selectedDataSourceId = preferredDataSourceId(buildDataSourceOptions(backend, configuredDataSources, peers));
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
    const activeBackend = backendForSelectedDataSource();
    if (!activeBackend) {
      rawRecords = [];
      dataScan = null;
      return;
    }
    const nextPage = Math.max(0, targetPage);
    const query = {
      schema: schemaNameForStandardId(selectedStandardId),
      limit: normalizedPageSize(),
      offset: nextPage * normalizedPageSize(),
    };
    workbenchLoading = true;
    try {
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
      await ingestDownloadedRecords(rawRecords);
      if (rawRecords.length > 0 && (!userEditedSql || sqlResult === null)) {
        if (!userEditedSql) sqlQueryText = defaultSqlQuery(selectedStandardId);
        await runSqlQuery();
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
    dataSourceSearchText = '';
    dataSourcePageIndex = 0;
    pageIndex = 0;
    rawRecords = [];
    dataScan = null;
    resetLocalFlatSqlStore();
    resetSqlForSelectedStandard();
    columnMenuOpen = false;
    userSelectedDataSource = true;
    userSelectedStandard = false;
    userEditedColumns = false;
    void initializeWorkbench();
  }

  function handleDataSourceSearchInput(): void {
    dataSourcePageIndex = 0;
  }

  function goToPreviousPage(): void {
    if (canGoPrevious) void runWorkbenchQuery(pageIndex - 1);
  }

  function goToNextPage(): void {
    if (canGoNext) void runWorkbenchQuery(pageIndex + 1);
  }

  function goToPreviousDataSourcePage(): void {
    if (dataSourcePageIndex > 0) dataSourcePageIndex -= 1;
  }

  function goToNextDataSourcePage(): void {
    if (dataSourcePageIndex + 1 < dataSourceTotalPageCount) dataSourcePageIndex += 1;
  }

  function handleSqlInput(): void {
    userEditedSql = true;
  }

  function handleSchemaSyncModeChange(standardId: string, event: Event): void {
    const value = (event.currentTarget as HTMLSelectElement).value;
    updateSchemaSyncPreference(standardId, {
      mode: value === 'sync' ? 'sync' : 'preview',
    });
  }

  function handleSchemaStorageCapInput(standardId: string, event: Event): void {
    updateSchemaSyncPreference(standardId, {
      storageCap: normalizedStorageCap((event.currentTarget as HTMLInputElement).value),
    });
  }

  function handleSchemaStorageUnitChange(standardId: string, event: Event): void {
    const value = (event.currentTarget as HTMLSelectElement).value;
    updateSchemaSyncPreference(standardId, {
      storageUnit: isStorageUnit(value) ? value : DEFAULT_SCHEMA_SYNC_PREFERENCE.storageUnit,
    });
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
      sqlResult = store.query(query, selectedStandardId);
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
      refreshLocalFlatSqlStats();
    } catch (error) {
      sqlError = error instanceof Error ? error.message : 'FlatSQL ingest failed';
    }
  }

  async function ensureLocalFlatSqlStore(): Promise<LocalFlatSqlStore | null> {
    const nextKey = `sdn-data:${selectedDataSourceId}`;
    if (localFlatSqlStore && localFlatSqlStoreKey === nextKey) return localFlatSqlStore;
    resetLocalFlatSqlStore();
    try {
      localFlatSqlStore = await createLocalFlatSqlStore({
        schemas: LOCAL_FLATSQL_SCHEMAS,
        persistenceKey: nextKey,
      });
      localFlatSqlStoreKey = nextKey;
      refreshLocalFlatSqlStats();
      return localFlatSqlStore;
    } catch (error) {
      sqlError = error instanceof Error ? error.message : 'FlatSQL initialization failed';
      return null;
    }
  }

  function resetLocalFlatSqlStore(): void {
    localFlatSqlStore?.destroy();
    localFlatSqlStore = null;
    localFlatSqlStoreKey = '';
    localFlatSqlStats = [];
    sqlResult = null;
    sqlError = '';
  }

  function refreshLocalFlatSqlStats(): void {
    localFlatSqlStats = localFlatSqlStore?.getStats() ?? [];
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

  function buildSchemaSyncRows(
    options: StandardOption[],
    dataSourceId: string,
    stats: LocalFlatSqlStandardStats[],
  ): SchemaSyncRow[] {
    return options.map((option) => ({
      ...option,
      localRows: localRowsForStandard(stats, option.id),
      preference: schemaSyncPreferenceFor(dataSourceId, option.id),
    }));
  }

  function localRowsForStandard(stats: LocalFlatSqlStandardStats[], standardId: string): number {
    return stats.find((entry) => entry.standardId === standardId)?.recordCount ?? 0;
  }

  function schemaSyncPreferenceFor(dataSourceId: string, standardId: string): SchemaSyncPreference {
    return schemaSyncPreferences[schemaSyncPreferenceKey(dataSourceId, standardId)] ?? DEFAULT_SCHEMA_SYNC_PREFERENCE;
  }

  function updateSchemaSyncPreference(standardId: string, patch: Partial<SchemaSyncPreference>): void {
    const key = schemaSyncPreferenceKey(selectedDataSourceId, standardId);
    const current = schemaSyncPreferenceFor(selectedDataSourceId, standardId);
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

  function isStorageUnit(value: unknown): value is StorageUnit {
    return typeof value === 'string' && STORAGE_CAP_UNITS.includes(value as StorageUnit);
  }

  function workbenchColumnsForStandard(standardId: string, rows: WorkbenchRow[]): WorkbenchColumn[] {
    const standardColumns = STANDARD_FIELD_COLUMNS[standardId] ?? [];
    const knownKeys = new Set([...METADATA_COLUMNS, ...standardColumns].map((column) => column.key));
    const dynamicColumns: WorkbenchColumn[] = [];
    for (const row of rows) {
      for (const key of Object.keys(row.decoded)) {
        if (INTERNAL_COLUMN_KEYS.has(key)) continue;
        if (knownKeys.has(key)) continue;
        knownKeys.add(key);
        dynamicColumns.push({ key, label: labelFromFieldKey(key), source: 'standard' });
      }
    }
    if (standardColumns.length === 0) return [...METADATA_COLUMNS, ...dynamicColumns];
    return [...standardColumns, ...METADATA_COLUMNS, ...dynamicColumns];
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
    if (!source.serverUrl) return null;
    const existing = dataSourceBackends.get(source.id);
    if (existing) return existing;
    const remoteBackend = createRemoteSdnBackend({ serverUrl: source.serverUrl });
    dataSourceBackends.set(source.id, remoteBackend);
    return remoteBackend;
  }

  function currentDataSourceOption(): DataSourceOption | null {
    const options = buildDataSourceOptions(backend, configuredDataSources, peers);
    return options.find((source) => source.id === selectedDataSourceId) ?? options[0] ?? null;
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
      const serverUrl = configuredNodeServerUrl(node);
      if (!serverUrl) continue;
      const peerId = configuredNodePeerId(node);
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
        serverUrl,
        searchText: [label, detail, publicKey, peerId, node.trustLevel, node.trust_level, node.addrs.join(' ')].filter(Boolean).join(' ').toLowerCase(),
      });
    }

    return dedupeDataSourceOptions(options);
  }

  function preferredDataSourceId(options: DataSourceOption[]): string {
    return options.find((source) => source.searchText.includes('celestrak'))?.id
      ?? options.find((source) => source.kind === 'configured')?.id
      ?? LOCAL_DATA_SOURCE_ID;
  }

  function filterDataSourceOptions(options: DataSourceOption[], query: string): DataSourceOption[] {
    const normalized = query.trim().toLowerCase();
    if (!normalized) return options;
    return options.filter((source) => source.searchText.includes(normalized));
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

  function configuredNodeServerUrl(node: ConfiguredSdnNode): string | null {
    return readRecordString(node.metadata ?? {}, 'admin_proxy_path', 'adminProxyPath', 'server_url', 'serverUrl')
      ?? `/api/local/sdn-nodes/${encodeURIComponent(node.id)}`;
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

  <div class="sdn-data-explorer-layout">
    <aside class="sdn-source-browser" aria-label="Data sources">
      <label class="sdn-source-search">
        <span>Data source</span>
        <input
          class="sdn-input"
          type="search"
          bind:value={dataSourceSearchText}
          on:input={handleDataSourceSearchInput}
          placeholder="Search sources"
          autocomplete="off"
        />
      </label>

      <div class="sdn-source-table-wrap">
        <table class="sdn-table sdn-source-table" aria-label="Data sources">
          <tbody>
            {#each paginatedDataSourceOptions as source}
              <tr class:active={source.id === selectedDataSourceId}>
                <td>
                  <button
                    class="sdn-source-row-button"
                    type="button"
                    aria-pressed={source.id === selectedDataSourceId}
                    on:click={() => selectDataSource(source.id)}
                  >
                    <span>{source.label || source.peerId || source.id}</span>
                    <small>{source.publicKey || source.peerId || source.detail}</small>
                  </button>
                </td>
              </tr>
            {:else}
              <tr>
                <td>No matching sources.</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>

      <div class="sdn-pagination sdn-source-pagination">
        <button class="sdn-button sdn-button-muted" type="button" on:click={goToPreviousDataSourcePage} disabled={dataSourcePageIndex <= 0}>Previous</button>
        <span class="sdn-page-count">{dataSourcePageLabel}</span>
        <button class="sdn-button sdn-button-muted" type="button" on:click={goToNextDataSourcePage} disabled={dataSourcePageIndex + 1 >= dataSourceTotalPageCount}>Next</button>
      </div>
    </aside>

    <div class="sdn-workbench-main">
      <div class="sdn-workbench-controls">
        <label>
          <span>Table</span>
          <select class="sdn-input sdn-select" bind:value={selectedStandardId} on:change={handleTableChange}>
            {#each standardOptions as standard}
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

      <div class="sdn-schema-sync-wrap">
        <table class="sdn-table sdn-schema-sync-table" aria-label="Schema sync">
          <thead>
            <tr>
              <th>Schema</th>
              <th>Remote rows</th>
              <th>Local rows</th>
              <th>Sync</th>
              <th>Storage cap</th>
            </tr>
          </thead>
          <tbody>
            {#each schemaSyncRows as schema}
              <tr class:active={schema.id === selectedStandardId}>
                <td>{schema.id}</td>
                <td>{formatNumber(schema.remoteRows)}</td>
                <td>{formatNumber(schema.localRows)}</td>
                <td>
                  <select
                    class="sdn-input sdn-select sdn-schema-sync-mode"
                    aria-label={`${schema.id} sync`}
                    value={schema.preference.mode}
                    on:change={(event) => handleSchemaSyncModeChange(schema.id, event)}
                  >
                    <option value="preview">Preview only</option>
                    <option value="sync">Sync locally</option>
                  </select>
                </td>
                <td>
                  <div class="sdn-storage-cap-controls">
                    <input
                      class="sdn-input"
                      type="number"
                      min="0.1"
                      step="0.1"
                      aria-label={`${schema.id} storage cap`}
                      value={schema.preference.storageCap}
                      disabled={schema.preference.mode !== 'sync'}
                      on:input={(event) => handleSchemaStorageCapInput(schema.id, event)}
                    />
                    <select
                      class="sdn-input sdn-select"
                      aria-label={`${schema.id} storage unit`}
                      value={schema.preference.storageUnit}
                      disabled={schema.preference.mode !== 'sync'}
                      on:change={(event) => handleSchemaStorageUnitChange(schema.id, event)}
                    >
                      {#each STORAGE_CAP_UNITS as unit}
                        <option value={unit}>{unit}</option>
                      {/each}
                    </select>
                  </div>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>

      {#if workbenchLoading}
        <p class="sdn-loading-inline" role="status">Loading</p>
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
                <tr>
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

      {#if sqlError}
        <p class="sdn-empty-inline" role="alert">{sqlError}</p>
      {/if}
    </div>
  </div>
</article>

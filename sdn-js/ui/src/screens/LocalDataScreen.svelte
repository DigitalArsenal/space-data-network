<script lang="ts">
  import { decodeEpmFlatBuffer } from '../../../src/ui/runtime/epm-flatbuffer';
  import type {
    DataSummary,
    ObservedSdnPeer,
    RawDataRecord,
    SdnBackend,
  } from '../../../src/ui/runtime/sdn-backend';
  import { createRemoteSdnBackend } from '../../../src/ui/runtime/sdn-backend-remote';

  type SortColumn = string;
  type SortDirection = 'asc' | 'desc';
  type ColumnSource = 'metadata' | 'standard';

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
    kind: 'local' | 'configured';
    serverUrl?: string;
    searchText: string;
  }

  export let backend: SdnBackend | null = null;
  export let peers: ObservedSdnPeer[] = [];
  export let route = '/data';

  const DEFAULT_STANDARD_ID = 'EPM';
  const PREFERRED_REMOTE_STANDARD_ID = 'OMM';
  const LOCAL_DATA_SOURCE_ID = 'local';
  const SCHEMA_EXTENSION = 'fbs';
  const PAGE_SIZE_OPTIONS = [10, 25, 50, 100];
  const METADATA_COLUMNS: WorkbenchColumn[] = [
    { key: 'schemaName', label: 'Message', source: 'metadata' },
    { key: 'cid', label: 'CID', source: 'metadata' },
    { key: 'peerId', label: 'Peer', source: 'metadata' },
    { key: 'providerId', label: 'Producer', source: 'metadata' },
    { key: 'sourceName', label: 'Source', source: 'metadata' },
    { key: 'batchId', label: 'Batch', source: 'metadata' },
    { key: 'timestamp', label: 'Timestamp', source: 'metadata' },
    { key: 'sizeBytes', label: 'Bytes', source: 'metadata' },
  ];
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
  const STANDARD_FIELD_COLUMNS: Record<string, WorkbenchColumn[]> = {
    EPM: EPM_STANDARD_COLUMNS,
  };

  let dataSummary: DataSummary | null = null;
  let selectedStandardId = DEFAULT_STANDARD_ID;
  let selectedDataSourceId = LOCAL_DATA_SOURCE_ID;
  let lastColumnStandardId = '';
  let columnMenuOpen = false;
  let visibleColumnKeys: string[] = [];
  let searchText = '';
  let dataSourceSearchText = '';
  let pageSize = 25;
  let pageIndex = 0;
  let sortColumn: SortColumn = 'timestamp';
  let sortDirection: SortDirection = 'desc';
  let rawRecords: RawDataRecord[] = [];
  let lastBackend: SdnBackend | null = null;
  let configuredDataSources: ConfiguredSdnNode[] = [];
  let dataSourceBackends = new Map<string, SdnBackend>();
  let userSelectedDataSource = false;
  let userSelectedStandard = false;
  let inspectCid = '';
  let inspectGatewayUrl = '';

  $: dataSourceOptions = buildDataSourceOptions(backend, configuredDataSources, peers);
  $: filteredDataSourceOptions = filterDataSourceOptions(dataSourceOptions, dataSourceSearchText);
  $: standardOptions = standardIdsFromSummary(dataSummary);
  $: decodedRows = rawRecords.map(decodeWorkbenchRecord);
  $: allColumns = workbenchColumnsForStandard(selectedStandardId, decodedRows);
  $: syncVisibleColumnKeys(allColumns);
  $: visibleColumns = allColumns.filter((column) => visibleColumnKeys.includes(column.key));
  $: filteredRows = filterRows(decodedRows, searchText);
  $: visibleRows = sortRows(filteredRows, sortColumn, sortDirection);
  $: estimatedTotalRows = totalRowsForStandardId(dataSummary, selectedStandardId);
  $: totalPageCount = estimatedTotalRows === null ? Math.max(1, pageIndex + (canGoNext ? 2 : 1)) : Math.max(1, Math.ceil(estimatedTotalRows / normalizedPageSize()));
  $: canGoPrevious = pageIndex > 0;
  $: canGoNext = rawRecords.length >= pageSize && (estimatedTotalRows === null || ((pageIndex + 1) * pageSize) < estimatedTotalRows);
  $: pageLabel = `${pageIndex + 1}/${totalPageCount}`;

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
      return;
    }
    try {
      const result = await activeBackend.getDataSummary();
      dataSummary = result.data;
      const nextStandardOptions = standardIdsFromSummary(result.data);
      if (!userSelectedStandard || !nextStandardOptions.includes(selectedStandardId)) {
        selectedStandardId = preferredStandardIdFromSummary(result.data);
      }
    } catch {
      dataSummary = null;
    }
  }

  async function runWorkbenchQuery(targetPage = pageIndex): Promise<void> {
    const activeBackend = backendForSelectedDataSource();
    if (!activeBackend) {
      rawRecords = [];
      return;
    }
    const nextPage = Math.max(0, targetPage);
    try {
      const result = await activeBackend.queryRawData({
        schema: schemaNameForStandardId(selectedStandardId),
        limit: normalizedPageSize(),
        offset: nextPage * normalizedPageSize(),
      });
      rawRecords = result.data ?? [];
      pageIndex = nextPage;
    } catch {
      rawRecords = [];
    }
  }

  function handleTableChange(): void {
    userSelectedStandard = true;
    columnMenuOpen = false;
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
    pageIndex = 0;
    rawRecords = [];
    columnMenuOpen = false;
    userSelectedDataSource = true;
    userSelectedStandard = false;
    void initializeWorkbench();
  }

  function goToPreviousPage(): void {
    if (canGoPrevious) void runWorkbenchQuery(pageIndex - 1);
  }

  function goToNextPage(): void {
    if (canGoNext) void runWorkbenchQuery(pageIndex + 1);
  }

  function setSort(column: SortColumn): void {
    if (sortColumn === column) {
      sortDirection = sortDirection === 'asc' ? 'desc' : 'asc';
      return;
    }
    sortColumn = column;
    sortDirection = column === 'sizeBytes' || column === 'timestamp' ? 'desc' : 'asc';
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
      visibleColumnKeys = visibleColumnKeys.filter((candidate) => candidate !== key);
      return;
    }
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
    if (column === 'sizeBytes') return (left.record.sizeBytes ?? 0) - (right.record.sizeBytes ?? 0);
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
    if (column === 'sizeBytes') return String(row.record.sizeBytes ?? 0);
    if (column in row.record) return String(row.record[column as keyof RawDataRecord] ?? '');
    return stringifyCellValue(row.decoded[column]);
  }

  function displayCellValue(row: WorkbenchRow, column: WorkbenchColumn): string {
    if (column.key === 'sizeBytes') return formatBytes(row.record.sizeBytes);
    return shorten(tableValue(row, column.key), column.source === 'standard' ? 40 : 34);
  }

  function fullCellValue(row: WorkbenchRow, column: WorkbenchColumn): string {
    if (column.key === 'sizeBytes') return formatBytes(row.record.sizeBytes);
    return tableValue(row, column.key);
  }

  function workbenchColumnsForStandard(standardId: string, rows: WorkbenchRow[]): WorkbenchColumn[] {
    const standardColumns = STANDARD_FIELD_COLUMNS[standardId] ?? [];
    const knownKeys = new Set([...METADATA_COLUMNS, ...standardColumns].map((column) => column.key));
    const dynamicColumns: WorkbenchColumn[] = [];
    for (const row of rows) {
      for (const key of Object.keys(row.decoded)) {
        if (knownKeys.has(key)) continue;
        knownKeys.add(key);
        dynamicColumns.push({ key, label: labelFromFieldKey(key), source: 'standard' });
      }
    }
    return [...METADATA_COLUMNS, ...standardColumns, ...dynamicColumns];
  }

  function syncVisibleColumnKeys(columns: WorkbenchColumn[]): void {
    const columnKeys = columns.map((column) => column.key);
    const validKeys = new Set(columnKeys);
    if (selectedStandardId !== lastColumnStandardId) {
      visibleColumnKeys = columnKeys;
      lastColumnStandardId = selectedStandardId;
      return;
    }
    if (visibleColumnKeys.length === 0 || visibleColumnKeys.some((key) => !validKeys.has(key))) {
      const nextKeys = visibleColumnKeys.filter((key) => validKeys.has(key));
      visibleColumnKeys = nextKeys.length > 0 ? nextKeys : columnKeys;
    }
  }

  function decodeWorkbenchRecord(record: RawDataRecord): WorkbenchRow {
    if (standardIdFromSchema(record.schemaName) !== 'EPM') return { record, decoded: {} };
    try {
      return { record, decoded: decodeEpmFlatBuffer(base64ToBytes(record.dataBase64)) };
    } catch {
      return { record, decoded: {} };
    }
  }

  function base64ToBytes(value: string): Uint8Array {
    const decoded = atob(value);
    const bytes = new Uint8Array(decoded.length);
    for (let index = 0; index < decoded.length; index += 1) bytes[index] = decoded.charCodeAt(index);
    return bytes;
  }

  function stringifyCellValue(value: unknown): string {
    if (value == null) return '';
    if (Array.isArray(value)) return value.map((entry) => stringifyCellValue(entry)).join(', ');
    if (typeof value === 'object') return JSON.stringify(value);
    return String(value);
  }

  function labelFromFieldKey(key: string): string {
    return key.split('_').filter(Boolean).map((part) => `${part.slice(0, 1).toUpperCase()}${part.slice(1)}`).join(' ');
  }

  function standardIdFromSchema(schemaName: string | null | undefined): string {
    const id = String(schemaName || '').split('.')[0]?.trim().toUpperCase();
    return id || DEFAULT_STANDARD_ID;
  }

  function standardIdsFromSummary(summary: DataSummary | null): string[] {
    const ids = new Set<string>();
    for (const schema of summary?.schemas ?? []) ids.add(standardIdFromSchema(schema.schemaName));
    for (const source of summary?.sources ?? []) ids.add(standardIdFromSchema(source.schemaName));
    if (ids.size === 0) ids.add(DEFAULT_STANDARD_ID);
    return Array.from(ids).sort();
  }

  function preferredStandardIdFromSummary(summary: DataSummary | null): string {
    const ids = standardIdsFromSummary(summary);
    if (ids.includes(PREFERRED_REMOTE_STANDARD_ID)) return PREFERRED_REMOTE_STANDARD_ID;
    if (ids.includes(DEFAULT_STANDARD_ID)) return DEFAULT_STANDARD_ID;
    return ids[0] ?? DEFAULT_STANDARD_ID;
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

  function normalizedPageSize(): number {
    const value = Number(pageSize) || 25;
    return Math.max(1, Math.min(100, value));
  }

  function formatBytes(value: number | null | undefined): string {
    if (value == null) return 'pending';
    if (value > 1_000_000_000) return `${(value / 1_000_000_000).toFixed(1)} GB`;
    if (value > 1_000_000) return `${(value / 1_000_000).toFixed(1)} MB`;
    if (value > 1_000) return `${(value / 1_000).toFixed(1)} KB`;
    return `${value} B`;
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
      kind: 'local',
      searchText: 'local desktop local-node',
    }];
    const observedNames = new Map(observedPeers.map((peer) => [peer.id, peer.name]));

    for (const node of configuredNodes) {
      const serverUrl = configuredNodeServerUrl(node);
      if (!serverUrl) continue;
      const label = configuredNodeLabel(node, observedNames);
      const detail = [node.id, configuredNodeHostName(node)].filter(Boolean).join(' / ');
      options.push({
        id: `configured:${node.id}`,
        label,
        detail,
        kind: 'configured',
        serverUrl,
        searchText: [label, detail, node.trustLevel, node.trust_level, node.addrs.join(' ')].filter(Boolean).join(' ').toLowerCase(),
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

  function configuredNodeLabel(node: ConfiguredSdnNode, observedNames: Map<string, string>): string {
    return observedNames.get(node.id) ?? node.name ?? node.id;
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
        <input class="sdn-input" type="search" bind:value={dataSourceSearchText} placeholder="Search sources" autocomplete="off" />
      </label>

      <div class="sdn-source-list" role="listbox" aria-label="Data source">
        {#each filteredDataSourceOptions as source}
          <button
            class="sdn-source-option"
            class:active={source.id === selectedDataSourceId}
            type="button"
            role="option"
            aria-selected={source.id === selectedDataSourceId}
            on:click={() => selectDataSource(source.id)}
          >
            <span>{source.label}</span>
            <small>{source.detail}</small>
          </button>
        {:else}
          <p class="sdn-empty-inline">No matching sources.</p>
        {/each}
      </div>
    </aside>

    <div class="sdn-workbench-main">
      <div class="sdn-workbench-controls">
        <label>
          <span>Table</span>
          <select class="sdn-input sdn-select" bind:value={selectedStandardId} on:change={handleTableChange}>
            {#each standardOptions as standardId}
              <option value={standardId}>{standardId}</option>
            {/each}
          </select>
        </label>

        <label class="sdn-workbench-search">
          <span>Search</span>
          <input class="sdn-input" type="search" bind:value={searchText} placeholder="plain text" autocomplete="off" />
        </label>

        <label>
          <span>Page size</span>
          <select class="sdn-input sdn-select" bind:value={pageSize} on:change={handlePageSizeChange}>
            {#each PAGE_SIZE_OPTIONS as option}
              <option value={option}>{option}</option>
            {/each}
          </select>
        </label>

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
      </div>

      <div class="sdn-table-wrap sdn-workbench-table-wrap">
        <table class="sdn-table sdn-workbench-table">
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
        </table>
      </div>

      <div class="sdn-pagination">
        <button class="sdn-button sdn-button-muted" type="button" on:click={goToPreviousPage} disabled={!canGoPrevious}>Previous</button>
        <span class="sdn-page-count">{pageLabel}</span>
        <button class="sdn-button sdn-button-muted" type="button" on:click={goToNextPage} disabled={!canGoNext}>Next</button>
      </div>
    </div>
  </div>
</article>

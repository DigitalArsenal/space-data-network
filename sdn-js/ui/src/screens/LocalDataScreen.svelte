<script lang="ts">
  import { decodeEpmFlatBuffer } from '../../../src/ui/runtime/epm-flatbuffer';
  import type {
    DataSummary,
    RawDataRecord,
    SdnBackend,
  } from '../../../src/ui/runtime/sdn-backend';

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

  export let backend: SdnBackend | null = null;

  const DEFAULT_STANDARD_ID = 'EPM';
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
  let dataSummaryState = 'pending';
  let queryState = 'pending';
  let selectedStandardId = DEFAULT_STANDARD_ID;
  let lastColumnStandardId = '';
  let columnMenuOpen = false;
  let visibleColumnKeys: string[] = [];
  let searchText = '';
  let pageSize = 25;
  let pageIndex = 0;
  let sortColumn: SortColumn = 'timestamp';
  let sortDirection: SortDirection = 'desc';
  let rawRecords: RawDataRecord[] = [];
  let lastBackend: SdnBackend | null = null;

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
    void initializeWorkbench();
  }

  async function initializeWorkbench(): Promise<void> {
    await loadDataSummary();
    await runWorkbenchQuery(0);
  }

  async function loadDataSummary(): Promise<void> {
    if (!backend) {
      dataSummary = null;
      dataSummaryState = 'backend unavailable';
      return;
    }
    dataSummaryState = 'loading';
    try {
      const result = await backend.getDataSummary();
      dataSummary = result.data;
      dataSummaryState = result.ok ? 'available' : result.capability.state;
      if (!standardIdsFromSummary(result.data).includes(selectedStandardId)) {
        selectedStandardId = standardIdsFromSummary(result.data)[0] ?? DEFAULT_STANDARD_ID;
      }
    } catch (error) {
      dataSummary = null;
      dataSummaryState = errorMessage(error);
    }
  }

  async function refreshWorkbench(): Promise<void> {
    await loadDataSummary();
    await runWorkbenchQuery(pageIndex);
  }

  async function runWorkbenchQuery(targetPage = pageIndex): Promise<void> {
    if (!backend) {
      rawRecords = [];
      queryState = 'backend unavailable';
      return;
    }
    const nextPage = Math.max(0, targetPage);
    queryState = 'loading';
    try {
      const result = await backend.queryRawData({
        schema: schemaNameForStandardId(selectedStandardId),
        limit: normalizedPageSize(),
        offset: nextPage * normalizedPageSize(),
      });
      rawRecords = result.data ?? [];
      pageIndex = nextPage;
      queryState = result.ok ? `${rawRecords.length} rows` : result.capability.state;
    } catch (error) {
      rawRecords = [];
      queryState = errorMessage(error);
    }
  }

  function handleTableChange(): void {
    columnMenuOpen = false;
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
    const ids = new Set<string>([DEFAULT_STANDARD_ID]);
    for (const schema of summary?.schemas ?? []) ids.add(standardIdFromSchema(schema.schemaName));
    for (const source of summary?.sources ?? []) ids.add(standardIdFromSchema(source.schemaName));
    return Array.from(ids).sort();
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

  function errorMessage(error: unknown): string {
    return error instanceof Error ? error.message : String(error);
  }
</script>

<article class="sdn-card sdn-glass sdn-workbench">
  <div class="sdn-card-head">
    <div>
      <h2>SQL Workbench</h2>
      <p>{queryState} / {dataSummaryState} / {estimatedTotalRows ?? dataSummary?.totalRecords ?? 'pending'} total</p>
    </div>
    <button class="sdn-button sdn-button-muted sdn-button-compact" type="button" on:click={refreshWorkbench} disabled={!backend}>Refresh</button>
  </div>

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

  <div class="sdn-workbench-meta">
    <span class="sdn-chip" data-state={backend ? 'online' : 'warning'}>{backend ? 'backend ready' : 'backend missing'}</span>
    <span class="sdn-chip" data-state="special">{selectedStandardId}</span>
    <span class="sdn-chip">{formatBytes(dataSummary?.totalBytes)}</span>
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
</article>

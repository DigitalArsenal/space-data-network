<script lang="ts">
  import { onMount } from 'svelte';
  import { decodeEpmFlatBuffer } from '../../../src/ui/runtime/epm-flatbuffer';
  import {
    createVCardQrPayload as createVCardQrPayloadLocal,
    epmJsonFromVCard as parseVCardEpmJson,
    identityPublicKeyValue,
  } from '../../../src/ui/runtime/identity-vcard';

  type HostedEpmKind = 'node-self' | 'hosted';
  type DirectoryKind = 'node' | 'person';
  type DirectorySortColumn = 'kind' | 'name' | 'peerId' | 'publicKey' | 'epm';
  type SortDirection = 'asc' | 'desc';

  interface BackendCapability {
    id: string;
    state: string;
    reason?: string;
  }

  interface BackendResult<T> {
    ok: boolean;
    capability: BackendCapability;
    data: T | null;
  }

  interface HostedEpmRecord {
    id: string;
    kind: HostedEpmKind;
    label: string;
    peerId: string;
    epmCid?: string;
    epmJson: Record<string, unknown>;
  }

  interface ConfiguredSdnNode {
    id: string;
    name: string;
    addrs: string[];
    trust_level?: string;
    trustLevel?: string;
    metadata?: Record<string, unknown>;
  }

  interface DirectoryBackend {
    searchDirectory(query: string): Promise<BackendResult<Array<Record<string, unknown>>>>;
    downloadHostedEpm(id: string, format: 'epm' | 'vcard'): Promise<BackendResult<{ url: string; filename: string }>>;
  }

  type IdentityRuntimeModule = {
    createVCardQrPayload: (input: Record<string, unknown> | HostedEpmRecord) => string;
  };

  type QrCodeModule = {
    toDataURL: (input: string, options?: Record<string, unknown>) => Promise<string>;
  };

  export let backend: DirectoryBackend | null = null;

  const DIRECTORY_PAGE_SIZE = 10;
  const SEARCH_DEBOUNCE_MS = 250;
  const identityRuntimeModules = import.meta.glob('../../../src/ui/runtime/identity.ts');

  let query = '';
  let searchState = '';
  let remoteResults: Array<Record<string, unknown>> = [];
  let configuredResults: Array<Record<string, unknown>> = [];
  let selectedRecord: Record<string, unknown> | null = null;
  let selectedQr = '';
  let selectedVCard = '';
  let sortColumn: DirectorySortColumn = 'name';
  let sortDirection: SortDirection = 'asc';
  let pageIndex = 0;
  let searchTimer: ReturnType<typeof setTimeout> | null = null;
  let searchSequence = 0;
  let identityRuntimePromise: Promise<IdentityRuntimeModule> | null = null;
  let qrCodeModulePromise: Promise<QrCodeModule> | null = null;

  $: normalizedQuery = query.trim().toLowerCase();
  $: visibleResults = normalizedQuery
    ? sortDirectoryRecords(
        dedupeDirectoryRecords([...configuredResults, ...remoteResults])
          .filter(recordHasDirectorySignal)
          .filter((record) => recordMatchesQuery(record, normalizedQuery)),
        sortColumn,
        sortDirection,
      )
    : [];
  $: totalPages = Math.max(1, Math.ceil(visibleResults.length / DIRECTORY_PAGE_SIZE));
  $: if (pageIndex > totalPages - 1) pageIndex = Math.max(0, totalPages - 1);
  $: paginatedResults = visibleResults.slice(pageIndex * DIRECTORY_PAGE_SIZE, (pageIndex + 1) * DIRECTORY_PAGE_SIZE);
  $: canGoPrevious = pageIndex > 0;
  $: canGoNext = pageIndex + 1 < totalPages;

  onMount(() => {
    void loadConfiguredDirectoryNodes();
    return () => {
      if (searchTimer) clearTimeout(searchTimer);
    };
  });

  function handleSearchInput(event: Event): void {
    setQuery((event.currentTarget as HTMLInputElement).value);
  }

  function setQuery(value: string): void {
    query = value;
    pageIndex = 0;
    searchState = '';
    selectedRecord = null;
    selectedQr = '';
    selectedVCard = '';
    scheduleDirectorySearch();
  }

  function scheduleDirectorySearch(): void {
    if (searchTimer) clearTimeout(searchTimer);
    const trimmed = query.trim();
    if (!trimmed) {
      remoteResults = [];
      return;
    }
    searchTimer = setTimeout(() => {
      void searchDirectory(trimmed);
    }, SEARCH_DEBOUNCE_MS);
  }

  async function loadConfiguredDirectoryNodes(): Promise<void> {
    if (typeof fetch !== 'function') {
      configuredResults = [];
      return;
    }
    try {
      const response = await fetch('/api/local/sdn-nodes', {
        headers: { accept: 'application/json' },
      });
      configuredResults = response.ok ? normalizeConfiguredDirectoryNodes(await response.json()) : [];
    } catch {
      configuredResults = [];
    }
  }

  async function searchDirectory(searchText: string): Promise<void> {
    if (!backend) return;
    const sequence = ++searchSequence;
    try {
      const result = await backend.searchDirectory(searchText);
      if (sequence !== searchSequence) return;
      remoteResults = result.ok ? normalizeDirectoryRecords(result.data ?? []) : [];
      searchState = result.ok ? '' : result.capability.reason ?? '';
    } catch (error) {
      if (sequence !== searchSequence) return;
      searchState = errorMessage(error);
      remoteResults = [];
    }
  }

  async function handleUploadSearch(event: Event, format: 'epm' | 'vcard'): Promise<void> {
    const input = event.currentTarget as HTMLInputElement;
    const file = input.files?.[0];
    input.value = '';
    if (!file) return;
    try {
      const bytes = new Uint8Array(await file.arrayBuffer());
      const text = new TextDecoder().decode(bytes).trim();
      const record = format === 'epm'
        ? recordFromUploadedEpm(bytes, text)
        : recordFromUploadedText(text);
      const nextQuery = uploadSearchQuery(record, text);
      if (!nextQuery) {
        searchState = 'Uploaded file did not include searchable public directory fields.';
        return;
      }
      setQuery(nextQuery);
    } catch (error) {
      searchState = errorMessage(error);
    }
  }

  async function showQr(record: Record<string, unknown>): Promise<void> {
    selectedRecord = record;
    selectedVCard = await createVCardQrPayloadFromRuntime(toHostedRecord(record));
    selectedQr = '';
    try {
      const qrCode = await loadQrCodeModule();
      selectedQr = await qrCode.toDataURL(selectedVCard, {
        color: { dark: '#f5f5f7', light: '#00000000' },
        errorCorrectionLevel: 'M',
        margin: 1,
        width: 220,
      });
      searchState = '';
    } catch (error) {
      searchState = `QR unavailable: ${errorMessage(error)}`;
    }
  }

  async function downloadHostedEpm(record: Record<string, unknown>, format: 'epm' | 'vcard'): Promise<void> {
    if (!backend) {
      searchState = 'Backend unavailable; public EPM download is disabled.';
      return;
    }
    const id = recordId(record);
    if (!id) return;
    try {
      const result = await backend.downloadHostedEpm(id, format);
      if (!result.ok || !result.data) {
        searchState = result.capability.reason ?? 'Public EPM download is unavailable for this directory record.';
        return;
      }
      triggerDownload(result.data.url, result.data.filename);
      searchState = '';
    } catch (error) {
      searchState = errorMessage(error);
    }
  }

  function setSort(column: DirectorySortColumn): void {
    if (sortColumn === column) {
      sortDirection = sortDirection === 'asc' ? 'desc' : 'asc';
      return;
    }
    sortColumn = column;
    sortDirection = 'asc';
  }

  function sortableDirectoryHeader(column: DirectorySortColumn, label: string): string {
    return `${label}${sortColumn === column ? (sortDirection === 'asc' ? ' ↑' : ' ↓') : ''}`;
  }

  function goToPreviousPage(): void {
    if (canGoPrevious) pageIndex -= 1;
  }

  function goToNextPage(): void {
    if (canGoNext) pageIndex += 1;
  }

  async function loadQrCodeModule(): Promise<QrCodeModule> {
    qrCodeModulePromise ??= importQrCodeModule();
    return qrCodeModulePromise;
  }

  async function importQrCodeModule(): Promise<QrCodeModule> {
    // @ts-expect-error qrcode does not ship TypeScript declarations in this package.
    const module = await import('qrcode');
    return (module.default ?? module) as QrCodeModule;
  }

  async function createVCardQrPayloadFromRuntime(record: HostedEpmRecord): Promise<string> {
    try {
      const runtime = await loadIdentityRuntime();
      return runtime.createVCardQrPayload(record);
    } catch {
      return createVCardQrPayloadLocal(record);
    }
  }

  async function loadIdentityRuntime(): Promise<IdentityRuntimeModule> {
    identityRuntimePromise ??= loadIdentityRuntimeModule();
    return identityRuntimePromise;
  }

  async function loadIdentityRuntimeModule(): Promise<IdentityRuntimeModule> {
    const load = identityRuntimeModules['../../../src/ui/runtime/identity.ts'];
    if (!load) {
      return { createVCardQrPayload: createVCardQrPayloadLocal };
    }
    const module = await load();
    const runtime = module as IdentityRuntimeModule;
    return {
      createVCardQrPayload: runtime.createVCardQrPayload ?? createVCardQrPayloadLocal,
    };
  }

  function normalizeConfiguredDirectoryNodes(payload: unknown): Array<Record<string, unknown>> {
    return normalizeConfiguredDataSources(payload).map((node) => {
      const peer = configuredNodePeerId(node);
      const publicKey = configuredNodePublicKey(node);
      const label = configuredNodeLabel(node, peer);
      return {
        id: `configured:${node.id}`,
        directoryKind: 'node',
        kind: 'node',
        name: label,
        displayName: label,
        peer_id: peer ?? node.id,
        public_key: publicKey ?? peer ?? node.id,
        addrs: node.addrs,
        source: 'configured',
        epm_json: {
          dn: label,
          peer_id: peer ?? node.id,
          public_key: publicKey ?? peer ?? node.id,
          multiformat_address: node.addrs,
        },
      };
    });
  }

  function normalizeDirectoryRecords(records: Array<Record<string, unknown>>): Array<Record<string, unknown>> {
    return records.map((record) => ({
      ...record,
      directoryKind: directoryKind(record),
    }));
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

  function configuredNodePeerId(node: ConfiguredSdnNode): string | null {
    return readRecordString(node.metadata ?? {}, 'peer_id', 'peerId')
      ?? node.addrs.map((addr) => addr.split('/p2p/')[1]).find((value): value is string => Boolean(value))
      ?? null;
  }

  function configuredNodePublicKey(node: ConfiguredSdnNode): string | null {
    return readRecordString(node.metadata ?? {}, 'public_key', 'publicKey', 'signing_public_key', 'signingPublicKey');
  }

  function configuredNodeLabel(node: ConfiguredSdnNode, peerId: string | null): string {
    if (node.name && node.name !== node.id && node.name !== peerId) return node.name;
    return node.name ?? peerId ?? node.id;
  }

  function recordsFromPayloadKey(payload: unknown, key: string): Array<Record<string, unknown>> {
    if (Array.isArray(payload)) return payload.filter(isRecord);
    if (!isRecord(payload)) return [];
    const records = payload[key];
    return Array.isArray(records) ? records.filter(isRecord) : [];
  }

  function readRecordString(record: Record<string, unknown>, ...keys: string[]): string | null {
    for (const key of keys) {
      const value = record[key];
      if (typeof value === 'string' && value.trim()) return value.trim();
    }
    return null;
  }

  function dedupeDirectoryRecords(records: Array<Record<string, unknown>>): Array<Record<string, unknown>> {
    const seen = new Set<string>();
    const next: Array<Record<string, unknown>> = [];
    for (const record of records) {
      const key = [
        directoryKind(record),
        recordId(record),
        peerId(record),
        publicKeyValue(normalizeEpmJson(record)),
        displayName(record),
      ].filter(Boolean).join(':');
      if (!key || seen.has(key)) continue;
      seen.add(key);
      next.push(record);
    }
    return next;
  }

  function sortDirectoryRecords(
    records: Array<Record<string, unknown>>,
    column: DirectorySortColumn,
    direction: SortDirection,
  ): Array<Record<string, unknown>> {
    return [...records].sort((left, right) => {
      const comparison = directorySortValue(left, column).localeCompare(directorySortValue(right, column), undefined, { numeric: true });
      return direction === 'asc' ? comparison : -comparison;
    });
  }

  function directorySortValue(record: Record<string, unknown>, column: DirectorySortColumn): string {
    if (column === 'kind') return directoryKind(record);
    if (column === 'peerId') return peerId(record);
    if (column === 'publicKey') return publicKeyValue(normalizeEpmJson(record)) ?? '';
    if (column === 'epm') return epmLabel(record);
    return displayName(record);
  }

  function recordMatchesQuery(record: Record<string, unknown>, normalizedSearch: string): boolean {
    return recordSearchText(record).includes(normalizedSearch);
  }

  function recordSearchText(record: Record<string, unknown>): string {
    const epm = normalizeEpmJson(record);
    return [
      directoryKind(record),
      displayName(record),
      peerId(record),
      epmLabel(record),
      publicKeyValue(epm),
      stringValue(record.id),
      stringValue(record.source),
      stringValue(record.email),
      stringValue(record.telephone),
      Array.isArray(record.addrs) ? record.addrs.join(' ') : '',
      JSON.stringify(record.metadata ?? {}),
      JSON.stringify(epm),
    ].filter(Boolean).join(' ').toLowerCase();
  }

  function recordHasDirectorySignal(record: Record<string, unknown>): boolean {
    const epm = normalizeEpmJson(record);
    return Boolean(displayName(record) || peerId(record) || epmLabel(record) || publicKeyValue(epm));
  }

  function directoryKind(record: Record<string, unknown>): DirectoryKind {
    const value = stringValue(record.directoryKind)
      ?? stringValue(record.directory_kind)
      ?? stringValue(record.kind)
      ?? stringValue(record.entity_type)
      ?? stringValue(normalizeEpmJson(record).directory_kind)
      ?? stringValue(normalizeEpmJson(record).entity_type)
      ?? '';
    return /person|user|hosted/i.test(value) ? 'person' : 'node';
  }

  function toHostedRecord(record: Record<string, unknown>): HostedEpmRecord {
    const epmJson = normalizeEpmJson(record);
    const kind = directoryKind(record) === 'node' ? 'node-self' : 'hosted';
    const id = recordId(record) || peerId(record) || 'directory-record';
    return {
      id,
      kind,
      label: displayName(record) || id,
      peerId: peerId(record),
      epmCid: epmLabel(record),
      epmJson,
    };
  }

  function publicKeyValue(epm: Record<string, unknown>): string | undefined {
    return identityPublicKeyValue(epm);
  }

  function normalizeEpmJson(record: Record<string, unknown>): Record<string, unknown> {
    const raw = record.epm_json ?? record.epmJson;
    if (typeof raw === 'string') {
      try {
        const parsed = JSON.parse(raw);
        if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) return parsed as Record<string, unknown>;
      } catch {
        return { ...record };
      }
    }
    if (raw && typeof raw === 'object' && !Array.isArray(raw)) return raw as Record<string, unknown>;
    return {
      directory_kind: record.directoryKind ?? record.directory_kind,
      entity_type: record.entity_type,
      dn: record.dn ?? record.name ?? record.displayName,
      legal_name: record.legal_name ?? record.legalName,
      email: record.email,
      telephone: record.telephone,
      peer_id: record.peer_id ?? record.peerId,
      epm_cid: record.epm_cid ?? record.epmCid,
      public_key: record.public_key ?? record.publicKey,
      signing_public_key: record.signing_public_key ?? record.signingPublicKey,
      encryption_public_key: record.encryption_public_key ?? record.encryptionPublicKey,
    };
  }

  function recordId(record: Record<string, unknown>): string {
    return stringValue(record.id)
      ?? stringValue(record.epm_id)
      ?? stringValue(record.epmId)
      ?? stringValue(record.peer_id)
      ?? stringValue(record.peerId)
      ?? stringValue(record.epm_cid)
      ?? stringValue(record.epmCid)
      ?? '';
  }

  function displayName(record: Record<string, unknown>): string {
    const epm = normalizeEpmJson(record);
    return stringValue(record.dn)
      ?? stringValue(record.displayName)
      ?? stringValue(record.legal_name)
      ?? stringValue(record.name)
      ?? stringValue(epm.dn)
      ?? stringValue(epm.DN)
      ?? stringValue(epm.displayName)
      ?? stringValue(epm.legal_name)
      ?? stringValue(epm.name)
      ?? '';
  }

  function peerId(record: Record<string, unknown>): string {
    const epm = normalizeEpmJson(record);
    return stringValue(record.peer_id)
      ?? stringValue(record.peerId)
      ?? stringValue(epm.peer_id)
      ?? stringValue(epm.peerId)
      ?? '';
  }

  function epmLabel(record: Record<string, unknown>): string {
    const epm = normalizeEpmJson(record);
    return stringValue(record.epm_cid)
      ?? stringValue(record.epmCid)
      ?? stringValue(epm.epm_cid)
      ?? stringValue(epm.epmCid)
      ?? '';
  }

  function recordFromUploadedEpm(bytes: Uint8Array, text: string): Record<string, unknown> {
    try {
      return decodeEpmFlatBuffer(bytes);
    } catch {
      return recordFromUploadedText(text);
    }
  }

  function recordFromUploadedText(text: string): Record<string, unknown> {
    const trimmed = text.trim();
    if (!trimmed) return {};
    if (/^BEGIN:VCARD/i.test(trimmed)) return parseVCardEpmJson(trimmed);
    try {
      const parsed = JSON.parse(trimmed);
      return isRecord(parsed) ? parsed : {};
    } catch {
      return {};
    }
  }

  function uploadSearchQuery(record: Record<string, unknown>, text: string): string {
    const epm = normalizeEpmJson(record);
    return [
      peerId(record),
      publicKeyValue(epm),
      epmLabel(record),
      displayName(record),
      stringValue(epm.email),
      text.match(/\b(?:xpub|[A-Fa-f0-9]{64,}|16Uiu2[1-9A-HJ-NP-Za-km-z]+)\b/)?.[0],
    ].filter(Boolean).join(' ').trim();
  }

  function triggerDownload(url: string, filename: string): void {
    const link = document.createElement('a');
    link.href = url;
    link.download = filename;
    link.rel = 'noreferrer';
    document.body.appendChild(link);
    link.click();
    link.remove();
  }

  function isRecord(value: unknown): value is Record<string, unknown> {
    return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
  }

  function stringValue(value: unknown): string | undefined {
    return typeof value === 'string' && value.trim() ? value.trim() : undefined;
  }

  function errorMessage(error: unknown): string {
    return error instanceof Error ? error.message : String(error);
  }
</script>

<article class="sdn-card sdn-glass">
  <div class="sdn-search-row sdn-directory-search-row">
    <input
      class="sdn-input"
      value={query}
      placeholder="Search public directory"
      aria-label="Search public directory"
      on:input={handleSearchInput}
    />
    <label class="sdn-file-button">
      EPM
      <input type="file" accept=".epm,application/octet-stream" on:change={(event) => void handleUploadSearch(event, 'epm')} />
    </label>
    <label class="sdn-file-button">
      vCard
      <input type="file" accept=".vcf,.vcard,text/vcard,text/x-vcard" on:change={(event) => void handleUploadSearch(event, 'vcard')} />
    </label>
  </div>
  {#if searchState}
    <p class="sdn-status-line">{searchState}</p>
  {/if}

  {#if paginatedResults.length > 0}
    <div class="sdn-table-wrap sdn-directory-table-wrap">
      <table class="sdn-table sdn-directory-table" aria-label="Directory results">
        <thead>
          <tr>
            <th><button type="button" on:click={() => setSort('kind')}>{sortableDirectoryHeader('kind', 'Type')}</button></th>
            <th><button type="button" on:click={() => setSort('name')}>{sortableDirectoryHeader('name', 'Name')}</button></th>
            <th><button type="button" on:click={() => setSort('peerId')}>{sortableDirectoryHeader('peerId', 'PeerID')}</button></th>
            <th><button type="button" on:click={() => setSort('publicKey')}>{sortableDirectoryHeader('publicKey', 'Public key')}</button></th>
            <th><button type="button" on:click={() => setSort('epm')}>{sortableDirectoryHeader('epm', 'EPM')}</button></th>
            <th>Actions</th>
          </tr>
        </thead>
        <tbody>
          {#each paginatedResults as record}
            <tr>
              <td>{directoryKind(record)}</td>
              <td>{displayName(record)}</td>
              <td><code>{peerId(record)}</code></td>
              <td><code>{publicKeyValue(normalizeEpmJson(record)) ?? ''}</code></td>
              <td>{epmLabel(record)}</td>
              <td>
                <div class="sdn-actions-nowrap">
                  <button class="sdn-button sdn-button-compact" type="button" on:click={() => downloadHostedEpm(record, 'epm')} disabled={!backend || !recordId(record)}>EPM</button>
                  <button class="sdn-button sdn-button-compact" type="button" on:click={() => downloadHostedEpm(record, 'vcard')} disabled={!backend || !recordId(record)}>vCard</button>
                  <button class="sdn-button sdn-button-compact" type="button" on:click={() => showQr(record)}>Show QR</button>
                </div>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>

    {#if totalPages > 1}
      <div class="sdn-pagination">
        <button class="sdn-button sdn-button-muted" type="button" on:click={goToPreviousPage} disabled={!canGoPrevious}>Previous</button>
        <span class="sdn-page-count">{pageIndex + 1} / {totalPages}</span>
        <button class="sdn-button sdn-button-muted" type="button" on:click={goToNextPage} disabled={!canGoNext}>Next</button>
      </div>
    {/if}
  {/if}

  {#if selectedRecord}
    <section class="sdn-directory-preview">
      <div>
        <h3>{displayName(selectedRecord)}</h3>
        <textarea class="sdn-input sdn-vcard-preview" readonly value={selectedVCard} aria-label="Directory public vCard payload"></textarea>
      </div>
      <div class="sdn-qr-frame">
        {#if selectedQr}
          <img src={selectedQr} alt="Directory public vCard QR code" />
        {/if}
      </div>
    </section>
  {/if}
</article>

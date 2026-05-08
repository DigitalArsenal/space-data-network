<script lang="ts">
  type HostedEpmKind = 'node-self' | 'hosted';

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

  interface DirectoryBackend {
    searchDirectory(query: string): Promise<BackendResult<Array<Record<string, unknown>>>>;
    downloadHostedEpm(id: string, format: 'epm' | 'vcard'): Promise<BackendResult<{ url: string; filename: string }>>;
  }

  type IdentityRuntimeModule = {
    createVCardQrPayload: (input: Record<string, unknown> | HostedEpmRecord) => string;
  };

  export let backend: DirectoryBackend | null = null;

  type QrCodeModule = {
    toDataURL: (input: string, options?: Record<string, unknown>) => Promise<string>;
  };

  const identityRuntimeModules = import.meta.glob('../../../src/ui/runtime/identity.ts');
  let query = '';
  let searchState = 'Search public directory records for nodes and people.';
  let results: Array<Record<string, unknown>> = [];
  let selectedRecord: Record<string, unknown> | null = null;
  let selectedQr = '';
  let selectedVCard = '';
  let identityRuntimePromise: Promise<IdentityRuntimeModule> | null = null;
  let qrCodeModulePromise: Promise<QrCodeModule> | null = null;

  $: nodeResults = results.filter((record) => record.directoryKind === 'node' || record.kind === 'node');
  $: personResults = results.filter((record) => record.directoryKind === 'person' || record.kind === 'user' || record.kind === 'person');

  async function searchDirectory(): Promise<void> {
    if (!backend) {
      searchState = 'Backend unavailable; directory search is disabled.';
      return;
    }
    const trimmed = query.trim();
    if (!trimmed) {
      results = [];
      searchState = 'Enter a node, person, peer ID, CID, or public key.';
      return;
    }

    searchState = 'Searching nodes and people...';
    selectedRecord = null;
    selectedQr = '';
    selectedVCard = '';
    try {
      const result = await backend.searchDirectory(trimmed);
      results = result.data ?? [];
      searchState = result.ok
        ? `Found ${results.length} public director${results.length === 1 ? 'y record' : 'y records'}.`
        : result.capability.reason ?? 'Directory search is unavailable.';
    } catch (error) {
      searchState = errorMessage(error);
      results = [];
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
      searchState = 'Public directory vCard QR ready.';
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
    if (!id) {
      searchState = 'Directory record has no EPM or peer identifier to download.';
      return;
    }
    searchState = `Preparing public ${format === 'vcard' ? 'vCard' : 'EPM'} download...`;
    try {
      const result = await backend.downloadHostedEpm(id, format);
      if (!result.ok || !result.data) {
        searchState = result.capability.reason ?? 'Public EPM download is unavailable for this directory record.';
        return;
      }
      triggerDownload(result.data.url, result.data.filename);
      searchState = `Public ${format === 'vcard' ? 'vCard' : 'EPM'} download started.`;
    } catch (error) {
      searchState = errorMessage(error);
    }
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
      const createVCardQrPayload = runtime.createVCardQrPayload;
      return createVCardQrPayload(record);
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

  function toHostedRecord(record: Record<string, unknown>): HostedEpmRecord {
    const epmJson = normalizeEpmJson(record);
    return normalizeHostedEpmRecord({
      id: recordId(record) || 'directory-record',
      kind: record.directoryKind === 'node' || record.kind === 'node' ? 'node-self' : 'hosted',
      epm_cid: stringValue(record.epm_cid) ?? stringValue(record.epmCid),
      epm_json: epmJson,
    });
  }

  function normalizeHostedEpmRecord(input: Record<string, unknown>): HostedEpmRecord {
    const epmJson = createPublicEpmExport(readRecord(input.epm_json ?? input.epmJson ?? input));
    const id = stringValue(input.id) ?? stringValue(input.epm_id) ?? stringValue(input.epmId) ?? stringValue(epmJson.peer_id) ?? 'directory-record';
    return {
      id,
      kind: input.kind === 'node-self' ? 'node-self' : 'hosted',
      label: stringValue(epmJson.dn) ?? stringValue(epmJson.DN) ?? stringValue(epmJson.displayName) ?? stringValue(epmJson.name) ?? id,
      peerId: stringValue(epmJson.peer_id) ?? stringValue(epmJson.peerId) ?? stringValue(epmJson.PeerID) ?? '',
      epmCid: stringValue(input.epm_cid) ?? stringValue(input.epmCid) ?? stringValue(epmJson.epm_cid) ?? stringValue(epmJson.epmCid),
      epmJson,
    };
  }

  function createVCardQrPayloadLocal(record: Record<string, unknown> | HostedEpmRecord): string {
    const normalized = isHostedEpmRecord(record) ? record : normalizeHostedEpmRecord(record);
    const epm = createPublicEpmExport(normalized.epmJson);
    const lines = ['BEGIN:VCARD', 'VERSION:3.0'];
    addVCardLine(lines, 'FN', stringValue(epm.dn) ?? stringValue(epm.DN) ?? normalized.label);
    addVCardLine(lines, 'X-SDN-DIRECTORY-KIND', normalized.kind === 'node-self' ? 'node' : 'user');
    addVCardLine(lines, 'X-SDN-PEER-ID', normalized.peerId);
    addVCardLine(lines, 'X-SDN-EPM-CID', normalized.epmCid ?? stringValue(epm.epm_cid) ?? stringValue(epm.epmCid));
    addVCardLine(lines, 'X-SDN-PUBLIC-KEY', stringValue(epm.public_key) ?? stringValue(epm.PUBLIC_KEY) ?? stringValue(epm.signing_pubkey_hex));
    lines.push('END:VCARD');
    return lines.join('\r\n');
  }

  function createPublicEpmExport(input: Record<string, unknown>): Record<string, unknown> {
    const out: Record<string, unknown> = {};
    for (const [key, value] of Object.entries(input)) {
      if (isSecretKey(key)) continue;
      if (Array.isArray(value)) {
        out[key] = value.map((entry) => isRecord(entry) ? createPublicEpmExport(entry) : entry);
      } else if (isRecord(value)) {
        out[key] = createPublicEpmExport(value);
      } else {
        out[key] = value;
      }
    }
    return out;
  }

  function addVCardLine(lines: string[], key: string, value: string | undefined): void {
    if (value?.trim()) lines.push(`${key}:${value.replace(/\r?\n/g, ' ')}`);
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
      dn: record.dn ?? record.name ?? record.displayName,
      legal_name: record.legal_name ?? record.legalName,
      peer_id: record.peer_id ?? record.peerId,
      epm_cid: record.epm_cid ?? record.epmCid,
      public_key: record.public_key ?? record.publicKey,
      bitcoin_address: record.bitcoin_address ?? record.bitcoinAddress,
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
    return stringValue(record.dn)
      ?? stringValue(record.displayName)
      ?? stringValue(record.legal_name)
      ?? stringValue(record.name)
      ?? recordId(record)
      ?? 'Directory record';
  }

  function peerId(record: Record<string, unknown>): string {
    return stringValue(record.peer_id) ?? stringValue(record.peerId) ?? 'pending';
  }

  function epmLabel(record: Record<string, unknown>): string {
    return stringValue(record.epm_cid) ?? stringValue(record.epmCid) ?? 'public EPM';
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

  function readRecord(value: unknown): Record<string, unknown> {
    if (typeof value === 'string') {
      try {
        const parsed = JSON.parse(value);
        return isRecord(parsed) ? parsed : {};
      } catch {
        return {};
      }
    }
    return isRecord(value) ? { ...value } : {};
  }

  function isHostedEpmRecord(value: Record<string, unknown> | HostedEpmRecord): value is HostedEpmRecord {
    return typeof value.id === 'string'
      && (value.kind === 'node-self' || value.kind === 'hosted')
      && typeof value.label === 'string'
      && typeof value.peerId === 'string'
      && isRecord(value.epmJson);
  }

  function isRecord(value: unknown): value is Record<string, unknown> {
    return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
  }

  function isSecretKey(key: string): boolean {
    return /private|secret|mnemonic|xpriv|core|seed/i.test(key);
  }

  function stringValue(value: unknown): string | undefined {
    return typeof value === 'string' && value.trim() ? value.trim() : undefined;
  }

  function errorMessage(error: unknown): string {
    return error instanceof Error ? error.message : String(error);
  }
</script>

<article class="sdn-card sdn-glass">
  <div class="sdn-card-head">
    <div>
      <h2>Directory Search</h2>
      <p>Search nodes and people by public EPM fields, peer ID, CID, or public key.</p>
    </div>
  </div>

  <form class="sdn-search-row" on:submit|preventDefault={searchDirectory}>
    <input class="sdn-input" bind:value={query} placeholder="Search public directory" aria-label="Search public directory" />
    <button class="sdn-button" type="submit" disabled={!backend}>Search</button>
  </form>
  <p class="sdn-status-line">{searchState}</p>

  <div class="sdn-directory-grid">
    <section>
      <h3>Nodes</h3>
      <div class="sdn-directory-list">
        {#each nodeResults as record}
          <div class="sdn-directory-result">
            <div>
              <strong>{displayName(record)}</strong>
              <span>{peerId(record)}</span>
              <small>{epmLabel(record)}</small>
            </div>
            <div class="sdn-actions-nowrap">
              <button class="sdn-button sdn-button-compact" type="button" on:click={() => downloadHostedEpm(record, 'epm')}>EPM</button>
              <button class="sdn-button sdn-button-compact" type="button" on:click={() => downloadHostedEpm(record, 'vcard')}>vCard</button>
              <button class="sdn-button sdn-button-compact" type="button" on:click={() => showQr(record)}>Show QR</button>
            </div>
          </div>
        {:else}
          <p>No node results.</p>
        {/each}
      </div>
    </section>

    <section>
      <h3>People</h3>
      <div class="sdn-directory-list">
        {#each personResults as record}
          <div class="sdn-directory-result">
            <div>
              <strong>{displayName(record)}</strong>
              <span>{peerId(record)}</span>
              <small>{epmLabel(record)}</small>
            </div>
            <div class="sdn-actions-nowrap">
              <button class="sdn-button sdn-button-compact" type="button" on:click={() => downloadHostedEpm(record, 'epm')}>EPM</button>
              <button class="sdn-button sdn-button-compact" type="button" on:click={() => downloadHostedEpm(record, 'vcard')}>vCard</button>
              <button class="sdn-button sdn-button-compact" type="button" on:click={() => showQr(record)}>Show QR</button>
            </div>
          </div>
        {:else}
          <p>No people results.</p>
        {/each}
      </div>
    </section>
  </div>

  {#if selectedRecord}
    <section class="sdn-directory-preview">
      <div>
        <h3>{displayName(selectedRecord)}</h3>
        <p>Public vCard QR contains public EPM fields and public keys only.</p>
        <textarea class="sdn-input sdn-vcard-preview" readonly value={selectedVCard} aria-label="Directory public vCard payload"></textarea>
      </div>
      <div class="sdn-qr-frame">
        {#if selectedQr}
          <img src={selectedQr} alt="Directory public vCard QR code" />
        {:else}
          <span>QR pending</span>
        {/if}
      </div>
    </section>
  {/if}
</article>

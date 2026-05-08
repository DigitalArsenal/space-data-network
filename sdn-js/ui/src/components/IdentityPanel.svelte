<script lang="ts">
  import { onDestroy } from 'svelte';

  type HostedEpmKind = 'node-self' | 'hosted';
  type CapabilityState = 'available' | 'degraded' | 'unavailable' | 'permission-required' | 'remote-only' | 'local-only';

  interface BackendCapability {
    id: string;
    state: CapabilityState;
    reason?: string;
  }

  interface BackendResult<T> {
    ok: boolean;
    capability: BackendCapability;
    data: T | null;
  }

  interface NodeSummary {
    displayName: string;
    peerId: string | null;
    agentVersion: string | null;
    online: boolean;
    runtime: string;
  }

  interface HostedEpmRecord {
    id: string;
    kind: HostedEpmKind;
    label: string;
    peerId: string;
    epmCid?: string;
    epmJson: Record<string, unknown>;
    source?: string;
    updatedAt?: number;
  }

  interface IdentityBackend {
    readonly mode: string;
    saveHostedEpm(record: HostedEpmRecord): Promise<BackendResult<HostedEpmRecord>>;
    importHostedEpm(input: { name: string; bytes?: Uint8Array; text?: string }): Promise<BackendResult<HostedEpmRecord>>;
    deleteHostedEpm(id: string): Promise<BackendResult<Record<string, unknown>>>;
    downloadHostedEpm(id: string, format: 'json' | 'epm' | 'vcard'): Promise<BackendResult<{ url: string; filename: string }>>;
    listWalletsAndEpms(): Promise<BackendResult<Array<Record<string, unknown>>>>;
    beginClaimEpm(): Promise<BackendResult<Record<string, unknown>>>;
    exportCore(): Promise<BackendResult<Record<string, unknown>>>;
    importCore(core: Record<string, unknown>): Promise<BackendResult<Record<string, unknown>>>;
  }

  interface MountedWalletUI {
    openLogin?: () => void | Promise<void>;
    openAccount?: () => void | Promise<void>;
    destroy?: () => void | Promise<void>;
  }

  export let backend: IdentityBackend | null = null;
  export let summary: NodeSummary | null = null;
  export let profile: Record<string, unknown> | null = null;
  export let capabilities: BackendCapability[] = [];
  export let hostedEpms: HostedEpmRecord[] = [];
  export let onReload: () => void | Promise<void> = () => {};

  type IdentityRuntimeModule = {
    createVCardQrPayload: (input: Record<string, unknown> | HostedEpmRecord) => string;
  };

  type QrCodeModule = {
    toDataURL: (input: string, options?: Record<string, unknown>) => Promise<string>;
  };

  type WalletRuntimeModule = {
    mountWalletUI: (host: HTMLElement, options?: Record<string, unknown>) => Promise<MountedWalletUI>;
  };

  const identityRuntimeModules = import.meta.glob('../../../src/ui/runtime/identity.ts');
  const walletRuntimeModules = import.meta.glob('../../../src/ui/runtime/wallet-ui.ts');
  const NEW_EPM_ID = '__new-hosted-epm__';
  const PROFILE_FIELDS = [
    { key: 'dn', label: 'Display name', placeholder: 'Jane Example' },
    { key: 'legal_name', label: 'Legal name', placeholder: 'Jane Example LLC' },
    { key: 'given_name', label: 'Given name', placeholder: 'Jane' },
    { key: 'family_name', label: 'Family name', placeholder: 'Example' },
    { key: 'email', label: 'Email', placeholder: 'jane@example.com' },
    { key: 'telephone', label: 'Telephone', placeholder: '+1 555 0100' },
    { key: 'entity_type', label: 'Entity type', placeholder: 'person, organization, node' },
    { key: 'peer_id', label: 'Peer ID', placeholder: '12D3KooW...' },
    { key: 'epm_cid', label: 'EPM CID', placeholder: 'bafy...' },
    { key: 'public_key', label: 'Public key', placeholder: 'Public signing or encryption key' },
    { key: 'multiformat_address', label: 'Multiformat address', placeholder: '/ip4/.../p2p/...' },
  ] as const;

  let selectedId = '';
  let draftId = '';
  let draftKind: HostedEpmKind = 'hosted';
  let draftFields: Record<string, string> = createEmptyDraftFields();
  let draftSourceKey = '';
  let saveState = 'Select a hosted EPM or create one.';
  let importState = 'Import public EPM JSON, .epm, vCard, or .vcf files.';
  let downloadState = 'Downloads contain public EPM fields and public keys only.';
  let walletState = 'Wallet surface is ready to load.';
  let coreState = 'Encrypted Core import/export requires the local wallet adapter.';
  let walletRecords: Array<Record<string, unknown>> = [];
  let qrDataUrl = '';
  let qrState = 'Public vCard QR uses public EPM fields and public keys only.';
  let qrPayloadKey = '';
  let vcardPayload = '';
  let vcardRecordKey = '';
  let walletHost: HTMLElement | null = null;
  let mountedWallet: MountedWalletUI | null = null;
  let identityRuntimePromise: Promise<IdentityRuntimeModule> | null = null;
  let qrCodeModulePromise: Promise<QrCodeModule> | null = null;
  let walletRuntimePromise: Promise<WalletRuntimeModule> | null = null;

  $: if (!selectedId && hostedEpms.length > 0) {
    selectedId = hostedEpms[0].id;
  }
  $: activeRecord = hostedEpms.find((record) => record.id === selectedId) ?? null;
  $: syncDraftFromRecord(activeRecord);
  $: previewRecord = buildPreviewRecord(activeRecord);
  $: publicJson = previewRecord ? JSON.stringify(previewRecord.epmJson, null, 2) : '{}';
  $: void updateVCardPayload(previewRecord);
  $: void renderQr(vcardPayload);
  $: nodeSelfCount = hostedEpms.filter((record) => record.kind === 'node-self').length;
  $: hostedCount = hostedEpms.filter((record) => record.kind === 'hosted').length;
  $: fallbackNodeIdentity = summary?.peerId ?? stringValue(profile?.peer_id) ?? stringValue(profile?.PeerID) ?? 'pending';

  onDestroy(() => {
    void mountedWallet?.destroy?.();
  });

  function createEmptyDraftFields(): Record<string, string> {
    return Object.fromEntries(PROFILE_FIELDS.map((field) => [field.key, '']));
  }

  function syncDraftFromRecord(record: HostedEpmRecord | null): void {
    const sourceKey = record ? `${record.id}:${record.updatedAt ?? ''}` : selectedId;
    if (sourceKey === draftSourceKey) return;
    draftSourceKey = sourceKey;

    if (!record) {
      if (selectedId === NEW_EPM_ID) return;
      draftId = '';
      draftKind = 'hosted';
      draftFields = createEmptyDraftFields();
      return;
    }

    draftId = record.id;
    draftKind = record.kind;
    draftFields = Object.fromEntries(PROFILE_FIELDS.map((field) => [
      field.key,
      fieldValue(record, field.key),
    ]));
  }

  function fieldValue(record: HostedEpmRecord, key: string): string {
    if (key === 'peer_id') return record.peerId;
    if (key === 'epm_cid') return record.epmCid ?? stringValue(record.epmJson.epm_cid) ?? stringValue(record.epmJson.epmCid) ?? '';
    const value = record.epmJson[key];
    if (Array.isArray(value)) return value.map((entry) => String(entry)).join(', ');
    return stringValue(value) ?? '';
  }

  function buildPreviewRecord(record: HostedEpmRecord | null): HostedEpmRecord | null {
    if (!record && selectedId !== NEW_EPM_ID) return null;
    const epmJson = createDraftEpmJson(record);
    const id = record?.id ?? (draftId.trim() || slugFromDraft() || 'hosted-epm');
    return normalizeHostedEpmRecord({
      id,
      kind: record?.kind ?? draftKind,
      epm_cid: draftFields.epm_cid,
      epm_json: epmJson,
      updatedAt: record?.updatedAt,
    });
  }

  function createDraftEpmJson(record: HostedEpmRecord | null): Record<string, unknown> {
    const epmJson: Record<string, unknown> = { ...(record?.epmJson ?? {}) };
    for (const field of PROFILE_FIELDS) {
      const value = draftFields[field.key]?.trim();
      if (value) {
        epmJson[field.key] = field.key === 'multiformat_address' && value.includes(',')
          ? value.split(',').map((entry) => entry.trim()).filter(Boolean)
          : value;
      } else {
        delete epmJson[field.key];
      }
    }
    return epmJson;
  }

  function startHostedEpm(): void {
    selectedId = NEW_EPM_ID;
    draftId = '';
    draftKind = 'hosted';
    draftFields = createEmptyDraftFields();
    draftSourceKey = NEW_EPM_ID;
    saveState = 'New hosted identity ready for public EPM fields.';
  }

  async function saveProfile(): Promise<void> {
    if (!backend) {
      saveState = 'Backend unavailable; hosted EPM save is disabled.';
      return;
    }
    const record = buildPreviewRecord(activeRecord);
    if (!record) {
      saveState = 'Select or create an EPM before saving.';
      return;
    }
    saveState = 'Saving public EPM profile...';
    try {
      const result = await backend.saveHostedEpm(record);
      if (!result.ok || !result.data) {
        saveState = result.capability.reason ?? 'Hosted EPM save is unavailable.';
        return;
      }
      selectedId = result.data.id;
      saveState = `${result.data.kind === 'node-self' ? 'Node self' : 'Hosted'} public EPM saved.`;
      await onReload();
    } catch (error) {
      saveState = errorMessage(error);
    }
  }

  async function deleteHostedEpm(): Promise<void> {
    if (!backend || !activeRecord || activeRecord.kind === 'node-self') return;
    saveState = 'Deleting hosted EPM...';
    try {
      const result = await backend.deleteHostedEpm(activeRecord.id);
      saveState = result.ok ? 'Hosted EPM deleted.' : result.capability.reason ?? 'Hosted EPM delete is unavailable.';
      if (result.ok) {
        selectedId = '';
        await onReload();
      }
    } catch (error) {
      saveState = errorMessage(error);
    }
  }

  async function importEpmFile(event: Event): Promise<void> {
    const input = event.currentTarget as HTMLInputElement;
    const file = input.files?.[0];
    input.value = '';
    if (!file || !backend) return;

    importState = `Importing ${file.name}...`;
    try {
      const originalText = await file.text();
      const text = isVCardName(file.name) ? JSON.stringify(epmJsonFromVCard(originalText)) : originalText;
      const bytes = new TextEncoder().encode(text);
      const result = await backend.importHostedEpm({ name: file.name, bytes, text });
      if (!result.ok || !result.data) {
        importState = result.capability.reason ?? 'Hosted EPM import is unavailable.';
        return;
      }
      selectedId = result.data.id;
      importState = 'Imported hosted public EPM.';
      await onReload();
    } catch (error) {
      importState = errorMessage(error);
    }
  }

  async function downloadHostedEpm(record: HostedEpmRecord | null, format: 'json' | 'epm' | 'vcard'): Promise<void> {
    if (!backend || !record) {
      downloadState = 'Select a hosted EPM before downloading.';
      return;
    }
    downloadState = `Preparing public ${formatLabel(format)} download...`;
    try {
      const result = await backend.downloadHostedEpm(record.id, format);
      if (!result.ok || !result.data) {
        downloadState = result.capability.reason ?? `Public ${formatLabel(format)} download is unavailable.`;
        return;
      }
      triggerDownload(result.data.url, result.data.filename);
      downloadState = `Public ${formatLabel(format)} download started.`;
    } catch (error) {
      downloadState = errorMessage(error);
    }
  }

  async function loadWallets(): Promise<void> {
    if (!backend) {
      walletState = 'Backend unavailable; wallet lookup is disabled.';
      return;
    }
    walletState = 'Loading wallet-derived public identities...';
    try {
      const result = await backend.listWalletsAndEpms();
      walletRecords = result.data ?? [];
      walletState = result.ok
        ? `Loaded ${walletRecords.length} wallet or EPM record${walletRecords.length === 1 ? '' : 's'}.`
        : result.capability.reason ?? 'Wallet identity list is unavailable.';
    } catch (error) {
      walletState = errorMessage(error);
    }
  }

  async function addWallet(): Promise<void> {
    if (!walletHost) {
      walletState = 'Wallet host is not mounted yet.';
      return;
    }
    walletState = 'Loading hd-wallet-ui...';
    try {
      const { mountWalletUI } = await loadWalletRuntime();
      mountedWallet ??= await mountWalletUI(walletHost, { backendMode: backend?.mode ?? 'desktop-local' });
      await mountedWallet.openLogin?.();
      walletState = 'hd-wallet-ui wallet surface opened.';
    } catch (error) {
      walletState = `hd-wallet-ui unavailable: ${errorMessage(error)}`;
    }
  }

  async function beginWalletClaim(): Promise<void> {
    if (!backend) return;
    walletState = 'Starting wallet EPM claim...';
    try {
      const result = await backend.beginClaimEpm();
      walletState = result.ok ? 'Wallet EPM claim started.' : result.capability.reason ?? 'Wallet EPM claim requires local confirmation.';
    } catch (error) {
      walletState = errorMessage(error);
    }
  }

  async function exportEncryptedCore(): Promise<void> {
    if (!backend) {
      coreState = 'Backend unavailable; Encrypted Core export is disabled.';
      return;
    }
    coreState = 'Requesting Encrypted Core export...';
    try {
      const result = await backend.exportCore();
      const url = stringValue(result.data?.url);
      if (result.ok && url) {
        triggerDownload(url, stringValue(result.data?.filename) ?? 'sdn-encrypted-core.sdncore');
      }
      coreState = result.ok ? 'Encrypted Core export request accepted.' : result.capability.reason ?? 'Encrypted Core export requires local wallet confirmation.';
    } catch (error) {
      coreState = errorMessage(error);
    }
  }

  async function importEncryptedCore(event: Event): Promise<void> {
    const input = event.currentTarget as HTMLInputElement;
    const file = input.files?.[0];
    input.value = '';
    if (!file || !backend) return;
    coreState = `Importing Encrypted Core artifact ${file.name}...`;
    try {
      const bytes = new Uint8Array(await file.arrayBuffer());
      const result = await backend.importCore({
        name: file.name,
        encrypted_core: bytesToBase64(bytes),
        encoding: 'base64',
      });
      coreState = result.ok ? 'Encrypted Core import request accepted.' : result.capability.reason ?? 'Encrypted Core import requires local wallet confirmation.';
    } catch (error) {
      coreState = errorMessage(error);
    }
  }

  async function updateVCardPayload(record: HostedEpmRecord | null): Promise<void> {
    const recordKey = record ? JSON.stringify([record.id, record.kind, record.peerId, record.epmCid, record.epmJson]) : '';
    if (recordKey === vcardRecordKey) return;
    vcardRecordKey = recordKey;
    if (!record) {
      vcardPayload = '';
      return;
    }
    try {
      const runtime = await loadIdentityRuntime();
      const createVCardQrPayload = runtime.createVCardQrPayload;
      if (vcardRecordKey === recordKey) {
        vcardPayload = createVCardQrPayload(record);
      }
    } catch {
      if (vcardRecordKey === recordKey) {
        vcardPayload = createVCardQrPayloadLocal(record);
      }
    }
  }

  async function renderQr(payload: string): Promise<void> {
    if (payload === qrPayloadKey) return;
    qrPayloadKey = payload;
    if (!payload) {
      qrDataUrl = '';
      return;
    }
    try {
      const qrCode = await loadQrCodeModule();
      const dataUrl = await qrCode.toDataURL(payload, {
        color: { dark: '#f5f5f7', light: '#00000000' },
        errorCorrectionLevel: 'M',
        margin: 1,
        width: 240,
      });
      if (qrPayloadKey === payload) {
        qrDataUrl = dataUrl;
        qrState = 'Public vCard QR ready. It contains public EPM fields and public keys only.';
      }
    } catch (error) {
      if (qrPayloadKey === payload) {
        qrDataUrl = '';
        qrState = `QR unavailable: ${errorMessage(error)}`;
      }
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

  async function loadWalletRuntime(): Promise<WalletRuntimeModule> {
    walletRuntimePromise ??= loadWalletRuntimeModule();
    return walletRuntimePromise;
  }

  async function loadWalletRuntimeModule(): Promise<WalletRuntimeModule> {
    const load = walletRuntimeModules['../../../src/ui/runtime/wallet-ui.ts'];
    if (!load) {
      throw new Error('hd-wallet-ui runtime module is unavailable');
    }
    return await load() as WalletRuntimeModule;
  }

  function normalizeHostedEpmRecord(input: Record<string, unknown>): HostedEpmRecord {
    const epmJson = createPublicEpmExport(readRecord(input.epm_json ?? input.epmJson ?? input));
    const id = stringValue(input.id) ?? stringValue(input.epm_id) ?? stringValue(input.epmId) ?? stringValue(epmJson.peer_id) ?? 'self';
    return {
      id,
      kind: input.kind === 'node-self' ? 'node-self' : 'hosted',
      label: stringValue(epmJson.dn) ?? stringValue(epmJson.DN) ?? stringValue(epmJson.displayName) ?? stringValue(epmJson.name) ?? id,
      peerId: stringValue(epmJson.peer_id) ?? stringValue(epmJson.peerId) ?? stringValue(epmJson.PeerID) ?? '',
      epmCid: stringValue(input.epm_cid) ?? stringValue(input.epmCid) ?? stringValue(epmJson.epm_cid) ?? stringValue(epmJson.epmCid),
      epmJson,
      source: stringValue(input.source),
      updatedAt: typeof input.updatedAt === 'number' ? input.updatedAt : undefined,
    };
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

  function addVCardLine(lines: string[], key: string, value: string | undefined): void {
    if (value?.trim()) lines.push(`${key}:${value.replace(/\r?\n/g, ' ')}`);
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

  function epmJsonFromVCard(text: string): Record<string, unknown> {
    const fields: Record<string, unknown> = {};
    for (const rawLine of text.split(/\r?\n/)) {
      const [rawKey, ...rest] = rawLine.split(':');
      const key = rawKey?.trim().toUpperCase();
      const value = rest.join(':').trim();
      if (!key || !value) continue;
      if (key === 'FN') fields.dn = value;
      if (key === 'EMAIL') fields.email = value;
      if (key === 'TEL') fields.telephone = value;
      if (key === 'X-SDN-PEER-ID') fields.peer_id = value;
      if (key === 'X-SDN-EPM-CID') fields.epm_cid = value;
      if (key === 'X-SDN-PUBLIC-KEY') fields.public_key = value;
    }
    return fields;
  }

  function slugFromDraft(): string {
    const source = draftFields.dn || draftFields.peer_id || 'hosted-epm';
    return source.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 48);
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

  function formatLabel(format: 'json' | 'epm' | 'vcard'): string {
    if (format === 'vcard') return 'vCard';
    return format.toUpperCase();
  }

  function isVCardName(name: string): boolean {
    return /\.(vcf|vcard)$/i.test(name);
  }

  function bytesToBase64(bytes: Uint8Array): string {
    let binary = '';
    for (const byte of bytes) binary += String.fromCharCode(byte);
    return btoa(binary);
  }

  function stringValue(value: unknown): string | undefined {
    return typeof value === 'string' && value.trim() ? value.trim() : undefined;
  }

  function errorMessage(error: unknown): string {
    return error instanceof Error ? error.message : String(error);
  }
</script>

<section class="sdn-identity-workspace">
  <article class="sdn-card sdn-glass">
    <div class="sdn-card-head">
      <div>
        <h2>Hosted EPM Registry</h2>
        <p>Node self identity stays separate from hosted public EPM identities.</p>
      </div>
      <button class="sdn-button" type="button" on:click={startHostedEpm}>Add hosted EPM</button>
    </div>

    <div class="sdn-registry-row">
      <label>
        <span>Local identity</span>
        <select class="sdn-input sdn-select" bind:value={selectedId} aria-label="Local hosted EPM selector">
          {#each hostedEpms as record}
            <option value={record.id}>
              {record.kind === 'node-self' ? 'Node self' : 'Hosted'} - {record.label || record.id}
            </option>
          {/each}
          {#if selectedId === NEW_EPM_ID}
            <option value={NEW_EPM_ID}>Hosted - new public EPM</option>
          {/if}
        </select>
      </label>
      <div class="sdn-identity-counts">
        <span class="sdn-chip" data-state={nodeSelfCount ? 'online' : 'warning'}>Node self: {nodeSelfCount}</span>
        <span class="sdn-chip" data-state={hostedCount ? 'online' : 'warning'}>Hosted: {hostedCount}</span>
        <span class="sdn-chip" data-state={fallbackNodeIdentity === 'pending' ? 'warning' : 'special'}>Peer: {fallbackNodeIdentity}</span>
      </div>
    </div>
  </article>

  <article class="sdn-card sdn-glass">
    <div class="sdn-card-head">
      <div>
        <h2>Public Profile Fields</h2>
        <p>These fields are saved into the public EPM profile only.</p>
      </div>
      <span class="sdn-chip" data-state={draftKind === 'node-self' ? 'special' : 'online'}>
        {draftKind === 'node-self' ? 'Node self' : 'Hosted identity'}
      </span>
    </div>

    <div class="sdn-form-grid">
      {#each PROFILE_FIELDS as field}
        <label>
          <span>{field.label}</span>
          <input
            class="sdn-input"
            bind:value={draftFields[field.key]}
            placeholder={field.placeholder}
            autocomplete="off"
          />
        </label>
      {/each}
    </div>

    <div class="sdn-toolbar">
      <button class="sdn-button" type="button" on:click={saveProfile} disabled={!backend}>Save public profile</button>
      <button class="sdn-button sdn-button-muted" type="button" on:click={deleteHostedEpm} disabled={!backend || !activeRecord || activeRecord.kind === 'node-self'}>
        Delete hosted EPM
      </button>
      <label class="sdn-file-button">
        Import EPM JSON/vCard
        <input type="file" accept=".json,.epm,.vcf,.vcard" on:change={importEpmFile} />
      </label>
    </div>
    <p class="sdn-status-line">{saveState}</p>
    <p class="sdn-status-line">{importState}</p>
  </article>

  <article class="sdn-card sdn-glass">
    <div class="sdn-card-head">
      <div>
        <h2>Public EPM Downloads</h2>
        <p>Exports include public EPM fields and public keys only.</p>
      </div>
    </div>
    <div class="sdn-toolbar">
      <button class="sdn-button" type="button" on:click={() => downloadHostedEpm(previewRecord, 'json')} disabled={!backend || !previewRecord}>
        Download JSON
      </button>
      <button class="sdn-button" type="button" on:click={() => downloadHostedEpm(previewRecord, 'epm')} disabled={!backend || !previewRecord}>
        Download EPM
      </button>
      <button class="sdn-button" type="button" on:click={() => downloadHostedEpm(previewRecord, 'vcard')} disabled={!backend || !previewRecord}>
        Download vCard
      </button>
    </div>
    <p class="sdn-status-line">{downloadState}</p>
    <pre class="sdn-public-json">{publicJson}</pre>
  </article>

  <article class="sdn-card sdn-glass">
    <div class="sdn-card-head">
      <div>
        <h2>Public vCard QR</h2>
        <p>QR content is generated from public EPM data and public keys only.</p>
      </div>
    </div>
    <div class="sdn-qr-layout">
      <div class="sdn-qr-frame" aria-label="Public vCard QR preview">
        {#if qrDataUrl}
          <img src={qrDataUrl} alt="Public vCard QR code" />
        {:else}
          <span>QR pending</span>
        {/if}
      </div>
      <textarea class="sdn-input sdn-vcard-preview" readonly value={vcardPayload} aria-label="Public vCard payload"></textarea>
    </div>
    <p class="sdn-status-line">{qrState}</p>
  </article>

  <article class="sdn-card sdn-glass">
    <div class="sdn-card-head">
      <div>
        <h2>Wallet Identity</h2>
        <p>Use the canonical hd-wallet-ui surface for wallet-derived public identity.</p>
      </div>
    </div>
    <div class="sdn-toolbar">
      <button class="sdn-button" type="button" on:click={addWallet}>Add wallet</button>
      <button class="sdn-button" type="button" on:click={loadWallets} disabled={!backend}>Refresh wallet EPMs</button>
      <button class="sdn-button" type="button" on:click={beginWalletClaim} disabled={!backend}>Claim EPM</button>
    </div>
    <div class="sdn-wallet-host" bind:this={walletHost}></div>
    <p class="sdn-status-line">{walletState}</p>
    {#if walletRecords.length}
      <div class="sdn-capability-list">
        {#each walletRecords.slice(0, 6) as walletRecord}
          <span class="sdn-chip" data-state="special">{String(walletRecord.dn ?? walletRecord.peer_id ?? walletRecord.id ?? 'wallet EPM')}</span>
        {/each}
      </div>
    {/if}
  </article>

  <article class="sdn-card sdn-glass">
    <div class="sdn-card-head">
      <div>
        <h2>Encrypted Core</h2>
        <p>Core import and export actions accept encrypted artifacts only.</p>
      </div>
    </div>
    <div class="sdn-toolbar">
      <button class="sdn-button" type="button" on:click={exportEncryptedCore} disabled={!backend}>Export Encrypted Core</button>
      <label class="sdn-file-button">
        Import Encrypted Core
        <input type="file" accept=".sdncore,.kmf,.enc,application/octet-stream" on:change={importEncryptedCore} />
      </label>
    </div>
    <p class="sdn-status-line">{coreState}</p>
    <div class="sdn-capability-list">
      {#each capabilities.filter((capability) => capability.id.toLowerCase().includes('identity') || capability.id.toLowerCase().includes('wallet')).slice(0, 6) as capability}
        <span class="sdn-chip" data-state={capability.state === 'available' ? 'online' : 'warning'}>
          {capability.id}: {capability.state}
        </span>
      {/each}
    </div>
  </article>
</section>

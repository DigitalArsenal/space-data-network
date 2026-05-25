<script lang="ts">
  import { onDestroy } from 'svelte';
  import { decodeEpmFlatBuffer } from '../../../src/ui/runtime/epm-flatbuffer';
  import {
    createVCardQrPayload as createVCardQrPayloadLocal,
    epmJsonFromVCard as parseVCardEpmJson,
    identityPublicKeyValue,
  } from '../../../src/ui/runtime/identity-vcard';
  import { shortPeerId } from '../../../src/ui/runtime/peer-identity';

  type HostedEpmKind = 'node-self' | 'hosted';
  type CapabilityState = 'available' | 'degraded' | 'unavailable' | 'permission-required' | 'remote-only' | 'local-only';
  type IdentityView = 'profile' | 'edit-profile' | 'hosted-epms' | 'keys-import' | 'security' | 'downloads' | 'settings';

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

  interface NodeIdentitySettings {
    ttlMs: number | 'app';
    flatbufferStoragePath?: string;
    updatedAt?: string;
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
    saveNodeProfile(profile: Record<string, unknown>): Promise<BackendResult<Record<string, unknown>>>;
    saveHostedEpm(record: HostedEpmRecord): Promise<BackendResult<HostedEpmRecord>>;
    importHostedEpm(input: { name: string; bytes?: Uint8Array; text?: string }): Promise<BackendResult<HostedEpmRecord>>;
    deleteHostedEpm(id: string): Promise<BackendResult<Record<string, unknown>>>;
    downloadHostedEpm(id: string, format: 'json' | 'epm' | 'vcard'): Promise<BackendResult<{ url: string; filename: string }>>;
    listWalletsAndEpms(): Promise<BackendResult<Array<Record<string, unknown>>>>;
    exportCore(): Promise<BackendResult<Record<string, unknown>>>;
    importCore(core: Record<string, unknown>): Promise<BackendResult<Record<string, unknown>>>;
    saveNodeIdentitySettings(settings: NodeIdentitySettings): Promise<BackendResult<NodeIdentitySettings>>;
    selectFlatbufferStorageLocation(currentPath?: string | null): Promise<BackendResult<{ canceled: boolean; path: string | null }>>;
    saveNodeAccessUser(user: { xpub: string; name?: string; trustLevel: string; signingPubKeyHex?: string }): Promise<BackendResult<Record<string, unknown>>>;
  }

  interface MountedWalletUI {
    openLogin?: () => void | Promise<void>;
    openAccount?: () => void | Promise<void>;
    destroy?: () => void | Promise<void>;
  }

  type IdentityRuntimeModule = {
    createVCardQrPayload: (input: Record<string, unknown> | HostedEpmRecord) => string;
  };

  type QrCodeModule = {
    toDataURL: (input: string, options?: Record<string, unknown>) => Promise<string>;
  };

  type WalletRuntimeModule = {
    mountWalletUI: (host: HTMLElement, options?: Record<string, unknown>) => Promise<MountedWalletUI>;
  };

  export let backend: IdentityBackend | null = null;
  export let summary: NodeSummary | null = null;
  export let profile: Record<string, unknown> | null = null;
  export let hostedEpms: HostedEpmRecord[] = [];
  export let onReload: () => void | Promise<void> = () => {};
  export let nodeIdentityLocked = false;
  export let nodeIdentitySettings: NodeIdentitySettings = { ttlMs: 3600000 };
  export let onNodeIdentitySettingsSave: (settings: NodeIdentitySettings) => void | Promise<void> = () => {};

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
  ] as const;

  let view: IdentityView = 'profile';
  let selectedId = '';
  let draftId = '';
  let draftKind: HostedEpmKind = 'hosted';
  let draftFields: Record<string, string> = createEmptyDraftFields();
  let draftSourceKey = '';
  let saveState = '';
  let importState = '';
  let downloadState = '';
  let walletState = '';
  let coreState = '';
  let securityState = '';
  let settingsState = '';
  let qrDataUrl = '';
  let qrState = '';
  let qrPayloadKey = '';
  let vcardPayload = '';
  let vcardRecordKey = '';
  let walletHost: HTMLElement | null = null;
  let mountedWallet: MountedWalletUI | null = null;
  let identityRuntimePromise: Promise<IdentityRuntimeModule> | null = null;
  let qrCodeModulePromise: Promise<QrCodeModule> | null = null;
  let walletRuntimePromise: Promise<WalletRuntimeModule> | null = null;
  let keygenUsername = '';
  let keygenPassword = '';
  let passphraseInput = '';
  let grantPublicKey = '';
  let unlockDurationValue = '3600000';
  let unlockDurationSourceKey = '';
  let flatbufferStoragePathValue = '';
  let flatbufferStoragePathSourceKey = '';

  $: if (!selectedId && hostedEpms.length > 0) {
    selectedId = hostedEpms[0].id;
  }
  $: fallbackRecord = createFallbackRecord();
  $: activeRecord = selectedId === NEW_EPM_ID
    ? null
    : hostedEpms.find((record) => record.id === selectedId) ?? hostedEpms[0] ?? fallbackRecord;
  $: syncDraftFromRecord(activeRecord);
  $: previewRecord = view === 'edit-profile' || selectedId === NEW_EPM_ID ? buildPreviewRecord(activeRecord) : null;
  $: profileRecord = previewRecord ?? activeRecord ?? fallbackRecord;
  $: publicJson = profileRecord ? JSON.stringify(createPublicEpmExport(profileRecord.epmJson), null, 2) : '{}';
  $: void updateVCardPayload(profileRecord);
  $: void renderQr(vcardPayload);
  $: fallbackNodeIdentity = summary?.peerId ?? stringValue(profile?.peer_id) ?? stringValue(profile?.PeerID) ?? 'pending';
  $: syncUnlockDurationFromSettings(nodeIdentitySettings);

  onDestroy(() => {
    void mountedWallet?.destroy?.();
  });

  function setView(nextView: IdentityView): void {
    view = nextView;
  }

  function createEmptyDraftFields(): Record<string, string> {
    return Object.fromEntries(PROFILE_FIELDS.map((field) => [field.key, '']));
  }

  function createFallbackRecord(): HostedEpmRecord {
    const epmJson = createPublicEpmExport({
      ...(profile ?? {}),
      dn: summary?.displayName ?? stringValue(profile?.dn) ?? 'Space Data Network',
      peer_id: summary?.peerId ?? stringValue(profile?.peer_id) ?? stringValue(profile?.PeerID) ?? '',
      agent_version: summary?.agentVersion ?? stringValue(profile?.agent_version) ?? '',
    });
    return normalizeHostedEpmRecord({
      id: 'self',
      kind: 'node-self',
      epm_json: epmJson,
    });
  }

  function syncDraftFromRecord(record: HostedEpmRecord | null): void {
    const sourceKey = record ? draftSourceKeyForRecord(record) : selectedId;
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

  function draftSourceKeyForRecord(record: HostedEpmRecord): string {
    return [
      record.id,
      record.kind,
      record.updatedAt ?? '',
      record.peerId,
      ...PROFILE_FIELDS.map((field) => fieldValue(record, field.key)),
    ].join('\u001f');
  }

  function fieldValue(record: HostedEpmRecord, key: string): string {
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
      epm_json: epmJson,
      updatedAt: record?.updatedAt,
    });
  }

  function createDraftEpmJson(record: HostedEpmRecord | null): Record<string, unknown> {
    const epmJson: Record<string, unknown> = { ...(record?.epmJson ?? {}) };
    for (const field of PROFILE_FIELDS) {
      const value = draftFields[field.key]?.trim();
      if (value) {
        epmJson[field.key] = value;
      } else {
        delete epmJson[field.key];
      }
    }
    return createPublicEpmExport(epmJson);
  }

  function startHostedEpm(): void {
    selectedId = NEW_EPM_ID;
    draftId = '';
    draftKind = 'hosted';
    draftFields = createEmptyDraftFields();
    draftSourceKey = NEW_EPM_ID;
    saveState = '';
    setView('edit-profile');
  }

  function selectRecord(record: HostedEpmRecord, nextView: IdentityView = 'profile'): void {
    selectedId = record.id;
    setView(nextView);
  }

  async function saveProfile(): Promise<void> {
    if (!backend) {
      saveState = 'Backend unavailable.';
      return;
    }
    const record = buildPreviewRecord(activeRecord);
    if (!record) {
      saveState = 'Select or create an EPM before saving.';
      return;
    }
    saveState = 'Saving...';
    try {
      if (record.kind === 'node-self') {
        const result = await backend.saveNodeProfile(record.epmJson);
        if (!result.ok || !result.data) {
          saveState = result.capability.reason ?? 'Node EPM save is unavailable.';
          return;
        }
        selectedId = 'self';
      } else {
        const result = await backend.saveHostedEpm(record);
        if (!result.ok || !result.data) {
          saveState = result.capability.reason ?? 'Local user save is unavailable.';
          return;
        }
        selectedId = result.data.id;
      }
      saveState = '';
      await onReload();
      setView('profile');
    } catch (error) {
      saveState = errorMessage(error);
    }
  }

  async function deleteHostedEpm(): Promise<void> {
    if (!backend || !activeRecord || activeRecord.kind === 'node-self') return;
    saveState = 'Deleting...';
    try {
      const result = await backend.deleteHostedEpm(activeRecord.id);
      if (result.ok) {
        saveState = '';
        selectedId = '';
        await onReload();
      } else {
        saveState = result.capability.reason ?? 'Local user delete is unavailable.';
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
      const text = isVCardName(file.name) ? JSON.stringify(parseVCardEpmJson(originalText)) : originalText;
      const bytes = new TextEncoder().encode(text);
      const result = await backend.importHostedEpm({ name: file.name, bytes, text });
      if (!result.ok || !result.data) {
        importState = result.capability.reason ?? 'Local user import is unavailable.';
        return;
      }
      selectedId = result.data.id;
      importState = '';
      await onReload();
      setView('profile');
    } catch (error) {
      importState = errorMessage(error);
    }
  }

  async function downloadHostedEpm(record: HostedEpmRecord | null, format: 'json' | 'epm' | 'vcard'): Promise<void> {
    if (!backend || !record) {
      downloadState = 'Select an EPM before downloading.';
      return;
    }
    downloadState = `Preparing ${formatLabel(format)}...`;
    try {
      const result = await backend.downloadHostedEpm(record.id, format);
      if (!result.ok || !result.data) {
        downloadState = result.capability.reason ?? `${formatLabel(format)} download is unavailable.`;
        return;
      }
      triggerDownload(result.data.url, result.data.filename);
      downloadState = '';
    } catch (error) {
      downloadState = errorMessage(error);
    }
  }

  async function loadWallets(): Promise<void> {
    if (!backend) {
      walletState = 'Backend unavailable.';
      return;
    }
    walletState = 'Loading...';
    try {
      const result = await backend.listWalletsAndEpms();
      walletState = result.ok ? '' : result.capability.reason ?? 'Wallet identity list is unavailable.';
    } catch (error) {
      walletState = errorMessage(error);
    }
  }

  async function openWalletFlow(flow: 'deterministic' | 'passphrase'): Promise<void> {
    if (flow === 'deterministic' && (!keygenUsername.trim() || !keygenPassword.trim())) {
      walletState = 'Username and password required.';
      return;
    }
    if (flow === 'passphrase' && !passphraseInput.trim()) {
      walletState = 'Passphrase required.';
      return;
    }
    walletState = flow === 'deterministic' ? 'Opening deterministic keygen...' : 'Opening passphrase import...';
    await addWallet();
  }

  async function addWallet(): Promise<void> {
    if (!walletHost) {
      walletState = 'Wallet host is not mounted yet.';
      return;
    }
    try {
      const { mountWalletUI } = await loadWalletRuntime();
      mountedWallet ??= await mountWalletUI(walletHost, { backendMode: backend?.mode ?? 'desktop-local' });
      await mountedWallet.openLogin?.();
      walletState = '';
    } catch (error) {
      walletState = `hd-wallet-wasm unavailable: ${errorMessage(error)}`;
    }
  }

  async function exportEncryptedCore(): Promise<void> {
    if (!backend) {
      coreState = 'Backend unavailable.';
      return;
    }
    coreState = 'Exporting encrypted Core...';
    try {
      const result = await backend.exportCore();
      const url = stringValue(result.data?.url);
      if (result.ok && url) {
        triggerDownload(url, stringValue(result.data?.filename) ?? 'sdn-encrypted-core.sdncore');
      }
      coreState = result.ok ? '' : result.capability.reason ?? 'Encrypted Core export requires local wallet confirmation.';
    } catch (error) {
      coreState = errorMessage(error);
    }
  }

  async function importEncryptedPrivateKeyFile(event: Event): Promise<void> {
    const input = event.currentTarget as HTMLInputElement;
    const file = input.files?.[0];
    input.value = '';
    if (!file || !backend) return;
    coreState = `Importing ${file.name}...`;
    try {
      const bytes = new Uint8Array(await file.arrayBuffer());
      const result = await backend.importCore({
        name: file.name,
        encrypted_private_key: bytesToBase64(bytes),
        encoding: 'base64',
      });
      coreState = result.ok ? '' : result.capability.reason ?? 'Encrypted private key import requires local wallet confirmation.';
    } catch (error) {
      coreState = errorMessage(error);
    }
  }

  async function grantAdminForPublicKey(publicKey: string, name = '', signingPubKeyHex = ''): Promise<void> {
    if (!backend) return;
    const normalizedPublicKey = publicKey.trim();
    if (!normalizedPublicKey) {
      securityState = 'Public key is required.';
      return;
    }
    securityState = 'Granting admin...';
    try {
      const result = await backend.saveNodeAccessUser({
        xpub: normalizedPublicKey,
        name: name.trim(),
        trustLevel: 'admin',
        signingPubKeyHex: signingPubKeyHex.trim(),
      });
      if (!result.ok) {
        securityState = result.capability.reason ?? 'Grant failed.';
        return;
      }
      grantPublicKey = '';
      securityState = '';
    } catch (error) {
      securityState = errorMessage(error);
    }
  }

  async function grantAdminFromSecurityFile(event: Event): Promise<void> {
    const input = event.currentTarget as HTMLInputElement;
    const file = input.files?.[0];
    input.value = '';
    if (!file || !backend) return;
    securityState = `Reading ${file.name}...`;
    try {
      const [buffer, text] = await Promise.all([file.arrayBuffer(), file.text()]);
      const grant = securityGrantFromUploadedIdentity(file.name, new Uint8Array(buffer), text);
      await grantAdminForPublicKey(grant.publicKey, grant.name, grant.signingPubKeyHex);
    } catch (error) {
      securityState = errorMessage(error);
    }
  }

  async function saveNodeIdentitySettings(): Promise<void> {
    if (!backend) {
      settingsState = 'Backend unavailable.';
      return;
    }
    const ttlMs = unlockDurationValue === 'app' ? 'app' : Number(unlockDurationValue);
    const nextSettings: NodeIdentitySettings = {
      ttlMs: ttlMs === 'app' ? 'app' : Math.max(60_000, ttlMs),
      flatbufferStoragePath: flatbufferStoragePathValue.trim(),
    };
    settingsState = 'Saving...';
    try {
      const result = await backend.saveNodeIdentitySettings(nextSettings);
      if (!result.ok || !result.data) {
        settingsState = result.capability.reason ?? 'Unlock duration save failed.';
        return;
      }
      await onNodeIdentitySettingsSave(result.data);
      settingsState = '';
    } catch (error) {
      settingsState = errorMessage(error);
    }
  }

  function syncUnlockDurationFromSettings(settings: NodeIdentitySettings): void {
    const key = String(settings.ttlMs ?? 3600000);
    if (key === unlockDurationSourceKey) return;
    unlockDurationSourceKey = key;
    unlockDurationValue = key;
  }

  $: syncFlatbufferStoragePathFromSettings(nodeIdentitySettings);

  function syncFlatbufferStoragePathFromSettings(settings: NodeIdentitySettings): void {
    const key = String(settings.flatbufferStoragePath ?? '');
    if (key === flatbufferStoragePathSourceKey) return;
    flatbufferStoragePathSourceKey = key;
    flatbufferStoragePathValue = key;
  }

  async function browseFlatbufferStorageLocation(): Promise<void> {
    if (!backend) {
      settingsState = 'Backend unavailable.';
      return;
    }
    settingsState = 'Opening directory picker...';
    try {
      const result = await backend.selectFlatbufferStorageLocation(flatbufferStoragePathValue || nodeIdentitySettings.flatbufferStoragePath || null);
      if (!result.ok || !result.data) {
        settingsState = result.capability.reason ?? 'Directory picker unavailable.';
        return;
      }
      if (result.data.canceled) {
        settingsState = '';
        return;
      }
      if (result.data.path) {
        flatbufferStoragePathValue = result.data.path;
      }
      settingsState = '';
    } catch (error) {
      settingsState = errorMessage(error);
    }
  }

  function resetFlatbufferStorageLocation(): void {
    flatbufferStoragePathValue = '';
    settingsState = '';
  }

  function securityGrantFromUploadedIdentity(filename: string, bytes: Uint8Array, text: string): { publicKey: string; name: string; signingPubKeyHex: string } {
    try {
      const textGrant = securityGrantFromText(text);
      if (textGrant.publicKey) return textGrant;
    } catch {
      // Fall through to binary EPM parsing below.
    }

    try {
      const epm = decodeEpmFlatBuffer(bytes);
      const binaryGrant = securityGrantFromJson(epm);
      if (binaryGrant.publicKey) return binaryGrant;
    } catch (error) {
      const suffix = filename ? ` (${filename})` : '';
      throw new Error(`Unable to read admin public key from uploaded EPM or .vcf${suffix}: ${errorMessage(error)}`);
    }

    throw new Error('Uploaded EPM or .vcf did not include a public key.');
  }

  function securityGrantFromText(text: string): { publicKey: string; name: string; signingPubKeyHex: string } {
    const trimmed = text.trim();
    if (!trimmed) throw new Error('uploaded EPM or .vcf is empty');
    if (/^BEGIN:VCARD/i.test(trimmed)) return securityGrantFromVCard(trimmed);
    return securityGrantFromJson(JSON.parse(trimmed));
  }

  function securityGrantFromJson(payload: unknown): { publicKey: string; name: string; signingPubKeyHex: string } {
    const objects = collectJsonObjects(payload);
    for (const object of objects) {
      const publicKey = firstString(
        object.xpub,
        object.XPUB,
        object.public_key,
        object.PUBLIC_KEY,
        object.publicKey,
        object.signing_public_key,
        object.signingPublicKey,
        object.signing_pubkey_hex,
      );
      const keys = Array.isArray(object.keys) ? object.keys : Array.isArray(object.KEYS) ? object.KEYS : [];
      const signingKey = keys.find((key) => firstString(key?.key_type, key?.KEY_TYPE).toLowerCase() === 'signing');
      const keyPublicKey = firstString(
        signingKey?.xpub,
        signingKey?.XPUB,
        signingKey?.public_key,
        signingKey?.PUBLIC_KEY,
        ...keys.map((key) => firstString(key?.xpub, key?.XPUB, key?.public_key, key?.PUBLIC_KEY)),
      );
      const resolvedPublicKey = publicKey || keyPublicKey;
      if (resolvedPublicKey) {
        return {
          publicKey: resolvedPublicKey,
          name: firstString(object.dn, object.DN, object.name, object.legal_name, object.LEGAL_NAME, 'Imported SDN identity'),
          signingPubKeyHex: firstString(object.signing_public_key, object.signingPublicKey, object.signing_pubkey_hex, signingKey?.public_key, signingKey?.PUBLIC_KEY),
        };
      }
    }

    throw new Error('uploaded EPM did not include a public key');
  }

  function securityGrantFromVCard(vcard: string): { publicKey: string; name: string; signingPubKeyHex: string } {
    const lines = vcardLines(vcard);
    const publicKey = firstString(
      vcardValue(lines, 'X-SDN-XPUB'),
      vcardValue(lines, 'X-XPUB'),
      vcardValue(lines, 'XPUB'),
      vcardValue(lines, 'X-SDN-PUBLIC-KEY'),
      vcardValue(lines, 'X-SDN-SIGNING-PUBLIC-KEY'),
      vcardEmailAlias(lines, 'spacedatanetwork.org'),
      unfoldedVCard(vcard).match(/\bxpub[A-Za-z0-9]+\b/)?.[0],
    );
    if (!publicKey) throw new Error('uploaded .vcf did not include a public key');
    return {
      publicKey,
      name: firstString(vcardValue(lines, 'FN'), vcardValue(lines, 'ORG'), 'Imported SDN identity'),
      signingPubKeyHex: firstString(
        vcardValue(lines, 'X-SDN-SIGNING-PUBLIC-KEY'),
        vcardEmailAlias(lines, 'signing.digitalarsenal.io', 'signing'),
      ),
    };
  }

  function collectJsonObjects(value: unknown, collected: Array<Record<string, unknown>> = [], depth = 0): Array<Record<string, unknown>> {
    if (depth > 4 || value == null) return collected;
    if (typeof value === 'string') {
      const trimmed = value.trim();
      if (trimmed.startsWith('{') || trimmed.startsWith('[')) {
        try {
          collectJsonObjects(JSON.parse(trimmed), collected, depth + 1);
        } catch {
          // Ignore strings that are not nested JSON.
        }
      }
      return collected;
    }
    if (Array.isArray(value)) {
      for (const item of value) collectJsonObjects(item, collected, depth + 1);
      return collected;
    }
    if (isRecord(value)) {
      collected.push(value);
      for (const key of ['epm_json', 'EPM_JSON', 'epm', 'EPM', 'record', 'RECORD']) {
        if (value[key] !== undefined) collectJsonObjects(value[key], collected, depth + 1);
      }
    }
    return collected;
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
      qrState = '';
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
        qrState = '';
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

  function isRecord(value: unknown): value is Record<string, unknown> {
    return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
  }

  function isSecretKey(key: string): boolean {
    return /private|secret|mnemonic|xpriv|core|seed/i.test(key);
  }

  function vcardLines(vcard: string): string[] {
    return unfoldedVCard(vcard).split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
  }

  function unfoldedVCard(vcard: string): string {
    return String(vcard ?? '').replace(/\r?\n[ \t]/g, '');
  }

  function vcardValue(lines: string[], fieldName: string): string {
    const normalizedField = fieldName.toUpperCase();
    for (const line of lines) {
      const colon = line.indexOf(':');
      if (colon < 0) continue;
      const name = line.slice(0, colon).split(';')[0].toUpperCase();
      if (name === normalizedField) return unescapeVCardValue(line.slice(colon + 1));
    }
    return '';
  }

  function vcardEmailAlias(lines: string[], domain: string, type?: string): string {
    const suffix = `@${domain}`.toLowerCase();
    for (const line of lines) {
      const colon = line.indexOf(':');
      if (colon < 0 || !line.slice(0, colon).toUpperCase().startsWith('EMAIL')) continue;
      if (type && !vcardLineHasEmailType(line.slice(0, colon), type)) continue;
      const value = unescapeVCardValue(line.slice(colon + 1));
      if (value.toLowerCase().endsWith(suffix)) return value.slice(0, -suffix.length);
    }
    return '';
  }

  function vcardLineHasEmailType(left: string, type: string): boolean {
    const wanted = type.toLowerCase();
    return left.split(';').slice(1).some((param) => {
      const normalized = param.trim().toLowerCase();
      if (normalized === wanted) return true;
      const [, rawValue = normalized] = normalized.split('=', 2);
      return rawValue.split(',').map((part) => part.trim()).includes(wanted);
    });
  }

  function unescapeVCardValue(value: string): string {
    return String(value ?? '')
      .replace(/\\n/g, '\n')
      .replace(/\\,/g, ',')
      .replace(/\\;/g, ';')
      .replace(/\\\\/g, '\\')
      .trim();
  }

  function firstString(...values: unknown[]): string {
    for (const value of values) {
      const normalized = stringValue(value);
      if (normalized) return normalized;
    }
    return '';
  }

  function slugFromDraft(): string {
    const source = draftFields.dn || activeRecord?.peerId || 'hosted-epm';
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
  <nav class="sdn-view-nav sdn-breadcrumb-tabs" aria-label="Identity sections">
    <button type="button" class:active={view === 'profile'} aria-current={view === 'profile' ? 'page' : undefined} on:click={() => setView('profile')}>Node Profile</button>
    <button type="button" class:active={view === 'hosted-epms'} aria-current={view === 'hosted-epms' ? 'page' : undefined} on:click={() => setView('hosted-epms')}>Local Users</button>
    <button type="button" class:active={view === 'keys-import'} aria-current={view === 'keys-import' ? 'page' : undefined} on:click={() => setView('keys-import')}>Keys / Import</button>
    <button type="button" class:active={view === 'security'} aria-current={view === 'security' ? 'page' : undefined} on:click={() => setView('security')}>Security</button>
    <button type="button" class:active={view === 'downloads'} aria-current={view === 'downloads' ? 'page' : undefined} on:click={() => setView('downloads')}>Downloads</button>
    <button type="button" class:active={view === 'settings'} aria-current={view === 'settings' ? 'page' : undefined} on:click={() => setView('settings')}>Settings</button>
  </nav>

  {#if view === 'profile'}
    <article class="sdn-card sdn-glass sdn-readable-panel sdn-profile-home">
      <div class="sdn-profile-summary">
        <div class="sdn-card-head">
          <div>
            <h2>{profileRecord.label || 'Space Data Network'}</h2>
          </div>
        </div>

        <dl class="sdn-details sdn-profile-details">
          <div><dt>Peer ID</dt><dd>{profileRecord.peerId || fallbackNodeIdentity}</dd></div>
          <div><dt>Agent</dt><dd>{summary?.agentVersion ?? stringValue(profileRecord.epmJson.agent_version) ?? 'pending'}</dd></div>
          <div><dt>Email</dt><dd>{stringValue(profileRecord.epmJson.email) ?? 'pending'}</dd></div>
          <div><dt>Entity</dt><dd>{stringValue(profileRecord.epmJson.entity_type) ?? 'node'}</dd></div>
          <div><dt>Signing public key</dt><dd>{identityPublicKeyValue(profileRecord.epmJson, 'signing') ?? 'pending'}</dd></div>
          <div><dt>Encryption public key</dt><dd>{identityPublicKeyValue(profileRecord.epmJson, 'encryption') ?? 'pending'}</dd></div>
        </dl>

        <div class="sdn-action-grid">
          <button class="sdn-button" type="button" on:click={() => setView('edit-profile')} disabled={nodeIdentityLocked || !backend}>Edit Profile</button>
        </div>
      </div>

      <div class="sdn-profile-qr">
        <div class="sdn-qr-frame" aria-label="Public vCard QR preview">
          {#if qrDataUrl}
            <img src={qrDataUrl} alt="Public vCard QR code" />
          {/if}
        </div>
        {#if qrState}
          <p class="sdn-status-line">{qrState}</p>
        {/if}
      </div>
    </article>
  {:else if view === 'edit-profile'}
    <article class="sdn-card sdn-glass sdn-readable-panel">
      <div class="sdn-card-head">
        <div>
          <h2>Edit Profile</h2>
        </div>
      </div>

      {#if selectedId === NEW_EPM_ID}
        <label class="sdn-field">
          <span>Local user ID</span>
          <input class="sdn-input" bind:value={draftId} placeholder="hosted-epm-id" autocomplete="off" />
        </label>
      {/if}

      <div class="sdn-profile-form">
        {#each PROFILE_FIELDS as field}
          <label class="sdn-field">
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

      <div class="sdn-toolbar sdn-section-toolbar">
        <button class="sdn-button" type="button" on:click={saveProfile} disabled={nodeIdentityLocked || !backend}>Save public profile</button>
        <button class="sdn-button sdn-button-muted" type="button" on:click={() => setView('profile')}>Cancel</button>
      </div>
      {#if saveState}
        <p class="sdn-status-line">{saveState}</p>
      {/if}
    </article>
  {:else if view === 'hosted-epms'}
    <article class="sdn-card sdn-glass sdn-readable-panel">
      <div class="sdn-card-head">
        <div>
          <h2>Local Users</h2>
        </div>
        <button class="sdn-button" type="button" on:click={startHostedEpm}>Add local user</button>
      </div>

      <div class="sdn-identity-list">
        {#each hostedEpms as record}
          <section class="sdn-identity-row" class:active={record.id === profileRecord.id}>
            <div class="sdn-identity-row-copy">
              <strong>{record.label || record.id}</strong>
              <span title={record.peerId || 'no peer id'} aria-label={record.peerId || 'no peer id'}>{shortPeerId(record.peerId || 'no peer id')}</span>
              {#if record.epmCid}
                <small title={record.epmCid} aria-label={record.epmCid}>{shortPeerId(record.epmCid)}</small>
              {/if}
            </div>
            <div class="sdn-actions-nowrap">
              <button class="sdn-button sdn-button-compact" type="button" on:click={() => selectRecord(record, 'profile')}>Profile</button>
              <button class="sdn-button sdn-button-compact" type="button" on:click={() => selectRecord(record, 'edit-profile')}>Edit</button>
              <button class="sdn-button sdn-button-compact" type="button" on:click={() => downloadHostedEpm(record, 'vcard')} disabled={!backend}>vCard</button>
              <button class="sdn-button sdn-button-compact sdn-button-muted" type="button" on:click={deleteHostedEpm} disabled={!backend || record.kind === 'node-self' || record.id !== activeRecord?.id}>Delete</button>
            </div>
          </section>
        {/each}
      </div>

      <div class="sdn-toolbar sdn-section-toolbar">
        <label class="sdn-file-button">
          Import EPM JSON/vCard
          <input type="file" accept=".json,.epm,.vcf,.vcard" on:change={importEpmFile} />
        </label>
      </div>
      {#if importState}
        <p class="sdn-status-line">{importState}</p>
      {/if}
    </article>
  {:else if view === 'keys-import'}
    <article class="sdn-card sdn-glass sdn-readable-panel">
      <div class="sdn-card-head">
        <div>
          <h2>Keys / Import</h2>
        </div>
        <button class="sdn-button" type="button" on:click={loadWallets} disabled={!backend}>Refresh</button>
      </div>

      <div class="sdn-key-method-grid">
        <section class="sdn-key-method">
          <h3>Deterministic Keygen</h3>
          <label class="sdn-field">
            <span>Username</span>
            <input class="sdn-input" bind:value={keygenUsername} autocomplete="username" />
          </label>
          <label class="sdn-field">
            <span>Password</span>
            <input class="sdn-input" type="password" bind:value={keygenPassword} autocomplete="new-password" />
          </label>
          <button class="sdn-button" type="button" on:click={() => openWalletFlow('deterministic')}>Create</button>
        </section>

        <section class="sdn-key-method">
          <h3>Import Passphrase</h3>
          <label class="sdn-field">
            <span>Passphrase</span>
            <textarea class="sdn-input sdn-secret-input" bind:value={passphraseInput}></textarea>
          </label>
          <button class="sdn-button" type="button" on:click={() => openWalletFlow('passphrase')}>Import</button>
        </section>

        <section class="sdn-key-method">
          <h3>Encrypted Private Key File</h3>
          <label class="sdn-file-button">
            Import encrypted key
            <input type="file" accept=".sdncore,.kmf,.enc,application/octet-stream" on:change={importEncryptedPrivateKeyFile} />
          </label>
          <button class="sdn-button sdn-button-muted" type="button" on:click={exportEncryptedCore} disabled={!backend}>Export Encrypted Core</button>
        </section>
      </div>

      <div class="sdn-wallet-host" bind:this={walletHost}></div>
      {#if walletState}
        <p class="sdn-status-line">{walletState}</p>
      {/if}
      {#if coreState}
        <p class="sdn-status-line">{coreState}</p>
      {/if}
    </article>
  {:else if view === 'security'}
    <article class="sdn-card sdn-glass sdn-readable-panel">
      <div class="sdn-card-head">
        <div>
          <h2>Security</h2>
        </div>
      </div>

      <div class="sdn-key-method-grid">
        <section class="sdn-key-method">
          <h3>Grant admin for public key</h3>
          <label class="sdn-field">
            <span>Public key</span>
            <input class="sdn-input" bind:value={grantPublicKey} autocomplete="off" />
          </label>
          <button class="sdn-button" type="button" on:click={() => grantAdminForPublicKey(grantPublicKey)} disabled={!backend}>Grant admin</button>
        </section>

        <section class="sdn-key-method">
          <h3>Upload EPM / .vcf</h3>
          <label class="sdn-file-button">
            Upload EPM / .vcf
            <input type="file" accept=".epm,.vcf,.vcard,application/octet-stream,text/vcard,text/x-vcard" on:change={grantAdminFromSecurityFile} />
          </label>
        </section>
      </div>
      {#if securityState}
        <p class="sdn-status-line">{securityState}</p>
      {/if}
    </article>
  {:else if view === 'downloads'}
    <article class="sdn-card sdn-glass sdn-readable-panel">
      <div class="sdn-card-head">
        <div>
          <h2>Downloads</h2>
          <p>{profileRecord.label || profileRecord.id}</p>
        </div>
      </div>

      <div class="sdn-toolbar sdn-section-toolbar">
        <button class="sdn-button" type="button" on:click={() => downloadHostedEpm(profileRecord, 'json')} disabled={!backend || !profileRecord}>Download JSON</button>
        <button class="sdn-button" type="button" on:click={() => downloadHostedEpm(profileRecord, 'epm')} disabled={!backend || !profileRecord}>Download EPM</button>
        <button class="sdn-button" type="button" on:click={() => downloadHostedEpm(profileRecord, 'vcard')} disabled={!backend || !profileRecord}>Download vCard</button>
      </div>
      {#if downloadState}
        <p class="sdn-status-line">{downloadState}</p>
      {/if}
      <pre class="sdn-public-json">{publicJson}</pre>
    </article>
  {:else if view === 'settings'}
    <article class="sdn-card sdn-glass sdn-readable-panel">
      <div class="sdn-card-head">
        <div>
          <h2>Settings</h2>
        </div>
      </div>

      <label class="sdn-field sdn-unlock-duration">
        <span>Unlock duration</span>
        <select class="sdn-input sdn-select" bind:value={unlockDurationValue}>
          <option value="900000">15 minutes</option>
          <option value="3600000">1 hour</option>
          <option value="28800000">8 hours</option>
          <option value="app">Until app closes</option>
        </select>
      </label>

      <label class="sdn-field sdn-flatbuffer-storage-location">
        <span>FlatBuffer data storage location</span>
        <div class="sdn-path-input-row">
          <input
            class="sdn-input"
            type="text"
            bind:value={flatbufferStoragePathValue}
            placeholder="Use default FlatBuffer data path"
            autocomplete="off"
          />
          <button class="sdn-button" type="button" on:click={browseFlatbufferStorageLocation} disabled={nodeIdentityLocked || !backend}>Browse</button>
          <button class="sdn-button sdn-button-muted" type="button" on:click={resetFlatbufferStorageLocation} disabled={nodeIdentityLocked}>Use default</button>
        </div>
      </label>

      <div class="sdn-toolbar sdn-section-toolbar">
        <button class="sdn-button" type="button" on:click={saveNodeIdentitySettings} disabled={nodeIdentityLocked || !backend}>Save settings</button>
      </div>
      {#if settingsState}
        <p class="sdn-status-line">{settingsState}</p>
      {/if}
    </article>
  {/if}
</section>

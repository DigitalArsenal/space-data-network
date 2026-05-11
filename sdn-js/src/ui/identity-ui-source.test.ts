import { existsSync, readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

const uiRoot = new URL('../../ui/src/', import.meta.url);
const runtimeRoot = new URL('./runtime/', import.meta.url);

function readUiSource(path: string): string {
  const fileUrl = new URL(path, uiRoot);
  expect(existsSync(fileUrl), `${path} should exist`).toBe(true);
  return readFileSync(fileUrl, 'utf8');
}

function readRuntimeSource(path: string): string {
  const fileUrl = new URL(path, runtimeRoot);
  expect(existsSync(fileUrl), `${path} should exist`).toBe(true);
  return readFileSync(fileUrl, 'utf8');
}

function expectSourceToContainAll(source: string, snippets: string[]): void {
  for (const snippet of snippets) {
    expect(source).toContain(snippet);
  }
}

describe('SDN identity Svelte source', () => {
  it('mounts the local users workspace from the node screen', () => {
    const source = readUiSource('screens/NodeScreen.svelte');

    expect(source).toContain("import IdentityPanel from '../components/IdentityPanel.svelte'");
    expect(source).toContain('<IdentityPanel');
    expect(source).not.toContain('MetricCard');
    expect(source).not.toContain('Runtime Mode');
    expect(source).not.toContain('Node Identity');
    expect(source).not.toContain('sdn-node-summary-grid');
    expect(source).not.toContain('<div class="sdn-grid');
    expect(source).not.toContain('<section class="sdn-panel-grid');
  });

  it('exposes a breadcrumb identity workflow instead of one crowded all-controls tab', () => {
    const source = readUiSource('components/IdentityPanel.svelte');

    expectSourceToContainAll(source, [
      "type IdentityView = 'profile' | 'edit-profile' | 'hosted-epms' | 'keys-import' | 'security' | 'downloads'",
      'sdn-breadcrumbs',
      'Identity /',
      'setView(',
      'Node Profile',
      'Edit Profile',
      'Local Users',
      'Keys / Import',
      'Security',
      'Downloads',
      'backend.saveHostedEpm',
      'backend.saveNodeProfile',
      'backend.importHostedEpm',
      'backend.downloadHostedEpm',
      'backend.saveNodeAccessUser',
      'createVCardQrPayload',
      'downloadHostedEpm(record.id,',
      '.json,.epm,.vcf,.vcard',
      'listWalletsAndEpms',
      'exportCore',
      'importCore',
      'Deterministic Keygen',
      'Import Passphrase',
      'Encrypted Private Key File',
      'Encrypted Core',
      'Grant admin',
      'Upload EPM / .vcf',
      'Public vCard QR',
    ]);
    expect(source).toContain('publicKeyEmailAddress');
    expect(source).toContain('spacedatanetwork.org');
    expect(source).toContain("profile: 'Node Profile'");
    expect(source).toContain('<button type="button" class:active={view === \'profile\'} on:click={() => setView(\'profile\')}>Node Profile</button>');
    expect(source).toContain("'hosted-epms': 'Local Users'");
    expect(source).toContain('<button type="button" class:active={view === \'hosted-epms\'} on:click={() => setView(\'hosted-epms\')}>Local Users</button>');
    expect(source).not.toContain('Hosted EPMs');
    expect(source).not.toMatch(/Claim EPM|Claim started|Claiming/i);

    for (const field of [
      'dn',
      'legal_name',
      'given_name',
      'family_name',
      'email',
      'telephone',
      'entity_type',
    ]) {
      expect(source).toContain(field);
    }

    expect(source).not.toContain("{ key: 'peer_id'");
    expect(source).not.toContain("{ key: 'epm_cid'");
    expect(source).not.toContain("{ key: 'public_key'");
    expect(source).not.toContain("{ key: 'signing_public_key'");
    expect(source).not.toContain("{ key: 'encryption_public_key'");
    expect(source).not.toContain("{ key: 'multiformat_address'");
  });

  it('does not render extraneous identity type tags', () => {
    const identitySource = readUiSource('components/IdentityPanel.svelte');
    const nodeSource = readUiSource('screens/NodeScreen.svelte');

    expect(identitySource).not.toContain('Node self profile');
    expect(identitySource).not.toContain('Hosted public EPM');
    expect(identitySource).not.toContain('Node self:');
    expect(identitySource).not.toContain('wallet EPM');
    expect(identitySource).not.toContain('<span class="sdn-chip"');
    expect(identitySource).not.toContain('public EPM</span>');
    expect(identitySource).not.toContain("{record.kind === 'node-self' ? 'Node self' : 'Hosted'}");
    expect(nodeSource).not.toContain('EPM Scope');
    expect(nodeSource).not.toContain('nodeSelfEpms');
    expect(nodeSource).not.toContain('hostedOnlyEpms');
  });

  it('keeps node security limited to admin public-key grant and EPM/vCard upload', () => {
    const source = readUiSource('components/IdentityPanel.svelte');
    const securityView = source.slice(source.indexOf("{:else if view === 'security'}"), source.indexOf("{:else if view === 'downloads'}"));

    expectSourceToContainAll(securityView, [
      'Grant admin for public key',
      'bind:value={grantPublicKey}',
      'grantAdminForPublicKey(grantPublicKey)',
      'Upload EPM / .vcf',
      'grantAdminFromSecurityFile',
      'accept=".epm,.vcf,.vcard,application/octet-stream,text/vcard,text/x-vcard"',
    ]);
    expect(securityView).not.toContain('Admin grants:');
    expect(securityView).not.toContain('Refresh');
    expect(securityView).not.toContain('Access grants');
    expect(securityView).not.toContain('Revoke admin');
    expect(securityView).not.toContain('Remove');
    expect(securityView).not.toContain('Config managed');
    expect(securityView).not.toContain('Name');
    expect(securityView).not.toContain('Signing public key');
  });

  it('renders EPM actions in peer rows and includes directory search', () => {
    const source = readUiSource('screens/PeersScreen.svelte');

    expect(source).toContain("import DirectorySearchPanel from '../components/DirectorySearchPanel.svelte'");
    expect(source).toContain('<DirectorySearchPanel');
    expect(source).toContain('<th><button type="button" on:click={() => setSort(\'name\')}>{sortablePeerHeader(\'name\', \'Name\')}</button></th>');
    expect(source).toContain('<th><button type="button" on:click={() => setSort(\'peerId\')}>{sortablePeerHeader(\'peerId\', \'PeerID\')}</button></th>');
    expect(source).toContain('<th><button type="button" on:click={() => setSort(\'trust\')}>{sortablePeerHeader(\'trust\', \'Trust\')}</button></th>');
    expect(source).toContain('<th><button type="button" on:click={() => setSort(\'ip\')}>{sortablePeerHeader(\'ip\', \'IP\')}</button></th>');
    expect(source).toContain('<th><button type="button" on:click={() => setSort(\'agent\')}>{sortablePeerHeader(\'agent\', \'Agent\')}</button></th>');
    expect(source).toContain('expandedPeerId');
    expect(source).toContain('togglePeer(peer)');
    expect(source).toContain('renderPeerQr');
    expect(source).toContain('peerMatchesQuery');
    expect(source).toContain('displayNameForPeer');
    expect(source).toContain('peerEmail');
    expect(source).toContain('peerPhone');
    expect(source).toContain('peerIp');
    expect(source).toContain('EPM Fields');
    expect(source).toContain('Public vCard QR');
    expect(source).toContain('Email');
    expect(source).toContain('Phone');
    expect(source).not.toContain('<th>EPM</th>');
    expect(source).not.toContain('downloadHostedEpm');
    expect(source).not.toContain('<th>Actions</th>');
  });

  it('queries nodes and people in the directory search panel', () => {
    const source = readUiSource('components/DirectorySearchPanel.svelte');

    expect(source).toContain('backend.searchDirectory');
    expect(source).toContain('directoryKind ===');
    expect(source).toContain('Nodes');
    expect(source).toContain('People');
    expect(source).toContain('downloadHostedEpm');
    expect(source).toContain('Show QR');
  });

  it('does not offer plaintext Core export controls in public identity UI copy', () => {
    const sources = [
      readUiSource('components/IdentityPanel.svelte'),
      readUiSource('components/DirectorySearchPanel.svelte'),
      readUiSource('screens/NodeScreen.svelte'),
      readUiSource('screens/PeersScreen.svelte'),
    ].join('\n');

    expect(sources).not.toMatch(/plaintext\s+core/i);
    expect(sources).not.toMatch(/plain\s+text\s+core/i);
  });
});

describe('SDN data Svelte source', () => {
  it('exposes a simple standards data table surface', () => {
    const source = readUiSource('screens/LocalDataScreen.svelte');

    expectSourceToContainAll(source, [
      'activeBackend.getDataSummary',
      'activeBackend.queryRawData',
      'backendForSelectedDataSource',
      'standardIdFromSchema',
      'schemaNameForStandardId',
      'runWorkbenchQuery',
      'sortableHeader',
      'filteredRows',
      'visibleRows',
      'visibleColumns',
      'STANDARD_FIELD_COLUMNS',
      'EPM_STANDARD_COLUMNS',
      'OMM_STANDARD_COLUMNS',
      'decodeWorkbenchRecord',
      'decodeEpmFlatBuffer',
      'decodeOmmFlatBuffer',
      'columnMenuOpen',
      'toggleColumn',
      'Columns',
      'sortColumn',
      'sortDirection',
      'Data source',
      'Data sources',
      'Table',
      'SQL',
      'Run SQL',
      'createWorkerLocalFlatSqlStore',
      'getRemoteDataSummary',
      'queryRemotePage',
      'syncSchema',
      'backendConfigForDataSource',
      'clearLocalFlatSqlStore',
      'ingestDownloadedRecords',
      'confirmResetLocalData',
      'Reset local cache',
      'Type RESET to clear',
      'INTERNAL_SQL_COLUMN_KEYS',
      'Page size',
      'Previous',
      'Next',
      'Message',
      'CID',
      'Peer',
      'Producer',
      'Source',
      'Batch',
      'Display name',
      'Legal name',
      'Email',
      'Entity type',
      'Object name',
      'Object ID',
      'NORAD catalog ID',
      'OBJECT_NAME',
      'NORAD_CAT_ID',
      'dataBytes',
      'schemaName',
      'providerId',
      'sourceName',
    ]);
    expect(source).not.toContain("label: 'Bytes'");
    expect(source).not.toContain('dataBase64');
    expect(source).not.toContain('SQL Workbench');
    expect(source).not.toContain('backend ready');
    expect(source).not.toContain('Refresh');
    expect(source).not.toContain('admin_proxy_path');
    expect(source).not.toContain('serverUrl');
    expect(source).not.toContain('createLocalFlatSqlStore');
    expect(source).not.toContain('createLibp2pFlatSqlSyncBackend');
    expect(source).not.toContain('await activeBackend.streamRawData({\\n          schema: scan.schema');
    expect(source).not.toContain('Source browser');
    expect(source).not.toContain('Query builder');
    expect(source).not.toContain('Record inspector');
    expect(source).not.toContain('Selected record');
    expect(source).not.toContain('Stored objects');
    expect(source).not.toContain('Object inspector');
    expect(source).not.toContain('Read raw');
    expect(source).not.toContain('Test EPM query');
    expect(source).not.toContain("label: 'Payload'");
    expect(source).not.toContain('PAYLOAD');
    expect(source).not.toContain('{pageLabel} / {filteredRows.length} visible / {selectedSchemaName}');
    expect(source).not.toContain('>EPM.fbs<');
    expect(source).not.toContain('Standards type');
    expect(source).toContain("if (!value) return '';");
    expect(source).not.toContain("if (!value) return 'pending';");
  });
});

describe('SDN data worker source', () => {
  it('keeps remote sync and FlatSQL ingest off the renderer thread', () => {
    const workerSource = readRuntimeSource('local-flatsql.worker.ts');
    const clientSource = readRuntimeSource('local-flatsql-worker-client.ts');

    expectSourceToContainAll(workerSource, [
      'createLibp2pFlatSqlSyncBackend',
      'queryRemotePage',
      'syncSchemaInWorker',
      'currentStore.ingestRecords',
      'prepareRecordsForTransfer',
      'workerGlobal.postMessage(response, transferables)',
    ]);
    expectSourceToContainAll(clientSource, [
      "new Worker(new URL('./local-flatsql.worker.ts', import.meta.url), { type: 'module' })",
      'syncProgressHandlers',
      'queryRemotePage',
      'syncSchema',
    ]);
  });

  it('registers a browser COI service worker for SharedArrayBuffer-capable static hosting', () => {
    const mainSource = readUiSource('main.ts');
    const coiSource = readUiSource('lib/cross-origin-isolation.ts');
    const serviceWorkerSource = readUiSource('lib/coi-serviceworker.js');

    expect(mainSource).toContain('ensureCrossOriginIsolation');
    expectSourceToContainAll(coiSource, [
      'coi-serviceworker.js',
      'globalThis.crossOriginIsolated',
      'navigator.serviceWorker.register',
      'window.location.reload',
    ]);
    expectSourceToContainAll(serviceWorkerSource, [
      "headers.set('Cross-Origin-Opener-Policy', 'same-origin')",
      "headers.set('Cross-Origin-Embedder-Policy', 'require-corp')",
      "headers.set('Cross-Origin-Resource-Policy', 'same-origin')",
    ]);
  });
});

describe('SDN identity styling guardrails', () => {
  it('uses darkest black and subtle non-blob texture tokens', () => {
    const tokens = readUiSource('styles/tokens.css');
    const appCss = readUiSource('styles/app.css');

    expect(tokens).toMatch(/--sdn-bg:\s*#000000\b/i);
    expect(appCss).toContain('repeating-linear-gradient');
    expect(appCss).toContain('repeating-radial-gradient');
    expect(appCss).not.toMatch(/\bblob\b|\borb\b/i);
  });

  it('uses liquid glass panels without pill-shaped buttons', () => {
    const appCss = readUiSource('styles/app.css');

    expect(appCss).toMatch(/\.sdn-glass\s*{[^}]*backdrop-filter:\s*blur\([^)]*\)\s*saturate\([^)]*\)/s);
    expect(appCss).toMatch(/\.sdn-button\s*{[^}]*border-radius:\s*var\(--sdn-radius-sm\)/s);
    expect(appCss).not.toMatch(/border-radius:\s*(999|1000|50%)/);
  });

  it('keeps edit-profile action buttons visually separated from form fields', () => {
    const appCss = readUiSource('styles/app.css');

    expect(appCss).toMatch(/\.sdn-profile-form\s*\+\s*\.sdn-toolbar\s*{[^}]*margin-top:\s*1\.1rem/s);
    expect(appCss).toMatch(/\.sdn-identity-list\s*\+\s*\.sdn-toolbar\s*{[^}]*margin-top:\s*1rem/s);
    expect(appCss).toMatch(/\.sdn-section-toolbar\s*{[^}]*margin-top:\s*1rem/s);
  });

  it('does not show idle or success-only identity status copy', () => {
    const identitySource = readUiSource('components/IdentityPanel.svelte');
    const directorySource = readUiSource('components/DirectorySearchPanel.svelte');
    const peersSource = readUiSource('screens/PeersScreen.svelte');
    const sources = [identitySource, directorySource, peersSource].join('\n');

    expect(identitySource).toContain('class="sdn-toolbar sdn-section-toolbar"');
    expect(identitySource).toContain('{#if qrState}');
    expect(identitySource).toContain('{#if saveState}');
    expect(identitySource).toContain('{#if importState}');
    expect(identitySource).toContain('{#if walletState}');
    expect(identitySource).toContain('{#if coreState}');
    expect(identitySource).toContain('{#if securityState}');
    expect(identitySource).toContain('{#if downloadState}');
    expect(directorySource).toContain('{#if searchState}');
    expect(peersSource).toContain('{#if peerQrState}');
    for (const copy of [
      'Profile ready.',
      'Ready.',
      'Public vCard QR ready.',
      'Public directory vCard QR ready.',
      'QR pending',
      'New profile.',
      'Saved.',
      'Deleted.',
      'Imported.',
      'download started.',
      'Search public directory records for nodes and people.',
      'Public vCard QR contains public EPM fields and public keys only.',
      'Loaded ${walletRecords.length}.',
      'surface opened.',
      'Export started.',
      'Import accepted.',
      'Admin granted.',
    ]) {
      expect(sources).not.toContain(copy);
    }
  });

  it('keeps peer actions on one line and centers select arrows', () => {
    const appCss = readUiSource('styles/app.css');

    expect(appCss).toMatch(/\.sdn-actions-nowrap\s*{[^}]*white-space:\s*nowrap/s);
    expect(appCss).toMatch(/\.sdn-select\s*{[^}]*background-position:[^;]*center/s);
  });

  it('keeps data workbench columns horizontally scrollable with compact cells', () => {
    const appCss = readUiSource('styles/app.css');

    expect(appCss).toMatch(/\.sdn-workbench-table-wrap\s*{[^}]*overflow-x:\s*auto/s);
    expect(appCss).toMatch(/\.sdn-workbench-table\s*{[^}]*min-width:\s*max-content/s);
    expect(appCss).toMatch(/\.sdn-workbench-table th,\s*\.sdn-workbench-table td\s*{[^}]*min-width:\s*50px/s);
  });

  it('does not present identity as claimed or show Core claim controls', () => {
    const sources = [
      readUiSource('App.svelte'),
      readUiSource('components/AppShell.svelte'),
      readUiSource('components/TopStatusBar.svelte'),
      readUiSource('screens/NodeScreen.svelte'),
      readUiSource('components/IdentityPanel.svelte'),
      readUiSource('lib/routes.ts'),
    ].join('\n');

    expect(sources).not.toMatch(/claimed/i);
    expect(sources).not.toMatch(/Claim Core|Claim EPM|claim-core/i);
    expect(sources).not.toContain('label="Core"');
  });
});

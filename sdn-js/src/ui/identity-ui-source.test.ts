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

  it('renders Peers as the data-source storefront with PGP ownertrust', () => {
    const source = readUiSource('screens/PeersScreen.svelte');

    expect(source).toContain("import DirectorySearchPanel from '../components/DirectorySearchPanel.svelte'");
    expect(source).toContain("from '../../../src/ui/runtime/data-directory'");
    expect(source).toContain('<DirectorySearchPanel');
    expect(source).toContain("type PeerView = 'home' | 'observed' | 'feeds' | 'peer-detail'");
    expect(source).toContain('PGP_OWNERTRUST_LEVELS');
    expect(source).toContain('DEFAULT_OWNERTRUST');
    expect(source).toContain('ownertrustForPeer');
    expect(source).toContain('isTrustedDirectoryOwnertrust');
    expect(source).toContain('subscribeToDataFeed');
    expect(source).toContain('upsertDataFeedSubscription');
    expect(source).toContain('persistDataDirectoryState');
    expect(source).toContain('Trust key');
    expect(source).toContain('unknown');
    expect(source).toContain('never');
    expect(source).toContain('marginal');
    expect(source).toContain('full');
    expect(source).toContain('ultimate');
    expect(source).toContain('Observed Peers');
    expect(source).toContain('Data Feeds');
    expect(source).toContain('Directory Search');
    expect(source).toContain('Available Data');
    expect(source).toContain('Available Modules');
    expect(source).toContain('<button class="sdn-storefront-stat" type="button" on:click={() => setPeerView(\'observed\')}>');
    expect(source).toContain('<button class="sdn-storefront-stat" type="button" on:click={() => setPeerView(\'feeds\')}>');
    expect(source).toContain('<th><button type="button" on:click={() => setSort(\'name\')}>{sortablePeerHeader(\'name\', \'Name\')}</button></th>');
    expect(source).toContain('<th><button type="button" on:click={() => setSort(\'peerId\')}>{sortablePeerHeader(\'peerId\', \'PeerID\')}</button></th>');
    expect(source).toContain('<th><button type="button" on:click={() => setSort(\'trust\')}>{sortablePeerHeader(\'trust\', \'Ownertrust\')}</button></th>');
    expect(source).toContain('<th><button type="button" on:click={() => setSort(\'ip\')}>{sortablePeerHeader(\'ip\', \'IP\')}</button></th>');
    expect(source).toContain('<th><button type="button" on:click={() => setSort(\'agent\')}>{sortablePeerHeader(\'agent\', \'Agent\')}</button></th>');
    expect(source).toContain('selectedPeerId');
    expect(source).toContain('showPeerDetail(peer)');
    expect(source).toContain('renderPeerQr');
    expect(source).toContain('peerMatchesQuery');
    expect(source).toContain('displayNameForPeer');
    expect(source).toContain('peerEmail');
    expect(source).toContain('peerPhone');
    expect(source).toContain('peerIp');
    expect(source).toContain('EPM Fields');
    expect(source).toContain('Email');
    expect(source).toContain('Phone');
    expect(source).not.toContain("import MetricCard from '../components/cards/MetricCard.svelte'");
    expect(source).not.toContain('MetricCard');
    expect(source).not.toContain('Mission Loadout');
    expect(source).not.toContain('Mission Builder');
    expect(source).not.toContain('Marketplace feed adapter pending');
    expect(source).not.toContain('<section class="sdn-panel-grid">');
    expect(source).not.toContain('sdn-grid-3');
    expect(source).not.toContain('<th>EPM</th>');
    expect(source).not.toContain('downloadHostedEpm');
    expect(source).not.toContain('<th>Actions</th>');
  });

  it('renders debounced unified directory search with upload search and configured nodes', () => {
    const source = readUiSource('components/DirectorySearchPanel.svelte');

    expectSourceToContainAll(source, [
      'const DIRECTORY_PAGE_SIZE = 10;',
      'const SEARCH_DEBOUNCE_MS = 250;',
      'loadConfiguredDirectoryNodes',
      "/api/local/sdn-nodes",
      'normalizeConfiguredDirectoryNodes',
      'backend.searchDirectory',
      'scheduleDirectorySearch',
      'handleUploadSearch',
      'decodeEpmFlatBuffer',
      'sortableDirectoryHeader',
      'aria-label="Directory results"',
      'Type',
      'Name',
      'PeerID',
      'Public key',
      'EPM',
      'vCard',
      'Show QR',
      'Previous',
      'Next',
    ]);
    expect(source).toContain("on:input={handleSearchInput}");
    expect(source).toContain("accept=\".epm,application/octet-stream\"");
    expect(source).toContain("accept=\".vcf,.vcard,text/vcard,text/x-vcard\"");
    expect(source).not.toContain('<form');
    expect(source).not.toContain('type="submit"');
    expect(source).not.toContain('>Nodes<');
    expect(source).not.toContain('>People<');
    expect(source).not.toContain('No node results');
    expect(source).not.toContain('No people results');
    expect(source).not.toContain('pending');
    expect(source).not.toContain('Directory record has no EPM or peer identifier to download.');
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
  it('exposes subscribed local data sync and query controls without source-storefront clutter', () => {
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
      'Table',
      'SQL',
      'Run SQL',
      'createWorkerLocalFlatSqlStore',
      'getRemoteDataSummary',
      'queryRemotePage',
      'syncSchema',
      'downloadSpeedBytesPerSecond',
      'formatBytesPerSecond',
      'boundedWireSpeedUtilization(schema.progress.wireSpeedUtilization)',
      'Download',
      'backendConfigForDataSource',
      'clearLocalFlatSqlStore',
      'ingestDownloadedRecords',
      'loadDataDirectoryState',
      'migrateSchemaSyncPreferencesToDataDirectory',
      'updateDataFeedSubscription',
      'persistDataDirectoryState',
      'schemaSyncRows = buildSubscribedSchemaSyncRows',
      'syncSelectedStandardWithSubscriptions(schemaSyncRows)',
      'scheduleSubscribedSchemaSyncs(schemaSyncRows)',
      'beginResetSubscriptionData',
      'confirmResetSubscriptionData',
      'Reset row',
      'Type RESET to clear',
      'Next sync attempt',
      'nextSyncAttemptLabel',
      'retrySubscriptionSync',
      '>Retry</button>',
      'schema.progress.error',
      'Sync filter',
      'handleSubscriptionFilterInput',
      'handleSubscriptionStorageCapInput',
      'handleSubscriptionStorageUnitChange',
      "type DataSection = 'storage' | 'subscriptions' | 'explorer'",
      'DATA_SECTIONS',
      "let selectedDataSection: DataSection = 'storage'",
      'setDataSection',
      'activeStorageRows',
      'selectedSchemaSyncRow',
      'refreshSubscriptionRemoteRowsFromSummary',
      'selectedSchemaSyncRow?.remoteRows',
      'Data / Storage',
      'Data / Sync Settings',
      'Data / Explorer',
      'Sync settings',
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
    expect(source).not.toContain("type DataSubview = 'storage' | 'subscriptions' | 'explorer'");
    expect(source).not.toContain('DATA_SUBVIEWS');
    expect(source).not.toContain('selectedDataSubview');
    expect(source).not.toContain('selectDataSubview');
    expect(source).not.toContain('sdn-source-browser');
    expect(source).not.toContain('Data source</span>');
    expect(source).not.toContain('Data sources');
    expect(source).not.toContain('Combined Storage / Subscriptions');
    expect(source).not.toContain('aria-label="Sync subscriptions"');
    expect(source).not.toContain('<th>Provider</th>');
    expect(source).not.toContain('Reset local cache');
    expect(source).not.toContain('confirmResetLocalData');
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
    expect(source).not.toContain('>Query</button>');
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
    const libp2pSource = readRuntimeSource('sdn-backend-libp2p-sync.ts');

    expectSourceToContainAll(workerSource, [
      'Libp2pFlatSqlSyncBackendCache',
      'withRemoteSyncTimeout',
      'downloadSpeedBytesPerSecond',
      'downloadedBytes',
      'queryRemotePage',
      'syncSchemaInWorker',
      "'Remote page chunk'",
      'currentStore.ingestFlatBufferStream',
      'timedFlatBufferStreamFromPublishedFlatSqlSegment',
      'PUBLISHED_MANIFEST_SYNC_CHUNK_SIZE',
      'PUBLISHED_SHARD_FETCH_CONCURRENCY',
      'fetchPublishedSegmentsInOrder',
      'recordsSincePersist >= options.request.persistRecordInterval',
      'downloadedBytes += streamBytes.byteLength',
      'downloadedBytes += chunk.recordStream.byteLength',
      'const resumeRecordOffset = Math.max(0, localRows - cumulativeRows);',
      'skipRecords: resumeRecordOffset',
      'recordKeyPrefix: `published:${segment.cid}`',
      'prepareRecordsForTransfer',
      'workerGlobal.postMessage(response, transferables)',
    ]);
    expect(workerSource).not.toContain('Remote page scan');
    expect(workerSource).not.toContain('Remote page stream');
    expectSourceToContainAll(clientSource, [
      "new Worker(new URL('./local-flatsql.worker.ts', import.meta.url), { type: 'module' })",
      'syncProgressHandlers',
      'queryRemotePage',
      'syncSchema',
    ]);
    expectSourceToContainAll(libp2pSource, [
      'requestFlatSqlSyncChunk',
      'createLibp2p',
      'exchangeFlatSqlSyncStream',
    ]);
    expect(libp2pSource).not.toContain("import('../../node')");
    expect(libp2pSource).not.toContain('SDNNode.create');
  });

  it('continues offset-based schema sync when a chunk has no cursor', () => {
    const workerSource = readRuntimeSource('local-flatsql.worker.ts');

    expect(workerSource).toContain('if (offset >= totalRows || scan.results.length === 0) break;');
    expect(workerSource).not.toContain('if (!nextCursor || offset >= totalRows || scan.results.length === 0) break;');
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

  it('keeps subscribed storage rows free of noisy mini pie backgrounds', () => {
    const localDataSource = readUiSource('screens/LocalDataScreen.svelte');
    const appCss = readUiSource('styles/app.css');

    expect(localDataSource).not.toContain('syncPieStyle');
    expect(localDataSource).not.toContain('sdn-sync-pie');
    expect(appCss).not.toContain('.sdn-sync-pie');
    expect(appCss).not.toContain('conic-gradient');
    expect(appCss).not.toContain('clip-path: circle');
  });

  it('keeps the app shell fixed while content panes own scrolling', () => {
    const appCss = readUiSource('styles/app.css');

    expect(appCss).toMatch(/html,\s*body,\s*#root\s*{[^}]*height:\s*100%[^}]*overflow:\s*hidden/s);
    expect(appCss).toMatch(/\.sdn-app\s*{[^}]*height:\s*100vh[^}]*overflow:\s*hidden/s);
    expect(appCss).toMatch(/\.sdn-main\s*{[^}]*grid-template-rows:\s*auto minmax\(0,\s*1fr\)[^}]*overflow:\s*hidden/s);
    expect(appCss).toMatch(/\.sdn-content\s*{[^}]*overflow:\s*auto/s);
    expect(appCss).toMatch(/\.sdn-workbench-main,\s*\.sdn-source-browser\s*{[^}]*align-content:\s*start/s);
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

import { existsSync, readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

const uiRoot = new URL('../../ui/src/', import.meta.url);
const runtimeRoot = new URL('./runtime/', import.meta.url);
const scriptsRoot = new URL('../../scripts/', import.meta.url);

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

function readScriptSource(path: string): string {
  const fileUrl = new URL(path, scriptsRoot);
  expect(existsSync(fileUrl), `${path} should exist`).toBe(true);
  return readFileSync(fileUrl, 'utf8');
}

function expectSourceToContainAll(source: string, snippets: string[]): void {
  for (const snippet of snippets) {
    expect(source).toContain(snippet);
  }
}

describe('SDN identity Svelte source', () => {
  it('wires wallet-gated node identity through the hd-wallet-ui login callback', () => {
    const source = readUiSource('lib/node-identity-session.ts');
    const walletRuntimeSource = readRuntimeSource('wallet-ui.ts');

    expectSourceToContainAll(source, [
      'export function createNodeIdentitySessionController',
      'sessionExpiresAt',
      'setTimeout',
      'confirmNodeIdentityReplacement',
      'logoutNodeIdentity',
      'onLogin',
      'openAccountAfterLogin: false',
      'applyWalletNodeIdentity',
      'signaturePayload',
      'bytesToHex',
    ]);
    expect(source).not.toContain('wallet-active-select');
    expect(source).not.toContain('querySelector');
    expect(source).not.toContain('getElementById');
    expect(walletRuntimeSource).not.toContain('sdn-wallet-shell');
    expect(walletRuntimeSource).not.toContain('data-wallet-action');
    expect(walletRuntimeSource).not.toContain('Open Login');
    expect(walletRuntimeSource).not.toContain('Open Account');
  });

  it('renders a locked node identity gate, logout confirmation, and unlock duration settings', () => {
    const appSource = readUiSource('App.svelte');
    const nodeSource = readUiSource('screens/NodeScreen.svelte');
    const topBarSource = readUiSource('components/TopStatusBar.svelte');
    const identitySource = readUiSource('components/IdentityPanel.svelte');

    expectSourceToContainAll(appSource, [
      'createNodeIdentitySessionController',
      'let nodeIdentityReady = false;',
      'let nodeIdentityLocked = true;',
      'let logoutConfirmOpen = false;',
      'nodeIdentitySession.loadSettings().finally',
      'nodeIdentityReady = true;',
      'confirmNodeIdentityReplacement',
      'nodeIdentitySession.logout()',
      "window.location.hash = '#/node'",
      'nodeIdentityLoginPromptKey += 1;',
      'Are you sure you want to log out?',
    ]);
    expectSourceToContainAll(nodeSource, [
      "import NodeIdentityGate from '../components/NodeIdentityGate.svelte'",
      '<NodeIdentityGate',
      '!nodeIdentityReady',
      'nodeIdentityLocked',
      'nodeIdentityLoginPromptKey',
      'onUnlock',
    ]);
    const gateSource = readUiSource('components/NodeIdentityGate.svelte');
    expect(gateSource).toContain('await controller.openLogin()');
    expect(gateSource).toContain('loginPromptKey');
    expect(gateSource).toContain('lastLoginPromptKey');
    expect(gateSource).not.toContain('<article');
    expect(gateSource).not.toContain('Unlock Node');
    expect(gateSource).not.toContain('>Login</button>');
    expectSourceToContainAll(topBarSource, [
      'Logout',
      'nodeIdentityLocked',
      'logoutClick',
    ]);
    expectSourceToContainAll(identitySource, [
      "type IdentityView = 'profile' | 'edit-profile' | 'hosted-epms' | 'keys-import' | 'security' | 'downloads' | 'settings'",
      'Settings',
      'Unlock duration',
      'FlatBuffer data storage location',
      'Use default',
      'resetFlatbufferStorageLocation',
      'flatbufferStoragePathValue',
      'selectFlatbufferStorageLocation',
      'browseFlatbufferStorageLocation',
      'saveNodeIdentitySettings',
      'nodeIdentityLocked',
      'disabled={nodeIdentityLocked || !backend}',
    ]);
  });

  it('mounts the local users workspace from the node screen', () => {
    const source = readUiSource('screens/NodeScreen.svelte');

    expect(source).toContain("import IdentityPanel from '../components/IdentityPanel.svelte'");
    expect(source).toContain('<IdentityPanel');
    expect(source).not.toContain('AdvancedDrawer');
    expect(source).not.toContain('advancedOpen');
    expect(source).not.toContain('Kubo Diagnostics');
    expect(source).not.toMatch(/>\s*Advanced\s*<\/button>/);
    expect(source).not.toContain('MetricCard');
    expect(source).not.toContain('Runtime Mode');
    expect(source).not.toContain('Node Identity');
    expect(source).not.toContain('sdn-node-summary-grid');
    expect(source).not.toContain('<div class="sdn-grid');
    expect(source).not.toContain('<section class="sdn-panel-grid');
  });

  it('exposes a breadcrumb identity workflow instead of one crowded all-controls tab', () => {
    const source = readUiSource('components/IdentityPanel.svelte');
    const appCss = readUiSource('styles/app.css');

    expectSourceToContainAll(source, [
      "type IdentityView = 'profile' | 'edit-profile' | 'hosted-epms' | 'keys-import' | 'security' | 'downloads' | 'settings'",
      'sdn-breadcrumb-tabs',
      'setView(',
      'Node Profile',
      'Edit Profile',
      'Local Users',
      'Keys / Import',
      'Security',
      'Downloads',
      'Settings',
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
    expect(source).toContain('<nav class="sdn-view-nav sdn-breadcrumb-tabs" aria-label="Identity sections">');
    expect(source).toContain('<button type="button" class:active={view === \'profile\'} aria-current={view === \'profile\' ? \'page\' : undefined} on:click={() => setView(\'profile\')}>Node Profile</button>');
    expect(source).toContain('<button type="button" class:active={view === \'hosted-epms\'} aria-current={view === \'hosted-epms\' ? \'page\' : undefined} on:click={() => setView(\'hosted-epms\')}>Local Users</button>');
    expect(source).not.toContain('Identity /');
    expect(source).not.toContain('aria-label="Identity breadcrumbs"');
    expect(appCss).toMatch(/\.sdn-breadcrumb-tabs\s*{[^}]*border:\s*0/s);
    expect(appCss).toMatch(/\.sdn-breadcrumb-tabs button\.active\s*{[^}]*color:\s*var\(--sdn-blue\)/s);
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

  it('keeps only the edit action on the node profile card', () => {
    const source = readUiSource('components/IdentityPanel.svelte');
    const profileView = source.slice(source.indexOf("{#if view === 'profile'}"), source.indexOf("{:else if view === 'edit-profile'}"));

    expect(profileView).toContain('<button class="sdn-button" type="button" on:click={() => setView(\'edit-profile\')} disabled={nodeIdentityLocked || !backend}>Edit Profile</button>');
    expect(profileView).not.toContain("setView('hosted-epms')");
    expect(profileView).not.toContain("setView('keys-import')");
    expect(profileView).not.toContain("setView('security')");
    expect(profileView).not.toContain("setView('downloads')");
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
      'DATA_QUERY_PROFILES',
      'standardIdFromSchema',
      'schemaNameForStandardId',
      'runWorkbenchQuery',
      'subscribedSourceOptions',
      'subscribedStandardOptions',
      'handleExplorerSourceChange',
      'handleExplorerStandardChange',
      'explorerSearchMode',
      'handleExplorerSearchInput',
      'handleExplorerSearchSubmit',
      'localExplorerResult',
      'localExplorerFilteredTotalRows',
      'localExplorerDatasetQueryActive',
      'explorerPageTotalRows = explorerSearchMode === \'plain\' && (localExplorerResult || localExplorerDatasetQueryActive) ? localExplorerTotalRows : estimatedTotalRows',
      'runLocalExplorerQuery',
      'scheduleLocalExplorerQuery',
      'buildLocalDataExplorerQuery',
      'localDataExplorerSearchColumns',
      'localDataExplorerCountFromResult',
      'withUiTimeout',
      'columnFilters',
      'handleColumnFilterInput',
      'columnFilterPlaceholder',
      'isNumericDataExplorerColumn',
      'sortableHeader',
      'filteredRows',
      'visibleRows',
      'visibleColumns',
      'STANDARD_FIELD_COLUMNS',
      'EPM_STANDARD_COLUMNS',
      'OMM_STANDARD_COLUMNS',
      'decodeWorkbenchRecord',
      'decodeCatFlatBuffer',
      'decodeEpmFlatBuffer',
      'decodeOmmFlatBuffer',
      'sortColumn',
      'sortDirection',
      'Source',
      'Data type',
      'Master search',
      'Plaintext',
      'SQL',
      'createWorkerLocalFlatSqlStore',
      'getRemoteDataSummary',
      'queryRemotePage',
      'queryProfile: subscriptionQueryProfileFor(activeSelection)',
      'syncSchema',
      'downloadSpeedBytesPerSecond',
      'formatBytesPerSecond',
      'boundedWireSpeedUtilization(nextProgress.wireSpeedUtilization)',
      'Download',
      'backendConfigForDataSource',
      'publishedShardSourcesForDataSource',
      'publishedShardSources:',
      'const sourceName = feedIdentity?.sourceName ?? null',
      'isFlatSqlSyncTransportAddr',
      "'/webrtc-direct/'",
      'clearLocalFlatSqlStore',
      'ingestDownloadedRecords',
      'loadDataDirectoryState',
      'migrateSchemaSyncPreferencesToDataDirectory',
      'updateDataFeedSubscription',
      'persistDataDirectoryState',
      'schemaSyncRows = buildSubscribedSchemaSyncRows',
      'publishedSnapshotTotalRows',
      'const rowCountRemoteRows = publishedSnapshotTotalRows ?? remoteRows',
      'remoteRowsForSchemaSyncRow(subscription, progress)',
      'schemaRowsCountLabel(schema)',
      'schemaPinnedRowsLabel(schema)',
      'schemaCachedBytesLabel(schema)',
      'schemaProgressLabel(schema)',
      'standardOptionLabel(standard)',
      'syncSelectedStandardWithSubscriptions(schemaSyncRows)',
      'scheduleSubscribedSchemaSyncs(schemaSyncRows)',
      'beginResetSubscriptionData',
      'confirmResetSubscriptionData',
      'Reset row',
      'Type RESET to clear',
      'Next {nextSyncAttemptLabel(schema)}',
      'nextSyncAttemptLabel',
      'retrySubscriptionSync',
      'aria-label={`${schema.id} retry sync`}',
      '>Retry</button>',
      'schema.progress.error',
      'Sync filter',
      'Sync profile',
      'handleSubscriptionQueryProfileChange',
      'handleSubscriptionFilterInput',
      'handleSubscriptionStorageCapInput',
      'handleSubscriptionStorageUnitChange',
      'pinVerifyToast',
      'showPinVerifyToast',
      'dismissPinVerifyToast',
      'aria-label="Dismiss verification toast"',
      "type DataSection = 'overview' | 'catalog' | 'subscriptions' | 'sources' | 'message-types' | 'storage' | 'billing' | 'activity' | 'explorer'",
      'buildDataCatalogRows',
      'buildDataOverviewVisuals',
      'overviewStorageGroup',
      'STORAGE_GROUP_OPTIONS',
      'Storage by',
      'summarizeDataCatalog',
      'DATA_SECTIONS',
      "let selectedDataSection: DataSection = 'overview'",
      'setDataSection',
      'activeStorageRows',
      'selectedSchemaSyncRow',
      'refreshSubscriptionRemoteRowsFromSummary',
      'selectedSchemaSyncRow?.remoteRows',
      'Data / Overview',
      'Data / Catalog',
      'Data / My Subscriptions',
      'Data / Sources',
      'Data / Message Types',
      'Data / Storage',
      'Data / Billing',
      'Data / Activity',
      'Data / Explorer',
      'My Subscriptions',
      'INTERNAL_SQL_COLUMN_KEYS',
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
      'CAT_STANDARD_COLUMNS',
      'Period (min)',
      'RCS (m^2)',
      'Size (m)',
      'Mass (kg)',
      'SQL_COLUMN_LABELS',
      'OPTIONAL_DEFAULT_VALUE_COLUMNS',
      'OBJECT_NAME',
      'NORAD_CAT_ID',
      'dataBytes',
      'schemaName',
      'providerId',
      'sourceName',
    ]);
    expect(source).not.toContain('gatewayUrl: localGatewayUrl()');
    expect(source).not.toContain('ipfsApiUrl: localKuboApiUrl()');
    expect(source).not.toContain('artifactPeerAddrsForDataSource(source)');
    const appSource = readUiSource('App.svelte');
    expectSourceToContainAll(appSource, [
      'let trustedPeers',
      'backend.listTrustedPeers()',
      '{trustedPeers}',
    ]);
    expect(source).not.toContain("type DataSubview = 'storage' | 'subscriptions' | 'explorer'");
    expect(source).not.toContain('DATA_SUBVIEWS');
    expect(source).not.toContain('selectedDataSubview');
    expect(source).not.toContain('selectDataSubview');
    expect(source).not.toContain('sdn-source-browser');
    expect(source).not.toContain('Data source</span>');
    expect(source).not.toContain('Combined Storage / Subscriptions');
    expect(source).not.toContain('aria-label="Sync subscriptions"');
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
    expect(source).not.toContain('filterSqlRecordsByText(filterSqlRecordsByColumns(localExplorerRecords');
    expect(source).not.toContain('handleQueryProfileChange');
    expect(source).not.toContain('Query profile</span>');
    expect(source).not.toContain("label: 'Payload'");
    expect(source).not.toContain('PAYLOAD');
    expect(source).not.toContain('{pageLabel} / {filteredRows.length} visible / {selectedSchemaName}');
    expect(source).not.toContain('>EPM.fbs<');
    expect(source).not.toContain('Standards type');
    expect(source).toContain("if (!value) return '';");
    expect(source).not.toContain("if (!value) return 'pending';");
    expect(source).toContain('void initializeWorkbench();');
    expect(source).not.toContain('await initializeWorkbench();');
    expect(source).not.toContain('filteredSubscribedSourceOptions');
    expect(source).not.toContain('explorerSourceSearchText');
    expect(source).not.toContain('handleExplorerSourceSearchInput');
    expect(source).not.toContain('Source search');
    expect(source).not.toContain("{#if selectedDataSection === 'explorer' && (workbenchLoading || localExplorerLoading)}");
    const explorerSection = source.slice(source.indexOf("{#if selectedDataSection === 'explorer'}"));
    expect(explorerSection).not.toContain('<span>Profile</span>');
    expect(explorerSection).not.toContain('<span>Entity</span>');
    expect(explorerSection).not.toContain('<span>Ask</span>');
    expect(explorerSection).not.toContain('Draft SQL');
    expect(explorerSection).not.toContain('<span>Page size</span>');
    expect(explorerSection).not.toContain('aria-label="Dataset summary"');
    expect(explorerSection).not.toContain('Remote rows');
    expect(explorerSection).not.toContain('Transport');
    expect(explorerSection).not.toContain('Scan');
  });

  it('does not present cached IPFS shard reads as a percent of wire speed', () => {
    const source = readUiSource('screens/LocalDataScreen.svelte');

    expect(source).toContain('downloadSpeedBytesPerSecond');
    expect(source).toContain('formatBytesPerSecond');
    expect(source).not.toContain('% wire');
  });
});

describe('SDN data worker source', () => {
  it('keeps remote sync and FlatSQL ingest off the renderer thread', () => {
    const workerSource = readRuntimeSource('local-flatsql.worker.ts');
    const localFlatSqlSource = readRuntimeSource('local-flatsql.ts');
    const clientSource = readRuntimeSource('local-flatsql-worker-client.ts');
    const libp2pSource = readRuntimeSource('sdn-backend-libp2p-sync.ts');
    const libp2pCacheSource = readRuntimeSource('libp2p-sync-backend-cache.ts');

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
      'const PUBLISHED_SHARD_FETCH_CONCURRENCY = 32',
      'PUBLISHED_SHARD_RANGE_BYTES',
      'const PUBLISHED_SHARD_RANGE_BYTES = 16 * 1024 * 1024',
      'const PUBLISHED_SHARD_RANGE_CONCURRENCY = 1',
      'fetchPublishedShardBytesViaLibp2pRanges',
      'readFlatSqlPublishedShardFromSources',
      'publishedShardBackendConfigsFor',
      '...(backendConfig.publishedShardSources ?? []).map(stripPublishedShardSources)',
      'stripPublishedShardSources(backendConfig)',
      'preferredSourceIndex',
      'totalRows = manifestTotalRows > 0 ? manifestTotalRows : Math.max(localRows, manifestTotalRows)',
      'downloadProgressPatch(downloadedBytes, networkTransferMs, measuredWireSpeedBytesPerSecond)',
      'downloadProgressPatch(downloadedBytes, networkTransferMs, options.measuredWireSpeedBytesPerSecond)',
      'segment.cid',
      'fetchPublishedSegmentsInOrder',
      'pendingPublishedSegmentItems',
      'completedPublishedSegmentCids',
      'completedPublishedRowsForSegments',
      'downloadedBytes += streamBytes.byteLength',
      'downloadedBytes += chunk.recordStream.byteLength',
      'skipRecords: fetched.skipRecords',
      'recordKeyOffset: fetched.cumulativeRows + fetched.skipRecords',
      'recordKeyPrefix: `published:${segment.cid}`',
      'recordPinLedgerEntries([pinLedgerEntryForPublishedSegment',
      'materializedAt: now',
      'prepareRecordsForTransfer',
      'workerGlobal.postMessage(response, transferables)',
    ]);
    expect(libp2pCacheSource).toContain('const PUBLISHED_SHARD_CLIENT_POOL_SIZE = 8');
    expect(workerSource).toMatch(/const sources = \[\s*\.\.\.\(backendConfig\.publishedShardSources \?\? \[\]\)\.map\(stripPublishedShardSources\),\s*stripPublishedShardSources\(backendConfig\),\s*\]/s);
    expect(workerSource).not.toContain('fetchCidBytesFromGateway');
    expect(workerSource).not.toContain('connectIpfsArtifactPeers');
    expect(workerSource).not.toContain('connectIpfsArtifactProviders');
    expect(workerSource).not.toContain('importPublishedFlatSqlShardCar');
    expect(workerSource).not.toContain('importPublishedShardCarBundles');
    expect(workerSource).not.toContain('importCarBytesToKubo');
    expect(workerSource).not.toContain('publishedShardGroupCarBundlesForSegments');
    expect(workerSource).not.toContain('withRemotePublishedShardBatchOperation');
    expect(workerSource).not.toContain('readFlatSqlPublishedShardBatch');
    expect(workerSource).not.toContain('segmentCoveredByImportedCar');
    expect(workerSource).not.toContain('Remote page scan');
    expect(workerSource).not.toContain('Remote page stream');
    expectSourceToContainAll(localFlatSqlSource, [
      'flatSqlSizePrefixedStreamInfo',
      'allFramesHaveDirectFileIdentifier',
      'state.db.ingest(directStreamBytes)',
    ]);
    expectSourceToContainAll(clientSource, [
      "new Worker(new URL('./local-flatsql.worker.ts', import.meta.url), { type: 'module' })",
      'syncProgressHandlers',
      'queryRemotePage',
      'syncSchema',
    ]);
    expectSourceToContainAll(libp2pSource, [
      'requestFlatSqlSyncChunk',
      'requestFlatSqlPublishedShard',
      'requestFlatSqlPublishedShardBatch',
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

  it('keeps the throughput harness on direct libp2p published-shard ranges for bulk shard bytes', () => {
    const source = readScriptSource('measure-flatsql-sync-throughput.mjs');

    expectSourceToContainAll(source, [
      'direct-libp2p-published-shard-ranges',
      'fetchPublishedShardBytesViaRanges',
      'readFlatSqlPublishedShard',
      'shardSources',
      '...(options.shardSources ?? [])',
      '{ peer: options.peer, addrs: options.addrs }',
      'multi-source direct libp2p',
      'selectedSegments',
      'There is no Kubo gateway, remote HTTP, or SSH data fallback in this harness.',
    ]);
    expect(source).toMatch(/return normalizeShardSourceEntries\(\[\s*\.\.\.\(options\.shardSources \?\? \[\]\),\s*\{ peer: options\.peer, addrs: options\.addrs \},\s*\]\)/s);
    expect(source).not.toContain('connectIpfsArtifactPeers');
    expect(source).not.toContain('connectIpfsArtifactProviders');
    expect(source).not.toContain('fetchCidBytesFromGateway');
    expect(source).not.toContain('importPublishedFlatSqlShardCar');
    expect(source).not.toContain('importCarBytesToKubo');
    expect(source).not.toContain('publishedShardGroupCarBundlesForSegments');
    expect(source).not.toContain('readFlatSqlPublishedShardBatch');
    expect(source).not.toContain("op: 'read_published_shard_batch'");
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
    const appCssWithoutChartGeometry = appCss
      .replace(/\.sdn-storage-donut\s*{[^}]*}/gs, '')
      .replace(/\.sdn-storage-legend-row::before\s*{[^}]*}/gs, '');

    expect(appCss).toMatch(/\.sdn-glass\s*{[^}]*backdrop-filter:\s*blur\([^)]*\)\s*saturate\([^)]*\)/s);
    expect(appCss).toMatch(/\.sdn-button\s*{[^}]*border-radius:\s*var\(--sdn-radius-sm\)/s);
    expect(appCssWithoutChartGeometry).not.toMatch(/border-radius:\s*(999|1000|50%)/);
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

  it('keeps data workbench columns inside the viewport with compact cells', () => {
    const appCss = readUiSource('styles/app.css');

    expect(appCss).toMatch(/\.sdn-workbench-table-wrap\s*{[^}]*overflow-x:\s*hidden/s);
    expect(appCss).toMatch(/\.sdn-workbench-table\s*{[^}]*width:\s*100%/s);
    expect(appCss).toMatch(/\.sdn-workbench-table\s*{[^}]*table-layout:\s*fixed/s);
    expect(appCss).toMatch(/\.sdn-workbench-table th,\s*\.sdn-workbench-table td\s*{[^}]*min-width:\s*0/s);
  });

  it('keeps explorer search controls on a common height', () => {
    const appCss = readUiSource('styles/app.css');

    expect(appCss).toMatch(/\.sdn-workbench-controls\s*{[^}]*--sdn-workbench-control-height:\s*2\.4rem/s);
    expect(appCss).toMatch(/\.sdn-explorer-controls \.sdn-input,\s*\.sdn-explorer-controls \.sdn-button\s*{[^}]*height:\s*var\(--sdn-workbench-control-height\)/s);
    expect(appCss).toMatch(/\.sdn-explorer-controls \.sdn-button\s*{[^}]*align-items:\s*center/s);
    expect(appCss).toMatch(/\.sdn-sql-input\s*{[^}]*padding:\s*calc\(\(var\(--sdn-workbench-control-height\) - 1rem\) \/ 2\) 0\.8rem/s);
  });

  it('keeps storage legend markers tight to labels with percent underneath', () => {
    const appCss = readUiSource('styles/app.css');

    expect(appCss).toMatch(/\.sdn-storage-legend\s*{[^}]*justify-content:\s*end/s);
    expect(appCss).toMatch(/\.sdn-storage-legend-row\s*{[^}]*grid-template-columns:\s*minmax\(0,\s*max-content\) max-content/s);
    expect(appCss).toMatch(/\.sdn-storage-legend-row\s*{[^}]*column-gap:\s*12px/s);
    expect(appCss).toMatch(/\.sdn-storage-legend-row::before\s*{[^}]*position:\s*absolute/s);
    expect(appCss).toMatch(/\.sdn-storage-legend-row em\s*{[^}]*grid-column:\s*2/s);
    expect(appCss).toMatch(/\.sdn-storage-legend-row em\s*{[^}]*justify-self:\s*end/s);
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

  it('keeps subscription row actions horizontal instead of stacked in the grid', () => {
    const appCss = readUiSource('styles/app.css');

    expect(appCss).toMatch(/\.sdn-subscription-row\s*{[^}]*grid-template-columns:[^}]*max-content/s);
    expect(appCss).toMatch(/\.sdn-subscription-row\s*>\s*\.sdn-subscription-actions\s*{[^}]*display:\s*flex/s);
    expect(appCss).toMatch(/\.sdn-subscription-actions\s*{[^}]*flex-wrap:\s*nowrap[^}]*overflow-x:\s*auto/s);
  });

  it('shows pin verification feedback as a dismissible fading toast', () => {
    const appCss = readUiSource('styles/app.css');

    expect(appCss).toMatch(/\.sdn-toast-region\s*{[^}]*position:\s*fixed[^}]*right:\s*1rem/s);
    expect(appCss).toMatch(/\.sdn-toast\s*{[^}]*animation:\s*sdn-toast-pop-fade/s);
    expect(appCss).toMatch(/\.sdn-toast-dismiss\s*{[^}]*position:\s*absolute[^}]*right:\s*0\.45rem/s);
    expect(appCss).toContain('@keyframes sdn-toast-pop-fade');
  });

  it('keeps the app shell fixed while content panes own scrolling', () => {
    const appCss = readUiSource('styles/app.css');

    expect(appCss).toMatch(/html,\s*body,\s*#root\s*{[^}]*height:\s*100%[^}]*overflow:\s*hidden/s);
    expect(appCss).toMatch(/\.sdn-app\s*{[^}]*height:\s*100vh[^}]*overflow:\s*hidden/s);
    expect(appCss).toMatch(/\.sdn-main\s*{[^}]*grid-template-rows:\s*auto minmax\(0,\s*1fr\)[^}]*overflow:\s*hidden/s);
    expect(appCss).toMatch(/\.sdn-content\s*{[^}]*align-content:\s*start[^}]*overflow:\s*auto/s);
    expect(appCss).toMatch(/\.sdn-content\s*{[^}]*padding:\s*1rem 2rem 2rem/s);
    expect(appCss).toMatch(/\.sdn-workbench-main,\s*\.sdn-source-browser\s*{[^}]*align-content:\s*start/s);
  });

  it('centers logout and node identity replacement modals in the viewport', () => {
    const appSource = readUiSource('App.svelte');
    const appCss = readUiSource('styles/app.css');

    expect(appSource).toContain('aria-label="Confirm logout"');
    expect(appSource).toContain('aria-label="Confirm node identity replacement"');
    expect(appCss).toMatch(/\.sdn-modal-backdrop\s*{[^}]*display:\s*grid[^}]*place-items:\s*center[^}]*min-height:\s*100dvh/s);
    expect(appCss).toMatch(/\.sdn-modal\s*{[^}]*position:\s*static[^}]*inset:\s*auto[^}]*margin:\s*0[^}]*max-height:\s*calc\(100dvh - 2rem\)[^}]*overflow:\s*auto/s);
  });

  it('anchors node identity layouts to content height instead of stretching nav rows', () => {
    const appCss = readUiSource('styles/app.css');

    expect(appCss).toMatch(/\.sdn-identity-workspace\s*{[^}]*align-content:\s*start/s);
    expect(appCss).toMatch(/\.sdn-identity-workspace\s*{[^}]*grid-auto-rows:\s*max-content/s);
    expect(appCss).toMatch(/\.sdn-breadcrumb-tabs\s*{[^}]*align-self:\s*start/s);
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

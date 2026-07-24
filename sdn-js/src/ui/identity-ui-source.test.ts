import { existsSync, readFileSync } from 'node:fs';
import { render } from 'svelte/server';
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
  it('keeps the node surface read-only and removes every automatic wallet gate', () => {
    const appSource = readUiSource('App.svelte');
    const nodeSource = readUiSource('screens/NodeScreen.svelte');
    const identitySource = readUiSource('components/IdentityPanel.svelte');
    const topBarSource = readUiSource('components/TopStatusBar.svelte');
    const upstreamSource = readUiSource('upstream-webui/overrides/App.js');
    const productionSource = [appSource, nodeSource, identitySource, topBarSource, upstreamSource].join('\n');

    expectSourceToContainAll(identitySource, [
      'Read-only node identity',
      'Hosted EPM status',
      'Published identities',
      'No hosted EPM records reported by this node.',
    ]);
    expect(nodeSource).toContain('<IdentityPanel {summary} {profile} {hostedEpms} />');
    expect(identitySource).not.toMatch(/<(?:button|input|form|textarea|select)\b/);

    for (const forbidden of [
      'NodeIdentityGate',
      'createNodeIdentitySessionController',
      'openLogin',
      'mountWallet',
      'login.sign',
      'applyWalletNodeIdentity',
      'saveNodeProfile',
      'saveHostedEpm',
      'importHostedEpm',
      'deleteHostedEpm',
      'Encrypted Private Key',
      'Deterministic Keygen',
      'Import Passphrase',
      'SessionControls',
    ]) {
      expect(productionSource, forbidden).not.toContain(forbidden);
    }

    expect(existsSync(new URL('../../ui/src/components/NodeIdentityGate.svelte', import.meta.url))).toBe(false);
    expect(existsSync(new URL('../../ui/src/lib/node-identity-session.ts', import.meta.url))).toBe(false);
  });

  it('renders reported node and EPM values without an editing control', async () => {
    const { default: NodeScreen } = await import('../../ui/src/screens/NodeScreen.svelte');
    const { body } = render(NodeScreen, {
      props: {
        summary: {
          displayName: 'Read Only Node',
          peerId: '12D3KooWReadOnlyNodeIdentifier',
          agentVersion: 'sdn/2.0.12',
          online: true,
          runtime: 'desktop-local',
        },
        profile: { dn: 'Read Only Node' },
        hostedEpms: [{
          id: 'ops',
          kind: 'hosted',
          label: 'Operations',
          peerId: '12D3KooWOperationsIdentifier',
          epmCid: 'bafybeigeneratedepmcid',
          epmJson: {},
        }],
      },
    });

    expect(body).toContain('Read Only Node');
    expect(body).toContain('Operations');
    expect(body).toContain('EPM');
    expect(body).not.toMatch(/<(?:button|input|form|textarea|select)\b/);
  });

  it('centralizes compact vCard QR metadata and identity alias handling', () => {
    const runtimeSource = readRuntimeSource('identity-vcard.ts');
    const directorySource = readUiSource('components/DirectorySearchPanel.svelte');
    const peersSource = readUiSource('screens/PeersScreen.svelte');
    const qrConsumerSources = [directorySource, peersSource];

    expectSourceToContainAll(runtimeSource, [
      'signing.spacedatanetwork.org',
      'encryption.spacedatanetwork.org',
      'peerid.spacedatanetwork.org',
      'xpub.spacedatanetwork.org',
      'spacedatanetwork.org',
      'createVCardQrPayloadFromVCard',
      'ADR;TYPE=WORK',
      'X-SDN-SIGNING-PUBLIC-KEY',
      'X-SDN-ENCRYPTION-PUBLIC-KEY',
      'EMAIL;TYPE=INTERNET;TYPE=${type}',
      'given_name',
      'family_name',
      'legal_name',
      'honorific_prefix',
      'honorific_suffix',
    ]);
    expect(runtimeSource).toContain("addCompactIdentityEmailLine(lines, 'xpub', xpub, XPUB_ALIAS_DOMAIN)");
    expect(runtimeSource).not.toContain("addCompactIdentityEmailLine(lines, 'peerid'");
    expect(runtimeSource).not.toContain('addVCardIdentityEmailLines');
    for (const source of qrConsumerSources) {
      expect(source).toContain("../../../src/ui/runtime/identity-vcard");
      expect(source).toContain("createVCardQrPayload as createVCardQrPayloadLocal");
      expect(source).not.toMatch(/function\s+addVCardLine/);
      expect(source).not.toMatch(/function\s+publicKeyEmailAddress/);
    }
  });

  it('reduces the full daemon vCard before the SpaceAware console encodes its QR', () => {
    const source = readUiSource('spaceaware/screens/console/QrOverlay.svelte');

    expect(source).toContain('createVCardQrPayloadFromVCard');
    expect(source).toContain('encodeQrDataUrl(createVCardQrPayloadFromVCard(vcard))');
    expect(source).not.toContain('encodeQrDataUrl(vcard)');
  });

  it('retains the Data screen instance across route changes', () => {
    const appSource = readUiSource('App.svelte');

    expect(appSource).toContain('let dataScreenPrimed = false;');
    expect(appSource).toContain("if (backend || primaryRoute === '/data') dataScreenPrimed = true;");
    expect(appSource).toContain('{#if dataScreenPrimed}');
    expect(appSource).toContain("<div hidden={primaryRoute !== '/data'} aria-hidden={primaryRoute !== '/data'}>");
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
      'liveSchemaSyncRows = buildSubscribedSchemaSyncRows',
      'schemaSyncRows = dataPageCacheActive && cachedDataPageView ? cachedDataPageView.schemaSyncRows : liveSchemaSyncRows',
      'publishedSnapshotTotalRows',
      'const rowCountRemoteRows = publishedSnapshotTotalRows ?? remoteRows',
      'remoteRowsForSchemaSyncRow(subscription, progress)',
      'schemaCompactRowsLabel(schema)',
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
      'nextSyncAttemptLabel(selectedSubscriptionDetailSchema)',
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
      "type DataSection = 'store' | 'subscriptions' | 'explorer'",
      'buildDataCatalogRows',
      'buildDataOverviewVisuals',
      'overviewStorageGroup',
      'STORAGE_GROUP_OPTIONS',
      'Storage by',
      'summarizeDataCatalog',
      'DATA_SECTIONS',
      "let selectedDataSection: DataSection = 'store'",
      'setDataSection',
      'activeStorageRows',
      'selectedSchemaSyncRow',
      'refreshSubscriptionRemoteRowsFromSummary',
      'selectedSchemaSyncRow?.remoteRows',
      'Data / Store',
      'Data / Subscriptions',
      'Data / Explorer',
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
    // Loop D.1 promoted the engine store from ui/runtime into core
    // src/local-flatsql.ts (the runtime path is a re-export shim).
    const localFlatSqlSource = readFileSync(new URL('../local-flatsql.ts', import.meta.url), 'utf8');
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
      'PUBLISHED_SHARD_BATCH_BYTES',
      'PUBLISHED_SHARD_BATCH_FETCH_CONCURRENCY',
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
      'publishedSegmentBatchItems',
      'completedPublishedSegmentCids',
      'completedPublishedRowsForSegments',
      'downloadedBytes += streamBytes.byteLength',
      'downloadedBytes += chunk.recordStream.byteLength',
      'skipRecords: fetched.skipRecords',
      'recordKeyOffset: fetched.cumulativeRows + fetched.skipRecords',
      'recordKeyPrefix: `published:${segment.cid}`',
      'const ledgerEntry = pinLedgerEntryForPublishedSegment',
      'recordPinLedgerEntries([ledgerEntry]',
      'materializedAt: now',
      'withRemotePublishedShardBatchOperation',
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
    expect(workerSource).toContain('readFlatSqlPublishedShardBatchFromSources');
    expect(workerSource).not.toContain('segmentCoveredByImportedCar');
    expect(workerSource).not.toContain('Remote page scan');
    expect(workerSource).not.toContain('Remote page stream');
    expectSourceToContainAll(localFlatSqlSource, [
      'flatSqlSizePrefixedStreamInfo',
      'allFramesHaveDirectFileIdentifier',
      'state.db.ingest(directStreamBytes, directSource)',
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
    expect(mainSource).toContain('VITE_EMBEDDED_SDN_APP');
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
      'published-shard-ranges',
      'published-shard-batches',
      'fetchPublishedShardBytesViaRanges',
      'readFlatSqlPublishedShard',
      'readFlatSqlPublishedShardBatch',
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

    expect(identitySource).toContain('Read-only node identity');
    expect(identitySource).not.toMatch(/<(?:button|input|form|textarea|select)\b/);
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
    expect(appCss).toMatch(/\.sdn-workbench-table-wrap\s*{[^}]*container-type:\s*inline-size/s);
    expect(appCss).toMatch(/--sdn-explorer-table-font-size:\s*clamp\(5px,[^;]*12px\)/s);
    expect(appCss).toMatch(/\.sdn-workbench-table\s*{[^}]*width:\s*100%/s);
    expect(appCss).toMatch(/\.sdn-workbench-table\s*{[^}]*table-layout:\s*fixed/s);
    expect(appCss).toMatch(/\.sdn-workbench-table th,\s*\.sdn-workbench-table td\s*{[^}]*font-size:\s*var\(--sdn-explorer-table-font-size\)/s);
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

  it('keeps subscription rows responsive and lets actions wrap inside the grid', () => {
    const appCss = readUiSource('styles/app.css');

    expect(appCss).toMatch(/\.sdn-subscription-row\s*{[^}]*grid-template-columns:\s*2\.5rem minmax\(0,\s*1\.35fr\)/s);
    expect(appCss).toMatch(/\.sdn-subscription-row\s*>\s*\.sdn-subscription-actions\s*{[^}]*display:\s*flex/s);
    expect(appCss).toMatch(/\.sdn-subscription-actions\s*{[^}]*flex-wrap:\s*wrap[^}]*overflow:\s*visible/s);
    expect(appCss).toMatch(/@media\s*\(max-width:\s*1120px\)\s*{[^}]*\.sdn-subscription-row\s*{[^}]*grid-template-columns:\s*2\.5rem minmax\(0,\s*1fr\)/s);
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

  it('removes logout and node identity replacement modals from the app', () => {
    const appSource = readUiSource('App.svelte');

    expect(appSource).not.toContain('aria-label="Confirm logout"');
    expect(appSource).not.toContain('aria-label="Confirm node identity replacement"');
    expect(appSource).not.toContain('Replace and sign EPM');
  });

  it('anchors node identity layouts to content height instead of stretching nav rows', () => {
    const appCss = readUiSource('styles/app.css');

    expect(appCss).toMatch(/\.sdn-identity-workspace\s*{[^}]*align-content:\s*start/s);
    expect(appCss).toMatch(/\.sdn-identity-workspace\s*{[^}]*grid-auto-rows:\s*max-content/s);
    expect(appCss).toMatch(/\.sdn-breadcrumb-tabs\s*{[^}]*align-self:\s*start/s);
  });

  it('caps node subpanels to a readable responsive width on wide screens', () => {
    const identitySource = readUiSource('components/IdentityPanel.svelte');
    const localDataSource = readUiSource('screens/LocalDataScreen.svelte');
    const appCss = readUiSource('styles/app.css');

    expect(identitySource).toContain('sdn-readable-panel');
    expect(localDataSource).not.toContain('sdn-readable-panel');
    expect(appCss).toMatch(/\.sdn-readable-panel\s*{[^}]*width:\s*min\(100%,\s*var\(--sdn-readable-panel-width\)\)/s);
    expect(appCss).toMatch(/\.sdn-readable-panel\s*{[^}]*justify-self:\s*start/s);
    expect(appCss).toMatch(/@media\s*\(min-width:\s*1600px\)\s*{[^}]*\.sdn-readable-panel\s*{[^}]*--sdn-readable-panel-width:\s*min\(64rem,\s*50vw\)/s);
    expect(appCss).toMatch(/\.sdn-path-input-row,\s*\.sdn-storage-row\s*{\s*grid-template-columns:\s*1fr/s);
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

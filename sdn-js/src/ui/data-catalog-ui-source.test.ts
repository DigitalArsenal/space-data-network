import fs from 'node:fs';
import path from 'node:path';
import { describe, expect, it } from 'vitest';

const localDataScreenSource = fs.readFileSync(
  path.resolve(__dirname, '../../ui/src/screens/LocalDataScreen.svelte'),
  'utf8',
);
const appCssSource = fs.readFileSync(
  path.resolve(__dirname, '../../ui/src/styles/app.css'),
  'utf8',
);

function sourceBetween(source: string, start: string, end: string): string {
  const startIndex = source.indexOf(start);
  expect(startIndex, `${start} should exist`).toBeGreaterThanOrEqual(0);
  const endIndex = source.indexOf(end, startIndex + start.length);
  expect(endIndex, `${end} should exist after ${start}`).toBeGreaterThan(startIndex);
  return source.slice(startIndex, endIndex);
}

describe('Data catalog UI source', () => {
  it('exposes only the consolidated data navigation sections', () => {
    for (const label of ['Store', 'Subscriptions', 'Explorer']) {
      expect(localDataScreenSource).toContain(`label: '${label}'`);
    }
    for (const retiredLabel of ['Overview', 'Catalog', 'My Subscriptions', 'Sources', 'Message Types', 'Storage', 'Billing', 'Activity']) {
      expect(localDataScreenSource).not.toContain(`label: '${retiredLabel}'`);
    }
    for (const retiredSection of ['overview', 'catalog', 'sources', 'message-types', 'storage', 'billing', 'activity']) {
      expect(localDataScreenSource).not.toContain(`selectedDataSection === '${retiredSection}'`);
    }
    expect(localDataScreenSource).toContain("let selectedDataSection: DataSection = 'store';");
  });

  it('does not hard-code marketplace pricing examples into the client', () => {
    expect(localDataScreenSource).not.toMatch(/\$(248|49|99)\/?(mo)?/);
  });

  it('renders the paid-aware store visualization panels', () => {
    const storeSection = sourceBetween(localDataScreenSource, 'aria-label="Data store"', "{#if selectedDataSection === 'subscriptions'}");

    expect(storeSection).toContain('sdn-overview-visuals');
    expect(storeSection).toContain('sdn-overview-storage-panel');
    expect(storeSection).toContain('Storage by');
    expect(storeSection).toContain('Cost and storage by provider');
    expect(localDataScreenSource).not.toContain('Access coverage');
    expect(localDataScreenSource).not.toContain('sdn-access-coverage-grid');
    expect(localDataScreenSource).not.toContain('selectOverviewCoverageCell');
    expect(appCssSource).toMatch(/\.sdn-overview-panel\s*{[^}]*min-height:\s*16\.25rem/s);
    expect(appCssSource).toMatch(/\.sdn-overview-storage-panel \.sdn-storage-access-visual\s*{[^}]*grid-template-columns:\s*minmax\(10\.75rem,\s*12rem\)\s+minmax\(0,\s*1fr\)/s);
    expect(appCssSource).toMatch(/\.sdn-overview-storage-panel \.sdn-storage-donut\s*{[^}]*grid-column:\s*1/s);
    expect(appCssSource).toMatch(/\.sdn-overview-storage-panel \.sdn-storage-donut\s*{[^}]*justify-self:\s*start/s);
    expect(appCssSource).toMatch(/\.sdn-overview-storage-panel \.sdn-storage-legend\s*{[^}]*grid-column:\s*2/s);
  });

  it('renders store filters for query, access, sync, and storage state', () => {
    const storeSection = sourceBetween(localDataScreenSource, 'aria-label="Data store"', "{#if selectedDataSection === 'subscriptions'}");

    expect(storeSection).toContain('sdn-catalog-filters');
    expect(storeSection).toContain('aria-label="Store filters"');
    expect(storeSection).toContain('catalogSearchText');
    expect(storeSection).toContain('aria-label="Search store"');
    expect(storeSection).toContain('catalogAccessFilter');
    expect(storeSection).toContain('catalogSyncFilter');
    expect(storeSection).toContain('catalogStorageFilter');
  });

  it('does not render separate Plan or Actions columns in data tables', () => {
    const storeProductsTable = sourceBetween(localDataScreenSource, 'aria-label="Store data products"', "{#if selectedDataSection === 'subscriptions'}");

    expect(storeProductsTable).not.toContain('<th>Plan</th>');
    expect(storeProductsTable).not.toContain('<th>Actions</th>');
  });

  it('routes desktop-local FlatSQL persistence through the configured desktop storage API', () => {
    expect(localDataScreenSource).toContain('function localFlatSqlDesktopPersistenceBaseUrl()');
    expect(localDataScreenSource).toContain("backend?.mode !== 'desktop-local'");
    expect(localDataScreenSource).toContain("!pathname.startsWith('/sdn')");
    expect(localDataScreenSource).toContain('desktopPersistenceBaseUrl: localFlatSqlDesktopPersistenceBaseUrl()');
  });

  it('prunes unsubscribed replace-snapshot local stores so stale SATCAT caches cannot linger', () => {
    expect(localDataScreenSource).toContain('const REPLACE_SNAPSHOT_STANDARD_IDS = LOCAL_FLATSQL_SCHEMAS');
    expect(localDataScreenSource).toContain("defaultDataFeedRetentionPolicy(schema.standardId) === 'replace-snapshot'");
    expect(localDataScreenSource).toContain('await pruneUnsubscribedReplaceSnapshotStores(migrationSources, dataDirectoryState.subscriptions);');
    expect(localDataScreenSource).toContain('function snapshotStoreKey(dataSourceId: string, datastoreKey: string | null, standardId: string): string');
    expect(localDataScreenSource).toContain('clearSchemaSyncProgressForSubscription(dataSourceId, standardId, null);');
  });

  it('renders Store as a searchable product store with expandable action rows', () => {
    const storeSection = sourceBetween(localDataScreenSource, 'aria-label="Data store"', "{#if selectedDataSection === 'subscriptions'}");
    const storeProductsTable = sourceBetween(localDataScreenSource, 'aria-label="Store data products"', "{#if selectedDataSection === 'subscriptions'}");

    expect(localDataScreenSource).not.toContain("let overviewTableSearchText = '';");
    expect(localDataScreenSource).not.toContain('filteredOverviewDataCatalogRows');
    expect(storeSection).toContain('aria-label="Search store"');
    expect(storeProductsTable).toContain('as row (catalogRowKey(row))}');
    expect(storeProductsTable).toContain('{#each filteredDataCatalogRows as row');
    expect(storeProductsTable).toContain('class="sdn-catalog-action-panel"');
    expect(storeProductsTable).toContain('sdn-catalog-action-row');
    expect(storeProductsTable).toContain('aria-expanded={expandedCatalogActionRowKey === catalogRowKey(row)}');
  });

  it('renders cached overview row counts before remote summaries and local stats finish loading', () => {
    expect(localDataScreenSource).toContain('let localFlatSqlStatsLoaded = false;');
    expect(localDataScreenSource).toContain("const DATA_PAGE_VIEW_CACHE_STORAGE_KEY = 'sdn:data-page-view-cache:v1';");
    expect(localDataScreenSource).toContain('let cachedDataPageView: CachedDataPageView | null = loadCachedDataPageView();');
    expect(localDataScreenSource).toContain('dataPageCacheActive');
    expect(localDataScreenSource).toContain('loadCachedDataPageView');
    expect(localDataScreenSource).toContain('persistCachedDataPageView');
    expect(localDataScreenSource).toContain('rememberDataPageViewCache');
    expect(localDataScreenSource).toContain('localFlatSqlStatsLoaded,');
    expect(localDataScreenSource).toContain('schemaSyncRows = dataPageCacheActive && cachedDataPageView ? cachedDataPageView.schemaSyncRows : liveSchemaSyncRows;');
    expect(localDataScreenSource).toContain('dataCatalogRows = dataPageCacheActive && cachedDataPageView ? cachedDataPageView.dataCatalogRows : buildCatalogRows(schemaSyncRows);');
    expect(localDataScreenSource).toContain('const sourceStatsAreAuthoritative = sourceStatsSelected && localStatsLoaded;');
    expect(localDataScreenSource).toContain('localFlatSqlStatsLoaded = true;');
    expect(localDataScreenSource).not.toContain('$: storageMetricsLoading = dataPageLoading || activeStorageRows.some(isSchemaRemoteRowsLoading);');

    const remoteRowsLoadingSource = sourceBetween(
      localDataScreenSource,
      'function remoteRowsAreLoading(',
      'function isSchemaRemoteRowsLoading',
    );
    expect(remoteRowsLoadingSource.indexOf('if (remoteRows > 0 || progress.totalRows > 0) return false;')).toBeLessThan(
      remoteRowsLoadingSource.indexOf('if (pageLoading) return true;'),
    );
  });

  it('uses record-table formatting and expandable row actions for store data products', () => {
    const catalogProductsTable = sourceBetween(localDataScreenSource, 'aria-label="Store data products"', "{#if selectedDataSection === 'subscriptions'}");

    expect(localDataScreenSource).toContain('svelte:window on:click={handleCatalogOutsideClick}');
    expect(localDataScreenSource).toContain('<article class="sdn-card sdn-glass sdn-workbench">');
    expect(localDataScreenSource).toContain('event.composedPath()');
    expect(localDataScreenSource).toContain('toggleCatalogRowActions(row)');
    expect(localDataScreenSource).toContain('return row.id || [');
    expect(localDataScreenSource).toContain('data-catalog-row-key={catalogRowKey(row)}');
    expect(localDataScreenSource).toContain('class="sdn-table-wrap sdn-workbench-table-wrap sdn-catalog-table-wrap"');
    expect(localDataScreenSource).toContain('class="sdn-table sdn-workbench-table sdn-catalog-table"');
    expect(localDataScreenSource).toContain('sdn-catalog-action-panel');
    expect(catalogProductsTable).toContain('colspan="7"');

    expect(catalogProductsTable).toContain('{#each');
    expect(catalogProductsTable).toContain('as row (catalogRowKey(row))}');
    expect(localDataScreenSource).toContain('handleCatalogCellButtonClick(row, event)');
    expect(localDataScreenSource).toContain('suppressCatalogOutsideClearOnce');
    expect(catalogProductsTable).toContain('class="sdn-catalog-cell-trigger"');
    expect(catalogProductsTable).toContain('on:click={(event) => handleCatalogCellButtonClick(row, event)}');
    expect(catalogProductsTable).toContain('on:keydown={(event) => handleCatalogRowKeydown(row, event)}');
    expect(catalogProductsTable).toContain('expandedCatalogActionRowKey === catalogRowKey(row)');
    expect(catalogProductsTable).not.toContain('catalogRowActionsExpanded(row)');
    expect(appCssSource).toContain('.sdn-catalog-cell-trigger');
    expect(catalogProductsTable).toContain('class="sdn-catalog-action-panel"');
    expect(catalogProductsTable).toContain('class="sdn-catalog-product-summary"');
    expect(catalogProductsTable).toContain('class="sdn-catalog-detail-grid"');
    expect(catalogProductsTable).toContain('catalogRowPrimaryActionLabel(row)');
    expect(catalogProductsTable).toContain('handleCatalogPrimaryAction(row)');
    expect(catalogProductsTable).toContain('catalogRowRawDataAvailable(row)');
    expect(catalogProductsTable).toContain('catalogRowProviderIdentityLabel(row)');
    expect(catalogProductsTable).toContain('catalogRowTrustLabel(row)');
    expect(catalogProductsTable).toContain('catalogRowVerificationLabel(row)');
    expect(catalogProductsTable).toContain('catalogRowStorageEstimateLabel(row)');
    expect(catalogProductsTable).toContain('catalogRowRestrictionLabel(row)');
    expect(catalogProductsTable).toContain('<span>Provider</span>');
    expect(catalogProductsTable).toContain('<span>Message types</span>');
    expect(catalogProductsTable).toContain('<span>Storage estimate</span>');
    expect(catalogProductsTable).not.toContain('class="sdn-catalog-action-panel" on:click');
    expect(appCssSource).toMatch(/\.sdn-catalog-row\.sdn-catalog-expanded td\s*{[^}]*background:\s*rgba\(10,\s*132,\s*255,\s*0\.18\)/s);
    expect(appCssSource).toContain('.sdn-catalog-product-summary');
    expect(appCssSource).toContain('.sdn-catalog-detail-grid');
    expect(appCssSource).toMatch(/\.sdn-catalog-detail-grid\s*{[^}]*grid-template-columns:\s*repeat\(4,\s*minmax\(0,\s*1fr\)\)/s);
  });

  it('keeps locked catalog products metadata-only until entitlement exists', () => {
    expect(localDataScreenSource).toContain("return row.access.state !== 'locked';");
    expect(localDataScreenSource).toContain('Raw records are hidden until this node has an active entitlement.');
    expect(localDataScreenSource).toContain("case 'locked': return 'View plans';");
    expect(localDataScreenSource).toContain('{#if catalogRowRawDataAvailable(row)}');
    expect(localDataScreenSource).toContain('Open Explorer');
  });

  it('renders always-visible explorer column query boxes under abbreviated headers', () => {
    const explorerTable = sourceBetween(localDataScreenSource, 'aria-label="Data rows"', '<div class="sdn-pagination">');

    expect(explorerTable).toContain('columnHeaderAbbreviation');
    expect(explorerTable).toContain('columnHeaderKeyLabel');
    expect(explorerTable).toContain('<tr class="sdn-column-filter-row">');
    expect(explorerTable).toContain('class="sdn-input sdn-column-filter"');
    expect(explorerTable).not.toContain('sdn-filter-toggle');
    expect(explorerTable).not.toContain('columnFilterOpen');
    expect(localDataScreenSource).toContain('sdn-column-key');
    expect(localDataScreenSource).toContain('explorerColumnKeyEntries');
    expect(localDataScreenSource).toContain("RA_OF_ASC_NODE: 'RAAN'");
    expect(localDataScreenSource).toContain("NORAD_CAT_ID: 'NORAD'");
    expect(localDataScreenSource).toContain('MPE_STANDARD_COLUMNS');
    expect(localDataScreenSource).toContain('SPW_STANDARD_COLUMNS');
    expect(appCssSource).toContain('.sdn-column-header-control');
    expect(appCssSource).toContain('.sdn-column-key');
    expect(appCssSource).not.toContain('.sdn-filter-toggle');
  });

  it('renders saved Explorer SQL/dashboard views with local persistence controls', () => {
    const explorerSection = sourceBetween(localDataScreenSource, 'aria-label="Data explorer"', '</section>');

    expect(localDataScreenSource).toContain("const EXPLORER_SAVED_VIEWS_STORAGE_KEY = 'sdn:data-explorer-saved-views:v1';");
    expect(localDataScreenSource).toContain('interface SavedExplorerView');
    expect(localDataScreenSource).toContain('let savedExplorerViews: SavedExplorerView[] = loadSavedExplorerViews();');
    expect(localDataScreenSource).toContain('function saveCurrentExplorerView(): void');
    expect(localDataScreenSource).toContain('function applySavedExplorerView');
    expect(localDataScreenSource).toContain('function deleteSelectedExplorerView(): void');
    expect(localDataScreenSource).toContain('persistSavedExplorerViews(savedExplorerViews)');
    expect(explorerSection).toContain('aria-label="Saved Explorer views"');
    expect(explorerSection).toContain('bind:value={selectedSavedExplorerViewId}');
    expect(explorerSection).toContain('placeholder="View name"');
    expect(explorerSection).toContain('Save view');
    expect(explorerSection).toContain('Delete view');
    expect(appCssSource).toContain('.sdn-saved-view-controls');
  });

  it('renders store renewal with renewal timing and unit price instead of quota text', () => {
    const catalogProductsTable = sourceBetween(localDataScreenSource, 'aria-label="Store data products"', "{#if selectedDataSection === 'subscriptions'}");

    expect(catalogProductsTable).toContain('<th>Renewal</th>');
    expect(catalogProductsTable).not.toContain('<th>Renewal / quota</th>');
    expect(catalogProductsTable).not.toContain('<th>Price / usage</th>');
    expect(catalogProductsTable).toContain('<strong>{row.plan.renewalLabel}</strong>');
    expect(catalogProductsTable).toContain('<span>{row.plan.priceLabel}</span>');
    expect(catalogProductsTable).not.toContain('row.plan.quotaLabel');
  });

  it('renders billing state inside the Store rather than a separate billing interface', () => {
    const storeSection = sourceBetween(localDataScreenSource, 'aria-label="Data store"', "{#if selectedDataSection === 'subscriptions'}");

    expect(localDataScreenSource).toContain('catalogRowHasBillingData');
    expect(localDataScreenSource).toContain('$: billingDataRows = dataPageCacheActive && cachedDataPageView ? cachedDataPageView.billingDataRows : dataCatalogRows.filter(catalogRowHasBillingData);');
    expect(localDataScreenSource).toContain('buildDataBillingProviderRows');
    expect(localDataScreenSource).toContain('$: billingProviderRows = dataPageCacheActive && cachedDataPageView ? cachedDataPageView.billingProviderRows : buildDataBillingProviderRows(dataCatalogRows);');
    expect(storeSection).toContain('dataCatalogSummary.billingMetricTitle');
    expect(storeSection).toContain('dataCatalogSummary.billingMetricValue');
    expect(storeSection).toContain('row.plan.renewalLabel');
    expect(storeSection).toContain('row.plan.priceLabel');
    expect(localDataScreenSource).not.toContain('aria-label="Billing"');
    expect(localDataScreenSource).not.toContain('aria-label="Spend by provider"');
    expect(localDataScreenSource).not.toContain('aria-label="Usage and renewals"');
  });

  it('renders message type health inside Subscriptions with direct management actions', () => {
    const subscriptionsSection = sourceBetween(localDataScreenSource, 'aria-label="Sync settings"', "{#if selectedDataSection === 'explorer'}");

    expect(localDataScreenSource).toContain('$: messageTypeRows = dataPageCacheActive && cachedDataPageView ? cachedDataPageView.messageTypeRows : sortedMessageTypeRows(schemaSyncRows);');
    expect(localDataScreenSource).toContain('function sortedMessageTypeRows(rows: SchemaSyncRow[]): SchemaSyncRow[]');
    expect(localDataScreenSource).toContain('rightRows - leftRows');
    expect(subscriptionsSection).toContain('{#each filteredSubscriptionRows as schema (schema.subscriptionId)}');
    expect(subscriptionsSection).toContain('schemaRemoteRowsLabel(schema)');
    expect(subscriptionsSection).toContain('schemaLocalRowsLabel(schema)');
    expect(subscriptionsSection).toContain('schemaPinnedRowsLabel(schema)');
    expect(subscriptionsSection).toContain('schemaCachedBytesLabel');
    expect(subscriptionsSection).toContain('schemaLastSyncedLabel');
    expect(subscriptionsSection).toContain('schemaHealthLabel(schema)');
    expect(subscriptionsSection).toContain('openSchemaInExplorer(selectedSubscriptionDetailSchema)');
    expect(subscriptionsSection).toContain('retrySubscriptionSync(schema)');
  });

  it('keeps provider/source provenance inside Store product details', () => {
    const storeSection = sourceBetween(localDataScreenSource, 'aria-label="Data store"', "{#if selectedDataSection === 'subscriptions'}");

    expect(localDataScreenSource).toContain('$: sourceProvenanceRows = dataPageCacheActive && cachedDataPageView ? cachedDataPageView.sourceProvenanceRows : buildSourceProvenanceRows(dataSourceOptions, schemaSyncRows, dataDirectoryState.peerTrust);');
    expect(localDataScreenSource).toContain('interface SourceProvenanceRow');
    expect(localDataScreenSource).toContain('function buildSourceProvenanceRows(');
    expect(storeSection).toContain('catalogRowProviderIdentityLabel(row)');
    expect(storeSection).toContain('<span>Public key</span>');
    expect(storeSection).toContain('shorten(row.providerPublicKey, 42)');
    expect(storeSection).toContain('catalogRowSourceCountLabel(row)');
    expect(storeSection).toContain('catalogRowTrustLabel(row)');
    expect(storeSection).toContain('catalogRowVerificationLabel(row)');
    expect(storeSection).toContain('shorten(row.providerPeerId ?? row.providerPublicKey ??');
    expect(localDataScreenSource).not.toContain('aria-label="Data sources"');
  });

  it('renders subscription storage rows with scoped reset and sync schedule state', () => {
    const storageSection = sourceBetween(localDataScreenSource, 'aria-label="Sync settings"', "{#if selectedDataSection === 'explorer'}");

    expect(storageSection).toContain('{#each filteredSubscriptionRows as schema (schema.subscriptionId)}');
    expect(storageSection).toContain('schemaRemoteRowsLabel(schema)');
    expect(storageSection).toContain('schemaLocalRowsLabel(schema)');
    expect(storageSection).toContain('schemaPinnedRowsLabel(schema)');
    expect(storageSection).toContain('schemaCachedBytesLabel(schema)');
    expect(storageSection).toContain('schemaDownloadSpeedLabel(schema)');
    expect(storageSection).toContain('schemaStoragePressureLabel(schema)');
    expect(storageSection).toContain('schemaRetentionPolicyLabel(schema)');
    expect(storageSection).toContain('nextSyncAttemptLabel(schema)');
    expect(storageSection).toContain('schemaLastSyncedLabel(schema)');
    expect(storageSection).toContain('class="sdn-storage-row-actions sdn-subscription-actions"');
    expect(storageSection).toContain('verifyPinnedArtifacts(schema)');
    expect(storageSection).toContain('beginResetSubscriptionData(schema.subscriptionId)');
    expect(storageSection).toContain('confirmResetSubscriptionData(schema)');
    expect(storageSection).not.toContain('Reset local cache');
    expect(appCssSource).toContain('.sdn-storage-row-actions');
  });

  it('surfaces stalled sync state and enables retry without showing it as ordinary syncing', () => {
    expect(localDataScreenSource).toContain('nextSchemaSyncStallState');
    expect(localDataScreenSource).toContain('isSchemaSyncProgressStalled');
    expect(localDataScreenSource).toContain('progressFingerprint: string | null');
    expect(localDataScreenSource).toContain('lastAdvancedAt: string | null');
    expect(localDataScreenSource).toContain('lastProgressObservedAt: string | null');
    expect(localDataScreenSource).toContain('stallObservationCount: number');
    expect(localDataScreenSource).toContain('stalledSince: string | null');
    expect(localDataScreenSource).toContain('function schemaSyncStalled(schema: SchemaSyncRow): boolean');
    expect(localDataScreenSource).toContain("if (schemaSyncStalled(schema)) return 'Stalled';");
    expect(localDataScreenSource).toContain("if (schemaSyncStalled(schema)) return 'stale';");
    expect(localDataScreenSource).toContain("if (schemaSyncStalled(schema)) return 'Stalled; retry recommended';");
    expect(localDataScreenSource).toContain('disabled={schemaRetryDisabled(schema)}');
    expect(localDataScreenSource).toContain('if (stalled) resetLocalFlatSqlStore();');
  });

  it('renders Subscriptions as filtered access, storage, pinning, and sync management', () => {
    const subscriptionsSection = sourceBetween(localDataScreenSource, 'aria-label="Sync settings"', "{#if selectedDataSection === 'explorer'}");

    expect(localDataScreenSource).toContain("type SubscriptionFilter = 'all' | 'active' | 'trials' | 'expiring' | 'payment-issues' | 'over-quota' | 'canceled' | 'free' | 'paid' | 'usage-based' | 'enterprise'");
    expect(localDataScreenSource).toContain('const SUBSCRIPTION_FILTERS');
    for (const label of ['Active', 'Trials', 'Expiring', 'Payment issues', 'Over quota', 'Canceled', 'Free', 'Paid', 'Usage-based', 'Enterprise']) {
      expect(localDataScreenSource).toContain(`label: '${label}'`);
    }
    expect(localDataScreenSource).toContain('$: filteredSubscriptionRows = filterSubscriptionRows(schemaSyncRows, subscriptionFilter, subscriptionSearchText);');
    expect(localDataScreenSource).toContain("let subscriptionSearchText = '';");
    expect(localDataScreenSource).toContain('function subscriptionMatchesFilter(schema: SchemaSyncRow, filter: SubscriptionFilter): boolean');
    expect(localDataScreenSource).toContain('function subscriptionSearchTextFor(schema: SchemaSyncRow): string');
    expect(localDataScreenSource).toContain('function openSubscriptionDetails(schema: SchemaSyncRow): void');
    expect(localDataScreenSource).toContain('function closeSubscriptionDetails(): void');
    expect(subscriptionsSection).toContain('aria-label="Subscription storage summary"');
    expect(subscriptionsSection).toContain('aria-label="Subscription filters"');
    expect(subscriptionsSection).toContain('aria-label="Search subscriptions"');
    expect(subscriptionsSection).toContain('{#each filteredSubscriptionRows as schema (schema.subscriptionId)}');
    expect(subscriptionsSection).toContain('subscriptionProductLabel(schema)');
    expect(subscriptionsSection).toContain('subscriptionAccessLabel(schema)');
    expect(subscriptionsSection).toContain('subscriptionPlanLabel(schema)');
    expect(subscriptionsSection).toContain('subscriptionCostLabel(schema)');
    expect(subscriptionsSection).toContain('subscriptionStorageStateLabel(schema)');
    expect(subscriptionsSection).toContain('schemaHealthLabel(schema)');
    expect(subscriptionsSection).toContain('handleSubscriptionStorageCapInput(schema, event)');
    expect(subscriptionsSection).toContain('handleSubscriptionQueryProfileChange(schema, event)');
    expect(subscriptionsSection).toContain('handleSubscriptionFilterInput(schema, event)');
    expect(subscriptionsSection).toContain('class="sdn-subscription-detail-drawer"');
    expect(subscriptionsSection).toContain('class="sdn-subscription-detail-grid"');
    for (const label of ['Access', 'Storage', 'Pinning', 'Sync', 'Freshness', 'Health']) {
      expect(subscriptionsSection).toContain(`<span>${label}</span>`);
    }
    expect(appCssSource).toContain('.sdn-subscription-filter-bar');
    expect(appCssSource).toContain('.sdn-subscription-detail-drawer');
    expect(appCssSource).toContain('.sdn-subscription-detail-grid');
  });

  it('keeps activity data as cached subscription/store state instead of a separate interface', () => {
    const subscriptionsSection = sourceBetween(localDataScreenSource, 'aria-label="Sync settings"', "{#if selectedDataSection === 'explorer'}");

    expect(localDataScreenSource).toContain('interface DataActivityRow');
    expect(localDataScreenSource).toContain('$: activityRows = dataPageCacheActive && cachedDataPageView ? cachedDataPageView.activityRows : buildDataActivityRows(schemaSyncRows, dataCatalogRows);');
    expect(localDataScreenSource).toContain('function buildDataActivityRows(');
    expect(localDataScreenSource).toContain('activitySortTime');
    expect(localDataScreenSource).toContain("eventType: 'Sync error'");
    expect(localDataScreenSource).toContain("catalogRow.access.state === 'payment-failed' || catalogRow.access.state === 'over-quota' ? 'Billing' : 'Access'");
    expect(localDataScreenSource).toContain("eventType: 'Subscription'");
    expect(localDataScreenSource).toContain("eventType: 'Verification'");
    expect(localDataScreenSource).toContain("eventType: 'Retry'");
    expect(subscriptionsSection).toContain('retrySubscriptionSync(schema)');
    expect(subscriptionsSection).toContain('schemaRetryDisabled(schema)');
    expect(subscriptionsSection).toContain('schema.progress.error');
    expect(localDataScreenSource).not.toContain('aria-label="Data activity"');
  });

  it('uses compact sync status bubbles with hover details instead of verbose row timing text', () => {
    expect(localDataScreenSource).toContain('syncBubbleLetter');
    expect(localDataScreenSource).toContain('syncBubbleTooltip');
    expect(localDataScreenSource).toContain('class="sdn-sync-bubble"');
    expect(localDataScreenSource).toContain('class="sdn-sync-cell"');
    expect(localDataScreenSource).toContain('data-tooltip={syncBubbleTooltip(schema)}');
    expect(localDataScreenSource).toContain('data-tooltip={catalogRowSyncBubbleTooltip(row)}');
    expect(localDataScreenSource).not.toContain('<span>{schemaTimingLabel(schema)}</span>');
    expect(localDataScreenSource).not.toContain('<span>Next sync attempt: {nextSyncAttemptLabel(schema)}</span>');
    expect(appCssSource).toContain('.sdn-sync-bubble');
    expect(appCssSource).toContain('.sdn-sync-cell');
    expect(appCssSource).toContain('.sdn-sync-bubble[data-tooltip]:hover::after');
    expect(appCssSource).toContain('right: calc(100% + 0.55rem)');
    expect(appCssSource).toContain('transform: translate(0.25rem, -50%)');
    expect(appCssSource).not.toContain('top: calc(100% + 0.45rem)');
    expect(appCssSource).toMatch(/\.sdn-sync-bubble\s*{[^}]*border-radius:\s*var\(--sdn-radius-sm\)/s);
    expect(appCssSource).not.toMatch(/\.sdn-sync-bubble\s*{[^}]*border-radius:\s*50%/s);
  });

  it('uses subscription sync preferences when the scheduler starts a data feed', () => {
    const syncFunction = sourceBetween(localDataScreenSource, 'async function synchronizeSchema', 'function applyWorkerSchemaSyncUpdate');

    expect(syncFunction).toContain('subscriptionSchemaSyncPreference(subscription');
    expect(syncFunction).not.toContain('const preference = schemaSyncPreferenceFor(dataSourceId, standardId, datastoreKey);');
  });

  it('lets each subscription choose append-only or replace-snapshot retention', () => {
    const syncFunction = sourceBetween(localDataScreenSource, 'async function synchronizeSchema', 'function applyWorkerSchemaSyncUpdate');

    expect(localDataScreenSource).toContain('DATA_RETENTION_POLICIES');
    expect(localDataScreenSource).toContain('handleSubscriptionRetentionPolicyChange');
    expect(localDataScreenSource).toContain('aria-label={`${schema.id} retention policy`}');
    expect(localDataScreenSource).toContain('value={schema.retentionPolicy}');
    expect(syncFunction).toContain('const retentionPolicy = subscriptionRetentionPolicyFor(subscription, standardId);');
    expect(syncFunction).toContain('retentionPolicyRequiresReset(initialProgress, retentionPolicy)');
    expect(syncFunction).toContain('clearLocalFlatSqlStore({');
  });
});

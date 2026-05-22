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
  it('exposes the paid-aware data navigation sections', () => {
    for (const label of [
      'Overview',
      'Catalog',
      'My Subscriptions',
      'Sources',
      'Message Types',
      'Storage',
      'Billing',
      'Activity',
      'Explorer',
    ]) {
      expect(localDataScreenSource).toContain(`label: '${label}'`);
    }
  });

  it('does not hard-code marketplace pricing examples into the client', () => {
    expect(localDataScreenSource).not.toMatch(/\$(248|49|99)\/?(mo)?/);
  });

  it('renders the paid-aware overview visualization panels', () => {
    expect(localDataScreenSource).toContain('sdn-overview-visuals');
    expect(localDataScreenSource).toContain('sdn-overview-storage-panel');
    expect(localDataScreenSource).toContain('Storage by');
    expect(localDataScreenSource).toContain('Cost and storage by provider');
    expect(localDataScreenSource).not.toContain('Access coverage');
    expect(localDataScreenSource).not.toContain('sdn-access-coverage-grid');
    expect(localDataScreenSource).not.toContain('selectOverviewCoverageCell');
    expect(appCssSource).toMatch(/\.sdn-overview-panel\s*{[^}]*min-height:\s*16\.25rem/s);
    expect(appCssSource).toMatch(/\.sdn-overview-storage-panel \.sdn-storage-access-visual\s*{[^}]*grid-template-columns:\s*minmax\(10\.75rem,\s*12rem\)\s+minmax\(0,\s*1fr\)/s);
    expect(appCssSource).toMatch(/\.sdn-overview-storage-panel \.sdn-storage-donut\s*{[^}]*grid-column:\s*1/s);
    expect(appCssSource).toMatch(/\.sdn-overview-storage-panel \.sdn-storage-donut\s*{[^}]*justify-self:\s*start/s);
    expect(appCssSource).toMatch(/\.sdn-overview-storage-panel \.sdn-storage-legend\s*{[^}]*grid-column:\s*2/s);
  });

  it('renders catalog filters for query, access, sync, and storage state', () => {
    expect(localDataScreenSource).toContain('sdn-catalog-filters');
    expect(localDataScreenSource).toContain('catalogSearchText');
    expect(localDataScreenSource).toContain('catalogAccessFilter');
    expect(localDataScreenSource).toContain('catalogSyncFilter');
    expect(localDataScreenSource).toContain('catalogStorageFilter');
  });

  it('does not render separate Plan or Actions columns in data tables', () => {
    const overviewProductsTable = sourceBetween(localDataScreenSource, 'aria-label="Data products"', "{#if selectedDataSection === 'catalog'}");
    const catalogProductsTable = sourceBetween(localDataScreenSource, 'aria-label="Catalog data products"', "{#if selectedDataSection === 'sources'}");

    for (const tableSource of [overviewProductsTable, catalogProductsTable]) {
      expect(tableSource).not.toContain('<th>Plan</th>');
      expect(tableSource).not.toContain('<th>Actions</th>');
    }
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

  it('does not render expandable action sub-rows in the overview data products table', () => {
    const overviewSection = sourceBetween(localDataScreenSource, 'aria-label="Data overview"', "{#if selectedDataSection === 'catalog'}");
    const overviewProductsTable = sourceBetween(localDataScreenSource, 'aria-label="Data products"', "{#if selectedDataSection === 'catalog'}");

    expect(localDataScreenSource).toContain("let overviewTableSearchText = '';");
    expect(localDataScreenSource).toContain('$: filteredOverviewDataCatalogRows = filterDataCatalogRows(dataCatalogRows, { query: overviewTableSearchText });');
    expect(overviewSection).toContain('aria-label="Search data products"');
    expect(overviewSection).toContain('placeholder="Search"');
    expect(overviewProductsTable).toContain('as row (catalogRowKey(row))}');
    expect(overviewProductsTable).toContain('{#each filteredOverviewDataCatalogRows as row');
    expect(overviewProductsTable).toContain('{#each');
    expect(overviewProductsTable).not.toContain('on:click|stopPropagation={() => toggleCatalogRowActions(row)}');
    expect(overviewProductsTable).not.toContain('class="sdn-catalog-action-panel"');
    expect(overviewProductsTable).not.toContain('sdn-catalog-action-row');
    expect(overviewProductsTable).not.toContain('aria-expanded={catalogRowActionsExpanded(row)}');
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

  it('uses record-table formatting and expandable row actions for catalog data products', () => {
    const catalogProductsTable = sourceBetween(localDataScreenSource, 'aria-label="Catalog data products"', "{#if selectedDataSection === 'sources'}");

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

  it('renders catalog renewal with renewal timing and unit price instead of quota text', () => {
    const catalogProductsTable = sourceBetween(localDataScreenSource, 'aria-label="Catalog data products"', "{#if selectedDataSection === 'sources'}");

    expect(catalogProductsTable).toContain('<th>Renewal</th>');
    expect(catalogProductsTable).not.toContain('<th>Renewal / quota</th>');
    expect(catalogProductsTable).not.toContain('<th>Price / usage</th>');
    expect(catalogProductsTable).toContain('<strong>{row.plan.renewalLabel}</strong>');
    expect(catalogProductsTable).toContain('<span>{row.plan.priceLabel}</span>');
    expect(catalogProductsTable).not.toContain('row.plan.quotaLabel');
  });

  it('renders billing state only when backend billing facts exist', () => {
    const overviewSection = sourceBetween(localDataScreenSource, 'aria-label="Data overview"', "{#if selectedDataSection === 'catalog'}");
    const billingSection = sourceBetween(localDataScreenSource, 'aria-label="Billing"', "{#if selectedDataSection === 'activity'}");

    expect(localDataScreenSource).toContain('catalogRowHasBillingData');
    expect(localDataScreenSource).toContain('$: billingDataRows = dataPageCacheActive && cachedDataPageView ? cachedDataPageView.billingDataRows : dataCatalogRows.filter(catalogRowHasBillingData);');
    expect(localDataScreenSource).toContain('buildDataBillingProviderRows');
    expect(localDataScreenSource).toContain('$: billingProviderRows = dataPageCacheActive && cachedDataPageView ? cachedDataPageView.billingProviderRows : buildDataBillingProviderRows(dataCatalogRows);');
    expect(overviewSection).toContain('dataCatalogSummary.billingMetricTitle');
    expect(overviewSection).toContain('dataCatalogSummary.billingMetricValue');
    expect(overviewSection).not.toContain('<span>Monthly spend</span>');
    expect(billingSection).toContain('{#if dataCatalogSummary.hasBillingData}');
    expect(billingSection).toContain('aria-label="Spend by provider"');
    expect(billingSection).toContain('{#each billingProviderRows as provider}');
    expect(billingSection).toContain('provider.priceLabel');
    expect(billingSection).toContain('provider.renewalLabel');
    expect(billingSection).toContain('aria-label="Usage and renewals"');
    expect(billingSection).toContain('{#each billingDataRows as row}');
    expect(billingSection).toContain('No paid subscriptions');
    expect(billingSection).toContain('Billing data is not available from this backend.');
    expect(billingSection).not.toContain('Budget');
    expect(billingSection).not.toContain('bind:value');
    expect(billingSection).not.toContain('<input');
    expect(billingSection).not.toContain('Monthly spend');
  });

  it('renders message types as sorted schema health rows with direct manage and explorer actions', () => {
    const messageTypesTable = sourceBetween(localDataScreenSource, 'aria-label="Message types"', "{#if selectedDataSection === 'billing'}");

    expect(localDataScreenSource).toContain('$: messageTypeRows = dataPageCacheActive && cachedDataPageView ? cachedDataPageView.messageTypeRows : sortedMessageTypeRows(schemaSyncRows);');
    expect(localDataScreenSource).toContain('function sortedMessageTypeRows(rows: SchemaSyncRow[]): SchemaSyncRow[]');
    expect(localDataScreenSource).toContain('rightRows - leftRows');
    expect(localDataScreenSource).toContain('function manageSchemaSubscription(schema: SchemaSyncRow): void');
    expect(messageTypesTable).toContain('{#each messageTypeRows as schema}');
    for (const header of ['<th>Remote</th>', '<th>Local</th>', '<th>Pinned</th>', '<th>Cached</th>', '<th>Freshness</th>', '<th>Sync</th>', '<th>Health</th>']) {
      expect(messageTypesTable).toContain(header);
    }
    expect(messageTypesTable).toContain('schemaRemoteRowsLabel(schema)');
    expect(messageTypesTable).toContain('schemaLocalRowsLabel(schema)');
    expect(messageTypesTable).toContain('schemaPinnedRowsCountLabel(schema)');
    expect(messageTypesTable).toContain('schemaCachedBytesLabel(schema)');
    expect(messageTypesTable).toContain('schemaLastSyncedLabel(schema)');
    expect(messageTypesTable).toContain('schemaHealthLabel(schema)');
    expect(messageTypesTable).toContain('openSchemaInExplorer(schema)');
    expect(messageTypesTable).toContain('manageSchemaSubscription(schema)');
    expect(messageTypesTable).toContain('retrySubscriptionSync(schema)');
  });

  it('keeps data sources as read-only subscribed/configured provenance rows', () => {
    const sourcesSection = sourceBetween(localDataScreenSource, 'aria-label="Data sources"', "{#if selectedDataSection === 'message-types'}");

    expect(localDataScreenSource).toContain('$: sourceProvenanceRows = dataPageCacheActive && cachedDataPageView ? cachedDataPageView.sourceProvenanceRows : buildSourceProvenanceRows(dataSourceOptions, schemaSyncRows, dataDirectoryState.peerTrust);');
    expect(localDataScreenSource).toContain('interface SourceProvenanceRow');
    expect(localDataScreenSource).toContain('function buildSourceProvenanceRows(');
    expect(sourcesSection).toContain('{#each sourceProvenanceRows as source (source.id)}');
    expect(sourcesSection).not.toContain('{#each dataSourceOptions as source}');
    expect(sourcesSection).not.toContain('Search');
    expect(sourcesSection).not.toContain('Directory');
    for (const header of ['<th>Provider</th>', '<th>Peer ID</th>', '<th>Public key</th>', '<th>Source / datastore</th>', '<th>Trust / access</th>', '<th>Products</th>']) {
      expect(sourcesSection).toContain(header);
    }
    expect(sourcesSection).toContain('{source.providerName}');
    expect(sourcesSection).toContain('{source.sourceDatastoreLabel}');
    expect(sourcesSection).toContain('{source.trustAccessLabel}');
    expect(sourcesSection).toContain('{source.productsLabel}');
    expect(sourcesSection).toContain('{source.rowsLabel}');
    expect(sourcesSection).toContain('shorten(source.peerId ??');
    expect(sourcesSection).toContain('shorten(source.publicKey ??');
  });

  it('renders Storage as active feed rows with scoped reset and sync schedule state', () => {
    const storageSection = sourceBetween(localDataScreenSource, 'aria-label="Local storage state"', "{#if selectedDataSection === 'subscriptions'}");

    expect(storageSection).toContain('{#each activeStorageRows as schema}');
    expect(storageSection).toContain('schemaRemoteRowsLabel(schema)');
    expect(storageSection).toContain('schemaLocalRowsLabel(schema)');
    expect(storageSection).toContain('schemaPinnedRowsLabel(schema)');
    expect(storageSection).toContain('schemaCachedBytesLabel(schema)');
    expect(storageSection).toContain('schemaDownloadSpeedLabel(schema)');
    expect(storageSection).toContain('schemaStoragePressureLabel(schema)');
    expect(storageSection).toContain('schemaRetentionPolicyLabel(schema)');
    expect(storageSection).toContain('nextSyncAttemptLabel(schema)');
    expect(storageSection).toContain('schemaLastSyncedLabel(schema)');
    expect(storageSection).toContain('class="sdn-storage-row-actions"');
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

  it('renders My Subscriptions as filtered access, storage, pinning, and sync management', () => {
    const subscriptionsSection = sourceBetween(localDataScreenSource, 'aria-label="Sync settings"', "{#if selectedDataSection === 'explorer'}");

    expect(localDataScreenSource).toContain("type SubscriptionFilter = 'all' | 'active' | 'trials' | 'expiring' | 'payment-issues' | 'over-quota' | 'canceled' | 'free' | 'paid' | 'usage-based' | 'enterprise'");
    expect(localDataScreenSource).toContain('const SUBSCRIPTION_FILTERS');
    for (const label of ['Active', 'Trials', 'Expiring', 'Payment issues', 'Over quota', 'Canceled', 'Free', 'Paid', 'Usage-based', 'Enterprise']) {
      expect(localDataScreenSource).toContain(`label: '${label}'`);
    }
    expect(localDataScreenSource).toContain('$: filteredSubscriptionRows = filterSubscriptionRows(schemaSyncRows, subscriptionFilter);');
    expect(localDataScreenSource).toContain('function subscriptionMatchesFilter(schema: SchemaSyncRow, filter: SubscriptionFilter): boolean');
    expect(localDataScreenSource).toContain('function openSubscriptionDetails(schema: SchemaSyncRow): void');
    expect(localDataScreenSource).toContain('function closeSubscriptionDetails(): void');
    expect(subscriptionsSection).toContain('aria-label="Subscription filters"');
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

  it('renders Activity as chronological feed events with retry affordances', () => {
    const activitySection = sourceBetween(localDataScreenSource, 'aria-label="Data activity"', "{#if selectedDataSection === 'storage'}");

    expect(localDataScreenSource).toContain('interface DataActivityRow');
    expect(localDataScreenSource).toContain('$: activityRows = dataPageCacheActive && cachedDataPageView ? cachedDataPageView.activityRows : buildDataActivityRows(schemaSyncRows, dataCatalogRows);');
    expect(localDataScreenSource).toContain('function buildDataActivityRows(');
    expect(localDataScreenSource).toContain('activitySortTime');
    expect(localDataScreenSource).toContain("eventType: 'Sync error'");
    expect(localDataScreenSource).toContain("catalogRow.access.state === 'payment-failed' || catalogRow.access.state === 'over-quota' ? 'Billing' : 'Access'");
    expect(localDataScreenSource).toContain("eventType: 'Subscription'");
    expect(localDataScreenSource).toContain("eventType: 'Verification'");
    expect(localDataScreenSource).toContain("eventType: 'Retry'");
    expect(activitySection).toContain('{#each activityRows as activity (activity.id)}');
    for (const header of ['<th>Event</th>', '<th>Message type</th>', '<th>Provider</th>', '<th>Status</th>', '<th>Detail</th>', '<th>Next attempt</th>', '<th>When</th>', '<th>Action</th>']) {
      expect(activitySection).toContain(header);
    }
    expect(activitySection).toContain('activity.retrySchema');
    expect(activitySection).toContain('retryActivitySync(activity)');
    expect(activitySection).toContain('activityRetryDisabled(activity)');
    expect(activitySection).toContain('No activity.');
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

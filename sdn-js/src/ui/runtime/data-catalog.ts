export type CatalogAccessState =
  | 'free'
  | 'paid-active'
  | 'trial'
  | 'locked'
  | 'expired'
  | 'over-quota'
  | 'payment-failed'
  | 'unknown';

export type CatalogSyncStatus = 'idle' | 'syncing' | 'synced' | 'queued' | 'stale' | 'failed' | 'capped';
export type CatalogStorageUnit = 'MB' | 'GB' | 'TB';

export interface DataCatalogInput {
  subscriptionId?: string | null;
  dataSourceId?: string | null;
  datastoreKey?: string | null;
  standardId: string;
  providerName: string;
  providerPeerId: string | null;
  providerPublicKey: string | null;
  remoteRows: number;
  localRows: number;
  pinnedRows: number;
  cachedBytes: number;
  storageCap: number;
  storageUnit: CatalogStorageUnit;
  syncStatus: string;
  nextSyncAttempt: string;
  lastSyncedAt: string | null;
  syncFilter: string;
  accessState?: CatalogAccessState;
  planLabel?: string | null;
  priceLabel?: string | null;
  renewalLabel?: string | null;
  quotaLabel?: string | null;
}

export interface DataCatalogRow {
  id: string;
  subscriptionId: string | null;
  dataSourceId: string | null;
  datastoreKey: string | null;
  provider: string;
  product: string;
  messageTypes: string[];
  access: {
    state: CatalogAccessState;
    label: string;
  };
  plan: {
    label: string;
    priceLabel: string;
    renewalLabel: string;
    quotaLabel: string;
  };
  storage: {
    localRows: number;
    remoteRows: number;
    pinnedRows: number;
    cachedBytes: number;
    storageCap: number;
    storageUnit: CatalogStorageUnit;
    policyLabel: string;
    filterLabel: string;
  };
  sync: {
    status: CatalogSyncStatus;
    label: string;
    nextAttempt: string;
    lastSyncedAt: string | null;
  };
  providerPeerId: string | null;
  providerPublicKey: string | null;
}

export interface DataCatalogSummary {
  totalProducts: number;
  freeProducts: number;
  activePaidSubscriptions: number;
  trialSubscriptions: number;
  lockedProducts: number;
  issueCount: number;
  syncingProducts: number;
  syncedProducts: number;
  queuedProducts: number;
  localStorageBytes: number;
  pinnedRows: number;
  hasBillingData: boolean;
  billingMetricTitle: string;
  billingMetricValue: string;
  monthlySpendLabel: string;
  dataHealthLabel: string;
}

export interface DataOverviewStorageSegment {
  key: string;
  label: string;
  bytes: number;
  percent: number;
}

export type DataOverviewStorageGroup = 'provider' | 'messageType' | 'access';

export interface DataOverviewCoverageCell {
  messageType: string;
  accessState: CatalogAccessState;
  accessLabel: string;
  syncStatus: CatalogSyncStatus;
  syncLabel: string;
  localRows: number;
  remoteRows: number;
  cachedBytes: number;
}

export interface DataOverviewCoverageRow {
  provider: string;
  providerPeerId: string | null;
  providerPublicKey: string | null;
  cells: DataOverviewCoverageCell[];
}

export interface DataOverviewProviderBar {
  provider: string;
  providerPeerId: string | null;
  providerPublicKey: string | null;
  localBytes: number;
  pinnedRows: number;
  products: number;
  activePaidSubscriptions: number;
  trialSubscriptions: number;
  planLabels: string[];
  percent: number;
}

export interface DataOverviewVisuals {
  storageTotalBytes: number;
  storageSegments: DataOverviewStorageSegment[];
  storageSegmentsByGroup: Record<DataOverviewStorageGroup, DataOverviewStorageSegment[]>;
  coverageRows: DataOverviewCoverageRow[];
  providerBars: DataOverviewProviderBar[];
  monthlySpendLabel: string;
}

export interface DataBillingProviderRow {
  key: string;
  provider: string;
  providerPeerId: string | null;
  providerPublicKey: string | null;
  productCount: number;
  productLabel: string;
  priceLabel: string;
  renewalLabel: string;
  quotaLabel: string;
}

export type DataCatalogAccessFilter = 'all' | 'paid' | 'issues' | CatalogAccessState;
export type DataCatalogSyncFilter = 'all' | 'issues' | CatalogSyncStatus;
export type DataCatalogStorageFilter = 'all' | 'stored' | 'not-stored';

export interface DataCatalogFilters {
  query?: string;
  access?: DataCatalogAccessFilter;
  sync?: DataCatalogSyncFilter;
  storage?: DataCatalogStorageFilter;
}

export function buildDataCatalogRows(inputs: DataCatalogInput[]): DataCatalogRow[] {
  return inputs.map((input) => {
    const standardId = normalizeStandardId(input.standardId);
    const accessState = normalizeAccessState(input.accessState);
    const syncStatus = normalizeSyncStatus(input.syncStatus, input.localRows, input.remoteRows);
    const subscriptionId = normalizedOptionalString(input.subscriptionId);
    const datastoreKey = normalizedOptionalString(input.datastoreKey);
    const rowIdentity = subscriptionId
      ?? [input.providerPeerId ?? input.providerName, datastoreKey, standardId].filter(Boolean).join(':');
    return {
      id: rowIdentity,
      subscriptionId,
      dataSourceId: normalizedOptionalString(input.dataSourceId),
      datastoreKey,
      provider: input.providerName.trim() || input.providerPeerId || 'Unknown provider',
      product: `${standardId} Feed`,
      messageTypes: [standardId],
      access: {
        state: accessState,
        label: accessLabel(accessState),
      },
      plan: {
        label: normalizedOptionalString(input.planLabel) ?? (accessState === 'free' ? 'No paid plan' : 'Plan unavailable'),
        priceLabel: normalizedOptionalString(input.priceLabel) ?? (accessState === 'free' ? 'Free' : 'Not available'),
        renewalLabel: normalizedOptionalString(input.renewalLabel) ?? 'No renewal',
        quotaLabel: normalizedOptionalString(input.quotaLabel) ?? 'No quota reported',
      },
      storage: {
        localRows: normalizedCount(input.localRows),
        remoteRows: normalizedCount(input.remoteRows),
        pinnedRows: normalizedCount(input.pinnedRows),
        cachedBytes: normalizedCount(input.cachedBytes),
        storageCap: normalizedCap(input.storageCap),
        storageUnit: normalizeStorageUnit(input.storageUnit),
        policyLabel: `${formatCap(input.storageCap)} ${normalizeStorageUnit(input.storageUnit)} cap`,
        filterLabel: input.syncFilter.trim() ? 'Filtered' : 'All records',
      },
      sync: {
        status: syncStatus,
        label: syncLabel(syncStatus),
        nextAttempt: input.nextSyncAttempt.trim() || 'Not scheduled',
        lastSyncedAt: normalizedOptionalString(input.lastSyncedAt),
      },
      providerPeerId: normalizedOptionalString(input.providerPeerId),
      providerPublicKey: normalizedOptionalString(input.providerPublicKey),
    };
  });
}

export function buildDataOverviewVisuals(rows: DataCatalogRow[], storageGroup: DataOverviewStorageGroup = 'provider'): DataOverviewVisuals {
  const summary = summarizeDataCatalog(rows);
  const storageBytesByProvider = new Map<string, { label: string; bytes: number }>();
  const storageBytesByMessageType = new Map<string, { label: string; bytes: number }>();
  const storageBytesByAccess = new Map<string, { label: string; bytes: number }>();
  const providers = new Map<string, {
    provider: string;
    providerPeerId: string | null;
    providerPublicKey: string | null;
    localBytes: number;
    pinnedRows: number;
    products: number;
    activePaidSubscriptions: number;
    trialSubscriptions: number;
    planLabels: Set<string>;
    cells: DataOverviewCoverageCell[];
  }>();

  for (const row of rows) {
    if (row.storage.cachedBytes > 0) {
      addStorageBytes(storageBytesByProvider, `provider:${row.provider}`, row.provider, row.storage.cachedBytes);
      addStorageBytes(storageBytesByAccess, row.access.state, row.access.label, row.storage.cachedBytes);
      const messageTypes = row.messageTypes.length > 0 ? row.messageTypes : [row.product];
      const messageTypeBytes = row.storage.cachedBytes / messageTypes.length;
      for (const messageType of messageTypes) {
        addStorageBytes(storageBytesByMessageType, messageType, messageType, messageTypeBytes);
      }
    }
    const providerKey = `${row.providerPeerId ?? row.provider}:${row.providerPublicKey ?? ''}`;
    const provider = providers.get(providerKey) ?? {
      provider: row.provider,
      providerPeerId: row.providerPeerId,
      providerPublicKey: row.providerPublicKey,
      localBytes: 0,
      pinnedRows: 0,
      products: 0,
      activePaidSubscriptions: 0,
      trialSubscriptions: 0,
      planLabels: new Set<string>(),
      cells: [],
    };
    provider.localBytes += row.storage.cachedBytes;
    provider.pinnedRows += row.storage.pinnedRows;
    provider.products += 1;
    if (row.access.state === 'paid-active') provider.activePaidSubscriptions += 1;
    if (row.access.state === 'trial') provider.trialSubscriptions += 1;
    provider.planLabels.add(row.plan.label);
    for (const messageType of row.messageTypes) {
      provider.cells.push({
        messageType,
        accessState: row.access.state,
        accessLabel: row.access.label,
        syncStatus: row.sync.status,
        syncLabel: row.sync.label,
        localRows: row.storage.localRows,
        remoteRows: row.storage.remoteRows,
        cachedBytes: row.storage.cachedBytes,
      });
    }
    providers.set(providerKey, provider);
  }

  const storageTotalBytes = rows.reduce((total, row) => total + row.storage.cachedBytes, 0);
  const storageSegmentsByGroup = {
    provider: storageSegmentsFromMap(storageBytesByProvider, storageTotalBytes),
    messageType: storageSegmentsFromMap(storageBytesByMessageType, storageTotalBytes),
    access: storageSegmentsFromMap(storageBytesByAccess, storageTotalBytes),
  };
  const storageSegments = storageSegmentsByGroup[storageGroup] ?? storageSegmentsByGroup.provider;

  const coverageRows = Array.from(providers.values())
    .map((provider) => ({
      provider: provider.provider,
      providerPeerId: provider.providerPeerId,
      providerPublicKey: provider.providerPublicKey,
      cells: provider.cells
        .sort((left, right) => (
          accessSortRank(left.accessState) - accessSortRank(right.accessState)
          || left.messageType.localeCompare(right.messageType)
        )),
    }))
    .sort((left, right) => {
      const leftBytes = left.cells.reduce((total, cell) => total + cell.cachedBytes, 0);
      const rightBytes = right.cells.reduce((total, cell) => total + cell.cachedBytes, 0);
      return rightBytes - leftBytes || left.provider.localeCompare(right.provider);
    });

  const maxProviderBytes = Math.max(0, ...Array.from(providers.values()).map((provider) => provider.localBytes));
  const providerBars = Array.from(providers.values())
    .map((provider) => ({
      provider: provider.provider,
      providerPeerId: provider.providerPeerId,
      providerPublicKey: provider.providerPublicKey,
      localBytes: provider.localBytes,
      pinnedRows: provider.pinnedRows,
      products: provider.products,
      activePaidSubscriptions: provider.activePaidSubscriptions,
      trialSubscriptions: provider.trialSubscriptions,
      planLabels: sortedPlanLabels(provider.planLabels),
      percent: percentageOf(provider.localBytes, maxProviderBytes),
    }))
    .sort((left, right) => right.localBytes - left.localBytes || left.provider.localeCompare(right.provider));

  return {
    storageTotalBytes,
    storageSegments,
    storageSegmentsByGroup,
    coverageRows,
    providerBars,
    monthlySpendLabel: summary.monthlySpendLabel,
  };
}

function addStorageBytes(target: Map<string, { label: string; bytes: number }>, key: string, label: string, bytes: number): void {
  const existing = target.get(key);
  if (existing) {
    existing.bytes += bytes;
    return;
  }
  target.set(key, { label, bytes });
}

function storageSegmentsFromMap(
  source: Map<string, { label: string; bytes: number }>,
  totalBytes: number,
): DataOverviewStorageSegment[] {
  return Array.from(source.entries())
    .map(([key, value]) => ({
      key,
      label: value.label,
      bytes: value.bytes,
      percent: percentageOf(value.bytes, totalBytes),
    }))
    .sort((left, right) => right.bytes - left.bytes || left.label.localeCompare(right.label));
}

export function filterDataCatalogRows(rows: DataCatalogRow[], filters: DataCatalogFilters = {}): DataCatalogRow[] {
  const terms = String(filters.query ?? '')
    .trim()
    .toLowerCase()
    .split(/\s+/)
    .filter(Boolean);
  const accessFilter = filters.access ?? 'all';
  const syncFilter = filters.sync ?? 'all';
  const storageFilter = filters.storage ?? 'all';
  return rows.filter((row) => {
    if (terms.length > 0 && !terms.every((term) => catalogRowSearchText(row).includes(term))) return false;
    if (!catalogRowMatchesAccessFilter(row, accessFilter)) return false;
    if (!catalogRowMatchesSyncFilter(row, syncFilter)) return false;
    if (!catalogRowMatchesStorageFilter(row, storageFilter)) return false;
    return true;
  });
}

export function summarizeDataCatalog(rows: DataCatalogRow[]): DataCatalogSummary {
  const activePaidSubscriptions = rows.filter((row) => row.access.state === 'paid-active').length;
  const trialSubscriptions = rows.filter((row) => row.access.state === 'trial').length;
  const billingRows = rows.filter(catalogRowHasBillingData);
  const hasBillingData = billingRows.length > 0;
  const billingSpendLabel = billingSpendSummaryLabel(billingRows);
  const issueCount = rows.filter((row) => (
    row.access.state === 'expired'
    || row.access.state === 'over-quota'
    || row.access.state === 'payment-failed'
    || row.sync.status === 'failed'
  )).length;
  const syncedProducts = rows.filter((row) => row.sync.status === 'synced').length;
  const syncingProducts = rows.filter((row) => row.sync.status === 'syncing').length;
  const queuedProducts = rows.filter((row) => row.sync.status === 'queued').length;
  return {
    totalProducts: rows.length,
    freeProducts: rows.filter((row) => row.access.state === 'free').length,
    activePaidSubscriptions,
    trialSubscriptions,
    lockedProducts: rows.filter((row) => row.access.state === 'locked').length,
    issueCount,
    syncingProducts,
    syncedProducts,
    queuedProducts,
    localStorageBytes: rows.reduce((total, row) => total + row.storage.cachedBytes, 0),
    pinnedRows: rows.reduce((total, row) => total + row.storage.pinnedRows, 0),
    hasBillingData,
    billingMetricTitle: hasBillingData ? 'Current period spend' : 'Paid subscriptions',
    billingMetricValue: hasBillingData
      ? billingSpendLabel
      : activePaidSubscriptions > 0
        ? `${activePaidSubscriptions} active paid · billing unavailable`
        : 'No paid subscriptions',
    monthlySpendLabel: hasBillingData
      ? billingSpendLabel
      : activePaidSubscriptions > 0 ? 'Billing data unavailable' : 'No paid subscriptions',
    dataHealthLabel: issueCount > 0
      ? `${issueCount} need attention`
      : `${syncedProducts} synced / ${queuedProducts} queued`,
  };
}

export function catalogRowHasBillingData(row: DataCatalogRow): boolean {
  return isCommercialAccessState(row.access.state)
    && (
      hasMeaningfulPriceLabel(row.plan.priceLabel)
      || hasMeaningfulRenewalLabel(row.plan.renewalLabel)
      || hasMeaningfulQuotaLabel(row.plan.quotaLabel)
    );
}

export function buildDataBillingProviderRows(rows: DataCatalogRow[]): DataBillingProviderRow[] {
  const providers = new Map<string, {
    provider: string;
    providerPeerId: string | null;
    providerPublicKey: string | null;
    productIds: Set<string>;
    prices: Set<string>;
    renewals: Set<string>;
    quotas: Set<string>;
  }>();

  for (const row of rows) {
    if (!catalogRowHasBillingData(row)) continue;
    const key = `${row.providerPeerId ?? row.provider}:${row.providerPublicKey ?? ''}`;
    const provider = providers.get(key) ?? {
      provider: row.provider,
      providerPeerId: row.providerPeerId,
      providerPublicKey: row.providerPublicKey,
      productIds: new Set<string>(),
      prices: new Set<string>(),
      renewals: new Set<string>(),
      quotas: new Set<string>(),
    };
    provider.productIds.add(row.id || `${row.provider}:${row.product}`);
    if (hasMeaningfulPriceLabel(row.plan.priceLabel)) provider.prices.add(row.plan.priceLabel);
    if (hasMeaningfulRenewalLabel(row.plan.renewalLabel)) provider.renewals.add(row.plan.renewalLabel);
    if (hasMeaningfulQuotaLabel(row.plan.quotaLabel)) provider.quotas.add(row.plan.quotaLabel);
    providers.set(key, provider);
  }

  return Array.from(providers.entries())
    .map(([key, provider]) => {
      const productCount = provider.productIds.size;
      return {
        key,
        provider: provider.provider,
        providerPeerId: provider.providerPeerId,
        providerPublicKey: provider.providerPublicKey,
        productCount,
        productLabel: `${productCount} billed ${productCount === 1 ? 'product' : 'products'}`,
        priceLabel: joinedOrUnavailable(provider.prices, 'Pricing unavailable'),
        renewalLabel: joinedOrUnavailable(provider.renewals, 'No renewal data'),
        quotaLabel: joinedOrUnavailable(provider.quotas, 'No quota data'),
      };
    })
    .sort((left, right) => right.productCount - left.productCount || left.provider.localeCompare(right.provider));
}

function catalogRowSearchText(row: DataCatalogRow): string {
  return [
    row.provider,
    row.product,
    row.messageTypes.join(' '),
    row.access.label,
    row.plan.label,
    row.plan.priceLabel,
    row.plan.renewalLabel,
    row.plan.quotaLabel,
    row.sync.label,
    row.sync.nextAttempt,
    row.providerPeerId,
    row.providerPublicKey,
  ].filter(Boolean).join(' ').toLowerCase();
}

function catalogRowMatchesAccessFilter(row: DataCatalogRow, filter: DataCatalogAccessFilter): boolean {
  if (filter === 'all') return true;
  if (filter === 'paid') {
    return row.access.state === 'paid-active'
      || row.access.state === 'locked'
      || row.access.state === 'expired'
      || row.access.state === 'over-quota'
      || row.access.state === 'payment-failed';
  }
  if (filter === 'issues') {
    return row.access.state === 'expired'
      || row.access.state === 'over-quota'
      || row.access.state === 'payment-failed'
      || row.sync.status === 'failed';
  }
  return row.access.state === filter;
}

function catalogRowMatchesSyncFilter(row: DataCatalogRow, filter: DataCatalogSyncFilter): boolean {
  if (filter === 'all') return true;
  if (filter === 'issues') return row.sync.status === 'failed' || row.sync.status === 'stale';
  return row.sync.status === filter;
}

function catalogRowMatchesStorageFilter(row: DataCatalogRow, filter: DataCatalogStorageFilter): boolean {
  if (filter === 'all') return true;
  const stored = row.storage.localRows > 0 || row.storage.cachedBytes > 0 || row.storage.pinnedRows > 0;
  return filter === 'stored' ? stored : !stored;
}

function billingSpendSummaryLabel(rows: DataCatalogRow[]): string {
  const prices = uniqueSortedLabels(rows.map((row) => row.plan.priceLabel).filter(hasMeaningfulPriceLabel));
  if (prices.length === 1) return prices[0];
  if (prices.length > 1) return `${prices.length} priced plans`;
  return 'Billing data available';
}

function uniqueSortedLabels(values: string[]): string[] {
  return Array.from(new Set(values.map((value) => value.trim()).filter(Boolean))).sort((left, right) => left.localeCompare(right));
}

function joinedOrUnavailable(values: Set<string>, fallback: string): string {
  const labels = uniqueSortedLabels(Array.from(values));
  return labels.length > 0 ? labels.join(', ') : fallback;
}

function isCommercialAccessState(state: CatalogAccessState): boolean {
  return state === 'paid-active'
    || state === 'trial'
    || state === 'expired'
    || state === 'over-quota'
    || state === 'payment-failed'
    || state === 'locked';
}

function hasMeaningfulPriceLabel(label: string): boolean {
  const value = label.trim();
  return value !== '' && value !== 'Free' && value !== 'Not available';
}

function hasMeaningfulRenewalLabel(label: string): boolean {
  const value = label.trim();
  return value !== '' && value !== 'No renewal';
}

function hasMeaningfulQuotaLabel(label: string): boolean {
  const value = label.trim();
  return value !== '' && value !== 'No quota reported';
}

function percentageOf(value: number, total: number): number {
  if (!Number.isFinite(value) || !Number.isFinite(total) || value <= 0 || total <= 0) return 0;
  return Math.round((value / total) * 1000) / 10;
}

function accessSortRank(state: CatalogAccessState): number {
  switch (state) {
    case 'payment-failed': return 0;
    case 'over-quota': return 1;
    case 'expired': return 2;
    case 'paid-active': return 3;
    case 'trial': return 4;
    case 'locked': return 5;
    case 'free': return 6;
    case 'unknown':
    default:
      return 7;
  }
}

function sortedPlanLabels(values: Set<string>): string[] {
  const labels = Array.from(values).filter((value) => value.trim());
  const defaultLabels = new Set(['No paid plan', 'Plan unavailable']);
  return labels.sort((left, right) => {
    const leftDefault = defaultLabels.has(left);
    const rightDefault = defaultLabels.has(right);
    if (leftDefault !== rightDefault) return leftDefault ? 1 : -1;
    return left.localeCompare(right);
  });
}

function normalizeAccessState(value: unknown): CatalogAccessState {
  const candidate = String(value ?? '').trim();
  if (
    candidate === 'paid-active'
    || candidate === 'trial'
    || candidate === 'locked'
    || candidate === 'expired'
    || candidate === 'over-quota'
    || candidate === 'payment-failed'
    || candidate === 'unknown'
  ) {
    return candidate;
  }
  return 'free';
}

function accessLabel(state: CatalogAccessState): string {
  switch (state) {
    case 'paid-active': return 'Active paid';
    case 'trial': return 'Trial';
    case 'locked': return 'Locked';
    case 'expired': return 'Expired';
    case 'over-quota': return 'Over quota';
    case 'payment-failed': return 'Payment failed';
    case 'unknown': return 'Unknown';
    case 'free':
    default:
      return 'Free';
  }
}

function normalizeSyncStatus(value: unknown, localRows: number, remoteRows: number): CatalogSyncStatus {
  const candidate = String(value ?? '').trim();
  if (candidate === 'syncing' || candidate === 'synced' || candidate === 'capped') return candidate;
  if (candidate === 'error' || candidate === 'failed') return 'failed';
  if (remoteRows > localRows && localRows > 0) return 'queued';
  return 'idle';
}

function syncLabel(status: CatalogSyncStatus): string {
  switch (status) {
    case 'syncing': return 'Syncing';
    case 'synced': return 'Synced';
    case 'queued': return 'Queued';
    case 'stale': return 'Stale';
    case 'failed': return 'Failed';
    case 'capped': return 'Capped';
    case 'idle':
    default:
      return 'Ready';
  }
}

function normalizeStandardId(value: unknown): string {
  const normalized = String(value ?? '').trim().toUpperCase();
  return normalized || 'DATA';
}

function normalizeStorageUnit(value: unknown): CatalogStorageUnit {
  return value === 'GB' || value === 'TB' ? value : 'MB';
}

function normalizedCount(value: unknown): number {
  const numeric = Number(value);
  if (!Number.isFinite(numeric) || numeric < 0) return 0;
  return Math.floor(numeric);
}

function normalizedCap(value: unknown): number {
  const numeric = Number(value);
  if (!Number.isFinite(numeric) || numeric <= 0) return 1;
  return Math.round(numeric * 10) / 10;
}

function formatCap(value: unknown): string {
  return String(normalizedCap(value));
}

function normalizedOptionalString(value: unknown): string | null {
  return typeof value === 'string' && value.trim() ? value.trim() : null;
}

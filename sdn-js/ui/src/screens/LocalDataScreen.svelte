<script lang="ts">
  import { onDestroy } from 'svelte';
  import { decodeCatFlatBuffer } from '../../../src/ui/runtime/cat-flatbuffer';
  import { decodeEpmFlatBuffer } from '../../../src/ui/runtime/epm-flatbuffer';
  import {
    canonicalizeDataDirectorySourceIds,
    DATA_FEED_RETENTION_POLICIES,
    DEFAULT_DATA_FEED_QUERY_PROFILE,
    defaultDataFeedRetentionPolicy,
    loadDataDirectoryState,
    migrateSchemaSyncPreferencesToDataDirectory,
    normalizeDataFeedRetentionPolicy,
    persistDataDirectoryState,
    updateDataFeedSubscription,
    type DataDirectoryState,
    type DataDirectoryMigrationSource,
    type DataFeedSubscription,
    type DataFeedRetentionPolicy,
  } from '../../../src/ui/runtime/data-directory';
  import {
    buildDataBillingProviderRows,
    buildDataCatalogRows,
    buildDataOverviewVisuals,
    catalogRowHasBillingData,
    filterDataCatalogRows,
    summarizeDataCatalog,
    type DataCatalogAccessFilter,
    type DataBillingProviderRow,
    type DataCatalogRow,
    type DataCatalogSummary,
    type DataCatalogStorageFilter,
    type DataCatalogSyncFilter,
    type DataOverviewProviderBar,
    type DataOverviewStorageGroup,
    type DataOverviewStorageSegment,
    type DataOverviewVisuals,
  } from '../../../src/ui/runtime/data-catalog';
  import {
    clearLocalFlatSqlStore,
    type LocalFlatSqlQueryResult,
    type LocalFlatSqlStandardStats,
  } from '../../../src/ui/runtime/local-flatsql';
  import {
    createWorkerLocalFlatSqlStore,
    type WorkerFlatSqlSyncBackendConfig,
    type WorkerLocalFlatSqlStore,
    type WorkerSchemaSyncUpdate,
  } from '../../../src/ui/runtime/local-flatsql-worker-client';
  import { decodeOmmFlatBuffer } from '../../../src/ui/runtime/omm-flatbuffer';
  import { decodePnmFlatBuffer } from '../../../src/ui/runtime/pnm-flatbuffer';
  import { normalizeIpfsArtifactPeerAddrs } from '../../../src/ui/runtime/ipfs-artifact-peers';
  import { boundedWireSpeedUtilization } from '../../../src/ui/runtime/sync-throughput';
  import {
    isSchemaSyncProgressStalled,
    nextSchemaSyncStallState,
  } from '../../../src/ui/runtime/schema-sync-stall';
  import { preferredDataSummarySource } from '../../../src/ui/runtime/peer-data-feeds';
  import { syncRowCountSummary, syncRowCountSummaryLabel } from '../../../src/ui/runtime/sync-progress';
  import { createSchemaSyncScheduler } from '../lib/schema-sync-scheduler';
  import {
    effectiveSchemaSyncStatus,
    schemaSyncStatusLabel as formatSchemaSyncStatusLabel,
  } from '../lib/schema-sync-labels';
  import { loadingMetricLabel } from '../lib/data-loading-labels';
  import {
    buildLocalDataExplorerQuery,
    isNumericDataExplorerColumn,
    localDataExplorerSearchColumns,
    localDataExplorerCountFromResult,
  } from '../lib/data-explorer-query';
  import type {
    DataScanResult,
    DataSummary,
    ObservedSdnPeer,
    RawDataRecord,
    SdnBackend,
  } from '../../../src/ui/runtime/sdn-backend';
  import { verify as verifyEd25519Signature } from '../../../src/crypto/hd-wallet';
  import CAT_SCHEMA from '../../../node_modules/spacedatastandards.org/schema/CAT/main.fbs?raw';
  import EPM_SCHEMA from '../../../node_modules/spacedatastandards.org/schema/EPM/main.fbs?raw';
  import MPE_SCHEMA from '../../../node_modules/spacedatastandards.org/schema/MPE/main.fbs?raw';
  import OMM_SCHEMA from '../../../node_modules/spacedatastandards.org/schema/OMM/main.fbs?raw';
  import PNM_SCHEMA from '../../../node_modules/spacedatastandards.org/schema/PNM/main.fbs?raw';
  import SPW_SCHEMA from '../../../node_modules/spacedatastandards.org/schema/SPW/main.fbs?raw';

  type SortColumn = string;
  type SortDirection = 'asc' | 'desc';
  type ColumnSource = 'metadata' | 'standard';
  type DataSection = 'store' | 'subscriptions' | 'explorer';
  type SchemaSyncMode = 'preview' | 'sync';
  type SchemaSyncStatus = 'idle' | 'syncing' | 'synced' | 'capped' | 'error';
  type StorageUnit = 'MB' | 'GB' | 'TB';
  type DataQueryProfile = 'ordered-offset-v1' | 'dataset-publication-offset-v1';
  type PinVerifyToastTone = 'success' | 'warning' | 'error';
  type ExplorerSearchMode = 'plain' | 'sql';
  type SyncBubbleTone = 'loading' | 'ready' | 'queued' | 'syncing' | 'synced' | 'stale' | 'capped' | 'failed' | 'paused';
  type SubscriptionFilter = 'all' | 'active' | 'trials' | 'expiring' | 'payment-issues' | 'over-quota' | 'canceled' | 'free' | 'paid' | 'usage-based' | 'enterprise';

  interface WorkbenchColumn {
    key: SortColumn;
    label: string;
    source: ColumnSource;
  }

  interface ColumnKeyEntry {
    key: string;
    abbreviation: string;
    label: string;
  }

  interface WorkbenchRow {
    record: RawDataRecord;
    decoded: Record<string, unknown>;
  }

  interface ConfiguredSdnNode {
    id: string;
    name: string;
    addrs: string[];
    trust_level?: string;
    trustLevel?: string;
    metadata?: Record<string, unknown>;
  }

  interface DataSourceOption {
    id: string;
    label: string;
    detail: string;
    peerId: string | null;
    publicKey: string | null;
    providerId?: string | null;
    sourceName?: string | null;
    kind: 'local' | 'configured';
    syncAddrs?: string[];
    artifactPeerAddrs?: string[];
    searchText: string;
  }

  interface StandardOption {
    id: string;
    remoteRows: number;
  }

  interface SchemaSyncPreference {
    mode: SchemaSyncMode;
    storageCap: number;
    storageUnit: StorageUnit;
  }

  interface SchemaSyncProgress {
    status: SchemaSyncStatus;
    syncedRows: number;
    totalRows: number;
    localRows: number;
    pinnedRows: number;
    missingRows: number;
    cachedBytes: number;
    pinnedBytes: number;
    downloadedBytes: number;
    downloadSpeedBytesPerSecond: number;
    measuredWireSpeedBytesPerSecond: number;
    wireSpeedUtilization: number | null;
    wireSpeedTarget: number;
    wireSpeedTargetMet: boolean | null;
    manifestDiscoveryMs: number;
    networkTransferMs: number;
    verificationMs: number;
    flatSqlMaterializationMs: number;
    providerPeerId: string | null;
    providerPublicKey: string | null;
    snapshotId: string | null;
    head: string | null;
    cursor: string | null;
    nextCursor: string | null;
    highWaterMark: string | null;
    queryProfile: string | null;
    chunkHash: string | null;
    syncProtocol: string | null;
    syncFilter: string | null;
    verifiedChunks: string[];
    lastSyncedAt: string | null;
    progressFingerprint: string | null;
    lastAdvancedAt: string | null;
    lastProgressObservedAt: string | null;
    stallObservationCount: number;
    stalledSince: string | null;
    error: string | null;
  }

  interface SchemaSyncRow extends StandardOption {
    subscriptionId: string;
    dataSourceId: string;
    datastoreKey: string | null;
    providerName: string;
    providerId: string | null;
    providerPeerId: string | null;
    providerPublicKey: string | null;
    sourceName: string | null;
    syncFilter: string;
    queryProfile: DataQueryProfile;
    retentionPolicy: DataFeedRetentionPolicy;
    localRows: number;
    cachedBytes: number;
    remoteRowsLoading: boolean;
    preference: SchemaSyncPreference;
    progress: SchemaSyncProgress;
  }

  interface ExplorerSourceOption {
    id: string;
    dataSourceId: string;
    datastoreKey: string | null;
    label: string;
    detail: string;
  }

  interface SourceProvenanceRow {
    id: string;
    providerName: string;
    detail: string;
    peerId: string | null;
    publicKey: string | null;
    sourceDatastoreLabel: string;
    trustAccessLabel: string;
    productsLabel: string;
    rowsLabel: string;
  }

  interface DataActivityRow {
    id: string;
    eventType: 'Sync' | 'Sync error' | 'Access' | 'Billing' | 'Subscription' | 'Verification' | 'Retry';
    standardId: string;
    providerName: string;
    statusLabel: string;
    detailLabel: string;
    nextAttemptLabel: string;
    occurredAtLabel: string;
    occurredAtMs: number;
    retrySchema: SchemaSyncRow | null;
  }

  interface CachedDataPageView {
    schemaSyncRows: SchemaSyncRow[];
    messageTypeRows: SchemaSyncRow[];
    sourceProvenanceRows: SourceProvenanceRow[];
    dataCatalogRows: DataCatalogRow[];
    dataCatalogSummary: DataCatalogSummary;
    dataOverviewVisuals: DataOverviewVisuals;
    billingDataRows: DataCatalogRow[];
    billingProviderRows: DataBillingProviderRow[];
    activityRows: DataActivityRow[];
    cachedAt: number;
  }

  interface FeedIdentity {
    providerId?: string | null;
    sourceName?: string | null;
  }

  interface PinVerifyToast {
    id: number;
    message: string;
    tone: PinVerifyToastTone;
  }

  interface SavedExplorerView {
    id: string;
    name: string;
    dataSourceId: string;
    datastoreKey: string | null;
    subscriptionId: string;
    standardId: string;
    searchMode: ExplorerSearchMode;
    searchText: string;
    sqlText: string;
    columnFilters: Record<string, string>;
    visibleColumnKeys: string[];
    pageSize: number;
    sortColumn: SortColumn;
    sortDirection: SortDirection;
    createdAt: string;
    updatedAt: string;
  }

  export let backend: SdnBackend | null = null;
  export let peers: ObservedSdnPeer[] = [];
  export let trustedPeers: ObservedSdnPeer[] = [];
  export let route = '/data';

  const DEFAULT_STANDARD_ID = 'EPM';
  const LOCAL_DATA_SOURCE_ID = 'local';
  const SCHEMA_EXTENSION = 'fbs';
  const DEFAULT_PAGE_SIZE = 10;
  const SYNC_PAGE_SIZE = 50_000;
  const SYNC_PERSIST_RECORD_INTERVAL = 100_000;
  const UI_REMOTE_TIMEOUT_MS = 8_000;
  const UI_LOCAL_QUERY_TIMEOUT_MS = 5_000;
  const LOCAL_EXPLORER_FILTER_DEBOUNCE_MS = 180;
  const DEFAULT_QUERY_PROFILE = DEFAULT_DATA_FEED_QUERY_PROFILE as DataQueryProfile;
  const DATA_QUERY_PROFILES: Array<{ id: DataQueryProfile; label: string }> = [
    { id: 'ordered-offset-v1', label: 'Ordered offset' },
    { id: 'dataset-publication-offset-v1', label: 'Published artifacts' },
  ];
  const DATA_RETENTION_POLICY_LABELS: Record<DataFeedRetentionPolicy, string> = {
    'append-only': 'Append history',
    'replace-snapshot': 'Update only',
  };
  const DATA_RETENTION_POLICIES: Array<{ id: DataFeedRetentionPolicy; label: string }> = DATA_FEED_RETENTION_POLICIES.map((id) => ({
    id,
    label: DATA_RETENTION_POLICY_LABELS[id],
  }));
  const SUBSCRIPTION_FILTERS: Array<{ id: SubscriptionFilter; label: string }> = [
    { id: 'all', label: 'All' },
    { id: 'active', label: 'Active' },
    { id: 'trials', label: 'Trials' },
    { id: 'expiring', label: 'Expiring' },
    { id: 'payment-issues', label: 'Payment issues' },
    { id: 'over-quota', label: 'Over quota' },
    { id: 'canceled', label: 'Canceled' },
    { id: 'free', label: 'Free' },
    { id: 'paid', label: 'Paid' },
    { id: 'usage-based', label: 'Usage-based' },
    { id: 'enterprise', label: 'Enterprise' },
  ];
  const DATA_SECTIONS: Array<{ id: DataSection; label: string; breadcrumb: string }> = [
    { id: 'store', label: 'Store', breadcrumb: 'Data / Store' },
    { id: 'subscriptions', label: 'Subscriptions', breadcrumb: 'Data / Subscriptions' },
    { id: 'explorer', label: 'Explorer', breadcrumb: 'Data / Explorer' },
  ];
  const STORAGE_GROUP_OPTIONS: Array<{ id: DataOverviewStorageGroup; label: string }> = [
    { id: 'provider', label: 'Provider' },
    { id: 'messageType', label: 'Data type' },
    { id: 'access', label: 'Paid vs free' },
  ];
  const EXPLORER_SAVED_VIEWS_STORAGE_KEY = 'sdn:data-explorer-saved-views:v1';
  const SCHEMA_SYNC_STORAGE_KEY = 'sdn:data-schema-sync:v1';
  const SCHEMA_SYNC_STATE_STORAGE_KEY = 'sdn:data-schema-sync-state:v1';
  const DATA_PAGE_VIEW_CACHE_STORAGE_KEY = 'sdn:data-page-view-cache:v1';
  const STORAGE_CAP_UNITS: StorageUnit[] = ['MB', 'GB', 'TB'];
  const DEFAULT_SCHEMA_SYNC_PREFERENCE: SchemaSyncPreference = {
    mode: 'preview',
    storageCap: 1,
    storageUnit: 'GB',
  };
  const LOCAL_FLATSQL_SCHEMAS = [
    { standardId: 'CAT', tableName: 'CAT', fileId: '$CAT', schema: CAT_SCHEMA },
    { standardId: 'EPM', tableName: 'EPM', fileId: '$EPM', schema: EPM_SCHEMA },
    { standardId: 'MPE', tableName: 'MPE', fileId: '$MPE', schema: MPE_SCHEMA },
    { standardId: 'OMM', tableName: 'OMM', fileId: '$OMM', schema: OMM_SCHEMA },
    { standardId: 'PNM', tableName: 'PNM', fileId: '$PNM', schema: PNM_SCHEMA },
    { standardId: 'SPW', tableName: 'SPW', fileId: '$SPW', schema: SPW_SCHEMA },
  ];
  const REPLACE_SNAPSHOT_STANDARD_IDS = LOCAL_FLATSQL_SCHEMAS
    .filter((schema) => defaultDataFeedRetentionPolicy(schema.standardId) === 'replace-snapshot')
    .map((schema) => schema.standardId);
  const METADATA_COLUMNS: WorkbenchColumn[] = [
    { key: 'schemaName', label: 'Message', source: 'metadata' },
    { key: 'cid', label: 'CID', source: 'metadata' },
    { key: 'peerId', label: 'Peer', source: 'metadata' },
    { key: 'providerId', label: 'Producer', source: 'metadata' },
    { key: 'sourceName', label: 'Source', source: 'metadata' },
    { key: 'batchId', label: 'Batch', source: 'metadata' },
    { key: 'timestamp', label: 'Timestamp', source: 'metadata' },
  ];
  const COLUMN_ABBREVIATIONS: Record<string, string> = {
    schemaName: 'STD',
    cid: 'CID',
    peerId: 'PEER',
    providerId: 'PROD',
    sourceName: 'SRC',
    batchId: 'BATCH',
    timestamp: 'TIME',
    DN: 'DN',
    LEGAL_NAME: 'LEGAL',
    FAMILY_NAME: 'FAM',
    GIVEN_NAME: 'GIVEN',
    ADDITIONAL_NAME: 'ADDN',
    HONORIFIC_PREFIX: 'PREFIX',
    HONORIFIC_SUFFIX: 'SUFFIX',
    JOB_TITLE: 'TITLE',
    OCCUPATION: 'OCC',
    EMAIL: 'EMAIL',
    TELEPHONE: 'TEL',
    ENTITY_TYPE: 'ENTITY',
    DIRECTORY_KIND: 'DIR',
    PEER_ID: 'PEER',
    SIGNING_PUBLIC_KEY: 'SIGN PK',
    ENCRYPTION_PUBLIC_KEY: 'ENC PK',
    ALTERNATE_NAMES: 'ALIAS',
    MULTIFORMAT_ADDRESS: 'ADDR',
    KEYS: 'KEYS',
    SIGNATURE: 'SIG',
    SIGNATURE_TIMESTAMP: 'SIG TS',
    CHAIN_PROOFS: 'PROOFS',
    OBJECT_NAME: 'OBJ',
    OBJECT_ID: 'INTL',
    NORAD_CAT_ID: 'NORAD',
    OBJECT_TYPE: 'TYPE',
    OPS_STATUS_CODE: 'OPS',
    OWNER: 'OWNER',
    LAUNCH_DATE: 'LDATE',
    LAUNCH_SITE: 'LSITE',
    DECAY_DATE: 'DDATE',
    PERIOD: 'PER',
    INCLINATION: 'INC',
    APOGEE: 'APO',
    PERIGEE: 'PERI',
    RCS: 'RCS',
    DATA_STATUS_CODE: 'DATA',
    ORBIT_CENTER: 'CENTER',
    ORBIT_TYPE: 'ORBIT',
    DEPLOYMENT_DATE: 'DEPLOY',
    MANEUVERABLE: 'MAN',
    SIZE: 'SIZE',
    MASS: 'MASS',
    MASS_TYPE: 'MASS T',
    CCSDS_OMM_VERS: 'VER',
    CREATION_DATE: 'CREATED',
    ORIGINATOR: 'ORG',
    CENTER_NAME: 'CENTER',
    REFERENCE_FRAME: 'RF',
    REFERENCE_FRAME_EPOCH: 'RF EPOCH',
    TIME_SYSTEM: 'TS',
    MEAN_ELEMENT_THEORY: 'MET',
    COMMENT: 'COMMENT',
    EPOCH: 'EPOCH',
    SEMI_MAJOR_AXIS: 'SMA',
    MEAN_MOTION: 'MM',
    ECCENTRICITY: 'ECC',
    RA_OF_ASC_NODE: 'RAAN',
    ARG_OF_PERICENTER: 'AOP',
    MEAN_ANOMALY: 'MA',
    GM: 'GM',
    SOLAR_RAD_AREA: 'SRP A',
    SOLAR_RAD_COEFF: 'SRP C',
    DRAG_AREA: 'DRAG A',
    DRAG_COEFF: 'DRAG C',
    EPHEMERIS_TYPE: 'EPH',
    CLASSIFICATION_TYPE: 'CLASS',
    ELEMENT_SET_NO: 'ELSET',
    REV_AT_EPOCH: 'REV',
    BSTAR: 'B*',
    MEAN_MOTION_DOT: 'MM DOT',
    MEAN_MOTION_DDOT: 'MM DDOT',
    COV_REFERENCE_FRAME: 'COV RF',
    COVARIANCE: 'COV',
    USER_DEFINED_BIP_0044_TYPE: 'BIP44',
    USER_DEFINED_OBJECT_DESIGNATOR: 'DESIG',
    USER_DEFINED_EARTH_MODEL: 'EARTH',
    USER_DEFINED_EPOCH_TIMESTAMP: 'EPOCH TS',
    USER_DEFINED_MICROSECONDS: 'USEC',
    ENTITY_ID: 'ENTITY',
    FILE_ID: 'FILE ID',
    FILE_NAME: 'FILE',
    PUBLISH_TIMESTAMP: 'PUB TS',
    SIGNATURE_TYPE: 'SIG TYPE',
    TIMESTAMP_SIGNATURE: 'TS SIG',
    TIMESTAMP_SIGNATURE_TYPE: 'TS SIG TYPE',
    DATE: 'DATE',
    BSRN: 'BSRN',
    ND: 'ND',
    KP1: 'Kp1',
    KP2: 'Kp2',
    KP3: 'Kp3',
    KP4: 'Kp4',
    KP5: 'Kp5',
    KP6: 'Kp6',
    KP7: 'Kp7',
    KP8: 'Kp8',
    KP_SUM: 'Kp SUM',
    AP1: 'Ap1',
    AP2: 'Ap2',
    AP3: 'Ap3',
    AP4: 'Ap4',
    AP5: 'Ap5',
    AP6: 'Ap6',
    AP7: 'Ap7',
    AP8: 'Ap8',
    AP_AVG: 'Ap AVG',
    CP: 'Cp',
    C9: 'C9',
    ISN: 'ISN',
    F107_OBS: 'F10.7 OBS',
    F107_ADJ: 'F10.7 ADJ',
    F107_DATA_TYPE: 'F10.7 TYPE',
    F107_OBS_CENTER81: 'F10.7 OBS C81',
    F107_OBS_LAST81: 'F10.7 OBS L81',
    F107_ADJ_CENTER81: 'F10.7 ADJ C81',
    F107_ADJ_LAST81: 'F10.7 ADJ L81',
  };
  const INTERNAL_COLUMN_KEYS = new Set(['bytes', 'dataBytes', 'data_base64', 'sizeBytes']);
  const INTERNAL_SQL_COLUMN_KEYS = new Set(['_data', '_offset', '_source', '_rowid']);
  const EPM_STANDARD_COLUMNS: WorkbenchColumn[] = [
    { key: 'dn', label: 'Display name', source: 'standard' },
    { key: 'legal_name', label: 'Legal name', source: 'standard' },
    { key: 'family_name', label: 'Family name', source: 'standard' },
    { key: 'given_name', label: 'Given name', source: 'standard' },
    { key: 'additional_name', label: 'Additional name', source: 'standard' },
    { key: 'honorific_prefix', label: 'Honorific prefix', source: 'standard' },
    { key: 'honorific_suffix', label: 'Honorific suffix', source: 'standard' },
    { key: 'job_title', label: 'Job title', source: 'standard' },
    { key: 'occupation', label: 'Occupation', source: 'standard' },
    { key: 'email', label: 'Email', source: 'standard' },
    { key: 'telephone', label: 'Telephone', source: 'standard' },
    { key: 'entity_type', label: 'Entity type', source: 'standard' },
    { key: 'directory_kind', label: 'Directory kind', source: 'standard' },
    { key: 'peer_id', label: 'EPM Peer ID', source: 'standard' },
    { key: 'signing_public_key', label: 'Signing public key', source: 'standard' },
    { key: 'encryption_public_key', label: 'Encryption public key', source: 'standard' },
    { key: 'alternate_names', label: 'Alternate names', source: 'standard' },
    { key: 'multiformat_address', label: 'Multiformat address', source: 'standard' },
    { key: 'keys', label: 'Keys', source: 'standard' },
    { key: 'signature', label: 'Signature', source: 'standard' },
    { key: 'signature_timestamp', label: 'Signature timestamp', source: 'standard' },
  ];
  const OMM_STANDARD_COLUMNS: WorkbenchColumn[] = [
    { key: 'OBJECT_NAME', label: 'Object name', source: 'standard' },
    { key: 'OBJECT_ID', label: 'Object ID', source: 'standard' },
    { key: 'NORAD_CAT_ID', label: 'NORAD catalog ID', source: 'standard' },
    { key: 'EPOCH', label: 'Epoch', source: 'standard' },
    { key: 'MEAN_MOTION', label: 'Mean motion (rev/day)', source: 'standard' },
    { key: 'ECCENTRICITY', label: 'Eccentricity', source: 'standard' },
    { key: 'INCLINATION', label: 'Inclination (deg)', source: 'standard' },
    { key: 'RA_OF_ASC_NODE', label: 'RA ascending node (deg)', source: 'standard' },
    { key: 'ARG_OF_PERICENTER', label: 'Argument of pericenter (deg)', source: 'standard' },
    { key: 'MEAN_ANOMALY', label: 'Mean anomaly (deg)', source: 'standard' },
    { key: 'BSTAR', label: 'BSTAR', source: 'standard' },
    { key: 'MEAN_ELEMENT_THEORY', label: 'Mean element theory', source: 'standard' },
    { key: 'TIME_SYSTEM', label: 'Time system', source: 'standard' },
    { key: 'EPHEMERIS_TYPE', label: 'Ephemeris type', source: 'standard' },
    { key: 'CLASSIFICATION_TYPE', label: 'Classification', source: 'standard' },
    { key: 'ORIGINATOR', label: 'Originator', source: 'standard' },
    { key: 'CREATION_DATE', label: 'Creation date', source: 'standard' },
    { key: 'CENTER_NAME', label: 'Center', source: 'standard' },
  ];
  const CAT_STANDARD_COLUMNS: WorkbenchColumn[] = [
    { key: 'OBJECT_NAME', label: 'Object name', source: 'standard' },
    { key: 'OBJECT_ID', label: 'Object ID', source: 'standard' },
    { key: 'NORAD_CAT_ID', label: 'NORAD catalog ID', source: 'standard' },
    { key: 'OBJECT_TYPE', label: 'Object type', source: 'standard' },
    { key: 'OPS_STATUS_CODE', label: 'Ops status', source: 'standard' },
    { key: 'OWNER', label: 'Owner', source: 'standard' },
    { key: 'LAUNCH_DATE', label: 'Launch date', source: 'standard' },
    { key: 'LAUNCH_SITE', label: 'Launch site', source: 'standard' },
    { key: 'DECAY_DATE', label: 'Decay date', source: 'standard' },
    { key: 'PERIOD', label: 'Period (min)', source: 'standard' },
    { key: 'INCLINATION', label: 'Inclination (deg)', source: 'standard' },
    { key: 'APOGEE', label: 'Apogee (km)', source: 'standard' },
    { key: 'PERIGEE', label: 'Perigee (km)', source: 'standard' },
    { key: 'RCS', label: 'RCS (m^2)', source: 'standard' },
    { key: 'DATA_STATUS_CODE', label: 'Data status', source: 'standard' },
    { key: 'ORBIT_CENTER', label: 'Orbit center', source: 'standard' },
    { key: 'ORBIT_TYPE', label: 'Orbit type', source: 'standard' },
    { key: 'DEPLOYMENT_DATE', label: 'Deployment date', source: 'standard' },
    { key: 'MANEUVERABLE', label: 'Maneuverable', source: 'standard' },
    { key: 'SIZE', label: 'Size (m)', source: 'standard' },
    { key: 'MASS', label: 'Mass (kg)', source: 'standard' },
    { key: 'MASS_TYPE', label: 'Mass type', source: 'standard' },
  ];
  const PNM_STANDARD_COLUMNS: WorkbenchColumn[] = [
    { key: 'FILE_ID', label: 'FILE_ID', source: 'standard' },
    { key: 'CID', label: 'CID', source: 'standard' },
    { key: 'FILE_NAME', label: 'FILE_NAME', source: 'standard' },
    { key: 'PUBLISH_TIMESTAMP', label: 'PUBLISH_TIMESTAMP', source: 'standard' },
    { key: 'MULTIFORMAT_ADDRESS', label: 'MULTIFORMAT_ADDRESS', source: 'standard' },
    { key: 'SIGNATURE', label: 'SIGNATURE', source: 'standard' },
    { key: 'SIGNATURE_TYPE', label: 'SIGNATURE_TYPE', source: 'standard' },
    { key: 'TIMESTAMP_SIGNATURE', label: 'TIMESTAMP_SIGNATURE', source: 'standard' },
    { key: 'TIMESTAMP_SIGNATURE_TYPE', label: 'TIMESTAMP_SIGNATURE_TYPE', source: 'standard' },
  ];
  const MPE_STANDARD_COLUMNS: WorkbenchColumn[] = [
    { key: 'ENTITY_ID', label: 'Entity ID', source: 'standard' },
    { key: 'EPOCH', label: 'Epoch (s since 1970)', source: 'standard' },
    { key: 'MEAN_MOTION', label: 'Mean motion (rev/day)', source: 'standard' },
    { key: 'ECCENTRICITY', label: 'Eccentricity', source: 'standard' },
    { key: 'INCLINATION', label: 'Inclination (deg)', source: 'standard' },
    { key: 'RA_OF_ASC_NODE', label: 'RA ascending node (deg)', source: 'standard' },
    { key: 'ARG_OF_PERICENTER', label: 'Argument of pericenter (deg)', source: 'standard' },
    { key: 'MEAN_ANOMALY', label: 'Mean anomaly (deg)', source: 'standard' },
    { key: 'BSTAR', label: 'BSTAR', source: 'standard' },
    { key: 'MEAN_ELEMENT_THEORY', label: 'Mean element theory', source: 'standard' },
  ];
  const SPW_STANDARD_COLUMNS: WorkbenchColumn[] = [
    { key: 'DATE', label: 'Date', source: 'standard' },
    { key: 'BSRN', label: 'Bartels solar rotation number', source: 'standard' },
    { key: 'ND', label: 'Day of Bartels rotation', source: 'standard' },
    { key: 'KP1', label: 'Kp interval 1', source: 'standard' },
    { key: 'KP2', label: 'Kp interval 2', source: 'standard' },
    { key: 'KP3', label: 'Kp interval 3', source: 'standard' },
    { key: 'KP4', label: 'Kp interval 4', source: 'standard' },
    { key: 'KP5', label: 'Kp interval 5', source: 'standard' },
    { key: 'KP6', label: 'Kp interval 6', source: 'standard' },
    { key: 'KP7', label: 'Kp interval 7', source: 'standard' },
    { key: 'KP8', label: 'Kp interval 8', source: 'standard' },
    { key: 'KP_SUM', label: 'Kp sum', source: 'standard' },
    { key: 'AP1', label: 'Ap interval 1', source: 'standard' },
    { key: 'AP2', label: 'Ap interval 2', source: 'standard' },
    { key: 'AP3', label: 'Ap interval 3', source: 'standard' },
    { key: 'AP4', label: 'Ap interval 4', source: 'standard' },
    { key: 'AP5', label: 'Ap interval 5', source: 'standard' },
    { key: 'AP6', label: 'Ap interval 6', source: 'standard' },
    { key: 'AP7', label: 'Ap interval 7', source: 'standard' },
    { key: 'AP8', label: 'Ap interval 8', source: 'standard' },
    { key: 'AP_AVG', label: 'Ap average', source: 'standard' },
    { key: 'CP', label: 'Cp', source: 'standard' },
    { key: 'C9', label: 'C9', source: 'standard' },
    { key: 'ISN', label: 'International sunspot number', source: 'standard' },
    { key: 'F107_OBS', label: 'Observed F10.7', source: 'standard' },
    { key: 'F107_ADJ', label: 'Adjusted F10.7', source: 'standard' },
    { key: 'F107_DATA_TYPE', label: 'F10.7 data type', source: 'standard' },
    { key: 'F107_OBS_CENTER81', label: 'Observed F10.7 centered 81-day average', source: 'standard' },
    { key: 'F107_OBS_LAST81', label: 'Observed F10.7 trailing 81-day average', source: 'standard' },
    { key: 'F107_ADJ_CENTER81', label: 'Adjusted F10.7 centered 81-day average', source: 'standard' },
    { key: 'F107_ADJ_LAST81', label: 'Adjusted F10.7 trailing 81-day average', source: 'standard' },
  ];
  const STANDARD_FIELD_COLUMNS: Record<string, WorkbenchColumn[]> = {
    CAT: CAT_STANDARD_COLUMNS,
    EPM: EPM_STANDARD_COLUMNS,
    MPE: MPE_STANDARD_COLUMNS,
    OMM: OMM_STANDARD_COLUMNS,
    PNM: PNM_STANDARD_COLUMNS,
    SPW: SPW_STANDARD_COLUMNS,
  };
  const SQL_COLUMN_LABELS: Record<string, string> = Object.fromEntries(
    [CAT_STANDARD_COLUMNS, EPM_STANDARD_COLUMNS, MPE_STANDARD_COLUMNS, OMM_STANDARD_COLUMNS, PNM_STANDARD_COLUMNS, SPW_STANDARD_COLUMNS]
      .flat()
      .flatMap((column) => [[column.key, column.label], [column.key.toUpperCase(), column.label]]),
  );
  const OPTIONAL_DEFAULT_VALUE_COLUMNS: Record<string, Set<string>> = {
    CAT: new Set(['MANEUVERABLE', 'SIZE', 'MASS', 'MASS_TYPE']),
    OMM: new Set(['GM', 'MASS', 'SOLAR_RAD_AREA', 'SOLAR_RAD_COEFF', 'DRAG_AREA', 'DRAG_COEFF']),
  };

  let dataSummary: DataSummary | null = null;
  let selectedDataSection: DataSection = 'store';
  let selectedStandardId = DEFAULT_STANDARD_ID;
  let selectedDataSourceId = LOCAL_DATA_SOURCE_ID;
  let selectedSubscriptionId = '';
  let selectedDatastoreKey: string | null = null;
  let selectedExplorerSourceKey = '';
  let explorerSearchMode: ExplorerSearchMode = 'plain';
  let explorerSearchText = '';
  let savedExplorerViews: SavedExplorerView[] = loadSavedExplorerViews();
  let selectedSavedExplorerViewId = '';
  let savedExplorerViewName = '';
  let columnFilters: Record<string, string> = {};
  let visibleColumnKeys: string[] = [];
  let searchText = '';
  let catalogSearchText = '';
  let catalogAccessFilter: DataCatalogAccessFilter = 'all';
  let catalogSyncFilter: DataCatalogSyncFilter = 'all';
  let catalogStorageFilter: DataCatalogStorageFilter = 'all';
  let subscriptionFilter: SubscriptionFilter = 'all';
  let subscriptionSearchText = '';
  let selectedSubscriptionDetailId = '';
  let expandedCatalogActionRowKey = '';
  let suppressCatalogOutsideUntil = 0;
  let overviewStorageGroup: DataOverviewStorageGroup = 'provider';
  let pageSize = DEFAULT_PAGE_SIZE;
  let pageIndex = 0;
  let sortColumn: SortColumn = 'timestamp';
  let sortDirection: SortDirection = 'desc';
  let rawRecords: RawDataRecord[] = [];
  let dataScan: DataScanResult | null = null;
  let dataPageLoading = true;
  let lastBackend: SdnBackend | null = null;
  let configuredDataSources: ConfiguredSdnNode[] = [];
  let userSelectedDataSource = false;
  let userSelectedStandard = false;
  let inspectCid = '';
  let inspectGatewayUrl = '';
  let localFlatSqlStore: WorkerLocalFlatSqlStore | null = null;
  let localFlatSqlStoreKey = '';
  let localFlatSqlStorePromise: Promise<WorkerLocalFlatSqlStore | null> | null = null;
  let localFlatSqlStorePromiseKey = '';
  let localFlatSqlStats: LocalFlatSqlStandardStats[] = [];
  let localFlatSqlStatsLoaded = false;
  let localExplorerResult: LocalFlatSqlQueryResult | null = null;
  let localExplorerFilteredTotalRows: number | null = null;
  let localExplorerDatasetFiltersActive = false;
  let localExplorerDatasetQueryActive = false;
  let localExplorerLoading = false;
  let localExplorerError = '';
  let localExplorerQueryTimer: ReturnType<typeof setTimeout> | null = null;
  let localExplorerQueryVersion = 0;
  let sqlQueryText = defaultSqlQuery(DEFAULT_STANDARD_ID);
  let sqlResult: LocalFlatSqlQueryResult | null = null;
  let sqlError = '';
  let sqlRunning = false;
  let userEditedSql = false;
  let dataDirectoryState: DataDirectoryState = loadDataDirectoryState();
  let cachedDataPageView: CachedDataPageView | null = loadCachedDataPageView();
  let cachedDataPageViewSignature = cachedDataPageView ? dataPageViewSignature(cachedDataPageView) : '';
  let schemaSyncPreferences: Record<string, SchemaSyncPreference> = loadSchemaSyncPreferences();
  let schemaSyncProgress: Record<string, SchemaSyncProgress> = loadSchemaSyncProgress();
  let activeSyncKeys = new Set<string>();
  let pausedSyncKeys = new Set<string>();
  let selectedPnmRow: WorkbenchRow | null = null;
  let pnmFileIdQuery = '';
  let pnmQueryResult: LocalFlatSqlQueryResult | null = null;
  let pnmQueryError = '';
  let pnmSignatureStatus = '';
  let pnmSignatureRunning = false;
  let resetSubscriptionId = '';
  let resetConfirmText = '';
  let resetStatus = '';
  let resetRunning = false;
  let pinVerifyToast: PinVerifyToast | null = null;
  let pinVerifyToastTimer: ReturnType<typeof setTimeout> | null = null;
  let pinVerifyToastId = 0;
  let pinVerifyRunning = false;
  const schemaSyncScheduler = createSchemaSyncScheduler({
    syncSchema: (standardId, dataSourceId, subscriptionId) => synchronizeSchema(standardId, dataSourceId, subscriptionId),
  });
  const schemaSyncSchedulers = new Map<string, typeof schemaSyncScheduler>([[LOCAL_DATA_SOURCE_ID, schemaSyncScheduler]]);

  $: dataSourceOptions = buildDataSourceOptions(backend, configuredDataSources, peers, trustedPeers);
  $: liveSchemaSyncRows = buildSubscribedSchemaSyncRows(dataDirectoryState.subscriptions, selectedDataSourceId, selectedDatastoreKey, localFlatSqlStats, localFlatSqlStatsLoaded, schemaSyncPreferences, dataSummary, dataScan, dataPageLoading, selectedStandardId);
  $: dataPageCacheActive = shouldUseCachedDataPageView(cachedDataPageView, liveSchemaSyncRows, dataPageLoading);
  $: schemaSyncRows = dataPageCacheActive && cachedDataPageView ? cachedDataPageView.schemaSyncRows : liveSchemaSyncRows;
  $: messageTypeRows = dataPageCacheActive && cachedDataPageView ? cachedDataPageView.messageTypeRows : sortedMessageTypeRows(schemaSyncRows);
  $: sourceProvenanceRows = dataPageCacheActive && cachedDataPageView ? cachedDataPageView.sourceProvenanceRows : buildSourceProvenanceRows(dataSourceOptions, schemaSyncRows, dataDirectoryState.peerTrust);
  $: subscribedSourceOptions = buildSubscribedSourceOptions(schemaSyncRows);
  $: selectedExplorerSourceKey = subscriptionSourceKey(selectedDataSourceId, selectedDatastoreKey);
  $: subscribedStandardOptions = schemaSyncRows.filter((row) => subscriptionSourceKey(row.dataSourceId, row.datastoreKey) === subscriptionSourceKey(selectedDataSourceId, selectedDatastoreKey));
  $: activeStorageRows = schemaSyncRows.filter((row) => row.preference.mode === 'sync');
  $: activeStorageRemoteRows = activeStorageRows.reduce((total, row) => total + row.remoteRows, 0);
  $: activeStorageLocalRows = activeStorageRows.reduce((total, row) => total + row.localRows, 0);
  $: activeStorageCachedBytes = activeStorageRows.reduce((total, row) => total + row.cachedBytes, 0);
  $: activeStoragePinnedRows = activeStorageRows.reduce((total, row) => total + row.progress.pinnedRows, 0);
  $: dataCatalogRows = dataPageCacheActive && cachedDataPageView ? cachedDataPageView.dataCatalogRows : buildCatalogRows(schemaSyncRows);
  $: dataCatalogSummary = dataPageCacheActive && cachedDataPageView ? cachedDataPageView.dataCatalogSummary : summarizeDataCatalog(dataCatalogRows);
  $: dataOverviewVisuals = dataPageCacheActive && cachedDataPageView ? cachedOverviewVisualsForGroup(cachedDataPageView.dataOverviewVisuals, overviewStorageGroup) : buildDataOverviewVisuals(dataCatalogRows, overviewStorageGroup);
  $: billingDataRows = dataPageCacheActive && cachedDataPageView ? cachedDataPageView.billingDataRows : dataCatalogRows.filter(catalogRowHasBillingData);
  $: billingProviderRows = dataPageCacheActive && cachedDataPageView ? cachedDataPageView.billingProviderRows : buildDataBillingProviderRows(dataCatalogRows);
  $: activityRows = dataPageCacheActive && cachedDataPageView ? cachedDataPageView.activityRows : buildDataActivityRows(schemaSyncRows, dataCatalogRows);
  $: filteredSubscriptionRows = filterSubscriptionRows(schemaSyncRows, subscriptionFilter, subscriptionSearchText);
  $: selectedSubscriptionDetailSchema = selectedSubscriptionDetailId
    ? schemaSyncRows.find((schema) => schema.subscriptionId === selectedSubscriptionDetailId) ?? null
    : null;
  $: filteredDataCatalogRows = filterDataCatalogRows(dataCatalogRows, {
    query: catalogSearchText,
    access: catalogAccessFilter,
    sync: catalogSyncFilter,
    storage: catalogStorageFilter,
  });
  $: selectedSchemaSyncRow = schemaSyncRows.find((row) => selectedSubscriptionId && row.subscriptionId === selectedSubscriptionId)
    ?? schemaSyncRows.find((row) => row.id === selectedStandardId && row.dataSourceId === selectedDataSourceId && (selectedDatastoreKey ? row.datastoreKey === selectedDatastoreKey : true))
    ?? schemaSyncRows.find((row) => row.id === selectedStandardId && row.dataSourceId === selectedDataSourceId)
    ?? schemaSyncRows.find((row) => row.id === selectedStandardId)
    ?? null;
  $: selectedDataSectionMeta = DATA_SECTIONS.find((section) => section.id === selectedDataSection) ?? DATA_SECTIONS[0];
  $: syncSelectedStandardWithSubscriptions(schemaSyncRows);
  $: decodedRows = rawRecords.map(decodeWorkbenchRecord);
  $: allColumns = workbenchColumnsForStandard(selectedStandardId, decodedRows);
  $: syncVisibleColumnKeys(allColumns);
  $: visibleColumns = allColumns.filter((column) => visibleColumnKeys.includes(column.key));
  $: filteredRows = filterRowsByColumns(filterRows(decodedRows, searchText), visibleColumns, columnFilters);
  $: visibleRows = sortRows(filteredRows, sortColumn, sortDirection);
  $: localExplorerRecords = localExplorerResult?.records ?? [];
  $: localExplorerColumns = visibleSqlColumns(localExplorerResult?.columns ?? [], localExplorerRecords, selectedStandardId);
  $: visibleLocalExplorerRecords = sortSqlRecords(localExplorerRecords, sortColumn, sortDirection);
  $: estimatedTotalRows = scanTotalRowsForStandard(dataScan, selectedStandardId)
    ?? selectedSchemaSyncRow?.remoteRows
    ?? totalRowsForStandardId(dataSummary, selectedStandardId)
    ?? null;
  $: localExplorerTotalRows = localExplorerResult
    ? localExplorerFilteredTotalRows
      ?? (localExplorerDatasetFiltersActive ? null : Math.max(localRowsForStandard(localFlatSqlStats, selectedStandardId), pageIndex * normalizedPageSize() + localExplorerRecords.length))
    : null;
  $: localExplorerDatasetQueryActive = explorerSearchMode === 'plain' && (Boolean(searchText.trim()) || hasActiveColumnFilters(columnFilters, localExplorerColumns));
  $: explorerPageTotalRows = explorerSearchMode === 'plain' && (localExplorerResult || localExplorerDatasetQueryActive) ? localExplorerTotalRows : estimatedTotalRows;
  $: explorerPageRowCount = explorerSearchMode === 'plain' && localExplorerResult ? localExplorerRecords.length : rawRecords.length;
  $: totalPageCount = explorerPageTotalRows === null ? Math.max(1, pageIndex + (canGoNext ? 2 : 1)) : Math.max(1, Math.ceil(explorerPageTotalRows / normalizedPageSize()));
  $: canGoPrevious = pageIndex > 0;
  $: canGoNext = explorerPageRowCount >= pageSize && (explorerPageTotalRows === null || ((pageIndex + 1) * pageSize) < explorerPageTotalRows);
  $: pageLabel = `${pageIndex + 1}/${totalPageCount}`;
  $: storageMetricsLoading = activeStorageRows.some(isSchemaRemoteRowsLoading);
  $: storageRemoteRowsMetric = loadingMetricLabel(storageMetricsLoading, formatNumber(activeStorageRemoteRows));
  $: storageLocalRowsMetric = loadingMetricLabel(storageMetricsLoading, formatNumber(activeStorageLocalRows));
  $: storageCachedMetric = loadingMetricLabel(storageMetricsLoading, formatBytes(activeStorageCachedBytes));
  $: storagePinnedRowsMetric = loadingMetricLabel(storageMetricsLoading, formatNumber(activeStoragePinnedRows));
  $: overviewLocalStorageMetric = loadingMetricLabel(storageMetricsLoading, formatBytes(dataCatalogSummary.localStorageBytes));
  $: overviewDataHealthMetric = loadingMetricLabel(storageMetricsLoading, dataCatalogSummary.dataHealthLabel);
  $: overviewStorageTotalMetric = loadingMetricLabel(storageMetricsLoading, formatBytes(dataOverviewVisuals.storageTotalBytes));
  $: sqlColumns = sqlResult?.columns ?? [];
  $: sqlRecords = sqlResult?.records ?? [];
  $: displaySqlColumns = visibleSqlColumns(sqlColumns, sqlRecords, selectedStandardId);
  $: filteredSqlRecords = filterSqlRecordsByColumns(sqlRecords, displaySqlColumns, columnFilters);
  $: visibleSqlRecords = sortSqlRecords(filteredSqlRecords, sortColumn, sortDirection);
  $: pnmQueryColumns = visibleSqlColumns(pnmQueryResult?.columns ?? [], pnmQueryRows, 'PNM');
  $: pnmQueryRows = pnmQueryResult?.records ?? [];
  $: selectedPnmDetails = selectedPnmRow?.decoded ?? {};
  $: explorerColumnKeyEntries = buildExplorerColumnKeyEntries(
    explorerSearchMode,
    Boolean(sqlResult),
    displaySqlColumns,
    Boolean(localExplorerResult),
    localExplorerColumns,
    visibleColumns,
  );
  $: rememberDataPageViewCache(
    dataPageCacheActive,
    schemaSyncRows,
    messageTypeRows,
    sourceProvenanceRows,
    dataCatalogRows,
    dataCatalogSummary,
    dataOverviewVisuals,
    billingDataRows,
    billingProviderRows,
    activityRows,
  );

  $: if (backend && backend !== lastBackend) {
    lastBackend = backend;
    void initializeDataExplorer();
  }

  $: scheduleSubscribedSchemaSyncs(schemaSyncRows, backend, dataPageLoading, dataSourceOptions);

  $: syncInspectRoute(route, backend);

  $: if (dataSourceOptions.length > 0 && !dataSourceOptions.some((source) => source.id === selectedDataSourceId)) {
    selectedDataSourceId = dataSourceOptions[0].id;
  }

  async function initializeDataExplorer(): Promise<void> {
    dataPageLoading = true;
    resetSchemaSyncSchedulers();
    try {
      configuredDataSources = [];
      dataDirectoryState = loadDataDirectoryState();
      selectedDataSourceId = LOCAL_DATA_SOURCE_ID;
      userSelectedDataSource = false;
      userSelectedStandard = false;
      await loadConfiguredDataSources();
      const migrationSources = dataDirectoryMigrationSources(buildDataSourceOptions(backend, configuredDataSources, peers, trustedPeers));
      dataDirectoryState = canonicalizeDataDirectorySourceIds(
        dataDirectoryState,
        migrationSources,
      );
      dataDirectoryState = migrateSchemaSyncPreferencesToDataDirectory(
        dataDirectoryState,
        schemaSyncPreferences,
        migrationSources,
        schemaSyncProgress,
      );
      persistDataDirectoryState(dataDirectoryState);
      await pruneUnsubscribedReplaceSnapshotStores(migrationSources, dataDirectoryState.subscriptions);
      if (!userSelectedDataSource) {
        selectedDataSourceId = preferredSubscribedDataSourceId(dataDirectoryState.subscriptions)
          ?? preferredDataSourceId(buildDataSourceOptions(backend, configuredDataSources, peers, trustedPeers));
      }
      void initializeWorkbench();
    } finally {
      dataPageLoading = false;
    }
  }

  async function initializeWorkbench(): Promise<void> {
    await loadDataSummary();
    await refreshLocalFlatSqlStats().catch(() => {
      localFlatSqlStats = [];
      localFlatSqlStatsLoaded = false;
    });
    await runLocalExplorerQuery(0);
    void runWorkbenchQuery(0);
  }

  async function loadConfiguredDataSources(): Promise<void> {
    if (typeof fetch !== 'function') {
      configuredDataSources = [];
      return;
    }
    try {
      const response = await fetch('/api/local/sdn-nodes', {
        headers: { accept: 'application/json' },
      });
      if (!response.ok) {
        configuredDataSources = [];
        return;
      }
      configuredDataSources = normalizeConfiguredDataSources(await response.json());
    } catch {
      configuredDataSources = [];
    }
  }

  async function loadDataSummary(): Promise<void> {
    const source = currentDataSourceOption();
    const workerBackendConfig = backendConfigForDataSource(source);
    if (workerBackendConfig) {
      try {
        const store = await withUiTimeout(ensureLocalFlatSqlStore(selectedDataSourceId, selectedDatastoreKey), UI_LOCAL_QUERY_TIMEOUT_MS, 'FlatSQL initialization');
        dataSummary = store ? await withUiTimeout(store.getRemoteDataSummary(workerBackendConfig), UI_REMOTE_TIMEOUT_MS, 'Remote data summary') : null;
        refreshSubscriptionRemoteRowsFromSummary(source?.id ?? selectedDataSourceId, dataSummary);
        const nextStandardOptions = standardIdsFromSummary(dataSummary);
        const previousStandardId = selectedStandardId;
        if (!userSelectedStandard || !nextStandardOptions.includes(selectedStandardId)) {
          selectedStandardId = preferredStandardIdForDataSourceSummary(source?.id ?? selectedDataSourceId, selectedDatastoreKey, dataSummary);
        }
        if (previousStandardId !== selectedStandardId && !userEditedSql) {
          resetSqlForSelectedStandard();
          void runLocalExplorerQuery(0);
        }
      } catch {
        dataSummary = null;
      }
      return;
    }

    const activeBackend = backendForSelectedDataSource();
    if (!activeBackend) {
      dataSummary = null;
      dataScan = null;
      return;
    }
    try {
      const result = await withUiTimeout(activeBackend.getDataSummary(), UI_REMOTE_TIMEOUT_MS, 'Remote data summary');
      dataSummary = result.data;
      refreshSubscriptionRemoteRowsFromSummary(source?.id ?? selectedDataSourceId, result.data);
      const nextStandardOptions = standardIdsFromSummary(result.data);
      const previousStandardId = selectedStandardId;
      if (!userSelectedStandard || !nextStandardOptions.includes(selectedStandardId)) {
        selectedStandardId = preferredStandardIdForDataSourceSummary(source?.id ?? selectedDataSourceId, selectedDatastoreKey, result.data);
      }
      if (previousStandardId !== selectedStandardId && !userEditedSql) {
        resetSqlForSelectedStandard();
        void runLocalExplorerQuery(0);
      }
    } catch {
      dataSummary = null;
    }
  }

  async function runWorkbenchQuery(targetPage = pageIndex): Promise<void> {
    const nextPage = Math.max(0, targetPage);
    const activeSelection = selectedSchemaSyncRowForSelection();
    const query = {
      schema: schemaNameForStandardId(selectedStandardId),
      ...(activeSelection?.datastoreKey ? { datastoreKey: activeSelection.datastoreKey } : {}),
      ...(activeSelection?.syncFilter ? { syncFilter: activeSelection.syncFilter } : {}),
      queryProfile: subscriptionQueryProfileFor(activeSelection),
      limit: normalizedPageSize(),
      offset: nextPage * normalizedPageSize(),
    };
    try {
      const source = currentDataSourceOption();
      const workerBackendConfig = backendConfigForDataSource(
        source,
        activeSelection?.datastoreKey ?? selectedDatastoreKey,
        activeSelection,
      );
      if (workerBackendConfig) {
        const store = await withUiTimeout(ensureLocalFlatSqlStore(selectedDataSourceId, activeSelection?.datastoreKey ?? selectedDatastoreKey), UI_LOCAL_QUERY_TIMEOUT_MS, 'FlatSQL initialization');
        if (!store) throw new Error('FlatSQL initialization failed');
        const result = await withUiTimeout(store.queryRemotePage({
          standardId: selectedStandardId,
          query,
          backendConfig: workerBackendConfig,
          source: source?.publicKey ?? source?.peerId ?? source?.id ?? null,
        }), UI_REMOTE_TIMEOUT_MS, 'Remote page query');
        dataScan = result.scan;
        rawRecords = result.records;
        localFlatSqlStats = result.stats;
        pageIndex = nextPage;
        resetPnmSelectionIfNeeded();
        if (rawRecords.length > 0 && !userEditedSql) {
          sqlQueryText = defaultSqlQuery(selectedStandardId);
        }
        return;
      }

      const activeBackend = backendForSelectedDataSource();
      if (!activeBackend) {
        rawRecords = [];
        dataScan = null;
        return;
      }

      try {
        const scanResult = await withUiTimeout(activeBackend.scanRawData(query), UI_REMOTE_TIMEOUT_MS, 'Remote data scan');
        dataScan = scanResult.ok ? scanResult.data : null;
      } catch {
        dataScan = null;
      }

      let nextRecords: RawDataRecord[] = [];
      if (dataScan?.results.length) {
        const streamResult = await withUiTimeout(activeBackend.streamRawData({
          schema: dataScan.schema,
          ...(query.datastoreKey ? { datastoreKey: query.datastoreKey } : {}),
          scanHash: dataScan.scanHash,
          chunkHash: dataScan.chunkHash || dataScan.scanHash,
          snapshotId: dataScan.snapshotId,
          head: dataScan.head,
          cursor: dataScan.cursor,
          nextCursor: dataScan.nextCursor,
          totalCount: dataScan.totalCount,
          highWaterMark: dataScan.highWaterMark,
          queryProfile: dataScan.queryProfile,
          ...(activeSelection?.syncFilter ? { syncFilter: activeSelection.syncFilter } : {}),
          records: dataScan.results,
        }), UI_REMOTE_TIMEOUT_MS, 'Remote data stream');
        nextRecords = streamResult.ok ? streamResult.data ?? [] : [];
      }
      if (nextRecords.length === 0 && (!dataScan || dataScan.results.length > 0)) {
        const result = await withUiTimeout(activeBackend.queryRawData(query), UI_REMOTE_TIMEOUT_MS, 'Remote data query');
        nextRecords = result.data ?? [];
      }
      rawRecords = nextRecords;
      pageIndex = nextPage;
      resetPnmSelectionIfNeeded();
      await ingestDownloadedRecords(rawRecords);
      if (rawRecords.length > 0 && !userEditedSql) {
        sqlQueryText = defaultSqlQuery(selectedStandardId);
        if (explorerSearchMode === 'sql') explorerSearchText = sqlQueryText;
      }
    } catch {
      rawRecords = [];
      dataScan = null;
    }
  }

  async function runLocalExplorerQuery(targetPage = pageIndex): Promise<void> {
    const nextPage = Math.max(0, targetPage);
    const queryVersion = ++localExplorerQueryVersion;
    clearLocalExplorerQueryTimer();
    localExplorerLoading = true;
    localExplorerError = '';
    const isCurrentQuery = () => queryVersion === localExplorerQueryVersion;
    try {
      const store = await withUiTimeout(
        ensureLocalFlatSqlStore(selectedDataSourceId, selectedSchemaSyncRow?.datastoreKey ?? selectedDatastoreKey),
        UI_LOCAL_QUERY_TIMEOUT_MS,
        'FlatSQL initialization',
      );
      if (!isCurrentQuery()) return;
      if (!store) {
        localExplorerResult = null;
        localExplorerFilteredTotalRows = null;
        localExplorerDatasetFiltersActive = false;
        return;
      }
      const explorerQuery = buildLocalDataExplorerQuery({
        standardId: selectedStandardId,
        page: nextPage,
        pageSize: normalizedPageSize(),
        searchText,
        searchColumns: localExplorerQueryColumns(selectedStandardId, localExplorerColumns),
        columnFilters,
        filterColumns: localExplorerColumns,
      });
      localExplorerDatasetFiltersActive = explorerQuery.hasDatasetFilters;
      const result = await withUiTimeout(
        store.query(explorerQuery.rowsSql, selectedStandardId, {
          defaultLimit: normalizedPageSize(),
          maxLimit: normalizedPageSize(),
          maxBytes: 128_000,
          timeoutMs: 5_000,
        }),
        UI_LOCAL_QUERY_TIMEOUT_MS,
        'Local FlatSQL query',
      );
      if (!isCurrentQuery()) return;
      localExplorerResult = result;
      if (explorerQuery.hasDatasetFilters) {
        const countResult = await withUiTimeout(
          store.query(explorerQuery.countSql, selectedStandardId, {
            defaultLimit: 1,
            maxLimit: 1,
            maxBytes: 16_000,
            timeoutMs: 5_000,
          }),
          UI_LOCAL_QUERY_TIMEOUT_MS,
          'Local FlatSQL count',
        ).catch(() => null);
        if (!isCurrentQuery()) return;
        localExplorerFilteredTotalRows = localDataExplorerCountFromResult(countResult);
      } else {
        localExplorerFilteredTotalRows = null;
      }
      try {
        const nextStats = await withUiTimeout(store.getStats({ includeCachedBytes: true }), UI_LOCAL_QUERY_TIMEOUT_MS, 'FlatSQL stats');
        if (!isCurrentQuery()) return;
        localFlatSqlStats = nextStats;
        localFlatSqlStatsLoaded = true;
      } catch {
        // Keep the cached snapshot visible if fresh stats are temporarily unavailable.
      }
      pageIndex = nextPage;
    } catch (error) {
      if (!isCurrentQuery()) return;
      localExplorerResult = null;
      localExplorerFilteredTotalRows = null;
      localExplorerError = error instanceof Error ? error.message : 'Local FlatSQL query failed';
    } finally {
      if (isCurrentQuery()) localExplorerLoading = false;
    }
  }

  function scheduleLocalExplorerQuery(targetPage = 0): void {
    clearLocalExplorerQueryTimer();
    localExplorerLoading = true;
    localExplorerQueryTimer = setTimeout(() => {
      localExplorerQueryTimer = null;
      void runLocalExplorerQuery(targetPage);
    }, LOCAL_EXPLORER_FILTER_DEBOUNCE_MS);
  }

  function clearLocalExplorerQueryTimer(): void {
    if (!localExplorerQueryTimer) return;
    clearTimeout(localExplorerQueryTimer);
    localExplorerQueryTimer = null;
  }

  function handleExplorerSourceChange(event: Event): void {
    selectedExplorerSourceKey = (event.currentTarget as HTMLSelectElement).value;
    const sourceRows = schemaSyncRows.filter((row) => subscriptionSourceKey(row.dataSourceId, row.datastoreKey) === selectedExplorerSourceKey);
    const selected = sourceRows.find((row) => row.id === selectedStandardId) ?? sourceRows[0] ?? null;
    if (selected) selectExplorerSchemaRow(selected);
  }

  function handleExplorerStandardChange(event: Event): void {
    selectedStandardId = (event.currentTarget as HTMLSelectElement).value;
    const selected = subscribedStandardOptions.find((row) => row.id === selectedStandardId) ?? subscribedStandardOptions[0] ?? null;
    if (selected) selectExplorerSchemaRow(selected);
  }

  function selectExplorerSchemaRow(selected: SchemaSyncRow): void {
    selectedSubscriptionId = selected.subscriptionId;
    selectedStandardId = selected.id;
    selectedDataSourceId = selected.dataSourceId;
    selectedDatastoreKey = selected.datastoreKey;
    resetLocalFlatSqlStore();
    userSelectedStandard = true;
    resetSqlForSelectedStandard();
    clearPnmSelection();
    columnFilters = {};
    searchText = '';
    explorerSearchText = explorerSearchMode === 'sql' ? sqlQueryText : '';
    dataScan = null;
    pageIndex = 0;
    localExplorerResult = null;
    localExplorerFilteredTotalRows = null;
    localExplorerDatasetFiltersActive = false;
    localExplorerError = '';
    void runLocalExplorerQuery(0);
    void runWorkbenchQuery(0);
  }

  function goToPreviousPage(): void {
    if (!canGoPrevious) return;
    if (explorerSearchMode === 'plain') {
      void runLocalExplorerQuery(pageIndex - 1);
      return;
    }
    void runWorkbenchQuery(pageIndex - 1);
  }

  function goToNextPage(): void {
    if (!canGoNext) return;
    if (explorerSearchMode === 'plain') {
      void runLocalExplorerQuery(pageIndex + 1);
      return;
    }
    void runWorkbenchQuery(pageIndex + 1);
  }

  function handleExplorerSearchModeChange(mode: ExplorerSearchMode): void {
    explorerSearchMode = mode;
    columnFilters = {};
    if (mode === 'sql') {
      sqlQueryText = sqlQueryText.trim() || defaultSqlQuery(selectedStandardId);
      explorerSearchText = sqlQueryText;
      searchText = '';
      return;
    }
    explorerSearchText = '';
    searchText = '';
    sqlResult = null;
    sqlError = '';
    pageIndex = 0;
    void runLocalExplorerQuery(0);
  }

  function handleExplorerSearchInput(event: Event): void {
    explorerSearchText = (event.currentTarget as HTMLInputElement | HTMLTextAreaElement).value;
    if (explorerSearchMode === 'plain') {
      searchText = explorerSearchText;
      sqlResult = null;
      sqlError = '';
      pageIndex = 0;
      scheduleLocalExplorerQuery(0);
      return;
    }
    sqlQueryText = explorerSearchText;
    userEditedSql = true;
  }

  function handleExplorerSearchKeydown(event: KeyboardEvent): void {
    if (explorerSearchMode !== 'sql') return;
    if (event.key !== 'Enter' || (!event.metaKey && !event.ctrlKey)) return;
    event.preventDefault();
    void handleExplorerSearchSubmit();
  }

  function handleSavedExplorerViewSelect(event: Event): void {
    selectedSavedExplorerViewId = (event.currentTarget as HTMLSelectElement).value;
    const selected = savedExplorerViews.find((view) => view.id === selectedSavedExplorerViewId);
    savedExplorerViewName = selected?.name ?? '';
    if (selected) applySavedExplorerView(selected.id);
  }

  function saveCurrentExplorerView(): void {
    const now = new Date().toISOString();
    const existing = savedExplorerViews.find((view) => view.id === selectedSavedExplorerViewId) ?? null;
    const name = savedExplorerViewName.trim() || currentExplorerViewName();
    const nextView: SavedExplorerView = {
      id: existing?.id ?? savedExplorerViewId(),
      name,
      dataSourceId: selectedDataSourceId,
      datastoreKey: selectedDatastoreKey,
      subscriptionId: selectedSubscriptionId,
      standardId: selectedStandardId,
      searchMode: explorerSearchMode,
      searchText: explorerSearchMode === 'plain' ? explorerSearchText.trim() : searchText.trim(),
      sqlText: explorerSearchMode === 'sql' ? (explorerSearchText.trim() || sqlQueryText.trim() || defaultSqlQuery(selectedStandardId)) : sqlQueryText.trim(),
      columnFilters: { ...columnFilters },
      visibleColumnKeys: [...visibleColumnKeys],
      pageSize: normalizedPageSize(),
      sortColumn,
      sortDirection,
      createdAt: existing?.createdAt ?? now,
      updatedAt: now,
    };
    savedExplorerViews = [
      nextView,
      ...savedExplorerViews.filter((view) => view.id !== nextView.id),
    ].slice(0, 64);
    selectedSavedExplorerViewId = nextView.id;
    savedExplorerViewName = nextView.name;
    persistSavedExplorerViews(savedExplorerViews);
  }

  function applySavedExplorerView(viewId = selectedSavedExplorerViewId): void {
    const view = savedExplorerViews.find((candidate) => candidate.id === viewId);
    if (!view) return;
    const selected = schemaSyncRows.find((row) => (
      row.subscriptionId === view.subscriptionId
      || (
        row.dataSourceId === view.dataSourceId
        && row.id === view.standardId
        && row.datastoreKey === view.datastoreKey
      )
    ));
    if (selected) {
      selectExplorerSchemaRow(selected);
    } else {
      selectedDataSourceId = view.dataSourceId;
      selectedDatastoreKey = view.datastoreKey;
      selectedSubscriptionId = view.subscriptionId;
      selectedStandardId = view.standardId;
      selectedExplorerSourceKey = subscriptionSourceKey(view.dataSourceId, view.datastoreKey);
      resetLocalFlatSqlStore();
    }
    pageSize = view.pageSize;
    sortColumn = view.sortColumn;
    sortDirection = view.sortDirection;
    columnFilters = { ...view.columnFilters };
    visibleColumnKeys = [...view.visibleColumnKeys];
    selectedSavedExplorerViewId = view.id;
    savedExplorerViewName = view.name;
    explorerSearchMode = view.searchMode;
    pageIndex = 0;
    if (view.searchMode === 'sql') {
      sqlQueryText = view.sqlText.trim() || defaultSqlQuery(view.standardId);
      explorerSearchText = sqlQueryText;
      searchText = '';
      userEditedSql = true;
      void runSqlQuery();
      return;
    }
    searchText = view.searchText;
    explorerSearchText = view.searchText;
    sqlResult = null;
    sqlError = '';
    scheduleLocalExplorerQuery(0);
  }

  function deleteSelectedExplorerView(): void {
    if (!selectedSavedExplorerViewId) return;
    savedExplorerViews = savedExplorerViews.filter((view) => view.id !== selectedSavedExplorerViewId);
    selectedSavedExplorerViewId = '';
    savedExplorerViewName = '';
    persistSavedExplorerViews(savedExplorerViews);
  }

  async function handleExplorerSearchSubmit(): Promise<void> {
    if (explorerSearchMode !== 'sql') return;
    sqlQueryText = explorerSearchText.trim() || defaultSqlQuery(selectedStandardId);
    explorerSearchText = sqlQueryText;
    userEditedSql = true;
    await runSqlQuery();
  }

  function handleColumnFilterInput(column: string, event: Event): void {
    const value = (event.currentTarget as HTMLInputElement).value;
    const nextFilters = { ...columnFilters };
    if (value.trim()) {
      nextFilters[column] = value;
    } else {
      delete nextFilters[column];
    }
    columnFilters = nextFilters;
    pageIndex = 0;
    if (explorerSearchMode === 'plain') scheduleLocalExplorerQuery(0);
  }

  function setDataSection(section: DataSection): void {
    selectedDataSection = section;
    if (section !== 'store') expandedCatalogActionRowKey = '';
    if (section === 'explorer') {
      void runLocalExplorerQuery(0);
    }
  }

  function buildCatalogRows(rows: SchemaSyncRow[]): DataCatalogRow[] {
    return buildDataCatalogRows(rows.map((row) => ({
      subscriptionId: row.subscriptionId,
      dataSourceId: row.dataSourceId,
      datastoreKey: row.datastoreKey,
      standardId: row.id,
      providerName: row.providerName,
      providerPeerId: row.providerPeerId,
      providerPublicKey: row.providerPublicKey,
      remoteRows: row.remoteRows,
      localRows: row.localRows,
      pinnedRows: row.progress.pinnedRows,
      cachedBytes: row.cachedBytes,
      storageCap: row.preference.storageCap,
      storageUnit: row.preference.storageUnit,
      syncStatus: row.progress.status,
      nextSyncAttempt: nextSyncAttemptLabel(row),
      lastSyncedAt: row.progress.lastSyncedAt,
      syncFilter: row.syncFilter,
    })));
  }

  function schemaForCatalogRow(row: DataCatalogRow): SchemaSyncRow | null {
    return schemaSyncRows.find((schema) => row.subscriptionId && schema.subscriptionId === row.subscriptionId)
      ?? schemaSyncRows.find((schema) => (
        schema.id === row.messageTypes[0]
        && schema.providerName === row.provider
        && schema.providerPeerId === row.providerPeerId
        && schema.datastoreKey === row.datastoreKey
      ))
      ?? null;
  }

  function catalogRowForSchema(schema: SchemaSyncRow): DataCatalogRow | null {
    return dataCatalogRows.find((row) => row.subscriptionId === schema.subscriptionId)
      ?? dataCatalogRows.find((row) => (
        row.messageTypes.includes(schema.id)
        && row.provider === schema.providerName
        && row.providerPeerId === schema.providerPeerId
        && row.datastoreKey === schema.datastoreKey
      ))
      ?? null;
  }

  function selectCatalogRow(row: DataCatalogRow, section: DataSection): void {
    const schema = schemaForCatalogRow(row);
    selectedDataSection = section;
    expandedCatalogActionRowKey = '';
    if (!schema) return;
    if (section === 'explorer') {
      selectExplorerSchemaRow(schema);
      return;
    }
    selectedSubscriptionId = schema.subscriptionId;
    selectedStandardId = schema.id;
    selectedDataSourceId = schema.dataSourceId;
    selectedDatastoreKey = schema.datastoreKey;
    userSelectedStandard = true;
    columnFilters = {};
    clearPnmSelection();
    dataScan = null;
    pageIndex = 0;
  }

  function catalogRowKey(row: DataCatalogRow): string {
    return row.id || [
      row.subscriptionId,
      row.provider,
      row.product,
      row.messageTypes.join(','),
      row.datastoreKey ?? '',
      row.providerPeerId ?? row.providerPublicKey ?? '',
    ].join('|');
  }

  function toggleCatalogRowActionsByKey(key: string): void {
    expandedCatalogActionRowKey = expandedCatalogActionRowKey === key ? '' : key;
  }

  function toggleCatalogRowActions(row: DataCatalogRow): void {
    toggleCatalogRowActionsByKey(catalogRowKey(row));
  }

  function suppressCatalogOutsideClearOnce(): void {
    suppressCatalogOutsideUntil = performance.now() + 150;
  }

  function handleCatalogCellButtonClick(row: DataCatalogRow, event: MouseEvent): void {
    event.stopPropagation();
    suppressCatalogOutsideClearOnce();
    toggleCatalogRowActions(row);
  }

  function handleCatalogRowKeydown(row: DataCatalogRow, event: KeyboardEvent): void {
    if (event.key !== 'Enter' && event.key !== ' ') return;
    event.preventDefault();
    event.stopPropagation();
    suppressCatalogOutsideClearOnce();
    toggleCatalogRowActions(row);
  }

  function handleCatalogOutsideClick(event: MouseEvent): void {
    if (performance.now() < suppressCatalogOutsideUntil) return;
    if (!expandedCatalogActionRowKey) return;
    if (event.composedPath().some((target) => target instanceof Element && target.closest('[data-catalog-row-key]'))) return;
    expandedCatalogActionRowKey = '';
  }

  function catalogRowCountsLabel(row: DataCatalogRow): string {
    const schema = schemaForCatalogRow(row);
    if (schema && isSchemaRemoteRowsLoading(schema)) return 'Loading';
    return `${formatNumber(row.storage.localRows)} local / ${formatNumber(row.storage.remoteRows)} remote`;
  }

  function catalogRowStorageLabel(row: DataCatalogRow): string {
    return formatBytes(row.storage.cachedBytes);
  }

  function catalogRowFreshnessLabel(row: DataCatalogRow): string {
    return row.sync.lastSyncedAt ? formatDateTime(row.sync.lastSyncedAt) : 'Never synced';
  }

  function catalogRowSyncLabel(row: DataCatalogRow): string {
    const schema = schemaForCatalogRow(row);
    if (schema && isSchemaRemoteRowsLoading(schema)) return 'Loading';
    return row.sync.label;
  }

  function catalogRowSourceCountLabel(row: DataCatalogRow): string {
    const sourceCount = row.providerPeerId || row.providerPublicKey ? 1 : 0;
    if (sourceCount === 0) return 'No source nodes advertised';
    return `${sourceCount} source node`;
  }

  function catalogRowProviderIdentityLabel(row: DataCatalogRow): string {
    const identity = row.providerPeerId ?? row.providerPublicKey;
    return identity ? shorten(identity, 42) : 'Provider identity unavailable';
  }

  function ownertrustLabel(ownertrust: string | null | undefined): string {
    switch (ownertrust) {
      case 'never': return 'Do not trust';
      case 'marginal': return 'Limited trust';
      case 'full': return 'Full trust';
      case 'ultimate': return 'Ultimate trust';
      case 'unknown':
      default:
        return 'Unknown trust';
    }
  }

  function catalogRowTrustLabel(row: DataCatalogRow): string {
    const trustKeys = [row.providerPeerId, row.providerPublicKey].filter(Boolean) as string[];
    const ownertrust = trustKeys.map((key) => dataDirectoryState.peerTrust[key]).find(Boolean);
    return `PGP: ${ownertrustLabel(ownertrust)}`;
  }

  function catalogRowVerificationLabel(row: DataCatalogRow): string {
    if (row.sync.status === 'failed') return 'Verification needs attention';
    if (row.storage.pinnedRows > 0) return 'Pinned artifacts tracked';
    if (row.sync.status === 'synced') return 'Synced from provider';
    return 'Verification pending';
  }

  function catalogRowUpdateCadenceLabel(row: DataCatalogRow): string {
    if (row.sync.nextAttempt && row.sync.nextAttempt !== 'Not scheduled') return row.sync.nextAttempt;
    if (row.sync.lastSyncedAt) return `Last synced ${formatDateTime(row.sync.lastSyncedAt)}`;
    return 'Update cadence unavailable';
  }

  function catalogRowStorageEstimateLabel(row: DataCatalogRow): string {
    return `${catalogRowStorageLabel(row)} local · ${row.storage.policyLabel}`;
  }

  function catalogRowRawDataAvailable(row: DataCatalogRow): boolean {
    return row.access.state !== 'locked';
  }

  function catalogRowRestrictionLabel(row: DataCatalogRow): string {
    if (row.access.state === 'locked') return 'Raw records are hidden until this node has an active entitlement.';
    if (row.access.state === 'expired') return 'Access has expired; retained local data remains separate from subscription access.';
    if (row.access.state === 'payment-failed') return 'Sync is paused until billing is restored; retained local data remains visible.';
    if (row.access.state === 'over-quota') return 'Sync is capped by quota; retained local data remains visible.';
    return 'Access, storage, and sync are managed separately for this product.';
  }

  function catalogRowPrimaryActionLabel(row: DataCatalogRow): string {
    if (row.sync.status === 'failed') return 'Retry sync';
    switch (row.access.state) {
      case 'locked': return 'View plans';
      case 'trial': return 'Manage trial';
      case 'paid-active': return 'Manage subscription';
      case 'expired': return 'Renew access';
      case 'payment-failed': return 'Update payment';
      case 'over-quota': return 'Increase limit';
      case 'free': return 'Manage storage';
      case 'unknown':
      default:
        return 'Details';
    }
  }

  function handleCatalogPrimaryAction(row: DataCatalogRow): void {
    const schema = schemaForCatalogRow(row);
    if (row.sync.status === 'failed' && schema) {
      retrySubscriptionSync(schema);
      return;
    }
    selectCatalogRow(row, 'subscriptions');
  }

  function overviewDonutStyle(segments: DataOverviewStorageSegment[]): string {
    if (segments.length === 0) {
      return 'background: radial-gradient(circle at center, rgba(12, 14, 18, 0.95) 0 58%, transparent 59%), conic-gradient(rgba(255, 255, 255, 0.08) 0 100%);';
    }
    let cursor = 0;
    const stops = segments.map((segment) => {
      const start = cursor;
      cursor += segment.percent;
      return `${storageSegmentColor(segment)} ${start}% ${Math.max(start, cursor)}%`;
    });
    if (cursor < 100) stops.push(`rgba(255, 255, 255, 0.08) ${cursor}% 100%`);
    return `background: radial-gradient(circle at center, rgba(12, 14, 18, 0.95) 0 58%, transparent 59%), conic-gradient(${stops.join(', ')});`;
  }

  function storageSegmentStyle(segment: DataOverviewStorageSegment): string {
    return `--sdn-segment-color: ${storageSegmentColor(segment)}`;
  }

  function providerBarStyle(bar: DataOverviewProviderBar): string {
    return `--sdn-provider-bar-width: ${Math.max(0, Math.min(100, bar.percent))}%`;
  }

  function selectOverviewProviderBar(bar: DataOverviewProviderBar): void {
    const row = dataCatalogRows.find((candidate) => (
      candidate.provider === bar.provider
      && candidate.providerPeerId === bar.providerPeerId
    )) ?? dataCatalogRows.find((candidate) => candidate.provider === bar.provider);
    if (row) selectCatalogRow(row, 'store');
  }

  function accessStateColor(state: string): string {
    switch (state) {
      case 'paid-active': return '#0a84ff';
      case 'trial': return '#bf5af2';
      case 'locked': return '#8e8e93';
      case 'expired': return '#ff9f0a';
      case 'over-quota': return '#ffd60a';
      case 'payment-failed': return '#ff453a';
      case 'unknown': return '#64d2ff';
      case 'free':
      default:
        return '#30d158';
    }
  }

  function storageSegmentColor(segment: DataOverviewStorageSegment): string {
    if (isAccessStateKey(segment.key)) return accessStateColor(segment.key);
    const colors = ['#0a84ff', '#30d158', '#bf5af2', '#ff9f0a', '#64d2ff', '#ffd60a', '#ff453a', '#5e5ce6'];
    let hash = 0;
    for (const char of segment.key) hash = ((hash << 5) - hash + char.charCodeAt(0)) | 0;
    return colors[Math.abs(hash) % colors.length] ?? colors[0];
  }

  function isAccessStateKey(value: string): boolean {
    return ['free', 'paid-active', 'trial', 'locked', 'expired', 'over-quota', 'payment-failed', 'unknown'].includes(value);
  }

  function isCatalogRowSelected(row: DataCatalogRow): boolean {
    const schema = schemaForCatalogRow(row);
    return schema ? isSchemaRowSelected(schema) : false;
  }

  function openSchemaInExplorer(schema: SchemaSyncRow): void {
    selectedDataSection = 'explorer';
    selectExplorerSchemaRow(schema);
  }

  function openSubscriptionDetails(schema: SchemaSyncRow): void {
    selectedSubscriptionId = schema.subscriptionId;
    selectedStandardId = schema.id;
    selectedDataSourceId = schema.dataSourceId;
    selectedDatastoreKey = schema.datastoreKey;
    selectedSubscriptionDetailId = schema.subscriptionId;
  }

  function closeSubscriptionDetails(): void {
    selectedSubscriptionDetailId = '';
  }

  function sortedMessageTypeRows(rows: SchemaSyncRow[]): SchemaSyncRow[] {
    return [...rows].sort((left, right) => {
      const leftRows = isSchemaRemoteRowsLoading(left) ? -1 : left.remoteRows;
      const rightRows = isSchemaRemoteRowsLoading(right) ? -1 : right.remoteRows;
      return rightRows - leftRows
        || left.id.localeCompare(right.id)
        || left.providerName.localeCompare(right.providerName);
    });
  }

  function filterSubscriptionRows(rows: SchemaSyncRow[], filter: SubscriptionFilter, query = ''): SchemaSyncRow[] {
    const normalizedQuery = query.trim().toLowerCase();
    return rows.filter((schema) => {
      if (filter !== 'all' && !subscriptionMatchesFilter(schema, filter)) return false;
      if (!normalizedQuery) return true;
      return subscriptionSearchTextFor(schema).includes(normalizedQuery);
    });
  }

  function subscriptionSearchTextFor(schema: SchemaSyncRow): string {
    return [
      schema.id,
      schema.providerName,
      schema.providerId,
      schema.providerPeerId,
      schema.providerPublicKey,
      schema.sourceName,
      schema.datastoreKey,
      schema.queryProfile,
      schema.retentionPolicy,
      subscriptionProductLabel(schema),
      subscriptionAccessLabel(schema),
      subscriptionPlanLabel(schema),
      subscriptionCostLabel(schema),
      subscriptionStorageStateLabel(schema),
      schemaHealthLabel(schema),
      syncStatusLabel(schema),
      schema.progress.error,
    ].filter(Boolean).join(' ').toLowerCase();
  }

  function subscriptionMatchesFilter(schema: SchemaSyncRow, filter: SubscriptionFilter): boolean {
    const row = catalogRowForSchema(schema);
    const accessState = row?.access.state ?? 'free';
    const planText = [
      row?.plan.label,
      row?.plan.priceLabel,
      row?.plan.renewalLabel,
      row?.plan.quotaLabel,
    ].filter(Boolean).join(' ').toLowerCase();
    switch (filter) {
      case 'active':
        return schema.preference.mode === 'sync'
          && accessState !== 'locked'
          && accessState !== 'expired'
          && accessState !== 'payment-failed'
          && accessState !== 'over-quota';
      case 'trials': return accessState === 'trial';
      case 'expiring': return row?.plan.renewalLabel !== undefined && row.plan.renewalLabel !== 'No renewal';
      case 'payment-issues': return accessState === 'payment-failed';
      case 'over-quota': return accessState === 'over-quota' || schema.progress.status === 'capped';
      case 'canceled': return accessState === 'expired' || planText.includes('cancel');
      case 'free': return accessState === 'free';
      case 'paid':
        return accessState === 'paid-active'
          || accessState === 'trial'
          || accessState === 'locked'
          || accessState === 'expired'
          || accessState === 'over-quota'
          || accessState === 'payment-failed';
      case 'usage-based': return /usage|overage|per-|per |quota/.test(planText);
      case 'enterprise': return planText.includes('enterprise');
      case 'all':
      default:
        return true;
    }
  }

  function syncSelectedStandardWithSubscriptions(rows: SchemaSyncRow[]): void {
    if (rows.length === 0) return;
    if (selectedSubscriptionId && rows.some((row) => row.subscriptionId === selectedSubscriptionId)) return;
    if (userSelectedStandard && rows.some((row) => (
      row.id === selectedStandardId
      && row.dataSourceId === selectedDataSourceId
      && (!selectedDatastoreKey || row.datastoreKey === selectedDatastoreKey)
    ))) return;
    const next = rows[0];
    selectedSubscriptionId = next.subscriptionId;
    selectedStandardId = next.id;
    selectedDataSourceId = next.dataSourceId;
    selectedDatastoreKey = next.datastoreKey;
    resetSqlForSelectedStandard();
    clearPnmSelection();
    dataScan = null;
    pageIndex = 0;
  }

  function isSchemaRowSelected(schema: SchemaSyncRow): boolean {
    if (selectedSubscriptionId) return schema.subscriptionId === selectedSubscriptionId;
    return schema.id === selectedStandardId
      && schema.dataSourceId === selectedDataSourceId
      && (selectedDatastoreKey === null || schema.datastoreKey === selectedDatastoreKey);
  }

  function selectedSchemaSyncRowForSelection(): SchemaSyncRow | null {
    return schemaSyncRows.find((row) => selectedSubscriptionId && row.subscriptionId === selectedSubscriptionId)
      ?? schemaSyncRows.find((row) => (
        row.id === selectedStandardId
        && row.dataSourceId === selectedDataSourceId
        && (selectedDatastoreKey === null || row.datastoreKey === selectedDatastoreKey)
      ))
      ?? null;
  }

  function handleSubscriptionStorageCapInput(schema: SchemaSyncRow, event: Event): void {
    const storageCap = normalizedStorageCap((event.currentTarget as HTMLInputElement).value);
    updateSubscription(schema.subscriptionId, { storageCap });
    updateSchemaSyncPreference(schema.id, { mode: 'sync', storageCap }, schema.dataSourceId, schema.datastoreKey);
    scheduleSubscribedSchemaSyncs(schemaSyncRows);
  }

  function handleSubscriptionStorageUnitChange(schema: SchemaSyncRow, event: Event): void {
    const value = (event.currentTarget as HTMLSelectElement).value;
    const storageUnit = isStorageUnit(value) ? value : DEFAULT_SCHEMA_SYNC_PREFERENCE.storageUnit;
    updateSubscription(schema.subscriptionId, { storageUnit });
    updateSchemaSyncPreference(schema.id, { mode: 'sync', storageUnit }, schema.dataSourceId, schema.datastoreKey);
    scheduleSubscribedSchemaSyncs(schemaSyncRows);
  }

  function handleSubscriptionFilterInput(schema: SchemaSyncRow, event: Event): void {
    updateSubscription(schema.subscriptionId, {
      syncFilter: (event.currentTarget as HTMLInputElement).value,
    });
  }

  function handleSubscriptionQueryProfileChange(schema: SchemaSyncRow, event: Event): void {
    updateSubscription(schema.subscriptionId, {
      queryProfile: normalizeDataQueryProfile((event.currentTarget as HTMLSelectElement).value),
    });
    schemaSyncSchedulerForDataSource(schema.dataSourceId).reset();
  }

  function handleSubscriptionRetentionPolicyChange(schema: SchemaSyncRow, event: Event): void {
    updateSubscription(schema.subscriptionId, {
      retentionPolicy: normalizeDataFeedRetentionPolicy((event.currentTarget as HTMLSelectElement).value, schema.id),
    });
    schemaSyncSchedulerForDataSource(schema.dataSourceId).reset();
  }

  function pauseSubscriptionSync(schema: SchemaSyncRow): void {
    const key = schemaSyncPreferenceKey(schema.dataSourceId, schema.id, schema.datastoreKey);
    pausedSyncKeys = new Set(pausedSyncKeys).add(key);
    updateSchemaSyncPreference(schema.id, { mode: 'preview' }, schema.dataSourceId, schema.datastoreKey);
    schemaSyncSchedulerForDataSource(schema.dataSourceId).reset();
    if (activeSyncKeys.has(key)) {
      const nextActive = new Set(activeSyncKeys);
      nextActive.delete(key);
      activeSyncKeys = nextActive;
      resetLocalFlatSqlStore();
    }
    refreshSchemaSyncProgress(schema.id, {
      status: 'idle',
      error: null,
      downloadSpeedBytesPerSecond: 0,
      wireSpeedUtilization: null,
    }, schema.dataSourceId, schema.datastoreKey);
  }

  function resumeSubscriptionSync(schema: SchemaSyncRow): void {
    const key = schemaSyncPreferenceKey(schema.dataSourceId, schema.id, schema.datastoreKey);
    const nextPaused = new Set(pausedSyncKeys);
    nextPaused.delete(key);
    pausedSyncKeys = nextPaused;
    updateSchemaSyncPreference(schema.id, { mode: 'sync' }, schema.dataSourceId, schema.datastoreKey);
    scheduleSubscribedSchemaSyncs(schemaSyncRows);
    void synchronizeSchema(schema.id, schema.dataSourceId, schema.subscriptionId);
  }

  function retrySubscriptionSync(schema: SchemaSyncRow): void {
    const key = schemaSyncPreferenceKey(schema.dataSourceId, schema.id, schema.datastoreKey);
    const stalled = schemaSyncStalled(schema);
    const nextPaused = new Set(pausedSyncKeys);
    nextPaused.delete(key);
    pausedSyncKeys = nextPaused;
    const nextActive = new Set(activeSyncKeys);
    nextActive.delete(key);
    activeSyncKeys = nextActive;
    if (stalled) resetLocalFlatSqlStore();
    updateSchemaSyncPreference(schema.id, { mode: 'sync' }, schema.dataSourceId, schema.datastoreKey);
    refreshSchemaSyncProgress(schema.id, {
      status: 'idle',
      error: null,
      downloadSpeedBytesPerSecond: 0,
      wireSpeedUtilization: null,
    }, schema.dataSourceId, schema.datastoreKey);
    schemaSyncSchedulerForDataSource(schema.dataSourceId).reset();
    void synchronizeSchema(schema.id, schema.dataSourceId, schema.subscriptionId);
  }

  function clearPinVerifyToastTimer(): void {
    if (!pinVerifyToastTimer) return;
    clearTimeout(pinVerifyToastTimer);
    pinVerifyToastTimer = null;
  }

  function dismissPinVerifyToast(): void {
    clearPinVerifyToastTimer();
    pinVerifyToast = null;
  }

  function showPinVerifyToast(message: string, tone: PinVerifyToastTone = 'success'): void {
    clearPinVerifyToastTimer();
    pinVerifyToast = {
      id: pinVerifyToastId + 1,
      message,
      tone,
    };
    pinVerifyToastId = pinVerifyToast.id;
    pinVerifyToastTimer = setTimeout(() => {
      pinVerifyToast = null;
      pinVerifyToastTimer = null;
    }, 5200);
  }

  async function verifyPinnedArtifacts(schema: SchemaSyncRow): Promise<void> {
    pinVerifyRunning = true;
    try {
      const store = await ensureLocalFlatSqlStore(schema.dataSourceId, schema.datastoreKey);
      if (!store) throw new Error('FlatSQL initialization failed');
      const entries = await store.listPinLedgerEntries({
        standardId: schema.id,
        role: 'shard',
        verificationState: 'verified',
      });
      const pinnedRows = entries.reduce((total, entry) => total + Math.max(0, entry.rowCount ?? 0), 0);
      const pinnedBytes = entries.reduce((total, entry) => total + Math.max(0, entry.byteCount ?? 0), 0);
      if (entries.length === 0) {
        showPinVerifyToast(`No verified pinned ${schema.id} shard artifacts for ${schema.providerName}.`, 'warning');
        return;
      }
      showPinVerifyToast(`Verified ${formatNumber(entries.length)} ${schema.id} shard artifacts covering ${formatNumber(pinnedRows)} rows / ${formatBytes(pinnedBytes)}.`, 'success');
    } catch (error) {
      showPinVerifyToast(error instanceof Error ? error.message : 'Pinned artifact verification failed', 'error');
    } finally {
      pinVerifyRunning = false;
    }
  }

  onDestroy(() => {
    clearPinVerifyToastTimer();
    clearLocalExplorerQueryTimer();
  });

  function updateSubscription(subscriptionId: string, patch: Partial<Pick<DataFeedSubscription, 'providerId' | 'sourceName' | 'remoteRows' | 'storageCap' | 'storageUnit' | 'syncFilter' | 'queryProfile' | 'retentionPolicy'>>): void {
    dataDirectoryState = updateDataFeedSubscription(dataDirectoryState, subscriptionId, patch);
    persistDataDirectoryState(dataDirectoryState);
  }

  function refreshSubscriptionRemoteRowsFromSummary(dataSourceId: string, summary: DataSummary | null): void {
    if (!summary) return;
    let nextState = dataDirectoryState;
    for (const subscription of dataDirectoryState.subscriptions) {
      if (subscription.dataSourceId !== dataSourceId) continue;
      const remoteRows = remoteRowsForSummarySubscription(summary, subscription);
      const identity = feedIdentityForSummarySubscription(summary, subscription);
      const patch: Partial<Pick<DataFeedSubscription, 'providerId' | 'sourceName' | 'remoteRows'>> = {};
      if (remoteRows !== null && remoteRows !== subscription.remoteRows) patch.remoteRows = remoteRows;
      if (identity && identity.providerId !== subscription.providerId) patch.providerId = identity.providerId;
      if (identity && identity.sourceName !== subscription.sourceName) patch.sourceName = identity.sourceName;
      if (Object.keys(patch).length === 0) continue;
      nextState = updateDataFeedSubscription(nextState, subscription.id, patch);
    }
    if (nextState === dataDirectoryState) return;
    dataDirectoryState = nextState;
    persistDataDirectoryState(dataDirectoryState);
  }

  async function runSqlQuery(): Promise<void> {
    const query = sqlQueryText.trim();
    if (!query) {
      sqlResult = null;
      sqlError = '';
      return;
    }
    sqlRunning = true;
    sqlError = '';
    try {
      const store = await withUiTimeout(ensureLocalFlatSqlStore(selectedDataSourceId, selectedSchemaSyncRow?.datastoreKey ?? selectedDatastoreKey), UI_LOCAL_QUERY_TIMEOUT_MS, 'FlatSQL initialization');
      if (!store) return;
      sqlResult = await withUiTimeout(store.query(query, selectedStandardId, {
        defaultLimit: normalizedPageSize(),
        maxLimit: normalizedPageSize(),
        maxBytes: 64_000,
        timeoutMs: 5_000,
      }), UI_LOCAL_QUERY_TIMEOUT_MS, 'Local SQL query');
    } catch (error) {
      sqlResult = null;
      sqlError = error instanceof Error ? error.message : 'SQL query failed';
    } finally {
      sqlRunning = false;
    }
  }

  async function ingestDownloadedRecords(records: RawDataRecord[]): Promise<void> {
    if (records.length === 0) return;
    try {
      const store = await ensureLocalFlatSqlStore(selectedDataSourceId, selectedSchemaSyncRow?.datastoreKey ?? selectedDatastoreKey);
      if (!store) return;
      const source = currentDataSourceOption();
      await store.ingestRecords(selectedStandardId, records, source?.publicKey ?? source?.peerId ?? source?.id ?? null);
      await refreshLocalFlatSqlStats();
    } catch (error) {
      sqlError = error instanceof Error ? error.message : 'FlatSQL ingest failed';
    }
  }

  function scheduleSubscribedSchemaSyncs(
    rows: SchemaSyncRow[],
    activeBackend: SdnBackend | null = backend,
    pageLoading = dataPageLoading,
    sources: DataSourceOption[] = dataSourceOptions,
  ): void {
    if (!activeBackend || pageLoading) return;
    const availableSourceIds = new Set(sources.map((source) => source.id));
    const bySource = new Map<string, SchemaSyncRow[]>();
    for (const row of rows) {
      if (row.preference.mode !== 'sync') continue;
      if (!availableSourceIds.has(row.dataSourceId)) continue;
      bySource.set(row.dataSourceId, [...bySource.get(row.dataSourceId) ?? [], row]);
    }
    for (const [dataSourceId, sourceRows] of bySource) {
      void schemaSyncSchedulerForDataSource(dataSourceId).schedule(sourceRows, dataSourceId);
    }
  }

  function schemaSyncSchedulerForDataSource(dataSourceId: string): typeof schemaSyncScheduler {
    const existing = schemaSyncSchedulers.get(dataSourceId);
    if (existing) return existing;
    const scheduler = createSchemaSyncScheduler({
      syncSchema: (standardId, sourceId, subscriptionId) => synchronizeSchema(standardId, sourceId, subscriptionId),
    });
    schemaSyncSchedulers.set(dataSourceId, scheduler);
    return scheduler;
  }

  async function pruneUnsubscribedReplaceSnapshotStores(
    sources: DataDirectoryMigrationSource[],
    subscriptions: DataFeedSubscription[],
  ): Promise<void> {
    if (REPLACE_SNAPSHOT_STANDARD_IDS.length === 0) return;
    const subscribedSnapshotKeys = new Set(
      subscriptions
        .filter((subscription) => REPLACE_SNAPSHOT_STANDARD_IDS.includes(subscription.standardId))
        .map((subscription) => snapshotStoreKey(subscription.dataSourceId, subscription.datastoreKey, subscription.standardId)),
    );
    const sourceIds = new Set([
      ...sources.map((source) => source.dataSourceId.trim()).filter(Boolean),
      ...subscriptions.map((subscription) => subscription.dataSourceId.trim()).filter(Boolean),
    ]);
    for (const dataSourceId of sourceIds) {
      for (const standardId of REPLACE_SNAPSHOT_STANDARD_IDS) {
        if (subscribedSnapshotKeys.has(snapshotStoreKey(dataSourceId, null, standardId))) continue;
        const persistenceKey = localFlatSqlPersistenceKey(dataSourceId);
        if (localFlatSqlStoreKey === persistenceKey) resetLocalFlatSqlStore();
        await clearLocalFlatSqlStore({
          persistenceKey,
          standardIds: [standardId],
          desktopPersistenceBaseUrl: localFlatSqlDesktopPersistenceBaseUrl(),
        });
        clearSchemaSyncProgressForSubscription(dataSourceId, standardId, null);
      }
    }
  }

  function snapshotStoreKey(dataSourceId: string, datastoreKey: string | null, standardId: string): string {
    return `${dataSourceId}:${datastoreKey ?? ''}:${standardId}`;
  }

  function resetSchemaSyncSchedulers(): void {
    schemaSyncScheduler.reset();
    for (const scheduler of schemaSyncSchedulers.values()) scheduler.reset();
  }

  async function synchronizeSchema(standardId: string, dataSourceId = selectedDataSourceId, subscriptionId = ''): Promise<void> {
    const subscription = subscriptionForSync(dataSourceId, standardId, subscriptionId);
    const datastoreKey = subscription?.datastoreKey ?? null;
    const key = schemaSyncPreferenceKey(dataSourceId, standardId, datastoreKey);
    const preference = subscription
      ? subscriptionSchemaSyncPreference(subscription)
      : schemaSyncPreferenceFor(dataSourceId, standardId, datastoreKey);
    if (preference.mode !== 'sync' || activeSyncKeys.has(key)) return;
    if (pausedSyncKeys.has(key)) return;

    const source = dataSourceOptionForId(dataSourceId);
    const backendConfig = backendConfigForDataSource(source, datastoreKey, subscription);
    if (!backendConfig) return;
    const remoteRows = subscription?.remoteRows ?? remoteRowsForSubscription(dataSourceId, standardId, datastoreKey) ?? totalRowsForStandardId(dataSummary, standardId) ?? 0;
    const syncFilter = subscription?.syncFilter ?? syncFilterForSubscription(dataSourceId, standardId, datastoreKey);
    const queryProfile = subscriptionQueryProfileFor(subscription);
    const retentionPolicy = subscriptionRetentionPolicyFor(subscription, standardId);
    let initialProgress = schemaSyncProgressFor(
      dataSourceId,
      standardId,
      remoteRows,
      localFlatSqlStats,
      selectedDataSourceId === dataSourceId && selectedDatastoreKey === datastoreKey,
      datastoreKey,
    );
    if (syncFilterChangedRequiresReset(initialProgress, syncFilter) || retentionPolicyRequiresReset(initialProgress, retentionPolicy)) {
      const persistenceKey = localFlatSqlPersistenceKey(dataSourceId, datastoreKey);
      if (localFlatSqlStoreKey === persistenceKey) resetLocalFlatSqlStore();
      await clearLocalFlatSqlStore({
        persistenceKey,
        standardIds: [standardId],
        desktopPersistenceBaseUrl: localFlatSqlDesktopPersistenceBaseUrl(),
      });
      clearSchemaSyncProgressForSubscription(dataSourceId, standardId, datastoreKey);
      if (selectedDataSourceId === dataSourceId && selectedDatastoreKey === datastoreKey) {
        await refreshLocalFlatSqlStats();
      }
      initialProgress = schemaSyncProgressFor(
        dataSourceId,
        standardId,
        remoteRows,
        localFlatSqlStats,
        selectedDataSourceId === dataSourceId && selectedDatastoreKey === datastoreKey,
        datastoreKey,
      );
    }
    activeSyncKeys = new Set(activeSyncKeys).add(key);
    refreshSchemaSyncProgress(standardId, {
      status: 'syncing',
      error: null,
      totalRows: remoteRows,
      providerPeerId: source?.peerId ?? null,
      providerPublicKey: source?.publicKey ?? null,
      syncFilter: syncFilter || null,
    }, dataSourceId, datastoreKey);

    let store: WorkerLocalFlatSqlStore | null = null;
    try {
      store = await ensureLocalFlatSqlStore(dataSourceId, datastoreKey);
      if (!store) throw new Error('FlatSQL initialization failed');
      const update = await store.syncSchema({
        standardId,
        schema: schemaNameForStandardId(standardId),
        backendConfig,
        initialProgress,
        totalRows: remoteRows,
        capBytes: storageCapBytes(preference),
        pageSize: SYNC_PAGE_SIZE,
        persistRecordInterval: SYNC_PERSIST_RECORD_INTERVAL,
        source: source?.publicKey ?? source?.peerId ?? source?.id ?? null,
        syncFilter,
        queryProfile,
      }, (nextUpdate) => applyWorkerSchemaSyncUpdate(standardId, dataSourceId, datastoreKey, nextUpdate));
      applyWorkerSchemaSyncUpdate(standardId, dataSourceId, datastoreKey, update);
    } catch (error) {
      if (pausedSyncKeys.has(key)) {
        refreshSchemaSyncProgress(standardId, {
          status: 'idle',
          error: null,
          downloadSpeedBytesPerSecond: 0,
          wireSpeedUtilization: null,
        }, dataSourceId, datastoreKey);
        return;
      }
      refreshSchemaSyncProgress(standardId, {
        status: 'error',
        error: error instanceof Error ? error.message : 'Schema sync failed',
      }, dataSourceId, datastoreKey);
      if (store) {
        await store.flush(standardId);
        await refreshLocalFlatSqlStats();
      }
    } finally {
      const nextActive = new Set(activeSyncKeys);
      nextActive.delete(key);
      activeSyncKeys = nextActive;
    }
  }

  function applyWorkerSchemaSyncUpdate(standardId: string, dataSourceId: string, datastoreKey: string | null, update: WorkerSchemaSyncUpdate): void {
    if (selectedDataSourceId === dataSourceId && selectedDatastoreKey === datastoreKey) {
      localFlatSqlStats = update.stats;
      localFlatSqlStatsLoaded = true;
      if (selectedDataSection === 'explorer' && selectedStandardId === standardId && update.progress.status !== 'syncing') {
        scheduleLocalExplorerQuery(pageIndex);
      }
    }
    refreshSchemaSyncProgress(standardId, update.progress, dataSourceId, datastoreKey);
  }

  function refreshSchemaSyncProgress(standardId: string, patch: Partial<SchemaSyncProgress>, dataSourceId = selectedDataSourceId, datastoreKey: string | null = selectedDatastoreKey): void {
    const key = schemaSyncPreferenceKey(dataSourceId, standardId, datastoreKey);
    const statsAreSelected = selectedDataSourceId === dataSourceId && selectedDatastoreKey === datastoreKey;
    const persisted = schemaSyncProgress[key];
    const publishedProgress = patch.queryProfile === 'dataset-publication-offset-v1' || persisted?.queryProfile === 'dataset-publication-offset-v1';
    const localRows = publishedProgress && typeof patch.localRows === 'number'
      ? patch.localRows
      : statsAreSelected ? localRowsForStandard(localFlatSqlStats, standardId) : patch.localRows ?? persisted?.localRows ?? 0;
    const cachedBytes = statsAreSelected ? cachedBytesForStandard(localFlatSqlStats, standardId) : patch.cachedBytes ?? persisted?.cachedBytes ?? 0;
    const current = schemaSyncProgressFor(dataSourceId, standardId, totalRowsForStandardId(dataSummary, standardId) ?? 0, localFlatSqlStats, statsAreSelected, datastoreKey);
    const nextProgress = {
      ...current,
      localRows,
      cachedBytes,
      ...patch,
      syncedRows: Math.max(patch.syncedRows ?? current.syncedRows, localRows),
    };
    const wireSpeedUtilization = boundedWireSpeedUtilization(nextProgress.wireSpeedUtilization);
    const rowCounts = syncRowCountSummary({
      localRows: nextProgress.localRows,
      syncedRows: nextProgress.syncedRows,
      pinnedRows: nextProgress.pinnedRows,
      remoteRows: nextProgress.totalRows,
      totalRows: nextProgress.totalRows,
    });
    const stallState = nextSchemaSyncStallState(persisted, {
      ...nextProgress,
      ...rowCounts,
    });
    schemaSyncProgress = {
      ...schemaSyncProgress,
      [key]: {
        ...nextProgress,
        ...rowCounts,
        wireSpeedUtilization,
        ...stallState,
      },
    };
    persistMeasuredWireSpeedBytesPerSecond(dataSourceId, schemaSyncProgress[key]?.measuredWireSpeedBytesPerSecond ?? 0);
    persistSchemaSyncProgress(schemaSyncProgress);
  }

  async function ensureLocalFlatSqlStore(dataSourceId = selectedDataSourceId, datastoreKey: string | null = selectedDatastoreKey): Promise<WorkerLocalFlatSqlStore | null> {
    const nextKey = localFlatSqlPersistenceKey(dataSourceId, datastoreKey);
    if (localFlatSqlStore && localFlatSqlStoreKey === nextKey) return localFlatSqlStore;
    if (localFlatSqlStorePromise && localFlatSqlStorePromiseKey === nextKey) return localFlatSqlStorePromise;
    resetLocalFlatSqlStore();
    localFlatSqlStorePromiseKey = nextKey;
    localFlatSqlStorePromise = (async () => {
      try {
        localFlatSqlStore = await createWorkerLocalFlatSqlStore({
          schemas: LOCAL_FLATSQL_SCHEMAS,
          persistenceKey: nextKey,
          desktopPersistenceBaseUrl: localFlatSqlDesktopPersistenceBaseUrl(),
        });
        localFlatSqlStoreKey = nextKey;
        await refreshLocalFlatSqlStats();
        return localFlatSqlStore;
      } catch (error) {
        sqlError = error instanceof Error ? error.message : 'FlatSQL initialization failed';
        return null;
      } finally {
        localFlatSqlStorePromise = null;
        localFlatSqlStorePromiseKey = '';
      }
    })();
    return localFlatSqlStorePromise;
  }

  function resetLocalFlatSqlStore(): void {
    localFlatSqlStore?.destroy();
    localFlatSqlStore = null;
    localFlatSqlStoreKey = '';
    localFlatSqlStorePromise = null;
    localFlatSqlStorePromiseKey = '';
    localFlatSqlStats = [];
    localFlatSqlStatsLoaded = false;
    sqlResult = null;
    sqlError = '';
  }

  function localFlatSqlPersistenceKey(dataSourceId: string, datastoreKey: string | null = null): string {
    return datastoreKey ? `sdn-data:${dataSourceId}:${datastoreKey}` : `sdn-data:${dataSourceId}`;
  }

  function localFlatSqlDesktopPersistenceBaseUrl(): string | null {
    if (backend?.mode !== 'desktop-local' || typeof window === 'undefined') return null;
    const { hostname, pathname, protocol, origin } = window.location;
    const isLoopbackHost = hostname === 'localhost' || hostname === '127.0.0.1' || hostname === '::1' || hostname === '[::1]';
    if (protocol !== 'http:' || !isLoopbackHost || !pathname.startsWith('/sdn')) return null;
    return origin;
  }

  function beginResetSubscriptionData(subscriptionId: string): void {
    resetSubscriptionId = subscriptionId;
    resetConfirmText = '';
    resetStatus = '';
  }

  function cancelResetSubscriptionData(): void {
    if (resetRunning) return;
    resetSubscriptionId = '';
    resetConfirmText = '';
    resetStatus = '';
  }

  async function confirmResetSubscriptionData(schema: SchemaSyncRow): Promise<void> {
    if (resetConfirmText.trim() !== 'RESET') {
      resetStatus = 'Type RESET to clear this row.';
      return;
    }
    resetRunning = true;
    resetStatus = '';
    const dataSourceId = schema.dataSourceId;
    const standardId = schema.id;
    try {
      resetLocalFlatSqlStore();
      await clearLocalFlatSqlStore({
        persistenceKey: localFlatSqlPersistenceKey(dataSourceId, schema.datastoreKey),
        standardIds: [standardId],
        desktopPersistenceBaseUrl: localFlatSqlDesktopPersistenceBaseUrl(),
      });
      clearSchemaSyncProgressForSubscription(dataSourceId, standardId, schema.datastoreKey);
      const nextActive = new Set(activeSyncKeys);
      nextActive.delete(schemaSyncPreferenceKey(dataSourceId, standardId, schema.datastoreKey));
      activeSyncKeys = nextActive;
      schemaSyncSchedulerForDataSource(dataSourceId).reset();
      rawRecords = [];
      dataScan = null;
      clearPnmSelection();
      resetSqlForSelectedStandard();
      selectedDataSourceId = dataSourceId;
      selectedStandardId = standardId;
      selectedSubscriptionId = schema.subscriptionId;
      selectedDatastoreKey = schema.datastoreKey;
      await ensureLocalFlatSqlStore(dataSourceId, schema.datastoreKey);
      await refreshLocalFlatSqlStats();
      resetStatus = `${standardId} row reset. Sync will restart from the first remote row.`;
      resetSubscriptionId = '';
      resetConfirmText = '';
      void synchronizeSchema(standardId, dataSourceId, schema.subscriptionId);
    } catch (error) {
      resetStatus = error instanceof Error ? error.message : 'Row reset failed';
    } finally {
      resetRunning = false;
    }
  }

  async function refreshLocalFlatSqlStats(includeCachedBytes = true): Promise<void> {
    if (!localFlatSqlStore) {
      localFlatSqlStats = [];
      localFlatSqlStatsLoaded = false;
      return;
    }
    localFlatSqlStats = await localFlatSqlStore.getStats({ includeCachedBytes });
    localFlatSqlStatsLoaded = true;
  }

  function resetSqlForSelectedStandard(): void {
    sqlQueryText = defaultSqlQuery(selectedStandardId);
    userEditedSql = false;
    sqlResult = null;
    sqlError = '';
  }

  function setSort(column: SortColumn): void {
    if (sortColumn === column) {
      sortDirection = sortDirection === 'asc' ? 'desc' : 'asc';
      return;
    }
    sortColumn = column;
    sortDirection = column === 'timestamp' ? 'desc' : 'asc';
  }

  function sortableHeader(column: SortColumn, label: string): string {
    if (sortColumn !== column) return label;
    return `${label} ${sortDirection.toUpperCase()}`;
  }

  function sortAria(column: SortColumn): 'ascending' | 'descending' | 'none' {
    if (sortColumn !== column) return 'none';
    return sortDirection === 'asc' ? 'ascending' : 'descending';
  }

  function filterRows(rows: WorkbenchRow[], query: string): WorkbenchRow[] {
    const normalized = query.trim().toLowerCase();
    if (!normalized) return rows;
    return rows.filter((record) => rowText(record).includes(normalized));
  }

  function filterRowsByColumns(rows: WorkbenchRow[], columns: WorkbenchColumn[], filters: Record<string, string>): WorkbenchRow[] {
    const activeFilters = activeColumnFilters(columns.map((column) => column.key), filters);
    if (activeFilters.length === 0) return rows;
    return rows.filter((row) => activeFilters.every(([column, query]) => tableValue(row, column).toLowerCase().includes(query)));
  }

  function sortRows(rows: WorkbenchRow[], column: SortColumn, direction: SortDirection): WorkbenchRow[] {
    const multiplier = direction === 'asc' ? 1 : -1;
    return [...rows].sort((left, right) => compareRows(left, right, column) * multiplier);
  }

  function compareRows(left: WorkbenchRow, right: WorkbenchRow, column: SortColumn): number {
    if (column === 'timestamp') return String(left.record.timestamp ?? '').localeCompare(String(right.record.timestamp ?? ''));
    return tableValue(left, column).localeCompare(tableValue(right, column));
  }

  function rowText(record: WorkbenchRow): string {
    return allColumns
      .map((column) => tableValue(record, column.key))
      .concat(record.record.schemaName, record.record.timestamp ?? '')
      .join(' ')
      .toLowerCase();
  }

  function tableValue(row: WorkbenchRow, column: SortColumn): string {
    if (column === 'schemaName') return standardIdFromSchema(row.record.schemaName);
    if (column in row.record) return String(row.record[column as keyof RawDataRecord] ?? '');
    return stringifyCellValue(row.decoded[column]);
  }

  function displayCellValue(row: WorkbenchRow, column: WorkbenchColumn): string {
    return shorten(tableValue(row, column.key), column.source === 'standard' ? 40 : 34);
  }

  function fullCellValue(row: WorkbenchRow, column: WorkbenchColumn): string {
    return tableValue(row, column.key);
  }

  function sqlCellValue(row: Record<string, unknown>, column: string): string {
    return stringifyCellValue(row[column]);
  }

  function displaySqlCellValue(row: Record<string, unknown>, column: string): string {
    return shorten(sqlCellValue(row, column), 40);
  }

  function filterSqlRecordsByColumns(records: Array<Record<string, unknown>>, columns: string[], filters: Record<string, string>): Array<Record<string, unknown>> {
    const activeFilters = activeColumnFilters(columns, filters);
    if (activeFilters.length === 0) return records;
    return records.filter((row) => activeFilters.every(([column, query]) => sqlCellValue(row, column).toLowerCase().includes(query)));
  }

  function sortSqlRecords(records: Array<Record<string, unknown>>, column: SortColumn, direction: SortDirection): Array<Record<string, unknown>> {
    if (!column) return records;
    const multiplier = direction === 'asc' ? 1 : -1;
    return [...records].sort((left, right) => sqlCellValue(left, column).localeCompare(sqlCellValue(right, column)) * multiplier);
  }

  function activeColumnFilters(columns: string[], filters: Record<string, string>): Array<[string, string]> {
    const validColumns = new Set(columns);
    return Object.entries(filters)
      .map(([column, value]) => [column, value.trim().toLowerCase()] as [string, string])
      .filter(([column, value]) => validColumns.has(column) && value.length > 0);
  }

  function columnFilterValue(column: string): string {
    return columnFilters[column] ?? '';
  }

  function columnFilterPlaceholder(column: string): string {
    return isNumericDataExplorerColumn(column) ? '>= 0, 1..10' : 'Filter';
  }

  function handleWorkbenchRowClick(row: WorkbenchRow): void {
    if (selectedStandardId === 'PNM' && !sqlResult) selectPnmRow(row);
  }

  function handleWorkbenchRowKeydown(row: WorkbenchRow, event: KeyboardEvent): void {
    if (event.key !== 'Enter' && event.key !== ' ') return;
    event.preventDefault();
    handleWorkbenchRowClick(row);
  }

  function handleLocalExplorerRowClick(row: Record<string, unknown>): void {
    if (selectedStandardId !== 'PNM' || sqlResult) return;
    selectPnmRow(localExplorerRecordToWorkbenchRow(row));
  }

  function handleLocalExplorerRowKeydown(row: Record<string, unknown>, event: KeyboardEvent): void {
    if (event.key !== 'Enter' && event.key !== ' ') return;
    event.preventDefault();
    handleLocalExplorerRowClick(row);
  }

  function selectPnmRow(row: WorkbenchRow): void {
    selectedPnmRow = row;
    pnmFileIdQuery = pnmValue(row.decoded, 'FILE_ID');
    pnmQueryResult = null;
    pnmQueryError = '';
    pnmSignatureStatus = '';
    void runPnmFileIdQuery();
  }

  function resetPnmSelectionIfNeeded(): void {
    if (selectedStandardId !== 'PNM') {
      clearPnmSelection();
      return;
    }
    if (!selectedPnmRow) return;
    const selectedCid = selectedPnmRow.record.cid;
    if (!rawRecords.some((record) => record.cid === selectedCid)) clearPnmSelection();
  }

  function clearPnmSelection(): void {
    selectedPnmRow = null;
    pnmFileIdQuery = '';
    pnmQueryResult = null;
    pnmQueryError = '';
    pnmSignatureStatus = '';
    pnmSignatureRunning = false;
  }

  async function runPnmFileIdQuery(): Promise<void> {
    const fileId = pnmFileIdQuery.trim();
    if (!fileId) {
      pnmQueryResult = null;
      pnmQueryError = 'FILE_ID is required';
      return;
    }
    pnmQueryError = '';
    try {
      const store = await ensureLocalFlatSqlStore(selectedDataSourceId, selectedSchemaSyncRow?.datastoreKey ?? selectedDatastoreKey);
      if (!store) return;
      pnmQueryResult = await store.query(`SELECT * FROM PNM WHERE FILE_ID = '${escapeSqlString(fileId)}' LIMIT 100`, 'PNM');
    } catch (error) {
      pnmQueryResult = null;
      pnmQueryError = error instanceof Error ? error.message : 'FILE_ID query failed';
    }
  }

  async function verifySelectedPnmSignature(): Promise<void> {
    if (!selectedPnmRow) return;
    pnmSignatureRunning = true;
    pnmSignatureStatus = '';
    try {
      pnmSignatureStatus = await verifyPnmSignature(selectedPnmRow.decoded, currentDataSourceOption()?.publicKey ?? null);
    } catch (error) {
      pnmSignatureStatus = error instanceof Error ? error.message : 'Signature verification failed';
    } finally {
      pnmSignatureRunning = false;
    }
  }

  function localExplorerRecordToWorkbenchRow(row: Record<string, unknown>): WorkbenchRow {
    const cid = stringifyCellValue(row.CID ?? row.cid ?? row.FILE_ID ?? row.fileId ?? 'local-pnm-row');
    return {
      record: {
        schemaName: schemaNameForStandardId(selectedStandardId),
        cid,
        peerId: stringifyCellValue(row.PEER ?? row.peerId ?? row.peer_id ?? ''),
        providerId: stringifyCellValue(row.PRODUCER ?? row.providerId ?? row.provider_id ?? ''),
        sourceName: stringifyCellValue(row.SOURCE ?? row.sourceName ?? row.source_name ?? ''),
        batchId: stringifyCellValue(row.BATCH ?? row.batchId ?? row.batch_id ?? ''),
        timestamp: stringifyCellValue(row.TIMESTAMP ?? row.timestamp ?? ''),
        sizeBytes: Number(row.sizeBytes ?? row.SIZE_BYTES ?? 0) || 0,
      },
      decoded: row,
    };
  }

  async function verifyPnmSignature(decoded: Record<string, unknown>, publicKeyText: string | null): Promise<string> {
    const cid = pnmValue(decoded, 'CID');
    const signature = pnmValue(decoded, 'SIGNATURE');
    const signatureType = pnmValue(decoded, 'SIGNATURE_TYPE').toLowerCase();
    if (!cid) return 'CID is unavailable; cannot reconstitute the signed payload.';
    if (!signature) return 'Signature not present on this PNM.';
    if (!signatureType.includes('ed25519')) {
      return signatureType
        ? `Signature type ${pnmValue(decoded, 'SIGNATURE_TYPE')} is not supported in this verifier.`
        : 'Signature type is unavailable.';
    }
    const publicKey = bytesFromEncodedString(publicKeyText ?? '');
    const signatureBytes = bytesFromEncodedString(signature);
    if (!publicKey || publicKey.byteLength !== 32) {
      return 'Cannot verify: provider public key is not a 32-byte Ed25519 key.';
    }
    if (!signatureBytes || signatureBytes.byteLength !== 64) {
      return 'Cannot verify: PNM signature is not a 64-byte Ed25519 signature.';
    }
    const valid = await verifyEd25519Signature(publicKey, new TextEncoder().encode(cid), signatureBytes);
    return valid ? 'Signature valid for reconstituted CID payload.' : 'Signature invalid for reconstituted CID payload.';
  }

  function pnmValue(decoded: Record<string, unknown>, key: string): string {
    return stringifyCellValue(decoded[key]);
  }

  function pnmSignaturePayload(decoded: Record<string, unknown>): string {
    return pnmValue(decoded, 'CID') || '';
  }

  function escapeSqlString(value: string): string {
    return value.replace(/'/g, "''");
  }

  function bytesFromEncodedString(value: string): Uint8Array | null {
    const trimmed = value.trim();
    if (!trimmed) return null;
    const hex = trimmed.startsWith('0x') ? trimmed.slice(2) : trimmed;
    if (/^[0-9a-fA-F]+$/.test(hex) && hex.length % 2 === 0) {
      const bytes = new Uint8Array(hex.length / 2);
      for (let index = 0; index < bytes.length; index += 1) {
        bytes[index] = Number.parseInt(hex.slice(index * 2, index * 2 + 2), 16);
      }
      return bytes;
    }
    try {
      const normalized = trimmed.replace(/-/g, '+').replace(/_/g, '/');
      const padded = normalized.padEnd(Math.ceil(normalized.length / 4) * 4, '=');
      const binary = atob(padded);
      return Uint8Array.from(binary, (char) => char.charCodeAt(0));
    } catch {
      return null;
    }
  }

  function visibleSqlColumns(columns: string[], records: Array<Record<string, unknown>>, standardId = selectedStandardId): string[] {
    const visible = columns.filter((column) => (
      !isInternalSqlColumn(column)
      && records.some((row) => hasDisplayValueForColumn(standardId, column, row[column]))
    ));
    if (visible.length > 0 || records.length > 0) return visible;
    return columns.filter((column) => !isInternalSqlColumn(column));
  }

  function hasDisplayValueForColumn(standardId: string, column: string, value: unknown): boolean {
    if (isOptionalDefaultValueColumn(standardId, column) && isDefaultEmptyCellValue(value)) return false;
    return hasDisplayValue(value);
  }

  function isOptionalDefaultValueColumn(standardId: string, column: string): boolean {
    return OPTIONAL_DEFAULT_VALUE_COLUMNS[standardIdFromSchema(standardId)]?.has(column.trim().toUpperCase()) ?? false;
  }

  function isDefaultEmptyCellValue(value: unknown): boolean {
    if (value == null) return true;
    if (typeof value === 'number') return value === 0;
    if (typeof value === 'boolean') return value === false;
    if (typeof value === 'string') {
      const normalized = value.trim().toLowerCase();
      return normalized === '' || normalized === '0' || normalized === 'false' || normalized === 'dry';
    }
    return false;
  }

  function sqlColumnHeader(column: string): string {
    return columnHeaderKeyLabel(column);
  }

  function columnHeaderKeyLabel(column: string, fallbackLabel?: string): string {
    const normalizedKey = normalizedColumnLookupKey(column);
    return SQL_COLUMN_LABELS[column]
      ?? SQL_COLUMN_LABELS[normalizedKey]
      ?? fallbackLabel
      ?? labelFromFieldKey(column);
  }

  function columnHeaderAbbreviation(column: string, fallbackLabel?: string): string {
    const normalizedKey = normalizedColumnLookupKey(column);
    return COLUMN_ABBREVIATIONS[column]
      ?? COLUMN_ABBREVIATIONS[normalizedKey]
      ?? fallbackColumnAbbreviation(columnHeaderKeyLabel(column, fallbackLabel), column);
  }

  function normalizedColumnLookupKey(column: string): string {
    return column.trim().toUpperCase();
  }

  function fallbackColumnAbbreviation(label: string, column: string): string {
    const labelWithoutUnits = label.replace(/\([^)]*\)/g, ' ');
    const tokens = labelWithoutUnits.split(/[^a-zA-Z0-9.]+/).filter(Boolean);
    if (tokens.length === 0) return normalizedColumnLookupKey(column).slice(0, 8);
    if (tokens.length === 1) return tokens[0].slice(0, 8).toUpperCase();
    return tokens.map((token) => token.slice(0, 1).toUpperCase()).join('').slice(0, 8);
  }

  function buildExplorerColumnKeyEntries(
    mode: ExplorerSearchMode,
    hasSqlResult: boolean,
    sqlColumnsForKey: string[],
    hasLocalResult: boolean,
    localColumnsForKey: string[],
    rawColumnsForKey: WorkbenchColumn[],
  ): ColumnKeyEntry[] {
    if (mode === 'sql' && hasSqlResult) return columnKeyEntriesFromSqlColumns(sqlColumnsForKey);
    if (hasLocalResult) return columnKeyEntriesFromSqlColumns(localColumnsForKey);
    return rawColumnsForKey.map((column) => columnKeyEntry(column.key, column.label));
  }

  function columnKeyEntriesFromSqlColumns(columns: string[]): ColumnKeyEntry[] {
    return columns.map((column) => columnKeyEntry(column));
  }

  function columnKeyEntry(column: string, fallbackLabel?: string): ColumnKeyEntry {
    return {
      key: column,
      abbreviation: columnHeaderAbbreviation(column, fallbackLabel),
      label: columnHeaderKeyLabel(column, fallbackLabel),
    };
  }

  function isInternalSqlColumn(column: string): boolean {
    return INTERNAL_SQL_COLUMN_KEYS.has(column) || column.startsWith('_');
  }

  function defaultSqlQuery(standardId: string): string {
    return `SELECT * FROM ${standardIdFromSchema(standardId)} LIMIT ${DEFAULT_PAGE_SIZE}`;
  }

  function localExplorerQueryColumns(standardId: string, columns: string[]): string[] {
    return localDataExplorerSearchColumns(standardId, columns).filter((column) => !isInternalSqlColumn(column));
  }

  function hasActiveColumnFilters(filters: Record<string, string>, columns: string[]): boolean {
    const activeColumns = new Set(columns.filter(Boolean));
    return Object.entries(filters).some(([column, value]) => {
      if (!value.trim()) return false;
      return activeColumns.size === 0 || activeColumns.has(column);
    });
  }

  function withUiTimeout<T>(promise: Promise<T> | T, timeoutMs: number, label: string): Promise<T> {
    const timedPromise = Promise.resolve(promise);
    return new Promise((resolve, reject) => {
      const timeout = setTimeout(() => reject(new Error(`${label} timed out`)), timeoutMs);
      timedPromise.then(
        (value) => {
          clearTimeout(timeout);
          resolve(value);
        },
        (error) => {
          clearTimeout(timeout);
          reject(error);
        },
      );
    });
  }

  function buildSubscribedSchemaSyncRows(
    subscriptions: DataFeedSubscription[],
    activeDataSourceId: string,
    activeDatastoreKey: string | null,
    stats: LocalFlatSqlStandardStats[],
    localStatsLoaded: boolean,
    preferences: Record<string, SchemaSyncPreference>,
    summary: DataSummary | null,
    scan: DataScanResult | null,
    pageLoading: boolean,
    activeStandardId: string,
  ): SchemaSyncRow[] {
    return subscriptions.map((subscription) => {
      const datastoreKey = subscription.datastoreKey ?? null;
      const sourceStatsSelected = subscription.dataSourceId === activeDataSourceId && datastoreKey === activeDatastoreKey;
      const sourceStatsAreAuthoritative = sourceStatsSelected && localStatsLoaded;
      const sourceStats = sourceStatsAreAuthoritative ? stats : [];
      const progress = schemaSyncProgressFor(
        subscription.dataSourceId,
        subscription.standardId,
        subscription.remoteRows,
        sourceStats,
        sourceStatsAreAuthoritative,
        datastoreKey,
      );
      const remoteRows = remoteRowsForSchemaSyncRow(subscription, progress);
      return {
        id: subscription.standardId,
        subscriptionId: subscription.id,
        dataSourceId: subscription.dataSourceId,
        datastoreKey,
        providerName: subscription.providerName,
        providerId: subscription.providerId,
        providerPeerId: subscription.peerId,
        providerPublicKey: subscription.providerPublicKey,
        sourceName: subscription.sourceName,
        syncFilter: subscription.syncFilter,
        queryProfile: normalizeDataQueryProfile(subscription.queryProfile),
        retentionPolicy: subscriptionRetentionPolicyFor(subscription, subscription.standardId),
        remoteRows,
        localRows: progress.localRows,
        cachedBytes: progress.cachedBytes,
        remoteRowsLoading: remoteRowsAreLoading(
          pageLoading,
          remoteRows,
          progress,
          summary,
          scan,
          subscription.standardId,
          datastoreKey,
          activeStandardId,
        ),
        preference: subscriptionSchemaSyncPreference(subscription, preferences),
        progress,
      };
    }).sort((left, right) => {
      const delta = right.remoteRows - left.remoteRows;
      return delta === 0 ? left.id.localeCompare(right.id) : delta;
    });
  }

  function remoteRowsForSchemaSyncRow(subscription: DataFeedSubscription, progress: SchemaSyncProgress): number {
    if (
      normalizeDataQueryProfile(subscription.queryProfile) === 'dataset-publication-offset-v1'
      && progress.queryProfile === 'dataset-publication-offset-v1'
      && progress.totalRows > 0
    ) {
      return progress.totalRows;
    }
    return Math.max(subscription.remoteRows, progress.totalRows);
  }

  function buildSubscribedSourceOptions(rows: SchemaSyncRow[]): ExplorerSourceOption[] {
    const options: ExplorerSourceOption[] = [];
    const seen = new Set<string>();
    for (const row of rows) {
      const key = subscriptionSourceKey(row.dataSourceId, row.datastoreKey);
      if (seen.has(key)) continue;
      seen.add(key);
      options.push({
        id: key,
        dataSourceId: row.dataSourceId,
        datastoreKey: row.datastoreKey,
        label: row.providerName,
        detail: row.datastoreKey ?? row.providerPeerId ?? row.providerPublicKey ?? row.dataSourceId,
      });
    }
    return options.sort((left, right) => left.label.localeCompare(right.label) || left.detail.localeCompare(right.detail));
  }

  function buildSourceProvenanceRows(
    sources: DataSourceOption[],
    rows: SchemaSyncRow[],
    peerTrust: Record<string, string | undefined>,
  ): SourceProvenanceRow[] {
    const sourceById = new Map(sources.map((source) => [source.id, source]));
    const rowMap = new Map<string, {
      id: string;
      providerName: string;
      detail: string;
      peerId: string | null;
      publicKey: string | null;
      kind: DataSourceOption['kind'];
      sourceDatastores: Set<string>;
      products: Set<string>;
      remoteRows: number;
      localRows: number;
      subscribed: boolean;
    }>();

    const ensureRow = (source: DataSourceOption | null, schema: SchemaSyncRow | null) => {
      const id = source?.id ?? schema?.dataSourceId ?? '';
      if (!id) return null;
      const existing = rowMap.get(id);
      if (existing) return existing;
      const next = {
        id,
        providerName: source?.label ?? schema?.providerName ?? schema?.providerPeerId ?? id,
        detail: source?.detail ?? schema?.providerId ?? schema?.datastoreKey ?? '',
        peerId: source?.peerId ?? schema?.providerPeerId ?? null,
        publicKey: source?.publicKey ?? schema?.providerPublicKey ?? null,
        kind: source?.kind ?? (id === LOCAL_DATA_SOURCE_ID ? 'local' : 'configured'),
        sourceDatastores: new Set<string>(),
        products: new Set<string>(),
        remoteRows: 0,
        localRows: 0,
        subscribed: false,
      };
      if (source?.sourceName) next.sourceDatastores.add(source.sourceName);
      if (schema?.sourceName) next.sourceDatastores.add(schema.sourceName);
      if (schema?.datastoreKey) next.sourceDatastores.add(schema.datastoreKey);
      rowMap.set(id, next);
      return next;
    };

    for (const schema of rows) {
      const row = ensureRow(sourceById.get(schema.dataSourceId) ?? null, schema);
      if (!row) continue;
      if (schema.providerName && (row.kind === 'local' || row.providerName === row.id)) {
        row.providerName = schema.providerName;
      }
      row.peerId = row.peerId ?? schema.providerPeerId;
      row.publicKey = row.publicKey ?? schema.providerPublicKey;
      row.products.add(schema.id);
      row.remoteRows += Math.max(schema.remoteRows, 0);
      row.localRows += Math.max(schema.localRows, 0);
      row.subscribed = true;
      if (schema.sourceName) row.sourceDatastores.add(schema.sourceName);
      if (schema.datastoreKey) row.sourceDatastores.add(schema.datastoreKey);
    }

    for (const source of sources) {
      const hasSubscribedRows = rows.some((schema) => schema.dataSourceId === source.id);
      if (source.kind !== 'configured' && !hasSubscribedRows) continue;
      ensureRow(source, null);
    }

    return [...rowMap.values()]
      .map((row): SourceProvenanceRow => {
        const trustKey = [row.peerId, row.publicKey].filter(Boolean).find((key) => peerTrust[key as string]);
        const trustLabel = row.kind === 'local'
          ? 'Local node'
          : `PGP: ${ownertrustLabel(trustKey ? peerTrust[trustKey] : null)}`;
        const accessLabel = row.subscribed ? 'Subscribed source' : 'Configured source';
        const products = [...row.products].sort();
        const productCount = products.length;
        return {
          id: row.id,
          providerName: row.providerName,
          detail: row.detail,
          peerId: row.peerId,
          publicKey: row.publicKey,
          sourceDatastoreLabel: sourceDatastoreLabel(row.sourceDatastores, row.detail),
          trustAccessLabel: `${trustLabel} · ${accessLabel}`,
          productsLabel: productCount > 0 ? products.join(', ') : 'No subscribed products',
          rowsLabel: productCount > 0
            ? `${formatNumber(productCount)} ${productCount === 1 ? 'product' : 'products'} · ${formatNumber(row.remoteRows)} remote · ${formatNumber(row.localRows)} local`
            : 'No synced rows',
        };
      })
      .sort((left, right) => left.providerName.localeCompare(right.providerName) || left.id.localeCompare(right.id));
  }

  function sourceDatastoreLabel(sourceDatastores: Set<string>, fallback: string): string {
    const labels = [...sourceDatastores].filter(Boolean).sort();
    if (labels.length > 0) return labels.join(', ');
    return fallback || 'Provider namespace pending';
  }

  function buildDataActivityRows(rows: SchemaSyncRow[], catalogRows: DataCatalogRow[]): DataActivityRow[] {
    const activities: DataActivityRow[] = [];
    for (const schema of rows) {
      const catalogRow = catalogRowForActivitySchema(schema, catalogRows);
      const lastSyncedMs = activitySortTime(schema.progress.lastSyncedAt);
      const retryable = schema.progress.status === 'error' || schemaSyncStalled(schema);
      const base = {
        standardId: schema.id,
        providerName: schema.providerName,
        nextAttemptLabel: nextSyncAttemptLabel(schema),
        occurredAtLabel: schemaLastSyncedLabel(schema),
        occurredAtMs: lastSyncedMs,
      };

      activities.push({
        id: `sync:${schema.subscriptionId}`,
        eventType: 'Sync',
        ...base,
        statusLabel: syncStatusLabel(schema),
        detailLabel: `${schemaProgressLabel(schema)} · ${schemaDownloadSpeedLabel(schema)}`,
        retrySchema: retryable ? schema : null,
      });

      activities.push({
        id: `subscription:${schema.subscriptionId}`,
        eventType: 'Subscription',
        ...base,
        statusLabel: schema.preference.mode === 'sync' ? 'Subscribed' : 'Preview',
        detailLabel: `${subscriptionSyncPolicyLabel(schema)} · ${schema.preference.storageCap} ${schema.preference.storageUnit} cap`,
        retrySchema: null,
      });

      if (schema.progress.error) {
        activities.push({
          id: `sync-error:${schema.subscriptionId}`,
          eventType: 'Sync error',
          ...base,
          statusLabel: 'Failed',
          detailLabel: schema.progress.error,
          retrySchema: schema,
        });
      }

      if (schemaSyncStalled(schema)) {
        activities.push({
          id: `verification:${schema.subscriptionId}`,
          eventType: 'Verification',
          ...base,
          statusLabel: 'Stalled',
          detailLabel: schemaSyncStalledLabel(schema),
          retrySchema: schema,
        });
      }

      if (retryable) {
        activities.push({
          id: `retry:${schema.subscriptionId}`,
          eventType: 'Retry',
          ...base,
          statusLabel: 'Retry available',
          detailLabel: 'Retry from the current local cursor and pinned shard state.',
          retrySchema: schema,
        });
      }

      if (catalogRow && catalogAccessNeedsAttention(catalogRow)) {
        const accessEventType: DataActivityRow['eventType'] = catalogRow.access.state === 'payment-failed' || catalogRow.access.state === 'over-quota' ? 'Billing' : 'Access';
        activities.push({
          id: `access:${schema.subscriptionId}`,
          eventType: accessEventType,
          ...base,
          statusLabel: catalogRow.access.label,
          detailLabel: catalogRowRestrictionLabel(catalogRow),
          nextAttemptLabel: catalogRow.plan.renewalLabel,
          retrySchema: null,
        });
      }
    }

    return activities.sort((left, right) => (
      right.occurredAtMs - left.occurredAtMs
      || activityEventRank(left.eventType) - activityEventRank(right.eventType)
      || left.standardId.localeCompare(right.standardId)
      || left.providerName.localeCompare(right.providerName)
    ));
  }

  function catalogRowForActivitySchema(schema: SchemaSyncRow, catalogRows: DataCatalogRow[]): DataCatalogRow | null {
    return catalogRows.find((row) => row.subscriptionId === schema.subscriptionId)
      ?? catalogRows.find((row) => (
        row.messageTypes.includes(schema.id)
        && row.provider === schema.providerName
        && row.providerPeerId === schema.providerPeerId
        && row.datastoreKey === schema.datastoreKey
      ))
      ?? null;
  }

  function catalogAccessNeedsAttention(row: DataCatalogRow): boolean {
    return row.access.state === 'expired'
      || row.access.state === 'over-quota'
      || row.access.state === 'payment-failed';
  }

  function activitySortTime(value: string | null | undefined): number {
    if (!value) return 0;
    const time = new Date(value).getTime();
    return Number.isFinite(time) ? time : 0;
  }

  function activityEventRank(eventType: DataActivityRow['eventType']): number {
    switch (eventType) {
      case 'Sync error': return 0;
      case 'Verification': return 1;
      case 'Retry': return 2;
      case 'Billing': return 3;
      case 'Access': return 4;
      case 'Subscription': return 5;
      case 'Sync':
      default:
        return 6;
    }
  }

  function subscriptionSourceKey(dataSourceId: string, datastoreKey: string | null = null): string {
    return datastoreKey ? `${dataSourceId}:datastore:${datastoreKey}` : dataSourceId;
  }

  function localRowsForStandard(stats: LocalFlatSqlStandardStats[], standardId: string): number {
    const stat = stats.find((entry) => entry.standardId === standardId);
    return Math.max(stat?.recordCount ?? 0, 0);
  }

  function cachedBytesForStandard(stats: LocalFlatSqlStandardStats[], standardId: string): number {
    return stats.find((entry) => entry.standardId === standardId)?.cachedBytes ?? 0;
  }

  function localStatsForStandard(stats: LocalFlatSqlStandardStats[], standardId: string): LocalFlatSqlStandardStats | null {
    return stats.find((entry) => entry.standardId === standardId) ?? null;
  }

  function schemaSyncPreferenceFor(dataSourceId: string, standardId: string, datastoreKey: string | null = null): SchemaSyncPreference {
    return schemaSyncPreferences[schemaSyncPreferenceKey(dataSourceId, standardId, datastoreKey)]
      ?? schemaSyncPreferences[schemaSyncPreferenceKey(dataSourceId, standardId)]
      ?? DEFAULT_SCHEMA_SYNC_PREFERENCE;
  }

  function subscriptionSchemaSyncPreference(
    subscription: DataFeedSubscription,
    preferences = schemaSyncPreferences,
  ): SchemaSyncPreference {
    const persisted = preferences[schemaSyncPreferenceKey(subscription.dataSourceId, subscription.standardId, subscription.datastoreKey)]
      ?? preferences[schemaSyncPreferenceKey(subscription.dataSourceId, subscription.standardId)];
    return normalizeSchemaSyncPreference({
      mode: persisted?.mode ?? 'sync',
      storageCap: persisted?.storageCap ?? subscription.storageCap,
      storageUnit: persisted?.storageUnit ?? subscription.storageUnit,
    }) ?? {
      mode: 'sync',
      storageCap: subscription.storageCap,
      storageUnit: subscription.storageUnit,
    };
  }

  function remoteRowsForSubscription(dataSourceId: string, standardId: string, datastoreKey: string | null = null): number | null {
    return dataDirectoryState.subscriptions.find((subscription) => (
      subscription.dataSourceId === dataSourceId
      && subscription.standardId === standardId
      && (datastoreKey === null || subscription.datastoreKey === datastoreKey)
    ))?.remoteRows ?? null;
  }

  function syncFilterForSubscription(dataSourceId: string, standardId: string, datastoreKey: string | null = null): string {
    return dataDirectoryState.subscriptions.find((subscription) => (
      subscription.dataSourceId === dataSourceId
      && subscription.standardId === standardId
      && (datastoreKey === null || subscription.datastoreKey === datastoreKey)
    ))?.syncFilter ?? '';
  }

  function subscriptionQueryProfileFor(subscription: Pick<DataFeedSubscription, 'queryProfile'> | Pick<SchemaSyncRow, 'queryProfile'> | null | undefined): DataQueryProfile {
    return normalizeDataQueryProfile(subscription?.queryProfile);
  }

  function subscriptionRetentionPolicyFor(
    subscription: Pick<DataFeedSubscription, 'retentionPolicy' | 'standardId'> | Pick<SchemaSyncRow, 'retentionPolicy' | 'id'> | null | undefined,
    standardId = '',
  ): DataFeedRetentionPolicy {
    return normalizeDataFeedRetentionPolicy(
      subscription?.retentionPolicy,
      'standardId' in (subscription ?? {}) ? (subscription as Pick<DataFeedSubscription, 'standardId'>).standardId : standardId,
    );
  }

  function syncFilterChangedRequiresReset(progress: SchemaSyncProgress, nextSyncFilter: string): boolean {
    const previous = progress.syncFilter?.trim() ?? '';
    const next = nextSyncFilter.trim();
    if (previous === next) return false;
    return progress.localRows > 0
      || progress.syncedRows > 0
      || progress.cachedBytes > 0
      || progress.pinnedRows > 0
      || Boolean(progress.lastSyncedAt);
  }

  function retentionPolicyRequiresReset(progress: SchemaSyncProgress, retentionPolicy: DataFeedRetentionPolicy): boolean {
    if (retentionPolicy !== 'replace-snapshot') return false;
    return progress.localRows > 0
      || progress.syncedRows > 0
      || progress.cachedBytes > 0
      || progress.pinnedRows > 0
      || Boolean(progress.lastSyncedAt);
  }

  function subscriptionForSync(dataSourceId: string, standardId: string, subscriptionId = ''): DataFeedSubscription | null {
    return dataDirectoryState.subscriptions.find((subscription) => subscriptionId && subscription.id === subscriptionId)
      ?? dataDirectoryState.subscriptions.find((subscription) => (
        subscription.dataSourceId === dataSourceId
        && subscription.standardId === standardId
        && (selectedDatastoreKey === null || subscription.datastoreKey === selectedDatastoreKey)
      ))
      ?? dataDirectoryState.subscriptions.find((subscription) => (
        subscription.dataSourceId === dataSourceId && subscription.standardId === standardId
      ))
      ?? null;
  }

  function schemaSyncProgressFor(
    dataSourceId: string,
    standardId: string,
    remoteRows: number,
    stats: LocalFlatSqlStandardStats[],
    statsAreAuthoritative = true,
    datastoreKey: string | null = null,
  ): SchemaSyncProgress {
    const key = schemaSyncPreferenceKey(dataSourceId, standardId, datastoreKey);
    const localStats = localStatsForStandard(stats, standardId);
    const localRows = localRowsForStandard(stats, standardId);
    const cachedBytes = cachedBytesForStandard(stats, standardId);
    const persisted = schemaSyncProgress[key];
    const activePersisted = statsAreAuthoritative && localRows === 0 && (persisted?.localRows ?? 0) > 0 ? null : persisted;
    const persistedTotalRows = activePersisted?.totalRows ?? 0;
    const publishedSnapshotTotalRows = activePersisted?.queryProfile === 'dataset-publication-offset-v1' && persistedTotalRows > 0
      ? persistedTotalRows
      : null;
    const totalRows = publishedSnapshotTotalRows ?? Math.max(remoteRows, persistedTotalRows);
    const rowCountRemoteRows = publishedSnapshotTotalRows ?? remoteRows;
    const complete = totalRows > 0 && localRows >= totalRows;
    const active = activeSyncKeys.has(key);
    const status = effectiveSchemaSyncStatus({
      active,
      complete,
      persistedStatus: activePersisted?.status,
    });
    const rowCounts = syncRowCountSummary({
      localRows,
      syncedRows: Math.max(localRows, activePersisted?.syncedRows ?? 0),
      pinnedRows: Math.max(localStats?.pinnedRows ?? 0, activePersisted?.pinnedRows ?? 0),
      remoteRows: rowCountRemoteRows,
      totalRows,
    });
    return {
      status,
      syncedRows: rowCounts.syncedRows,
      totalRows: rowCounts.totalRows,
      localRows,
      pinnedRows: rowCounts.pinnedRows,
      missingRows: rowCounts.missingRows,
      cachedBytes,
      pinnedBytes: Math.max(localStats?.pinnedBytes ?? 0, activePersisted?.pinnedBytes ?? 0),
      downloadedBytes: activePersisted?.downloadedBytes ?? 0,
      downloadSpeedBytesPerSecond: active ? activePersisted?.downloadSpeedBytesPerSecond ?? 0 : 0,
      measuredWireSpeedBytesPerSecond: activePersisted?.measuredWireSpeedBytesPerSecond ?? measuredWireSpeedBytesPerSecondForSource(dataSourceId),
      wireSpeedUtilization: active ? activePersisted?.wireSpeedUtilization ?? null : null,
      wireSpeedTarget: activePersisted?.wireSpeedTarget ?? 0.8,
      wireSpeedTargetMet: activePersisted?.wireSpeedTargetMet ?? null,
      manifestDiscoveryMs: activePersisted?.manifestDiscoveryMs ?? 0,
      networkTransferMs: activePersisted?.networkTransferMs ?? 0,
      verificationMs: activePersisted?.verificationMs ?? 0,
      flatSqlMaterializationMs: activePersisted?.flatSqlMaterializationMs ?? 0,
      providerPeerId: activePersisted?.providerPeerId ?? null,
      providerPublicKey: activePersisted?.providerPublicKey ?? null,
      snapshotId: localStats?.snapshotId ?? activePersisted?.snapshotId ?? null,
      head: localStats?.head ?? activePersisted?.head ?? null,
      cursor: activePersisted?.cursor ?? null,
      nextCursor: activePersisted?.nextCursor ?? null,
      highWaterMark: localStats?.highWaterMark ?? activePersisted?.highWaterMark ?? null,
      queryProfile: activePersisted?.queryProfile ?? null,
      chunkHash: activePersisted?.chunkHash ?? null,
      syncProtocol: activePersisted?.syncProtocol ?? null,
      syncFilter: activePersisted?.syncFilter ?? null,
      verifiedChunks: activePersisted?.verifiedChunks ?? [],
      lastSyncedAt: localStats?.lastSyncedAt ?? activePersisted?.lastSyncedAt ?? null,
      progressFingerprint: activePersisted?.progressFingerprint ?? null,
      lastAdvancedAt: activePersisted?.lastAdvancedAt ?? null,
      lastProgressObservedAt: activePersisted?.lastProgressObservedAt ?? null,
      stallObservationCount: activePersisted?.stallObservationCount ?? 0,
      stalledSince: activePersisted?.stalledSince ?? null,
      error: activePersisted?.error ?? null,
    };
  }

  function updateSchemaSyncPreference(standardId: string, patch: Partial<SchemaSyncPreference>, dataSourceId = selectedDataSourceId, datastoreKey: string | null = selectedDatastoreKey): void {
    const key = schemaSyncPreferenceKey(dataSourceId, standardId, datastoreKey);
    const current = schemaSyncPreferenceFor(dataSourceId, standardId, datastoreKey);
    schemaSyncPreferences = {
      ...schemaSyncPreferences,
      [key]: normalizeSchemaSyncPreference({ ...current, ...patch }) ?? DEFAULT_SCHEMA_SYNC_PREFERENCE,
    };
    persistSchemaSyncPreferences(schemaSyncPreferences);
  }

  function schemaSyncPreferenceKey(dataSourceId: string, standardId: string, datastoreKey: string | null = null): string {
    const base = `${dataSourceId}:${standardId.trim().toUpperCase() || DEFAULT_STANDARD_ID}`;
    return datastoreKey ? `${base}:${datastoreKey}` : base;
  }

  function shouldUseCachedDataPageView(cache: CachedDataPageView | null, liveRows: SchemaSyncRow[], loading: boolean): boolean {
    return Boolean(cache && loading && liveRows.length === 0 && cache.schemaSyncRows.length > 0);
  }

  function rememberDataPageViewCache(
    cacheActive: boolean,
    schemaRows: SchemaSyncRow[],
    messageRows: SchemaSyncRow[],
    provenanceRows: SourceProvenanceRow[],
    catalogRows: DataCatalogRow[],
    catalogSummary: DataCatalogSummary,
    overviewVisuals: DataOverviewVisuals,
    billedRows: DataCatalogRow[],
    providerRows: DataBillingProviderRow[],
    timelineRows: DataActivityRow[],
  ): void {
    if (cacheActive) return;
    if (schemaRows.length === 0 && catalogRows.length === 0 && provenanceRows.length === 0) return;
    const snapshot: CachedDataPageView = {
      schemaSyncRows: schemaRows,
      messageTypeRows: messageRows,
      sourceProvenanceRows: provenanceRows,
      dataCatalogRows: catalogRows,
      dataCatalogSummary: catalogSummary,
      dataOverviewVisuals: cacheableOverviewVisuals(overviewVisuals),
      billingDataRows: billedRows,
      billingProviderRows: providerRows,
      activityRows: timelineRows,
      cachedAt: Date.now(),
    };
    const signature = dataPageViewSignature(snapshot);
    if (signature === cachedDataPageViewSignature) return;
    cachedDataPageViewSignature = signature;
    cachedDataPageView = snapshot;
    persistCachedDataPageView(snapshot);
  }

  function cacheableOverviewVisuals(visuals: DataOverviewVisuals): DataOverviewVisuals {
    const storageSegmentsByGroup = normalizedStorageSegmentsByGroup(visuals.storageSegmentsByGroup, visuals.storageSegments);
    return {
      ...visuals,
      storageSegmentsByGroup,
      storageSegments: storageSegmentsByGroup.provider,
    };
  }

  function cachedOverviewVisualsForGroup(visuals: DataOverviewVisuals, group: DataOverviewStorageGroup): DataOverviewVisuals {
    const storageSegmentsByGroup = normalizedStorageSegmentsByGroup(visuals.storageSegmentsByGroup, visuals.storageSegments);
    return {
      ...visuals,
      storageSegmentsByGroup,
      storageSegments: storageSegmentsByGroup[group] ?? storageSegmentsByGroup.provider,
    };
  }

  function normalizedStorageSegmentsByGroup(
    value: Partial<Record<DataOverviewStorageGroup, DataOverviewStorageSegment[]>> | undefined,
    fallback: DataOverviewStorageSegment[] = [],
  ): Record<DataOverviewStorageGroup, DataOverviewStorageSegment[]> {
    return {
      provider: Array.isArray(value?.provider) ? value.provider : fallback,
      messageType: Array.isArray(value?.messageType) ? value.messageType : [],
      access: Array.isArray(value?.access) ? value.access : [],
    };
  }

  function loadCachedDataPageView(): CachedDataPageView | null {
    if (typeof window === 'undefined') return null;
    try {
      return normalizeCachedDataPageView(JSON.parse(window.localStorage.getItem(DATA_PAGE_VIEW_CACHE_STORAGE_KEY) ?? 'null') as unknown);
    } catch {
      return null;
    }
  }

  function normalizeCachedDataPageView(value: unknown): CachedDataPageView | null {
    if (!isRecord(value)) return null;
    const schemaSyncRows = cachedArray<SchemaSyncRow>(value.schemaSyncRows);
    const dataCatalogRows = cachedArray<DataCatalogRow>(value.dataCatalogRows);
    const sourceProvenanceRows = cachedArray<SourceProvenanceRow>(value.sourceProvenanceRows);
    if (schemaSyncRows.length === 0 && dataCatalogRows.length === 0 && sourceProvenanceRows.length === 0) return null;
    const dataCatalogSummary = isRecord(value.dataCatalogSummary)
      ? value.dataCatalogSummary as unknown as DataCatalogSummary
      : summarizeDataCatalog(dataCatalogRows);
    const dataOverviewVisuals = isRecord(value.dataOverviewVisuals)
      ? cachedOverviewVisualsForGroup(value.dataOverviewVisuals as unknown as DataOverviewVisuals, 'provider')
      : buildDataOverviewVisuals(dataCatalogRows);
    return {
      schemaSyncRows,
      messageTypeRows: cachedArray<SchemaSyncRow>(value.messageTypeRows),
      sourceProvenanceRows,
      dataCatalogRows,
      dataCatalogSummary,
      dataOverviewVisuals,
      billingDataRows: cachedArray<DataCatalogRow>(value.billingDataRows),
      billingProviderRows: cachedArray<DataBillingProviderRow>(value.billingProviderRows),
      activityRows: cachedArray<DataActivityRow>(value.activityRows),
      cachedAt: typeof value.cachedAt === 'number' ? value.cachedAt : 0,
    };
  }

  function cachedArray<T>(value: unknown): T[] {
    return Array.isArray(value) ? value as T[] : [];
  }

  function persistCachedDataPageView(view: CachedDataPageView): void {
    if (typeof window === 'undefined') return;
    try {
      window.localStorage.setItem(DATA_PAGE_VIEW_CACHE_STORAGE_KEY, JSON.stringify(view));
    } catch {
      // The cache is only for faster tab activation; storage failures must not block the page.
    }
  }

  function dataPageViewSignature(view: CachedDataPageView): string {
    try {
      return JSON.stringify([
        view.schemaSyncRows,
        view.sourceProvenanceRows,
        view.dataCatalogRows,
        view.dataCatalogSummary,
        view.dataOverviewVisuals,
        view.billingDataRows,
        view.billingProviderRows,
        view.activityRows,
      ]) ?? '';
    } catch {
      return String(view.cachedAt);
    }
  }

  function loadSchemaSyncPreferences(): Record<string, SchemaSyncPreference> {
    if (typeof window === 'undefined') return {};
    try {
      const parsed = JSON.parse(window.localStorage.getItem(SCHEMA_SYNC_STORAGE_KEY) ?? '{}') as unknown;
      if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return {};
      const preferences: Record<string, SchemaSyncPreference> = {};
      for (const [key, value] of Object.entries(parsed as Record<string, unknown>)) {
        const preference = normalizeSchemaSyncPreference(value);
        if (preference) preferences[key] = preference;
      }
      return preferences;
    } catch {
      return {};
    }
  }

  function persistSchemaSyncPreferences(preferences: Record<string, SchemaSyncPreference>): void {
    if (typeof window === 'undefined') return;
    try {
      window.localStorage.setItem(SCHEMA_SYNC_STORAGE_KEY, JSON.stringify(preferences));
    } catch {
      // Storage quota or privacy settings should not block the data explorer.
    }
  }

  function loadSavedExplorerViews(): SavedExplorerView[] {
    if (typeof window === 'undefined') return [];
    try {
      const parsed = JSON.parse(window.localStorage.getItem(EXPLORER_SAVED_VIEWS_STORAGE_KEY) ?? '[]') as unknown;
      if (!Array.isArray(parsed)) return [];
      return parsed
        .map(normalizeSavedExplorerView)
        .filter((view): view is SavedExplorerView => view !== null)
        .slice(0, 64);
    } catch {
      return [];
    }
  }

  function persistSavedExplorerViews(views: SavedExplorerView[]): void {
    if (typeof window === 'undefined') return;
    try {
      window.localStorage.setItem(EXPLORER_SAVED_VIEWS_STORAGE_KEY, JSON.stringify(views));
    } catch {
      // Saved views are convenience state and must not block query execution.
    }
  }

  function normalizeSavedExplorerView(value: unknown): SavedExplorerView | null {
    if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
    const candidate = value as Record<string, unknown>;
    const id = normalizedOptionalString(candidate.id) ?? savedExplorerViewId();
    const standardId = standardIdFromSchema(normalizedOptionalString(candidate.standardId) ?? DEFAULT_STANDARD_ID);
    const dataSourceId = normalizedOptionalString(candidate.dataSourceId) ?? LOCAL_DATA_SOURCE_ID;
    const createdAt = normalizedOptionalTimestamp(candidate.createdAt) ?? new Date().toISOString();
    return {
      id,
      name: normalizedOptionalString(candidate.name) ?? `${standardId} view`,
      dataSourceId,
      datastoreKey: normalizedOptionalString(candidate.datastoreKey),
      subscriptionId: normalizedOptionalString(candidate.subscriptionId) ?? '',
      standardId,
      searchMode: candidate.searchMode === 'sql' ? 'sql' : 'plain',
      searchText: normalizedString(candidate.searchText),
      sqlText: normalizedString(candidate.sqlText) || defaultSqlQuery(standardId),
      columnFilters: normalizeStringRecord(candidate.columnFilters),
      visibleColumnKeys: normalizedStringArray(candidate.visibleColumnKeys),
      pageSize: normalizedPageSizeValue(candidate.pageSize),
      sortColumn: normalizedOptionalString(candidate.sortColumn) ?? 'timestamp',
      sortDirection: candidate.sortDirection === 'asc' ? 'asc' : 'desc',
      createdAt,
      updatedAt: normalizedOptionalTimestamp(candidate.updatedAt) ?? createdAt,
    };
  }

  function normalizeStringRecord(value: unknown): Record<string, string> {
    if (!value || typeof value !== 'object' || Array.isArray(value)) return {};
    const entries = Object.entries(value as Record<string, unknown>)
      .map(([key, entry]) => [key.trim(), normalizedString(entry).trim()] as const)
      .filter(([key, entry]) => key.length > 0 && entry.length > 0);
    return Object.fromEntries(entries);
  }

  function loadSchemaSyncProgress(): Record<string, SchemaSyncProgress> {
    if (typeof window === 'undefined') return {};
    try {
      const parsed = JSON.parse(window.localStorage.getItem(SCHEMA_SYNC_STATE_STORAGE_KEY) ?? '{}') as unknown;
      if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return {};
      const progress: Record<string, SchemaSyncProgress> = {};
      for (const [key, value] of Object.entries(parsed as Record<string, unknown>)) {
        const normalized = normalizeSchemaSyncProgress(value);
        if (normalized) progress[key] = normalized;
      }
      return progress;
    } catch {
      return {};
    }
  }

  function persistSchemaSyncProgress(progress: Record<string, SchemaSyncProgress>): void {
    if (typeof window === 'undefined') return;
    try {
      window.localStorage.setItem(SCHEMA_SYNC_STATE_STORAGE_KEY, JSON.stringify(progress));
    } catch {
      // Storage quota or privacy settings should not block progress reporting.
    }
  }

  function persistMeasuredWireSpeedBytesPerSecond(dataSourceId: string, value: number): void {
    if (typeof window === 'undefined') return;
    const numeric = Number(value);
    if (!Number.isFinite(numeric) || numeric <= 0) return;
    try {
      window.localStorage.setItem(`sdn-data-wire-speed-bytes-per-second:${dataSourceId}`, String(Math.floor(numeric)));
    } catch {
      // Storage quota or privacy settings should not block sync.
    }
  }

  function clearSchemaSyncProgressForSubscription(dataSourceId: string, standardId: string, datastoreKey: string | null = null): void {
    const key = schemaSyncPreferenceKey(dataSourceId, standardId, datastoreKey);
    schemaSyncProgress = Object.fromEntries(
      Object.entries(schemaSyncProgress).filter(([candidate]) => candidate !== key),
    );
    persistSchemaSyncProgress(schemaSyncProgress);
  }

  function normalizeSchemaSyncPreference(value: unknown): SchemaSyncPreference | null {
    if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
    const candidate = value as Record<string, unknown>;
    return {
      mode: candidate.mode === 'sync' ? 'sync' : 'preview',
      storageCap: normalizedStorageCap(candidate.storageCap),
      storageUnit: isStorageUnit(candidate.storageUnit) ? candidate.storageUnit : DEFAULT_SCHEMA_SYNC_PREFERENCE.storageUnit,
    };
  }

  function normalizedStorageCap(value: unknown): number {
    const numeric = Number(value);
    if (!Number.isFinite(numeric)) return DEFAULT_SCHEMA_SYNC_PREFERENCE.storageCap;
    return Math.max(0.1, Math.min(1_000_000, Math.round(numeric * 10) / 10));
  }

  function normalizedPageSizeValue(value: unknown): number {
    const numeric = Math.floor(Number(value));
    if (!Number.isFinite(numeric) || numeric <= 0) return DEFAULT_PAGE_SIZE;
    return Math.max(1, Math.min(100, numeric));
  }

  function normalizeSchemaSyncProgress(value: unknown): SchemaSyncProgress | null {
    if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
    const candidate = value as Record<string, unknown>;
    return {
      status: isSchemaSyncStatus(candidate.status) ? candidate.status : 'idle',
      syncedRows: normalizedRowCount(candidate.syncedRows),
      totalRows: normalizedRowCount(candidate.totalRows),
      localRows: normalizedRowCount(candidate.localRows),
      pinnedRows: normalizedRowCount(candidate.pinnedRows),
      missingRows: normalizedRowCount(candidate.missingRows),
      cachedBytes: normalizedRowCount(candidate.cachedBytes),
      pinnedBytes: normalizedRowCount(candidate.pinnedBytes),
      downloadedBytes: normalizedRowCount(candidate.downloadedBytes),
      downloadSpeedBytesPerSecond: normalizedRowCount(candidate.downloadSpeedBytesPerSecond),
      measuredWireSpeedBytesPerSecond: normalizedRowCount(candidate.measuredWireSpeedBytesPerSecond),
      wireSpeedUtilization: normalizedOptionalRatio(candidate.wireSpeedUtilization),
      wireSpeedTarget: normalizedOptionalRatio(candidate.wireSpeedTarget) ?? 0.8,
      wireSpeedTargetMet: typeof candidate.wireSpeedTargetMet === 'boolean' ? candidate.wireSpeedTargetMet : null,
      manifestDiscoveryMs: normalizedRowCount(candidate.manifestDiscoveryMs),
      networkTransferMs: normalizedRowCount(candidate.networkTransferMs),
      verificationMs: normalizedRowCount(candidate.verificationMs),
      flatSqlMaterializationMs: normalizedRowCount(candidate.flatSqlMaterializationMs),
      providerPeerId: normalizedOptionalString(candidate.providerPeerId),
      providerPublicKey: normalizedOptionalString(candidate.providerPublicKey),
      snapshotId: normalizedOptionalString(candidate.snapshotId),
      head: normalizedOptionalString(candidate.head),
      cursor: normalizedOptionalString(candidate.cursor),
      nextCursor: normalizedOptionalString(candidate.nextCursor),
      highWaterMark: normalizedOptionalString(candidate.highWaterMark),
      queryProfile: normalizedOptionalString(candidate.queryProfile),
      chunkHash: normalizedOptionalString(candidate.chunkHash),
      syncProtocol: normalizedOptionalString(candidate.syncProtocol),
      syncFilter: normalizedOptionalString(candidate.syncFilter),
      verifiedChunks: normalizedStringArray(candidate.verifiedChunks).slice(-256),
      lastSyncedAt: typeof candidate.lastSyncedAt === 'string' ? candidate.lastSyncedAt : null,
      progressFingerprint: normalizedOptionalString(candidate.progressFingerprint),
      lastAdvancedAt: normalizedOptionalTimestamp(candidate.lastAdvancedAt),
      lastProgressObservedAt: normalizedOptionalTimestamp(candidate.lastProgressObservedAt),
      stallObservationCount: normalizedRowCount(candidate.stallObservationCount),
      stalledSince: normalizedOptionalTimestamp(candidate.stalledSince),
      error: typeof candidate.error === 'string' ? candidate.error : null,
    };
  }

  function normalizedOptionalString(value: unknown): string | null {
    return typeof value === 'string' && value.trim() ? value.trim() : null;
  }

  function normalizedString(value: unknown): string {
    return typeof value === 'string' ? value : '';
  }

  function normalizedOptionalTimestamp(value: unknown): string | null {
    if (typeof value !== 'string' || !value.trim()) return null;
    const timestamp = Date.parse(value);
    return Number.isFinite(timestamp) ? new Date(timestamp).toISOString() : null;
  }

  function normalizedStringArray(value: unknown): string[] {
    if (!Array.isArray(value)) return [];
    return value
      .filter((entry): entry is string => typeof entry === 'string' && entry.trim().length > 0)
      .map((entry) => entry.trim());
  }

  function normalizedRowCount(value: unknown): number {
    const numeric = Number(value);
    if (!Number.isFinite(numeric) || numeric < 0) return 0;
    return Math.floor(numeric);
  }

  function normalizedOptionalRatio(value: unknown): number | null {
    if (value === null || value === undefined || value === '') return null;
    const numeric = Number(value);
    if (!Number.isFinite(numeric) || numeric < 0) return null;
    return boundedWireSpeedUtilization(numeric);
  }

  function isSchemaSyncStatus(value: unknown): value is SchemaSyncStatus {
    return value === 'idle' || value === 'syncing' || value === 'synced' || value === 'capped' || value === 'error';
  }

  function isStorageUnit(value: unknown): value is StorageUnit {
    return typeof value === 'string' && STORAGE_CAP_UNITS.includes(value as StorageUnit);
  }

  function normalizeDataQueryProfile(value: unknown): DataQueryProfile {
    const candidate = String(value ?? '').trim();
    return DATA_QUERY_PROFILES.some((profile) => profile.id === candidate)
      ? candidate as DataQueryProfile
      : DEFAULT_QUERY_PROFILE;
  }

  function workbenchColumnsForStandard(standardId: string, rows: WorkbenchRow[]): WorkbenchColumn[] {
    const standardColumns = STANDARD_FIELD_COLUMNS[standardId] ?? [];
    const metadataColumns = standardId === 'PNM'
      ? METADATA_COLUMNS.filter((column) => column.key !== 'cid')
      : METADATA_COLUMNS;
    const knownKeys = new Set([...metadataColumns, ...standardColumns].map((column) => column.key));
    const dynamicColumns: WorkbenchColumn[] = [];
    for (const row of rows) {
      for (const key of Object.keys(row.decoded)) {
        if (INTERNAL_COLUMN_KEYS.has(key)) continue;
        if (knownKeys.has(key)) continue;
        knownKeys.add(key);
        dynamicColumns.push({ key, label: labelFromFieldKey(key), source: 'standard' });
      }
    }
    if (standardColumns.length === 0) return [...metadataColumns, ...dynamicColumns];
    return [...standardColumns, ...metadataColumns, ...dynamicColumns];
  }

  function syncVisibleColumnKeys(columns: WorkbenchColumn[]): void {
    const columnKeys = columns.map((column) => column.key);
    const defaultKeys = dataAwareDefaultColumnKeys(columns, decodedRows);
    updateVisibleColumnKeys(defaultKeys.length > 0 ? defaultKeys : columnKeys);
  }

  function dataAwareDefaultColumnKeys(columns: WorkbenchColumn[], rows: WorkbenchRow[]): string[] {
    if (rows.length === 0) return columns.map((column) => column.key).filter((key) => !INTERNAL_COLUMN_KEYS.has(key));
    return columns
      .filter((column) => !INTERNAL_COLUMN_KEYS.has(column.key))
      .filter((column) => rows.some((row) => hasDisplayValue(tableValue(row, column.key))))
      .map((column) => column.key);
  }

  function updateVisibleColumnKeys(nextKeys: string[]): void {
    if (arraysEqual(visibleColumnKeys, nextKeys)) return;
    visibleColumnKeys = nextKeys;
  }

  function decodeWorkbenchRecord(record: RawDataRecord): WorkbenchRow {
    try {
      const bytes = recordBytes(record);
      const standardId = standardIdFromSchema(record.schemaName);
      if (standardId === 'CAT') return { record, decoded: decodeCatFlatBuffer(bytes) };
      if (standardId === 'EPM') return { record, decoded: decodeEpmFlatBuffer(bytes) };
      if (standardId === 'OMM') return { record, decoded: decodeOmmFlatBuffer(bytes) };
      if (standardId === 'PNM') return { record, decoded: decodePnmFlatBuffer(bytes) };
      return { record, decoded: {} };
    } catch {
      return { record, decoded: {} };
    }
  }

  function recordBytes(record: RawDataRecord): Uint8Array {
    if (record.dataBytes) return record.dataBytes;
    throw new Error('raw FlatBuffer bytes are unavailable');
  }

  function stringifyCellValue(value: unknown): string {
    if (value == null) return '';
    if (Array.isArray(value)) return value.map((entry) => stringifyCellValue(entry)).join(', ');
    if (typeof value === 'object') return JSON.stringify(value);
    return String(value);
  }

  function hasDisplayValue(value: unknown): boolean {
    if (value == null) return false;
    if (Array.isArray(value)) return value.some(hasDisplayValue);
    if (typeof value === 'string') return value.trim().length > 0;
    if (typeof value === 'object') return Object.values(value).some(hasDisplayValue);
    return true;
  }

  function arraysEqual(left: string[], right: string[]): boolean {
    return left.length === right.length && left.every((value, index) => value === right[index]);
  }

  function labelFromFieldKey(key: string): string {
    return key.split('_').filter(Boolean).map((part) => `${part.slice(0, 1).toUpperCase()}${part.slice(1)}`).join(' ');
  }

  function standardIdFromSchema(schemaName: string | null | undefined): string {
    const id = String(schemaName || '').split('.')[0]?.trim().toUpperCase();
    return id || DEFAULT_STANDARD_ID;
  }

  function standardOptionsFromSummary(summary: DataSummary | null): StandardOption[] {
    const ids = new Set<string>();
    for (const schema of summary?.schemas ?? []) ids.add(standardIdFromSchema(schema.schemaName));
    for (const source of summary?.sources ?? []) ids.add(standardIdFromSchema(source.schemaName));
    if (ids.size === 0) ids.add(DEFAULT_STANDARD_ID);
    return Array.from(ids)
      .map((id) => ({ id, remoteRows: totalRowsForStandardId(summary, id) ?? 0 }))
      .sort((left, right) => right.remoteRows - left.remoteRows || left.id.localeCompare(right.id));
  }

  function standardIdsFromSummary(summary: DataSummary | null): string[] {
    return standardOptionsFromSummary(summary).map((option) => option.id);
  }

  function preferredStandardIdFromSummary(summary: DataSummary | null): string {
    return standardOptionsFromSummary(summary)[0]?.id ?? DEFAULT_STANDARD_ID;
  }

  function preferredStandardIdForDataSourceSummary(
    dataSourceId: string,
    datastoreKey: string | null,
    summary: DataSummary | null,
  ): string {
    const summaryStandardIds = new Set(standardIdsFromSummary(summary));
    const subscribed = dataDirectoryState.subscriptions
      .filter((subscription) => (
        subscription.dataSourceId === dataSourceId
        && (datastoreKey === null || subscription.datastoreKey === datastoreKey)
        && summaryStandardIds.has(subscription.standardId)
      ))
      .sort((left, right) => right.remoteRows - left.remoteRows || left.standardId.localeCompare(right.standardId));
    return subscribed[0]?.standardId ?? preferredStandardIdFromSummary(summary);
  }

  function schemaNameForStandardId(standardId: string): string {
    const id = standardId.trim().toUpperCase() || DEFAULT_STANDARD_ID;
    const exact = dataSummary?.schemas.find((schema) => standardIdFromSchema(schema.schemaName) === id);
    if (exact) return exact.schemaName;
    const source = dataSummary?.sources.find((entry) => standardIdFromSchema(entry.schemaName) === id);
    if (source) return source.schemaName;
    return `${id}.${SCHEMA_EXTENSION}`;
  }

  function totalRowsForStandardId(summary: DataSummary | null, standardId: string): number | null {
    if (!summary) return null;
    const schemaCount = summary.schemas.find((schema) => standardIdFromSchema(schema.schemaName) === standardId)?.count;
    if (typeof schemaCount === 'number') return schemaCount;
    let sourceCount = 0;
    let matchedSource = false;
    for (const source of summary.sources) {
      if (standardIdFromSchema(source.schemaName) !== standardId) continue;
      matchedSource = true;
      sourceCount += source.count;
    }
    return matchedSource ? sourceCount : null;
  }

  function remoteRowsForSummarySubscription(summary: DataSummary | null, subscription: DataFeedSubscription): number | null {
    const source = summarySourceForSubscription(summary, subscription);
    if (source && typeof source.count === 'number') return source.count;
    if (!summary) return null;
    return totalRowsForStandardId(summary, subscription.standardId);
  }

  function feedIdentityForSummarySubscription(summary: DataSummary | null, subscription: DataFeedSubscription): FeedIdentity | null {
    const source = summarySourceForSubscription(summary, subscription);
    if (!source) return null;
    return {
      providerId: source.providerId || null,
      sourceName: source.sourceName || null,
    };
  }

  function summarySourceForSubscription(summary: DataSummary | null, subscription: DataFeedSubscription): DataSummary['sources'][number] | null {
    if (!summary) return null;
    const datastoreKey = subscription.datastoreKey ?? null;
    if (datastoreKey) {
      return summary.sources.find((source) => (
        source.datastoreKey === datastoreKey
        && standardIdFromSchema(source.schemaName) === subscription.standardId
      )) ?? null;
    }
    return preferredDataSummarySource(summary.sources, {
      standardId: subscription.standardId,
      providerId: subscription.providerId,
      sourceName: subscription.sourceName,
    });
  }

  function scanTotalRowsForStandard(scan: DataScanResult | null, standardId: string): number | null {
    if (!scan || standardIdFromSchema(scan.schema) !== standardId) return null;
    return Number.isFinite(scan.totalCount) ? scan.totalCount : null;
  }

  function remoteRowsAreLoading(
    pageLoading: boolean,
    remoteRows: number,
    progress: SchemaSyncProgress,
    summary: DataSummary | null,
    scan: DataScanResult | null,
    standardId: string,
    datastoreKey: string | null,
    activeStandardId: string,
  ): boolean {
    if (remoteRows > 0 || progress.totalRows > 0) return false;
    if (progress.lastSyncedAt || progress.status === 'synced') return false;
    if (summaryHasRemoteRowsForStandard(summary, standardId, datastoreKey)) return false;
    if (standardId === activeStandardId && scanTotalRowsForStandard(scan, standardId) !== null) return false;
    if (pageLoading) return true;
    return true;
  }

  function isSchemaRemoteRowsLoading(schema: SchemaSyncRow): boolean {
    return schema.remoteRowsLoading;
  }

  function summaryHasRemoteRowsForStandard(summary: DataSummary | null, standardId: string, datastoreKey: string | null): boolean {
    if (!summary) return false;
    if (datastoreKey) {
      return summary.sources.some((source) => (
        source.datastoreKey === datastoreKey
        && standardIdFromSchema(source.schemaName) === standardId
      ));
    }
    return totalRowsForStandardId(summary, standardId) !== null;
  }

  function formatNumber(value: number): string {
    return new Intl.NumberFormat('en-US').format(value);
  }

  function syncProgressLabel(schema: SchemaSyncRow): string {
    if (isSchemaRemoteRowsLoading(schema)) return 'Loading';
    const rowCounts = syncRowCountSummary({
      localRows: schema.localRows,
      syncedRows: schema.progress.syncedRows,
      pinnedRows: schema.progress.pinnedRows,
      remoteRows: schema.remoteRows,
      totalRows: schema.progress.totalRows,
    });
    if (rowCounts.totalRows === 0) return 'No remote rows';
    return syncRowCountSummaryLabel(rowCounts);
  }

  function loadingSchemaDataLabel(schema: SchemaSyncRow, formattedValue: string): string {
    return loadingMetricLabel(isSchemaRemoteRowsLoading(schema), formattedValue);
  }

  function schemaRowsCountLabel(schema: SchemaSyncRow): string {
    return loadingSchemaDataLabel(schema, `${formatNumber(schema.localRows)} local / ${formatNumber(schema.remoteRows)} remote`);
  }

  function schemaRemoteRowsLabel(schema: SchemaSyncRow): string {
    return loadingSchemaDataLabel(schema, formatNumber(schema.remoteRows));
  }

  function schemaLocalRowsLabel(schema: SchemaSyncRow): string {
    return formatNumber(schema.localRows);
  }

  function schemaPinnedRowsLabel(schema: SchemaSyncRow): string {
    return loadingSchemaDataLabel(schema, `${formatNumber(schema.progress.pinnedRows)} pinned`);
  }

  function schemaCachedBytesLabel(schema: SchemaSyncRow): string {
    return loadingSchemaDataLabel(schema, formatBytes(schema.cachedBytes));
  }

  function schemaProgressLabel(schema: SchemaSyncRow): string {
    return loadingSchemaDataLabel(schema, syncProgressLabel(schema));
  }

  function schemaDownloadSpeedLabel(schema: SchemaSyncRow): string {
    return loadingSchemaDataLabel(schema, syncDownloadSpeedLabel(schema));
  }

  function schemaStoragePressureLabel(schema: SchemaSyncRow): string {
    const capBytes = storageCapBytes(schema.preference);
    if (capBytes <= 0) return 'No storage cap';
    const cachedBytes = Math.max(schema.cachedBytes, schema.progress.cachedBytes);
    const percent = Math.min(100, Math.max(0, Math.round((cachedBytes / capBytes) * 100)));
    return loadingSchemaDataLabel(schema, `${formatBytes(cachedBytes)} / ${formatBytes(capBytes)} cap (${percent}%)`);
  }

  function schemaRetentionPolicyLabel(schema: SchemaSyncRow): string {
    const profile = DATA_QUERY_PROFILES.find((entry) => entry.id === schema.queryProfile)?.label ?? schema.queryProfile;
    const retention = dataRetentionPolicyLabel(schema.retentionPolicy);
    const filter = schema.syncFilter.trim() ? `Filter: ${schema.syncFilter.trim()}` : 'All records';
    return `${profile} · ${retention} · ${filter}`;
  }

  function schemaLastSyncedLabel(schema: SchemaSyncRow): string {
    return loadingSchemaDataLabel(schema, schema.progress.lastSyncedAt ? formatDateTime(schema.progress.lastSyncedAt) : 'Never synced');
  }

  function schemaSyncStalled(schema: SchemaSyncRow): boolean {
    return isSchemaSyncProgressStalled(schema.progress);
  }

  function schemaSyncStalledLabel(schema: SchemaSyncRow): string {
    if (!schema.progress.stalledSince) return 'Sync stalled';
    return `Sync stalled since ${formatDateTime(schema.progress.stalledSince)}`;
  }

  function schemaRetryDisabled(schema: SchemaSyncRow): boolean {
    const active = activeSyncKeys.has(schemaSyncPreferenceKey(schema.dataSourceId, schema.id, schema.datastoreKey));
    return active && !schemaSyncStalled(schema);
  }

  function schemaHealthLabel(schema: SchemaSyncRow): string {
    if (isSchemaRemoteRowsLoading(schema)) return 'Loading';
    if (schema.progress.error) return schema.progress.error;
    if (schemaSyncStalled(schema)) return schemaSyncStalledLabel(schema);
    if (schema.remoteRows > schema.localRows) return `${formatNumber(schema.remoteRows - schema.localRows)} missing`;
    return schemaProgressLabel(schema);
  }

  function subscriptionProductLabel(schema: SchemaSyncRow): string {
    return catalogRowForSchema(schema)?.product ?? `${schema.id} Feed`;
  }

  function subscriptionAccessLabel(schema: SchemaSyncRow): string {
    return catalogRowForSchema(schema)?.access.label ?? 'Free';
  }

  function subscriptionPlanLabel(schema: SchemaSyncRow): string {
    return catalogRowForSchema(schema)?.plan.label ?? 'No paid plan';
  }

  function subscriptionCostLabel(schema: SchemaSyncRow): string {
    return catalogRowForSchema(schema)?.plan.priceLabel ?? 'Free';
  }

  function subscriptionRenewalLabel(schema: SchemaSyncRow): string {
    return catalogRowForSchema(schema)?.plan.renewalLabel ?? 'No renewal';
  }

  function subscriptionStorageStateLabel(schema: SchemaSyncRow): string {
    return `${schemaCachedBytesLabel(schema)} cached / ${schemaPinnedRowsLabel(schema)}`;
  }

  function subscriptionSyncPolicyLabel(schema: SchemaSyncRow): string {
    const profile = DATA_QUERY_PROFILES.find((entry) => entry.id === schema.queryProfile)?.label ?? schema.queryProfile;
    const retention = dataRetentionPolicyLabel(schema.retentionPolicy);
    const filter = schema.syncFilter.trim() ? 'filtered' : 'all records';
    return `${profile} / ${retention} / ${filter}`;
  }

  function dataRetentionPolicyLabel(policy: DataFeedRetentionPolicy): string {
    return DATA_RETENTION_POLICIES.find((entry) => entry.id === policy)?.label ?? policy;
  }

  function standardOptionLabel(standard: StandardOption): string {
    const countsLoading = dataPageLoading
      || (
        standard.remoteRows === 0
        && totalRowsForStandardId(dataSummary, standard.id) === null
        && scanTotalRowsForStandard(dataScan, standard.id) === null
      );
    return `${standard.id} (${loadingMetricLabel(countsLoading, formatNumber(standard.remoteRows))})`;
  }

  function syncDownloadSpeedLabel(schema: SchemaSyncRow): string {
    return `Download ${formatBytesPerSecond(schema.progress.downloadSpeedBytesPerSecond)}`;
  }

  function syncTimingLabel(schema: SchemaSyncRow): string {
    const progress = schema.progress;
    return `Timing: manifest ${formatDuration(progress.manifestDiscoveryMs)} / network ${formatDuration(progress.networkTransferMs)} / verify ${formatDuration(progress.verificationMs)} / FlatSQL ${formatDuration(progress.flatSqlMaterializationMs)}`;
  }

  function syncStatusLabel(schema: SchemaSyncRow): string {
    if (isSchemaRemoteRowsLoading(schema)) return 'Loading';
    if (schemaSyncStalled(schema)) return 'Stalled';
    return formatSchemaSyncStatusLabel({
      preferenceMode: schema.preference.mode,
      progressStatus: schema.progress.status,
      localRows: schema.localRows,
      remoteRows: schema.remoteRows,
    });
  }

  function syncBubbleTone(schema: SchemaSyncRow): SyncBubbleTone {
    if (isSchemaRemoteRowsLoading(schema)) return 'loading';
    if (schema.preference.mode !== 'sync') return 'paused';
    if (schema.progress.status === 'error') return 'failed';
    if (schemaSyncStalled(schema)) return 'stale';
    if (schema.progress.status === 'capped') return 'capped';
    if (schema.progress.status === 'syncing') return 'syncing';
    if (schema.progress.status === 'synced' || (schema.remoteRows > 0 && schema.localRows >= schema.remoteRows)) return 'synced';
    if (schema.remoteRows > schema.localRows) return 'queued';
    return 'ready';
  }

  function syncBubbleLetter(schema: SchemaSyncRow): string {
    switch (syncBubbleTone(schema)) {
      case 'loading': return 'L';
      case 'paused': return 'P';
      case 'failed': return 'E';
      case 'capped': return 'C';
      case 'syncing': return 'S';
      case 'synced': return 'V';
      case 'stale': return 'T';
      case 'queued': return 'Q';
      default: return 'R';
    }
  }

  function syncBubbleTooltip(schema: SchemaSyncRow): string {
    const parts = [
      `Status: ${syncStatusLabel(schema)}`,
      `Rows: ${syncProgressLabel(schema)}`,
      syncDownloadSpeedLabel(schema),
      `Next: ${nextSyncAttemptLabel(schema)}`,
      `Last: ${schemaLastSyncedLabel(schema)}`,
      syncTimingLabel(schema),
    ];
    if (schema.progress.error) parts.push(`Error: ${schema.progress.error}`);
    if (schemaSyncStalled(schema)) parts.push(schemaSyncStalledLabel(schema));
    return parts.join('\n');
  }

  function catalogRowSyncBubbleTone(row: DataCatalogRow): SyncBubbleTone {
    const schema = schemaForCatalogRow(row);
    if (schema) return syncBubbleTone(schema);
    if (row.sync.status === 'failed') return 'failed';
    if (row.sync.status === 'capped') return 'capped';
    if (row.sync.status === 'syncing') return 'syncing';
    if (row.sync.status === 'synced') return 'synced';
    if (row.sync.status === 'stale') return 'stale';
    if (row.sync.status === 'queued') return 'queued';
    return 'ready';
  }

  function catalogRowSyncBubbleLetter(row: DataCatalogRow): string {
    const schema = schemaForCatalogRow(row);
    if (schema) return syncBubbleLetter(schema);
    switch (catalogRowSyncBubbleTone(row)) {
      case 'failed': return 'E';
      case 'capped': return 'C';
      case 'syncing': return 'S';
      case 'synced': return 'V';
      case 'stale': return 'T';
      case 'queued': return 'Q';
      default: return 'R';
    }
  }

  function catalogRowSyncBubbleTooltip(row: DataCatalogRow): string {
    const schema = schemaForCatalogRow(row);
    if (schema) return syncBubbleTooltip(schema);
    return [
      `Status: ${catalogRowSyncLabel(row)}`,
      `Next: ${row.sync.nextAttempt}`,
      `Freshness: ${catalogRowFreshnessLabel(row)}`,
    ].join('\n');
  }

  function nextSyncAttemptLabel(schema: SchemaSyncRow): string {
    if (isSchemaRemoteRowsLoading(schema)) return 'Loading';
    if (schemaSyncStalled(schema)) return 'Stalled; retry recommended';
    const key = schemaSyncPreferenceKey(schema.dataSourceId, schema.id, schema.datastoreKey);
    if (activeSyncKeys.has(key)) return 'Syncing now';
    if (schema.progress.status === 'error') return 'On next scheduler pass';
    if (schema.preference.mode !== 'sync') return 'Not scheduled';
    if (schema.remoteRows > schema.localRows) return 'Queued';
    return 'When remote rows advance';
  }

  function formatDateTime(value: string): string {
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return value;
    return new Intl.DateTimeFormat(undefined, {
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
    }).format(date);
  }

  function formatBytes(value: number): string {
    if (!Number.isFinite(value) || value <= 0) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    let unitIndex = 0;
    let nextValue = value;
    while (nextValue >= 1024 && unitIndex < units.length - 1) {
      nextValue /= 1024;
      unitIndex += 1;
    }
    const digits = nextValue >= 10 || unitIndex === 0 ? 0 : 1;
    return `${nextValue.toFixed(digits)} ${units[unitIndex]}`;
  }

  function formatBytesPerSecond(value: number): string {
    return `${formatBytes(value)}/s`;
  }

  function formatDuration(milliseconds: number): string {
    const value = Math.max(0, Math.floor(milliseconds));
    if (value < 1000) return `${value} ms`;
    const seconds = value / 1000;
    if (seconds < 60) return `${seconds.toFixed(seconds < 10 ? 1 : 0)} s`;
    const minutes = Math.floor(seconds / 60);
    const remainingSeconds = Math.round(seconds % 60);
    return `${minutes}m ${remainingSeconds}s`;
  }

  function storageCapBytes(preference: SchemaSyncPreference): number {
    const unitMultiplier = preference.storageUnit === 'TB'
      ? 1024 ** 4
      : preference.storageUnit === 'GB'
        ? 1024 ** 3
        : 1024 ** 2;
    return Math.max(1, Math.floor(preference.storageCap * unitMultiplier));
  }

  function normalizedPageSize(): number {
    return normalizedPageSizeValue(pageSize);
  }

  function currentExplorerViewName(): string {
    const mode = explorerSearchMode === 'sql' ? 'SQL' : 'Search';
    return `${selectedStandardId} ${mode} view`;
  }

  function savedExplorerViewId(): string {
    const random = Math.random().toString(36).slice(2, 9);
    return `explorer-view-${Date.now()}-${random}`;
  }

  function savedExplorerViewOptionLabel(view: SavedExplorerView): string {
    const source = subscribedSourceOptions.find((option) => option.id === subscriptionSourceKey(view.dataSourceId, view.datastoreKey))?.label
      ?? view.dataSourceId;
    return `${view.name} · ${source} · ${view.standardId}`;
  }

  function shorten(value: string | null | undefined, length = 28): string {
    if (!value) return '';
    if (value.length <= length) return value;
    return `${value.slice(0, Math.max(4, length - 5))}...${value.slice(-4)}`;
  }

  function backendForSelectedDataSource(): SdnBackend | null {
    const source = currentDataSourceOption();
    if (!backend || !source) return null;
    if (source.kind === 'local') return backend;
    return null;
  }

  function backendConfigForDataSource(
    source: DataSourceOption | null,
    datastoreKey: string | null = null,
    feedIdentity: FeedIdentity | null = null,
  ): WorkerFlatSqlSyncBackendConfig | null {
    if (!source || source.kind !== 'configured' || !source.peerId || !source.syncAddrs?.length) return null;
    const providerId = feedIdentity?.providerId ?? configuredProviderIdFromSource(source);
    const sourceName = feedIdentity?.sourceName ?? null;
    const publishedShardSources = publishedShardSourcesForDataSource(source, datastoreKey, {
      providerId,
      sourceName,
    });
    return {
      targetPeerId: source.peerId,
      candidateAddrs: source.syncAddrs,
      datastoreKey,
      providerId,
      sourceName,
      displayName: source.label,
      publicKey: source.publicKey,
      measuredWireSpeedBytesPerSecond: measuredWireSpeedBytesPerSecondForSource(source.id),
      publishedShardSources: publishedShardSources,
    };
  }

  function publishedShardSourcesForDataSource(
    source: DataSourceOption,
    datastoreKey: string | null,
    feedIdentity: FeedIdentity,
  ): WorkerFlatSqlSyncBackendConfig[] {
    return dataSourceOptions
      .filter((candidate) => candidate.kind === 'configured' && candidate.id !== source.id)
      .filter((candidate) => Boolean(candidate.peerId && candidate.syncAddrs?.length))
      .filter((candidate) => dataSourceCanMirrorPublishedShard(candidate, source, feedIdentity))
      .map((candidate) => ({
        targetPeerId: candidate.peerId ?? '',
        candidateAddrs: candidate.syncAddrs ?? [],
        datastoreKey,
        providerId: feedIdentity.providerId ?? null,
        sourceName: feedIdentity.sourceName ?? null,
        displayName: candidate.label,
        publicKey: candidate.publicKey,
        measuredWireSpeedBytesPerSecond: measuredWireSpeedBytesPerSecondForSource(candidate.id),
      }));
  }

  function dataSourceCanMirrorPublishedShard(
    candidate: DataSourceOption,
    source: DataSourceOption,
    feedIdentity: FeedIdentity,
  ): boolean {
    const providerId = feedIdentity.providerId ?? configuredProviderIdFromSource(source);
    const candidateProviderId = configuredProviderIdFromSource(candidate);
    if (providerId && candidateProviderId && candidateProviderId !== providerId) return false;
    const sourceName = feedIdentity.sourceName ?? source.sourceName ?? null;
    if (sourceName && candidate.sourceName && candidate.sourceName !== sourceName) return false;
    return true;
  }

  function measuredWireSpeedBytesPerSecondForSource(dataSourceId: string): number {
    const local = typeof window !== 'undefined' ? window.localStorage : null;
    const env = import.meta.env as ImportMetaEnv & {
      readonly SDN_UI_WIRE_SPEED_BYTES_PER_SECOND?: string;
      readonly SDN_UI_WIRE_SPEED_BPS?: string;
    };
    const byteCandidates = [
      local?.getItem(`sdn-data-wire-speed-bytes-per-second:${dataSourceId}`),
      local?.getItem('sdn-data-wire-speed-bytes-per-second'),
      env.SDN_UI_WIRE_SPEED_BYTES_PER_SECOND,
    ];
    for (const candidate of byteCandidates) {
      const numeric = Number(candidate);
      if (Number.isFinite(numeric) && numeric > 0) return Math.floor(numeric);
    }
    const bitCandidates = [
      local?.getItem(`sdn-data-wire-speed-bps:${dataSourceId}`),
      local?.getItem('sdn-data-wire-speed-bps'),
      env.SDN_UI_WIRE_SPEED_BPS,
    ];
    for (const candidate of bitCandidates) {
      const numeric = Number(candidate);
      if (Number.isFinite(numeric) && numeric > 0) return Math.floor(numeric / 8);
    }
    return 0;
  }

  function currentDataSourceOption(): DataSourceOption | null {
    const options = buildDataSourceOptions(backend, configuredDataSources, peers, trustedPeers);
    return options.find((source) => source.id === selectedDataSourceId) ?? options[0] ?? null;
  }

  function dataSourceOptionForId(dataSourceId: string): DataSourceOption | null {
    const options = buildDataSourceOptions(backend, configuredDataSources, peers, trustedPeers);
    return options.find((source) => source.id === dataSourceId) ?? null;
  }

  function buildDataSourceOptions(
    localBackend: SdnBackend | null,
    configuredNodes: ConfiguredSdnNode[],
    observedPeers: ObservedSdnPeer[],
    knownTrustedPeers: ObservedSdnPeer[],
  ): DataSourceOption[] {
    const options: DataSourceOption[] = [{
      id: LOCAL_DATA_SOURCE_ID,
      label: 'Local Desktop',
      detail: localBackend?.mode ?? 'desktop-local',
      peerId: null,
      publicKey: null,
      kind: 'local',
      searchText: 'local desktop local-node',
    }];
    const observedNames = new Map([
      ...knownTrustedPeers.map((peer) => [peer.id, peer.name] as const),
      ...observedPeers.map((peer) => [peer.id, peer.name] as const),
    ]);

    for (const node of configuredNodes) {
      const peerId = configuredNodePeerId(node);
      const syncAddrs = configuredNodeSyncAddrs(node, peerId);
      if (!peerId || syncAddrs.length === 0) continue;
      const publicKey = configuredNodePublicKey(node) ?? peerId;
      const providerId = configuredNodeProviderId(node);
      const sourceName = configuredNodeSourceName(node);
      const label = configuredNodeLabel(node, observedNames, peerId);
      const detail = [node.id, configuredNodeHostName(node)].filter(Boolean).join(' / ');
      const artifactPeerAddrs = configuredNodeArtifactPeerAddrs(node);
      options.push({
        id: `configured:${node.id}`,
        label,
        detail,
        peerId,
        publicKey,
        providerId,
        sourceName,
        kind: 'configured',
        syncAddrs,
        artifactPeerAddrs,
        searchText: [label, detail, publicKey, peerId, providerId, sourceName, node.trustLevel, node.trust_level, syncAddrs.join(' '), artifactPeerAddrs.join(' ')].filter(Boolean).join(' ').toLowerCase(),
      });
    }

    return dedupeDataSourceOptions(options);
  }

  function dataDirectoryMigrationSources(options: DataSourceOption[]): DataDirectoryMigrationSource[] {
    return options
      .filter((source) => source.peerId)
      .map((source) => ({
        dataSourceId: source.id,
        peerId: source.peerId ?? source.id,
        providerName: source.label,
        providerPublicKey: source.publicKey,
        legacyDataSourceIds: [
          source.id.startsWith('configured:') ? source.id.slice('configured:'.length) : '',
          source.peerId ?? '',
          source.providerId ?? '',
        ].filter(Boolean),
      }));
  }

  function preferredDataSourceId(options: DataSourceOption[]): string {
    return options.find((source) => source.searchText.includes('celestrak'))?.id
      ?? options.find((source) => source.kind === 'configured')?.id
      ?? LOCAL_DATA_SOURCE_ID;
  }

  function preferredSubscribedDataSourceId(subscriptions: DataFeedSubscription[]): string | null {
    const firstConfigured = subscriptions.find((subscription) => subscription.dataSourceId !== LOCAL_DATA_SOURCE_ID);
    return firstConfigured?.dataSourceId ?? subscriptions[0]?.dataSourceId ?? null;
  }

  function normalizeConfiguredDataSources(payload: unknown): ConfiguredSdnNode[] {
    const records = recordsFromPayloadKey(payload, 'nodes');
    return records.map((record): ConfiguredSdnNode | null => {
      const id = readRecordString(record, 'id', 'peer_id', 'peerId');
      if (!id) return null;
      const addrs = Array.isArray(record.addrs) ? record.addrs.filter((entry): entry is string => typeof entry === 'string') : [];
      return {
        id,
        name: readRecordString(record, 'name', 'display_name', 'displayName', 'dn') ?? id,
        addrs,
        trust_level: readRecordString(record, 'trust_level', 'trustLevel') ?? undefined,
        metadata: isRecord(record.metadata) ? record.metadata : {},
      };
    }).filter((record): record is ConfiguredSdnNode => record !== null);
  }

  function configuredNodeSyncAddrs(node: ConfiguredSdnNode, peerId: string | null): string[] {
    if (!peerId) return [];
    return node.addrs
      .map((addr) => addr.trim())
      .filter((addr) => addr.includes('/p2p/') && addr.endsWith(`/p2p/${peerId}`) && isFlatSqlSyncTransportAddr(addr));
  }

  function isFlatSqlSyncTransportAddr(addr: string): boolean {
    return /\/tcp\/\d+\/wss?\//.test(addr)
      || (/\/tcp\/\d+\/p2p\//.test(addr) && !addr.includes('/ws') && !addr.includes('/wss'))
      || addr.includes('/webrtc-direct/')
      || addr.includes('/webtransport/');
  }

  function configuredNodeHostName(node: ConfiguredSdnNode): string {
    return readRecordString(node.metadata ?? {}, 'host_name', 'hostName') ?? '';
  }

  function configuredNodePeerId(node: ConfiguredSdnNode): string | null {
    return readRecordString(node.metadata ?? {}, 'peer_id', 'peerId')
      ?? node.addrs.map((addr) => addr.split('/p2p/')[1]).find((value): value is string => Boolean(value))
      ?? null;
  }

  function configuredNodePublicKey(node: ConfiguredSdnNode): string | null {
    return readRecordString(node.metadata ?? {}, 'xpub', 'XPUB', 'extended_public_key', 'extendedPublicKey', 'hd_xpub', 'hdXpub', 'public_key', 'publicKey', 'signing_public_key', 'signingPublicKey');
  }

  function configuredNodeProviderId(node: ConfiguredSdnNode): string | null {
    return readRecordString(node.metadata ?? {}, 'provider_id', 'providerId');
  }

  function configuredNodeSourceName(node: ConfiguredSdnNode): string | null {
    return readRecordString(node.metadata ?? {}, 'source_name', 'sourceName');
  }

  function configuredNodeArtifactPeerAddrs(node: ConfiguredSdnNode): string[] {
    const metadata = node.metadata ?? {};
    return normalizeIpfsArtifactPeerAddrs(
      metadata.ipfs_artifact_addrs
        ?? metadata.ipfsArtifactAddrs
        ?? metadata.artifact_addrs
        ?? metadata.artifactAddrs,
    );
  }

  function configuredNodeLabel(node: ConfiguredSdnNode, observedNames: Map<string, string>, peerId: string | null): string {
    if (node.name && node.name !== node.id && node.name !== peerId) return node.name;
    const observedPeerName = peerId ? observedNames.get(peerId) : null;
    if (observedPeerName && observedPeerName !== peerId) return observedPeerName;
    const observedNodeName = observedNames.get(node.id);
    if (observedNodeName && observedNodeName !== node.id) return observedNodeName;
    return node.name ?? peerId ?? node.id;
  }

  function configuredProviderIdFromSource(source: DataSourceOption): string | null {
    if (source.providerId) return source.providerId;
    const id = source.id.startsWith('configured:') ? source.id.slice('configured:'.length) : source.id;
    return id || source.peerId;
  }

  function dedupeDataSourceOptions(options: DataSourceOption[]): DataSourceOption[] {
    const seen = new Set<string>();
    return options.filter((source) => {
      if (seen.has(source.id)) return false;
      seen.add(source.id);
      return true;
    });
  }

  function recordsFromPayloadKey(payload: unknown, key: string): Array<Record<string, unknown>> {
    if (Array.isArray(payload)) return payload.filter(isRecord);
    if (!isRecord(payload)) return [];
    const value = payload[key];
    if (Array.isArray(value)) return value.filter(isRecord);
    return [];
  }

  function readRecordString(record: Record<string, unknown>, ...keys: string[]): string | null {
    for (const key of keys) {
      const value = record[key];
      if (typeof value === 'string' && value.trim()) return value.trim();
    }
    return null;
  }

  function isRecord(value: unknown): value is Record<string, unknown> {
    return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
  }

  function syncInspectRoute(nextRoute: string, activeBackend: SdnBackend | null): void {
    const nextCid = inspectCidFromRoute(nextRoute);
    if (nextCid === inspectCid) return;
    inspectCid = nextCid;
    inspectGatewayUrl = '';
    if (nextCid && activeBackend) void loadInspectGateway(nextCid, activeBackend);
  }

  async function loadInspectGateway(cid: string, activeBackend: SdnBackend): Promise<void> {
    try {
      const result = await activeBackend.resolveCid(cid);
      if (inspectCid === cid) inspectGatewayUrl = result.data?.gatewayUrl ?? '';
    } catch {
      if (inspectCid === cid) inspectGatewayUrl = '';
    }
  }

  function inspectCidFromRoute(value: string): string {
    const query = value.split('?')[1] ?? '';
    const params = new URLSearchParams(query);
    return params.get('inspect') ?? '';
  }
</script>

<svelte:window on:click={handleCatalogOutsideClick} />

<article class="sdn-card sdn-glass sdn-workbench">
  {#if inspectCid}
    <div class="sdn-inspect-strip">
      <code>{inspectCid}</code>
      <a class="sdn-button sdn-button-muted" href={inspectGatewayUrl || '#'} target="_blank" rel="noreferrer">Open Gateway</a>
    </div>
  {/if}

  <div class="sdn-workbench-main">
      <nav class="sdn-data-subnav" aria-label="Data state">
        <div class="sdn-breadcrumb">{selectedDataSectionMeta.breadcrumb}</div>
        <div class="sdn-data-subnav-actions" role="group" aria-label="Data sections">
          {#each DATA_SECTIONS as section}
            <button
              class="sdn-button sdn-button-muted sdn-button-compact"
              class:active={selectedDataSection === section.id}
              type="button"
              on:click={() => setDataSection(section.id)}
            >
              {section.label}
            </button>
          {/each}
        </div>
      </nav>


      {#if selectedDataSection === 'store'}
        <section class="sdn-catalog-panel" aria-label="Data store">
          <div class="sdn-dataset-summary" aria-label="Store summary">
            <div class="sdn-dataset-metric" aria-label={`Local storage ${overviewLocalStorageMetric}`}>
              <span>Local storage</span>
              <strong>{overviewLocalStorageMetric}</strong>
            </div>
            <div class="sdn-dataset-metric" aria-label={`Subscriptions ${formatNumber(schemaSyncRows.length)} feeds`}>
              <span>Subscriptions</span>
              <strong>{formatNumber(schemaSyncRows.length)} feeds</strong>
            </div>
            <div class="sdn-dataset-metric" aria-label={`${dataCatalogSummary.billingMetricTitle} ${dataCatalogSummary.billingMetricValue}`}>
              <span>{dataCatalogSummary.billingMetricTitle}</span>
              <strong>{dataCatalogSummary.billingMetricValue}</strong>
            </div>
            <div class="sdn-dataset-metric" aria-label={`Data health ${overviewDataHealthMetric}`}>
              <span>Data health</span>
              <strong>{overviewDataHealthMetric}</strong>
            </div>
          </div>

          <div class="sdn-overview-visuals" aria-label="Store provider overview">
            <section class="sdn-overview-panel sdn-overview-storage-panel" aria-label="Storage by">
              <div class="sdn-overview-panel-head">
                <label class="sdn-overview-storage-group">
                  <span>Storage by</span>
                  <select class="sdn-input sdn-select" bind:value={overviewStorageGroup}>
                    {#each STORAGE_GROUP_OPTIONS as option}
                      <option value={option.id}>{option.label}</option>
                    {/each}
                  </select>
                </label>
                <span>{overviewStorageTotalMetric}</span>
              </div>
              <div class="sdn-storage-access-visual">
                <div
                  class="sdn-storage-donut"
                  style={overviewDonutStyle(dataOverviewVisuals.storageSegments)}
                  aria-label={`Storage by ${overviewStorageGroup} ${overviewStorageTotalMetric}`}
                >
                  <strong>{overviewStorageTotalMetric}</strong>
                  <span>local</span>
                </div>
                <div class="sdn-storage-legend">
                  {#each dataOverviewVisuals.storageSegments as segment}
                    <div class="sdn-storage-legend-row" style={storageSegmentStyle(segment)}>
                      <span>{segment.label}</span>
                      <strong>{formatBytes(segment.bytes)}</strong>
                      <em>{segment.percent}%</em>
                    </div>
                  {:else}
                    <p class="sdn-empty-inline">No local storage yet.</p>
                  {/each}
                </div>
              </div>
            </section>

            <section class="sdn-overview-panel" aria-label="Cost and storage by provider">
              <div class="sdn-overview-panel-head">
                <strong>Cost and storage by provider</strong>
                <span>{dataOverviewVisuals.monthlySpendLabel}</span>
              </div>
              <div class="sdn-provider-bars">
                {#each dataOverviewVisuals.providerBars as bar}
                  <button
                    class="sdn-provider-bar-row"
                    type="button"
                    style={providerBarStyle(bar)}
                    on:click={() => selectOverviewProviderBar(bar)}
                  >
                    <span>
                      <strong>{bar.provider}</strong>
                      <em>{bar.planLabels.join(', ')}</em>
                    </span>
                    <span>
                      <strong>{loadingMetricLabel(storageMetricsLoading, formatBytes(bar.localBytes))}</strong>
                      <em>{loadingMetricLabel(storageMetricsLoading, `${formatNumber(bar.pinnedRows)} pinned rows`)}</em>
                    </span>
                    <i aria-hidden="true"></i>
                  </button>
                {:else}
                  <p class="sdn-empty-inline">{dataPageLoading ? 'Loading' : 'No provider storage data.'}</p>
                {/each}
              </div>
            </section>
          </div>

          <div class="sdn-catalog-filters" aria-label="Store filters">
            <label class="sdn-catalog-search">
              <span>Search</span>
              <input class="sdn-input" bind:value={catalogSearchText} placeholder="Providers, products, message types" aria-label="Search store" />
            </label>
            <label>
              <span>Access</span>
              <select class="sdn-input sdn-select" bind:value={catalogAccessFilter}>
                <option value="all">All access</option>
                <option value="free">Free</option>
                <option value="paid">Paid</option>
                <option value="paid-active">Active paid</option>
                <option value="trial">Trial</option>
                <option value="locked">Locked</option>
                <option value="expired">Expired</option>
                <option value="over-quota">Over quota</option>
                <option value="payment-failed">Payment failed</option>
                <option value="issues">Issues</option>
              </select>
            </label>
            <label>
              <span>Sync</span>
              <select class="sdn-input sdn-select" bind:value={catalogSyncFilter}>
                <option value="all">All sync</option>
                <option value="idle">Ready</option>
                <option value="queued">Queued</option>
                <option value="syncing">Syncing</option>
                <option value="synced">Synced</option>
                <option value="stale">Stale</option>
                <option value="failed">Failed</option>
                <option value="capped">Capped</option>
                <option value="issues">Issues</option>
              </select>
            </label>
            <label>
              <span>Storage</span>
              <select class="sdn-input sdn-select" bind:value={catalogStorageFilter}>
                <option value="all">All storage</option>
                <option value="stored">Stored locally</option>
                <option value="not-stored">Not stored</option>
              </select>
            </label>
            <span class="sdn-catalog-filter-count">{formatNumber(filteredDataCatalogRows.length)} / {formatNumber(dataCatalogRows.length)}</span>
          </div>

          <div class="sdn-table-wrap sdn-workbench-table-wrap sdn-catalog-table-wrap">
            <table class="sdn-table sdn-workbench-table sdn-catalog-table" aria-label="Store data products">
              <thead>
                <tr>
                  <th>Provider</th>
                  <th>Product</th>
                  <th>Types</th>
                  <th>Access</th>
                  <th>Storage</th>
                  <th>Sync</th>
                  <th>Renewal</th>
                </tr>
              </thead>
              <tbody>
                {#each filteredDataCatalogRows as row (catalogRowKey(row))}
                  <tr
                    class="sdn-catalog-row"
                    class:active={isCatalogRowSelected(row) || expandedCatalogActionRowKey === catalogRowKey(row)}
                    class:sdn-catalog-expanded={expandedCatalogActionRowKey === catalogRowKey(row)}
                    data-catalog-row-key={catalogRowKey(row)}
                    role="button"
                    tabindex="0"
                    aria-expanded={expandedCatalogActionRowKey === catalogRowKey(row)}
                    on:keydown={(event) => handleCatalogRowKeydown(row, event)}
                  >
                    <td>
                      <button class="sdn-catalog-cell-trigger" type="button" on:click={(event) => handleCatalogCellButtonClick(row, event)}>
                        <span class="sdn-cell-stack">
                          <strong>{row.provider}</strong>
                          <span>{shorten(row.providerPeerId ?? row.providerPublicKey ?? '', 34)}</span>
                        </span>
                      </button>
                    </td>
                    <td><button class="sdn-catalog-cell-trigger" type="button" on:click={(event) => handleCatalogCellButtonClick(row, event)}>{row.product}</button></td>
                    <td><button class="sdn-catalog-cell-trigger" type="button" on:click={(event) => handleCatalogCellButtonClick(row, event)}>{row.messageTypes.join(', ')}</button></td>
                    <td><button class="sdn-catalog-cell-trigger" type="button" on:click={(event) => handleCatalogCellButtonClick(row, event)}>{row.access.label}</button></td>
                    <td>
                      <button class="sdn-catalog-cell-trigger" type="button" on:click={(event) => handleCatalogCellButtonClick(row, event)}>
                        <span class="sdn-cell-stack">
                          <strong>{catalogRowStorageLabel(row)}</strong>
                          <span>{row.storage.policyLabel}</span>
                        </span>
                      </button>
                    </td>
                    <td class="sdn-sync-cell">
                      <button class="sdn-catalog-cell-trigger sdn-catalog-sync-trigger" type="button" on:click={(event) => handleCatalogCellButtonClick(row, event)}>
                        <span
                          class="sdn-sync-bubble"
                          data-tone={catalogRowSyncBubbleTone(row)}
                          data-tooltip={catalogRowSyncBubbleTooltip(row)}
                          title={catalogRowSyncBubbleTooltip(row)}
                          aria-label={catalogRowSyncBubbleTooltip(row)}
                        >{catalogRowSyncBubbleLetter(row)}</span>
                      </button>
                    </td>
                    <td>
                      <button class="sdn-catalog-cell-trigger" type="button" on:click={(event) => handleCatalogCellButtonClick(row, event)}>
                        <span class="sdn-cell-stack">
                          <strong>{row.plan.renewalLabel}</strong>
                          <span>{row.plan.priceLabel}</span>
                        </span>
                      </button>
                    </td>
                  </tr>
	                  {#if expandedCatalogActionRowKey === catalogRowKey(row)}
	                    <tr class="sdn-catalog-action-row" data-catalog-row-key={catalogRowKey(row)}>
	                      <td colspan="7">
	                        <div class="sdn-catalog-action-panel">
	                          <div class="sdn-catalog-product-summary">
	                            <div class="sdn-cell-stack">
	                              <strong>{row.product}</strong>
	                              <span>{row.provider} · {catalogRowSourceCountLabel(row)} · {catalogRowUpdateCadenceLabel(row)}</span>
	                            </div>
	                            <div class="sdn-catalog-row-actions">
	                              <button class="sdn-button sdn-button-compact" type="button" on:click={() => handleCatalogPrimaryAction(row)}>{catalogRowPrimaryActionLabel(row)}</button>
	                              <button class="sdn-button sdn-button-muted sdn-button-compact" type="button" on:click={() => selectCatalogRow(row, 'subscriptions')}>Manage</button>
	                              {#if catalogRowRawDataAvailable(row)}
	                                <button class="sdn-button sdn-button-muted sdn-button-compact" type="button" on:click={() => selectCatalogRow(row, 'explorer')}>Open Explorer</button>
	                              {/if}
	                            </div>
	                          </div>
	                          <div class="sdn-catalog-detail-grid" aria-label={`${row.product} catalog details`}>
	                            <div>
	                              <span>Provider</span>
	                              <strong>{row.provider}</strong>
	                              <em>{catalogRowProviderIdentityLabel(row)}</em>
	                            </div>
	                            <div>
	                              <span>Public key</span>
	                              <strong>{row.providerPublicKey ? shorten(row.providerPublicKey, 42) : 'Unavailable'}</strong>
	                              <em>{row.providerPeerId ? shorten(row.providerPeerId, 42) : 'Peer ID unavailable'}</em>
	                            </div>
	                            <div>
	                              <span>Message types</span>
	                              <strong>{row.messageTypes.join(', ')}</strong>
	                              <em>{catalogRowSourceCountLabel(row)}</em>
	                            </div>
	                            <div>
	                              <span>Access</span>
	                              <strong>{row.access.label}</strong>
	                              <em>{row.plan.priceLabel}</em>
	                            </div>
	                            <div>
	                              <span>Trust</span>
	                              <strong>{catalogRowTrustLabel(row)}</strong>
	                              <em>{catalogRowVerificationLabel(row)}</em>
	                            </div>
	                            <div>
	                              <span>Storage estimate</span>
	                              <strong>{catalogRowStorageEstimateLabel(row)}</strong>
	                              <em>{catalogRowCountsLabel(row)}</em>
	                            </div>
	                            <div>
	                              <span>Renewal</span>
	                              <strong>{row.plan.renewalLabel}</strong>
	                              <em>{row.storage.filterLabel}</em>
	                            </div>
	                            <div>
	                              <span>Sync</span>
	                              <strong>{catalogRowSyncLabel(row)}</strong>
	                              <em>{catalogRowUpdateCadenceLabel(row)}</em>
	                            </div>
	                            <div>
	                              <span>Policy</span>
	                              <strong>{catalogRowRestrictionLabel(row)}</strong>
	                              <em>{row.plan.label}</em>
	                            </div>
	                          </div>
	                        </div>
	                      </td>
	                    </tr>
	                  {/if}
                {:else}
                  <tr>
                    <td colspan="7">{dataPageLoading ? 'Loading' : 'No matching data products.'}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        </section>
      {/if}


      {#if selectedDataSection === 'subscriptions'}
        <section class="sdn-storage-state" aria-label="Sync settings">
          <div class="sdn-dataset-summary" aria-label="Subscription storage summary">
            <div class="sdn-dataset-metric" aria-label={`Remote rows ${storageRemoteRowsMetric}`}>
              <span>Remote rows</span>
              <strong>{storageRemoteRowsMetric}</strong>
            </div>
            <div class="sdn-dataset-metric" aria-label={`Local rows ${storageLocalRowsMetric}`}>
              <span>Local rows</span>
              <strong>{storageLocalRowsMetric}</strong>
            </div>
            <div class="sdn-dataset-metric" aria-label={`Cached ${storageCachedMetric}`}>
              <span>Cached</span>
              <strong>{storageCachedMetric}</strong>
            </div>
            <div class="sdn-dataset-metric" aria-label={`Pinned rows ${storagePinnedRowsMetric}`}>
              <span>Pinned rows</span>
              <strong>{storagePinnedRowsMetric}</strong>
            </div>
          </div>

          <div class="sdn-subscription-filter-bar" aria-label="Subscription filters">
            <label class="sdn-catalog-search">
              <span>Search</span>
              <input class="sdn-input" bind:value={subscriptionSearchText} placeholder="Provider, product, type, status" aria-label="Search subscriptions" />
            </label>
            {#each SUBSCRIPTION_FILTERS as filter}
              <button
                class="sdn-button sdn-button-muted sdn-button-compact"
                class:active={subscriptionFilter === filter.id}
                type="button"
                on:click={() => { subscriptionFilter = filter.id; }}
              >{filter.label}</button>
            {/each}
            <span>{formatNumber(filteredSubscriptionRows.length)} / {formatNumber(schemaSyncRows.length)}</span>
          </div>

          <div class="sdn-storage-grid">
            {#each filteredSubscriptionRows as schema (schema.subscriptionId)}
              <article class="sdn-storage-row sdn-subscription-row" class:active={isSchemaRowSelected(schema)}>
                <div>
                  <strong>{subscriptionProductLabel(schema)}</strong>
                  <span>{schema.providerName}</span>
                  <span>{schemaRowsCountLabel(schema)}</span>
                </div>
                <div>
                  <strong>{subscriptionAccessLabel(schema)}</strong>
                  <span>{subscriptionPlanLabel(schema)}</span>
                  <span>{subscriptionCostLabel(schema)}</span>
                </div>
                <div>
                  <strong>{subscriptionStorageStateLabel(schema)}</strong>
                  <span>Local {schemaLocalRowsLabel(schema)} / remote {schemaRemoteRowsLabel(schema)}</span>
                  <span>{schemaPinnedRowsLabel(schema)}</span>
                  <span>{schemaCachedBytesLabel(schema)}</span>
                  <span>{schemaStoragePressureLabel(schema)}</span>
                  <span>{schemaRetentionPolicyLabel(schema)}</span>
                  <span>{schema.preference.storageCap} {schema.preference.storageUnit} cap</span>
                </div>
                <div>
                  <span
                    class="sdn-sync-bubble"
                    data-tone={syncBubbleTone(schema)}
                    data-tooltip={syncBubbleTooltip(schema)}
                    title={syncBubbleTooltip(schema)}
                    aria-label={syncBubbleTooltip(schema)}
                  >{syncBubbleLetter(schema)}</span>
                  <span>{schemaProgressLabel(schema)}</span>
                  <span>{schemaDownloadSpeedLabel(schema)}</span>
                  <span>{schemaHealthLabel(schema)}</span>
                  <span>Next {nextSyncAttemptLabel(schema)}</span>
                  <span>Last {schemaLastSyncedLabel(schema)}</span>
                  {#if schema.progress.error}
                    <span class="sdn-sync-error" title={schema.progress.error}>{shorten(schema.progress.error, 120)}</span>
                  {/if}
                </div>
                <label>
                  <span>Storage cap</span>
                  <div class="sdn-storage-cap-controls">
                    <input
                      class="sdn-input"
                      type="number"
                      min="0.1"
                      step="0.1"
                      aria-label={`${schema.id} storage cap`}
                      value={schema.preference.storageCap}
                      on:input={(event) => handleSubscriptionStorageCapInput(schema, event)}
                    />
                    <select
                      class="sdn-input sdn-select"
                      aria-label={`${schema.id} storage unit`}
                      value={schema.preference.storageUnit}
                      on:change={(event) => handleSubscriptionStorageUnitChange(schema, event)}
                    >
                      {#each STORAGE_CAP_UNITS as unit}
                        <option value={unit}>{unit}</option>
                      {/each}
                    </select>
                  </div>
                </label>
                <label>
                  <span>Sync profile</span>
                  <select
                    class="sdn-input sdn-select"
                    aria-label={`${schema.id} sync profile`}
                    value={schema.queryProfile}
                    on:change={(event) => handleSubscriptionQueryProfileChange(schema, event)}
                  >
                    {#each DATA_QUERY_PROFILES as profile}
                      <option value={profile.id}>{profile.label}</option>
                    {/each}
                  </select>
                </label>
                <label>
                  <span>Retention</span>
                  <select
                    class="sdn-input sdn-select"
                    aria-label={`${schema.id} retention policy`}
                    value={schema.retentionPolicy}
                    on:change={(event) => handleSubscriptionRetentionPolicyChange(schema, event)}
                  >
                    {#each DATA_RETENTION_POLICIES as policy}
                      <option value={policy.id}>{policy.label}</option>
                    {/each}
                  </select>
                </label>
                <label>
                  <span>Sync filter</span>
                  <input
                    class="sdn-input sdn-sync-filter"
                    aria-label={`${schema.id} sync filter`}
                    value={schema.syncFilter}
                    placeholder="Sync filter"
                    on:input={(event) => handleSubscriptionFilterInput(schema, event)}
                  />
                </label>
                <div class="sdn-storage-row-actions sdn-subscription-actions">
                  <button class="sdn-button sdn-button-muted sdn-button-compact" type="button" aria-label={`${schema.id} retry sync`} on:click={() => retrySubscriptionSync(schema)} disabled={schemaRetryDisabled(schema)}>Retry</button>
                  {#if schema.preference.mode === 'sync'}
                    <button class="sdn-button sdn-button-muted sdn-button-compact" type="button" on:click={() => pauseSubscriptionSync(schema)}>Pause</button>
                  {:else}
                    <button class="sdn-button sdn-button-compact" type="button" on:click={() => resumeSubscriptionSync(schema)}>Resume</button>
                  {/if}
                  <button class="sdn-button sdn-button-muted sdn-button-compact" type="button" on:click={() => void verifyPinnedArtifacts(schema)} disabled={pinVerifyRunning}>Verify pins</button>
	                  {#if resetSubscriptionId === schema.subscriptionId}
	                    <div class="sdn-reset-confirm sdn-reset-row-confirm" role="group" aria-label={`${schema.id} row reset confirmation`}>
	                      <label>
	                        <span>Type RESET to clear this row.</span>
                        <input class="sdn-input" bind:value={resetConfirmText} autocomplete="off" />
                      </label>
                      <div class="sdn-toolbar">
                        <button class="sdn-button sdn-button-compact" type="button" on:click={() => void confirmResetSubscriptionData(schema)} disabled={resetRunning || resetConfirmText.trim() !== 'RESET'}>Clear</button>
                        <button class="sdn-button sdn-button-muted sdn-button-compact" type="button" on:click={cancelResetSubscriptionData} disabled={resetRunning}>Cancel</button>
                      </div>
	                    </div>
	                  {:else}
	                    <button class="sdn-button sdn-button-muted sdn-button-compact" type="button" on:click={() => beginResetSubscriptionData(schema.subscriptionId)} disabled={resetRunning}>Reset row</button>
	                  {/if}
                  <button class="sdn-button sdn-button-muted sdn-button-compact" type="button" on:click={() => openSubscriptionDetails(schema)}>Details</button>
                </div>
              </article>
            {:else}
              {#if dataPageLoading}
                <p class="sdn-loading-inline" role="status">Loading</p>
              {:else}
                <p class="sdn-empty-inline">No subscribed data feeds.</p>
              {/if}
            {/each}
          </div>
          {#if selectedSubscriptionDetailSchema}
            <aside class="sdn-subscription-detail-drawer" aria-label={`${selectedSubscriptionDetailSchema.id} subscription details`}>
              <div class="sdn-catalog-product-summary">
                <div class="sdn-cell-stack">
                  <strong>{subscriptionProductLabel(selectedSubscriptionDetailSchema)}</strong>
                  <span>{selectedSubscriptionDetailSchema.providerName} · {selectedSubscriptionDetailSchema.id}</span>
                </div>
                <div class="sdn-catalog-row-actions">
                  <button class="sdn-button sdn-button-muted sdn-button-compact" type="button" on:click={() => openSchemaInExplorer(selectedSubscriptionDetailSchema)}>Open Explorer</button>
                  <button class="sdn-button sdn-button-muted sdn-button-compact" type="button" on:click={closeSubscriptionDetails}>Close</button>
                </div>
              </div>
              <div class="sdn-subscription-detail-grid">
                <div>
                  <span>Access</span>
                  <strong>{subscriptionAccessLabel(selectedSubscriptionDetailSchema)}</strong>
                  <em>{subscriptionPlanLabel(selectedSubscriptionDetailSchema)} · {subscriptionCostLabel(selectedSubscriptionDetailSchema)}</em>
                </div>
                <div>
                  <span>Storage</span>
                  <strong>{schemaCachedBytesLabel(selectedSubscriptionDetailSchema)}</strong>
                  <em>{schemaRowsCountLabel(selectedSubscriptionDetailSchema)}</em>
                </div>
                <div>
                  <span>Pinning</span>
                  <strong>{schemaPinnedRowsLabel(selectedSubscriptionDetailSchema)}</strong>
                  <em>{selectedSubscriptionDetailSchema.preference.storageCap} {selectedSubscriptionDetailSchema.preference.storageUnit} cap</em>
                </div>
                <div>
                  <span>Sync</span>
                  <strong>{syncStatusLabel(selectedSubscriptionDetailSchema)}</strong>
                  <em>{subscriptionSyncPolicyLabel(selectedSubscriptionDetailSchema)}</em>
                </div>
                <div>
                  <span>Freshness</span>
                  <strong>{schemaLastSyncedLabel(selectedSubscriptionDetailSchema)}</strong>
                  <em>{nextSyncAttemptLabel(selectedSubscriptionDetailSchema)}</em>
                </div>
                <div>
                  <span>Health</span>
                  <strong>{schemaHealthLabel(selectedSubscriptionDetailSchema)}</strong>
                  <em>{subscriptionRenewalLabel(selectedSubscriptionDetailSchema)}</em>
                </div>
              </div>
            </aside>
          {/if}
          {#if resetStatus}
            <p class="sdn-empty-inline" role="status">{resetStatus}</p>
          {/if}
        </section>
      {/if}

      {#if selectedDataSection === 'explorer'}
        <section class="sdn-explorer-panel" aria-label="Data explorer">
          <div class="sdn-workbench-controls sdn-explorer-controls">
            <label>
              <span>Source</span>
              <select class="sdn-input sdn-select" bind:value={selectedExplorerSourceKey} on:change={handleExplorerSourceChange}>
                {#each subscribedSourceOptions as source}
                  <option value={source.id}>{source.label}</option>
                {/each}
              </select>
            </label>

            <label>
              <span>Data type</span>
              <select class="sdn-input sdn-select" bind:value={selectedStandardId} on:change={handleExplorerStandardChange}>
                {#each subscribedStandardOptions as standard}
                  <option value={standard.id}>{standardOptionLabel(standard)}</option>
                {/each}
              </select>
            </label>

            <div class="sdn-search-mode" aria-label="Search mode">
              <button
                class="sdn-button sdn-button-muted sdn-button-compact"
                class:active={explorerSearchMode === 'plain'}
                type="button"
                aria-pressed={explorerSearchMode === 'plain'}
                on:click={() => handleExplorerSearchModeChange('plain')}
              >Plaintext</button>
              <button
                class="sdn-button sdn-button-muted sdn-button-compact"
                class:active={explorerSearchMode === 'sql'}
                type="button"
                aria-pressed={explorerSearchMode === 'sql'}
                on:click={() => handleExplorerSearchModeChange('sql')}
              >SQL</button>
            </div>

            <label class="sdn-master-search" class:sdn-master-search-sql={explorerSearchMode === 'sql'}>
              <span>Master search</span>
              {#if explorerSearchMode === 'sql'}
                <textarea
                  class="sdn-input sdn-sql-input"
                  value={explorerSearchText}
                  rows="2"
                  spellcheck="false"
                  placeholder={defaultSqlQuery(selectedStandardId)}
                  on:input={handleExplorerSearchInput}
                  on:keydown={handleExplorerSearchKeydown}
                ></textarea>
              {:else}
                <input
                  class="sdn-input"
                  value={explorerSearchText}
                  placeholder={`Search ${selectedStandardId}`}
                  on:input={handleExplorerSearchInput}
                />
              {/if}
            </label>

	            {#if explorerSearchMode === 'sql'}
	              <button class="sdn-button sdn-button-muted" type="button" on:click={() => void handleExplorerSearchSubmit()} disabled={sqlRunning}>{sqlRunning ? 'Running' : 'Run'}</button>
	            {/if}
	          </div>

	          <div class="sdn-saved-view-controls" aria-label="Saved Explorer views">
	            <label>
	              <span>Saved views</span>
	              <select
	                class="sdn-input sdn-select"
	                bind:value={selectedSavedExplorerViewId}
	                on:change={handleSavedExplorerViewSelect}
	              >
	                <option value="">Saved views</option>
	                {#each savedExplorerViews as view (view.id)}
	                  <option value={view.id}>{savedExplorerViewOptionLabel(view)}</option>
	                {/each}
	              </select>
	            </label>
	            <label>
	              <span>Name</span>
	              <input
	                class="sdn-input"
	                bind:value={savedExplorerViewName}
	                placeholder="View name"
	              />
	            </label>
	            <button class="sdn-button sdn-button-muted" type="button" on:click={saveCurrentExplorerView}>Save view</button>
	            <button class="sdn-button sdn-button-muted" type="button" on:click={() => applySavedExplorerView()} disabled={!selectedSavedExplorerViewId}>Apply view</button>
	            <button class="sdn-button sdn-button-muted" type="button" on:click={deleteSelectedExplorerView} disabled={!selectedSavedExplorerViewId}>Delete view</button>
	          </div>

	          {#if explorerColumnKeyEntries.length > 0}
	            <div class="sdn-column-key" aria-label="Column abbreviation key">
              <strong>Key</strong>
              {#each explorerColumnKeyEntries as entry}
                <span title={entry.key}>
                  <b>{entry.abbreviation}</b>
                  <em>{entry.label}</em>
                </span>
              {/each}
            </div>
          {/if}

          <div class="sdn-table-wrap sdn-workbench-table-wrap">
            <table class="sdn-table sdn-workbench-table" aria-label="Data rows">
              {#if explorerSearchMode === 'sql' && sqlResult}
                <thead>
                  <tr>
                    {#each displaySqlColumns as column}
                      <th aria-sort={sortAria(column)}>
                        <div class="sdn-column-header-control">
                          <button class="sdn-sort-button" type="button" title={columnHeaderKeyLabel(column)} on:click={() => setSort(column)}>
                            {sortableHeader(column, columnHeaderAbbreviation(column))}
                          </button>
                        </div>
                      </th>
                    {/each}
                  </tr>
                  <tr class="sdn-column-filter-row">
                    {#each displaySqlColumns as column}
                      <th>
                        <input
                          class="sdn-input sdn-column-filter"
                          value={columnFilterValue(column)}
                          placeholder={columnFilterPlaceholder(column)}
                          aria-label={`Filter ${columnHeaderKeyLabel(column)}`}
                          on:input={(event) => handleColumnFilterInput(column, event)}
                        />
                      </th>
                    {/each}
                  </tr>
                </thead>
                <tbody>
                  {#each visibleSqlRecords as row}
                    <tr>
                      {#each displaySqlColumns as column}
                        <td title={sqlCellValue(row, column)}>{displaySqlCellValue(row, column)}</td>
                      {/each}
                    </tr>
                  {:else}
                    <tr>
                      <td colspan={Math.max(1, displaySqlColumns.length)}>No rows loaded for {selectedStandardId}.</td>
                    </tr>
                  {/each}
                </tbody>
              {:else if localExplorerResult}
                <thead>
                  <tr>
                    {#each localExplorerColumns as column}
                      <th aria-sort={sortAria(column)}>
                        <div class="sdn-column-header-control">
                          <button class="sdn-sort-button" type="button" title={columnHeaderKeyLabel(column)} on:click={() => setSort(column)}>
                            {sortableHeader(column, columnHeaderAbbreviation(column))}
                          </button>
                        </div>
                      </th>
                    {/each}
                  </tr>
                  <tr class="sdn-column-filter-row">
                    {#each localExplorerColumns as column}
                      <th>
                        <input
                          class="sdn-input sdn-column-filter"
                          value={columnFilterValue(column)}
                          placeholder={columnFilterPlaceholder(column)}
                          aria-label={`Filter ${columnHeaderKeyLabel(column)}`}
                          on:input={(event) => handleColumnFilterInput(column, event)}
                        />
                      </th>
                    {/each}
                  </tr>
                </thead>
                <tbody>
                  {#each visibleLocalExplorerRecords as row}
                    <tr
                      class:sdn-clickable-row={selectedStandardId === 'PNM'}
                      class:active={selectedPnmRow?.record.cid === stringifyCellValue(row.CID ?? row.cid ?? row.FILE_ID ?? row.fileId ?? '')}
                      role={selectedStandardId === 'PNM' ? 'button' : undefined}
                      tabindex={selectedStandardId === 'PNM' ? 0 : undefined}
                      on:click={() => handleLocalExplorerRowClick(row)}
                      on:keydown={(event) => handleLocalExplorerRowKeydown(row, event)}
                    >
                      {#each localExplorerColumns as column}
                        <td title={sqlCellValue(row, column)}>{displaySqlCellValue(row, column)}</td>
                      {/each}
                    </tr>
                  {:else}
                    <tr>
                      <td colspan={Math.max(1, localExplorerColumns.length)}>{localExplorerLoading ? 'Loading' : `No rows loaded for ${selectedStandardId}.`}</td>
                    </tr>
                  {/each}
                </tbody>
              {:else}
                <thead>
                  <tr>
                    {#each visibleColumns as column}
                      <th aria-sort={sortAria(column.key)}>
                        <div class="sdn-column-header-control">
                          <button class="sdn-sort-button" type="button" title={columnHeaderKeyLabel(column.key, column.label)} on:click={() => setSort(column.key)}>
                            {sortableHeader(column.key, columnHeaderAbbreviation(column.key, column.label))}
                          </button>
                        </div>
                      </th>
                    {/each}
                  </tr>
                  <tr class="sdn-column-filter-row">
                    {#each visibleColumns as column}
                      <th>
                        <input
                          class="sdn-input sdn-column-filter"
                          value={columnFilterValue(column.key)}
                          placeholder={columnFilterPlaceholder(column.key)}
                          aria-label={`Filter ${columnHeaderKeyLabel(column.key, column.label)}`}
                          on:input={(event) => handleColumnFilterInput(column.key, event)}
                        />
                      </th>
                    {/each}
                  </tr>
                </thead>
                <tbody>
                  {#each visibleRows as row}
                    <tr
                      class:sdn-clickable-row={selectedStandardId === 'PNM'}
                      class:active={selectedPnmRow?.record.cid === row.record.cid}
                      role={selectedStandardId === 'PNM' ? 'button' : undefined}
                      tabindex={selectedStandardId === 'PNM' ? 0 : undefined}
                      on:click={() => handleWorkbenchRowClick(row)}
                      on:keydown={(event) => handleWorkbenchRowKeydown(row, event)}
                    >
                      {#each visibleColumns as column}
                        <td title={fullCellValue(row, column)}>{displayCellValue(row, column)}</td>
                      {/each}
                    </tr>
                  {:else}
                    <tr>
                      <td colspan={Math.max(1, visibleColumns.length)}>No rows loaded for {selectedStandardId}.</td>
                    </tr>
                  {/each}
                </tbody>
              {/if}
            </table>
          </div>

          <div class="sdn-pagination">
            <button class="sdn-button sdn-button-muted" type="button" on:click={goToPreviousPage} disabled={!canGoPrevious}>Previous</button>
            <span class="sdn-page-count">{pageLabel}</span>
            <button class="sdn-button sdn-button-muted" type="button" on:click={goToNextPage} disabled={!canGoNext}>Next</button>
          </div>

          {#if selectedStandardId === 'PNM' && selectedPnmRow}
            <section class="sdn-pnm-detail" aria-label="PNM detail">
              <div class="sdn-pnm-detail-head">
                <div>
                  <strong>PNM publication</strong>
                  <span>{pnmValue(selectedPnmDetails, 'FILE_ID')}</span>
                </div>
                <button class="sdn-button sdn-button-muted" type="button" on:click={() => void verifySelectedPnmSignature()} disabled={pnmSignatureRunning}>Verify signature</button>
              </div>

              <div class="sdn-pnm-fields">
                {#each PNM_STANDARD_COLUMNS as column}
                  {#if hasDisplayValue(selectedPnmDetails[column.key])}
                    <div>
                      <span>{column.label}</span>
                      <code>{pnmValue(selectedPnmDetails, column.key)}</code>
                    </div>
                  {/if}
                {/each}
              </div>

              <label class="sdn-pnm-file-query">
                <span>FILE_ID</span>
                <div>
                  <input class="sdn-input" bind:value={pnmFileIdQuery} />
                  <button class="sdn-button sdn-button-muted" type="button" on:click={() => void runPnmFileIdQuery()}>Find</button>
                </div>
              </label>

              <div class="sdn-pnm-signature-payload">
                <span>Reconstituted signature payload</span>
                <code>{pnmSignaturePayload(selectedPnmDetails)}</code>
              </div>

              {#if pnmSignatureStatus}
                <p class="sdn-empty-inline" role="status">{pnmSignatureStatus}</p>
              {/if}

              {#if pnmQueryError}
                <p class="sdn-empty-inline" role="alert">{pnmQueryError}</p>
              {:else if pnmQueryResult}
                <div class="sdn-table-wrap sdn-pnm-query-table-wrap">
                  <table class="sdn-table sdn-pnm-query-table" aria-label="PNM FILE_ID results">
                    <thead>
                      <tr>
                        {#each pnmQueryColumns as column}
                          <th>{sqlColumnHeader(column)}</th>
                        {/each}
                      </tr>
                    </thead>
                    <tbody>
                      {#each pnmQueryRows as row}
                        <tr>
                          {#each pnmQueryColumns as column}
                            <td title={sqlCellValue(row, column)}>{displaySqlCellValue(row, column)}</td>
                          {/each}
                        </tr>
                      {:else}
                        <tr>
                          <td colspan={Math.max(1, pnmQueryColumns.length)}>No PNMs found for this FILE_ID.</td>
                        </tr>
                      {/each}
                    </tbody>
                  </table>
                </div>
              {/if}
            </section>
          {/if}

          {#if sqlError}
            <p class="sdn-empty-inline" role="alert">{sqlError}</p>
          {/if}
          {#if localExplorerError}
            <p class="sdn-empty-inline" role="alert">{localExplorerError}</p>
          {/if}
        </section>
      {/if}
  </div>
</article>

{#if pinVerifyToast}
  <div class="sdn-toast-region" role="region" aria-label="Verification notifications">
    <div class="sdn-toast" data-tone={pinVerifyToast.tone} role={pinVerifyToast.tone === 'error' ? 'alert' : 'status'}>
      <strong>{pinVerifyToast.tone === 'error' ? 'Verification failed' : 'Pin verification'}</strong>
      <span>{pinVerifyToast.message}</span>
      <button class="sdn-toast-dismiss" type="button" aria-label="Dismiss verification toast" on:click={dismissPinVerifyToast}>x</button>
    </div>
  </div>
{/if}

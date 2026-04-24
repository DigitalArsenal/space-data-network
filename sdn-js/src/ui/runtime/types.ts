export const OBSERVED_PEER_SOURCES = [
  'seed',
  'dht',
  'provider',
  'protocol',
  'identity',
] as const;

export type ObservedPeerSource = (typeof OBSERVED_PEER_SOURCES)[number];

export interface ObservedPeerObservation {
  peerId: string;
  source: ObservedPeerSource;
  observedAt?: number;
  detail?: string;
}

export interface ObservedPeerRecord {
  peerId: string;
  observedAt: number;
  detail?: string;
  sources: ObservedPeerSource[];
}

export type ListingStatus = 'public' | 'unlisted' | 'retired';

export interface CanonicalListing {
  pluginId: string;
  version: string;
  name?: string;
  description?: string;
  tagline?: string;
  publisherName?: string;
  publisherHandle?: string;
  publisherPeerId?: string;
  observedAt?: number;
  status?: ListingStatus;
  tags?: string[];
  standardsUsed?: string[];
}

export type AddressLookupChain = 'bitcoin' | 'ethereum' | 'solana' | (string & {});

export interface AddressLookupKey {
  chain: string;
  namespace: string;
  normalizedValue: string;
  discoveryCID: string;
}

export type DirectoryRecordKind = 'node' | 'user';

export interface DirectoryRecordBase {
  kind: DirectoryRecordKind;
  peer_id: string;
  dn?: string;
  legal_name?: string;
  bitcoin_address?: string;
  epm_cid?: string;
  source?: string;
  updated_at?: number;
}

export interface DirectoryNodeRecord extends DirectoryRecordBase {
  kind: 'node';
  peer_id: string;
}

export interface DirectoryUserRecord extends DirectoryRecordBase {
  kind: 'user';
  peer_id: string;
}

export interface DirectorySnapshot {
  query: string;
  nodes: DirectoryNodeRecord[];
  users: DirectoryUserRecord[];
}

export interface DirectoryImportRequest {
  kind?: DirectoryRecordKind;
  source?: string;
  epm_cid?: string;
  epm_json?: Record<string, unknown>;
  record?: Record<string, unknown>;
  vcard?: string;
}

export interface DirectoryImportResult {
  imported: number;
  nodes: DirectoryNodeRecord[];
  users: DirectoryUserRecord[];
}

export interface DirectoryAdapter {
  readonly mode: 'server' | 'helia';
  search(query: string): Promise<DirectorySnapshot>;
  importRecord(record: DirectoryImportRequest): Promise<DirectoryImportResult>;
}

export const APP_SECTIONS = [
  'network',
  'marketplace',
  'delivery',
  'identity',
] as const;

export type AppSectionId = (typeof APP_SECTIONS)[number];

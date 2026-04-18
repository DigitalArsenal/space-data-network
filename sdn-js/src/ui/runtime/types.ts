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
}

export type AddressLookupChain = 'bitcoin' | 'ethereum' | 'solana' | (string & {});

export interface AddressLookupKey {
  chain: string;
  namespace: string;
  normalizedValue: string;
  discoveryCID: string;
}

export const APP_SECTIONS = [
  'network',
  'marketplace',
  'delivery',
  'identity',
] as const;

export type AppSectionId = (typeof APP_SECTIONS)[number];

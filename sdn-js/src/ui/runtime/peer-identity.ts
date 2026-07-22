import type { HostedEpmRecord } from './identity';
import { createPublicEpmExport } from './identity-vcard';
import type { ObservedSdnPeer } from './sdn-backend';

const DISPLAY_NAME_FIELDS = ['dn', 'DN', 'display_name', 'displayName', 'legal_name', 'legalName', 'name'];
const EMAIL_FIELDS = ['email', 'EMAIL', 'email_address', 'emailAddress', 'mail'];
const PHONE_FIELDS = ['telephone', 'TELEPHONE', 'phone', 'tel'];
const PEER_ID_FIELDS = ['peer_id', 'peerId', 'PeerID', 'ipfs_peer_id', 'ipfsPeerId'];
const AGENT_VERSION_FIELDS = ['agent_version', 'agentVersion'];
const EPM_CID_FIELDS = ['epm_cid', 'epmCid', 'public_epm_cid', 'publicEpmCid'];
const PUBLIC_KEY_FIELDS = ['public_key', 'PUBLIC_KEY', 'publicKey'];
const SIGNING_PUBLIC_KEY_FIELDS = ['signing_public_key', 'signingPublicKey', 'signing_pubkey_hex', 'signingPubkeyHex'];
const ENCRYPTION_PUBLIC_KEY_FIELDS = ['encryption_public_key', 'encryptionPublicKey', 'encryption_pubkey_hex', 'encryptionPubkeyHex'];
export function peerDisplayName(peer: ObservedSdnPeer, hostedEpm?: HostedEpmRecord | null): string {
  const epm = peerEpmJson(peer, hostedEpm);
  return pickString(epm, DISPLAY_NAME_FIELDS) || stringValue(peer.name) || peer.id;
}

export function peerEmail(peer: ObservedSdnPeer, hostedEpm?: HostedEpmRecord | null): string {
  return pickString(peerEpmJson(peer, hostedEpm), EMAIL_FIELDS) || '';
}

export function peerPhone(peer: ObservedSdnPeer, hostedEpm?: HostedEpmRecord | null): string {
  return pickString(peerEpmJson(peer, hostedEpm), PHONE_FIELDS) || '';
}

export function shortPeerId(peerId: string | null | undefined): string {
  const value = stringValue(peerId) ?? '';
  if (value.length <= 13) return value;
  return `${value.slice(0, 5)}...${value.slice(-5)}`;
}

export function peerEpmCid(peer: ObservedSdnPeer, hostedEpm?: HostedEpmRecord | null): string | undefined {
  const metadata = metadataRecord(peer);
  return stringValue(hostedEpm?.epmCid)
    || pickString(hostedEpm?.epmJson, EPM_CID_FIELDS)
    || pickString(metadata, EPM_CID_FIELDS)
    || pickString(peer as unknown as Record<string, unknown>, EPM_CID_FIELDS);
}

export function peerEpmJson(peer: ObservedSdnPeer, hostedEpm?: HostedEpmRecord | null): Record<string, unknown> {
  const metadata = createPublicEpmExport(metadataRecord(peer));
  const hostedJson = createPublicEpmExport(hostedEpm?.epmJson ?? {});
  const epm: Record<string, unknown> = {
    ...metadata,
    ...hostedJson,
  };

  setStringIfPresent(epm, 'dn', pickString(hostedJson, DISPLAY_NAME_FIELDS) || pickString(metadata, DISPLAY_NAME_FIELDS) || stringValue(peer.name) || peer.id);
  setStringIfPresent(epm, 'peer_id', pickString(hostedJson, PEER_ID_FIELDS) || pickString(metadata, PEER_ID_FIELDS) || peer.id);
  setStringIfPresent(epm, 'agent_version', pickString(hostedJson, AGENT_VERSION_FIELDS) || pickString(metadata, AGENT_VERSION_FIELDS) || stringValue(peer.agentVersion));
  setStringIfPresent(epm, 'epm_cid', peerEpmCid(peer, hostedEpm));
  setStringIfPresent(epm, 'public_key', pickString(hostedJson, PUBLIC_KEY_FIELDS) || pickString(metadata, PUBLIC_KEY_FIELDS));
  setStringIfPresent(epm, 'signing_public_key', pickString(hostedJson, SIGNING_PUBLIC_KEY_FIELDS) || pickString(metadata, SIGNING_PUBLIC_KEY_FIELDS));
  setStringIfPresent(epm, 'encryption_public_key', pickString(hostedJson, ENCRYPTION_PUBLIC_KEY_FIELDS) || pickString(metadata, ENCRYPTION_PUBLIC_KEY_FIELDS));

  return epm;
}

export function peerHostedEpmRecord(peer: ObservedSdnPeer, hostedEpm?: HostedEpmRecord | null): HostedEpmRecord {
  const epmJson = peerEpmJson(peer, hostedEpm);
  const record: HostedEpmRecord = {
    id: hostedEpm?.id ?? peer.id,
    kind: hostedEpm?.kind ?? 'hosted',
    label: hostedEpm?.label ?? peerDisplayName(peer, hostedEpm),
    peerId: hostedEpm?.peerId || pickString(epmJson, PEER_ID_FIELDS) || peer.id,
    epmJson,
  };
  const epmCid = peerEpmCid(peer, hostedEpm);
  if (epmCid) record.epmCid = epmCid;
  if (hostedEpm?.source) record.source = hostedEpm.source;
  if (typeof hostedEpm?.updatedAt === 'number') record.updatedAt = hostedEpm.updatedAt;
  return record;
}

export function hostedEpmRecordFromDirectoryRecord(record: Record<string, unknown>): HostedEpmRecord | null {
  const epmJson = createPublicEpmExport(recordFromValue(record.epm_json ?? record.epmJson ?? record));
  const peerId = pickString(record, PEER_ID_FIELDS) || pickString(epmJson, PEER_ID_FIELDS);
  if (!peerId) return null;
  const epmCid = pickString(record, EPM_CID_FIELDS) || pickString(epmJson, EPM_CID_FIELDS);
  const label = pickString(record, DISPLAY_NAME_FIELDS) || pickString(epmJson, DISPLAY_NAME_FIELDS) || peerId;
  const mergedEpmJson = {
    ...epmJson,
    dn: label,
    peer_id: peerId,
    ...(epmCid ? { epm_cid: epmCid } : {}),
  };
  const hosted: HostedEpmRecord = {
    id: peerId,
    kind: 'hosted',
    label,
    peerId,
    epmJson: mergedEpmJson,
  };
  if (epmCid) hosted.epmCid = epmCid;
  const source = pickString(record, ['source']);
  if (source) hosted.source = source;
  const updatedAt = pickNumber(record, ['updated_at', 'updatedAt']);
  if (typeof updatedAt === 'number') hosted.updatedAt = updatedAt;
  return hosted;
}

function metadataRecord(peer: ObservedSdnPeer): Record<string, unknown> {
  return isRecord(peer.metadata) ? peer.metadata : {};
}

function recordFromValue(value: unknown): Record<string, unknown> {
  if (typeof value === 'string') {
    try {
      const parsed = JSON.parse(value);
      return isRecord(parsed) ? parsed : {};
    } catch {
      return {};
    }
  }
  return isRecord(value) ? value : {};
}

function pickString(record: Record<string, unknown> | undefined, keys: string[]): string | undefined {
  if (!record) return undefined;
  for (const key of keys) {
    const value = stringValue(record[key]);
    if (value) return value;
  }
  return undefined;
}

function pickNumber(record: Record<string, unknown>, keys: string[]): number | undefined {
  for (const key of keys) {
    const value = record[key];
    if (typeof value === 'number' && Number.isFinite(value)) return value;
  }
  return undefined;
}

function setStringIfPresent(record: Record<string, unknown>, key: string, value: string | undefined): void {
  if (value) record[key] = value;
}

function stringValue(value: unknown): string | undefined {
  if (typeof value === 'string') {
    const trimmed = value.trim();
    return trimmed ? trimmed : undefined;
  }
  if (typeof value === 'number' || typeof value === 'boolean') return String(value);
  return undefined;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

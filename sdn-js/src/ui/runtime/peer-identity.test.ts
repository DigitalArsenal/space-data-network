import { describe, expect, it } from 'vitest';
import { createVCardQrPayload } from './identity-vcard';
import {
  hostedEpmRecordFromDirectoryRecord,
  peerDisplayName,
  peerEmail,
  peerEpmCid,
  peerEpmJson,
  peerHostedEpmRecord,
  peerPhone,
} from './peer-identity';
import type { HostedEpmRecord } from './identity';
import type { ObservedSdnPeer } from './sdn-backend';

const PEER_ID = '16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45';

function observedPeer(metadata: Record<string, unknown>): ObservedSdnPeer {
  return {
    id: PEER_ID,
    name: 'SpaceAware.io',
    addrs: [],
    trustLevel: 'observed',
    agentVersion: 'space-data-network/0.5.0',
    metadata,
  };
}

describe('peer identity projection', () => {
  it('uses observed peer metadata for EPM display fields and vCard QR payloads', () => {
    const peer = observedPeer({
      dn: 'SpaceAware Directory',
      email: 'ops@spaceaware.io',
      telephone: '+1-555-0100',
      public_key: 'node-public-key',
      signing_public_key: 'signing-public-key',
      encryption_public_key: 'encryption-public-key',
      epm_cid: 'bafy-peer-epm',
    });

    expect(peerDisplayName(peer)).toBe('SpaceAware Directory');
    expect(peerEmail(peer)).toBe('ops@spaceaware.io');
    expect(peerPhone(peer)).toBe('+1-555-0100');
    expect(peerEpmCid(peer)).toBe('bafy-peer-epm');
    expect(peerEpmJson(peer)).toMatchObject({
      dn: 'SpaceAware Directory',
      email: 'ops@spaceaware.io',
      telephone: '+1-555-0100',
      peer_id: PEER_ID,
      public_key: 'node-public-key',
      signing_public_key: 'signing-public-key',
      encryption_public_key: 'encryption-public-key',
      epm_cid: 'bafy-peer-epm',
    });

    const payload = createVCardQrPayload(peerHostedEpmRecord(peer));
    const unfoldedPayload = payload.replace(/\r\n[ \t]/g, '');
    expect(payload).toContain('UID:16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45');
    expect(payload).toContain('X-SDN-EPM-CID:bafy-peer-epm');
    expect(payload).toContain('X-SDN-PUBLIC-KEY:node-public-key');
    expect(payload).toContain('X-SDN-SIGNING-PUBLIC-KEY:signing-public-key');
    expect(payload).toContain('X-SDN-ENCRYPTION-PUBLIC-KEY:encryption-public-key');
    expect(unfoldedPayload).toContain('EMAIL;type=INTERNET;type=signing:signing-public-key@signing.digitalarsenal.io');
    expect(unfoldedPayload).toContain('EMAIL;type=INTERNET;type=encryption:encryption-public-key@encryption.digitalarsenal.io');
  });

  it('lets hosted EPM values override metadata while preserving missing metadata keys', () => {
    const peer = observedPeer({
      dn: 'Metadata Name',
      public_key: 'metadata-public-key',
      signing_public_key: 'metadata-signing-key',
      encryption_public_key: 'metadata-encryption-key',
      epm_cid: 'bafy-metadata-epm',
    });
    const hosted: HostedEpmRecord = {
      id: 'hosted-record',
      kind: 'hosted',
      label: 'Hosted Name',
      peerId: PEER_ID,
      epmCid: 'bafy-hosted-epm',
      epmJson: {
        dn: 'Hosted Name',
        public_key: 'hosted-public-key',
      },
    };

    expect(peerDisplayName(peer, hosted)).toBe('Hosted Name');
    expect(peerEpmCid(peer, hosted)).toBe('bafy-hosted-epm');
    expect(peerEpmJson(peer, hosted)).toMatchObject({
      dn: 'Hosted Name',
      public_key: 'hosted-public-key',
      signing_public_key: 'metadata-signing-key',
      encryption_public_key: 'metadata-encryption-key',
      epm_cid: 'bafy-hosted-epm',
    });
  });

  it('normalizes public directory records into hosted EPM records for observed peers', () => {
    const record = hostedEpmRecordFromDirectoryRecord({
      kind: 'node',
      peer_id: PEER_ID,
      dn: 'Directory Node',
      epm_cid: 'bafy-directory-epm',
      source: 'sdn-advertisement-discovery',
      epm_json: JSON.stringify({
        entity_type: 'node',
        peer_id: PEER_ID,
        keys: [
          {
            address_type: 'ed25519',
            key_type: 'signing',
            public_key: 'directory-signing-key',
          },
        ],
      }),
      updated_at: 1779450456,
    });

    expect(record).toMatchObject({
      id: PEER_ID,
      kind: 'hosted',
      label: 'Directory Node',
      peerId: PEER_ID,
      epmCid: 'bafy-directory-epm',
      source: 'sdn-advertisement-discovery',
      updatedAt: 1779450456,
      epmJson: {
        dn: 'Directory Node',
        peer_id: PEER_ID,
        epm_cid: 'bafy-directory-epm',
        keys: [
          {
            address_type: 'ed25519',
            key_type: 'signing',
            public_key: 'directory-signing-key',
          },
        ],
      },
    });
  });
});

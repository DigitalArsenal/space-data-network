import { describe, expect, it } from 'vitest';
import { createVCardQrPayload } from './identity-vcard';
import {
  deriveHostedEpmRecordKeysFromXpub,
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
const HD_TEST_XPUB = 'xpub6BpyEDT14VWygfxLMawQKhGXLCVMhJK7voSnjD7VsYYzUfQb6vbTwNhDbXwsa5KraQQgfpDzTq45TfdXQzNiFRfGoFpgbd9KymJsauL4MuT';
const HD_TEST_SIGNING_PUBLIC_KEY = '0321fce2a66e6c1be09128b20e3f50374fa05ec1ceb84eaa78e69cf1cddc60a7a6';
const HD_TEST_ENCRYPTION_PUBLIC_KEY = '0301f6e5f01a7765617c817568db07e81dc1b86a87575f4702f347b5897f6b1d06';

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
    expect(payload).not.toContain('X-SDN-PUBLIC-KEY:node-public-key');
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

  it('publishes xpub and derives documented signing/encryption public keys for vCard QR payloads', async () => {
    const record: HostedEpmRecord = {
      id: PEER_ID,
      kind: 'hosted',
      label: 'Directory Node',
      peerId: PEER_ID,
      epmCid: 'bafy-directory-epm',
      epmJson: {
        dn: 'Directory Node',
        peer_id: PEER_ID,
        xpub: HD_TEST_XPUB,
        keys: [
          {
            key_type: 'signing',
            address_type: 'secp256k1',
            xpub: HD_TEST_XPUB,
          },
        ],
      },
    };

    const enriched = await deriveHostedEpmRecordKeysFromXpub(record);

    expect(enriched.epmJson).toMatchObject({
      xpub: HD_TEST_XPUB,
      signing_public_key: HD_TEST_SIGNING_PUBLIC_KEY,
      encryption_public_key: HD_TEST_ENCRYPTION_PUBLIC_KEY,
    });
    expect(enriched.epmJson.keys).toEqual(expect.arrayContaining([
      expect.objectContaining({
        key_type: 'signing',
        address_type: 'secp256k1',
        public_key: HD_TEST_SIGNING_PUBLIC_KEY,
        derivation_path: "m/44'/0'/0'/0/0",
        xpub: HD_TEST_XPUB,
      }),
      expect.objectContaining({
        key_type: 'encryption',
        address_type: 'secp256k1',
        public_key: HD_TEST_ENCRYPTION_PUBLIC_KEY,
        derivation_path: "m/44'/0'/0'/1/0",
        xpub: HD_TEST_XPUB,
      }),
    ]));

    const payload = createVCardQrPayload(enriched);
    const unfoldedPayload = payload.replace(/\r\n[ \t]/g, '');
    expect(unfoldedPayload).toContain(`X-SDN-XPUB:${HD_TEST_XPUB}`);
    expect(unfoldedPayload).toContain(`X-SDN-SIGNING-PUBLIC-KEY:${HD_TEST_SIGNING_PUBLIC_KEY}`);
    expect(unfoldedPayload).toContain(`X-SDN-ENCRYPTION-PUBLIC-KEY:${HD_TEST_ENCRYPTION_PUBLIC_KEY}`);
    expect(unfoldedPayload).toContain(`EMAIL;type=INTERNET;type=signing:${HD_TEST_SIGNING_PUBLIC_KEY}@signing.digitalarsenal.io`);
    expect(unfoldedPayload).toContain(`EMAIL;type=INTERNET;type=encryption:${HD_TEST_ENCRYPTION_PUBLIC_KEY}@encryption.digitalarsenal.io`);
  });

  it('derives signing and encryption keys from an xpub-only EPM', async () => {
    const enriched = await deriveHostedEpmRecordKeysFromXpub({
      id: PEER_ID,
      kind: 'hosted',
      label: 'Directory Node',
      peerId: PEER_ID,
      epmCid: 'bafy-directory-epm',
      epmJson: {
        dn: 'Directory Node',
        peer_id: PEER_ID,
        xpub: HD_TEST_XPUB,
      },
    });

    expect(enriched.epmJson.xpub).toBe(HD_TEST_XPUB);
    expect(enriched.epmJson.signing_public_key).toBe(HD_TEST_SIGNING_PUBLIC_KEY);
    expect(enriched.epmJson.encryption_public_key).toBe(HD_TEST_ENCRYPTION_PUBLIC_KEY);
  });
});

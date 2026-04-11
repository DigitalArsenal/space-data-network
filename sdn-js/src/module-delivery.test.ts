import { describe, expect, it, vi } from 'vitest';
import {
  MODULE_DELIVERY_PROTOCOL_ID,
  decodeModuleDeliveryMessage,
  encodeModuleDeliveryMessage,
} from '@spacedatanetwork/plugin-sdk';

vi.mock('./crypto/hd-wallet', async () => {
  const actual = await vi.importActual<typeof import('./crypto/hd-wallet')>('./crypto/hd-wallet');
  return {
    ...actual,
    derivePeerIdFromPublicKey: vi.fn(async () => 'provider-peer-id'),
    sign: vi.fn(async () => new Uint8Array([0xaa, 0xbb, 0xcc])),
  };
});

import { fetchEncryptedModuleBundle, requestModuleGrant } from './module-delivery';

describe('module-delivery', () => {
  it('performs the FlatBuffer challenge and proof exchange over the module delivery protocol', async () => {
    const transport = {
      calls: [] as Array<{
        targetPeerId: string;
        protocolId: string;
        payload: ReturnType<typeof decodeModuleDeliveryMessage>;
        candidateAddrs: string[];
      }>,
      async dialProtocol(
        targetPeerId: string,
        protocolId: string,
        payload: Uint8Array,
        candidateAddrs: string[] = [],
      ) {
        const decoded = decodeModuleDeliveryMessage(payload);
        this.calls.push({ targetPeerId, protocolId, payload: decoded, candidateAddrs });

        if (decoded.type === 'grant_request') {
          return encodeModuleDeliveryMessage({
            type: 'grant_challenge',
            payload: {
              reqId: decoded.payload.reqId,
              challenge: new Uint8Array([1, 2, 3, 4]),
              expiresAtMs: 1_700_000_900_000,
              providerPeerId: 'provider-peer-id',
              providerPublicKey: hexToBytes('02'.padEnd(66, '1')),
            },
          });
        }

        expect(decoded.type).toBe('grant_proof');
        return encodeModuleDeliveryMessage({
          type: 'grant_response',
          payload: {
            reqId: decoded.payload.reqId,
            entitlementStatus: 'active',
            capabilityToken: 'capability-token',
            expiresAtMs: 1_700_003_600_000,
            grantSignature: new Uint8Array([9, 9, 9]),
            bundleDescriptor: {
              cid: 'bafyencryptedmodule',
              contentHash: new Uint8Array(32).fill(7),
              sizeBytes: 4,
              moduleId: 'com.space-data-network.fastest-path',
              moduleVersion: '0.5.22',
              runtime: 'wasm32',
              abi: 'sdk-0.5.22',
              contentCodec: 'application/wasm+encrypted',
              encryptionCodec: 'xchacha20poly1305',
            },
            wrappedContentKey: {
              wrappingAlgorithm: 'x25519-xsalsa20poly1305',
              recipientKeyId: 'requester-encryption-key',
              recipientPublicKey: new Uint8Array(32).fill(8),
              ephemeralPublicKey: new Uint8Array(32).fill(9),
              nonce: new Uint8Array(24).fill(3),
              ciphertext: new Uint8Array([4, 5, 6]),
              tag: new Uint8Array([7, 8, 9]),
            },
          },
        });
      },
      async fetchCIDBytes() {
        return new Uint8Array([1, 2, 3, 4]);
      },
    };

    const result = await requestModuleGrant(transport, {
      serverDescriptor: {
        publicKey: '02'.padEnd(66, '1'),
        relayAddresses: ['/dns4/relay.example/tcp/443/wss/p2p/relay-peer'],
      },
      requesterIdentity: {
        peerId: 'requester-peer-id',
        xpub: 'xpub-requester',
        signingKey: {
          privateKey: new Uint8Array(32).fill(5),
          publicKey: new Uint8Array(32).fill(6),
        },
        encryptionKey: {
          privateKey: new Uint8Array(32).fill(7),
          publicKey: new Uint8Array(32).fill(8),
        },
      },
      moduleId: 'com.space-data-network.fastest-path',
      moduleVersion: '0.5.22',
      reqId: 'req-123',
      requestedAtMs: 1_700_000_000_000,
    });

    expect(transport.calls).toHaveLength(2);
    expect(transport.calls[0]).toMatchObject({
      targetPeerId: 'provider-peer-id',
      protocolId: MODULE_DELIVERY_PROTOCOL_ID,
      candidateAddrs: ['/dns4/relay.example/tcp/443/wss/p2p/relay-peer'],
      payload: {
        type: 'grant_request',
        payload: {
          reqId: 'req-123',
          requesterPeerId: 'requester-peer-id',
          requesterXpub: 'xpub-requester',
        },
      },
    });
    expect(transport.calls[1].payload).toMatchObject({
      type: 'grant_proof',
      payload: {
        reqId: 'req-123',
        signature: new Uint8Array([0xaa, 0xbb, 0xcc]),
      },
    });

    expect(result.grant.bundleDescriptor).toMatchObject({
      cid: 'bafyencryptedmodule',
      moduleId: 'com.space-data-network.fastest-path',
      moduleVersion: '0.5.22',
    });
    expect(result.provider.peerId).toBe('provider-peer-id');
  });

  it('fetches the encrypted bundle bytes by CID and verifies the declared content hash', async () => {
    const content = new Uint8Array([10, 20, 30, 40]);
    const contentHash = await sha256(content);
    const transport = {
      async fetchCIDBytes(cid: string) {
        expect(cid).toBe('bafyencryptedmodule');
        return content;
      },
    };

    const bundle = await fetchEncryptedModuleBundle(transport, {
      grant: {
        bundleDescriptor: {
          cid: 'bafyencryptedmodule',
          contentHash,
          sizeBytes: 4,
          moduleId: 'com.space-data-network.fastest-path',
        },
        wrappedContentKey: {
          wrappingAlgorithm: 'x25519',
          recipientPublicKey: new Uint8Array(32),
          ephemeralPublicKey: new Uint8Array(32),
          nonce: new Uint8Array(24),
          ciphertext: new Uint8Array(3),
          tag: new Uint8Array(3),
        },
      },
      provider: {
        peerId: 'provider-peer-id',
        publicKey: hexToBytes('02'.padEnd(66, '1')),
        publicKeyHex: '02'.padEnd(66, '1'),
        relayAddresses: [],
        source: 'descriptor',
      },
    });

    expect(bundle.encryptedBundleBytes).toEqual(content);
  });

  it('fails when the fetched bundle hash does not match the grant descriptor', async () => {
    await expect(
      fetchEncryptedModuleBundle(
        {
          async fetchCIDBytes() {
            return new Uint8Array([1, 2, 3, 4]);
          },
        },
        {
          grant: {
            bundleDescriptor: {
              cid: 'bafyencryptedmodule',
              contentHash: new Uint8Array(32).fill(9),
              sizeBytes: 4,
              moduleId: 'com.space-data-network.fastest-path',
            },
            wrappedContentKey: {
              wrappingAlgorithm: 'x25519',
              recipientPublicKey: new Uint8Array(32),
              ephemeralPublicKey: new Uint8Array(32),
              nonce: new Uint8Array(24),
              ciphertext: new Uint8Array(3),
              tag: new Uint8Array(3),
            },
          },
          provider: {
            peerId: 'provider-peer-id',
            publicKey: hexToBytes('02'.padEnd(66, '1')),
            publicKeyHex: '02'.padEnd(66, '1'),
            relayAddresses: [],
            source: 'descriptor',
          },
        },
      ),
    ).rejects.toThrow(/hash mismatch/i);
  });
});

function hexToBytes(hex: string): Uint8Array {
  const normalized = hex.trim().toLowerCase();
  const bytes = new Uint8Array(normalized.length / 2);
  for (let index = 0; index < bytes.length; index += 1) {
    bytes[index] = Number.parseInt(normalized.slice(index * 2, index * 2 + 2), 16);
  }
  return bytes;
}

async function sha256(value: Uint8Array): Promise<Uint8Array> {
  const digest = await globalThis.crypto.subtle.digest('SHA-256', value);
  return new Uint8Array(digest);
}

import { createHash } from 'node:crypto';
import { describe, expect, it, vi } from 'vitest';

const licensingMocks = vi.hoisted(() => ({
  encodeLicensingChallengeRequest: vi.fn(() => new Uint8Array([1])),
  encodeLicensingProof: vi.fn(() => new Uint8Array([2])),
  decodeLicensingChallengeMessage: vi.fn(() => ({
    messageType: 'response',
    role: 'provider',
    reqId: 'req-events',
    moduleId: 'com.space-data-network.fastest-path',
    moduleVersion: '0.5.22',
    requestedDomain: 'app.example.com',
    requestedTimeoutMs: 30_000,
    requestedAtMs: 1_700_000_000_000,
    challengeNonce: new Uint8Array([1, 2, 3, 4]),
    expiresAtMs: 1_700_000_900_000,
    providerPeerId: 'provider-peer-id',
    rawBytes: new Uint8Array([7, 8, 9]),
  })),
  decodeLicensingGrant: vi.fn(() => ({
    reqId: 'req-events',
    moduleId: 'com.space-data-network.fastest-path',
    moduleVersion: '0.5.22',
    requestedDomain: 'app.example.com',
    requestedTimeoutMs: 30_000,
    grantedDomain: 'app.example.com',
    grantedTimeoutMs: 30_000,
    expiresAtMs: 1_700_003_600_000,
    grantStatus: 'active',
    capabilityToken: new Uint8Array([1, 2, 3]),
    grantVerifierPublicKey: new Uint8Array(32).fill(5),
    providerSignature: new Uint8Array(64).fill(9),
  })),
  validateLicensingGrant: vi.fn((grant) => grant),
  extractGrantModuleDescriptor: vi.fn(() => ({
    cid: 'bafyencryptedmodule',
    contentHash: new Uint8Array(32).fill(7),
    sizeBytes: 4,
    moduleId: 'com.space-data-network.fastest-path',
    moduleVersion: '0.5.22',
    allowedDomains: ['app.example.com'],
    maxGrantTimeoutMs: 30_000,
    encrypted: true,
  })),
  extractWrappedContentKey: vi.fn(() => ({
    wrappingAlgorithm: 'x25519',
    keyBytes: new Uint8Array(32).fill(4),
    requesterEphemeralPublicKey: new Uint8Array(32),
    providerEphemeralPublicKey: new Uint8Array(32),
    hkdfSalt: new Uint8Array(0),
    iv: new Uint8Array(12),
    ciphertext: new Uint8Array(4),
    tag: new Uint8Array(16),
    expiresAtMs: 1_700_003_600_000,
    recipientPublicKey: new Uint8Array(32),
    ephemeralPublicKey: new Uint8Array(32),
    nonce: new Uint8Array(12),
    header: {
      version: 1,
      keyExchange: 'x25519',
      symmetric: 'aes-256-ctr',
      keyDerivation: 'hkdf-sha256',
      ephemeralPublicKey: new Uint8Array(32),
      nonceStart: new Uint8Array(12),
      recipientKeyId: new Uint8Array(0),
      schemaHash: new Uint8Array(0),
    },
    encryptedPayload: new Uint8Array(0),
    recipientKeyIdBytes: new Uint8Array(0),
    schemaHash: new Uint8Array(0),
  })),
}));

const cryptoMocks = vi.hoisted(() => ({
  derivePeerIdFromPublicKey: vi.fn(async () => 'provider-peer-id'),
  sign: vi.fn(async () => new Uint8Array([0xaa, 0xbb, 0xcc])),
  sha256: vi.fn(async (value: Uint8Array) => {
    return new Uint8Array(createHash('sha256').update(value).digest());
  }),
}));

vi.mock('space-data-module-sdk/licensing', () => ({
  LicensingProtocolError: class LicensingProtocolError extends Error {
    code?: string;

    constructor(code?: string, message?: string) {
      super(message);
      this.code = code;
    }
  },
  decodeLicensingChallengeMessage: licensingMocks.decodeLicensingChallengeMessage,
  decodeLicensingGrant: licensingMocks.decodeLicensingGrant,
  encodeLicensingChallengeRequest: licensingMocks.encodeLicensingChallengeRequest,
  encodeLicensingProof: licensingMocks.encodeLicensingProof,
  extractGrantModuleDescriptor: licensingMocks.extractGrantModuleDescriptor,
  extractWrappedContentKey: licensingMocks.extractWrappedContentKey,
  validateLicensingGrant: licensingMocks.validateLicensingGrant,
}));

vi.mock('./crypto/hd-wallet', () => ({
  derivePeerIdFromPublicKey: cryptoMocks.derivePeerIdFromPublicKey,
  sign: cryptoMocks.sign,
  sha256: cryptoMocks.sha256,
}));

import {
  fetchEncryptedModuleBundle,
  requestModuleGrant,
} from './module-delivery';

describe('module-delivery observers', () => {
  it('emits provider discovery, challenge/grant, and CID fetch lifecycle events', async () => {
    const content = new Uint8Array([10, 20, 30, 40]);
    const contentHash = createHash('sha256').update(content).digest();
    licensingMocks.extractGrantModuleDescriptor.mockReturnValueOnce({
      cid: 'bafyencryptedmodule',
      contentHash: new Uint8Array(contentHash),
      sizeBytes: 4,
      moduleId: 'com.space-data-network.fastest-path',
      moduleVersion: '0.5.22',
      allowedDomains: ['app.example.com'],
      maxGrantTimeoutMs: 30_000,
      encrypted: true,
    });

    const deliveryEvents: Array<{ stage: string; moduleId?: string; cid?: string }> = [];
    const transport = {
      async discoverProviders() {
        return [
          {
            peerId: 'provider-peer-id',
            multiaddrs: ['/dns4/discovered-relay.example/tcp/443/wss/p2p/discovered-relay-peer'],
          },
        ];
      },
      async dialProtocol(_targetPeerId: string, _protocolId: string, payload: Uint8Array) {
        return payload[0] === 1 ? new Uint8Array([10]) : new Uint8Array([20]);
      },
      async fetchCIDBytes() {
        return content;
      },
    };

    const grant = await requestModuleGrant(transport, {
      serverDescriptor: {
        publicKey: '02'.padEnd(66, '1'),
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
      requesterDomain: 'app.example.com',
      requestedTimeoutMs: 30_000,
      reqId: 'req-events',
      requestedAtMs: 1_700_000_000_000,
      observer: {
        onEvent(event) {
          deliveryEvents.push(event);
        },
      },
    });

    const bundle = await fetchEncryptedModuleBundle(transport, grant, {
      onEvent(event) {
        deliveryEvents.push(event);
      },
    });

    expect(bundle.encryptedBundleBytes).toEqual(content);
    expect(deliveryEvents.map((event) => event.stage)).toEqual([
      'provider-discovery',
      'challenge-sent',
      'challenge-received',
      'grant-received',
      'cid-fetch-start',
      'cid-fetch-complete',
      'cid-fetch-validated',
    ]);
    expect(deliveryEvents[0]).toMatchObject({
      moduleId: 'com.space-data-network.fastest-path',
      providerPeerId: 'provider-peer-id',
    });
    expect(deliveryEvents[4]).toMatchObject({
      cid: 'bafyencryptedmodule',
    });
  });
});

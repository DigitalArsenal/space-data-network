import { describe, expect, it, vi } from 'vitest';

vi.mock('./crypto/hd-wallet', () => ({
  ed25519PublicKey: vi.fn(async () => new Uint8Array([0x01, 0x02, 0x03, 0x04])),
  sign: vi.fn(async () => new Uint8Array([0xaa, 0xbb, 0xcc])),
  deriveSecp256k1Key: vi.fn(),
  derivePeerIdFromPublicKey: vi.fn(),
  derivePeerIdFromXpub: vi.fn(() => 'derived-peer-id'),
}));

describe('license.requestLicenseGrantViaRelay', () => {
  it('performs the challenge and proof exchange over the license protocol', async () => {
    const {
      LICENSE_PROTOCOL_ID,
      requestLicenseGrantViaRelay,
    } = await import('./license');

    const seenMessages: Array<Record<string, unknown>> = [];
    const transport = {
      async dialProtocolThroughRelay(
        relayAddr: string,
        targetPeerId: string,
        protocolId: string,
        payload: Uint8Array | string
      ) {
        expect(relayAddr).toBe('/dns4/relay.example/tcp/443/wss/p2p/relay-peer');
        expect(targetPeerId).toBe('license-peer-id');
        expect(protocolId).toBe(LICENSE_PROTOCOL_ID);

        const body = typeof payload === 'string' ? payload : new TextDecoder().decode(payload);
        const message = JSON.parse(body.trim()) as Record<string, unknown>;
        seenMessages.push(message);

        if (message.type === 'challenge_request') {
          expect(message).toMatchObject({
            req_id: 'req-123',
            xpub: 'xpub-license-client',
            peer_id: 'derived-peer-id',
            client_pubkey_hex: '01020304',
            ts: 1_700_000_000,
          });
          return new TextEncoder().encode(
            JSON.stringify({
              type: 'challenge_response',
              req_id: 'req-123',
              challenge: 'AQIDBA',
              expires_at: 1_700_000_900,
              server_peer_id: 'license-peer-id',
            }) + '\n'
          );
        }

        expect(message).toMatchObject({
          type: 'proof_request',
          req_id: 'req-123',
          xpub: 'xpub-license-client',
          peer_id: 'derived-peer-id',
          challenge: 'AQIDBA',
          signature_hex: 'aabbcc',
        });

        return new TextEncoder().encode(
          JSON.stringify({
            type: 'grant_response',
            req_id: 'req-123',
            entitlement: {
              xpub: 'xpub-license-client',
              plan: 'pro',
              status: 'active',
              updated_at: 1_700_000_001,
            },
            capability_token: 'token-123',
            expires_at: 1_700_003_600,
          }) + '\n'
        );
      },
    };

    const result = await requestLicenseGrantViaRelay(transport, {
      relayAddr: '/dns4/relay.example/tcp/443/wss/p2p/relay-peer',
      licensePeerId: 'license-peer-id',
      xpub: 'xpub-license-client',
      signingPrivateKey: new Uint8Array(32).fill(7),
      reqId: 'req-123',
      now: 1_700_000_000,
    });

    expect(seenMessages).toHaveLength(2);
    expect(result).toEqual({
      peerId: 'derived-peer-id',
      clientPublicKeyHex: '01020304',
      response: {
        type: 'grant_response',
        req_id: 'req-123',
        entitlement: {
          xpub: 'xpub-license-client',
          plan: 'pro',
          status: 'active',
          updated_at: 1_700_000_001,
        },
        capability_token: 'token-123',
        expires_at: 1_700_003_600,
      },
    });
  });
});

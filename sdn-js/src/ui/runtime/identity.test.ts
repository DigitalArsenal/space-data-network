import { describe, expect, it } from 'vitest';
import {
  createChunkedQrPayloads,
  createPublicEpmExport,
  createVCardQrPayload,
  normalizeHostedEpmRecord,
  reassembleChunkedQrPayloads,
} from './identity';

describe('normalizeHostedEpmRecord', () => {
  it('keeps node-owned identity separate from additional local EPMs', () => {
    const own = normalizeHostedEpmRecord({
      id: 'self',
      kind: 'node-self',
      epm_json: { dn: 'Local Node', peer_id: '12D3KooWNode' },
    });
    const hosted = normalizeHostedEpmRecord({
      id: 'alice',
      kind: 'hosted',
      epm_json: { dn: 'Alice', peer_id: '16Uiu2Alice' },
    });

    expect(own.kind).toBe('node-self');
    expect(hosted.kind).toBe('hosted');
    expect(own.peerId).toBe('12D3KooWNode');
    expect(hosted.peerId).toBe('16Uiu2Alice');
  });
});

describe('public EPM exports', () => {
  it('removes Core, mnemonic, and private key fields from public exports', () => {
    const exported = createPublicEpmExport({
      dn: 'Alice',
      peer_id: '16Uiu2Alice',
      public_key: 'abcdef',
      private_key: 'must-not-export',
      mnemonic: 'must not export',
      core: { seed: 'must-not-export' },
      keys: [{ PUBLIC_KEY: 'pub', PRIVATE_KEY: 'secret', XPRIV: 'xpriv' }],
    });

    expect(JSON.stringify(exported)).toContain('abcdef');
    expect(JSON.stringify(exported)).toContain('pub');
    expect(JSON.stringify(exported)).not.toContain('must-not-export');
    expect(JSON.stringify(exported)).not.toContain('mnemonic');
    expect(JSON.stringify(exported)).not.toContain('XPRIV');
  });

  it('removes nested camelCase and uppercase private wallet material from public exports', () => {
    const exported = createPublicEpmExport({
      DN: 'Alice',
      PUBLIC_KEYS: ['pub-1'],
      CORE: { encryptedCoreBytes: 'encrypted-core-bytes' },
      Seed: 'seed phrase',
      walletPrivateMaterial: {
        walletPrivateKey: 'wallet-private-key',
      },
      chainProofs: [
        {
          publicAddress: '0xpublic',
          SECRET: 'chain-secret',
          XPriv: 'xpriv-secret',
        },
      ],
      keys: [
        {
          PUBLIC_KEY: 'pub-2',
          privateKey: 'private-key',
          privateSeed: 'private-seed',
        },
      ],
    });

    const serialized = JSON.stringify(exported);
    expect(serialized).toContain('Alice');
    expect(serialized).toContain('pub-1');
    expect(serialized).toContain('pub-2');
    expect(serialized).toContain('0xpublic');
    expect(serialized).not.toMatch(/encrypted-core-bytes|seed phrase|wallet-private-key|chain-secret|xpriv-secret|private-key|private-seed/i);
    expect(serialized).not.toMatch(/CORE|Seed|walletPrivate|SECRET|XPriv|privateKey|privateSeed/);
  });

  it('creates vCard QR payloads with profile metadata and public identity aliases', () => {
    const payload = createVCardQrPayload({
      id: 'alice',
      kind: 'hosted',
      epm_json: {
        dn: 'Dr. Alice Q. Example',
        legal_name: 'Example Orbital LLC',
        given_name: 'Alice',
        family_name: 'Example',
        additional_name: 'Q.',
        honorific_prefix: 'Dr.',
        honorific_suffix: 'PhD',
        email: 'alice@example.com',
        telephone: '+1 555 0100',
        job_title: 'Flight Director',
        occupation: 'Operator',
        address: {
          po_box: 'Box 42',
          street: '1 Orbit Way',
          locality: 'Cape Canaveral',
          region: 'FL',
          postal_code: '32920',
          country: 'USA',
        },
        peer_id: '16Uiu2Alice',
        epm_cid: 'bafyepm',
        public_key: 'node-public',
        signing_public_key: 'signing-public',
        encryption_public_key: 'encryption-public',
        keys: [
          { key_type: 'signing', public_key: 'signing-from-keys' },
          { key_type: 'encryption', public_key: 'encryption-from-keys' },
        ],
        private_key: 'must-not-export',
      },
    });
    const unfolded = payload.replace(/\r\n[ \t]/g, '');

    expect(payload).toContain('BEGIN:VCARD');
    expect(payload).toContain('N:Example;Alice;Q.;Dr.;PhD');
    expect(payload).toContain('FN:Dr. Alice Q. Example');
    expect(payload).toContain('ORG:Example Orbital LLC');
    expect(payload).toContain('EMAIL;TYPE=INTERNET:alice@example.com');
    expect(payload).toContain('TEL:+1 555 0100');
    expect(payload).toContain('TITLE:Flight Director');
    expect(payload).toContain('ROLE:Operator');
    expect(payload).toContain('ADR;TYPE=WORK:Box 42;;1 Orbit Way;Cape Canaveral;FL;32920;USA');
    expect(payload).toContain('X-SDN-PEER-ID:16Uiu2Alice');
    expect(payload).toContain('X-SDN-EPM-CID:bafyepm');
    expect(payload).toContain('EMAIL;TYPE=INTERNET:node-public@spacedatanetwork.org');
    expect(payload).toContain('EMAIL;type=INTERNET;type=signing:signing-public@signing.digitalarsenal.io');
    expect(unfolded).toContain('EMAIL;type=INTERNET;type=encryption:encryption-public@encryption.digitalarsenal.io');
    expect(payload).toContain('X-SDN-PUBLIC-KEY:node-public');
    expect(payload).toContain('X-SDN-SIGNING-PUBLIC-KEY:signing-public');
    expect(payload).toContain('X-SDN-ENCRYPTION-PUBLIC-KEY:encryption-public');
    expect(payload).not.toContain('must-not-export');
  });

  it('falls back to typed key arrays for signing and encryption email aliases', () => {
    const payload = createVCardQrPayload({
      id: 'node',
      kind: 'node-self',
      epm_json: {
        dn: 'Local Node',
        peer_id: '12D3KooWNode',
        keys: [
          { key_type: 'signing', public_key: 'array-signing-key' },
          { address_type: 'x25519', public_key: 'array-encryption-key' },
        ],
      },
    });
    const unfolded = payload.replace(/\r\n[ \t]/g, '');

    expect(unfolded).toContain('EMAIL;type=INTERNET;type=signing:array-signing-key@signing.digitalarsenal.io');
    expect(unfolded).toContain('EMAIL;type=INTERNET;type=encryption:array-encryption-key@encryption.digitalarsenal.io');
  });

  it('imports signing and encryption public keys from typed vCard email aliases into EPM JSON', async () => {
    const vcardModule = await import('./identity-vcard');
    const fromVCard = (vcardModule as unknown as {
      epmJsonFromVCard?: (payload: string) => Record<string, unknown>;
    }).epmJsonFromVCard;
    expect(fromVCard).toBeTypeOf('function');

    const epm = fromVCard?.([
      'BEGIN:VCARD',
      'VERSION:3.0',
      'FN:Alice Example',
      'EMAIL;TYPE=INTERNET:alice@example.com',
      'EMAIL;type=INTERNET;type=signing:ed25519-signing-public@signing.digitalarsenal.io',
      'EMAIL;type=INTERNET;type=encryption:x25519-encryption-public@encryption.digitalarsenal.io',
      'X-SDN-PEER-ID:16Uiu2Alice',
      'END:VCARD',
    ].join('\r\n'));

    expect(epm).toMatchObject({
      dn: 'Alice Example',
      email: 'alice@example.com',
      peer_id: '16Uiu2Alice',
      signing_public_key: 'ed25519-signing-public',
      encryption_public_key: 'x25519-encryption-public',
    });
  });

  it('resolves display public keys from EPM key records when top-level fields are absent', async () => {
    const vcardModule = await import('./identity-vcard');
    const identityPublicKeyValue = (vcardModule as unknown as {
      identityPublicKeyValue?: (epm: Record<string, unknown>, type?: 'signing' | 'encryption') => string | undefined;
    }).identityPublicKeyValue;
    expect(identityPublicKeyValue).toBeTypeOf('function');

    const epm = {
      keys: [
        { key_type: 'signing', public_key: 'signing-from-key-record' },
        { address_type: 'x25519', public_key: 'encryption-from-key-record' },
      ],
    };

    expect(identityPublicKeyValue?.(epm)).toBe('signing-from-key-record');
    expect(identityPublicKeyValue?.(epm, 'signing')).toBe('signing-from-key-record');
    expect(identityPublicKeyValue?.(epm, 'encryption')).toBe('encryption-from-key-record');
  });
});

describe('chunked EPM QR payloads', () => {
  it('reassembles chunked payloads and detects missing chunks', async () => {
    const bytes = new TextEncoder().encode('x'.repeat(4096));
    const chunks = await createChunkedQrPayloads(bytes, {
      id: 'alice',
      mimeType: 'application/vnd.sdn.epm',
      maxPayloadChars: 700,
    });

    expect(chunks.length).toBeGreaterThan(1);
    await expect(reassembleChunkedQrPayloads(chunks)).resolves.toEqual(bytes);
    await expect(reassembleChunkedQrPayloads(chunks.slice(1))).rejects.toThrow(/missing QR chunk/i);
  });

  it('rejects tampered chunk payloads by sha256 metadata', async () => {
    const bytes = new TextEncoder().encode('deterministic EPM bytes');
    const chunks = await createChunkedQrPayloads(bytes, {
      id: 'alice',
      mimeType: 'application/vnd.sdn.epm',
      maxPayloadChars: 80,
    });
    const first = JSON.parse(chunks[0].replace('sdn-epm-qr:v1:', ''));
    first.payload = first.payload.replace(/.$/, first.payload.endsWith('A') ? 'B' : 'A');

    await expect(
      reassembleChunkedQrPayloads([`sdn-epm-qr:v1:${JSON.stringify(first)}`, ...chunks.slice(1)]),
    ).rejects.toThrow(/QR payload digest/i);
  });
});

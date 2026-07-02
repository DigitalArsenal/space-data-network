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
        xpub: 'xpub-node',
        public_key: 'node-public',
        signing_public_key: 'signing-public',
        encryption_public_key: 'encryption-public',
        keys: [
          { key_type: 'signing', public_key: 'signing-public', derivation_path: "m/44'/0'/0'/0/0" },
          { key_type: 'encryption', public_key: 'encryption-public', derivation_path: "m/44'/0'/0'/1/0" },
        ],
        private_key: 'must-not-export',
      },
    });
    const unfolded = payload.replace(/\r\n[ \t]/g, '');

    expect(payload).toContain('BEGIN:VCARD');
    expect(payload).toContain('N:Example;Alice;Q.;Dr.;PhD');
    expect(payload).toContain('FN:Dr. Alice Q. Example');
    expect(payload).not.toContain('ORG:Example Orbital LLC');
    expect(payload).not.toContain('EMAIL;TYPE=INTERNET:alice@example.com');
    expect(payload).toContain('TEL:+1 555 0100');
    expect(payload).not.toContain('TITLE:Flight Director');
    expect(payload).not.toContain('ROLE:Operator');
    expect(payload).toContain('ADR;TYPE=WORK:Box 42;;1 Orbit Way;Cape Canaveral;FL;32920;USA');
    expect(payload).not.toContain('X-SDN-PEER-ID:16Uiu2Alice');
    expect(payload).not.toContain('X-SDN-EPM-CID:bafyepm');
    expect(payload).not.toContain('X-SDN-XPUB:xpub-node');
    expect(unfolded).toContain('EMAIL;TYPE=INTERNET;TYPE=peerid:16Uiu2Alice@peerid.spacedatanetwork.org');
    expect(unfolded).toContain('EMAIL;TYPE=INTERNET;TYPE=xpub:xpub-node@xpub.spacedatanetwork.org');
    expect(payload).not.toContain('EMAIL;type=INTERNET;type=signing:signing-public@signing.spacedatanetwork.org');
    expect(unfolded).not.toContain('EMAIL;type=INTERNET;type=encryption:encryption-public@encryption.spacedatanetwork.org');
    expect(payload).not.toContain('EMAIL;TYPE=INTERNET:node-public@spacedatanetwork.org');
    expect(payload).not.toContain('X-SDN-PUBLIC-KEY:node-public');
    expect(payload).not.toContain('X-SDN-SIGNING-PUBLIC-KEY:signing-public');
    expect(payload).not.toContain('X-SDN-ENCRYPTION-PUBLIC-KEY:encryption-public');
    expect(payload).not.toContain('must-not-export');
  });

  it('omits signing and encryption key arrays from compact QR aliases', () => {
    const payload = createVCardQrPayload({
      id: 'node',
      kind: 'node-self',
      epm_json: {
        dn: 'Local Node',
        peer_id: '12D3KooWNode',
        keys: [
          { key_type: 'signing', public_key: 'array-signing-key', derivation_path: "m/44'/0'/0'/0/0" },
          { key_type: 'encryption', public_key: 'array-encryption-key', derivation_path: "m/44'/0'/0'/1/0" },
        ],
      },
    });
    const unfolded = payload.replace(/\r\n[ \t]/g, '');

    expect(unfolded).toContain('EMAIL;TYPE=INTERNET;TYPE=peerid:12D3KooWNode@peerid.spacedatanetwork.org');
    expect(unfolded).not.toContain('array-signing-key@signing.spacedatanetwork.org');
    expect(unfolded).not.toContain('array-encryption-key@encryption.spacedatanetwork.org');
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
      'EMAIL;type=INTERNET;type=signing:ed25519-signing-public@signing.spacedatanetwork.org',
      'EMAIL;type=INTERNET;type=encryption:x25519-encryption-public@encryption.spacedatanetwork.org',
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

  it('imports PeerID and xpub from compact QR vCard email aliases', async () => {
    const vcardModule = await import('./identity-vcard');
    const fromVCard = (vcardModule as unknown as {
      epmJsonFromVCard?: (payload: string) => Record<string, unknown>;
    }).epmJsonFromVCard;
    expect(fromVCard).toBeTypeOf('function');

    const epm = fromVCard?.([
      'BEGIN:VCARD',
      'VERSION:3.0',
      'FN:CelesTrak Provider',
      'EMAIL;TYPE=INTERNET;TYPE=peerid:16Uiu2Peer@peerid.spacedatanetwork.org',
      'EMAIL;TYPE=INTERNET;TYPE=xpub:xpub6Provider@xpub.spacedatanetwork.org',
      'END:VCARD',
    ].join('\r\n'));

    expect(epm).toMatchObject({
      dn: 'CelesTrak Provider',
      peer_id: '16Uiu2Peer',
      xpub: 'xpub6Provider',
    });
    expect(epm).not.toHaveProperty('email');
  });

  it('resolves role public keys and derivation paths from EPM key records when top-level fields are absent', async () => {
    const vcardModule = await import('./identity-vcard');
    const identityPublicKeyValue = (vcardModule as unknown as {
      identityPublicKeyValue?: (epm: Record<string, unknown>, type?: 'signing' | 'encryption') => string | undefined;
    }).identityPublicKeyValue;
    const identityPublicKeyDetails = (vcardModule as unknown as {
      identityPublicKeyDetails?: (
        epm: Record<string, unknown>,
        type: 'signing' | 'encryption',
      ) => { publicKey: string; derivationPath?: string } | undefined;
    }).identityPublicKeyDetails;
    const identityXpubValue = (vcardModule as unknown as {
      identityXpubValue?: (epm: Record<string, unknown>) => string | undefined;
    }).identityXpubValue;
    expect(identityPublicKeyValue).toBeTypeOf('function');
    expect(identityPublicKeyDetails).toBeTypeOf('function');
    expect(identityXpubValue).toBeTypeOf('function');

    const epm = {
      keys: [
        {
          key_type: 'signing',
          public_key: 'signing-from-key-record',
          derivation_path: "m/44'/0'/0'/0/0",
          xpub: 'xpub-from-key-record',
        },
        {
          key_type: 'encryption',
          public_key: 'encryption-from-key-record',
          derivation_path: "m/44'/0'/0'/1/0",
          xpub: 'xpub-from-key-record',
        },
      ],
    };

    expect(identityXpubValue?.(epm)).toBe('xpub-from-key-record');
    expect(identityPublicKeyValue?.(epm)).toBeUndefined();
    expect(identityPublicKeyValue?.(epm, 'signing')).toBe('signing-from-key-record');
    expect(identityPublicKeyValue?.(epm, 'encryption')).toBe('encryption-from-key-record');
    expect(identityPublicKeyDetails?.(epm, 'signing')).toEqual({
      publicKey: 'signing-from-key-record',
      derivationPath: "m/44'/0'/0'/0/0",
    });
    expect(identityPublicKeyDetails?.(epm, 'encryption')).toEqual({
      publicKey: 'encryption-from-key-record',
      derivationPath: "m/44'/0'/0'/1/0",
    });
  });

  it('does not treat a legacy account identity key as the signing or encryption public key', async () => {
    const vcardModule = await import('./identity-vcard');
    const identityPublicKeyValue = (vcardModule as unknown as {
      identityPublicKeyValue?: (epm: Record<string, unknown>, type?: 'signing' | 'encryption') => string | undefined;
    }).identityPublicKeyValue;
    expect(identityPublicKeyValue).toBeTypeOf('function');

    const epm = {
      public_key: 'legacy-identity-public-key',
      keys: [
        {
          key_type: 'signing',
          address_type: 'secp256k1',
          public_key: 'legacy-identity-public-key',
          key_address: "m/44'/0'/0'",
        },
      ],
    };

    expect(identityPublicKeyValue?.(epm)).toBe('legacy-identity-public-key');
    expect(identityPublicKeyValue?.(epm, 'signing')).toBeUndefined();
    expect(identityPublicKeyValue?.(epm, 'encryption')).toBeUndefined();
  });

  it('does not treat non-identity signing keys as xpub-derived signing keys', async () => {
    const vcardModule = await import('./identity-vcard');
    const identityPublicKeyValue = (vcardModule as unknown as {
      identityPublicKeyValue?: (epm: Record<string, unknown>, type?: 'signing' | 'encryption') => string | undefined;
    }).identityPublicKeyValue;
    expect(identityPublicKeyValue).toBeTypeOf('function');

    const epm = {
      public_key: 'legacy-identity-public-key',
      keys: [
        {
          key_type: 'signing',
          public_key: 'dataset-publication-signing-key',
          key_address: 'sdn/dataset-publication/v1',
        },
      ],
    };

    expect(identityPublicKeyValue?.(epm, 'signing')).toBeUndefined();
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

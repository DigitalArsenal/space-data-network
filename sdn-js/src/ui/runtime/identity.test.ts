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

  it('creates bounded vCard QR payloads with contact fields and one HD xpub alias only', () => {
    const xpub = 'xpub6BpyEDT14VWygfxLMawQKhGXLCVMhJK7voSnjD7VsYYzUfQb6vbTwNhDbXwsa5KraQQgfpDzTq45TfdXQzNiFRfGoFpgbd9KymJsauL4MuT';
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
        xpub,
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
    expect(payload).toContain('EMAIL:alice@example.com');
    expect(payload).toContain('TEL:+1 555 0100');
    expect(payload).toContain('ADR;TYPE=WORK:Box 42;;1 Orbit Way;Cape Canaveral;FL;32920;USA');
    const xpubAlias = `EMAIL;TYPE=INTERNET;TYPE=xpub:${xpub}@xpub.spacedatanetwork.org`;
    expect(unfolded).toContain(xpubAlias);
    expect(unfolded.split(xpubAlias)).toHaveLength(2);
    for (const forbidden of [
      'PRODID',
      'ORG:',
      'TITLE:',
      'ROLE:',
      'peerid.spacedatanetwork.org',
      'X-SDN-XPUB',
      '16Uiu2Alice',
      'bafyepm',
      'node-public',
      'signing-public',
      'encryption-public',
      'must-not-export',
    ]) {
      expect(unfolded).not.toContain(forbidden);
    }
    expect(new TextEncoder().encode(payload).byteLength).toBeLessThanOrEqual(512);
  });

  it('refuses to create an identity QR without the required HD xpub alias', () => {
    expect(() => createVCardQrPayload({
      id: 'contact-only',
      kind: 'hosted',
      epm_json: {
        dn: 'Contact Only',
        email: 'contact@example.com',
        telephone: '+1 555 0100',
      },
    })).toThrow('HD extended public key is required for identity QR');
  });

  it('refuses an xpub value that cannot be stored as the vCard email alias', () => {
    expect(() => createVCardQrPayload({
      id: 'invalid-xpub',
      kind: 'hosted',
      epm_json: {
        dn: 'Invalid Xpub',
        xpub: 'xpub:cannot-be-an-email-local-part',
      },
    })).toThrow('HD extended public key is required for identity QR');
  });

  it('fits a representative contact and HD xpub payload in a version-15 level-M QR or smaller', async () => {
    // @ts-expect-error qrcode does not ship TypeScript declarations in this package.
    const module = await import('qrcode');
    const qrCode = (module.default ?? module) as {
      create: (payload: string, options: { errorCorrectionLevel: 'M' }) => { modules: { size: number } };
    };
    const payload = createVCardQrPayload({
      id: 'alice',
      kind: 'hosted',
      label: 'Dr. Alice Q. Example',
      peerId: '16Uiu2Alice',
      epmJson: {
        dn: 'Dr. Alice Q. Example',
        given_name: 'Alice',
        family_name: 'Example',
        additional_name: 'Q.',
        honorific_prefix: 'Dr.',
        honorific_suffix: 'PhD',
        email: 'alice@example.com',
        telephone: '+1 555 0100',
        address: {
          po_box: 'Box 42',
          street: '1 Orbit Way',
          locality: 'Cape Canaveral',
          region: 'FL',
          postal_code: '32920',
          country: 'USA',
        },
        xpub: 'xpub6BpyEDT14VWygfxLMawQKhGXLCVMhJK7voSnjD7VsYYzUfQb6vbTwNhDbXwsa5KraQQgfpDzTq45TfdXQzNiFRfGoFpgbd9KymJsauL4MuT',
      },
    });

    expect(qrCode.create(payload, { errorCorrectionLevel: 'M' }).modules.size).toBeLessThanOrEqual(77);
  });

  it('omits signing and encryption key arrays from compact QR aliases', () => {
    const payload = createVCardQrPayload({
      id: 'node',
      kind: 'node-self',
      epm_json: {
        dn: 'Local Node',
        peer_id: '12D3KooWNode',
        xpub: 'xpub6BpyEDT14VWygfxLMawQKhGXLCVMhJK7voSnjD7VsYYzUfQb6vbTwNhDbXwsa5KraQQgfpDzTq45TfdXQzNiFRfGoFpgbd9KymJsauL4MuT',
        keys: [
          { key_type: 'signing', public_key: 'array-signing-key', derivation_path: "m/44'/0'/0'/0/0" },
          { key_type: 'encryption', public_key: 'array-encryption-key', derivation_path: "m/44'/0'/0'/1/0" },
        ],
      },
    });
    const unfolded = payload.replace(/\r\n[ \t]/g, '');

    expect(unfolded).not.toContain('12D3KooWNode');
    expect(unfolded).not.toContain('peerid.spacedatanetwork.org');
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

  it('reduces a full daemon vCard to the same bounded contact and xpub QR contract', async () => {
    const vcardModule = await import('./identity-vcard');
    const reduceForQr = (vcardModule as unknown as {
      createVCardQrPayloadFromVCard?: (payload: string) => string;
    }).createVCardQrPayloadFromVCard;
    expect(reduceForQr).toBeTypeOf('function');

    const xpub = 'xpub6BpyEDT14VWygfxLMawQKhGXLCVMhJK7voSnjD7VsYYzUfQb6vbTwNhDbXwsa5KraQQgfpDzTq45TfdXQzNiFRfGoFpgbd9KymJsauL4MuT';
    const full = [
      'BEGIN:VCARD',
      'VERSION:3.0',
      'PRODID:-//Space Data Network//Full EPM//EN',
      'N:Example;Alice;;;',
      'FN:Alice Example',
      'ORG:Example Orbital LLC',
      'TITLE:Flight Director',
      'EMAIL:alice@example.com',
      'TEL:+1 555 0100',
      'ADR;TYPE=WORK:Box 42;;1 Orbit Way;Cape Canaveral;FL;32920;USA',
      'X-SDN-PEER-ID:16Uiu2Alice',
      'X-SDN-EPM-CID:bafyepm',
      'X-SDN-EPM-B64:' + 'z'.repeat(2200),
      `EMAIL;TYPE=INTERNET;TYPE=xpub:${xpub}@xpub.spacedatanetwork.org`,
      'EMAIL;TYPE=INTERNET;TYPE=signing:signing-public@signing.spacedatanetwork.org',
      'END:VCARD',
    ].join('\r\n');

    const payload = reduceForQr?.(full) ?? '';
    const unfolded = payload.replace(/\r\n[ \t]/g, '');
    expect(unfolded).toContain('N:Example;Alice;;;');
    expect(unfolded).toContain('FN:Alice Example');
    expect(unfolded).toContain('EMAIL:alice@example.com');
    expect(unfolded).toContain('TEL:+1 555 0100');
    expect(unfolded).toContain('ADR;TYPE=WORK:Box 42;;1 Orbit Way;Cape Canaveral;FL;32920;USA');
    expect(unfolded).toContain(`EMAIL;TYPE=INTERNET;TYPE=xpub:${xpub}@xpub.spacedatanetwork.org`);
    for (const forbidden of ['PRODID', 'ORG:', 'TITLE:', '16Uiu2Alice', 'bafyepm', 'X-SDN-EPM-B64', 'signing-public']) {
      expect(unfolded).not.toContain(forbidden);
    }
    expect(new TextEncoder().encode(payload).byteLength).toBeLessThanOrEqual(512);
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

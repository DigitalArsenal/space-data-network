import { describe, expect, it } from 'vitest';
import {
  createChunkedQrPayloads,
  createPublicEpmExport,
  createVCardQrPayload,
  normalizeHostedEpmRecord,
  reassembleChunkedQrPayloads,
} from './identity';

// §21 fixture keys (owner ruling 2026-08-19): the card carries b64url of the
// hex-decoded LITERAL public keys — never the xpub or derivation paths.
const SIGNING_PUBLIC_KEY_HEX = '0321fce2a66e6c1be09128b20e3f50374fa05ec1ceb84eaa78e69cf1cddc60a7a6';
const ENCRYPTION_PUBLIC_KEY_HEX = '0301f6e5f01a7765617c817568db07e81dc1b86a87575f4702f347b5897f6b1d06';
const SIGN_ALIAS_LINE = 'EMAIL;TYPE=INTERNET;TYPE=sign:AyH84qZubBvgkSiyDj9QN0-gXsHOuE6qeOac8c3cYKem@sign.spacedatanetwork.org';
const ENCRYPT_ALIAS_LINE = 'EMAIL;TYPE=INTERNET;TYPE=encrypt:AwH25fAad2VhfIF1aNsH6B3BuGqHV19HAvNHtYl_ax0G@encrypt.spacedatanetwork.org';

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

  it('creates bounded vCard QR payloads with contact fields and literal sign/encrypt key aliases', () => {
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
        xpub: 'xpub6BpyEDT14VWygfxLMawQKhGXLCVMhJK7voSnjD7VsYYzUfQb6vbTwNhDbXwsa5KraQQgfpDzTq45TfdXQzNiFRfGoFpgbd9KymJsauL4MuT',
        public_key: 'node-public',
        signing_public_key: SIGNING_PUBLIC_KEY_HEX,
        encryption_public_key: ENCRYPTION_PUBLIC_KEY_HEX,
        keys: [
          { key_type: 'signing', public_key: SIGNING_PUBLIC_KEY_HEX, derivation_path: "m/44'/0'/0'/0/0" },
          { key_type: 'encryption', public_key: ENCRYPTION_PUBLIC_KEY_HEX, derivation_path: "m/44'/0'/0'/1/0" },
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
    // §21: the aliases carry b64url(literal key bytes) — exactly one row each.
    expect(unfolded.split(SIGN_ALIAS_LINE)).toHaveLength(2);
    expect(unfolded.split(ENCRYPT_ALIAS_LINE)).toHaveLength(2);
    for (const forbidden of [
      'PRODID',
      'ORG:',
      'TITLE:',
      'ROLE:',
      'peerid.spacedatanetwork.org',
      'xpub',
      'signing.spacedatanetwork.org',
      'encryption.spacedatanetwork.org',
      'X-SDN-XPUB',
      '16Uiu2Alice',
      'bafyepm',
      'node-public',
      SIGNING_PUBLIC_KEY_HEX,
      ENCRYPTION_PUBLIC_KEY_HEX,
      "m/44'",
      'must-not-export',
    ]) {
      expect(unfolded).not.toContain(forbidden);
    }
    expect(new TextEncoder().encode(payload).byteLength).toBeLessThanOrEqual(512);
  });

  it('refuses to create an identity QR without a verifiable signing public key', () => {
    expect(() => createVCardQrPayload({
      id: 'contact-only',
      kind: 'hosted',
      epm_json: {
        dn: 'Contact Only',
        email: 'contact@example.com',
        telephone: '+1 555 0100',
      },
    })).toThrow('a verifiable signing public key is required for identity QR');
  });

  it('does not accept a bare xpub as QR identity material', () => {
    // §21: the xpub is PRIVATE and never satisfies the QR — the card needs
    // the literal signing key bytes (the sign alias).
    expect(() => createVCardQrPayload({
      id: 'xpub-only',
      kind: 'hosted',
      epm_json: {
        dn: 'Xpub Only',
        xpub: 'xpub6BpyEDT14VWygfxLMawQKhGXLCVMhJK7voSnjD7VsYYzUfQb6vbTwNhDbXwsa5KraQQgfpDzTq45TfdXQzNiFRfGoFpgbd9KymJsauL4MuT',
      },
    })).toThrow('a verifiable signing public key is required for identity QR');
  });

  it('fits a representative contact and literal-key payload in a version-15 level-M QR or smaller', async () => {
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
        signing_public_key: SIGNING_PUBLIC_KEY_HEX,
        encryption_public_key: ENCRYPTION_PUBLIC_KEY_HEX,
      },
    });

    expect(qrCode.create(payload, { errorCorrectionLevel: 'M' }).modules.size).toBeLessThanOrEqual(77);
  });

  it('carries signing and encryption keys as b64url aliases, never raw arrays or paths', () => {
    const payload = createVCardQrPayload({
      id: 'node',
      kind: 'node-self',
      epm_json: {
        dn: 'Local Node',
        peer_id: '12D3KooWNode',
        xpub: 'xpub6BpyEDT14VWygfxLMawQKhGXLCVMhJK7voSnjD7VsYYzUfQb6vbTwNhDbXwsa5KraQQgfpDzTq45TfdXQzNiFRfGoFpgbd9KymJsauL4MuT',
        keys: [
          { key_type: 'signing', public_key: SIGNING_PUBLIC_KEY_HEX, derivation_path: "m/44'/0'/0'/0/0" },
          { key_type: 'encryption', public_key: ENCRYPTION_PUBLIC_KEY_HEX, derivation_path: "m/44'/0'/0'/1/0" },
        ],
      },
    });
    const unfolded = payload.replace(/\r\n[ \t]/g, '');

    expect(unfolded).not.toContain('12D3KooWNode');
    expect(unfolded).not.toContain('peerid.spacedatanetwork.org');
    expect(unfolded).not.toContain('xpub');
    expect(unfolded).not.toContain(SIGNING_PUBLIC_KEY_HEX);
    expect(unfolded).not.toContain(ENCRYPTION_PUBLIC_KEY_HEX);
    expect(unfolded).not.toContain("m/44'");
    expect(unfolded).not.toContain('signing.spacedatanetwork.org');
    expect(unfolded).not.toContain('encryption.spacedatanetwork.org');
    // The keys array rides the card as b64url literal-key aliases instead.
    expect(unfolded).toContain(SIGN_ALIAS_LINE);
    expect(unfolded).toContain(ENCRYPT_ALIAS_LINE);
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

  it('imports PeerID and literal signing/encryption keys from compact QR vCard email aliases', async () => {
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
      SIGN_ALIAS_LINE,
      ENCRYPT_ALIAS_LINE,
      'END:VCARD',
    ].join('\r\n'));

    // The b64url local parts decode back to the hex public keys the record
    // carries; the xpub alias is retired and never imported.
    expect(epm).toMatchObject({
      dn: 'CelesTrak Provider',
      peer_id: '16Uiu2Peer',
      signing_public_key: SIGNING_PUBLIC_KEY_HEX,
      encryption_public_key: ENCRYPTION_PUBLIC_KEY_HEX,
    });
    expect(epm).not.toHaveProperty('email');
    expect(epm).not.toHaveProperty('xpub');
  });

  it('reduces a full daemon vCard to the same bounded contact and literal-key QR contract', async () => {
    const vcardModule = await import('./identity-vcard');
    const reduceForQr = (vcardModule as unknown as {
      createVCardQrPayloadFromVCard?: (payload: string) => string;
    }).createVCardQrPayloadFromVCard;
    expect(reduceForQr).toBeTypeOf('function');

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
      SIGN_ALIAS_LINE,
      ENCRYPT_ALIAS_LINE,
      'END:VCARD',
    ].join('\r\n');

    const payload = reduceForQr?.(full) ?? '';
    const unfolded = payload.replace(/\r\n[ \t]/g, '');
    expect(unfolded).toContain('N:Example;Alice;;;');
    expect(unfolded).toContain('FN:Alice Example');
    expect(unfolded).toContain('EMAIL:alice@example.com');
    expect(unfolded).toContain('TEL:+1 555 0100');
    expect(unfolded).toContain('ADR;TYPE=WORK:Box 42;;1 Orbit Way;Cape Canaveral;FL;32920;USA');
    // §21: the b64url literal-key aliases survive the reduction untouched.
    expect(unfolded).toContain(SIGN_ALIAS_LINE);
    expect(unfolded).toContain(ENCRYPT_ALIAS_LINE);
    for (const forbidden of [
      'PRODID',
      'ORG:',
      'TITLE:',
      '16Uiu2Alice',
      'bafyepm',
      'X-SDN-EPM-B64',
      SIGNING_PUBLIC_KEY_HEX,
      ENCRYPTION_PUBLIC_KEY_HEX,
      'xpub',
    ]) {
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

  it('treats a KEY_TYPE signing key as the signing public key even with a legacy account path', async () => {
    const vcardModule = await import('./identity-vcard');
    const identityPublicKeyValue = (vcardModule as unknown as {
      identityPublicKeyValue?: (epm: Record<string, unknown>, type?: 'signing' | 'encryption') => string | undefined;
    }).identityPublicKeyValue;
    expect(identityPublicKeyValue).toBeTypeOf('function');

    // §21: KEY_TYPE is the authority — the legacy path-based discrimination is
    // RETIRED (paths are private). A key the record declares signing IS the
    // signing public key; the epmsig binding (the signature verifies against
    // the card key) is what holds it honest.
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
    expect(identityPublicKeyValue?.(epm, 'signing')).toBe('legacy-identity-public-key');
    expect(identityPublicKeyValue?.(epm, 'encryption')).toBeUndefined();
  });

  it('resolves typed signing keys but refuses QR emission for undecodable hex', async () => {
    const vcardModule = await import('./identity-vcard');
    const identityPublicKeyValue = (vcardModule as unknown as {
      identityPublicKeyValue?: (epm: Record<string, unknown>, type?: 'signing' | 'encryption') => string | undefined;
    }).identityPublicKeyValue;
    expect(identityPublicKeyValue).toBeTypeOf('function');

    // A KEY_TYPE signing key resolves regardless of its purpose-labeled
    // KEY_ADDRESS (paths no longer discriminate under §21)...
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
    expect(identityPublicKeyValue?.(epm, 'signing')).toBe('dataset-publication-signing-key');

    // ...but a QR is only servable for keys whose bytes are decodable hex:
    // undecodable material refuses, mirroring the Go side which skips the
    // alias rather than emitting a dead row (no garbage b64url aliases).
    expect(() => createVCardQrPayload({ ...epm, dn: 'Dataset Node' })).toThrow(
      'a verifiable signing public key is required for identity QR',
    );
    expect(() => createVCardQrPayload({ dn: 'Plain Node', signing_public_key: 'not-hex' })).toThrow(
      'a verifiable signing public key is required for identity QR',
    );
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

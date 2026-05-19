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

  it('creates vCard QR payloads with public key and EPM fields only', () => {
    const payload = createVCardQrPayload({
      id: 'alice',
      kind: 'hosted',
      epm_json: {
        dn: 'Alice Example',
        peer_id: '16Uiu2Alice',
        epm_cid: 'bafyepm',
        public_key: 'abcdef',
        signing_public_key: 'signing-public',
        encryption_public_key: 'encryption-public',
        private_key: 'must-not-export',
      },
    });

    expect(payload).toContain('BEGIN:VCARD');
    expect(payload).toContain('FN:Alice Example');
    expect(payload).toContain('X-SDN-PEER-ID:16Uiu2Alice');
    expect(payload).toContain('X-SDN-EPM-CID:bafyepm');
    expect(payload).toContain('EMAIL;TYPE=INTERNET:abcdef@spacedatanetwork.org');
    expect(payload).toContain('X-SDN-PUBLIC-KEY:abcdef');
    expect(payload).toContain('X-SDN-SIGNING-PUBLIC-KEY:signing-public');
    expect(payload).toContain('X-SDN-ENCRYPTION-PUBLIC-KEY:encryption-public');
    expect(payload).not.toContain('must-not-export');
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

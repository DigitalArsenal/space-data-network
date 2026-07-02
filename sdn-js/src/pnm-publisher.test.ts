import { describe, it, expect } from 'vitest';

import {
  buildSignedPnm,
  verifySignedPnm,
  publishSignedPnm,
  signAndPublishPnm,
  pnmSignaturePayload,
  PNM_TOPIC,
} from './pnm-publisher';
import { initHDWallet, ed25519PublicKey } from './crypto/hd-wallet';
import { decodePnmFlatBuffer } from './ui/runtime/pnm-flatbuffer';

let seedCounter = 40;
async function keypair() {
  await initHDWallet();
  const privateKey = Uint8Array.from({ length: 32 }, (_, i) => i + seedCounter++);
  return { privateKey, publicKey: await ed25519PublicKey(privateKey) };
}

describe('pnm-publisher', () => {
  it('signature payload matches the Go byte layout', () => {
    const payload = pnmSignaturePayload('bafycid', '$DPM');
    const expected = [
      ...new TextEncoder().encode('SDN-DPM-PNM\x00'),
      ...new TextEncoder().encode('$DPM'),
      0,
      ...new TextEncoder().encode('bafycid'),
    ];
    expect([...payload]).toEqual(expected);
  });

  it('builds a size-prefixed $PNM the existing decoder reads', async () => {
    const keys = await keypair();
    const bytes = await buildSignedPnm({
      cid: 'bafyexamplecid',
      fileId: '$DPM',
      fileName: 'batch-1.fb',
      publishedAt: '2026-07-02T00:00:00.000Z',
      signingKey: keys.privateKey,
    });
    const decoded = decodePnmFlatBuffer(bytes);
    expect(decoded.CID).toBe('bafyexamplecid');
    expect(decoded.FILE_ID).toBe('$DPM');
    expect(decoded.FILE_NAME).toBe('batch-1.fb');
    expect(decoded.MULTIFORMAT_ADDRESS).toBe('/ipfs/bafyexamplecid');
    expect(decoded.PUBLISH_TIMESTAMP).toBe('2026-07-02T00:00:00.000Z');
    expect(decoded.SIGNATURE_TYPE).toBe('Ed25519');
    expect(String(decoded.SIGNATURE)).toMatch(/^[0-9a-f]{128}$/);
  });

  it('verifySignedPnm accepts a valid envelope and rejects tampering', async () => {
    const keys = await keypair();
    const bytes = await buildSignedPnm({
      cid: 'bafyexamplecid',
      fileId: 'EPHEMERIS',
      signingKey: keys.privateKey,
    });
    const evidence = await verifySignedPnm(bytes, keys.publicKey);
    expect(evidence.cid).toBe('bafyexamplecid');
    expect(evidence.fileId).toBe('EPHEMERIS');
    expect(evidence.signature.length).toBe(64);

    const other = await keypair();
    await expect(verifySignedPnm(bytes, other.publicKey)).rejects.toThrow(
      /invalid PNM signature/,
    );
  });

  it('publishes on the canonical PNM topic', async () => {
    const keys = await keypair();
    const published: Array<{ topic: string; data: Uint8Array }> = [];
    const publisher = {
      publish: async (topic: string, data: Uint8Array) => {
        published.push({ topic, data });
      },
    };
    const { topic, pnmBytes } = await signAndPublishPnm(publisher, {
      cid: 'bafycid2',
      fileId: '$DPM',
      signingKey: keys.privateKey,
    });
    expect(topic).toBe(PNM_TOPIC);
    expect(PNM_TOPIC).toBe('/spacedatanetwork/sds/PNM.fbs');
    expect(published[0].topic).toBe(PNM_TOPIC);
    expect(published[0].data).toEqual(pnmBytes);

    await publishSignedPnm(publisher, pnmBytes, '/custom/topic');
    expect(published[1].topic).toBe('/custom/topic');
  });
});

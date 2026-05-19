import { describe, expect, it } from 'vitest';
import { decodeEncryptedCoreArtifact, encodeEncryptedCoreArtifact } from './identity-core';

describe('encrypted Core artifacts', () => {
  it('round-trips Core material through encrypted KMF plus appended ENC metadata', async () => {
    const artifact = await encodeEncryptedCoreArtifact({
      passphrase: 'correct horse battery staple',
      core: {
        mnemonic: 'abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about',
        account: 0,
        peerId: '12D3KooWCore',
        xpub: 'xpub-test',
        publicEpmHash: 'bafyhash',
      },
    });

    expect(artifact.bytes.length).toBeGreaterThan(128);
    expect(new TextDecoder().decode(artifact.bytes)).not.toContain('abandon');
    expect(artifact.sections.map((section) => section.type)).toEqual(['KMF', 'ENC', 'SIG']);

    const decoded = await decodeEncryptedCoreArtifact({
      passphrase: 'correct horse battery staple',
      bytes: artifact.bytes,
    });

    expect(decoded.core.peerId).toBe('12D3KooWCore');
    expect(decoded.core.mnemonic).toContain('abandon');
  });

  it('rejects plaintext JSON imports', async () => {
    await expect(
      decodeEncryptedCoreArtifact({
        passphrase: 'pw',
        bytes: new TextEncoder().encode(JSON.stringify({ mnemonic: 'plain text' })),
      }),
    ).rejects.toThrow(/encrypted Core artifact/i);
  });

  it('rejects the wrong passphrase before returning Core material', async () => {
    const artifact = await encodeEncryptedCoreArtifact({
      passphrase: 'right',
      core: { mnemonic: 'secret words', account: 0, peerId: 'peer', xpub: 'xpub' },
    });

    await expect(
      decodeEncryptedCoreArtifact({
        passphrase: 'wrong',
        bytes: artifact.bytes,
      }),
    ).rejects.toThrow(/decrypt/i);
  });

  it('rejects tampered digest metadata before decrypting Core material', async () => {
    const artifact = await encodeEncryptedCoreArtifact({
      passphrase: 'right',
      core: { mnemonic: 'secret words', account: 0, peerId: 'peer', xpub: 'xpub' },
    });
    const tampered = new Uint8Array(artifact.bytes);
    const kmfSection = artifact.sections.find((section) => section.type === 'KMF');
    expect(kmfSection).toBeDefined();
    tampered[kmfSection!.offset + kmfSection!.bytes.length - 1] ^= 0xff;

    await expect(
      decodeEncryptedCoreArtifact({
        passphrase: 'right',
        bytes: tampered,
      }),
    ).rejects.toThrow(/digest metadata/i);
  });
});

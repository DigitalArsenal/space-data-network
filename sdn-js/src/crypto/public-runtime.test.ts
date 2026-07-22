import { ed25519 } from '@noble/curves/ed25519';
import { describe, expect, it } from 'vitest';

import {
  decryptPublicAesGcm,
  publicSha256,
  verifyPublicEd25519Signature,
} from './public-runtime';

describe('public non-wallet crypto runtime', () => {
  it('hashes and verifies without loading the HD wallet runtime', () => {
    const message = new TextEncoder().encode('sdn public verification');
    const seed = new Uint8Array(32).fill(0x24);
    const publicKey = ed25519.getPublicKey(seed);
    const signature = ed25519.sign(message, seed);

    expect(bytesToHex(publicSha256(new TextEncoder().encode('abc')))).toBe(
      'ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad',
    );
    expect(verifyPublicEd25519Signature(publicKey, message, signature)).toBe(true);
    expect(verifyPublicEd25519Signature(publicKey, new Uint8Array(message.length), signature)).toBe(false);
  });

  it('decrypts the NIST AES-256-GCM empty-message vector', async () => {
    const plaintext = await decryptPublicAesGcm(
      new Uint8Array(32),
      hexToBytes('530f8afbc74536b9a963b4f1c4cb738b'),
      new Uint8Array(12),
    );

    expect(plaintext).toEqual(new Uint8Array());
  });
});

function hexToBytes(value: string): Uint8Array {
  return Uint8Array.from(value.match(/.{2}/g) ?? [], (byte) => Number.parseInt(byte, 16));
}

function bytesToHex(value: Uint8Array): string {
  return Array.from(value, (byte) => byte.toString(16).padStart(2, '0')).join('');
}

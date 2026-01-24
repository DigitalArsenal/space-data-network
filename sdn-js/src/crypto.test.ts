import { describe, it, expect, beforeEach } from 'vitest';
import {
  getCryptoMetrics,
  isCryptoAvailable,
  isCryptoWasmVerified,
  encrypt,
  decrypt,
  generateKey,
  randomBytes,
  sha256,
} from './crypto';

describe('crypto', () => {
  describe('getCryptoMetrics', () => {
    it('should return crypto metrics object', () => {
      const metrics = getCryptoMetrics();
      expect(metrics).toHaveProperty('wasmLoadAttempts');
      expect(metrics).toHaveProperty('wasmLoadSuccesses');
      expect(metrics).toHaveProperty('wasmLoadFailures');
      expect(metrics).toHaveProperty('lastLoadTime');
      expect(metrics).toHaveProperty('lastError');
    });

    it('should return a copy (immutable)', () => {
      const metrics1 = getCryptoMetrics();
      const metrics2 = getCryptoMetrics();
      expect(metrics1).not.toBe(metrics2);
    });
  });

  describe('isCryptoAvailable', () => {
    it('should return false when WASM not loaded', () => {
      // In test environment, WASM is not loaded
      expect(isCryptoAvailable()).toBe(false);
    });
  });

  describe('isCryptoWasmVerified', () => {
    it('should return false when WASM not verified', () => {
      expect(isCryptoWasmVerified()).toBe(false);
    });
  });

  describe('generateKey', () => {
    it('should generate 32-byte key', () => {
      const key = generateKey();
      expect(key).toBeInstanceOf(Uint8Array);
      expect(key.length).toBe(32);
    });

    it('should generate unique keys', () => {
      const key1 = generateKey();
      const key2 = generateKey();
      expect(key1).not.toEqual(key2);
    });
  });

  describe('randomBytes', () => {
    it('should generate specified number of bytes', () => {
      const bytes16 = randomBytes(16);
      expect(bytes16.length).toBe(16);

      const bytes64 = randomBytes(64);
      expect(bytes64.length).toBe(64);

      const bytes128 = randomBytes(128);
      expect(bytes128.length).toBe(128);
    });

    it('should generate unique random bytes', () => {
      const bytes1 = randomBytes(32);
      const bytes2 = randomBytes(32);
      expect(bytes1).not.toEqual(bytes2);
    });

    it('should handle zero length', () => {
      const bytes = randomBytes(0);
      expect(bytes.length).toBe(0);
    });
  });

  describe('sha256', () => {
    it('should compute correct hash for known input', async () => {
      const input = new TextEncoder().encode('hello world');
      const hash = await sha256(input);

      expect(hash).toBeInstanceOf(Uint8Array);
      expect(hash.length).toBe(32);

      // Known SHA-256 hash of "hello world"
      const expectedHex =
        'b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9';
      const actualHex = Array.from(hash)
        .map((b) => b.toString(16).padStart(2, '0'))
        .join('');
      expect(actualHex).toBe(expectedHex);
    });

    it('should produce different hashes for different inputs', async () => {
      const hash1 = await sha256(new TextEncoder().encode('input1'));
      const hash2 = await sha256(new TextEncoder().encode('input2'));
      expect(hash1).not.toEqual(hash2);
    });

    it('should handle empty input', async () => {
      const hash = await sha256(new Uint8Array(0));
      expect(hash.length).toBe(32);

      // Known SHA-256 hash of empty string
      const expectedHex =
        'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855';
      const actualHex = Array.from(hash)
        .map((b) => b.toString(16).padStart(2, '0'))
        .join('');
      expect(actualHex).toBe(expectedHex);
    });

    it('should handle binary data', async () => {
      const binary = new Uint8Array([0x00, 0xff, 0x7f, 0x80]);
      const hash = await sha256(binary);
      expect(hash.length).toBe(32);
    });
  });

  describe('encrypt/decrypt (Web Crypto fallback)', () => {
    it('should encrypt and decrypt data', async () => {
      const key = generateKey();
      const plaintext = new TextEncoder().encode('Hello, World!');

      const ciphertext = await encrypt(key, plaintext);
      expect(ciphertext).toBeInstanceOf(Uint8Array);
      expect(ciphertext.length).toBeGreaterThan(plaintext.length); // IV + tag overhead

      const decrypted = await decrypt(key, ciphertext);
      expect(decrypted).toEqual(plaintext);
    });

    it('should produce different ciphertext for same plaintext (random IV)', async () => {
      const key = generateKey();
      const plaintext = new TextEncoder().encode('Same message');

      const ciphertext1 = await encrypt(key, plaintext);
      const ciphertext2 = await encrypt(key, plaintext);

      // Ciphertexts should be different due to random IV
      expect(ciphertext1).not.toEqual(ciphertext2);
    });

    it('should fail decryption with wrong key', async () => {
      const key1 = generateKey();
      const key2 = generateKey();
      const plaintext = new TextEncoder().encode('Secret message');

      const ciphertext = await encrypt(key1, plaintext);

      await expect(decrypt(key2, ciphertext)).rejects.toThrow();
    });

    it('should handle empty plaintext', async () => {
      const key = generateKey();
      const plaintext = new Uint8Array(0);

      const ciphertext = await encrypt(key, plaintext);
      const decrypted = await decrypt(key, ciphertext);

      expect(decrypted).toEqual(plaintext);
    });

    it('should handle large data', async () => {
      const key = generateKey();
      const plaintext = randomBytes(10000); // 10KB

      const ciphertext = await encrypt(key, plaintext);
      const decrypted = await decrypt(key, ciphertext);

      expect(decrypted).toEqual(plaintext);
    });

    it('should fail with corrupted ciphertext', async () => {
      const key = generateKey();
      const plaintext = new TextEncoder().encode('Test message');

      const ciphertext = await encrypt(key, plaintext);

      // Corrupt the ciphertext
      const corrupted = new Uint8Array(ciphertext);
      corrupted[corrupted.length - 1] ^= 0xff;

      await expect(decrypt(key, corrupted)).rejects.toThrow();
    });
  });
});

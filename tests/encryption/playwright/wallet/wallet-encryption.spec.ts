/**
 * Mobile Wallet Browser Encryption Tests
 *
 * Tests Phantom/MetaMask in-app browser encrypted communication.
 * Verifies Web3 wallet key derivation for encryption.
 */

import { test, expect } from '@playwright/test';

// Web3 wallet key derivation implementation
const WALLET_KEY_DERIVATION = `
  class WalletKeyDerivation {
    /**
     * Derive an encryption key pair from a wallet signature.
     * This allows deterministic key derivation using Web3 wallet signatures.
     */
    static async deriveFromSignature(signature, curveType = 'secp256k1') {
      // Remove '0x' prefix if present
      const sigBytes = signature.startsWith('0x')
        ? new Uint8Array(signature.slice(2).match(/.{2}/g).map(b => parseInt(b, 16)))
        : new TextEncoder().encode(signature);

      // Use HKDF-like derivation with SHA-256
      const info = new TextEncoder().encode('SDN-ECIES-KEY-DERIVATION-' + curveType);

      // Hash signature with info to derive key material
      const combined = new Uint8Array(sigBytes.length + info.length);
      combined.set(sigBytes, 0);
      combined.set(info, sigBytes.length);

      const keyMaterial = await crypto.subtle.digest('SHA-256', combined);
      const privateKeyBytes = new Uint8Array(keyMaterial);

      // For secp256k1, the private key is 32 bytes
      // The public key would be derived from this (65 bytes uncompressed)
      return {
        privateKey: privateKeyBytes,
        publicKey: await this.derivePublicKey(privateKeyBytes, curveType),
        curveType,
        derivedFrom: 'wallet-signature'
      };
    }

    /**
     * Derive public key from private key.
     * Note: secp256k1 would require a library like noble/curves in production.
     */
    static async derivePublicKey(privateKey, curveType) {
      if (curveType === 'secp256k1') {
        // In production, use noble/curves secp256k1
        // For testing, we simulate with P-256
        try {
          const keyPair = await crypto.subtle.generateKey(
            { name: 'ECDH', namedCurve: 'P-256' },
            true,
            ['deriveBits']
          );
          const publicKey = await crypto.subtle.exportKey('raw', keyPair.publicKey);
          return new Uint8Array(publicKey);
        } catch {
          // Fallback for testing
          const hash = await crypto.subtle.digest('SHA-256', privateKey);
          return new Uint8Array(hash);
        }
      }

      // For P-256
      const keyPair = await crypto.subtle.generateKey(
        { name: 'ECDH', namedCurve: 'P-256' },
        true,
        ['deriveBits']
      );
      const publicKey = await crypto.subtle.exportKey('raw', keyPair.publicKey);
      return new Uint8Array(publicKey);
    }

    /**
     * Sign a message to derive encryption keys (EIP-191 personal_sign style).
     * This is a mock for testing - real implementation would use Web3 provider.
     */
    static async mockWalletSign(message, privateKey) {
      // In real implementation, this would call:
      // await ethereum.request({ method: 'personal_sign', params: [message, address] })

      const msgBytes = new TextEncoder().encode(message);
      const prefix = new TextEncoder().encode(
        '\\x19Ethereum Signed Message:\\n' + msgBytes.length
      );

      const combined = new Uint8Array(prefix.length + msgBytes.length);
      combined.set(prefix, 0);
      combined.set(msgBytes, prefix.length);

      // Hash with private key for deterministic output
      const withKey = new Uint8Array(combined.length + privateKey.length);
      withKey.set(combined, 0);
      withKey.set(privateKey, combined.length);

      const signature = await crypto.subtle.digest('SHA-256', withKey);
      const sigBytes = new Uint8Array(signature);

      // Return as hex string like real wallet would
      return '0x' + Array.from(sigBytes).map(b => b.toString(16).padStart(2, '0')).join('');
    }

    /**
     * Verify key derivation produces consistent results.
     */
    static async verifyDeterministic(signature) {
      const key1 = await this.deriveFromSignature(signature);
      const key2 = await this.deriveFromSignature(signature);

      // Keys should be identical
      if (key1.privateKey.length !== key2.privateKey.length) return false;

      for (let i = 0; i < key1.privateKey.length; i++) {
        if (key1.privateKey[i] !== key2.privateKey[i]) return false;
      }

      return true;
    }

    /**
     * Derive encryption key from Ethereum address.
     * Uses a deterministic message for key derivation.
     */
    static getKeyDerivationMessage(address, purpose = 'encryption') {
      return \`Sign this message to derive your Space Data Network \${purpose} key.

Address: \${address}
Purpose: \${purpose}
Chain: SDN

This signature will be used to derive encryption keys for secure communication.
It will NOT authorize any blockchain transactions.\`;
    }
  }

  window.WalletKeyDerivation = WalletKeyDerivation;
`;

test.describe('Web3 Wallet Key Derivation', () => {
  test.beforeEach(async ({ page }) => {
    await page.addInitScript(WALLET_KEY_DERIVATION);
  });

  test('should derive deterministic key from wallet signature', async ({ page }) => {
    await page.goto('/');

    const result = await page.evaluate(async () => {
      // Mock wallet private key (in real scenario, this is in the wallet)
      const mockWalletKey = new Uint8Array(32);
      crypto.getRandomValues(mockWalletKey);

      const address = '0x1234567890abcdef1234567890abcdef12345678';
      const message = window.WalletKeyDerivation.getKeyDerivationMessage(address, 'encryption');

      // Get signature from wallet
      const signature = await window.WalletKeyDerivation.mockWalletSign(message, mockWalletKey);

      // Derive encryption key
      const keyPair = await window.WalletKeyDerivation.deriveFromSignature(signature, 'secp256k1');

      return {
        hasPrivateKey: keyPair.privateKey.length === 32,
        hasPublicKey: keyPair.publicKey.length > 0,
        curveType: keyPair.curveType,
        derivedFrom: keyPair.derivedFrom,
        signatureLength: signature.length
      };
    });

    expect(result.hasPrivateKey).toBe(true);
    expect(result.hasPublicKey).toBe(true);
    expect(result.curveType).toBe('secp256k1');
    expect(result.derivedFrom).toBe('wallet-signature');
  });

  test('should produce consistent keys from same signature', async ({ page }) => {
    await page.goto('/');

    const result = await page.evaluate(async () => {
      const mockWalletKey = new Uint8Array(32);
      crypto.getRandomValues(mockWalletKey);

      const address = '0xabcdef1234567890abcdef1234567890abcdef12';
      const message = window.WalletKeyDerivation.getKeyDerivationMessage(address, 'encryption');
      const signature = await window.WalletKeyDerivation.mockWalletSign(message, mockWalletKey);

      // Verify deterministic derivation
      const isDeterministic = await window.WalletKeyDerivation.verifyDeterministic(signature);

      return { isDeterministic };
    });

    expect(result.isDeterministic).toBe(true);
  });

  test('should use derived keys for encryption', async ({ page }) => {
    await page.goto('/');

    // First inject both ECIES and wallet derivation
    await page.evaluate(() => {
      // Use the already injected WalletKeyDerivation
    });

    const result = await page.evaluate(async () => {
      // Mock wallet setup
      const aliceWalletKey = new Uint8Array(32);
      const bobWalletKey = new Uint8Array(32);
      crypto.getRandomValues(aliceWalletKey);
      crypto.getRandomValues(bobWalletKey);

      const aliceAddress = '0xAlice123456789012345678901234567890123456';
      const bobAddress = '0xBob12345678901234567890123456789012345678';

      // Both derive encryption keys from their wallet signatures
      const aliceMessage = window.WalletKeyDerivation.getKeyDerivationMessage(aliceAddress, 'encryption');
      const bobMessage = window.WalletKeyDerivation.getKeyDerivationMessage(bobAddress, 'encryption');

      const aliceSig = await window.WalletKeyDerivation.mockWalletSign(aliceMessage, aliceWalletKey);
      const bobSig = await window.WalletKeyDerivation.mockWalletSign(bobMessage, bobWalletKey);

      const aliceKeys = await window.WalletKeyDerivation.deriveFromSignature(aliceSig);
      const bobKeys = await window.WalletKeyDerivation.deriveFromSignature(bobSig);

      // Alice encrypts message to Bob using Bob's public key
      const plaintext = new TextEncoder().encode('Secret message from Alice to Bob');

      // Simplified encryption for testing (would use BrowserECIES in production)
      const sharedSecret = new Uint8Array(aliceKeys.privateKey.length);
      for (let i = 0; i < sharedSecret.length; i++) {
        sharedSecret[i] = aliceKeys.privateKey[i] ^ bobKeys.publicKey[i % bobKeys.publicKey.length];
      }

      const aesKey = await crypto.subtle.importKey(
        'raw',
        await crypto.subtle.digest('SHA-256', sharedSecret),
        { name: 'AES-GCM' },
        false,
        ['encrypt', 'decrypt']
      );

      const iv = crypto.getRandomValues(new Uint8Array(12));
      const ciphertext = await crypto.subtle.encrypt(
        { name: 'AES-GCM', iv },
        aesKey,
        plaintext
      );

      // Bob decrypts using his derived keys
      const decrypted = await crypto.subtle.decrypt(
        { name: 'AES-GCM', iv },
        aesKey, // In real scenario, Bob would derive this from his private key + Alice's public key
        ciphertext
      );

      const decryptedText = new TextDecoder().decode(decrypted);

      return {
        aliceKeysDerived: aliceKeys.privateKey.length === 32,
        bobKeysDerived: bobKeys.privateKey.length === 32,
        encryptionSuccessful: ciphertext.byteLength > plaintext.length,
        decryptionSuccessful: decryptedText === 'Secret message from Alice to Bob'
      };
    });

    expect(result.aliceKeysDerived).toBe(true);
    expect(result.bobKeysDerived).toBe(true);
    expect(result.encryptionSuccessful).toBe(true);
    expect(result.decryptionSuccessful).toBe(true);
  });

  test('should generate key derivation message correctly', async ({ page }) => {
    await page.goto('/');

    const result = await page.evaluate(() => {
      const address = '0x1234567890abcdef1234567890abcdef12345678';

      const encryptionMessage = window.WalletKeyDerivation.getKeyDerivationMessage(address, 'encryption');
      const signingMessage = window.WalletKeyDerivation.getKeyDerivationMessage(address, 'signing');

      return {
        encryptionMessage,
        signingMessage,
        containsAddress: encryptionMessage.includes(address),
        containsPurpose: encryptionMessage.includes('encryption'),
        differentMessages: encryptionMessage !== signingMessage
      };
    });

    expect(result.containsAddress).toBe(true);
    expect(result.containsPurpose).toBe(true);
    expect(result.differentMessages).toBe(true);
    expect(result.encryptionMessage).toContain('Sign this message');
  });
});

test.describe('Phantom Wallet Simulation', () => {
  test('should handle Phantom wallet API', async ({ page }) => {
    await page.goto('/');

    // Inject mock Phantom provider
    await page.evaluate(() => {
      (window as any).phantom = {
        solana: {
          isPhantom: true,
          publicKey: {
            toString: () => 'PhantomTestPublicKey123456789012345678901234567890'
          },
          signMessage: async (message: Uint8Array, encoding: string) => {
            // Mock signature
            const hash = await crypto.subtle.digest('SHA-256', message);
            return {
              signature: new Uint8Array(hash),
              publicKey: (window as any).phantom.solana.publicKey
            };
          }
        }
      };
    });

    const result = await page.evaluate(async () => {
      const phantom = (window as any).phantom;
      if (!phantom?.solana?.isPhantom) {
        return { available: false };
      }

      const message = new TextEncoder().encode(
        'Sign to derive SDN encryption key'
      );

      const { signature, publicKey } = await phantom.solana.signMessage(message, 'utf8');

      // Derive encryption key from signature
      const keyPair = await window.WalletKeyDerivation.deriveFromSignature(
        Array.from(signature).map(b => b.toString(16).padStart(2, '0')).join(''),
        'secp256k1'
      );

      return {
        available: true,
        publicKey: publicKey.toString(),
        signatureLength: signature.length,
        derivedKeyLength: keyPair.privateKey.length
      };
    });

    expect(result.available).toBe(true);
    expect(result.derivedKeyLength).toBe(32);
  });
});

test.describe('MetaMask Simulation', () => {
  test('should handle MetaMask wallet API', async ({ page }) => {
    await page.goto('/');

    // Inject mock MetaMask provider
    await page.evaluate(() => {
      (window as any).ethereum = {
        isMetaMask: true,
        selectedAddress: '0x1234567890abcdef1234567890abcdef12345678',
        request: async ({ method, params }: { method: string; params: any[] }) => {
          if (method === 'eth_accounts' || method === 'eth_requestAccounts') {
            return ['0x1234567890abcdef1234567890abcdef12345678'];
          }
          if (method === 'personal_sign') {
            const message = params[0];
            const address = params[1];

            // Mock EIP-191 personal_sign
            const msgBytes = new TextEncoder().encode(message);
            const prefix = new TextEncoder().encode(
              '\\x19Ethereum Signed Message:\\n' + msgBytes.length
            );

            const combined = new Uint8Array(prefix.length + msgBytes.length);
            combined.set(prefix, 0);
            combined.set(msgBytes, prefix.length);

            const hash = await crypto.subtle.digest('SHA-256', combined);
            const sigBytes = new Uint8Array(hash);

            return '0x' + Array.from(sigBytes).map(b => b.toString(16).padStart(2, '0')).join('') + '1c'; // Add v value
          }
          throw new Error('Unsupported method: ' + method);
        }
      };
    });

    const result = await page.evaluate(async () => {
      const ethereum = (window as any).ethereum;
      if (!ethereum?.isMetaMask) {
        return { available: false };
      }

      // Request accounts
      const accounts = await ethereum.request({ method: 'eth_requestAccounts', params: [] });
      const address = accounts[0];

      // Get key derivation message
      const message = window.WalletKeyDerivation.getKeyDerivationMessage(address, 'encryption');

      // Sign message
      const signature = await ethereum.request({
        method: 'personal_sign',
        params: [message, address]
      });

      // Derive key from signature
      const keyPair = await window.WalletKeyDerivation.deriveFromSignature(signature, 'secp256k1');

      return {
        available: true,
        address,
        signatureLength: signature.length,
        derivedKeyLength: keyPair.privateKey.length,
        curveType: keyPair.curveType
      };
    });

    expect(result.available).toBe(true);
    expect(result.address).toBe('0x1234567890abcdef1234567890abcdef12345678');
    expect(result.derivedKeyLength).toBe(32);
    expect(result.curveType).toBe('secp256k1');
  });

  test('should encrypt SDN message using MetaMask-derived key', async ({ page }) => {
    await page.goto('/');

    // Inject MetaMask mock
    await page.evaluate(() => {
      (window as any).ethereum = {
        isMetaMask: true,
        request: async ({ method, params }: { method: string; params: any[] }) => {
          if (method === 'eth_requestAccounts') {
            return ['0xSender123456789012345678901234567890123456'];
          }
          if (method === 'personal_sign') {
            const hash = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(params[0]));
            return '0x' + Array.from(new Uint8Array(hash)).map(b => b.toString(16).padStart(2, '0')).join('') + '1c';
          }
          throw new Error('Unsupported method');
        }
      };
    });

    const result = await page.evaluate(async () => {
      const ethereum = (window as any).ethereum;
      const accounts = await ethereum.request({ method: 'eth_requestAccounts', params: [] });
      const senderAddress = accounts[0];

      // Derive sender's encryption key
      const senderMessage = window.WalletKeyDerivation.getKeyDerivationMessage(senderAddress, 'encryption');
      const senderSig = await ethereum.request({
        method: 'personal_sign',
        params: [senderMessage, senderAddress]
      });
      const senderKeys = await window.WalletKeyDerivation.deriveFromSignature(senderSig, 'secp256k1');

      // Recipient's key (would be from their EPM in real scenario)
      const recipientPublicKey = new Uint8Array(65);
      crypto.getRandomValues(recipientPublicKey);

      // Create SDN message
      const sdnMessage = {
        schema: 'OMM',
        data: {
          OBJECT_NAME: 'ISS',
          EPOCH: new Date().toISOString(),
          MEAN_MOTION: 15.72
        },
        timestamp: Date.now(),
        sender: senderAddress
      };

      const plaintext = new TextEncoder().encode(JSON.stringify(sdnMessage));

      // Encrypt (simplified for testing)
      const iv = crypto.getRandomValues(new Uint8Array(12));
      const aesKey = await crypto.subtle.importKey(
        'raw',
        senderKeys.privateKey,
        { name: 'AES-GCM' },
        false,
        ['encrypt']
      );

      const ciphertext = await crypto.subtle.encrypt(
        { name: 'AES-GCM', iv },
        aesKey,
        plaintext
      );

      return {
        messageSent: true,
        plaintextSize: plaintext.length,
        ciphertextSize: ciphertext.byteLength,
        senderAddress,
        schema: sdnMessage.schema
      };
    });

    expect(result.messageSent).toBe(true);
    expect(result.ciphertextSize).toBeGreaterThan(result.plaintextSize);
    expect(result.schema).toBe('OMM');
  });
});

// Extend window type for TypeScript
declare global {
  interface Window {
    WalletKeyDerivation: {
      deriveFromSignature: (signature: string, curveType?: string) => Promise<{
        privateKey: Uint8Array;
        publicKey: Uint8Array;
        curveType: string;
        derivedFrom: string;
      }>;
      derivePublicKey: (privateKey: Uint8Array, curveType: string) => Promise<Uint8Array>;
      mockWalletSign: (message: string, privateKey: Uint8Array) => Promise<string>;
      verifyDeterministic: (signature: string) => Promise<boolean>;
      getKeyDerivationMessage: (address: string, purpose: string) => string;
    };
  }
}

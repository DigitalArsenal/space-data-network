/**
 * Secure HD wallet key storage using IndexedDB + WebCrypto AES-GCM.
 *
 * Stores encrypted mnemonic phrases. All derived keys are re-derived on unlock.
 * Plaintext metadata (xpub, peerId) is stored for quick identity lookups
 * without decryption.
 */

import { openDB, type IDBPDatabase } from 'idb';

const DB_NAME = 'sdn-keystore';
const DB_VERSION = 1;
const STORE_NAME = 'wallets';
const PBKDF2_ITERATIONS = 600_000;

interface StoredWallet {
  id: string;
  encryptedMnemonic: ArrayBuffer;
  iv: Uint8Array;
  salt: Uint8Array;
  account: number;
  xpub: string;
  peerId: string;
  method: 'pin' | 'passkey' | 'password';
  createdAt: number;
}

export interface WalletMetadata {
  id: string;
  account: number;
  xpub: string;
  peerId: string;
  method: 'pin' | 'passkey' | 'password';
  createdAt: number;
}

export class HDKeyStore {
  private db: IDBPDatabase | null = null;

  /**
   * Open the key store database.
   */
  static async open(dbName: string = DB_NAME): Promise<HDKeyStore> {
    const store = new HDKeyStore();
    store.db = await openDB(dbName, DB_VERSION, {
      upgrade(db) {
        if (!db.objectStoreNames.contains(STORE_NAME)) {
          db.createObjectStore(STORE_NAME, { keyPath: 'id' });
        }
      },
    });
    return store;
  }

  /**
   * Store an encrypted mnemonic.
   * Returns the wallet ID.
   */
  async store(
    mnemonic: string,
    pin: string,
    account: number,
    xpub: string,
    peerId: string,
    method: 'pin' | 'passkey' | 'password' = 'pin',
  ): Promise<string> {
    if (!this.db) throw new Error('KeyStore not open');

    const id = crypto.randomUUID?.() || generateId();
    const salt = crypto.getRandomValues(new Uint8Array(32));
    const iv = crypto.getRandomValues(new Uint8Array(12));

    const aesKey = await deriveAesKey(pin, salt);
    const encoded = new TextEncoder().encode(mnemonic);
    const encryptedMnemonic = await crypto.subtle.encrypt(
      { name: 'AES-GCM', iv },
      aesKey,
      encoded,
    );

    const entry: StoredWallet = {
      id,
      encryptedMnemonic,
      iv,
      salt,
      account,
      xpub,
      peerId,
      method,
      createdAt: Date.now(),
    };

    await this.db.put(STORE_NAME, entry);
    return id;
  }

  /**
   * Retrieve and decrypt a mnemonic.
   */
  async retrieve(id: string, pin: string): Promise<string> {
    if (!this.db) throw new Error('KeyStore not open');

    const entry = await this.db.get(STORE_NAME, id) as StoredWallet | undefined;
    if (!entry) throw new Error('Wallet not found');

    const aesKey = await deriveAesKey(pin, entry.salt);
    const decrypted = await crypto.subtle.decrypt(
      { name: 'AES-GCM', iv: entry.iv.buffer as ArrayBuffer },
      aesKey,
      entry.encryptedMnemonic,
    );

    return new TextDecoder().decode(decrypted);
  }

  /**
   * Check if any stored wallet exists.
   */
  async hasStored(): Promise<boolean> {
    if (!this.db) throw new Error('KeyStore not open');
    const count = await this.db.count(STORE_NAME);
    return count > 0;
  }

  /**
   * List all stored wallet metadata (without decryption).
   */
  async listMetadata(): Promise<WalletMetadata[]> {
    if (!this.db) throw new Error('KeyStore not open');
    const entries = await this.db.getAll(STORE_NAME) as StoredWallet[];
    return entries.map(({ id, account, xpub, peerId, method, createdAt }) => ({
      id, account, xpub, peerId, method, createdAt,
    }));
  }

  /**
   * Get metadata for a specific wallet without decryption.
   */
  async getMetadata(id: string): Promise<WalletMetadata | null> {
    if (!this.db) throw new Error('KeyStore not open');
    const entry = await this.db.get(STORE_NAME, id) as StoredWallet | undefined;
    if (!entry) return null;
    const { account, xpub, peerId, method, createdAt } = entry;
    return { id, account, xpub, peerId, method, createdAt };
  }

  /**
   * Delete a stored wallet.
   */
  async delete(id: string): Promise<void> {
    if (!this.db) throw new Error('KeyStore not open');
    await this.db.delete(STORE_NAME, id);
  }

  /**
   * Close the database.
   */
  async close(): Promise<void> {
    this.db?.close();
    this.db = null;
  }
}

async function deriveAesKey(pin: string, salt: Uint8Array): Promise<CryptoKey> {
  const pinBytes = new TextEncoder().encode(pin);
  const baseKey = await crypto.subtle.importKey(
    'raw',
    pinBytes,
    'PBKDF2',
    false,
    ['deriveBits', 'deriveKey'],
  );
  return crypto.subtle.deriveKey(
    { name: 'PBKDF2', salt: salt.buffer as ArrayBuffer, iterations: PBKDF2_ITERATIONS, hash: 'SHA-256' },
    baseKey,
    { name: 'AES-GCM', length: 256 },
    false,
    ['encrypt', 'decrypt'],
  );
}

function generateId(): string {
  const rnd = crypto.getRandomValues(new Uint8Array(16));
  return Array.from(rnd).map(b => b.toString(16).padStart(2, '0')).join('');
}

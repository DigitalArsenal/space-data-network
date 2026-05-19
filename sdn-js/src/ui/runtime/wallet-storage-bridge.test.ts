import { describe, expect, it, vi } from 'vitest';
import {
  installWalletStorageDiskMirror,
  isPersistedWalletLocalStorageKey,
} from './wallet-storage-bridge';
import type { BackendResult, SdnBackend } from './sdn-backend';

describe('wallet localStorage disk mirror', () => {
  it('hydrates persisted wallet entries before hd-wallet-ui reads localStorage', async () => {
    const storage = new MemoryStorage();
    const saveWalletStorage = vi.fn(async (entries: Record<string, string | null>) => ok({
      encryptedAtRest: true,
      entries: Object.fromEntries(
        Object.entries(entries).filter((entry): entry is [string, string] => typeof entry[1] === 'string'),
      ),
    }));
    const backend = fakeBackend({
      getWalletStorage: async () => ok({
        encryptedAtRest: true,
        entries: {
          wallet_storage_metadata: '{"method":"pin"}',
          wallet_storage_encrypted: '{"ciphertext":"stored"}',
          passkey_credential: '{"id":"legacy"}',
          'not-wallet-state': 'ignored',
        },
      }),
      saveWalletStorage,
    });

    const bridge = await installWalletStorageDiskMirror(backend, storage as unknown as Storage);

    expect(storage.getItem('wallet_storage_metadata')).toBe('{"method":"pin"}');
    expect(storage.getItem('wallet_storage_encrypted')).toBe('{"ciphertext":"stored"}');
    expect(storage.getItem('passkey_credential')).toBe('{"id":"legacy"}');
    expect(storage.getItem('not-wallet-state')).toBeNull();
    expect(saveWalletStorage).not.toHaveBeenCalled();
    await bridge?.destroy();
  });

  it('mirrors only hd-wallet remember-wallet keys back to the desktop backend', async () => {
    const storage = new MemoryStorage();
    const saveWalletStorage = vi.fn(async (entries: Record<string, string | null>) => ok({
      encryptedAtRest: true,
      entries: {},
    }));
    const bridge = await installWalletStorageDiskMirror(fakeBackend({
      getWalletStorage: async () => ok({ encryptedAtRest: true, entries: {} }),
      saveWalletStorage,
    }), storage as unknown as Storage);

    storage.setItem('wallet_storage_passkey_credential', '{"id":"credential"}');
    storage.setItem('hd-wallet-wallets', '[{"name":"Operations"}]');
    storage.setItem('hd-wallet-price-cache-v1', '{"USD":1}');
    storage.removeItem('wallet_storage_passkey_credential');
    await bridge?.flush();

    expect(saveWalletStorage).toHaveBeenCalledTimes(1);
    expect(saveWalletStorage).toHaveBeenCalledWith({
      wallet_storage_passkey_credential: null,
      'hd-wallet-wallets': '[{"name":"Operations"}]',
    });
    await bridge?.destroy();
  });

  it('clears mirrored wallet keys when hd-wallet-ui clears browser storage', async () => {
    const storage = new MemoryStorage();
    storage.setItem('wallet_storage_metadata', '{"method":"pin"}');
    storage.setItem('wallet_storage_encrypted', '{"ciphertext":"old"}');
    storage.setItem('unrelated', 'keep out of disk mirror');
    const saveWalletStorage = vi.fn(async () => ok({ encryptedAtRest: true, entries: {} }));
    const bridge = await installWalletStorageDiskMirror(fakeBackend({
      getWalletStorage: async () => ok({ encryptedAtRest: true, entries: {} }),
      saveWalletStorage,
    }), storage as unknown as Storage);

    storage.clear();
    await bridge?.flush();

    expect(saveWalletStorage).toHaveBeenCalledWith({
      wallet_storage_metadata: null,
      wallet_storage_encrypted: null,
    });
    await bridge?.destroy();
  });

  it('recognizes the current and legacy hd-wallet-ui remember-wallet key set', () => {
    expect(isPersistedWalletLocalStorageKey('wallet_storage_metadata')).toBe(true);
    expect(isPersistedWalletLocalStorageKey('wallet_storage_passkey_credential')).toBe(true);
    expect(isPersistedWalletLocalStorageKey('encrypted_wallet')).toBe(true);
    expect(isPersistedWalletLocalStorageKey('passkey_wallet')).toBe(true);
    expect(isPersistedWalletLocalStorageKey('hd-wallet-active-accounts')).toBe(true);
    expect(isPersistedWalletLocalStorageKey('hd-wallet-price-cache-v1')).toBe(false);
  });
});

function fakeBackend(methods: {
  getWalletStorage: SdnBackend['getWalletStorage'];
  saveWalletStorage: SdnBackend['saveWalletStorage'];
}): SdnBackend {
  return {
    mode: 'desktop-local',
    getWalletStorage: methods.getWalletStorage,
    saveWalletStorage: methods.saveWalletStorage,
  } as Partial<SdnBackend> as SdnBackend;
}

function ok<T>(data: T): BackendResult<T> {
  return {
    ok: true,
    capability: { id: 'test', state: 'available' },
    data,
  };
}

class MemoryStorage {
  private readonly items = new Map<string, string>();

  get length(): number {
    return this.items.size;
  }

  key(index: number): string | null {
    return Array.from(this.items.keys())[index] ?? null;
  }

  getItem(key: string): string | null {
    return this.items.get(String(key)) ?? null;
  }

  setItem(key: string, value: string): void {
    this.items.set(String(key), String(value));
  }

  removeItem(key: string): void {
    this.items.delete(String(key));
  }

  clear(): void {
    this.items.clear();
  }
}

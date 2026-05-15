import type { SdnBackend } from './sdn-backend';

const WALLET_STORAGE_PREFIX = 'wallet_storage_';
const WALLET_STORAGE_EXACT_KEYS = new Set([
  'encrypted_wallet',
  'passkey_credential',
  'passkey_wallet',
  'hd-wallet-wallets',
  'hd-wallet-active-accounts',
  'hd-wallet-vcard-identity',
  'hd-wallet-messaging-key-config-v1',
  'wallet-pki-keys',
]);

interface StoragePatchState {
  backend: SdnBackend;
  storage: Storage;
  target: Storage;
  originalSetItem: Storage['setItem'];
  originalRemoveItem: Storage['removeItem'];
  originalClear: Storage['clear'];
  suppressWrites: number;
  pendingEntries: Record<string, string | null>;
  flushScheduled: boolean;
  flushPromise: Promise<void> | null;
  refCount: number;
}

export interface WalletStorageDiskMirror {
  flush(): Promise<void>;
  destroy(): Promise<void>;
}

const patchStates = new WeakMap<Storage, StoragePatchState>();

export function isPersistedWalletLocalStorageKey(key: string): boolean {
  return key.startsWith(WALLET_STORAGE_PREFIX) || WALLET_STORAGE_EXACT_KEYS.has(key);
}

export async function installWalletStorageDiskMirror(
  backend: SdnBackend | null | undefined,
  storage: Storage | null | undefined = defaultLocalStorage(),
): Promise<WalletStorageDiskMirror | null> {
  if (!backend || backend.mode !== 'desktop-local' || !storage) return null;

  const state = patchStorage(storage, backend);
  const snapshot = await backend.getWalletStorage();
  if (snapshot.ok && snapshot.data) {
    withSuppressedWrites(state, () => {
      for (const [key, value] of Object.entries(snapshot.data?.entries ?? {})) {
        if (isPersistedWalletLocalStorageKey(key)) {
          storage.setItem(key, value);
        }
      }
    });
  }

  return {
    flush: () => flushWalletStorageMirror(state),
    async destroy() {
      await flushWalletStorageMirror(state);
      unpatchStorage(state);
    },
  };
}

function defaultLocalStorage(): Storage | null {
  try {
    return typeof globalThis.localStorage !== 'undefined' ? globalThis.localStorage : null;
  } catch {
    return null;
  }
}

function patchStorage(storage: Storage, backend: SdnBackend): StoragePatchState {
  const existing = patchStates.get(storage);
  if (existing) {
    existing.backend = backend;
    existing.refCount += 1;
    return existing;
  }

  const target = storagePatchTarget(storage);
  const state: StoragePatchState = {
    backend,
    storage,
    target,
    originalSetItem: target.setItem,
    originalRemoveItem: target.removeItem,
    originalClear: target.clear,
    suppressWrites: 0,
    pendingEntries: {},
    flushScheduled: false,
    flushPromise: null,
    refCount: 1,
  };

  target.setItem = function setItem(this: Storage, key: string, value: string): void {
    state.originalSetItem.call(this, key, value);
    if (this === storage && state.suppressWrites === 0 && isPersistedWalletLocalStorageKey(String(key))) {
      queueWalletStoragePersist(state, String(key), String(value));
    }
  };
  target.removeItem = function removeItem(this: Storage, key: string): void {
    state.originalRemoveItem.call(this, key);
    if (this === storage && state.suppressWrites === 0 && isPersistedWalletLocalStorageKey(String(key))) {
      queueWalletStoragePersist(state, String(key), null);
    }
  };
  target.clear = function clear(this: Storage): void {
    const walletKeys = this === storage ? currentPersistedWalletKeys(storage) : [];
    state.originalClear.call(this);
    if (this === storage && state.suppressWrites === 0) {
      for (const key of walletKeys) {
        queueWalletStoragePersist(state, key, null);
      }
    }
  };

  patchStates.set(storage, state);
  return state;
}

function storagePatchTarget(storage: Storage): Storage {
  const prototype = Object.getPrototypeOf(storage) as Storage | null;
  if (
    prototype &&
    typeof prototype.setItem === 'function' &&
    typeof prototype.removeItem === 'function' &&
    typeof prototype.clear === 'function'
  ) {
    return prototype;
  }
  return storage;
}

function withSuppressedWrites(state: StoragePatchState, callback: () => void): void {
  state.suppressWrites += 1;
  try {
    callback();
  } finally {
    state.suppressWrites -= 1;
  }
}

function currentPersistedWalletKeys(storage: Storage): string[] {
  const keys: string[] = [];
  for (let index = 0; index < storage.length; index += 1) {
    const key = storage.key(index);
    if (key && isPersistedWalletLocalStorageKey(key)) keys.push(key);
  }
  return keys;
}

function queueWalletStoragePersist(state: StoragePatchState, key: string, value: string | null): void {
  state.pendingEntries[key] = value;
  if (state.flushScheduled) return;
  state.flushScheduled = true;
  queueMicrotask(() => {
    void flushWalletStorageMirror(state);
  });
}

async function flushWalletStorageMirror(state: StoragePatchState): Promise<void> {
  if (state.flushPromise) {
    await state.flushPromise;
    return flushWalletStorageMirror(state);
  }
  const entries = state.pendingEntries;
  state.pendingEntries = {};
  state.flushScheduled = false;
  if (Object.keys(entries).length === 0) return;
  state.flushPromise = state.backend.saveWalletStorage(entries)
    .then(() => undefined)
    .catch((error) => {
      console.warn('[sdn wallet] failed to persist wallet storage mirror:', error);
    })
    .finally(() => {
      state.flushPromise = null;
    });
  await state.flushPromise;
  if (Object.keys(state.pendingEntries).length > 0) {
    await flushWalletStorageMirror(state);
  }
}

function unpatchStorage(state: StoragePatchState): void {
  state.refCount -= 1;
  if (state.refCount > 0) return;
  state.target.setItem = state.originalSetItem;
  state.target.removeItem = state.originalRemoveItem;
  state.target.clear = state.originalClear;
  patchStates.delete(state.storage);
}

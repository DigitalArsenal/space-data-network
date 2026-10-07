/**
 * Where a signed-in dashboard keeps its sealed session between page loads
 * (owner 2026-10-07: "I don't want to have to login every time ... maybe
 * store the key in local storage but encrypted").
 *
 * The session key's seed is stored encrypted with AES-GCM under a key the
 * page can use but never read: WebCrypto makes it non-extractable, and
 * IndexedDB keeps the key object, never its bytes. The ciphertext is bound
 * to this origin, the node's fingerprint, the session public key and the
 * expiry, so it opens only for the same node and the same session. A record
 * that is expired, for another node, or does not open is deleted on sight.
 */

export interface SessionVault {
  get(key: string): Promise<any>;
  put(key: string, value: any): Promise<void>;
  delete(key: string): Promise<void>;
}

export interface SessionToStore {
  origin: string;
  fingerprint: string;
  sessionPubHex: string;
  expiresAtMs: number;
  seed: Uint8Array;
  /** What the page shows about the wallet. Never a secret. */
  facts?: unknown;
}

export interface StoredSession {
  seed: Uint8Array;
  sessionPubHex: string;
  expiresAtMs: number;
  facts?: unknown;
}

const DATABASE = 'sdn-sealed-session';
const STORE = 'vault';
const WRAP_KEY = 'wrap-key';
const SESSION = 'session';
/** A session this close to its end is not worth resuming. */
const RESUME_MARGIN_MS = 60_000;

/** The browser's IndexedDB as a vault, or null where there is none. */
export function indexedDbVault(idb: IDBFactory | undefined = globalThis.indexedDB): SessionVault | null {
  if (!idb) return null;
  const open = () => new Promise<IDBDatabase>((resolve, reject) => {
    const request = idb.open(DATABASE, 1);
    request.onupgradeneeded = () => request.result.createObjectStore(STORE);
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
  });
  const run = async (mode: IDBTransactionMode, step: (store: IDBObjectStore) => IDBRequest): Promise<any> => {
    const db = await open();
    try {
      return await new Promise((resolve, reject) => {
        const tx = db.transaction(STORE, mode);
        const request = step(tx.objectStore(STORE));
        tx.oncomplete = () => resolve(request.result);
        tx.onerror = () => reject(tx.error);
        tx.onabort = () => reject(tx.error);
      });
    } finally {
      db.close();
    }
  };
  return {
    get: (key) => run('readonly', (store) => store.get(key)),
    put: async (key, value) => { await run('readwrite', (store) => store.put(value, key)); },
    delete: async (key) => { await run('readwrite', (store) => store.delete(key)); },
  };
}

const boundTo = (r: { origin: string; fingerprint: string; sessionPubHex: string; expiresAtMs: number }) =>
  new TextEncoder().encode(`${r.origin}\n${r.fingerprint}\n${r.sessionPubHex}\n${r.expiresAtMs}`);

async function wrapKey(vault: SessionVault, create: boolean): Promise<CryptoKey | null> {
  const existing = await vault.get(WRAP_KEY);
  if (existing || !create) return existing ?? null;
  const key = await crypto.subtle.generateKey({ name: 'AES-GCM', length: 256 }, false, ['encrypt', 'decrypt']);
  await vault.put(WRAP_KEY, key);
  return key;
}

/** Keep one session, replacing any earlier one. */
export async function storeSession(vault: SessionVault, session: SessionToStore): Promise<void> {
  const key = await wrapKey(vault, true);
  if (!key) throw new Error('no key to seal the session with');
  const iv = crypto.getRandomValues(new Uint8Array(12));
  const record = {
    v: 1,
    origin: session.origin,
    fingerprint: session.fingerprint,
    sessionPubHex: session.sessionPubHex,
    expiresAtMs: session.expiresAtMs,
    facts: session.facts ?? null,
    iv,
  };
  const ciphertext = new Uint8Array(await crypto.subtle.encrypt({ name: 'AES-GCM', iv, additionalData: boundTo(record) }, key, new Uint8Array(session.seed)));
  await vault.put(SESSION, { ...record, ciphertext });
}

/** The stored session for this origin and node, while it lasts. */
export async function loadSession(vault: SessionVault, where: { origin: string; fingerprint: string; nowMs: number }): Promise<StoredSession | null> {
  const record = await vault.get(SESSION);
  if (!record) return null;
  const usable = record.v === 1 && record.origin === where.origin && record.fingerprint === where.fingerprint
    && Number(record.expiresAtMs) > where.nowMs + RESUME_MARGIN_MS;
  const key = usable ? await wrapKey(vault, false) : null;
  if (key) {
    try {
      const seed = new Uint8Array(await crypto.subtle.decrypt({ name: 'AES-GCM', iv: record.iv, additionalData: boundTo(record) }, key, record.ciphertext));
      return { seed, sessionPubHex: record.sessionPubHex, expiresAtMs: Number(record.expiresAtMs), facts: record.facts ?? undefined };
    } catch {
      /* not this key's ciphertext: deleted below */
    }
  }
  await vault.delete(SESSION);
  return null;
}

export async function clearSession(vault: SessionVault): Promise<void> {
  await vault.delete(SESSION);
}

/**
 * The dashboard's sealed admin session (node-owned runtime; the dashboard
 * imports it as `sdn-node-sealed-runtime`).
 *
 * On a remote node every admin command goes through POST /api/rpc, signed by
 * a session key the wallet delegated once at sign-in and sealed to the node's
 * advertised encryption key (sdn-server internal/sealed, auth delegation.go).
 * This module holds that session and installs a fetch wrapper so every
 * same-origin /api/ call the dashboard makes travels sealed, without each
 * call site knowing. The session outlives a page load: rememberSession keeps
 * it encrypted in this browser (sealed-session-store.ts), resumeSession picks
 * it up again, and sign-out (revoke) deletes it.
 */

import { call, createSessionSigner, REVOKE_ROUTE, RPC_ROUTE, sessionSignerFromSeed, type NodeTransport, type RpcSigner } from '../../sealed-rpc';
import { sha256, useHDWalletModule } from '../../crypto/hd-wallet';
import { clearSession, indexedDbVault, loadSession, storeSession, type SessionVault } from './sealed-session-store';

/** Use the wallet module the page already loaded (see useHDWalletModule). */
export const useWalletModule = useHDWalletModule;

export const DELEGATION_PREFIX = 'SDN-RPC-DELEGATION/v1';
/** A week, under the node's 8-day ceiling (auth delegation.go MaxDelegation). */
export const DELEGATION_MS = 7 * 24 * 60 * 60 * 1000;
const PIN_KEY = 'sdn.sealed.fingerprint';

/**
 * Requests that stay plain: sign-in itself, the public node descriptor, and
 * the sealed route. Everything else under /api/ is sealed once a session is
 * active.
 */
const PLAIN = new Set(['/api/auth/challenge', '/api/auth/verify', '/api/auth/delegate', '/api/node/info', RPC_ROUTE]);

/** A dashboard is remote unless it runs in the desktop app or on this box. */
export function isRemoteDashboard(loc: { hostname: string } = globalThis.location, win: any = globalThis): boolean {
  if (win?.sdn?.isDesktop) return false;
  const host = String(loc?.hostname ?? '').replace(/^\[|\]$/g, '').toLowerCase();
  return !(host === 'localhost' || host === '::1' || /^127\./.test(host));
}

function fromHex(hex: string): Uint8Array {
  const clean = String(hex ?? '').trim();
  if (!/^([0-9a-f]{2})+$/i.test(clean)) throw new Error('malformed hex');
  const out = new Uint8Array(clean.length / 2);
  for (let i = 0; i < out.length; i++) out[i] = parseInt(clean.slice(i * 2, i * 2 + 2), 16);
  return out;
}

export function toHex(bytes: Uint8Array): string {
  return Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
}

/** The node's sealed-transport keys, from its public descriptor. */
export async function readNodeTransport(origin: string, fetchImpl: typeof fetch = fetch): Promise<NodeTransport> {
  const response = await fetchImpl(`${origin}/api/node/info`, { credentials: 'omit', cache: 'no-store' });
  if (!response.ok) throw new Error(`node info: HTTP ${response.status}`);
  const info = await response.json();
  const t = info?.sealed_transport;
  if (!t?.encryption_key || !t?.signing_key || t?.key_exchange !== 'Secp256k1') {
    throw new Error('This node does not offer the sealed admin transport.');
  }
  return { encryptionKey: fromHex(t.encryption_key), signingKey: fromHex(t.signing_key), fingerprint: String(t.fingerprint ?? '') };
}

/** Compare the node's fingerprint with the one this browser pinned for it. */
export function checkPin(origin: string, fingerprint: string, storage: Pick<Storage, 'getItem'> | null = safeStorage()): 'match' | 'new' | 'changed' {
  let pins: Record<string, string> = {};
  try {
    pins = JSON.parse(storage?.getItem(PIN_KEY) ?? '{}') ?? {};
  } catch {
    pins = {};
  }
  const pinned = pins[origin];
  if (!pinned) return 'new';
  return pinned === fingerprint ? 'match' : 'changed';
}

/** Remember the fingerprint an admin confirmed for this node. */
export function pinFingerprint(origin: string, fingerprint: string, storage: Pick<Storage, 'getItem' | 'setItem'> | null = safeStorage()): void {
  try {
    const pins = JSON.parse(storage?.getItem(PIN_KEY) ?? '{}') ?? {};
    pins[origin] = fingerprint;
    storage?.setItem(PIN_KEY, JSON.stringify(pins));
  } catch {
    /* no storage: the check repeats next time */
  }
}

function safeStorage(): Storage | null {
  try {
    return globalThis.localStorage ?? null;
  } catch {
    return null;
  }
}

/** The 32 bytes the wallet signs to delegate a session key (auth/delegation.go). */
export async function delegationDigest(challenge: Uint8Array, sessionPub: Uint8Array, expiresAtMs: number): Promise<Uint8Array> {
  const prefix = new TextEncoder().encode(DELEGATION_PREFIX);
  const expiry = new Uint8Array(8);
  new DataView(expiry.buffer).setBigUint64(0, BigInt(expiresAtMs));
  const input = new Uint8Array(prefix.length + challenge.length + sessionPub.length + 8);
  input.set(prefix, 0);
  input.set(challenge, prefix.length);
  input.set(sessionPub, prefix.length + challenge.length);
  input.set(expiry, prefix.length + challenge.length + sessionPub.length);
  return sha256(input);
}

type SessionSigner = RpcSigner & { seed: Uint8Array };

export interface PendingSession {
  signer: SessionSigner;
  sessionPubHex: string;
  expiresAtMs: number;
}

/** Make the throwaway session key the wallet will delegate. */
export async function beginSession(nowMs = Date.now()): Promise<PendingSession> {
  const signer = await createSessionSigner();
  return { signer, sessionPubHex: toHex(signer.publicKey), expiresAtMs: nowMs + DELEGATION_MS };
}

interface ActiveSession {
  origin: string;
  node: NodeTransport;
  signer: SessionSigner;
  sessionId: string;
  expiresAtMs: number;
}

let active: ActiveSession | null = null;

/** Start sealing: the node accepted the delegation. */
export function activate(origin: string, node: NodeTransport, pending: PendingSession): void {
  active = { origin, node, signer: pending.signer, sessionId: pending.sessionPubHex.slice(0, 16), expiresAtMs: pending.expiresAtMs };
}

export function sealedActive(nowMs = Date.now()): boolean {
  return Boolean(active && active.expiresAtMs > nowMs);
}

let defaultVault: SessionVault | null | undefined;
const browserVault = (): SessionVault | null => (defaultVault === undefined ? (defaultVault = indexedDbVault()) : defaultVault);

/**
 * Keep the active session in this browser so the next page load resumes it.
 * `facts` is what the page shows about the wallet; it must hold no secret.
 * Resolves false where the browser keeps nothing (no IndexedDB, private mode).
 */
export async function rememberSession(facts?: unknown, vault: SessionVault | null = browserVault()): Promise<boolean> {
  if (!active || !vault) return false;
  try {
    await storeSession(vault, {
      origin: active.origin,
      fingerprint: active.node.fingerprint,
      sessionPubHex: toHex(active.signer.publicKey),
      expiresAtMs: active.expiresAtMs,
      seed: active.signer.seed,
      facts,
    });
    return true;
  } catch {
    return false;
  }
}

/**
 * Resume the session this browser kept for this node, if it still lasts.
 * `prepare` runs only when there is one to resume (loading the wallet module
 * the session key signs with). Resolves the facts kept with it and when the
 * session ends, or null.
 * The caller confirms the node still knows the session, and calls
 * forgetSession when it does not.
 */
export async function resumeSession(
  origin: string,
  node: NodeTransport,
  { prepare, vault = browserVault(), nowMs = Date.now() }: { prepare?: () => Promise<void>; vault?: SessionVault | null; nowMs?: number } = {},
): Promise<{ facts: unknown; expiresAtMs: number } | null> {
  if (!vault) return null;
  try {
    const stored = await loadSession(vault, { origin, fingerprint: node.fingerprint, nowMs });
    if (!stored) return null;
    await prepare?.();
    const signer = await sessionSignerFromSeed(stored.seed);
    if (toHex(signer.publicKey) !== stored.sessionPubHex) {
      await clearSession(vault);
      return null;
    }
    active = { origin, node, signer, sessionId: stored.sessionPubHex.slice(0, 16), expiresAtMs: stored.expiresAtMs };
    return { facts: stored.facts ?? null, expiresAtMs: stored.expiresAtMs };
  } catch {
    return null;
  }
}

/** Drop the session here and the copy this browser kept. */
export async function forgetSession(vault: SessionVault | null = browserVault()): Promise<void> {
  active = null;
  if (!vault) return;
  try {
    await clearSession(vault);
  } catch {
    /* nothing kept */
  }
}

/** Send one request sealed, as a fetch Response. */
export async function sealedFetch(route: string, init: RequestInit = {}, fetchImpl: typeof fetch = fetch): Promise<Response> {
  if (!active) throw new Error('No sealed admin session.');
  const method = String(init.method ?? 'GET').toUpperCase();
  let body: Uint8Array | undefined;
  if (init.body != null) {
    body = typeof init.body === 'string'
      ? new TextEncoder().encode(init.body)
      : new Uint8Array(await new Response(init.body as BodyInit).arrayBuffer());
  }
  const headers = new Headers(init.headers ?? {});
  const result = await call(active.origin, active.node, active.signer, {
    method, route, body, contentType: headers.get('Content-Type') ?? undefined,
  }, { sessionId: active.sessionId, fetchImpl });
  return new Response(result.body.length ? arrayBufferOf(result.body) : null, {
    status: result.status || 502,
    headers: result.contentType ? { 'Content-Type': result.contentType } : {},
  });
}

/** A standalone ArrayBuffer copy: what fetch/Response accept as a body. */
function arrayBufferOf(bytes: Uint8Array): ArrayBuffer {
  return bytes.slice().buffer as ArrayBuffer;
}

/** End the session here, on the node and in this browser's storage. */
export async function revoke(fetchImpl: typeof fetch = fetch): Promise<void> {
  if (active) {
    try {
      await sealedFetch(REVOKE_ROUTE, { method: 'POST' }, fetchImpl);
    } catch {
      /* the delegation expires on its own */
    }
  }
  await forgetSession();
}

/**
 * Route every same-origin /api/ fetch through the sealed transport while a
 * session is active. Returns a function that removes the wrapper.
 */
export function installSealedFetch(win: any = globalThis): () => void {
  const original: typeof fetch = win.fetch.bind(win);
  const wrapped = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const url = new URL(typeof input === 'string' || input instanceof URL ? String(input) : input.url, win.location?.href);
    const sameOrigin = url.origin === win.location?.origin;
    if (!sameOrigin || !url.pathname.startsWith('/api/') || PLAIN.has(url.pathname) || !sealedActive()) {
      return original(input, init);
    }
    if (input instanceof Request && !init) {
      init = { method: input.method, headers: input.headers, body: input.method === 'GET' || input.method === 'HEAD' ? undefined : await input.arrayBuffer() };
    }
    return sealedFetch(url.pathname + url.search, init ?? {}, original);
  };
  win.fetch = wrapped;
  return () => {
    if (win.fetch === wrapped) win.fetch = original;
  };
}

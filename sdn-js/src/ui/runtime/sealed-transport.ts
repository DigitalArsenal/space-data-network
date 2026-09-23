/**
 * The dashboard's sealed admin session (node-owned runtime; the dashboard
 * imports it as `sdn-node-sealed-runtime`).
 *
 * On a remote node every admin command goes through POST /api/rpc, signed by
 * an in-memory session key the wallet delegated once at sign-in and sealed to
 * the node's advertised encryption key (sdn-server internal/sealed, auth
 * delegation.go). This module holds that session and installs a fetch wrapper
 * so every same-origin /api/ call the dashboard makes travels sealed, without
 * each call site knowing.
 */

import { call, createSessionSigner, REVOKE_ROUTE, RPC_ROUTE, type NodeTransport, type RpcSigner } from '../../sealed-rpc';
import { initHDWallet, sha256 } from '../../crypto/hd-wallet';

export const DELEGATION_PREFIX = 'SDN-RPC-DELEGATION/v1';
/** Kept under the node's 12-hour ceiling. */
export const DELEGATION_MS = 8 * 60 * 60 * 1000;
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

export interface PendingSession {
  signer: RpcSigner;
  sessionPubHex: string;
  expiresAtMs: number;
}

/** Make the throwaway session key the wallet will delegate. */
export async function beginSession(nowMs = Date.now()): Promise<PendingSession> {
  await initHDWallet();
  const signer = await createSessionSigner();
  return { signer, sessionPubHex: toHex(signer.publicKey), expiresAtMs: nowMs + DELEGATION_MS };
}

interface ActiveSession {
  origin: string;
  node: NodeTransport;
  signer: RpcSigner;
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
  return new Response(result.body.length ? result.body : null, {
    status: result.status || 502,
    headers: result.contentType ? { 'Content-Type': result.contentType } : {},
  });
}

/** End the session here and on the node. */
export async function revoke(fetchImpl: typeof fetch = fetch): Promise<void> {
  if (!active) return;
  try {
    await sealedFetch(REVOKE_ROUTE, { method: 'POST' }, fetchImpl);
  } catch {
    /* the delegation expires on its own */
  } finally {
    active = null;
  }
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

/*
 * hd-wallet-wasm sign-in for the dashboard (graph task
 * nst-node-edit-permissions-ui deliverable 1; wire contract:
 * graph/tasks/nst-node-admin-contract.md §1–§5, §7, §9).
 *
 * THE SEED NEVER LEAVES THE PAGE. The recovery phrase is read from a form
 * field, turned into a seed, turned into keys, and dropped. The node only
 * ever sees `xpub`, `client_pubkey_hex` and a signature — never the phrase,
 * never the seed, never a private key. Nothing here writes localStorage,
 * sessionStorage, IndexedDB or any network body carrying key material.
 *
 * Loading: the wallet ES module is imported LAZILY from the node's OWN
 * origin (`/wallet-wasm/runtime/index.mjs`, contract §1) the first time an
 * operator opens sign-in. It is deliberately NOT inlined into the
 * single-file page: hd-wallet.js is 5.2 MB (the .wasm rides in it as a
 * data: URI), which would multiply the embedded homepage ~20x for a
 * surface most visitors never touch. Same-origin only — if the assets are
 * unstaged the node answers 404 and the UI reports "sign-in unavailable".
 * Reaching a CDN would be a CSP defect, not a degradation (contract §1).
 */

/** Contract §1: the ONE entry point. hd-wallet.js is imported by it, relatively. */
export const WALLET_ENTRY = '/wallet-wasm/runtime/index.mjs';

/** Contract §2 derivation paths for BIP-44 account index N. */
export function accountPaths(account = 0) {
  const n = Number.isInteger(account) && account >= 0 ? account : 0;
  return {
    /** secp256k1 BIP-32 — this IS the xpub / PeerID identity. */
    identity: `m/44'/0'/${n}'`,
    /** Ed25519 SLIP-10, fully hardened — the auth signing key. */
    signing: `m/44'/0'/${n}'/0'/0'`,
    /** X25519 — not used by sign-in, documented for completeness. */
    encryption: `m/44'/0'/${n}'/1'/0'`,
  };
}

/** Lowercase hex of a byte sequence. */
export function toHex(bytes) {
  let out = '';
  for (const b of bytes) out += b.toString(16).padStart(2, '0');
  return out;
}

/** Hex (with or without 0x) → Uint8Array. Returns null on malformed input. */
export function fromHex(hex) {
  const clean = String(hex ?? '').trim().replace(/^0x/i, '');
  if (clean.length === 0 || clean.length % 2 !== 0 || /[^0-9a-fA-F]/.test(clean)) return null;
  const out = new Uint8Array(clean.length / 2);
  for (let i = 0; i < out.length; i += 1) out[i] = parseInt(clean.slice(i * 2, i * 2 + 2), 16);
  return out;
}

/**
 * Decode the node's challenge. It is base64 RawStdEncoding — standard
 * alphabet, NO '=' padding (contract §4). atob() needs padding, so a LOCAL
 * COPY is padded here; the original string must be echoed back VERBATIM.
 */
export function decodeChallengeB64(challenge) {
  const raw = String(challenge ?? '');
  if (!raw) return null;
  let padded = raw;
  while (padded.length % 4 !== 0) padded += '=';
  let bin;
  try {
    bin = atob(padded);
  } catch {
    return null;
  }
  return Uint8Array.from(bin, (c) => c.charCodeAt(0));
}

/**
 * POST /api/auth/challenge body (contract §4).
 * `ts` is Unix SECONDS. `xpub` is always sent: the node then resolves the
 * user by xpub, which is the only path that works for a config/admin-added
 * user whose signing key is not bound yet (TOFU, §5.3) and the only path
 * that works at all for first-admin bootstrap (§5.1, where it is MANDATORY).
 */
export function challengeRequestBody({ clientPubKeyHex, xpub, nowMs = Date.now() }) {
  const body = {
    client_pubkey_hex: String(clientPubKeyHex ?? '').trim().toLowerCase(),
    ts: Math.floor(nowMs / 1000),
  };
  const x = String(xpub ?? '').trim();
  if (x) body.xpub = x;
  return body;
}

/**
 * POST /api/auth/verify body (contract §4).
 * `challenge` is echoed EXACTLY as received — unpadded. The server decodes
 * it with RawStdEncoding, which REJECTS '=' padding.
 */
export function verifyRequestBody({ challengeId, clientPubKeyHex, challenge, signatureHex, xpub }) {
  const body = {
    challenge_id: String(challengeId ?? ''),
    client_pubkey_hex: String(clientPubKeyHex ?? '').trim().toLowerCase(),
    challenge: String(challenge ?? ''),
    signature_hex: String(signatureHex ?? '').trim().toLowerCase(),
  };
  const x = String(xpub ?? '').trim();
  if (x) body.xpub = x;
  return body;
}

/**
 * Attestation signing preimage (contract §7 / internal/auth/attestation.go
 * canonicalBytes) — NOT the raw-challenge form of §3:
 *
 *   u32be len | XPub  ‖  u32be len | Claim  ‖  i64be IssuedAt.UnixNano  ‖  u32be len | Nonce
 *
 * @param {{xpub: string, claim: string, issuedAtSeconds: number, nonce: Uint8Array}} att
 */
export function attestationPreimage({ xpub, claim, issuedAtSeconds, nonce }) {
  const enc = new TextEncoder();
  const x = enc.encode(String(xpub ?? ''));
  const c = enc.encode(String(claim ?? ''));
  const n = nonce instanceof Uint8Array ? nonce : new Uint8Array(0);
  const out = new Uint8Array(4 + x.length + 4 + c.length + 8 + 4 + n.length);
  const view = new DataView(out.buffer);
  let off = 0;
  view.setUint32(off, x.length, false);
  off += 4;
  out.set(x, off);
  off += x.length;
  view.setUint32(off, c.length, false);
  off += 4;
  out.set(c, off);
  off += c.length;
  view.setBigInt64(off, BigInt(Math.trunc(issuedAtSeconds ?? 0)) * 1000000000n, false);
  off += 8;
  view.setUint32(off, n.length, false);
  off += 4;
  out.set(n, off);
  return out;
}

/** RFC3339 with SECOND precision, which is what /api/auth/attest parses. */
export function rfc3339Seconds(unixSeconds) {
  return new Date(Math.trunc(unixSeconds) * 1000).toISOString().replace(/\.\d{3}Z$/, 'Z');
}

/**
 * `xpub_fingerprint` (contract §4, added 2026-07-26): the first 8 bytes of
 * SHA-256 over the exact xpub string, lowercase hex — 16 chars.
 *
 * The node deliberately NEVER returns the raw xpub (an extended public key
 * enumerates the identity's whole derived address subtree, so a stolen
 * session cookie would become a permanent chain-privacy compromise). To
 * confirm a session belongs to the key this page just unlocked, we hash our
 * OWN xpub the same way and compare.
 *
 * Returns '' when WebCrypto's SubtleCrypto is unavailable — a page served
 * over plain http to a non-localhost address is not a secure context, and
 * "cannot compute" must not masquerade as "mismatch".
 */
export async function xpubFingerprint(xpub) {
  const value = String(xpub ?? '').trim();
  const subtle = globalThis.crypto?.subtle;
  if (!value || !subtle) return '';
  try {
    const digest = await subtle.digest('SHA-256', new TextEncoder().encode(value));
    return toHex(new Uint8Array(digest).subarray(0, 8));
  } catch {
    return '';
  }
}

/**
 * Compare a locally computed fingerprint with the node's.
 * UNVERIFIABLE IS NOT A MISMATCH: an absent local fingerprint (no in-page
 * key, or no SubtleCrypto) or an absent server fingerprint (older node)
 * leaves the session as the node reported it. Only two present, differing
 * values mean "this session is not the key you unlocked".
 */
export function fingerprintMatches(local, remote) {
  const a = String(local ?? '').trim().toLowerCase();
  const b = String(remote ?? '').trim().toLowerCase();
  if (!a || !b) return true;
  return a === b;
}

/** Chip form of a 16-hex fingerprint: first 8 chars + ellipsis. */
export function shortFingerprint(fingerprint) {
  const fp = String(fingerprint ?? '').trim().toLowerCase();
  if (!fp) return '';
  return fp.length <= 8 ? fp : `${fp.slice(0, 8)}…`;
}

// ---------------------------------------------------------------------------
// Runtime (browser-only below this line)
// ---------------------------------------------------------------------------

let walletPromise = null;

/**
 * Lazily import + initialize hd-wallet-wasm from the node's own origin.
 * Rejects (and forgets the attempt, so a later retry re-tries) when the
 * assets are unstaged.
 */
export function loadWallet() {
  if (!walletPromise) {
    const entry = WALLET_ENTRY;
    walletPromise = (async () => {
      const ns = await import(/* @vite-ignore */ entry);
      if (typeof ns.default !== 'function') throw new Error('wallet entry point has no init()');
      const mod = await ns.default();
      return { mod, Curve: ns.Curve };
    })();
    walletPromise.catch(() => {
      walletPromise = null;
    });
  }
  return walletPromise;
}

/** Best-effort zeroing of key material we are done with. */
function wipe(...items) {
  for (const item of items) {
    try {
      if (item instanceof Uint8Array) item.fill(0);
      else if (item && typeof item.wipe === 'function') item.wipe();
    } catch {
      /* wiping is best-effort; never let it break the flow */
    }
  }
}

/**
 * Derive the node-compatible identity from a recovery phrase (contract §2).
 * Returns an object holding ONLY the public identity plus an in-memory
 * signer. Seed, master key and account key are wiped before returning; the
 * 32-byte Ed25519 signing seed is retained in this closure so the operator
 * can attest (§7) without re-entering the phrase, and is zeroed by
 * `destroy()` (called on sign-out).
 */
export async function deriveIdentity(mnemonic, account = 0) {
  const { mod, Curve } = await loadWallet();
  const phrase = String(mnemonic ?? '').trim().replace(/\s+/g, ' ');
  if (!phrase) throw new Error('Enter your recovery phrase.');
  if (typeof mod.mnemonic?.validate === 'function' && !mod.mnemonic.validate(phrase)) {
    throw new Error('That is not a valid BIP-39 recovery phrase.');
  }

  const paths = accountPaths(account);
  const seed = mod.mnemonic.toSeed(phrase, '');
  let master = null;
  let acct = null;
  let neutered = null;
  let xpub = '';
  let peerId = '';
  let sk = null;
  try {
    master = mod.hdkey.fromSeed(seed, Curve.SECP256K1);
    acct = master.derivePath(paths.identity);
    neutered = acct.neutered();
    xpub = neutered.toXpub();
    peerId = mod.libp2p.peerIdFromXpub(xpub);
    // SLIP-10 Ed25519, fully hardened — NOT buildSigningPath/getSigningKey,
    // which build the non-hardened secp256k1 BIP-44 path (contract §2).
    sk = mod.slip10.deriveEd25519Path(seed, paths.signing).privateKey;
  } finally {
    wipe(seed, master, acct, neutered);
  }
  const pk = mod.curves.ed25519.publicKeyFromSeed(sk);

  return {
    xpub,
    peerId,
    account,
    signingPubKeyHex: toHex(pk),
    /** Raw Ed25519 over the exact message bytes — no digest, no envelope (§3). */
    sign: (message) => mod.curves.ed25519.sign(message, sk),
    destroy: () => wipe(sk),
  };
}

/**
 * Full sign-in (contract §4): challenge → sign → verify → session cookie.
 * @param {{mnemonic: string, account?: number, post: (path: string, body: any) => Promise<any>}} args
 * @returns {Promise<{user: {name?: string, trust_level: string}, identity: object, expiresAt: number}>}
 */
export async function signIn({ mnemonic, account = 0, post }) {
  const identity = await deriveIdentity(mnemonic, account);
  try {
    const challenge = await post(
      '/api/auth/challenge',
      challengeRequestBody({ clientPubKeyHex: identity.signingPubKeyHex, xpub: identity.xpub })
    );
    const bytes = decodeChallengeB64(challenge?.challenge);
    if (!bytes) throw new Error('The node returned a malformed challenge.');
    const signature = identity.sign(bytes);
    const verified = await post(
      '/api/auth/verify',
      verifyRequestBody({
        challengeId: challenge.challenge_id,
        clientPubKeyHex: identity.signingPubKeyHex,
        // VERBATIM, unpadded — the server decodes with RawStdEncoding.
        challenge: challenge.challenge,
        signatureHex: toHex(signature),
        xpub: identity.xpub,
      })
    );
    // The node names the identity it resolved with a non-invertible
    // fingerprint (§4). Hash our own xpub and confirm the session it just
    // issued really belongs to the key this page unlocked.
    const local = await xpubFingerprint(identity.xpub);
    if (!fingerprintMatches(local, verified?.user?.xpub_fingerprint)) {
      throw new Error('The node issued a session for a different identity than the key you unlocked.');
    }
    return {
      user: verified?.user ?? {},
      identity,
      localFingerprint: local,
      expiresAt: verified?.expires_at ?? 0,
    };
  } catch (err) {
    identity.destroy();
    throw err;
  }
}

/**
 * Prove possession of the signed-in key to the node (contract §7).
 * Grants nothing and changes no trust level — it answers "is the key on
 * file for this xpub the key this browser holds?".
 */
export async function attest({ identity, claim = 'self', post }) {
  const issuedAtSeconds = Math.floor(Date.now() / 1000);
  const nonce = new Uint8Array(16);
  (globalThis.crypto ?? {}).getRandomValues?.(nonce);
  const preimage = attestationPreimage({ xpub: identity.xpub, claim, issuedAtSeconds, nonce });
  const signature = identity.sign(preimage);
  return post('/api/auth/attest', {
    xpub: identity.xpub,
    claim,
    issued_at: rfc3339Seconds(issuedAtSeconds),
    nonce_hex: toHex(nonce),
    signature_hex: toHex(signature),
  });
}

/** Derive a libp2p peer id from an xpub (contract §8: xpubs are NOT peer ids). */
export async function peerIdFromXpub(xpub) {
  const { mod } = await loadWallet();
  return mod.libp2p.peerIdFromXpub(String(xpub ?? '').trim());
}

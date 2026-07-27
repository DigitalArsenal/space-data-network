/*
 * The §4 sign-in wire, and the hd-wallet-wasm core the wallet UI runs on
 * (graph task nst-node-edit-permissions-ui; contract
 * graph/tasks/nst-node-admin-contract.md §1–§7, §11).
 *
 * CUSTODY: this module never holds a phrase, a password or a private key.
 * hd-wallet-ui collects the credentials, derives, signs and wipes its own
 * inputs (see walletui.js); the node only ever sees `xpub`,
 * `client_pubkey_hex` and a signature. Nothing here writes localStorage,
 * sessionStorage or IndexedDB, and no request body carries key material.
 *
 * OWNER LAW 2026-07-27: "we do NOT load anything from a site." Both wallet
 * packages are imported LAZILY from the node's OWN origin —
 * `/wallet-wasm/runtime/index.mjs` (§1) and `/wallet-ui/*` (§11.3) — the
 * first time an operator opens sign-in. They are deliberately not inlined
 * into the single-file page: hd-wallet.js alone is 5.2 MB (the .wasm rides
 * in it as a data: URI). If the assets are unstaged the node answers 404 and
 * the UI reports SIGN-IN UNAVAILABLE. A CDN fallback would be a defect, not
 * a degradation.
 */

import {
  RAW_CHALLENGE_OPERATION,
  buildRegistryBinding,
  collectLegacyCredentials,
  createWalletApp,
  readIdentity,
  signChallengeWithWallet,
} from './walletui.js';

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
 * `ts` is Unix SECONDS. `xpub` is OMITTED on every path this page drives
 * (§13.1(i)): the node resolves the operator by signing key, and §14.1 admits
 * the node's own root keys before any user lookup at all. The parameter
 * survives for a future v2 identity that reports a real ACCOUNT xpub — the
 * legacy wallet identities report a MASTER xpub, which must never be sent.
 * (§5's "xpub is MANDATORY for bootstrap" is superseded by §13.1 and §14.1.)
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
 * canonicalBytes) — NOT the raw-challenge form of §3.
 *
 * NO CALLER IN THIS PAGE, deliberately: §13.2 rules attestation CLI/API-only
 * this wave. It is not implementable through hd-wallet-ui — the operation
 * table is a frozen six-entry object, and raw-challenge locks its payload to
 * exactly 32 bytes while this preimage is variable-length. The encoder stays
 * (and stays tested) because it is the §7 wire truth; it gains a caller the
 * day an upstream operation can sign a caller-supplied payload.
 *
 * Layout:
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

/**
 * Full sign-in (contract §4, §11) driven by hd-wallet-ui, mounted in our own
 * DOM and wearing our own stylesheet (owner directive 2026-07-27).
 *
 * Order matters and is forced by the wire: the wallet must unlock FIRST,
 * because §4 binds the challenge to a public key at mint time and the wallet
 * only reveals the key (and the xpub) as `unlockLegacy`'s return value. The
 * package's own `start()` never surfaces either — its signed result carries
 * `keyId: "sha256:…"` and nothing else — which is why §11.4's manual drive is
 * the only composition that satisfies both sides.
 *
 * Custody stays entirely with the wallet: it reads the credential controls,
 * derives, signs and wipes. This function never sees a phrase, a password or
 * a private key, and nothing is stored anywhere.
 *
 * @returns {Promise<{user: object, identity: object, localFingerprint: string, expiresAt: number}>}
 */
export async function signInWithWalletUI({ mount, profile, post, displayName, document: doc = globalThis.document }) {
  const { identity, controller, published, binding, close } = await unlockWithWalletUI({
    mount,
    profile,
    displayName,
    document: doc,
  });
  try {
    // NO `xpub` ON THE WIRE — ruled 2026-07-27 (§13.1(i)). The node resolves
    // an already-registered operator by SIGNING KEY when the xpub is absent
    // (GetUserBySigningPubKey). Sending one would put a depth-0 MASTER xpub —
    // the only kind this wallet's raw-32 identities report — on the wire and
    // into auth.db, a durable at-rest secret enumerating the entire wallet.
    // So it is never sent here, and never in step 3 either (§4: send it in
    // verify only if you sent it in challenge).
    const challenge = await post(
      '/api/auth/challenge',
      challengeRequestBody({ clientPubKeyHex: identity.clientPubKeyHex })
    );
    if (!decodeChallengeB64(challenge?.challenge)) throw new Error('The node returned a malformed challenge.');

    // The wallet renders its OWN confirmation dialog here, signs, and hands
    // the result back through the in-process relay.
    const { signatureHex } = await signChallengeWithWallet({
      controller,
      published,
      binding,
      challengeBase64: challenge.challenge,
    });

    const verified = await post(
      '/api/auth/verify',
      verifyRequestBody({
        challengeId: challenge.challenge_id,
        clientPubKeyHex: identity.clientPubKeyHex,
        // VERBATIM, unpadded — the server decodes with RawStdEncoding.
        challenge: challenge.challenge,
        signatureHex,
      })
    );
    // No local fingerprint to compare: §4's `xpub_fingerprint` names the xpub
    // the NODE holds for this operator (a config label, typically the
    // show-identity account xpub), which is deliberately NOT the wallet's
    // master xpub. Hashing the wallet's would manufacture a false mismatch.
    // The node's value is taken as reported and displayed.
    return {
      user: verified?.user ?? {},
      // No `sign`: the wallet owns the key and destroyed it when the
      // operation completed (§13.2). No `xpub`: see above.
      identity: {
        signingPubKeyHex: identity.clientPubKeyHex,
        derivationPath: identity.derivationPath,
        destroy: () => {},
      },
      localFingerprint: '',
      expiresAt: verified?.expires_at ?? 0,
    };
  } finally {
    close();
  }
}

/**
 * Mount the wallet and unlock it — the half of sign-in that touches no
 * network at all: credentials in, public identity out, nothing transmitted.
 */
async function unlockWithWalletUI({ mount, profile, displayName, document: doc }) {
  const { mod } = await loadWallet();
  const origin = globalThis.location?.origin ?? '';
  const binding = await buildRegistryBinding(origin, displayName);
  const { app, controller, published } = await createWalletApp({ mount, wasm: mod, binding, document: doc });
  const close = () => {
    try {
      app.stop('close');
    } catch {
      /* the controller self-destroys on completion; stop() is belt-and-braces */
    }
  };
  try {
    const title = `Sign in to ${binding.clientDisplayName}`;
    const { controls } = await collectLegacyCredentials({ profile, controller, mount, title, document: doc });
    const identity = readIdentity(
      await controller.unlockLegacy({ ...controls, operation: RAW_CHALLENGE_OPERATION, profile })
    );
    mount.replaceChildren?.();
    return { identity, controller, published, binding, close };
  } catch (err) {
    close();
    throw err;
  }
}

/** Derive a libp2p peer id from an xpub (contract §8: xpubs are NOT peer ids). */
export async function peerIdFromXpub(xpub) {
  const { mod } = await loadWallet();
  return mod.libp2p.peerIdFromXpub(String(xpub ?? '').trim());
}

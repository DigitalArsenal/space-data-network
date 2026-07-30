/**
 * Path-scoped deterministic module-delivery identity.
 *
 * SPEC: sdn/sandcastle-module-identity/v1
 *
 * A private sandcastle is served under a secret path segment:
 *
 *     https://<origin>/OrbPro/private/<uuid>/sandcastle/...
 *
 * That UUID is ALREADY the access capability for the deployment. This module
 * turns it into deterministic key material so that every visitor holding the
 * URL derives the SAME HD identity, and that single public key (xpub) can be
 * registered in a module's `PLG.ALLOWED_XPUBS` allowlist. Without this, an
 * anonymous browser mints a fresh random identity per page load, which makes
 * an xpub allowlist and a shareable gallery mutually exclusive.
 *
 * SECURITY MODEL — stated plainly, because it is a deliberate trade:
 *  - The UUID is a BEARER capability (~122 bits of entropy for a v4 UUID).
 *    Anyone who learns the URL can derive this keypair. That is the intent:
 *    "knowing the URL" IS "holding the grant identity".
 *  - Therefore this identity is NOT a user identity and MUST NOT be used for
 *    node administration, wallet operations, payment, or anything that
 *    authenticates a person. It authorizes ONE thing: fetching module grants
 *    for a deployment whose URL the caller already has.
 *  - Revocation = rotate the UUID and drop the old xpub from every
 *    `ALLOWED_XPUBS` in the same wave. There is no other revocation path.
 *  - No secret is added to any repository: the derivation is public, and the
 *    node stores only the resulting PUBLIC key.
 *
 * DERIVATION (versioned, domain-separated, RFC 5869):
 *   ikm  = UTF-8 bytes of the canonical lowercase hyphenated uuid text (36 B)
 *   salt = UTF-8 "sdn/sandcastle-module-identity/salt/v1"
 *   info = UTF-8 "sdn/sandcastle-module-identity/v1"
 *   seed = HKDF-SHA256(ikm, salt, info, 64)
 *   identity = deriveIdentity(seed, account)
 *
 * The 16 raw UUID bytes are deliberately NOT used as ikm: the text form has
 * exactly one canonical encoding, so two implementations cannot disagree, and
 * it stays greppable in deployment evidence.
 *
 * The serving ORIGIN is deliberately NOT mixed into `info`. Origin
 * authorization is a separate layer (the requester-domain gate on the
 * provider); folding it into the KDF would silently invalidate every
 * registration the moment a deployment moves origin, and would hide the
 * missing domain gate instead of closing it.
 *
 * ISOMORPHISM: the HKDF step runs in the hd-wallet-wasm backend (the same
 * `utils.hkdf` used by `ecies.ts`), not in WebCrypto, so browser, Node and
 * WasmEdge produce identical bytes from identical inputs.
 */

import { deriveIdentity, hkdfSha256 } from './hd-wallet';
import type { DerivedIdentity } from './types';

/** Domain-separation info string. Changing this is a BREAKING re-registration. */
export const PATH_SCOPED_IDENTITY_INFO_V1 = 'sdn/sandcastle-module-identity/v1';

/** Fixed non-secret HKDF salt. Changing this is a BREAKING re-registration. */
export const PATH_SCOPED_IDENTITY_SALT_V1 =
  'sdn/sandcastle-module-identity/salt/v1';

/** Byte length of the BIP-32 seed the HD machinery requires. */
export const PATH_SCOPED_IDENTITY_SEED_BYTES = 64;

/**
 * Canonical lowercase hyphenated UUID. Anything else is refused: a permissive
 * parser here would mean two spellings of the same UUID derive two different
 * identities, only one of which is registered.
 */
const CANONICAL_UUID_RE =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

/**
 * Matches the private-deployment path segment: `/private/<uuid>/`.
 * Case-insensitive on the UUID only; the literal segment is `private`.
 */
const PRIVATE_PATH_SEGMENT_RE =
  /(?:^|\/)private\/([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})(?:\/|$)/;

/**
 * Normalize a UUID to the canonical form this spec derives from, or return
 * null when the input is not a UUID at all.
 */
export function canonicalizePathScopeUuid(value: unknown): string | null {
  if (typeof value !== 'string') {
    return null;
  }
  const candidate = value.trim().toLowerCase();
  return CANONICAL_UUID_RE.test(candidate) ? candidate : null;
}

/**
 * Extract the private-deployment UUID from a URL pathname (or a full URL).
 *
 * Returns null for every path that is not a `/private/<uuid>/` deployment —
 * the public gallery and local dev included. Callers MUST treat null as
 * "keep the existing random-per-load identity": this function fails closed
 * into today's behaviour, never into a guessed identity.
 */
export function extractPathScopeUuid(pathnameOrUrl: unknown): string | null {
  if (typeof pathnameOrUrl !== 'string' || pathnameOrUrl.length === 0) {
    return null;
  }
  let pathname = pathnameOrUrl;
  if (/^[a-z][a-z0-9+.-]*:\/\//i.test(pathname)) {
    try {
      pathname = new URL(pathname).pathname;
    } catch {
      return null;
    }
  }
  const match = PRIVATE_PATH_SEGMENT_RE.exec(pathname);
  return match ? canonicalizePathScopeUuid(match[1]) : null;
}

function utf8(value: string): Uint8Array {
  return new TextEncoder().encode(value);
}

/**
 * HKDF-SHA256 the canonical UUID text into a 64-byte BIP-32 seed.
 *
 * @throws when the UUID is not canonical — deriving from a malformed scope
 *   would produce an unregistered identity and a confusing `xpub_not_allowed`
 *   instead of a clear local failure.
 */
export async function derivePathScopedSeed(uuid: string): Promise<Uint8Array> {
  const canonical = canonicalizePathScopeUuid(uuid);
  if (!canonical) {
    throw new Error(
      'path-scoped module identity requires a canonical lowercase hyphenated UUID',
    );
  }
  const seed = await hkdfSha256(
    utf8(canonical),
    utf8(PATH_SCOPED_IDENTITY_SALT_V1),
    utf8(PATH_SCOPED_IDENTITY_INFO_V1),
    PATH_SCOPED_IDENTITY_SEED_BYTES,
  );
  if (!seed || seed.length !== PATH_SCOPED_IDENTITY_SEED_BYTES) {
    throw new Error(
      `path-scoped module identity seed must be ${PATH_SCOPED_IDENTITY_SEED_BYTES} bytes`,
    );
  }
  return seed;
}

/**
 * Derive the deterministic module-delivery identity for a private-deployment
 * UUID. Same UUID + same account => same peerId, xpub, signing key and
 * encryption key, on every runtime, forever.
 *
 * The returned `xpub` is what gets registered in `PLG.ALLOWED_XPUBS`.
 */
export async function derivePathScopedIdentity(
  uuid: string,
  account = 0,
): Promise<DerivedIdentity> {
  const seed = await derivePathScopedSeed(uuid);
  return deriveIdentity(seed, account);
}

/**
 * Convenience for the client seam: derive from a pathname when it is a
 * `/private/<uuid>/` deployment, otherwise return null so the caller keeps
 * its existing per-load random identity.
 */
export async function derivePathScopedIdentityForPath(
  pathnameOrUrl: unknown,
  account = 0,
): Promise<DerivedIdentity | null> {
  const uuid = extractPathScopeUuid(pathnameOrUrl);
  if (!uuid) {
    return null;
  }
  return derivePathScopedIdentity(uuid, account);
}

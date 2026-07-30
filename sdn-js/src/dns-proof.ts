/**
 * SDN domain-to-nodekey proof: browser-side verification.
 *
 * Companion to `sdn-server/internal/dnsproof`. The canonical statement bytes,
 * the record grammar and the validity rules are the SAME CONTRACT; the golden
 * statement in `dns-proof.test.ts` is byte-identical to the one in
 * `dnsproof_test.go` on purpose, because a proof minted by the node and refused
 * by the browser is worse than no proof at all.
 *
 * WHY THERE IS NO EXISTING STANDARD TO IMPORT (Hermes ruling 2026-07-30, task
 * sdn-dns-key-proof-standard): every deployed DNS-TXT key mechanism proves one
 * direction only. DKIM key records, `_dnsaddr`, `_atproto`, `_ens` and every
 * commercial site-verification token publish or point at a key — presence
 * proves DNS control, and the same record copied into another zone still
 * "verifies". ACME dns-01 is a hash of a CA-chosen token scoped to one issuance
 * session. OPENPGPKEY and DANE hand the whole question to DNSSEC. So this
 * composes: DKIM's tag-value syntax (RFC 6376 §3.2) and underscore-label
 * convention, plus the signed-statement pattern already in EPM CHAIN_PROOFS,
 * which supplies the missing direction.
 *
 * The security property: the verifier rebuilds the signed statement from the
 * domain IT QUERIED. A copied record therefore fails with no revocation, no
 * registry, and no third party in the path.
 *
 * WHAT THIS DOES NOT DEFEND AGAINST (Seal Council, Hephaestus 2026-07-30 —
 * surface this in any UI that renders a result): the resolver quorum defends
 * against a tampering RESOLVER. It does NOT defend against compromise of the
 * authoritative zone or the registrar. For spaceaware.io the authoritative
 * nameservers are Cloudflare's AND Cloudflare is a quorum member, so a zone
 * compromise forges the record and every other resolver faithfully repeats it.
 * A domain proof answers "did the domain owner and the key holder both agree",
 * not "is the domain owner honest".
 */

import { publicSha256, verifyPublicEd25519Signature } from './crypto/public-runtime';
import { secp256k1 } from '@noble/curves/secp256k1';

/** Value of the mandatory, first-position `v=` tag. */
export const PROOF_VERSION = 'SDN1';

/** First line of every canonical statement. Versioned apart from the record. */
export const STATEMENT_PREFIX = 'sdn-domain-proof/1';

/** Underscore label the proof is published under. NOT `_domainkey`. */
export const OWNER_LABEL = '_sdnkey';

/** Seconds of tolerated clock skew on `ts=`. Matches the Go ClockSkew. */
export const CLOCK_SKEW_SECONDS = 300;

/**
 * Maximum validity window a proof may claim, in seconds. Matches Go MaxValidity.
 *
 * Seal Council condition (Hephaestus, 2026-07-30): the duration is the one
 * caller-influenced field that is not structurally constrained the way the domain
 * is, and THERE IS NO REVOCATION CHANNEL. An unexpired proof for domain X stays
 * verifiable after the key has moved on to serving a different host, so the
 * VERIFIER enforces the bound too — not just the signer. A cap only the signer
 * honours is a cap an attacker with the key simply ignores.
 */
export const MAX_VALIDITY_SECONDS = 365 * 24 * 60 * 60;

export type ProofAlgorithm = 'ed25519' | 'secp256k1';

export interface DomainProof {
  /** The domain the verifier QUERIED. Never read from the record. */
  domain: string;
  algorithm: ProofAlgorithm;
  publicKey: Uint8Array;
  /** libp2p peer id the key speaks for; '' when the proof binds no peer. */
  peerId: string;
  issuedAt: number;
  /** 0 = no expiry. */
  expiresAt: number;
  signature: Uint8Array;
}

/**
 * A DoH endpoint that answers the Google-schema JSON API with permissive CORS.
 *
 * `kind` records the TXT `data` encoding actually MEASURED from that operator
 * on 2026-07-30 — see normalizeTxtPresentation. It is documentation, not a
 * parsing switch: the normalizer sniffs, because an operator may change.
 */
export interface DohResolver {
  name: string;
  url: string;
  /** Independent operator identity. Quorum counts OPERATORS, not URLs. */
  operator: string;
}

/**
 * Browser-usable JSON DoH endpoints, each verified by hand on 2026-07-30 to
 * return HTTP 200 with `access-control-allow-origin: *` and a correct answer.
 *
 * Deliberately excluded, with reasons, so nobody re-adds them hopefully:
 *   - Quad9  — JSON API retired 2025-05-05.
 *   - NextDNS, AdGuard — answer JSON but send NO CORS header: unusable here.
 *   - Mullvad — wireformat only; HTTP 400 for application/dns-json.
 *
 * Raising N beyond 3 needs RFC 8484 wireformat support, which is a follow-on.
 */
export const DEFAULT_DOH_RESOLVERS: readonly DohResolver[] = Object.freeze([
  { name: 'cloudflare', operator: 'cloudflare', url: 'https://cloudflare-dns.com/dns-query' },
  { name: 'google', operator: 'google', url: 'https://dns.google/resolve' },
  { name: 'doh.sb', operator: 'doh.sb', url: 'https://doh.sb/dns-query' },
]);

/**
 * Minimum number of INDEPENDENT operators that must return the same value.
 *
 * A single DoH endpoint is a trusted intermediary, which the owner directive
 * forbids. Fail-closed follows: if fewer than this many operators answer, the
 * result is UNAVAILABLE and nothing is trusted. Unavailable is not "allow".
 */
export const DEFAULT_QUORUM = 2;

export interface VerifyOptions {
  resolvers?: readonly DohResolver[];
  quorum?: number;
  /** Unix seconds. Injectable so tests do not depend on the wall clock. */
  now?: number;
  fetchImpl?: typeof fetch;
  /** Per-request timeout in ms. */
  timeoutMs?: number;
  selector?: string;
}

export interface DomainProofResult {
  /** The owner name queried. */
  ownerName: string;
  domain: string;
  /** Proofs that parsed, verified, and met quorum. Newest first. */
  proofs: DomainProof[];
  /** Operators that answered at all. */
  respondingOperators: string[];
  /** True when >= quorum operators answered. False => UNAVAILABLE, trust nothing. */
  quorumMet: boolean;
  /**
   * DNSSEC state as reported by the responding resolvers' AD flags.
   * 'secure'   — every responder set AD.
   * 'insecure' — no responder set AD (the zone is unsigned; expected today for
   *              spaceaware.io, which has no DS record).
   * 'downgrade'— responders DISAGREE. Refused: one resolver validating and
   *              another not is the shape of a stripping attack.
   */
  dnssec: 'secure' | 'insecure' | 'downgrade';
  /** Human-readable reasons a value was not admitted. Never silently dropped. */
  rejected: string[];
  errors: string[];
}

const textEncoder = new TextEncoder();

/** Lowercase, strip the root dot, and refuse anything that is not an A-label. */
export function normalizeDomain(domain: string): string {
  let d = (domain ?? '').trim();
  if (d.endsWith('.')) d = d.slice(0, -1);
  d = d.toLowerCase();
  if (!d) throw new Error('dnsproof: empty domain');
  // IDNA has more than one mapping profile. A canonical statement whose bytes
  // depend on which library canonicalized the domain is not canonical, so
  // U-labels are refused rather than transcoded here.
  for (const ch of d) {
    if (ch.codePointAt(0)! > 0x7f) {
      throw new Error(
        `dnsproof: ${JSON.stringify(domain)} is not an A-label; convert IDN to punycode before proving it`,
      );
    }
  }
  if (/[ \t;"\\]/.test(d)) {
    throw new Error(`dnsproof: ${JSON.stringify(domain)} contains a character that cannot appear in a canonical statement`);
  }
  if (d.startsWith('.') || d.includes('..')) throw new Error(`dnsproof: ${JSON.stringify(domain)} has an empty label`);
  if (!d.includes('.')) throw new Error(`dnsproof: ${JSON.stringify(domain)} is not a fully qualified domain`);
  return d;
}

/** `_sdnkey.<domain>`, or `<selector>._sdnkey.<domain>`. */
export function ownerName(domain: string, selector = ''): string {
  const d = normalizeDomain(domain);
  const sel = (selector ?? '').trim().toLowerCase();
  if (!sel) return `${OWNER_LABEL}.${d}`;
  if (/[.\s]/.test(sel)) throw new Error(`dnsproof: selector ${JSON.stringify(selector)} must be a single label`);
  return `${sel}.${OWNER_LABEL}.${d}`;
}

function toHex(bytes: Uint8Array): string {
  let out = '';
  for (const b of bytes) out += b.toString(16).padStart(2, '0');
  return out;
}

/**
 * The exact bytes covered by the signature.
 *
 * Rules, identical to the Go implementation, chosen so no canonicalization
 * library is needed on either side:
 *   - fixed line order, EVERY line always present (absent value = empty value),
 *   - exactly one LF after every line INCLUDING the last,
 *   - lowercase hex for the key.
 */
export function canonicalStatement(proof: Omit<DomainProof, 'signature'>): Uint8Array {
  const domain = normalizeDomain(proof.domain);
  const algorithm = normalizeAlgorithm(proof.algorithm);
  assertPublicKeyLength(algorithm, proof.publicKey);
  if (!Number.isInteger(proof.issuedAt) || proof.issuedAt <= 0) {
    throw new Error('dnsproof: issued-at must be a positive integer unix timestamp');
  }
  if (!Number.isInteger(proof.expiresAt) || proof.expiresAt < 0) {
    throw new Error('dnsproof: expires-at must be a non-negative integer unix timestamp');
  }
  if (proof.expiresAt !== 0 && proof.expiresAt <= proof.issuedAt) {
    throw new Error(`dnsproof: expires-at ${proof.expiresAt} is not after issued-at ${proof.issuedAt}`);
  }
  // Enforced in canonicalStatement, the one function both producers and verifiers
  // pass through, so an over-long proof can neither be minted nor accepted.
  if (proof.expiresAt !== 0 && proof.expiresAt - proof.issuedAt > MAX_VALIDITY_SECONDS) {
    throw new Error(
      `dnsproof: validity window ${proof.expiresAt - proof.issuedAt}s exceeds the ${MAX_VALIDITY_SECONDS}s maximum; ` +
        'there is no revocation channel, so a proof may not outlive its cap',
    );
  }
  const peerId = (proof.peerId ?? '').trim();
  if (/[\s;]/.test(peerId)) throw new Error('dnsproof: peer id contains whitespace or a tag separator');

  const statement =
    `${STATEMENT_PREFIX}\n` +
    `domain=${domain}\n` +
    `key=${algorithm}:${toHex(proof.publicKey)}\n` +
    `peerid=${peerId}\n` +
    `issued=${proof.issuedAt}\n` +
    `expires=${proof.expiresAt}\n`;
  return textEncoder.encode(statement);
}

/**
 * Convert a DoH JSON `data` field to the raw TXT value.
 *
 * This exists because the browser-usable JSON DoH providers DISAGREE, which was
 * measured rather than assumed (2026-07-30, `google._domainkey.anthropic.com`,
 * a 410-byte DKIM key):
 *
 *   Cloudflare -> 415 chars, DNS presentation format: `"<255 bytes>" "<rest>"`
 *   doh.sb     -> 415 chars, same presentation format
 *   Google     -> 410 chars, already concatenated, unquoted
 *
 * A quorum that compares those strings directly never agrees, and would report
 * every proof on earth as unverifiable. Normalizing first is not politeness, it
 * is the difference between a working quorum and a broken one.
 */
export function normalizeTxtPresentation(data: string): string {
  const s = (data ?? '').trim();
  if (!s.startsWith('"')) return s;
  let out = '';
  let inString = false;
  let escaped = false;
  for (const ch of s) {
    if (escaped) {
      out += ch;
      escaped = false;
    } else if (inString && ch === '\\') {
      escaped = true;
    } else if (ch === '"') {
      inString = !inString;
    } else if (inString) {
      out += ch;
    } else if (ch === ' ' || ch === '\t') {
      // separator between DNS character-strings
    } else {
      throw new Error(`dnsproof: unexpected character ${JSON.stringify(ch)} outside a quoted string in TXT data`);
    }
  }
  if (inString || escaped) throw new Error('dnsproof: unterminated quoted string in TXT data');
  return out;
}

/**
 * DKIM tag-value grammar (RFC 6376 §3.2): ';'-separated, whitespace around '='
 * and ';' insignificant, trailing ';' allowed, UNKNOWN TAGS IGNORED so a later
 * version can add tags without breaking today's verifiers. Duplicates are
 * refused, because "which one wins" is what an attacker looks for.
 */
function parseTagList(value: string): { tags: Map<string, string>; order: string[] } {
  const tags = new Map<string, string>();
  const order: string[] = [];
  for (const chunk of value.split(';')) {
    const part = chunk.trim();
    if (!part) continue;
    const eq = part.indexOf('=');
    if (eq < 0) throw new Error(`dnsproof: tag ${JSON.stringify(part)} has no '='`);
    const name = part.slice(0, eq).trim().toLowerCase();
    if (!name) throw new Error(`dnsproof: empty tag name in ${JSON.stringify(part)}`);
    if (tags.has(name)) throw new Error(`dnsproof: duplicate tag ${JSON.stringify(name)}`);
    tags.set(name, part.slice(eq + 1).trim());
    order.push(name);
  }
  return { tags, order };
}

function normalizeAlgorithm(alg: string | undefined): ProofAlgorithm {
  const a = (alg ?? '').trim().toLowerCase();
  if (a === '' || a === 'ed25519') return 'ed25519';
  if (a === 'secp256k1') return 'secp256k1';
  throw new Error(`dnsproof: unsupported key algorithm ${JSON.stringify(alg)}`);
}

function assertPublicKeyLength(alg: ProofAlgorithm, pub: Uint8Array): void {
  const want = alg === 'ed25519' ? 32 : 33;
  if (pub.length !== want) {
    throw new Error(`dnsproof: ${alg} public key is ${pub.length} bytes, want ${want}`);
  }
}

/**
 * Decode base64url, tolerating padding and standard base64 alphabet on input.
 *
 * Output is always emitted unpadded base64url; input is lenient because DKIM's
 * `p=` is standard base64 and that is what operator muscle memory produces.
 */
function decodeBase64(value: string): Uint8Array {
  const s = (value ?? '').trim();
  if (!s) throw new Error('empty');
  let b64 = s.replace(/-/g, '+').replace(/_/g, '/');
  while (b64.length % 4 !== 0) b64 += '=';
  if (!/^[A-Za-z0-9+/]+={0,2}$/.test(b64)) throw new Error('not base64');
  const bin = atob(b64);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i += 1) out[i] = bin.charCodeAt(i);
  return out;
}

function parseUnix(value: string | undefined, required: boolean): number {
  const s = (value ?? '').trim();
  if (!s) {
    if (required) throw new Error('required');
    return 0;
  }
  if (!/^\d+$/.test(s)) throw new Error('not a non-negative integer');
  const n = Number(s);
  if (!Number.isSafeInteger(n)) throw new Error('out of range');
  return n;
}

/** Parse one TXT value against the domain it was queried at. */
export function parseRecord(domain: string, record: string): DomainProof {
  const value = (record ?? '').trim();
  if (!value) throw new Error('dnsproof: empty TXT record');
  const normalizedDomain = normalizeDomain(domain);

  const { tags, order } = parseTagList(value);
  if (order.length === 0 || order[0] !== 'v') throw new Error('dnsproof: v= must be the first tag');
  if ((tags.get('v') ?? '').toUpperCase() !== PROOF_VERSION) {
    throw new Error(`dnsproof: unsupported proof version ${JSON.stringify(tags.get('v'))} (want ${PROOF_VERSION})`);
  }

  const algorithm = normalizeAlgorithm(tags.get('k'));

  // d= is optional and redundant; the owner name already carries the domain.
  // When an operator includes it anyway, a mismatch is a hard failure: it means
  // the record was copied.
  const claimed = (tags.get('d') ?? '').trim();
  if (claimed) {
    const claimedDomain = normalizeDomain(claimed);
    if (claimedDomain !== normalizedDomain) {
      throw new Error(`dnsproof: record claims ${claimedDomain}, queried ${normalizedDomain}`);
    }
  }

  let publicKey: Uint8Array;
  try {
    publicKey = decodeBase64(tags.get('p') ?? '');
  } catch (err) {
    throw new Error(`dnsproof: p=: ${(err as Error).message}`);
  }
  assertPublicKeyLength(algorithm, publicKey);

  let signature: Uint8Array;
  try {
    signature = decodeBase64(tags.get('sig') ?? '');
  } catch (err) {
    throw new Error(`dnsproof: sig=: ${(err as Error).message}`);
  }

  let issuedAt: number;
  let expiresAt: number;
  try {
    issuedAt = parseUnix(tags.get('ts'), true);
  } catch (err) {
    throw new Error(`dnsproof: ts=: ${(err as Error).message}`);
  }
  try {
    expiresAt = parseUnix(tags.get('xp'), false);
  } catch (err) {
    throw new Error(`dnsproof: xp=: ${(err as Error).message}`);
  }

  return {
    domain: normalizedDomain,
    algorithm,
    publicKey,
    peerId: (tags.get('id') ?? '').trim(),
    issuedAt,
    expiresAt,
    signature,
  };
}

/**
 * Verify a proof's signature and validity window. Throws with a legible reason;
 * callers that want a boolean use isValidProof.
 *
 * `nowSeconds` is a parameter because a verifier that silently trusts the local
 * clock is a verifier an attacker can move.
 */
export function verifyProof(proof: DomainProof, nowSeconds: number): void {
  const statement = canonicalStatement(proof);
  if (nowSeconds < proof.issuedAt - CLOCK_SKEW_SECONDS) {
    throw new Error(`dnsproof: proof is issued in the future beyond the allowed clock skew (issued ${proof.issuedAt}, now ${nowSeconds})`);
  }
  if (proof.expiresAt !== 0 && nowSeconds >= proof.expiresAt) {
    throw new Error(`dnsproof: proof has expired (expired ${proof.expiresAt}, now ${nowSeconds})`);
  }

  if (proof.algorithm === 'ed25519') {
    if (proof.signature.length !== 64) {
      throw new Error(`dnsproof: ed25519 signature is ${proof.signature.length} bytes, want 64`);
    }
    if (!verifyPublicEd25519Signature(proof.publicKey, statement, proof.signature)) {
      throw new Error('dnsproof: signature does not verify against the canonical statement');
    }
    return;
  }

  // secp256k1: ECDSA-DER over sha256(statement) — not a free choice, it is
  // exactly what sdn-server/internal/epm/signature.go already accepts for
  // secp256k1 EPM signing keys.
  // @noble/curves 1.9.x infers the signature encoding from the input length, so
  // the DER form is parsed EXPLICITLY and re-serialized compact before it is
  // verified. Relying on that inference would mean a signature that happened to
  // be 64 bytes got read under a different encoding than it was signed with.
  let ok = false;
  try {
    const compact = secp256k1.Signature.fromDER(proof.signature).toCompactRawBytes();
    ok = secp256k1.verify(compact, publicSha256(statement), proof.publicKey);
  } catch (err) {
    throw new Error(`dnsproof: ${(err as Error).message}`);
  }
  if (!ok) throw new Error('dnsproof: signature does not verify against the canonical statement');
}

export function isValidProof(proof: DomainProof, nowSeconds: number): boolean {
  try {
    verifyProof(proof, nowSeconds);
    return true;
  } catch {
    return false;
  }
}

/** Stable identifier to compare against the key that signed a module/manifest. */
export function keyFingerprint(proof: Pick<DomainProof, 'algorithm' | 'publicKey'>): string {
  return `${proof.algorithm}:${toHex(proof.publicKey)}`;
}

/**
 * Parse every TXT value at an owner name and keep the ones that verify.
 *
 * Several TXT RRs at one name is the NORMAL case, not an error: a domain may
 * front several nodes, and key rotation overlaps old and new. The rule is
 * accept-any-match — the SPF/DKIM rotation lesson — so one stale record cannot
 * deny service to a good one. Foreign records (SPF, verification tokens) at the
 * same name are skipped silently; malformed SDN records are reported.
 */
export function selectProofs(
  domain: string,
  values: readonly string[],
  nowSeconds: number,
): { proofs: DomainProof[]; rejected: string[] } {
  const proofs: DomainProof[] = [];
  const rejected: string[] = [];
  const seen = new Set<string>();
  for (const raw of values) {
    let value: string;
    try {
      value = normalizeTxtPresentation(raw);
    } catch (err) {
      rejected.push((err as Error).message);
      continue;
    }
    if (!value.trim().toLowerCase().startsWith('v=sdn1')) continue;
    try {
      const proof = parseRecord(domain, value);
      verifyProof(proof, nowSeconds);
      const fp = keyFingerprint(proof);
      if (seen.has(fp)) continue;
      seen.add(fp);
      proofs.push(proof);
    } catch (err) {
      rejected.push((err as Error).message);
    }
  }
  proofs.sort((a, b) => b.issuedAt - a.issuedAt);
  return { proofs, rejected };
}

interface DohAnswer {
  name?: string;
  type?: number;
  TTL?: number;
  data?: string;
}

interface DohResponse {
  Status?: number;
  AD?: boolean;
  Answer?: DohAnswer[];
}

interface ResolverOutcome {
  operator: string;
  /** Normalized TXT values, deduplicated. */
  values: Set<string>;
  ad: boolean;
  error?: string;
}

async function queryOne(
  resolver: DohResolver,
  name: string,
  fetchImpl: typeof fetch,
  timeoutMs: number,
): Promise<ResolverOutcome> {
  const url = `${resolver.url}?name=${encodeURIComponent(name)}&type=TXT`;
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    // `accept` is a CORS-safelisted request header for this value, so no
    // preflight is triggered and the query works from a plain page.
    const res = await fetchImpl(url, {
      headers: { accept: 'application/dns-json' },
      signal: controller.signal,
      // No credentials, ever: this is a public lookup and must not carry
      // ambient cookies to a third-party resolver.
      credentials: 'omit',
      redirect: 'error',
    });
    if (!res.ok) {
      return { operator: resolver.operator, values: new Set(), ad: false, error: `${resolver.name}: HTTP ${res.status}` };
    }
    const body = (await res.json()) as DohResponse;
    // Status 0 = NOERROR. Anything else (NXDOMAIN=3 included) means there is no
    // proof here; that is a fact, not an error, and it must not count toward
    // agreement on a VALUE while still counting as a response.
    const values = new Set<string>();
    if (body.Status === 0) {
      for (const answer of body.Answer ?? []) {
        if (typeof answer.data !== 'string') continue;
        try {
          values.add(normalizeTxtPresentation(answer.data));
        } catch {
          // A value this resolver mangled cannot reach quorum anyway.
        }
      }
    }
    return { operator: resolver.operator, values, ad: body.AD === true };
  } catch (err) {
    return { operator: resolver.operator, values: new Set(), ad: false, error: `${resolver.name}: ${(err as Error).message}` };
  } finally {
    clearTimeout(timer);
  }
}

/**
 * Resolve the proof records for a domain and verify them, requiring agreement
 * from `quorum` INDEPENDENT resolver operators.
 *
 * Fail-closed in every direction:
 *   - fewer than `quorum` operators answer  -> quorumMet false, proofs empty,
 *   - operators disagree about a value      -> that value is not admitted,
 *   - operators disagree about DNSSEC       -> dnssec 'downgrade' (caller MUST
 *     treat that as untrusted; a stripping attack looks exactly like this),
 *   - value parses but does not verify      -> reported in `rejected`.
 *
 * A caller must gate trust on `quorumMet && proofs.length > 0 && dnssec !==
 * 'downgrade'`, never on `proofs.length` alone.
 */
export async function verifyDomainProof(
  domain: string,
  options: VerifyOptions = {},
): Promise<DomainProofResult> {
  const resolvers = options.resolvers ?? DEFAULT_DOH_RESOLVERS;
  const quorum = options.quorum ?? DEFAULT_QUORUM;
  const nowSeconds = options.now ?? Math.floor(Date.now() / 1000);
  const timeoutMs = options.timeoutMs ?? 5000;
  const fetchImpl = options.fetchImpl ?? globalThis.fetch;
  if (typeof fetchImpl !== 'function') {
    throw new Error('dnsproof: no fetch implementation available; pass options.fetchImpl');
  }
  const normalizedDomain = normalizeDomain(domain);
  const name = ownerName(normalizedDomain, options.selector ?? '');

  const outcomes = await Promise.all(resolvers.map((r) => queryOne(r, name, fetchImpl, timeoutMs)));

  const errors: string[] = [];
  const responding: string[] = [];
  const adFlags: boolean[] = [];
  const counts = new Map<string, Set<string>>();
  for (const outcome of outcomes) {
    if (outcome.error) {
      errors.push(outcome.error);
      continue;
    }
    responding.push(outcome.operator);
    adFlags.push(outcome.ad);
    for (const value of outcome.values) {
      const operators = counts.get(value) ?? new Set<string>();
      operators.add(outcome.operator);
      counts.set(value, operators);
    }
  }

  const uniqueResponders = new Set(responding);
  const quorumMet = uniqueResponders.size >= quorum;

  let dnssec: DomainProofResult['dnssec'] = 'insecure';
  if (adFlags.length > 0) {
    const secure = adFlags.filter(Boolean).length;
    if (secure === adFlags.length) dnssec = 'secure';
    else if (secure > 0) dnssec = 'downgrade';
  }

  const rejected: string[] = [];
  const admitted: string[] = [];
  for (const [value, operators] of counts) {
    if (operators.size >= quorum) admitted.push(value);
    else if (value.trim().toLowerCase().startsWith('v=sdn1')) {
      rejected.push(
        `value seen by only ${operators.size} of ${quorum} required operators (${[...operators].join(', ')})`,
      );
    }
  }

  const selected = quorumMet
    ? selectProofs(normalizedDomain, admitted, nowSeconds)
    : { proofs: [] as DomainProof[], rejected: [] as string[] };

  return {
    ownerName: name,
    domain: normalizedDomain,
    proofs: selected.proofs,
    respondingOperators: [...uniqueResponders],
    quorumMet,
    dnssec,
    rejected: [...rejected, ...selected.rejected],
    errors,
  };
}

/**
 * The trust predicate a provider dashboard should call.
 *
 * IMPORTANT — the owner directive said "anything loaded from the server that
 * resolves to that IP address should be automatically trusted". That predicate
 * is UNIMPLEMENTABLE AS STATED and was corrected with evidence (Hermes +
 * Hephaestus, 2026-07-30): sdn.spaceaware.io resolves to 104.21.35.243 and
 * 172.67.181.124, which are Cloudflare SHARED ANYCAST addresses serving
 * millions of unrelated zones. IP equality would auto-trust the entire
 * Cloudflare edge. The sound predicate is DOMAIN + KEY: the origin's hostname
 * must be, or sit under, a domain whose proof binds the key that actually
 * signed the artifact.
 */
export function proofBindsKey(
  result: DomainProofResult,
  expectedFingerprint: string,
): DomainProof | null {
  if (!result.quorumMet) return null;
  if (result.dnssec === 'downgrade') return null;
  return result.proofs.find((p) => keyFingerprint(p) === expectedFingerprint) ?? null;
}

/**
 * True when `originHost` is covered by a proven `provenDomain` — exact match or
 * a subdomain.
 *
 * The dotted-boundary check is the point: a naive endsWith() would let
 * `evil-spaceaware.io` pass for `spaceaware.io`.
 */
export function originCoveredByDomain(originHost: string, provenDomain: string): boolean {
  let host: string;
  let proven: string;
  try {
    host = normalizeDomain(originHost);
    proven = normalizeDomain(provenDomain);
  } catch {
    return false;
  }
  return host === proven || host.endsWith(`.${proven}`);
}

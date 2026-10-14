/**
 * EPM Attestation - Content signing and chain binding proof utilities
 *
 * Provides functions to sign EPM (Entity Profile Message) content and
 * build/verify chain binding proofs that link blockchain keys to the
 * same HD wallet identity.
 *
 * These functions operate on JSON representations of EPM data and
 * require an initialized HDWalletModule for crypto operations.
 *
 * @module epm-attestation
 */

// =============================================================================
// Canonical Payload
// =============================================================================

/**
 * Build a canonical attestation payload for chain proof signing.
 * This is the message that each chain key signs to prove common wallet origin.
 *
 * The payload is a deterministic JSON string (sorted keys) containing:
 * - version: attestation format version
 * - xpub: BIP-32 extended public key (account-level identity)
 * - signing_pubkey_hex: Ed25519 signing public key
 * - encryption_pubkey_hex: X25519 encryption public key
 * - issued_at: Unix timestamp (seconds)
 *
 * @param {Object} params
 * @param {string} params.xpub - BIP-32 extended public key
 * @param {string} params.signingPubKeyHex - Ed25519 signing public key (hex)
 * @param {string} params.encryptionPubKeyHex - X25519 encryption public key (hex)
 * @param {number} params.issuedAt - Unix timestamp in seconds
 * @param {string} [params.identityPubKeyHex] - secp256k1 identity public key (hex)
 * @param {string} [params.version='1'] - Attestation format version
 * @returns {string} Canonical JSON string (deterministic, sorted keys)
 */
export function buildCanonicalPayload({
  xpub,
  signingPubKeyHex,
  encryptionPubKeyHex,
  issuedAt,
  identityPubKeyHex = '',
  version = '1',
}) {
  const payload = {
    encryption_pubkey_hex: encryptionPubKeyHex,
    identity_pubkey_hex: identityPubKeyHex,
    issued_at: issuedAt,
    signing_pubkey_hex: signingPubKeyHex,
    version,
    xpub,
  };
  // Keys are alphabetically sorted by construction
  return JSON.stringify(payload);
}

// =============================================================================
// EPM Content Signing
// =============================================================================

// EntityType / KeyType enum labels (FlatBuffer order). The in-module verifier
// emits these names, so we must too.
const EPM_ENTITY_TYPE_NAMES = ['User', 'Node'];
const EPM_KEY_TYPE_NAMES = ['Signing', 'Encryption'];

// Whitespace set trimmed by the Go/C++ canonicalizer: space, tab, NL, CR, VT, FF.
// (Deliberately NOT JS \s, which also strips NBSP/U+2028/etc. and would diverge.)
function epmTrim(value) {
  if (typeof value !== 'string') return '';
  return value.replace(/^[ \t\n\r\x0b\f]+/, '').replace(/[ \t\n\r\x0b\f]+$/, '');
}

// Go addBytesString: trim, omit when empty.
function epmAddStr(obj, key, value) {
  const t = epmTrim(value);
  if (t !== '') obj[key] = t;
}

// Trimmed non-empty strings -> array; attach only when non-empty.
function epmAddStrArray(obj, key, values) {
  if (!Array.isArray(values)) return;
  const arr = [];
  for (const v of values) {
    const t = epmTrim(v);
    if (t !== '') arr.push(t);
  }
  if (arr.length) obj[key] = arr;
}

function epmEnumName(value, names) {
  if (typeof value === 'number') return names[value];
  return value; // already a label, or undefined
}

/**
 * RFC 8785 (JCS) canonicalization: recursively sort object keys by UTF-16 code
 * units, then ECMAScript JSON.stringify (minimal escaping, no HTML escaping of
 * & < >, raw non-ASCII, integer numbers). Byte-identical to common/jcs in wasm.
 */
function epmJcsCanonicalize(value) {
  const sortDeep = (v) => {
    if (Array.isArray(v)) return v.map(sortDeep);
    if (v && typeof v === 'object') {
      const out = {};
      for (const k of Object.keys(v).sort((a, b) => (a < b ? -1 : a > b ? 1 : 0))) {
        out[k] = sortDeep(v[k]);
      }
      return out;
    }
    return v;
  };
  return JSON.stringify(sortDeep(value));
}

/**
 * Build the canonical EPM signing content. Byte-identical to the in-module
 * verifier (common/epm BuildSigningContent + common/jcs Canonicalize), so a
 * wallet signature over this content verifies isomorphically in the browser and
 * on wasmedge. Mirrors the field set/rules exactly: trim + omit-empty strings,
 * enum-label ENTITY_TYPE (always) / KEY_TYPE (Signing|Encryption only), nested
 * ADDRESS, all public CryptoKey fields in KEYS, CHAIN_PROOFS arrays,
 * SIGNATURE_TIMESTAMP (integer, when nonzero), and SIGNATURE excluded.
 *
 * @param {Object} epm - EPM fields as a plain object (schema UPPER_SNAKE keys;
 *   ENTITY_TYPE/KEY_TYPE may be enum index or label)
 * @returns {Uint8Array} UTF-8 encoded canonical (JCS) representation
 */
export function buildEPMSigningContent(epm) {
  const g = (k) => epm[k] ?? epm[k.toLowerCase()];
  const content = {};

  epmAddStr(content, 'DN', g('DN'));
  epmAddStr(content, 'LEGAL_NAME', g('LEGAL_NAME'));
  epmAddStr(content, 'FAMILY_NAME', g('FAMILY_NAME'));
  epmAddStr(content, 'GIVEN_NAME', g('GIVEN_NAME'));
  epmAddStr(content, 'ADDITIONAL_NAME', g('ADDITIONAL_NAME'));
  epmAddStr(content, 'HONORIFIC_PREFIX', g('HONORIFIC_PREFIX'));
  epmAddStr(content, 'HONORIFIC_SUFFIX', g('HONORIFIC_SUFFIX'));
  epmAddStr(content, 'JOB_TITLE', g('JOB_TITLE'));
  epmAddStr(content, 'OCCUPATION', g('OCCUPATION'));
  epmAddStr(content, 'EMAIL', g('EMAIL'));
  epmAddStr(content, 'TELEPHONE', g('TELEPHONE'));
  // SDS 1.239.0 annex rule 7: the photo (an RFC 2397 data URI) is signed like
  // the name it pictures, as SDN's Go signer and the modules' C++ projection do.
  epmAddStr(content, 'PHOTO', g('PHOTO'));

  const addr = g('ADDRESS');
  if (addr && typeof addr === 'object') {
    const a = {};
    const ag = (k) => addr[k] ?? addr[k.toLowerCase()];
    epmAddStr(a, 'COUNTRY', ag('COUNTRY'));
    epmAddStr(a, 'REGION', ag('REGION'));
    epmAddStr(a, 'LOCALITY', ag('LOCALITY'));
    epmAddStr(a, 'POSTAL_CODE', ag('POSTAL_CODE'));
    epmAddStr(a, 'STREET', ag('STREET'));
    epmAddStr(a, 'POST_OFFICE_BOX_NUMBER', ag('POST_OFFICE_BOX_NUMBER'));
    if (Object.keys(a).length) content.ADDRESS = a;
  }

  epmAddStrArray(content, 'ALTERNATE_NAMES', g('ALTERNATE_NAMES'));

  const keys = g('KEYS');
  if (Array.isArray(keys)) {
    const arr = [];
    for (const k of keys) {
      if (!k || typeof k !== 'object') continue;
      const e = {};
      const kg = (kk) => k[kk] ?? k[kk.toLowerCase()];
      epmAddStr(e, 'PUBLIC_KEY', kg('PUBLIC_KEY'));
      epmAddStr(e, 'XPUB', kg('XPUB'));
      epmAddStr(e, 'ADDRESS_TYPE', kg('ADDRESS_TYPE'));
      epmAddStr(e, 'KEY_ADDRESS', kg('KEY_ADDRESS'));
      const kt = epmEnumName(kg('KEY_TYPE'), EPM_KEY_TYPE_NAMES);
      if (kt === 'Signing' || kt === 'Encryption') e.KEY_TYPE = kt;
      epmAddStr(e, 'KEY_PATH', kg('KEY_PATH'));
      epmAddStr(e, 'ALGORITHM', kg('ALGORITHM'));
      epmAddStr(e, 'ENCODING', kg('ENCODING'));
      if (Object.keys(e).length) arr.push(e);
    }
    if (arr.length) content.KEYS = arr;
  }

  epmAddStrArray(content, 'MULTIFORMAT_ADDRESS', g('MULTIFORMAT_ADDRESS'));

  // ENTITY_TYPE: always present, verbatim enum label (default User, the FB default).
  const etRaw = g('ENTITY_TYPE');
  const et = etRaw == null ? EPM_ENTITY_TYPE_NAMES[0] : epmEnumName(etRaw, EPM_ENTITY_TYPE_NAMES);
  content.ENTITY_TYPE = typeof et === 'string' ? et : EPM_ENTITY_TYPE_NAMES[0];

  const ts = g('SIGNATURE_TIMESTAMP');
  const tsNum = Number(ts);
  if (ts != null && Number.isFinite(tsNum) && tsNum !== 0) {
    content.SIGNATURE_TIMESTAMP = Math.trunc(tsNum);
  }

  const proofs = g('CHAIN_PROOFS');
  if (Array.isArray(proofs)) {
    const arr = [];
    for (const p of proofs) {
      if (!p || typeof p !== 'object') continue;
      const e = {};
      const pg = (kk) => p[kk] ?? p[kk.toLowerCase()];
      epmAddStr(e, 'CHAIN', pg('CHAIN'));
      epmAddStr(e, 'ADDRESS', pg('ADDRESS'));
      epmAddStr(e, 'PUBLIC_KEY', pg('PUBLIC_KEY'));
      epmAddStr(e, 'KEY_PATH', pg('KEY_PATH'));
      epmAddStr(e, 'SIGNATURE', pg('SIGNATURE'));
      epmAddStr(e, 'SIGNED_PAYLOAD', pg('SIGNED_PAYLOAD'));
      epmAddStr(e, 'ALGORITHM', pg('ALGORITHM'));
      epmAddStr(e, 'ENCODING', pg('ENCODING'));
      if (Object.keys(e).length) arr.push(e);
    }
    if (arr.length) content.CHAIN_PROOFS = arr;
  }

  return new TextEncoder().encode(epmJcsCanonicalize(content));
}

// =============================================================================
// ECDSA encodings (formatting only; hashing and signing stay in native code)
// =============================================================================

const SECP256K1_ORDER = 0xfffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364141n;
const SECP256K1_HALF_ORDER = SECP256K1_ORDER >> 1n;

function bytesToBigInt(bytes) {
  let value = 0n;
  for (const b of bytes) value = (value << 8n) | BigInt(b);
  return value;
}

// One DER INTEGER for an unsigned big-endian scalar: minimal length, a 0x00
// pad when the high bit is set.
function derInteger(scalar) {
  let start = 0;
  while (start < scalar.length - 1 && scalar[start] === 0) start++;
  const body = scalar.subarray(start);
  const pad = body[0] & 0x80 ? 1 : 0;
  const out = new Uint8Array(2 + pad + body.length);
  out[0] = 0x02;
  out[1] = pad + body.length;
  out.set(body, 2 + pad);
  return out;
}

/**
 * Encode a 64-byte compact r||s ECDSA signature as DER
 * (SEQUENCE { INTEGER r, INTEGER s }), the SDS EPM annex encoding.
 *
 * @param {Uint8Array} compact - 64-byte r||s
 * @returns {Uint8Array} DER signature
 */
export function compactSignatureToDer(compact) {
  if (!(compact instanceof Uint8Array) || compact.length !== 64) {
    throw new TypeError('compact signature must be 64 bytes (r||s)');
  }
  const r = derInteger(compact.subarray(0, 32));
  const s = derInteger(compact.subarray(32, 64));
  const der = new Uint8Array(2 + r.length + s.length);
  der[0] = 0x30;
  der[1] = r.length + s.length;
  der.set(r, 2);
  der.set(s, 2 + r.length);
  return der;
}

// Strict DER: exactly SEQUENCE { INTEGER r, INTEGER s } with minimal,
// positive, nonzero integers of at most 32 bytes and no trailing bytes.
function isStrictSecp256k1Der(der) {
  if (!(der instanceof Uint8Array) || der.length < 8 || der.length > 72) return false;
  if (der[0] !== 0x30 || der[1] !== der.length - 2) return false;
  let pos = 2;
  for (let i = 0; i < 2; i++) {
    if (der[pos] !== 0x02) return false;
    const len = der[pos + 1];
    pos += 2;
    if (len < 1 || len > 33 || pos + len > der.length) return false;
    const body = der.subarray(pos, pos + len);
    if (body[0] & 0x80) return false; // negative
    if (len > 1 && body[0] === 0 && !(body[1] & 0x80)) return false; // not minimal
    if (len === 33 && body[0] !== 0) return false;
    const value = bytesToBigInt(body);
    if (value === 0n || value >= SECP256K1_ORDER) return false;
    pos += len;
  }
  return pos === der.length;
}

/**
 * Sign EPM content. Default curve is ed25519 (fast, the network default); pass
 * `{ curve: 'secp256k1' }` to sign with secp256k1: DER-encoded ECDSA over
 * SHA-256 of the canonical content, hashed once (SDS EPM annex; the Go and C++
 * EPM verifiers check exactly this). The content canonicalization is identical
 * for both curves; only the signature differs.
 *
 * @param {Object} wallet - Initialized HDWalletModule
 * @param {Object} epm - EPM fields as a plain object (without SIGNATURE/SIGNATURE_TIMESTAMP)
 * @param {Uint8Array} privateKey - 32-byte private key (ed25519 seed or secp256k1 key)
 * @param {{ curve?: 'ed25519'|'secp256k1' }} [options]
 * @returns {{ signature: string, timestamp: number }} Hex signature (ed25519: 64 raw
 *   bytes; secp256k1: DER) and Unix timestamp
 */
export function signEPMContent(wallet, epm, privateKey, options = {}) {
  const curve = String(options.curve || 'ed25519').toLowerCase();
  if (curve !== 'ed25519' && curve !== 'secp256k1') {
    throw new Error(`signEPMContent: unsupported curve "${curve}"`);
  }
  const timestamp = Math.floor(Date.now() / 1000);
  const content = buildEPMSigningContent({ ...epm, SIGNATURE_TIMESTAMP: timestamp });
  // curves.secp256k1.sign hashes its input with SHA-256: pass the content itself.
  const sig =
    curve === 'secp256k1'
      ? compactSignatureToDer(wallet.curves.secp256k1.sign(content, privateKey))
      : wallet.curves.ed25519.sign(content, privateKey);
  return {
    signature: wallet.utils.encodeHex(sig),
    timestamp,
  };
}

/**
 * Verify an EPM content signature. Dispatches on the explicit `options.curve`,
 * else infers from the public key length (32 = ed25519; 33/65 = secp256k1).
 * secp256k1 must be a strict DER ECDSA signature over SHA-256 of the canonical
 * content (hashed once), matching signEPMContent and the Go/C++ verifiers; a
 * signature over SHA-256(SHA-256(content)) does not verify.
 *
 * @param {Object} wallet - Initialized HDWalletModule
 * @param {Object} epm - Full EPM object including SIGNATURE and SIGNATURE_TIMESTAMP
 * @param {Uint8Array} publicKey - ed25519 (32B) or secp256k1 (33/65B) public key
 * @param {{ curve?: 'ed25519'|'secp256k1' }} [options]
 * @returns {boolean} True if signature is valid
 */
export function verifyEPMSignature(wallet, epm, publicKey, options = {}) {
  const sigHex = epm.SIGNATURE || epm.signature;
  if (!sigHex) return false;

  const content = buildEPMSigningContent(epm);
  const sig = wallet.utils.decodeHex(sigHex);
  const curve = String(
    options.curve || (publicKey && publicKey.length === 32 ? 'ed25519' : 'secp256k1'),
  ).toLowerCase();
  if (curve === 'secp256k1') {
    if (!isStrictSecp256k1Der(sig)) return false;
    return wallet.curves.secp256k1.verify(content, sig, publicKey);
  }
  if (curve === 'ed25519') {
    return wallet.curves.ed25519.verify(content, sig, publicKey);
  }
  return false;
}

// =============================================================================
// Chain Proof Building
// =============================================================================

// =============================================================================
// Chain message hashes (each chain's own signed-message protocol)
// =============================================================================

function concatBytes(...parts) {
  const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
  let offset = 0;
  for (const p of parts) {
    out.set(p, offset);
    offset += p.length;
  }
  return out;
}

// Bitcoin CompactSize (little-endian varint).
function compactSize(length) {
  if (length < 253) return Uint8Array.of(length);
  if (length <= 0xffff) return Uint8Array.of(253, length & 0xff, length >>> 8);
  if (length <= 0xffffffff) {
    return Uint8Array.of(254, length & 0xff, (length >>> 8) & 0xff, (length >>> 16) & 0xff, length >>> 24);
  }
  throw new RangeError('message too long for a Bitcoin signed message');
}

const BITCOIN_MESSAGE_MAGIC = new TextEncoder().encode('\x18Bitcoin Signed Message:\n');

/**
 * Bitcoin signed-message hash: SHA-256(SHA-256("\x18Bitcoin Signed Message:\n"
 * || CompactSize(len) || message)), the digest Bitcoin Core's signmessage signs
 * and SDN's Go verifier recovers from. The double SHA-256 is the protocol's.
 *
 * @param {Object} wallet - Initialized HDWalletModule
 * @param {Uint8Array} message - Message bytes
 * @returns {Uint8Array} 32-byte digest
 */
export function bitcoinSignedMessageHash(wallet, message) {
  const preimage = concatBytes(BITCOIN_MESSAGE_MAGIC, compactSize(message.length), message);
  return wallet.utils.sha256(wallet.utils.sha256(preimage));
}

/**
 * Ethereum EIP-191 personal_sign hash: Keccak-256("\x19Ethereum Signed Message:\n"
 * || decimal(len) || message).
 *
 * @param {Object} wallet - Initialized HDWalletModule
 * @param {Uint8Array} message - Message bytes
 * @returns {Uint8Array} 32-byte digest
 */
export function ethereumPersonalSignHash(wallet, message) {
  const prefix = new TextEncoder().encode(`\x19Ethereum Signed Message:\n${message.length}`);
  return wallet.utils.keccak256(concatBytes(prefix, message));
}

// Recoverable signature over a protocol digest -> 65-byte compact header||r||s
// (header 31 + recovery id: a compressed key, as Bitcoin Core and dcrd emit).
function signCompactRecoverable(wallet, digest, privateKey) {
  const { signature, recoveryId } = wallet.curves.secp256k1.signRecoverable(digest, privateKey);
  return concatBytes(Uint8Array.of(31 + recoveryId), signature);
}

// Recover the compressed key from a 65-byte header||r||s signature over a
// protocol digest; null when malformed. Headers 27..34 as dcrd accepts.
function recoverCompact(wallet, digest, signature, { requireLowS }) {
  if (signature.length !== 65) return null;
  const header = signature[0];
  if (header < 27 || header > 34) return null;
  const r = bytesToBigInt(signature.subarray(1, 33));
  const s = bytesToBigInt(signature.subarray(33, 65));
  if (r === 0n || r >= SECP256K1_ORDER || s === 0n || s >= SECP256K1_ORDER) return null;
  if (requireLowS && s > SECP256K1_HALF_ORDER) return null;
  try {
    return wallet.curves.secp256k1.recover(digest, signature.subarray(1, 65), (header - 27) & 3);
  } catch {
    return null;
  }
}

function bytesEqual(a, b) {
  if (a.length !== b.length) return false;
  let diff = 0;
  for (let i = 0; i < a.length; i++) diff |= a[i] ^ b[i];
  return diff === 0;
}

// =============================================================================
// Chain Proof Building
// =============================================================================

/**
 * Build a Bitcoin chain proof: a Bitcoin signed message (signmessage) over the
 * canonical payload, as a 65-byte compact recoverable signature
 * (header 31+recid || r || s).
 *
 * @param {Object} wallet - Initialized HDWalletModule
 * @param {Object} params
 * @param {string} params.address - Bitcoin address
 * @param {string} params.publicKeyHex - Compressed secp256k1 public key (hex)
 * @param {Uint8Array} params.privateKey - 32-byte secp256k1 private key
 * @param {string} params.keyPath - BIP-44 derivation path
 * @param {string} params.canonicalPayload - Result of buildCanonicalPayload()
 * @returns {Object} ChainProof object
 */
export function buildBitcoinChainProof(wallet, { address, publicKeyHex, privateKey, keyPath, canonicalPayload }) {
  const payloadBytes = new TextEncoder().encode(canonicalPayload);
  const sig = signCompactRecoverable(wallet, bitcoinSignedMessageHash(wallet, payloadBytes), privateKey);

  return {
    CHAIN: 'bitcoin',
    ADDRESS: address,
    PUBLIC_KEY: publicKeyHex,
    KEY_PATH: keyPath,
    SIGNATURE: wallet.utils.encodeHex(sig),
    SIGNED_PAYLOAD: wallet.utils.encodeHex(payloadBytes),
    ALGORITHM: 'secp256k1-compact-bitcoin',
    ENCODING: 'compact',
  };
}

/**
 * Build an Ethereum chain proof: an EIP-191 personal_sign signature over the
 * canonical payload, as a 65-byte compact recoverable signature
 * (header 31+recid || r || s, low-S).
 *
 * @param {Object} wallet - Initialized HDWalletModule
 * @param {Object} params
 * @param {string} params.address - Ethereum address (0x-prefixed)
 * @param {string} params.publicKeyHex - Compressed secp256k1 public key (hex)
 * @param {Uint8Array} params.privateKey - 32-byte secp256k1 private key
 * @param {string} params.keyPath - BIP-44 derivation path
 * @param {string} params.canonicalPayload - Result of buildCanonicalPayload()
 * @returns {Object} ChainProof object
 */
export function buildEthereumChainProof(wallet, { address, publicKeyHex, privateKey, keyPath, canonicalPayload }) {
  const payloadBytes = new TextEncoder().encode(canonicalPayload);
  const sig = signCompactRecoverable(wallet, ethereumPersonalSignHash(wallet, payloadBytes), privateKey);

  return {
    CHAIN: 'ethereum',
    ADDRESS: address,
    PUBLIC_KEY: publicKeyHex,
    KEY_PATH: keyPath,
    SIGNATURE: wallet.utils.encodeHex(sig),
    SIGNED_PAYLOAD: wallet.utils.encodeHex(payloadBytes),
    ALGORITHM: 'secp256k1-compact-ethereum',
    ENCODING: 'compact',
  };
}

/**
 * Build a Solana chain proof.
 * Signs the canonical payload with Ed25519.
 *
 * @param {Object} wallet - Initialized HDWalletModule
 * @param {Object} params
 * @param {string} params.address - Solana address (base58)
 * @param {string} params.publicKeyHex - Ed25519 public key (hex)
 * @param {Uint8Array} params.privateKey - 32-byte Ed25519 private key (seed)
 * @param {string} params.keyPath - BIP-44 derivation path
 * @param {string} params.canonicalPayload - Result of buildCanonicalPayload()
 * @returns {Object} ChainProof object
 */
export function buildSolanaChainProof(wallet, { address, publicKeyHex, privateKey, keyPath, canonicalPayload }) {
  const payloadBytes = new TextEncoder().encode(canonicalPayload);
  const sig = wallet.curves.ed25519.sign(payloadBytes, privateKey);

  return {
    CHAIN: 'solana',
    ADDRESS: address,
    PUBLIC_KEY: publicKeyHex,
    KEY_PATH: keyPath,
    SIGNATURE: wallet.utils.encodeHex(sig),
    SIGNED_PAYLOAD: wallet.utils.encodeHex(payloadBytes),
    ALGORITHM: 'ed25519',
    ENCODING: 'raw-ed25519',
  };
}

// =============================================================================
// Chain Proof Verification
// =============================================================================

/**
 * Verify a single chain proof. Bitcoin and Ethereum proofs are checked as their
 * chains do (and as SDN's Go verifier does): hash the payload with the chain's
 * signed-message protocol, recover the key from the 65-byte compact signature,
 * and compare it with PUBLIC_KEY. Ethereum proofs must be low-S.
 *
 * @param {Object} wallet - Initialized HDWalletModule
 * @param {Object} proof - ChainProof object with CHAIN, PUBLIC_KEY, SIGNATURE, SIGNED_PAYLOAD, ALGORITHM
 * @returns {boolean} True if the signature is valid for the given public key and payload
 */
export function verifyChainProof(wallet, proof) {
  const pubKey = wallet.utils.decodeHex(proof.PUBLIC_KEY);
  const sig = wallet.utils.decodeHex(proof.SIGNATURE);
  const payload = wallet.utils.decodeHex(proof.SIGNED_PAYLOAD);

  const algorithm = proof.ALGORITHM;

  if (algorithm === 'ed25519' || algorithm === 'raw-ed25519') {
    return wallet.curves.ed25519.verify(payload, sig, pubKey);
  }

  if (algorithm === 'secp256k1-compact-bitcoin' || algorithm === 'secp256k1-compact-ethereum') {
    if (proof.ENCODING !== undefined && proof.ENCODING !== 'compact') return false;
    if (pubKey.length !== 33) return false;
    const ethereum = algorithm === 'secp256k1-compact-ethereum';
    const digest = ethereum
      ? ethereumPersonalSignHash(wallet, payload)
      : bitcoinSignedMessageHash(wallet, payload);
    const recovered = recoverCompact(wallet, digest, sig, { requireLowS: ethereum });
    return recovered !== null && bytesEqual(recovered, pubKey);
  }

  return false;
}

/**
 * Verify all chain proofs in an EPM.
 *
 * @param {Object} wallet - Initialized HDWalletModule
 * @param {Object[]} chainProofs - Array of ChainProof objects
 * @returns {{ valid: boolean, results: Array<{ chain: string, valid: boolean }> }}
 */
export function verifyAllChainProofs(wallet, chainProofs) {
  if (!chainProofs || chainProofs.length === 0) {
    return { valid: false, results: [] };
  }

  const results = chainProofs.map((proof) => ({
    chain: proof.CHAIN,
    valid: verifyChainProof(wallet, proof),
  }));

  return {
    valid: results.every((r) => r.valid),
    results,
  };
}

/**
 * Build all chain proofs for a full identity attestation.
 *
 * @param {Object} wallet - Initialized HDWalletModule
 * @param {Object} params
 * @param {string} params.canonicalPayload - Result of buildCanonicalPayload()
 * @param {Object} params.bitcoin - { address, publicKeyHex, privateKey, keyPath }
 * @param {Object} params.ethereum - { address, publicKeyHex, privateKey, keyPath }
 * @param {Object} params.solana - { address, publicKeyHex, privateKey, keyPath }
 * @returns {Object[]} Array of ChainProof objects
 */
export function buildAllChainProofs(wallet, { canonicalPayload, bitcoin, ethereum, solana }) {
  const proofs = [];

  if (bitcoin) {
    proofs.push(buildBitcoinChainProof(wallet, { ...bitcoin, canonicalPayload }));
  }
  if (ethereum) {
    proofs.push(buildEthereumChainProof(wallet, { ...ethereum, canonicalPayload }));
  }
  if (solana) {
    proofs.push(buildSolanaChainProof(wallet, { ...solana, canonicalPayload }));
  }

  return proofs;
}

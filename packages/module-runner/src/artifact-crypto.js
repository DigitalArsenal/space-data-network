/**
 * Artifact crypto — isomorphic WebCrypto implementation of the
 * ecies-x25519-hkdf-sha256-aes-256-gcm encrypted artifact envelope.
 *
 * Matches the Go DecryptStagedArtifactEnvelope / EncryptStagedArtifactEnvelope
 * in sdn-server/internal/license/plugins.go exactly.
 *
 * Works in browser (WebCrypto) and Node.js 18+ (crypto.subtle).
 *
 * Envelope JSON schema:
 * {
 *   keyEncryption: {
 *     scheme: "ecies-x25519-hkdf-sha256-aes-256-gcm",
 *     ephemeralPublicKeyHex: "<hex 32 bytes>",
 *     hkdfSaltB64: "<base64>",
 *     wrapIvB64: "<base64 12 bytes>",
 *     wrappedKeyB64: "<base64>",
 *     wrappedKeyTagB64: "<base64 16 bytes>"
 *   },
 *   contentEncryption: {
 *     algorithm: "aes-256-gcm",
 *     ivB64: "<base64 12 bytes>",
 *     tagB64: "<base64 16 bytes>",
 *     ciphertextB64: "<base64>"
 *   }
 * }
 */

const SCHEME = "ecies-x25519-hkdf-sha256-aes-256-gcm";
const ALGORITHM = "aes-256-gcm";

// HKDF info strings tried in order (matches Go envelopeKeyWrapInfos)
const WRAP_INFOS = [
  "orbpro-key-server-artifact-wrap-v1",
  "plugin-key-server-artifact-wrap-v1",
];

// --- Encoding helpers ---

function hexToBytes(hex) {
  const h = hex.replace(/^0x/i, "");
  const bytes = new Uint8Array(h.length / 2);
  for (let i = 0; i < bytes.length; i++) {
    bytes[i] = parseInt(h.slice(i * 2, i * 2 + 2), 16);
  }
  return bytes;
}

function bytesToHex(bytes) {
  return Array.from(bytes).map((b) => b.toString(16).padStart(2, "0")).join("");
}

function fromBase64(b64) {
  // Handle both standard and URL-safe base64, with or without padding
  const padded = b64.replace(/-/g, "+").replace(/_/g, "/");
  const raw = atob(padded);
  const bytes = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i++) bytes[i] = raw.charCodeAt(i);
  return bytes;
}

function toBase64(bytes) {
  let binary = "";
  for (let i = 0; i < bytes.length; i++) binary += String.fromCharCode(bytes[i]);
  return btoa(binary);
}

function concat(...arrays) {
  const total = arrays.reduce((n, a) => n + a.length, 0);
  const out = new Uint8Array(total);
  let off = 0;
  for (const a of arrays) { out.set(a, off); off += a.length; }
  return out;
}

// --- X25519 key helpers (PKCS8 / SPKI wrappers) ---

// PKCS8 header for X25519 (OID 1.3.101.110): 16 bytes + 32-byte raw key
const PKCS8_HEADER = hexToBytes("302e020100300506032b656e04220420");
// SPKI header for X25519: 12 bytes + 32-byte raw key
const SPKI_HEADER = hexToBytes("302a300506032b656e032100");

async function importX25519Private(rawBytes) {
  const pkcs8 = concat(PKCS8_HEADER, rawBytes);
  return crypto.subtle.importKey("pkcs8", pkcs8, "X25519", false, ["deriveBits"]);
}

async function importX25519Public(rawBytes) {
  const spki = concat(SPKI_HEADER, rawBytes);
  return crypto.subtle.importKey("spki", spki, "X25519", false, []);
}

/** Generate an X25519 key pair, returning raw 32-byte keys. */
export async function generateX25519KeyPair() {
  const pair = await crypto.subtle.generateKey("X25519", true, ["deriveBits"]);
  const spki = new Uint8Array(await crypto.subtle.exportKey("spki", pair.publicKey));
  const pkcs8 = new Uint8Array(await crypto.subtle.exportKey("pkcs8", pair.privateKey));
  return {
    publicKey: spki.slice(12),   // strip 12-byte SPKI header
    privateKey: pkcs8.slice(16), // strip 16-byte PKCS8 header
  };
}

async function x25519ECDH(privateKeyBytes, publicKeyBytes) {
  const priv = await importX25519Private(privateKeyBytes);
  const pub = await importX25519Public(publicKeyBytes);
  const bits = await crypto.subtle.deriveBits({ name: "X25519", public: pub }, priv, 256);
  return new Uint8Array(bits);
}

async function hkdfSHA256(sharedSecret, salt, info, keyLenBytes) {
  const raw = await crypto.subtle.importKey("raw", sharedSecret, "HKDF", false, ["deriveBits"]);
  const infoBytes = typeof info === "string" ? new TextEncoder().encode(info) : info;
  const bits = await crypto.subtle.deriveBits(
    { name: "HKDF", hash: "SHA-256", salt, info: infoBytes },
    raw,
    keyLenBytes * 8,
  );
  return new Uint8Array(bits);
}

async function aesGcmDecrypt(keyBytes, iv, ciphertext, tag) {
  const key = await crypto.subtle.importKey("raw", keyBytes, "AES-GCM", false, ["decrypt"]);
  // WebCrypto expects ciphertext+tag concatenated
  const plain = await crypto.subtle.decrypt(
    { name: "AES-GCM", iv },
    key,
    concat(ciphertext, tag),
  );
  return new Uint8Array(plain);
}

async function aesGcmEncrypt(keyBytes, iv, plaintext) {
  const key = await crypto.subtle.importKey("raw", keyBytes, "AES-GCM", false, ["encrypt"]);
  const result = await crypto.subtle.encrypt({ name: "AES-GCM", iv }, key, plaintext);
  const bytes = new Uint8Array(result);
  // Split ciphertext and 16-byte tag
  const ciphertext = bytes.slice(0, bytes.length - 16);
  const tag = bytes.slice(bytes.length - 16);
  return { ciphertext, tag };
}

// --- Public API ---

/**
 * Decrypt a staged artifact envelope.
 *
 * @param {string|object} envelope — JSON string or parsed object
 * @param {Uint8Array}    recipientPrivateKey — 32-byte X25519 private key
 * @returns {Promise<Uint8Array>} decrypted plaintext (e.g. WASM bytes)
 */
export async function decryptArtifact(envelope, recipientPrivateKey) {
  if (recipientPrivateKey.length !== 32) {
    throw new Error(`Invalid private key length: expected 32, got ${recipientPrivateKey.length}`);
  }
  const env = typeof envelope === "string" ? JSON.parse(envelope) : envelope;

  const scheme = (env.keyEncryption?.scheme ?? "").trim();
  if (scheme !== SCHEME) {
    throw new Error(`Unsupported envelope scheme: "${scheme}"`);
  }

  const ephemeralPub = hexToBytes(env.keyEncryption.ephemeralPublicKeyHex);
  if (ephemeralPub.length !== 32) {
    throw new Error("Invalid ephemeral public key length");
  }

  const sharedSecret = await x25519ECDH(recipientPrivateKey, ephemeralPub);
  const hkdfSalt = fromBase64(env.keyEncryption.hkdfSaltB64);
  const wrapIV = fromBase64(env.keyEncryption.wrapIvB64);
  const wrappedKey = fromBase64(env.keyEncryption.wrappedKeyB64);
  const wrappedKeyTag = fromBase64(env.keyEncryption.wrappedKeyTagB64);

  // Try each HKDF info string until one succeeds (matches Go's envelopeKeyWrapInfos)
  let contentKey = null;
  let lastErr = null;
  for (const info of WRAP_INFOS) {
    try {
      const wrapKey = await hkdfSHA256(sharedSecret, hkdfSalt, info, 32);
      contentKey = await aesGcmDecrypt(wrapKey, wrapIV, wrappedKey, wrappedKeyTag);
      break;
    } catch (err) {
      lastErr = err;
    }
  }
  if (!contentKey) {
    throw new Error(`Failed to unwrap content key: ${lastErr?.message}`);
  }

  const contentIV = fromBase64(env.contentEncryption.ivB64);
  const contentTag = fromBase64(env.contentEncryption.tagB64);
  const ciphertext = fromBase64(env.contentEncryption.ciphertextB64);

  return aesGcmDecrypt(contentKey, contentIV, ciphertext, contentTag);
}

/**
 * Encrypt plaintext bytes into a staged artifact envelope.
 *
 * @param {Uint8Array} plaintext
 * @param {Uint8Array} recipientPublicKey — 32-byte X25519 public key
 * @param {string}     [wrapInfo]         — HKDF info string (default: first in WRAP_INFOS)
 * @returns {Promise<object>} the envelope object (JSON-serializable)
 */
export async function encryptArtifact(
  plaintext,
  recipientPublicKey,
  wrapInfo = WRAP_INFOS[0],
) {
  if (recipientPublicKey.length !== 32) {
    throw new Error(`Invalid public key length: expected 32, got ${recipientPublicKey.length}`);
  }

  // Generate ephemeral X25519 key pair
  const ephemeral = await generateX25519KeyPair();

  const sharedSecret = await x25519ECDH(ephemeral.privateKey, recipientPublicKey);

  // HKDF salt: 32 random bytes
  const hkdfSalt = crypto.getRandomValues(new Uint8Array(32));

  // Derive wrap key
  const wrapKey = await hkdfSHA256(sharedSecret, hkdfSalt, wrapInfo, 32);

  // Generate content key: 32 random bytes
  const contentKey = crypto.getRandomValues(new Uint8Array(32));

  // Wrap the content key
  const wrapIV = crypto.getRandomValues(new Uint8Array(12));
  const { ciphertext: wrappedKey, tag: wrappedKeyTag } = await aesGcmEncrypt(
    wrapKey,
    wrapIV,
    contentKey,
  );

  // Encrypt the plaintext
  const contentIV = crypto.getRandomValues(new Uint8Array(12));
  const { ciphertext, tag: contentTag } = await aesGcmEncrypt(
    contentKey,
    contentIV,
    plaintext,
  );

  return {
    keyEncryption: {
      scheme: SCHEME,
      ephemeralPublicKeyHex: bytesToHex(ephemeral.publicKey),
      hkdfSaltB64: toBase64(hkdfSalt),
      wrapIvB64: toBase64(wrapIV),
      wrappedKeyB64: toBase64(wrappedKey),
      wrappedKeyTagB64: toBase64(wrappedKeyTag),
    },
    contentEncryption: {
      algorithm: ALGORITHM,
      ivB64: toBase64(contentIV),
      tagB64: toBase64(contentTag),
      ciphertextB64: toBase64(ciphertext),
    },
  };
}

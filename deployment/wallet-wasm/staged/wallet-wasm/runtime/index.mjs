/**
 * HD Wallet WASM - JavaScript ES6 Wrapper
 *
 * Comprehensive HD wallet implementation with:
 * - BIP-32/39/44/49/84 support
 * - Multi-curve cryptography (secp256k1, Ed25519, P-256, P-384, X25519)
 * - Multi-chain support (Bitcoin, Ethereum, Solana, Cosmos, Polkadot)
 * - Hardware wallet abstraction (requires bridge)
 * - Transaction building and signing
 *
 * @module hd-wallet-wasm
 * @version 2.0.29
 */

// Import aligned API for batch operations
import { AlignedAPI } from './aligned.mjs';
import {
  createSdnPluginContract,
  SDN_PLUGIN_MANIFEST_EXPORTS
} from './sdn-plugin.mjs';
import { HD_WALLET_SDN_PLUGIN_MANIFEST } from './sdn-plugin-manifest-source.mjs';
import { createSdnTypedCapabilities } from './sdn-typed.mjs';

const walletOriginCapabilitiesByModule = new WeakMap();

function createWalletOriginCapabilitiesBinding(module) {
  const sdn = module.sdn;
  const utils = module.utils;
  const wasmSha256 = utils.sha256;
  return Object.freeze({
    sdn,
    sha256(bytes) {
      return wasmSha256(bytes);
    },
  });
}

function installWalletOriginCapabilities(module) {
  const binding = createWalletOriginCapabilitiesBinding(module);
  Object.defineProperty(module, 'walletOriginCapabilities', {
    value: binding,
    enumerable: false,
    writable: false,
    configurable: false,
  });
  walletOriginCapabilitiesByModule.set(module, binding);
}

/**
 * Resolve the wallet-origin binding only for the exact initialized owner.
 * Forged pairs and copied bindings are never accepted as module authority.
 *
 * @param {Object} module - An initialized HDWalletModule returned by init().
 * @returns {{sdn: Object, sha256: (bytes: Uint8Array) => Uint8Array}}
 */
export function getWalletOriginCapabilities(module) {
  if (module === null || typeof module !== 'object') {
    throw new TypeError('Invalid HD wallet module');
  }
  const binding = walletOriginCapabilitiesByModule.get(module);
  if (!binding) throw new TypeError('Invalid HD wallet module');
  const descriptor = Object.getOwnPropertyDescriptor(
    module,
    'walletOriginCapabilities',
  );
  if (!descriptor || descriptor.value !== binding || descriptor.enumerable ||
      descriptor.writable || descriptor.configurable) {
    throw new TypeError('Invalid HD wallet module');
  }
  return binding;
}

// =============================================================================
// Enums (matching TypeScript definitions)
// =============================================================================

/**
 * Elliptic curve types
 * @readonly
 * @enum {number}
 */
export const Curve = Object.freeze({
  SECP256K1: 0,
  ED25519: 1,
  P256: 2,
  P384: 3,
  X25519: 4
});

/**
 * SLIP-44 coin types
 * @readonly
 * @enum {number}
 */
export const CoinType = Object.freeze({
  BITCOIN: 0,
  BITCOIN_TESTNET: 1,
  LITECOIN: 2,
  DOGECOIN: 3,
  ETHEREUM: 60,
  ETHEREUM_CLASSIC: 61,
  COSMOS: 118,
  STELLAR: 148,
  BITCOIN_CASH: 145,
  POLKADOT: 354,
  KUSAMA: 434,
  SOLANA: 501,
  BINANCE: 714,
  CARDANO: 1815
});

/**
 * BIP-39 wordlist languages
 * - ENGLISH is supported in the default build.
 * - Other locales are currently reserved and return NOT_SUPPORTED.
 * @readonly
 * @enum {number}
 */
export const Language = Object.freeze({
  ENGLISH: 0,
  JAPANESE: 1,
  KOREAN: 2,
  SPANISH: 3,
  CHINESE_SIMPLIFIED: 4,
  CHINESE_TRADITIONAL: 5,
  FRENCH: 6,
  ITALIAN: 7,
  CZECH: 8,
  PORTUGUESE: 9
});

/**
 * WASI feature flags
 * @readonly
 * @enum {number}
 */
export const WasiFeature = Object.freeze({
  RANDOM: 0,
  FILESYSTEM: 1,
  NETWORK: 2,
  USB_HID: 3,
  CLOCK: 4,
  ENVIRONMENT: 5
});

/**
 * WASI warning codes
 * @readonly
 * @enum {number}
 */
export const WasiWarning = Object.freeze({
  NONE: 0,
  NEEDS_ENTROPY: 1,
  NEEDS_BRIDGE: 2,
  NOT_AVAILABLE_WASI: 3,
  DISABLED_FIPS: 4,
  NEEDS_CAPABILITY: 5
});

/**
 * Entropy status
 * @readonly
 * @enum {number}
 */
export const EntropyStatus = Object.freeze({
  NOT_INITIALIZED: 0,
  INITIALIZED: 1,
  SUFFICIENT: 2
});

/**
 * Bitcoin address types
 * @readonly
 * @enum {number}
 */
export const BitcoinAddressType = Object.freeze({
  P2PKH: 0,
  P2SH: 1,
  P2WPKH: 2,
  P2WSH: 3,
  P2TR: 4
});

/**
 * Bitcoin transaction script types (matches C++ tx::ScriptType)
 * @readonly
 * @enum {number}
 */
export const BitcoinScriptType = Object.freeze({
  UNKNOWN: 0,
  P2PKH: 1,
  P2SH: 2,
  P2WPKH: 3,
  P2WSH: 4,
  P2TR: 5,
  P2SH_P2WPKH: 6,
  P2SH_P2WSH: 7,
  NULLDATA: 8,
  MULTISIG: 9
});

/**
 * Network type
 * @readonly
 * @enum {number}
 */
export const Network = Object.freeze({
  MAINNET: 0,
  TESTNET: 1
});

/**
 * X.509 certificate encoding formats
 * @readonly
 * @enum {number}
 */
export const X509Encoding = Object.freeze({
  PEM: 0,
  DER: 1,
  PKCS12: 2
});

// =============================================================================
// Error handling
// =============================================================================

/**
 * HD Wallet error codes
 * @readonly
 * @enum {number}
 */
const ErrorCode = Object.freeze({
  OK: 0,
  UNKNOWN: 1,
  INVALID_ARGUMENT: 2,
  NOT_SUPPORTED: 3,
  OUT_OF_MEMORY: 4,
  INTERNAL: 5,
  NO_ENTROPY: 100,
  INSUFFICIENT_ENTROPY: 101,
  INVALID_WORD: 200,
  INVALID_CHECKSUM: 201,
  INVALID_MNEMONIC_LENGTH: 202,
  INVALID_ENTROPY_LENGTH: 203,
  INVALID_SEED: 300,
  INVALID_PATH: 301,
  INVALID_CHILD_INDEX: 302,
  HARDENED_FROM_PUBLIC: 303,
  INVALID_EXTENDED_KEY: 304,
  INVALID_PRIVATE_KEY: 400,
  INVALID_PUBLIC_KEY: 401,
  INVALID_SIGNATURE: 402,
  VERIFICATION_FAILED: 403,
  KEY_DERIVATION_FAILED: 404,
  INVALID_TRANSACTION: 500,
  INSUFFICIENT_FUNDS: 501,
  INVALID_ADDRESS: 502,
  DEVICE_NOT_CONNECTED: 600,
  DEVICE_COMM_ERROR: 601,
  USER_CANCELLED: 602,
  DEVICE_BUSY: 603,
  DEVICE_NOT_SUPPORTED: 604,
  BRIDGE_NOT_SET: 700,
  BRIDGE_FAILED: 701,
  NEEDS_BRIDGE: 702,
  FIPS_NOT_ALLOWED: 800
});

/**
 * Error message map
 */
const ERROR_MESSAGES = {
  [ErrorCode.OK]: 'Success',
  [ErrorCode.UNKNOWN]: 'Unknown error',
  [ErrorCode.INVALID_ARGUMENT]: 'Invalid argument',
  [ErrorCode.NOT_SUPPORTED]: 'Operation not supported',
  [ErrorCode.OUT_OF_MEMORY]: 'Out of memory',
  [ErrorCode.INTERNAL]: 'Internal error',
  [ErrorCode.NO_ENTROPY]: 'No entropy available - call injectEntropy() first',
  [ErrorCode.INSUFFICIENT_ENTROPY]: 'Insufficient entropy',
  [ErrorCode.INVALID_WORD]: 'Invalid mnemonic word',
  [ErrorCode.INVALID_CHECKSUM]: 'Invalid mnemonic checksum',
  [ErrorCode.INVALID_MNEMONIC_LENGTH]: 'Invalid mnemonic length',
  [ErrorCode.INVALID_ENTROPY_LENGTH]: 'Invalid entropy length',
  [ErrorCode.INVALID_SEED]: 'Invalid seed',
  [ErrorCode.INVALID_PATH]: 'Invalid derivation path',
  [ErrorCode.INVALID_CHILD_INDEX]: 'Invalid child index',
  [ErrorCode.HARDENED_FROM_PUBLIC]: 'Cannot derive hardened child from public key',
  [ErrorCode.INVALID_EXTENDED_KEY]: 'Invalid extended key format',
  [ErrorCode.INVALID_PRIVATE_KEY]: 'Invalid private key',
  [ErrorCode.INVALID_PUBLIC_KEY]: 'Invalid public key',
  [ErrorCode.INVALID_SIGNATURE]: 'Invalid signature',
  [ErrorCode.VERIFICATION_FAILED]: 'Signature verification failed',
  [ErrorCode.KEY_DERIVATION_FAILED]: 'Key derivation failed',
  [ErrorCode.INVALID_TRANSACTION]: 'Invalid transaction',
  [ErrorCode.INSUFFICIENT_FUNDS]: 'Insufficient funds',
  [ErrorCode.INVALID_ADDRESS]: 'Invalid address',
  [ErrorCode.DEVICE_NOT_CONNECTED]: 'Hardware device not connected',
  [ErrorCode.DEVICE_COMM_ERROR]: 'Hardware device communication error',
  [ErrorCode.USER_CANCELLED]: 'Operation cancelled by user',
  [ErrorCode.DEVICE_BUSY]: 'Hardware device busy',
  [ErrorCode.DEVICE_NOT_SUPPORTED]: 'Operation not supported by this device',
  [ErrorCode.BRIDGE_NOT_SET]: 'Bridge callback not set',
  [ErrorCode.BRIDGE_FAILED]: 'Bridge callback failed',
  [ErrorCode.NEEDS_BRIDGE]: 'Feature requires WASI bridge',
  [ErrorCode.FIPS_NOT_ALLOWED]: 'Algorithm not allowed in FIPS mode'
};

/**
 * HD Wallet Error class
 */
class HDWalletError extends Error {
  /**
   * @param {number} code - Error code
   * @param {string} [message] - Optional custom message
   */
  constructor(code, message) {
    const lookup = code < 0 ? -code : code;
    super(message || ERROR_MESSAGES[lookup] || `Error code: ${code}`);
    this.name = 'HDWalletError';
    this.code = lookup;
  }
}

/**
 * Check result and throw if error
 * @param {number} result - Result code from WASM function
 * @throws {HDWalletError} If result is non-zero
 */
function checkResult(result) {
  if (result !== 0) {
    throw new HDWalletError(result);
  }
}

/**
 * Return a WASM function if exported by this build, otherwise null.
 * Some features are optional and can be omitted from lean builds.
 */
function getWasmFunction(wasm, name) {
  const fn = wasm[name];
  return typeof fn === 'function' ? fn : null;
}

/**
 * Require a WASM function to exist, otherwise throw a typed library error.
 */
function requireWasmFunction(wasm, name, code = ErrorCode.NOT_SUPPORTED, message) {
  const fn = getWasmFunction(wasm, name);
  if (!fn) {
    throw new HDWalletError(code, message || `Function ${name} is not available in this build`);
  }
  return fn;
}

// Encoding fallback constants (used when optional WASM codecs are not exported)
const BASE58_ALPHABET = '123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz';
const BASE58_INDEX = Object.freeze(Object.fromEntries(
  Array.from(BASE58_ALPHABET).map((char, i) => [char, i])
));
const BECH32_CHARSET = 'qpzry9x8gf2tvdw0s3jn54khce6mua7l';
const BECH32_INDEX = Object.freeze(Object.fromEntries(
  Array.from(BECH32_CHARSET).map((char, i) => [char, i])
));

function encodeBase58Fallback(data) {
  let zeros = 0;
  while (zeros < data.length && data[zeros] === 0) zeros++;

  const size = Math.floor((data.length - zeros) * 138 / 100) + 1;
  const b58 = new Uint8Array(size);

  for (let i = zeros; i < data.length; i++) {
    let carry = data[i];
    for (let j = size - 1; j >= 0; j--) {
      carry += 256 * b58[j];
      b58[j] = carry % 58;
      carry = Math.floor(carry / 58);
    }
  }

  let it = 0;
  while (it < b58.length && b58[it] === 0) it++;

  let out = '1'.repeat(zeros);
  while (it < b58.length) {
    out += BASE58_ALPHABET[b58[it++]];
  }
  return out;
}

function decodeBase58Fallback(str) {
  let zeros = 0;
  while (zeros < str.length && str[zeros] === '1') zeros++;

  const size = Math.floor((str.length - zeros) * 733 / 1000) + 1;
  const b256 = new Uint8Array(size);

  for (let i = zeros; i < str.length; i++) {
    const value = BASE58_INDEX[str[i]];
    if (value === undefined) {
      throw new HDWalletError(ErrorCode.INVALID_ARGUMENT, `Invalid Base58 character: ${str[i]}`);
    }

    let carry = value;
    for (let j = size - 1; j >= 0; j--) {
      carry += 58 * b256[j];
      b256[j] = carry % 256;
      carry = Math.floor(carry / 256);
    }
  }

  let it = 0;
  while (it < b256.length && b256[it] === 0) it++;

  const out = new Uint8Array(zeros + (b256.length - it));
  out.fill(0, 0, zeros);
  out.set(b256.slice(it), zeros);
  return out;
}

function encodeHexFallback(data) {
  return Array.from(data).map(b => b.toString(16).padStart(2, '0')).join('');
}

function decodeHexFallback(str) {
  if (str.length % 2 !== 0) {
    throw new HDWalletError(ErrorCode.INVALID_ARGUMENT, 'Hex string must have an even length');
  }
  if (!/^[0-9a-fA-F]*$/.test(str)) {
    throw new HDWalletError(ErrorCode.INVALID_ARGUMENT, 'Invalid hex string');
  }
  const out = new Uint8Array(str.length / 2);
  for (let i = 0; i < out.length; i++) {
    out[i] = parseInt(str.slice(i * 2, i * 2 + 2), 16);
  }
  return out;
}

function encodeBase64Fallback(data) {
  if (typeof Buffer !== 'undefined') {
    return Buffer.from(data).toString('base64');
  }
  let binary = '';
  for (let i = 0; i < data.length; i++) binary += String.fromCharCode(data[i]);
  return btoa(binary);
}

function decodeBase64Fallback(str) {
  const base64Pattern = /^[A-Za-z0-9+/]*={0,2}$/;
  if (str.length % 4 !== 0 || !base64Pattern.test(str)) {
    throw new HDWalletError(ErrorCode.INVALID_ARGUMENT, 'Invalid base64 string');
  }
  try {
    if (typeof Buffer !== 'undefined') {
      return new Uint8Array(Buffer.from(str, 'base64'));
    }
    const binary = atob(str);
    const out = new Uint8Array(binary.length);
    for (let i = 0; i < binary.length; i++) out[i] = binary.charCodeAt(i);
    return out;
  } catch (_) {
    throw new HDWalletError(ErrorCode.INVALID_ARGUMENT, 'Invalid base64 string');
  }
}

const UTF8_ENCODER = new TextEncoder();
const UTF8_DECODER = new TextDecoder();

function encodeBech32Fallback(hrp, data) {
  let out = `${hrp}1`;
  for (let i = 0; i < data.length; i++) {
    out += BECH32_CHARSET[data[i] & 0x1f];
  }
  return out;
}

function decodeBech32Fallback(str) {
  const sep = str.lastIndexOf('1');
  if (sep <= 0) {
    throw new HDWalletError(ErrorCode.INVALID_ARGUMENT, 'Invalid bech32 string');
  }
  const hrp = str.slice(0, sep);
  const dataPart = str.slice(sep + 1);
  const out = new Uint8Array(dataPart.length);

  for (let i = 0; i < dataPart.length; i++) {
    const value = BECH32_INDEX[dataPart[i].toLowerCase()];
    if (value === undefined) {
      throw new HDWalletError(ErrorCode.INVALID_ARGUMENT, `Invalid bech32 character: ${dataPart[i]}`);
    }
    out[i] = value;
  }

  return { hrp, data: out };
}

// =============================================================================
// Memory Helpers
// =============================================================================

/**
 * Allocate memory and copy data
 * @param {WebAssembly.Module} wasm - WASM module
 * @param {Uint8Array} data - Data to copy
 * @returns {number} Pointer to allocated memory
 */
function allocAndCopy(wasm, data) {
  // Many C++ implementations assume pointers are non-null even when `len == 0`.
  // Allocate a 1-byte placeholder in that case to avoid passing NULL into WASM.
  const len = data.length;
  const allocLen = len === 0 ? 1 : len;
  const ptr = wasm._hd_alloc(allocLen);
  if (!ptr) throw new HDWalletError(ErrorCode.OUT_OF_MEMORY);
  if (len > 0) wasm.HEAPU8.set(data, ptr);
  return ptr;
}

/**
 * Allocate memory for string and copy
 * @param {WebAssembly.Module} wasm - WASM module
 * @param {string} str - String to copy
 * @returns {number} Pointer to allocated memory
 */
function allocString(wasm, str) {
  const len = wasm.lengthBytesUTF8(str) + 1;
  const ptr = wasm._hd_alloc(len);
  if (!ptr) throw new HDWalletError(ErrorCode.OUT_OF_MEMORY);
  wasm.stringToUTF8(str, ptr, len);
  return ptr;
}

/**
 * Read bytes from WASM memory
 * @param {WebAssembly.Module} wasm - WASM module
 * @param {number} ptr - Pointer to memory
 * @param {number} len - Number of bytes to read
 * @returns {Uint8Array} Copy of the data
 */
function readBytes(wasm, ptr, len) {
  return new Uint8Array(wasm.HEAPU8.buffer, ptr, len).slice();
}

/**
 * Read null-terminated string from WASM memory
 * @param {WebAssembly.Module} wasm - WASM module
 * @param {number} ptr - Pointer to string
 * @returns {string} JavaScript string
 */
function readString(wasm, ptr) {
  return wasm.UTF8ToString(ptr);
}

function readSize32(wasm, ptr) {
  return wasm.getValue(ptr, 'i32') >>> 0;
}

function shouldRetryDynamicResult(result, capacity, requiredSize) {
  return result === -ErrorCode.OUT_OF_MEMORY && requiredSize > capacity;
}

function callDynamicBytesResult(wasm, invoke, options = {}) {
  const { wipeOutput = false, maxAttempts = 6 } = options;
  const lenPtr = wasm._hd_alloc(4);
  if (!lenPtr) throw new HDWalletError(ErrorCode.OUT_OF_MEMORY);

  try {
    wasm.setValue(lenPtr, 0, 'i32');
    checkResult(invoke(0, lenPtr));
    let requestedSize = readSize32(wasm, lenPtr);

    // Some outputs are regenerated on each call, so their exact size can grow
    // between the length probe and the materialization call.
    for (let attempt = 0; attempt < maxAttempts; attempt++) {
      const capacity = requestedSize > 0 ? requestedSize : 1;
      const outPtr = wasm._hd_alloc(capacity);
      if (!outPtr) throw new HDWalletError(ErrorCode.OUT_OF_MEMORY);

      try {
        wasm.setValue(lenPtr, capacity, 'i32');
        const result = invoke(outPtr, lenPtr);
        const actualSize = readSize32(wasm, lenPtr);
        if (result === 0) {
          return readBytes(wasm, outPtr, actualSize);
        }
        if (shouldRetryDynamicResult(result, capacity, actualSize)) {
          requestedSize = Math.max(actualSize, capacity * 2);
          continue;
        }
        checkResult(result);
      } finally {
        if (wipeOutput && capacity > 0) {
          wasm._hd_secure_wipe(outPtr, capacity);
        }
        wasm._hd_dealloc(outPtr);
      }
    }

    throw new HDWalletError(
      ErrorCode.OUT_OF_MEMORY,
      'Dynamic result size changed too many times while materializing output'
    );
  } finally {
    wasm._hd_dealloc(lenPtr);
  }
}

function callDynamicStringResult(wasm, invoke, options = {}) {
  const { wipeOutput = false, maxAttempts = 6 } = options;
  const lenPtr = wasm._hd_alloc(4);
  if (!lenPtr) throw new HDWalletError(ErrorCode.OUT_OF_MEMORY);

  try {
    wasm.setValue(lenPtr, 0, 'i32');
    checkResult(invoke(0, lenPtr));
    let requestedSize = readSize32(wasm, lenPtr);

    for (let attempt = 0; attempt < maxAttempts; attempt++) {
      const capacity = requestedSize > 0 ? requestedSize : 1;
      const outPtr = wasm._hd_alloc(capacity);
      if (!outPtr) throw new HDWalletError(ErrorCode.OUT_OF_MEMORY);

      try {
        wasm.setValue(lenPtr, capacity, 'i32');
        const result = invoke(outPtr, lenPtr);
        const actualSize = readSize32(wasm, lenPtr);
        if (result === 0) {
          return readString(wasm, outPtr);
        }
        if (shouldRetryDynamicResult(result, capacity, actualSize)) {
          requestedSize = Math.max(actualSize, capacity * 2);
          continue;
        }
        checkResult(result);
      } finally {
        if (wipeOutput && capacity > 0) {
          wasm._hd_secure_wipe(outPtr, capacity);
        }
        wasm._hd_dealloc(outPtr);
      }
    }

    throw new HDWalletError(
      ErrorCode.OUT_OF_MEMORY,
      'Dynamic result size changed too many times while materializing output'
    );
  } finally {
    wasm._hd_dealloc(lenPtr);
  }
}

function toUint8Array(data, name = 'data') {
  if (data instanceof Uint8Array) {
    return data;
  }
  if (ArrayBuffer.isView(data)) {
    return new Uint8Array(data.buffer, data.byteOffset, data.byteLength).slice();
  }
  if (data instanceof ArrayBuffer) {
    return new Uint8Array(data.slice(0));
  }
  throw new HDWalletError(ErrorCode.INVALID_ARGUMENT, `${name} must be binary data`);
}

function curveToName(curve) {
  switch (curve) {
    case Curve.SECP256K1: return 'secp256k1';
    case Curve.ED25519: return 'ed25519';
    case Curve.P256: return 'p256';
    case Curve.P384: return 'p384';
    case Curve.X25519: return 'x25519';
    default:
      throw new HDWalletError(ErrorCode.INVALID_ARGUMENT, `Unsupported curve: ${curve}`);
  }
}

function escapeX509ListValue(value) {
  return String(value).replace(/\\/g, '\\\\').replace(/,/g, '\\,');
}

function assertX509CertificateEncoding(encoding, name) {
  if (encoding === X509Encoding.PKCS12) {
    throw new HDWalletError(
      ErrorCode.INVALID_ARGUMENT,
      `${name} cannot be PKCS12; use exportPkcs12() or importPkcs12() for bundles`
    );
  }
}

function normalizeX509CertificateInput(certificate, encoding, name = 'certificate') {
  assertX509CertificateEncoding(encoding, 'encoding');
  if (encoding === X509Encoding.PEM) {
    if (typeof certificate === 'string') {
      return UTF8_ENCODER.encode(certificate);
    }
    return toUint8Array(certificate, name);
  }
  return toUint8Array(certificate, name);
}

function normalizePemInput(value, name) {
  if (typeof value === 'string') {
    return UTF8_ENCODER.encode(value);
  }
  return toUint8Array(value, name);
}

function decodeX509CertificateOutput(bytes, encoding) {
  return encoding === X509Encoding.PEM ? UTF8_DECODER.decode(bytes) : bytes;
}

function normalizeParsedCertificateJson(parsed) {
  return {
    subjectDn: parsed.subject_dn ?? '',
    issuerDn: parsed.issuer_dn ?? '',
    serialHex: parsed.serial_hex ?? '',
    notBefore: parsed.not_before ?? '',
    notAfter: parsed.not_after ?? '',
    isCa: parsed.is_ca === true,
    dnsNames: Array.isArray(parsed.dns_names) ? parsed.dns_names : [],
    ipAddresses: Array.isArray(parsed.ip_addresses) ? parsed.ip_addresses : [],
    emailAddresses: Array.isArray(parsed.email_addresses) ? parsed.email_addresses : [],
    uriNames: Array.isArray(parsed.uri_names) ? parsed.uri_names : [],
    walletAttestationComment: parsed.wallet_attestation_comment ?? '',
    walletAttestationValid: parsed.wallet_attestation_valid === true
  };
}

function normalizePkcs12ImportJson(parsed) {
  return {
    certificatePem: parsed.certificate_pem ?? '',
    privateKeyPem: parsed.private_key_pem ?? '',
    chainPem: parsed.chain_pem ?? ''
  };
}

function serializeX509Options(options) {
  if (!options || typeof options !== 'object') {
    throw new HDWalletError(ErrorCode.INVALID_ARGUMENT, 'X.509 options must be an object');
  }

  const lines = [];
  const pushScalar = (key, value) => {
    if (value === undefined || value === null || value === '') {
      return;
    }
    lines.push(`${key}=${value}`);
  };
  const pushList = (key, values) => {
    if (!Array.isArray(values) || values.length === 0) {
      return;
    }
    lines.push(`${key}=${values.map(escapeX509ListValue).join(',')}`);
  };

  pushScalar('subject_dn', options.subjectDn);
  pushScalar('serial_hex', options.serialHex);
  pushScalar('not_before_unix', options.notBeforeUnix);
  pushScalar('not_after_unix', options.notAfterUnix);
  if (options.isCa !== undefined) {
    pushScalar('is_ca', options.isCa ? 'true' : 'false');
  }
  if (options.pathLen !== undefined && options.pathLen !== null) {
    pushScalar('path_len', options.pathLen);
  }
  pushList('dns', options.dnsNames);
  pushList('ips', options.ipAddresses);
  pushList('emails', options.emailAddresses);
  pushList('uris', options.uriNames);
  pushList('key_usage', options.keyUsage);
  pushList('extended_key_usage', options.extendedKeyUsage);
  pushScalar('friendly_name', options.friendlyName);

  if (options.walletAttestation) {
    const attestation = options.walletAttestation;
    pushScalar('wallet_curve', curveToName(attestation.curve));
    pushScalar('wallet_private_key_hex', encodeHexFallback(toUint8Array(attestation.privateKey, 'walletAttestation.privateKey')));
    pushScalar('wallet_key_label', attestation.keyLabel);
    pushScalar('wallet_comment_prefix', attestation.commentPrefix);
  }

  return lines.join('\n');
}

// =============================================================================
// HDKey Class
// =============================================================================

/**
 * BIP-32 HD Key
 * Represents a node in the HD key derivation tree.
 */
class HDKey {
  /**
   * @param {WebAssembly.Module} wasm - WASM module
   * @param {number} handle - Native key handle
   * @param {string} [path='m'] - Derivation path
   */
  constructor(wasm, handle, path = 'm') {
    /** @private */
    this._wasm = wasm;
    /** @private */
    this._handle = handle;
    /** @private */
    this._path = path;
    /** @private */
    this._destroyed = false;

    // SECURITY FIX [VULN-14]: Register for GC-based cleanup as safety net
    if (_keyRegistry) {
      _keyRegistry.register(this, { wasm, handle }, this);
    }
  }

  /**
   * Derivation path
   * @type {string}
   */
  get path() {
    return this._path;
  }

  /**
   * Key depth in derivation tree
   * @type {number}
   */
  get depth() {
    this._checkDestroyed();
    return this._wasm._hd_key_get_depth(this._handle);
  }

  /**
   * Parent fingerprint
   * @type {number}
   */
  get parentFingerprint() {
    this._checkDestroyed();
    return this._wasm._hd_key_get_parent_fingerprint(this._handle);
  }

  /**
   * Child index
   * @type {number}
   */
  get childIndex() {
    this._checkDestroyed();
    return this._wasm._hd_key_get_child_index(this._handle);
  }

  /**
   * Is this a neutered (public-only) key?
   * @type {boolean}
   */
  get isNeutered() {
    this._checkDestroyed();
    return this._wasm._hd_key_is_neutered(this._handle) !== 0;
  }

  /**
   * Elliptic curve (currently always secp256k1)
   * @type {number}
   */
  get curve() {
    return Curve.SECP256K1;
  }

  /**
   * Check if key has been destroyed
   * @private
   */
  _checkDestroyed() {
    if (this._destroyed) {
      throw new HDWalletError(ErrorCode.INVALID_ARGUMENT, 'Key has been destroyed');
    }
  }

  /**
   * Get private key bytes
   * @returns {Uint8Array} 32-byte private key
   * @throws {HDWalletError} If key is neutered
   */
  privateKey() {
    this._checkDestroyed();
    const ptr = this._wasm._hd_alloc(32);
    try {
      const result = this._wasm._hd_key_get_private(this._handle, ptr, 32);
      checkResult(result);
      return readBytes(this._wasm, ptr, 32);
    } finally {
      // SECURITY FIX [VULN-04]: Wipe private key from WASM heap before freeing
      this._wasm._hd_secure_wipe(ptr, 32);
      this._wasm._hd_dealloc(ptr);
    }
  }

  /**
   * Get compressed public key bytes
   * @returns {Uint8Array} 33-byte compressed public key
   */
  publicKey() {
    this._checkDestroyed();
    const ptr = this._wasm._hd_alloc(33);
    try {
      const result = this._wasm._hd_key_get_public(this._handle, ptr, 33);
      checkResult(result);
      return readBytes(this._wasm, ptr, 33);
    } finally {
      this._wasm._hd_dealloc(ptr);
    }
  }

  /**
   * Get uncompressed public key bytes
   * @returns {Uint8Array} 65-byte uncompressed public key
   */
  publicKeyUncompressed() {
    this._checkDestroyed();
    const compressed = this.publicKey();
    const inPtr = allocAndCopy(this._wasm, compressed);
    const outPtr = this._wasm._hd_alloc(65);
    try {
      const result = this._wasm._hd_curve_decompress_pubkey(inPtr, Curve.SECP256K1, outPtr, 65);
      checkResult(result);
      return readBytes(this._wasm, outPtr, 65);
    } finally {
      this._wasm._hd_dealloc(inPtr);
      this._wasm._hd_dealloc(outPtr);
    }
  }

  /**
   * Get chain code bytes
   * @returns {Uint8Array} 32-byte chain code
   */
  chainCode() {
    this._checkDestroyed();
    const ptr = this._wasm._hd_alloc(32);
    try {
      const result = this._wasm._hd_key_get_chain_code(this._handle, ptr, 32);
      checkResult(result);
      return readBytes(this._wasm, ptr, 32);
    } finally {
      this._wasm._hd_dealloc(ptr);
    }
  }

  /**
   * Get key fingerprint
   * @returns {number} Fingerprint (first 4 bytes of HASH160 of public key)
   */
  fingerprint() {
    this._checkDestroyed();
    return this._wasm._hd_key_get_fingerprint(this._handle);
  }

  /**
   * Derive child key at index
   * @param {number} index - Child index
   * @returns {HDKey} Derived child key
   */
  deriveChild(index) {
    this._checkDestroyed();
    const childHandle = this._wasm._hd_key_derive_child(this._handle, index);
    if (!childHandle) {
      throw new HDWalletError(ErrorCode.KEY_DERIVATION_FAILED);
    }
    const childPath = `${this._path}/${index}`;
    return new HDKey(this._wasm, childHandle, childPath);
  }

  /**
   * Derive hardened child key
   * @param {number} index - Child index (will be hardened)
   * @returns {HDKey} Derived child key
   */
  deriveHardened(index) {
    this._checkDestroyed();
    const childHandle = this._wasm._hd_key_derive_hardened(this._handle, index);
    if (!childHandle) {
      throw new HDWalletError(ErrorCode.KEY_DERIVATION_FAILED);
    }
    const childPath = `${this._path}/${index}'`;
    return new HDKey(this._wasm, childHandle, childPath);
  }

  /**
   * Derive key at path
   * @param {string} path - Derivation path (e.g., "m/44'/60'/0'/0/0")
   * @returns {HDKey} Derived key
   */
  derivePath(path) {
    this._checkDestroyed();
    const pathPtr = allocString(this._wasm, path);
    try {
      const childHandle = this._wasm._hd_key_derive_path(this._handle, pathPtr);
      if (!childHandle) {
        throw new HDWalletError(ErrorCode.INVALID_PATH);
      }
      // Compute resulting path
      let resultPath = path;
      if (path.startsWith('m/') || path === 'm') {
        resultPath = path;
      } else if (path.startsWith('/')) {
        resultPath = this._path + path;
      } else {
        resultPath = this._path + '/' + path;
      }
      return new HDKey(this._wasm, childHandle, resultPath);
    } finally {
      this._wasm._hd_dealloc(pathPtr);
    }
  }

  /**
   * Get neutered (public-only) version
   * @returns {HDKey} Neutered key
   */
  neutered() {
    this._checkDestroyed();
    const neuteredHandle = this._wasm._hd_key_neutered(this._handle);
    if (!neuteredHandle) {
      throw new HDWalletError(ErrorCode.INTERNAL);
    }
    return new HDKey(this._wasm, neuteredHandle, this._path);
  }

  /**
   * Serialize as extended private key (xprv)
   * @returns {string} Base58Check-encoded xprv
   * @throws {HDWalletError} If key is neutered
   */
  toXprv() {
    this._checkDestroyed();
    const ptr = this._wasm._hd_alloc(128);
    try {
      const result = this._wasm._hd_key_serialize_xprv(this._handle, ptr, 128);
      checkResult(result);
      return readString(this._wasm, ptr);
    } finally {
      // SECURITY FIX [VULN-05]: Wipe xprv (contains private key) from WASM heap
      this._wasm._hd_secure_wipe(ptr, 128);
      this._wasm._hd_dealloc(ptr);
    }
  }

  /**
   * Serialize as extended public key (xpub)
   * @returns {string} Base58Check-encoded xpub
   */
  toXpub() {
    this._checkDestroyed();
    const ptr = this._wasm._hd_alloc(128);
    try {
      const result = this._wasm._hd_key_serialize_xpub(this._handle, ptr, 128);
      checkResult(result);
      return readString(this._wasm, ptr);
    } finally {
      this._wasm._hd_dealloc(ptr);
    }
  }

  /**
   * Compute libp2p peerID bytes for this key
   * @returns {Uint8Array} PeerID as raw multihash bytes
   */
  peerId() {
    this._checkDestroyed();
    const pubKey = this.publicKey();
    const pubPtr = allocAndCopy(this._wasm, pubKey);
    const outPtr = this._wasm._hd_alloc(64);
    try {
      const result = this._wasm._hd_libp2p_peer_id(pubPtr, pubKey.length, this.curve, outPtr, 64);
      if (result < 0) throw new HDWalletError(result);
      return readBytes(this._wasm, outPtr, result);
    } finally {
      this._wasm._hd_dealloc(pubPtr);
      this._wasm._hd_dealloc(outPtr);
    }
  }

  /**
   * Get peerID as base58btc string
   * @returns {string}
   */
  peerIdString() {
    this._checkDestroyed();
    const pubKey = this.publicKey();
    const pubPtr = allocAndCopy(this._wasm, pubKey);
    const outPtr = this._wasm._hd_alloc(128);
    try {
      const result = this._wasm._hd_libp2p_peer_id_string(pubPtr, pubKey.length, this.curve, outPtr, 128);
      checkResult(result);
      return readString(this._wasm, outPtr);
    } finally {
      this._wasm._hd_dealloc(pubPtr);
      this._wasm._hd_dealloc(outPtr);
    }
  }

  /**
   * Get IPNS hash (CIDv1 base36lower with 'k' prefix)
   * @returns {string}
   */
  ipnsHash() {
    this._checkDestroyed();
    const pubKey = this.publicKey();
    const pubPtr = allocAndCopy(this._wasm, pubKey);
    const outPtr = this._wasm._hd_alloc(128);
    try {
      const result = this._wasm._hd_libp2p_ipns_hash(pubPtr, pubKey.length, this.curve, outPtr, 128);
      checkResult(result);
      return readString(this._wasm, outPtr);
    } finally {
      this._wasm._hd_dealloc(pubPtr);
      this._wasm._hd_dealloc(outPtr);
    }
  }

  /**
   * Securely wipe key from memory
   */
  wipe() {
    if (!this._destroyed && this._handle) {
      this._wasm._hd_key_wipe(this._handle);
      this._wasm._hd_key_destroy(this._handle);
      this._handle = null;
      this._destroyed = true;
      // Unregister from FinalizationRegistry since we've cleaned up explicitly
      if (_keyRegistry) {
        _keyRegistry.unregister(this);
      }
    }
  }

  /**
   * Clone key
   * @returns {HDKey} Independent copy
   */
  clone() {
    this._checkDestroyed();
    const clonedHandle = this._wasm._hd_key_clone(this._handle);
    if (!clonedHandle) {
      throw new HDWalletError(ErrorCode.OUT_OF_MEMORY);
    }
    return new HDKey(this._wasm, clonedHandle, this._path);
  }
}

// =============================================================================
// SECURITY FIX [VULN-14]: FinalizationRegistry to auto-wipe leaked HDKey objects
// =============================================================================

/**
 * Registry that wipes native key handles when HDKey JS objects are garbage collected
 * without the user calling .wipe(). This is a safety net, not a replacement for
 * explicit cleanup — users should still call .wipe() when done.
 */
let _keyRegistry = null;
try {
  if (typeof FinalizationRegistry !== 'undefined') {
    _keyRegistry = new FinalizationRegistry(({ wasm, handle }) => {
      if (handle) {
        try {
          wasm._hd_key_wipe(handle);
          wasm._hd_key_destroy(handle);
        } catch (e) {
          // Ignore errors during GC cleanup
        }
      }
    });
  }
} catch (e) {
  // FinalizationRegistry not available in this environment
}

// =============================================================================
// Module Initialization
// =============================================================================

/**
 * SECURITY FIX [HIGH-05]: WASM Module Integrity Verification
 *
 * Verify the integrity of a WASM module using the package WASM SHA-256 helper.
 *
 * @param {ArrayBuffer|Uint8Array} wasmBytes - The WASM binary
 * @param {string} expectedHash - Expected SHA-256 hash in hex format
 * @returns {Promise<boolean>} True if hash matches
 * @throws {Error} If hash doesn't match
 */
export async function verifyWasmIntegrity(wasmBytes, expectedHash) {
  if (!expectedHash) {
    console.warn('[HD Wallet] No expected hash provided, skipping integrity check');
    return true;
  }

  const bytes = wasmBytes instanceof Uint8Array ? wasmBytes : new Uint8Array(wasmBytes);

  const hashHex = await computeWasmHash(bytes);

  if (hashHex.toLowerCase() !== expectedHash.toLowerCase()) {
    throw new Error(
      `WASM module integrity check failed!\n` +
      `Expected: ${expectedHash}\n` +
      `Got: ${hashHex}\n` +
      `The WASM module may have been tampered with.`
    );
  }

  return true;
}

function bytesToHex(bytes) {
  return Array.from(bytes, b => b.toString(16).padStart(2, '0')).join('');
}

/**
 * Compute SHA-256 hash of bytes with the package WASM implementation.
 *
 * @param {ArrayBuffer|Uint8Array} wasmBytes - The WASM binary
 * @returns {Promise<string>} SHA-256 hash in hex format
 */
export async function computeWasmHash(wasmBytes) {
  const bytes = wasmBytes instanceof Uint8Array ? wasmBytes : new Uint8Array(wasmBytes);
  const wallet = await init();
  return bytesToHex(wallet.utils.sha256(bytes));
}

/**
 * WASM module loader (single-file, isomorphic)
 * @returns {Promise<Object>} Initialized WASM module
 */
async function loadWasmModule() {
  let HDWalletWasm;

  try {
    const module = await import('../hd-wallet.js');
    HDWalletWasm = module.default;
  } catch (e) {
    if (typeof globalThis !== 'undefined' && globalThis.HDWalletWasm) {
      HDWalletWasm = globalThis.HDWalletWasm;
    } else {
      throw new Error(
        'Failed to load HD Wallet WASM module. ' +
        'Make sure hd-wallet.js is accessible or set it on globalThis.HDWalletWasm'
      );
    }
  }

  return HDWalletWasm();
}

/**
 * Create the HD Wallet module instance
 * @param {Object} wasm - Initialized WASM module
 * @returns {Object} HDWalletModule API
 */
function createModule(wasm) {
  // ==========================================================================
  // Mnemonic API
  // ==========================================================================

  /**
   * BIP-39 Mnemonic API
   * @type {Object}
   */
  const mnemonic = {
    /**
     * Generate a random mnemonic phrase
     * @param {number} [wordCount=24] - Number of words (12, 15, 18, 21, or 24)
     * @param {number} [language=Language.ENGLISH] - Wordlist language
     * @returns {string} Mnemonic phrase
     * @throws {HDWalletError} If entropy not available
     */
    generate(wordCount = 24, language = Language.ENGLISH) {
      const ptr = wasm._hd_alloc(1024);
      try {
        const result = wasm._hd_mnemonic_generate(ptr, 1024, wordCount, language);
        checkResult(result);
        return readString(wasm, ptr);
      } finally {
        wasm._hd_dealloc(ptr);
      }
    },

    /**
     * Validate a mnemonic phrase
     * @param {string} mnemonicStr - Mnemonic phrase to validate
     * @param {number} [language=Language.ENGLISH] - Wordlist language
     * @returns {boolean} True if valid
     */
    validate(mnemonicStr, language = Language.ENGLISH) {
      const ptr = allocString(wasm, mnemonicStr);
      try {
        const result = wasm._hd_mnemonic_validate(ptr, language);
        return result === 0;
      } finally {
        wasm._hd_dealloc(ptr);
      }
    },

    /**
     * Convert mnemonic to 64-byte seed
     * @param {string} mnemonicStr - Mnemonic phrase
     * @param {string} [passphrase=''] - Optional passphrase
     * @returns {Uint8Array} 64-byte seed
     */
    toSeed(mnemonicStr, passphrase = '') {
      const mnemonicPtr = allocString(wasm, mnemonicStr);
      const passphrasePtr = allocString(wasm, passphrase);
      const seedPtr = wasm._hd_alloc(64);
      try {
        const result = wasm._hd_mnemonic_to_seed(mnemonicPtr, passphrasePtr, seedPtr, 64);
        checkResult(result);
        return readBytes(wasm, seedPtr, 64);
      } finally {
        wasm._hd_secure_wipe(seedPtr, 64);
        // SECURITY FIX [VULN-07]: Wipe mnemonic and passphrase from WASM heap
        wasm._hd_secure_wipe(mnemonicPtr, wasm.lengthBytesUTF8(mnemonicStr) + 1);
        wasm._hd_secure_wipe(passphrasePtr, wasm.lengthBytesUTF8(passphrase) + 1);
        wasm._hd_dealloc(mnemonicPtr);
        wasm._hd_dealloc(passphrasePtr);
        wasm._hd_dealloc(seedPtr);
      }
    },

    /**
     * Convert mnemonic to entropy bytes
     * @param {string} mnemonicStr - Mnemonic phrase
     * @param {number} [language=Language.ENGLISH] - Wordlist language
     * @returns {Uint8Array} Entropy bytes
     */
    toEntropy(mnemonicStr, language = Language.ENGLISH) {
      const mnemonicPtr = allocString(wasm, mnemonicStr);
      const mnemonicLen = wasm.lengthBytesUTF8(mnemonicStr) + 1;
      const entropyPtr = wasm._hd_alloc(33);
      const sizePtr = wasm._hd_alloc(4);
      try {
        wasm.setValue(sizePtr, 33, 'i32');
        const result = wasm._hd_mnemonic_to_entropy(mnemonicPtr, language, entropyPtr, sizePtr);
        checkResult(result);
        const size = wasm.getValue(sizePtr, 'i32');
        return readBytes(wasm, entropyPtr, size);
      } finally {
        // SECURITY FIX [HIGH-04]: Wipe mnemonic from memory before deallocation
        wasm._hd_secure_wipe(mnemonicPtr, mnemonicLen);
        wasm._hd_dealloc(mnemonicPtr);
        // Also wipe entropy since it's sensitive
        wasm._hd_secure_wipe(entropyPtr, 33);
        wasm._hd_dealloc(entropyPtr);
        wasm._hd_dealloc(sizePtr);
      }
    },

    /**
     * Convert entropy to mnemonic
     * @param {Uint8Array} entropy - Entropy bytes (16, 20, 24, 28, or 32 bytes)
     * @param {number} [language=Language.ENGLISH] - Wordlist language
     * @returns {string} Mnemonic phrase
     */
    fromEntropy(entropy, language = Language.ENGLISH) {
      const entropyPtr = allocAndCopy(wasm, entropy);
      const outputPtr = wasm._hd_alloc(1024);
      try {
        const result = wasm._hd_entropy_to_mnemonic(entropyPtr, entropy.length, language, outputPtr, 1024);
        checkResult(result);
        return readString(wasm, outputPtr);
      } finally {
        // SECURITY FIX [HIGH-04]: Wipe sensitive data before deallocation
        wasm._hd_secure_wipe(entropyPtr, entropy.length);
        wasm._hd_dealloc(entropyPtr);
        // Wipe mnemonic output buffer
        wasm._hd_secure_wipe(outputPtr, 1024);
        wasm._hd_dealloc(outputPtr);
      }
    },

    /**
     * Get wordlist for language
     * @param {number} [language=Language.ENGLISH] - Wordlist language
     * @returns {string[]} Array of 2048 words
     */
    getWordlist(language = Language.ENGLISH) {
      const ptr = wasm._hd_mnemonic_get_wordlist(language);
      if (!ptr) {
        throw new HDWalletError(ErrorCode.NOT_SUPPORTED, 'Mnemonic wordlist not available for selected language');
      }
      const json = readString(wasm, ptr);
      return JSON.parse(json);
    },

    /**
     * Get word suggestions for autocomplete
     * @param {string} prefix - Word prefix
     * @param {number} [language=Language.ENGLISH] - Wordlist language
     * @param {number} [maxSuggestions=5] - Maximum suggestions
     * @returns {string[]} Suggested words
     */
    suggestWords(prefix, language = Language.ENGLISH, maxSuggestions = 5) {
      const prefixPtr = allocString(wasm, prefix);
      const outputPtr = wasm._hd_alloc(1024);
      try {
        const result = wasm._hd_mnemonic_suggest_word(prefixPtr, language, outputPtr, 1024, maxSuggestions);
        if (result < 0) throw new HDWalletError(result);
        const text = readString(wasm, outputPtr);
        // C API returns newline-separated words
        return text ? text.split('\n').filter(w => w.length > 0) : [];
      } finally {
        wasm._hd_dealloc(prefixPtr);
        wasm._hd_dealloc(outputPtr);
      }
    },

    /**
     * Check if word is in wordlist
     * @param {string} word - Word to check
     * @param {number} [language=Language.ENGLISH] - Wordlist language
     * @returns {boolean} True if word is in wordlist
     */
    checkWord(word, language = Language.ENGLISH) {
      const wordPtr = allocString(wasm, word);
      try {
        const result = wasm._hd_mnemonic_check_word(wordPtr, language);
        return result >= 0;
      } finally {
        wasm._hd_dealloc(wordPtr);
      }
    }
  };

  // ==========================================================================
  // HDKey API
  // ==========================================================================

  /**
   * BIP-32 HD Key API
   * @type {Object}
   */
  const hdkey = {
    /**
     * Create master key from seed
     * @param {Uint8Array} seed - 16-64 byte seed (BIP-32 allows 128-512 bits)
     * @param {number} [curve=Curve.SECP256K1] - Elliptic curve
     * @returns {HDKey} Master HD key
     */
    fromSeed(seed, curve = Curve.SECP256K1) {
      // BIP-32 allows 128-512 bits (16-64 bytes)
      if (seed.length < 16 || seed.length > 64) {
        throw new HDWalletError(ErrorCode.INVALID_SEED, 'Seed must be 16-64 bytes');
      }
      const seedPtr = allocAndCopy(wasm, seed);
      try {
        const handle = wasm._hd_key_from_seed(seedPtr, seed.length, curve);
        if (!handle) {
          throw new HDWalletError(ErrorCode.INVALID_SEED);
        }
        return new HDKey(wasm, handle, 'm');
      } finally {
        wasm._hd_secure_wipe(seedPtr, seed.length);
        wasm._hd_dealloc(seedPtr);
      }
    },

    /**
     * Parse extended private key
     * @param {string} xprv - Base58Check-encoded xprv
     * @returns {HDKey} HD key
     */
    fromXprv(xprv) {
      const xprvPtr = allocString(wasm, xprv);
      try {
        const handle = wasm._hd_key_from_xprv(xprvPtr);
        if (!handle) {
          throw new HDWalletError(ErrorCode.INVALID_EXTENDED_KEY);
        }
        return new HDKey(wasm, handle);
      } finally {
        wasm._hd_dealloc(xprvPtr);
      }
    },

    /**
     * Parse extended public key
     * @param {string} xpub - Base58Check-encoded xpub
     * @returns {HDKey} HD key (neutered)
     */
    fromXpub(xpub) {
      const xpubPtr = allocString(wasm, xpub);
      try {
        const handle = wasm._hd_key_from_xpub(xpubPtr);
        if (!handle) {
          throw new HDWalletError(ErrorCode.INVALID_EXTENDED_KEY);
        }
        return new HDKey(wasm, handle);
      } finally {
        wasm._hd_dealloc(xpubPtr);
      }
    },

    /**
     * Build BIP-44 path
     * @param {number} purpose - Purpose (44, 49, 84)
     * @param {number} coinType - SLIP-44 coin type
     * @param {number} [account=0] - Account index
     * @param {number} [change=0] - Change (0=external, 1=internal)
     * @param {number} [index=0] - Address index
     * @returns {string} Derivation path
     */
    buildPath(purpose, coinType, account = 0, change = 0, index = 0) {
      const ptr = wasm._hd_alloc(128);
      try {
        const result = wasm._hd_path_build(ptr, 128, purpose, coinType, account, change, index);
        checkResult(result);
        return readString(wasm, ptr);
      } finally {
        wasm._hd_dealloc(ptr);
      }
    },

    /**
     * Parse BIP-44 path
     * @param {string} path - Derivation path
     * @returns {Object} Parsed path components
     */
    parsePath(path) {
      const pathPtr = allocString(wasm, path);
      const purposePtr = wasm._hd_alloc(4);
      const coinTypePtr = wasm._hd_alloc(4);
      const accountPtr = wasm._hd_alloc(4);
      const changePtr = wasm._hd_alloc(4);
      const indexPtr = wasm._hd_alloc(4);
      try {
        const result = wasm._hd_path_parse(pathPtr, purposePtr, coinTypePtr, accountPtr, changePtr, indexPtr);
        checkResult(result);
        return {
          purpose: wasm.getValue(purposePtr, 'i32'),
          coinType: wasm.getValue(coinTypePtr, 'i32'),
          account: wasm.getValue(accountPtr, 'i32'),
          change: wasm.getValue(changePtr, 'i32'),
          index: wasm.getValue(indexPtr, 'i32')
        };
      } finally {
        wasm._hd_dealloc(pathPtr);
        wasm._hd_dealloc(purposePtr);
        wasm._hd_dealloc(coinTypePtr);
        wasm._hd_dealloc(accountPtr);
        wasm._hd_dealloc(changePtr);
        wasm._hd_dealloc(indexPtr);
      }
    }
  };

  // ==========================================================================
  // Curves API
  // ==========================================================================

  // Some builds expose different p256/p384 verify ABIs. Probe once and cache.
  let p256VerifyAbi = null; // 'five' | 'six'
  let p384VerifyAbi = null; // 'five' | 'six'

  /**
   * Multi-curve cryptography API
   * @type {Object}
   */
  const curves = {
    /**
     * Derive public key from private key
     * @param {Uint8Array} privateKey - Private key bytes
     * @param {number} curve - Curve type
     * @returns {Uint8Array} Compressed public key
     */
      publicKeyFromPrivate(privateKey, curve) {
        const pubFromPrivFn = requireWasmFunction(wasm, '_hd_curve_pubkey_from_privkey');
        const privPtr = allocAndCopy(wasm, privateKey);
        const pubPtr = wasm._hd_alloc(65);
        try {
          const result = pubFromPrivFn(privPtr, curve, pubPtr, 65);
          if (result < 0) throw new HDWalletError(result);
          const expectedLen = curve === Curve.P384 ? 49 : 33;
          if (result === 0 || result === expectedLen) {
            return readBytes(wasm, pubPtr, expectedLen);
          }
          throw new HDWalletError(result);
        } finally {
          wasm._hd_secure_wipe(privPtr, privateKey.length);
          wasm._hd_dealloc(privPtr);
          wasm._hd_dealloc(pubPtr);
        }
    },

    /**
     * Compress public key
     * @param {Uint8Array} publicKey - Uncompressed public key (65 bytes)
     * @param {number} curve - Curve type
     * @returns {Uint8Array} Compressed public key (33 bytes)
     */
      compressPublicKey(publicKey, curve) {
        const compressFn = getWasmFunction(wasm, '_hd_curve_compress_pubkey');
        if (!compressFn) {
        // Lightweight JS fallback for uncompressed EC points: 0x04 || X || Y.
        if (publicKey.length !== 65 && publicKey.length !== 97) {
          throw new HDWalletError(ErrorCode.INVALID_ARGUMENT, 'Invalid uncompressed public key length');
        }
        if (publicKey[0] !== 0x04) {
          throw new HDWalletError(ErrorCode.INVALID_PUBLIC_KEY);
        }
        const coordLen = (publicKey.length - 1) / 2;
        const out = new Uint8Array(coordLen + 1);
        out[0] = (publicKey[publicKey.length - 1] & 1) ? 0x03 : 0x02;
        out.set(publicKey.slice(1, coordLen + 1), 1);
        return out;
      }

        const inPtr = allocAndCopy(wasm, publicKey);
        const outPtr = wasm._hd_alloc(65);
        try {
          const result = compressFn(inPtr, curve, outPtr, 65);
          if (result < 0) throw new HDWalletError(result);
          const expectedLen = curve === Curve.P384 ? 49 : 33;
          if (result === 0 || result === expectedLen) {
            return readBytes(wasm, outPtr, expectedLen);
          }
          throw new HDWalletError(result);
        } finally {
          wasm._hd_dealloc(inPtr);
          wasm._hd_dealloc(outPtr);
        }
      },

    /**
     * Decompress public key
     * @param {Uint8Array} publicKey - Compressed public key (33 bytes)
     * @param {number} curve - Curve type
     * @returns {Uint8Array} Uncompressed public key (65 bytes)
     */
      decompressPublicKey(publicKey, curve) {
        const decompressFn = requireWasmFunction(wasm, '_hd_curve_decompress_pubkey');
        const inPtr = allocAndCopy(wasm, publicKey);
        const outPtr = wasm._hd_alloc(97);
        try {
          const result = decompressFn(inPtr, curve, outPtr, 97);
          if (result < 0) throw new HDWalletError(result);
          const expectedLen = curve === Curve.P384 ? 97 : 65;
          if (result === 0 || result === expectedLen) {
            return readBytes(wasm, outPtr, expectedLen);
          }
          throw new HDWalletError(result);
        } finally {
          wasm._hd_dealloc(inPtr);
          wasm._hd_dealloc(outPtr);
        }
      },

    /**
     * Generic ECDH shared secret computation
     *
     * Performs ECDH key exchange on any supported curve by specifying
     * the curve parameter. For convenience, use the curve-specific
     * methods (e.g. curves.secp256k1.ecdh()) instead.
     *
     * @param {number} curve - Curve enum value (Curve.SECP256K1, Curve.P256, etc.)
     * @param {Uint8Array} privateKey - Private key bytes
     * @param {Uint8Array} publicKey - Other party's public key bytes
     * @returns {Uint8Array} Shared secret bytes
     */
    ecdh(curve, privateKey, publicKey) {
      const ecdhFn = requireWasmFunction(wasm, '_hd_ecdh');
      const privPtr = allocAndCopy(wasm, privateKey);
      const pubPtr = allocAndCopy(wasm, publicKey);
      const secretSize = curve === Curve.P384 ? 48 : 32;
      const secretPtr = wasm._hd_alloc(secretSize);
      try {
        const result = ecdhFn(curve, privPtr, privateKey.length, pubPtr, publicKey.length, secretPtr, secretSize);
        if (result < 0) throw new HDWalletError(ErrorCode.INTERNAL, 'ECDH key exchange failed');
        // result is the actual secret length on success
        const actualLen = result > 0 ? result : secretSize;
        return readBytes(wasm, secretPtr, actualLen);
      } finally {
        wasm._hd_secure_wipe(privPtr, privateKey.length);
        wasm._hd_secure_wipe(secretPtr, secretSize);
        wasm._hd_dealloc(privPtr);
        wasm._hd_dealloc(pubPtr);
        wasm._hd_dealloc(secretPtr);
      }
    },

    /**
     * secp256k1 ECDSA operations
     */
    secp256k1: {
      /**
       * Sign message with secp256k1
       * @param {Uint8Array} message - Message to sign (typically 32-byte hash)
       * @param {Uint8Array} privateKey - 32-byte private key
       * @returns {Uint8Array} Signature
       */
      sign(message, privateKey) {
        const msgPtr = allocAndCopy(wasm, message);
        const keyPtr = allocAndCopy(wasm, privateKey);
        const sigPtr = wasm._hd_alloc(72);
        try {
          const len = wasm._hd_secp256k1_sign(msgPtr, message.length, keyPtr, sigPtr, 72);
          if (len < 0) throw new HDWalletError(len);
          return readBytes(wasm, sigPtr, len);
        } finally {
          wasm._hd_secure_wipe(keyPtr, 32);
          wasm._hd_dealloc(msgPtr);
          wasm._hd_dealloc(keyPtr);
          wasm._hd_dealloc(sigPtr);
        }
      },

      /**
       * Sign message with recovery ID
       * @param {Uint8Array} message - Message to sign
       * @param {Uint8Array} privateKey - Private key
       * @returns {Object} { signature: Uint8Array, recoveryId: number }
       */
      signRecoverable(message, privateKey) {
        const signRecoverableFn = requireWasmFunction(wasm, '_hd_secp256k1_sign_recoverable');
        const msgPtr = allocAndCopy(wasm, message);
        const keyPtr = allocAndCopy(wasm, privateKey);
        const sigPtr = wasm._hd_alloc(65);
        try {
          // C signature: int32_t hd_secp256k1_sign_recoverable(hash, hash_len, privkey, out_sig65)
          // Returns 65 on success (bytes written), negative on error.
          // out_sig65 = r[32] + s[32] + v[1], recovery ID is at byte[64].
          const result = signRecoverableFn(msgPtr, message.length, keyPtr, sigPtr);
          if (result < 0) throw new HDWalletError(result);
          const signature = readBytes(wasm, sigPtr, 64);
          const recoveryId = wasm.HEAPU8[sigPtr + 64];
          return { signature, recoveryId };
        } finally {
          wasm._hd_secure_wipe(keyPtr, 32);
          wasm._hd_dealloc(msgPtr);
          wasm._hd_dealloc(keyPtr);
          wasm._hd_dealloc(sigPtr);
        }
      },

      /**
       * Verify secp256k1 signature
       * @param {Uint8Array} message - Original message
       * @param {Uint8Array} signature - Signature to verify
       * @param {Uint8Array} publicKey - Public key
       * @returns {boolean} True if valid
       */
      verify(message, signature, publicKey) {
        const msgPtr = allocAndCopy(wasm, message);
        const sigPtr = allocAndCopy(wasm, signature);
        const pubPtr = allocAndCopy(wasm, publicKey);
        try {
          const result = wasm._hd_secp256k1_verify(msgPtr, message.length, sigPtr, signature.length, pubPtr, publicKey.length);
          return result === 1;
        } finally {
          wasm._hd_dealloc(msgPtr);
          wasm._hd_dealloc(sigPtr);
          wasm._hd_dealloc(pubPtr);
        }
      },

      /**
       * Recover public key from signature
       * @param {Uint8Array} message - Original message
       * @param {Uint8Array} signature - Signature
       * @param {number} recoveryId - Recovery ID (0-3)
       * @returns {Uint8Array} Recovered public key
       */
      recover(message, signature, recoveryId) {
        const recoverFn = requireWasmFunction(wasm, '_hd_secp256k1_recover');
        const msgPtr = allocAndCopy(wasm, message);
        // Build 65-byte sig buffer: r[32] + s[32] + v[1]
        // C signature: int32_t hd_secp256k1_recover(hash, hash_len, sig65, out_pubkey33)
        const sig65 = new Uint8Array(65);
        sig65.set(signature.subarray(0, 64));
        sig65[64] = recoveryId;
        const sigPtr = allocAndCopy(wasm, sig65);
        const pubPtr = wasm._hd_alloc(33);
        try {
          const result = recoverFn(msgPtr, message.length, sigPtr, pubPtr);
          if (result < 0) throw new HDWalletError(result);
          return readBytes(wasm, pubPtr, 33);
        } finally {
          wasm._hd_dealloc(msgPtr);
          wasm._hd_dealloc(sigPtr);
          wasm._hd_dealloc(pubPtr);
        }
      },

      /**
       * ECDH shared secret
       * @param {Uint8Array} privateKey - Own private key
       * @param {Uint8Array} publicKey - Other party's public key
       * @returns {Uint8Array} Shared secret
       */
      ecdh(privateKey, publicKey) {
        const privPtr = allocAndCopy(wasm, privateKey);
        const pubPtr = allocAndCopy(wasm, publicKey);
        const secretPtr = wasm._hd_alloc(32);
        try {
          const result = wasm._hd_ecdh_secp256k1(privPtr, pubPtr, publicKey.length, secretPtr, 32);
          if (result < 0) throw new HDWalletError(result);
          return readBytes(wasm, secretPtr, 32);
        } finally {
          wasm._hd_secure_wipe(privPtr, 32);
          wasm._hd_secure_wipe(secretPtr, 32);
          wasm._hd_dealloc(privPtr);
          wasm._hd_dealloc(pubPtr);
          wasm._hd_dealloc(secretPtr);
        }
      }
    },

    /**
     * Ed25519 EdDSA operations
     */
    ed25519: {
      /**
       * Derive Ed25519 public key from 32-byte seed
       * @param {Uint8Array} seed - 32-byte seed (will be expanded internally)
       * @returns {Uint8Array} 32-byte public key
       */
      publicKeyFromSeed(seed) {
        const seedPtr = allocAndCopy(wasm, seed);
        const pubPtr = wasm._hd_alloc(32);
        try {
          const result = wasm._hd_ed25519_pubkey_from_seed(seedPtr, pubPtr, 32);
          if (result < 0) throw new HDWalletError(result);
          return readBytes(wasm, pubPtr, 32);
        } finally {
          wasm._hd_secure_wipe(seedPtr, 32);
          wasm._hd_dealloc(seedPtr);
          wasm._hd_dealloc(pubPtr);
        }
      },

      /**
       * Sign message with Ed25519
       * @param {Uint8Array} message - Message to sign
       * @param {Uint8Array} privateKey - 32-byte private key
       * @returns {Uint8Array} 64-byte signature
       */
      sign(message, privateKey) {
        const msgPtr = allocAndCopy(wasm, message);
        const keyPtr = allocAndCopy(wasm, privateKey);
        const sigPtr = wasm._hd_alloc(64);
        try {
          const len = wasm._hd_ed25519_sign(msgPtr, message.length, keyPtr, sigPtr, 64);
          if (len < 0) throw new HDWalletError(len);
          return readBytes(wasm, sigPtr, 64);
        } finally {
          wasm._hd_secure_wipe(keyPtr, 32);
          wasm._hd_dealloc(msgPtr);
          wasm._hd_dealloc(keyPtr);
          wasm._hd_dealloc(sigPtr);
        }
      },

      /**
       * Verify Ed25519 signature
       * @param {Uint8Array} message - Original message
       * @param {Uint8Array} signature - 64-byte signature
       * @param {Uint8Array} publicKey - 32-byte public key
       * @returns {boolean} True if valid
       */
      verify(message, signature, publicKey) {
        const msgPtr = allocAndCopy(wasm, message);
        const sigPtr = allocAndCopy(wasm, signature);
        const pubPtr = allocAndCopy(wasm, publicKey);
        try {
          const result = wasm._hd_ed25519_verify(msgPtr, message.length, sigPtr, signature.length, pubPtr, publicKey.length);
          return result === 1;
        } finally {
          wasm._hd_dealloc(msgPtr);
          wasm._hd_dealloc(sigPtr);
          wasm._hd_dealloc(pubPtr);
        }
      }
    },

    /**
     * P-256 (secp256r1) ECDSA operations
     */
    p256: {
      sign(message, privateKey) {
        const msgPtr = allocAndCopy(wasm, message);
        const keyPtr = allocAndCopy(wasm, privateKey);
        const sigPtr = wasm._hd_alloc(72);
        try {
          const len = wasm._hd_p256_sign(msgPtr, message.length, keyPtr, sigPtr, 72);
          if (len < 0) throw new HDWalletError(len);
          return readBytes(wasm, sigPtr, len);
        } finally {
          wasm._hd_secure_wipe(keyPtr, 32);
          wasm._hd_dealloc(msgPtr);
          wasm._hd_dealloc(keyPtr);
          wasm._hd_dealloc(sigPtr);
        }
      },

      verify(message, signature, publicKey) {
        const verifyFn = requireWasmFunction(wasm, '_hd_p256_verify');
        const msgPtr = allocAndCopy(wasm, message);
        const sigPtr = allocAndCopy(wasm, signature);
        const pubPtr = allocAndCopy(wasm, publicKey);
        try {
          // C: hd_p256_verify(message, message_len, public_key, public_key_len, signature)
          return verifyFn(msgPtr, message.length, pubPtr, publicKey.length, sigPtr) === 0;
        } finally {
          wasm._hd_dealloc(msgPtr);
          wasm._hd_dealloc(sigPtr);
          wasm._hd_dealloc(pubPtr);
        }
      },

      ecdh(privateKey, publicKey) {
        const privPtr = allocAndCopy(wasm, privateKey);
        const pubPtr = allocAndCopy(wasm, publicKey);
        const secretPtr = wasm._hd_alloc(32);
        try {
          const result = wasm._hd_ecdh_p256(privPtr, pubPtr, publicKey.length, secretPtr, 32);
          if (result < 0) throw new HDWalletError(result);
          return readBytes(wasm, secretPtr, 32);
        } finally {
          wasm._hd_secure_wipe(privPtr, 32);
          wasm._hd_secure_wipe(secretPtr, 32);
          wasm._hd_dealloc(privPtr);
          wasm._hd_dealloc(pubPtr);
          wasm._hd_dealloc(secretPtr);
        }
      }
    },

    /**
     * P-384 (secp384r1) ECDSA operations
     */
    p384: {
      sign(message, privateKey) {
        const msgPtr = allocAndCopy(wasm, message);
        const keyPtr = allocAndCopy(wasm, privateKey);
        const sigPtr = wasm._hd_alloc(104);
        try {
          const len = wasm._hd_p384_sign(msgPtr, message.length, keyPtr, sigPtr, 104);
          if (len < 0) throw new HDWalletError(len);
          return readBytes(wasm, sigPtr, len);
        } finally {
          wasm._hd_secure_wipe(keyPtr, 48);
          wasm._hd_dealloc(msgPtr);
          wasm._hd_dealloc(keyPtr);
          wasm._hd_dealloc(sigPtr);
        }
      },

      verify(message, signature, publicKey) {
        const verifyFn = requireWasmFunction(wasm, '_hd_p384_verify');
        const msgPtr = allocAndCopy(wasm, message);
        const sigPtr = allocAndCopy(wasm, signature);
        const pubPtr = allocAndCopy(wasm, publicKey);
        try {
          // C: hd_p384_verify(message, message_len, public_key, public_key_len, signature)
          return verifyFn(msgPtr, message.length, pubPtr, publicKey.length, sigPtr) === 0;
        } finally {
          wasm._hd_dealloc(msgPtr);
          wasm._hd_dealloc(sigPtr);
          wasm._hd_dealloc(pubPtr);
        }
      },

      ecdh(privateKey, publicKey) {
        const privPtr = allocAndCopy(wasm, privateKey);
        const pubPtr = allocAndCopy(wasm, publicKey);
        const secretPtr = wasm._hd_alloc(48);
        try {
          const result = wasm._hd_ecdh_p384(privPtr, pubPtr, publicKey.length, secretPtr, 48);
          if (result < 0) throw new HDWalletError(result);
          return readBytes(wasm, secretPtr, 48);
        } finally {
          wasm._hd_secure_wipe(privPtr, 48);
          wasm._hd_secure_wipe(secretPtr, 48);
          wasm._hd_dealloc(privPtr);
          wasm._hd_dealloc(pubPtr);
          wasm._hd_dealloc(secretPtr);
        }
      }
    },

    /**
     * X25519 key exchange
     */
    x25519: {
      /**
       * Derive X25519 public key from private key
       * @param {Uint8Array} privateKey - 32-byte private key
       * @returns {Uint8Array} 32-byte public key
       */
      publicKey(privateKey) {
        const privPtr = allocAndCopy(wasm, privateKey);
        const pubPtr = wasm._hd_alloc(32);
        try {
          const result = wasm._hd_x25519_pubkey(privPtr, pubPtr, 32);
          if (result < 0) throw new HDWalletError(result);
          return readBytes(wasm, pubPtr, 32);
        } finally {
          wasm._hd_secure_wipe(privPtr, 32);
          wasm._hd_dealloc(privPtr);
          wasm._hd_dealloc(pubPtr);
        }
      },

      /**
       * Perform X25519 ECDH key exchange
       * @param {Uint8Array} privateKey - Our 32-byte private key
       * @param {Uint8Array} publicKey - Their 32-byte public key
       * @returns {Uint8Array} 32-byte shared secret
       */
      ecdh(privateKey, publicKey) {
        const privPtr = allocAndCopy(wasm, privateKey);
        const pubPtr = allocAndCopy(wasm, publicKey);
        const secretPtr = wasm._hd_alloc(32);
        try {
          const result = wasm._hd_ecdh_x25519(privPtr, pubPtr, secretPtr, 32);
          if (result < 0) throw new HDWalletError(result);
          return readBytes(wasm, secretPtr, 32);
        } finally {
          wasm._hd_secure_wipe(privPtr, 32);
          wasm._hd_secure_wipe(secretPtr, 32);
          wasm._hd_dealloc(privPtr);
          wasm._hd_dealloc(pubPtr);
          wasm._hd_dealloc(secretPtr);
        }
      }
    }
  };

  // ==========================================================================
  // Generic Coin API
  // ==========================================================================

  /**
   * Generic coin operations
   *
   * These functions dispatch to the appropriate coin-specific implementation
   * based on the coin type parameter. Use the CoinType enum for the coinType
   * argument (e.g. CoinType.BITCOIN, CoinType.ETHEREUM, CoinType.SOLANA).
   *
   * @type {Object}
   */
  const coin = {
    /**
     * Generate address from public key for any supported coin
     * @param {number} coinType - CoinType enum value
     * @param {Uint8Array} publicKey - Public key bytes
     * @param {number} [network=Network.MAINNET] - Network
     * @returns {string} Address string
     */
    addressFromPubkey(coinType, publicKey, network = Network.MAINNET) {
      const fn = requireWasmFunction(wasm, '_hd_coin_address_from_pubkey');
      const pubPtr = allocAndCopy(wasm, publicKey);
      const outPtr = wasm._hd_alloc(128);
      try {
        const result = fn(coinType, network, pubPtr, publicKey.length, outPtr, 128);
        checkResult(result);
        return readString(wasm, outPtr);
      } finally {
        wasm._hd_dealloc(pubPtr);
        wasm._hd_dealloc(outPtr);
      }
    },

    /**
     * Validate address for any supported coin
     * @param {number} coinType - CoinType enum value
     * @param {string} address - Address to validate
     * @param {number} [network=Network.MAINNET] - Network
     * @returns {boolean} True if valid
     */
    validateAddress(coinType, address, network = Network.MAINNET) {
      const fn = requireWasmFunction(wasm, '_hd_coin_validate_address');
      const addrPtr = allocString(wasm, address);
      try {
        return fn(coinType, network, addrPtr) === 0;
      } finally {
        wasm._hd_dealloc(addrPtr);
      }
    },

    /**
     * Sign message for any supported coin
     * @param {number} coinType - CoinType enum value
     * @param {Uint8Array} message - Message bytes
     * @param {Uint8Array} privateKey - Private key bytes
     * @returns {Uint8Array} Signature bytes
     */
    signMessage(coinType, message, privateKey) {
      const fn = requireWasmFunction(wasm, '_hd_coin_sign_message');
      const msgPtr = allocAndCopy(wasm, message);
      const keyPtr = allocAndCopy(wasm, privateKey);
      const sigLenPtr = wasm._hd_alloc(4);
      // Pre-allocate enough for the largest possible signature (65 bytes for ECDSA recoverable)
      const maxSigLen = 128;
      wasm.setValue(sigLenPtr, maxSigLen, 'i32');
      const sigPtr = wasm._hd_alloc(maxSigLen);
      try {
        const result = fn(coinType, msgPtr, message.length, keyPtr, sigPtr, sigLenPtr);
        checkResult(result);
        const actualLen = wasm.getValue(sigLenPtr, 'i32');
        return readBytes(wasm, sigPtr, actualLen);
      } finally {
        wasm._hd_secure_wipe(keyPtr, privateKey.length);
        wasm._hd_dealloc(msgPtr);
        wasm._hd_dealloc(keyPtr);
        wasm._hd_dealloc(sigPtr);
        wasm._hd_dealloc(sigLenPtr);
      }
    },

    /**
     * Verify message signature for any supported coin
     * @param {number} coinType - CoinType enum value
     * @param {Uint8Array} message - Original message bytes
     * @param {Uint8Array} signature - Signature bytes
     * @param {Uint8Array} publicKey - Public key bytes
     * @returns {boolean} True if signature is valid
     */
    verifyMessage(coinType, message, signature, publicKey) {
      const fn = requireWasmFunction(wasm, '_hd_coin_verify_message');
      const msgPtr = allocAndCopy(wasm, message);
      const sigPtr = allocAndCopy(wasm, signature);
      const pubPtr = allocAndCopy(wasm, publicKey);
      try {
        const result = fn(coinType, msgPtr, message.length, sigPtr, signature.length, pubPtr, publicKey.length);
        if (result < 0 || result > 1) throw new HDWalletError(result);
        return result === 1;
      } finally {
        wasm._hd_dealloc(msgPtr);
        wasm._hd_dealloc(sigPtr);
        wasm._hd_dealloc(pubPtr);
      }
    }
  };

  // ==========================================================================
  // Bitcoin API
  // ==========================================================================

  /**
   * Bitcoin API
   * @type {Object}
   */
  const bitcoin = {
    /**
     * Get Bitcoin address from public key
     * @param {Uint8Array} publicKey - Public key
     * @param {number} type - Address type (P2PKH, P2SH, P2WPKH, P2WSH, P2TR)
     * @param {number} [network=Network.MAINNET] - Network
     * @returns {string} Bitcoin address
     */
    getAddress(publicKey, type, network = Network.MAINNET) {
      const pubPtr = allocAndCopy(wasm, publicKey);
      const outPtr = wasm._hd_alloc(128);
      try {
        let fnName;
        switch (type) {
          case BitcoinAddressType.P2PKH:
            fnName = '_hd_btc_p2pkh_address';
            break;
          case BitcoinAddressType.P2SH:
            fnName = '_hd_btc_p2sh_address';
            break;
          case BitcoinAddressType.P2WPKH:
            fnName = '_hd_btc_p2wpkh_address';
            break;
          case BitcoinAddressType.P2WSH:
            fnName = '_hd_btc_p2wsh_address';
            break;
          case BitcoinAddressType.P2TR:
            fnName = '_hd_btc_p2tr_address';
            break;
          default:
            throw new HDWalletError(ErrorCode.INVALID_ARGUMENT, 'Invalid address type');
        }
        const addressFn = requireWasmFunction(wasm, fnName);
        const result = addressFn(pubPtr, publicKey.length, network, outPtr, 128);
        checkResult(result);
        return readString(wasm, outPtr);
      } finally {
        wasm._hd_dealloc(pubPtr);
        wasm._hd_dealloc(outPtr);
      }
    },

    /**
     * Validate Bitcoin address
     * @param {string} address - Address to validate
     * @returns {boolean} True if valid
     */
    validateAddress(address, network = Network.MAINNET) {
      const validateFn = requireWasmFunction(wasm, '_hd_btc_validate_address');
      const addrPtr = allocString(wasm, address);
      try {
        const result = validateFn(addrPtr, network);
        return result === 0;
      } finally {
        wasm._hd_dealloc(addrPtr);
      }
    },

    decodeAddress(address) {
      const decodeFn = requireWasmFunction(wasm, '_hd_btc_decode_address');
      const addrPtr = allocString(wasm, address);
      const typePtr = wasm._hd_alloc(4);
      const hashPtr = wasm._hd_alloc(32);
      const hashLenPtr = wasm._hd_alloc(4);
      const networkPtr = wasm._hd_alloc(4);
      try {
        wasm.setValue(hashLenPtr, 32, 'i32');
        const result = decodeFn(addrPtr, typePtr, hashPtr, hashLenPtr, networkPtr);
        checkResult(result);
        const hashLen = wasm.getValue(hashLenPtr, 'i32');
        return {
          type: wasm.getValue(typePtr, 'i32'),
          hash: readBytes(wasm, hashPtr, hashLen),
          network: wasm.getValue(networkPtr, 'i32')
        };
      } finally {
        wasm._hd_dealloc(addrPtr);
        wasm._hd_dealloc(typePtr);
        wasm._hd_dealloc(hashPtr);
        wasm._hd_dealloc(hashLenPtr);
        wasm._hd_dealloc(networkPtr);
      }
    },

    detectAddressType(address, network = Network.MAINNET) {
      const detectFn = requireWasmFunction(wasm, '_hd_btc_detect_address_type');
      const addrPtr = allocString(wasm, address);
      try {
        return detectFn(addrPtr, network);
      } finally {
        wasm._hd_dealloc(addrPtr);
      }
    },

    toWIF(privateKey, compressed = true, network = Network.MAINNET) {
      const toWifFn = requireWasmFunction(wasm, '_hd_btc_to_wif');
      const keyPtr = allocAndCopy(wasm, privateKey);
      const outPtr = wasm._hd_alloc(64);
      try {
        const result = toWifFn(keyPtr, compressed ? 1 : 0, network, outPtr, 64);
        checkResult(result);
        return readString(wasm, outPtr);
      } finally {
        wasm._hd_secure_wipe(keyPtr, 32);
        wasm._hd_dealloc(keyPtr);
        wasm._hd_dealloc(outPtr);
      }
    },

    fromWIF(wif) {
      const fromWifFn = requireWasmFunction(wasm, '_hd_btc_from_wif');
      const wifPtr = allocString(wasm, wif);
      const keyPtr = wasm._hd_alloc(32);
      const compressedPtr = wasm._hd_alloc(4);
      try {
        const result = fromWifFn(wifPtr, keyPtr, compressedPtr);
        checkResult(result);
        return {
          privateKey: readBytes(wasm, keyPtr, 32),
          compressed: wasm.getValue(compressedPtr, 'i32') === 1
        };
      } finally {
        wasm._hd_secure_wipe(keyPtr, 32);
        wasm._hd_dealloc(wifPtr);
        wasm._hd_dealloc(keyPtr);
        wasm._hd_dealloc(compressedPtr);
      }
    },

    signMessage(message, privateKey, compressed = true) {
      const signFn = requireWasmFunction(wasm, '_hd_btc_sign_message');
      const msgPtr = allocString(wasm, message);
      const keyPtr = allocAndCopy(wasm, privateKey);
      const sigPtr = wasm._hd_alloc(65);
      try {
        const result = signFn(msgPtr, keyPtr, compressed ? 1 : 0, sigPtr, 65);
        checkResult(result);
        const sigBytes = readBytes(wasm, sigPtr, 65);
        return encodeBase64Fallback(sigBytes);
      } finally {
        wasm._hd_secure_wipe(keyPtr, 32);
        wasm._hd_dealloc(msgPtr);
        wasm._hd_dealloc(keyPtr);
        wasm._hd_dealloc(sigPtr);
      }
    },

    verifyMessage(message, signature, address, network = Network.MAINNET) {
      const verifyFn = requireWasmFunction(wasm, '_hd_btc_verify_message');
      const sigBytes = decodeBase64Fallback(signature);
      const msgPtr = allocString(wasm, message);
      const sigPtr = allocAndCopy(wasm, sigBytes);
      const addrPtr = allocString(wasm, address);
      try {
        const result = verifyFn(msgPtr, sigPtr, sigBytes.length, addrPtr, network);
        return result === 1;
      } finally {
        wasm._hd_dealloc(msgPtr);
        wasm._hd_dealloc(sigPtr);
        wasm._hd_dealloc(addrPtr);
      }
    },

    /**
     * Transaction builder
     */
    tx: {
      _makeTxObj(handle) {
        return {
          _handle: handle,
          _inputCount: 0,

          addInput(input) {
            const addInputFn = requireWasmFunction(wasm, '_hd_btc_tx_add_input');
            // Convert hex txid to 32-byte array
            const txidHex = input.txid;
            const txidBytes = new Uint8Array(32);
            for (let i = 0; i < 32; i++) {
              txidBytes[i] = parseInt(txidHex.substr(i * 2, 2), 16);
            }
            const txidPtr = allocAndCopy(wasm, txidBytes);
            const pubPtr = input.pubkey ? allocAndCopy(wasm, input.pubkey) : 0;
            try {
              const result = addInputFn(
                this._handle, txidPtr, input.vout,
                BigInt(input.amount), input.scriptType || 0,
                pubPtr, input.sequence || 0xffffffff
              );
              checkResult(result);
              this._inputCount++;
            } finally {
              wasm._hd_dealloc(txidPtr);
              if (pubPtr) wasm._hd_dealloc(pubPtr);
            }
            return this;
          },

          addOutput(address, amount, network = Network.MAINNET) {
            // Use address-based output helper: decode address to script internally
            const addP2wpkhFn = getWasmFunction(wasm, '_hd_btc_tx_add_p2wpkh_output');
            const addP2pkhFn = getWasmFunction(wasm, '_hd_btc_tx_add_p2pkh_output');
            const addOutputFn = requireWasmFunction(wasm, '_hd_btc_tx_add_output');

            // Decode the address to get the hash
            const decodeFn = getWasmFunction(wasm, '_hd_btc_decode_address');
            if (decodeFn) {
              const addrPtr = allocString(wasm, address);
              const typePtr = wasm._hd_alloc(4);
              const hashPtr = wasm._hd_alloc(32);
              const hashLenPtr = wasm._hd_alloc(4);
              const networkPtr = wasm._hd_alloc(4);
              try {
                wasm.setValue(hashLenPtr, 32, 'i32');
                const decResult = decodeFn(addrPtr, typePtr, hashPtr, hashLenPtr, networkPtr);
                if (decResult === 0) {
                  const addrType = wasm.getValue(typePtr, 'i32');
                  const hashLen = wasm.getValue(hashLenPtr, 'i32');
                  // P2WPKH = 2, P2PKH = 0
                  if (addrType === 2 && addP2wpkhFn) {
                    const result = addP2wpkhFn(this._handle, BigInt(amount), hashPtr);
                    checkResult(result);
                    return this;
                  } else if (addrType === 0 && addP2pkhFn) {
                    const result = addP2pkhFn(this._handle, BigInt(amount), hashPtr);
                    checkResult(result);
                    return this;
                  }
                  // For other types, build script manually
                  const hash = readBytes(wasm, hashPtr, hashLen);
                  let script;
                  if (addrType === 2) { // P2WPKH: OP_0 <20-byte-hash>
                    script = new Uint8Array([0x00, 0x14, ...hash]);
                  } else if (addrType === 0) { // P2PKH: OP_DUP OP_HASH160 <20> <hash> OP_EQUALVERIFY OP_CHECKSIG
                    script = new Uint8Array([0x76, 0xa9, 0x14, ...hash, 0x88, 0xac]);
                  } else if (addrType === 1) { // P2SH: OP_HASH160 <20> <hash> OP_EQUAL
                    script = new Uint8Array([0xa9, 0x14, ...hash, 0x87]);
                  } else if (addrType === 3) { // P2WSH: OP_0 <32-byte-hash>
                    script = new Uint8Array([0x00, 0x20, ...hash]);
                  } else if (addrType === 4) { // P2TR: OP_1 <32-byte-key>
                    script = new Uint8Array([0x51, 0x20, ...hash]);
                  } else {
                    throw new HDWalletError(ErrorCode.INVALID_ARGUMENT, 'Unsupported address type');
                  }
                  const scriptPtr = allocAndCopy(wasm, script);
                  try {
                    const result = addOutputFn(this._handle, BigInt(amount), scriptPtr, script.length);
                    checkResult(result);
                  } finally {
                    wasm._hd_dealloc(scriptPtr);
                  }
                  return this;
                }
              } finally {
                wasm._hd_dealloc(addrPtr);
                wasm._hd_dealloc(typePtr);
                wasm._hd_dealloc(hashPtr);
                wasm._hd_dealloc(hashLenPtr);
                wasm._hd_dealloc(networkPtr);
              }
            }
            throw new HDWalletError(ErrorCode.NOT_SUPPORTED, 'Address decoding not available');
          },

          sign(privateKey) {
            const signFn = requireWasmFunction(wasm, '_hd_btc_tx_sign');
            const keyPtr = allocAndCopy(wasm, privateKey);
            try {
              // Sign all inputs
              for (let i = 0; i < this._inputCount; i++) {
                const result = signFn(this._handle, keyPtr, i);
                checkResult(result);
              }
            } finally {
              wasm._hd_secure_wipe(keyPtr, 32);
              wasm._hd_dealloc(keyPtr);
            }
            return this;
          },

          serialize() {
            const serializeFn = requireWasmFunction(wasm, '_hd_btc_tx_serialize');
            const outPtr = wasm._hd_alloc(65536);
            const sizePtr = wasm._hd_alloc(4);
            try {
              wasm.setValue(sizePtr, 65536, 'i32');
              const result = serializeFn(this._handle, outPtr, 65536, sizePtr);
              checkResult(result);
              const size = wasm.getValue(sizePtr, 'i32');
              return readBytes(wasm, outPtr, size);
            } finally {
              wasm._hd_dealloc(outPtr);
              wasm._hd_dealloc(sizePtr);
            }
          },

          getTxid() {
            const getTxidFn = requireWasmFunction(wasm, '_hd_btc_tx_get_txid_hex');
            const outPtr = wasm._hd_alloc(128);
            try {
              const result = getTxidFn(this._handle, outPtr, 128);
              checkResult(result);
              return readString(wasm, outPtr);
            } finally {
              wasm._hd_dealloc(outPtr);
            }
          },

          getSize() {
            const getSizeFn = requireWasmFunction(wasm, '_hd_btc_tx_get_size');
            return getSizeFn(this._handle);
          },

          getVsize() {
            const getVsizeFn = requireWasmFunction(wasm, '_hd_btc_tx_get_vsize');
            return getVsizeFn(this._handle);
          },

          getWeight() {
            const getWeightFn = requireWasmFunction(wasm, '_hd_btc_tx_get_weight');
            return getWeightFn(this._handle);
          },

          getFee() {
            const getFeeFn = requireWasmFunction(wasm, '_hd_btc_tx_get_fee');
            return getFeeFn(this._handle);
          },

          validate() {
            const validateFn = requireWasmFunction(wasm, '_hd_btc_tx_validate');
            return validateFn(this._handle) === 0;
          },

          destroy() {
            if (this._handle) {
              const destroyFn = requireWasmFunction(wasm, '_hd_btc_tx_destroy');
              destroyFn(this._handle);
              this._handle = null;
            }
          }
        };
      },

      create() {
        const createFn = getWasmFunction(wasm, '_hd_btc_tx_create');
        if (!createFn) throw new HDWalletError(ErrorCode.NOT_SUPPORTED);
        const handle = createFn();
        if (!handle) throw new HDWalletError(ErrorCode.NOT_SUPPORTED);
        return this._makeTxObj(handle);
      },

      parse(data) {
        const parseFn = requireWasmFunction(wasm, '_hd_btc_tx_parse');
        const dataPtr = allocAndCopy(wasm, data);
        try {
          const handle = parseFn(dataPtr, data.length);
          if (!handle) throw new HDWalletError(ErrorCode.INVALID_TRANSACTION);
          return this._makeTxObj(handle);
        } finally {
          wasm._hd_dealloc(dataPtr);
        }
      }
    }
  };

  // ==========================================================================
  // Ethereum API
  // ==========================================================================

  /**
   * Ethereum API
   * @type {Object}
   */
  const ethereum = {
    /**
     * Get Ethereum address from public key
     * @param {Uint8Array} publicKey - Uncompressed public key (65 bytes)
     * @returns {string} Ethereum address (with 0x prefix)
     */
    getAddress(publicKey) {
      const getAddressFn = requireWasmFunction(wasm, '_hd_eth_address');
      const pubPtr = allocAndCopy(wasm, publicKey);
      const outPtr = wasm._hd_alloc(64);
      try {
        const result = getAddressFn(pubPtr, publicKey.length, outPtr, 64);
        checkResult(result);
        return readString(wasm, outPtr);
      } finally {
        wasm._hd_dealloc(pubPtr);
        wasm._hd_dealloc(outPtr);
      }
    },

    /**
     * Get checksummed address (EIP-55)
     * @param {string} address - Address to checksum
     * @returns {string} Checksummed address
     */
    getChecksumAddress(address) {
      const checksumFn = requireWasmFunction(wasm, '_hd_eth_checksum_address');
      const addrPtr = allocString(wasm, address);
      const outPtr = wasm._hd_alloc(64);
      try {
        const result = checksumFn(addrPtr, outPtr, 64);
        checkResult(result);
        return readString(wasm, outPtr);
      } finally {
        wasm._hd_dealloc(addrPtr);
        wasm._hd_dealloc(outPtr);
      }
    },

    /**
     * Validate Ethereum address
     * @param {string} address - Address to validate
     * @returns {boolean} True if valid
     */
    validateAddress(address) {
      const validateFn = requireWasmFunction(wasm, '_hd_eth_validate_address');
      const addrPtr = allocString(wasm, address);
      try {
        const result = validateFn(addrPtr);
        return result === 0;
      } finally {
        wasm._hd_dealloc(addrPtr);
      }
    },

    /**
     * Sign message (EIP-191)
     * @param {string} message - Message to sign
     * @param {Uint8Array} privateKey - Private key
     * @returns {string} Hex-encoded signature
     */
    signMessage(message, privateKey) {
      const signFn = requireWasmFunction(wasm, '_hd_eth_sign_message');
      const enc = new TextEncoder();
      const msgBytes = typeof message === 'string' ? enc.encode(message) : message;
      const msgPtr = allocAndCopy(wasm, msgBytes);
      const keyPtr = allocAndCopy(wasm, privateKey);
      const sigPtr = wasm._hd_alloc(65);
      try {
        const result = signFn(msgPtr, msgBytes.length, keyPtr, sigPtr, 65);
        checkResult(result);
        const sigBytes = readBytes(wasm, sigPtr, 65);
        return '0x' + encodeHexFallback(sigBytes);
      } finally {
        wasm._hd_secure_wipe(keyPtr, 32);
        wasm._hd_dealloc(msgPtr);
        wasm._hd_dealloc(keyPtr);
        wasm._hd_dealloc(sigPtr);
      }
    },

    /**
     * Sign typed data (EIP-712)
     * @param {Object} typedData - Typed data object
     * @param {Uint8Array} privateKey - Private key
     * @returns {string} Hex-encoded signature
     */
    signTypedData(typedData, privateKey) {
      const signTypedDataFn = requireWasmFunction(wasm, '_hd_eth_sign_typed_data');

      // --- EIP-712 struct hash computation (data encoding, not crypto) ---
      // keccak256 via WASM
      function k256(data) {
        const d = data instanceof Uint8Array ? data : new TextEncoder().encode(data);
        const dPtr = allocAndCopy(wasm, d);
        const hPtr = wasm._hd_alloc(32);
        try {
          const r = wasm._hd_hash_keccak256(dPtr, d.length, hPtr, 32);
          if (r < 0) throw new HDWalletError(r);
          return readBytes(wasm, hPtr, 32);
        } finally {
          wasm._hd_dealloc(dPtr);
          wasm._hd_dealloc(hPtr);
        }
      }

      // Build canonical type string with referenced types sorted alphabetically
      function encodeType(typeName) {
        const fields = typedData.types[typeName];
        if (!fields) throw new HDWalletError(ErrorCode.INVALID_ARGUMENT, `Unknown EIP-712 type: ${typeName}`);
        let result = `${typeName}(${fields.map(f => `${f.type} ${f.name}`).join(',')})`;
        const referenced = new Set();
        function findRefs(tn) {
          for (const field of typedData.types[tn] || []) {
            const base = field.type.replace(/\[\d*\]$/, '');
            if (typedData.types[base] && base !== typeName && !referenced.has(base)) {
              referenced.add(base);
              findRefs(base);
            }
          }
        }
        findRefs(typeName);
        for (const ref of Array.from(referenced).sort()) {
          result += `${ref}(${typedData.types[ref].map(f => `${f.type} ${f.name}`).join(',')})`;
        }
        return result;
      }

      // Encode a single value according to its EIP-712 type
      function encodeValue(type, value) {
        if (type === 'string') return k256(value);
        if (type === 'bytes') {
          const hex = value.startsWith('0x') ? value.slice(2) : value;
          return k256(decodeHexFallback(hex));
        }
        if (type === 'address') {
          const hex = (value.startsWith('0x') ? value.slice(2) : value).toLowerCase();
          const padded = new Uint8Array(32);
          const bytes = decodeHexFallback(hex);
          padded.set(bytes, 32 - bytes.length);
          return padded;
        }
        if (/^uint\d*$/.test(type) || /^int\d*$/.test(type)) {
          let n = BigInt(value);
          if (n < 0n) n = (1n << 256n) + n;
          const bytes = new Uint8Array(32);
          for (let i = 31; i >= 0 && n > 0n; i--) {
            bytes[i] = Number(n & 0xffn);
            n >>= 8n;
          }
          return bytes;
        }
        if (type === 'bool') {
          const bytes = new Uint8Array(32);
          bytes[31] = value ? 1 : 0;
          return bytes;
        }
        if (/^bytes\d+$/.test(type)) {
          const hex = (typeof value === 'string' && value.startsWith('0x')) ? value.slice(2) : (typeof value === 'string' ? value : encodeHexFallback(value));
          const padded = new Uint8Array(32);
          padded.set(decodeHexFallback(hex));
          return padded;
        }
        if (typedData.types[type]) return hashStruct(type, value);
        if (type.endsWith(']')) {
          const base = type.replace(/\[\d*\]$/, '');
          const parts = value.map(v => encodeValue(base, v));
          const buf = new Uint8Array(parts.length * 32);
          parts.forEach((p, i) => buf.set(p, i * 32));
          return k256(buf);
        }
        throw new HDWalletError(ErrorCode.INVALID_ARGUMENT, `Unsupported EIP-712 type: ${type}`);
      }

      function hashStruct(typeName, data) {
        const th = k256(encodeType(typeName));
        const fields = typedData.types[typeName];
        const parts = [th, ...fields.map(f => encodeValue(f.type, data[f.name]))];
        const buf = new Uint8Array(parts.length * 32);
        parts.forEach((p, i) => buf.set(p, i * 32));
        return k256(buf);
      }

      const structHash = hashStruct(typedData.primaryType, typedData.message);
      // --- End EIP-712 struct hash computation ---

      const domain = typedData.domain;
      const namePtr = allocString(wasm, domain.name || '');
      const versionPtr = allocString(wasm, domain.version || '');
      const contractPtr = allocString(wasm, domain.verifyingContract || '');
      const structHashPtr = allocAndCopy(wasm, structHash);
      const keyPtr = allocAndCopy(wasm, privateKey);
      const sigPtr = wasm._hd_alloc(65);
      try {
        const result = signTypedDataFn(namePtr, versionPtr, BigInt(domain.chainId || 0), contractPtr, structHashPtr, keyPtr, sigPtr, 65);
        checkResult(result);
        const sigBytes = readBytes(wasm, sigPtr, 65);
        return '0x' + encodeHexFallback(sigBytes);
      } finally {
        wasm._hd_secure_wipe(keyPtr, 32);
        wasm._hd_dealloc(namePtr);
        wasm._hd_dealloc(versionPtr);
        wasm._hd_dealloc(contractPtr);
        wasm._hd_dealloc(structHashPtr);
        wasm._hd_dealloc(keyPtr);
        wasm._hd_dealloc(sigPtr);
      }
    },

    verifyMessage(message, signature) {
      const recoverFn = requireWasmFunction(wasm, '_hd_eth_recover_address');
      const enc = new TextEncoder();
      const msgBytes = typeof message === 'string' ? enc.encode(message) : message;
      const sigHex = signature.startsWith('0x') ? signature.slice(2) : signature;
      const sigBytes = decodeHexFallback(sigHex);
      const msgPtr = allocAndCopy(wasm, msgBytes);
      const sigPtr = allocAndCopy(wasm, sigBytes);
      const addrPtr = wasm._hd_alloc(64);
      try {
        const result = recoverFn(msgPtr, msgBytes.length, sigPtr, sigBytes.length, addrPtr, 64);
        checkResult(result);
        return readString(wasm, addrPtr);
      } finally {
        wasm._hd_dealloc(msgPtr);
        wasm._hd_dealloc(sigPtr);
        wasm._hd_dealloc(addrPtr);
      }
    },

    verifyChecksum(address) {
      const verifyFn = requireWasmFunction(wasm, '_hd_eth_verify_checksum');
      const addrPtr = allocString(wasm, address);
      try {
        return verifyFn(addrPtr) === 1;
      } finally {
        wasm._hd_dealloc(addrPtr);
      }
    },

    hashMessage(message) {
      const hashFn = requireWasmFunction(wasm, '_hd_eth_hash_message');
      const enc = new TextEncoder();
      const msgBytes = typeof message === 'string' ? enc.encode(message) : message;
      const msgPtr = allocAndCopy(wasm, msgBytes);
      const hashPtr = wasm._hd_alloc(32);
      try {
        const result = hashFn(msgPtr, msgBytes.length, hashPtr);
        checkResult(result);
        return readBytes(wasm, hashPtr, 32);
      } finally {
        wasm._hd_dealloc(msgPtr);
        wasm._hd_dealloc(hashPtr);
      }
    },

    tx: {
      _makeTxObj(handle) {
        return {
          _handle: handle,

          sign(privateKey) {
            const signFn = requireWasmFunction(wasm, '_hd_eth_tx_sign');
            const keyPtr = allocAndCopy(wasm, privateKey);
            try {
              const result = signFn(this._handle, keyPtr);
              checkResult(result);
            } finally {
              wasm._hd_secure_wipe(keyPtr, 32);
              wasm._hd_dealloc(keyPtr);
            }
            return this;
          },

          serialize() {
            const serializeFn = requireWasmFunction(wasm, '_hd_eth_tx_serialize');
            const outPtr = wasm._hd_alloc(65536);
            const sizePtr = wasm._hd_alloc(4);
            try {
              wasm.setValue(sizePtr, 65536, 'i32');
              const result = serializeFn(this._handle, outPtr, 65536, sizePtr);
              checkResult(result);
              const size = wasm.getValue(sizePtr, 'i32');
              return readBytes(wasm, outPtr, size);
            } finally {
              wasm._hd_dealloc(outPtr);
              wasm._hd_dealloc(sizePtr);
            }
          },

          getHash() {
            const getHashFn = requireWasmFunction(wasm, '_hd_eth_tx_get_hash_hex');
            const outPtr = wasm._hd_alloc(128);
            try {
              const result = getHashFn(this._handle, outPtr, 128);
              checkResult(result);
              return readString(wasm, outPtr);
            } finally {
              wasm._hd_dealloc(outPtr);
            }
          },

          destroy() {
            if (this._handle) {
              const destroyFn = requireWasmFunction(wasm, '_hd_eth_tx_destroy');
              destroyFn(this._handle);
              this._handle = null;
            }
          }
        };
      },

      createEIP1559(params) {
        const createFn = requireWasmFunction(wasm, '_hd_eth_tx_create_eip1559');
        const handle = createFn(
          BigInt(params.chainId),
          BigInt(params.maxPriorityFeePerGas),
          BigInt(params.maxFeePerGas)
        );
        if (!handle) throw new HDWalletError(ErrorCode.NOT_SUPPORTED);

        // Set remaining fields
        const setNonceFn = requireWasmFunction(wasm, '_hd_eth_tx_set_nonce');
        setNonceFn(handle, BigInt(params.nonce || 0));

        const setGasLimitFn = requireWasmFunction(wasm, '_hd_eth_tx_set_gas_limit');
        setGasLimitFn(handle, BigInt(params.gasLimit));

        if (params.to) {
          const setToFn = requireWasmFunction(wasm, '_hd_eth_tx_set_to_hex');
          const toPtr = allocString(wasm, params.to);
          try {
            setToFn(handle, toPtr);
          } finally {
            wasm._hd_dealloc(toPtr);
          }
        }

        if (params.value !== undefined) {
          const setValueFn = requireWasmFunction(wasm, '_hd_eth_tx_set_value');
          setValueFn(handle, BigInt(params.value));
        }

        if (params.data) {
          const setDataFn = requireWasmFunction(wasm, '_hd_eth_tx_set_data');
          const dataPtr = allocAndCopy(wasm, params.data);
          try {
            setDataFn(handle, dataPtr, params.data.length);
          } finally {
            wasm._hd_dealloc(dataPtr);
          }
        }

        return this._makeTxObj(handle);
      },

      create(params) {
        const createFn = requireWasmFunction(wasm, '_hd_eth_tx_create');
        const handle = createFn(BigInt(params?.chainId || 1));
        if (!handle) throw new HDWalletError(ErrorCode.NOT_SUPPORTED);
        return this._makeTxObj(handle);
      },

      parse(data) {
        const parseFn = requireWasmFunction(wasm, '_hd_eth_tx_parse');
        const dataPtr = allocAndCopy(wasm, data);
        try {
          const handle = parseFn(dataPtr, data.length);
          if (!handle) throw new HDWalletError(ErrorCode.INVALID_TRANSACTION);
          return this._makeTxObj(handle);
        } finally {
          wasm._hd_dealloc(dataPtr);
        }
      }
    }
  };

  // ==========================================================================
  // Cosmos API
  // ==========================================================================

  /**
   * Cosmos/Tendermint API
   * @type {Object}
   */
  const cosmos = {
    getAddress(publicKey, prefix = 'cosmos') {
      const getAddressFn = requireWasmFunction(wasm, '_hd_cosmos_address');
      const pubPtr = allocAndCopy(wasm, publicKey);
      const prefixPtr = allocString(wasm, prefix);
      const outPtr = wasm._hd_alloc(128);
      try {
        const result = getAddressFn(pubPtr, publicKey.length, prefixPtr, outPtr, 128);
        checkResult(result);
        return readString(wasm, outPtr);
      } finally {
        wasm._hd_dealloc(pubPtr);
        wasm._hd_dealloc(prefixPtr);
        wasm._hd_dealloc(outPtr);
      }
    },

    validateAddress(address, prefix) {
      const validateFn = requireWasmFunction(wasm, '_hd_cosmos_validate_address');
      const addrPtr = allocString(wasm, address);
      const prefixPtr = prefix ? allocString(wasm, prefix) : 0;
      try {
        return validateFn(addrPtr, prefixPtr) === 0;
      } finally {
        wasm._hd_dealloc(addrPtr);
        if (prefixPtr) wasm._hd_dealloc(prefixPtr);
      }
    },

    convertPrefix(address, newPrefix) {
      const convertFn = requireWasmFunction(wasm, '_hd_cosmos_convert_prefix');
      const addrPtr = allocString(wasm, address);
      const prefixPtr = allocString(wasm, newPrefix);
      const outPtr = wasm._hd_alloc(128);
      try {
        const result = convertFn(addrPtr, prefixPtr, outPtr, 128);
        checkResult(result);
        return readString(wasm, outPtr);
      } finally {
        wasm._hd_dealloc(addrPtr);
        wasm._hd_dealloc(prefixPtr);
        wasm._hd_dealloc(outPtr);
      }
    },

    signAmino(doc, privateKey) {
      const signAminoFn = requireWasmFunction(wasm, '_hd_cosmos_sign_amino');
      // Serialize the sign doc to canonical JSON bytes
      const jsonStr = JSON.stringify(doc);
      const enc = new TextEncoder();
      const docBytes = enc.encode(jsonStr);
      const docPtr = allocAndCopy(wasm, docBytes);
      const keyPtr = allocAndCopy(wasm, privateKey);
      const sigPtr = wasm._hd_alloc(64);
      try {
        const result = signAminoFn(docPtr, docBytes.length, keyPtr, sigPtr, 64);
        checkResult(result);
        return readBytes(wasm, sigPtr, 64);
      } finally {
        wasm._hd_secure_wipe(keyPtr, 32);
        wasm._hd_dealloc(docPtr);
        wasm._hd_dealloc(keyPtr);
        wasm._hd_dealloc(sigPtr);
      }
    },

    signDirect(...args) {
      const signDirectFn = requireWasmFunction(wasm, '_hd_cosmos_sign_direct');
      // Support both 2-arg (signDocBytes, privateKey) and 5-arg
      // (bodyBytes, authInfoBytes, chainId, accountNumber, privateKey) forms
      if (args.length === 2) {
        const [signDocBytes, privateKey] = args;
        const docPtr = allocAndCopy(wasm, signDocBytes);
        const keyPtr = allocAndCopy(wasm, privateKey);
        const sigPtr = wasm._hd_alloc(64);
        try {
          const result = signDirectFn(docPtr, signDocBytes.length, keyPtr, sigPtr, 64);
          checkResult(result);
          return readBytes(wasm, sigPtr, 64);
        } finally {
          wasm._hd_secure_wipe(keyPtr, 32);
          wasm._hd_dealloc(docPtr);
          wasm._hd_dealloc(keyPtr);
          wasm._hd_dealloc(sigPtr);
        }
      } else {
        // 5-arg form: build SignDoc from body, authInfo, chainId, accountNumber
        const [bodyBytes, authInfoBytes, chainId, accountNumber, privateKey] = args;
        // Build a minimal SignDoc by concatenating the fields
        // The C function takes raw signDoc bytes, so we need to construct them
        // For protobuf SignDoc: bodyBytes + authInfoBytes + chainId + accountNumber
        // We'll pass the raw concatenation and let the C function handle it
        const enc = new TextEncoder();
        const chainBytes = enc.encode(chainId);
        const accNum = BigInt(accountNumber);
        // Build a simple envelope: length-prefixed fields
        const totalLen = bodyBytes.length + authInfoBytes.length + chainBytes.length + 8;
        const signDoc = new Uint8Array(totalLen);
        let off = 0;
        signDoc.set(bodyBytes, off); off += bodyBytes.length;
        signDoc.set(authInfoBytes, off); off += authInfoBytes.length;
        signDoc.set(chainBytes, off); off += chainBytes.length;
        // Account number as 8 bytes LE
        for (let i = 0; i < 8; i++) {
          signDoc[off + i] = Number((accNum >> BigInt(i * 8)) & 0xFFn);
        }
        const docPtr = allocAndCopy(wasm, signDoc);
        const keyPtr = allocAndCopy(wasm, privateKey);
        const sigPtr = wasm._hd_alloc(64);
        try {
          const result = signDirectFn(docPtr, signDoc.length, keyPtr, sigPtr, 64);
          checkResult(result);
          return readBytes(wasm, sigPtr, 64);
        } finally {
          wasm._hd_secure_wipe(keyPtr, 32);
          wasm._hd_dealloc(docPtr);
          wasm._hd_dealloc(keyPtr);
          wasm._hd_dealloc(sigPtr);
        }
      }
    },

    verifySignature(message, signature, publicKey) {
      const verifyFn = requireWasmFunction(wasm, '_hd_cosmos_verify_signature');
      const msgPtr = allocAndCopy(wasm, message);
      const sigPtr = allocAndCopy(wasm, signature);
      const pubPtr = allocAndCopy(wasm, publicKey);
      try {
        return verifyFn(msgPtr, message.length, sigPtr, signature.length, pubPtr, publicKey.length) === 1;
      } finally {
        wasm._hd_dealloc(msgPtr);
        wasm._hd_dealloc(sigPtr);
        wasm._hd_dealloc(pubPtr);
      }
    },

    verify(signature, message, publicKey) {
      return this.verifySignature(message, signature, publicKey);
    }
  };

  // ==========================================================================
  // Solana API
  // ==========================================================================

  /**
   * Solana API
   * @type {Object}
   */
  const solana = {
    getAddress(publicKey) {
      const getAddressFn = requireWasmFunction(wasm, '_hd_sol_address');
      const pubPtr = allocAndCopy(wasm, publicKey);
      const outPtr = wasm._hd_alloc(64);
      try {
        const result = getAddressFn(pubPtr, publicKey.length, outPtr, 64);
        checkResult(result);
        return readString(wasm, outPtr);
      } finally {
        wasm._hd_dealloc(pubPtr);
        wasm._hd_dealloc(outPtr);
      }
    },

    validateAddress(address) {
      const validateFn = requireWasmFunction(wasm, '_hd_sol_validate_address');
      const addrPtr = allocString(wasm, address);
      try {
        return validateFn(addrPtr) === 0;
      } finally {
        wasm._hd_dealloc(addrPtr);
      }
    },

    signMessage(message, privateKey) {
      const signFn = requireWasmFunction(wasm, '_hd_sol_sign_message');
      const msgPtr = allocAndCopy(wasm, message);
      const keyPtr = allocAndCopy(wasm, privateKey);
      const sigPtr = wasm._hd_alloc(64);
      try {
        const result = signFn(msgPtr, message.length, keyPtr, sigPtr, 64);
        checkResult(result);
        return readBytes(wasm, sigPtr, 64);
      } finally {
        wasm._hd_secure_wipe(keyPtr, 32);
        wasm._hd_dealloc(msgPtr);
        wasm._hd_dealloc(keyPtr);
        wasm._hd_dealloc(sigPtr);
      }
    },

    verifyMessage(message, signature, publicKey) {
      const verifyFn = requireWasmFunction(wasm, '_hd_sol_verify_message');
      const msgPtr = allocAndCopy(wasm, message);
      const sigPtr = allocAndCopy(wasm, signature);
      const pubPtr = allocAndCopy(wasm, publicKey);
      try {
        return verifyFn(msgPtr, message.length, sigPtr, signature.length, pubPtr, publicKey.length) === 1;
      } finally {
        wasm._hd_dealloc(msgPtr);
        wasm._hd_dealloc(sigPtr);
        wasm._hd_dealloc(pubPtr);
      }
    },

    addressToPubkey(address) {
      const fn = requireWasmFunction(wasm, '_hd_sol_address_to_pubkey');
      const addrPtr = allocString(wasm, address);
      const outPtr = wasm._hd_alloc(32);
      try {
        const result = fn(addrPtr, outPtr, 32);
        checkResult(result);
        return readBytes(wasm, outPtr, 32);
      } finally {
        wasm._hd_dealloc(addrPtr);
        wasm._hd_dealloc(outPtr);
      }
    },

    derivePubkey(privateKey) {
      const fn = requireWasmFunction(wasm, '_hd_sol_derive_pubkey');
      const keyPtr = allocAndCopy(wasm, privateKey);
      const outPtr = wasm._hd_alloc(32);
      try {
        const result = fn(keyPtr, outPtr, 32);
        checkResult(result);
        return readBytes(wasm, outPtr, 32);
      } finally {
        wasm._hd_secure_wipe(keyPtr, 32);
        wasm._hd_dealloc(keyPtr);
        wasm._hd_dealloc(outPtr);
      }
    },

    findPDA(programId, seeds) {
      const fn = requireWasmFunction(wasm, '_hd_sol_find_pda');
      const progPtr = allocAndCopy(wasm, programId);
      let totalLen = 0;
      for (const s of seeds) totalLen += s.length;
      const flatSeeds = new Uint8Array(totalLen);
      let off = 0;
      for (const s of seeds) { flatSeeds.set(s, off); off += s.length; }
      const seedsPtr = totalLen > 0 ? allocAndCopy(wasm, flatSeeds) : 0;
      const seedLensPtr = wasm._hd_alloc(seeds.length * 4);
      for (let i = 0; i < seeds.length; i++) {
        wasm.setValue(seedLensPtr + i * 4, seeds[i].length, 'i32');
      }
      const addrPtr = wasm._hd_alloc(64);
      const bumpPtr = wasm._hd_alloc(1);
      try {
        const result = fn(progPtr, seedsPtr || 0, totalLen, seedLensPtr, seeds.length, addrPtr, 64, bumpPtr);
        checkResult(result);
        return {
          address: readString(wasm, addrPtr),
          bump: wasm.HEAPU8[bumpPtr]
        };
      } finally {
        wasm._hd_dealloc(progPtr);
        if (seedsPtr) wasm._hd_dealloc(seedsPtr);
        wasm._hd_dealloc(seedLensPtr);
        wasm._hd_dealloc(addrPtr);
        wasm._hd_dealloc(bumpPtr);
      }
    },

    getAssociatedTokenAddress(walletAddress, mintAddress) {
      const fn = requireWasmFunction(wasm, '_hd_sol_get_associated_token_address');
      const walletPtr = allocString(wasm, walletAddress);
      const mintPtr = allocString(wasm, mintAddress);
      const outPtr = wasm._hd_alloc(64);
      try {
        const result = fn(walletPtr, mintPtr, outPtr, 64);
        checkResult(result);
        return readString(wasm, outPtr);
      } finally {
        wasm._hd_dealloc(walletPtr);
        wasm._hd_dealloc(mintPtr);
        wasm._hd_dealloc(outPtr);
      }
    }
  };

  // ==========================================================================
  // Polkadot API
  // ==========================================================================

  /**
   * Polkadot/Substrate API
   * @type {Object}
   */
  const polkadot = {
    getAddress(publicKey, ss58Prefix = 0) {
      const getAddressFn = requireWasmFunction(wasm, '_hd_dot_address');
      const pubPtr = allocAndCopy(wasm, publicKey);
      const outPtr = wasm._hd_alloc(64);
      try {
        const result = getAddressFn(pubPtr, publicKey.length, ss58Prefix, outPtr, 64);
        checkResult(result);
        return readString(wasm, outPtr);
      } finally {
        wasm._hd_dealloc(pubPtr);
        wasm._hd_dealloc(outPtr);
      }
    },

    validateAddress(address, prefix) {
      const validateFn = requireWasmFunction(wasm, '_hd_dot_validate_address');
      const addrPtr = allocString(wasm, address);
      try {
        return validateFn(addrPtr, prefix !== undefined ? prefix : -1) === 0;
      } finally {
        wasm._hd_dealloc(addrPtr);
      }
    },

    decodeAddress(address) {
      const decodeFn = requireWasmFunction(wasm, '_hd_dot_decode_address');
      const addrPtr = allocString(wasm, address);
      const prefixPtr = wasm._hd_alloc(2);
      const pubPtr = wasm._hd_alloc(32);
      try {
        const result = decodeFn(addrPtr, prefixPtr, pubPtr, 32);
        checkResult(result);
        return {
          prefix: wasm.getValue(prefixPtr, 'i16'),
          publicKey: readBytes(wasm, pubPtr, 32)
        };
      } finally {
        wasm._hd_dealloc(addrPtr);
        wasm._hd_dealloc(prefixPtr);
        wasm._hd_dealloc(pubPtr);
      }
    },

    convertPrefix(address, newPrefix) {
      const convertFn = requireWasmFunction(wasm, '_hd_dot_convert_prefix');
      const addrPtr = allocString(wasm, address);
      const outPtr = wasm._hd_alloc(64);
      try {
        const result = convertFn(addrPtr, newPrefix, outPtr, 64);
        checkResult(result);
        return readString(wasm, outPtr);
      } finally {
        wasm._hd_dealloc(addrPtr);
        wasm._hd_dealloc(outPtr);
      }
    },

    derivePubkey(privateKey) {
      const fn = requireWasmFunction(wasm, '_hd_dot_derive_pubkey');
      const keyPtr = allocAndCopy(wasm, privateKey);
      const outPtr = wasm._hd_alloc(32);
      try {
        const result = fn(keyPtr, outPtr, 32);
        checkResult(result);
        return readBytes(wasm, outPtr, 32);
      } finally {
        wasm._hd_secure_wipe(keyPtr, 32);
        wasm._hd_dealloc(keyPtr);
        wasm._hd_dealloc(outPtr);
      }
    },

    signMessage(message, privateKey) {
      const signFn = requireWasmFunction(wasm, '_hd_dot_sign_message');
      const msgPtr = allocAndCopy(wasm, message);
      const keyPtr = allocAndCopy(wasm, privateKey);
      const sigPtr = wasm._hd_alloc(64);
      try {
        const result = signFn(msgPtr, message.length, keyPtr, sigPtr, 64);
        checkResult(result);
        return readBytes(wasm, sigPtr, 64);
      } finally {
        wasm._hd_secure_wipe(keyPtr, 32);
        wasm._hd_dealloc(msgPtr);
        wasm._hd_dealloc(keyPtr);
        wasm._hd_dealloc(sigPtr);
      }
    },

    verifyMessage(message, signature, publicKey) {
      const verifyFn = requireWasmFunction(wasm, '_hd_dot_verify_message');
      const msgPtr = allocAndCopy(wasm, message);
      const sigPtr = allocAndCopy(wasm, signature);
      const pubPtr = allocAndCopy(wasm, publicKey);
      try {
        return verifyFn(msgPtr, message.length, sigPtr, signature.length, pubPtr, publicKey.length) === 1;
      } finally {
        wasm._hd_dealloc(msgPtr);
        wasm._hd_dealloc(sigPtr);
        wasm._hd_dealloc(pubPtr);
      }
    }
  };

  // ==========================================================================
  // Hardware Wallet API
  // ==========================================================================

  /**
   * Hardware Wallet API (requires WASI bridge)
   * @type {Object}
   */
  const hardware = {
    /**
     * Check if hardware wallet support is available
     * @returns {boolean}
     */
    isAvailable() {
      const hasFeatureFn = getWasmFunction(wasm, '_hd_wasi_has_feature');
      return hasFeatureFn ? hasFeatureFn(WasiFeature.USB_HID) !== 0 : false;
    },

    /**
     * Enumerate connected hardware wallets
     * @returns {Promise<Object[]>} Array of device descriptors
     */
    async enumerate() {
      const enumerateFn = getWasmFunction(wasm, '_hd_hw_enumerate');
      if (!enumerateFn) return [];
      const ptr = enumerateFn();
      const json = readString(wasm, ptr);
      return JSON.parse(json);
    },

    /**
     * Connect to a hardware wallet
     * @param {string} devicePath - Device path from enumeration
     * @returns {Promise<Object>} Hardware wallet interface
     */
    async connect(devicePath) {
      if (!this.isAvailable()) {
        throw new HDWalletError(
          ErrorCode.NOT_SUPPORTED,
          'Hardware wallet support is not available in this build'
        );
      }

      const pathPtr = allocString(wasm, devicePath);
      try {
        const handle = wasm._hd_hw_connect(pathPtr);
        if (!handle) {
          throw new HDWalletError(ErrorCode.DEVICE_NOT_CONNECTED);
        }

        return {
          _handle: handle,

          get vendor() {
            const ptr = wasm._hd_hw_get_vendor(this._handle);
            return readString(wasm, ptr);
          },

          get model() {
            const ptr = wasm._hd_hw_get_model(this._handle);
            return readString(wasm, ptr);
          },

          get firmwareVersion() {
            const ptr = wasm._hd_hw_get_firmware_version(this._handle);
            return readString(wasm, ptr);
          },

          get isConnected() {
            return wasm._hd_hw_is_connected(this._handle) !== 0;
          },

          async getPublicKey(path, curve = Curve.SECP256K1) {
            const pathPtr = allocString(wasm, path);
            const outPtr = wasm._hd_alloc(65);
            try {
              const result = wasm._hd_hw_get_public_key(this._handle, pathPtr, curve, outPtr, 65);
              checkResult(result);
              return readBytes(wasm, outPtr, 33);
            } finally {
              wasm._hd_dealloc(pathPtr);
              wasm._hd_dealloc(outPtr);
            }
          },

          async signTransaction(path, transaction) {
            const pathPtr = allocString(wasm, path);
            const txPtr = allocAndCopy(wasm, transaction);
            const sigPtr = wasm._hd_alloc(128);
            try {
              const result = wasm._hd_hw_sign_transaction(this._handle, pathPtr, txPtr, transaction.length, sigPtr, 128);
              checkResult(result);
              return readBytes(wasm, sigPtr, 64);
            } finally {
              wasm._hd_dealloc(pathPtr);
              wasm._hd_dealloc(txPtr);
              wasm._hd_dealloc(sigPtr);
            }
          },

          async signMessage(path, message) {
            const pathPtr = allocString(wasm, path);
            const msgPtr = allocString(wasm, message);
            const sigPtr = wasm._hd_alloc(128);
            try {
              const result = wasm._hd_hw_sign_message(this._handle, pathPtr, msgPtr, sigPtr, 128);
              checkResult(result);
              return readBytes(wasm, sigPtr, 64);
            } finally {
              wasm._hd_dealloc(pathPtr);
              wasm._hd_dealloc(msgPtr);
              wasm._hd_dealloc(sigPtr);
            }
          },

          async ping() {
            return wasm._hd_hw_ping(this._handle) === 0;
          },

          disconnect() {
            if (this._handle) {
              wasm._hd_hw_disconnect(this._handle);
              this._handle = null;
            }
          }
        };
      } finally {
        wasm._hd_dealloc(pathPtr);
      }
    }
  };

  // ==========================================================================
  // Keyring API
  // ==========================================================================

  /**
   * Keyring API
   * @type {Object}
   */
  const keyring = {
    /**
     * Create a new keyring
     * @returns {Object} Keyring instance
     */
    create() {
      const createFn = getWasmFunction(wasm, '_hd_keyring_create');
      const handle = createFn ? createFn() : null;

      return {
        _handle: handle,

        addWallet(seed, name) {
          const addWalletFn = getWasmFunction(wasm, '_hd_keyring_add_wallet');
          if (!addWalletFn) return '';
          const seedPtr = allocAndCopy(wasm, seed);
          const namePtr = name ? allocString(wasm, name) : 0;
          try {
            const ptr = addWalletFn(this._handle, seedPtr, seed.length, namePtr);
            return readString(wasm, ptr);
          } finally {
            wasm._hd_secure_wipe(seedPtr, seed.length);
            wasm._hd_dealloc(seedPtr);
            if (namePtr) wasm._hd_dealloc(namePtr);
          }
        },

        removeWallet(id) {
          const removeWalletFn = requireWasmFunction(wasm, '_hd_keyring_remove_wallet', ErrorCode.NOT_SUPPORTED, 'Keyring is not available in the WASM build');
          const idPtr = allocString(wasm, id);
          try {
            const result = removeWalletFn(this._handle, idPtr);
            checkResult(result);
          } finally {
            wasm._hd_dealloc(idPtr);
          }
        },

        getWalletCount() {
          const getWalletCountFn = getWasmFunction(wasm, '_hd_keyring_get_wallet_count');
          return getWalletCountFn ? getWalletCountFn(this._handle) : 0;
        },

        getAccounts(walletId, coinType, count = 10) {
          const getAccountsFn = getWasmFunction(wasm, '_hd_keyring_get_accounts');
          if (!getAccountsFn) return [];
          const idPtr = allocString(wasm, walletId);
          try {
            const ptr = getAccountsFn(this._handle, idPtr, coinType, count);
            return JSON.parse(readString(wasm, ptr));
          } finally {
            wasm._hd_dealloc(idPtr);
          }
        },

        signTransaction(walletId, path, transaction) {
          const signTransactionFn = requireWasmFunction(wasm, '_hd_keyring_sign_transaction', ErrorCode.NOT_SUPPORTED, 'Keyring is not available in the WASM build');
          const idPtr = allocString(wasm, walletId);
          const pathPtr = allocString(wasm, path);
          const txPtr = allocAndCopy(wasm, transaction);
          const sigPtr = wasm._hd_alloc(128);
          try {
            const result = signTransactionFn(this._handle, idPtr, pathPtr, txPtr, transaction.length, sigPtr, 128);
            checkResult(result);
            return readBytes(wasm, sigPtr, 64);
          } finally {
            wasm._hd_dealloc(idPtr);
            wasm._hd_dealloc(pathPtr);
            wasm._hd_dealloc(txPtr);
            wasm._hd_dealloc(sigPtr);
          }
        },

        signMessage(walletId, path, message) {
          const signMessageFn = requireWasmFunction(wasm, '_hd_keyring_sign_message', ErrorCode.NOT_SUPPORTED, 'Keyring is not available in the WASM build');
          const idPtr = allocString(wasm, walletId);
          const pathPtr = allocString(wasm, path);
          const msgPtr = allocAndCopy(wasm, message);
          const sigPtr = wasm._hd_alloc(128);
          try {
            const result = signMessageFn(this._handle, idPtr, pathPtr, msgPtr, message.length, sigPtr, 128);
            checkResult(result);
            return readBytes(wasm, sigPtr, 64);
          } finally {
            wasm._hd_dealloc(idPtr);
            wasm._hd_dealloc(pathPtr);
            wasm._hd_dealloc(msgPtr);
            wasm._hd_dealloc(sigPtr);
          }
        },

        destroy() {
          if (this._handle) {
            const destroyFn = getWasmFunction(wasm, '_hd_keyring_destroy');
            if (destroyFn) {
              destroyFn(this._handle);
            }
            this._handle = null;
          }
        }
      };
    }
  };

  // ==========================================================================
  // Utils API
  // ==========================================================================

  /**
   * Utility functions
   * @type {Object}
   */
  const utils = {
    // Hashing
    sha256(data) {
      const dataPtr = allocAndCopy(wasm, data);
      const hashPtr = wasm._hd_alloc(32);
      try {
        const result = wasm._hd_hash_sha256(dataPtr, data.length, hashPtr, 32);
        if (result < 0) throw new HDWalletError(result);
        return readBytes(wasm, hashPtr, 32);
      } finally {
        wasm._hd_dealloc(dataPtr);
        wasm._hd_dealloc(hashPtr);
      }
    },

    sha512(data) {
      const dataPtr = allocAndCopy(wasm, data);
      const hashPtr = wasm._hd_alloc(64);
      try {
        const result = wasm._hd_hash_sha512(dataPtr, data.length, hashPtr, 64);
        if (result < 0) throw new HDWalletError(result);
        return readBytes(wasm, hashPtr, 64);
      } finally {
        wasm._hd_dealloc(dataPtr);
        wasm._hd_dealloc(hashPtr);
      }
    },

    keccak256(data) {
      const dataPtr = allocAndCopy(wasm, data);
      const hashPtr = wasm._hd_alloc(32);
      try {
        const result = wasm._hd_hash_keccak256(dataPtr, data.length, hashPtr, 32);
        if (result < 0) throw new HDWalletError(result);
        return readBytes(wasm, hashPtr, 32);
      } finally {
        wasm._hd_dealloc(dataPtr);
        wasm._hd_dealloc(hashPtr);
      }
    },

    ripemd160(data) {
      const dataPtr = allocAndCopy(wasm, data);
      const hashPtr = wasm._hd_alloc(20);
      try {
        const result = wasm._hd_hash_ripemd160(dataPtr, data.length, hashPtr, 20);
        if (result < 0) throw new HDWalletError(result);
        return readBytes(wasm, hashPtr, 20);
      } finally {
        wasm._hd_dealloc(dataPtr);
        wasm._hd_dealloc(hashPtr);
      }
    },

    hash160(data) {
      const dataPtr = allocAndCopy(wasm, data);
      const hashPtr = wasm._hd_alloc(20);
      try {
        const result = wasm._hd_hash_hash160(dataPtr, data.length, hashPtr, 20);
        if (result < 0) throw new HDWalletError(result);
        return readBytes(wasm, hashPtr, 20);
      } finally {
        wasm._hd_dealloc(dataPtr);
        wasm._hd_dealloc(hashPtr);
      }
    },

    blake2b(data, outputLength = 32) {
      const dataPtr = allocAndCopy(wasm, data);
      const hashPtr = wasm._hd_alloc(64);
      try {
        const result = wasm._hd_hash_blake2b(dataPtr, data.length, hashPtr, 64, outputLength);
        if (result < 0) throw new HDWalletError(result);
        return readBytes(wasm, hashPtr, outputLength);
      } finally {
        wasm._hd_dealloc(dataPtr);
        wasm._hd_dealloc(hashPtr);
      }
    },

    blake2s(data, outputLength = 32) {
      const dataPtr = allocAndCopy(wasm, data);
      const hashPtr = wasm._hd_alloc(32);
      try {
        const result = wasm._hd_hash_blake2s(dataPtr, data.length, hashPtr, 32, outputLength);
        if (result < 0) throw new HDWalletError(result);
        return readBytes(wasm, hashPtr, outputLength);
      } finally {
        wasm._hd_dealloc(dataPtr);
        wasm._hd_dealloc(hashPtr);
      }
    },

    // Key derivation (sync - uses Crypto++ in WASM)
    hkdf(ikm, salt, info, length) {
      const ikmPtr = allocAndCopy(wasm, ikm);
      const saltPtr = allocAndCopy(wasm, salt);
      const infoPtr = allocAndCopy(wasm, info);
      const outPtr = wasm._hd_alloc(length);
      try {
        const result = wasm._hd_kdf_hkdf(ikmPtr, ikm.length, saltPtr, salt.length, infoPtr, info.length, outPtr, length);
        if (result < 0) throw new HDWalletError(result);
        return readBytes(wasm, outPtr, length);
      } finally {
        wasm._hd_secure_wipe(ikmPtr, ikm.length);
        wasm._hd_dealloc(ikmPtr);
        wasm._hd_dealloc(saltPtr);
        wasm._hd_dealloc(infoPtr);
        wasm._hd_dealloc(outPtr);
      }
    },

    // AES-GCM encryption/decryption (uses WASM/Crypto++ or OpenSSL)
    aesGcm: {
      /**
       * Encrypt data with AES-GCM (synchronous WASM)
       * @param {Uint8Array} key - AES-256 key (32 bytes)
       * @param {Uint8Array} plaintext - Data to encrypt
       * @param {Uint8Array} iv - Initialization vector (12 bytes)
       * @param {Uint8Array} [aad] - Additional authenticated data
       * @returns {{ciphertext: Uint8Array, tag: Uint8Array}}
       */
      encrypt(key, plaintext, iv, aad = new Uint8Array(0)) {
        if (key.length !== 32) throw new HDWalletError(ErrorCode.INVALID_ARGUMENT, 'Key must be 32 bytes');
        if (iv.length !== 12) throw new HDWalletError(ErrorCode.INVALID_ARGUMENT, 'IV must be 12 bytes');

        const keyPtr = allocAndCopy(wasm, key);
        const ptPtr = allocAndCopy(wasm, plaintext);
        const ivPtr = allocAndCopy(wasm, iv);
        const aadPtr = aad.length > 0 ? allocAndCopy(wasm, aad) : 0;
        const ctPtr = wasm._hd_alloc(plaintext.length);
        const tagPtr = wasm._hd_alloc(16);

        try {
          const result = wasm._hd_aes_gcm_encrypt(
            keyPtr, key.length,
            ptPtr, plaintext.length,
            ivPtr, iv.length,
            aadPtr, aad.length,
            ctPtr, tagPtr
          );
          if (result < 0) throw new HDWalletError(-result);

          return {
            ciphertext: readBytes(wasm, ctPtr, plaintext.length),
            tag: readBytes(wasm, tagPtr, 16)
          };
        } finally {
          wasm._hd_secure_wipe(keyPtr, key.length);
          wasm._hd_dealloc(keyPtr);
          wasm._hd_dealloc(ptPtr);
          wasm._hd_dealloc(ivPtr);
          if (aadPtr) wasm._hd_dealloc(aadPtr);
          wasm._hd_dealloc(ctPtr);
          wasm._hd_dealloc(tagPtr);
        }
      },

      /**
       * Decrypt data with AES-GCM (synchronous WASM)
       * @param {Uint8Array} key - AES-256 key (32 bytes)
       * @param {Uint8Array} ciphertext - Encrypted data
       * @param {Uint8Array} tag - Authentication tag (16 bytes)
       * @param {Uint8Array} iv - Initialization vector (12 bytes)
       * @param {Uint8Array} [aad] - Additional authenticated data
       * @returns {Uint8Array} Decrypted plaintext
       * @throws {HDWalletError} If authentication fails
       */
      decrypt(key, ciphertext, tag, iv, aad = new Uint8Array(0)) {
        if (key.length !== 32) throw new HDWalletError(ErrorCode.INVALID_ARGUMENT, 'Key must be 32 bytes');
        if (iv.length !== 12) throw new HDWalletError(ErrorCode.INVALID_ARGUMENT, 'IV must be 12 bytes');
        if (tag.length !== 16) throw new HDWalletError(ErrorCode.INVALID_ARGUMENT, 'Tag must be 16 bytes');

        const keyPtr = allocAndCopy(wasm, key);
        const ctPtr = allocAndCopy(wasm, ciphertext);
        const ivPtr = allocAndCopy(wasm, iv);
        const tagPtr = allocAndCopy(wasm, tag);
        const aadPtr = aad.length > 0 ? allocAndCopy(wasm, aad) : 0;
        const ptPtr = wasm._hd_alloc(ciphertext.length);

        try {
          const result = wasm._hd_aes_gcm_decrypt(
            keyPtr, key.length,
            ctPtr, ciphertext.length,
            ivPtr, iv.length,
            aadPtr, aad.length,
            tagPtr, ptPtr
          );
          if (result < 0) {
            throw new HDWalletError(
              result === -2 ? ErrorCode.VERIFICATION_FAILED : -result,
              result === -2 ? 'AES-GCM authentication failed' : undefined
            );
          }

          return readBytes(wasm, ptPtr, ciphertext.length);
        } finally {
          wasm._hd_secure_wipe(keyPtr, key.length);
          wasm._hd_dealloc(keyPtr);
          wasm._hd_dealloc(ctPtr);
          wasm._hd_dealloc(ivPtr);
          wasm._hd_dealloc(tagPtr);
          if (aadPtr) wasm._hd_dealloc(aadPtr);
          wasm._hd_dealloc(ptPtr);
        }
      }
    },

    /** Generate cryptographically secure random bytes */
    getRandomBytes(length) {
      const bytes = new Uint8Array(length);
      if (typeof globalThis.crypto !== 'undefined' && globalThis.crypto.getRandomValues) {
        globalThis.crypto.getRandomValues(bytes);
      } else {
        throw new Error('No cryptographic random source available');
      }
      return bytes;
    },

    /** Generate a random IV for AES-GCM (12 bytes) */
    generateIv() {
      return this.getRandomBytes(12);
    },

    /** Generate a random AES key (default 256 bits) */
    generateAesKey(bits = 256) {
      if (![128, 192, 256].includes(bits)) {
        throw new Error('AES key size must be 128, 192, or 256 bits');
      }
      return this.getRandomBytes(bits / 8);
    },

    pbkdf2(password, salt, iterations, length) {
      const pwdPtr = allocAndCopy(wasm, password);
      const saltPtr = allocAndCopy(wasm, salt);
      const outPtr = wasm._hd_alloc(length);
      try {
        const result = wasm._hd_kdf_pbkdf2(pwdPtr, password.length, saltPtr, salt.length, iterations, outPtr, length);
        if (result < 0) throw new HDWalletError(result);
        return readBytes(wasm, outPtr, length);
      } finally {
        wasm._hd_secure_wipe(pwdPtr, password.length);
        wasm._hd_dealloc(pwdPtr);
        wasm._hd_dealloc(saltPtr);
        wasm._hd_dealloc(outPtr);
      }
    },

    scrypt(password, salt, n, r, p, length) {
      const pwdPtr = allocAndCopy(wasm, password);
      const saltPtr = allocAndCopy(wasm, salt);
      const outPtr = wasm._hd_alloc(length);
      try {
        const result = wasm._hd_kdf_scrypt(pwdPtr, password.length, saltPtr, salt.length, BigInt(n), r, p, outPtr, length);
        if (result < 0) throw new HDWalletError(result);
        return readBytes(wasm, outPtr, length);
      } finally {
        wasm._hd_secure_wipe(pwdPtr, password.length);
        wasm._hd_dealloc(pwdPtr);
        wasm._hd_dealloc(saltPtr);
        wasm._hd_dealloc(outPtr);
      }
    },

    // Encoding
    encodeBase58(data) {
      const encodeBase58Fn = getWasmFunction(wasm, '_hd_encode_base58');
      if (!encodeBase58Fn) {
        return encodeBase58Fallback(data);
      }
      const dataPtr = allocAndCopy(wasm, data);
      try {
        const ptr = encodeBase58Fn(dataPtr, data.length);
        return readString(wasm, ptr);
      } finally {
        wasm._hd_dealloc(dataPtr);
      }
    },

    decodeBase58(str) {
      const decodeBase58Fn = getWasmFunction(wasm, '_hd_decode_base58');
      if (!decodeBase58Fn) {
        return decodeBase58Fallback(str);
      }
      const strPtr = allocString(wasm, str);
      const outPtr = wasm._hd_alloc(256);
      const sizePtr = wasm._hd_alloc(4);
      try {
        wasm.setValue(sizePtr, 256, 'i32');
        const result = decodeBase58Fn(strPtr, outPtr, sizePtr);
        checkResult(result);
        const size = wasm.getValue(sizePtr, 'i32');
        return readBytes(wasm, outPtr, size);
      } finally {
        wasm._hd_dealloc(strPtr);
        wasm._hd_dealloc(outPtr);
        wasm._hd_dealloc(sizePtr);
      }
    },

    encodeBase58Check(data) {
      const encodeBase58CheckFn = getWasmFunction(wasm, '_hd_encode_base58check');
      if (!encodeBase58CheckFn) {
        const checksum = this.sha256(this.sha256(data)).slice(0, 4);
        const payload = new Uint8Array(data.length + checksum.length);
        payload.set(data, 0);
        payload.set(checksum, data.length);
        return encodeBase58Fallback(payload);
      }
      const dataPtr = allocAndCopy(wasm, data);
      try {
        const ptr = encodeBase58CheckFn(dataPtr, data.length);
        return readString(wasm, ptr);
      } finally {
        wasm._hd_dealloc(dataPtr);
      }
    },

    decodeBase58Check(str) {
      const decodeBase58CheckFn = getWasmFunction(wasm, '_hd_decode_base58check');
      if (!decodeBase58CheckFn) {
        const decoded = decodeBase58Fallback(str);
        if (decoded.length < 4) {
          throw new HDWalletError(ErrorCode.INVALID_ARGUMENT, 'Invalid Base58Check payload');
        }
        const payload = decoded.slice(0, decoded.length - 4);
        const checksum = decoded.slice(decoded.length - 4);
        const expected = this.sha256(this.sha256(payload)).slice(0, 4);
        for (let i = 0; i < 4; i++) {
          if (checksum[i] !== expected[i]) {
            throw new HDWalletError(ErrorCode.INVALID_CHECKSUM);
          }
        }
        return payload;
      }
      const strPtr = allocString(wasm, str);
      const outPtr = wasm._hd_alloc(256);
      const sizePtr = wasm._hd_alloc(4);
      try {
        wasm.setValue(sizePtr, 256, 'i32');
        const result = decodeBase58CheckFn(strPtr, outPtr, sizePtr);
        checkResult(result);
        const size = wasm.getValue(sizePtr, 'i32');
        return readBytes(wasm, outPtr, size);
      } finally {
        wasm._hd_dealloc(strPtr);
        wasm._hd_dealloc(outPtr);
        wasm._hd_dealloc(sizePtr);
      }
    },

    encodeBech32(hrp, data) {
      const encodeBech32Fn = getWasmFunction(wasm, '_hd_encode_bech32');
      if (!encodeBech32Fn) {
        return encodeBech32Fallback(hrp, data);
      }
      const hrpPtr = allocString(wasm, hrp);
      const dataPtr = allocAndCopy(wasm, data);
      try {
        const ptr = encodeBech32Fn(hrpPtr, dataPtr, data.length);
        return readString(wasm, ptr);
      } finally {
        wasm._hd_dealloc(hrpPtr);
        wasm._hd_dealloc(dataPtr);
      }
    },

    decodeBech32(str) {
      const decodeBech32Fn = getWasmFunction(wasm, '_hd_decode_bech32');
      if (!decodeBech32Fn) {
        return decodeBech32Fallback(str);
      }
      const strPtr = allocString(wasm, str);
      const hrpPtr = wasm._hd_alloc(128);
      const dataPtr = wasm._hd_alloc(256);
      const sizePtr = wasm._hd_alloc(4);
      try {
        wasm.setValue(sizePtr, 256, 'i32');
        const result = decodeBech32Fn(strPtr, hrpPtr, 128, dataPtr, sizePtr);
        checkResult(result);
        const size = wasm.getValue(sizePtr, 'i32');
        return {
          hrp: readString(wasm, hrpPtr),
          data: readBytes(wasm, dataPtr, size)
        };
      } finally {
        wasm._hd_dealloc(strPtr);
        wasm._hd_dealloc(hrpPtr);
        wasm._hd_dealloc(dataPtr);
        wasm._hd_dealloc(sizePtr);
      }
    },

    encodeHex(data) {
      return encodeHexFallback(data);
    },

    decodeHex(str) {
      return decodeHexFallback(str);
    },

    encodeBase64(data) {
      return encodeBase64Fallback(data);
    },

    decodeBase64(str) {
      return decodeBase64Fallback(str);
    },

    // Memory
    secureWipe(data) {
      if (data instanceof Uint8Array) {
        // Wipe in-place
        for (let i = 0; i < data.length; i++) {
          data[i] = 0;
        }
      }
    }
  };

  // ==========================================================================
  // X.509 API
  // ==========================================================================

  /**
   * OpenSSL-backed X.509 certificate API
   * @type {Object}
   */
  const x509 = {
    isAvailable() {
      return typeof wasm._hd_x509_generate_private_key === 'function' &&
        typeof wasm._hd_openssl_init_fips === 'function';
    },

    generatePrivateKey(curve) {
      const fn = requireWasmFunction(wasm, '_hd_x509_generate_private_key');
      return callDynamicBytesResult(
        wasm,
        (outPtr, outLenPtr) => fn(curve, outPtr, outLenPtr),
        { wipeOutput: true }
      );
    },

    exportPrivateKeyPem(curve, privateKey) {
      const fn = requireWasmFunction(wasm, '_hd_x509_export_private_key_pem');
      const keyBytes = toUint8Array(privateKey, 'privateKey');
      const keyPtr = allocAndCopy(wasm, keyBytes);
      try {
        return callDynamicStringResult(
          wasm,
          (outPtr, outLenPtr) => fn(curve, keyPtr, keyBytes.length, outPtr, outLenPtr),
          { wipeOutput: true }
        );
      } finally {
        wasm._hd_secure_wipe(keyPtr, keyBytes.length);
        wasm._hd_dealloc(keyPtr);
      }
    },

    importPrivateKeyPem(curve, pem) {
      const fn = requireWasmFunction(wasm, '_hd_x509_import_private_key_pem');
      const pemPtr = allocString(wasm, pem);
      const pemLen = wasm.lengthBytesUTF8(pem) + 1;
      try {
        return callDynamicBytesResult(
          wasm,
          (outPtr, outLenPtr) => fn(curve, pemPtr, outPtr, outLenPtr),
          { wipeOutput: true }
        );
      } finally {
        wasm._hd_secure_wipe(pemPtr, pemLen);
        wasm._hd_dealloc(pemPtr);
      }
    },

    createSelfSignedCertificate(options, certificateCurve, certificatePrivateKey, outputEncoding = X509Encoding.PEM) {
      const fn = requireWasmFunction(wasm, '_hd_x509_create_self_signed');
      const optionsSpec = serializeX509Options(options);
      const optionsPtr = allocString(wasm, optionsSpec);
      const optionsLen = wasm.lengthBytesUTF8(optionsSpec) + 1;
      const keyBytes = toUint8Array(certificatePrivateKey, 'certificatePrivateKey');
      const keyPtr = allocAndCopy(wasm, keyBytes);

      try {
        const bytes = callDynamicBytesResult(
          wasm,
          (outPtr, outLenPtr) => fn(
            optionsPtr,
            certificateCurve,
            keyPtr,
            keyBytes.length,
            outputEncoding,
            outPtr,
            outLenPtr
          )
        );
        return decodeX509CertificateOutput(bytes, outputEncoding);
      } finally {
        wasm._hd_secure_wipe(optionsPtr, optionsLen);
        wasm._hd_secure_wipe(keyPtr, keyBytes.length);
        wasm._hd_dealloc(optionsPtr);
        wasm._hd_dealloc(keyPtr);
      }
    },

    issueCertificate(
      options,
      issuerCurve,
      issuerPrivateKey,
      issuerCertificate,
      issuerCertificateEncoding,
      subjectCurve,
      subjectPrivateKey,
      outputEncoding = X509Encoding.PEM
    ) {
      assertX509CertificateEncoding(issuerCertificateEncoding, 'issuerCertificateEncoding');
      const fn = requireWasmFunction(wasm, '_hd_x509_issue_certificate');
      const optionsSpec = serializeX509Options(options);
      const optionsPtr = allocString(wasm, optionsSpec);
      const optionsLen = wasm.lengthBytesUTF8(optionsSpec) + 1;
      const issuerKeyBytes = toUint8Array(issuerPrivateKey, 'issuerPrivateKey');
      const issuerKeyPtr = allocAndCopy(wasm, issuerKeyBytes);
      const issuerCertBytes = normalizeX509CertificateInput(
        issuerCertificate,
        issuerCertificateEncoding,
        'issuerCertificate'
      );
      const issuerCertPtr = allocAndCopy(wasm, issuerCertBytes);
      const subjectKeyBytes = toUint8Array(subjectPrivateKey, 'subjectPrivateKey');
      const subjectKeyPtr = allocAndCopy(wasm, subjectKeyBytes);

      try {
        const bytes = callDynamicBytesResult(
          wasm,
          (outPtr, outLenPtr) => fn(
            optionsPtr,
            issuerCurve,
            issuerKeyPtr,
            issuerKeyBytes.length,
            issuerCertPtr,
            issuerCertBytes.length,
            issuerCertificateEncoding,
            subjectCurve,
            subjectKeyPtr,
            subjectKeyBytes.length,
            outputEncoding,
            outPtr,
            outLenPtr
          )
        );
        return decodeX509CertificateOutput(bytes, outputEncoding);
      } finally {
        wasm._hd_secure_wipe(optionsPtr, optionsLen);
        wasm._hd_secure_wipe(issuerKeyPtr, issuerKeyBytes.length);
        wasm._hd_secure_wipe(subjectKeyPtr, subjectKeyBytes.length);
        wasm._hd_dealloc(optionsPtr);
        wasm._hd_dealloc(issuerKeyPtr);
        wasm._hd_dealloc(issuerCertPtr);
        wasm._hd_dealloc(subjectKeyPtr);
      }
    },

    convertCertificate(certificate, inputEncoding, outputEncoding) {
      assertX509CertificateEncoding(inputEncoding, 'inputEncoding');
      assertX509CertificateEncoding(outputEncoding, 'outputEncoding');
      const fn = requireWasmFunction(wasm, '_hd_x509_convert_certificate');
      const certBytes = normalizeX509CertificateInput(certificate, inputEncoding);
      const certPtr = allocAndCopy(wasm, certBytes);

      try {
        const bytes = callDynamicBytesResult(
          wasm,
          (outPtr, outLenPtr) => fn(
            certPtr,
            certBytes.length,
            inputEncoding,
            outputEncoding,
            outPtr,
            outLenPtr
          )
        );
        return decodeX509CertificateOutput(bytes, outputEncoding);
      } finally {
        wasm._hd_dealloc(certPtr);
      }
    },

    parseCertificate(certificate, inputEncoding = X509Encoding.PEM) {
      assertX509CertificateEncoding(inputEncoding, 'inputEncoding');
      const fn = requireWasmFunction(wasm, '_hd_x509_parse_certificate_json');
      const certBytes = normalizeX509CertificateInput(certificate, inputEncoding);
      const certPtr = allocAndCopy(wasm, certBytes);

      try {
        return normalizeParsedCertificateJson(JSON.parse(
          callDynamicStringResult(
            wasm,
            (outPtr, outLenPtr) => fn(certPtr, certBytes.length, inputEncoding, outPtr, outLenPtr)
          )
        ));
      } finally {
        wasm._hd_dealloc(certPtr);
      }
    },

    verifyWalletAttestation(certificate, inputEncoding = X509Encoding.PEM) {
      assertX509CertificateEncoding(inputEncoding, 'inputEncoding');
      const fn = requireWasmFunction(wasm, '_hd_x509_verify_wallet_attestation');
      const certBytes = normalizeX509CertificateInput(certificate, inputEncoding);
      const certPtr = allocAndCopy(wasm, certBytes);

      try {
        const result = fn(certPtr, certBytes.length, inputEncoding);
        if (result < 0) {
          throw new HDWalletError(result);
        }
        return result === 1;
      } finally {
        wasm._hd_dealloc(certPtr);
      }
    },

    exportPkcs12(
      certificate,
      certificateEncoding,
      privateKeyCurve,
      privateKey,
      password,
      friendlyName = '',
      chainPem = ''
    ) {
      assertX509CertificateEncoding(certificateEncoding, 'certificateEncoding');
      const fn = requireWasmFunction(wasm, '_hd_x509_export_pkcs12');
      const certBytes = normalizeX509CertificateInput(certificate, certificateEncoding);
      const certPtr = allocAndCopy(wasm, certBytes);
      const keyBytes = toUint8Array(privateKey, 'privateKey');
      const keyPtr = allocAndCopy(wasm, keyBytes);
      const passwordPtr = allocString(wasm, password);
      const passwordLen = wasm.lengthBytesUTF8(password) + 1;
      const friendlyNamePtr = allocString(wasm, friendlyName);
      const chainBytes = normalizePemInput(chainPem, 'chainPem');
      const chainPtr = chainBytes.length > 0 ? allocAndCopy(wasm, chainBytes) : 0;

      try {
        return callDynamicBytesResult(
          wasm,
          (outPtr, outLenPtr) => fn(
            certPtr,
            certBytes.length,
            certificateEncoding,
            privateKeyCurve,
            keyPtr,
            keyBytes.length,
            passwordPtr,
            friendlyNamePtr,
            chainPtr,
            chainBytes.length,
            outPtr,
            outLenPtr
          ),
          { wipeOutput: true }
        );
      } finally {
        wasm._hd_secure_wipe(keyPtr, keyBytes.length);
        wasm._hd_secure_wipe(passwordPtr, passwordLen);
        wasm._hd_dealloc(certPtr);
        wasm._hd_dealloc(keyPtr);
        wasm._hd_dealloc(passwordPtr);
        wasm._hd_dealloc(friendlyNamePtr);
        if (chainPtr) {
          wasm._hd_dealloc(chainPtr);
        }
      }
    },

    importPkcs12(pkcs12Bundle, password) {
      const fn = requireWasmFunction(wasm, '_hd_x509_import_pkcs12_json');
      const bundleBytes = toUint8Array(pkcs12Bundle, 'pkcs12Bundle');
      const bundlePtr = allocAndCopy(wasm, bundleBytes);
      const passwordPtr = allocString(wasm, password);
      const passwordLen = wasm.lengthBytesUTF8(password) + 1;

      try {
        return normalizePkcs12ImportJson(JSON.parse(
          callDynamicStringResult(
            wasm,
            (outPtr, outLenPtr) => fn(bundlePtr, bundleBytes.length, passwordPtr, outPtr, outLenPtr),
            { wipeOutput: true }
          )
        ));
      } finally {
        wasm._hd_secure_wipe(bundlePtr, bundleBytes.length);
        wasm._hd_secure_wipe(passwordPtr, passwordLen);
        wasm._hd_dealloc(bundlePtr);
        wasm._hd_dealloc(passwordPtr);
      }
    }
  };

  // ==========================================================================
  // ECIES API
  // ==========================================================================

  const CURVE_NAME_MAP = { secp256k1: 0, ed25519: 1, p256: 2, p384: 3, x25519: 4 };

  const ecies = {
    encrypt(curve, recipientPublicKey, plaintext, aad = new Uint8Array(0)) {
      const curveId = typeof curve === 'number' ? curve : (CURVE_NAME_MAP[curve] ?? 0);
      const encryptFn = requireWasmFunction(wasm, '_hd_ecies_encrypt');
      const overheadFn = requireWasmFunction(wasm, '_hd_ecies_overhead');
      const overhead = overheadFn(curveId);
      const outSize = plaintext.length + overhead;
      const pubPtr = allocAndCopy(wasm, recipientPublicKey);
      const ptPtr = allocAndCopy(wasm, plaintext);
      const aadPtr = aad.length > 0 ? allocAndCopy(wasm, aad) : 0;
      const outPtr = wasm._hd_alloc(outSize);
      try {
        const result = encryptFn(curveId, pubPtr, recipientPublicKey.length,
          ptPtr, plaintext.length, aadPtr, aad.length, outPtr, outSize);
        if (result < 0) throw new HDWalletError(-result);
        return readBytes(wasm, outPtr, result);
      } finally {
        wasm._hd_dealloc(pubPtr);
        wasm._hd_dealloc(ptPtr);
        if (aadPtr) wasm._hd_dealloc(aadPtr);
        wasm._hd_dealloc(outPtr);
      }
    },

    decrypt(curve, recipientPrivateKey, ciphertext, aad = new Uint8Array(0)) {
      const curveId = typeof curve === 'number' ? curve : (CURVE_NAME_MAP[curve] ?? 0);
      const decryptFn = requireWasmFunction(wasm, '_hd_ecies_decrypt');
      const privPtr = allocAndCopy(wasm, recipientPrivateKey);
      const ctPtr = allocAndCopy(wasm, ciphertext);
      const aadPtr = aad.length > 0 ? allocAndCopy(wasm, aad) : 0;
      const outPtr = wasm._hd_alloc(ciphertext.length);
      try {
        const result = decryptFn(curveId, privPtr, recipientPrivateKey.length,
          ctPtr, ciphertext.length, aadPtr, aad.length, outPtr, ciphertext.length);
        if (result < 0) throw new HDWalletError(-result);
        return readBytes(wasm, outPtr, result);
      } finally {
        wasm._hd_secure_wipe(privPtr, recipientPrivateKey.length);
        wasm._hd_dealloc(privPtr);
        wasm._hd_dealloc(ctPtr);
        if (aadPtr) wasm._hd_dealloc(aadPtr);
        wasm._hd_dealloc(outPtr);
      }
    }
  };

  // ==========================================================================
  // AES-CTR API
  // ==========================================================================

  const aesCtr = {
    encrypt(key, plaintext, iv) {
      const encryptFn = requireWasmFunction(wasm, '_hd_aes_ctr_encrypt');
      const keyPtr = allocAndCopy(wasm, key);
      const ptPtr = allocAndCopy(wasm, plaintext);
      const ivPtr = allocAndCopy(wasm, iv);
      const outPtr = wasm._hd_alloc(plaintext.length);
      try {
        const result = encryptFn(keyPtr, key.length, ptPtr, plaintext.length, ivPtr, iv.length, outPtr, plaintext.length);
        if (result < 0) throw new HDWalletError(-result);
        return readBytes(wasm, outPtr, plaintext.length);
      } finally {
        wasm._hd_secure_wipe(keyPtr, key.length);
        wasm._hd_dealloc(keyPtr);
        wasm._hd_dealloc(ptPtr);
        wasm._hd_dealloc(ivPtr);
        wasm._hd_dealloc(outPtr);
      }
    },

    decrypt(key, ciphertext, iv) {
      const decryptFn = requireWasmFunction(wasm, '_hd_aes_ctr_decrypt');
      const keyPtr = allocAndCopy(wasm, key);
      const ctPtr = allocAndCopy(wasm, ciphertext);
      const ivPtr = allocAndCopy(wasm, iv);
      const outPtr = wasm._hd_alloc(ciphertext.length);
      try {
        const result = decryptFn(keyPtr, key.length, ctPtr, ciphertext.length, ivPtr, iv.length, outPtr, ciphertext.length);
        if (result < 0) throw new HDWalletError(-result);
        return readBytes(wasm, outPtr, ciphertext.length);
      } finally {
        wasm._hd_secure_wipe(keyPtr, key.length);
        wasm._hd_dealloc(keyPtr);
        wasm._hd_dealloc(ctPtr);
        wasm._hd_dealloc(ivPtr);
        wasm._hd_dealloc(outPtr);
      }
    }
  };

  // ==========================================================================
  // SLIP-10 API
  // ==========================================================================

  const slip10 = {
    deriveEd25519Path(seed, path) {
      const deriveFn = requireWasmFunction(wasm, '_hd_slip10_ed25519_derive_path');
      const seedPtr = allocAndCopy(wasm, seed);
      const pathPtr = allocString(wasm, path);
      const keyPtr = wasm._hd_alloc(32);
      const chainCodePtr = wasm._hd_alloc(32);
      try {
        const result = deriveFn(seedPtr, seed.length, pathPtr, keyPtr, chainCodePtr);
        checkResult(result);
        return {
          privateKey: readBytes(wasm, keyPtr, 32),
          chainCode: readBytes(wasm, chainCodePtr, 32)
        };
      } finally {
        wasm._hd_secure_wipe(seedPtr, seed.length);
        wasm._hd_secure_wipe(keyPtr, 32);
        wasm._hd_dealloc(seedPtr);
        wasm._hd_dealloc(pathPtr);
        wasm._hd_dealloc(keyPtr);
        wasm._hd_dealloc(chainCodePtr);
      }
    }
  };

  // ==========================================================================
  // libp2p PeerID / IPNS API
  // ==========================================================================

  const libp2p = {
    /**
     * Compute libp2p peerID (raw multihash bytes) from a public key
     * @param {Uint8Array} publicKey - Compressed pubkey (secp256k1/P-256) or raw 32-byte (ed25519)
     * @param {number} curve - Curve enum value
     * @returns {Uint8Array} PeerID as raw multihash bytes
     */
    peerIdFromPublicKey(publicKey, curve) {
      const pubPtr = allocAndCopy(wasm, publicKey);
      const outPtr = wasm._hd_alloc(64);
      try {
        const result = wasm._hd_libp2p_peer_id(pubPtr, publicKey.length, curve, outPtr, 64);
        if (result < 0) throw new HDWalletError(result);
        return readBytes(wasm, outPtr, result);
      } finally {
        wasm._hd_dealloc(pubPtr);
        wasm._hd_dealloc(outPtr);
      }
    },

    /**
     * Encode peerID bytes as base58btc string
     * @param {Uint8Array} peerIdBytes - Raw multihash bytes
     * @returns {string}
     */
    peerIdToString(peerIdBytes) {
      return utils.encodeBase58(peerIdBytes);
    },

    /**
     * Compute IPNS hash (CIDv1 base36lower with 'k' prefix)
     * @param {Uint8Array} peerIdBytes - Raw peerID multihash bytes
     * @returns {string}
     */
    ipnsHash(peerIdBytes) {
      // Use the WASM function via a dummy key + curve? No — we have raw peerID bytes.
      // For raw bytes, build CID and encode in JS using the WASM base36 encoder.
      // However, peerIdBytes came from peerIdFromPublicKey which already used WASM.
      // We could also re-derive from pubkey, but for the namespace API we accept
      // pre-computed peerID bytes and just do the CID wrapping + encoding here.
      // We still use WASM for base36 encoding.
      const dataPtr = allocAndCopy(wasm, peerIdBytes);
      const outPtr = wasm._hd_alloc(128);
      // Build CID in JS (trivial: 2-byte prefix + multihash)
      const cid = new Uint8Array(2 + peerIdBytes.length);
      cid[0] = 0x01; // CIDv1
      cid[1] = 0x72; // libp2p-key codec
      cid.set(peerIdBytes, 2);
      const cidPtr = allocAndCopy(wasm, cid);
      try {
        const result = wasm._hd_encode_base36lower(cidPtr, cid.length, outPtr, 128);
        checkResult(result);
        return 'k' + readString(wasm, outPtr);
      } finally {
        wasm._hd_dealloc(dataPtr);
        wasm._hd_dealloc(cidPtr);
        wasm._hd_dealloc(outPtr);
      }
    },

    /**
     * Compute IPNS hash (CIDv1 base32lower with 'b' prefix)
     * @param {Uint8Array} peerIdBytes - Raw peerID multihash bytes
     * @returns {string}
     */
    ipnsHashBase32(peerIdBytes) {
      const cid = new Uint8Array(2 + peerIdBytes.length);
      cid[0] = 0x01; // CIDv1
      cid[1] = 0x72; // libp2p-key codec
      cid.set(peerIdBytes, 2);
      const cidPtr = allocAndCopy(wasm, cid);
      const outPtr = wasm._hd_alloc(128);
      try {
        const result = wasm._hd_encode_base32lower(cidPtr, cid.length, outPtr, 128);
        checkResult(result);
        return 'b' + readString(wasm, outPtr);
      } finally {
        wasm._hd_dealloc(cidPtr);
        wasm._hd_dealloc(outPtr);
      }
    },

    peerIdFromXpub(xpubStr) {
      const key = hdkey.fromXpub(xpubStr);
      try {
        return key.peerIdString();
      } finally {
        key.wipe();
      }
    },

    ipnsHashFromXpub(xpubStr) {
      const key = hdkey.fromXpub(xpubStr);
      try {
        return key.ipnsHash();
      } finally {
        key.wipe();
      }
    }
  };

  // ==========================================================================
  // Return the module API
  // ==========================================================================

  const module = {
    // Module info
    getVersion() {
      const ptr = wasm._hd_get_version_string();
      return readString(wasm, ptr);
    },

    hasCryptopp() {
      return wasm._hd_has_cryptopp() !== 0;
    },

    isFipsMode() {
      return wasm._hd_is_fips_mode() !== 0;
    },

    getSupportedCoins() {
      const getSupportedCoinsFn = getWasmFunction(wasm, '_hd_get_supported_coins');
      if (!getSupportedCoinsFn) {
        return ['Bitcoin', 'Ethereum', 'Cosmos', 'Solana', 'Polkadot'];
      }
      const ptr = getSupportedCoinsFn();
      try {
        return JSON.parse(readString(wasm, ptr));
      } catch (_) {
        return [];
      }
    },

    getSupportedCurves() {
      const getSupportedCurvesFn = getWasmFunction(wasm, '_hd_get_supported_curves');
      if (!getSupportedCurvesFn) {
        return ['secp256k1', 'ed25519', 'p256', 'p384', 'x25519'];
      }
      const ptr = getSupportedCurvesFn();
      try {
        return JSON.parse(readString(wasm, ptr));
      } catch (_) {
        return [];
      }
    },

    // WASI bridge
    wasiHasFeature(feature) {
      const hasFeatureFn = getWasmFunction(wasm, '_hd_wasi_has_feature');
      return hasFeatureFn ? hasFeatureFn(feature) !== 0 : false;
    },

    wasiGetWarning(feature) {
      const getWarningFn = getWasmFunction(wasm, '_hd_wasi_get_warning');
      return getWarningFn ? getWarningFn(feature) : WasiWarning.NOT_AVAILABLE_WASI;
    },

    wasiGetWarningMessage(feature) {
      return 'WASI warning API unavailable in this build';
    },

    // Entropy
    injectEntropy(entropy) {
      const entropyPtr = allocAndCopy(wasm, entropy);
      try {
        wasm._hd_inject_entropy(entropyPtr, entropy.length);
      } finally {
        wasm._hd_secure_wipe(entropyPtr, entropy.length);
        wasm._hd_dealloc(entropyPtr);
      }
    },

    getEntropyStatus() {
      return wasm._hd_get_entropy_status();
    },

    // OpenSSL FIPS support
    /**
     * Initialize OpenSSL in FIPS mode
     * @returns {boolean} True if FIPS mode activated, false if using default provider
     */
    initFips() {
      // Check if the function exists (only when built with HD_WALLET_USE_OPENSSL)
      if (typeof wasm._hd_openssl_init_fips !== 'function') {
        console.warn('[HD Wallet] OpenSSL not compiled in, skipping FIPS init');
        return false;
      }
      const result = wasm._hd_openssl_init_fips();
      if (result === 1) {
        console.log('[HD Wallet] OpenSSL FIPS mode activated');
        return true;
      } else if (result === -1) {
        console.warn('[HD Wallet] FIPS provider not available, using default OpenSSL');
        wasm._hd_openssl_init_default();
        return false;
      } else {
        console.error('[HD Wallet] Failed to initialize OpenSSL');
        return false;
      }
    },

    /**
     * Check if OpenSSL backend is being used
     * @returns {boolean}
     */
    isOpenSSL() {
      return typeof wasm._hd_openssl_init_fips === 'function';
    },

    /**
     * Check if running in FIPS mode (OpenSSL FIPS provider active)
     * @returns {boolean}
     */
    isOpenSSLFips() {
      if (typeof wasm._hd_openssl_is_fips !== 'function') {
        return false;
      }
      return wasm._hd_openssl_is_fips() !== 0;
    },

    // APIs
    mnemonic,
    hdkey,
    curves,
    coin,
    bitcoin,
    ethereum,
    cosmos,
    solana,
    polkadot,
    hardware,
    keyring,
    utils,
    x509,
    ecies,
    aesCtr,
    slip10,
    libp2p,

    // Key derivation helpers (convenience wrappers)
    buildSigningPath,
    buildEncryptionPath,
    getSigningKey,
    getEncryptionKey,

    // Aligned binary API for efficient batch operations
    get aligned() {
      if (!this._aligned) {
        // Create a wrapper that exposes wasmMemory for the aligned API
        const wasmWithMemory = Object.create(wasm);
        Object.defineProperty(wasmWithMemory, 'wasmMemory', {
          get: () => ({ buffer: wasm.HEAPU8.buffer })
        });
        this._aligned = new AlignedAPI(wasmWithMemory);
      }
      return this._aligned;
    }
  };

  Object.defineProperty(module, 'sdn', {
    value: createSdnTypedCapabilities(wasm),
    enumerable: true,
    writable: false,
    configurable: false,
  });
  installWalletOriginCapabilities(module);
  module.plugin = createSdnPluginContract({ wallet: module, wasm });
  return module;
}

// =============================================================================
// BIP-44 Derivation Path Helpers
// =============================================================================

/**
 * Build a BIP-44 signing key path: m/44'/coinType'/account'/0/index
 *
 * Uses change=0 (external chain) for signing keys per BIP-44 convention.
 *
 * @param {string|number} coinType - SLIP-44 coin type number
 * @param {string|number} [account='0'] - Account index
 * @param {string|number} [index='0'] - Address/key index
 * @returns {string} BIP-44 derivation path
 *
 * @example
 * buildSigningPath(60);          // "m/44'/60'/0'/0/0" (Ethereum)
 * buildSigningPath(0, '0', '1'); // "m/44'/0'/0'/0/1"  (Bitcoin, 2nd key)
 * buildSigningPath(501);         // "m/44'/501'/0'/0/0"  (Solana)
 */
export function buildSigningPath(coinType, account = '0', index = '0') {
  return `m/44'/${coinType}'/${account}'/0/${index}`;
}

/**
 * Build a BIP-44 encryption key path: m/44'/coinType'/account'/1/index
 *
 * Uses change=1 (internal chain) to derive encryption keys separate from
 * signing keys. This separation ensures that compromising an encryption key
 * does not compromise signing capability.
 *
 * @param {string|number} coinType - SLIP-44 coin type number
 * @param {string|number} [account='0'] - Account index
 * @param {string|number} [index='0'] - Address/key index
 * @returns {string} Derivation path for encryption keys
 *
 * @example
 * buildEncryptionPath(60);          // "m/44'/60'/0'/1/0" (Ethereum)
 * buildEncryptionPath(501, '0', '3');  // "m/44'/501'/0'/1/3"  (Solana, 4th plugin key)
 */
export function buildEncryptionPath(coinType, account = '0', index = '0') {
  return `m/44'/${coinType}'/${account}'/1/${index}`;
}

/**
 * Get the signing keypair for a wallet at a given BIP-44 path.
 *
 * Derives the key at m/44'/coinType'/account'/0/index and returns
 * both private and public key bytes.
 *
 * @param {HDKey} hdRoot - Master HD key (from hdkey.fromSeed())
 * @param {string|number} coinType - SLIP-44 coin type number
 * @param {string|number} [account='0'] - Account index
 * @param {string|number} [index='0'] - Address/key index
 * @returns {{ privateKey: Uint8Array, publicKey: Uint8Array, path: string }} Signing keypair
 *
 * @example
 * const wallet = await createHDWallet();
 * const seed = wallet.mnemonic.toSeed(mnemonic);
 * const root = wallet.hdkey.fromSeed(seed);
 * const { privateKey, publicKey, path } = getSigningKey(root, 60);
 * const sig = wallet.curves.secp256k1.sign(messageHash, privateKey);
 */
export function getSigningKey(hdRoot, coinType, account = '0', index = '0') {
  const path = buildSigningPath(coinType, account, index);
  const derived = hdRoot.derivePath(path);
  try {
    return {
      privateKey: derived.privateKey(),
      publicKey: derived.publicKey(),
      path,
    };
  } finally {
    derived.wipe();
  }
}

/**
 * Get the encryption keypair for a wallet at a given BIP-44 path.
 *
 * Derives the key at m/44'/coinType'/account'/1/index and returns
 * both private and public key bytes. Use this for ECDH key agreement,
 * ECIES encryption, or any asymmetric encryption operation.
 *
 * @param {HDKey} hdRoot - Master HD key (from hdkey.fromSeed())
 * @param {string|number} coinType - SLIP-44 coin type number
 * @param {string|number} [account='0'] - Account index
 * @param {string|number} [index='0'] - Address/key index
 * @returns {{ privateKey: Uint8Array, publicKey: Uint8Array, path: string }} Encryption keypair
 *
 * @example
 * const wallet = await createHDWallet();
 * const seed = wallet.mnemonic.toSeed(mnemonic);
 * const root = wallet.hdkey.fromSeed(seed);
 * const { privateKey, publicKey } = getEncryptionKey(root, 0, '0', '0');
 * // Use publicKey for ECIES encryption, privateKey for decryption
 * const shared = wallet.curves.secp256k1.ecdh(privateKey, otherPublicKey);
 */
export function getEncryptionKey(hdRoot, coinType, account = '0', index = '0') {
  const path = buildEncryptionPath(coinType, account, index);
  const derived = hdRoot.derivePath(path);
  try {
    return {
      privateKey: derived.privateKey(),
      publicKey: derived.publicKey(),
      path,
    };
  } finally {
    derived.wipe();
  }
}

/**
 * Well-known coin types for common use cases
 * @readonly
 * @enum {number}
 */
export const WellKnownCoinType = Object.freeze({
  /** Space Data Network identity (uses Bitcoin coin type 0) */
  SDN: 0,
  /** Bitcoin */
  BITCOIN: 0,
  /** Ethereum */
  ETHEREUM: 60,
  /** Solana */
  SOLANA: 501,
});

// =============================================================================
// Module Export
// =============================================================================

/**
 * Initialize the HD Wallet WASM module
 * @returns {Promise<Object>} Initialized HDWalletModule
 */
export default async function init() {
  const wasm = await loadWasmModule();
  return createModule(wasm);
}

/**
 * Create HD Wallet instance (alternative syntax)
 * @returns {Promise<Object>} Initialized HDWalletModule
 */
export async function createHDWallet() {
  return init();
}

// Export types and enums
export {
  HDKey,
  HDWalletError,
  ErrorCode,
  HD_WALLET_SDN_PLUGIN_MANIFEST,
  SDN_PLUGIN_MANIFEST_EXPORTS
};

// EPM Attestation
export {
  buildCanonicalPayload,
  buildEPMSigningContent,
  signEPMContent,
  verifyEPMSignature,
  buildBitcoinChainProof,
  buildEthereumChainProof,
  buildSolanaChainProof,
  buildAllChainProofs,
  verifyChainProof,
  verifyAllChainProofs,
} from './epm-attestation.mjs';

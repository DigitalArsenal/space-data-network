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
 * @version 2.1.2
 */

// Import aligned API for batch operations
import { AlignedAPI } from './aligned.mjs';
import {
  createSdnPluginContract,
  SDN_PLUGIN_MANIFEST_EXPORTS
} from './sdn-plugin.mjs';
import { HD_WALLET_SDN_PLUGIN_MANIFEST } from './sdn-plugin-manifest-source.mjs';
import { createSdnTypedCapabilities } from './sdn-typed.mjs';
import { createInputValidation } from './input-validation.mjs';

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
 * Check a status-returning export: 0 is success, any other value is an error.
 * @param {number} result - Result code from WASM function
 * @throws {HDWalletError} If result is non-zero
 */
function checkResult(result) {
  if (result !== 0) {
    throw new HDWalletError(nativeErrorCode(result));
  }
}

// Native return convention: an export whose success value is 0 reports any
// other value as an error; an export whose success is a length, a count or 1/0
// reports failure as a negative -ErrorCode. The checks below accept exactly the
// documented success value, so no error code (of either sign) is ever read as a
// length and no output buffer is read after a failed call.

/**
 * Map a failed native result to a library error code.
 * @param {number} result - Native return value that was not success
 * @returns {number} Known error code, or INTERNAL for anything else
 */
function nativeErrorCode(result) {
  const code = result < 0 ? -result : result;
  return code !== ErrorCode.OK && ERROR_MESSAGES[code] !== undefined ? code : ErrorCode.INTERNAL;
}

/**
 * Check a length-returning export: exactly `expected` bytes were written.
 * @param {number} result - Native return value
 * @param {number} expected - Byte length a successful call writes
 * @returns {number} The length
 * @throws {HDWalletError} On any other value, positive or negative
 */
function checkLength(result, expected) {
  if (result !== expected) {
    throw new HDWalletError(nativeErrorCode(result));
  }
  return result;
}

/**
 * Check a variable-length export: a length within [min, max].
 * @param {number} result - Native return value
 * @param {number} min - Smallest valid length
 * @param {number} max - Largest valid length (the output capacity)
 * @returns {number} The length
 * @throws {HDWalletError} On any value outside the range
 */
function checkLengthWithin(result, min, max) {
  if (!Number.isInteger(result) || result < min || result > max) {
    throw new HDWalletError(nativeErrorCode(result));
  }
  return result;
}

/**
 * Input validation for every wrapper (input-validation.mjs): type, exact
 * length, and for keys a native validity check, before anything reaches WASM.
 * Verification returns false for invalid bytes and throws only for programmer
 * errors (wrong argument types, unknown curves or coins).
 */
const V = createInputValidation((code, message) => new HDWalletError(code, message));

/** Decode a hex string to bytes, or null when it is not valid hex. */
function hexOrNull(value) {
  if (typeof value !== 'string') return null;
  const hex = value.startsWith('0x') || value.startsWith('0X') ? value.slice(2) : value;
  if (hex.length % 2 !== 0 || !/^[0-9a-fA-F]*$/.test(hex)) return null;
  return decodeHexFallback(hex);
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

/** %TypedArray%.prototype.fill, captured at load (uses the internal length). */
const TYPED_ARRAY_FILL = Object.getPrototypeOf(Uint8Array.prototype).fill;

// Internal-slot getters, captured at load: they work on objects of any realm
// and cannot be overridden by the object.
const slotGetter = (prototype, name) => Object.getOwnPropertyDescriptor(prototype, name)?.get;
const TYPED_ARRAY_SLOTS = ['buffer', 'byteOffset', 'byteLength']
  .map((name) => slotGetter(Object.getPrototypeOf(Uint8Array.prototype), name));
const DATA_VIEW_SLOTS = ['buffer', 'byteOffset', 'byteLength'].map((name) => slotGetter(DataView.prototype, name));
const ARRAY_BUFFER_BYTE_LENGTH = slotGetter(ArrayBuffer.prototype, 'byteLength');
const ARRAY_BUFFER_DETACHED = slotGetter(ArrayBuffer.prototype, 'detached');
const SHARED_BUFFER_BYTE_LENGTH = typeof SharedArrayBuffer === 'function'
  ? slotGetter(SharedArrayBuffer.prototype, 'byteLength')
  : undefined;

/**
 * The bytes under binary data of any kind and realm: { buffer, offset,
 * length, detached }, or null for anything that is not binary data.
 */
function binaryRegion(value) {
  const read = (getters) => {
    try {
      const [buffer, offset, length] = getters.map((get) => get.call(value));
      return { buffer, offset, length };
    } catch {
      return null;
    }
  };
  let region = read(TYPED_ARRAY_SLOTS) ?? read(DATA_VIEW_SLOTS);
  if (region === null) {
    for (const byteLength of [ARRAY_BUFFER_BYTE_LENGTH, SHARED_BUFFER_BYTE_LENGTH]) {
      if (!byteLength) continue;
      try {
        region = { buffer: value, offset: 0, length: byteLength.call(value) };
        break;
      } catch {
        // Not that kind of buffer.
      }
    }
  }
  if (region === null) return null;
  let detached = false;
  try {
    detached = ARRAY_BUFFER_DETACHED ? ARRAY_BUFFER_DETACHED.call(region.buffer) : false;
  } catch {
    detached = false;  // A SharedArrayBuffer cannot be detached.
  }
  return { ...region, detached };
}

/** The largest Ethereum chain ID (EIP-2294). */
const ETH_MAX_CHAIN_ID = 9223372036854775771n;

// =============================================================================
// Memory Helpers
// =============================================================================

/**
 * Allocate memory and copy data
 * @param {WebAssembly.Module} wasm - WASM module
 * @param {Uint8Array} data - Data to copy
 * @returns {{ptr: number, len: number}} Heap pointer and the copied length
 */
function allocAndCopy(wasm, data) {
  // Only binary data reaches the heap (a string or array would be coerced).
  V.bytes(data, 'data');
  // One copy from the array's own bytes and length: the caller's `length`
  // (a subclass getter, an own property) is never read again. Callers pass
  // the returned `len` to native code.
  const copy = new Uint8Array(data);
  const len = copy.length;
  // Many C++ implementations assume pointers are non-null even when `len == 0`.
  // Allocate a 1-byte placeholder in that case to avoid passing NULL into WASM.
  const ptr = wasm._hd_alloc(len === 0 ? 1 : len);
  if (!ptr) throw new HDWalletError(ErrorCode.OUT_OF_MEMORY);
  wasm.HEAPU8.set(copy, ptr);
  copy.fill(0);
  return { ptr, len };
}

/**
 * Allocate memory for string and copy
 * @param {WebAssembly.Module} wasm - WASM module
 * @param {string} str - String to copy
 * @returns {number} Pointer to allocated memory
 */
function allocString(wasm, str) {
  V.cString(str, 'string argument');
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

// Binary data is a Uint8Array in every family: a DataView, an ArrayBuffer or
// another typed array is refused, as everywhere else.
function toUint8Array(data, name = 'data') {
  return V.bytes(data, name);
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
    const { ptr: inPtr, len: inLen } = allocAndCopy(this._wasm, compressed);
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
    V.uint32(index, 'index');
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
    V.integer(index, 'index', 0, 0x7fffffff);
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
    const { ptr: pubPtr, len: pubLen } = allocAndCopy(this._wasm, pubKey);
    const outPtr = this._wasm._hd_alloc(64);
    try {
      const result = checkLengthWithin(
        this._wasm._hd_libp2p_peer_id(pubPtr, pubLen, this.curve, outPtr, 64), 1, 64);
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
    const { ptr: pubPtr, len: pubLen } = allocAndCopy(this._wasm, pubKey);
    const outPtr = this._wasm._hd_alloc(128);
    try {
      const result = this._wasm._hd_libp2p_peer_id_string(pubPtr, pubLen, this.curve, outPtr, 128);
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
    const { ptr: pubPtr, len: pubLen } = allocAndCopy(this._wasm, pubKey);
    const outPtr = this._wasm._hd_alloc(128);
    try {
      const result = this._wasm._hd_libp2p_ipns_hash(pubPtr, pubLen, this.curve, outPtr, 128);
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
      V.integer(wordCount, 'wordCount', 0, 0x7fffffff);
      V.integer(language, 'language', 0, 0x7fffffff);
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
      V.integer(language, 'language', 0, 0x7fffffff);
      if (!V.isCString(V.string(mnemonicStr, 'mnemonic'))) return false;
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
      V.string(mnemonicStr, 'mnemonic');
      V.string(passphrase, 'passphrase');
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
      V.integer(language, 'language', 0, 0x7fffffff);
      const mnemonicPtr = allocString(wasm, mnemonicStr);
      const mnemonicLen = wasm.lengthBytesUTF8(mnemonicStr) + 1;
      const entropyPtr = wasm._hd_alloc(33);
      const sizePtr = wasm._hd_alloc(4);
      try {
        wasm.setValue(sizePtr, 33, 'i32');
        const result = wasm._hd_mnemonic_to_entropy(mnemonicPtr, language, entropyPtr, sizePtr);
        checkResult(result);
        const size = checkLengthWithin(wasm.getValue(sizePtr, 'i32'), 16, 32);
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
      V.bytes(entropy, 'entropy', ErrorCode.INVALID_ENTROPY_LENGTH);
      V.integer(language, 'language', 0, 0x7fffffff);
      const { ptr: entropyPtr, len: entropyLen } = allocAndCopy(wasm, entropy);
      const outputPtr = wasm._hd_alloc(1024);
      try {
        const result = wasm._hd_entropy_to_mnemonic(entropyPtr, entropyLen, language, outputPtr, 1024);
        checkResult(result);
        return readString(wasm, outputPtr);
      } finally {
        // SECURITY FIX [HIGH-04]: Wipe sensitive data before deallocation
        wasm._hd_secure_wipe(entropyPtr, entropyLen);
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
      V.integer(language, 'language', 0, 0x7fffffff);
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
      V.integer(language, 'language', 0, 0x7fffffff);
      V.integer(maxSuggestions, 'maxSuggestions', 0, 2048);
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
      V.integer(language, 'language', 0, 0x7fffffff);
      const wordPtr = allocString(wasm, word);
      try {
        const result = wasm._hd_mnemonic_check_word(wordPtr, language);
        // Word index 0-2047, or -1 when the word is not in the list; any
        // other value is an error (invalid argument, unsupported language).
        if (result >= 0 && result <= 2047) return true;
        if (result === -1) return false;
        throw new HDWalletError(nativeErrorCode(result));
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
      V.bytesInRange(seed, 16, 64, 'Seed', ErrorCode.INVALID_SEED);
      V.curve(curve);
      const { ptr: seedPtr, len: seedLen } = allocAndCopy(wasm, seed);
      try {
        const handle = wasm._hd_key_from_seed(seedPtr, seedLen, curve);
        if (!handle) {
          throw new HDWalletError(ErrorCode.INVALID_SEED);
        }
        return new HDKey(wasm, handle, 'm');
      } finally {
        wasm._hd_secure_wipe(seedPtr, seedLen);
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
      V.uint32(purpose, 'purpose');
      V.uint32(coinType, 'coinType');
      V.uint32(account, 'account');
      V.uint32(change, 'change');
      V.uint32(index, 'index');
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
        V.privateKey(wasm, privateKey, curve);
        const { ptr: privPtr, len: privLen } = allocAndCopy(wasm, privateKey);
        const pubPtr = wasm._hd_alloc(65);
        try {
          // Returns the key length; a private key outside 0 < d < n fails.
          const expectedLen = curve === Curve.P384 ? 49 : 33;
          checkLength(pubFromPrivFn(privPtr, curve, pubPtr, 65), expectedLen);
          return readBytes(wasm, pubPtr, expectedLen);
        } finally {
          wasm._hd_secure_wipe(privPtr, privLen);
          wasm._hd_dealloc(privPtr);
          wasm._hd_dealloc(pubPtr);
        }
    },

    /**
     * Compress public key. The key must be a valid uncompressed point of the
     * curve (checked by the module); the compressed form is its parity byte
     * and x-coordinate.
     * @param {Uint8Array} publicKey - Uncompressed public key (65 bytes; 97 for P-384)
     * @param {number} curve - Curve type (secp256k1, P-256 or P-384)
     * @returns {Uint8Array} Compressed public key (33 bytes; 49 for P-384)
     */
    compressPublicKey(publicKey, curve) {
      V.curve(curve, [Curve.SECP256K1, Curve.P256, Curve.P384]);
      const uncompressedLength = curve === Curve.P384 ? 97 : 65;
      V.publicKey(wasm, publicKey, curve, 'publicKey', [uncompressedLength]);
      const coordLen = (uncompressedLength - 1) / 2;
      const out = new Uint8Array(coordLen + 1);
      out[0] = (publicKey[uncompressedLength - 1] & 1) ? 0x03 : 0x02;
      out.set(publicKey.subarray(1, coordLen + 1), 1);
      return out;
    },

    /**
     * Decompress public key
     * @param {Uint8Array} publicKey - Compressed public key (33 bytes)
     * @param {number} curve - Curve type
     * @returns {Uint8Array} Uncompressed public key (65 bytes)
     */
      decompressPublicKey(publicKey, curve) {
        const decompressFn = requireWasmFunction(wasm, '_hd_curve_decompress_pubkey');
        // The export reads exactly 33 bytes: a valid compressed key only.
        V.publicKey(wasm, publicKey, curve, 'publicKey', [curve === Curve.P384 ? 49 : 33]);
        const { ptr: inPtr, len: inLen } = allocAndCopy(wasm, publicKey);
        const outPtr = wasm._hd_alloc(97);
        try {
          // Status export: 0 on success (secp256k1 only), -Error otherwise.
          checkResult(decompressFn(inPtr, curve, outPtr, 97));
          const expectedLen = curve === Curve.P384 ? 97 : 65;
          return readBytes(wasm, outPtr, expectedLen);
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
      V.curve(curve, [Curve.SECP256K1, Curve.P256, Curve.P384, Curve.X25519]);
      V.privateKey(wasm, privateKey, curve);
      V.publicKey(wasm, publicKey, curve);
      const { ptr: privPtr, len: privLen } = allocAndCopy(wasm, privateKey);
      const { ptr: pubPtr, len: pubLen } = allocAndCopy(wasm, publicKey);
      const secretSize = curve === Curve.P384 ? 48 : 32;
      const secretPtr = wasm._hd_alloc(secretSize);
      try {
        const result = ecdhFn(curve, privPtr, privLen, pubPtr, pubLen, secretPtr, secretSize);
        // Returns the secret length (the curve's field size); -1 on failure.
        if (result !== secretSize) throw new HDWalletError(ErrorCode.INTERNAL, 'ECDH key exchange failed');
        return readBytes(wasm, secretPtr, secretSize);
      } finally {
        wasm._hd_secure_wipe(privPtr, privLen);
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
       * Sign a message with secp256k1 ECDSA. Hashes the message with SHA-256
       * internally and signs that hash (RFC 6979, low-S): pass the message, never
       * a hash of it, or the signature covers SHA-256(SHA-256(message)) and no
       * standard verifier accepts it.
       * @param {Uint8Array} message - Message bytes (hashed once with SHA-256 here)
       * @param {Uint8Array} privateKey - 32-byte private key
       * @returns {Uint8Array} 64-byte compact signature r||s
       */
      sign(message, privateKey) {
        V.bytes(message, 'message');
        V.privateKey(wasm, privateKey, Curve.SECP256K1);
        const { ptr: msgPtr, len: msgLen } = allocAndCopy(wasm, message);
        const { ptr: keyPtr, len: keyLen } = allocAndCopy(wasm, privateKey);
        const sigPtr = wasm._hd_alloc(72);
        try {
          // Returns 64 (compact r||s); a private key outside 0 < d < n fails.
          checkLength(wasm._hd_secp256k1_sign(msgPtr, msgLen, keyPtr, sigPtr, 72), 64);
          return readBytes(wasm, sigPtr, 64);
        } finally {
          wasm._hd_secure_wipe(keyPtr, 32);
          wasm._hd_dealloc(msgPtr);
          wasm._hd_dealloc(keyPtr);
          wasm._hd_dealloc(sigPtr);
        }
      },

      /**
       * Sign a 32-byte hash with recovery ID. Unlike sign(), the input is NOT
       * hashed: pass the protocol digest you computed (e.g. Keccak-256 for
       * Ethereum, the Bitcoin signed-message hash).
       * @param {Uint8Array} message - 32-byte hash, signed as given
       * @param {Uint8Array} privateKey - 32-byte private key
       * @returns {Object} { signature: Uint8Array (64-byte r||s), recoveryId: number }
       */
      signRecoverable(message, privateKey) {
        const signRecoverableFn = requireWasmFunction(wasm, '_hd_secp256k1_sign_recoverable');
        V.bytesOfLength(message, 32, 'message hash');
        V.privateKey(wasm, privateKey, Curve.SECP256K1);
        const { ptr: msgPtr, len: msgLen } = allocAndCopy(wasm, message);
        const { ptr: keyPtr, len: keyLen } = allocAndCopy(wasm, privateKey);
        const sigPtr = wasm._hd_alloc(65);
        try {
          // C signature: int32_t hd_secp256k1_sign_recoverable(hash, hash_len, privkey, out_sig65)
          // Returns 65 on success (bytes written), -Error on failure.
          // out_sig65 = r[32] + s[32] + v[1], recovery ID is at byte[64].
          checkLength(signRecoverableFn(msgPtr, msgLen, keyPtr, sigPtr), 65);
          const recoveryId = wasm.HEAPU8[sigPtr + 64];
          if (recoveryId > 3) throw new HDWalletError(ErrorCode.INVALID_SIGNATURE);
          const signature = readBytes(wasm, sigPtr, 64);
          return { signature, recoveryId };
        } finally {
          wasm._hd_secure_wipe(keyPtr, 32);
          wasm._hd_dealloc(msgPtr);
          wasm._hd_dealloc(keyPtr);
          wasm._hd_dealloc(sigPtr);
        }
      },

      /**
       * Verify a secp256k1 ECDSA signature over SHA-256(message). Hashes the
       * message with SHA-256 internally: pass the message, not a hash of it.
       * @param {Uint8Array} message - Original message bytes (hashed once with SHA-256 here)
       * @param {Uint8Array} signature - 64-byte compact r||s or DER signature
       * @param {Uint8Array} publicKey - 33- or 65-byte public key
       * @returns {boolean} True if valid
       */
      verify(message, signature, publicKey) {
        V.bytes(message, 'message');
        V.bytes(signature, 'signature');
        V.bytes(publicKey, 'publicKey');
        // Compact r||s (64 bytes) or DER (8-72 bytes); a key of either SEC1 size.
        if (V.byteLength(signature) < 8 || V.byteLength(signature) > 72 || !V.hasLength(publicKey, [33, 65])) {
          return false;
        }
        const { ptr: msgPtr, len: msgLen } = allocAndCopy(wasm, message);
        const { ptr: sigPtr, len: sigLen } = allocAndCopy(wasm, signature);
        const { ptr: pubPtr, len: pubLen } = allocAndCopy(wasm, publicKey);
        try {
          const result = wasm._hd_secp256k1_verify(msgPtr, msgLen, sigPtr, sigLen, pubPtr, pubLen);
          return result === 1;
        } finally {
          wasm._hd_dealloc(msgPtr);
          wasm._hd_dealloc(sigPtr);
          wasm._hd_dealloc(pubPtr);
        }
      },

      /**
       * Recover the public key from a recoverable signature. Like
       * signRecoverable(), the input is the 32-byte hash, used as given.
       * @param {Uint8Array} message - 32-byte hash that was signed
       * @param {Uint8Array} signature - 64-byte r||s
       * @param {number} recoveryId - Recovery ID (0-3)
       * @returns {Uint8Array} Recovered 33-byte compressed public key
       */
      recover(message, signature, recoveryId) {
        const recoverFn = requireWasmFunction(wasm, '_hd_secp256k1_recover');
        V.bytesOfLength(message, 32, 'message hash');
        V.bytesOfLength(signature, 64, 'signature', ErrorCode.INVALID_SIGNATURE);
        if (!Number.isInteger(recoveryId) || recoveryId < 0 || recoveryId > 3) {
          throw new HDWalletError(ErrorCode.INVALID_SIGNATURE, 'recoveryId must be 0, 1, 2 or 3');
        }
        const { ptr: msgPtr, len: msgLen } = allocAndCopy(wasm, message);
        // Build 65-byte sig buffer: r[32] + s[32] + v[1]
        // C signature: int32_t hd_secp256k1_recover(hash, hash_len, sig65, out_pubkey33)
        const sig65 = new Uint8Array(65);
        sig65.set(signature.subarray(0, 64));
        sig65[64] = recoveryId;
        const { ptr: sigPtr, len: sigLen } = allocAndCopy(wasm, sig65);
        const pubPtr = wasm._hd_alloc(33);
        try {
          // Returns 33 on success; a signature no key recovers from (ids 2
          // and 3 when r + n >= p included) fails, so this throws.
          checkLength(recoverFn(msgPtr, msgLen, sigPtr, pubPtr), 33);
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
        V.privateKey(wasm, privateKey, Curve.SECP256K1);
        V.publicKey(wasm, publicKey, Curve.SECP256K1);
        const { ptr: privPtr, len: privLen } = allocAndCopy(wasm, privateKey);
        const { ptr: pubPtr, len: pubLen } = allocAndCopy(wasm, publicKey);
        const secretPtr = wasm._hd_alloc(32);
        try {
          checkLength(wasm._hd_ecdh_secp256k1(privPtr, pubPtr, pubLen, secretPtr, 32), 32);
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
        V.privateKey(wasm, seed, Curve.ED25519, 'seed');
        const { ptr: seedPtr, len: seedLen } = allocAndCopy(wasm, seed);
        const pubPtr = wasm._hd_alloc(32);
        try {
          checkResult(wasm._hd_ed25519_pubkey_from_seed(seedPtr, pubPtr, 32));
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
        V.bytes(message, 'message');
        V.privateKey(wasm, privateKey, Curve.ED25519);
        const { ptr: msgPtr, len: msgLen } = allocAndCopy(wasm, message);
        const { ptr: keyPtr, len: keyLen } = allocAndCopy(wasm, privateKey);
        const sigPtr = wasm._hd_alloc(64);
        try {
          checkLength(wasm._hd_ed25519_sign(msgPtr, msgLen, keyPtr, sigPtr, 64), 64);
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
        V.bytes(message, 'message');
        V.bytes(signature, 'signature');
        V.bytes(publicKey, 'publicKey');
        if (V.byteLength(signature) !== 64 || V.byteLength(publicKey) !== 32) return false;
        const { ptr: msgPtr, len: msgLen } = allocAndCopy(wasm, message);
        const { ptr: sigPtr, len: sigLen } = allocAndCopy(wasm, signature);
        const { ptr: pubPtr, len: pubLen } = allocAndCopy(wasm, publicKey);
        try {
          // RFC 8032 strict: S < L, A and R canonical and not of small order.
          const result = wasm._hd_ed25519_verify(msgPtr, msgLen, sigPtr, sigLen, pubPtr, pubLen);
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
      /**
       * Sign a message with P-256 ECDSA. Hashes the message with SHA-256
       * internally: pass the message, never a hash of it.
       * @param {Uint8Array} message - Message bytes (hashed once with SHA-256 here)
       * @param {Uint8Array} privateKey - 32-byte private key
       * @returns {Uint8Array} 64-byte compact signature r||s
       */
      sign(message, privateKey) {
        V.bytes(message, 'message');
        V.privateKey(wasm, privateKey, Curve.P256);
        const { ptr: msgPtr, len: msgLen } = allocAndCopy(wasm, message);
        const { ptr: keyPtr, len: keyLen } = allocAndCopy(wasm, privateKey);
        const sigPtr = wasm._hd_alloc(72);
        try {
          checkLength(wasm._hd_p256_sign(msgPtr, msgLen, keyPtr, sigPtr, 72), 64);
          return readBytes(wasm, sigPtr, 64);
        } finally {
          wasm._hd_secure_wipe(keyPtr, 32);
          wasm._hd_dealloc(msgPtr);
          wasm._hd_dealloc(keyPtr);
          wasm._hd_dealloc(sigPtr);
        }
      },

      /**
       * Verify a P-256 ECDSA signature over SHA-256(message).
       * @param {Uint8Array} message - Original message bytes (hashed once with SHA-256 here)
       * @param {Uint8Array} signature - 64-byte compact signature r||s
       * @param {Uint8Array} publicKey - 33- or 65-byte public key
       * @returns {boolean} True if valid
       */
      verify(message, signature, publicKey) {
        const verifyFn = requireWasmFunction(wasm, '_hd_p256_verify');
        V.bytes(message, 'message');
        V.bytes(signature, 'signature');
        // The export reads exactly 64 signature bytes and takes no length.
        if (V.byteLength(signature) !== 64 || !V.isPublicKey(wasm, publicKey, Curve.P256)) return false;
        const { ptr: msgPtr, len: msgLen } = allocAndCopy(wasm, message);
        const { ptr: sigPtr, len: sigLen } = allocAndCopy(wasm, signature);
        const { ptr: pubPtr, len: pubLen } = allocAndCopy(wasm, publicKey);
        try {
          // C: hd_p256_verify(message, message_len, public_key, public_key_len, signature)
          return verifyFn(msgPtr, msgLen, pubPtr, pubLen, sigPtr) === 0;
        } finally {
          wasm._hd_dealloc(msgPtr);
          wasm._hd_dealloc(sigPtr);
          wasm._hd_dealloc(pubPtr);
        }
      },

      ecdh(privateKey, publicKey) {
        V.privateKey(wasm, privateKey, Curve.P256);
        V.publicKey(wasm, publicKey, Curve.P256);
        const { ptr: privPtr, len: privLen } = allocAndCopy(wasm, privateKey);
        const { ptr: pubPtr, len: pubLen } = allocAndCopy(wasm, publicKey);
        const secretPtr = wasm._hd_alloc(32);
        try {
          checkLength(wasm._hd_ecdh_p256(privPtr, pubPtr, pubLen, secretPtr, 32), 32);
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
      /**
       * Sign a message with P-384 ECDSA. Hashes the message with SHA-384
       * internally: pass the message, never a hash of it.
       * @param {Uint8Array} message - Message bytes (hashed once with SHA-384 here)
       * @param {Uint8Array} privateKey - 48-byte private key
       * @returns {Uint8Array} 96-byte compact signature r||s
       */
      sign(message, privateKey) {
        V.bytes(message, 'message');
        V.privateKey(wasm, privateKey, Curve.P384);
        const { ptr: msgPtr, len: msgLen } = allocAndCopy(wasm, message);
        const { ptr: keyPtr, len: keyLen } = allocAndCopy(wasm, privateKey);
        const sigPtr = wasm._hd_alloc(104);
        try {
          checkLength(wasm._hd_p384_sign(msgPtr, msgLen, keyPtr, sigPtr, 104), 96);
          return readBytes(wasm, sigPtr, 96);
        } finally {
          wasm._hd_secure_wipe(keyPtr, 48);
          wasm._hd_dealloc(msgPtr);
          wasm._hd_dealloc(keyPtr);
          wasm._hd_dealloc(sigPtr);
        }
      },

      /**
       * Verify a P-384 ECDSA signature over SHA-384(message).
       * @param {Uint8Array} message - Original message bytes (hashed once with SHA-384 here)
       * @param {Uint8Array} signature - 96-byte compact signature r||s
       * @param {Uint8Array} publicKey - 49- or 97-byte public key
       * @returns {boolean} True if valid
       */
      verify(message, signature, publicKey) {
        const verifyFn = requireWasmFunction(wasm, '_hd_p384_verify');
        V.bytes(message, 'message');
        V.bytes(signature, 'signature');
        // The export reads exactly 96 signature bytes and takes no length.
        if (V.byteLength(signature) !== 96 || !V.isPublicKey(wasm, publicKey, Curve.P384)) return false;
        const { ptr: msgPtr, len: msgLen } = allocAndCopy(wasm, message);
        const { ptr: sigPtr, len: sigLen } = allocAndCopy(wasm, signature);
        const { ptr: pubPtr, len: pubLen } = allocAndCopy(wasm, publicKey);
        try {
          // C: hd_p384_verify(message, message_len, public_key, public_key_len, signature)
          return verifyFn(msgPtr, msgLen, pubPtr, pubLen, sigPtr) === 0;
        } finally {
          wasm._hd_dealloc(msgPtr);
          wasm._hd_dealloc(sigPtr);
          wasm._hd_dealloc(pubPtr);
        }
      },

      ecdh(privateKey, publicKey) {
        V.privateKey(wasm, privateKey, Curve.P384);
        V.publicKey(wasm, publicKey, Curve.P384);
        const { ptr: privPtr, len: privLen } = allocAndCopy(wasm, privateKey);
        const { ptr: pubPtr, len: pubLen } = allocAndCopy(wasm, publicKey);
        const secretPtr = wasm._hd_alloc(48);
        try {
          checkLength(wasm._hd_ecdh_p384(privPtr, pubPtr, pubLen, secretPtr, 48), 48);
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
        V.privateKey(wasm, privateKey, Curve.X25519);
        const { ptr: privPtr, len: privLen } = allocAndCopy(wasm, privateKey);
        const pubPtr = wasm._hd_alloc(32);
        try {
          checkResult(wasm._hd_x25519_pubkey(privPtr, pubPtr, 32));
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
        V.privateKey(wasm, privateKey, Curve.X25519);
        V.publicKey(wasm, publicKey, Curve.X25519);
        const { ptr: privPtr, len: privLen } = allocAndCopy(wasm, privateKey);
        const { ptr: pubPtr, len: pubLen } = allocAndCopy(wasm, publicKey);
        const secretPtr = wasm._hd_alloc(32);
        try {
          checkResult(wasm._hd_ecdh_x25519(privPtr, pubPtr, secretPtr, 32));
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
  // Coins of the generic coin API and the curve of their keys.
  const COIN_CURVES = Object.freeze({
    [CoinType.BITCOIN]: Curve.SECP256K1,
    [CoinType.BITCOIN_TESTNET]: Curve.SECP256K1,
    [CoinType.ETHEREUM]: Curve.SECP256K1,
    [CoinType.COSMOS]: Curve.SECP256K1,
    [CoinType.SOLANA]: Curve.ED25519,
    [CoinType.POLKADOT]: Curve.ED25519,
  });

  function coinCurve(coinType) {
    V.integer(coinType, 'coinType', -0x80000000, 0x7fffffff);
    const curve = COIN_CURVES[coinType];
    if (curve === undefined) {
      throw new HDWalletError(ErrorCode.NOT_SUPPORTED, `Unsupported coin type: ${coinType}`);
    }
    return curve;
  }

  // The generic export takes 33 bytes for every coin: a compressed secp256k1
  // key, or 0x00 || an Ed25519 key.
  function coinPublicKey(coinType, publicKey) {
    const curve = coinCurve(coinType);
    if (curve === Curve.SECP256K1) {
      return V.publicKey(wasm, publicKey, Curve.SECP256K1, 'publicKey', [33]);
    }
    V.bytesOfLength(publicKey, [32, 33], 'publicKey', ErrorCode.INVALID_PUBLIC_KEY);
    if (publicKey.length === 32) {
      const padded = new Uint8Array(33);
      padded.set(publicKey, 1);
      return padded;
    }
    if (publicKey[0] !== 0x00) {
      throw new HDWalletError(ErrorCode.INVALID_PUBLIC_KEY, 'publicKey must be 32 bytes, or 33 with a 0x00 prefix');
    }
    return publicKey;
  }

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
      publicKey = coinPublicKey(coinType, publicKey);
      V.oneOf(network, [Network.MAINNET, Network.TESTNET], 'network');
      const { ptr: pubPtr, len: pubLen } = allocAndCopy(wasm, publicKey);
      const outPtr = wasm._hd_alloc(128);
      try {
        const result = fn(coinType, network, pubPtr, pubLen, outPtr, 128);
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
      V.integer(coinType, 'coinType', -0x80000000, 0x7fffffff);
      V.integer(network, 'network', -0x80000000, 0x7fffffff);
      if (!V.isCString(V.string(address, 'address'))) return false;
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
      V.bytes(message, 'message');
      V.privateKey(wasm, privateKey, coinCurve(coinType));
      const { ptr: msgPtr, len: msgLen } = allocAndCopy(wasm, message);
      const { ptr: keyPtr, len: keyLen } = allocAndCopy(wasm, privateKey);
      const sigLenPtr = wasm._hd_alloc(4);
      // Pre-allocate enough for the largest possible signature (65 bytes for ECDSA recoverable)
      const maxSigLen = 128;
      wasm.setValue(sigLenPtr, maxSigLen, 'i32');
      const sigPtr = wasm._hd_alloc(maxSigLen);
      try {
        const result = fn(coinType, msgPtr, msgLen, keyPtr, sigPtr, sigLenPtr);
        checkResult(result);
        const actualLen = checkLengthWithin(wasm.getValue(sigLenPtr, 'i32'), 1, maxSigLen);
        return readBytes(wasm, sigPtr, actualLen);
      } finally {
        wasm._hd_secure_wipe(keyPtr, keyLen);
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
      coinCurve(coinType);
      V.bytes(message, 'message');
      V.bytes(signature, 'signature');
      V.bytes(publicKey, 'publicKey');
      const { ptr: msgPtr, len: msgLen } = allocAndCopy(wasm, message);
      const { ptr: sigPtr, len: sigLen } = allocAndCopy(wasm, signature);
      const { ptr: pubPtr, len: pubLen } = allocAndCopy(wasm, publicKey);
      try {
        // The export takes every length: any failure (a malformed signature
        // or key included) is "not verified".
        return fn(coinType, msgPtr, msgLen, sigPtr, sigLen, pubPtr, pubLen) === 1;
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
      V.oneOf(network, [Network.MAINNET, Network.TESTNET], 'network');
      switch (type) {
        case BitcoinAddressType.P2PKH:
          V.publicKey(wasm, publicKey, Curve.SECP256K1);
          break;
        case BitcoinAddressType.P2SH:
        case BitcoinAddressType.P2WPKH:
          V.publicKey(wasm, publicKey, Curve.SECP256K1, 'publicKey', [33]);
          break;
        case BitcoinAddressType.P2TR:
          if (V.bytesOfLength(publicKey, [32, 33], 'publicKey', ErrorCode.INVALID_PUBLIC_KEY).length === 32) {
            // An x-only key: valid when 02 || x is a point.
            const even = new Uint8Array(33);
            even[0] = 0x02;
            even.set(publicKey, 1);
            V.publicKey(wasm, even, Curve.SECP256K1, 'publicKey', [33]);
          } else {
            V.publicKey(wasm, publicKey, Curve.SECP256K1, 'publicKey', [33]);
          }
          break;
        case BitcoinAddressType.P2WSH:
          // A witness script, not a key.
          V.bytesInRange(publicKey, 1, 10000, 'witnessScript');
          break;
        default:
          throw new HDWalletError(ErrorCode.INVALID_ARGUMENT, 'Invalid address type');
      }
      const { ptr: pubPtr, len: pubLen } = allocAndCopy(wasm, publicKey);
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
        const result = addressFn(pubPtr, pubLen, network, outPtr, 128);
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
      V.integer(network, 'network', -0x80000000, 0x7fffffff);
      if (!V.isCString(V.string(address, 'address'))) return false;
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
        const hashLen = checkLengthWithin(wasm.getValue(hashLenPtr, 'i32'), 1, 32);
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
      V.integer(network, 'network', -0x80000000, 0x7fffffff);
      const addrPtr = allocString(wasm, address);
      try {
        return detectFn(addrPtr, network);
      } finally {
        wasm._hd_dealloc(addrPtr);
      }
    },

    toWIF(privateKey, compressed = true, network = Network.MAINNET) {
      const toWifFn = requireWasmFunction(wasm, '_hd_btc_to_wif');
      V.privateKey(wasm, privateKey, Curve.SECP256K1);
      V.oneOf(network, [Network.MAINNET, Network.TESTNET], 'network');
      const { ptr: keyPtr, len: keyLen } = allocAndCopy(wasm, privateKey);
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
      V.string(message, 'message');
      V.privateKey(wasm, privateKey, Curve.SECP256K1);
      const msgPtr = allocString(wasm, message);
      const { ptr: keyPtr, len: keyLen } = allocAndCopy(wasm, privateKey);
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
      V.string(message, 'message');
      V.string(signature, 'signature');
      V.string(address, 'address');
      V.integer(network, 'network', -0x80000000, 0x7fffffff);
      // A signature that is not base64 of 65 bytes is "not verified".
      let sigBytes;
      try {
        sigBytes = decodeBase64Fallback(signature);
      } catch {
        return false;
      }
      if (V.byteLength(sigBytes) !== 65) return false;
      const msgPtr = allocString(wasm, message);
      const { ptr: sigPtr, len: sigLen } = allocAndCopy(wasm, sigBytes);
      const addrPtr = allocString(wasm, address);
      try {
        const result = verifyFn(msgPtr, sigPtr, sigLen, addrPtr, network);
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
            if (input === null || typeof input !== 'object') {
              throw new HDWalletError(ErrorCode.INVALID_ARGUMENT, 'input must be an object');
            }
            // Each field is read once; only these values are used below.
            const { txid, vout, amount: amountArg, scriptType: scriptTypeArg, sequence: sequenceArg, pubkey } = input;
            // The txid: 32 bytes as 64 hex digits, in the given byte order.
            const txidBytes = typeof txid === 'string' && txid.length === 64
              ? hexOrNull(txid)
              : null;
            if (txidBytes === null) {
              throw new HDWalletError(ErrorCode.INVALID_ARGUMENT, 'txid must be 64 hex digits');
            }
            V.uint32(vout, 'vout');
            const amount = V.unsignedBigInt(amountArg, 'amount', 64);
            const scriptType = scriptTypeArg || 0;
            V.integer(scriptType, 'scriptType', 0, BitcoinScriptType.MULTISIG);
            const sequence = sequenceArg || 0xffffffff;
            V.uint32(sequence, 'sequence');
            // The export reads a 33-byte compressed key.
            V.publicKey(wasm, pubkey, Curve.SECP256K1, 'pubkey', [33]);
            const { ptr: txidPtr } = allocAndCopy(wasm, txidBytes);
            const { ptr: pubPtr } = allocAndCopy(wasm, pubkey);
            try {
              const result = addInputFn(
                this._handle, txidPtr, vout,
                amount, scriptType,
                pubPtr, sequence
              );
              checkResult(result);
              this._inputCount++;
            } finally {
              wasm._hd_dealloc(txidPtr);
              wasm._hd_dealloc(pubPtr);
            }
            return this;
          },

          addOutput(address, amount, network = Network.MAINNET) {
            V.string(address, 'address');
            amount = V.unsignedBigInt(amount, 'amount', 64);
            // The address must be of this network: a mainnet transaction never
            // pays a testnet address, or the reverse. Regtest is not supported.
            V.oneOf(network, [Network.MAINNET, Network.TESTNET], 'network');
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
                checkResult(decResult);
                const addrType = wasm.getValue(typePtr, 'i32');
                const hashLen = checkLengthWithin(wasm.getValue(hashLenPtr, 'i32'), 1, 32);
                if (wasm.getValue(networkPtr, 'i32') !== network) {
                  throw new HDWalletError(ErrorCode.INVALID_ADDRESS, 'address is not of this transaction\'s network');
                }
                // The native decoder types an address by witness version and
                // program length together; each type has one payload length
                // (the P2WPKH/P2PKH helpers read a 20-byte key hash).
                const payloadLength = [20, 20, 20, 32, 32][addrType];
                if (payloadLength === undefined || hashLen !== payloadLength) {
                  throw new HDWalletError(ErrorCode.INVALID_ADDRESS);
                }
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
                const { ptr: scriptPtr, len: scriptLen } = allocAndCopy(wasm, script);
                try {
                  const result = addOutputFn(this._handle, BigInt(amount), scriptPtr, scriptLen);
                  checkResult(result);
                } finally {
                  wasm._hd_dealloc(scriptPtr);
                }
                return this;
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
            V.privateKey(wasm, privateKey, Curve.SECP256K1);
            const { ptr: keyPtr, len: keyLen } = allocAndCopy(wasm, privateKey);
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
              const size = checkLengthWithin(wasm.getValue(sizePtr, 'i32'), 1, 65536);
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
        V.bytes(data, 'data');
        const { ptr: dataPtr, len: dataLen } = allocAndCopy(wasm, data);
        try {
          const handle = parseFn(dataPtr, dataLen);
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
  // An Ethereum message: a string (UTF-8) or bytes.
  function ethereumMessageBytes(message) {
    if (typeof message === 'string') return UTF8_ENCODER.encode(message);
    return V.bytes(message, 'message');
  }

  const ethereum = {
    /**
     * Get Ethereum address from public key
     * @param {Uint8Array} publicKey - Uncompressed public key (65 bytes)
     * @returns {string} Ethereum address (with 0x prefix)
     */
    getAddress(publicKey) {
      const getAddressFn = requireWasmFunction(wasm, '_hd_eth_address');
      V.bytesOfLength(publicKey, [33, 64, 65], 'publicKey', ErrorCode.INVALID_PUBLIC_KEY);
      if (V.byteLength(publicKey) === 64) {
        // Raw x || y: valid when 04 || x || y is a point.
        const sec1 = new Uint8Array(65);
        sec1[0] = 0x04;
        sec1.set(publicKey, 1);
        V.publicKey(wasm, sec1, Curve.SECP256K1);
      } else {
        V.publicKey(wasm, publicKey, Curve.SECP256K1);
      }
      const { ptr: pubPtr, len: pubLen } = allocAndCopy(wasm, publicKey);
      const outPtr = wasm._hd_alloc(64);
      try {
        const result = getAddressFn(pubPtr, pubLen, outPtr, 64);
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
      if (!V.isCString(V.string(address, 'address'))) return false;
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
      const msgBytes = ethereumMessageBytes(message);
      V.privateKey(wasm, privateKey, Curve.SECP256K1);
      const { ptr: msgPtr, len: msgLen } = allocAndCopy(wasm, msgBytes);
      const { ptr: keyPtr, len: keyLen } = allocAndCopy(wasm, privateKey);
      const sigPtr = wasm._hd_alloc(65);
      try {
        const result = signFn(msgPtr, msgLen, keyPtr, sigPtr, 65);
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
      if (typedData === null || typeof typedData !== 'object' ||
          typedData.types === null || typeof typedData.types !== 'object' ||
          typeof typedData.primaryType !== 'string' ||
          typedData.domain === null || typeof typedData.domain !== 'object') {
        throw new HDWalletError(ErrorCode.INVALID_ARGUMENT, 'typedData must have types, primaryType, domain and message');
      }
      V.privateKey(wasm, privateKey, Curve.SECP256K1);

      // --- EIP-712 struct hash computation (data encoding, not crypto) ---
      // keccak256 via WASM
      function k256(data) {
        const d = data instanceof Uint8Array ? data : new TextEncoder().encode(data);
        const { ptr: dPtr, len: dLen } = allocAndCopy(wasm, d);
        const hPtr = wasm._hd_alloc(32);
        try {
          checkLength(wasm._hd_hash_keccak256(dPtr, dLen, hPtr, 32), 32);
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
      const { ptr: structHashPtr, len: structHashLen } = allocAndCopy(wasm, structHash);
      const { ptr: keyPtr, len: keyLen } = allocAndCopy(wasm, privateKey);
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

    /**
     * Recover the signer's address of an EIP-191 message signature. A recover,
     * not a boolean verify: compare the result with the expected address. A
     * signature that recovers no key (malformed hex, not 65 bytes, v outside
     * 0, 1, 27, 28, r or s out of range) throws INVALID_SIGNATURE; it never
     * yields an address.
     * @param {string|Uint8Array} message
     * @param {string} signature - 0x-prefixed hex r || s || v
     * @returns {string} Recovered address
     */
    verifyMessage(message, signature) {
      const recoverFn = requireWasmFunction(wasm, '_hd_eth_recover_address');
      const msgBytes = ethereumMessageBytes(message);
      V.string(signature, 'signature');
      const sigBytes = hexOrNull(signature);
      if (sigBytes === null || V.byteLength(sigBytes) !== 65 || ![0, 1, 27, 28].includes(sigBytes[64])) {
        throw new HDWalletError(ErrorCode.INVALID_SIGNATURE, 'signature must be 65 bytes r || s || v with v 0, 1, 27 or 28');
      }
      const { ptr: msgPtr, len: msgLen } = allocAndCopy(wasm, msgBytes);
      const { ptr: sigPtr, len: sigLen } = allocAndCopy(wasm, sigBytes);
      const addrPtr = wasm._hd_alloc(64);
      try {
        const result = recoverFn(msgPtr, msgLen, sigPtr, sigLen, addrPtr, 64);
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
      const msgBytes = ethereumMessageBytes(message);
      const { ptr: msgPtr, len: msgLen } = allocAndCopy(wasm, msgBytes);
      const hashPtr = wasm._hd_alloc(32);
      try {
        const result = hashFn(msgPtr, msgLen, hashPtr);
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
            V.privateKey(wasm, privateKey, Curve.SECP256K1);
            const { ptr: keyPtr, len: keyLen } = allocAndCopy(wasm, privateKey);
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
              const size = checkLengthWithin(wasm.getValue(sizePtr, 'i32'), 1, 65536);
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

      // Read each field of `params` once (a getter or a Uint8Array subclass
      // could answer differently on a second read: a longer `length` put heap
      // bytes into the signed data) and check it. The nonce and the chain ID
      // have no defaults: a missing nonce signed nonce 0, a missing chain ID
      // chain 1. The native setters take uint64 (a 256-bit value as 32 bytes);
      // a larger or negative number would wrap modulo 2^64, so it is refused.
      _readParams(params) {
        if (params === null || typeof params !== 'object') {
          throw new HDWalletError(ErrorCode.INVALID_ARGUMENT, 'params must be an object');
        }
        const {
          nonce, chainId, gasLimit, gasPrice, maxFeePerGas, maxPriorityFeePerGas, to, value, data,
        } = params;
        if (nonce === undefined || nonce === null) {
          throw new HDWalletError(ErrorCode.INVALID_ARGUMENT, 'nonce is required');
        }
        if (chainId === undefined || chainId === null) {
          throw new HDWalletError(ErrorCode.INVALID_ARGUMENT, 'chainId is required');
        }
        const fields = {
          nonce: V.unsignedBigInt(nonce, 'nonce', 64),
          chainId: V.unsignedBigInt(chainId, 'chainId', 64),
          gasLimit: gasLimit === undefined ? undefined : V.unsignedBigInt(gasLimit, 'gasLimit', 64),
          gasPrice: gasPrice === undefined ? undefined : V.unsignedBigInt(gasPrice, 'gasPrice', 64),
          maxFeePerGas,
          maxPriorityFeePerGas,
          to: to === undefined || to === null || to === '' ? undefined : V.string(to, 'to'),
          value: value === undefined ? undefined : V.unsignedBigInt(value, 'value', 256),
          // A copy made from the array's own bytes and length.
          data: data === undefined || data === null ? undefined : new Uint8Array(V.bytes(data, 'data')),
        };
        // EIP-2294: 1 <= chainId <= floor(MAX_UINT64 / 2) - 36, so a legacy
        // v = 2 * chainId + 35 + recovery id fits 64 bits.
        if (fields.chainId === 0n || fields.chainId > ETH_MAX_CHAIN_ID) {
          throw new HDWalletError(ErrorCode.INVALID_ARGUMENT, `chainId must be from 1 to ${ETH_MAX_CHAIN_ID}`);
        }
        return fields;
      },

      _setCommonFields(handle, fields) {
        requireWasmFunction(wasm, '_hd_eth_tx_set_nonce')(handle, fields.nonce);
        if (fields.gasLimit !== undefined) {
          requireWasmFunction(wasm, '_hd_eth_tx_set_gas_limit')(handle, fields.gasLimit);
        }

        if (fields.to !== undefined) {
          const setToFn = requireWasmFunction(wasm, '_hd_eth_tx_set_to_hex');
          const toPtr = allocString(wasm, fields.to);
          try {
            // An address that does not parse fails here. Without a recipient
            // the transaction does not serialize or sign (it would otherwise
            // send the value to 0x00..00).
            checkResult(setToFn(handle, toPtr));
          } finally {
            wasm._hd_dealloc(toPtr);
          }
        }

        if (fields.value !== undefined) {
          // Always the 256-bit setter: wei amounts reach 2^64 at ~18.45 ETH.
          const setValueFn = requireWasmFunction(wasm, '_hd_eth_tx_set_value_256');
          const valueBytes = new Uint8Array(32);
          for (let i = 31, v = fields.value; i >= 0 && v > 0n; i--, v >>= 8n) valueBytes[i] = Number(v & 0xffn);
          const { ptr: valuePtr, len: valueLen } = allocAndCopy(wasm, valueBytes);
          try {
            setValueFn(handle, valuePtr);
          } finally {
            wasm._hd_dealloc(valuePtr);
          }
        }

        if (fields.data !== undefined) {
          const setDataFn = requireWasmFunction(wasm, '_hd_eth_tx_set_data');
          const { ptr: dataPtr, len: dataLen } = allocAndCopy(wasm, fields.data);
          try {
            setDataFn(handle, dataPtr, dataLen);
          } finally {
            wasm._hd_dealloc(dataPtr);
          }
        }
      },

      // EIP-1559 transaction. nonce, chainId (>= 1), gasLimit, maxFeePerGas
      // and maxPriorityFeePerGas are required.
      createEIP1559(params) {
        const createFn = requireWasmFunction(wasm, '_hd_eth_tx_create_eip1559');
        const fields = this._readParams(params);
        if (fields.gasLimit === undefined) {
          throw new HDWalletError(ErrorCode.INVALID_ARGUMENT, 'gasLimit is required');
        }
        const maxPriorityFeePerGas = V.unsignedBigInt(fields.maxPriorityFeePerGas, 'maxPriorityFeePerGas', 64);
        const maxFeePerGas = V.unsignedBigInt(fields.maxFeePerGas, 'maxFeePerGas', 64);
        const handle = createFn(fields.chainId, maxPriorityFeePerGas, maxFeePerGas);
        if (!handle) throw new HDWalletError(ErrorCode.NOT_SUPPORTED);

        try {
          this._setCommonFields(handle, fields);
        } catch (error) {
          requireWasmFunction(wasm, '_hd_eth_tx_destroy')(handle);
          throw error;
        }

        return this._makeTxObj(handle);
      },

      // Legacy (EIP-155) transaction. nonce and chainId (>= 1) are required.
      // Before 2.1.1 every field but chainId was ignored, so the declared
      // to/value/gasPrice never reached the signed bytes.
      create(params = {}) {
        const createFn = requireWasmFunction(wasm, '_hd_eth_tx_create');
        const fields = this._readParams(params);
        const handle = createFn(fields.chainId);
        if (!handle) throw new HDWalletError(ErrorCode.NOT_SUPPORTED);

        try {
          if (fields.gasPrice !== undefined) requireWasmFunction(wasm, '_hd_eth_tx_set_gas_price')(handle, fields.gasPrice);
          this._setCommonFields(handle, fields);
        } catch (error) {
          requireWasmFunction(wasm, '_hd_eth_tx_destroy')(handle);
          throw error;
        }

        return this._makeTxObj(handle);
      },

      parse(data) {
        const parseFn = requireWasmFunction(wasm, '_hd_eth_tx_parse');
        V.bytes(data, 'data');
        const { ptr: dataPtr, len: dataLen } = allocAndCopy(wasm, data);
        try {
          const handle = parseFn(dataPtr, dataLen);
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
      V.publicKey(wasm, publicKey, Curve.SECP256K1, 'publicKey', [33]);
      V.string(prefix, 'prefix');
      const { ptr: pubPtr, len: pubLen } = allocAndCopy(wasm, publicKey);
      const prefixPtr = allocString(wasm, prefix);
      const outPtr = wasm._hd_alloc(128);
      try {
        const result = getAddressFn(pubPtr, pubLen, prefixPtr, outPtr, 128);
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
      if (prefix !== undefined && prefix !== null && prefix !== '') V.string(prefix, 'prefix');
      if (!V.isCString(V.string(address, 'address'))) return false;
      if (prefix && !V.isCString(prefix)) return false;
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
      if (doc === null || typeof doc !== 'object') {
        throw new HDWalletError(ErrorCode.INVALID_ARGUMENT, 'sign doc must be an object');
      }
      V.privateKey(wasm, privateKey, Curve.SECP256K1);
      // Serialize the sign doc to canonical JSON bytes
      const jsonStr = JSON.stringify(doc);
      const enc = new TextEncoder();
      const docBytes = enc.encode(jsonStr);
      const { ptr: docPtr, len: docLen } = allocAndCopy(wasm, docBytes);
      const { ptr: keyPtr, len: keyLen } = allocAndCopy(wasm, privateKey);
      const sigPtr = wasm._hd_alloc(64);
      try {
        const result = signAminoFn(docPtr, docLen, keyPtr, sigPtr, 64);
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
        V.bytes(signDocBytes, 'signDocBytes');
        V.privateKey(wasm, privateKey, Curve.SECP256K1);
        const { ptr: docPtr, len: docLen } = allocAndCopy(wasm, signDocBytes);
        const { ptr: keyPtr, len: keyLen } = allocAndCopy(wasm, privateKey);
        const sigPtr = wasm._hd_alloc(64);
        try {
          const result = signDirectFn(docPtr, docLen, keyPtr, sigPtr, 64);
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
        if (args.length !== 5) {
          throw new HDWalletError(ErrorCode.INVALID_ARGUMENT, 'signDirect takes (signDocBytes, privateKey) or (bodyBytes, authInfoBytes, chainId, accountNumber, privateKey)');
        }
        const [bodyArg, authInfoArg, chainId, accountNumber, privateKey] = args;
        // Copies read once: the lengths below size the SignDoc.
        const bodyBytes = V.snapshot(bodyArg, 'bodyBytes');
        const authInfoBytes = V.snapshot(authInfoArg, 'authInfoBytes');
        V.string(chainId, 'chainId');
        V.unsignedBigInt(accountNumber, 'accountNumber', 64);
        V.privateKey(wasm, privateKey, Curve.SECP256K1);
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
        const { ptr: docPtr, len: docLen } = allocAndCopy(wasm, signDoc);
        const { ptr: keyPtr, len: keyLen } = allocAndCopy(wasm, privateKey);
        const sigPtr = wasm._hd_alloc(64);
        try {
          const result = signDirectFn(docPtr, docLen, keyPtr, sigPtr, 64);
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
      V.bytes(message, 'message');
      V.bytes(signature, 'signature');
      V.bytes(publicKey, 'publicKey');
      if (V.byteLength(signature) !== 64 || V.byteLength(publicKey) !== 33) return false;
      const { ptr: msgPtr, len: msgLen } = allocAndCopy(wasm, message);
      const { ptr: sigPtr, len: sigLen } = allocAndCopy(wasm, signature);
      const { ptr: pubPtr, len: pubLen } = allocAndCopy(wasm, publicKey);
      try {
        return verifyFn(msgPtr, msgLen, sigPtr, sigLen, pubPtr, pubLen) === 1;
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
      // Any 32 bytes: Solana addresses include off-curve program addresses.
      V.bytesOfLength(publicKey, 32, 'publicKey', ErrorCode.INVALID_PUBLIC_KEY);
      const { ptr: pubPtr, len: pubLen } = allocAndCopy(wasm, publicKey);
      const outPtr = wasm._hd_alloc(64);
      try {
        const result = getAddressFn(pubPtr, pubLen, outPtr, 64);
        checkResult(result);
        return readString(wasm, outPtr);
      } finally {
        wasm._hd_dealloc(pubPtr);
        wasm._hd_dealloc(outPtr);
      }
    },

    validateAddress(address) {
      const validateFn = requireWasmFunction(wasm, '_hd_sol_validate_address');
      if (!V.isCString(V.string(address, 'address'))) return false;
      const addrPtr = allocString(wasm, address);
      try {
        return validateFn(addrPtr) === 0;
      } finally {
        wasm._hd_dealloc(addrPtr);
      }
    },

    signMessage(message, privateKey) {
      const signFn = requireWasmFunction(wasm, '_hd_sol_sign_message');
      V.bytes(message, 'message');
      V.privateKey(wasm, privateKey, Curve.ED25519);
      const { ptr: msgPtr, len: msgLen } = allocAndCopy(wasm, message);
      const { ptr: keyPtr, len: keyLen } = allocAndCopy(wasm, privateKey);
      const sigPtr = wasm._hd_alloc(64);
      try {
        const result = signFn(msgPtr, msgLen, keyPtr, sigPtr, 64);
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
      V.bytes(message, 'message');
      V.bytes(signature, 'signature');
      V.bytes(publicKey, 'publicKey');
      if (V.byteLength(signature) !== 64 || V.byteLength(publicKey) !== 32) return false;
      const { ptr: msgPtr, len: msgLen } = allocAndCopy(wasm, message);
      const { ptr: sigPtr, len: sigLen } = allocAndCopy(wasm, signature);
      const { ptr: pubPtr, len: pubLen } = allocAndCopy(wasm, publicKey);
      try {
        return verifyFn(msgPtr, msgLen, sigPtr, sigLen, pubPtr, pubLen) === 1;
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
      V.privateKey(wasm, privateKey, Curve.ED25519);
      const { ptr: keyPtr, len: keyLen } = allocAndCopy(wasm, privateKey);
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
      // The export reads a 32-byte program id; Solana allows up to 16 seeds
      // of up to 32 bytes.
      V.bytesOfLength(programId, 32, 'programId', ErrorCode.INVALID_PUBLIC_KEY);
      // The array and each seed are read once (an array or a Proxy could
      // answer a second read differently): the copies' lengths go to the heap.
      const count = Array.isArray(seeds) ? seeds.length : -1;
      if (!Number.isInteger(count) || count < 0 || count > 16) {
        throw new HDWalletError(ErrorCode.INVALID_ARGUMENT, 'seeds must be an array of at most 16 seeds');
      }
      const seedCopies = [];
      for (let i = 0; i < count; i++) {
        seedCopies.push(V.snapshot(V.bytesInRange(seeds[i], 0, 32, 'seed'), 'seed'));
      }
      const { ptr: progPtr } = allocAndCopy(wasm, programId);
      let totalLen = 0;
      for (const s of seedCopies) totalLen += s.length;
      const flatSeeds = new Uint8Array(totalLen);
      let off = 0;
      for (const s of seedCopies) { flatSeeds.set(s, off); off += s.length; }
      const { ptr: seedsPtr } = totalLen > 0 ? allocAndCopy(wasm, flatSeeds) : { ptr: 0 };
      const seedLensPtr = wasm._hd_alloc(seedCopies.length * 4);
      for (let i = 0; i < seedCopies.length; i++) {
        wasm.setValue(seedLensPtr + i * 4, seedCopies[i].length, 'i32');
      }
      const addrPtr = wasm._hd_alloc(64);
      const bumpPtr = wasm._hd_alloc(1);
      try {
        const result = fn(progPtr, seedsPtr || 0, totalLen, seedLensPtr, seedCopies.length, addrPtr, 64, bumpPtr);
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
      V.bytesOfLength(publicKey, 32, 'publicKey', ErrorCode.INVALID_PUBLIC_KEY);
      V.integer(ss58Prefix, 'ss58Prefix', 0, 16383);
      const { ptr: pubPtr, len: pubLen } = allocAndCopy(wasm, publicKey);
      const outPtr = wasm._hd_alloc(64);
      try {
        const result = getAddressFn(pubPtr, pubLen, ss58Prefix, outPtr, 64);
        checkResult(result);
        return readString(wasm, outPtr);
      } finally {
        wasm._hd_dealloc(pubPtr);
        wasm._hd_dealloc(outPtr);
      }
    },

    validateAddress(address, prefix) {
      const validateFn = requireWasmFunction(wasm, '_hd_dot_validate_address');
      if (prefix !== undefined) V.integer(prefix, 'prefix', 0, 16383);
      if (!V.isCString(V.string(address, 'address'))) return false;
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
      V.integer(newPrefix, 'newPrefix', 0, 16383);
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
      V.privateKey(wasm, privateKey, Curve.ED25519);
      const { ptr: keyPtr, len: keyLen } = allocAndCopy(wasm, privateKey);
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
      V.bytes(message, 'message');
      V.privateKey(wasm, privateKey, Curve.ED25519);
      const { ptr: msgPtr, len: msgLen } = allocAndCopy(wasm, message);
      const { ptr: keyPtr, len: keyLen } = allocAndCopy(wasm, privateKey);
      const sigPtr = wasm._hd_alloc(64);
      try {
        const result = signFn(msgPtr, msgLen, keyPtr, sigPtr, 64);
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
      V.bytes(message, 'message');
      V.bytes(signature, 'signature');
      V.bytes(publicKey, 'publicKey');
      if (V.byteLength(signature) !== 64 || V.byteLength(publicKey) !== 32) return false;
      const { ptr: msgPtr, len: msgLen } = allocAndCopy(wasm, message);
      const { ptr: sigPtr, len: sigLen } = allocAndCopy(wasm, signature);
      const { ptr: pubPtr, len: pubLen } = allocAndCopy(wasm, publicKey);
      try {
        return verifyFn(msgPtr, msgLen, sigPtr, sigLen, pubPtr, pubLen) === 1;
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
            const { ptr: txPtr, len: txLen } = allocAndCopy(wasm, transaction);
            const sigPtr = wasm._hd_alloc(128);
            try {
              const result = wasm._hd_hw_sign_transaction(this._handle, pathPtr, txPtr, txLen, sigPtr, 128);
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
          const { ptr: seedPtr, len: seedLen } = allocAndCopy(wasm, seed);
          const namePtr = name ? allocString(wasm, name) : 0;
          try {
            const ptr = addWalletFn(this._handle, seedPtr, seedLen, namePtr);
            return readString(wasm, ptr);
          } finally {
            wasm._hd_secure_wipe(seedPtr, seedLen);
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
          const { ptr: txPtr, len: txLen } = allocAndCopy(wasm, transaction);
          const sigPtr = wasm._hd_alloc(128);
          try {
            const result = signTransactionFn(this._handle, idPtr, pathPtr, txPtr, txLen, sigPtr, 128);
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
          const { ptr: msgPtr, len: msgLen } = allocAndCopy(wasm, message);
          const sigPtr = wasm._hd_alloc(128);
          try {
            const result = signMessageFn(this._handle, idPtr, pathPtr, msgPtr, msgLen, sigPtr, 128);
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
      const { ptr: dataPtr, len: dataLen } = allocAndCopy(wasm, data);
      const hashPtr = wasm._hd_alloc(32);
      try {
        checkLength(wasm._hd_hash_sha256(dataPtr, dataLen, hashPtr, 32), 32);
        return readBytes(wasm, hashPtr, 32);
      } finally {
        wasm._hd_dealloc(dataPtr);
        wasm._hd_dealloc(hashPtr);
      }
    },

    sha512(data) {
      const { ptr: dataPtr, len: dataLen } = allocAndCopy(wasm, data);
      const hashPtr = wasm._hd_alloc(64);
      try {
        checkLength(wasm._hd_hash_sha512(dataPtr, dataLen, hashPtr, 64), 64);
        return readBytes(wasm, hashPtr, 64);
      } finally {
        wasm._hd_dealloc(dataPtr);
        wasm._hd_dealloc(hashPtr);
      }
    },

    keccak256(data) {
      const { ptr: dataPtr, len: dataLen } = allocAndCopy(wasm, data);
      const hashPtr = wasm._hd_alloc(32);
      try {
        checkLength(wasm._hd_hash_keccak256(dataPtr, dataLen, hashPtr, 32), 32);
        return readBytes(wasm, hashPtr, 32);
      } finally {
        wasm._hd_dealloc(dataPtr);
        wasm._hd_dealloc(hashPtr);
      }
    },

    ripemd160(data) {
      const { ptr: dataPtr, len: dataLen } = allocAndCopy(wasm, data);
      const hashPtr = wasm._hd_alloc(20);
      try {
        checkLength(wasm._hd_hash_ripemd160(dataPtr, dataLen, hashPtr, 20), 20);
        return readBytes(wasm, hashPtr, 20);
      } finally {
        wasm._hd_dealloc(dataPtr);
        wasm._hd_dealloc(hashPtr);
      }
    },

    hash160(data) {
      const { ptr: dataPtr, len: dataLen } = allocAndCopy(wasm, data);
      const hashPtr = wasm._hd_alloc(20);
      try {
        checkLength(wasm._hd_hash_hash160(dataPtr, dataLen, hashPtr, 20), 20);
        return readBytes(wasm, hashPtr, 20);
      } finally {
        wasm._hd_dealloc(dataPtr);
        wasm._hd_dealloc(hashPtr);
      }
    },

    blake2b(data, outputLength = 32) {
      // The export checks the size against its 64-byte buffer.
      V.integer(outputLength, 'outputLength', 0, 0x7fffffff);
      const { ptr: dataPtr, len: dataLen } = allocAndCopy(wasm, data);
      const hashPtr = wasm._hd_alloc(64);
      try {
        checkLength(wasm._hd_hash_blake2b(dataPtr, dataLen, hashPtr, 64, outputLength), outputLength);
        return readBytes(wasm, hashPtr, outputLength);
      } finally {
        wasm._hd_dealloc(dataPtr);
        wasm._hd_dealloc(hashPtr);
      }
    },

    blake2s(data, outputLength = 32) {
      // The export checks the size against its 32-byte buffer.
      V.integer(outputLength, 'outputLength', 0, 0x7fffffff);
      const { ptr: dataPtr, len: dataLen } = allocAndCopy(wasm, data);
      const hashPtr = wasm._hd_alloc(32);
      try {
        checkLength(wasm._hd_hash_blake2s(dataPtr, dataLen, hashPtr, 32, outputLength), outputLength);
        return readBytes(wasm, hashPtr, outputLength);
      } finally {
        wasm._hd_dealloc(dataPtr);
        wasm._hd_dealloc(hashPtr);
      }
    },

    // Key derivation (sync - uses Crypto++ in WASM)
    hkdf(ikm, salt, info, length) {
      V.bytes(ikm, 'ikm');
      V.bytes(salt, 'salt');
      V.bytes(info, 'info');
      // The export refuses more than 255 blocks of output.
      V.integer(length, 'length', 1, 0x3fffffff);
      const { ptr: ikmPtr, len: ikmLen } = allocAndCopy(wasm, ikm);
      const { ptr: saltPtr, len: saltLen } = allocAndCopy(wasm, salt);
      const { ptr: infoPtr, len: infoLen } = allocAndCopy(wasm, info);
      const outPtr = wasm._hd_alloc(length);
      try {
        checkLength(wasm._hd_kdf_hkdf(ikmPtr, ikmLen, saltPtr, saltLen, infoPtr, infoLen, outPtr, length), length);
        return readBytes(wasm, outPtr, length);
      } finally {
        wasm._hd_secure_wipe(ikmPtr, ikmLen);
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
        V.bytesOfLength(key, 32, 'Key');
        V.bytes(plaintext, 'plaintext');
        V.bytesOfLength(iv, 12, 'IV');
        V.bytes(aad, 'aad');

        const { ptr: keyPtr, len: keyLen } = allocAndCopy(wasm, key);
        const { ptr: ptPtr, len: ptLen } = allocAndCopy(wasm, plaintext);
        const { ptr: ivPtr, len: ivLen } = allocAndCopy(wasm, iv);
        const { ptr: aadPtr, len: aadLen } = V.byteLength(aad) > 0 ? allocAndCopy(wasm, aad) : { ptr: 0, len: 0 };
        const ctPtr = wasm._hd_alloc(ptLen);
        const tagPtr = wasm._hd_alloc(16);

        try {
          const result = wasm._hd_aes_gcm_encrypt(
            keyPtr, keyLen,
            ptPtr, ptLen,
            ivPtr, ivLen,
            aadPtr, aadLen,
            ctPtr, tagPtr
          );
          checkLength(result, ptLen);

          return {
            ciphertext: readBytes(wasm, ctPtr, ptLen),
            tag: readBytes(wasm, tagPtr, 16)
          };
        } finally {
          wasm._hd_secure_wipe(keyPtr, keyLen);
          wasm._hd_secure_wipe(ptPtr, ptLen);
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
        V.bytesOfLength(key, 32, 'Key');
        V.bytes(ciphertext, 'ciphertext');
        V.bytesOfLength(tag, 16, 'Tag');
        V.bytesOfLength(iv, 12, 'IV');
        V.bytes(aad, 'aad');

        const { ptr: keyPtr, len: keyLen } = allocAndCopy(wasm, key);
        const { ptr: ctPtr, len: ctLen } = allocAndCopy(wasm, ciphertext);
        const { ptr: ivPtr, len: ivLen } = allocAndCopy(wasm, iv);
        const { ptr: tagPtr, len: tagLen } = allocAndCopy(wasm, tag);
        const { ptr: aadPtr, len: aadLen } = V.byteLength(aad) > 0 ? allocAndCopy(wasm, aad) : { ptr: 0, len: 0 };
        const ptPtr = wasm._hd_alloc(ctLen);

        try {
          const result = wasm._hd_aes_gcm_decrypt(
            keyPtr, keyLen,
            ctPtr, ctLen,
            ivPtr, ivLen,
            aadPtr, aadLen,
            tagPtr, ptPtr
          );
          if (result !== ctLen) {
            const code = nativeErrorCode(result);
            throw new HDWalletError(
              code,
              code === ErrorCode.VERIFICATION_FAILED ? 'AES-GCM authentication failed' : undefined
            );
          }

          return readBytes(wasm, ptPtr, ctLen);
        } finally {
          wasm._hd_secure_wipe(keyPtr, keyLen);
          wasm._hd_dealloc(keyPtr);
          wasm._hd_dealloc(ctPtr);
          wasm._hd_dealloc(ivPtr);
          wasm._hd_dealloc(tagPtr);
          if (aadPtr) wasm._hd_dealloc(aadPtr);
          wasm._hd_secure_wipe(ptPtr, ctLen);
          wasm._hd_dealloc(ptPtr);
        }
      }
    },

    /** Generate cryptographically secure random bytes */
    getRandomBytes(length) {
      V.integer(length, 'length', 0, 0x7fffffff);
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
      V.bytes(password, 'password');
      V.bytes(salt, 'salt');
      V.integer(iterations, 'iterations', 1, 0x7fffffff);
      V.integer(length, 'length', 1, 0x3fffffff);
      const { ptr: pwdPtr, len: pwdLen } = allocAndCopy(wasm, password);
      const { ptr: saltPtr, len: saltLen } = allocAndCopy(wasm, salt);
      const outPtr = wasm._hd_alloc(length);
      try {
        checkLength(wasm._hd_kdf_pbkdf2(pwdPtr, pwdLen, saltPtr, saltLen, iterations, outPtr, length), length);
        return readBytes(wasm, outPtr, length);
      } finally {
        wasm._hd_secure_wipe(pwdPtr, pwdLen);
        wasm._hd_dealloc(pwdPtr);
        wasm._hd_dealloc(saltPtr);
        wasm._hd_dealloc(outPtr);
      }
    },

    scrypt(password, salt, n, r, p, length) {
      V.bytes(password, 'password');
      V.bytes(salt, 'salt');
      V.integer(n, 'n', 2, Number.MAX_SAFE_INTEGER);
      V.integer(r, 'r', 1, 0x7fffffff);
      V.integer(p, 'p', 1, 0x7fffffff);
      V.integer(length, 'length', 1, 0x3fffffff);
      const { ptr: pwdPtr, len: pwdLen } = allocAndCopy(wasm, password);
      const { ptr: saltPtr, len: saltLen } = allocAndCopy(wasm, salt);
      const outPtr = wasm._hd_alloc(length);
      try {
        checkLength(wasm._hd_kdf_scrypt(pwdPtr, pwdLen, saltPtr, saltLen, BigInt(n), r, p, outPtr, length), length);
        return readBytes(wasm, outPtr, length);
      } finally {
        wasm._hd_secure_wipe(pwdPtr, pwdLen);
        wasm._hd_dealloc(pwdPtr);
        wasm._hd_dealloc(saltPtr);
        wasm._hd_dealloc(outPtr);
      }
    },

    // Encoding
    encodeBase58(data) {
      V.bytes(data, 'data');
      const encodeBase58Fn = getWasmFunction(wasm, '_hd_encode_base58');
      if (!encodeBase58Fn) {
        return encodeBase58Fallback(data);
      }
      const { ptr: dataPtr, len: dataLen } = allocAndCopy(wasm, data);
      try {
        const ptr = encodeBase58Fn(dataPtr, dataLen);
        return readString(wasm, ptr);
      } finally {
        wasm._hd_dealloc(dataPtr);
      }
    },

    decodeBase58(str) {
      V.string(str, 'str');
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
      V.bytes(data, 'data');
      const encodeBase58CheckFn = getWasmFunction(wasm, '_hd_encode_base58check');
      if (!encodeBase58CheckFn) {
        const checksum = this.sha256(this.sha256(data)).slice(0, 4);
        const payload = new Uint8Array(V.byteLength(data) + checksum.length);
        payload.set(data, 0);
        payload.set(checksum, V.byteLength(data));
        return encodeBase58Fallback(payload);
      }
      const { ptr: dataPtr, len: dataLen } = allocAndCopy(wasm, data);
      try {
        const ptr = encodeBase58CheckFn(dataPtr, dataLen);
        return readString(wasm, ptr);
      } finally {
        wasm._hd_dealloc(dataPtr);
      }
    },

    decodeBase58Check(str) {
      V.string(str, 'str');
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
      V.string(hrp, 'hrp');
      V.bytes(data, 'data');
      const encodeBech32Fn = getWasmFunction(wasm, '_hd_encode_bech32');
      if (!encodeBech32Fn) {
        return encodeBech32Fallback(hrp, data);
      }
      const hrpPtr = allocString(wasm, hrp);
      const { ptr: dataPtr, len: dataLen } = allocAndCopy(wasm, data);
      try {
        const ptr = encodeBech32Fn(hrpPtr, dataPtr, dataLen);
        return readString(wasm, ptr);
      } finally {
        wasm._hd_dealloc(hrpPtr);
        wasm._hd_dealloc(dataPtr);
      }
    },

    decodeBech32(str) {
      V.string(str, 'str');
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
      return encodeHexFallback(V.bytes(data, 'data'));
    },

    decodeHex(str) {
      return decodeHexFallback(V.string(str, 'str'));
    },

    encodeBase64(data) {
      return encodeBase64Fallback(V.bytes(data, 'data'));
    },

    decodeBase64(str) {
      return decodeBase64Fallback(V.string(str, 'str'));
    },

    // Memory
    /**
     * Zero, in place, the bytes under any typed array, DataView, ArrayBuffer
     * or SharedArrayBuffer (any realm), read from its internal slots, never
     * from overridable properties. Throws INVALID_ARGUMENT for anything else
     * and for a detached buffer (its bytes are no longer here to wipe).
     */
    secureWipe(data) {
      const region = binaryRegion(data);
      if (region === null) {
        throw new HDWalletError(ErrorCode.INVALID_ARGUMENT,
          'secureWipe takes a typed array, DataView, ArrayBuffer or SharedArrayBuffer');
      }
      if (region.detached) {
        throw new HDWalletError(ErrorCode.INVALID_ARGUMENT, 'secureWipe: the buffer is detached');
      }
      TYPED_ARRAY_FILL.call(new Uint8Array(region.buffer, region.offset, region.length), 0);
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
      V.curve(curve);
      return callDynamicBytesResult(
        wasm,
        (outPtr, outLenPtr) => fn(curve, outPtr, outLenPtr),
        { wipeOutput: true }
      );
    },

    exportPrivateKeyPem(curve, privateKey) {
      const fn = requireWasmFunction(wasm, '_hd_x509_export_private_key_pem');
      const keyBytes = V.privateKey(wasm, toUint8Array(privateKey, 'privateKey'), curve);
      const { ptr: keyPtr, len: keyLen } = allocAndCopy(wasm, keyBytes);
      try {
        return callDynamicStringResult(
          wasm,
          (outPtr, outLenPtr) => fn(curve, keyPtr, keyLen, outPtr, outLenPtr),
          { wipeOutput: true }
        );
      } finally {
        wasm._hd_secure_wipe(keyPtr, keyLen);
        wasm._hd_dealloc(keyPtr);
      }
    },

    importPrivateKeyPem(curve, pem) {
      const fn = requireWasmFunction(wasm, '_hd_x509_import_private_key_pem');
      V.curve(curve);
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
      const keyBytes = V.privateKey(wasm, toUint8Array(certificatePrivateKey, 'certificatePrivateKey'),
        certificateCurve, 'certificatePrivateKey');
      const { ptr: keyPtr, len: keyLen } = allocAndCopy(wasm, keyBytes);

      try {
        const bytes = callDynamicBytesResult(
          wasm,
          (outPtr, outLenPtr) => fn(
            optionsPtr,
            certificateCurve,
            keyPtr,
            keyLen,
            outputEncoding,
            outPtr,
            outLenPtr
          )
        );
        return decodeX509CertificateOutput(bytes, outputEncoding);
      } finally {
        wasm._hd_secure_wipe(optionsPtr, optionsLen);
        wasm._hd_secure_wipe(keyPtr, keyLen);
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
      const issuerKeyBytes = V.privateKey(wasm, toUint8Array(issuerPrivateKey, 'issuerPrivateKey'),
        issuerCurve, 'issuerPrivateKey');
      const { ptr: issuerKeyPtr, len: issuerKeyLen } = allocAndCopy(wasm, issuerKeyBytes);
      const issuerCertBytes = normalizeX509CertificateInput(
        issuerCertificate,
        issuerCertificateEncoding,
        'issuerCertificate'
      );
      const { ptr: issuerCertPtr, len: issuerCertLen } = allocAndCopy(wasm, issuerCertBytes);
      const subjectKeyBytes = V.privateKey(wasm, toUint8Array(subjectPrivateKey, 'subjectPrivateKey'),
        subjectCurve, 'subjectPrivateKey');
      const { ptr: subjectKeyPtr, len: subjectKeyLen } = allocAndCopy(wasm, subjectKeyBytes);

      try {
        const bytes = callDynamicBytesResult(
          wasm,
          (outPtr, outLenPtr) => fn(
            optionsPtr,
            issuerCurve,
            issuerKeyPtr,
            issuerKeyLen,
            issuerCertPtr,
            issuerCertLen,
            issuerCertificateEncoding,
            subjectCurve,
            subjectKeyPtr,
            subjectKeyLen,
            outputEncoding,
            outPtr,
            outLenPtr
          )
        );
        return decodeX509CertificateOutput(bytes, outputEncoding);
      } finally {
        wasm._hd_secure_wipe(optionsPtr, optionsLen);
        wasm._hd_secure_wipe(issuerKeyPtr, issuerKeyLen);
        wasm._hd_secure_wipe(subjectKeyPtr, subjectKeyLen);
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
      const { ptr: certPtr, len: certLen } = allocAndCopy(wasm, certBytes);

      try {
        const bytes = callDynamicBytesResult(
          wasm,
          (outPtr, outLenPtr) => fn(
            certPtr,
            certLen,
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
      const { ptr: certPtr, len: certLen } = allocAndCopy(wasm, certBytes);

      try {
        return normalizeParsedCertificateJson(JSON.parse(
          callDynamicStringResult(
            wasm,
            (outPtr, outLenPtr) => fn(certPtr, certLen, inputEncoding, outPtr, outLenPtr)
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
      const { ptr: certPtr, len: certLen } = allocAndCopy(wasm, certBytes);

      try {
        const result = fn(certPtr, certLen, inputEncoding);
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
      const { ptr: certPtr, len: certLen } = allocAndCopy(wasm, certBytes);
      const keyBytes = V.privateKey(wasm, toUint8Array(privateKey, 'privateKey'), privateKeyCurve);
      V.string(password, 'password');
      V.string(friendlyName, 'friendlyName');
      const { ptr: keyPtr, len: keyLen } = allocAndCopy(wasm, keyBytes);
      const passwordPtr = allocString(wasm, password);
      const passwordLen = wasm.lengthBytesUTF8(password) + 1;
      const friendlyNamePtr = allocString(wasm, friendlyName);
      const chainBytes = normalizePemInput(chainPem, 'chainPem');
      const { ptr: chainPtr, len: chainLen } = V.byteLength(chainBytes) > 0 ? allocAndCopy(wasm, chainBytes) : { ptr: 0, len: 0 };

      try {
        return callDynamicBytesResult(
          wasm,
          (outPtr, outLenPtr) => fn(
            certPtr,
            certLen,
            certificateEncoding,
            privateKeyCurve,
            keyPtr,
            keyLen,
            passwordPtr,
            friendlyNamePtr,
            chainPtr,
            chainLen,
            outPtr,
            outLenPtr
          ),
          { wipeOutput: true }
        );
      } finally {
        wasm._hd_secure_wipe(keyPtr, keyLen);
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
      V.string(password, 'password');
      const { ptr: bundlePtr, len: bundleLen } = allocAndCopy(wasm, bundleBytes);
      const passwordPtr = allocString(wasm, password);
      const passwordLen = wasm.lengthBytesUTF8(password) + 1;

      try {
        return normalizePkcs12ImportJson(JSON.parse(
          callDynamicStringResult(
            wasm,
            (outPtr, outLenPtr) => fn(bundlePtr, bundleLen, passwordPtr, outPtr, outLenPtr),
            { wipeOutput: true }
          )
        ));
      } finally {
        wasm._hd_secure_wipe(bundlePtr, bundleLen);
        wasm._hd_secure_wipe(passwordPtr, passwordLen);
        wasm._hd_dealloc(bundlePtr);
        wasm._hd_dealloc(passwordPtr);
      }
    }
  };

  // ==========================================================================
  // ECIES API
  // ==========================================================================

  const CURVE_NAME_MAP = Object.freeze({ secp256k1: 0, ed25519: 1, p256: 2, p384: 3, x25519: 4 });

  // An ECIES curve: a Curve id or its name; an unknown name is an error, never
  // secp256k1 by default.
  function eciesCurve(curve) {
    const curveId = typeof curve === 'string' && Object.hasOwn(CURVE_NAME_MAP, curve)
      ? CURVE_NAME_MAP[curve]
      : curve;
    return V.curve(curveId, [Curve.SECP256K1, Curve.P256, Curve.P384, Curve.X25519]);
  }

  const ecies = {
    encrypt(curve, recipientPublicKey, plaintext, aad = new Uint8Array(0)) {
      const curveId = eciesCurve(curve);
      V.publicKey(wasm, recipientPublicKey, curveId, 'recipientPublicKey');
      V.bytes(plaintext, 'plaintext');
      V.bytes(aad, 'aad');
      const encryptFn = requireWasmFunction(wasm, '_hd_ecies_encrypt');
      const overheadFn = requireWasmFunction(wasm, '_hd_ecies_overhead');
      const overhead = overheadFn(curveId);
      if (!Number.isInteger(overhead) || overhead <= 0) {
        throw new HDWalletError(ErrorCode.NOT_SUPPORTED, 'ECIES is not supported on this curve');
      }
      const outSize = V.byteLength(plaintext) + overhead;
      const { ptr: pubPtr, len: pubLen } = allocAndCopy(wasm, recipientPublicKey);
      const { ptr: ptPtr, len: ptLen } = allocAndCopy(wasm, plaintext);
      const { ptr: aadPtr, len: aadLen } = V.byteLength(aad) > 0 ? allocAndCopy(wasm, aad) : { ptr: 0, len: 0 };
      const outPtr = wasm._hd_alloc(outSize);
      try {
        const result = checkLength(encryptFn(curveId, pubPtr, pubLen,
          ptPtr, ptLen, aadPtr, aadLen, outPtr, outSize), outSize);
        return readBytes(wasm, outPtr, result);
      } finally {
        wasm._hd_dealloc(pubPtr);
        wasm._hd_secure_wipe(ptPtr, ptLen);
        wasm._hd_dealloc(ptPtr);
        if (aadPtr) wasm._hd_dealloc(aadPtr);
        wasm._hd_dealloc(outPtr);
      }
    },

    decrypt(curve, recipientPrivateKey, ciphertext, aad = new Uint8Array(0)) {
      const curveId = eciesCurve(curve);
      V.privateKey(wasm, recipientPrivateKey, curveId, 'recipientPrivateKey');
      V.bytes(ciphertext, 'ciphertext');
      V.bytes(aad, 'aad');
      const decryptFn = requireWasmFunction(wasm, '_hd_ecies_decrypt');
      const { ptr: privPtr, len: privLen } = allocAndCopy(wasm, recipientPrivateKey);
      const { ptr: ctPtr, len: ctLen } = allocAndCopy(wasm, ciphertext);
      const { ptr: aadPtr, len: aadLen } = V.byteLength(aad) > 0 ? allocAndCopy(wasm, aad) : { ptr: 0, len: 0 };
      const outPtr = wasm._hd_alloc(ctLen);
      try {
        const result = checkLengthWithin(decryptFn(curveId, privPtr, privLen,
          ctPtr, ctLen, aadPtr, aadLen, outPtr, ctLen), 0, ctLen);
        return readBytes(wasm, outPtr, result);
      } finally {
        wasm._hd_secure_wipe(privPtr, privLen);
        wasm._hd_dealloc(privPtr);
        wasm._hd_dealloc(ctPtr);
        if (aadPtr) wasm._hd_dealloc(aadPtr);
        wasm._hd_secure_wipe(outPtr, ctLen);
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
      V.bytesOfLength(key, [16, 24, 32], 'key');
      V.bytes(plaintext, 'plaintext');
      V.bytesOfLength(iv, 16, 'iv');
      const { ptr: keyPtr, len: keyLen } = allocAndCopy(wasm, key);
      const { ptr: ptPtr, len: ptLen } = allocAndCopy(wasm, plaintext);
      const { ptr: ivPtr, len: ivLen } = allocAndCopy(wasm, iv);
      const outPtr = wasm._hd_alloc(ptLen);
      try {
        checkLength(encryptFn(keyPtr, keyLen, ptPtr, ptLen, ivPtr, ivLen, outPtr, ptLen), ptLen);
        return readBytes(wasm, outPtr, ptLen);
      } finally {
        wasm._hd_secure_wipe(keyPtr, keyLen);
        wasm._hd_secure_wipe(ptPtr, ptLen);
        wasm._hd_dealloc(keyPtr);
        wasm._hd_dealloc(ptPtr);
        wasm._hd_dealloc(ivPtr);
        wasm._hd_dealloc(outPtr);
      }
    },

    decrypt(key, ciphertext, iv) {
      const decryptFn = requireWasmFunction(wasm, '_hd_aes_ctr_decrypt');
      V.bytesOfLength(key, [16, 24, 32], 'key');
      V.bytes(ciphertext, 'ciphertext');
      V.bytesOfLength(iv, 16, 'iv');
      const { ptr: keyPtr, len: keyLen } = allocAndCopy(wasm, key);
      const { ptr: ctPtr, len: ctLen } = allocAndCopy(wasm, ciphertext);
      const { ptr: ivPtr, len: ivLen } = allocAndCopy(wasm, iv);
      const outPtr = wasm._hd_alloc(ctLen);
      try {
        checkLength(decryptFn(keyPtr, keyLen, ctPtr, ctLen, ivPtr, ivLen, outPtr, ctLen), ctLen);
        return readBytes(wasm, outPtr, ctLen);
      } finally {
        wasm._hd_secure_wipe(keyPtr, keyLen);
        wasm._hd_dealloc(keyPtr);
        wasm._hd_dealloc(ctPtr);
        wasm._hd_dealloc(ivPtr);
        wasm._hd_secure_wipe(outPtr, ctLen);
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
      V.bytesOfLength(seed, 64, 'seed', ErrorCode.INVALID_SEED);
      V.string(path, 'path');
      const { ptr: seedPtr, len: seedLen } = allocAndCopy(wasm, seed);
      const pathPtr = allocString(wasm, path);
      const keyPtr = wasm._hd_alloc(32);
      const chainCodePtr = wasm._hd_alloc(32);
      try {
        const result = deriveFn(seedPtr, seedLen, pathPtr, keyPtr, chainCodePtr);
        checkResult(result);
        return {
          privateKey: readBytes(wasm, keyPtr, 32),
          chainCode: readBytes(wasm, chainCodePtr, 32)
        };
      } finally {
        wasm._hd_secure_wipe(seedPtr, seedLen);
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
      // libp2p key forms: secp256k1 compressed, Ed25519 32 bytes, P-256 SEC1.
      V.curve(curve, [Curve.SECP256K1, Curve.ED25519, Curve.P256]);
      V.publicKey(wasm, publicKey, curve, 'publicKey',
        curve === Curve.SECP256K1 ? [33] : V.publicKeyLengths(curve));
      const { ptr: pubPtr, len: pubLen } = allocAndCopy(wasm, publicKey);
      const outPtr = wasm._hd_alloc(64);
      try {
        const result = checkLengthWithin(
          wasm._hd_libp2p_peer_id(pubPtr, pubLen, curve, outPtr, 64), 1, 64);
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
      // A peer ID multihash: identity or SHA-256, at most 64 bytes.
      const peerId = V.snapshot(V.bytesInRange(peerIdBytes, 2, 64, 'peerIdBytes'), 'peerIdBytes');
      const outPtr = wasm._hd_alloc(128);
      // Build CID in JS (trivial: 2-byte prefix + multihash)
      const cid = new Uint8Array(2 + peerId.length);
      cid[0] = 0x01; // CIDv1
      cid[1] = 0x72; // libp2p-key codec
      cid.set(peerId, 2);
      const { ptr: cidPtr, len: cidLen } = allocAndCopy(wasm, cid);
      try {
        const result = wasm._hd_encode_base36lower(cidPtr, cidLen, outPtr, 128);
        checkResult(result);
        return 'k' + readString(wasm, outPtr);
      } finally {
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
      const peerId = V.snapshot(V.bytesInRange(peerIdBytes, 2, 64, 'peerIdBytes'), 'peerIdBytes');
      const cid = new Uint8Array(2 + peerId.length);
      cid[0] = 0x01; // CIDv1
      cid[1] = 0x72; // libp2p-key codec
      cid.set(peerId, 2);
      const { ptr: cidPtr, len: cidLen } = allocAndCopy(wasm, cid);
      const outPtr = wasm._hd_alloc(128);
      try {
        const result = wasm._hd_encode_base32lower(cidPtr, cidLen, outPtr, 128);
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
      V.bytesInRange(entropy, 1, 0x100000, 'entropy');
      const { ptr: entropyPtr, len: entropyLen } = allocAndCopy(wasm, entropy);
      try {
        wasm._hd_inject_entropy(entropyPtr, entropyLen);
      } finally {
        wasm._hd_secure_wipe(entropyPtr, entropyLen);
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
 * const sig = wallet.curves.secp256k1.sign(message, privateKey); // hashes with SHA-256
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
  compactSignatureToDer,
  bitcoinSignedMessageHash,
  ethereumPersonalSignHash,
} from './epm-attestation.mjs';

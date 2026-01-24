/**
 * SDN Crypto - WASM-based cryptographic operations for FlatBuffer encryption
 * Features:
 * - Ed25519 signing and verification
 * - AES-GCM encryption/decryption
 * - SRI hash verification for WASM integrity
 */

let cryptoModule: CryptoModule | null = null;
let wasmVerified = false;

/**
 * Metrics for crypto WASM loading
 */
export interface CryptoMetrics {
  wasmLoadAttempts: number;
  wasmLoadSuccesses: number;
  wasmLoadFailures: number;
  wasmVerificationSuccesses: number;
  wasmVerificationFailures: number;
  lastLoadTime: number | null;
  lastError: string | null;
}

const cryptoMetrics: CryptoMetrics = {
  wasmLoadAttempts: 0,
  wasmLoadSuccesses: 0,
  wasmLoadFailures: 0,
  wasmVerificationSuccesses: 0,
  wasmVerificationFailures: 0,
  lastLoadTime: null,
  lastError: null,
};

/**
 * Get current crypto metrics
 */
export function getCryptoMetrics(): Readonly<CryptoMetrics> {
  return { ...cryptoMetrics };
}

interface WasmExports {
  _encrypt_bytes?: (keyPtr: number, keyLen: number, dataPtr: number, dataLen: number, outPtr: number, outLen: number) => number;
  _decrypt_bytes?: (keyPtr: number, keyLen: number, dataPtr: number, dataLen: number, outPtr: number, outLen: number) => number;
  _ed25519_sign?: (keyPtr: number, keyLen: number, msgPtr: number, msgLen: number, sigPtr: number) => number;
  _ed25519_verify?: (keyPtr: number, keyLen: number, msgPtr: number, msgLen: number, sigPtr: number, sigLen: number) => number;
  _malloc?: (size: number) => number;
  _free?: (ptr: number) => void;
  memory?: WebAssembly.Memory;
}

interface CryptoModule {
  ready: Promise<void>;
  _encrypt_bytes: (keyPtr: number, keyLen: number, dataPtr: number, dataLen: number, outPtr: number, outLen: number) => number;
  _decrypt_bytes: (keyPtr: number, keyLen: number, dataPtr: number, dataLen: number, outPtr: number, outLen: number) => number;
  _ed25519_sign: (keyPtr: number, keyLen: number, msgPtr: number, msgLen: number, sigPtr: number) => number;
  _ed25519_verify: (keyPtr: number, keyLen: number, msgPtr: number, msgLen: number, sigPtr: number, sigLen: number) => number;
  _malloc: (size: number) => number;
  _free: (ptr: number) => void;
  HEAPU8: Uint8Array;
}

interface CryptoLoadOptions {
  /** Expected SRI hash for integrity verification */
  expectedSri?: string;
  /** Skip integrity verification (not recommended for production) */
  skipIntegrityCheck?: boolean;
}

const WASM_PATHS = [
  './flatc-crypto.wasm',
  '/flatc-crypto.wasm',
  'https://cdn.spacedatanetwork.org/flatc-crypto.wasm',
];

/**
 * Verify WASM integrity using SRI hash
 */
async function verifySri(data: ArrayBuffer, expectedSri: string): Promise<boolean> {
  try {
    // Parse the expected SRI hash (format: "sha384-base64hash")
    const match = expectedSri.match(/^sha(256|384|512)-(.+)$/);
    if (!match) {
      console.warn('Invalid SRI format:', expectedSri);
      return false;
    }

    const algorithm = `SHA-${match[1]}`;
    const expectedHash = match[2];

    // Compute hash of the data
    const hashBuffer = await crypto.subtle.digest(algorithm, data);
    const hashArray = new Uint8Array(hashBuffer);
    const actualHash = btoa(String.fromCharCode(...hashArray));

    // Compare hashes
    if (actualHash !== expectedHash) {
      console.error('Crypto WASM integrity check failed: hash mismatch');
      return false;
    }

    return true;
  } catch (err) {
    console.error('SRI verification error:', err);
    return false;
  }
}

/**
 * Fetch SRI hash for WASM file
 */
async function fetchSriHash(wasmPath: string): Promise<string | null> {
  try {
    const sriPath = wasmPath + '.sri';
    const response = await fetch(sriPath);
    if (!response.ok) return null;
    return (await response.text()).trim();
  } catch {
    return null;
  }
}

/**
 * Check if the crypto WASM was verified
 */
export function isCryptoWasmVerified(): boolean {
  return wasmVerified;
}

/**
 * Load the crypto WASM module with integrity verification
 */
export async function loadCryptoModule(options: CryptoLoadOptions = {}): Promise<boolean> {
  if (cryptoModule) {
    return true;
  }

  cryptoMetrics.wasmLoadAttempts++;

  for (const path of WASM_PATHS) {
    try {
      const response = await fetch(path);
      if (!response.ok) continue;

      const wasmBytes = await response.arrayBuffer();

      // Verify integrity if not skipped
      if (!options.skipIntegrityCheck) {
        let expectedSri = options.expectedSri;

        // Try to fetch SRI hash if not provided
        if (!expectedSri) {
          expectedSri = (await fetchSriHash(path)) ?? undefined;
        }

        if (expectedSri) {
          const isValid = await verifySri(wasmBytes, expectedSri);
          if (!isValid) {
            cryptoMetrics.wasmVerificationFailures++;
            console.error(`Crypto WASM from ${path} failed integrity check`);
            continue;
          }
          cryptoMetrics.wasmVerificationSuccesses++;
          wasmVerified = true;
        } else {
          console.warn(`No SRI hash available for ${path}, loading without verification`);
        }
      }

      const memory = new WebAssembly.Memory({ initial: 256, maximum: 512 });

      const importObject = {
        env: {
          memory,
          abort: () => { throw new Error('WASM abort'); },
        },
        wasi_snapshot_preview1: {
          fd_write: () => 0,
          fd_seek: () => 0,
          fd_close: () => 0,
          proc_exit: () => {},
          environ_sizes_get: () => 0,
          environ_get: () => 0,
        },
      };

      const wasmModule = await WebAssembly.instantiate(wasmBytes, importObject);
      const exports = wasmModule.instance.exports as WasmExports;

      const wasmMemory = exports.memory ?? memory;

      cryptoModule = {
        ready: Promise.resolve(),
        _encrypt_bytes: exports._encrypt_bytes ?? (() => 0),
        _decrypt_bytes: exports._decrypt_bytes ?? (() => 0),
        _ed25519_sign: exports._ed25519_sign ?? (() => 0),
        _ed25519_verify: exports._ed25519_verify ?? (() => 0),
        _malloc: exports._malloc ?? (() => 0),
        _free: exports._free ?? (() => {}),
        HEAPU8: new Uint8Array(wasmMemory.buffer),
      };

      cryptoMetrics.wasmLoadSuccesses++;
      cryptoMetrics.lastLoadTime = Date.now();
      return true;
    } catch (err) {
      cryptoMetrics.lastError = err instanceof Error ? err.message : String(err);
      continue;
    }
  }

  cryptoMetrics.wasmLoadFailures++;
  return false;
}

/**
 * Check if WASM crypto module is loaded
 */
export function isCryptoAvailable(): boolean {
  return cryptoModule !== null;
}

/**
 * Encrypt data using AES-GCM with WASM or Web Crypto API fallback
 */
export async function encrypt(key: Uint8Array, data: Uint8Array): Promise<Uint8Array> {
  // Try WASM first
  if (cryptoModule) {
    try {
      const result = wasmEncrypt(key, data);
      if (result) return result;
    } catch {
      // Fall through to Web Crypto
    }
  }

  // Fallback to Web Crypto API
  return webCryptoEncrypt(key, data);
}

/**
 * Decrypt data using AES-GCM with WASM or Web Crypto API fallback
 */
export async function decrypt(key: Uint8Array, ciphertext: Uint8Array): Promise<Uint8Array> {
  // Try WASM first
  if (cryptoModule) {
    try {
      const result = wasmDecrypt(key, ciphertext);
      if (result) return result;
    } catch {
      // Fall through to Web Crypto
    }
  }

  // Fallback to Web Crypto API
  return webCryptoDecrypt(key, ciphertext);
}

/**
 * Sign data using Ed25519
 */
export async function sign(privateKey: Uint8Array, message: Uint8Array): Promise<Uint8Array> {
  if (!cryptoModule) {
    throw new Error('Crypto module not loaded - Ed25519 signing requires WASM');
  }

  const keyPtr = allocateBytes(privateKey);
  const msgPtr = allocateBytes(message);
  const sigPtr = cryptoModule._malloc(64);

  try {
    const result = cryptoModule._ed25519_sign(
      keyPtr, privateKey.length,
      msgPtr, message.length,
      sigPtr
    );

    if (result === 0) {
      throw new Error('Signing failed');
    }

    return readBytes(sigPtr, 64);
  } finally {
    cryptoModule._free(keyPtr);
    cryptoModule._free(msgPtr);
    cryptoModule._free(sigPtr);
  }
}

/**
 * Verify Ed25519 signature
 */
export async function verify(
  publicKey: Uint8Array,
  message: Uint8Array,
  signature: Uint8Array
): Promise<boolean> {
  if (!cryptoModule) {
    throw new Error('Crypto module not loaded - Ed25519 verification requires WASM');
  }

  const keyPtr = allocateBytes(publicKey);
  const msgPtr = allocateBytes(message);
  const sigPtr = allocateBytes(signature);

  try {
    const result = cryptoModule._ed25519_verify(
      keyPtr, publicKey.length,
      msgPtr, message.length,
      sigPtr, signature.length
    );

    return result !== 0;
  } finally {
    cryptoModule._free(keyPtr);
    cryptoModule._free(msgPtr);
    cryptoModule._free(sigPtr);
  }
}

/**
 * Generate a random encryption key
 */
export function generateKey(): Uint8Array {
  const key = new Uint8Array(32);
  crypto.getRandomValues(key);
  return key;
}

/**
 * Generate random bytes
 */
export function randomBytes(length: number): Uint8Array {
  const bytes = new Uint8Array(length);
  crypto.getRandomValues(bytes);
  return bytes;
}

/**
 * Compute SHA-256 hash
 */
export async function sha256(data: Uint8Array): Promise<Uint8Array> {
  // Create a copy as ArrayBuffer to satisfy TypeScript
  const dataBuffer = new ArrayBuffer(data.length);
  new Uint8Array(dataBuffer).set(data);

  const hashBuffer = await crypto.subtle.digest('SHA-256', dataBuffer);
  return new Uint8Array(hashBuffer);
}

// Internal helpers

function wasmEncrypt(key: Uint8Array, data: Uint8Array): Uint8Array | null {
  if (!cryptoModule) return null;

  const keyPtr = allocateBytes(key);
  const dataPtr = allocateBytes(data);
  const outSize = data.length + 28; // Nonce (12) + tag (16)
  const outPtr = cryptoModule._malloc(outSize);

  try {
    const resultSize = cryptoModule._encrypt_bytes(
      keyPtr, key.length,
      dataPtr, data.length,
      outPtr, outSize
    );

    if (resultSize === 0) return null;
    return readBytes(outPtr, resultSize);
  } finally {
    cryptoModule._free(keyPtr);
    cryptoModule._free(dataPtr);
    cryptoModule._free(outPtr);
  }
}

function wasmDecrypt(key: Uint8Array, ciphertext: Uint8Array): Uint8Array | null {
  if (!cryptoModule) return null;

  const keyPtr = allocateBytes(key);
  const dataPtr = allocateBytes(ciphertext);
  const outSize = ciphertext.length;
  const outPtr = cryptoModule._malloc(outSize);

  try {
    const resultSize = cryptoModule._decrypt_bytes(
      keyPtr, key.length,
      dataPtr, ciphertext.length,
      outPtr, outSize
    );

    if (resultSize === 0) return null;
    return readBytes(outPtr, resultSize);
  } finally {
    cryptoModule._free(keyPtr);
    cryptoModule._free(dataPtr);
    cryptoModule._free(outPtr);
  }
}

async function webCryptoEncrypt(key: Uint8Array, data: Uint8Array): Promise<Uint8Array> {
  const iv = crypto.getRandomValues(new Uint8Array(12));

  // Create a copy of the key as ArrayBuffer
  const keyBuffer = new ArrayBuffer(key.length);
  new Uint8Array(keyBuffer).set(key);

  const cryptoKey = await crypto.subtle.importKey(
    'raw',
    keyBuffer,
    { name: 'AES-GCM' },
    false,
    ['encrypt']
  );

  // Create a copy of the data as ArrayBuffer
  const dataBuffer = new ArrayBuffer(data.length);
  new Uint8Array(dataBuffer).set(data);

  const encrypted = await crypto.subtle.encrypt(
    { name: 'AES-GCM', iv },
    cryptoKey,
    dataBuffer
  );

  // Prepend IV to ciphertext
  const result = new Uint8Array(iv.length + encrypted.byteLength);
  result.set(iv, 0);
  result.set(new Uint8Array(encrypted), iv.length);

  return result;
}

async function webCryptoDecrypt(key: Uint8Array, ciphertext: Uint8Array): Promise<Uint8Array> {
  const iv = ciphertext.slice(0, 12);
  const encrypted = ciphertext.slice(12);

  // Create a copy of the key as ArrayBuffer
  const keyBuffer = new ArrayBuffer(key.length);
  new Uint8Array(keyBuffer).set(key);

  const cryptoKey = await crypto.subtle.importKey(
    'raw',
    keyBuffer,
    { name: 'AES-GCM' },
    false,
    ['decrypt']
  );

  // Create a copy of the encrypted data as ArrayBuffer
  const encryptedBuffer = new ArrayBuffer(encrypted.length);
  new Uint8Array(encryptedBuffer).set(encrypted);

  const decrypted = await crypto.subtle.decrypt(
    { name: 'AES-GCM', iv },
    cryptoKey,
    encryptedBuffer
  );

  return new Uint8Array(decrypted);
}

function allocateBytes(data: Uint8Array): number {
  if (!cryptoModule) throw new Error('Crypto module not loaded');

  const ptr = cryptoModule._malloc(data.length);
  cryptoModule.HEAPU8.set(data, ptr);
  return ptr;
}

function readBytes(ptr: number, length: number): Uint8Array {
  if (!cryptoModule) throw new Error('Crypto module not loaded');

  const result = new Uint8Array(length);
  result.set(cryptoModule.HEAPU8.slice(ptr, ptr + length));
  return result;
}

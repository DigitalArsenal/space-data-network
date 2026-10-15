/**
 * HD Wallet WASM - Aligned Binary API
 *
 * Provides efficient batch operations using aligned, fixed-size structs
 * for zero-copy WASM interop. This module wraps the C++ aligned API
 * with JavaScript-friendly interfaces.
 *
 * Features:
 * - Batch key derivation (derive 100s of keys efficiently)
 * - Batch signing (sign multiple hashes with same key)
 * - Batch verification
 * - Streaming key derivation (generators for unlimited sequences)
 *
 * @module hd-wallet-wasm/aligned
 */

// Import generated aligned struct definitions
import {
  // Size constants
  EXTENDEDKEYDATA_SIZE,
  BATCHDERIVEREQUEST_SIZE,
  DERIVEDKEYENTRY_SIZE,
  BATCHSIGNREQUEST_SIZE,
  HASHENTRY_SIZE,
  SIGNATUREENTRY_SIZE,
  BATCHVERIFYREQUEST_SIZE,
  VERIFYENTRY_SIZE,
  VERIFYRESULTENTRY_SIZE,
  STREAMDERIVECONFIG_SIZE,
  STREAMSTATUS_SIZE,

  // View classes
  ExtendedKeyDataView,
  BatchDeriveRequestView,
  DerivedKeyEntryView,
  DerivedKeyEntryArrayView,
  BatchSignRequestView,
  HashEntryView,
  HashEntryArrayView,
  SignatureEntryView,
  SignatureEntryArrayView,
  BatchVerifyRequestView,
  VerifyEntryView,
  VerifyEntryArrayView,
  VerifyResultEntryView,
  VerifyResultEntryArrayView,
  StreamDeriveConfigView,
  StreamStatusView,

  // Enums
  Curve as AlignedCurve,
  Error as AlignedErrorCode,
} from './generated/aligned/hd_wallet_aligned.mjs';
import { createInputValidation } from './input-validation.mjs';

// Re-export for external use
export { AlignedCurve, AlignedErrorCode };
export {
  EXTENDEDKEYDATA_SIZE,
  BATCHDERIVEREQUEST_SIZE,
  DERIVEDKEYENTRY_SIZE,
  ExtendedKeyDataView,
  BatchDeriveRequestView,
  DerivedKeyEntryView,
  DerivedKeyEntryArrayView,
};

// =============================================================================
// Error Handling
// =============================================================================

const ERROR_MESSAGES = {
  [AlignedErrorCode.OK]: 'Success',
  [AlignedErrorCode.UNKNOWN]: 'Unknown error',
  [AlignedErrorCode.INVALID_ARGUMENT]: 'Invalid argument',
  [AlignedErrorCode.NOT_SUPPORTED]: 'Operation not supported',
  [AlignedErrorCode.OUT_OF_MEMORY]: 'Out of memory',
  [AlignedErrorCode.INTERNAL]: 'Internal error',
  [AlignedErrorCode.NO_ENTROPY]: 'No entropy available',
  [AlignedErrorCode.INSUFFICIENT_ENTROPY]: 'Insufficient entropy',
  [AlignedErrorCode.INVALID_PRIVATE_KEY]: 'Invalid private key',
  [AlignedErrorCode.INVALID_PUBLIC_KEY]: 'Invalid public key',
  [AlignedErrorCode.INVALID_SIGNATURE]: 'Invalid signature',
  [AlignedErrorCode.VERIFICATION_FAILED]: 'Signature verification failed',
  [AlignedErrorCode.KEY_DERIVATION_FAILED]: 'Key derivation failed',
  [AlignedErrorCode.HARDENED_FROM_PUBLIC]: 'Cannot derive hardened from public key',
};

/**
 * Aligned API Error
 */
export class AlignedError extends Error {
  constructor(code, message) {
    const lookup = code < 0 ? -code : code;
    super(message || ERROR_MESSAGES[lookup] || `Error code: ${code}`);
    this.name = 'AlignedError';
    this.code = lookup;
  }
}

// Input validation shared with the main API (input-validation.mjs): every
// key, hash and signature is checked for type, length and (keys) validity
// before it is written into a request.
const V = createInputValidation((code, message) => new AlignedError(code, message));

// Largest batch: the request structs carry 32-bit counts, and the result
// buffers are allocated up front.
const MAX_BATCH = 0x100000;

// Allocate `size` bytes and zero them, so no field a request leaves unset is
// read from stale heap.
function allocZeroed(wasm, size) {
  const pointer = wasm._hd_alloc(Math.max(1, size));
  if (!pointer) {
    throw new AlignedError(AlignedErrorCode.OUT_OF_MEMORY);
  }
  wasm.HEAPU8.fill(0, pointer, pointer + Math.max(1, size));
  return pointer;
}

// Curves the batch APIs sign and verify with; P-384 signatures do not fit the
// 64-byte entries and X25519 does not sign, so entries for them report
// NOT_SUPPORTED.
const BATCH_CURVES = Object.freeze([
  AlignedCurve.SECP256K1,
  AlignedCurve.ED25519,
  AlignedCurve.P256,
  AlignedCurve.P384,
  AlignedCurve.X25519,
]);

// The batch exports return the number of entries written, or a negative
// -Error code. Only a count within [0, capacity] is success; anything else
// throws before a result entry is read.
function checkCount(result, capacity) {
  if (!Number.isInteger(result) || result < 0 || result > capacity) {
    throw new AlignedError(result < 0 ? result : AlignedErrorCode.INTERNAL);
  }
  return result;
}

// =============================================================================
// AlignedKeyDeriver - Batch Key Derivation
// =============================================================================

/**
 * Efficient batch key derivation using aligned binary format.
 *
 * @example
 * ```js
 * const deriver = new AlignedKeyDeriver(wasm);
 *
 * // Derive 100 keys at once
 * const keys = deriver.deriveBatch(masterKey, 0, 100);
 * for (const key of keys) {
 *   console.log(key.index, key.publicKey);
 * }
 *
 * // Stream unlimited keys
 * for (const batch of deriver.streamKeys(masterKey, 0, 50)) {
 *   for (const key of batch) {
 *     processKey(key);
 *   }
 * }
 * ```
 */
export class AlignedKeyDeriver {
  #wasm;

  /**
   * @param {Object} wasm - WASM module instance
   */
  constructor(wasm) {
    this.#wasm = wasm;
  }

  /**
   * Derive multiple child keys from a base key in batch.
   *
   * @param {Object} baseKey - Base HDKey or ExtendedKeyData
   * @param {number} startIndex - Starting child index
   * @param {number} count - Number of keys to derive
   * @param {boolean} [hardened=false] - Use hardened derivation
   * @returns {Array<DerivedKey>} Array of derived keys
   */
  deriveBatch(baseKey, startIndex, count, hardened = false) {
    V.uint32(startIndex, 'startIndex');
    V.integer(count, 'count', 0, MAX_BATCH);
    // Short-circuit: requesting zero keys returns empty array
    if (count === 0) {
      return [];
    }

    const wasm = this.#wasm;

    // Allocate request buffer
    const requestPtr = allocZeroed(wasm, BATCHDERIVEREQUEST_SIZE);

    try {
      // Fill request
      const requestView = BatchDeriveRequestView.fromMemory(wasm.wasmMemory, requestPtr);
      this.#fillBaseKey(requestView, baseKey);
      requestView.start_index = startIndex >>> 0;
      requestView.count = count >>> 0;
      requestView.hardened = hardened ? 1 : 0;

      // Allocate results buffer
      const resultsSize = count * DERIVEDKEYENTRY_SIZE;
      const resultsPtr = wasm._hd_alloc(resultsSize);
      if (!resultsPtr) {
        throw new AlignedError(AlignedErrorCode.OUT_OF_MEMORY);
      }

      try {
        // Call WASM function
        const derivedCount = checkCount(wasm._hd_aligned_derive_batch(
          requestPtr,
          resultsPtr,
          count
        ), count);

        // Read results
        const results = [];
        const resultsArray = DerivedKeyEntryArrayView.fromMemory(
          wasm.wasmMemory, resultsPtr, derivedCount
        );

        for (let i = 0; i < derivedCount; i++) {
          const entry = resultsArray.at(i);
          results.push({
            index: entry.index,
            error: entry.error,
            publicKey: entry.error === AlignedErrorCode.OK
              ? new Uint8Array(entry.public_key_data)
              : null,
            privateKey: entry.error === AlignedErrorCode.OK
              ? new Uint8Array(entry.private_key_data)
              : null,
          });
        }

        return results;
      } finally {
        // Entries carry derived private keys.
        wasm._hd_secure_wipe(resultsPtr, resultsSize);
        wasm._hd_dealloc(resultsPtr);
      }
    } finally {
      // The request carries the base private key.
      wasm._hd_secure_wipe(requestPtr, BATCHDERIVEREQUEST_SIZE);
      wasm._hd_dealloc(requestPtr);
    }
  }

  /**
   * Generator for streaming key derivation.
   *
   * @param {Object} baseKey - Base HDKey or ExtendedKeyData
   * @param {number} [startIndex=0] - Starting child index
   * @param {number} [batchSize=100] - Keys per batch
   * @param {boolean} [hardened=false] - Use hardened derivation
   * @yields {Array<DerivedKey>} Batches of derived keys
   */
  *streamKeys(baseKey, startIndex = 0, batchSize = 100, hardened = false) {
    const wasm = this.#wasm;
    V.uint32(startIndex, 'startIndex');
    // The results buffer holds exactly batchSize entries.
    V.integer(batchSize, 'batchSize', 1, MAX_BATCH);

    // Allocate config buffer
    const configPtr = allocZeroed(wasm, STREAMDERIVECONFIG_SIZE);

    // Fill config, create the stream, then wipe the config (it carries the
    // base private key) whatever happens.
    let streamHandle;
    try {
      const configView = StreamDeriveConfigView.fromMemory(wasm.wasmMemory, configPtr);
      this.#fillBaseKey(configView, baseKey);
      configView.start_index = startIndex >>> 0;
      configView.batch_size = batchSize >>> 0;
      configView.hardened = hardened ? 1 : 0;

      streamHandle = wasm._hd_aligned_stream_create(configPtr);
    } finally {
      wasm._hd_secure_wipe(configPtr, STREAMDERIVECONFIG_SIZE);
      wasm._hd_dealloc(configPtr);
    }

    if (!streamHandle) {
      throw new AlignedError(AlignedErrorCode.INTERNAL, 'Failed to create stream');
    }

    // Allocate results buffer
    const resultsSize = batchSize * DERIVEDKEYENTRY_SIZE;
    const resultsPtr = wasm._hd_alloc(resultsSize);
    if (!resultsPtr) {
      wasm._hd_aligned_stream_destroy(streamHandle);
      throw new AlignedError(AlignedErrorCode.OUT_OF_MEMORY);
    }

    try {
      while (true) {
        // 0 ends the stream; a negative -Error throws instead of ending it
        // silently.
        const derivedCount = checkCount(wasm._hd_aligned_stream_next(
          streamHandle,
          resultsPtr,
          batchSize
        ), batchSize);

        if (derivedCount === 0) {
          break;
        }

        // Read batch results
        const batch = [];
        const resultsArray = DerivedKeyEntryArrayView.fromMemory(
          wasm.wasmMemory, resultsPtr, derivedCount
        );

        for (let i = 0; i < derivedCount; i++) {
          const entry = resultsArray.at(i);
          batch.push({
            index: entry.index,
            error: entry.error,
            publicKey: entry.error === AlignedErrorCode.OK
              ? new Uint8Array(entry.public_key_data)
              : null,
            privateKey: entry.error === AlignedErrorCode.OK
              ? new Uint8Array(entry.private_key_data)
              : null,
          });
        }

        yield batch;
      }
    } finally {
      wasm._hd_secure_wipe(resultsPtr, resultsSize);
      wasm._hd_dealloc(resultsPtr);
      wasm._hd_aligned_stream_destroy(streamHandle);
    }
  }

  /**
   * Fill base key data in request/config view
   * @private
   */
  #fillBaseKey(view, baseKey) {
    if (baseKey && baseKey._handle) {
      // HDKey object - use conversion function
      const keyDataPtr = this.#wasm._hd_alloc(EXTENDEDKEYDATA_SIZE);
      if (!keyDataPtr) {
        throw new AlignedError(AlignedErrorCode.OUT_OF_MEMORY);
      }
      try {
        // On failure the buffer holds no key: never copy it.
        const result = this.#wasm._hd_aligned_from_extended_key(baseKey._handle, keyDataPtr);
        if (result !== 0) {
          throw new AlignedError(result);
        }
        const keyDataView = ExtendedKeyDataView.fromMemory(
          this.#wasm.wasmMemory, keyDataPtr
        );
        // Copy to request view's base_key fields
        view.base_key_curve = keyDataView.curve;
        view.base_key_depth = keyDataView.depth;
        view.base_key_parent_fingerprint = keyDataView.parent_fingerprint;
        view.base_key_child_index = keyDataView.child_index;
        view.base_key_chain_code_data.set(keyDataView.chain_code_data);
        view.base_key_public_key_data.set(keyDataView.public_key_data);
        view.base_key_private_key_data.set(keyDataView.private_key_data);
        view.base_key_has_private_key = keyDataView.has_private_key;
      } finally {
        this.#wasm._hd_secure_wipe(keyDataPtr, EXTENDEDKEYDATA_SIZE);
        this.#wasm._hd_dealloc(keyDataPtr);
      }
    } else if (baseKey && baseKey.privateKey && baseKey.chainCode) {
      // Plain object with key data: every field at its exact size, the keys
      // valid for the curve (32-byte scalars, 33-byte compressed keys).
      const curve = baseKey.curve ?? AlignedCurve.SECP256K1;
      V.curve(curve, [AlignedCurve.SECP256K1, AlignedCurve.P256]);
      const depth = baseKey.depth ?? 0;
      const parentFingerprint = baseKey.parentFingerprint ?? 0;
      const childIndex = baseKey.childIndex ?? 0;
      V.integer(depth, 'depth', 0, 255);
      V.uint32(parentFingerprint, 'parentFingerprint');
      V.uint32(childIndex, 'childIndex');
      V.bytesOfLength(baseKey.chainCode, 32, 'chainCode');
      V.privateKey(this.#wasm, baseKey.privateKey, curve);
      V.publicKey(this.#wasm, baseKey.publicKey, curve, 'publicKey', [33]);
      view.base_key_curve = curve;
      view.base_key_depth = depth;
      view.base_key_parent_fingerprint = parentFingerprint;
      view.base_key_child_index = childIndex;
      view.base_key_chain_code_data.set(baseKey.chainCode);
      view.base_key_public_key_data.set(baseKey.publicKey);
      view.base_key_private_key_data.set(baseKey.privateKey);
      view.base_key_has_private_key = 1;
    } else {
      throw new AlignedError(
        AlignedErrorCode.INVALID_ARGUMENT,
        'baseKey must be an HDKey or have privateKey and chainCode'
      );
    }
  }
}

// =============================================================================
// AlignedSigner - Batch Signing
// =============================================================================

/**
 * Efficient batch signing using aligned binary format.
 *
 * @example
 * ```js
 * const signer = new AlignedSigner(wasm);
 *
 * // Sign multiple hashes
 * const hashes = [hash1, hash2, hash3];
 * const signatures = signer.signBatch(privateKey, hashes);
 * ```
 */
export class AlignedSigner {
  #wasm;

  /**
   * @param {Object} wasm - WASM module instance
   */
  constructor(wasm) {
    this.#wasm = wasm;
  }

  /**
   * Sign multiple message hashes with the same private key.
   *
   * @param {Uint8Array} privateKey - 32-byte private key
   * @param {Array<Uint8Array>} hashes - Array of 32-byte message hashes
   * @param {number} [curve=AlignedCurve.SECP256K1] - Elliptic curve
   * @returns {Array<SignatureResult>} Array of signature results
   */
  signBatch(privateKey, hashes, curve = AlignedCurve.SECP256K1) {
    const wasm = this.#wasm;
    V.curve(curve, BATCH_CURVES);
    // The array and each entry are read once (a Proxy or a getter could answer
    // a second read differently); only the copies are used below.
    const hashCount = Array.isArray(hashes) ? hashes.length : -1;
    if (!Number.isInteger(hashCount) || hashCount < 0 || hashCount > MAX_BATCH) {
      throw new AlignedError(AlignedErrorCode.INVALID_ARGUMENT, `hashes must be an array of at most ${MAX_BATCH}`);
    }
    const hashCopies = [];
    for (let i = 0; i < hashCount; i++) hashCopies.push(V.snapshot(hashes[i], 'hash'));
    // The request holds a 32-byte key: secp256k1 and P-256 keys must be
    // 0 < d < n; Ed25519 seeds are any 32 bytes. P-384 and X25519 entries
    // report NOT_SUPPORTED.
    if (curve === AlignedCurve.SECP256K1 || curve === AlignedCurve.P256 || curve === AlignedCurve.ED25519) {
      V.privateKey(wasm, privateKey, curve);
    } else {
      V.bytesOfLength(privateKey, 32, 'privateKey', AlignedErrorCode.INVALID_PRIVATE_KEY);
    }
    for (const hash of hashCopies) V.bytesOfLength(hash, 32, 'hash');
    const count = hashCopies.length;

    // Allocate request
    const requestPtr = allocZeroed(wasm, BATCHSIGNREQUEST_SIZE);

    try {
      // Fill request
      const requestView = BatchSignRequestView.fromMemory(wasm.wasmMemory, requestPtr);
      requestView.private_key_data.set(privateKey);
      requestView.curve = curve;
      requestView.count = count;

      // Allocate and fill hashes
      const hashesPtr = allocZeroed(wasm, count * HASHENTRY_SIZE);

      try {
        const hashesArray = HashEntryArrayView.fromMemory(wasm.wasmMemory, hashesPtr, count);
        for (let i = 0; i < count; i++) {
          const entry = hashesArray.at(i);
          entry.index = i;
          entry.hash_data.set(hashCopies[i]);
        }

        // Allocate results
        const resultsPtr = allocZeroed(wasm, count * SIGNATUREENTRY_SIZE);

        try {
          // Call WASM function
          const signedCount = checkCount(wasm._hd_aligned_sign_batch(
            requestPtr,
            hashesPtr,
            count,
            resultsPtr,
            count
          ), count);

          // Read results
          const results = [];
          const resultsArray = SignatureEntryArrayView.fromMemory(
            wasm.wasmMemory, resultsPtr, signedCount
          );

          for (let i = 0; i < signedCount; i++) {
            const entry = resultsArray.at(i);
            results.push({
              index: entry.index,
              error: entry.error,
              signature: entry.error === AlignedErrorCode.OK
                ? new Uint8Array(entry.signature_data)
                : null,
              recoveryId: entry.recovery_id,
            });
          }

          return results;
        } finally {
          wasm._hd_dealloc(resultsPtr);
        }
      } finally {
        wasm._hd_dealloc(hashesPtr);
      }
    } finally {
      // The request carries the private key.
      wasm._hd_secure_wipe(requestPtr, BATCHSIGNREQUEST_SIZE);
      wasm._hd_dealloc(requestPtr);
    }
  }

  /**
   * Verify multiple signatures against the same public key.
   *
   * @param {Uint8Array} publicKey - 33-byte compressed public key
   * @param {Array<{hash: Uint8Array, signature: Uint8Array}>} entries - Hash/signature pairs
   * @param {number} [curve=AlignedCurve.SECP256K1] - Elliptic curve
   * @returns {Array<VerifyResult>} Array of verification results
   */
  verifyBatch(publicKey, entries, curve = AlignedCurve.SECP256K1) {
    const wasm = this.#wasm;
    V.curve(curve, BATCH_CURVES);
    // The array, each entry and each field are read once (a Proxy or a getter
    // could answer a second read differently); only the copies are used below.
    const entryCount = Array.isArray(entries) ? entries.length : -1;
    if (!Number.isInteger(entryCount) || entryCount < 0 || entryCount > MAX_BATCH) {
      throw new AlignedError(AlignedErrorCode.INVALID_ARGUMENT, `entries must be an array of at most ${MAX_BATCH}`);
    }
    // Types are programmer errors (throw); wrong lengths or an invalid key
    // only make the affected entries invalid.
    const publicKeyCopy = V.snapshot(publicKey, 'publicKey');
    const entryCopies = [];
    for (let i = 0; i < entryCount; i++) {
      const entry = entries[i];
      if (entry === null || typeof entry !== 'object') {
        throw new AlignedError(AlignedErrorCode.INVALID_ARGUMENT, 'each entry must be { hash, signature }');
      }
      const { hash, signature } = entry;
      entryCopies.push({ hash: V.snapshot(hash, 'hash'), signature: V.snapshot(signature, 'signature') });
    }
    const count = entryCopies.length;

    // The request holds a 33-byte key: a compressed secp256k1 or P-256 key,
    // or an Ed25519 key after one padding byte.
    let keyBytes = null;
    if (curve === AlignedCurve.ED25519) {
      if (publicKeyCopy.length === 32) {
        keyBytes = new Uint8Array(33);
        keyBytes.set(publicKeyCopy, 1);
      } else if (publicKeyCopy.length === 33) {
        keyBytes = publicKeyCopy;
      }
    } else if (curve === AlignedCurve.SECP256K1 || curve === AlignedCurve.P256) {
      if (V.isPublicKey(wasm, publicKeyCopy, curve, [33])) keyBytes = publicKeyCopy;
    } else {
      // P-384 / X25519: the export reports NOT_SUPPORTED for every entry.
      keyBytes = new Uint8Array(33);
    }
    const entryValid = entryCopies.map((entry) => keyBytes !== null &&
      entry.hash.length === 32 && entry.signature.length === 64);

    // Allocate request
    const requestPtr = allocZeroed(wasm, BATCHVERIFYREQUEST_SIZE);

    try {
      // Fill request
      const requestView = BatchVerifyRequestView.fromMemory(wasm.wasmMemory, requestPtr);
      if (keyBytes !== null) requestView.public_key_data.set(keyBytes);
      requestView.curve = curve;
      requestView.count = count;

      // Allocate and fill entries (an invalid entry stays all zero and its
      // result is reported invalid below whatever the export says)
      const entriesPtr = allocZeroed(wasm, count * VERIFYENTRY_SIZE);

      try {
        const entriesArray = VerifyEntryArrayView.fromMemory(wasm.wasmMemory, entriesPtr, count);
        for (let i = 0; i < count; i++) {
          const view = entriesArray.at(i);
          view.index = i;
          if (entryValid[i]) {
            view.hash_data.set(entryCopies[i].hash);
            view.signature_data.set(entryCopies[i].signature);
          }
        }

        // Allocate results
        const resultsPtr = allocZeroed(wasm, count * VERIFYRESULTENTRY_SIZE);

        try {
          // Call WASM function
          const verifiedCount = checkCount(wasm._hd_aligned_verify_batch(
            requestPtr,
            entriesPtr,
            count,
            resultsPtr,
            count
          ), count);

          // Read results
          const results = [];
          const resultsArray = VerifyResultEntryArrayView.fromMemory(
            wasm.wasmMemory, resultsPtr, verifiedCount
          );

          for (let i = 0; i < verifiedCount; i++) {
            const entry = resultsArray.at(i);
            if (!entryValid[i]) {
              results.push({
                index: i,
                valid: false,
                error: keyBytes === null
                  ? AlignedErrorCode.INVALID_PUBLIC_KEY
                  : AlignedErrorCode.INVALID_SIGNATURE,
              });
              continue;
            }
            results.push({
              index: entry.index,
              valid: entry.error === AlignedErrorCode.OK,
              error: entry.error,
            });
          }

          return results;
        } finally {
          wasm._hd_dealloc(resultsPtr);
        }
      } finally {
        wasm._hd_dealloc(entriesPtr);
      }
    } finally {
      wasm._hd_dealloc(requestPtr);
    }
  }
}

// =============================================================================
// AlignedAPI - Combined API
// =============================================================================

/**
 * Combined aligned API providing access to batch operations.
 */
export class AlignedAPI {
  #wasm;
  #keyDeriver;
  #signer;

  /**
   * @param {Object} wasm - WASM module instance
   */
  constructor(wasm) {
    this.#wasm = wasm;
    this.#keyDeriver = null;
    this.#signer = null;
  }

  /**
   * Get key deriver instance
   * @returns {AlignedKeyDeriver}
   */
  get keyDeriver() {
    if (!this.#keyDeriver) {
      this.#keyDeriver = new AlignedKeyDeriver(this.#wasm);
    }
    return this.#keyDeriver;
  }

  /**
   * Get signer instance
   * @returns {AlignedSigner}
   */
  get signer() {
    if (!this.#signer) {
      this.#signer = new AlignedSigner(this.#wasm);
    }
    return this.#signer;
  }

  /**
   * Convert HDKey to ExtendedKeyData bytes
   * @param {Object} hdKey - HDKey instance
   * @returns {Uint8Array} Extended key data bytes
   */
  keyToBytes(hdKey) {
    const wasm = this.#wasm;
    if (!hdKey || !hdKey._handle) {
      throw new AlignedError(AlignedErrorCode.INVALID_ARGUMENT, 'hdKey must be a live HDKey');
    }
    const ptr = wasm._hd_alloc(EXTENDEDKEYDATA_SIZE);
    if (!ptr) {
      throw new AlignedError(AlignedErrorCode.OUT_OF_MEMORY);
    }

    try {
      const result = wasm._hd_aligned_from_extended_key(hdKey._handle, ptr);
      if (result !== 0) {
        throw new AlignedError(result);
      }
      return new Uint8Array(wasm.HEAPU8.buffer, ptr, EXTENDEDKEYDATA_SIZE).slice();
    } finally {
      wasm._hd_secure_wipe(ptr, EXTENDEDKEYDATA_SIZE);
      wasm._hd_dealloc(ptr);
    }
  }

  /**
   * Get size of DerivedKeyEntry struct
   * @returns {number}
   */
  get derivedKeyEntrySize() {
    return DERIVEDKEYENTRY_SIZE;
  }

  /**
   * Get size of ExtendedKeyData struct
   * @returns {number}
   */
  get extendedKeyDataSize() {
    return EXTENDEDKEYDATA_SIZE;
  }

  /**
   * Get size of BatchDeriveRequest struct
   * @returns {number}
   */
  get batchDeriveRequestSize() {
    return BATCHDERIVEREQUEST_SIZE;
  }
}

export default AlignedAPI;

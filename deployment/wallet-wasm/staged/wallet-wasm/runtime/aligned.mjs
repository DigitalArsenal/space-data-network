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
    super(message || ERROR_MESSAGES[code] || `Error code: ${code}`);
    this.name = 'AlignedError';
    this.code = code;
  }
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
    // Short-circuit: requesting zero keys returns empty array
    if (count === 0) {
      return [];
    }

    const wasm = this.#wasm;

    // Allocate request buffer
    const requestPtr = wasm._hd_alloc(BATCHDERIVEREQUEST_SIZE);
    if (!requestPtr) {
      throw new AlignedError(AlignedErrorCode.OUT_OF_MEMORY);
    }

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
        const derivedCount = wasm._hd_aligned_derive_batch(
          requestPtr,
          resultsPtr,
          count
        );

        if (derivedCount < 0) {
          throw new AlignedError(derivedCount);
        }

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
        wasm._hd_dealloc(resultsPtr);
      }
    } finally {
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

    // Allocate config buffer
    const configPtr = wasm._hd_alloc(STREAMDERIVECONFIG_SIZE);
    if (!configPtr) {
      throw new AlignedError(AlignedErrorCode.OUT_OF_MEMORY);
    }

    // Fill config
    const configView = StreamDeriveConfigView.fromMemory(wasm.wasmMemory, configPtr);
    this.#fillBaseKey(configView, baseKey);
    configView.start_index = startIndex >>> 0;
    configView.batch_size = batchSize >>> 0;
    configView.hardened = hardened ? 1 : 0;

    // Create stream
    const streamHandle = wasm._hd_aligned_stream_create(configPtr);
    wasm._hd_dealloc(configPtr);

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
        const derivedCount = wasm._hd_aligned_stream_next(
          streamHandle,
          resultsPtr,
          batchSize
        );

        if (derivedCount <= 0) {
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
      wasm._hd_dealloc(resultsPtr);
      wasm._hd_aligned_stream_destroy(streamHandle);
    }
  }

  /**
   * Fill base key data in request/config view
   * @private
   */
  #fillBaseKey(view, baseKey) {
    if (baseKey._handle) {
      // HDKey object - use conversion function
      const keyDataPtr = this.#wasm._hd_alloc(EXTENDEDKEYDATA_SIZE);
      try {
        this.#wasm._hd_aligned_from_extended_key(baseKey._handle, keyDataPtr);
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
        this.#wasm._hd_dealloc(keyDataPtr);
      }
    } else if (baseKey.privateKey && baseKey.chainCode) {
      // Plain object with key data
      view.base_key_curve = baseKey.curve ?? AlignedCurve.SECP256K1;
      view.base_key_depth = baseKey.depth ?? 0;
      view.base_key_parent_fingerprint = baseKey.parentFingerprint ?? 0;
      view.base_key_child_index = baseKey.childIndex ?? 0;
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
    const count = hashes.length;

    // Allocate request
    const requestPtr = wasm._hd_alloc(BATCHSIGNREQUEST_SIZE);
    if (!requestPtr) {
      throw new AlignedError(AlignedErrorCode.OUT_OF_MEMORY);
    }

    try {
      // Fill request
      const requestView = BatchSignRequestView.fromMemory(wasm.wasmMemory, requestPtr);
      requestView.private_key_data.set(privateKey);
      requestView.curve = curve;
      requestView.count = count;

      // Allocate and fill hashes
      const hashesPtr = wasm._hd_alloc(count * HASHENTRY_SIZE);
      if (!hashesPtr) {
        throw new AlignedError(AlignedErrorCode.OUT_OF_MEMORY);
      }

      try {
        const hashesArray = HashEntryArrayView.fromMemory(wasm.wasmMemory, hashesPtr, count);
        for (let i = 0; i < count; i++) {
          const entry = hashesArray.at(i);
          entry.index = i;
          entry.hash_data.set(hashes[i]);
        }

        // Allocate results
        const resultsPtr = wasm._hd_alloc(count * SIGNATUREENTRY_SIZE);
        if (!resultsPtr) {
          throw new AlignedError(AlignedErrorCode.OUT_OF_MEMORY);
        }

        try {
          // Call WASM function
          const signedCount = wasm._hd_aligned_sign_batch(
            requestPtr,
            hashesPtr,
            count,
            resultsPtr,
            count
          );

          if (signedCount < 0) {
            throw new AlignedError(signedCount);
          }

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
    const count = entries.length;

    // Allocate request
    const requestPtr = wasm._hd_alloc(BATCHVERIFYREQUEST_SIZE);
    if (!requestPtr) {
      throw new AlignedError(AlignedErrorCode.OUT_OF_MEMORY);
    }

    try {
      // Fill request
      const requestView = BatchVerifyRequestView.fromMemory(wasm.wasmMemory, requestPtr);
      requestView.public_key_data.set(publicKey);
      requestView.curve = curve;
      requestView.count = count;

      // Allocate and fill entries
      const entriesPtr = wasm._hd_alloc(count * VERIFYENTRY_SIZE);
      if (!entriesPtr) {
        throw new AlignedError(AlignedErrorCode.OUT_OF_MEMORY);
      }

      try {
        const entriesArray = VerifyEntryArrayView.fromMemory(wasm.wasmMemory, entriesPtr, count);
        for (let i = 0; i < count; i++) {
          const view = entriesArray.at(i);
          view.index = i;
          view.hash_data.set(entries[i].hash);
          view.signature_data.set(entries[i].signature);
        }

        // Allocate results
        const resultsPtr = wasm._hd_alloc(count * VERIFYRESULTENTRY_SIZE);
        if (!resultsPtr) {
          throw new AlignedError(AlignedErrorCode.OUT_OF_MEMORY);
        }

        try {
          // Call WASM function
          const verifiedCount = wasm._hd_aligned_verify_batch(
            requestPtr,
            entriesPtr,
            count,
            resultsPtr,
            count
          );

          if (verifiedCount < 0) {
            throw new AlignedError(verifiedCount);
          }

          // Read results
          const results = [];
          const resultsArray = VerifyResultEntryArrayView.fromMemory(
            wasm.wasmMemory, resultsPtr, verifiedCount
          );

          for (let i = 0; i < verifiedCount; i++) {
            const entry = resultsArray.at(i);
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

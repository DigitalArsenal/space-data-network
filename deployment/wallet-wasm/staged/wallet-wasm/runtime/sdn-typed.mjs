const MAX_OUTPUT_BYTES = 131072;
const MAX_INPUT_JSON_BYTES = 131072;
const MAX_AAD_BYTES = 4096;
const MAX_MODERN_CREDENTIAL_BYTES = 256;
const MAX_LEGACY_CREDENTIAL_BYTES = 4096;
const MAX_MNEMONIC_BYTES = 1024;
const MIN_REMEMBERED_CIPHERTEXT_BYTES = 16;
const MAX_REMEMBERED_CIPHERTEXT_BYTES = 1024;
const MAX_NATIVE_SLOT = 16n;
const UINT64_MAX = (1n << 64n) - 1n;

const ERROR_CODES = Object.freeze([
  'INVALID_USERNAME',
  'INVALID_PASSWORD',
  'COMMON_PASSWORD',
  'KDF_FAILURE',
  'INVALID_MNEMONIC',
  'INVALID_ACCOUNT',
  'STALE_HANDLE',
  'OPERATION_NOT_ALLOWED',
  'INVALID_REQUEST',
  'AUTHENTICATION_FAILED',
  'CAPACITY_EXCEEDED',
  'CRYPTO_FAILURE',
  'OUT_OF_MEMORY',
  'FIPS_NOT_ALLOWED',
]);

const RAW_ENTRYPOINTS = Object.freeze([
  '_hd_sdn_derive_password_identity',
  '_hd_sdn_derive_legacy_password_identity',
  '_hd_sdn_import_legacy_mnemonic_identity',
  '_hd_sdn_import_remembered_identity',
  '_hd_sdn_sign_login_v1',
  '_hd_sdn_sign_login_v2',
  '_hd_sdn_sign_asset_review_authority_activation',
  '_hd_sdn_sign_asset_review_decision',
  '_hd_sdn_seal_remembered_identity',
  '_hd_sdn_destroy_identity',
]);

const textEncoder = new TextEncoder();
const textDecoder = new TextDecoder('utf-8', { fatal: true });
const SafeUint8Array = Uint8Array;
const typedArrayPrototype = Object.getPrototypeOf(SafeUint8Array.prototype);
const typedArrayTag = Object.getOwnPropertyDescriptor(
  typedArrayPrototype,
  Symbol.toStringTag,
).get;
const typedArrayByteLength = Object.getOwnPropertyDescriptor(
  typedArrayPrototype,
  'byteLength',
).get;
const intrinsicFill = SafeUint8Array.prototype.fill;
const intrinsicSet = SafeUint8Array.prototype.set;

class SdnWalletError extends Error {
  constructor(code) {
    super(code);
    this.name = 'SdnWalletError';
    this.code = code;
  }

  toJSON() {
    return { name: this.name, code: this.code };
  }
}

function fail(code) {
  throw new SdnWalletError(code);
}

function statusCode(status) {
  return Number.isInteger(status) && status >= 1 && status <= ERROR_CODES.length
    ? ERROR_CODES[status - 1]
    : 'CRYPTO_FAILURE';
}

function throwStatus(status) {
  fail(statusCode(status));
}

function isObject(value) {
  return value !== null && typeof value === 'object';
}

function exactKeys(value, keys) {
  if (!isObject(value) || Array.isArray(value)) return false;
  const actual = Object.keys(value).sort();
  const expected = [...keys].sort();
  return actual.length === expected.length &&
    actual.every((key, index) => key === expected[index]);
}

function requireExactObject(value, keys) {
  if (!exactKeys(value, keys)) fail('INVALID_REQUEST');
  return value;
}

function requireBytes(value, code = 'INVALID_REQUEST') {
  let tag;
  try {
    tag = typedArrayTag.call(value);
  } catch {
    fail(code);
  }
  if (tag !== 'Uint8Array') fail(code);
  return value;
}

function copyBytes(value, code = 'INVALID_REQUEST') {
  const source = requireBytes(value, code);
  let length;
  try {
    length = typedArrayByteLength.call(source);
  } catch {
    fail(code);
  }
  let copy;
  try {
    copy = new SafeUint8Array(length);
  } catch {
    fail('OUT_OF_MEMORY');
  }
  try {
    intrinsicSet.call(copy, source);
  } catch {
    intrinsicFill.call(copy, 0);
    fail(code);
  }
  return copy;
}

function copySecretAndWipeCaller(value, code) {
  const caller = requireBytes(value, code);
  const copy = copyBytes(caller, code);
  if (wipeAll([caller])) {
    wipeAll([copy]);
    fail('CRYPTO_FAILURE');
  }
  return copy;
}

function isUint8Array(value) {
  try {
    return typedArrayTag.call(value) === 'Uint8Array';
  } catch {
    return false;
  }
}

function wipeAll(values) {
  let failed = false;
  for (const value of values) {
    if (!isUint8Array(value)) continue;
    try {
      intrinsicFill.call(value, 0);
    } catch {
      failed = true;
    }
  }
  return failed;
}

function onceWipe(values) {
  let complete = false;
  let failed = false;
  return () => {
    if (!complete) {
      complete = true;
      failed = wipeAll(values());
    }
    return failed;
  };
}

function validUtf16(value) {
  if (typeof value !== 'string') return false;
  for (let index = 0; index < value.length; index += 1) {
    const unit = value.charCodeAt(index);
    if (unit >= 0xd800 && unit <= 0xdbff) {
      if (index + 1 >= value.length) return false;
      const next = value.charCodeAt(index + 1);
      if (next < 0xdc00 || next > 0xdfff) return false;
      index += 1;
    } else if (unit >= 0xdc00 && unit <= 0xdfff) {
      return false;
    }
  }
  return true;
}

function encodeString(value, maximum = MAX_INPUT_JSON_BYTES) {
  if (!validUtf16(value)) fail('INVALID_REQUEST');
  const bytes = textEncoder.encode(value);
  if (bytes.length > maximum) fail('INVALID_REQUEST');
  return bytes;
}

function encodeRequest(value) {
  let serialized;
  try {
    serialized = JSON.stringify(value);
  } catch {
    fail('INVALID_REQUEST');
  }
  if (typeof serialized !== 'string') fail('INVALID_REQUEST');
  return encodeString(serialized, MAX_INPUT_JSON_BYTES);
}

function requireAccount(value) {
  if (value !== 0 && value !== 1) fail('INVALID_ACCOUNT');
  return value;
}

function validNativeHandle(value) {
  if (typeof value !== 'bigint' || value <= 0n || value > UINT64_MAX) return false;
  const slot = value & 0xffffffffn;
  const generation = value >> 32n;
  return generation !== 0n && slot !== 0n && slot <= MAX_NATIVE_SLOT;
}

function validLowerHex(value, length) {
  return typeof value === 'string' && value.length === length &&
    /^[0-9a-f]+$/.test(value);
}

function validKeyId(value) {
  return typeof value === 'string' && /^sha256:[0-9a-f]{64}$/.test(value);
}

function copyDescriptor(value, scheme, profile, expected) {
  const keys = [
    'purpose', 'identityScheme', 'seedProfile', 'signatureProfile', 'curve',
    'derivation', 'path', 'encoding', 'publicKeyHex', 'bip32Fingerprint', 'keyId',
  ];
  if (!exactKeys(value, keys) ||
      value.purpose !== expected.purpose ||
      value.identityScheme !== scheme ||
      value.seedProfile !== profile ||
      value.signatureProfile !== expected.signatureProfile ||
      value.curve !== expected.curve ||
      value.derivation !== expected.derivation ||
      value.path !== expected.path ||
      value.encoding !== 'raw' ||
      !validLowerHex(value.publicKeyHex, 64) ||
      value.bip32Fingerprint !== null ||
      !validKeyId(value.keyId)) {
    fail('CRYPTO_FAILURE');
  }
  return Object.freeze({
    purpose: value.purpose,
    identityScheme: value.identityScheme,
    seedProfile: value.seedProfile,
    signatureProfile: value.signatureProfile,
    curve: value.curve,
    derivation: value.derivation,
    path: value.path,
    encoding: value.encoding,
    publicKeyHex: value.publicKeyHex,
    bip32Fingerprint: null,
    keyId: value.keyId,
  });
}

function copyIdentity(value) {
  const keys = [
    'schemaVersion', 'identityScheme', 'seedProfile', 'accountIndex',
    'accountLabel', 'accountXpub', 'accountPeerId', 'accountFingerprint', 'keys',
  ];
  if (!exactKeys(value, keys) || value.schemaVersion !== 1 ||
      (value.accountIndex !== 0 && value.accountIndex !== 1) ||
      value.accountLabel !== null || typeof value.accountXpub !== 'string' ||
      value.accountXpub.length === 0 || typeof value.accountPeerId !== 'string' ||
      value.accountPeerId.length === 0 || !validLowerHex(value.accountFingerprint, 8) ||
      !Array.isArray(value.keys)) {
    fail('CRYPTO_FAILURE');
  }

  const prefix = `m/44'/0'/${value.accountIndex}'/`;
  let expected;
  if (value.identityScheme === 'sdn-bip32-slip10-purpose-v1' &&
      value.seedProfile === 'password-scrypt-v2') {
    expected = [
      {
        purpose: 'asset-review-approval',
        signatureProfile: 'ed25519-over-sha256-jcs-v1',
        curve: 'ed25519',
        derivation: 'slip10',
        path: `${prefix}2'/0'`,
      },
      {
        purpose: 'contact-encryption',
        signatureProfile: null,
        curve: 'x25519',
        derivation: 'slip10',
        path: `${prefix}1'/0'`,
      },
      {
        purpose: 'sdn-authentication',
        signatureProfile: 'ed25519-over-sha256-jcs-v1',
        curve: 'ed25519',
        derivation: 'slip10',
        path: `${prefix}0'/0'`,
      },
    ];
  } else if (
    (value.identityScheme === 'sdn-fast-password-auth-v1-legacy' &&
      value.seedProfile === 'password-fast-v1-legacy') ||
    (value.identityScheme === 'sdn-bip39-auth-v1-legacy' &&
      value.seedProfile === 'bip39-mnemonic-v1-legacy')
  ) {
    expected = [{
      purpose: 'sdn-authentication',
      signatureProfile: 'ed25519-raw-32-v1',
      curve: 'ed25519',
      derivation: 'bip32-scalar-as-ed25519-seed',
      path: `${prefix}0/0`,
    }];
  } else {
    fail('CRYPTO_FAILURE');
  }
  if (value.keys.length !== expected.length) fail('CRYPTO_FAILURE');
  const descriptors = value.keys.map((descriptor, index) =>
    copyDescriptor(descriptor, value.identityScheme, value.seedProfile, expected[index]));
  return Object.freeze({
    schemaVersion: 1,
    identityScheme: value.identityScheme,
    seedProfile: value.seedProfile,
    accountIndex: value.accountIndex,
    accountLabel: null,
    accountXpub: value.accountXpub,
    accountPeerId: value.accountPeerId,
    accountFingerprint: value.accountFingerprint,
    keys: Object.freeze(descriptors),
  });
}

function parseIdentity(bytes) {
  let value;
  try {
    value = JSON.parse(textDecoder.decode(bytes));
  } catch {
    fail('CRYPTO_FAILURE');
  }
  return copyIdentity(value);
}

function copySignature(value, canonical) {
  const common = [
    'schemaVersion', 'keyId', 'identityScheme', 'algorithm', 'encoding',
    'signatureProfile', 'signatureHex',
  ];
  const keys = canonical
    ? [...common, 'canonicalEnvelope', 'signedDigestSha256']
    : common;
  const validScheme = canonical
    ? value?.identityScheme === 'sdn-bip32-slip10-purpose-v1'
    : value?.identityScheme === 'sdn-fast-password-auth-v1-legacy' ||
      value?.identityScheme === 'sdn-bip39-auth-v1-legacy';
  if (!exactKeys(value, keys) || value.schemaVersion !== 1 ||
      !validKeyId(value.keyId) || !validScheme || value.algorithm !== 'ed25519' ||
      value.encoding !== 'raw' || !validLowerHex(value.signatureHex, 128) ||
      value.signatureProfile !== (canonical
        ? 'ed25519-over-sha256-jcs-v1'
        : 'ed25519-raw-32-v1')) {
    fail('CRYPTO_FAILURE');
  }
  if (canonical && (typeof value.canonicalEnvelope !== 'string' ||
      value.canonicalEnvelope.length === 0 ||
      !validLowerHex(value.signedDigestSha256, 64))) {
    fail('CRYPTO_FAILURE');
  }
  const result = {
    schemaVersion: 1,
    keyId: value.keyId,
    identityScheme: value.identityScheme,
    algorithm: 'ed25519',
    encoding: 'raw',
    signatureProfile: value.signatureProfile,
  };
  if (canonical) {
    result.canonicalEnvelope = value.canonicalEnvelope;
    result.signedDigestSha256 = value.signedDigestSha256;
  }
  result.signatureHex = value.signatureHex;
  return Object.freeze(result);
}

function parseSignature(bytes, canonical) {
  let value;
  try {
    value = JSON.parse(textDecoder.decode(bytes));
  } catch {
    fail('CRYPTO_FAILURE');
  }
  return copySignature(value, canonical);
}

function checkedHeap(wasm) {
  let heap;
  try {
    heap = wasm.HEAPU8;
  } catch {
    fail('CRYPTO_FAILURE');
  }
  if (!(heap instanceof Uint8Array)) fail('CRYPTO_FAILURE');
  return heap;
}

function createAllocationScope(wasm) {
  const allocations = [];
  let cleaned = false;
  let cleanupFailed = false;
  return {
    allocate(bytesOrSize, secret = false) {
      const bytes = typeof bytesOrSize === 'number' ? null : bytesOrSize;
      const requested = bytes === null ? bytesOrSize : bytes.length;
      if (!Number.isSafeInteger(requested) || requested < 0 || requested > 0xffffffff) {
        fail('OUT_OF_MEMORY');
      }
      const size = Math.max(1, requested);
      let pointer;
      try {
        pointer = wasm._malloc(size);
      } catch {
        fail('OUT_OF_MEMORY');
      }
      if (!Number.isInteger(pointer) || pointer <= 0 || pointer > 0xffffffff) {
        fail('OUT_OF_MEMORY');
      }
      allocations.push({ pointer, size, secret });
      let heap;
      try {
        heap = wasm.HEAPU8;
        if (!(heap instanceof Uint8Array) || pointer + size > heap.byteLength ||
            pointer + size <= pointer) {
          fail('OUT_OF_MEMORY');
        }
        heap.fill(0, pointer, pointer + size);
        if (bytes !== null && bytes.length !== 0) heap.set(bytes, pointer);
      } catch {
        fail('OUT_OF_MEMORY');
      }
      return pointer;
    },
    view() {
      return new DataView(checkedHeap(wasm).buffer);
    },
    bytes(pointer, length) {
      const heap = checkedHeap(wasm);
      if (!Number.isInteger(length) || length <= 0 || length > MAX_OUTPUT_BYTES ||
          pointer <= 0 || pointer + length > heap.byteLength) {
        fail('CRYPTO_FAILURE');
      }
      return heap.slice(pointer, pointer + length);
    },
    cleanup() {
      if (cleaned) return cleanupFailed;
      cleaned = true;
      for (let index = allocations.length - 1; index >= 0; index -= 1) {
        const { pointer, size, secret } = allocations[index];
        if (secret) {
          try {
            const heap = checkedHeap(wasm);
            if (pointer + size <= heap.byteLength) {
              heap.fill(0, pointer, pointer + size);
            } else {
              cleanupFailed = true;
            }
          } catch {
            cleanupFailed = true;
          }
        }
        try {
          wasm._free(pointer);
        } catch {
          cleanupFailed = true;
        }
      }
      allocations.length = 0;
      return cleanupFailed;
    },
  };
}

function readHandle(scope, pointer) {
  try {
    return scope.view().getBigUint64(pointer, true);
  } catch {
    fail('CRYPTO_FAILURE');
  }
}

function readRequired(scope, pointer) {
  try {
    return scope.view().getUint32(pointer, true);
  } catch {
    fail('CRYPTO_FAILURE');
  }
}

/**
 * Internal per-WASM-instance factory. This is intentionally not a package
 * export subpath; the wallet-origin wrapper constructs it inside createModule.
 */
export function createSdnTypedCapabilities(wasm) {
  if (!isObject(wasm) || typeof wasm._malloc !== 'function' ||
      typeof wasm._free !== 'function' ||
      RAW_ENTRYPOINTS.some((name) => typeof wasm[name] !== 'function')) {
    fail('CRYPTO_FAILURE');
  }

  const issuedHandles = new WeakMap();
  const unpublishedRollbackQueue = new Set();

  function rollback(nativeValue) {
    if (typeof nativeValue !== 'bigint' || nativeValue === 0n) return;
    unpublishedRollbackQueue.add(nativeValue);
    try {
      wasm._hd_sdn_destroy_identity(nativeValue);
      unpublishedRollbackQueue.delete(nativeValue);
    } catch {
      // Keep unpublished native authority retryable. A later derive/import
      // must drain this exact value before it can allocate another slot.
    }
  }

  function drainRollbackQueue() {
    for (const nativeValue of unpublishedRollbackQueue) {
      try {
        wasm._hd_sdn_destroy_identity(nativeValue);
      } catch {
        fail('CRYPTO_FAILURE');
      }
      unpublishedRollbackQueue.delete(nativeValue);
    }
  }

  function isDestroyPendingHandle(handle) {
    if (!isObject(handle)) return false;
    return issuedHandles.get(handle)?.state === 'destroy-pending';
  }

  function recordFor(handle) {
    if (!isObject(handle)) fail('STALE_HANDLE');
    const record = issuedHandles.get(handle);
    if (!record || record.state !== 'active') fail('STALE_HANDLE');
    return record;
  }

  function invokeDerive(
    rawName,
    rawArguments,
    inputs,
    expectedIdentityScheme,
    expectedSeedProfile,
    expectedAccountIndex,
    cleanupSecrets,
  ) {
    drainRollbackQueue();
    const scope = createAllocationScope(wasm);
    let pending = 0n;
    let completed = false;
    try {
      const pointers = inputs.map(({ bytes, secret }) => scope.allocate(bytes, secret));
      const outHandle = scope.allocate(8, true);
      const outRequired = scope.allocate(4);
      const outJson = scope.allocate(MAX_OUTPUT_BYTES);
      let status;
      let rawThrew = false;
      try {
        status = wasm[rawName].apply(wasm, [
          ...rawArguments(pointers),
          outHandle,
          outJson,
          MAX_OUTPUT_BYTES,
          outRequired,
        ]);
      } catch {
        rawThrew = true;
      }
      pending = readHandle(scope, outHandle);
      if (rawThrew) fail('CRYPTO_FAILURE');
      if (status !== 0) throwStatus(status);
      if (!validNativeHandle(pending)) fail('CRYPTO_FAILURE');
      const required = readRequired(scope, outRequired);
      if (required <= 0 || required > MAX_OUTPUT_BYTES) fail('CRYPTO_FAILURE');
      const identity = parseIdentity(scope.bytes(outJson, required));
      if (identity.identityScheme !== expectedIdentityScheme ||
          identity.seedProfile !== expectedSeedProfile ||
          identity.accountIndex !== expectedAccountIndex) {
        fail('CRYPTO_FAILURE');
      }
      if (scope.cleanup()) fail('CRYPTO_FAILURE');
      if (cleanupSecrets()) fail('CRYPTO_FAILURE');
      const handle = Object.freeze(Object.create(null));
      const record = { nativeValue: pending, state: 'active' };
      issuedHandles.set(handle, record);
      const result = Object.freeze({ handle, identity });
      completed = true;
      pending = 0n;
      return result;
    } finally {
      if (!completed && pending !== 0n) rollback(pending);
      scope.cleanup();
    }
  }

  function invokeSign(rawName, record, inputBytes, rawArguments, canonical) {
    const scope = createAllocationScope(wasm);
    try {
      const input = scope.allocate(inputBytes);
      const outRequired = scope.allocate(4);
      const outJson = scope.allocate(MAX_OUTPUT_BYTES);
      let status;
      try {
        status = wasm[rawName].apply(wasm, rawArguments(
          record.nativeValue,
          input,
          inputBytes.length,
          outJson,
          outRequired,
        ));
      } catch {
        fail('CRYPTO_FAILURE');
      }
      if (status !== 0) throwStatus(status);
      const required = readRequired(scope, outRequired);
      if (required <= 0 || required > MAX_OUTPUT_BYTES) fail('CRYPTO_FAILURE');
      return parseSignature(scope.bytes(outJson, required), canonical);
    } finally {
      if (scope.cleanup()) fail('CRYPTO_FAILURE');
    }
  }

  const capabilities = {
    async derivePasswordIdentity(input) {
      let callerPassword;
      let password;
      const cleanupSecrets = onceWipe(() => [password, callerPassword]);
      try {
        if (isObject(input)) callerPassword = input.passwordUtf8;
        requireExactObject(input, ['usernameUtf8', 'passwordUtf8', 'accountIndex']);
        const username = copyBytes(input.usernameUtf8, 'INVALID_USERNAME');
        password = copySecretAndWipeCaller(callerPassword, 'INVALID_PASSWORD');
        if (username.length > MAX_MODERN_CREDENTIAL_BYTES) fail('INVALID_USERNAME');
        if (password.length > MAX_MODERN_CREDENTIAL_BYTES) fail('INVALID_PASSWORD');
        const account = requireAccount(input.accountIndex);
        return invokeDerive(
          '_hd_sdn_derive_password_identity',
          ([usernamePointer, passwordPointer]) => [
            usernamePointer, username.length,
            passwordPointer, password.length,
            account,
          ],
          [{ bytes: username }, { bytes: password, secret: true }],
          'sdn-bip32-slip10-purpose-v1',
          'password-scrypt-v2',
          account,
          cleanupSecrets,
        );
      } finally {
        if (cleanupSecrets()) fail('CRYPTO_FAILURE');
      }
    },

    async deriveLegacyPasswordIdentity(input) {
      let callerPassword;
      let password;
      const cleanupSecrets = onceWipe(() => [password, callerPassword]);
      try {
        if (isObject(input)) callerPassword = input.passwordUtf8;
        requireExactObject(input, ['usernameUtf8', 'passwordUtf8', 'accountIndex']);
        const username = copyBytes(input.usernameUtf8, 'INVALID_USERNAME');
        password = copySecretAndWipeCaller(callerPassword, 'INVALID_PASSWORD');
        if (username.length > MAX_LEGACY_CREDENTIAL_BYTES) fail('INVALID_USERNAME');
        if (password.length > MAX_LEGACY_CREDENTIAL_BYTES) fail('INVALID_PASSWORD');
        const account = requireAccount(input.accountIndex);
        return invokeDerive(
          '_hd_sdn_derive_legacy_password_identity',
          ([usernamePointer, passwordPointer]) => [
            usernamePointer, username.length,
            passwordPointer, password.length,
            account,
          ],
          [{ bytes: username }, { bytes: password, secret: true }],
          'sdn-fast-password-auth-v1-legacy',
          'password-fast-v1-legacy',
          account,
          cleanupSecrets,
        );
      } finally {
        if (cleanupSecrets()) fail('CRYPTO_FAILURE');
      }
    },

    async importLegacyMnemonicIdentity(input) {
      let callerMnemonic;
      let mnemonic;
      const cleanupSecrets = onceWipe(() => [mnemonic, callerMnemonic]);
      try {
        if (isObject(input)) callerMnemonic = input.mnemonicUtf8;
        requireExactObject(input, ['mnemonicUtf8', 'accountIndex']);
        mnemonic = copySecretAndWipeCaller(callerMnemonic, 'INVALID_MNEMONIC');
        if (mnemonic.length > MAX_MNEMONIC_BYTES) fail('INVALID_MNEMONIC');
        const account = requireAccount(input.accountIndex);
        return invokeDerive(
          '_hd_sdn_import_legacy_mnemonic_identity',
          ([mnemonicPointer]) => [mnemonicPointer, mnemonic.length, account],
          [{ bytes: mnemonic, secret: true }],
          'sdn-bip39-auth-v1-legacy',
          'bip39-mnemonic-v1-legacy',
          account,
          cleanupSecrets,
        );
      } finally {
        if (cleanupSecrets()) fail('CRYPTO_FAILURE');
      }
    },

    importRememberedIdentity(input) {
      let callerPrf;
      let prf;
      const cleanupSecrets = onceWipe(() => [prf, callerPrf]);
      try {
        if (isObject(input)) callerPrf = input.prfOutput;
        requireExactObject(input, [
          'ciphertextAndTag', 'prfOutput', 'hkdfSalt', 'nonce',
          'canonicalUsernameUtf8', 'canonicalAad',
        ]);
        const ciphertext = copyBytes(input.ciphertextAndTag);
        const salt = copyBytes(input.hkdfSalt);
        const nonce = copyBytes(input.nonce);
        const username = copyBytes(input.canonicalUsernameUtf8, 'INVALID_USERNAME');
        const aad = encodeString(input.canonicalAad, MAX_AAD_BYTES);
        prf = copySecretAndWipeCaller(callerPrf, 'INVALID_REQUEST');
        if (prf.length !== 32 || salt.length !== 32 || nonce.length !== 12 ||
            ciphertext.length < MIN_REMEMBERED_CIPHERTEXT_BYTES ||
            ciphertext.length > MAX_REMEMBERED_CIPHERTEXT_BYTES ||
            username.length > MAX_MODERN_CREDENTIAL_BYTES) {
          fail('INVALID_REQUEST');
        }
        return invokeDerive(
          '_hd_sdn_import_remembered_identity',
          (pointers) => [
            pointers[0], ciphertext.length,
            pointers[1], prf.length,
            pointers[2], salt.length,
            pointers[3], nonce.length,
            pointers[4], username.length,
            pointers[5], aad.length,
          ],
          [
            { bytes: ciphertext }, { bytes: prf, secret: true }, { bytes: salt },
            { bytes: nonce }, { bytes: username }, { bytes: aad },
          ],
          'sdn-bip32-slip10-purpose-v1',
          'password-scrypt-v2',
          0,
          cleanupSecrets,
        );
      } finally {
        if (cleanupSecrets()) fail('CRYPTO_FAILURE');
      }
    },

    signSdnLoginV1(handle, challenge) {
      const record = recordFor(handle);
      const challengeBytes = copyBytes(challenge);
      if (challengeBytes.length !== 32) fail('INVALID_REQUEST');
      return invokeSign(
        '_hd_sdn_sign_login_v1',
        record,
        challengeBytes,
        (native, input, length, output, required) => [
          native, input, length, output, MAX_OUTPUT_BYTES, required,
        ],
        false,
      );
    },

    signSdnLoginV2(handle, request, registryRow) {
      const record = recordFor(handle);
      if (registryRow !== 'sdn-node-console-v2') fail('INVALID_REQUEST');
      const requestBytes = encodeRequest(request);
      return invokeSign(
        '_hd_sdn_sign_login_v2',
        record,
        requestBytes,
        (native, input, length, output, required) => [
          native, input, length, 1, output, MAX_OUTPUT_BYTES, required,
        ],
        true,
      );
    },

    signAssetReviewAuthorityActivation(handle, request, registryRow) {
      const record = recordFor(handle);
      if (registryRow !== 'asset-review-authority-activation-v1') {
        fail('INVALID_REQUEST');
      }
      const requestBytes = encodeRequest(request);
      return invokeSign(
        '_hd_sdn_sign_asset_review_authority_activation',
        record,
        requestBytes,
        (native, input, length, output, required) => [
          native, input, length, 2, output, MAX_OUTPUT_BYTES, required,
        ],
        true,
      );
    },

    signAssetReviewDecision(handle, request, registryRow) {
      const record = recordFor(handle);
      if (registryRow !== 'asset-review-decision-v1') fail('INVALID_REQUEST');
      const requestBytes = encodeRequest(request);
      return invokeSign(
        '_hd_sdn_sign_asset_review_decision',
        record,
        requestBytes,
        (native, input, length, output, required) => [
          native, input, length, 3, output, MAX_OUTPUT_BYTES, required,
        ],
        true,
      );
    },

    sealRememberedIdentity(handle, input) {
      let callerPassword;
      let callerPrf;
      let password;
      let prf;
      let authorityEstablished = false;
      const rejectInputBeforeRead = isDestroyPendingHandle(handle);
      const scope = createAllocationScope(wasm);
      try {
        const record = recordFor(handle);
        authorityEstablished = true;
        if (!rejectInputBeforeRead && isObject(input)) {
          callerPassword = input.passwordUtf8;
          callerPrf = input.prfOutput;
        }
        requireExactObject(input, [
          'passwordUtf8', 'prfOutput', 'hkdfSalt', 'nonce', 'canonicalAad',
        ]);
        const salt = copyBytes(input.hkdfSalt);
        const nonce = copyBytes(input.nonce);
        const aad = encodeString(input.canonicalAad, MAX_AAD_BYTES);
        password = copySecretAndWipeCaller(callerPassword, 'INVALID_PASSWORD');
        prf = copySecretAndWipeCaller(callerPrf, 'INVALID_REQUEST');
        if (password.length > MAX_MODERN_CREDENTIAL_BYTES || prf.length !== 32 ||
            salt.length !== 32 || nonce.length !== 12) {
          fail('INVALID_REQUEST');
        }
        const passwordPointer = scope.allocate(password, true);
        const prfPointer = scope.allocate(prf, true);
        const saltPointer = scope.allocate(salt);
        const noncePointer = scope.allocate(nonce);
        const aadPointer = scope.allocate(aad);
        const outRequired = scope.allocate(4);
        const outBytes = scope.allocate(MAX_OUTPUT_BYTES);
        let status;
        try {
          status = wasm._hd_sdn_seal_remembered_identity(
            record.nativeValue,
            passwordPointer, password.length,
            prfPointer, prf.length,
            saltPointer, salt.length,
            noncePointer, nonce.length,
            aadPointer, aad.length,
            outBytes, MAX_OUTPUT_BYTES, outRequired,
          );
        } catch {
          fail('CRYPTO_FAILURE');
        }
        if (status !== 0) throwStatus(status);
        const required = readRequired(scope, outRequired);
        if (required <= 16 || required > MAX_REMEMBERED_CIPHERTEXT_BYTES) {
          fail('CRYPTO_FAILURE');
        }
        return scope.bytes(outBytes, required);
      } finally {
        const cleanupFailed = scope.cleanup();
        if (!rejectInputBeforeRead && isObject(input)) {
          if (!isUint8Array(callerPassword)) {
            try {
              callerPassword = input.passwordUtf8;
            } catch {
              // Preserve the handle/validation error while attempting cleanup.
            }
          }
          if (!isUint8Array(callerPrf)) {
            try {
              callerPrf = input.prfOutput;
            } catch {
              // Preserve the handle/validation error while attempting cleanup.
            }
          }
        }
        const secretCleanupFailed = wipeAll([
          password, prf, callerPassword, callerPrf,
        ]);
        if (authorityEstablished && (cleanupFailed || secretCleanupFailed)) {
          fail('CRYPTO_FAILURE');
        }
      }
    },

    destroySdnIdentity(handle) {
      if (!isObject(handle)) fail('STALE_HANDLE');
      const record = issuedHandles.get(handle);
      if (!record) fail('STALE_HANDLE');
      if (record.state === 'destroyed') return;
      record.state = 'destroy-pending';
      try {
        wasm._hd_sdn_destroy_identity(record.nativeValue);
      } catch {
        fail('CRYPTO_FAILURE');
      }
      record.nativeValue = 0n;
      record.state = 'destroyed';
    },
  };

  return Object.freeze(capabilities);
}

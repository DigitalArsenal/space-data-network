import {
  cloneSdnPluginManifest,
  HD_WALLET_SDN_PLUGIN_MANIFEST,
} from './sdn-plugin-manifest-source.mjs';

const MANIFEST_EXPORTS = Object.freeze({
  bytesSymbol: 'plugin_get_manifest_flatbuffer',
  sizeSymbol: 'plugin_get_manifest_flatbuffer_size',
});

const textEncoder = new TextEncoder();

function toUint8Array(value, fieldName) {
  if (value instanceof Uint8Array) {
    return new Uint8Array(value);
  }
  if (ArrayBuffer.isView(value)) {
    return new Uint8Array(value.buffer, value.byteOffset, value.byteLength);
  }
  if (value instanceof ArrayBuffer) {
    return new Uint8Array(value);
  }
  throw new TypeError(`${fieldName} must be a Uint8Array, ArrayBufferView, or ArrayBuffer.`);
}

function optionalUint8Array(value, fieldName) {
  if (value === null || value === undefined) {
    return null;
  }
  return toUint8Array(value, fieldName);
}

function cloneFrame(frame, payload) {
  return {
    portId: frame.portId,
    typeRef: frame.typeRef ? { ...frame.typeRef } : null,
    alignment: frame.alignment ?? 8,
    offset: frame.offset ?? 0,
    size: frame.size ?? 0,
    ownership: frame.ownership ?? 'shared',
    generation: frame.generation ?? 0,
    mutability: frame.mutability ?? 'immutable',
    traceId: frame.traceId ?? null,
    streamId: frame.streamId ?? 1,
    sequence: frame.sequence ?? 1,
    payload,
  };
}

function buildOutputFrame(sourceFrame, portId, schemaName, fileIdentifier, payload) {
  return cloneFrame(
    {
      portId,
      typeRef: {
        schemaName,
        fileIdentifier,
        schemaHash: [],
        acceptsAnyFlatbuffer: false,
      },
      alignment: 8,
      offset: 0,
      size: 0,
      ownership: 'shared',
      generation: 0,
      mutability: 'immutable',
      traceId:
        sourceFrame?.traceId ??
        `${HD_WALLET_SDN_PLUGIN_MANIFEST.pluginId}:${portId}`,
      streamId: sourceFrame?.streamId ?? 1,
      sequence: sourceFrame?.sequence ?? 1,
    },
    payload
  );
}

function resolveLastInput(request) {
  if (!Array.isArray(request?.inputs) || request.inputs.length === 0) {
    throw new Error('Plugin invocation requires at least one input frame.');
  }
  return request.inputs[request.inputs.length - 1];
}

function resolveRandomBytes(randomBytes, length, context) {
  if (typeof randomBytes !== 'function') {
    throw new Error(
      'encrypt_fields requires an explicit randomBytes capability callback.'
    );
  }
  const bytes = toUint8Array(randomBytes(length, context), 'randomBytes result');
  if (bytes.length !== length) {
    throw new Error(`randomBytes capability must return exactly ${length} bytes.`);
  }
  return bytes;
}

function detectSigningCurve(payload) {
  const curve = String(payload.curve ?? '').trim();
  if (curve) {
    return curve;
  }
  const algorithm = String(payload.algorithm ?? '').trim();
  if (algorithm.startsWith('ed25519')) {
    return 'ed25519';
  }
  return 'secp256k1';
}

function resolveSigningInput(wallet, payload, curve) {
  const messageBytes = toUint8Array(
    payload.messageBytes ?? payload.message ?? payload.protectedRecordBytes,
    'message'
  );
  if (curve === 'secp256k1') {
    return {
      messageBytes,
      digest: optionalUint8Array(payload.digest, 'digest') ?? wallet.utils.sha256(messageBytes),
    };
  }
  return {
    messageBytes,
    digest: optionalUint8Array(payload.digest, 'digest'),
  };
}

function resolveSignatureEnvelope(wallet, payload, walletSign) {
  const curve = detectSigningCurve(payload);
  const { messageBytes, digest } = resolveSigningInput(wallet, payload, curve);

  if (!payload.signerPrivateKey && typeof walletSign !== 'function') {
    throw new Error(
      'sign_detached requires signerPrivateKey bytes or a walletSign capability callback.'
    );
  }

  if (typeof walletSign === 'function' && !payload.signerPrivateKey) {
    const response = walletSign({
      curve,
      payload,
      messageBytes,
      digest,
    });
    if (!response || typeof response !== 'object') {
      throw new Error('walletSign capability must return a signature envelope.');
    }
    return {
      curve,
      algorithm:
        response.algorithm ??
        (curve === 'ed25519'
          ? digest
            ? 'ed25519-prehash-sha256'
            : 'ed25519'
          : 'secp256k1-sha256'),
      messageBytes,
      digest,
      signature: toUint8Array(response.signature, 'walletSign signature'),
      publicKey: toUint8Array(response.publicKey, 'walletSign publicKey'),
    };
  }

  const privateKey = toUint8Array(payload.signerPrivateKey, 'signerPrivateKey');

  if (curve === 'ed25519') {
    const signatureInput = digest ?? messageBytes;
    return {
      curve,
      algorithm:
        payload.algorithm ??
        (digest ? 'ed25519-prehash-sha256' : 'ed25519'),
      messageBytes,
      digest,
      signature: wallet.curves.ed25519.sign(signatureInput, privateKey),
      publicKey:
        optionalUint8Array(payload.publicKey, 'publicKey') ??
        wallet.curves.ed25519.publicKeyFromSeed(privateKey),
    };
  }

  return {
    curve: 'secp256k1',
    algorithm: payload.algorithm ?? 'secp256k1-sha256',
    messageBytes,
    digest,
    signature: wallet.curves.secp256k1.sign(digest, privateKey),
    publicKey:
      optionalUint8Array(payload.publicKey, 'publicKey') ??
      wallet.curves.publicKeyFromPrivate(privateKey, 0),
  };
}

function resolveVerificationEnvelope(wallet, payload) {
  const curve = detectSigningCurve(payload);
  const signature = toUint8Array(payload.signature, 'signature');
  const publicKey = toUint8Array(payload.publicKey, 'publicKey');
  const { messageBytes, digest } = resolveSigningInput(wallet, payload, curve);
  const verificationInput = curve === 'secp256k1' ? digest : digest ?? messageBytes;
  const valid =
    curve === 'ed25519'
      ? wallet.curves.ed25519.verify(verificationInput, signature, publicKey)
      : wallet.curves.secp256k1.verify(verificationInput, signature, publicKey);

  return {
    curve,
    algorithm:
      payload.algorithm ??
      (curve === 'ed25519'
        ? digest
          ? 'ed25519-prehash-sha256'
          : 'ed25519'
        : 'secp256k1-sha256'),
    messageBytes,
    digest,
    signature,
    publicKey,
    valid,
  };
}

function resolveFieldCurve(payload, field) {
  return String(field?.curve ?? payload.curve ?? 'x25519').trim() || 'x25519';
}

function ecdhForCurve(wallet, curve, privateKey, publicKey) {
  if (curve === 'secp256k1') {
    return wallet.curves.secp256k1.ecdh(privateKey, publicKey);
  }
  if (curve === 'x25519') {
    return wallet.curves.x25519.ecdh(privateKey, publicKey);
  }
  throw new Error(`encrypt_fields does not support curve "${curve}".`);
}

function publicKeyForCurve(wallet, curve, privateKey) {
  if (curve === 'secp256k1') {
    return wallet.curves.publicKeyFromPrivate(privateKey, 0);
  }
  if (curve === 'x25519') {
    return wallet.curves.x25519.publicKey(privateKey);
  }
  throw new Error(`encrypt_fields does not support curve "${curve}".`);
}

function algorithmForCurve(curve) {
  if (curve === 'secp256k1') {
    return 'secp256k1-hkdf-aes-256-gcm';
  }
  return 'x25519-hkdf-aes-256-gcm';
}

function resolveSenderPrivateKey(randomBytes, payload, curve) {
  if (payload.senderPrivateKey) {
    return toUint8Array(payload.senderPrivateKey, 'senderPrivateKey');
  }
  if (curve === 'x25519') {
    return resolveRandomBytes(randomBytes, 32, {
      methodId: 'encrypt_fields',
      purpose: 'senderPrivateKey',
    });
  }
  throw new Error(
    'encrypt_fields requires senderPrivateKey when using secp256k1 field encryption.'
  );
}

function normalizeAad(field, payload) {
  return (
    optionalUint8Array(field?.aad, 'aad') ??
    optionalUint8Array(payload?.aad, 'aad') ??
    new Uint8Array()
  );
}

function encryptFieldsPayload(wallet, payload, randomBytes) {
  const fields = Array.isArray(payload.fields) ? payload.fields : [];
  if (fields.length === 0) {
    throw new Error('encrypt_fields requires at least one field entry.');
  }

  const encryptedFields = [];
  for (const field of fields) {
    const curve = resolveFieldCurve(payload, field);
    const recipientPublicKey = toUint8Array(
      payload.recipientPublicKey ?? field.recipientPublicKey,
      'recipientPublicKey'
    );
    const senderPrivateKey = resolveSenderPrivateKey(randomBytes, payload, curve);
    const senderPublicKey =
      optionalUint8Array(field.senderPublicKey, 'senderPublicKey') ??
      publicKeyForCurve(wallet, curve, senderPrivateKey);
    const salt =
      optionalUint8Array(field.salt, 'salt') ??
      resolveRandomBytes(randomBytes, 32, {
        methodId: 'encrypt_fields',
        fieldPath: field.fieldPath,
        purpose: 'salt',
      });
    const iv =
      optionalUint8Array(field.iv, 'iv') ??
      resolveRandomBytes(randomBytes, 12, {
        methodId: 'encrypt_fields',
        fieldPath: field.fieldPath,
        purpose: 'iv',
      });
    const plaintext = toUint8Array(field.plaintext, 'plaintext');
    const aad = normalizeAad(field, payload);
    const sharedSecret = ecdhForCurve(wallet, curve, senderPrivateKey, recipientPublicKey);
    const hkdfInfo = textEncoder.encode(`field:${field.fieldPath}`);
    const aesKey = wallet.utils.hkdf(sharedSecret, salt, hkdfInfo, 32);
    const { ciphertext, tag } = wallet.utils.aesGcm.encrypt(
      aesKey,
      plaintext,
      iv,
      aad
    );

    encryptedFields.push({
      fieldPath: String(field.fieldPath ?? ''),
      curve,
      algorithm: algorithmForCurve(curve),
      salt,
      iv,
      tag,
      ciphertext,
      senderPublicKey,
      aad,
    });
  }

  return { fields: encryptedFields };
}

function decryptFieldsPayload(wallet, payload) {
  const fields = Array.isArray(payload.fields) ? payload.fields : [];
  if (fields.length === 0) {
    throw new Error('decrypt_fields requires at least one encrypted field entry.');
  }

  const recipientPrivateKey = toUint8Array(
    payload.recipientPrivateKey,
    'recipientPrivateKey'
  );
  const decryptedFields = [];

  for (const field of fields) {
    const curve = resolveFieldCurve(payload, field);
    const senderPublicKey = toUint8Array(field.senderPublicKey, 'senderPublicKey');
    const salt = toUint8Array(field.salt, 'salt');
    const iv = toUint8Array(field.iv, 'iv');
    const tag = toUint8Array(field.tag, 'tag');
    const ciphertext = toUint8Array(field.ciphertext, 'ciphertext');
    const aad = normalizeAad(field, payload);
    const sharedSecret = ecdhForCurve(wallet, curve, recipientPrivateKey, senderPublicKey);
    const hkdfInfo = textEncoder.encode(`field:${field.fieldPath}`);
    const aesKey = wallet.utils.hkdf(sharedSecret, salt, hkdfInfo, 32);
    const plaintext = wallet.utils.aesGcm.decrypt(aesKey, ciphertext, tag, iv, aad);

    decryptedFields.push({
      fieldPath: String(field.fieldPath ?? ''),
      curve,
      algorithm: field.algorithm ?? algorithmForCurve(curve),
      plaintext,
      aad,
    });
  }

  return { fields: decryptedFields };
}

function readEmbeddedManifestBytes(wasm) {
  const getBytes = wasm._plugin_get_manifest_flatbuffer;
  const getSize = wasm._plugin_get_manifest_flatbuffer_size;
  if (typeof getBytes !== 'function' || typeof getSize !== 'function') {
    throw new Error('Embedded plugin manifest exports are not available in this build.');
  }
  const pointer = Number(getBytes());
  const size = Number(getSize());
  if (!Number.isFinite(pointer) || !Number.isFinite(size) || size <= 0) {
    throw new Error('Embedded plugin manifest exports returned invalid values.');
  }
  return wasm.HEAPU8.slice(pointer, pointer + size);
}

function buildInvocationResult(outputs) {
  return {
    outputs,
    backlogRemaining: 0,
    yielded: false,
  };
}

export function createSdnPluginContract({ wallet, wasm, randomBytes = null, walletSign = null }) {
  function invoke(methodId, request = {}) {
    const inputFrame = resolveLastInput(request);
    const payload = inputFrame.payload ?? {};

    switch (methodId) {
      case 'encrypt_fields':
        return buildInvocationResult([
          buildOutputFrame(
            inputFrame,
            'encrypted_fields',
            'EncryptedFieldSet.fbs',
            'EFLD',
            encryptFieldsPayload(wallet, payload, randomBytes)
          ),
        ]);
      case 'decrypt_fields':
        return buildInvocationResult([
          buildOutputFrame(
            inputFrame,
            'field_set',
            'FieldSelectionBundle.fbs',
            'FSLB',
            decryptFieldsPayload(wallet, payload)
          ),
        ]);
      case 'sign_detached':
        return buildInvocationResult([
          buildOutputFrame(
            inputFrame,
            'signature',
            'DetachedSignature.fbs',
            'SIGD',
            resolveSignatureEnvelope(wallet, payload, walletSign)
          ),
        ]);
      case 'verify_detached':
        return buildInvocationResult([
          buildOutputFrame(
            inputFrame,
            'verification',
            'DetachedVerificationResult.fbs',
            'SIGV',
            resolveVerificationEnvelope(wallet, payload)
          ),
        ]);
      default:
        throw new Error(`Unknown SDN plugin method "${methodId}".`);
    }
  }

  return {
    manifest: cloneSdnPluginManifest(),
    manifestExports: MANIFEST_EXPORTS,
    getManifest() {
      return cloneSdnPluginManifest();
    },
    getManifestBytes() {
      return readEmbeddedManifestBytes(wasm);
    },
    withCapabilities(capabilities = {}) {
      return createSdnPluginContract({
        wallet,
        wasm,
        randomBytes:
          capabilities.randomBytes !== undefined
            ? capabilities.randomBytes
            : randomBytes,
        walletSign:
          capabilities.walletSign !== undefined
            ? capabilities.walletSign
            : walletSign,
      });
    },
    invoke,
    encrypt_fields(request) {
      return invoke('encrypt_fields', request);
    },
    decrypt_fields(request) {
      return invoke('decrypt_fields', request);
    },
    sign_detached(request) {
      return invoke('sign_detached', request);
    },
    verify_detached(request) {
      return invoke('verify_detached', request);
    },
  };
}

export { MANIFEST_EXPORTS as SDN_PLUGIN_MANIFEST_EXPORTS };


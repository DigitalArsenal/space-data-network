import * as flatbuffers from 'flatbuffers';
import {
  decryptProtectedBytes,
  extractPublicationRecordCollection,
} from 'space-data-module-sdk/transport';
import { createBrowserModuleHarness } from 'space-data-module-sdk/testing/browser';
import { KMF } from 'spacedatastandards.org/lib/js/REC/KMF.js';
import { REC } from 'spacedatastandards.org/lib/js/REC/REC.js';
import { Record } from 'spacedatastandards.org/lib/js/REC/Record.js';

import { x25519ECDH } from '../../crypto/hd-wallet';
import type {
  ModuleDeliveryEvent,
  ModuleDeliveryObserver,
} from '../../module-delivery';

const DEFAULT_GRANT_PAYLOAD_CONTEXT = 'space-data-network/module-delivery/grant/v1';
const KMF_KEY_BYTES_FIELD_ID = 4;

export interface WrappedContentKeyLike {
  keyBytes: Uint8Array;
  encryptedPayload?: Uint8Array;
  providerEphemeralPublicKey: Uint8Array;
  header?: {
    context?: string;
    rootType?: string;
  };
  keyMaterialRootType?: string;
}

export interface LoadedModuleHarnessLike {
  invoke: (request: unknown) => Promise<unknown>;
  destroy?: () => void;
}

function cloneBytes(bytes: Uint8Array): Uint8Array<ArrayBuffer> {
  return new Uint8Array(bytes);
}

export async function unwrapGrantContentKey(
  wrappedContentKey: WrappedContentKeyLike,
  recipientPrivateKey: Uint8Array,
  observer?: ModuleDeliveryObserver,
): Promise<Uint8Array> {
  emit(observer, {
    stage: 'unwrap-start',
    timestamp: Date.now(),
  });

  if (wrappedContentKey.keyBytes.length > 0) {
    const keyBytes = wrappedContentKey.keyBytes.slice();
    emit(observer, {
      stage: 'unwrap-complete',
      timestamp: Date.now(),
      bytes: keyBytes.length,
    });
    return keyBytes;
  }

  const encryptedPayload = wrappedContentKey.encryptedPayload?.slice();
  if (!encryptedPayload || encryptedPayload.length === 0) {
    throw new Error('wrapped content key payload missing');
  }

  const rootType = normalizeRootType(
    wrappedContentKey.header?.rootType ?? wrappedContentKey.keyMaterialRootType,
  );
  if (rootType === 'KMF') {
    const keyBytes = decodeKmfKeyBytes(encryptedPayload);
    emit(observer, {
      stage: 'unwrap-complete',
      timestamp: Date.now(),
      bytes: keyBytes.length,
    });
    return keyBytes;
  }

  if (rootType !== 'REC') {
    throw new Error(`unsupported wrapped content key root type: ${rootType || '<missing>'}`);
  }

  const sharedSecret = await x25519ECDH(
    recipientPrivateKey,
    wrappedContentKey.providerEphemeralPublicKey,
  );
  const payloadKey = await hkdfBytes(
    sharedSecret,
    new TextEncoder().encode(
      wrappedContentKey.header?.context ?? DEFAULT_GRANT_PAYLOAD_CONTEXT,
    ),
    32,
  );
  const keyBytes = await decodeRecWrappedKmfKeyBytes(encryptedPayload, payloadKey);
  emit(observer, {
    stage: 'unwrap-complete',
    timestamp: Date.now(),
    bytes: keyBytes.length,
  });
  return keyBytes;
}

export async function decryptEncryptedModuleBundle(
  encryptedBundleBytes: Uint8Array,
  contentKey: Uint8Array,
  observer?: ModuleDeliveryObserver,
): Promise<Uint8Array> {
  emit(observer, {
    stage: 'decrypt-start',
    timestamp: Date.now(),
    bytes: encryptedBundleBytes.length,
  });

  const protectedArtifact = extractPublicationRecordCollection(encryptedBundleBytes);
  if (protectedArtifact?.enc) {
    const decryptedBundle = await decryptProtectedBytes({
      protectedBytes: encryptedBundleBytes,
      recipientPrivateKey: cloneBytes(contentKey),
    });
    emit(observer, {
      stage: 'decrypt-complete',
      timestamp: Date.now(),
      bytes: decryptedBundle.length,
    });
    return decryptedBundle;
  }

  if (encryptedBundleBytes.length <= 28) {
    throw new Error('encrypted bundle must contain iv and authentication tag');
  }
  if (contentKey.length !== 32) {
    throw new Error(`expected 32-byte AES-GCM content key, got ${contentKey.length}`);
  }

  const iv = encryptedBundleBytes.subarray(0, 12);
  const ciphertext = encryptedBundleBytes.subarray(12);
  const key = await crypto.subtle.importKey(
    'raw',
    cloneBytes(contentKey),
    'AES-GCM',
    false,
    ['decrypt'],
  );
  const plaintext = await crypto.subtle.decrypt(
    { name: 'AES-GCM', iv: cloneBytes(iv) },
    key,
    cloneBytes(ciphertext),
  );
  const decryptedBundle = new Uint8Array(plaintext);
  emit(observer, {
    stage: 'decrypt-complete',
    timestamp: Date.now(),
    bytes: decryptedBundle.length,
  });
  return decryptedBundle;
}

export async function loadDecryptedModule(
  wasmBytes: Uint8Array,
  observer?: ModuleDeliveryObserver,
): Promise<LoadedModuleHarnessLike> {
  emit(observer, {
    stage: 'sdk-load-start',
    timestamp: Date.now(),
    bytes: wasmBytes.length,
  });
  const harness = await createBrowserModuleHarness({
    wasmSource: wasmBytes,
  });
  emit(observer, {
    stage: 'sdk-load-complete',
    timestamp: Date.now(),
    bytes: wasmBytes.length,
  });
  return harness as LoadedModuleHarnessLike;
}

export async function invokeLoadedModule<TResult = unknown>(
  harness: LoadedModuleHarnessLike,
  request: unknown,
  observer?: ModuleDeliveryObserver,
): Promise<TResult> {
  emit(observer, {
    stage: 'invoke-start',
    timestamp: Date.now(),
  });

  try {
    const result = await harness.invoke(request);
    emit(observer, {
      stage: 'invoke-result',
      timestamp: Date.now(),
      detail: summarizeInvokeResult(result),
    });
    return result as TResult;
  } catch (error) {
    emit(observer, {
      stage: 'invoke-error',
      timestamp: Date.now(),
      error: formatUnknownError(error),
    });
    throw error;
  }
}

async function decodeRecWrappedKmfKeyBytes(
  encryptedPayload: Uint8Array,
  payloadKey: Uint8Array,
): Promise<Uint8Array> {
  const rec = REC.getRootAsREC(new flatbuffers.ByteBuffer(encryptedPayload));
  const record = rec.RECORDS(0, new Record());
  if (!record) {
    throw new Error('wrapped content key REC record missing');
  }

  const kmf = record.value(new KMF());
  if (!kmf) {
    throw new Error('wrapped content key REC did not contain a KMF payload');
  }

  const keyBytesView = kmf.keyBytesArray();
  if (!keyBytesView || keyBytesView.length === 0) {
    throw new Error('wrapped content key KMF key bytes missing');
  }

  await decryptFlatbufferVectorInPlace(keyBytesView, payloadKey, KMF_KEY_BYTES_FIELD_ID, 0);
  return keyBytesView.slice();
}

function decodeKmfKeyBytes(bytes: Uint8Array): Uint8Array {
  const kmf = KMF.getRootAsKMF(new flatbuffers.ByteBuffer(bytes));
  const keyBytes = kmf.keyBytesArray();
  if (!keyBytes || keyBytes.length === 0) {
    throw new Error('wrapped content key KMF key bytes missing');
  }
  return keyBytes.slice();
}

async function decryptFlatbufferVectorInPlace(
  bytes: Uint8Array,
  payloadKey: Uint8Array,
  fieldId: number,
  recordIndex: number,
): Promise<void> {
  const fieldKey = await deriveFlatbufferFieldBytes(
    payloadKey,
    'flatbuffers-field',
    fieldId,
    recordIndex,
    32,
  );
  const fieldIv = await deriveFlatbufferFieldBytes(
    payloadKey,
    'flatbuffers-iv',
    fieldId,
    recordIndex,
    16,
  );
  const cryptoKey = await crypto.subtle.importKey(
    'raw',
    cloneBytes(fieldKey),
    'AES-CTR',
    false,
    ['decrypt'],
  );
  const plaintext = await crypto.subtle.decrypt(
    {
      name: 'AES-CTR',
      counter: cloneBytes(fieldIv),
      length: 128,
    },
    cryptoKey,
    cloneBytes(bytes),
  );
  bytes.set(new Uint8Array(plaintext));
}

async function deriveFlatbufferFieldBytes(
  payloadKey: Uint8Array,
  label: string,
  fieldId: number,
  recordIndex: number,
  outputLength: number,
): Promise<Uint8Array> {
  const labelBytes = new TextEncoder().encode(label);
  const info = new Uint8Array(labelBytes.length + 2 + 4);
  info.set(labelBytes, 0);
  const view = new DataView(info.buffer);
  view.setUint16(labelBytes.length, fieldId, false);
  view.setUint32(labelBytes.length + 2, recordIndex >>> 0, false);
  return hkdfBytes(payloadKey, info, outputLength);
}

async function hkdfBytes(
  inputKeyMaterial: Uint8Array,
  info: Uint8Array,
  outputLength: number,
): Promise<Uint8Array> {
  const keyMaterial = await crypto.subtle.importKey(
    'raw',
    cloneBytes(inputKeyMaterial),
    'HKDF',
    false,
    ['deriveBits'],
  );
  const bits = await crypto.subtle.deriveBits(
    {
      name: 'HKDF',
      hash: 'SHA-256',
      salt: cloneBytes(new Uint8Array(0)),
      info: cloneBytes(info),
    },
    keyMaterial,
    outputLength * 8,
  );
  return new Uint8Array(bits);
}

function normalizeRootType(value: string | undefined): string {
  return String(value || '').trim().replace(/^\$/, '').toUpperCase();
}

function summarizeInvokeResult(result: unknown): string {
  if (!result || typeof result !== 'object') {
    return String(result);
  }

  const statusCode = Number((result as { statusCode?: unknown }).statusCode);
  if (Number.isFinite(statusCode)) {
    return `statusCode=${statusCode}`;
  }

  return 'invoke completed';
}

function emit(
  observer: ModuleDeliveryObserver | undefined,
  event: ModuleDeliveryEvent,
): void {
  observer?.onEvent?.(event);
}

function formatUnknownError(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

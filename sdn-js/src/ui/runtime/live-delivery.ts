import * as flatbuffers from 'flatbuffers';
import { createBrowserModuleHarness } from 'space-data-module-sdk/testing/browser';
import { KMF } from 'spacedatastandards.org/lib/js/REC/KMF.js';

import { aesGcmDecryptWithIv } from '../../crypto/hd-wallet';
import type {
  ModuleDeliveryEvent,
  ModuleDeliveryObserver,
} from '../../module-delivery';

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
  invokeDirect?: (request: unknown) => Promise<unknown>;
  runtime?: {
    surface?: 'direct' | 'command' | string;
  };
  memory?: WebAssembly.Memory;
  destroy?: () => void;
}

export interface LoadDecryptedModuleOptions {
  observer?: ModuleDeliveryObserver;
  sharedMemory?: boolean;
  initialMemoryBytes?: number;
  maximumMemoryBytes?: number;
  /**
   * BrowserHost capability adapters (ipfs/storage/pubsub/walletSign, e.g.
   * from createModuleHostCapabilityAdapters) granting the guest module host
   * capabilities. Forwarded as hostOptions.capabilityAdapters.
   */
  capabilityAdapters?: Record<string, unknown>;
  /** Extra BrowserHost options forwarded verbatim to the harness. */
  hostOptions?: Record<string, unknown>;
}

export interface GrantProtectedModuleBundleInput {
  grantResponseBytes: Uint8Array;
  encryptedBundleBytes?: Uint8Array;
}

export interface ClientDecryptLike {
  decryptArtifact(
    firstArg: {
      grantResponseBytes: Uint8Array;
      encryptedBundleBytes?: Uint8Array;
    },
    privateKey: Uint8Array,
  ): Promise<Uint8Array>;
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

  const rootType = normalizeRootType(
    wrappedContentKey.header?.rootType ?? wrappedContentKey.keyMaterialRootType,
  );
  if (rootType === 'KMF') {
    const encryptedPayload = wrappedContentKey.encryptedPayload?.slice();
    if (!encryptedPayload || encryptedPayload.length === 0) {
      throw new Error('wrapped content key payload missing');
    }
    const keyBytes = decodeKmfKeyBytes(encryptedPayload);
    emit(observer, {
      stage: 'unwrap-complete',
      timestamp: Date.now(),
      bytes: keyBytes.length,
    });
    return keyBytes;
  }

  void recipientPrivateKey;
  throw new Error(
    'Encrypted grant key unwrap must run through the WASM client-decrypt module; use decryptArtifact(grantResponseBytes, privateKey).',
  );
}

export async function decryptEncryptedModuleBundle(
  encryptedBundleBytes: Uint8Array,
  contentKey: Uint8Array,
  aadOrObserver?: Uint8Array | ModuleDeliveryObserver,
  observer?: ModuleDeliveryObserver,
): Promise<Uint8Array> {
  const aadBytes = aadOrObserver instanceof Uint8Array ? aadOrObserver : undefined;
  const deliveryObserver = aadOrObserver instanceof Uint8Array ? observer : aadOrObserver;
  emit(deliveryObserver, {
    stage: 'decrypt-start',
    timestamp: Date.now(),
    bytes: encryptedBundleBytes.length,
  });

  if (encryptedBundleBytes.length <= 28) {
    throw new Error('encrypted bundle must contain iv and authentication tag');
  }
  if (contentKey.length !== 32) {
    throw new Error(`expected 32-byte AES-GCM content key, got ${contentKey.length}`);
  }

  const iv = encryptedBundleBytes.subarray(0, 12);
  const ciphertext = encryptedBundleBytes.subarray(12);
  const decryptedBundle = await aesGcmDecryptWithIv(
    cloneBytes(contentKey),
    cloneBytes(ciphertext),
    cloneBytes(iv),
    aadBytes ? cloneBytes(aadBytes) : undefined,
  );
  emit(deliveryObserver, {
    stage: 'decrypt-complete',
    timestamp: Date.now(),
    bytes: decryptedBundle.length,
  });
  return decryptedBundle;
}

export async function decryptGrantProtectedModuleBundle(
  input: GrantProtectedModuleBundleInput,
  recipientPrivateKey: Uint8Array,
  clientDecrypt: ClientDecryptLike,
  observer?: ModuleDeliveryObserver,
): Promise<Uint8Array> {
  emit(observer, {
    stage: 'unwrap-start',
    timestamp: Date.now(),
  });
  if (input.grantResponseBytes.length === 0) {
    throw new Error('grant response bytes are required for client-decrypt');
  }
  if (recipientPrivateKey.length === 0) {
    throw new Error('recipient private key is required for client-decrypt');
  }

  emit(observer, {
    stage: 'decrypt-start',
    timestamp: Date.now(),
    bytes: input.encryptedBundleBytes?.length ?? input.grantResponseBytes.length,
  });
  const decryptedBundle = await clientDecrypt.decryptArtifact(
    {
      grantResponseBytes: cloneBytes(input.grantResponseBytes),
      encryptedBundleBytes: input.encryptedBundleBytes
        ? cloneBytes(input.encryptedBundleBytes)
        : undefined,
    },
    cloneBytes(recipientPrivateKey),
  );
  emit(observer, {
    stage: 'decrypt-complete',
    timestamp: Date.now(),
    bytes: decryptedBundle.length,
  });
  return decryptedBundle;
}

export async function loadDecryptedModule(
  wasmBytes: Uint8Array,
  observerOrOptions?: ModuleDeliveryObserver | LoadDecryptedModuleOptions,
): Promise<LoadedModuleHarnessLike> {
  const options = normalizeLoadOptions(observerOrOptions);
  emit(options.observer, {
    stage: 'sdk-load-start',
    timestamp: Date.now(),
    bytes: wasmBytes.length,
  });
  const hostOptions =
    options.capabilityAdapters || options.hostOptions
      ? {
          ...options.hostOptions,
          ...(options.capabilityAdapters
            ? { capabilityAdapters: options.capabilityAdapters }
            : {}),
        }
      : undefined;
  const harness = await createBrowserModuleHarness({
    wasmSource: wasmBytes,
    sharedMemory: options.sharedMemory,
    initialMemoryBytes: options.initialMemoryBytes,
    maximumMemoryBytes: options.maximumMemoryBytes,
    ...(hostOptions ? { hostOptions } : {}),
  });
  emit(options.observer, {
    stage: 'sdk-load-complete',
    timestamp: Date.now(),
    bytes: wasmBytes.length,
  });
  return harness as LoadedModuleHarnessLike;
}

function normalizeLoadOptions(
  observerOrOptions?: ModuleDeliveryObserver | LoadDecryptedModuleOptions,
): LoadDecryptedModuleOptions {
  if (!observerOrOptions) {
    return {};
  }
  const options = observerOrOptions as LoadDecryptedModuleOptions;
  if (
    'sharedMemory' in options ||
    'initialMemoryBytes' in options ||
    'maximumMemoryBytes' in options ||
    'capabilityAdapters' in options ||
    'hostOptions' in options ||
    'observer' in options
  ) {
    return options;
  }
  return {
    observer: observerOrOptions as ModuleDeliveryObserver,
  };
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
    const invokeRequest = prepareInvokeRequest(harness, request);
    const invoke = shouldUseDirectExternalArena(harness, request) && harness.invokeDirect
      ? harness.invokeDirect
      : harness.invoke;
    const result = await invoke(invokeRequest);
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

function shouldUseDirectExternalArena(harness: LoadedModuleHarnessLike, request: unknown): boolean {
  if (
    harness.runtime?.surface !== 'direct' ||
    !isSharedArrayBufferLike(harness.memory?.buffer)
  ) {
    return false;
  }
  return request !== null &&
    typeof request === 'object' &&
    !Array.isArray(request) &&
    !(request instanceof Uint8Array) &&
    !('externalArena' in request);
}

function isSharedArrayBufferLike(value: ArrayBufferLike | undefined): boolean {
  return value !== undefined &&
    typeof SharedArrayBuffer === 'function' &&
    (value instanceof SharedArrayBuffer ||
      Object.prototype.toString.call(value) === '[object SharedArrayBuffer]');
}

function prepareInvokeRequest(harness: LoadedModuleHarnessLike, request: unknown): unknown {
  if (!shouldUseDirectExternalArena(harness, request)) {
    return request;
  }
  return {
    ...(request as Record<string, unknown>),
    externalArena: harness.memory ? new Uint8Array(harness.memory.buffer) : new Uint8Array(0),
  };
}

function decodeKmfKeyBytes(bytes: Uint8Array): Uint8Array {
  const kmf = KMF.getRootAsKMF(new flatbuffers.ByteBuffer(bytes));
  const keyBytes = kmf.keyBytesArray();
  if (!keyBytes || keyBytes.length === 0) {
    throw new Error('wrapped content key KMF key bytes missing');
  }
  return keyBytes.slice();
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

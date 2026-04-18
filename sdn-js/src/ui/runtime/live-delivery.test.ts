import * as flatbuffers from 'flatbuffers';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import {
  encryptBytesForRecipient,
  generateX25519Keypair,
} from 'space-data-module-sdk/transport';
import { KMF } from 'spacedatastandards.org/lib/js/REC/KMF.js';
import { REC } from 'spacedatastandards.org/lib/js/REC/REC.js';
import { Record } from 'spacedatastandards.org/lib/js/REC/Record.js';
import { RecordType } from 'spacedatastandards.org/lib/js/REC/RecordType.js';
import { keyMaterialAlgorithm } from 'spacedatastandards.org/lib/js/REC/keyMaterialAlgorithm.js';
import { keyMaterialEncoding } from 'spacedatastandards.org/lib/js/REC/keyMaterialEncoding.js';
import { keyMaterialRole } from 'spacedatastandards.org/lib/js/REC/keyMaterialRole.js';

const { createBrowserModuleHarness } = vi.hoisted(() => ({
  createBrowserModuleHarness: vi.fn(async () => ({
    invoke: vi.fn(async () => ({
      statusCode: 0,
      outputs: [],
    })),
  })),
}));

const { x25519ECDH } = vi.hoisted(() => ({
  x25519ECDH: vi.fn(async () => new Uint8Array(32).fill(9)),
}));

vi.mock('space-data-module-sdk/testing/browser', () => ({
  createBrowserModuleHarness,
}));

vi.mock('../../crypto/hd-wallet', () => {
  return {
    x25519ECDH,
  };
});

import {
  decryptEncryptedModuleBundle,
  invokeLoadedModule,
  loadDecryptedModule,
  unwrapGrantContentKey,
} from './live-delivery';

const GRANT_PAYLOAD_CONTEXT = 'space-data-network/module-delivery/grant/v1';
const KMF_KEY_BYTES_FIELD_ID = 4;

describe('live-delivery', () => {
  beforeEach(() => {
    createBrowserModuleHarness.mockClear();
    x25519ECDH.mockClear();
  });

  it('unwraps a REC-wrapped content key and emits decrypt/load/invoke lifecycle events', async () => {
    const contentKey = new Uint8Array(32).fill(4);
    const wrappedContentKey = await createWrappedContentKeyFixture(contentKey);
    const deliveryEvents: string[] = [];

    const unwrapped = await unwrapGrantContentKey(
      wrappedContentKey,
      new Uint8Array(32).fill(7),
      {
        onEvent(event) {
          deliveryEvents.push(event.stage);
        },
      },
    );

    expect(unwrapped).toEqual(contentKey);

    const encryptedBundleBytes = await encryptBundleBytes(
      new TextEncoder().encode('live bundle'),
      contentKey,
    );
    const decryptedBundle = await decryptEncryptedModuleBundle(
      encryptedBundleBytes,
      contentKey,
      {
        onEvent(event) {
          deliveryEvents.push(event.stage);
        },
      },
    );

    expect(new TextDecoder().decode(decryptedBundle)).toBe('live bundle');

    const harness = await loadDecryptedModule(new Uint8Array([0, 97, 115, 109]), {
      onEvent(event) {
        deliveryEvents.push(event.stage);
      },
    });
    const response = await invokeLoadedModule(
      harness,
      { methodId: 'echo', inputs: [] },
      {
        onEvent(event) {
          deliveryEvents.push(event.stage);
        },
      },
    );

    expect(response.statusCode).toBe(0);
    expect(createBrowserModuleHarness).toHaveBeenCalledTimes(1);
    expect(deliveryEvents).toEqual([
      'unwrap-start',
      'unwrap-complete',
      'decrypt-start',
      'decrypt-complete',
      'sdk-load-start',
      'sdk-load-complete',
      'invoke-start',
      'invoke-result',
    ]);
  });

  it('decrypts SDK REC-protected fetched artifacts with a wrapped X25519 private key', async () => {
    const plaintext = new TextEncoder().encode('sdk protected bundle');
    const recipient = await generateX25519Keypair();
    const protectedEnvelope = await encryptBytesForRecipient({
      plaintext,
      recipientPublicKey: recipient.publicKey,
      context: 'space-data-module-sdk/package',
      rootType: 'WASM',
    });
    const protectedBytes = Uint8Array.from(
      Buffer.from(protectedEnvelope.protectedBlobBase64, 'base64'),
    );

    const decryptedBundle = await decryptEncryptedModuleBundle(
      protectedBytes,
      recipient.privateKey,
    );

    expect(decryptedBundle).toEqual(plaintext);
  });
});

async function createWrappedContentKeyFixture(contentKey: Uint8Array) {
  const builder = new flatbuffers.Builder(256);
  const versionOffset = builder.createString('1.0.0');
  const keyIdOffset = builder.createString('module-key');
  const keyBytesOffset = KMF.createKeyBytesVector(builder, contentKey);
  const kmfOffset = KMF.createKMF(
    builder,
    keyIdOffset,
    keyMaterialRole.PublicationContent,
    keyMaterialAlgorithm.Aes256Gcm,
    keyMaterialEncoding.RawBytes,
    keyBytesOffset,
    1,
    1n,
  );
  const standardOffset = builder.createString('KMF');
  const recordOffset = Record.createRecord(builder, RecordType.KMF, kmfOffset, standardOffset);
  const recordsOffset = REC.createRecordsVector(builder, [recordOffset]);
  const recOffset = REC.createREC(builder, versionOffset, recordsOffset);
  REC.finishRECBuffer(builder, recOffset);

  const encryptedPayload = builder.asUint8Array();
  const bb = new flatbuffers.ByteBuffer(encryptedPayload);
  const rec = REC.getRootAsREC(bb);
  const record = rec.RECORDS(0, new Record());
  const kmf = record?.value(new KMF());
  const keyBytesView = kmf?.keyBytesArray();
  if (!keyBytesView) {
    throw new Error('fixture key bytes missing');
  }

  const payloadKey = await hkdfBytes(new Uint8Array(32).fill(9), new TextEncoder().encode(GRANT_PAYLOAD_CONTEXT), 32);
  await cryptFlatbufferVectorInPlace(keyBytesView, payloadKey, KMF_KEY_BYTES_FIELD_ID, 0);

  return {
    keyBytes: new Uint8Array(0),
    encryptedPayload,
    providerEphemeralPublicKey: new Uint8Array(32).fill(3),
    keyMaterialRootType: 'REC',
    header: {
      context: GRANT_PAYLOAD_CONTEXT,
      rootType: 'REC',
    },
  };
}

async function encryptBundleBytes(plaintext: Uint8Array, contentKey: Uint8Array): Promise<Uint8Array> {
  const iv = new Uint8Array(12).fill(1);
  const cryptoKey = await crypto.subtle.importKey(
    'raw',
    contentKey,
    'AES-GCM',
    false,
    ['encrypt'],
  );
  const ciphertext = await crypto.subtle.encrypt(
    { name: 'AES-GCM', iv },
    cryptoKey,
    plaintext,
  );
  const encrypted = new Uint8Array(iv.length + ciphertext.byteLength);
  encrypted.set(iv, 0);
  encrypted.set(new Uint8Array(ciphertext), iv.length);
  return encrypted;
}

async function cryptFlatbufferVectorInPlace(
  bytes: Uint8Array,
  payloadKey: Uint8Array,
  fieldId: number,
  recordIndex: number,
): Promise<void> {
  const fieldKey = await deriveFlatbufferFieldBytes(payloadKey, 'flatbuffers-field', fieldId, recordIndex, 32);
  const fieldIv = await deriveFlatbufferFieldBytes(payloadKey, 'flatbuffers-iv', fieldId, recordIndex, 16);
  const cryptoKey = await crypto.subtle.importKey(
    'raw',
    fieldKey,
    'AES-CTR',
    false,
    ['encrypt'],
  );
  const ciphertext = await crypto.subtle.encrypt(
    {
      name: 'AES-CTR',
      counter: fieldIv,
      length: 128,
    },
    cryptoKey,
    bytes.slice(),
  );
  bytes.set(new Uint8Array(ciphertext));
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
    inputKeyMaterial,
    'HKDF',
    false,
    ['deriveBits'],
  );
  const bits = await crypto.subtle.deriveBits(
    {
      name: 'HKDF',
      hash: 'SHA-256',
      salt: new Uint8Array(0),
      info,
    },
    keyMaterial,
    outputLength * 8,
  );
  return new Uint8Array(bits);
}

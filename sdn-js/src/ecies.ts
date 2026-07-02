/**
 * SDN unified cross-runtime ECIES (docs/UNIFIED_ECIES.md) — JS/browser side.
 *
 * Consumes/produces the same SDS `$ENC` header + `$KMF` payload as the Go
 * reference (sdn-server internal/ecies) and the C++/WASM path. The ECDH step
 * is parameterized by `ENC.KEY_EXCHANGE` (X25519 or Secp256k1); everything
 * after — HKDF-SHA256 key derivation and AES-256-CTR field encryption — is
 * curve-agnostic and byte-identical across runtimes.
 *
 * All crypto goes through the wasm wallet (native/WASM crypto boundary — no
 * WebCrypto, per the repo policy).
 */

import * as flatbuffers from 'flatbuffers';
import { ENC } from 'spacedatastandards.org/lib/js/ENC/ENC.js';
import { KMF } from 'spacedatastandards.org/lib/js/KMF/KMF.js';
import {
  x25519ECDH,
  x25519PublicKey,
  secp256k1ECDH,
  secp256k1PublicKey,
  hkdfSha256,
  aesCtrDecryptWithIv,
  randomBytes,
} from './crypto/hd-wallet';

/** Grant domain separator — matches the Go/C++ kGrantPayloadContext default. */
export const DEFAULT_GRANT_CONTEXT = 'space-data-network/module-delivery/grant/v1';

export enum EciesKeyExchange {
  X25519 = 0,
  Secp256k1 = 1,
  P256 = 2,
}

const KMF_KEY_BYTES_FIELD_ID = 4;
const KMF_KEY_BYTES_RECORD_INDEX = 0;
const AES_KEY_BYTES = 32;
const CTR_IV_BYTES = 16;
const CONTENT_KEY_BYTES = 32;

const textEncoder = new TextEncoder();

function be16(value: number): Uint8Array {
  return new Uint8Array([(value >> 8) & 0xff, value & 0xff]);
}
function be32(value: number): Uint8Array {
  return new Uint8Array([
    (value >>> 24) & 0xff,
    (value >>> 16) & 0xff,
    (value >>> 8) & 0xff,
    value & 0xff,
  ]);
}
function concat(...parts: Uint8Array[]): Uint8Array {
  let total = 0;
  for (const p of parts) total += p.length;
  const out = new Uint8Array(total);
  let off = 0;
  for (const p of parts) {
    out.set(p, off);
    off += p.length;
  }
  return out;
}

async function ecdh(
  kx: EciesKeyExchange,
  privateKey: Uint8Array,
  publicKey: Uint8Array,
): Promise<Uint8Array> {
  switch (kx) {
    case EciesKeyExchange.X25519:
      return x25519ECDH(privateKey, publicKey);
    case EciesKeyExchange.Secp256k1:
      // Raw X coordinate (RFC 5903), matching Go decred GenerateSharedSecret
      // and CryptoPP ECDH<ECP>.Agree.
      return secp256k1ECDH(privateKey, publicKey);
    default:
      throw new Error(`ecies: unsupported key exchange ${kx}`);
  }
}

/** HKDF-SHA256 with an empty salt (RFC 5869 zero-salt), matching Go/C++. */
async function hkdf(ikm: Uint8Array, info: Uint8Array, length: number): Promise<Uint8Array> {
  return hkdfSha256(ikm, new Uint8Array(0), info, length);
}

async function deriveMasterKey(shared: Uint8Array, context: string): Promise<Uint8Array> {
  return hkdf(shared, textEncoder.encode(context), AES_KEY_BYTES);
}

async function fieldCipherXOR(master: Uint8Array, data: Uint8Array): Promise<Uint8Array> {
  const fieldKey = await hkdf(
    master,
    concat(textEncoder.encode('flatbuffers-field'), be16(KMF_KEY_BYTES_FIELD_ID), be32(KMF_KEY_BYTES_RECORD_INDEX)),
    AES_KEY_BYTES,
  );
  const fieldIV = await hkdf(
    master,
    concat(textEncoder.encode('flatbuffers-iv'), be16(KMF_KEY_BYTES_FIELD_ID), be32(KMF_KEY_BYTES_RECORD_INDEX)),
    CTR_IV_BYTES,
  );
  // AES-256-CTR is its own inverse; decrypt(key, data, iv) == the XOR transform,
  // so it serves both wrap (over plaintext) and unwrap (over ciphertext).
  return aesCtrDecryptWithIv(fieldKey, data, fieldIV);
}

export interface EciesWrapResult {
  encBytes: Uint8Array;
  kmfBytes: Uint8Array;
}

/** Unwrap a content key from an `$ENC` header + `$KMF` payload. */
export async function eciesUnwrap(
  recipientPrivateKey: Uint8Array,
  encBytes: Uint8Array,
  kmfBytes: Uint8Array,
  context?: string,
): Promise<Uint8Array> {
  const header = ENC.getRootAsENC(new flatbuffers.ByteBuffer(encBytes));
  const kx = header.KEY_EXCHANGE() as number as EciesKeyExchange;
  const ephPub = header.ephemeralPublicKeyArray();
  if (!ephPub || ephPub.length === 0) {
    throw new Error('ecies: ENC missing ephemeral public key');
  }
  const shared = await ecdh(kx, recipientPrivateKey, ephPub);
  const ctx = context ?? header.CONTEXT() ?? DEFAULT_GRANT_CONTEXT;
  const master = await deriveMasterKey(shared, ctx);

  const km = KMF.getRootAsKMF(new flatbuffers.ByteBuffer(kmfBytes));
  const wrapped = km.keyBytesArray();
  if (!wrapped || wrapped.length !== CONTENT_KEY_BYTES) {
    throw new Error(`ecies: KMF KEY_BYTES must be ${CONTENT_KEY_BYTES} bytes`);
  }
  return fieldCipherXOR(master, new Uint8Array(wrapped));
}

/** Wrap a 32-byte content key for a recipient, producing `$ENC` + `$KMF`. */
export async function eciesWrap(
  recipientPublicKey: Uint8Array,
  contentKey: Uint8Array,
  options: {
    keyExchange: EciesKeyExchange;
    context?: string;
    ephemeralPrivateKey?: Uint8Array;
    recipientKeyId?: Uint8Array;
    nonceStart?: Uint8Array;
  },
): Promise<EciesWrapResult> {
  if (contentKey.length !== CONTENT_KEY_BYTES) {
    throw new Error(`ecies: content key must be ${CONTENT_KEY_BYTES} bytes`);
  }
  const context = options.context ?? DEFAULT_GRANT_CONTEXT;

  let ephPriv: Uint8Array;
  let ephPub: Uint8Array;
  switch (options.keyExchange) {
    case EciesKeyExchange.X25519:
      ephPriv = options.ephemeralPrivateKey ?? randomBytes(32);
      ephPub = await x25519PublicKey(ephPriv);
      break;
    case EciesKeyExchange.Secp256k1:
      ephPriv = options.ephemeralPrivateKey ?? randomBytes(32);
      ephPub = await secp256k1PublicKey(ephPriv);
      break;
    default:
      throw new Error(`ecies: wrap unsupported key exchange ${options.keyExchange}`);
  }

  const shared = await ecdh(options.keyExchange, ephPriv, recipientPublicKey);
  const master = await deriveMasterKey(shared, context);
  const wrapped = await fieldCipherXOR(master, new Uint8Array(contentKey));

  const nonceStart = options.nonceStart ?? randomBytes(12);

  const encBuilder = new flatbuffers.Builder(128);
  const ephOff = ENC.createEphemeralPublicKeyVector(encBuilder, ephPub);
  const nonceOff = ENC.createNonceStartVector(encBuilder, nonceStart);
  const ridOff = options.recipientKeyId
    ? ENC.createRecipientKeyIdVector(encBuilder, options.recipientKeyId)
    : 0;
  const ctxOff = encBuilder.createString(context);
  ENC.startENC(encBuilder);
  ENC.addVersion(encBuilder, 1);
  ENC.addKeyExchange(encBuilder, options.keyExchange as number);
  ENC.addSymmetric(encBuilder, 0 /* AES_256_CTR */);
  ENC.addKeyDerivation(encBuilder, 0 /* HKDF_SHA256 */);
  ENC.addEphemeralPublicKey(encBuilder, ephOff);
  ENC.addNonceStart(encBuilder, nonceOff);
  if (ridOff) ENC.addRecipientKeyId(encBuilder, ridOff);
  ENC.addContext(encBuilder, ctxOff);
  ENC.finishENCBuffer(encBuilder, ENC.endENC(encBuilder));

  const kmfBuilder = new flatbuffers.Builder(128);
  const keyOff = KMF.createKeyBytesVector(kmfBuilder, wrapped);
  KMF.startKMF(kmfBuilder);
  KMF.addKeyBytes(kmfBuilder, keyOff);
  KMF.finishKMFBuffer(kmfBuilder, KMF.endKMF(kmfBuilder));

  return {
    encBytes: encBuilder.asUint8Array().slice(),
    kmfBytes: kmfBuilder.asUint8Array().slice(),
  };
}

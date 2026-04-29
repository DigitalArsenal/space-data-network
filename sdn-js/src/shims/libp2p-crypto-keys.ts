import { ed25519 } from '@noble/curves/ed25519';
import { secp256k1 } from '@noble/curves/secp256k1';
import { base58btc } from 'multiformats/bases/base58';
import { identity } from 'multiformats/hashes/identity';
import { equals as equalBytes } from 'uint8arrays/equals';

import { sha256 } from '../crypto/hd-wallet';

const KEY_TYPE_RSA = 0;
const KEY_TYPE_ED25519 = 1;
const KEY_TYPE_SECP256K1 = 2;
const LIBP2P_KEY_CODE = 0x72;

export const keysPBM = {
  KeyType: {
    RSA: 'RSA',
    Ed25519: 'Ed25519',
    Secp256k1: 'Secp256k1',
  },
};

export const supportedKeys = {
  ed25519: {
    generateKeyPair: generateEd25519KeyPair,
  },
  secp256k1: {
    generateKeyPair: generateSecp256k1KeyPair,
  },
};

export class Secp256k1PublicKey {
  readonly type = 'secp256k1';
  private readonly key: Uint8Array;

  constructor(key: Uint8Array) {
    secp256k1.ProjectivePoint.fromHex(key);
    this.key = key.slice();
  }

  verify(data: Uint8Array, sig: Uint8Array): Promise<boolean> {
    return sha256(toBytes(data)).then((digest) =>
      secp256k1.verify(sig, digest, this.key),
    );
  }

  marshal(): Uint8Array {
    return this.key.slice();
  }

  get raw(): Uint8Array {
    return this.marshal();
  }

  get bytes(): Uint8Array {
    return encodeKey(KEY_TYPE_SECP256K1, this.key);
  }

  equals(key: { bytes: Uint8Array }): boolean {
    return equalBytes(this.bytes, key.bytes);
  }

  hash(): Promise<Uint8Array> {
    return sha256(this.bytes);
  }

  toMultihash(): ReturnType<typeof identity.digest> {
    return identity.digest(this.bytes);
  }

  toCID(): { code: number; multihash: ReturnType<typeof identity.digest> } {
    return {
      code: LIBP2P_KEY_CODE,
      multihash: this.toMultihash(),
    };
  }
}

export class Secp256k1PrivateKey {
  readonly type = 'secp256k1';
  private readonly key: Uint8Array;
  private readonly publicKey: Uint8Array;

  constructor(key: Uint8Array, publicKey?: Uint8Array) {
    this.key = key.slice();
    this.publicKey = publicKey?.slice() ?? secp256k1.getPublicKey(this.key, true);
    secp256k1.ProjectivePoint.fromHex(this.publicKey);
  }

  sign(message: Uint8Array): Promise<Uint8Array> {
    return sha256(toBytes(message)).then((digest) =>
      secp256k1.sign(digest, this.key).toDERRawBytes(),
    );
  }

  get public(): Secp256k1PublicKey {
    return new Secp256k1PublicKey(this.publicKey);
  }

  marshal(): Uint8Array {
    return this.key.slice();
  }

  get raw(): Uint8Array {
    return this.marshal();
  }

  get bytes(): Uint8Array {
    return encodeKey(KEY_TYPE_SECP256K1, this.key);
  }

  equals(key: { bytes: Uint8Array }): boolean {
    return equalBytes(this.bytes, key.bytes);
  }

  hash(): Promise<Uint8Array> {
    return sha256(this.bytes);
  }

  async id(): Promise<string> {
    return base58btc.encode(await this.public.hash()).slice(1);
  }

  export(): never {
    throw new Error('Private key export is disabled in the SDN browser bundle');
  }
}

export class Ed25519PublicKey {
  readonly type = 'Ed25519';
  private readonly key: Uint8Array;

  constructor(key: Uint8Array) {
    if (key.length !== 32) {
      throw new Error(`Ed25519 public key must be 32 bytes, got ${key.length}`);
    }
    this.key = key.slice();
  }

  verify(data: Uint8Array, sig: Uint8Array): boolean {
    return ed25519.verify(sig, toBytes(data), this.key);
  }

  marshal(): Uint8Array {
    return this.key.slice();
  }

  get raw(): Uint8Array {
    return this.marshal();
  }

  get bytes(): Uint8Array {
    return encodeKey(KEY_TYPE_ED25519, this.key);
  }

  equals(key: { bytes: Uint8Array }): boolean {
    return equalBytes(this.bytes, key.bytes);
  }

  hash(): ReturnType<typeof identity.digest> {
    return identity.digest(this.bytes);
  }

  toMultihash(): ReturnType<typeof identity.digest> {
    return this.hash();
  }
}

export class Ed25519PrivateKey {
  readonly type = 'Ed25519';
  private readonly key: Uint8Array;
  private readonly publicKey: Uint8Array;

  constructor(key: Uint8Array, publicKey?: Uint8Array) {
    if (key.length !== 32 && key.length !== 64) {
      throw new Error(`Ed25519 private key must be 32 or 64 bytes, got ${key.length}`);
    }
    this.key = key.length === 64 ? key.subarray(0, 32).slice() : key.slice();
    this.publicKey = publicKey?.slice() ?? ed25519.getPublicKey(this.key);
  }

  sign(message: Uint8Array): Uint8Array {
    return ed25519.sign(toBytes(message), this.key);
  }

  get public(): Ed25519PublicKey {
    return new Ed25519PublicKey(this.publicKey);
  }

  marshal(): Uint8Array {
    const out = new Uint8Array(64);
    out.set(this.key);
    out.set(this.publicKey, 32);
    return out;
  }

  get raw(): Uint8Array {
    return this.marshal();
  }

  get bytes(): Uint8Array {
    return encodeKey(KEY_TYPE_ED25519, this.marshal());
  }

  equals(key: { bytes: Uint8Array }): boolean {
    return equalBytes(this.bytes, key.bytes);
  }

  async id(): Promise<string> {
    return base58btc.encode(this.public.hash().bytes).slice(1);
  }

  export(): never {
    throw new Error('Private key export is disabled in the SDN browser bundle');
  }
}

export const RsaPrivateKey = unsupportedKeyClass('RSA private keys');
export const RsaPublicKey = unsupportedKeyClass('RSA public keys');
export const MAX_RSA_KEY_SIZE = 8192;

export function generateKeyPair(type = 'secp256k1'): Promise<Secp256k1PrivateKey | Ed25519PrivateKey> {
  if (type.toLowerCase() === 'ed25519') {
    return Promise.resolve(generateEd25519KeyPair());
  }
  if (type.toLowerCase() === 'secp256k1') {
    return Promise.resolve(generateSecp256k1KeyPair());
  }
  throw new Error(`Unsupported key type '${type}' in the SDN browser bundle`);
}

export function generateKeyPairFromSeed(type: string, seed: Uint8Array): Promise<Ed25519PrivateKey> {
  if (type.toLowerCase() !== 'ed25519') {
    throw new Error(`Unsupported seeded key type '${type}' in the SDN browser bundle`);
  }
  return Promise.resolve(new Ed25519PrivateKey(seed));
}

export function unmarshalPublicKey(buf: Uint8Array): Secp256k1PublicKey | Ed25519PublicKey {
  const decoded = decodeKey(buf);
  if (decoded.type === KEY_TYPE_SECP256K1) {
    return new Secp256k1PublicKey(decoded.data);
  }
  if (decoded.type === KEY_TYPE_ED25519) {
    return new Ed25519PublicKey(decoded.data);
  }
  throw new Error('Unsupported public key type in the SDN browser bundle');
}

export function unmarshalPrivateKey(buf: Uint8Array): Promise<Secp256k1PrivateKey | Ed25519PrivateKey> {
  const decoded = decodeKey(buf);
  if (decoded.type === KEY_TYPE_SECP256K1) {
    return Promise.resolve(new Secp256k1PrivateKey(decoded.data));
  }
  if (decoded.type === KEY_TYPE_ED25519) {
    return Promise.resolve(new Ed25519PrivateKey(decoded.data));
  }
  throw new Error('Unsupported private key type in the SDN browser bundle');
}

export function marshalPublicKey(key: { bytes: Uint8Array }): Uint8Array {
  return key.bytes.slice();
}

export function marshalPrivateKey(key: { bytes: Uint8Array }): Uint8Array {
  return key.bytes.slice();
}

export function publicKeyToProtobuf(key: { bytes: Uint8Array }): Uint8Array {
  return marshalPublicKey(key);
}

export function privateKeyToProtobuf(key: { bytes: Uint8Array }): Uint8Array {
  return marshalPrivateKey(key);
}

export function publicKeyFromProtobuf(buf: Uint8Array): Secp256k1PublicKey | Ed25519PublicKey {
  return unmarshalPublicKey(buf);
}

export function privateKeyFromProtobuf(buf: Uint8Array): Promise<Secp256k1PrivateKey | Ed25519PrivateKey> {
  return unmarshalPrivateKey(buf);
}

export function privateKeyFromRaw(buf: Uint8Array): Secp256k1PrivateKey | Ed25519PrivateKey {
  if (buf.length === 32) {
    return new Secp256k1PrivateKey(buf);
  }
  if (buf.length === 64) {
    return new Ed25519PrivateKey(buf);
  }
  throw new Error(`Unsupported raw private key length ${buf.length}`);
}

export function publicKeyFromMultihash(multihash: { digest: Uint8Array }): Secp256k1PublicKey | Ed25519PublicKey {
  return unmarshalPublicKey(multihash.digest);
}

export function privateKeyToCryptoKeyPair(): never {
  throw new Error('CryptoKey conversion is disabled in the SDN browser bundle');
}

export const keyStretcher = async (): Promise<never> => {
  throw new Error('Key stretching must run through the SDN native crypto boundary');
};

export const generateEphemeralKeyPair = async (): Promise<never> => {
  throw new Error('Ephemeral ECDH must run through the SDN native crypto boundary');
};

export function importKey(): never {
  throw new Error('Encrypted key import is disabled in the SDN browser bundle');
}

function generateSecp256k1KeyPair(): Secp256k1PrivateKey {
  return new Secp256k1PrivateKey(secp256k1.utils.randomPrivateKey());
}

function generateEd25519KeyPair(): Ed25519PrivateKey {
  return new Ed25519PrivateKey(ed25519.utils.randomPrivateKey());
}

function encodeKey(type: number, data: Uint8Array): Uint8Array {
  return concatBytes(
    encodeVarint(8),
    encodeVarint(type),
    encodeVarint(18),
    encodeVarint(data.length),
    data,
  );
}

function decodeKey(buf: Uint8Array): { type: number; data: Uint8Array } {
  let offset = 0;
  let type = KEY_TYPE_RSA;
  let data = new Uint8Array(0);

  while (offset < buf.length) {
    const tag = readVarint(buf, offset);
    offset = tag.offset;
    const field = tag.value >>> 3;
    const wireType = tag.value & 0x07;
    if (field === 1 && wireType === 0) {
      const value = readVarint(buf, offset);
      type = value.value;
      offset = value.offset;
      continue;
    }
    if (field === 2 && wireType === 2) {
      const length = readVarint(buf, offset);
      offset = length.offset;
      data = buf.slice(offset, offset + length.value);
      offset += length.value;
      continue;
    }
    offset = skipField(buf, offset, wireType);
  }

  return { type, data };
}

function readVarint(buf: Uint8Array, startOffset: number): { value: number; offset: number } {
  let value = 0;
  let shift = 0;
  let offset = startOffset;
  while (offset < buf.length) {
    const byte = buf[offset++];
    value |= (byte & 0x7f) << shift;
    if ((byte & 0x80) === 0) {
      return { value, offset };
    }
    shift += 7;
  }
  throw new Error('Truncated protobuf varint');
}

function encodeVarint(value: number): Uint8Array {
  const bytes: number[] = [];
  let current = value >>> 0;
  while (current >= 0x80) {
    bytes.push((current & 0x7f) | 0x80);
    current >>>= 7;
  }
  bytes.push(current);
  return Uint8Array.from(bytes);
}

function skipField(buf: Uint8Array, offset: number, wireType: number): number {
  if (wireType === 0) {
    return readVarint(buf, offset).offset;
  }
  if (wireType === 2) {
    const length = readVarint(buf, offset);
    return length.offset + length.value;
  }
  throw new Error(`Unsupported protobuf wire type ${wireType}`);
}

function concatBytes(...chunks: Uint8Array[]): Uint8Array {
  const out = new Uint8Array(chunks.reduce((sum, chunk) => sum + chunk.length, 0));
  let offset = 0;
  for (const chunk of chunks) {
    out.set(chunk, offset);
    offset += chunk.length;
  }
  return out;
}

function toBytes(value: Uint8Array | { subarray(): Uint8Array }): Uint8Array {
  return value instanceof Uint8Array ? value : value.subarray();
}

function unsupportedKeyClass(name: string) {
  return class {
    constructor() {
      throw new Error(`${name} are disabled in the SDN browser bundle`);
    }
  };
}

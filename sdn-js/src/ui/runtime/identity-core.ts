import * as flatbuffers from 'flatbuffers';
import { ENC } from 'spacedatastandards.org/lib/js/REC/ENC.js';
import { KDF } from 'spacedatastandards.org/lib/js/REC/KDF.js';
import { KMF } from 'spacedatastandards.org/lib/js/REC/KMF.js';
import { KeyExchange } from 'spacedatastandards.org/lib/js/REC/KeyExchange.js';
import { SymmetricAlgo } from 'spacedatastandards.org/lib/js/REC/SymmetricAlgo.js';
import { keyMaterialAlgorithm } from 'spacedatastandards.org/lib/js/REC/keyMaterialAlgorithm.js';
import { keyMaterialEncoding } from 'spacedatastandards.org/lib/js/REC/keyMaterialEncoding.js';
import { keyMaterialRole } from 'spacedatastandards.org/lib/js/REC/keyMaterialRole.js';

import {
  aesGcmDecryptWithIv,
  aesGcmEncryptWithIv,
  pbkdf2Sha256,
  randomBytes,
  sha256,
} from '../../crypto/hd-wallet';

export interface CoreMaterial {
  mnemonic: string;
  account: number;
  peerId: string;
  xpub: string;
  publicEpmHash?: string;
  createdAt?: number;
}

export interface CoreArtifactSection {
  type: 'KMF' | 'ENC' | 'SIG' | string;
  bytes: Uint8Array;
  offset: number;
}

export interface EncodedCoreArtifact {
  bytes: Uint8Array;
  sections: CoreArtifactSection[];
}

export interface DecodedCoreArtifact {
  core: CoreMaterial;
  sections: CoreArtifactSection[];
}

const MAGIC = new TextEncoder().encode('SDNCORE1');
const CONTEXT = 'sdn-core-export';
const ROOT_TYPE = '$KMF';
const DIGEST_METADATA_ALGORITHM = 'sha256-digest-v1';
const PBKDF2_ITERATIONS = 600_000;
const ZERO_SCHEMA_HASH = new Uint8Array(32);
const encoder = new TextEncoder();
const decoder = new TextDecoder();

export async function encodeEncryptedCoreArtifact(input: {
  passphrase: string;
  core: CoreMaterial;
}): Promise<EncodedCoreArtifact> {
  if (!input.passphrase) {
    throw new Error('A passphrase is required for encrypted Core artifact export');
  }

  const salt = randomBytes(32);
  const nonce = randomBytes(12);
  const key = await derivePassphraseKey(input.passphrase, salt);
  const plaintext = encoder.encode(canonicalJson(input.core));
  const ciphertext = await aesGcmEncryptWithIv(key, plaintext, nonce, encoder.encode(CONTEXT));

  const kmfBytes = encodeKmf(ciphertext);
  const encBytes = encodeEnc({ nonce, salt, timestamp: Date.now() });
  const sigBytes = await encodeDigestMetadata(kmfBytes, encBytes);
  return frameSections([
    { type: 'KMF', bytes: kmfBytes },
    { type: 'ENC', bytes: encBytes },
    { type: 'SIG', bytes: sigBytes },
  ]);
}

export async function decodeEncryptedCoreArtifact(input: {
  passphrase: string;
  bytes: Uint8Array;
}): Promise<DecodedCoreArtifact> {
  const sections = parseFramedSections(input.bytes);
  const kmfSection = getRequiredSection(sections, 'KMF');
  const encSection = getRequiredSection(sections, 'ENC');
  const sigSection = getRequiredSection(sections, 'SIG');

  await verifyDigestMetadata(kmfSection.bytes, encSection.bytes, sigSection.bytes);

  const kmf = KMF.getRootAsKMF(new flatbuffers.ByteBuffer(kmfSection.bytes));
  const enc = ENC.getRootAsENC(new flatbuffers.ByteBuffer(encSection.bytes));
  if (enc.CONTEXT() !== CONTEXT || enc.ROOT_TYPE() !== ROOT_TYPE) {
    throw new Error('Invalid encrypted Core artifact ENC metadata');
  }

  const salt = enc.ephemeralPublicKeyArray();
  const nonce = enc.nonceStartArray();
  const ciphertext = kmf.keyBytesArray();
  if (!salt?.length || nonce.length !== 12 || !ciphertext?.length) {
    throw new Error('Invalid encrypted Core artifact payload');
  }

  const key = await derivePassphraseKey(input.passphrase, salt);
  let plaintext: Uint8Array;
  try {
    plaintext = await aesGcmDecryptWithIv(key, ciphertext, nonce, encoder.encode(CONTEXT));
  } catch (error) {
    throw new Error(`Unable to decrypt encrypted Core artifact: ${error instanceof Error ? error.message : 'decrypt failed'}`);
  }

  const core = parseCoreMaterial(plaintext);
  return { core, sections };
}

function encodeKmf(ciphertext: Uint8Array): Uint8Array {
  const builder = new flatbuffers.Builder(1024);
  const keyId = builder.createString(CONTEXT);
  const keyBytes = KMF.createKeyBytesVector(builder, ciphertext);
  const offset = KMF.createKMF(
    builder,
    keyId,
    keyMaterialRole.DecryptKey,
    keyMaterialAlgorithm.Aes256Gcm,
    keyMaterialEncoding.RawBytes,
    keyBytes,
    1,
    0n,
  );
  KMF.finishKMFBuffer(builder, offset);
  return builder.asUint8Array();
}

function encodeEnc(input: { nonce: Uint8Array; salt: Uint8Array; timestamp: number }): Uint8Array {
  const builder = new flatbuffers.Builder(1024);
  const salt = ENC.createEphemeralPublicKeyVector(builder, input.salt);
  const nonce = ENC.createNonceStartVector(builder, input.nonce);
  const context = builder.createString(CONTEXT);
  const rootType = builder.createString(ROOT_TYPE);
  const schemaHash = ENC.createSchemaHashVector(builder, ZERO_SCHEMA_HASH);
  const offset = ENC.createENC(
    builder,
    1,
    KeyExchange.X25519,
    SymmetricAlgo.AES_256_CTR,
    KDF.HKDF_SHA256,
    salt,
    nonce,
    0,
    context,
    schemaHash,
    rootType,
    BigInt(input.timestamp),
  );
  ENC.finishENCBuffer(builder, offset);
  return builder.asUint8Array();
}

async function encodeDigestMetadata(kmfBytes: Uint8Array, encBytes: Uint8Array): Promise<Uint8Array> {
  const digest = bytesToHex(await sha256(concatBytes(kmfBytes, encBytes)));
  return encoder.encode(canonicalJson({ algorithm: DIGEST_METADATA_ALGORITHM, digest }));
}

async function verifyDigestMetadata(kmfBytes: Uint8Array, encBytes: Uint8Array, sigBytes: Uint8Array): Promise<void> {
  let metadata: unknown;
  try {
    metadata = JSON.parse(decoder.decode(sigBytes));
  } catch {
    throw new Error('Invalid encrypted Core artifact digest metadata');
  }
  if (!isRecord(metadata) || metadata.algorithm !== DIGEST_METADATA_ALGORITHM || typeof metadata.digest !== 'string') {
    throw new Error('Invalid encrypted Core artifact digest metadata');
  }
  const digest = bytesToHex(await sha256(concatBytes(kmfBytes, encBytes)));
  if (metadata.digest !== digest) {
    throw new Error('Encrypted Core artifact digest metadata mismatch');
  }
}

function frameSections(sections: Array<{ type: string; bytes: Uint8Array }>): EncodedCoreArtifact {
  const framed: Uint8Array[] = [MAGIC];
  const artifactSections: CoreArtifactSection[] = [];
  let offset = MAGIC.length;
  for (const section of sections) {
    const typeBytes = encoder.encode(section.type);
    const typeLength = uint32Le(typeBytes.length);
    const bytesLength = uint32Le(section.bytes.length);
    const bytesOffset = offset + typeLength.length + typeBytes.length + bytesLength.length;
    artifactSections.push({ type: section.type, bytes: section.bytes, offset: bytesOffset });
    framed.push(typeLength, typeBytes, bytesLength, section.bytes);
    offset = bytesOffset + section.bytes.length;
  }
  return { bytes: concatBytes(...framed), sections: artifactSections };
}

function parseFramedSections(bytes: Uint8Array): CoreArtifactSection[] {
  if (!startsWith(bytes, MAGIC)) {
    throw new Error('Expected encrypted Core artifact with SDNCORE1 framing');
  }
  const sections: CoreArtifactSection[] = [];
  let offset = MAGIC.length;
  while (offset < bytes.length) {
    const typeLength = readUint32Le(bytes, offset);
    offset += 4;
    const type = decoder.decode(bytes.slice(offset, offset + typeLength));
    offset += typeLength;
    const bytesLength = readUint32Le(bytes, offset);
    offset += 4;
    const bytesOffset = offset;
    const sectionBytes = bytes.slice(offset, offset + bytesLength);
    if (sectionBytes.length !== bytesLength) {
      throw new Error('Truncated encrypted Core artifact section');
    }
    sections.push({ type, bytes: sectionBytes, offset: bytesOffset });
    offset += bytesLength;
  }
  return sections;
}

function getRequiredSection(sections: CoreArtifactSection[], type: 'KMF' | 'ENC' | 'SIG'): CoreArtifactSection {
  const section = sections.find((candidate) => candidate.type === type);
  if (!section) {
    throw new Error(`Missing encrypted Core artifact ${type} section`);
  }
  return section;
}

async function derivePassphraseKey(passphrase: string, salt: Uint8Array): Promise<Uint8Array> {
  return pbkdf2Sha256(encoder.encode(passphrase), salt, PBKDF2_ITERATIONS, 32);
}

function parseCoreMaterial(bytes: Uint8Array): CoreMaterial {
  let parsed: unknown;
  try {
    parsed = JSON.parse(decoder.decode(bytes));
  } catch {
    throw new Error('Encrypted Core artifact decrypted to invalid Core JSON');
  }
  if (
    !isRecord(parsed) ||
    typeof parsed.mnemonic !== 'string' ||
    typeof parsed.account !== 'number' ||
    typeof parsed.peerId !== 'string' ||
    typeof parsed.xpub !== 'string'
  ) {
    throw new Error('Encrypted Core artifact decrypted to incomplete Core material');
  }
  return {
    mnemonic: parsed.mnemonic,
    account: parsed.account,
    peerId: parsed.peerId,
    xpub: parsed.xpub,
    publicEpmHash: typeof parsed.publicEpmHash === 'string' ? parsed.publicEpmHash : undefined,
    createdAt: typeof parsed.createdAt === 'number' ? parsed.createdAt : undefined,
  };
}

function canonicalJson(value: unknown): string {
  if (Array.isArray(value)) {
    return `[${value.map((item) => canonicalJson(item)).join(',')}]`;
  }
  if (isRecord(value)) {
    return `{${Object.keys(value)
      .filter((key) => value[key] !== undefined)
      .sort()
      .map((key) => `${JSON.stringify(key)}:${canonicalJson(value[key])}`)
      .join(',')}}`;
  }
  return JSON.stringify(value);
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

function uint32Le(value: number): Uint8Array {
  const out = new Uint8Array(4);
  new DataView(out.buffer).setUint32(0, value, true);
  return out;
}

function readUint32Le(bytes: Uint8Array, offset: number): number {
  if (offset + 4 > bytes.length) {
    throw new Error('Truncated encrypted Core artifact section header');
  }
  return new DataView(bytes.buffer, bytes.byteOffset + offset, 4).getUint32(0, true);
}

function startsWith(bytes: Uint8Array, prefix: Uint8Array): boolean {
  return bytes.length >= prefix.length && prefix.every((byte, index) => bytes[index] === byte);
}

function bytesToHex(bytes: Uint8Array): string {
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('');
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

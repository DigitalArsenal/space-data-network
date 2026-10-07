/**
 * Module artifacts as a node signs them (owner 2026-10-07: the dashboard's
 * FlatSQL engine ships like every other module): the wasm, then an SDS $REC
 * record collection holding one $MBL bundle listing, then an 8-byte footer,
 * the collection's length (u32 little-endian) and "$REC". The listing's
 * signature entry carries the node's Ed25519 signature over the statement
 * `SDN-MODULE-PUBLICATION-V1 || 0x00 || sha256(module)`.
 *
 * This is the node-signed path of the module SDK's verifyModuleArtifact
 * (space-data-module-sdk src/bundle/signing.js) and of the node's own reader
 * (sdn-server internal/modulert), in the same order and with the same refusal
 * codes, with no WebCrypto and no wasm crypto runtime, so the engine worker
 * can check its engine before compiling it.
 */
import { ed25519 } from '@noble/curves/ed25519.js';
import { sha256 } from '@noble/hashes/sha2.js';
import * as flatbuffers from 'flatbuffers';
import { MBL } from 'spacedatastandards.org/lib/js/MBL/MBL.js';
import { ModuleBundleEntryRole } from 'spacedatastandards.org/lib/js/MBL/ModuleBundleEntryRole.js';
import { ModulePayloadEncoding } from 'spacedatastandards.org/lib/js/MBL/ModulePayloadEncoding.js';

/** The statement domain a node's module signature is bound to. */
export const MODULE_PUBLICATION_DOMAIN = 'SDN-MODULE-PUBLICATION-V1';

const TRAILER_MAGIC = '$REC';
const FOOTER_LENGTH = 8;

/** A refused artifact; `code` is the module SDK's refusal code. */
export class ModuleArtifactError extends Error {
  constructor(readonly code: string, message: string) {
    super(message);
    this.name = 'ModuleArtifactError';
  }
}

export interface VerifiedModule {
  /** The module bytes, trailer removed: what a wasm engine compiles. */
  module: Uint8Array;
  /** The signer's public key, lowercase hex. */
  publicKeyHex: string;
  keyId: string | null;
}

interface SignaturePayload {
  algorithm?: unknown;
  keyId?: unknown;
  publicKeyHex?: unknown;
  signatureHex?: unknown;
  signedHashHex?: unknown;
  statementDomain?: unknown;
}

const text = (value: unknown): string => (typeof value === 'string' ? value.trim() : '');

function hexBytes(value: string): Uint8Array | null {
  if (!/^(?:[0-9a-f]{2})*$/i.test(value)) return null;
  const out = new Uint8Array(value.length / 2);
  for (let i = 0; i < out.length; i += 1) out[i] = parseInt(value.slice(i * 2, i * 2 + 2), 16);
  return out;
}

const toHex = (bytes: Uint8Array): string => Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('');

/** The module and its record collection, or null when the bytes carry no trailer. */
function splitTrailer(bytes: Uint8Array): { module: Uint8Array; collection: Uint8Array } | null {
  if (bytes.length < FOOTER_LENGTH) return null;
  if (new TextDecoder().decode(bytes.subarray(bytes.length - 4)) !== TRAILER_MAGIC) return null;
  const length = new DataView(bytes.buffer, bytes.byteOffset + bytes.length - FOOTER_LENGTH, 4).getUint32(0, true);
  const start = bytes.length - FOOTER_LENGTH - length;
  if (start < 0) return null;
  return { module: bytes.subarray(0, start), collection: bytes.subarray(start, bytes.length - FOOTER_LENGTH) };
}

/**
 * The signature entry's payload in the collection's MBL record, or null.
 * Records are told apart by Record.standard, never the union ordinal, as
 * every SDN reader does. REC's root: version (field 0), records (field 1);
 * a Record: value_type (field 0), value (field 1), standard (field 2).
 */
function signaturePayload(collection: Uint8Array): SignaturePayload | null {
  const bb = new flatbuffers.ByteBuffer(collection);
  if (collection.length < 8 || !bb.__has_identifier(TRAILER_MAGIC)) {
    throw new ModuleArtifactError('malformed_trailer', 'The module trailer is not a $REC record collection.');
  }
  const root = bb.readUint32(bb.position()) + bb.position();
  const records = bb.__offset(root, 6);
  if (!records) return null;
  const count = bb.__vector_len(root + records);
  const start = bb.__vector(root + records);
  for (let i = 0; i < count; i += 1) {
    const record = bb.__indirect(start + i * 4);
    const standardAt = bb.__offset(record, 8);
    const standard = standardAt ? String(bb.__string(record + standardAt)).trim().toUpperCase() : '';
    const valueAt = bb.__offset(record, 6);
    if (standard !== 'MBL' || !valueAt) continue;
    const listing = new MBL().__init(bb.__indirect(record + valueAt), bb);
    for (let j = 0; j < listing.entriesLength(); j += 1) {
      const entry = listing.entries(j);
      if (!entry) continue;
      const id = String(entry.entry_id() ?? '').trim().toLowerCase();
      const section = String(entry.section_name() ?? '').trim().toLowerCase();
      if (entry.role() !== ModuleBundleEntryRole.SIGNATURE && id !== 'signature' && section !== 'sds.signature') continue;
      if (entry.payload_encoding() !== ModulePayloadEncoding.JSON_UTF8) {
        throw new ModuleArtifactError('invalid_signature', 'The module signature entry is not JSON.');
      }
      const payload = entry.payloadArray();
      return JSON.parse(new TextDecoder().decode(payload ?? new Uint8Array())) as SignaturePayload;
    }
    return null;
  }
  return null;
}

/**
 * Verify a node-signed module artifact against the keys trusted to sign it,
 * and return the module to compile. Throws ModuleArtifactError on anything
 * else: unsigned, signed for another purpose, by another key, or altered.
 */
export function verifyNodeSignedModule(bytes: Uint8Array, trustedKeys: readonly string[]): VerifiedModule {
  const split = splitTrailer(bytes);
  const payload = split ? signaturePayload(split.collection) : null;
  if (!split || !payload) throw new ModuleArtifactError('missing_signature', 'The module carries no signature.');
  if (text(payload.algorithm).toLowerCase() !== 'ed25519') {
    throw new ModuleArtifactError('unsupported_algorithm', `Unsupported module signature algorithm: ${text(payload.algorithm)}`);
  }
  const signature = hexBytes(text(payload.signatureHex));
  if (!signature || signature.length !== 64) throw new ModuleArtifactError('invalid_signature', 'The module signature must be 64 bytes.');
  const publicKeyHex = text(payload.publicKeyHex).toLowerCase();
  const publicKey = hexBytes(publicKeyHex);
  if (!publicKey || publicKey.length !== 32) throw new ModuleArtifactError('invalid_public_key', 'The module signer key must be 32 bytes.');
  const trusted = trustedKeys.map((key) => key.trim().toLowerCase());
  if (!trusted.includes(publicKeyHex)) throw new ModuleArtifactError('untrusted_signer', 'The module is signed by a key this node does not publish with.');
  if (text(payload.statementDomain) !== MODULE_PUBLICATION_DOMAIN) {
    throw new ModuleArtifactError('wrong_statement_domain', 'The module signature was not made for a module publication.');
  }
  const contentHash = sha256(split.module);
  if (text(payload.signedHashHex).toLowerCase() !== toHex(contentHash)) {
    throw new ModuleArtifactError('hash_mismatch', 'The module does not match the hash its signature names.');
  }
  const label = new TextEncoder().encode(MODULE_PUBLICATION_DOMAIN);
  const statement = new Uint8Array(label.length + 1 + contentHash.length);
  statement.set(label, 0);
  statement.set(contentHash, label.length + 1);
  if (!ed25519.verify(signature, statement, publicKey)) {
    throw new ModuleArtifactError('invalid_signature', 'The module signature does not verify.');
  }
  return { module: split.module, publicKeyHex, keyId: text(payload.keyId) || null };
}

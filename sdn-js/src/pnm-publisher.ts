/**
 * PNM signing + publish for the browser/Helia node (WS6.6).
 *
 * Until now the Helia side only subscribed to and decoded PNMs
 * (ui/runtime/pnm-flatbuffer.ts). This module adds the producer half,
 * byte-compatible with the Go node's dataset-publication PNM envelope
 * (sdn-server internal/storage/manifest.go BuildDatasetPublicationPNM /
 * internal/channels/pnm_verifier.go):
 *
 *  - size-prefixed $PNM FlatBuffer with MULTIFORMAT_ADDRESS ("/ipfs/<cid>"),
 *    PUBLISH_TIMESTAMP (RFC3339), CID, FILE_NAME, FILE_ID,
 *    SIGNATURE_TYPE "Ed25519", and SIGNATURE = hex(ed25519 over
 *    "SDN-DPM-PNM\0" + FILE_ID + "\0" + CID), signed with the wallet's
 *    Ed25519 key (native/WASM crypto boundary — no WebCrypto).
 *  - published on the gossipsub topic /spacedatanetwork/sds/PNM.fbs — the
 *    topic the Go node's EPM service consumes.
 */

import * as flatbuffers from 'flatbuffers';
import { PNM } from 'spacedatastandards.org/lib/js/PNM/PNM.js';
import { sign, verify } from './crypto/hd-wallet';

export const PNM_SCHEMA = 'PNM.fbs';
export const PNM_TOPIC = `/spacedatanetwork/sds/${PNM_SCHEMA}`;

const textEncoder = new TextEncoder();

/** Signature payload — must match Go datasetPublicationPNMSignaturePayload. */
export function pnmSignaturePayload(cid: string, fileId: string): Uint8Array {
  const prefix = textEncoder.encode('SDN-DPM-PNM\x00');
  const fileIdBytes = textEncoder.encode(fileId);
  const cidBytes = textEncoder.encode(cid);
  const payload = new Uint8Array(prefix.length + fileIdBytes.length + 1 + cidBytes.length);
  payload.set(prefix, 0);
  payload.set(fileIdBytes, prefix.length);
  payload[prefix.length + fileIdBytes.length] = 0;
  payload.set(cidBytes, prefix.length + fileIdBytes.length + 1);
  return payload;
}

function bytesToHex(bytes: Uint8Array): string {
  return Array.from(bytes)
    .map((b) => b.toString(16).padStart(2, '0'))
    .join('');
}

function hexToBytes(hexValue: string): Uint8Array {
  const clean = hexValue.trim();
  const bytes = new Uint8Array(clean.length / 2);
  for (let i = 0; i < bytes.length; i++) {
    bytes[i] = parseInt(clean.slice(i * 2, i * 2 + 2), 16);
  }
  return bytes;
}

export interface BuildSignedPnmOptions {
  /** Content id the PNM announces (e.g. a DPM manifest or record CID). */
  cid: string;
  /** SDS file identifier of the announced content (e.g. "$DPM", "EPHEMERIS"). */
  fileId: string;
  fileName?: string;
  publishedAt?: Date | string;
  multiformatAddress?: string;
  /** Ed25519 private key (wallet-derived) used to sign the announcement. */
  signingKey: Uint8Array;
}

/** Build + sign a size-prefixed $PNM envelope (Go-verifier compatible). */
export async function buildSignedPnm(options: BuildSignedPnmOptions): Promise<Uint8Array> {
  const cid = options.cid?.trim();
  const fileId = options.fileId?.trim();
  if (!cid) throw new Error('buildSignedPnm requires a cid');
  if (!fileId) throw new Error('buildSignedPnm requires a fileId');
  if (!(options.signingKey instanceof Uint8Array) || options.signingKey.length === 0) {
    throw new Error('buildSignedPnm requires an Ed25519 signing key');
  }
  const publishedAt =
    options.publishedAt instanceof Date
      ? options.publishedAt.toISOString()
      : (options.publishedAt ?? new Date().toISOString());

  const signature = await sign(options.signingKey, pnmSignaturePayload(cid, fileId));
  if (signature.length !== 64) {
    throw new Error(`ed25519 signature length = ${signature.length}, want 64`);
  }

  const builder = new flatbuffers.Builder(256);
  const addr = builder.createString(options.multiformatAddress ?? `/ipfs/${cid}`);
  const timestamp = builder.createString(publishedAt);
  const cidOffset = builder.createString(cid);
  const fileName = builder.createString(options.fileName ?? '');
  const fileIdOffset = builder.createString(fileId);
  const signatureOffset = builder.createString(bytesToHex(signature));
  const signatureType = builder.createString('Ed25519');

  PNM.startPNM(builder);
  PNM.addMultiformatAddress(builder, addr);
  PNM.addPublishTimestamp(builder, timestamp);
  PNM.addCid(builder, cidOffset);
  PNM.addFileName(builder, fileName);
  PNM.addFileId(builder, fileIdOffset);
  PNM.addSignature(builder, signatureOffset);
  PNM.addSignatureType(builder, signatureType);
  const root = PNM.endPNM(builder);
  PNM.finishSizePrefixedPNMBuffer(builder, root);
  return builder.asUint8Array().slice();
}

export interface SignedPnmEvidence {
  cid: string;
  fileId: string;
  signatureType: string;
  signature: Uint8Array;
  multiformatAddress?: string;
  publishTimestamp?: string;
}

/** Decode + verify a signed PNM envelope (mirror of the Go verifier). */
export async function verifySignedPnm(
  pnmBytes: Uint8Array,
  providerPublicKey?: Uint8Array,
): Promise<SignedPnmEvidence> {
  if (!pnmBytes || pnmBytes.length === 0) {
    throw new Error('PNM bytes are required');
  }
  const pnm = PNM.getSizePrefixedRootAsPNM(new flatbuffers.ByteBuffer(pnmBytes));
  const cid = String(pnm.CID() ?? '').trim();
  if (!cid) throw new Error('PNM missing CID');
  const fileId = String(pnm.FILE_ID() ?? '').trim();
  if (!fileId) throw new Error('PNM missing FILE_ID');
  const signatureType = String(pnm.SIGNATURE_TYPE() ?? '').trim();
  if (signatureType !== 'Ed25519') {
    throw new Error(`PNM SIGNATURE_TYPE = "${signatureType}", want Ed25519`);
  }
  const signatureHex = String(pnm.SIGNATURE() ?? '').trim();
  if (!signatureHex) throw new Error('PNM missing signature');
  const signature = hexToBytes(signatureHex);
  if (signature.length !== 64) {
    throw new Error(`PNM signature length = ${signature.length}, want 64`);
  }
  if (providerPublicKey) {
    const valid = await verify(
      providerPublicKey,
      pnmSignaturePayload(cid, fileId),
      signature,
    );
    if (!valid) throw new Error('invalid PNM signature');
  }
  return {
    cid,
    fileId,
    signatureType,
    signature,
    multiformatAddress: String(pnm.MULTIFORMAT_ADDRESS() ?? '').trim() || undefined,
    publishTimestamp: String(pnm.PUBLISH_TIMESTAMP() ?? '').trim() || undefined,
  };
}

/** Anything that can publish raw bytes to a gossipsub topic. */
export interface RawTopicPublisher {
  publish(topic: string, data: Uint8Array): Promise<unknown>;
}

/** Publish a signed PNM on the canonical PNM topic. */
export async function publishSignedPnm(
  publisher: RawTopicPublisher,
  pnmBytes: Uint8Array,
  topic = PNM_TOPIC,
): Promise<string> {
  await publisher.publish(topic, pnmBytes);
  return topic;
}

/** Build, sign, and publish in one step. */
export async function signAndPublishPnm(
  publisher: RawTopicPublisher,
  options: BuildSignedPnmOptions,
): Promise<{ topic: string; pnmBytes: Uint8Array }> {
  const pnmBytes = await buildSignedPnm(options);
  const topic = await publishSignedPnm(publisher, pnmBytes);
  return { topic, pnmBytes };
}

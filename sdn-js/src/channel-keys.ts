/**
 * Group-chat channel key management (browser mirror of the Go
 * internal/channelkeys package), built on the unified ECIES one-to-many
 * primitive (eciesWrapForRecipients): a channel owns one symmetric content key,
 * membership is a set of member encryption keys, and the content key is wrapped
 * one-to-many to every member as an SDS $ENC/$KMF envelope. Membership changes
 * rekey the channel as required for forward secrecy.
 *
 * This is the key-management layer for WS9 encrypted pub/sub channel chat; the
 * message layer (AES-256-GCM under the content key) is built on top of it.
 */

import * as flatbuffers from 'flatbuffers';
import { ENC } from 'spacedatastandards.org/lib/js/ENC/ENC.js';
import {
  eciesWrapForRecipients,
  eciesUnwrap,
  EciesKeyExchange,
  type EciesRecipient,
} from './ecies';
import {
  initHDWallet,
  randomBytes,
  sign as ed25519Sign,
  verify as ed25519Verify,
  ed25519PublicKey,
  aesGcmEncryptWithIv,
  aesGcmDecryptWithIv,
} from './crypto/hd-wallet';

const CONTENT_KEY_BYTES = 32;
const GCM_NONCE_BYTES = 12;
const SIG_BYTES = 64;
// The deployed SDK raw-byte encoding of AES-256-GCM in ENC.SYMMETRIC (the
// published SymmetricAlgo enum only names AES_256_CTR=0).
const SYMMETRIC_ALGO_AES_256_GCM = 1;
const MESSAGE_SIG_PREFIX = new TextEncoder().encode('SDN-CHN-MSG\x00');

/** One channel participant's encryption identity. */
export interface ChannelMember {
  /** Stable member id (peer id / handle); stamped as RECIPIENT_KEY_ID. */
  id: string;
  /** X25519 (32 bytes) or secp256k1 compressed (33 bytes), matching keyExchange. */
  publicKey: Uint8Array;
  keyExchange: EciesKeyExchange;
}

/** One member's wrapped copy of the channel content key. */
export interface ChannelMemberEnvelope {
  memberId: string;
  epoch: number;
  encBytes: Uint8Array;
  kmfBytes: Uint8Array;
}

export interface ChannelKeysOptions {
  /** ECIES context (domain separator); defaults to a channel-scoped context. */
  context?: string;
  /** Pin the initial content key (deterministic tests); defaults to random. */
  contentKey?: Uint8Array;
}

function defaultContext(id: string): string {
  return `space-data-network/channel/${id}/v1`;
}

/**
 * A keyed group-chat channel: an id, a rotating symmetric content key with an
 * epoch counter, a member set, and the ECIES context used when wrapping.
 */
export class ChannelKeys {
  readonly id: string;
  readonly context: string;
  private _epoch = 1;
  private contentKey: Uint8Array;
  private readonly members = new Map<string, ChannelMember>();

  private constructor(id: string, context: string, contentKey: Uint8Array) {
    this.id = id;
    this.context = context;
    this.contentKey = contentKey;
  }

  /**
   * Create a channel with a freshly generated content key at epoch 1 and no
   * members. Add members with addMember, then wrapForMembers to mint envelopes.
   */
  static async create(id: string, options?: ChannelKeysOptions): Promise<ChannelKeys> {
    if (!id) throw new Error('channelkeys: channel id required');
    await initHDWallet();
    const context = options?.context ?? defaultContext(id);
    let contentKey: Uint8Array;
    if (options?.contentKey) {
      if (options.contentKey.length !== CONTENT_KEY_BYTES) {
        throw new Error(`channelkeys: content key must be ${CONTENT_KEY_BYTES} bytes`);
      }
      contentKey = options.contentKey.slice();
    } else {
      contentKey = randomBytes(CONTENT_KEY_BYTES);
    }
    return new ChannelKeys(id, context, contentKey);
  }

  get epoch(): number {
    return this._epoch;
  }

  /** A copy of the current content key for message encryption. */
  getContentKey(): Uint8Array {
    return this.contentKey.slice();
  }

  /** The current member set, sorted by id. */
  getMembers(): ChannelMember[] {
    return [...this.members.values()].sort((a, b) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0));
  }

  /**
   * Add a member. Adding does NOT rotate the content key: the new member shares
   * the current key and can read current/future messages. Re-run wrapForMembers
   * to mint the new member's envelope.
   */
  addMember(m: ChannelMember): void {
    if (!m.id) throw new Error('channelkeys: member id required');
    if (!m.publicKey || m.publicKey.length === 0) {
      throw new Error('channelkeys: member public key required');
    }
    this.members.set(m.id, m);
  }

  /**
   * Remove a member and rekey the channel (fresh content key, epoch bumped) so
   * the removed member cannot read messages published afterward (forward
   * secrecy). Throws if the member is absent.
   */
  removeMember(id: string): void {
    if (!this.members.has(id)) {
      throw new Error(`channelkeys: member ${JSON.stringify(id)} not in channel`);
    }
    this.members.delete(id);
    this.rekey();
  }

  /** Rotate the content key and bump the epoch. */
  rekey(): void {
    this.contentKey = randomBytes(CONTENT_KEY_BYTES);
    this._epoch += 1;
  }

  /**
   * Wrap the current content key one-to-many to every current member, returning
   * one $ENC/$KMF envelope per member (stamped with the member id as
   * RECIPIENT_KEY_ID and the current epoch). Throws if there are no members.
   */
  async wrapForMembers(): Promise<ChannelMemberEnvelope[]> {
    const members = this.getMembers();
    if (members.length === 0) {
      throw new Error('channelkeys: channel has no members to wrap for');
    }
    const enc = new TextEncoder();
    const recipients: EciesRecipient[] = members.map((m) => ({
      publicKey: m.publicKey,
      keyExchange: m.keyExchange,
      keyId: enc.encode(m.id),
    }));
    const envs = await eciesWrapForRecipients(this.contentKey, recipients, this.context);
    return envs.map((e, i) => ({
      memberId: members[i].id,
      epoch: this._epoch,
      encBytes: e.encBytes,
      kmfBytes: e.kmfBytes,
    }));
  }

  /**
   * Recover the channel content key from a member's envelope using the member's
   * private key. The context must match the channel's.
   */
  static async unwrapForMember(
    memberPrivateKey: Uint8Array,
    encBytes: Uint8Array,
    kmfBytes: Uint8Array,
    context: string,
  ): Promise<Uint8Array> {
    return eciesUnwrap(memberPrivateKey, encBytes, kmfBytes, context);
  }
}

// ---------------------------------------------------------------------------
// Channel chat message envelope (WS9.2) — the encrypted pub/sub wire format,
// byte-identical with Go internal/channelkeys/message.go:
//
//   u32LE(len(encBytes)) || encBytes || signature(64) || ciphertext||tag
//
// encBytes is a standalone SDS $ENC header: SYMMETRIC=1 (AES-256-GCM, deployed
// SDK convention), EPHEMERAL_PUBLIC_KEY = the SENDER's ed25519 public key,
// NONCE_START = 12-byte GCM nonce, RECIPIENT_KEY_ID = be64(content-key epoch),
// CONTEXT = channel context, TIMESTAMP = sender unix-ms. The header is bound
// as GCM AAD, and the ed25519 signature covers
// "SDN-CHN-MSG\0" || encBytes || ciphertext.
// ---------------------------------------------------------------------------

/** A decrypted + signature-verified channel chat message. */
export interface ChannelMessage {
  plaintext: Uint8Array;
  senderPublicKey: Uint8Array;
  epoch: number;
  timestampMs: number;
}

export interface EncryptChannelMessageOptions {
  /** Pin the 12-byte GCM nonce (deterministic vectors); defaults to random. */
  nonce?: Uint8Array;
  /** Sender clock in unix milliseconds; defaults to 0 (caller-stamped). */
  timestampMs?: number;
}

function concatBytes(...parts: Uint8Array[]): Uint8Array {
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

function buildMessageENC(
  senderPub: Uint8Array,
  nonce: Uint8Array,
  epoch: number,
  context: string,
  timestampMs: number,
): Uint8Array {
  const b = new flatbuffers.Builder(160);
  const pubOff = ENC.createEphemeralPublicKeyVector(b, senderPub);
  const nonceOff = ENC.createNonceStartVector(b, nonce);
  const epochId = new Uint8Array(8);
  new DataView(epochId.buffer).setBigUint64(0, BigInt(epoch), false); // big-endian
  const ridOff = ENC.createRecipientKeyIdVector(b, epochId);
  const ctxOff = b.createString(context);
  ENC.startENC(b);
  ENC.addVersion(b, 1);
  // Raw byte 1 = AES-256-GCM (deployed convention; not in the published enum).
  ENC.addSymmetric(b, SYMMETRIC_ALGO_AES_256_GCM as Parameters<typeof ENC.addSymmetric>[1]);
  ENC.addEphemeralPublicKey(b, pubOff);
  ENC.addNonceStart(b, nonceOff);
  ENC.addRecipientKeyId(b, ridOff);
  ENC.addContext(b, ctxOff);
  ENC.addTimestamp(b, BigInt(timestampMs));
  ENC.finishENCBuffer(b, ENC.endENC(b));
  return b.asUint8Array().slice();
}

/**
 * Seal a chat message for the channel: AES-256-GCM under the channel content
 * key with the $ENC header as AAD, signed by the sender's ed25519 key.
 * context/epoch must be the channel's current context/epoch.
 */
export async function encryptChannelMessage(
  contentKey: Uint8Array,
  senderPrivateKey: Uint8Array,
  context: string,
  epoch: number,
  plaintext: Uint8Array,
  options?: EncryptChannelMessageOptions,
): Promise<Uint8Array> {
  if (contentKey.length !== CONTENT_KEY_BYTES) {
    throw new Error(`channelkeys: content key must be ${CONTENT_KEY_BYTES} bytes`);
  }
  const nonce = options?.nonce ?? randomBytes(GCM_NONCE_BYTES);
  if (nonce.length !== GCM_NONCE_BYTES) {
    throw new Error(`channelkeys: nonce must be ${GCM_NONCE_BYTES} bytes`);
  }
  const senderPub = await ed25519PublicKey(senderPrivateKey);
  const encBytes = buildMessageENC(senderPub, nonce, epoch, context, options?.timestampMs ?? 0);

  const ciphertext = await aesGcmEncryptWithIv(
    contentKey,
    plaintext,
    nonce,
    encBytes as Uint8Array<ArrayBuffer>,
  );
  const sig = await ed25519Sign(senderPrivateKey, concatBytes(MESSAGE_SIG_PREFIX, encBytes, ciphertext));

  const lenLE = new Uint8Array(4);
  new DataView(lenLE.buffer).setUint32(0, encBytes.length, true);
  return concatBytes(lenLE, encBytes, sig, ciphertext);
}

/**
 * Open a channel chat envelope with the channel content key, verifying the
 * sender signature and the AAD-bound header. expectedContext must match the
 * channel context ('' accepts the header's context).
 */
export async function decryptChannelMessage(
  contentKey: Uint8Array,
  envelope: Uint8Array,
  expectedContext: string,
): Promise<ChannelMessage> {
  if (contentKey.length !== CONTENT_KEY_BYTES) {
    throw new Error(`channelkeys: content key must be ${CONTENT_KEY_BYTES} bytes`);
  }
  if (envelope.length < 4) throw new Error('channelkeys: envelope too short');
  const encLen = new DataView(envelope.buffer, envelope.byteOffset).getUint32(0, true);
  if (encLen < 4 || envelope.length < 4 + encLen + SIG_BYTES) {
    throw new Error('channelkeys: envelope truncated');
  }
  const encBytes = envelope.slice(4, 4 + encLen);
  const sig = envelope.slice(4 + encLen, 4 + encLen + SIG_BYTES);
  const ciphertext = envelope.slice(4 + encLen + SIG_BYTES);

  const header = ENC.getRootAsENC(new flatbuffers.ByteBuffer(encBytes));
  if ((header.SYMMETRIC() as number) !== SYMMETRIC_ALGO_AES_256_GCM) {
    throw new Error(`channelkeys: unsupported symmetric algo ${header.SYMMETRIC()}`);
  }
  const senderPub = header.ephemeralPublicKeyArray();
  if (!senderPub || senderPub.length !== 32) {
    throw new Error('channelkeys: header missing sender public key');
  }
  const nonce = header.nonceStartArray();
  if (!nonce || nonce.length !== GCM_NONCE_BYTES) {
    throw new Error('channelkeys: header missing GCM nonce');
  }
  const ctx = header.CONTEXT() ?? '';
  if (expectedContext !== '' && ctx !== expectedContext) {
    throw new Error(`channelkeys: context mismatch: ${JSON.stringify(ctx)}`);
  }
  const keyId = header.recipientKeyIdArray();
  let epoch = 0;
  if (keyId && keyId.length === 8) {
    epoch = Number(new DataView(keyId.buffer, keyId.byteOffset).getBigUint64(0, false));
  }

  const ok = await ed25519Verify(
    new Uint8Array(senderPub),
    concatBytes(MESSAGE_SIG_PREFIX, encBytes, ciphertext),
    sig,
  );
  if (!ok) throw new Error('channelkeys: sender signature invalid');

  const plaintext = await aesGcmDecryptWithIv(
    contentKey,
    ciphertext,
    new Uint8Array(nonce),
    encBytes as Uint8Array<ArrayBuffer>,
  );
  return {
    plaintext,
    senderPublicKey: new Uint8Array(senderPub),
    epoch,
    timestampMs: Number(header.TIMESTAMP()),
  };
}

/**
 * Gossipsub topic for a channel's encrypted chat, mirroring Go
 * channelkeys.ChatTopic and the CHANNEL_TOPIC_PREFIX convention.
 */
export function channelChatTopic(channelId: string): string {
  return `/spacedatanetwork/channels/${channelId}/chat`;
}

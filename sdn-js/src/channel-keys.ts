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

import {
  eciesWrapForRecipients,
  eciesUnwrap,
  EciesKeyExchange,
  type EciesRecipient,
} from './ecies';
import { initHDWallet, randomBytes } from './crypto/hd-wallet';

const CONTENT_KEY_BYTES = 32;

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

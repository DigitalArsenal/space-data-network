/**
 * PeerID derivation, pinned.
 *
 * Every SDN browser identity is a secp256k1 key from the HD wallet, and the
 * PeerID derived from it is what the Go node's admission policy sees, what the
 * trust graph records, what relay reservations are keyed to, and what every
 * published EPM/PNM signature binds. If it moves by one byte, none of that
 * matches any more — and nothing throws.
 *
 * libp2p 2 removed `Libp2pInit.peerId` and @libp2p/peer-id 5 removed
 * `peerIdFromKeys(publicKey, privateKey)`, so node.ts had to stop deriving the
 * PeerID from the key PAIR and start passing the private key alone. This file
 * is the check that the change was a no-op:
 *
 *  - the expected PeerID is computed here from first principles (compressed
 *    secp256k1 public key → libp2p key protobuf → identity multihash →
 *    base58btc) and ALSO written out as a literal, so a derivation change shows
 *    up as a diff and not just as two computations agreeing on something new;
 *  - the private-key route the node now takes and the public-key route it used
 *    to take must produce that same string;
 *  - the HD-wallet WASM helper the discovery layer uses (`derivePeerIdFromPublicKey`)
 *    must agree with both;
 *  - and a REAL libp2p node built from the key must report it.
 *
 * No mocks: this runs the installed libp2p.
 */

import { describe, expect, it } from 'vitest';
import { secp256k1 } from '@noble/curves/secp256k1.js';
import { privateKeyFromProtobuf, publicKeyFromProtobuf } from '@libp2p/crypto/keys';
import { peerIdFromPrivateKey, peerIdFromPublicKey } from '@libp2p/peer-id';
import { base58btc } from 'multiformats/bases/base58';
import { createLibp2p } from 'libp2p';

import { marshalSecp256k1PrivateKey, marshalSecp256k1PublicKey } from './node';
import { derivePeerIdFromPublicKey, initHDWallet } from './crypto/hd-wallet';

/** A fixed, non-secret test key: bytes 0x01..0x20. */
const IDENTITY_PRIVATE_KEY = Uint8Array.from({ length: 32 }, (_, index) => index + 1);
const IDENTITY_PUBLIC_KEY = secp256k1.getPublicKey(IDENTITY_PRIVATE_KEY, true);

/**
 * The PeerID this key must always produce. Changing this literal changes every
 * browser's identity on the network.
 */
const EXPECTED_PEER_ID = '16Uiu2HAm4Ms862Gnqafssgvik4JJ1LuqWMcKNipq4nm2UaoLRbeP';

describe('secp256k1 PeerID derivation', () => {
  it('matches a PeerID derived from the multiformats primitives directly', () => {
    // A secp256k1 PeerID is base58btc(identity-multihash(key protobuf)), with
    // the multibase prefix stripped. Nothing libp2p-specific about it.
    const publicKeyProtobuf = marshalSecp256k1PublicKey(IDENTITY_PUBLIC_KEY);
    const identityMultihash = Uint8Array.from([
      0x00,
      publicKeyProtobuf.length,
      ...publicKeyProtobuf,
    ]);
    expect(base58btc.encode(identityMultihash).slice(1)).toBe(EXPECTED_PEER_ID);
  });

  it('derives the same PeerID from the private key alone, which is what the node now passes', () => {
    const privateKey = privateKeyFromProtobuf(
      marshalSecp256k1PrivateKey(IDENTITY_PRIVATE_KEY),
    );
    expect(peerIdFromPrivateKey(privateKey).toString()).toBe(EXPECTED_PEER_ID);
  });

  it('derives the same PeerID from the public key, which is what the node used to pass', () => {
    const publicKey = publicKeyFromProtobuf(marshalSecp256k1PublicKey(IDENTITY_PUBLIC_KEY));
    expect(peerIdFromPublicKey(publicKey).toString()).toBe(EXPECTED_PEER_ID);
  });

  it('agrees with the HD-wallet helper the discovery layer derives provider PeerIDs with', async () => {
    expect(await initHDWallet()).toBe(true);
    expect(await derivePeerIdFromPublicKey(IDENTITY_PUBLIC_KEY)).toBe(EXPECTED_PEER_ID);
  });

  it('is the PeerID a real libp2p node reports for that key', async () => {
    const node = await createLibp2p({
      privateKey: privateKeyFromProtobuf(marshalSecp256k1PrivateKey(IDENTITY_PRIVATE_KEY)),
      addresses: { listen: [] },
      transports: [],
      start: false,
    });
    try {
      expect(node.peerId.toString()).toBe(EXPECTED_PEER_ID);
    } finally {
      await node.stop();
    }
  }, 30_000);
});

// WS9.3 in-browser E2E — page side.
//
// Creates THREE real browser libp2p nodes in this page: alice + bob (channel
// members) and mallory (connected non-member). All dial the Node.js websocket
// hub and join the channel chat gossipsub topic. Alice mints the channel
// (WS9.1 ChannelKeys), wraps the content key one-to-many to the members, bob
// recovers it from HIS $ENC/$KMF envelope, alice publishes an encrypted
// message (WS9.2 envelope) over REAL gossipsub, bob decrypts + attributes the
// sender, and mallory — receiving the very same wire bytes — cannot decrypt
// and never sees plaintext on the wire.
//
// Results are logged as `E2E_RESULT {json}` for the chrome-devtools driver.
import { createLibp2p } from 'libp2p';
import { webSockets } from '@libp2p/websockets';
import { all as wsFilters } from '@libp2p/websockets/filters';
import { identify } from '@libp2p/identify';
import { gossipsub } from '@chainsafe/libp2p-gossipsub';
import { noise } from '@chainsafe/libp2p-noise';
import { yamux } from '@chainsafe/libp2p-yamux';
import { multiaddr } from '@multiformats/multiaddr';

import {
  ChannelKeys,
  encryptChannelMessage,
  decryptChannelMessage,
  channelChatTopic,
} from '../../src/channel-keys.ts';
import { EciesKeyExchange } from '../../src/ecies.ts';
import {
  initHDWallet,
  x25519PublicKey,
  ed25519PublicKey,
} from '../../src/crypto/hd-wallet.ts';

const CHANNEL_ID = 'ws9-e2e-room';
const PLAINTEXT = 'ws9 encrypted browser chat';

const toHex = (b) => Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('');

async function browserNode() {
  return createLibp2p({
    transports: [webSockets({ filter: wsFilters })],
    connectionEncryption: [noise()],
    streamMuxers: [yamux()],
    // The browser default gater denies loopback; this E2E dials 127.0.0.1.
    connectionGater: { denyDialMultiaddr: () => false },
    services: {
      pubsub: gossipsub({ allowPublishToZeroTopicPeers: true, emitSelf: false }),
      identify: identify(),
    },
  });
}

function waitForMessage(node, topic, timeoutMs = 20000) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error('timeout waiting for gossipsub message')), timeoutMs);
    node.services.pubsub.addEventListener('message', (evt) => {
      if (evt.detail.topic !== topic) return;
      clearTimeout(timer);
      resolve(evt.detail.data);
    });
  });
}

async function waitForTopicPeers(node, topic, min, timeoutMs = 20000) {
  const start = Date.now();
  for (;;) {
    if (node.services.pubsub.getSubscribers(topic).length >= min) return;
    if (Date.now() - start > timeoutMs) {
      throw new Error(`timeout: topic peers ${node.services.pubsub.getSubscribers(topic).length} < ${min}`);
    }
    await new Promise((r) => setTimeout(r, 100));
  }
}

window.runChannelChatE2E = async function runChannelChatE2E(hubAddrStr) {
  const result = {
    ok: false,
    memberDecrypted: false,
    senderAttributed: false,
    outsiderBlocked: false,
    wireLeaksPlaintext: null,
    epoch: 0,
    error: null,
  };
  try {
    await initHDWallet();
    const hubAddr = multiaddr(hubAddrStr);
    const topic = channelChatTopic(CHANNEL_ID);

    const [alice, bob, mallory] = await Promise.all([browserNode(), browserNode(), browserNode()]);
    await Promise.all([alice.dial(hubAddr), bob.dial(hubAddr), mallory.dial(hubAddr)]);
    for (const n of [alice, bob, mallory]) n.services.pubsub.subscribe(topic);
    // Everyone must see the hub (and via it, the mesh) on the topic.
    await Promise.all([
      waitForTopicPeers(alice, topic, 1),
      waitForTopicPeers(bob, topic, 1),
      waitForTopicPeers(mallory, topic, 1),
    ]);

    // WS9.1: alice owns the channel; bob is a member with an X25519 key.
    const bobPriv = crypto.getRandomValues(new Uint8Array(32));
    const bobPub = await x25519PublicKey(bobPriv);
    const channel = await ChannelKeys.create(CHANNEL_ID);
    channel.addMember({ id: 'bob', publicKey: bobPub, keyExchange: EciesKeyExchange.X25519 });
    const envelopes = await channel.wrapForMembers();
    const bobEnv = envelopes.find((e) => e.memberId === 'bob');
    const bobKey = await ChannelKeys.unwrapForMember(bobPriv, bobEnv.encBytes, bobEnv.kmfBytes, channel.context);
    result.epoch = channel.epoch;

    // WS9.2: alice encrypts + signs, publishes over REAL gossipsub.
    const senderSeed = crypto.getRandomValues(new Uint8Array(32));
    const senderPub = await ed25519PublicKey(senderSeed);
    const envelope = await encryptChannelMessage(
      channel.getContentKey(),
      senderSeed,
      channel.context,
      channel.epoch,
      new TextEncoder().encode(PLAINTEXT),
      { timestampMs: Date.now() },
    );

    const bobRecv = waitForMessage(bob, topic);
    const malloryRecv = waitForMessage(mallory, topic);
    await alice.services.pubsub.publish(topic, envelope);

    // Member: decrypts with the key from HIS wrapped envelope.
    const bobBytes = await bobRecv;
    const msg = await decryptChannelMessage(bobKey, bobBytes, channel.context);
    result.memberDecrypted = new TextDecoder().decode(msg.plaintext) === PLAINTEXT;
    result.senderAttributed = toHex(msg.senderPublicKey) === toHex(senderPub);

    // Non-member: same wire bytes, no key → blocked; wire carries no plaintext.
    const malloryBytes = await malloryRecv;
    result.wireLeaksPlaintext = toHex(malloryBytes).includes(toHex(new TextEncoder().encode(PLAINTEXT)));
    try {
      await decryptChannelMessage(new Uint8Array(32), malloryBytes, channel.context);
      result.outsiderBlocked = false;
    } catch {
      result.outsiderBlocked = true;
    }

    result.ok = result.memberDecrypted && result.senderAttributed && result.outsiderBlocked && result.wireLeaksPlaintext === false;
  } catch (err) {
    result.error = String(err?.stack ?? err);
  }
  console.log(`E2E_RESULT ${JSON.stringify(result)}`);
  return result;
};

console.log('E2E_PAGE_READY');

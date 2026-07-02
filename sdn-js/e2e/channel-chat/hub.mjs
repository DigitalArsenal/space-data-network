// WS9.3 in-browser E2E — gossipsub hub + out-of-page NON-MEMBER observer.
//
// Browser libp2p nodes cannot listen, so this Node process is the websocket
// rendezvous: every browser node dials it, and because it subscribes to the
// channel chat topic it forwards gossipsub messages between the browser
// members. It holds NO channel key, so it doubles as a non-member assert:
// every message it observes must be ciphertext (no plaintext substring).
// (The full wrong-key decrypt-failure assert runs in-page via mallory.)
//
// stdout protocol (read by the test driver):
//   HUB_READY <multiaddr>
//   HUB_OBSERVED {"bytes":<n>,"leaksPlaintext":<bool>}
import { createLibp2p } from 'libp2p';
import { tcp } from '@libp2p/tcp';
import { webSockets } from '@libp2p/websockets';
import { all as wsFilters } from '@libp2p/websockets/filters';
import { identify } from '@libp2p/identify';
import { gossipsub } from '@chainsafe/libp2p-gossipsub';
import { noise } from '@chainsafe/libp2p-noise';
import { yamux } from '@chainsafe/libp2p-yamux';

const CHANNEL_ID = 'ws9-e2e-room';
// Mirrors channelChatTopic() in src/channel-keys.ts / Go channelkeys.ChatTopic.
const TOPIC = `/spacedatanetwork/channels/${CHANNEL_ID}/chat`;
// The plaintext the members exchange — the hub must NEVER see these bytes.
const PLAINTEXT_UTF8 = 'ws9 encrypted browser chat';

const node = await createLibp2p({
  addresses: { listen: ['/ip4/127.0.0.1/tcp/0/ws'] },
  transports: [tcp(), webSockets({ filter: wsFilters })],
  connectionEncryption: [noise()],
  streamMuxers: [yamux()],
  services: {
    pubsub: gossipsub({ allowPublishToZeroTopicPeers: true, emitSelf: false }),
    identify: identify(),
  },
});

const pubsub = node.services.pubsub;
pubsub.subscribe(TOPIC);
pubsub.addEventListener('message', (evt) => {
  if (evt.detail.topic !== TOPIC) return;
  const bytes = evt.detail.data;
  const hex = Buffer.from(bytes).toString('hex');
  const plaintextHex = Buffer.from(PLAINTEXT_UTF8, 'utf8').toString('hex');
  const leaksPlaintext = hex.includes(plaintextHex);
  console.log(`HUB_OBSERVED ${JSON.stringify({ bytes: bytes.length, leaksPlaintext })}`);
});

const wsAddr = node.getMultiaddrs().find((a) => a.toString().includes('/ws'));
console.log(`HUB_READY ${wsAddr.toString()}`);

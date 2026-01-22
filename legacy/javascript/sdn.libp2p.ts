

import { createLibp2p, } from 'libp2p'
import { webSockets } from '@libp2p/websockets'
import { yamux } from '@chainsafe/libp2p-yamux'
import { noise } from '@chainsafe/libp2p-noise'
import { tls } from '@libp2p/tls';
import { gossipsub } from '@chainsafe/libp2p-gossipsub'
import { pubsubPeerDiscovery } from '@libp2p/pubsub-peer-discovery'
import { identify } from '@libp2p/identify'
import { circuitRelayTransport } from '@libp2p/circuit-relay-v2'
import { bootstrap } from '@libp2p/bootstrap'
import { webTransport } from '@libp2p/webtransport'
import { peerIdFromString } from '@libp2p/peer-id'
import { multiaddr } from 'multiaddr'
import { mplex } from '@libp2p/mplex'
import * as filters from '@libp2p/websockets/filters'
import { pipe } from 'it-pipe'
import { Uint8ArrayList } from 'uint8arraylist'

const tokyo2WS = "/ip4/209.182.234.97/tcp/8080/ws/p2p/16Uiu2HAkxKtJncDGfgtFpx4mNqtrzbBBrCZ8iaKKyKuEqEHuEz5J";

const node = await createLibp2p({
    transports: [
        webTransport(),
        webSockets({ filter: filters.all }),
        circuitRelayTransport({ // allows the current node to make and accept relayed connections
            discoverRelays: 100, // how many network relays to find
            reservationConcurrency: 100 // how many relays to attempt to reserve slots on at once
        })
    ], // Any libp2p transport(s) can be used
    streamMuxers: [
        yamux()
    ],
    connectionEncryption: [
        noise()
    ],
    peerDiscovery: [
        bootstrap({
            list: ['/dnsaddr/bootstrap.libp2p.io/p2p/QmNnooDu7bfjPFoTZYxMNLWUQJyrVwtbZg5gBMjTezGAJN',
                '/dnsaddr/bootstrap.libp2p.io/p2p/QmbLHAnMoJPWSCR5Zhtx6BHJX9KiKNN6tpvbUcqanj75Nb',
                '/dnsaddr/bootstrap.libp2p.io/p2p/QmZa1sAxajnQjVM8WjWXoMbmPd7NsWhfKsPkErzpm9wGkp',
                '/dnsaddr/bootstrap.libp2p.io/p2p/QmQCU2EcMqAqQPR2i9bChDtGNJchTbq5TbXJJ16u19uLTa',
                '/dnsaddr/bootstrap.libp2p.io/p2p/QmcZf59bWwK5XFi76CZX8cbJ4BhTzzA3gU1ZjYZcYW3dwt',
                '/ip4/209.182.234.97/tcp/8080/ws/p2p/16Uiu2HAkxKtJncDGfgtFpx4mNqtrzbBBrCZ8iaKKyKuEqEHuEz5J',
            ]
        }),
        pubsubPeerDiscovery({ topics: ["bb9e1781dfcd6a3cfbac4681901e96b757bc5d60925bb2837ae43982ab260e28"] })
    ],
    services: {
        pubsub: gossipsub({
            emitSelf: false,

        }),
        identify: identify()
    }
})

await node.start();

const aa = multiaddr(tokyo2WS);

console.log(aa);

try {
    await node.dial(aa);
    console.log('Connected to the server!');
} catch (err) {
    console.error('Failed to connect to the server:', err);
}
/**
 * TODO 
 * - Create the peer discovery bridge from DHT to WebSockets pubsub
 * - Use that to find the relays
 */


setInterval(async () => {
    const target = "16Uiu2HAmR3UHmGprRrFaQrmAfLNSb6pcFHyPSJoBqj5QV1hb9NeK";
    const relayAddress = multiaddr(`/ip4/209.182.234.97/tcp/8080/ws/p2p/16Uiu2HAkxKtJncDGfgtFpx4mNqtrzbBBrCZ8iaKKyKuEqEHuEz5J/p2p-circuit/p2p/${target}`);

    try {
        const stream = await node.dialProtocol(multiaddr(relayAddress), "/space-data-network/id-exchange/1.0.0", {
            runOnTransientConnection: true
        });
        console.log('Connected to peer through relay!');

        if (stream) {
            // Use the sink function to send data
            const encoder = new TextEncoder();
            const data = encoder.encode('ping');

            // Using it-pipe to manage stream sinks and sources
            await pipe(
                [data], // Data must be an iterable, array works here
                stream.sink
            );

            // Receive data from the source
            const results = await pipe(
                stream.source, // Source from the yamux stream
                async function collect(source) {
                    const list = new Uint8ArrayList();
                    const decoder = new TextDecoder();

                    for await (const chunk of source) {
                        list.append(chunk);  // Append each chunk to Uint8ArrayList
                    }

                    // Use subarray to get a contiguous Uint8Array
                    const completeArray = list.subarray();
                    console.log(completeArray);
                    // Decode the Uint8Array to a string using TextDecoder
                    const text = decoder.decode(completeArray);

                    return text;
                }
            );

            console.log('Received response:', results);

            // Properly close the stream after the interaction
            await stream.close();
            console.log('Stream closed successfully');
        } else {
            console.log('Stream is undefined.');
        }
    } catch (err) {
        console.log('Failed to connect to the peer through the relay:', err);
    }
}, 2000);
/**
 *  
 * 
 */

/*const peerId = peerIdFromString('16Uiu2HAmR3UHmGprRrFaQrmAfLNSb6pcFHyPSJoBqj5QV1hb9NeK')
const peerInfo = await node.peerRouting.findPeer(peerId).catch(e => { console.log(e) })

console.info(peerInfo)
*/
node.addEventListener('peer:discovery', async (evt) => {
    console.log('found peer: ', evt.detail.id.string);
    const relayAddress = multiaddr(`/ip4/209.182.234.97/tcp/8080/ws/p2p/16Uiu2HAkxKtJncDGfgtFpx4mNqtrzbBBrCZ8iaKKyKuEqEHuEz5J/p2p-circuit/p2p/${evt.detail.id.string}`);

    try {
        /*  const { stream } = await node.dialProtocol(multiaddr(relayAddress), "/ipfs/ping/1.0.0",
              {
                  runOnTransientConnection: true
              });
  
          console.log('Connected to peer through relay!');*/
    } catch (err) {
        console.log('Failed to connect to the peer through the relay:', err);
    }
})

globalThis.node = node;
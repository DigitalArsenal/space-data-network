/**
 * Browser/IPFS compatibility probe based on main_old/javascript/sdn.libp2p.ts.
 *
 * It boots an SDN browser node with IPFS bootstrap peers, dials a relay,
 * then runs the legacy id-exchange protocol through that relay circuit.
 */

import { SDNNode } from '../src/node';

const relayAddr = '/ip4/209.182.234.97/tcp/8080/ws/p2p/16Uiu2HAkxKtJncDGfgtFpx4mNqtrzbBBrCZ8iaKKyKuEqEHuEz5J';
const targetPeerId = '16Uiu2HAmR3UHmGprRrFaQrmAfLNSb6pcFHyPSJoBqj5QV1hb9NeK';

async function main(): Promise<void> {
  const node = await SDNNode.create({
    edgeRelays: [relayAddr],
    includeIPFSBootstrap: true,
  });

  try {
    await node.dial(relayAddr);
    console.log('Connected to relay:', relayAddr);

    const response = await node.idExchangeThroughRelay(relayAddr, targetPeerId, 'ping');
    console.log('id-exchange response:', response);
  } finally {
    await node.stop();
  }
}

main().catch((err) => {
  console.error('IPFS relay probe failed:', err);
});

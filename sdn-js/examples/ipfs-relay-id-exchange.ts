/**
 * Browser/IPFS compatibility probe based on main_old/javascript/sdn.libp2p.ts.
 *
 * It boots an SDN node from runtime-supplied provider identity metadata,
 * then dials one of the supplied relay candidates.
 *
 * Required environment:
 * - SDN_PROVIDER_PUBLIC_KEY: provider compressed secp256k1 public key (hex)
 * - SDN_PROVIDER_RELAYS: comma-separated relay candidate multiaddrs
 *
 * Optional:
 * - SDN_TARGET_PEER_ID: peer to probe via legacy id-exchange. Defaults to the
 *   provider peer id derived from SDN_PROVIDER_PUBLIC_KEY.
 */

import { deriveProviderPeerId } from '../src/discovery';
import { SDNNode } from '../src/node';

const PROVIDER_PUBLIC_KEY = process.env.SDN_PROVIDER_PUBLIC_KEY ?? '';
const RELAY_CANDIDATES = (process.env.SDN_PROVIDER_RELAYS ?? '')
  .split(',')
  .map((value) => value.trim())
  .filter(Boolean);
const TARGET_PEER_ID = process.env.SDN_TARGET_PEER_ID ?? '';

async function main(): Promise<void> {
  const { providerPeerId, relayAddr } = await resolveRelayCandidate();
  console.log('Using relay address:', relayAddr);

  const node = await SDNNode.create({
    edgeRelays: [relayAddr],
    includeIPFSBootstrap: false,
    enableStorage: false,
  });

  try {
    await node.dial(relayAddr);
    console.log('Connected to relay:', relayAddr);

    const probePeerId = TARGET_PEER_ID || providerPeerId;
    if (probePeerId) {
      const response = await node.idExchangeThroughRelay(relayAddr, probePeerId, 'ping');
      console.log('id-exchange response:', response);
    }
  } finally {
    await node.stop();
  }
}

async function resolveRelayCandidate(): Promise<{ providerPeerId: string; relayAddr: string }> {
  if (!PROVIDER_PUBLIC_KEY) {
    throw new Error('SDN_PROVIDER_PUBLIC_KEY is required');
  }
  if (RELAY_CANDIDATES.length === 0) {
    throw new Error('SDN_PROVIDER_RELAYS must include at least one relay candidate');
  }

  const providerPeerId = await deriveProviderPeerId(hexToBytes(PROVIDER_PUBLIC_KEY));
  return {
    providerPeerId,
    relayAddr: RELAY_CANDIDATES[0].includes('/p2p/')
      ? RELAY_CANDIDATES[0]
      : `${RELAY_CANDIDATES[0]}/p2p/${providerPeerId}`,
  };
}

function hexToBytes(hex: string): Uint8Array {
  const normalized = hex.trim().toLowerCase();
  if (normalized.length % 2 !== 0) {
    throw new Error('SDN_PROVIDER_PUBLIC_KEY must be valid hex');
  }
  const bytes = new Uint8Array(normalized.length / 2);
  for (let index = 0; index < bytes.length; index += 1) {
    bytes[index] = Number.parseInt(normalized.slice(index * 2, index * 2 + 2), 16);
  }
  return bytes;
}

main().catch((err) => {
  console.error('IPFS relay probe failed:', err);
});

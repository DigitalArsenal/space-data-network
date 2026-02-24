/**
 * Browser/IPFS compatibility probe based on main_old/javascript/sdn.libp2p.ts.
 *
 * It boots an SDN node, resolves the live relay deployment from
 * https://spaceaware.io/api/node/info, then dials that relay.
 *
 * If SDN_TARGET_PEER_ID is provided, it will also run legacy id-exchange
 * through the relay circuit.
 */

import { SDNNode } from '../src/node';

const NODE_INFO_URL = process.env.SDN_SPACEAWARE_NODE_INFO_URL ?? 'https://spaceaware.io/api/node/info';
const TARGET_PEER_ID = process.env.SDN_TARGET_PEER_ID;

interface NodeInfoResponse {
  peer_id?: unknown;
  listen_addresses?: unknown;
}

async function main(): Promise<void> {
  const relayAddr = await resolveSpaceawareRelayAddr();
  console.log('Using relay address:', relayAddr);

  const node = await SDNNode.create({
    edgeRelays: [relayAddr],
    includeIPFSBootstrap: false,
    enableStorage: false,
  });

  try {
    await node.dial(relayAddr);
    console.log('Connected to relay:', relayAddr);

    if (TARGET_PEER_ID) {
      const response = await node.idExchangeThroughRelay(relayAddr, TARGET_PEER_ID, 'ping');
      console.log('id-exchange response:', response);
    } else {
      console.log('Set SDN_TARGET_PEER_ID to run id-exchange through the relay circuit.');
    }
  } finally {
    await node.stop();
  }
}

async function resolveSpaceawareRelayAddr(): Promise<string> {
  const response = await fetch(NODE_INFO_URL);
  if (!response.ok) {
    throw new Error(`Failed to fetch node info (${response.status}) from ${NODE_INFO_URL}`);
  }

  const body = (await response.json()) as NodeInfoResponse;
  const peerId = asNonEmptyString(body.peer_id, 'peer_id');
  const listenAddrs = asStringArray(body.listen_addresses).filter((addr) => addr.includes('/ws'));

  // Prefer DNS over TLS for browser/firewall-friendly dial when available.
  const dnsWss = `/dns4/spaceaware.io/tcp/443/wss/p2p/${peerId}`;
  if (listenAddrs.length === 0) {
    return dnsWss;
  }

  return listenAddrs[0].includes('/p2p/') ? listenAddrs[0] : `${listenAddrs[0]}/p2p/${peerId}`;
}

function asNonEmptyString(value: unknown, fieldName: string): string {
  if (typeof value !== 'string' || value.trim().length === 0) {
    throw new Error(`Node info missing "${fieldName}"`);
  }
  return value;
}

function asStringArray(value: unknown): string[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.filter((entry): entry is string => typeof entry === 'string' && entry.trim().length > 0);
}

main().catch((err) => {
  console.error('IPFS relay probe failed:', err);
});

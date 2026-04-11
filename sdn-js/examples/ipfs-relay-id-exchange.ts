/**
 * Browser/IPFS relay probe for the module-delivery requester path.
 *
 * It boots an SDN node from runtime-supplied provider identity metadata,
 * then dials one of the supplied relay candidates that can carry
 * `/space-data-network/module-delivery/1.0.0` and performs a real grant
 * request against the provider. An authorization failure still proves
 * the relay path and module-delivery protocol are reachable.
 *
 * Required environment:
 * - SDN_PROVIDER_PUBLIC_KEY: provider compressed secp256k1 public key (hex)
 * - SDN_PROVIDER_RELAYS: comma-separated relay candidate multiaddrs
 * - SDN_MODULE_ID: requested module id for the probe
 *
 * Optional environment:
 * - SDN_MODULE_VERSION: requested module version
 * - SDN_MODULE_VARIANT: requested module variant
 */

import { deriveIdentity, randomBytes } from '../src/crypto';
import { deriveProviderPeerId } from '../src/discovery';
import {
  MODULE_DELIVERY_PROTOCOL_ID,
  ModuleDeliveryProtocolError,
  requestModuleGrant,
} from '../src/module-delivery';
import { SDNNode } from '../src/node';

const PROVIDER_PUBLIC_KEY = process.env.SDN_PROVIDER_PUBLIC_KEY ?? '';
const RELAY_CANDIDATES = (process.env.SDN_PROVIDER_RELAYS ?? '')
  .split(',')
  .map((value) => value.trim())
  .filter(Boolean);
const MODULE_ID = (process.env.SDN_MODULE_ID ?? '').trim();
const MODULE_VERSION = trimOptional(process.env.SDN_MODULE_VERSION);
const MODULE_VARIANT = trimOptional(process.env.SDN_MODULE_VARIANT);

async function main(): Promise<void> {
  const { providerPeerId, relayAddr } = await resolveRelayCandidate();
  console.log('Using relay address:', relayAddr);

  const node = await SDNNode.create({
    edgeRelays: [relayAddr],
    includeIPFSBootstrap: false,
    enableStorage: false,
  });

  try {
    const requesterIdentity = await deriveIdentity(randomBytes(64));
    const grant = await requestModuleGrant(node, {
      serverDescriptor: {
        publicKey: PROVIDER_PUBLIC_KEY,
        relayAddresses: [relayAddr],
      },
      requesterIdentity,
      moduleId: MODULE_ID,
      moduleVersion: MODULE_VERSION,
      moduleVariant: MODULE_VARIANT,
    });

    console.log('Module delivery exchange succeeded.');
    console.log('Provider peer ID:', providerPeerId);
    console.log('Module delivery protocol:', MODULE_DELIVERY_PROTOCOL_ID);
    console.log('Bundle CID:', grant.grant.bundleDescriptor.cid);
  } catch (err) {
    if (err instanceof ModuleDeliveryProtocolError) {
      console.log('Module delivery exchange reached the provider but was rejected.');
      console.log('Provider peer ID:', providerPeerId);
      console.log('Module delivery protocol:', MODULE_DELIVERY_PROTOCOL_ID);
      console.log('Provider response code:', err.code);
      console.log('Provider response message:', err.message);
      return;
    }
    throw err;
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
  if (!MODULE_ID) {
    throw new Error('SDN_MODULE_ID is required');
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

function trimOptional(value: string | undefined): string | undefined {
  const normalized = String(value || '').trim();
  return normalized || undefined;
}

main().catch((err) => {
  console.error('Module-delivery relay probe failed:', err);
});

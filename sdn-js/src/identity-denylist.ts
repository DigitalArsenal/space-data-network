/**
 * The network's trust anchors and the SEC-001 identity denylist, read from the
 * ONE committed file deployment/identity/anchors.json (compiled into this
 * package as identity-anchors.generated.ts by deployment/identity/sync-anchors.mjs).
 *
 * SEC-001: the BIP-39 seed behind the previous sdn.spaceaware.io identity was
 * committed to the public repository. Every public value derived from it is
 * denylisted: sdn-js never dials it as a relay or bootstrap peer, never accepts
 * a provider descriptor or EPM that names it, and never accepts a licensing
 * grant stamped with its grant-verifier key — whatever a runtime override, a
 * WASM relay list, a delegated router or a remote peer says.
 */
import { IDENTITY_ANCHORS } from './identity-anchors.generated';

const PLACEHOLDER_PREFIX = 'PLACEHOLDER-';

export class DeniedIdentityError extends Error {
  readonly code = 'SEC001_DENIED_IDENTITY';

  constructor(what: string) {
    super(`${what} is a denylisted identity (SEC-001: its seed was published)`);
    this.name = 'DeniedIdentityError';
  }
}

const deny = IDENTITY_ANCHORS.denylist;
const DENIED: ReadonlySet<string> = new Set<string>([
  ...deny.peerIds,
  ...deny.secp256k1PublicKeys.map((v) => v.toLowerCase()),
  ...deny.ed25519PublicKeys.map((v) => v.toLowerCase()),
  ...deny.xpubs,
  ...deny.updateRootKeyIds.map((v) => v.toLowerCase()),
  ...deny.discoveryCids,
  ...deny.chainAddresses,
]);

export function isPlaceholderAnchor(value: string | undefined | null): boolean {
  return typeof value === 'string' && value.startsWith(PLACEHOLDER_PREFIX);
}

/** True for any public value (PeerID, hex key, xpub, key id, CID, address) of a denylisted identity. */
export function isDeniedIdentityValue(value: string | undefined | null): boolean {
  if (typeof value !== 'string') return false;
  const trimmed = value.trim();
  if (!trimmed) return false;
  return DENIED.has(trimmed) || DENIED.has(trimmed.toLowerCase());
}

function toHex(bytes: Uint8Array): string {
  let out = '';
  for (const byte of bytes) out += byte.toString(16).padStart(2, '0');
  return out;
}

/** True for raw public key bytes (secp256k1 compressed or Ed25519/X25519) of a denylisted identity. */
export function isDeniedPublicKey(key: Uint8Array | undefined | null): boolean {
  if (!key || key.length === 0) return false;
  return DENIED.has(toHex(key));
}

export function assertNotDeniedValue(value: string | undefined | null, what: string): void {
  if (isDeniedIdentityValue(value)) throw new DeniedIdentityError(what);
}

export function assertNotDeniedPublicKey(key: Uint8Array | undefined | null, what: string): void {
  if (isDeniedPublicKey(key)) throw new DeniedIdentityError(what);
}

/** The /p2p/ peer of a multiaddr, or ''. */
export function peerIdOfMultiaddr(addr: string): string {
  const match = /\/(?:p2p|ipfs)\/([1-9A-HJ-NP-Za-km-z]+)(?:\/|$)/.exec(addr);
  return match ? match[1] : '';
}

/**
 * True when a multiaddr names a denylisted peer anywhere in it — as the target
 * or as a relay hop (`.../p2p/<relay>/p2p-circuit/p2p/<target>`).
 */
export function isDeniedMultiaddr(addr: string): boolean {
  for (const match of addr.matchAll(/\/(?:p2p|ipfs)\/([1-9A-HJ-NP-Za-km-z]+)/g)) {
    if (DENIED.has(match[1])) return true;
  }
  return false;
}

export function filterDeniedMultiaddrs(addrs: readonly string[]): string[] {
  return addrs.filter((addr) => !isDeniedMultiaddr(addr));
}

type AnchorEndpoint = {
  readonly peerId: string;
  readonly browserTransports: readonly string[];
  readonly bootstrapTransports: readonly string[];
};

function endpointAddrs(endpoint: AnchorEndpoint, kind: 'browserTransports' | 'bootstrapTransports'): string[] {
  if (!endpoint.peerId || isPlaceholderAnchor(endpoint.peerId) || isDeniedIdentityValue(endpoint.peerId)) {
    return [];
  }
  return endpoint[kind].map((transport) => `${transport.replace(/\/+$/, '')}/p2p/${endpoint.peerId}`);
}

/** The anchored provider's PeerID, or '' while the anchors file still holds a placeholder. */
export const ANCHORED_PROVIDER_PEER_ID: string =
  isPlaceholderAnchor(IDENTITY_ANCHORS.provider.peerId) || isDeniedIdentityValue(IDENTITY_ANCHORS.provider.peerId)
    ? ''
    : IDENTITY_ANCHORS.provider.peerId;

/** The anchored PeerID of a named peer (e.g. 'celestrak.eth'), or ''. */
export function anchoredPeerId(name: string): string {
  const entry = IDENTITY_ANCHORS.peers.find((peer) => peer.name === name);
  if (!entry || isPlaceholderAnchor(entry.peerId) || isDeniedIdentityValue(entry.peerId)) return '';
  return entry.peerId;
}

/** Browser-dialable relay multiaddrs for the provider and the other anchored peers, provider first. */
export function anchoredBrowserRelays(): string[] {
  return [IDENTITY_ANCHORS.provider, ...IDENTITY_ANCHORS.peers].flatMap((endpoint) =>
    endpointAddrs(endpoint, 'browserTransports'),
  );
}

/** Node (tcp/ws) bootstrap multiaddrs for the provider and the other anchored peers. */
export function anchoredBootstrapAddrs(): string[] {
  return [IDENTITY_ANCHORS.provider, ...IDENTITY_ANCHORS.peers].flatMap((endpoint) =>
    endpointAddrs(endpoint, 'bootstrapTransports'),
  );
}

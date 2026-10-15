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
  ...(deny.x25519PublicKeys as readonly string[]).map((v) => v.toLowerCase()),
  ...deny.xpubs,
  ...deny.updateRootKeyIds.map((v) => v.toLowerCase()),
  ...deny.discoveryCids,
  ...deny.chainAddresses,
  // An Ethereum address in any letter case, not only its EIP-55 checksum case.
  ...deny.chainAddresses.filter((v) => /^0x/i.test(v)).map((v) => v.toLowerCase()),
]);

export function isPlaceholderAnchor(value: string | undefined | null): boolean {
  return typeof value === 'string' && value.startsWith(PLACEHOLDER_PREFIX);
}

const KEY_LENGTHS = new Set([32, 33, 64, 65]);

/** A key written as (0x-)hex or base64/base64url, as bytes; null for anything else. */
function decodeKeyText(value: string): Uint8Array | null {
  const hexText = value.replace(/^0x/i, '');
  if (/^(?:[0-9a-f]{2})+$/i.test(hexText) && KEY_LENGTHS.has(hexText.length / 2)) {
    const out = new Uint8Array(hexText.length / 2);
    for (let i = 0; i < out.length; i += 1) out[i] = Number.parseInt(hexText.slice(i * 2, i * 2 + 2), 16);
    return out;
  }
  if (/^[A-Za-z0-9+/_-]{40,88}={0,2}$/.test(value)) {
    try {
      const b64 = value.replace(/-/g, '+').replace(/_/g, '/');
      const binary = atob(b64 + '='.repeat((4 - (b64.length % 4)) % 4));
      if (KEY_LENGTHS.has(binary.length)) return Uint8Array.from(binary, (c) => c.charCodeAt(0));
    } catch {
      return null;
    }
  }
  return null;
}

/**
 * True for any public value (PeerID, key, xpub, key id, CID, address) of a
 * denylisted identity, in any spelling: verbatim, any letter case, 0x-prefixed
 * hex, base64/base64url, and every secp256k1 point form (verifier r3 S3).
 */
export function isDeniedIdentityValue(value: string | undefined | null): boolean {
  if (typeof value !== 'string') return false;
  const trimmed = value.trim();
  if (!trimmed) return false;
  if (DENIED.has(trimmed) || DENIED.has(trimmed.toLowerCase())) return true;
  const raw = decodeKeyText(trimmed);
  return raw !== null && isDeniedPublicKey(raw);
}

/** 02/03 || X for a point's X and Y (hex): the parity of Y picks the prefix. */
function compressedSecp256k1Hex(xHex: string, yHex: string): string {
  const yLast = Number.parseInt(yHex.slice(-2), 16);
  return `${yLast % 2 === 0 ? '02' : '03'}${xHex.toLowerCase()}`;
}

function toHex(bytes: Uint8Array): string {
  let out = '';
  for (const byte of bytes) out += byte.toString(16).padStart(2, '0');
  return out;
}

/**
 * True for raw public key bytes of a denylisted identity: Ed25519/X25519 (32),
 * compressed secp256k1 (33), and the same point uncompressed (04||X||Y),
 * hybrid (06/07||X||Y) or raw (X||Y) — verifier r2/r3 S3.
 */
export function isDeniedPublicKey(key: Uint8Array | undefined | null): boolean {
  if (!key || key.length === 0) return false;
  const hexKey = toHex(key);
  if (DENIED.has(hexKey)) return true;
  if (key.length === 65 && (key[0] === 0x04 || key[0] === 0x06 || key[0] === 0x07)) {
    return DENIED.has(compressedSecp256k1Hex(hexKey.slice(2, 66), hexKey.slice(66)));
  }
  if (key.length === 64) return DENIED.has(compressedSecp256k1Hex(hexKey.slice(0, 64), hexKey.slice(64)));
  return false;
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

/**
 * True when any string or key-sized byte array anywhere in `value` (an EPM, a
 * parsed EPM, a descriptor) is a public value of a denylisted identity: a
 * PeerID, hex key, xpub, key id, CID, chain address, or a multiaddr naming the
 * peer. Depth-limited; never throws.
 */
export function namesDeniedIdentity(value: unknown, depth = 0): boolean {
  if (depth > 8 || value === null || value === undefined) return false;
  if (typeof value === 'string') return isDeniedIdentityValue(value) || isDeniedMultiaddr(value);
  if (value instanceof Uint8Array) return (value.length === 32 || value.length === 33) && isDeniedPublicKey(value);
  if (Array.isArray(value)) return value.some((item) => namesDeniedIdentity(item, depth + 1));
  if (typeof value === 'object') {
    return Object.values(value as Record<string, unknown>).some((item) => namesDeniedIdentity(item, depth + 1));
  }
  return false;
}

type PeerLike = { toString(): string };
type GateFn = (...args: never[]) => boolean | Promise<boolean>;
type GateLike = { [name: string]: unknown };

async function baseDenies(base: GateLike, name: string, args: unknown[]): Promise<boolean> {
  const fn = base[name];
  return typeof fn === 'function' ? Boolean(await (fn as (...a: unknown[]) => unknown)(...args)) : false;
}

/**
 * A libp2p connection gater that refuses the denylisted identities at the
 * transport level (verifier S3): never dial them, and drop any connection,
 * inbound or outbound, whose remote peer turns out to be one of them after the
 * handshake — so DHT, delegated-routing and pubsub peers cannot reach the old
 * identity either. Every other gate is the caller's (or libp2p's default):
 * `denyDialMultiaddr` is only wrapped when the caller replaced it, so a
 * browser keeps libp2p's private-address policy.
 */
export function deniedIdentityConnectionGater<T extends GateLike>(base: T = {} as T): T & Record<string, GateFn> {
  const peerDenied = (peerId: PeerLike | undefined) => Boolean(peerId) && isDeniedIdentityValue(String(peerId));
  const gate: GateLike = { ...base };
  gate.denyDialPeer = async (peerId: PeerLike, ...rest: unknown[]) =>
    peerDenied(peerId) || baseDenies(base, 'denyDialPeer', [peerId, ...rest]);
  for (const name of [
    'denyInboundEncryptedConnection',
    'denyOutboundEncryptedConnection',
    'denyInboundUpgradedConnection',
    'denyOutboundUpgradedConnection',
  ]) {
    gate[name] = async (peerId: PeerLike, ...rest: unknown[]) => peerDenied(peerId) || baseDenies(base, name, [peerId, ...rest]);
  }
  if (typeof base.denyDialMultiaddr === 'function') {
    gate.denyDialMultiaddr = async (addr: PeerLike, ...rest: unknown[]) =>
      isDeniedMultiaddr(String(addr)) || baseDenies(base, 'denyDialMultiaddr', [addr, ...rest]);
  }
  return gate as T & Record<string, GateFn>;
}

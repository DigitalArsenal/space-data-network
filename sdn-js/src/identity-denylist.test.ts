import { beforeAll, describe, expect, it } from 'vitest';
import { initHDWallet } from './crypto/hd-wallet';
import {
  DEFAULT_EDGE_RELAYS,
  EdgeDiscovery,
  REGIONAL_FALLBACK_RELAYS,
  SPACEAWARE_RELAY_PEER_ID,
  getBootstrapRelays,
} from './edge-discovery';
import { IDENTITY_ANCHORS } from './identity-anchors.generated';
import { Builder } from 'flatbuffers';
import { EPMResolver, KeyType, verifyEPMSignature } from './epm-resolver';
import { normalizeEPMDescriptor } from './server-descriptor';
import {
  DeniedIdentityError,
  deniedIdentityConnectionGater,
  namesDeniedIdentity,
  anchoredBrowserRelays,
  filterDeniedMultiaddrs,
  isDeniedIdentityValue,
  isDeniedMultiaddr,
  isDeniedPublicKey,
} from './identity-denylist';
import { normalizeServerDescriptor } from './server-descriptor';

// SEC-001 regression guard: the PUBLIC values of the sdn.spaceaware.io identity
// whose seed was committed to the public repository.
const LEAKED_PEER_ID = '16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45';
const LEAKED_IDENTITY_KEY = '0257d9a39fac79d4c36e017b3b6913f60684586605ebb9370cf417ef44bf0f7cd2';
const LEAKED_GRANT_KEY = '8e2f6db0048925ce413512df5b4261cee5ad7e04e65588d1a9af26cd4254fa2b';
const LEAKED_PUBLISHER_KEY = '0d80e1fd5f9a4e34dfdf36a0e152bd99a65cfff8bcc6cab2757b484ae442fc8c';

// A synthetic, unrelated provider key (random; no deployed node uses it).
const OTHER_PROVIDER_KEY = '034b6473b0e212f062d132560f5dbb65cbc856584c9201f63b7762545ece39b168';
const OTHER_PEER_ID = '16Uiu2HAmHjHmWJ3zZApae9Ryh4gWjtNHArrEz4x3JoAykfGeQTZy';

function hexToBytes(hex: string): Uint8Array {
  const out = new Uint8Array(hex.length / 2);
  for (let i = 0; i < out.length; i += 1) out[i] = Number.parseInt(hex.slice(i * 2, i * 2 + 2), 16);
  return out;
}

describe('SEC-001 identity denylist', () => {
  beforeAll(async () => {
    await initHDWallet();
  });

  it('names every public value of the leaked identity', () => {
    for (const value of [LEAKED_PEER_ID, LEAKED_IDENTITY_KEY, LEAKED_GRANT_KEY, LEAKED_PUBLISHER_KEY, 'd4a971a7e534']) {
      expect(isDeniedIdentityValue(value)).toBe(true);
    }
    expect(isDeniedIdentityValue(LEAKED_GRANT_KEY.toUpperCase())).toBe(true);
    expect(isDeniedPublicKey(hexToBytes(LEAKED_GRANT_KEY))).toBe(true);
    expect(isDeniedIdentityValue(OTHER_PEER_ID)).toBe(false);
    expect(isDeniedIdentityValue(IDENTITY_ANCHORS.peers[0].peerId)).toBe(false);
  });

  it('drops every multiaddr that names the leaked peer, as target or as relay hop', () => {
    const addrs = [
      `/dns4/sdn.spaceaware.io/tcp/443/wss/p2p/${LEAKED_PEER_ID}`,
      `/ip4/159.203.150.8/tcp/4004/ws/p2p/${LEAKED_PEER_ID}/p2p-circuit/p2p/${OTHER_PEER_ID}`,
      `/dns4/relay.example/tcp/443/wss/p2p/${OTHER_PEER_ID}`,
    ];
    expect(addrs.map(isDeniedMultiaddr)).toEqual([true, true, false]);
    expect(filterDeniedMultiaddrs(addrs)).toEqual([`/dns4/relay.example/tcp/443/wss/p2p/${OTHER_PEER_ID}`]);
  });

  it('never ships the leaked peer as a default, regional or anchored relay', () => {
    for (const relay of [...DEFAULT_EDGE_RELAYS, ...Object.values(REGIONAL_FALLBACK_RELAYS).flat(), ...anchoredBrowserRelays()]) {
      expect(relay).not.toContain(LEAKED_PEER_ID);
      expect(relay).not.toContain('PLACEHOLDER-');
    }
    expect(SPACEAWARE_RELAY_PEER_ID).not.toBe(LEAKED_PEER_ID);
  });

  it('EdgeDiscovery refuses the leaked peer from callers and constructors', () => {
    const discovery = new EdgeDiscovery([`/dns4/sdn.spaceaware.io/tcp/443/wss/p2p/${LEAKED_PEER_ID}`]);
    discovery.addRelay(`/dns4/sdn.spaceaware.io/tcp/443/wss/p2p/${LEAKED_PEER_ID}`);
    expect(discovery.getRelays()).toEqual([]);
  });

  it('getBootstrapRelays drops a delegated-routing answer for the leaked peer', async () => {
    const fetchImpl = (async () => ({
      ok: true,
      json: async () => ({ Peers: [{ Addrs: ['/dns4/sdn.spaceaware.io/tcp/443/wss'], ID: LEAKED_PEER_ID }] }),
    })) as unknown as typeof fetch;
    const relays = await getBootstrapRelays({
      routingEndpoint: 'https://router.example',
      peerIds: [LEAKED_PEER_ID],
      fetchImpl,
    });
    for (const relay of relays) expect(relay).not.toContain(LEAKED_PEER_ID);
  });

  it('refuses a provider descriptor for the leaked identity', async () => {
    await expect(normalizeServerDescriptor({ publicKey: LEAKED_IDENTITY_KEY })).rejects.toBeInstanceOf(
      DeniedIdentityError,
    );
  });

  it('refuses a provider descriptor that advertises the leaked grant verifier key', async () => {
    await expect(
      normalizeServerDescriptor({ publicKey: OTHER_PROVIDER_KEY, grantVerifierPublicKeys: [LEAKED_GRANT_KEY] }),
    ).rejects.toBeInstanceOf(DeniedIdentityError);
  });

  it('keeps an ordinary descriptor but drops relay addresses naming the leaked peer', async () => {
    const normalized = await normalizeServerDescriptor({
      publicKey: OTHER_PROVIDER_KEY,
      relayAddresses: [
        `/dns4/sdn.spaceaware.io/tcp/443/wss/p2p/${LEAKED_PEER_ID}`,
        '/dns4/relay.example/tcp/443/wss',
      ],
    });
    expect(normalized.peerId).toBe(OTHER_PEER_ID);
    expect(normalized.relayAddresses).toEqual(['/dns4/relay.example/tcp/443/wss']);
  });

  it('S3: the connection gate refuses the leaked peer and keeps the caller\'s other gates', async () => {
    const leaked = { toString: () => LEAKED_PEER_ID };
    const other = { toString: () => OTHER_PEER_ID };
    const baseDialPeer = async (p: { toString(): string }) => p.toString() === 'blocked-by-caller';
    const gate = deniedIdentityConnectionGater({ denyDialPeer: baseDialPeer, denyInboundConnection: async () => true });
    expect(await gate.denyDialPeer(leaked)).toBe(true);
    expect(await gate.denyDialPeer(other)).toBe(false);
    expect(await gate.denyDialPeer({ toString: () => 'blocked-by-caller' })).toBe(true);
    for (const name of ['denyInboundEncryptedConnection', 'denyOutboundEncryptedConnection', 'denyInboundUpgradedConnection', 'denyOutboundUpgradedConnection']) {
      expect(await gate[name](leaked as never)).toBe(true);
      expect(await gate[name](other as never)).toBe(false);
    }
    expect(await (gate.denyInboundConnection as () => Promise<boolean>)()).toBe(true);
    expect(gate.denyDialMultiaddr).toBeUndefined();
    const withMultiaddr = deniedIdentityConnectionGater({ denyDialMultiaddr: async () => false });
    expect(await withMultiaddr.denyDialMultiaddr({ toString: () => `/dns4/sdn.spaceaware.io/tcp/443/wss/p2p/${LEAKED_PEER_ID}` } as never)).toBe(true);
    expect(await withMultiaddr.denyDialMultiaddr({ toString: () => `/dns4/relay.example/tcp/443/wss/p2p/${OTHER_PEER_ID}` } as never)).toBe(false);
  });

  it('S3: normalizeEPMDescriptor refuses an EPM naming the leaked identity and admits an ordinary one', async () => {
    await expect(normalizeEPMDescriptor(buildEPM({ publicKeyHex: LEAKED_IDENTITY_KEY }))).rejects.toBeInstanceOf(DeniedIdentityError);
    await expect(
      normalizeEPMDescriptor(buildEPM({ publicKeyHex: OTHER_PROVIDER_KEY, grantVerifierPublicKeyHex: LEAKED_GRANT_KEY })),
    ).rejects.toBeInstanceOf(DeniedIdentityError);
    const ok = await normalizeEPMDescriptor(
      buildEPM({
        publicKeyHex: OTHER_PROVIDER_KEY,
        multiformatAddresses: [`/dns4/sdn.spaceaware.io/tcp/443/wss/p2p/${LEAKED_PEER_ID}`, '/dns4/relay.example/tcp/443/wss'],
      }),
    );
    expect(ok.peerId).toBe(OTHER_PEER_ID);
    expect(ok.relayAddresses).toEqual(['/dns4/relay.example/tcp/443/wss']);
  });

  it('S3: the EPM resolver never resolves, caches or hands out keys of the leaked identity', async () => {
    const resolver = new EPMResolver();
    await expect(resolver.resolveByPeerID(LEAKED_PEER_ID)).rejects.toBeInstanceOf(DeniedIdentityError);
    expect(() => resolver.addParsedEPM(LEAKED_PEER_ID, { keys: [{ publicKey: OTHER_PROVIDER_KEY, keyType: KeyType.Encryption }] })).toThrow(DeniedIdentityError);
    expect(() =>
      resolver.addParsedEPM(OTHER_PEER_ID, { keys: [{ publicKey: LEAKED_PUBLISHER_KEY, keyType: KeyType.Signing, algorithm: 'x25519' }] }),
    ).toThrow(DeniedIdentityError);
    expect(() =>
      resolver.addParsedEPM(OTHER_PEER_ID, {
        keys: [{ publicKey: OTHER_PROVIDER_KEY, keyType: KeyType.Encryption }],
        multiformatAddresses: [`/dns4/sdn.spaceaware.io/tcp/443/wss/p2p/${LEAKED_PEER_ID}`],
      }),
    ).toThrow(DeniedIdentityError);
    expect(await resolver.getEncryptionKey(OTHER_PEER_ID)).toBeNull();
    resolver.addParsedEPM(OTHER_PEER_ID, { keys: [{ publicKey: OTHER_PROVIDER_KEY, keyType: KeyType.Encryption }] });
    expect((await resolver.getEncryptionKey(OTHER_PEER_ID))?.publicKeyHex).toBe(OTHER_PROVIDER_KEY);
  });

  it('S3: verifyEPMSignature (as sdn-js re-exports it) refuses the leaked signer', () => {
    // The gate answers before any curve is touched, so no wallet is needed.
    expect(verifyEPMSignature({} as never, { SIGNATURE: '00' }, hexToBytes(LEAKED_PUBLISHER_KEY))).toBe(false);
    expect(verifyEPMSignature({} as never, { SIGNATURE: '00', KEYS: [{ PUBLIC_KEY: LEAKED_IDENTITY_KEY }] }, hexToBytes(OTHER_PROVIDER_KEY))).toBe(false);
    expect(namesDeniedIdentity({ a: [{ b: `/ip4/1.2.3.4/tcp/1/p2p/${LEAKED_PEER_ID}` }] })).toBe(true);
    expect(namesDeniedIdentity({ a: [{ b: OTHER_PROVIDER_KEY }] })).toBe(false);
  });
});

describe('SEC-001 S3: uncompressed secp256k1 keys', () => {
  it('denies all five denylisted secp256k1 keys in uncompressed form, as bytes, hex and 0x-hex', async () => {
    const { ECDH } = await import('node:crypto');
    const keys = IDENTITY_ANCHORS.denylist.secp256k1PublicKeys;
    expect(keys.length).toBe(5);
    for (const compressed of keys) {
      const uncompressed = ECDH.convertKey(compressed, 'secp256k1', 'hex', 'hex', 'uncompressed') as string;
      expect(uncompressed).toMatch(/^04[0-9a-f]{128}$/);
      expect(isDeniedPublicKey(hexToBytes(uncompressed))).toBe(true);
      expect(isDeniedIdentityValue(uncompressed)).toBe(true);
      expect(isDeniedIdentityValue(`0x${uncompressed.toUpperCase()}`)).toBe(true);
    }
    // An ordinary key, uncompressed, is not denied.
    const other = ECDH.convertKey(OTHER_PROVIDER_KEY, 'secp256k1', 'hex', 'hex', 'uncompressed') as string;
    expect(isDeniedPublicKey(hexToBytes(other))).toBe(false);
    expect(isDeniedIdentityValue(other)).toBe(false);
  });
});

// A minimal $EPM (the shape server-descriptor.test.ts builds): a secp256k1
// provider key, an optional Ed25519 grant-verifier key, relay addresses.
function buildEPM(input: { publicKeyHex: string; grantVerifierPublicKeyHex?: string; multiformatAddresses?: string[] }): Uint8Array {
  const builder = new Builder(256);
  const key = (hex: string, addressType: string) => {
    const publicKeyOffset = builder.createString(hex);
    const addressTypeOffset = builder.createString(addressType);
    builder.startObject(7);
    builder.addFieldOffset(0, publicKeyOffset, 0);
    builder.addFieldOffset(5, addressTypeOffset, 0);
    builder.addFieldInt8(6, 0, 0);
    return builder.endObject();
  };
  const vector = (offsets: number[]) => {
    builder.startVector(4, offsets.length, 4);
    for (let i = offsets.length - 1; i >= 0; i -= 1) builder.addOffset(offsets[i]);
    return builder.endVector();
  };
  const keyOffsets = [key(input.publicKeyHex, 'secp256k1')];
  if (input.grantVerifierPublicKeyHex) keyOffsets.push(key(input.grantVerifierPublicKeyHex, 'ed25519'));
  const keysOffset = vector(keyOffsets);
  const addressOffsets = (input.multiformatAddresses ?? []).map((value) => builder.createString(value));
  const multiformatOffset = addressOffsets.length > 0 ? vector(addressOffsets) : 0;
  builder.startObject(15);
  builder.addFieldOffset(13, keysOffset, 0);
  if (multiformatOffset !== 0) builder.addFieldOffset(14, multiformatOffset, 0);
  builder.finish(builder.endObject(), '$EPM');
  return builder.asUint8Array();
}

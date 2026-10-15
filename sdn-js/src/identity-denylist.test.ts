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
import {
  DeniedIdentityError,
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
});

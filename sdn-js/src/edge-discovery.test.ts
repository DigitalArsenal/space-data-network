import { describe, it, expect, beforeEach, vi } from 'vitest';
import {
  DEFAULT_EDGE_RELAYS,
  REGIONAL_FALLBACK_RELAYS,
  getDiscoveryMetrics,
  resetDiscoveryMetrics,
  getRegionalRelays,
  getAllFallbackRelays,
  EdgeDiscovery,
  isBrowserDialableAddr,
  rankBrowserBootstrapAddrs,
  resolvePeerBootstrapAddrs,
  getBootstrapRelays,
  SPACEAWARE_RELAY_PEER_ID,
  CELESTRAK_RELAY_PEER_ID,
} from './edge-discovery';

describe('edge-discovery', () => {
  describe('DEFAULT_EDGE_RELAYS', () => {
    it('should contain relay addresses', () => {
      expect(DEFAULT_EDGE_RELAYS.length).toBeGreaterThan(0);
    });

    it('should have valid multiaddr format', () => {
      for (const relay of DEFAULT_EDGE_RELAYS) {
        expect(relay).toMatch(/^\/(dns4|ip4|ip6)\//);
        expect(relay).toContain('/p2p/');
      }
    });

    it('should use DNS-based addresses for production relays', () => {
      const dnsRelays = DEFAULT_EDGE_RELAYS.filter((r) => r.startsWith('/dns4/'));
      expect(dnsRelays.length).toBeGreaterThan(0);
    });

    // BOOTSTRAP ROT LAW (upstream-sdn-2, 2026-08-06). The previous version of
    // this test PINNED two webrtc-direct certhash addresses. Both were dead by
    // 2026-08-06 — the full node advertises no webrtc-direct address at all,
    // and the celestrak peer had rotated its identity entirely. A pinned
    // certificate hash in a shipped browser bundle is guaranteed to rot, so the
    // invariant is now inverted: shipped defaults must carry NO certhash.
    it('ships no pinned certificate hashes (they rot on rotation)', () => {
      for (const relay of DEFAULT_EDGE_RELAYS) {
        expect(relay).not.toContain('/certhash/');
      }
      for (const relay of Object.values(REGIONAL_FALLBACK_RELAYS).flat()) {
        expect(relay).not.toContain('/certhash/');
      }
    });

    it('bootstraps the full node over a CA-authenticated, browser-dialable address', () => {
      // Verified live 2026-08-06: this address dials 16Uiu2HAm1Lbv…Fm45 through
      // Cloudflare and yields a DIRECT (non-transient) connection.
      expect(DEFAULT_EDGE_RELAYS).toContain(
        '/dns4/sdn.spaceaware.io/tcp/443/wss/p2p/16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45',
      );
      for (const relay of DEFAULT_EDGE_RELAYS) {
        expect(isBrowserDialableAddr(relay)).toBe(true);
      }
      expect(DEFAULT_EDGE_RELAYS.join('\n')).not.toContain('/ip4/159.203.150.8/');
    });

    it('never ships a plain /ws address (mixed content on an HTTPS origin)', () => {
      // The forbidden thing is an UNENCRYPTED websocket, not the four
      // characters "/ws": an AutoTLS address ends `/tls/ws/p2p/…` and is
      // exactly what we want to ship. Strip the TLS-wrapped form first, then
      // assert nothing insecure survives.
      for (const relay of [
        ...DEFAULT_EDGE_RELAYS,
        ...Object.values(REGIONAL_FALLBACK_RELAYS).flat(),
      ]) {
        expect(relay.replace('/tls/ws', '')).not.toMatch(/\/ws(\/|$)/);
      }
    });

    // ops-host02-browser-relay-promotion (owner ruling 2026-08-06). Before
    // this, the fleet advertised ONE CA-authenticated browser-dialable address
    // and ensureMinimumRelays could not honestly clear a floor of 2 with
    // distinct nodes.
    it('ships TWO distinct browser-dialable nodes, not one address twice', () => {
      const peers = new Set(
        DEFAULT_EDGE_RELAYS.map((r) => r.split('/p2p/')[1]).filter(Boolean),
      );
      expect(peers.size).toBeGreaterThanOrEqual(2);
      expect(peers.has(SPACEAWARE_RELAY_PEER_ID)).toBe(true);
      expect(peers.has(CELESTRAK_RELAY_PEER_ID)).toBe(true);
    });

    it('bootstraps celestrak.eth over its AutoTLS libp2p.direct address', () => {
      // Live-verified 2026-08-06 from this package's own js-libp2p stack:
      // DIAL_OK 499 ms, limits null (direct), Let's Encrypt chain verified
      // off-box. The libp2p.direct label is the node's OWN peer id in base36,
      // so the name and its certificate rotate with the node, not with a
      // pinned hash.
      const celestrak = DEFAULT_EDGE_RELAYS.find((r) =>
        r.includes(CELESTRAK_RELAY_PEER_ID),
      );
      expect(celestrak).toBeDefined();
      expect(celestrak).toContain('.libp2p.direct/');
      expect(celestrak).toContain('/tls/ws/');
      expect(celestrak).not.toContain('/certhash/');
      expect(isBrowserDialableAddr(celestrak as string)).toBe(true);
      // The SHORT /dns4 form is load-bearing: @libp2p/websockets discards the
      // long /ip4/…/tls/sni/<name>/ws form before opening a socket.
      expect(celestrak?.startsWith('/dns4/')).toBe(true);
    });
  });

  describe('browser bootstrap address hygiene', () => {
    it('rejects addresses a browser cannot dial from an HTTPS origin', () => {
      expect(isBrowserDialableAddr('/ip4/159.203.150.8/tcp/4004/ws')).toBe(false);
      expect(isBrowserDialableAddr('/ip4/1.2.3.4/tcp/4002')).toBe(false);
      expect(isBrowserDialableAddr('/ip4/1.2.3.4/udp/4001/quic-v1')).toBe(false);
    });

    it('accepts CA-authenticated wss and AutoTLS libp2p.direct /tls/ws', () => {
      expect(
        isBrowserDialableAddr('/dns4/sdn.spaceaware.io/tcp/443/wss/p2p/peer'),
      ).toBe(true);
      expect(
        isBrowserDialableAddr(
          '/dns4/167-172-219-213.k51qzi5uqu5diqcia4ahdv9x3znxts7c3swrel32a0i1mk3qukdg25z4h6rypi.libp2p.direct/tcp/4002/tls/ws',
        ),
      ).toBe(true);
    });

    it('ranks CA-authenticated addresses ahead of freshly resolved certhash ones', () => {
      const ranked = rankBrowserBootstrapAddrs([
        '/ip4/1.2.3.4/udp/4003/webrtc-direct/certhash/uEiFAKE',
        '/ip4/159.203.150.8/tcp/4004/ws',
        '/dns4/sdn.spaceaware.io/tcp/443/wss/p2p/peer',
      ]);
      expect(ranked).toEqual([
        '/dns4/sdn.spaceaware.io/tcp/443/wss/p2p/peer',
        '/ip4/1.2.3.4/udp/4003/webrtc-direct/certhash/uEiFAKE',
      ]);
    });
  });

  describe('resolvePeerBootstrapAddrs (delegated routing)', () => {
    const PEER = '16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45';

    it('resolves current addresses and re-attaches the /p2p component', async () => {
      const fetchImpl = vi.fn(async () => ({
        ok: true,
        json: async () => ({
          Peers: [
            {
              Addrs: [
                '/ip4/159.203.150.8/tcp/4004/ws',
                '/dns4/sdn.spaceaware.io/tcp/443/wss',
              ],
              ID: PEER,
            },
          ],
        }),
      })) as unknown as typeof fetch;

      const addrs = await resolvePeerBootstrapAddrs(PEER, {
        routingEndpoint: 'https://sdn.spaceaware.io',
        fetchImpl,
      });

      // Plain /ws is dropped; the wss address gains /p2p/<peer>.
      expect(addrs).toEqual([
        `/dns4/sdn.spaceaware.io/tcp/443/wss/p2p/${PEER}`,
      ]);
      expect(fetchImpl).toHaveBeenCalledWith(
        `https://sdn.spaceaware.io/routing/v1/peers/${PEER}`,
        expect.objectContaining({ headers: { Accept: 'application/json' } }),
      );
    });

    it('returns [] when the router fails, so callers fall back to defaults', async () => {
      const failing = vi.fn(async () => {
        throw new Error('offline');
      }) as unknown as typeof fetch;
      await expect(
        resolvePeerBootstrapAddrs(PEER, {
          routingEndpoint: 'https://example.invalid',
          fetchImpl: failing,
        }),
      ).resolves.toEqual([]);

      const notOk = vi.fn(async () => ({ ok: false })) as unknown as typeof fetch;
      await expect(
        resolvePeerBootstrapAddrs(PEER, {
          routingEndpoint: 'https://example.invalid',
          fetchImpl: notOk,
        }),
      ).resolves.toEqual([]);
    });

    it('contacts no routing endpoint unless one is explicitly configured', async () => {
      const fetchImpl = vi.fn() as unknown as typeof fetch;
      const relays = await getBootstrapRelays({ fetchImpl });
      expect(fetchImpl).not.toHaveBeenCalled();
      expect(relays.length).toBeGreaterThan(0);
    });
  });

  describe('REGIONAL_FALLBACK_RELAYS', () => {
    it('should have regional categories', () => {
      expect(Object.keys(REGIONAL_FALLBACK_RELAYS).length).toBeGreaterThan(0);
    });

    it('should have valid relay addresses per region', () => {
      for (const [region, relays] of Object.entries(REGIONAL_FALLBACK_RELAYS)) {
        expect(relays.length).toBeGreaterThan(0);
        for (const relay of relays) {
          expect(relay).toContain('/p2p/');
        }
      }
    });
  });

  describe('getDiscoveryMetrics', () => {
    beforeEach(() => {
      resetDiscoveryMetrics();
    });

    it('should return initial metrics', () => {
      const metrics = getDiscoveryMetrics();
      expect(metrics.wasmLoadAttempts).toBe(0);
      expect(metrics.wasmLoadSuccesses).toBe(0);
      expect(metrics.wasmLoadFailures).toBe(0);
      expect(metrics.relaysDiscovered).toBe(0);
      expect(metrics.fallbacksUsed).toBe(0);
    });

    it('should return a copy of metrics (immutable)', () => {
      const metrics1 = getDiscoveryMetrics();
      const metrics2 = getDiscoveryMetrics();
      expect(metrics1).not.toBe(metrics2);
      expect(metrics1).toEqual(metrics2);
    });
  });

  describe('resetDiscoveryMetrics', () => {
    it('should reset all metrics to initial values', () => {
      // We can't directly modify internal metrics, but after reset they should be 0
      resetDiscoveryMetrics();
      const metrics = getDiscoveryMetrics();
      expect(metrics.wasmLoadAttempts).toBe(0);
      expect(metrics.wasmLoadSuccesses).toBe(0);
      expect(metrics.wasmLoadFailures).toBe(0);
      expect(metrics.lastLoadTime).toBeNull();
      expect(metrics.lastError).toBeNull();
    });
  });

  describe('getRegionalRelays', () => {
    it('should return relays for a specific region', () => {
      const regions = Object.keys(REGIONAL_FALLBACK_RELAYS);
      if (regions.length > 0) {
        const regionRelays = getRegionalRelays(regions[0]);
        expect(regionRelays).toEqual(REGIONAL_FALLBACK_RELAYS[regions[0]]);
      }
    });

    it('should return all regional relays when no region specified', () => {
      const allRelays = getRegionalRelays();
      const expectedCount = Object.values(REGIONAL_FALLBACK_RELAYS).flat().length;
      expect(allRelays.length).toBe(expectedCount);
    });

    it('should return all relays for unknown region', () => {
      const relays = getRegionalRelays('unknown-region');
      expect(relays.length).toBeGreaterThan(0);
    });
  });

  describe('getAllFallbackRelays', () => {
    it('should include default relays', () => {
      const all = getAllFallbackRelays();
      for (const relay of DEFAULT_EDGE_RELAYS) {
        expect(all).toContain(relay);
      }
    });

    it('should include regional relays', () => {
      const all = getAllFallbackRelays();
      const regionalRelays = Object.values(REGIONAL_FALLBACK_RELAYS).flat();
      for (const relay of regionalRelays) {
        expect(all).toContain(relay);
      }
    });

    it('should not have duplicates', () => {
      const all = getAllFallbackRelays();
      const unique = new Set(all);
      expect(unique.size).toBe(all.length);
    });
  });

  describe('EdgeDiscovery', () => {
    let discovery: EdgeDiscovery;

    beforeEach(() => {
      discovery = new EdgeDiscovery(['relay1', 'relay2', 'relay3']);
    });

    describe('constructor', () => {
      it('should initialize with provided relays', () => {
        expect(discovery.getRelays()).toContain('relay1');
        expect(discovery.getRelays()).toContain('relay2');
        expect(discovery.getRelays()).toContain('relay3');
      });

      it('should use defaults when no relays provided', () => {
        const defaultDiscovery = new EdgeDiscovery();
        expect(defaultDiscovery.getRelays().length).toBe(DEFAULT_EDGE_RELAYS.length);
      });
    });

    describe('getRelays', () => {
      it('should return all known relays', () => {
        const relays = discovery.getRelays();
        expect(relays.length).toBe(3);
      });
    });

    describe('addRelay', () => {
      it('should add a new relay', () => {
        discovery.addRelay('relay4');
        expect(discovery.getRelays()).toContain('relay4');
      });

      it('should not duplicate existing relays', () => {
        const before = discovery.getRelays().length;
        discovery.addRelay('relay1'); // Already exists
        expect(discovery.getRelays().length).toBe(before);
      });
    });

    describe('removeRelay', () => {
      it('should remove an existing relay', () => {
        discovery.removeRelay('relay2');
        expect(discovery.getRelays()).not.toContain('relay2');
      });

      it('should handle removing non-existent relay', () => {
        const before = discovery.getRelays().length;
        discovery.removeRelay('nonexistent');
        expect(discovery.getRelays().length).toBe(before);
      });
    });

    describe('hasRelay', () => {
      it('should return true for existing relay', () => {
        expect(discovery.hasRelay('relay1')).toBe(true);
      });

      it('should return false for non-existent relay', () => {
        expect(discovery.hasRelay('nonexistent')).toBe(false);
      });
    });

    describe('markFailed / markSuccess', () => {
      it('should track failures', () => {
        discovery.markFailed('relay1');
        discovery.markFailed('relay1');
        // Still should have the relay (not at max failures yet)
        expect(discovery.hasRelay('relay1')).toBe(true);
      });

      it('should remove relay after max failures', () => {
        discovery.markFailed('relay1');
        discovery.markFailed('relay1');
        discovery.markFailed('relay1'); // 3rd failure
        expect(discovery.hasRelay('relay1')).toBe(false);
      });

      it('should reset failure count on success', () => {
        discovery.markFailed('relay1');
        discovery.markFailed('relay1');
        discovery.markSuccess('relay1');
        discovery.markFailed('relay1'); // Should be 1st failure again
        expect(discovery.hasRelay('relay1')).toBe(true);
      });

      it('should re-add relay on success', () => {
        discovery.removeRelay('relay1');
        discovery.markSuccess('relay1');
        expect(discovery.hasRelay('relay1')).toBe(true);
      });
    });

    describe('getBestRelays', () => {
      it('should return requested number of relays', () => {
        const best = discovery.getBestRelays(2);
        expect(best.length).toBe(2);
      });

      it('should prioritize relays with fewer failures', () => {
        discovery.markFailed('relay1');
        discovery.markFailed('relay1');
        discovery.markFailed('relay2');
        // relay3 has no failures
        const best = discovery.getBestRelays(1);
        expect(best[0]).toBe('relay3');
      });

      it('should return all relays if count exceeds available', () => {
        const best = discovery.getBestRelays(10);
        expect(best.length).toBe(3);
      });
    });

    describe('ensureMinimumRelays', () => {
      // Best-effort by construction: it can only add relays that actually
      // exist. Since the certhash purge (upstream-sdn-2) the shipped fallback
      // pool is the set of CA-authenticated addresses the fleet really
      // advertises — one today — so the assertion is "drains the pool", not
      // "invents addresses". Growing the pool is an infrastructure change
      // (a second AutoTLS/wss bootstrap host), not a code change.
      it('adds every available fallback when below minimum', () => {
        const smallDiscovery = new EdgeDiscovery(['only-one']);
        smallDiscovery.ensureMinimumRelays(3);

        const expected = Math.min(3, 1 + getAllFallbackRelays().length);
        expect(smallDiscovery.getRelays().length).toBe(expected);
        for (const fallback of getAllFallbackRelays().slice(0, 2)) {
          expect(smallDiscovery.getRelays()).toContain(fallback);
        }
      });

      it('should not add relays when above minimum', () => {
        const before = discovery.getRelays().length;
        discovery.ensureMinimumRelays(2);
        expect(discovery.getRelays().length).toBe(before);
      });
    });

    describe('getCircuitAddress', () => {
      it('should generate circuit relay address', () => {
        const circuit = discovery.getCircuitAddress('QmPeerID123');
        expect(circuit).toContain('/p2p-circuit/p2p/QmPeerID123');
      });

      it('should return null when no relays available', () => {
        const emptyDiscovery = new EdgeDiscovery([]);
        const circuit = emptyDiscovery.getCircuitAddress('QmPeerID123');
        expect(circuit).toBeNull();
      });
    });
  });
});

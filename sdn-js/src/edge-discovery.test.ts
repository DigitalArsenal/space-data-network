import { describe, it, expect, beforeEach, vi } from 'vitest';
import {
  DEFAULT_EDGE_RELAYS,
  REGIONAL_FALLBACK_RELAYS,
  getDiscoveryMetrics,
  resetDiscoveryMetrics,
  getRegionalRelays,
  getAllFallbackRelays,
  EdgeDiscovery,
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
      it('should add fallbacks when below minimum', () => {
        const smallDiscovery = new EdgeDiscovery(['only-one']);
        smallDiscovery.ensureMinimumRelays(3);
        expect(smallDiscovery.getRelays().length).toBeGreaterThanOrEqual(3);
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

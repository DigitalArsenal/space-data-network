/**
 * BOOTSTRAP CONVERGENCE GUARD — "at any relay count, including zero."
 *
 * Filed as a scheduler task storm (graph task
 * `sdn-js-browser-runtime-2013-breaks-beta-catalogue`): the 2.0.13/2.0.14
 * bootstrap rework was accused of spinning an unbounded zero-delay retry loop
 * in the browser, on the theory that `ensureMinimumRelays` could not satisfy
 * its floor from a two-relay fleet. HERMES measured that premise and REFUTED
 * it (2026-08-06): the browser runtime schedules ~1 timer/second at idle at
 * every relay count including zero, and the 21 k `RunTask`/6 s that was read as
 * a storm is the OrbPro render loop's normal cost — 20,292 and 21,132 per 6 s
 * measured on the arm that was called healthy.
 *
 * These tests exist so the refuted theory can never quietly become true. They
 * are deliberately about TERMINATION and BOUNDEDNESS, not about which
 * addresses ship (that is `edge-discovery.test.ts`):
 *
 *   - a floor the fleet cannot satisfy must return short, not spin;
 *   - runtime bootstrap resolution must make a BOUNDED number of calls and
 *     never retry a failure on its own;
 *   - discovery's periodic work must schedule a bounded number of timers and
 *     surrender every one of them on stop.
 *
 * A regression that reintroduces a retry loop fails here as a task-count
 * ceiling breach, which is the only symptom a storm actually has.
 */

import { describe, it, expect, vi, afterEach } from 'vitest';
import {
  DEFAULT_EDGE_RELAYS,
  EdgeDiscovery,
  getAllFallbackRelays,
  getBootstrapRelays,
  resolvePeerBootstrapAddrs,
  SPACEAWARE_RELAY_PEER_ID,
} from './edge-discovery';

/**
 * Count every timer a block of work schedules. A storm is not "slow code" —
 * it is an unbounded number of scheduled tasks, so counting them is the only
 * assertion that distinguishes the two.
 */
function countScheduledTimers<T>(run: () => T): { result: T; timeouts: number; intervals: number; zeroDelay: number } {
  const realTimeout = globalThis.setTimeout;
  const realInterval = globalThis.setInterval;
  let timeouts = 0;
  let intervals = 0;
  let zeroDelay = 0;
  const handles: Array<ReturnType<typeof setTimeout>> = [];
  const intervalHandles: Array<ReturnType<typeof setInterval>> = [];
  try {
    (globalThis as unknown as { setTimeout: typeof setTimeout }).setTimeout = ((
      fn: TimerHandler,
      delay?: number,
      ...rest: unknown[]
    ) => {
      timeouts += 1;
      if (!delay || delay <= 1) zeroDelay += 1;
      const handle = (realTimeout as typeof setTimeout)(fn, delay, ...(rest as []));
      handles.push(handle);
      return handle;
    }) as typeof setTimeout;
    (globalThis as unknown as { setInterval: typeof setInterval }).setInterval = ((
      fn: TimerHandler,
      delay?: number,
      ...rest: unknown[]
    ) => {
      intervals += 1;
      const handle = (realInterval as typeof setInterval)(fn, delay, ...(rest as []));
      intervalHandles.push(handle);
      return handle;
    }) as typeof setInterval;
    const result = run();
    return { result, timeouts, intervals, zeroDelay };
  } finally {
    globalThis.setTimeout = realTimeout;
    globalThis.setInterval = realInterval;
    for (const h of handles) clearTimeout(h);
    for (const h of intervalHandles) clearInterval(h);
  }
}

describe('bootstrap convergence (no task storm at any relay count)', () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  describe('ensureMinimumRelays terminates at every floor', () => {
    // 0 and 1 are satisfiable, 2 is exactly the fleet, 3 and 99 are NOT — and
    // the unsatisfiable cases are the ones a retry loop would hide in.
    for (const floor of [0, 1, 2, 3, 99]) {
      it(`returns without spinning for a floor of ${floor}`, () => {
        const discovery = new EdgeDiscovery([]);
        const { timeouts, intervals, zeroDelay } = countScheduledTimers(() =>
          discovery.ensureMinimumRelays(floor),
        );

        // The only honest outcome of an unsatisfiable floor is a SHORT list.
        expect(discovery.getRelays().length).toBeLessThanOrEqual(
          Math.min(floor, getAllFallbackRelays().length),
        );
        // Padding the count with invented addresses is how the dead certhash
        // entries got shipped; every relay must come from the real list.
        for (const relay of discovery.getRelays()) {
          expect(getAllFallbackRelays()).toContain(relay);
        }
        // A storm has a signature: scheduled work. This path schedules NONE.
        expect(timeouts).toBe(0);
        expect(intervals).toBe(0);
        expect(zeroDelay).toBe(0);
      });
    }

    it('is idempotent — repeating it neither grows the list nor schedules work', () => {
      const discovery = new EdgeDiscovery([]);
      const { timeouts, intervals } = countScheduledTimers(() => {
        for (let i = 0; i < 1_000; i += 1) discovery.ensureMinimumRelays(3);
      });
      expect(discovery.getRelays().length).toBeLessThanOrEqual(
        getAllFallbackRelays().length,
      );
      expect(timeouts).toBe(0);
      expect(intervals).toBe(0);
    });
  });

  describe('runtime bootstrap resolution is bounded', () => {
    it('contacts nothing at all without a routing endpoint (zero-external-origin law)', async () => {
      const fetchImpl = vi.fn();
      const relays = await getBootstrapRelays({ fetchImpl: fetchImpl as unknown as typeof fetch });
      expect(fetchImpl).not.toHaveBeenCalled();
      expect(relays.length).toBeGreaterThan(0);
    });

    it('makes exactly ONE call per peer id and never retries a failure', async () => {
      const fetchImpl = vi.fn().mockRejectedValue(new Error('offline'));
      const resolved = await resolvePeerBootstrapAddrs(SPACEAWARE_RELAY_PEER_ID, {
        routingEndpoint: 'http://127.0.0.1:5001',
        fetchImpl: fetchImpl as unknown as typeof fetch,
      });
      // Failure is reported as "nothing found", not retried into a loop; the
      // caller's fallback is the static list.
      expect(resolved).toEqual([]);
      expect(fetchImpl).toHaveBeenCalledTimes(1);
    });

    it('falls back to the static list after a failed lookup, without re-dialling', async () => {
      const fetchImpl = vi.fn().mockResolvedValue({ ok: false, status: 503 });
      const relays = await getBootstrapRelays({
        routingEndpoint: 'http://127.0.0.1:5001',
        peerIds: ['peer-a', 'peer-b', 'peer-c'],
        fetchImpl: fetchImpl as unknown as typeof fetch,
      });
      expect(fetchImpl).toHaveBeenCalledTimes(3);
      expect(relays).toEqual([...DEFAULT_EDGE_RELAYS]);
    });

    it('a router that answers with nothing still terminates', async () => {
      const fetchImpl = vi.fn().mockResolvedValue({
        ok: true,
        json: async () => ({ Peers: [] }),
      });
      const relays = await getBootstrapRelays({
        routingEndpoint: 'http://127.0.0.1:5001',
        fetchImpl: fetchImpl as unknown as typeof fetch,
      });
      expect(fetchImpl).toHaveBeenCalledTimes(1);
      expect(relays).toEqual([...DEFAULT_EDGE_RELAYS]);
    });
  });

  describe('periodic discovery work is bounded and fully surrendered on stop', () => {
    it('probing schedules ONE interval regardless of relay count, including zero', () => {
      for (const relays of [[], [...DEFAULT_EDGE_RELAYS], [...DEFAULT_EDGE_RELAYS, ...getAllFallbackRelays()]]) {
        const discovery = new EdgeDiscovery(relays);
        const probeAll = vi
          .spyOn(discovery, 'probeAllRelays')
          .mockResolvedValue(new Map());
        const { intervals } = countScheduledTimers(() => discovery.startProbing(30_000));
        try {
          expect(intervals).toBe(1);
          // One immediate probe, not one per relay per tick.
          expect(probeAll).toHaveBeenCalledTimes(1);
        } finally {
          discovery.stopProbing();
        }
      }
    });

    it('restarting probing never accumulates intervals', () => {
      const discovery = new EdgeDiscovery([]);
      vi.spyOn(discovery, 'probeAllRelays').mockResolvedValue(new Map());
      const { intervals } = countScheduledTimers(() => {
        for (let i = 0; i < 25; i += 1) discovery.startProbing(30_000);
      });
      discovery.stopProbing();
      // 25 starts, 25 intervals created — but each start stops the previous
      // one first, so at most ONE is ever live. The ceiling that matters is
      // that a restart is not quadratic and leaves nothing behind.
      expect(intervals).toBe(25);
      expect(
        (discovery as unknown as { probeInterval: unknown }).probeInterval,
      ).toBeNull();
    });

    it('an offline node idles: no zero-delay timers are ever scheduled', () => {
      const discovery = new EdgeDiscovery([]);
      vi.spyOn(discovery, 'probeAllRelays').mockResolvedValue(new Map());
      const { zeroDelay } = countScheduledTimers(() => {
        discovery.ensureMinimumRelays(3);
        discovery.startProbing(30_000);
        discovery.getBestRelays(3);
        discovery.getCircuitAddress('16Uiu2HAmTestTarget');
        discovery.stopProbing();
      });
      // setTimeout(fn, 0) in a retry path is the storm's actual mechanism.
      // There is not one anywhere in this lane, and there must never be.
      expect(zeroDelay).toBe(0);
    });
  });
});

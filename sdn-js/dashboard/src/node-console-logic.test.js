/*
 * The NODE dashboard's rules, as tests (task
 * sdn-dashboard-restore-template-widgets, wave 1).
 *
 * Every assertion here is one of the honesty rules from IRIS's ruling. They are
 * not "does it format nicely" checks — they are the locks that stop a future
 * change from fabricating a disk, dashing an unknown field, mislabelling a
 * sparkline window, or leaving a hole in the grid.
 */
import { describe, expect, it, vi } from 'vitest';

import {
  BANDWIDTH_SAMPLE_MS,
  SPARK_SAMPLES,
  createRuntimeFeed,
  foldRuntime,
  formatBytes,
  formatRateMBs,
  formatStartedUTC,
  formatUptimeClock,
  sparkBars,
  sparkSpanS,
  storageFraction,
} from './runtime.js';
import { DEFAULT_LAYOUT, PUBLIC_LAYOUT, WIDGETS, LAYOUT_KEY, layoutFor, rowsOf, tilesExactly } from './node-layout.js';

describe('the template layout, transcribed', () => {
  it('is the export\'s DEFAULT_LAYOUT verbatim', () => {
    // SDN Console.dc.html:873 — health(4) identity(4) service(4) netmap(8)
    // throughput(4). This is the owner's screenshot.
    expect(DEFAULT_LAYOUT).toEqual([
      { id: 'health', span: 4 },
      { id: 'identity', span: 4 },
      { id: 'service', span: 4 },
      { id: 'netmap', span: 8 },
      { id: 'throughput', span: 4 },
    ]);
  });

  it('keeps the design\'s localStorage key for wave 2', () => {
    expect(LAYOUT_KEY).toBe('sdn_node_layout_v1');
  });

  it('never leaves a hole in the 12-column grid, signed in or not', () => {
    // A layout whose row does not fill 12 columns renders a void beside a
    // panel — the "looks wrong is FAIL" failure this test exists to prevent.
    expect(tilesExactly(DEFAULT_LAYOUT)).toBe(true);
    expect(tilesExactly(PUBLIC_LAYOUT)).toBe(true);
    expect(rowsOf(DEFAULT_LAYOUT).map((r) => r.items.length)).toEqual([3, 2]);
    expect(rowsOf(PUBLIC_LAYOUT).map((r) => r.items.length)).toEqual([2, 1]);
  });

  it('only ever uses spans the design declares for that widget', () => {
    for (const layout of [DEFAULT_LAYOUT, PUBLIC_LAYOUT]) {
      for (const w of layout) {
        expect(WIDGETS[w.id], `${w.id} must be a registered widget`).toBeTruthy();
        expect(WIDGETS[w.id].spans, `${w.id} span ${w.span}`).toContain(w.span);
      }
    }
  });

  it('drops SERVICE and THROUGHPUT from the anonymous view, and only those', () => {
    const admin = layoutFor(true).map((w) => w.id);
    const anon = layoutFor(false).map((w) => w.id);
    expect(admin).toEqual(['health', 'identity', 'service', 'netmap', 'throughput']);
    expect(anon).toEqual(['health', 'identity', 'netmap']);
    expect(admin.filter((id) => !anon.includes(id))).toEqual(['service', 'throughput']);
  });

  it('hands back copies, so a caller cannot mutate the template', () => {
    layoutFor(true)[0].span = 99;
    expect(DEFAULT_LAYOUT[0].span).toBe(4);
  });

  it('registers no widget it cannot render in this wave', () => {
    // peersum / storage / activity are wave 2. A registry entry with no
    // renderer is a hole waiting for the first stale layout to find.
    expect(Object.keys(WIDGETS).sort()).toEqual(['health', 'identity', 'netmap', 'service', 'throughput']);
  });
});

describe('a measurement is printed, an absence is not', () => {
  it('formats bytes the way a disk states its own capacity', () => {
    expect(formatBytes(0)).toBe('0 B');
    expect(formatBytes(1240876)).toBe('1.2 MB');
    expect(formatBytes(4.8e9)).toBe('4.8 GB');
    expect(formatBytes(32e9)).toBe('32 GB');
  });

  it('returns nothing at all for a non-measurement', () => {
    // '' is what lets the widget OMIT the row instead of printing a dash.
    expect(formatBytes(null)).toBe('');
    expect(formatBytes(undefined)).toBe('');
    expect(formatBytes(-1)).toBe('');
    expect(formatBytes(Number.NaN)).toBe('');
    expect(formatRateMBs(null)).toBe('');
    expect(formatRateMBs(Number.NaN)).toBe('');
  });

  it('reads rate_in_bps as libp2p writes it — bytes per second', () => {
    expect(formatRateMBs(3_420_000)).toBe('3.42');
    expect(formatRateMBs(880_000)).toBe('0.88');
    expect(formatRateMBs(0)).toBe('0.00');
  });

  it('carries the START DATE once the node did not start today (IRIS C3)', () => {
    const now = Date.parse('2026-07-30T09:00:00Z');
    // Same UTC day: the time alone identifies the instant.
    expect(formatStartedUTC('2026-07-30T05:41:00Z', now)).toBe('05:41Z');
    // A different day: "05:41Z" beside "UPTIME 4d 02:11" would be a riddle.
    expect(formatStartedUTC('2026-07-26T05:41:00Z', now)).toBe('07-26 05:41Z');
    // Nothing honest to print -> nothing printed, so the cell is omitted.
    expect(formatStartedUTC('', now)).toBe('');
    expect(formatStartedUTC('not-a-date', now)).toBe('');
    expect(formatStartedUTC(null, now)).toBe('');
  });

  it('prints the service clock the way the design does', () => {
    expect(formatUptimeClock(4 * 86400 + 2 * 3600 + 11 * 60)).toBe('4d 02:11');
    expect(formatUptimeClock(2 * 3600 + 11 * 60)).toBe('02:11');
    expect(formatUptimeClock(41 * 60)).toBe('0:41');
    expect(formatUptimeClock(0)).toBe('0:00');
  });
});

describe('a null disk never becomes a zero-capacity bar', () => {
  it('draws a fraction only when both numbers are real', () => {
    expect(storageFraction(1e9, 4e9)).toBeCloseTo(0.25);
    expect(storageFraction(0, 4e9)).toBe(0);
  });

  it('refuses to draw when the node reported no disk', () => {
    // caps/nodestatus.go answers "disk": null when statfs is unavailable —
    // precisely so the UI cannot invent a 0-byte volume.
    expect(storageFraction(1e9, null)).toBeNull();
    expect(storageFraction(1e9, 0)).toBeNull();
    expect(storageFraction(1e9, undefined)).toBeNull();
    expect(storageFraction(null, 4e9)).toBeNull();
  });

  it('clamps a store larger than the probe reports rather than overflowing the bar', () => {
    expect(storageFraction(9e9, 4e9)).toBe(1);
  });
});

describe('the sparkline states the window it actually has', () => {
  const sample = (inBps, outBps = 0) => ({ rate_in_bps: inBps, rate_out_bps: outBps });

  it('takes the last 12 samples — the design\'s 60s window', () => {
    const history = Array.from({ length: 24 }, (_, i) => sample(i * 1000));
    const bars = sparkBars(history);
    expect(bars).toHaveLength(SPARK_SAMPLES);
    // The newest sample is the tallest here, so it is the 100% bar.
    expect(bars.at(-1).pct).toBe(100);
    expect(sparkSpanS(bars.length)).toBe(60);
    expect(BANDWIDTH_SAMPLE_MS).toBe(5000);
  });

  it('highlights the NEWEST bar, never index 8', () => {
    const bars = sparkBars(Array.from({ length: 12 }, () => sample(1000)));
    expect(bars.filter((b) => b.newest)).toHaveLength(1);
    expect(bars.at(-1).newest).toBe(true);
    expect(bars[8].newest).toBe(false);
  });

  it('draws only the bars it has, and labels the real span', () => {
    // A node up for 20 seconds has four samples. Drawing eight would be
    // drawing data it does not have; labelling it -60s would be a lie.
    const bars = sparkBars([sample(1000), sample(2000), sample(3000), sample(4000)]);
    expect(bars).toHaveLength(4);
    // Four 5-second buckets = 20s, not the design's -60s.
    expect(sparkSpanS(bars.length)).toBe(20);
    expect(sparkSpanS(0)).toBe(0);
    expect(sparkBars([])).toEqual([]);
  });

  it('keeps an idle window visible instead of an empty panel', () => {
    const bars = sparkBars(Array.from({ length: 12 }, () => sample(0, 0)));
    expect(bars.every((b) => b.pct === 3)).toBe(true);
  });

  it('scales to the peak in the window, counting both directions', () => {
    const bars = sparkBars([sample(1000, 1000), sample(0, 0), sample(2000, 2000)]);
    expect(bars.map((b) => b.pct)).toEqual([50, 3, 100]);
  });

  it('survives a malformed history without inventing bars', () => {
    expect(sparkBars(null)).toEqual([]);
    expect(sparkBars([{}, { rate_in_bps: 'x' }])).toHaveLength(2);
  });
});

describe('folding the runtime sources', () => {
  it('reports nothing privileged before the admin snapshot arrives', () => {
    const r = foldRuntime();
    expect(r.privileged).toBe(false);
    expect(r.rateInBps).toBeNull();
    expect(r.diskCapacityBytes).toBeNull();
    expect(r.history).toEqual([]);
    expect(r.autostartKnown).toBe(false);
  });

  it('states uptime and mode from the anonymous surface when there is no session', () => {
    const r = foldRuntime({ relay: { mode: 'full', uptime_seconds: 4039 } });
    expect(r.privileged).toBe(false);
    expect(r.mode).toBe('full');
    expect(r.uptimeS).toBe(4039);
  });

  it('NEVER states a store total without the admin snapshot (IRIS C1)', () => {
    // /api/v1/stats seeds total_bytes:0 and serves it on a store-read budget
    // miss (internal/api/coreapi.go:271-272), so "busy" and "empty" arrive as
    // the same bytes. It is not a source here, and an unprivileged fold has
    // NOTHING to say about storage.
    const r = foldRuntime({ relay: { mode: 'full', uptime_seconds: 4039 } });
    expect(r.storeBytes).toBeNull();
    expect(r.storeRecords).toBeNull();
    // Even if a caller hands the fold a stats-shaped object, it is ignored.
    expect(foldRuntime({ stats: { total_bytes: 0, total_records: 0 } }).storeBytes).toBeNull();
  });

  it('lets the admin snapshot supersede the public numbers', () => {
    const r = foldRuntime({
      relay: { mode: 'full', uptime_seconds: 10 },
      snapshot: {
        uptime_seconds: 4039,
        started_at: '2026-07-30T04:00:00Z',
        store: { total_bytes: 1240876, total_records: 332 },
        disk: { capacity_bytes: 84e9, available_bytes: 40e9 },
        service: { state: 'running', mode: 'full', autostart_known: false },
        bandwidth: { rate_in_bps: 3_420_000, rate_out_bps: 880_000, history: [{ rate_in_bps: 1 }] },
      },
    });
    expect(r.privileged).toBe(true);
    expect(r.uptimeS).toBe(4039);
    expect(r.storeBytes).toBe(1240876);
    expect(r.diskCapacityBytes).toBe(84e9);
    expect(r.serviceState).toBe('running');
    expect(r.history).toHaveLength(1);
  });

  it('carries autostart_known through as FALSE rather than assuming a boolean', () => {
    // The cell is omitted BECAUSE this is false, and it must come back on its
    // own the day a host can answer.
    expect(foldRuntime({ snapshot: { service: { autostart_known: false } } }).autostartKnown).toBe(false);
    expect(foldRuntime({ snapshot: { service: { autostart_known: true } } }).autostartKnown).toBe(true);
  });

  it('keeps a null disk null', () => {
    const r = foldRuntime({ snapshot: { store: { total_bytes: 5 }, disk: null, bandwidth: null } });
    expect(r.diskCapacityBytes).toBeNull();
    expect(r.rateInBps).toBeNull();
    expect(r.storeBytes).toBe(5);
  });
});

describe('the poller never touches the admin surface without a session', () => {
  const anonymousPaths = ['/api/v1/version', '/api/relay/status'];

  it('reads only anonymous endpoints until setAdmin(true)', async () => {
    const seen = [];
    const feed = createRuntimeFeed({
      onUpdate: () => {},
      fetchJson: async (path) => {
        seen.push(path);
        return {};
      },
    });
    vi.useFakeTimers();
    feed.start();
    await vi.advanceTimersByTimeAsync(1);
    feed.stop();
    vi.useRealTimers();

    expect(seen).not.toContain('/api/node/runtime');
    // /api/v1/stats is NOT read at all any more (IRIS C1).
    expect(seen).not.toContain('/api/v1/stats');
    expect(seen.every((p) => anonymousPaths.includes(p))).toBe(true);
  });

  it('reads the admin snapshot once told there is an Admin session', async () => {
    const seen = [];
    const feed = createRuntimeFeed({
      onUpdate: () => {},
      fetchJson: async (path) => {
        seen.push(path);
        return { service: { state: 'running' } };
      },
    });
    feed.setAdmin(true);
    await Promise.resolve();
    await Promise.resolve();
    expect(seen).toContain('/api/node/runtime');
  });

  it('drops the snapshot the moment the session ends', async () => {
    let latest = null;
    const feed = createRuntimeFeed({
      onUpdate: (r) => (latest = r),
      fetchJson: async () => ({ service: { state: 'running', mode: 'full' }, bandwidth: { rate_in_bps: 5, history: [] } }),
    });
    feed.setAdmin(true);
    // Let the immediate poll settle.
    for (let i = 0; i < 8; i += 1) await Promise.resolve();
    expect(latest?.privileged).toBe(true);

    feed.setAdmin(false);
    expect(latest?.privileged).toBe(false);
    expect(latest?.rateInBps).toBeNull();
  });

  it('keeps the last good value when a source fails', async () => {
    let calls = 0;
    let latest = null;
    const feed = createRuntimeFeed({
      onUpdate: (r) => (latest = r),
      fetchJson: async (path) => {
        calls += 1;
        if (path === '/api/relay/status' && calls > 3) throw new Error('node restarting');
        return { mode: 'full', uptime_seconds: 7, suite_version: '1.0.4' };
      },
    });
    feed.setAdmin(false);
    feed.start();
    for (let i = 0; i < 12; i += 1) await Promise.resolve();
    feed.stop();
    expect(latest?.mode).toBe('full');
  });
});

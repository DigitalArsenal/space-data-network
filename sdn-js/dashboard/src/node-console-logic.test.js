/*
 * The NODE dashboard's rules, as tests (tasks
 * sdn-dashboard-restore-template-widgets wave 1 +
 * sdn-dashboard-wave2-edit-layout wave 2).
 *
 * Every assertion here is one of the honesty rules from IRIS's ruling. They are
 * not "does it format nicely" checks — they are the locks that stop a future
 * change from fabricating a disk, dashing an unknown field, mislabelling a
 * sparkline window, or leaving a hole in the grid.
 */
import { describe, expect, it, vi } from 'vitest';
import { readFileSync } from 'node:fs';

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
import {
  DEFAULT_LAYOUT,
  LAYOUT_KEY,
  PRIVILEGED_WIDGETS,
  PUBLIC_LAYOUT,
  WIDGETS,
  addWidget,
  availableWidgets,
  cycleSpan,
  layoutFor,
  moveWidget,
  readLayout,
  removeWidget,
  renderLayout,
  resetLayout,
  rowsOf,
  sanitizeLayout,
  tilesExactly,
  writeLayout,
} from './node-layout.js';
import { ACTIVITY_KINDS, activityRow, formatActivityClock } from './runtime.js';

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

  it('is the export\'s full eight-widget registry, and nothing invented', () => {
    // WAVE 2 completes the registry: peersum / storage / activity arrive WITH
    // EDIT LAYOUT, because "the ADD menu is empty without them" (IRIS §2). The
    // list is the template's own (`SDN Console.dc.html:863-871`) — a NINTH id is
    // forbidden, since inventing a widget the design does not define is inventing
    // design (IRIS R6).
    expect(Object.keys(WIDGETS).sort()).toEqual([
      'activity', 'health', 'identity', 'netmap', 'peersum', 'service', 'storage', 'throughput',
    ]);
    // And every entry can actually be RENDERED: a registry id with no renderer is
    // a hole waiting for the first stale layout to find.
    const console = readFileSync(new URL('./NodeConsole.svelte', import.meta.url), 'utf8');
    for (const id of Object.keys(WIDGETS)) {
      expect(console, `${id} is registered but has no renderer`).toContain(`w.id === '${id}'`);
    }
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
    expect(r.autostart).toBe('');
    expect(r.canRestart).toBe(false);
    expect(r.canStop).toBe(false);
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

  it('takes AUTOSTART from the supervisor probe as systemd\'s own word', () => {
    // Wave 1 read `service.autostart_known` off the node_status_read snapshot,
    // which is a literal false there because THAT capability has no systemd
    // surface — so the cell could only ever become honest by fabricating a
    // boolean, which is what the hardcoded "ENABLED" did (IRIS §4 / R8). The
    // honest source is GET /api/node/service, and the value is a WORD: "static"
    // and "indirect" are neither enabled nor disabled, and a boolean would have
    // to lie about one of them.
    for (const word of ['enabled', 'disabled', 'static', 'indirect', 'masked']) {
      expect(foldRuntime({ supervisor: { autostart: word } }).autostart).toBe(word);
    }
    // No probe, or a probe that proved no supervisor: no cell.
    expect(foldRuntime({ snapshot: { service: { autostart_known: false } } }).autostart).toBe('');
    expect(foldRuntime({ supervisor: { supervisor: '', control_enabled: false } }).autostart).toBe('');
  });

  it('renders a lifecycle control ONLY from the host\'s own can_restart/can_stop', () => {
    // Fail-closed on the host (a proven supervisor AND the unit-level opt-in),
    // and the page adds no inference of its own: a supervisor that is detected but
    // not granted control yields NO buttons, not disabled ones (IRIS §5).
    const detectedNoGrant = foldRuntime({
      supervisor: { supervisor: 'systemd', unit: 'x.service', autostart: 'enabled', can_restart: false, can_stop: false },
    });
    expect(detectedNoGrant.supervisor).toBe('systemd');
    expect(detectedNoGrant.autostart).toBe('enabled');
    expect(detectedNoGrant.canRestart).toBe(false);
    expect(detectedNoGrant.canStop).toBe(false);

    const granted = foldRuntime({
      supervisor: { supervisor: 'systemd', unit: 'x.service', can_restart: true, can_stop: true, restart_policy: 'always' },
    });
    expect(granted.canRestart).toBe(true);
    expect(granted.canStop).toBe(true);
    // The policy is carried because it is what a STOP MEANS on this host.
    expect(granted.restartPolicy).toBe('always');
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

// ---------------------------------------------------------------------------
// WAVE 2 — EDIT LAYOUT. The design demonstrates these mutators; here they are
// RULES. Each one is a pure function over a layout, so the arrangement the
// operator sees is provably the arrangement that gets persisted.
// ---------------------------------------------------------------------------

/** A localStorage stand-in — the app injects globalThis.localStorage. */
function fakeStore(initial = {}) {
  const map = new Map(Object.entries(initial));
  return {
    getItem: (k) => (map.has(k) ? map.get(k) : null),
    setItem: (k, v) => map.set(k, v),
    seen: () => Object.fromEntries(map),
  };
}

describe('EDIT LAYOUT persists what it renders', () => {
  it('reads a saved layout back through the design\'s own key', () => {
    const store = fakeStore({ [LAYOUT_KEY]: JSON.stringify([{ id: 'identity', span: 6 }, { id: 'health', span: 6 }]) });
    expect(readLayout(store)).toEqual([{ id: 'identity', span: 6 }, { id: 'health', span: 6 }]);
  });

  it('falls back to the template default when a stored layout names a widget that no longer exists', () => {
    // The template's own rule (`:883`): one stale id and the WHOLE layout is
    // rejected. A partial repair would silently change an arrangement the
    // operator chose, and a stale id is exactly the shape a rename leaves behind.
    const store = fakeStore({ [LAYOUT_KEY]: JSON.stringify([{ id: 'health', span: 4 }, { id: 'conjunction', span: 8 }]) });
    expect(readLayout(store)).toEqual(DEFAULT_LAYOUT);
  });

  it('rejects a duplicate id — the renderer is a keyed each and two keys is a blank page', () => {
    expect(sanitizeLayout([{ id: 'health', span: 4 }, { id: 'health', span: 4 }])).toBeNull();
  });

  it('repairs an off-vocabulary span instead of throwing the layout away', () => {
    // 7 is not in health's `spans:[4,6]`: it breaks 12-column tiling and leaves
    // the cycle button with no index to advance from. The ARRANGEMENT is still the
    // operator's, so the span is snapped to the widget's default.
    expect(sanitizeLayout([{ id: 'health', span: 7 }])).toEqual([{ id: 'health', span: WIDGETS.health.def }]);
  });

  it('survives a storage that refuses to answer, or answers garbage', () => {
    const thrower = { getItem: () => { throw new Error('private mode'); }, setItem: () => { throw new Error('private mode'); } };
    expect(readLayout(thrower)).toEqual(DEFAULT_LAYOUT);
    expect(writeLayout(thrower, DEFAULT_LAYOUT)).toBe(false);
    expect(readLayout(fakeStore({ [LAYOUT_KEY]: 'not json' }))).toEqual(DEFAULT_LAYOUT);
    expect(readLayout(fakeStore({ [LAYOUT_KEY]: '[]' }))).toEqual(DEFAULT_LAYOUT);
  });

  it('moves a widget to the position it was dragged over, and never loses one', () => {
    const before = resetLayout();
    const after = moveWidget(before, 'throughput', 'health');
    expect(after.map((w) => w.id)).toEqual(['throughput', 'health', 'identity', 'service', 'netmap']);
    expect(after).toHaveLength(before.length);
    // Pure: the input is untouched, so a caller can render one and persist the other.
    expect(before.map((w) => w.id)).toEqual(['health', 'identity', 'service', 'netmap', 'throughput']);
  });

  it('ignores a drag onto itself or onto a widget that is not placed', () => {
    const layout = resetLayout();
    expect(moveWidget(layout, 'health', 'health')).toBe(layout);
    expect(moveWidget(layout, 'health', 'peersum')).toBe(layout);
    expect(moveWidget(layout, '', 'health')).toBe(layout);
  });

  it('cycles a span through the design\'s vocabulary and wraps', () => {
    let layout = [{ id: 'netmap', span: 6 }];
    for (const want of [8, 12, 6, 8]) {
      layout = cycleSpan(layout, 'netmap');
      expect(layout[0].span).toBe(want);
    }
  });

  it('adds only registered widgets, at their declared default span, once', () => {
    const layout = addWidget(resetLayout(), 'activity');
    expect(layout.at(-1)).toEqual({ id: 'activity', span: WIDGETS.activity.def });
    // A second add is a no-op, not a duplicate (which sanitizeLayout would then reject).
    expect(addWidget(layout, 'activity')).toBe(layout);
    expect(addWidget(layout, 'not-a-widget')).toBe(layout);
  });

  it('offers exactly the widgets that are not placed, and nothing else', () => {
    expect(availableWidgets(resetLayout()).map((w) => w.id).sort()).toEqual(['activity', 'peersum', 'storage']);
    // Titles come from the registry, so the ADD menu cannot name a widget
    // differently from its own panel.
    expect(availableWidgets(resetLayout()).find((w) => w.id === 'storage').title).toBe(WIDGETS.storage.title);
    const everything = Object.keys(WIDGETS).reduce((l, id) => addWidget(l, id), []);
    expect(availableWidgets(everything)).toEqual([]);
  });

  it('removes a widget without touching the others', () => {
    const after = removeWidget(resetLayout(), 'service');
    expect(after.map((w) => w.id)).toEqual(['health', 'identity', 'netmap', 'throughput']);
  });

  it('gives an anonymous viewer the public layout no matter what is stored', () => {
    // renderLayout COMPUTES the anonymous view, so a stored arrangement would be
    // discarded on render — which is why the edit chrome is Admin-only (IRIS R3).
    const stored = [{ id: 'activity', span: 12 }];
    expect(renderLayout(false, stored)).toEqual(PUBLIC_LAYOUT);
    expect(renderLayout(true, stored)).toEqual(stored);
    // Signing out must not destroy what the operator saved.
    expect(stored).toEqual([{ id: 'activity', span: 12 }]);
  });

  it('every widget the public layout offers is renderable without a session', () => {
    for (const w of PUBLIC_LAYOUT) {
      expect(PRIVILEGED_WIDGETS.has(w.id), `${w.id} needs the admin snapshot`).toBe(false);
    }
    // And the four that DO need it are named, so a future widget cannot quietly
    // join the anonymous view and render blank.
    expect([...PRIVILEGED_WIDGETS].sort()).toEqual(['activity', 'service', 'storage', 'throughput']);
  });

  it('an edited layout still tiles the 12-column grid when the operator uses the offered spans', () => {
    // health 4 -> 6, identity 4 -> 6 fills row one exactly; netmap 8 + throughput 4
    // fills row two. The point is that the vocabulary MAKES tiling reachable.
    let layout = removeWidget(resetLayout(), 'service');
    layout = cycleSpan(cycleSpan(layout, 'health'), 'identity');
    expect(tilesExactly(layout)).toBe(true);
    expect(rowsOf(layout)).toHaveLength(2);
  });
});

describe('the activity ring reads as sentences, not as machine kinds', () => {
  it('prints the design\'s clock from an RFC3339 stamp', () => {
    expect(formatActivityClock('2026-07-30T12:04:22Z')).toBe('12:04:22');
    expect(formatActivityClock('')).toBe('');
    expect(formatActivityClock('not a date')).toBe('');
  });

  it('names every kind the host actually emits', () => {
    // The taps are internal/node/epm_exchange_notifee.go, internal/node/node.go
    // and internal/api/channels.go. A kind missing here would render as a machine
    // token in front of the operator.
    for (const kind of ['peer_connected', 'peer_disconnected', 'pnm_publication', 'record_stored', 'grant_issued']) {
      expect(ACTIVITY_KINDS[kind], kind).toBeTruthy();
    }
  });

  it('renders an unmapped kind rather than dropping the event', () => {
    // A new tap must be VISIBLE the day it lands — silently swallowing it is how
    // a host stops telling an operator what it is doing.
    const row = activityRow({ ts: '2026-07-30T12:00:00Z', kind: 'channel_revoked', detail: 'alpha' });
    expect(row.text).toBe('channel_revoked · alpha');
    expect(row.token).toBe('textDim');
  });

  it('shortens the peer id and omits an absent one', () => {
    const withPeer = activityRow(
      { ts: '2026-07-30T12:00:00Z', kind: 'peer_connected', peer_id: '12D3KooWLongPeerIdentifier', detail: '' },
      (id) => `${id.slice(0, 6)}…`
    );
    expect(withPeer.text).toBe('Peer connected · 12D3Ko…');
    const withoutPeer = activityRow({ ts: '2026-07-30T12:00:00Z', kind: 'pnm_publication', detail: 'OMM.fbs' });
    expect(withoutPeer.text).toBe('Publication received · OMM.fbs');
  });
});

describe('the first poll after signing in reads everything', () => {
  it('resets the sampling phase, so no admin source is a minute late', async () => {
    // Found from a screenshot of the built page: the AUTOSTART cell was absent
    // and the ACTIVITY LOG said "Reading the activity ring…" for a full minute
    // after an admin session appeared, because the slow sources sample on
    // `tick % N === 0` and setAdmin landed on tick 1. An absent cell that means
    // "not read yet" is indistinguishable from one that means "this host cannot
    // answer" — which is the whole distinction this wave exists to make.
    const seen = [];
    const feed = createRuntimeFeed({
      onUpdate: () => {},
      fetchJson: (path) => {
        seen.push(path);
        return Promise.resolve({});
      },
    });
    feed.start();
    // Three anonymous polls, so the phase is deliberately NOT on a boundary.
    await Promise.resolve();
    await Promise.resolve();
    seen.length = 0;
    feed.setAdmin(true);
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    feed.stop();

    const asked = seen.join(' ');
    for (const path of ['/api/node/runtime', '/api/node/activity', '/api/node/service']) {
      expect(asked, `${path} must be read on the FIRST admin poll`).toContain(path);
    }
  });
});

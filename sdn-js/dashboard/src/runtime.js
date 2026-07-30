/*
 * The NODE dashboard's runtime facts — the data seam behind NodeConsole.svelte's
 * NODE HEALTH, SERVICE and NETWORK THROUGHPUT widgets (graph task
 * sdn-dashboard-restore-template-widgets, IRIS ruling 2026-07-30 §2 wave 1).
 *
 * THREE SOURCES, ALL REAL, NONE INVENTED
 * --------------------------------------
 *   · GET /api/v1/version      (anonymous)  suite + standards version
 *   · GET /api/relay/status    (anonymous)  mode, uptime_seconds
 *   · GET /api/node/runtime    (ADMIN)      the whole node_status_read snapshot:
 *                                           uptime, store, disk, service,
 *                                           bandwidth totals + the 5s history
 *                                           ring (sdn-server/cmd/
 *                                           spacedatanetwork/node_runtime_api.go)
 *
 * `/api/v1/stats` IS NOT ONE OF THEM, and that is a correction, not an omission
 * (IRIS condition C1). It looks like the obvious anonymous source for the store
 * totals, but `internal/api/coreapi.go:271-272` seeds the response with literal
 * `"total_records": 0, "total_bytes": 0` and serves those when the store-read
 * budget is missed — so "the store is busy" and "the store is empty" arrive as
 * the same bytes. Reading it made the STORAGE row print `0 B` on one render and
 * `80 kB` on the next, for the same node. A fabricated zero is exactly what
 * constraint (c) forbids, so the ONLY store total this file will state is the
 * admin snapshot's, which is measured or absent.
 *
 * §16.5's rule is absolute here: `/api/node/runtime` is NEVER called without a
 * confirmed Admin session. A 403 in the console on every public page load is
 * exactly the noise that trains operators to ignore real errors.
 *
 * WHAT IS DELIBERATELY ABSENT
 *   · autostart — the node reports `service.autostart_known: false` because it
 *     has no surface into systemd/desktop autostart. There is no field to read,
 *     so the widget omits the cell (IRIS §4: "not 'ENABLED', and not 'UNKNOWN'
 *     either — a dash in a data slot still asserts the field is meaningful").
 *   · RESTART / STOP / CHECK — no lifecycle endpoint exists anywhere in the node
 *     and creating one is a new host capability, STOPPED FOR THE OWNER. Nothing
 *     in this file so much as names them.
 */

import { apiFetch } from './api.js';

/**
 * The node samples its bandwidth counter every 5s into a 24-slot ring
 * (nodeStatusBandwidthSampleInterval / nodeStatusBandwidthHistoryCapacity,
 * internal/node/node.go). Polling at the same cadence means one new bar per
 * poll — faster would re-render identical data, slower would skip bars.
 */
export const RUNTIME_POLL_MS = 5000;
export const BANDWIDTH_SAMPLE_MS = 5000;
/** The design's sparkline is a 60s window: 12 samples x 5s (IRIS constraint (d)). */
export const SPARK_SAMPLES = 12;

/** Anonymous facts change far more slowly than the bandwidth ring. */
const VERSION_EVERY = 1; // once — a process cannot change its own build
const RELAY_EVERY = 3; // 15s
/**
 * The activity ring (ACTIVITY LOG widget, wave 2). Events arrive when peers
 * connect, records land and grants issue — minutes apart on a quiet node, not
 * seconds — so re-reading 256 entries every 5s would be 12x the traffic for the
 * same rows. 15s is the same cadence the anonymous facts use.
 */
const ACTIVITY_EVERY = 3; // 15s
/**
 * The supervisor probe (`GET /api/node/service`). A unit's enablement and
 * restart policy change only when an operator changes them, so this is the
 * slowest source on the timer — but it is re-read rather than read once, because
 * `systemctl enable` on the box must reach the AUTOSTART cell without a reload.
 */
const SERVICE_EVERY = 12; // 60s
/** The rows the design's ACTIVITY LOG shows per page (`SDN Console.dc.html:875-881`). */
export const ACTIVITY_ROWS = 5;
/**
 * How many events to hold. The widget shows ACTIVITY_ROWS at a time and
 * PAGINATES (grammar law L4 — widgets never scroll), so it needs more than one
 * page in hand: five pages of five is a couple of minutes of a busy node and
 * still a fraction of the ring's 256.
 */
export const ACTIVITY_FETCH = 25;

/**
 * SI byte formatting, matching how a disk states its own capacity ("32 GB").
 * Returns '' for a value that is not a real measurement, so a caller can omit
 * the row rather than print a fabricated zero.
 */
export function formatBytes(bytes) {
  if (typeof bytes !== 'number' || !Number.isFinite(bytes) || bytes < 0) return '';
  if (bytes === 0) return '0 B';
  const units = ['B', 'kB', 'MB', 'GB', 'TB', 'PB'];
  let i = 0;
  let v = bytes;
  while (v >= 1000 && i < units.length - 1) {
    v /= 1000;
    i += 1;
  }
  // Two significant decimals under 10, one under 100, none above — the same
  // shape as the design's "4.8 GB" / "32 GB".
  const digits = i === 0 ? 0 : v < 10 ? 1 : 0;
  return `${v.toFixed(digits)} ${units[i]}`;
}

/**
 * libp2p's flow meter counts BYTES per second (go-libp2p metrics.Stats.RateIn),
 * so `rate_in_bps` is bytes/s and MB/s is a plain 1e6 divide. Returns the number
 * only — the widget owns the "MB/s ↓" label, exactly as the design splits them.
 */
export function formatRateMBs(bytesPerSecond) {
  if (typeof bytesPerSecond !== 'number' || !Number.isFinite(bytesPerSecond) || bytesPerSecond < 0) return '';
  return (bytesPerSecond / 1e6).toFixed(2);
}

/** Seconds → the design's SERVICE clock: "4d 02:11", else "02:11", else "0:41". */
export function formatUptimeClock(seconds) {
  const s = Math.max(0, Math.floor(seconds || 0));
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  const pad = (n) => String(n).padStart(2, '0');
  if (d) return `${d}d ${pad(h)}:${pad(m)}`;
  if (h) return `${pad(h)}:${pad(m)}`;
  return `0:${pad(m)}`;
}

/**
 * `started_at` (RFC3339 UTC) on the UTC clock: "05:41Z" for a node that started
 * today, "07-26 05:41Z" for one that did not.
 *
 * The date is not decoration (IRIS condition C3). A bare "05:41Z" beside
 * "UPTIME 4d 02:11" reads as a contradiction, and once uptime crosses a day the
 * time alone no longer identifies an instant. Returns '' for anything that is
 * not a real timestamp, so the cell can be omitted.
 */
export function formatStartedUTC(iso, nowMs = Date.now()) {
  const text = String(iso ?? '').trim();
  if (!text) return '';
  const d = new Date(text);
  if (Number.isNaN(d.getTime())) return '';
  const p = (n) => String(n).padStart(2, '0');
  const hhmm = `${p(d.getUTCHours())}:${p(d.getUTCMinutes())}Z`;
  const today = new Date(nowMs);
  const sameDay =
    d.getUTCFullYear() === today.getUTCFullYear() &&
    d.getUTCMonth() === today.getUTCMonth() &&
    d.getUTCDate() === today.getUTCDate();
  return sameDay ? hhmm : `${p(d.getUTCMonth() + 1)}-${p(d.getUTCDate())} ${hhmm}`;
}

/**
 * The last N samples of the bandwidth ring as bar heights.
 *
 * Heights are relative to the tallest bar IN THE WINDOW — the design has no
 * axis, so the sparkline is a shape, not a scale, and an absolute ceiling would
 * flatten every bar on a quiet node into nothing.
 *
 * The newest bar is the highlighted one. The design export highlights index 8,
 * which is fixture styling — index 8 of a live ring means nothing (IRIS
 * constraint (d): "highlight the newest bar or none, never index 8").
 *
 * Fewer than N samples returns fewer bars: a node up for 20 seconds has four
 * samples and drawing eight would be drawing data it does not have. `spanS` is
 * the real span the bars cover, so the axis can label itself honestly instead of
 * always claiming −60s.
 */
export function sparkBars(history, want = SPARK_SAMPLES) {
  const samples = Array.isArray(history) ? history.slice(-Math.max(1, want)) : [];
  const rates = samples.map((s) => {
    const inBps = Number(s?.rate_in_bps);
    const outBps = Number(s?.rate_out_bps);
    return (Number.isFinite(inBps) ? inBps : 0) + (Number.isFinite(outBps) ? outBps : 0);
  });
  const peak = Math.max(0, ...rates);
  const last = rates.length - 1;
  return rates.map((rate, i) => ({
    // A floor of 3% keeps an idle window readable as "twelve quiet samples"
    // rather than as an empty panel with no bars at all.
    pct: peak > 0 ? Math.max(3, Math.round((rate / peak) * 100)) : 3,
    newest: i === last,
  }));
}

/**
 * The real time span a sparkline covers, in seconds.
 *
 * Each bar is a 5-second BUCKET — the rate measured over the interval ending at
 * that sample — so N bars cover N x 5s, and the design's twelve bars are exactly
 * its "−60s" label. A node with four samples covers 20s and says so.
 */
export function sparkSpanS(barCount, sampleMs = BANDWIDTH_SAMPLE_MS) {
  return barCount > 0 ? Math.round((barCount * sampleMs) / 1000) : 0;
}

/**
 * `ts` (RFC3339 UTC) as the design's ACTIVITY LOG clock: "12:04:22" (`:876`).
 * Returns '' for anything that is not a real timestamp, so a row can print its
 * text without a fabricated time beside it.
 */
export function formatActivityClock(iso) {
  const text = String(iso ?? '').trim();
  if (!text) return '';
  const d = new Date(text);
  if (Number.isNaN(d.getTime())) return '';
  const p = (n) => String(n).padStart(2, '0');
  return `${p(d.getUTCHours())}:${p(d.getUTCMinutes())}:${p(d.getUTCSeconds())}`;
}

/**
 * The activity ring's event KINDS, rendered as the sentence the design writes
 * ("Channel mpe-screening-alpha · grant accepted") plus its colour token.
 *
 * The host emits machine kinds (`peer_connected`, `record_stored`,
 * `pnm_publication`, `grant_issued`, `peer_disconnected` —
 * internal/modulert/caps/nodeactivity.go) and a `detail` that is already
 * PII-free by that file's own contract. This map is the ONLY place the wording
 * lives, and an unmapped kind renders its own machine name rather than being
 * dropped: a new tap must be visible the day it lands, not silently swallowed.
 */
export const ACTIVITY_KINDS = {
  peer_connected: { label: 'Peer connected', token: 'green' },
  peer_disconnected: { label: 'Peer disconnected', token: 'textMuted' },
  pnm_publication: { label: 'Publication received', token: 'ice' },
  record_stored: { label: 'Record stored', token: 'cyan' },
  grant_issued: { label: 'Channel grant issued', token: 'green' },
};

/**
 * One activity event as the row the widget prints: a clock, a colour token and
 * one sentence. `peer_id` is SHORTENED — the ring carries full 52-character peer
 * ids and the design's row is one line.
 */
export function activityRow(event, shorten = (id) => id) {
  const kind = String(event?.kind ?? '').trim();
  const known = ACTIVITY_KINDS[kind];
  const subject = String(event?.peer_id ?? '').trim();
  const detail = String(event?.detail ?? '').trim();
  const parts = [known ? known.label : kind || 'Event'];
  if (subject) parts.push(shorten(subject));
  if (detail) parts.push(detail);
  return {
    clock: formatActivityClock(event?.ts),
    token: known ? known.token : 'textDim',
    text: parts.join(' · '),
  };
}

/**
 * used/capacity as a 0..1 bar fraction, or null when there is nothing honest to
 * draw. A null `disk` (statfs unavailable) must NOT become a zero-capacity bar
 * — the node reports null precisely so the UI does not fabricate one.
 */
export function storageFraction(usedBytes, capacityBytes) {
  if (typeof usedBytes !== 'number' || !Number.isFinite(usedBytes) || usedBytes < 0) return null;
  if (typeof capacityBytes !== 'number' || !Number.isFinite(capacityBytes) || capacityBytes <= 0) return null;
  return Math.max(0, Math.min(1, usedBytes / capacityBytes));
}

/**
 * Fold the four sources into the flat shape the widgets read. Pure — the poller
 * below is the only thing that touches the network, which is what makes every
 * rule above testable.
 */
export function foldRuntime({ version = null, relay = null, snapshot = null, activity = null, supervisor = null } = {}) {
  const service = snapshot?.service ?? null;
  const bandwidth = snapshot?.bandwidth ?? null;
  const disk = snapshot?.disk ?? null;
  const store = snapshot?.store ?? null;

  // Measured or absent — never a budget-miss default (see C1 above).
  const storeBytes = Number.isFinite(Number(store?.total_bytes)) ? Number(store.total_bytes) : null;
  const storeRecords = Number.isFinite(Number(store?.total_records)) ? Number(store.total_records) : null;

  return {
    /** True once the ADMIN snapshot has actually been read. */
    privileged: Boolean(snapshot),
    suiteVersion: (version?.suite_version ?? '').trim(),
    standardsVersion: (version?.standards_version ?? '').trim(),
    // `mode` is a real configured value on both surfaces; the snapshot's copy
    // wins only because it is read in the same instant as everything else.
    mode: (service?.mode ?? relay?.mode ?? '').trim(),
    serviceState: (service?.state ?? '').trim(),
    // Uptime is the node's own count either way. relay/status carries it
    // anonymously, so SERVICE can state it before there is a session.
    uptimeS: Number.isFinite(Number(snapshot?.uptime_seconds))
      ? Number(snapshot.uptime_seconds)
      : Number.isFinite(Number(relay?.uptime_seconds))
        ? Number(relay.uptime_seconds)
        : null,
    startedAt: (snapshot?.started_at ?? '').trim(),
    /**
     * AUTOSTART, from the ONE surface that can honestly answer it: the
     * supervisor probe (GET /api/node/service -> internal/hostsvc, reading
     * systemd's UnitFileState). Wave 1 rendered a HARDCODED "ENABLED" behind
     * `service.autostart_known`, which the node_status_read capability reports as
     * literal false because IT has no systemd surface — so the cell could never
     * become honest by flipping that flag, only by fabricating a boolean (IRIS
     * §4 / R8, hermes-dash4's blocking condition).
     *
     * The value is systemd's own WORD ("enabled" / "disabled" / "static" /
     * "indirect" / "masked"), never a boolean: "static" and "indirect" are
     * neither, and flattening them would be the same lie in a new shape. Empty
     * string = no supervisor was proven, and the cell is ABSENT.
     */
    autostart: (supervisor?.autostart ?? '').trim(),
    /** The supervisor's name ("systemd"), or '' when none was proven. */
    supervisor: (supervisor?.supervisor ?? '').trim(),
    /** The unit that owns this process, as resolved from its own cgroup. */
    unit: (supervisor?.unit ?? '').trim(),
    /** systemd's Restart= policy — what a STOP would actually mean on this host. */
    restartPolicy: (supervisor?.restart_policy ?? '').trim(),
    /**
     * The ONLY fields a lifecycle control may be rendered from. Both are
     * fail-closed on the host side (a proven supervisor AND the unit-level
     * opt-in), and absent/false here means the buttons are NOT DRAWN — never
     * drawn disabled (IRIS §5: a greyed button advertises a capability the node
     * does not have).
     */
    canRestart: Boolean(supervisor?.can_restart),
    canStop: Boolean(supervisor?.can_stop),
    storeBytes,
    storeRecords,
    diskCapacityBytes: Number.isFinite(Number(disk?.capacity_bytes)) ? Number(disk.capacity_bytes) : null,
    diskAvailableBytes: Number.isFinite(Number(disk?.available_bytes)) ? Number(disk.available_bytes) : null,
    rateInBps: Number.isFinite(Number(bandwidth?.rate_in_bps)) ? Number(bandwidth.rate_in_bps) : null,
    rateOutBps: Number.isFinite(Number(bandwidth?.rate_out_bps)) ? Number(bandwidth.rate_out_bps) : null,
    history: Array.isArray(bandwidth?.history) ? bandwidth.history : [],
    /**
     * The activity ring, NEWEST FIRST as the host returns it. An empty array is
     * an honest empty ring — the design's five fixture rows
     * ("Schema sync · OMM updated to 9,120 rows") describe a node that is not
     * this one and ship nowhere.
     */
    activity: Array.isArray(activity?.events) ? activity.events : [],
    /**
     * True once the ring has actually ANSWERED. `activity: []` alone cannot
     * distinguish "this node has done nothing yet" from "the widget has not read
     * the ring yet", and those are different sentences on screen.
     */
    activityRead: Boolean(activity),
  };
}

/**
 * Poll the runtime sources on one timer.
 *
 * `setAdmin(true)` is what unlocks the admin snapshot, and it is called ONLY
 * from a confirmed Admin session. Turning it back off drops the snapshot
 * immediately, so signing out cannot leave privileged numbers on screen.
 *
 * Every fetch fails soft: a source that errors keeps its previous value and the
 * page keeps rendering the facts it does have. `fetchJson` is injectable so the
 * unit tests never touch the network.
 */
export function createRuntimeFeed({ onUpdate, fetchJson = (path) => apiFetch(path) } = {}) {
  let admin = false;
  let tick = 0;
  let timer = null;
  let stopped = false;
  const raw = { version: null, relay: null, snapshot: null, activity: null, supervisor: null };

  const emit = () => onUpdate?.(foldRuntime(raw));

  const read = async (key, path) => {
    try {
      raw[key] = await fetchJson(path);
    } catch {
      /* keep the last good value — a blip is not news */
    }
  };

  async function poll() {
    const n = tick;
    tick += 1;
    const jobs = [];
    if (n % VERSION_EVERY === 0 && !raw.version) jobs.push(read('version', '/api/v1/version'));
    if (n % RELAY_EVERY === 0) jobs.push(read('relay', '/api/relay/status'));
    if (admin) jobs.push(read('snapshot', '/api/node/runtime'));
    // The ring is Admin-only for the same reason the snapshot is (§16.5): it
    // names the peers this host talks to. Never called without a session.
    if (admin && n % ACTIVITY_EVERY === 0) jobs.push(read('activity', `/api/node/activity?limit=${ACTIVITY_FETCH}`));
    if (admin && n % SERVICE_EVERY === 0) jobs.push(read('supervisor', '/api/node/service'));
    await Promise.all(jobs);
    if (!stopped) emit();
  }

  return {
    /** Enable/disable the ADMIN snapshot. Idempotent. */
    setAdmin(next) {
      const on = Boolean(next);
      if (on === admin) return;
      admin = on;
      if (!admin) {
        raw.snapshot = null;
        // The ring goes with the snapshot: peer ids read under a session must
        // not survive it.
        raw.activity = null;
        // And so does the supervisor state — a signed-out page must not keep
        // claiming to know this host's unit, and must not keep can_restart true
        // for a control it may no longer invoke.
        raw.supervisor = null;
        emit();
        return;
      }
      poll();
    },
    start() {
      if (timer) return;
      stopped = false;
      poll();
      timer = setInterval(poll, RUNTIME_POLL_MS);
    },
    stop() {
      stopped = true;
      if (timer) clearInterval(timer);
      timer = null;
    },
  };
}

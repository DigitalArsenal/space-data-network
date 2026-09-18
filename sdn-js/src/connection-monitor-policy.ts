/**
 * Connection-monitor policy — why an sdn-js node must not abort its own
 * connections on a heartbeat that merely arrived late.
 *
 * THE DEFECT THIS EXISTS FOR
 * (`sdn-delivery-client-aborts-its-own-dial-under-slow-load`, P1).
 *
 * A browser page loading eleven closed RF modules over the module-delivery
 * protocol lost exactly one delivery per ~22 cold loads with
 * `AbortError: Read aborted`, while the provider's audit for the same window
 * recorded 23 of 23 challenges requested, issued and GRANTED, zero refusals and
 * zero aborts. The node answered everything. The client killed its own dial.
 *
 * MEASURED (20 cold loads under 6x CPU throttling, with `AbortSignal.timeout`
 * and `AbortController` shimmed at page start to record each timer's creation
 * stack and firing): in the failing runs, and only in those, a 2000 ms signal
 * created by `AdaptiveTimeout.getTimeoutSignal` fires; ~2 ms later every
 * `AbstractStream` controller on that connection fires, then the muxer's. The
 * connection carrying all in-flight deliveries is torn down, and each dying
 * stream's multistream-select read rejects with `Read aborted`.
 *
 * That timer belongs to libp2p's own `ConnectionMonitor`, which pings EVERY
 * connection every `pingInterval` (default 10 s) by opening a stream on that
 * same connection, and aborts the whole connection when the ping fails.
 *
 * On libp2p 1.9.4 the deadline was a FIXED 2000 ms: it came from an
 * `AdaptiveTimeout` whose moving average was never fed, because
 * `ConnectionMonitor` never called `cleanUp()`, so
 * `Math.max(round(0 * 1.2), minTimeout)` collapsed to `DEFAULT_MIN_TIMEOUT`
 * (2000 ms). `abortConnectionOnPingFailure` was declared in the .d.ts and never
 * read in the .js, so the abort was unconditional and the flag was inert.
 *
 * A page that starts a globe, compiles WASM and decrypts module payloads runs
 * 2.6–3.0 s long tasks as a matter of course. A 2000 ms deadline cannot survive
 * one, and the failure mode is not "the ping is reported late" but "everything
 * in flight on that connection dies".
 *
 * WHAT CHANGED IN libp2p 3.3.11 (verified against the installed bytes by
 * connection-monitor-policy.test.ts, which fails if any of it stops holding):
 *
 * 1. `abortConnectionOnPingFailure` IS read now, defaulting to `true`
 *    (`connection-monitor.js`), so the abort is still the default behaviour —
 *    the net effect on an sdn-js node is unchanged — but the flag is now a real
 *    lever rather than a lie.
 * 2. `cleanUp(signal)` IS called now, so the moving average really does adapt
 *    and the deadline is `max(movingAverage * timeoutMultiplier, minTimeout)`
 *    clamped to `maxTimeout`.
 * 3. `DEFAULT_MIN_TIMEOUT` rose from 2000 ms to 5000 ms, with a new
 *    `DEFAULT_MAX_TIMEOUT` of 60 s.
 *
 * None of that rescues a browser: 5000 ms is still well under the 2.6–3.0 s
 * stalls measured back to back, and an adaptive average computed from healthy
 * pings makes the deadline SHORTER for a fast peer, not longer for a busy tab.
 *
 * THE POLICY. Liveness is worth keeping — a relay can die silently and leave a
 * half-open circuit — so the monitor stays ON, with a deadline an order of
 * magnitude above the worst measured stall and a slower cadence. A missed
 * heartbeat then means the peer really is gone, not that the tab was busy. The
 * deadline is still the lever that separates "slow" from "dead": switching
 * `abortConnectionOnPingFailure` off instead would silence a genuinely dead
 * relay rather than tolerate a busy one.
 *
 * Callers can override per node (`connectionMonitor` in {@link SDNConfig}),
 * including `false` to switch the monitor off entirely. An explicit override is
 * honoured verbatim — a deployment that knows its own latency budget outranks
 * this default.
 */

/**
 * How often to ping each connection, in ms.
 *
 * Three times slower than libp2p's 10 s default: each ping is an opportunity
 * for this failure, the connections in question are dialled for delivery and
 * live seconds-to-minutes, and nothing in sdn-js consumes `conn.rtt`.
 */
export const SDN_CONNECTION_MONITOR_PING_INTERVAL_MS = 30_000;

/**
 * How long a heartbeat may take before the connection is judged dead, in ms.
 *
 * 6x libp2p 3's 5000 ms floor (15x the 2000 ms floor of the 1.x this package
 * used to build on) and 10x the worst main-thread stall measured on the RF
 * gallery under 6x throttling. A truly dead connection is still reaped within
 * ~one ping interval plus this deadline.
 */
export const SDN_CONNECTION_MONITOR_TIMEOUT_MS = 30_000;

/**
 * libp2p's own floor, for the comparison the policy is justified against.
 * 2000 ms on libp2p 1.9.4, 5000 ms on 3.x — both under one measured stall.
 */
export const LIBP2P_DEFAULT_MIN_TIMEOUT_MS = 5_000;

/**
 * The subset of libp2p's `ConnectionMonitorInit` this policy sets. Structural,
 * not imported: `libp2p` does not export the type from its package root, and
 * sdn-js must not reach into another package's `dist/` path.
 */
export interface SdnConnectionMonitorInit {
  /** Whether the monitor runs at all. libp2p reads this one (`libp2p.js`). */
  enabled?: boolean;
  /** Heartbeat cadence in ms. */
  pingInterval?: number;
  /** `AdaptiveTimeout` init for the heartbeat deadline. */
  pingTimeout?: {
    interval?: number;
    minTimeout?: number;
    timeoutMultiplier?: number;
    failureMultiplier?: number;
  };
  /** Ping protocol prefix, passed through untouched. */
  protocolPrefix?: string;
  /**
   * Whether a failed heartbeat aborts the connection. libp2p 1.9.4 declared
   * this and never read it; 3.x reads it and defaults it to `true`, which is
   * the behaviour this policy wants, so it is left unset.
   */
  abortConnectionOnPingFailure?: boolean;
}

/**
 * What a caller may pass as `connectionMonitor`:
 * - `undefined` / `true` — the sdn-js default policy below;
 * - `false` — no monitor at all;
 * - an object — merged OVER the default policy, field by field.
 */
export type SdnConnectionMonitorConfig =
  | boolean
  | SdnConnectionMonitorInit
  | undefined;

/**
 * The default policy, frozen so a consumer that reads it cannot mutate the
 * defaults for every other node in the page.
 */
export const SDN_CONNECTION_MONITOR_DEFAULTS: Readonly<SdnConnectionMonitorInit> =
  Object.freeze({
    enabled: true,
    pingInterval: SDN_CONNECTION_MONITOR_PING_INTERVAL_MS,
    pingTimeout: Object.freeze({
      // `minTimeout` is the floor, and on libp2p 3.x - which really does feed
      // the moving average - it is what holds. The effective deadline is
      // `max(movingAverage * timeoutMultiplier, minTimeout)`, so an average
      // measured from healthy pings can only ever RAISE it above this floor,
      // never below; multipliers >= 1 keep that true if upstream changes how
      // the average is fed.
      interval: SDN_CONNECTION_MONITOR_TIMEOUT_MS,
      minTimeout: SDN_CONNECTION_MONITOR_TIMEOUT_MS,
      timeoutMultiplier: 2,
      failureMultiplier: 2,
    }),
  });

/**
 * Resolve the libp2p `connectionMonitor` init for a node.
 *
 * @param config the node's `connectionMonitor` setting, if any.
 * @returns an init object safe to hand to `createLibp2p`.
 */
export function resolveConnectionMonitorInit(
  config?: SdnConnectionMonitorConfig,
): SdnConnectionMonitorInit {
  if (config === false) {
    return { enabled: false };
  }
  if (config === undefined || config === true) {
    return {
      ...SDN_CONNECTION_MONITOR_DEFAULTS,
      pingTimeout: { ...SDN_CONNECTION_MONITOR_DEFAULTS.pingTimeout },
    };
  }
  // An explicit object is a deliberate override: merge it over the policy so a
  // caller can change one field (say `pingInterval`) without silently
  // reinstating libp2p's own floor for the others.
  const pingTimeout = {
    ...SDN_CONNECTION_MONITOR_DEFAULTS.pingTimeout,
    ...(config.pingTimeout ?? {}),
  };
  return {
    ...SDN_CONNECTION_MONITOR_DEFAULTS,
    ...config,
    pingTimeout,
  };
}

/**
 * One-line description of the resolved policy, for node banners and debug
 * output. A silent transport policy is how this defect survived twenty-two
 * measured page loads.
 */
export function describeConnectionMonitorPolicy(
  init: SdnConnectionMonitorInit,
): string {
  if (init.enabled === false) {
    return "connection monitor: off (no heartbeat, no self-abort)";
  }
  const deadline = init.pingTimeout?.minTimeout ?? 0;
  return (
    `connection monitor: ping every ${Math.round((init.pingInterval ?? 0) / 1000)}s, ` +
    `abort after ${Math.round(deadline / 1000)}s without a reply`
  );
}

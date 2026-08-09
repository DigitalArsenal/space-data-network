/**
 * The connection-monitor policy, and the third-party behaviour it exists to
 * neutralise (`sdn-delivery-client-aborts-its-own-dial-under-slow-load`).
 *
 * Two halves, deliberately:
 *
 * 1. the resolver and its wiring into `SDNNode` / `createHeliaSDNNode`, so the
 *    policy cannot be silently dropped from `createLibp2p`;
 * 2. a CONTRACT check against the installed libp2p's own shipped source. The
 *    policy is a set of numbers whose justification lives entirely in that
 *    file: if a future libp2p starts honouring `abortConnectionOnPingFailure`,
 *    or stops aborting the connection outright, these numbers should be
 *    revisited rather than inherited. A failing test is how we find out.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";
import { existsSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import {
  SDN_CONNECTION_MONITOR_DEFAULTS,
  SDN_CONNECTION_MONITOR_PING_INTERVAL_MS,
  SDN_CONNECTION_MONITOR_TIMEOUT_MS,
  describeConnectionMonitorPolicy,
  resolveConnectionMonitorInit,
} from "./connection-monitor-policy";

/** libp2p's own floor, the one this policy exists to climb off. */
const LIBP2P_DEFAULT_MIN_TIMEOUT_MS = 2_000;
/** The worst main-thread stall measured on the RF gallery under 6x throttling. */
const WORST_MEASURED_MAIN_THREAD_STALL_MS = 3_000;

describe("resolveConnectionMonitorInit", () => {
  it("defaults to a heartbeat that a busy browser can answer", () => {
    const init = resolveConnectionMonitorInit(undefined);

    expect(init.enabled).toBe(true);
    expect(init.pingInterval).toBe(SDN_CONNECTION_MONITOR_PING_INTERVAL_MS);
    expect(init.pingTimeout?.minTimeout).toBe(SDN_CONNECTION_MONITOR_TIMEOUT_MS);
    // The whole point: the deadline must clear the stall that used to kill
    // in-flight deliveries, with room to spare.
    expect(init.pingTimeout?.minTimeout).toBeGreaterThan(
      WORST_MEASURED_MAIN_THREAD_STALL_MS * 5,
    );
    expect(init.pingTimeout?.minTimeout).toBeGreaterThan(
      LIBP2P_DEFAULT_MIN_TIMEOUT_MS * 10,
    );
  });

  it("treats `true` as the default policy", () => {
    expect(resolveConnectionMonitorInit(true)).toEqual(
      resolveConnectionMonitorInit(undefined),
    );
  });

  it("switches the heartbeat off for `false`", () => {
    expect(resolveConnectionMonitorInit(false)).toEqual({ enabled: false });
  });

  it("merges a partial override over the policy instead of replacing it", () => {
    const init = resolveConnectionMonitorInit({ pingInterval: 5_000 });

    expect(init.pingInterval).toBe(5_000);
    // Overriding the cadence must NOT hand the deadline back to libp2p's floor.
    expect(init.pingTimeout?.minTimeout).toBe(SDN_CONNECTION_MONITOR_TIMEOUT_MS);
    expect(init.enabled).toBe(true);
  });

  it("honours an explicit deadline verbatim", () => {
    const init = resolveConnectionMonitorInit({
      pingTimeout: { minTimeout: 90_000 },
    });

    expect(init.pingTimeout?.minTimeout).toBe(90_000);
    expect(init.pingTimeout?.timeoutMultiplier).toBe(
      SDN_CONNECTION_MONITOR_DEFAULTS.pingTimeout?.timeoutMultiplier,
    );
  });

  it("never mutates the shared defaults", () => {
    const init = resolveConnectionMonitorInit(undefined);
    init.pingInterval = 1;
    (init.pingTimeout as { minTimeout: number }).minTimeout = 1;

    expect(SDN_CONNECTION_MONITOR_DEFAULTS.pingInterval).toBe(
      SDN_CONNECTION_MONITOR_PING_INTERVAL_MS,
    );
    expect(SDN_CONNECTION_MONITOR_DEFAULTS.pingTimeout?.minTimeout).toBe(
      SDN_CONNECTION_MONITOR_TIMEOUT_MS,
    );
    expect(resolveConnectionMonitorInit(undefined).pingInterval).toBe(
      SDN_CONNECTION_MONITOR_PING_INTERVAL_MS,
    );
  });

  it("describes itself in one line, both ways", () => {
    expect(describeConnectionMonitorPolicy(resolveConnectionMonitorInit())).toBe(
      "connection monitor: ping every 30s, abort after 30s without a reply",
    );
    expect(
      describeConnectionMonitorPolicy(resolveConnectionMonitorInit(false)),
    ).toBe("connection monitor: off (no heartbeat, no self-abort)");
  });
});

describe("libp2p connection-monitor contract (installed version)", () => {
  // libp2p is ESM-only and exposes neither a CJS entry nor `./package.json`
  // through its exports map, so `require.resolve` cannot reach it at all.
  // Walk up for the install directory instead — this is a check ON the
  // installed bytes, not an import of them.
  const libp2pDistDir = (() => {
    let dir = dirname(fileURLToPath(import.meta.url));
    for (let depth = 0; depth < 8; depth += 1) {
      const candidate = join(dir, "node_modules/libp2p/dist/src");
      if (existsSync(candidate)) {
        return candidate;
      }
      const parent = dirname(dir);
      if (parent === dir) {
        break;
      }
      dir = parent;
    }
    return null;
  })();
  const monitorPath = libp2pDistDir
    ? join(libp2pDistDir, "connection-monitor.js")
    : "";
  const libp2pPath = libp2pDistDir ? join(libp2pDistDir, "libp2p.js") : "";
  const installed =
    libp2pDistDir != null && existsSync(monitorPath) && existsSync(libp2pPath);
  const maybe = installed ? it : it.skip;

  maybe("reads `connectionMonitor.enabled`, so `false` really is off", () => {
    const source = readFileSync(libp2pPath, "utf8");
    expect(source).toMatch(/connectionMonitor\?\.enabled !== false/);
  });

  maybe("still aborts the whole connection when a heartbeat fails", () => {
    const source = readFileSync(monitorPath, "utf8");
    // If this stops being true the deadline stops being load-bearing.
    expect(source).toMatch(/conn\.abort\(err\)/);
  });

  maybe("still ignores `abortConnectionOnPingFailure`", () => {
    const source = readFileSync(monitorPath, "utf8");
    // Declared in the .d.ts, never read in the .js. The day it IS read, the
    // flag becomes the right lever and this policy should be reconsidered.
    expect(source).not.toMatch(/abortConnectionOnPingFailure/);
  });

  maybe("takes its deadline from an AdaptiveTimeout it never feeds", () => {
    const source = readFileSync(monitorPath, "utf8");
    expect(source).toMatch(/new AdaptiveTimeout\(/);
    // No `cleanUp()` call means the moving average stays at zero and the
    // effective deadline is exactly `minTimeout` — which is why the policy
    // sets that field and not the multipliers.
    expect(source).not.toMatch(/cleanUp\(/);
  });
});

/*
 * ---------------------------------------------------------------------------
 * Wiring: the policy must reach `createLibp2p`, in BOTH node lanes.
 * ---------------------------------------------------------------------------
 */
const createLibp2pMock = vi.fn();
const createHeliaMock = vi.fn();
const getBootstrapRelaysMock = vi.fn();
const initHDWalletMock = vi.fn(async () => true);

class MockEdgeDiscovery {
  readonly relays: string[];
  readonly probeAllRelays = vi.fn(async () => new Map());
  readonly startProbing = vi.fn();
  readonly stopProbing = vi.fn();
  readonly getBestRelays = vi.fn((count: number) => this.relays.slice(0, count));

  constructor(initialRelays: string[] = []) {
    this.relays = initialRelays.slice();
  }
}

vi.mock("libp2p", () => ({
  createLibp2p: createLibp2pMock,
}));

vi.mock("@libp2p/bootstrap", () => ({
  bootstrap: vi.fn(({ list }: { list: string[] }) => ({ list })),
}));

vi.mock("@libp2p/websockets", () => ({
  webSockets: vi.fn(() => ({ transport: "webSockets" })),
}));

vi.mock("@libp2p/websockets/filters", () => ({
  all: vi.fn(),
}));

vi.mock("@libp2p/webtransport", () => ({
  webTransport: vi.fn(() => ({ transport: "webTransport" })),
}));

vi.mock("@spacedatanetwork/libp2p-webrtc-v1", () => ({
  webRTC: vi.fn(() => ({ transport: "webRTC" })),
  webRTCDirect: vi.fn(() => ({ transport: "webRTCDirect" })),
}));

vi.mock("@libp2p/circuit-relay-v2", () => ({
  circuitRelayTransport: vi.fn(() => ({ transport: "relay" })),
}));

vi.mock("@libp2p/identify", () => ({
  identify: vi.fn(() => ({ service: "identify" })),
}));

vi.mock("@chainsafe/libp2p-gossipsub", () => ({
  gossipsub: vi.fn(() => ({ service: "pubsub" })),
  GossipSub: class {},
}));

vi.mock("@chainsafe/libp2p-noise", () => ({
  noise: vi.fn(() => ({ encryption: "noise" })),
}));

vi.mock("@chainsafe/libp2p-yamux", () => ({
  yamux: vi.fn(() => ({ muxer: "yamux" })),
}));

vi.mock("@libp2p/kad-dht", () => ({
  kadDHT: vi.fn(() => ({ service: "dht" })),
}));

vi.mock("./edge-discovery", () => ({
  getBootstrapRelays: getBootstrapRelaysMock,
  EdgeDiscovery: MockEdgeDiscovery,
}));

vi.mock("./crypto/hd-wallet", () => ({
  initHDWallet: initHDWalletMock,
}));

vi.mock("helia", () => ({
  createHelia: createHeliaMock,
}));

vi.mock("@helia/unixfs", () => ({ unixfs: vi.fn(() => ({})) }));
vi.mock("@helia/block-brokers", () => ({ bitswap: vi.fn(() => ({})) }));
vi.mock("@helia/routers", () => ({ libp2pRouting: vi.fn(() => ({})) }));

describe("SDNNode passes the policy to libp2p", () => {
  const relay = "/ip4/127.0.0.1/tcp/14080/ws/p2p/local-provider";

  beforeEach(() => {
    vi.resetModules();
    vi.clearAllMocks();
    getBootstrapRelaysMock.mockResolvedValue([relay]);
    createLibp2pMock.mockResolvedValue({
      peerId: { toString: () => "test-peer-id" },
      services: {},
      addEventListener: vi.fn(),
      start: vi.fn(async () => undefined),
      stop: vi.fn(async () => undefined),
      getPeers: vi.fn(() => []),
    });
  });

  it("configures the heartbeat by default — never libp2p's 2000 ms floor", async () => {
    const { SDNNode } = await import("./node");

    const node = await SDNNode.create({
      edgeRelays: [relay],
      enableStorage: false,
    });

    const opts = createLibp2pMock.mock.calls[0][0];
    expect(opts.connectionMonitor).toEqual(resolveConnectionMonitorInit());
    expect(opts.connectionMonitor.pingTimeout.minTimeout).toBe(
      SDN_CONNECTION_MONITOR_TIMEOUT_MS,
    );

    await node.stop();
  });

  it("switches the heartbeat off when the caller asks", async () => {
    const { SDNNode } = await import("./node");

    const node = await SDNNode.create({
      edgeRelays: [relay],
      enableStorage: false,
      connectionMonitor: false,
    });

    expect(createLibp2pMock.mock.calls[0][0].connectionMonitor).toEqual({
      enabled: false,
    });

    await node.stop();
  });

  it("carries a caller's cadence without losing the deadline", async () => {
    const { SDNNode } = await import("./node");

    const node = await SDNNode.create({
      edgeRelays: [relay],
      enableStorage: false,
      connectionMonitor: { pingInterval: 60_000 },
    });

    const monitor = createLibp2pMock.mock.calls[0][0].connectionMonitor;
    expect(monitor.pingInterval).toBe(60_000);
    expect(monitor.pingTimeout.minTimeout).toBe(
      SDN_CONNECTION_MONITOR_TIMEOUT_MS,
    );

    await node.stop();
  });
});

describe("the Helia lane carries the same policy", () => {
  const relay = "/ip4/127.0.0.1/tcp/14080/ws/p2p/local-provider";

  beforeEach(() => {
    vi.resetModules();
    vi.clearAllMocks();
    getBootstrapRelaysMock.mockResolvedValue([relay]);
    createLibp2pMock.mockResolvedValue({
      peerId: { toString: () => "test-peer-id" },
      services: {},
      addEventListener: vi.fn(),
      start: vi.fn(async () => undefined),
      stop: vi.fn(async () => undefined),
      getPeers: vi.fn(() => []),
      getConnections: vi.fn(() => []),
      dial: vi.fn(async () => ({})),
    });
    createHeliaMock.mockResolvedValue({
      libp2p: { getPeers: vi.fn(() => []) },
      stop: vi.fn(async () => undefined),
    });
  });

  it("configures the heartbeat for createHeliaSDNNode too", async () => {
    const { createHeliaSDNNode } = await import("./helia");

    await createHeliaSDNNode({ edgeRelays: [relay] });

    const opts = createLibp2pMock.mock.calls[0][0];
    expect(opts.connectionMonitor).toEqual(resolveConnectionMonitorInit());
  });
});

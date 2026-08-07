/**
 * The renderer main thread is a shared resource sdn-js is a GUEST on.
 *
 * These are regression tests for a P1 measured on 2026-08-07: every RF
 * sandcastle demo AND the live `spaceaware.io/beta` stopped answering ~12 s
 * after load, permanently, because `SDNNode.create()` / `createHeliaSDNNode()`
 * started a Kad-DHT client by default. A browser DHT client expands its query
 * frontier entirely in MICROTASKS (candidate peers are rejected by
 * `isDialable()` before any I/O), which starves the task queue — so timers
 * never fire, the DHT's own query timeout never fires, and the wedge is
 * unbounded by construction. Byte-level A/B on one box: removing only
 * `services.dht` kept both surfaces interactive for the full window.
 *
 * The invariant these tests hold: NOTHING that can hold the main thread is
 * started unless the caller asks for it by name, and every API that needs the
 * omitted service fails LOUDLY and immediately instead of stalling.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";

const createLibp2pMock = vi.fn();
const kadDHTMock = vi.fn(() => ({ service: "dht" }));
const getBootstrapRelaysMock = vi.fn();
const initHDWalletMock = vi.fn(async () => true);
const createHeliaFromLibp2pMock = vi.fn();
const fetchCIDBytesFromHeliaMock = vi.fn();

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

vi.mock("libp2p", () => ({ createLibp2p: createLibp2pMock }));
vi.mock("@libp2p/bootstrap", () => ({
  bootstrap: vi.fn(({ list }: { list: string[] }) => ({ list })),
}));
vi.mock("@libp2p/websockets", () => ({
  webSockets: vi.fn(() => ({ transport: "webSockets" })),
}));
vi.mock("@libp2p/websockets/filters", () => ({ all: vi.fn() }));
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
vi.mock("@libp2p/kad-dht", () => ({ kadDHT: kadDHTMock }));
vi.mock("./edge-discovery", () => ({
  getBootstrapRelays: getBootstrapRelaysMock,
  EdgeDiscovery: MockEdgeDiscovery,
}));
vi.mock("./crypto/hd-wallet", () => ({ initHDWallet: initHDWalletMock }));

const RELAY = "/ip4/127.0.0.1/tcp/14080/ws/p2p/local-provider";

function libp2pOptionsFromLastCall(): Record<string, any> {
  expect(createLibp2pMock).toHaveBeenCalled();
  return createLibp2pMock.mock.calls.at(-1)![0] as Record<string, any>;
}

beforeEach(() => {
  vi.resetModules();
  vi.clearAllMocks();
  getBootstrapRelaysMock.mockResolvedValue([RELAY]);
  createLibp2pMock.mockResolvedValue({
    peerId: { toString: () => "test-peer-id" },
    services: {},
    contentRouting: {
      findProviders: vi.fn(),
      provide: vi.fn(async () => undefined),
    },
    addEventListener: vi.fn(),
    start: vi.fn(async () => undefined),
    stop: vi.fn(async () => undefined),
    getPeers: vi.fn(() => []),
  });
  createHeliaFromLibp2pMock.mockResolvedValue({
    stop: vi.fn(async () => undefined),
  });
  fetchCIDBytesFromHeliaMock.mockResolvedValue(new Uint8Array([1, 2, 3]));
});

describe("SDNNode does not start a Kad-DHT unless asked", () => {
  beforeEach(() => {
    vi.doMock("./helia", () => ({
      createHeliaFromLibp2p: createHeliaFromLibp2pMock,
      fetchCIDBytesFromHelia: fetchCIDBytesFromHeliaMock,
    }));
  });

  it("omits the dht service by default (the renderer wedge of 2026-08-07)", async () => {
    const { SDNNode } = await import("./node");
    const node = await SDNNode.create({
      edgeRelays: [RELAY],
      enableStorage: false,
    });

    expect(libp2pOptionsFromLastCall().services.dht).toBeUndefined();
    expect(kadDHTMock).not.toHaveBeenCalled();

    await node.stop();
  });

  it("still omits it when the flag is explicitly false", async () => {
    const { SDNNode } = await import("./node");
    const node = await SDNNode.create({
      edgeRelays: [RELAY],
      enableStorage: false,
      enableDHT: false,
    });

    expect(libp2pOptionsFromLastCall().services.dht).toBeUndefined();

    await node.stop();
  });

  it("starts it only for a caller that opts in by name", async () => {
    const { SDNNode } = await import("./node");
    const node = await SDNNode.create({
      edgeRelays: [RELAY],
      enableStorage: false,
      enableDHT: true,
    });

    expect(libp2pOptionsFromLastCall().services.dht).toEqual({
      service: "dht",
    });
    expect(kadDHTMock).toHaveBeenCalledWith({ clientMode: true });

    await node.stop();
  });

  it("keeps pubsub, which never needed content routing", async () => {
    const { SDNNode } = await import("./node");
    const node = await SDNNode.create({
      edgeRelays: [RELAY],
      enableStorage: false,
    });

    expect(libp2pOptionsFromLastCall().services.pubsub).toEqual({
      service: "pubsub",
    });

    await node.stop();
  });
});

describe("DHT-dependent APIs fail bounded and legibly when it is off", () => {
  beforeEach(() => {
    vi.doMock("./helia", () => ({
      createHeliaFromLibp2p: createHeliaFromLibp2pMock,
      fetchCIDBytesFromHelia: fetchCIDBytesFromHeliaMock,
    }));
  });

  const CID_STR = "bafkreia24vofdq575eafzg2mvxwmr6krj2rvo7onif4t3ty5tqcewx3ao4";

  it("discoverProviders() rejects instead of stalling", async () => {
    const { SDNNode } = await import("./node");
    const node = await SDNNode.create({
      edgeRelays: [RELAY],
      enableStorage: false,
    });

    await expect(node.discoverProviders(CID_STR)).rejects.toThrow(
      /requires libp2p content routing.*enableDHT: true/su,
    );

    await node.stop();
  });

  it("discoverSDNAdvertisementPeers() rejects instead of stalling", async () => {
    const { SDNNode } = await import("./node");
    const node = await SDNNode.create({
      edgeRelays: [RELAY],
      enableStorage: false,
    });

    await expect(node.discoverSDNAdvertisementPeers()).rejects.toThrow(
      /enableDHT: true/su,
    );

    await node.stop();
  });

  it("provideSDNAdvertisementFlag() rejects instead of stalling", async () => {
    const { SDNNode } = await import("./node");
    const node = await SDNNode.create({
      edgeRelays: [RELAY],
      enableStorage: false,
    });

    await expect(node.provideSDNAdvertisementFlag()).rejects.toThrow(
      /enableDHT: true/su,
    );

    await node.stop();
  });

  it("names the renderer hazard so nobody re-enables it on the main thread", async () => {
    const { dhtRequiredError } = await import("./node");
    const message = dhtRequiredError("X()").message;

    expect(message).toContain("renderer main thread");
    expect(message).toContain("worker");
  });
});

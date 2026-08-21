import { beforeEach, describe, expect, it, vi } from "vitest";

const createLibp2pMock = vi.fn();
const bootstrapMock = vi.fn(({ list }: { list: string[] }) => ({ list }));
const getBootstrapRelaysMock = vi.fn();
const initHDWalletMock = vi.fn(async () => true);
const createHeliaFromLibp2pMock = vi.fn();
const fetchCIDBytesFromHeliaMock = vi.fn();
const edgeDiscoveryInstances: MockEdgeDiscovery[] = [];

class MockEdgeDiscovery {
  readonly relays: string[];
  readonly probeAllRelays = vi.fn(async () => new Map());
  readonly startProbing = vi.fn();
  readonly stopProbing = vi.fn();
  readonly getBestRelays = vi.fn((count: number) =>
    this.relays.slice(0, count),
  );

  constructor(initialRelays: string[] = []) {
    this.relays = initialRelays.slice();
    edgeDiscoveryInstances.push(this);
  }
}

/** A stream-shaped object `exchangeStream` can sink into and read from. */
function makeMockStream(): {
  sink: ReturnType<typeof vi.fn>;
  source: AsyncIterable<Uint8Array>;
  close: ReturnType<typeof vi.fn>;
} {
  return {
    sink: vi.fn(async () => undefined),
    source: (async function* () {
      yield new Uint8Array([7, 8, 9]);
    })(),
    close: vi.fn(async () => undefined),
  };
}

vi.mock("libp2p", () => ({
  createLibp2p: createLibp2pMock,
}));

vi.mock("@libp2p/bootstrap", () => ({
  bootstrap: bootstrapMock,
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

vi.mock("./helia", () => ({
  createHeliaFromLibp2p: createHeliaFromLibp2pMock,
  fetchCIDBytesFromHelia: fetchCIDBytesFromHeliaMock,
}));

describe("SDNNode relay bootstrap", () => {
  beforeEach(() => {
    vi.resetModules();
    vi.clearAllMocks();
    edgeDiscoveryInstances.length = 0;
    vi.restoreAllMocks();
    getBootstrapRelaysMock.mockResolvedValue([
      "/dns4/bootstrap.example/tcp/443/wss/p2p/bootstrap-peer",
    ]);
    createLibp2pMock.mockResolvedValue({
      peerId: { toString: () => "test-peer-id" },
      services: {},
      addEventListener: vi.fn(),
      start: vi.fn(async () => undefined),
      stop: vi.fn(async () => undefined),
      getPeers: vi.fn(() => []),
      dialProtocol: vi.fn(async () => makeMockStream()),
    });
    createHeliaFromLibp2pMock.mockResolvedValue({
      stop: vi.fn(async () => undefined),
    });
    fetchCIDBytesFromHeliaMock.mockResolvedValue(new Uint8Array([1, 2, 3]));
  });

  it("treats explicit edge relays as authoritative by default", async () => {
    const { SDNNode } = await import("./node");
    const explicitRelay = "/ip4/127.0.0.1/tcp/14080/ws/p2p/local-provider";

    const node = await SDNNode.create({
      edgeRelays: [explicitRelay],
      enableStorage: false,
    });

    expect(getBootstrapRelaysMock).not.toHaveBeenCalled();
    expect(edgeDiscoveryInstances).toHaveLength(1);
    expect(edgeDiscoveryInstances[0].probeAllRelays).not.toHaveBeenCalled();
    expect(edgeDiscoveryInstances[0].startProbing).not.toHaveBeenCalled();
    expect(bootstrapMock).toHaveBeenCalledWith({ list: [explicitRelay] });

    await node.stop();
  });

  it("enables WebRTC-direct transport for browser-dialable full-node bootstrap addresses", async () => {
    const { SDNNode } = await import("./node");

    const node = await SDNNode.create({
      edgeRelays: [
        "/ip4/167.172.219.213/udp/4003/webrtc-direct/certhash/uEiD8YU5I18BuOBAcE8z_3NFRoGnhu9dKdTjG7PAqVbAjEQ/p2p/16Uiu2HAm9oK2jAeVC2RMESFcYfq7BKGp2K2CCDxzoKhB5s9vpbj3",
      ],
      enableStorage: false,
    });

    expect(createLibp2pMock.mock.calls[0][0].transports).toEqual(
      expect.arrayContaining([expect.objectContaining({ transport: "webRTCDirect" })]),
    );

    await node.stop();
  });

  it("keeps discovery probing for the default bootstrap path", async () => {
    const { SDNNode, IPFS_BOOTSTRAP_PEERS } = await import("./node");

    const node = await SDNNode.create({
      enableStorage: false,
    });

    expect(getBootstrapRelaysMock).toHaveBeenCalledTimes(1);
    expect(edgeDiscoveryInstances).toHaveLength(1);
    expect(edgeDiscoveryInstances[0].probeAllRelays).toHaveBeenCalledTimes(1);
    expect(edgeDiscoveryInstances[0].startProbing).toHaveBeenCalledTimes(1);
    expect(bootstrapMock).toHaveBeenCalledWith({
      list: [
        ...IPFS_BOOTSTRAP_PEERS,
        "/dns4/bootstrap.example/tcp/443/wss/p2p/bootstrap-peer",
      ],
    });

    await node.stop();
  });

  it("requires an HTTP CID fetch endpoint in the browser bundle", async () => {
    const { SDNNode } = await import("./node");
    const node = await SDNNode.create({
      edgeRelays: ["/ip4/127.0.0.1/tcp/14080/ws/p2p/local-provider"],
      enableStorage: false,
    });

    await expect(node.fetchCIDBytes("bafkreicidone")).rejects.toThrow(
      /ipfsApiBaseUrl or ipfsGatewayBaseUrl/i,
    );
    expect(createHeliaFromLibp2pMock).not.toHaveBeenCalled();
    expect(fetchCIDBytesFromHeliaMock).not.toHaveBeenCalled();

    await node.stop();
  });

  it("uses the configured IPFS API for CID fetches without starting Helia", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue({
      ok: true,
      arrayBuffer: async () => Uint8Array.from([9, 8, 7]).buffer,
    } as Response);

    const { SDNNode } = await import("./node");
    const node = await SDNNode.create({
      edgeRelays: ["/ip4/127.0.0.1/tcp/14080/ws/p2p/local-provider"],
      enableStorage: false,
      ipfsApiBaseUrl: "/api/v0",
    });

    const result = await node.fetchCIDBytes("bafkreicidviahttp");

    expect(result).toEqual(Uint8Array.from([9, 8, 7]));
    expect(fetchMock).toHaveBeenCalledWith(
      expect.objectContaining({
        href: "http://localhost/api/v0/cat?arg=bafkreicidviahttp",
      }),
      expect.objectContaining({
        method: "POST",
        headers: { accept: "application/octet-stream" },
      }),
    );
    expect(createHeliaFromLibp2pMock).not.toHaveBeenCalled();
    expect(fetchCIDBytesFromHeliaMock).not.toHaveBeenCalled();

    await node.stop();
  });

  it("uses the configured IPFS gateway for CID fetches without starting Helia", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue({
      ok: true,
      arrayBuffer: async () => Uint8Array.from([7, 8, 9]).buffer,
    } as Response);

    const { SDNNode } = await import("./node");
    const node = await SDNNode.create({
      edgeRelays: ["/ip4/127.0.0.1/tcp/14080/ws/p2p/local-provider"],
      enableStorage: false,
      ipfsGatewayBaseUrl: "https://ipfs.io/ipfs/",
    });

    const result = await node.fetchCIDBytes("bafkreicidviagateway");

    expect(result).toEqual(Uint8Array.from([7, 8, 9]));
    expect(fetchMock).toHaveBeenCalledWith(
      expect.objectContaining({
        href: "https://ipfs.io/ipfs/bafkreicidviagateway",
      }),
      expect.objectContaining({
        method: "GET",
        headers: { accept: "application/octet-stream" },
      }),
    );
    expect(createHeliaFromLibp2pMock).not.toHaveBeenCalled();
    expect(fetchCIDBytesFromHeliaMock).not.toHaveBeenCalled();

    await node.stop();
  });

  it("times out a configured IPFS API CID fetch without starting Helia", async () => {
    const fetchMock = vi
      .spyOn(globalThis, "fetch")
      .mockImplementationOnce(() => new Promise(() => undefined));

    const { SDNNode } = await import("./node");
    const node = await SDNNode.create({
      edgeRelays: ["/ip4/127.0.0.1/tcp/14080/ws/p2p/local-provider"],
      enableStorage: false,
      ipfsApiBaseUrl: "/api/v0",
      ipfsFetchTimeoutMs: 1,
    });

    await expect(node.fetchCIDBytes("bafkreihttphangcid")).rejects.toThrow(
      /timed out/i,
    );
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(createHeliaFromLibp2pMock).not.toHaveBeenCalled();
    expect(fetchCIDBytesFromHeliaMock).not.toHaveBeenCalled();

    await node.stop();
  });

  it("rejects the removed legacy 'indexeddb' storage backend with a clear error (loop D.5)", async () => {
    const { SDNNode } = await import("./node");

    await expect(
      SDNNode.create({
        edgeRelays: ["/ip4/127.0.0.1/tcp/14080/ws/p2p/local-provider"],
        storageBackend: "indexeddb" as never,
      }),
    ).rejects.toThrow(/SDNStorage.*removed/is);
  });
});

/*
 * ---------------------------------------------------------------------------
 * Regression: every delivery dial carries OUR deadline signal
 * (sdn-js-connection-monitor-kills-its-own-in-flight-streams, ASK 2-3).
 *
 * libp2p 1.9.4's upgrader creates a silent 30000 ms timer exactly when
 * `options.signal == null` ("no abort signal was passed ... falling back to
 * default timeout", upgrader.js). The defect half of this task is that
 * `SDNNode.dialProtocol` passed NO options at all, so that 30 s default
 * governed every module-delivery and flatsql-sync stream this node opened.
 * These tests pin the seam: a signal is always passed, its deadline comes
 * from SDNConfig (host-tightenable), and a timed-out attempt kills only that
 * attempt — never the connection it rides on.
 * ---------------------------------------------------------------------------
 */
describe("SDNNode dialProtocol deadline", () => {
  const relay = "/ip4/127.0.0.1/tcp/14080/ws/p2p/local-provider";
  const targetPeer =
    "12D3KooWGhZfrxQVvwQHNGRkeJhGqMbkDqjktfpBXzn47N78XY9j";
  const DELIVERY_PROTOCOL = "/space-data-network/module-delivery/1.0.0";

  beforeEach(() => {
    vi.resetModules();
    vi.clearAllMocks();
    edgeDiscoveryInstances.length = 0;
    vi.restoreAllMocks();
    getBootstrapRelaysMock.mockResolvedValue([relay]);
    createLibp2pMock.mockResolvedValue({
      peerId: { toString: () => "test-peer-id" },
      services: {},
      addEventListener: vi.fn(),
      start: vi.fn(async () => undefined),
      stop: vi.fn(async () => undefined),
      getPeers: vi.fn(() => []),
      dialProtocol: vi.fn(async () => makeMockStream()),
    });
    createHeliaFromLibp2pMock.mockResolvedValue({
      stop: vi.fn(async () => undefined),
    });
    fetchCIDBytesFromHeliaMock.mockResolvedValue(new Uint8Array([1, 2, 3]));
  });

  async function lastMockLibp2p(): Promise<{
    dialProtocol: ReturnType<typeof vi.fn>;
  }> {
    const result = createLibp2pMock.mock.results[0];
    if (result == null || result.type !== "return") {
      throw new Error("expected createLibp2p to have resolved in this test");
    }
    // `mockResolvedValue` hands the mock fn a promise; the mock's result
    // slot holds that promise, not the object it resolves with.
    return (await result.value) as {
      dialProtocol: ReturnType<typeof vi.fn>;
    };
  }

  it("passes an explicit signal, so libp2p's silent 30s fallback never governs our deliveries", async () => {
    const { SDNNode, DEFAULT_DIAL_PROTOCOL_TIMEOUT_MS } = await import(
      "./node"
    );
    const timeoutSpy = vi.spyOn(AbortSignal, "timeout");
    const node = await SDNNode.create({
      edgeRelays: [relay],
      enableStorage: false,
    });

    const reply = await node.dialProtocol(
      targetPeer,
      DELIVERY_PROTOCOL,
      new Uint8Array([1]),
    );

    expect(reply).toEqual(new Uint8Array([7, 8, 9]));
    // The deadline is OUR constant, not an absent options argument.
    expect(timeoutSpy).toHaveBeenCalledWith(DEFAULT_DIAL_PROTOCOL_TIMEOUT_MS);
    const call = (await lastMockLibp2p()).dialProtocol.mock.calls[0];
    expect(call[2]).toEqual(
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    );

    await node.stop();
  });

  it("honours dialProtocolTimeoutMs from SDNConfig — a host can tighten it", async () => {
    const { SDNNode } = await import("./node");
    const timeoutSpy = vi.spyOn(AbortSignal, "timeout");
    const node = await SDNNode.create({
      edgeRelays: [relay],
      enableStorage: false,
      dialProtocolTimeoutMs: 5_000,
    });

    await node.dialProtocol(
      targetPeer,
      DELIVERY_PROTOCOL,
      new Uint8Array([1]),
    );

    expect(timeoutSpy).toHaveBeenCalledWith(5_000);

    await node.stop();
  });

  it("gives every candidate-address attempt a fresh budget — one timed-out address does not abort the rest", async () => {
    const { SDNNode } = await import("./node");
    const timeoutSpy = vi.spyOn(AbortSignal, "timeout");
    const node = await SDNNode.create({
      edgeRelays: [relay],
      enableStorage: false,
      dialProtocolTimeoutMs: 6_000,
    });

    (await lastMockLibp2p()).dialProtocol.mockRejectedValueOnce(
      new Error("first candidate refused"),
    );

    await node.dialProtocol(
      targetPeer,
      DELIVERY_PROTOCOL,
      new Uint8Array([1]),
      [
        "/ip4/127.0.0.1/tcp/14081/ws",
        "/ip4/127.0.0.1/tcp/14082/ws",
      ],
    );

    const calls = (await lastMockLibp2p()).dialProtocol.mock.calls;
    expect(calls).toHaveLength(2);
    for (const call of calls) {
      expect(call[2]).toEqual(
        expect.objectContaining({ signal: expect.any(AbortSignal) }),
      );
    }
    // Two candidates, two deadlines — the shared-signal instant-abort trap
    // is exactly what this task exists to rule out.
    expect(timeoutSpy).toHaveBeenCalledWith(6_000);
    expect(timeoutSpy).toHaveBeenCalledTimes(2);

    await node.stop();
  });
});

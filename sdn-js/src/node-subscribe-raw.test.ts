import { beforeEach, describe, expect, it, vi } from "vitest";

/**
 * subscribeRaw() — the binary counterpart to publishRaw().
 *
 * Regression cover for the live defect recorded in upstream-sdn-2: real PNM
 * announcements on /spacedatanetwork/sds/PNM.fbs are size-prefixed FlatBuffers
 * ("$PNM" file identifier at byte offset 8), and subscribe()'s JSON.parse path
 * discards 100% of them. These tests pin BOTH halves: raw bytes arrive intact,
 * and the JSON path is unchanged.
 */

const createLibp2pMock = vi.fn();
const getBootstrapRelaysMock = vi.fn();

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

/** Minimal gossipsub stand-in: records topics and replays "message" events. */
class MockPubSub {
  readonly subscribed: string[] = [];
  readonly unsubscribed: string[] = [];
  private listeners: Array<{
    fn: (evt: { detail: unknown }) => void;
    signal?: AbortSignal;
  }> = [];

  subscribe(topic: string): void {
    this.subscribed.push(topic);
  }

  unsubscribe(topic: string): void {
    this.unsubscribed.push(topic);
  }

  addEventListener(
    _type: string,
    fn: (evt: { detail: unknown }) => void,
    opts?: { signal?: AbortSignal },
  ): void {
    this.listeners.push({ fn, signal: opts?.signal });
  }

  emit(topic: string, data: Uint8Array, from: string): void {
    for (const l of this.listeners) {
      if (l.signal?.aborted) continue;
      l.fn({ detail: { topic, data, from: { toString: () => from } } });
    }
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
vi.mock("@libp2p/kad-dht", () => ({ kadDHT: vi.fn(() => ({ service: "dht" })) }));
vi.mock("./edge-discovery", () => ({
  getBootstrapRelays: getBootstrapRelaysMock,
  EdgeDiscovery: MockEdgeDiscovery,
}));
vi.mock("./crypto/hd-wallet", () => ({ initHDWallet: vi.fn(async () => true) }));
vi.mock("./helia", () => ({
  createHeliaFromLibp2p: vi.fn(async () => ({ stop: vi.fn(async () => undefined) })),
  fetchCIDBytesFromHelia: vi.fn(),
}));

const PNM_TOPIC = "/spacedatanetwork/sds/PNM.fbs";

/**
 * A size-prefixed FlatBuffer shaped like a live PNM announcement:
 *   [0..3]  little-endian size prefix
 *   [4..7]  root table offset
 *   [8..11] file identifier "$PNM"
 * The tail carries bytes that are not valid UTF-8/JSON, which is precisely why
 * the JSON path drops real traffic.
 */
function makeSizePrefixedPNM(): Uint8Array {
  const body = new Uint8Array([
    0x04, 0x00, 0x00, 0x00, // root table offset
    0x24, 0x50, 0x4e, 0x4d, // "$PNM" at absolute offset 8
    0xff, 0xfe, 0x00, 0x80, // non-UTF-8 tail
    0x10, 0x00, 0x0a, 0x00,
  ]);
  const out = new Uint8Array(4 + body.length);
  new DataView(out.buffer).setUint32(0, body.length, true);
  out.set(body, 4);
  return out;
}

async function createNodeWithPubSub(): Promise<{
  node: any;
  pubsub: MockPubSub;
}> {
  const pubsub = new MockPubSub();
  createLibp2pMock.mockResolvedValue({
    peerId: { toString: () => "test-peer-id" },
    services: { pubsub },
    addEventListener: vi.fn(),
    start: vi.fn(async () => undefined),
    stop: vi.fn(async () => undefined),
    getPeers: vi.fn(() => []),
  });
  const { SDNNode } = await import("./node");
  const node = await SDNNode.create({
    edgeRelays: ["/ip4/127.0.0.1/tcp/14080/ws/p2p/local-provider"],
    enableStorage: false,
  });
  return { node, pubsub };
}

describe("SDNNode.subscribeRaw", () => {
  beforeEach(() => {
    vi.resetModules();
    vi.clearAllMocks();
    getBootstrapRelaysMock.mockResolvedValue([]);
  });

  it("delivers a size-prefixed $PNM FlatBuffer to the callback byte-for-byte", async () => {
    const { node, pubsub } = await createNodeWithPubSub();
    const received: Array<{ bytes: Uint8Array; from: string }> = [];

    await node.subscribeRaw("PNM.fbs", (bytes: Uint8Array, from: string) => {
      received.push({ bytes, from });
    });

    expect(pubsub.subscribed).toContain(PNM_TOPIC);

    const payload = makeSizePrefixedPNM();
    pubsub.emit(PNM_TOPIC, payload, "12D3KooWPublisher");

    expect(received).toHaveLength(1);
    expect(received[0].from).toBe("12D3KooWPublisher");
    // Byte-for-byte identity, not a coerced/re-encoded copy.
    expect(Array.from(received[0].bytes)).toEqual(Array.from(payload));
    // The identifier survives at its wire offset.
    expect(
      new TextDecoder().decode(received[0].bytes.subarray(8, 12)),
    ).toBe("$PNM");
    // And the declared size prefix still matches the body length.
    expect(new DataView(received[0].bytes.buffer, received[0].bytes.byteOffset).getUint32(0, true))
      .toBe(received[0].bytes.length - 4);

    await node.stop();
  });

  it("does not decode, validate, or JSON-parse the payload", async () => {
    const { node, pubsub } = await createNodeWithPubSub();
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    const seen: Uint8Array[] = [];

    await node.subscribeRaw("PNM.fbs", (bytes: Uint8Array) => seen.push(bytes));
    pubsub.emit(PNM_TOPIC, makeSizePrefixedPNM(), "12D3KooWPublisher");

    expect(seen).toHaveLength(1);
    // subscribe()'s "Failed to parse message" path must never be taken here.
    expect(warn).not.toHaveBeenCalled();

    warn.mockRestore();
    await node.stop();
  });

  it("ignores other topics and empty payloads", async () => {
    const { node, pubsub } = await createNodeWithPubSub();
    const seen: Uint8Array[] = [];

    await node.subscribeRaw("PNM.fbs", (bytes: Uint8Array) => seen.push(bytes));

    pubsub.emit("/spacedatanetwork/sds/OMM.fbs", makeSizePrefixedPNM(), "peer");
    pubsub.emit(PNM_TOPIC, new Uint8Array(0), "peer");

    expect(seen).toHaveLength(0);

    await node.stop();
  });

  it("leaves the JSON subscribe() path unchanged", async () => {
    const { node, pubsub } = await createNodeWithPubSub();
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    const jsonSeen: unknown[] = [];

    await node.subscribe("PNM.fbs", (data: unknown) => jsonSeen.push(data));

    // JSON still decodes.
    pubsub.emit(
      PNM_TOPIC,
      new TextEncoder().encode(JSON.stringify({ NORAD_CAT_ID: 25544 })),
      "peer",
    );
    expect(jsonSeen).toEqual([{ NORAD_CAT_ID: 25544 }]);

    // ...and binary is still dropped by that path (the documented behaviour
    // subscribeRaw exists to work around).
    pubsub.emit(PNM_TOPIC, makeSizePrefixedPNM(), "peer");
    expect(jsonSeen).toHaveLength(1);
    expect(warn).toHaveBeenCalled();

    warn.mockRestore();
    await node.stop();
  });

  it("runs a raw and a JSON subscription on one topic without either evicting the other", async () => {
    const { node, pubsub } = await createNodeWithPubSub();
    vi.spyOn(console, "warn").mockImplementation(() => undefined);
    const raw: Uint8Array[] = [];
    const json: unknown[] = [];

    await node.subscribeRaw("PNM.fbs", (bytes: Uint8Array) => raw.push(bytes));
    await node.subscribe("PNM.fbs", (data: unknown) => json.push(data));

    pubsub.emit(PNM_TOPIC, makeSizePrefixedPNM(), "peer");
    pubsub.emit(
      PNM_TOPIC,
      new TextEncoder().encode(JSON.stringify({ ok: true })),
      "peer",
    );

    expect(raw).toHaveLength(2); // raw sees everything on the topic
    expect(json).toEqual([{ ok: true }]);

    // Dropping the JSON side must not tear down the topic the raw side needs.
    await node.unsubscribe("PNM.fbs");
    expect(pubsub.unsubscribed).not.toContain(PNM_TOPIC);

    pubsub.emit(PNM_TOPIC, makeSizePrefixedPNM(), "peer");
    expect(raw).toHaveLength(3);
    expect(json).toHaveLength(1);

    // Only when the last subscription goes does the topic get left.
    await node.unsubscribeRaw("PNM.fbs");
    expect(pubsub.unsubscribed).toContain(PNM_TOPIC);

    pubsub.emit(PNM_TOPIC, makeSizePrefixedPNM(), "peer");
    expect(raw).toHaveLength(3);

    await node.stop();
  });

  it("accepts arbitrary schema strings, exactly like publishRaw", async () => {
    const { node, pubsub } = await createNodeWithPubSub();
    const seen: Uint8Array[] = [];

    await node.subscribeRaw("EPM.fbs", (bytes: Uint8Array) => seen.push(bytes));
    expect(pubsub.subscribed).toContain("/spacedatanetwork/sds/EPM.fbs");

    pubsub.emit("/spacedatanetwork/sds/EPM.fbs", makeSizePrefixedPNM(), "peer");
    expect(seen).toHaveLength(1);

    await node.stop();
  });

  it("stop() cancels raw subscriptions", async () => {
    const { node, pubsub } = await createNodeWithPubSub();
    const seen: Uint8Array[] = [];

    await node.subscribeRaw("PNM.fbs", (bytes: Uint8Array) => seen.push(bytes));
    pubsub.emit(PNM_TOPIC, makeSizePrefixedPNM(), "peer");
    expect(seen).toHaveLength(1);

    await node.stop();

    pubsub.emit(PNM_TOPIC, makeSizePrefixedPNM(), "peer");
    expect(seen).toHaveLength(1);
  });
});

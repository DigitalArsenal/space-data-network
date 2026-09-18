import { beforeEach, describe, expect, it, vi } from 'vitest';
import { Uint8ArrayList } from 'uint8arraylist';

/**
 * A minimal stand-in for helia 7's `createHeliaLight`.
 *
 * helia 7 no longer accepts a pre-built libp2p: a node is composed with
 * `addMixin` / `addRouter` and then started. A mock that just returns a bag of
 * properties would never run `attachLibp2p`, so this one keeps the mixin list
 * and runs it on start(), which is what makes the assertions below mean
 * anything.
 */
const createdHeliaNodes: any[] = [];
const createHeliaLightMock = vi.fn((init: any) => {
  const mixins: any[] = [];
  const routers: any[] = [];
  const helia: any = {
    init,
    mixins,
    routers,
    blockstore: {},
    addMixin: (mixin: any) => mixins.push(mixin),
    addRouter: (router: any) => routers.push(router),
    hasRouter: (name: string) => routers.some((router) => router?.name === name),
    async start() {
      for (const mixin of mixins) await mixin.start?.(helia);
      return helia;
    },
    async stop() {
      for (const mixin of mixins) await mixin.stop?.(helia);
      return helia;
    },
  };
  createdHeliaNodes.push(helia);
  return helia;
});
const createdLibp2pNodes: any[] = [];
const createLibp2pMock = vi.fn(async (init: any) => {
  const node = {
    init,
    dial: vi.fn(async () => undefined),
    handle: vi.fn(),
    stop: vi.fn(async () => undefined),
  };
  createdLibp2pNodes.push(node);
  return node;
});
const unixfsCatMock = vi.fn();
const unixfsMock = vi.fn(() => ({
  cat: unixfsCatMock,
}));
const bootstrapMock = vi.fn(({ list }: { list: string[] }) => ({ list }));
const withBitswapMock = vi.fn((helia: any) => helia);
const trustlessGatewayMock = vi.fn(() => ({ blockBroker: 'trustless-gateway' }));
const fallbackRouterMock = vi.fn((options: unknown) => ({ name: 'fallback-router', options }));
const getBootstrapRelaysMock = vi.fn(async () => []);
const initHDWalletMock = vi.fn(async () => true);
const peerIdFromStringMock = vi.fn((peerId: string) => ({
  multihash: { bytes: new Uint8Array([1, 2, 3]) },
  peerId,
  toMultihash: () => ({ bytes: new Uint8Array([1, 2, 3]) }),
  toString: () => peerId,
}));

vi.mock('helia', () => ({
  createHeliaLight: createHeliaLightMock,
}));

vi.mock('@helia/unixfs', () => ({
  unixfs: unixfsMock,
}));

vi.mock('libp2p', () => ({
  createLibp2p: createLibp2pMock,
}));

vi.mock('@libp2p/bootstrap', () => ({
  bootstrap: bootstrapMock,
}));

vi.mock('@helia/bitswap', () => ({
  withBitswap: withBitswapMock,
}));

vi.mock('@helia/trustless-gateway-client', () => ({
  trustlessGatewayBlockBroker: trustlessGatewayMock,
}));

vi.mock('@helia/fallback-router', () => ({
  fallbackRouter: fallbackRouterMock,
}));

vi.mock('@libp2p/websockets', () => ({
  webSockets: vi.fn(() => ({ transport: 'webSockets' })),
}));

vi.mock('@libp2p/webtransport', () => ({
  webTransport: vi.fn(() => ({ transport: 'webTransport' })),
}));

vi.mock('@libp2p/webrtc', () => ({
  webRTCDirect: vi.fn(() => ({ transport: 'webRTCDirect' })),
}));

vi.mock('@libp2p/circuit-relay-v2', () => ({
  circuitRelayTransport: vi.fn(() => ({ transport: 'relay' })),
}));

vi.mock('@libp2p/identify', () => ({
  identify: vi.fn(() => ({ service: 'identify' })),
}));

vi.mock('@libp2p/gossipsub', () => ({
  gossipsub: vi.fn(() => ({ service: 'pubsub' })),
}));

vi.mock('@libp2p/noise', () => ({
  noise: vi.fn(() => ({ encryption: 'noise' })),
}));

vi.mock('@libp2p/yamux', () => ({
  yamux: vi.fn(() => ({ muxer: 'yamux' })),
}));

vi.mock('@libp2p/ping', () => ({
  ping: vi.fn(() => ({ service: 'ping' })),
}));

vi.mock('@libp2p/kad-dht', () => ({
  kadDHT: vi.fn(() => ({ service: 'dht' })),
}));

vi.mock('@libp2p/peer-id', () => ({
  peerIdFromString: peerIdFromStringMock,
  peerIdFromCID: vi.fn((cid: unknown) => cid),
}));

vi.mock('@multiformats/multiaddr', () => ({
  multiaddr: vi.fn((addr: string) => ({
    addr,
    // multiaddr 13 ("lightweight multiaddrs") removed getPeerId(); components
    // are the replacement, and the LAST /p2p component is the target peer.
    getComponents: () =>
      addr
        .split('/')
        .reduce<Array<{ name: string; value?: string }>>((components, segment, index, segments) => {
          if (segment === 'p2p') components.push({ name: 'p2p', value: segments[index + 1] });
          return components;
        }, []),
    toString: () => addr,
  })),
}));

vi.mock('./edge-discovery', () => ({
  getBootstrapRelays: getBootstrapRelaysMock,
}));

vi.mock('./crypto/hd-wallet', () => ({
  initHDWallet: initHDWalletMock,
}));

/**
 * The nine tests that used to live here covered withHeliaStreamHandlerCompat,
 * addLegacyEventedStreamCompat and addLegacyWritableStreamCompat — a
 * hand-written bridge that made a libp2p@1 node look like the libp2p@3 API
 * helia@6 called. With one libp2p major in the tree that bridge is gone, so
 * those tests have no subject: they were measuring sdn-js's impersonation of
 * upstream, not upstream.
 *
 * What they protected is now covered where the behaviour actually lives:
 * src/stream-exchange.test.ts drives both SDN stream exchanges against REAL
 * libp2p 3 streams from @libp2p/utils' streamPair() — whole delivery of a
 * multi-megabyte payload in both directions, back-pressure via send()/onDrain(),
 * clone-on-read, and a reset peer failing instead of hanging — and
 * src/go-libp2p-interop.test.ts runs the same exchanges over a socket against a
 * real go-libp2p host.
 */
describe('createHeliaFromLibp2p', () => {
  beforeEach(() => {
    vi.resetModules();
    vi.clearAllMocks();
    createdHeliaNodes.length = 0;
  });

  it('hands Helia the libp2p instance it was given, not one of its own', async () => {
    const libp2p = { handle: vi.fn(), start: vi.fn(), stop: vi.fn(async () => undefined), status: 'started' };
    const { createHeliaFromLibp2p } = await import('./helia');
    const helia: any = await createHeliaFromLibp2p(libp2p as any);

    expect(createLibp2pMock).not.toHaveBeenCalled();
    expect(helia.libp2p).toBe(libp2p);
  });

  it('routes Helia content lookups through that libp2p', async () => {
    const libp2p = { handle: vi.fn(), stop: vi.fn(async () => undefined), status: 'started' };
    const { createHeliaFromLibp2p } = await import('./helia');
    const helia: any = await createHeliaFromLibp2p(libp2p as any);

    expect(helia.routers.map((router: any) => router.name)).toContain('libp2p-router');
  });

  it('installs bitswap', async () => {
    const libp2p = { handle: vi.fn(), stop: vi.fn(async () => undefined), status: 'started' };
    const { createHeliaFromLibp2p } = await import('./helia');
    const helia = await createHeliaFromLibp2p(libp2p as any);

    expect(withBitswapMock).toHaveBeenCalledWith(helia);
  });

  it('stops the libp2p it was given when Helia stops', async () => {
    const stop = vi.fn(async () => undefined);
    const libp2p = { handle: vi.fn(), stop, status: 'started' };
    const { createHeliaFromLibp2p } = await import('./helia');
    const helia: any = await createHeliaFromLibp2p(libp2p as any);

    await helia.stop();
    expect(stop).toHaveBeenCalledOnce();
  });

  it('starts a libp2p that was created stopped, and leaves a started one alone', async () => {
    const { createHeliaFromLibp2p } = await import('./helia');

    const startedStart = vi.fn(async () => undefined);
    await createHeliaFromLibp2p({
      handle: vi.fn(),
      start: startedStart,
      stop: vi.fn(async () => undefined),
      status: 'started',
    } as any);
    expect(startedStart).not.toHaveBeenCalled();

    const stoppedStart = vi.fn(async () => undefined);
    await createHeliaFromLibp2p({
      handle: vi.fn(),
      start: stoppedStart,
      stop: vi.fn(async () => undefined),
      status: 'stopped',
    } as any);
    expect(stoppedStart).toHaveBeenCalledOnce();
  });
});

describe('fetchCIDBytesFromHelia', () => {
  beforeEach(() => {
    vi.resetModules();
    vi.clearAllMocks();
  });

  it('stops a bounded whole-object read before retaining an oversized chunk', async () => {
    let closed = false;
    unixfsCatMock.mockImplementation(async function* () {
      try { yield new Uint8Array([1, 2]); yield new Uint8Array([3, 4]); }
      finally { closed = true; }
    });
    const { fetchCIDBytesFromHelia } = await import('./helia');
    await expect(fetchCIDBytesFromHelia({} as any,
      'bafybeictmtgyw4re2xa3afwvxi4gq3n5nmzkeoasg2ltasd53f343f3meu',
      { maxBytes: 3 })).rejects.toThrow(/limit.*3|3.*limit/i);
    expect(closed).toBe(true);
  });

  it('streams on demand and closes the iterator and provider session on early return', async () => {
    let produced = 0;
    let closed = false;
    unixfsCatMock.mockImplementation(async function* () {
      try { produced++; yield new Uint8Array([1]); produced++; yield new Uint8Array([2]); }
      finally { closed = true; }
    });
    const close = vi.fn(async () => {});
    const { streamCIDFromHelia } = await import('./helia');
    const helia = { blockstore: { createSession: () => ({ close }) }, libp2p: { peerStore: { merge: vi.fn() } } } as any;
    for await (const chunk of streamCIDFromHelia(helia,
      'bafybeictmtgyw4re2xa3afwvxi4gq3n5nmzkeoasg2ltasd53f343f3meu',
      { providerAddrs: ['/ip4/127.0.0.1/tcp/4001/p2p/12D3KooWGhZfrxQVvwQHNGRkeJhGqMbkDqjktfpBXzn47N78XY9j'] })) {
      expect([...chunk]).toEqual([1]);
      break;
    }
    expect(produced).toBe(1);
    expect(closed).toBe(true);
    expect(close).toHaveBeenCalledOnce();
  });

  it('refuses an already aborted retrieval without starting UnixFS', async () => {
    const controller = new AbortController();
    controller.abort(new Error('cancelled'));
    const { fetchCIDBytesFromHelia } = await import('./helia');
    await expect(fetchCIDBytesFromHelia({} as any,
      'bafybeictmtgyw4re2xa3afwvxi4gq3n5nmzkeoasg2ltasd53f343f3meu',
      { signal: controller.signal })).rejects.toThrow('cancelled');
    expect(unixfsCatMock).not.toHaveBeenCalled();
  });

  it('passes abort signals through to UnixFS cat', async () => {
    const controller = new AbortController();
    let observedSignal: AbortSignal | undefined;
    unixfsCatMock.mockImplementation(async function* (_cid: unknown, options: { signal?: AbortSignal } = {}) {
      observedSignal = options.signal;
      yield new Uint8Array([1, 2, 3]);
    });

    const { fetchCIDBytesFromHelia } = await import('./helia');
    const bytes = await fetchCIDBytesFromHelia(
      {} as any,
      'bafybeictmtgyw4re2xa3afwvxi4gq3n5nmzkeoasg2ltasd53f343f3meu',
      { signal: controller.signal },
    );

    expect(observedSignal).toBe(controller.signal);
    expect([...bytes]).toEqual([1, 2, 3]);
  });

  it('converts provider bootstrap multiaddrs into Bitswap provider hints', async () => {
    const providerAddr =
      '/ip4/167.172.219.213/udp/4002/quic-v1/webtransport/certhash/uEiBOyLtiqwp724bnjCPSZ9eeOM-g_65WBuRmxN53t6i10Q/certhash/uEiCqb3a2To5BYq62U_p5tjQOLjM2UUvMeCaClVXD95Jn9g/p2p/12D3KooWGhZfrxQVvwQHNGRkeJhGqMbkDqjktfpBXzn47N78XY9j';
    let observedOptions: { providers?: unknown[] } | undefined;
    unixfsCatMock.mockImplementation(async function* (_cid: unknown, options: { providers?: unknown[] } = {}) {
      observedOptions = options;
      yield new Uint8Array([4, 5, 6]);
    });

    const { fetchCIDBytesFromHelia } = await import('./helia');
    const session = {
      close: vi.fn(),
    };
    const libp2p = {
      dial: vi.fn(async () => undefined),
      peerStore: {
        merge: vi.fn(async () => undefined),
      },
    };
    const blockstore = {
      createSession: vi.fn(() => session),
    };
    const bytes = await fetchCIDBytesFromHelia(
      { blockstore, libp2p } as any,
      'bafybeictmtgyw4re2xa3afwvxi4gq3n5nmzkeoasg2ltasd53f343f3meu',
      { providerAddrs: [providerAddr], maxProviders: 0 },
    );

    expect([...bytes]).toEqual([4, 5, 6]);
    expect(libp2p.dial).not.toHaveBeenCalled();
    expect(libp2p.peerStore.merge).toHaveBeenCalledWith(
      expect.objectContaining({
        peerId: '12D3KooWGhZfrxQVvwQHNGRkeJhGqMbkDqjktfpBXzn47N78XY9j',
      }),
      {
        multiaddrs: [expect.objectContaining({ addr: providerAddr })],
      },
    );
    // helia 7 types a session provider as `CID | Multiaddr | Multiaddr[]` and
    // calls getComponents() on it, so the hint has to be the MULTIADDR. Handing
    // it a PeerId, as helia 6 accepted, throws inside the trustless-gateway
    // session and the session never becomes ready - see
    // helia-trustless-gateway.test.ts, which covers that end to end.
    expect(blockstore.createSession).toHaveBeenCalledWith(
      expect.anything(),
      expect.objectContaining({
        maxProviders: 1,
        providers: [expect.objectContaining({ addr: providerAddr })],
      }),
    );
    expect(unixfsMock).toHaveBeenCalledWith({ blockstore: session });
    expect(session.close).toHaveBeenCalled();
    expect(observedOptions?.providers).toEqual([
      expect.objectContaining({ addr: providerAddr }),
    ]);
  });
});

describe('createHeliaSDNNode', () => {
  beforeEach(() => {
    vi.resetModules();
    vi.clearAllMocks();
    createdLibp2pNodes.length = 0;
    createdHeliaNodes.length = 0;
    getBootstrapRelaysMock.mockResolvedValue([]);
  });

  it('enables verified HTTP blocks only for explicitly configured HTTPS gateway origins', async () => {
    const { createHeliaSDNNode } = await import('./helia');
    const node = await createHeliaSDNNode({
      edgeRelays: [],
      ipfsTrustlessGateways: ['https://sdn.example/', 'https://sdn.example', 'https://second.example:8443'],
    });
    expect(fallbackRouterMock).toHaveBeenCalledExactlyOnceWith({
      gateways: ['https://sdn.example', 'https://second.example:8443'],
      shuffle: false,
    });
    expect(trustlessGatewayMock).toHaveBeenCalledExactlyOnceWith();
    expect(createHeliaLightMock.mock.calls[0][0]).toMatchObject({
      blockBrokers: [{ blockBroker: 'trustless-gateway' }],
      routers: [{ name: 'fallback-router' }],
    });
    await node.stop();
  });

  it.each([undefined, []])('does not configure any HTTP gateway when omitted or empty (%j)', async (gateways) => {
    const { createHeliaSDNNode } = await import('./helia');
    const node = await createHeliaSDNNode({ edgeRelays: [], ipfsTrustlessGateways: gateways });
    expect(trustlessGatewayMock).not.toHaveBeenCalled();
    expect(fallbackRouterMock).not.toHaveBeenCalled();
    expect(createHeliaLightMock.mock.calls[0][0]).toMatchObject({
      blockBrokers: [],
      routers: [],
    });
    await node.stop();
  });

  it.each([
    'https://sdn.example', null, [null], [''], ['http://sdn.example'],
    ['https://user:password@sdn.example'], ['https://sdn.example/ipfs'],
    ['https://sdn.example?query=1'], ['https://sdn.example#fragment'],
    ['https://sdn.example', 'not a URL'],
  ])('rejects invalid gateway configuration before creating a node (%j)', async (gateways) => {
    const { createHeliaSDNNode } = await import('./helia');
    await expect(createHeliaSDNNode({ edgeRelays: [], ipfsTrustlessGateways: gateways as any }))
      .rejects.toThrow('ipfsTrustlessGateways');
    expect(createLibp2pMock).not.toHaveBeenCalled();
    expect(createHeliaLightMock).not.toHaveBeenCalled();
  });

  it('enables WebRTC-direct transport for browser-dialable full-node bootstrap addresses', async () => {
    const { createHeliaSDNNode } = await import('./helia');

    const node = await createHeliaSDNNode({
      edgeRelays: [
        '/ip4/167.172.219.213/udp/4003/webrtc-direct/certhash/uEiD8YU5I18BuOBAcE8z_3NFRoGnhu9dKdTjG7PAqVbAjEQ/p2p/16Uiu2HAm9oK2jAeVC2RMESFcYfq7BKGp2K2CCDxzoKhB5s9vpbj3',
      ],
    });

    expect(createLibp2pMock.mock.calls[0][0].transports).toEqual(
      expect.arrayContaining([expect.objectContaining({ transport: 'webRTCDirect' })]),
    );
    expect(bootstrapMock).toHaveBeenCalledWith({
      list: [
        '/ip4/167.172.219.213/udp/4003/webrtc-direct/certhash/uEiD8YU5I18BuOBAcE8z_3NFRoGnhu9dKdTjG7PAqVbAjEQ/p2p/16Uiu2HAm9oK2jAeVC2RMESFcYfq7BKGp2K2CCDxzoKhB5s9vpbj3',
      ],
    });

    await node.stop();
  });

  it('uses direct libp2p bitswap routing instead of Helia HTTP gateway defaults', async () => {
    const { createHeliaSDNNode } = await import('./helia');

    const node = await createHeliaSDNNode({
      edgeRelays: [
        '/ip4/104.131.11.220/udp/4003/webrtc-direct/certhash/uEiDHMHA60lI3WloWOnksNqBZe8J7zUcxrIV_yB6E5NBMyw/p2p/16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45',
      ],
    });

    // Bitswap over the libp2p this node built, and NO HTTP gateway broker or
    // router unless the caller named one.
    const createdLibp2p = await createLibp2pMock.mock.results[0].value;
    expect(withBitswapMock).toHaveBeenCalledTimes(1);
    expect(createHeliaLightMock.mock.calls[0][0]).toMatchObject({
      blockBrokers: [],
      routers: [],
    });
    const helia: any = createdHeliaNodes[0];
    expect(helia.libp2p).toBe(createdLibp2p);
    expect(helia.routers.map((router: any) => router.name)).toEqual(['libp2p-router']);

    await node.stop();
  });

  it('actively dials configured browser-dialable full-node bootstrap addresses before returning', async () => {
    const { createHeliaSDNNode } = await import('./helia');
    const spaceAwareAddr =
      '/ip4/104.131.11.220/udp/4003/webrtc-direct/certhash/uEiDHMHA60lI3WloWOnksNqBZe8J7zUcxrIV_yB6E5NBMyw/p2p/16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45';
    const celestrakAddr =
      '/ip4/167.172.219.213/udp/4003/webrtc-direct/certhash/uEiD8YU5I18BuOBAcE8z_3NFRoGnhu9dKdTjG7PAqVbAjEQ/p2p/16Uiu2HAm9oK2jAeVC2RMESFcYfq7BKGp2K2CCDxzoKhB5s9vpbj3';

    const node = await createHeliaSDNNode({
      edgeRelays: [spaceAwareAddr, celestrakAddr],
    });

    expect(createdLibp2pNodes[0].dial).toHaveBeenCalledTimes(2);
    expect(createdLibp2pNodes[0].dial).toHaveBeenNthCalledWith(
      1,
      expect.objectContaining({ addr: spaceAwareAddr }),
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    );
    expect(createdLibp2pNodes[0].dial).toHaveBeenNthCalledWith(
      2,
      expect.objectContaining({ addr: celestrakAddr }),
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    );

    await node.stop();
  });

  it('starts NO Kad-DHT by default — a browser DHT client wedges the renderer', async () => {
    // 2026-08-07 P1: the default-on DHT starved the main thread ~12 s after
    // load on every RF sandcastle AND on live spaceaware.io/beta. Content
    // routing is opt-in now; bitswap over directly dialled relays is how the
    // /beta catalog reads, and it needs nothing here.
    const { createHeliaSDNNode } = await import('./helia');
    const node = await createHeliaSDNNode({
      edgeRelays: ['/ip4/127.0.0.1/tcp/14080/ws/p2p/local-provider'],
    });

    expect(createLibp2pMock.mock.calls[0][0].services.dht).toBeUndefined();
    // ...and no ping service either: a browser that never runs the DHT should
    // still tell a Go peer nothing about itself.
    expect(createLibp2pMock.mock.calls[0][0].services.ping).toBeUndefined();

    await node.stop();
  });

  it('starts the DHT only when the caller opts in by name', async () => {
    const { createHeliaSDNNode } = await import('./helia');
    const node = await createHeliaSDNNode({
      edgeRelays: ['/ip4/127.0.0.1/tcp/14080/ws/p2p/local-provider'],
      enableDHT: true,
    });

    expect(createLibp2pMock.mock.calls[0][0].services.dht).toEqual({
      service: 'dht',
    });
    // kad-dht 16 declares a @libp2p/ping service dependency AND calls ping()
    // during routing-table eviction, so the DHT lane must register the real one.
    expect(createLibp2pMock.mock.calls[0][0].services.ping).toEqual({
      service: 'ping',
    });

    await node.stop();
  });

  it('can disable DHT and auto-dial for direct provider-addressed browser nodes', async () => {
    const { createHeliaSDNNode } = await import('./helia');
    const celestrakAddr =
      '/ip4/167.172.219.213/udp/4003/webrtc-direct/certhash/uEiD8YU5I18BuOBAcE8z_3NFRoGnhu9dKdTjG7PAqVbAjEQ/p2p/16Uiu2HAm9oK2jAeVC2RMESFcYfq7BKGp2K2CCDxzoKhB5s9vpbj3';

    const node = await createHeliaSDNNode({
      edgeRelays: [celestrakAddr],
      enableDHT: false,
      enableAutoDial: false,
    });

    expect(createLibp2pMock.mock.calls[0][0].services.dht).toBeUndefined();
    // libp2p 2 removed the autodialer and libp2p 3 has no `minConnections`, so
    // `enableAutoDial: false` is the upstream default now and this config
    // must NOT invent a connectionManager override for it.
    expect(createLibp2pMock.mock.calls[0][0].connectionManager).toBeUndefined();

    await node.stop();
  });
});

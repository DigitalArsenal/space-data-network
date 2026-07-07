import { beforeEach, describe, expect, it, vi } from 'vitest';

const createHeliaMock = vi.fn(async (init: any) => ({
  init,
  libp2p: init?.libp2p,
  stop: vi.fn(async () => undefined),
}));
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
const bitswapMock = vi.fn(() => ({ blockBroker: 'bitswap' }));
const libp2pRoutingMock = vi.fn((libp2p: unknown) => ({ router: 'libp2p', libp2p }));
const getBootstrapRelaysMock = vi.fn(async () => []);
const initHDWalletMock = vi.fn(async () => true);
const peerIdFromStringMock = vi.fn((peerId: string) => ({
  multihash: { bytes: new Uint8Array([1, 2, 3]) },
  peerId,
  toString: () => peerId,
}));

vi.mock('helia', () => ({
  createHelia: createHeliaMock,
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

vi.mock('@helia/block-brokers', () => ({
  bitswap: bitswapMock,
}));

vi.mock('@helia/routers', () => ({
  libp2pRouting: libp2pRoutingMock,
}));

vi.mock('@libp2p/websockets', () => ({
  webSockets: vi.fn(() => ({ transport: 'webSockets' })),
}));

vi.mock('@libp2p/websockets/filters', () => ({
  all: vi.fn(),
}));

vi.mock('@libp2p/webtransport', () => ({
  webTransport: vi.fn(() => ({ transport: 'webTransport' })),
}));

vi.mock('@spacedatanetwork/libp2p-webrtc-v1', () => ({
  webRTCDirect: vi.fn(() => ({ transport: 'webRTCDirect' })),
}));

vi.mock('@libp2p/circuit-relay-v2', () => ({
  circuitRelayTransport: vi.fn(() => ({ transport: 'relay' })),
}));

vi.mock('@libp2p/identify', () => ({
  identify: vi.fn(() => ({ service: 'identify' })),
}));

vi.mock('@chainsafe/libp2p-gossipsub', () => ({
  gossipsub: vi.fn(() => ({ service: 'pubsub' })),
}));

vi.mock('@chainsafe/libp2p-noise', () => ({
  noise: vi.fn(() => ({ encryption: 'noise' })),
}));

vi.mock('@chainsafe/libp2p-yamux', () => ({
  yamux: vi.fn(() => ({ muxer: 'yamux' })),
}));

vi.mock('@libp2p/kad-dht', () => ({
  kadDHT: vi.fn(() => ({ service: 'dht' })),
}));

vi.mock('@libp2p/peer-id', () => ({
  peerIdFromString: peerIdFromStringMock,
}));

vi.mock('@multiformats/multiaddr', () => ({
  multiaddr: vi.fn((addr: string) => ({
    addr,
    getPeerId: () => addr.split('/p2p/')[1] ?? null,
    toString: () => addr,
  })),
}));

vi.mock('./edge-discovery', () => ({
  getBootstrapRelays: getBootstrapRelaysMock,
}));

vi.mock('./crypto/hd-wallet', () => ({
  initHDWallet: initHDWalletMock,
}));

describe('createHeliaFromLibp2p', () => {
  beforeEach(() => {
    vi.resetModules();
    vi.clearAllMocks();
  });

  it('adapts two-argument stream handlers to incoming stream data objects', async () => {
    const incoming = {
      stream: { id: 'stream-1', protocol: '/ipfs/bitswap/1.2.0' },
      connection: { remotePeer: 'peer-1' },
    };
    const originalHandle = vi.fn(async (_protocols, handler) => {
      handler(incoming);
    });

    const { createHeliaFromLibp2p } = await import('./helia');
    await createHeliaFromLibp2p({ handle: originalHandle } as any);

    const patchedLibp2p = createHeliaMock.mock.calls[0][0].libp2p;
    const spy = vi.fn();
    const handler = function (stream: unknown, connection: unknown) {
      spy(stream, connection);
    };
    await patchedLibp2p.handle('/ipfs/bitswap/1.2.0', handler, {});

    expect(spy).toHaveBeenCalledWith(incoming.stream, incoming.connection);
  });

  it('leaves single-argument stream handlers untouched', async () => {
    const incoming = {
      stream: { id: 'stream-1', protocol: '/ipfs/bitswap/1.2.0' },
      connection: { remotePeer: 'peer-1' },
    };
    const originalHandle = vi.fn(async (_protocols, handler) => {
      handler(incoming);
    });

    const { createHeliaFromLibp2p } = await import('./helia');
    await createHeliaFromLibp2p({ handle: originalHandle } as any);

    const patchedLibp2p = createHeliaMock.mock.calls[0][0].libp2p;
    const handler = vi.fn();
    await patchedLibp2p.handle('/test/1.0.0', handler, {});

    expect(handler).toHaveBeenCalledWith(incoming);
    expect(handler.mock.calls[0]).toHaveLength(1);
  });

  it('adapts outgoing protocol streams to the legacy write API Helia Bitswap expects', async () => {
    const written: Uint8Array[] = [];
    let sourceEnded = false;
    const originalClose = vi.fn(async () => undefined);
    const stream = {
      sink: vi.fn(async (source: AsyncIterable<Uint8Array>) => {
        for await (const chunk of source) {
          written.push(chunk.slice());
        }
        sourceEnded = true;
      }),
      close: originalClose,
      closeRead: vi.fn(async () => undefined),
      closeWrite: vi.fn(async () => undefined),
    };
    const originalDialProtocol = vi.fn(async () => stream);

    const { createHeliaFromLibp2p } = await import('./helia');
    await createHeliaFromLibp2p({
      handle: vi.fn(),
      dialProtocol: originalDialProtocol,
    } as any);

    const patchedLibp2p = createHeliaMock.mock.calls[0][0].libp2p;
    const patchedStream = await patchedLibp2p.dialProtocol(
      'peer-1',
      '/ipfs/bitswap/1.2.0',
    );

    expect(patchedStream.send).toEqual(expect.any(Function));
    expect(patchedStream.onDrain).toEqual(expect.any(Function));
    expect(patchedStream.send(new Uint8Array([7, 8, 9]))).toBe(true);
    await patchedStream.close();

    expect(stream.sink).toHaveBeenCalledTimes(1);
    expect(written.map((chunk) => [...chunk])).toEqual([[7, 8, 9]]);
    expect(sourceEnded).toBe(true);
    expect(originalClose).toHaveBeenCalled();
  });

  it('adapts incoming source streams to the legacy event API Helia Bitswap expects', async () => {
    const received: Uint8Array[] = [];
    const originalClose = vi.fn(async () => undefined);
    const stream = {
      close: originalClose,
      async *source() {
        yield new Uint8Array([10, 11, 12]);
      },
    };
    const incoming = {
      stream,
      connection: { remotePeer: 'peer-1' },
    };
    const originalHandle = vi.fn(async (_protocols, handler) => {
      await handler(incoming);
    });

    const { createHeliaFromLibp2p } = await import('./helia');
    await createHeliaFromLibp2p({ handle: originalHandle } as any);

    const patchedLibp2p = createHeliaMock.mock.calls[0][0].libp2p;
    await patchedLibp2p.handle(
      '/ipfs/bitswap/1.2.0',
      async (adaptedStream: any, _connection: unknown) => {
        await adaptedStream.close();
        const done = new Promise<void>((resolve) => {
          adaptedStream.addEventListener('remoteCloseWrite', resolve);
        });
        adaptedStream.addEventListener('message', (event: { data: Uint8Array }) => {
          received.push(event.data.slice());
        });
        await done;
      },
      {},
    );

    expect(originalClose).not.toHaveBeenCalled();
    expect(received.map((chunk) => [...chunk])).toEqual([[10, 11, 12]]);
  });
});

describe('fetchCIDBytesFromHelia', () => {
  beforeEach(() => {
    vi.resetModules();
    vi.clearAllMocks();
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
    expect(blockstore.createSession).toHaveBeenCalledWith(
      expect.anything(),
      expect.objectContaining({
        maxProviders: 1,
        providers: [
          expect.objectContaining({
            peerId: '12D3KooWGhZfrxQVvwQHNGRkeJhGqMbkDqjktfpBXzn47N78XY9j',
            toMultihash: expect.any(Function),
          }),
        ],
      }),
    );
    expect(unixfsMock).toHaveBeenCalledWith({ blockstore: session });
    expect(session.close).toHaveBeenCalled();
    expect(observedOptions?.providers).toEqual([
      expect.objectContaining({
        peerId: '12D3KooWGhZfrxQVvwQHNGRkeJhGqMbkDqjktfpBXzn47N78XY9j',
        toMultihash: expect.any(Function),
      }),
    ]);
  });
});

describe('createHeliaSDNNode', () => {
  beforeEach(() => {
    vi.resetModules();
    vi.clearAllMocks();
    createdLibp2pNodes.length = 0;
    getBootstrapRelaysMock.mockResolvedValue([]);
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

    expect(bitswapMock).toHaveBeenCalledWith();
    const createdLibp2p = await createLibp2pMock.mock.results[0].value;
    expect(libp2pRoutingMock).toHaveBeenCalledWith(createdLibp2p);
    expect(createHeliaMock.mock.calls[0][0]).toMatchObject({
      blockBrokers: [{ blockBroker: 'bitswap' }],
      routers: [{ router: 'libp2p' }],
    });

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
    expect(createLibp2pMock.mock.calls[0][0].connectionManager).toMatchObject({
      minConnections: 0,
    });

    await node.stop();
  });
});

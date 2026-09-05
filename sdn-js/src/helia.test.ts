import { beforeEach, describe, expect, it, vi } from 'vitest';
import { Uint8ArrayList } from 'uint8arraylist';

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

  async function writable(sink: (source: AsyncIterable<Uint8Array>) => Promise<void>, close = vi.fn(async () => {})) {
    const { createHeliaFromLibp2p } = await import('./helia');
    await createHeliaFromLibp2p({ handle: vi.fn(), dialProtocol: async () => ({ sink, close }) } as any);
    return createHeliaMock.mock.calls[0][0].libp2p.dialProtocol('peer', '/test');
  }

  it('backpressures a stalled sink and refuses writes beyond its byte budget', async () => {
    let release!: () => void;
    const blocked = new Promise<void>((resolve) => { release = resolve; });
    let received = 0;
    const stream = await writable(async (source) => {
      await blocked;
      for await (const chunk of source) received += chunk.byteLength;
    });
    expect(stream.send(new Uint8Array(1024 * 1024))).toBe(false);
    expect(() => stream.send(new Uint8Array(8 * 1024 * 1024))).toThrow(/buffer|limit|budget/i);
    let drained = false;
    const drain = stream.onDrain().then(() => { drained = true; });
    await Promise.resolve();
    expect(drained).toBe(false);
    release();
    await drain;
    await stream.close();
    expect(received).toBe(1024 * 1024);
  });

  it('propagates sink failure to drain, subsequent writes and close', async () => {
    const close = vi.fn(async () => {});
    const stream = await writable(async () => { throw new Error('sink failed'); }, close);
    stream.send(new Uint8Array(1024 * 1024));
    await expect(stream.onDrain()).rejects.toThrow('sink failed');
    expect(() => stream.send(new Uint8Array([1]))).toThrow('sink failed');
    await expect(stream.close()).rejects.toThrow('sink failed');
    expect(close).toHaveBeenCalledOnce();
  });

  it('accepts the length-prefixed Uint8ArrayList used by real Bitswap', async () => {
    const received: number[] = [];
    const stream = await writable(async (source) => {
      for await (const chunk of source) received.push(...chunk);
    });
    const chunk = new Uint8ArrayList(new Uint8Array([2]), new Uint8Array([7, 8]));
    expect(stream.send(chunk)).toBe(true);
    chunk.set(1, 99);
    await stream.close();
    expect(received).toEqual([2, 7, 8]);
  });

  it('aborts queued writes and releases a pending close even if the sink stalls', async () => {
    const stream = await writable(async () => new Promise<void>(() => {}));
    stream.send(new Uint8Array([1]));
    const closing = stream.close();
    stream.abort(new Error('transport cancelled'));
    await expect(closing).rejects.toThrow('transport cancelled');
    expect(() => stream.send(new Uint8Array([2]))).toThrow('transport cancelled');
  });

  it('cancels a drain waiter without losing the accepted bytes', async () => {
    let release!: () => void;
    const blocked = new Promise<void>((resolve) => { release = resolve; });
    let received = 0;
    const stream = await writable(async (source) => {
      await blocked;
      for await (const chunk of source) received += chunk.byteLength;
    });
    stream.send(new Uint8Array(1024 * 1024));
    const controller = new AbortController();
    const drain = stream.onDrain({ signal: controller.signal });
    controller.abort(new Error('cancelled'));
    await expect(drain).rejects.toThrow('cancelled');
    release();
    await stream.close();
    expect(received).toBe(1024 * 1024);
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

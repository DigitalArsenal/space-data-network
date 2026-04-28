import { beforeEach, describe, expect, it, vi } from 'vitest';

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
  readonly getBestRelays = vi.fn((count: number) => this.relays.slice(0, count));

  constructor(initialRelays: string[] = []) {
    this.relays = initialRelays.slice();
    edgeDiscoveryInstances.push(this);
  }
}

vi.mock('libp2p', () => ({
  createLibp2p: createLibp2pMock,
}));

vi.mock('@libp2p/bootstrap', () => ({
  bootstrap: bootstrapMock,
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

vi.mock('@libp2p/circuit-relay-v2', () => ({
  circuitRelayTransport: vi.fn(() => ({ transport: 'relay' })),
}));

vi.mock('@libp2p/identify', () => ({
  identify: vi.fn(() => ({ service: 'identify' })),
}));

vi.mock('@chainsafe/libp2p-gossipsub', () => ({
  gossipsub: vi.fn(() => ({ service: 'pubsub' })),
  GossipSub: class {},
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

vi.mock('./edge-discovery', () => ({
  getBootstrapRelays: getBootstrapRelaysMock,
  EdgeDiscovery: MockEdgeDiscovery,
}));

vi.mock('./crypto/hd-wallet', () => ({
  initHDWallet: initHDWalletMock,
}));

vi.mock('./helia', () => ({
  createHeliaFromLibp2p: createHeliaFromLibp2pMock,
  fetchCIDBytesFromHelia: fetchCIDBytesFromHeliaMock,
}));

describe('SDNNode relay bootstrap', () => {
  beforeEach(() => {
    vi.resetModules();
    vi.clearAllMocks();
    edgeDiscoveryInstances.length = 0;
    vi.restoreAllMocks();
    getBootstrapRelaysMock.mockResolvedValue(['/dns4/bootstrap.example/tcp/443/wss/p2p/bootstrap-peer']);
    createLibp2pMock.mockResolvedValue({
      peerId: { toString: () => 'test-peer-id' },
      services: {},
      addEventListener: vi.fn(),
      start: vi.fn(async () => undefined),
      stop: vi.fn(async () => undefined),
      getPeers: vi.fn(() => []),
    });
    createHeliaFromLibp2pMock.mockResolvedValue({ stop: vi.fn(async () => undefined) });
    fetchCIDBytesFromHeliaMock.mockResolvedValue(new Uint8Array([1, 2, 3]));
  });

  it('treats explicit edge relays as authoritative by default', async () => {
    const { SDNNode } = await import('./node');
    const explicitRelay = '/ip4/127.0.0.1/tcp/14080/ws/p2p/local-provider';

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

  it('keeps discovery probing for the default bootstrap path', async () => {
    const { SDNNode, IPFS_BOOTSTRAP_PEERS } = await import('./node');

    const node = await SDNNode.create({
      enableStorage: false,
    });

    expect(getBootstrapRelaysMock).toHaveBeenCalledTimes(1);
    expect(edgeDiscoveryInstances).toHaveLength(1);
    expect(edgeDiscoveryInstances[0].probeAllRelays).toHaveBeenCalledTimes(1);
    expect(edgeDiscoveryInstances[0].startProbing).toHaveBeenCalledTimes(1);
    expect(bootstrapMock).toHaveBeenCalledWith({
      list: [...IPFS_BOOTSTRAP_PEERS, '/dns4/bootstrap.example/tcp/443/wss/p2p/bootstrap-peer'],
    });

    await node.stop();
  });

  it('reuses the in-flight helia initialization across concurrent CID fetches', async () => {
    let resolveHelia: ((value: { stop: () => Promise<void> }) => void) | null = null;
    createHeliaFromLibp2pMock.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveHelia = resolve;
        }),
    );

    const { SDNNode } = await import('./node');
    const node = await SDNNode.create({
      edgeRelays: ['/ip4/127.0.0.1/tcp/14080/ws/p2p/local-provider'],
      enableStorage: false,
    });

    const firstFetch = node.fetchCIDBytes('bafkreicidone');
    const secondFetch = node.fetchCIDBytes('bafkreicidtwo');

    expect(createHeliaFromLibp2pMock).toHaveBeenCalledTimes(1);

    resolveHelia?.({ stop: vi.fn(async () => undefined) });
    await Promise.all([firstFetch, secondFetch]);

    expect(createHeliaFromLibp2pMock).toHaveBeenCalledTimes(1);
    expect(fetchCIDBytesFromHeliaMock).toHaveBeenCalledTimes(2);

    await node.stop();
  });

  it('uses the configured IPFS API for CID fetches without starting Helia', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      arrayBuffer: async () => Uint8Array.from([9, 8, 7]).buffer,
    } as Response);

    const { SDNNode } = await import('./node');
    const node = await SDNNode.create({
      edgeRelays: ['/ip4/127.0.0.1/tcp/14080/ws/p2p/local-provider'],
      enableStorage: false,
      ipfsApiBaseUrl: '/api/v0',
    });

    const result = await node.fetchCIDBytes('bafkreicidviahttp');

    expect(result).toEqual(Uint8Array.from([9, 8, 7]));
    expect(fetchMock).toHaveBeenCalledWith(
      expect.objectContaining({
        href: 'http://localhost/api/v0/cat?arg=bafkreicidviahttp',
      }),
      expect.objectContaining({
        method: 'POST',
        headers: { accept: 'application/octet-stream' },
      }),
    );
    expect(createHeliaFromLibp2pMock).not.toHaveBeenCalled();
    expect(fetchCIDBytesFromHeliaMock).not.toHaveBeenCalled();

    await node.stop();
  });

  it('times out a configured IPFS API CID fetch without starting Helia', async () => {
    const fetchMock = vi
      .spyOn(globalThis, 'fetch')
      .mockImplementationOnce(() => new Promise(() => undefined));

    const { SDNNode } = await import('./node');
    const node = await SDNNode.create({
      edgeRelays: ['/ip4/127.0.0.1/tcp/14080/ws/p2p/local-provider'],
      enableStorage: false,
      ipfsApiBaseUrl: '/api/v0',
      ipfsFetchTimeoutMs: 1,
    });

    await expect(node.fetchCIDBytes('bafkreihttphangcid')).rejects.toThrow(/timed out/i);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(createHeliaFromLibp2pMock).not.toHaveBeenCalled();
    expect(fetchCIDBytesFromHeliaMock).not.toHaveBeenCalled();

    await node.stop();
  });

  it('falls back from IPFS API to gateway fetch before starting Helia', async () => {
    const fetchMock = vi
      .spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce({
        ok: false,
        status: 502,
        statusText: 'Bad Gateway',
        text: async () => 'api unavailable',
      } as Response)
      .mockResolvedValueOnce({
        ok: true,
        arrayBuffer: async () => Uint8Array.from([4, 2]).buffer,
      } as Response);

    const { SDNNode } = await import('./node');
    const node = await SDNNode.create({
      edgeRelays: ['/ip4/127.0.0.1/tcp/14080/ws/p2p/local-provider'],
      enableStorage: false,
      ipfsApiBaseUrl: 'https://provider.example/api/v0',
      ipfsGatewayBaseUrl: 'https://provider.example/ipfs',
      ipfsFetchTimeoutMs: 1_000,
    });

    const result = await node.fetchCIDBytes('bafkreifallbackcid');

    expect(result).toEqual(Uint8Array.from([4, 2]));
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(String((fetchMock.mock.calls[0]?.[0] as URL).href)).toBe(
      'https://provider.example/api/v0/cat?arg=bafkreifallbackcid',
    );
    expect(String((fetchMock.mock.calls[1]?.[0] as URL).href)).toBe(
      'https://provider.example/ipfs/bafkreifallbackcid',
    );
    expect(createHeliaFromLibp2pMock).not.toHaveBeenCalled();

    await node.stop();
  });

  it('falls back from gateway fetch to Helia when HTTP transports fail', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: false,
      status: 504,
      statusText: 'Gateway Timeout',
      text: async () => 'gateway timeout',
    } as Response);
    fetchCIDBytesFromHeliaMock.mockResolvedValueOnce(Uint8Array.from([7, 7, 7]));

    const { SDNNode } = await import('./node');
    const node = await SDNNode.create({
      edgeRelays: ['/ip4/127.0.0.1/tcp/14080/ws/p2p/local-provider'],
      enableStorage: false,
      ipfsGatewayBaseUrl: 'https://provider.example/ipfs',
      ipfsFetchTimeoutMs: 1_000,
    });

    const result = await node.fetchCIDBytes('bafkreiheliacid');

    expect(result).toEqual(Uint8Array.from([7, 7, 7]));
    expect(createHeliaFromLibp2pMock).toHaveBeenCalledTimes(1);
    expect(fetchCIDBytesFromHeliaMock).toHaveBeenCalledTimes(1);

    await node.stop();
  });

  it('applies a finite default timeout to Helia CID fetches', async () => {
    vi.useFakeTimers();
    fetchCIDBytesFromHeliaMock.mockImplementationOnce(() => new Promise(() => undefined));

    const { SDNNode } = await import('./node');
    const node = await SDNNode.create({
      edgeRelays: ['/ip4/127.0.0.1/tcp/14080/ws/p2p/local-provider'],
      enableStorage: false,
    });

    const fetchPromise = expect(node.fetchCIDBytes('bafkreiheliadefaulttimeout')).rejects.toThrow(/timed out after 60000ms/i);
    await vi.advanceTimersByTimeAsync(60_000);
    await fetchPromise;

    await node.stop();
    vi.useRealTimers();
  });

  it('half-closes protocol request streams before reading the response', async () => {
    const responseBytes = Uint8Array.from([4, 5, 6]);
    const sink = vi.fn(async (source: AsyncIterable<Uint8Array>) => {
      const chunks: Uint8Array[] = [];
      for await (const chunk of source) {
        chunks.push(chunk);
      }
      expect(chunks).toEqual([Uint8Array.from([1, 2, 3])]);
    });
    const closeWrite = vi.fn(async () => undefined);
    const close = vi.fn(async () => undefined);
    const stream = {
      sink,
      closeWrite,
      close,
      source: (async function *source() {
        yield responseBytes;
      })(),
    };
    const dialProtocol = vi.fn(async () => stream);

    createLibp2pMock.mockResolvedValueOnce({
      peerId: { toString: () => 'test-peer-id' },
      services: {},
      addEventListener: vi.fn(),
      start: vi.fn(async () => undefined),
      stop: vi.fn(async () => undefined),
      getPeers: vi.fn(() => []),
      dialProtocol,
    });

    const { SDNNode } = await import('./node');
    const node = await SDNNode.create({
      edgeRelays: ['/ip4/127.0.0.1/tcp/14080/ws/p2p/local-provider'],
      enableStorage: false,
    });

    const result = await node.dialProtocol(
      '16Uiu2HAm9xJ3JeeWsvLKGdyM7H6SKApGCV55zGfA9di4J5NaKdsf',
      '/space-data-network/module-delivery/1.0.0',
      Uint8Array.from([1, 2, 3]),
      ['/ip4/127.0.0.1/tcp/14080/ws'],
    );

    expect(result).toEqual(responseBytes);
    expect(closeWrite).toHaveBeenCalledTimes(1);
    expect(close).toHaveBeenCalledTimes(1);
    expect(closeWrite.mock.invocationCallOrder[0]).toBeLessThan(
      close.mock.invocationCallOrder[0],
    );

    await node.stop();
  });
});

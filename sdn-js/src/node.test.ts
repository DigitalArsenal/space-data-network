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

  it('falls back to the configured IPFS API when Helia CID fetch fails', async () => {
    createHeliaFromLibp2pMock.mockResolvedValue({ stop: vi.fn(async () => undefined) });
    fetchCIDBytesFromHeliaMock.mockRejectedValueOnce(new Error('helia failed'));
    const fetchMock = vi
      .spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(
        new Response(Uint8Array.from([9, 8, 7]), {
          status: 200,
          headers: { 'Content-Type': 'application/octet-stream' },
        }),
      );

    const { SDNNode } = await import('./node');
    const node = await SDNNode.create({
      edgeRelays: ['/ip4/127.0.0.1/tcp/14080/ws/p2p/local-provider'],
      enableStorage: false,
      ipfsApiBaseUrl: '/api/v0',
    });

    const result = await node.fetchCIDBytes('bafkreifallbackcid');

    expect(result).toEqual(Uint8Array.from([9, 8, 7]));
    expect(fetchMock).toHaveBeenCalledWith('/api/v0/cat?arg=bafkreifallbackcid', {
      method: 'POST',
    });

    await node.stop();
  });

  it('prefers the configured IPFS API over Helia CID fetches when an API base URL is available', async () => {
    createHeliaFromLibp2pMock.mockResolvedValue({ stop: vi.fn(async () => undefined) });
    const fetchMock = vi
      .spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(
        new Response(Uint8Array.from([2, 4, 6]), {
          status: 200,
          headers: { 'Content-Type': 'application/octet-stream' },
        }),
      );

    const { SDNNode } = await import('./node');
    const node = await SDNNode.create({
      edgeRelays: ['/ip4/127.0.0.1/tcp/14080/ws/p2p/local-provider'],
      enableStorage: false,
      ipfsApiBaseUrl: '/api/v0',
    });

    const result = await node.fetchCIDBytes('bafkreiapipreferredcid');

    expect(result).toEqual(Uint8Array.from([2, 4, 6]));
    expect(fetchCIDBytesFromHeliaMock).not.toHaveBeenCalled();
    expect(fetchMock).toHaveBeenCalledWith('/api/v0/cat?arg=bafkreiapipreferredcid', {
      method: 'POST',
    });

    await node.stop();
  });

  it('falls back to the configured IPFS API when Helia CID fetch does not resolve before the timeout', async () => {
    createHeliaFromLibp2pMock.mockResolvedValue({ stop: vi.fn(async () => undefined) });
    fetchCIDBytesFromHeliaMock.mockImplementationOnce(
      () => new Promise(() => undefined),
    );
    const fetchMock = vi
      .spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(
        new Response(Uint8Array.from([4, 5, 6]), {
          status: 200,
          headers: { 'Content-Type': 'application/octet-stream' },
        }),
      );

    const { SDNNode } = await import('./node');
    const node = await SDNNode.create({
      edgeRelays: ['/ip4/127.0.0.1/tcp/14080/ws/p2p/local-provider'],
      enableStorage: false,
      ipfsApiBaseUrl: '/api/v0',
      ipfsFetchTimeoutMs: 1,
    });

    const result = await node.fetchCIDBytes('bafkreiheliahangcid');

    expect(result).toEqual(Uint8Array.from([4, 5, 6]));
    expect(fetchMock).toHaveBeenCalledWith('/api/v0/cat?arg=bafkreiheliahangcid', {
      method: 'POST',
    });

    await node.stop();
  });
});

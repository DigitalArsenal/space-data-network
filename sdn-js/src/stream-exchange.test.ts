/**
 * The SDN request/response stream exchange, against REAL libp2p 3 streams.
 *
 * Every SDN protocol — flatsql-sync, module-delivery, id-exchange — is one
 * exchange: write the whole request, half-close the write side, read the reply
 * to EOF. There are exactly two implementations of it in this package,
 * `exchangeStream` in node.ts and `performFlatSqlSyncStreamExchange` in
 * ui/runtime/sdn-backend-libp2p-sync.ts, and both were rewritten for libp2p 3,
 * which replaced the duplex `sink`/`source` pair with an event-driven
 * MessageStream: `send()` signals back-pressure by returning false, `onDrain()`
 * waits for the buffer, `close()` closes only the write side, and the stream
 * itself is the async iterable of inbound messages.
 *
 * These tests drive the real `@libp2p/utils` `streamPair()` — the same
 * `AbstractStream` implementation yamux builds on — rather than a hand-written
 * double, so they fail if the semantics this code relies on change under it.
 *
 * They replace the nine tests in helia.test.ts that covered
 * `addLegacyWritableStreamCompat` / `addLegacyEventedStreamCompat`: those
 * measured a hand-written bridge between libp2p 1 and libp2p 3 that no longer
 * exists. The behaviour they were protecting — a large payload is delivered
 * whole, back-pressure is respected rather than dropped, a failed exchange
 * tears the stream down instead of hanging — is what is checked here, on the
 * code that now carries it.
 */

import { describe, expect, it, vi, beforeEach } from 'vitest';
import { streamPair } from '@libp2p/utils';
import type { Stream } from '@libp2p/interface';

import { exchangeFlatSqlSyncStream } from './ui/runtime/sdn-backend-libp2p-sync';

const createLibp2pMock = vi.fn();
const getBootstrapRelaysMock = vi.fn(async () => [] as string[]);
const initHDWalletMock = vi.fn(async () => true);

class MockEdgeDiscovery {
  readonly relays: string[];
  readonly probeAllRelays = vi.fn(async () => new Map());
  readonly startProbing = vi.fn();
  readonly getBestRelays = vi.fn((count: number) => this.relays.slice(0, count));
  readonly stopProbing = vi.fn();
  constructor(initialRelays: string[] = []) {
    this.relays = initialRelays.slice();
  }
}

vi.mock('libp2p', () => ({ createLibp2p: createLibp2pMock }));
vi.mock('@libp2p/bootstrap', () => ({ bootstrap: vi.fn(() => ({ peerDiscovery: 'bootstrap' })) }));
vi.mock('@libp2p/websockets', () => ({ webSockets: vi.fn(() => ({ transport: 'webSockets' })) }));
vi.mock('@libp2p/webtransport', () => ({ webTransport: vi.fn(() => ({ transport: 'webTransport' })) }));
vi.mock('@libp2p/webrtc', () => ({
  webRTC: vi.fn(() => ({ transport: 'webRTC' })),
  webRTCDirect: vi.fn(() => ({ transport: 'webRTCDirect' })),
}));
vi.mock('@libp2p/circuit-relay-v2', () => ({ circuitRelayTransport: vi.fn(() => ({ transport: 'relay' })) }));
vi.mock('@libp2p/identify', () => ({ identify: vi.fn(() => ({ service: 'identify' })) }));
vi.mock('@libp2p/ping', () => ({ ping: vi.fn(() => ({ service: 'ping' })) }));
vi.mock('@libp2p/gossipsub', () => ({
  gossipsub: vi.fn(() => ({ service: 'pubsub' })),
  GossipSub: class {},
}));
vi.mock('@libp2p/noise', () => ({ noise: vi.fn(() => ({ encryption: 'noise' })) }));
vi.mock('@libp2p/yamux', () => ({ yamux: vi.fn(() => ({ muxer: 'yamux' })) }));
vi.mock('@libp2p/kad-dht', () => ({ kadDHT: vi.fn(() => ({ service: 'dht' })) }));
vi.mock('./edge-discovery', () => ({
  getBootstrapRelays: getBootstrapRelaysMock,
  EdgeDiscovery: MockEdgeDiscovery,
}));
vi.mock('./crypto/hd-wallet', () => ({ initHDWallet: initHDWalletMock }));
vi.mock('./helia', () => ({
  createHeliaFromLibp2p: vi.fn(async () => ({})),
  fetchCIDBytesFromHelia: vi.fn(),
}));

const PEER = '12D3KooWRD6km1Tm6dfmpcUX1Uu1kM2rcMYPSk9HRyN3btNPRLym';
const PROTOCOL = '/space-data-network/flatsql-sync/1.0.0';

/**
 * Run the SDN handler side of an exchange on the far end of a stream pair:
 * read the request to EOF, reply once, close.
 */
async function serveOnce(
  stream: Stream,
  reply: (request: Uint8Array) => Uint8Array,
): Promise<Uint8Array> {
  const chunks: Uint8Array[] = [];
  for await (const chunk of stream) {
    chunks.push(chunk instanceof Uint8Array ? chunk.slice() : chunk.subarray().slice());
  }
  const request = concat(chunks);
  const response = reply(request);
  if (response.byteLength > 0 && !stream.send(response)) {
    await stream.onDrain();
  }
  await stream.close();
  return request;
}

function concat(chunks: Uint8Array[]): Uint8Array {
  const total = chunks.reduce((sum, chunk) => sum + chunk.byteLength, 0);
  const out = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    out.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return out;
}

async function createNodeOver(stream: Stream) {
  createLibp2pMock.mockResolvedValue({
    peerId: { toString: () => 'local-peer' },
    services: {},
    addEventListener: vi.fn(),
    start: vi.fn(async () => undefined),
    stop: vi.fn(async () => undefined),
    getPeers: vi.fn(() => []),
    getConnections: vi.fn(() => []),
    dial: vi.fn(async () => ({})),
    dialProtocol: vi.fn(async () => stream),
  });
  const { SDNNode } = await import('./node');
  return SDNNode.create({
    edgeRelays: ['/ip4/127.0.0.1/tcp/4001/ws/p2p/relay'],
    includeIPFSBootstrap: false,
    enableStorage: false,
    enableRelayProbing: false,
  });
}

describe('SDNNode.dialProtocol over a real libp2p 3 stream', () => {
  beforeEach(() => {
    vi.resetModules();
    vi.clearAllMocks();
  });

  it('writes the request, half-closes, and reads the reply to EOF', async () => {
    const [outbound, inbound] = await streamPair({ protocol: PROTOCOL });
    const served = serveOnce(inbound, (request) =>
      new TextEncoder().encode(`echo:${new TextDecoder().decode(request)}`),
    );

    const node = await createNodeOver(outbound);
    const reply = await node.dialProtocol(PEER, PROTOCOL, new TextEncoder().encode('request-1'));

    expect(new TextDecoder().decode(reply)).toBe('echo:request-1');
    expect(new TextDecoder().decode(await served)).toBe('request-1');
    await node.stop();
  });

  it('delivers a payload far larger than one write buffer without truncating it', async () => {
    // Big enough that send() has to report back-pressure at least once, which
    // is the case the old sink() handled internally and the caller must now
    // handle by awaiting onDrain().
    const size = 4 * 1024 * 1024;
    const payload = new Uint8Array(size);
    for (let i = 0; i < size; i++) payload[i] = (i * 17 + 3) & 0xff;

    const [outbound, inbound] = await streamPair({ protocol: PROTOCOL });
    const served = serveOnce(inbound, () => new Uint8Array([0xff]));

    const node = await createNodeOver(outbound);
    const reply = await node.dialProtocol(PEER, PROTOCOL, payload);

    expect([...reply]).toEqual([0xff]);
    const received = await served;
    expect(received.byteLength).toBe(size);
    expect(received).toEqual(payload);
    await node.stop();
  }, 60_000);

  it('reads a reply far larger than one read buffer without truncating it', async () => {
    const size = 4 * 1024 * 1024;
    const response = new Uint8Array(size);
    for (let i = 0; i < size; i++) response[i] = (i * 29 + 11) & 0xff;

    const [outbound, inbound] = await streamPair({ protocol: PROTOCOL });
    void serveOnce(inbound, () => response);

    const node = await createNodeOver(outbound);
    const reply = await node.dialProtocol(PEER, PROTOCOL, new Uint8Array([1]));

    expect(reply.byteLength).toBe(size);
    expect(reply).toEqual(response);
    await node.stop();
  }, 60_000);

  it('copies every chunk out of libp2p buffers before returning it', async () => {
    // libp2p reuses receive buffers; the reply must not alias one. Mutating the
    // sender's view after the exchange must not change what the caller holds.
    const [outbound, inbound] = await streamPair({ protocol: PROTOCOL });
    const sent = new Uint8Array([1, 2, 3, 4]);
    void serveOnce(inbound, () => sent);

    const node = await createNodeOver(outbound);
    const reply = await node.dialProtocol(PEER, PROTOCOL, new Uint8Array([0]));
    sent.fill(0xaa);

    expect([...reply]).toEqual([1, 2, 3, 4]);
    await node.stop();
  });

  it('rejects rather than hanging when the peer resets the stream', async () => {
    const [outbound, inbound] = await streamPair({ protocol: PROTOCOL });
    const node = await createNodeOver(outbound);

    inbound.abort(new Error('peer went away'));

    await expect(
      node.dialProtocol(PEER, PROTOCOL, new Uint8Array([1, 2, 3])),
    ).rejects.toThrow();
    await node.stop();
  }, 30_000);
});

describe('exchangeFlatSqlSyncStream over a real libp2p 3 stream', () => {
  it('writes the request, half-closes, and reads the reply to EOF', async () => {
    const [outbound, inbound] = await streamPair({ protocol: PROTOCOL });
    const served = serveOnce(inbound, (request) => new Uint8Array([...request].reverse()));

    const reply = await exchangeFlatSqlSyncStream(outbound, new Uint8Array([1, 2, 3, 4]));

    expect([...reply]).toEqual([4, 3, 2, 1]);
    expect([...(await served)]).toEqual([1, 2, 3, 4]);
  });

  it('carries a multi-megabyte shard whole', async () => {
    const size = 4 * 1024 * 1024;
    const shard = new Uint8Array(size);
    for (let i = 0; i < size; i++) shard[i] = (i * 13 + 5) & 0xff;

    const [outbound, inbound] = await streamPair({ protocol: PROTOCOL });
    void serveOnce(inbound, () => shard);

    const reply = await exchangeFlatSqlSyncStream(outbound, new Uint8Array([7]));

    expect(reply.byteLength).toBe(size);
    expect(reply).toEqual(shard);
  }, 60_000);

  it('bounds an exchange whose peer never answers', async () => {
    const [outbound] = await streamPair({ protocol: PROTOCOL });
    await expect(
      exchangeFlatSqlSyncStream(outbound, new Uint8Array([1]), { timeoutMs: 50, label: 'probe' }),
    ).rejects.toThrow(/probe timed out after 50 ms/);
  }, 30_000);
});

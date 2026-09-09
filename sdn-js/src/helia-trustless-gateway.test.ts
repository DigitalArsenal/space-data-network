import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { createLibp2p } from 'libp2p';
import { unixfs } from '@helia/unixfs';
import { CID } from 'multiformats/cid';
import { sha256 } from 'multiformats/hashes/sha2';
import type { Helia } from 'helia';
import { createHeliaFromLibp2p, fetchCIDBytesFromHelia } from './helia';

const gateway = 'https://catalog-gateway.example';
const metadata = new TextEncoder().encode('immutable publication metadata');
const artifact = Uint8Array.from({ length: 1_100_000 }, (_, i) => i % 251);
let producer: Helia;
let metadataCid: CID;
let artifactCid: CID;
const clients: Helia[] = [];

async function peerlessHelia(gateways?: string[]): Promise<Helia> {
  // Real Helia/brokers/UnixFS/verifier, with no listeners, peers or DHT.
  const libp2p = await createLibp2p({ addresses: { listen: [] }, transports: [] });
  return createHeliaFromLibp2p(libp2p, { ipfsTrustlessGateways: gateways });
}

async function rawBlock(cid: CID): Promise<Uint8Array> {
  const chunks: Uint8Array[] = [];
  for await (const chunk of producer.blockstore.get(cid)) chunks.push(chunk);
  const bytes = new Uint8Array(chunks.reduce((total, chunk) => total + chunk.length, 0));
  let offset = 0;
  for (const chunk of chunks) {
    bytes.set(chunk, offset);
    offset += chunk.length;
  }
  return bytes;
}

function serveGateway(corrupt?: (cid: CID) => boolean) {
  const requests: URL[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(input instanceof Request ? input.url : String(input));
    requests.push(url);
    // An unspecified library default gateway must fail this fixture.
    expect(url.origin).toBe(gateway);
    expect(url.searchParams.get('format')).toBe('raw');
    expect(new Headers(init?.headers).get('accept')).toBe('application/vnd.ipld.raw');
    const cid = CID.parse(url.pathname.slice('/ipfs/'.length));
    const bytes = await rawBlock(cid);
    if (corrupt?.(cid)) bytes[bytes.length - 1] ^= 1;
    return new Response(bytes, { headers: { 'Content-Type': 'application/vnd.ipld.raw' } });
  }));
  return requests;
}

describe('explicit trustless gateway content verification', () => {
  beforeAll(async () => {
    producer = await peerlessHelia();
    metadataCid = CID.createV1(0x55, await sha256.digest(metadata));
    await producer.blockstore.put(metadataCid, metadata);
    artifactCid = await unixfs(producer).addBytes(artifact);
  });

  afterEach(async () => {
    await Promise.all(clients.splice(0).map((client) => client.stop()));
    vi.unstubAllGlobals();
  });

  afterAll(async () => { await producer?.stop(); });

  it('retrieves exact immutable metadata and a multi-block artifact when bitswap has no peers', async () => {
    const requests = serveGateway();
    const client = await peerlessHelia([gateway]);
    clients.push(client);
    expect(await fetchCIDBytesFromHelia(client, metadataCid.toString(), {
      signal: AbortSignal.timeout(3000),
    })).toEqual(metadata);
    // Cover the provider-hinted session path used by the native catalog oracle.
    expect(await fetchCIDBytesFromHelia(client, artifactCid.toString(), {
      providerAddrs: ['/dns4/unavailable.example/tcp/443/tls/ws/p2p/12D3KooWMtfuRiHtDuzMMRYB2oX8UKVqP43hZQakGBLhWsMnCd7K'],
      maxProviders: 5,
      signal: AbortSignal.timeout(3000),
    })).toEqual(artifact);
    expect(new Set(requests.map((url) => url.pathname)).size).toBeGreaterThan(2);
    expect(requests.every((url) => url.origin === gateway)).toBe(true);
  });

  it.each(['metadata', 'artifact leaf'])('rejects a corrupted %s raw block instead of returning unverified bytes', async (part) => {
    const requests = serveGateway((cid) => part === 'metadata'
      ? cid.equals(metadataCid)
      : !cid.equals(artifactCid));
    const client = await peerlessHelia([gateway]);
    clients.push(client);
    const cid = part === 'metadata' ? metadataCid : artifactCid;
    await expect(fetchCIDBytesFromHelia(client, cid.toString(), {
      signal: AbortSignal.timeout(3000),
    })).rejects.toThrow();
    expect(requests.length).toBeGreaterThan(0);
    expect(requests.every((url) => url.origin === gateway)).toBe(true);
  });

  it('does not contact any gateway without the opt-in', async () => {
    const requests = serveGateway();
    const client = await peerlessHelia();
    clients.push(client);
    await expect(fetchCIDBytesFromHelia(client, metadataCid.toString(), {
      signal: AbortSignal.timeout(100),
    })).rejects.toThrow();
    expect(requests).toEqual([]);
  });
});

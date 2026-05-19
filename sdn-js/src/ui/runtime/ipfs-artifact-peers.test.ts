import { describe, expect, it } from 'vitest';
import {
  artifactPeerAddrsForTrustedPeers,
  connectIpfsArtifactProviders,
  connectIpfsArtifactPeers,
  normalizeIpfsArtifactPeerAddrs,
  prioritizeIpfsArtifactPeerAddrs,
} from './ipfs-artifact-peers';

describe('IPFS artifact peer routing', () => {
  it('normalizes artifact peer addresses from configured-node metadata', () => {
    expect(normalizeIpfsArtifactPeerAddrs([
      ' /ip4/167.172.219.213/tcp/4002/p2p/12D3KooWCelesTrak ',
      '',
      '/ip4/167.172.219.213/tcp/4002/p2p/12D3KooWCelesTrak',
      '/ip4/159.203.150.8/tcp/4002/p2p/12D3KooWSpaceAware',
    ])).toEqual([
      '/ip4/167.172.219.213/tcp/4002/p2p/12D3KooWCelesTrak',
      '/ip4/159.203.150.8/tcp/4002/p2p/12D3KooWSpaceAware',
    ]);
    expect(normalizeIpfsArtifactPeerAddrs('/ip4/127.0.0.1/tcp/4002/p2p/12D3KooWLocal,/dns4/provider.example/tcp/4001/p2p/12D3KooWRemote')).toEqual([
      '/ip4/127.0.0.1/tcp/4002/p2p/12D3KooWLocal',
      '/dns4/provider.example/tcp/4001/p2p/12D3KooWRemote',
    ]);
  });

  it('connects local Kubo to artifact peers before gateway shard reads', async () => {
    const calls: string[] = [];
    const result = await connectIpfsArtifactPeers({
      ipfsApiUrl: 'http://127.0.0.1:5001/',
      artifactPeerAddrs: [
        '/ip4/167.172.219.213/tcp/4002/p2p/12D3KooWCelesTrak',
        '/ip4/159.203.150.8/tcp/4002/p2p/12D3KooWSpaceAware',
      ],
      fetch: async (url, init) => {
        calls.push(`${init?.method ?? 'GET'} ${String(url)}`);
        return new Response(JSON.stringify({ Strings: ['connect success'] }), { status: 200 });
      },
    });

    expect(result).toEqual({
      attempted: 2,
      connected: 2,
      failed: 0,
    });
    expect(calls).toEqual([
      'POST http://127.0.0.1:5001/api/v0/swarm/connect?arg=%2Fip4%2F167.172.219.213%2Ftcp%2F4002%2Fp2p%2F12D3KooWCelesTrak&timeout=5000ms',
      'POST http://127.0.0.1:5001/api/v0/swarm/connect?arg=%2Fip4%2F159.203.150.8%2Ftcp%2F4002%2Fp2p%2F12D3KooWSpaceAware&timeout=5000ms',
    ]);
  });

  it('normalizes desktop Kubo API multiaddrs before constructing swarm URLs', async () => {
    const calls: string[] = [];
    const result = await connectIpfsArtifactPeers({
      ipfsApiUrl: '/ip4/127.0.0.1/tcp/5001',
      artifactPeerAddrs: ['/ip4/167.172.219.213/tcp/4002/p2p/12D3KooWCelesTrak'],
      fetch: async (url, init) => {
        calls.push(`${init?.method ?? 'GET'} ${String(url)}`);
        return new Response(JSON.stringify({ Strings: ['connect success'] }), { status: 200 });
      },
    });

    expect(result).toEqual({
      attempted: 1,
      connected: 1,
      failed: 0,
    });
    expect(calls).toEqual([
      'POST http://127.0.0.1:5001/api/v0/swarm/connect?arg=%2Fip4%2F167.172.219.213%2Ftcp%2F4002%2Fp2p%2F12D3KooWCelesTrak&timeout=5000ms',
    ]);
  });

  it('ignores malformed local Kubo API endpoints instead of throwing Invalid URL during sync', async () => {
    await expect(connectIpfsArtifactPeers({
      ipfsApiUrl: 'not a url',
      artifactPeerAddrs: ['/ip4/167.172.219.213/tcp/4002/p2p/12D3KooWCelesTrak'],
      fetch: async () => {
        throw new Error('fetch should not be called for malformed API endpoints');
      },
    })).resolves.toEqual({
      attempted: 0,
      connected: 0,
      failed: 0,
    });
  });

  it('prioritizes the selected provider while adding every discovered seed peer once', () => {
    expect(prioritizeIpfsArtifactPeerAddrs(
      ['/ip4/167.172.219.213/tcp/4002/p2p/12D3KooWCelesTrak'],
      [
        '/ip4/159.203.150.8/tcp/4002/p2p/12D3KooWSpaceAware',
        '/ip4/167.172.219.213/tcp/4002/p2p/12D3KooWCelesTrak',
        '',
      ],
    )).toEqual([
      '/ip4/167.172.219.213/tcp/4002/p2p/12D3KooWCelesTrak',
      '/ip4/159.203.150.8/tcp/4002/p2p/12D3KooWSpaceAware',
    ]);
  });

  it('adds generic trusted peer artifact addresses without using untrusted observed peers', () => {
    expect(artifactPeerAddrsForTrustedPeers([
      {
        id: '16Uiu2HUnknown',
        trustLevel: 'unknown',
        artifactPeerAddrs: ['/ip4/198.51.100.10/tcp/4002/p2p/12D3KooWUnknown'],
      },
      {
        id: '16Uiu2HNever',
        trust_level: 'never',
        metadata: {
          ipfs_artifact_addrs: ['/ip4/198.51.100.11/tcp/4002/p2p/12D3KooWNever'],
        },
      },
      {
        id: '16Uiu2HMarginal',
        trustLevel: 'marginal',
        artifactPeerAddrs: ['/ip4/203.0.113.20/tcp/4002/p2p/12D3KooWMarginal'],
      },
      {
        id: '16Uiu2HTrusted',
        trust_level: 'trusted',
        metadata: {
          ipfs_artifact_addrs: [
            '/dns4/trusted-seed.example/tcp/4002/p2p/12D3KooWTrusted',
            '/ip4/203.0.113.20/tcp/4002/p2p/12D3KooWMarginal',
          ],
        },
      },
    ])).toEqual([
      '/ip4/203.0.113.20/tcp/4002/p2p/12D3KooWMarginal',
      '/dns4/trusted-seed.example/tcp/4002/p2p/12D3KooWTrusted',
    ]);
  });

  it('discovers and connects IPFS providers for published shard CIDs', async () => {
    const calls: string[] = [];
    const providerEvents = [
      JSON.stringify({
        Type: 4,
        Responses: [
          { ID: '12D3KooWProviderA', Addrs: ['/ip4/203.0.113.10/tcp/4001'] },
          { ID: '12D3KooWProviderB', Addrs: ['/dns4/provider-b.example/tcp/4001/p2p/12D3KooWProviderB'] },
        ],
      }),
      'not json',
      JSON.stringify({
        Type: 4,
        Responses: [
          { ID: '12D3KooWProviderA', Addrs: ['/ip4/203.0.113.10/tcp/4001'] },
        ],
      }),
    ].join('\n');

    const result = await connectIpfsArtifactProviders({
      ipfsApiUrl: 'http://127.0.0.1:5001/',
      cids: ['bafyShardA', 'bafyShardA', 'bafyShardB'],
      numProviders: 8,
      fetch: async (url, init) => {
        calls.push(`${init?.method ?? 'GET'} ${String(url)}`);
        const requestUrl = String(url);
        if (requestUrl.includes('/api/v0/routing/findprovs')) {
          return new Response(providerEvents, { status: 200 });
        }
        return new Response(JSON.stringify({ Strings: ['connect success'] }), { status: 200 });
      },
    });

    expect(result).toEqual({
      attempted: 2,
      connected: 2,
      failed: 0,
      discovered: 2,
    });
    expect(calls).toEqual([
      'POST http://127.0.0.1:5001/api/v0/routing/findprovs?arg=bafyShardA&num-providers=8',
      'POST http://127.0.0.1:5001/api/v0/routing/findprovs?arg=bafyShardB&num-providers=8',
      'POST http://127.0.0.1:5001/api/v0/swarm/connect?arg=%2Fip4%2F203.0.113.10%2Ftcp%2F4001%2Fp2p%2F12D3KooWProviderA&timeout=5000ms',
      'POST http://127.0.0.1:5001/api/v0/swarm/connect?arg=%2Fdns4%2Fprovider-b.example%2Ftcp%2F4001%2Fp2p%2F12D3KooWProviderB&timeout=5000ms',
    ]);
  });

  it('bounds IPFS provider discovery requests', async () => {
    const result = await Promise.race([
      connectIpfsArtifactProviders({
        ipfsApiUrl: 'http://127.0.0.1:5001/',
        cids: ['bafyShardA'],
        timeoutMs: 1,
        fetch: async () => new Promise<Response>(() => undefined),
      }),
      new Promise<never>((_, reject) => setTimeout(() => reject(new Error('provider discovery did not time out')), 25)),
    ]);

    expect(result).toEqual({
      attempted: 0,
      connected: 0,
      failed: 0,
      discovered: 0,
    });
  });

  it('cancels stalled provider discovery response bodies after the timeout', async () => {
    let cancelled = false;
    const body = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(new Uint8Array([123]));
      },
      cancel() {
        cancelled = true;
      },
    });

    const result = await Promise.race([
      connectIpfsArtifactProviders({
        ipfsApiUrl: 'http://127.0.0.1:5001/',
        cids: ['bafyShardA'],
        timeoutMs: 1,
        fetch: async () => new Response(body, { status: 200 }),
      }),
      new Promise<never>((_, reject) => setTimeout(() => reject(new Error('provider discovery body did not time out')), 25)),
    ]);

    expect(result).toEqual({
      attempted: 0,
      connected: 0,
      failed: 0,
      discovered: 0,
    });
    expect(cancelled).toBe(true);
  });

  it('starts provider discovery for shard CIDs concurrently so sync can reach shard downloads quickly', async () => {
    const calls: string[] = [];
    const responses: Array<(response: Response) => void> = [];
    const discovery = connectIpfsArtifactProviders({
      ipfsApiUrl: 'http://127.0.0.1:5001/',
      cids: ['bafyShardA', 'bafyShardB', 'bafyShardC'],
      timeoutMs: 1000,
      fetch: async (url) => {
        calls.push(String(url));
        return await new Promise<Response>((resolve) => responses.push(resolve));
      },
    });

    await Promise.resolve();
    await Promise.resolve();

    expect(calls).toEqual([
      'http://127.0.0.1:5001/api/v0/routing/findprovs?arg=bafyShardA&num-providers=20',
      'http://127.0.0.1:5001/api/v0/routing/findprovs?arg=bafyShardB&num-providers=20',
      'http://127.0.0.1:5001/api/v0/routing/findprovs?arg=bafyShardC&num-providers=20',
    ]);

    for (const resolve of responses) resolve(new Response('', { status: 200 }));
    await expect(discovery).resolves.toEqual({
      attempted: 0,
      connected: 0,
      failed: 0,
      discovered: 0,
    });
  });
});

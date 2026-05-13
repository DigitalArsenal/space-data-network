import { describe, expect, it } from 'vitest';
import {
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
});

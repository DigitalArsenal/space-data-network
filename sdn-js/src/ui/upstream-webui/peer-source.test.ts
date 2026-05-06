import { describe, expect, it } from 'vitest';

import {
  buildObservedSdnPeers,
  createHostedRegistryPeerSource,
  formatPeerConnection,
  normalizeTrustedPeerToSwarmPeer,
  trustedPeerListToPeerLocationsForSwarm,
} from '../../../ui/src/upstream-webui/peer-source.js';

describe('upstream webui peer source', () => {
  it('normalizes an SDN trusted peer into an upstream swarm peer shape', () => {
    expect(normalizeTrustedPeerToSwarmPeer({
      id: '12D3KooWExample',
      addrs: [
        '/ip4/127.0.0.1/tcp/14080/ws/p2p/12D3KooWExample',
        '/dns4/relay.example/tcp/443/wss/p2p/12D3KooWExample',
      ],
      trust_level: 'admin',
      name: 'Dev Admin',
      organization: 'Space Data Network',
      metadata: {
        agent_version: 'sdn-server/2.0.2',
        protocols: '/spacedatanetwork/epm-exchange/1.0.0,/space-data-network/module-delivery/1.0.0',
      },
    })).toEqual({
      addr: '/ip4/127.0.0.1/tcp/14080/ws/p2p/12D3KooWExample',
      direction: null,
      identify: {
        AgentVersion: 'sdn-server/2.0.2',
      },
      latency: null,
      peer: '12D3KooWExample',
      sdnMeta: {
        name: 'Dev Admin',
        organization: 'Space Data Network',
        trustLevel: 'admin',
      },
      sdnSources: ['registry'],
      streams: [
        { protocol: '/spacedatanetwork/epm-exchange/1.0.0' },
        { protocol: '/space-data-network/module-delivery/1.0.0' },
      ],
    });
  });

  it('builds upstream peer table rows from trusted SDN peers', () => {
    expect(trustedPeerListToPeerLocationsForSwarm([
      {
        id: '12D3KooWExample',
        addrs: ['/ip4/127.0.0.1/tcp/14080/ws/p2p/12D3KooWExample'],
        trust_level: 'standard',
        metadata: {
          agent_version: 'sdn-js/2.0.2',
          protocols: '/spacedatanetwork/sds/SSA',
        },
      },
    ])).toEqual([
      {
        address: '/ip4/127.0.0.1/tcp/14080/ws/p2p/12D3KooWExample',
        agentVersion: 'sdn-js/2.0.2',
        connection: 'ws',
        coordinates: null,
        direction: null,
        flagCode: null,
        isNearby: false,
        isPrivate: true,
        latency: null,
        location: 'Private SDN link',
        peerId: '12D3KooWExample',
        protocols: '/spacedatanetwork/sds/SSA',
      },
    ]);
  });

  it('derives agentVersion from the SDN advertisement flag when no explicit agent version is present', () => {
    expect(trustedPeerListToPeerLocationsForSwarm([
      {
        id: '12D3KooWFlagOnly',
        addrs: ['/dns4/sdn.example/tcp/443/wss/p2p/12D3KooWFlagOnly'],
        trust_level: 'standard',
        metadata: {
          advertisement_flags: 'spacedatanetwork/1.0.0',
        },
      },
    ])).toEqual([
      {
        address: '/dns4/sdn.example/tcp/443/wss/p2p/12D3KooWFlagOnly',
        agentVersion: 'spacedatanetwork/1.0.0',
        connection: 'wss',
        coordinates: null,
        direction: null,
        flagCode: null,
        isNearby: false,
        isPrivate: false,
        latency: null,
        location: null,
        peerId: '12D3KooWFlagOnly',
        protocols: '',
      },
    ]);
  });

  it('derives an upstream connection label from an SDN multiaddr', () => {
    expect(formatPeerConnection('/ip4/127.0.0.1/tcp/14080/ws/p2p/12D3KooWExample')).toBe('ws');
    expect(formatPeerConnection('/dns4/relay.example/tcp/443/wss/p2p/12D3KooWExample')).toBe('wss');
    expect(formatPeerConnection('/ip4/127.0.0.1/udp/4001/quic-v1/p2p/12D3KooWExample')).toBe('quic-v1');
    expect(formatPeerConnection('/ip4/127.0.0.1/tcp/14001/p2p/12D3KooWExample')).toBe('tcp');
  });

  it('builds live SDN peers from the peer graph without counting generic IPFS-only clients', () => {
    expect(buildObservedSdnPeers({
      local_peer_id: '12D3KooWLocal',
      nodes: [
        {
          peer_id: '12D3KooWLocal',
          is_online: true,
          multiformat_address: ['/ip4/127.0.0.1/tcp/14001/p2p/12D3KooWLocal'],
        },
        {
          peer_id: '12D3KooWTrusted',
          dn: 'Trusted Relay',
          organization: 'Space Data Network',
          trust_level: 'trusted',
          is_online: true,
          multiformat_address: ['/dns4/relay.example/tcp/443/wss/p2p/12D3KooWTrusted'],
        },
        {
          peer_id: '12D3KooWProtocol',
          is_online: true,
          multiformat_address: ['/ip4/203.0.113.10/tcp/4001/p2p/12D3KooWProtocol'],
        },
        {
          peer_id: '12D3KooWIpfsOnly',
          is_online: true,
          multiformat_address: ['/ip4/203.0.113.20/tcp/4001/p2p/12D3KooWIpfsOnly'],
        },
        {
          peer_id: '12D3KooWOfflineTrusted',
          trust_level: 'admin',
          is_online: false,
          multiformat_address: ['/ip4/203.0.113.30/tcp/4001/p2p/12D3KooWOfflineTrusted'],
        },
      ],
      edges: [
        {
          source_peer_id: '12D3KooWLocal',
          target_peer_id: '12D3KooWProtocol',
          protocols: ['/space-data-network/module-delivery/1.0.0'],
        },
        {
          source_peer_id: '12D3KooWLocal',
          target_peer_id: '12D3KooWIpfsOnly',
          protocols: ['/ipfs/bitswap/1.2.0'],
        },
      ],
    }, [
      {
        id: '12D3KooWTrusted',
        addrs: ['/dns4/relay.example/tcp/443/wss/p2p/12D3KooWTrusted'],
        trust_level: 'trusted',
        name: 'Trusted Relay',
        organization: 'Space Data Network',
        metadata: {
          agent_version: 'sdn-server/2.0.2',
          protocols: '/space-data-network/module-delivery/1.0.0',
        },
      },
      {
        id: '12D3KooWProtocol',
        addrs: ['/ip4/203.0.113.10/tcp/4001/p2p/12D3KooWProtocol'],
        metadata: {
          agent_version: 'sdn-js/2.0.2',
        },
      },
    ])).toEqual([
      {
        addrs: ['/dns4/relay.example/tcp/443/wss/p2p/12D3KooWTrusted'],
        id: '12D3KooWTrusted',
        metadata: {
          agent_version: 'sdn-server/2.0.2',
          protocols: '/space-data-network/module-delivery/1.0.0',
        },
        name: 'Trusted Relay',
        organization: 'Space Data Network',
        trust_level: 'trusted',
      },
      {
        addrs: ['/ip4/203.0.113.10/tcp/4001/p2p/12D3KooWProtocol'],
        id: '12D3KooWProtocol',
        metadata: {
          agent_version: 'sdn-js/2.0.2',
          protocols: '/space-data-network/module-delivery/1.0.0',
        },
      },
    ]);
  });

  it('prefers the observed peer graph and falls back to the registry endpoint', async () => {
    const fetch = vi.fn(async (url) => {
      if (url === 'https://node.example/api/peers/sdn') {
        return {
          ok: true,
          status: 200,
          json: async () => ([
            {
              id: '12D3KooWAdvertised',
              addrs: ['/dns4/sdn.example/tcp/443/wss/p2p/12D3KooWAdvertised'],
              trust_level: 'trusted',
              metadata: {
                agent_version: 'spacedatanetwork/1.0.0',
                protocols: '/space-data-network/module-delivery/1.0.0',
                advertisement_flags: 'spacedatanetwork/1.0.0',
              },
            },
          ]),
        };
      }
      if (url === 'https://node.example/api/peers/graph') {
        return {
          ok: false,
          status: 404,
          json: async () => ({ message: 'not found' }),
        };
      }
      if (url === 'https://node.example/api/peers') {
        return {
          ok: true,
          status: 200,
          json: async () => ([
            {
              id: '12D3KooWFallback',
              addrs: ['/ip4/127.0.0.1/tcp/14080/ws/p2p/12D3KooWFallback'],
              trust_level: 'standard',
            },
          ]),
        };
      }
      throw new Error(`unexpected url ${url}`);
    });

    const source = createHostedRegistryPeerSource({
      baseUrl: 'https://node.example',
      fetchImpl: fetch,
    });

    await expect(source.listPeers()).resolves.toEqual([
      {
        id: '12D3KooWAdvertised',
        addrs: ['/dns4/sdn.example/tcp/443/wss/p2p/12D3KooWAdvertised'],
        trust_level: 'trusted',
        metadata: {
          agent_version: 'spacedatanetwork/1.0.0',
          protocols: '/space-data-network/module-delivery/1.0.0',
          advertisement_flags: 'spacedatanetwork/1.0.0',
        },
      },
    ]);
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(fetch).toHaveBeenNthCalledWith(1, 'https://node.example/api/peers/sdn');
  });

  it('falls back to the peer graph and registry endpoints when the SDN-only endpoint is unavailable', async () => {
    const fetch = vi.fn(async (url) => {
      if (url === 'https://node.example/api/peers/sdn') {
        return {
          ok: false,
          status: 404,
          json: async () => ({ message: 'not found' }),
        };
      }
      if (url === 'https://node.example/api/peers/graph') {
        return {
          ok: false,
          status: 404,
          json: async () => ({ message: 'not found' }),
        };
      }
      if (url === 'https://node.example/api/peers') {
        return {
          ok: true,
          status: 200,
          json: async () => ([
            {
              id: '12D3KooWFallback',
              addrs: ['/ip4/127.0.0.1/tcp/14080/ws/p2p/12D3KooWFallback'],
              trust_level: 'standard',
            },
          ]),
        };
      }
      throw new Error(`unexpected url ${url}`);
    });

    const source = createHostedRegistryPeerSource({
      baseUrl: 'https://node.example',
      fetchImpl: fetch,
    });

    await expect(source.listPeers()).resolves.toEqual([
      {
        id: '12D3KooWFallback',
        addrs: ['/ip4/127.0.0.1/tcp/14080/ws/p2p/12D3KooWFallback'],
        trust_level: 'standard',
      },
    ]);
    expect(fetch).toHaveBeenNthCalledWith(1, 'https://node.example/api/peers/sdn');
    expect(fetch).toHaveBeenNthCalledWith(2, 'https://node.example/api/peers/graph');
    expect(fetch).toHaveBeenNthCalledWith(3, 'https://node.example/api/peers');
  });

  it('falls back to Kubo swarm peers on the desktop static origin', async () => {
    const fetch = vi.fn(async (url, init) => {
      if (url === 'http://127.0.0.1:17890/api/peers/sdn' ||
        url === 'http://127.0.0.1:17890/api/peers/graph' ||
        url === 'http://127.0.0.1:17890/api/peers') {
        return {
          ok: false,
          status: 404,
          json: async () => ({ message: 'not found' }),
        };
      }
      if (url === 'http://127.0.0.1:17890/api/local/sdn-nodes') {
        return {
          ok: true,
          status: 200,
          json: async () => ({ nodes: [] }),
        };
      }
      if (url === 'http://127.0.0.1:5001/api/v0/swarm/peers?verbose=true&identify=true&timeout=10000ms') {
        expect(init).toEqual({ method: 'POST' });
        return {
          ok: true,
          status: 200,
          json: async () => ({
            Peers: [
              {
                Addr: '/ip4/159.203.150.8/tcp/4001',
                Peer: '16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45',
                Identify: {
                  AgentVersion: 'spacedatanetwork/1.0.3',
                  Protocols: [
                    '/ipfs/id/1.0.0',
                    '/space-data-network/module-delivery/1.0.0',
                  ],
                },
              },
              {
                Addr: '/ip4/203.0.113.20/tcp/4001',
                Peer: '12D3KooWIpfsOnly',
                Identify: {
                  AgentVersion: 'kubo/0.39.0',
                  Protocols: ['/ipfs/id/1.0.0'],
                },
              },
            ],
          }),
        };
      }
      throw new Error(`unexpected url ${url}`);
    });

    const source = createHostedRegistryPeerSource({
      baseUrl: 'http://127.0.0.1:17890',
      kuboApiBaseUrl: 'http://127.0.0.1:5001',
      fetchImpl: fetch,
    });

    await expect(source.listPeers()).resolves.toEqual([
      {
        id: '16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45',
        addrs: ['/ip4/159.203.150.8/tcp/4001/p2p/16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45'],
        metadata: {
          agent_version: 'spacedatanetwork/1.0.3',
          protocols: '/ipfs/id/1.0.0,/space-data-network/module-delivery/1.0.0',
        },
      },
    ]);
    expect(fetch).toHaveBeenNthCalledWith(4, 'http://127.0.0.1:17890/api/local/sdn-nodes');
    expect(fetch).toHaveBeenNthCalledWith(5, 'http://127.0.0.1:5001/api/v0/swarm/peers?verbose=true&identify=true&timeout=10000ms', { method: 'POST' });
  });

  it('uses desktop configured SDN nodes before Kubo when hosted registry endpoints are absent', async () => {
    const fetch = vi.fn(async (url) => {
      if (url === 'http://127.0.0.1:17890/api/peers/sdn' ||
        url === 'http://127.0.0.1:17890/api/peers/graph' ||
        url === 'http://127.0.0.1:17890/api/peers') {
        return {
          ok: false,
          status: 404,
          json: async () => ({ message: 'not found' }),
        };
      }
      if (url === 'http://127.0.0.1:17890/api/local/sdn-nodes') {
        return {
          ok: true,
          status: 200,
          json: async () => ({
            nodes: [
              {
                id: 'space-data-network-01',
                name: 'space-data-network-01',
                addrs: [],
                trust_level: 'trusted',
                metadata: {
                  agent_version: 'sdn-configured-node',
                  protocols: '/space-data-network/configured-node/1.0.0',
                },
              },
              {
                id: 'space-data-network-02',
                name: 'space-data-network-02',
                addrs: [],
                trust_level: 'trusted',
                metadata: {
                  agent_version: 'sdn-configured-node',
                  protocols: '/space-data-network/configured-node/1.0.0',
                },
              },
            ],
          }),
        };
      }
      if (url.includes('/api/v0/swarm/peers')) {
        throw new Error('Kubo fallback should not run when configured SDN nodes are available');
      }
      throw new Error(`unexpected url ${url}`);
    });

    const source = createHostedRegistryPeerSource({
      baseUrl: 'http://127.0.0.1:17890',
      kuboApiBaseUrl: 'http://127.0.0.1:5001',
      fetchImpl: fetch,
    });

    await expect(source.listPeers()).resolves.toEqual([
      {
        id: 'space-data-network-01',
        name: 'space-data-network-01',
        addrs: [],
        trust_level: 'trusted',
        metadata: {
          agent_version: 'sdn-configured-node',
          protocols: '/space-data-network/configured-node/1.0.0',
        },
      },
      {
        id: 'space-data-network-02',
        name: 'space-data-network-02',
        addrs: [],
        trust_level: 'trusted',
        metadata: {
          agent_version: 'sdn-configured-node',
          protocols: '/space-data-network/configured-node/1.0.0',
        },
      },
    ]);
    expect(fetch).toHaveBeenCalledTimes(4);
    expect(fetch).toHaveBeenNthCalledWith(4, 'http://127.0.0.1:17890/api/local/sdn-nodes');
  });
});

import { describe, expect, it } from 'vitest';
import { SdnApiError, type RequestOptions } from '../../lib/auth/sdn-api-client';
import type { AuthSessionState } from '../../lib/auth/auth-store';
import { EPM_CID_NOT_PUBLISHED } from './node-data';
import {
  PEERS_EMPTY_LABEL,
  PEERS_FILTERED_EMPTY_LABEL,
  PEERS_LOADING_LABEL,
  buildConnectAddr,
  PEER_CONNECT_NO_ADDRESS_TOOLTIP,
  PEER_CONNECT_REQUIRES_ADMIN_TOOLTIP,
  PEER_CONNECT_TOOLTIP,
  PEER_EPM_NOT_AVAILABLE_TOOLTIP,
  PEER_PAID_CALLOUT_TEXT,
  buildPeerDetailView,
  buildPeerRowViews,
  buildPeerRows,
  canConnectPeers,
  connectToPeer,
  fetchPeerDetail,
  filterPeers,
  loadPeersDashboardData,
  markPaidProviders,
  parsePeerDetail,
  parsePeersResponse,
  peerFilterTabStyle,
  peersEmptyStateLabel,
  type PeerFilterTab,
  type PeerRow,
  type PeersApiClient,
} from './peers-data';

// ---------------------------------------------------------------------------
// parsePeersResponse (re-export sanity — full behavior covered by
// node-data.test.ts's parseNodePeers suite)
// ---------------------------------------------------------------------------

describe('parsePeersResponse', () => {
  it('parses the real /api/v1/peers object shape', () => {
    const result = parsePeersResponse({ peers: [{ peer_id: '12D3KooWAAA', addrs: ['/ip4/1.2.3.4/tcp/4001'] }] });
    expect(result).toEqual([{ peerId: '12D3KooWAAA', addrs: ['/ip4/1.2.3.4/tcp/4001'] }]);
  });

  it('degrades to [] for a malformed payload', () => {
    expect(parsePeersResponse(null)).toEqual([]);
    expect(parsePeersResponse([])).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// parsePeerDetail
// ---------------------------------------------------------------------------

describe('parsePeerDetail', () => {
  it('parses the real /api/v1/peers/{peerId} shape', () => {
    expect(parsePeerDetail({ peer_id: '12D3KooWAAA', addrs: ['/ip4/1.2.3.4/tcp/4001'], connection_count: 2 })).toEqual({
      peerId: '12D3KooWAAA',
      addrs: ['/ip4/1.2.3.4/tcp/4001'],
      connectionCount: 2,
    });
  });

  it('returns null when peer_id is missing', () => {
    expect(parsePeerDetail({ addrs: [], connection_count: 1 })).toBeNull();
  });

  it('returns null for a non-object payload', () => {
    expect(parsePeerDetail(null)).toBeNull();
    expect(parsePeerDetail('12D3KooWAAA')).toBeNull();
    expect(parsePeerDetail([1, 2, 3])).toBeNull();
  });

  it('defaults addrs to [] and connectionCount to null when absent', () => {
    expect(parsePeerDetail({ peer_id: '12D3KooWAAA' })).toEqual({
      peerId: '12D3KooWAAA',
      addrs: [],
      connectionCount: null,
    });
  });

  it('filters non-string entries out of addrs', () => {
    const result = parsePeerDetail({ peer_id: '12D3KooWAAA', addrs: ['/ip4/1.2.3.4/tcp/4001', 42, null] });
    expect(result?.addrs).toEqual(['/ip4/1.2.3.4/tcp/4001']);
  });
});

// ---------------------------------------------------------------------------
// markPaidProviders
// ---------------------------------------------------------------------------

describe('markPaidProviders', () => {
  const peers = [
    { peerId: '12D3KooWProvider', addrs: [] },
    { peerId: '12D3KooWPlain', addrs: [] },
  ];

  it('marks a peer PAID when a listing.provider_peer_id matches its peerId', () => {
    const result = markPaidProviders({ listings: [{ provider_peer_id: '12D3KooWProvider' }] }, peers);
    expect(result.has('12D3KooWProvider')).toBe(true);
    expect(result.has('12D3KooWPlain')).toBe(false);
  });

  it('handles listings:null honestly — no badge shown, matches this node\'s real response', () => {
    const result = markPaidProviders({ listings: null, total: 0, facets: {} }, peers);
    expect(result.size).toBe(0);
  });

  it('handles a missing listings field', () => {
    expect(markPaidProviders({ total: 0 }, peers).size).toBe(0);
  });

  it('handles a non-object payload', () => {
    expect(markPaidProviders(null, peers).size).toBe(0);
    expect(markPaidProviders(undefined, peers).size).toBe(0);
  });

  it('ignores a listing whose provider_peer_id matches no known peer', () => {
    const result = markPaidProviders({ listings: [{ provider_peer_id: '12D3KooWStranger' }] }, peers);
    expect(result.size).toBe(0);
  });

  it('ignores a listing with no provider_peer_id field', () => {
    const result = markPaidProviders({ listings: [{ title: 'no provider here' }] }, peers);
    expect(result.size).toBe(0);
  });

  it('dedupes multiple listings from the same provider into one marked peer', () => {
    const result = markPaidProviders(
      { listings: [{ provider_peer_id: '12D3KooWProvider' }, { provider_peer_id: '12D3KooWProvider' }] },
      peers,
    );
    expect(result.size).toBe(1);
  });
});

// ---------------------------------------------------------------------------
// buildPeerRows
// ---------------------------------------------------------------------------

describe('buildPeerRows', () => {
  it('always marks trust as observed (never a fabricated trusted)', () => {
    const rows = buildPeerRows([{ peerId: '12D3KooWAAA', addrs: [] }], new Set());
    expect(rows[0]?.trust).toBe('observed');
  });

  it('marks paid from the provided set and leaves name null (no dn surface)', () => {
    const rows = buildPeerRows(
      [
        { peerId: '12D3KooWAAA', addrs: ['/ip4/1.2.3.4/tcp/4001'] },
        { peerId: '12D3KooWBBB', addrs: [] },
      ],
      new Set(['12D3KooWAAA']),
    );
    expect(rows[0]).toEqual({ peerId: '12D3KooWAAA', addrs: ['/ip4/1.2.3.4/tcp/4001'], name: null, trust: 'observed', paid: true });
    expect(rows[1]?.paid).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// filterPeers
// ---------------------------------------------------------------------------

describe('filterPeers', () => {
  const rows: PeerRow[] = [
    { peerId: '12D3KooWAliceLongId', addrs: ['/ip4/1.1.1.1/tcp/4001'], name: null, trust: 'observed', paid: true },
    { peerId: '12D3KooWBobLongerId', addrs: ['/ip4/2.2.2.2/tcp/4001'], name: 'Bob Provider', trust: 'trusted', paid: false },
    { peerId: '12D3KooWCarolId', addrs: [], name: null, trust: 'observed', paid: false },
  ];

  it('"all" returns every row untouched', () => {
    expect(filterPeers('', 'all', rows)).toHaveLength(3);
  });

  it('"trusted" is honestly empty when no row has real trust', () => {
    const onlyObserved = rows.filter((r) => r.trust === 'observed');
    expect(filterPeers('', 'trusted', onlyObserved)).toEqual([]);
  });

  it('"trusted" returns rows that do carry real trust', () => {
    expect(filterPeers('', 'trusted', rows)).toEqual([rows[1]]);
  });

  it('"observed" returns connected swarm peers', () => {
    expect(filterPeers('', 'observed', rows)).toEqual([rows[0], rows[2]]);
  });

  it('"providers" returns only paid rows', () => {
    expect(filterPeers('', 'providers', rows)).toEqual([rows[0]]);
  });

  it('search matches a peerId substring, case-insensitively', () => {
    expect(filterPeers('carolid', 'all', rows)).toEqual([rows[2]]);
  });

  it('search matches name when one exists', () => {
    expect(filterPeers('bob provider', 'all', rows)).toEqual([rows[1]]);
  });

  it('search + tab combine (AND, not OR)', () => {
    expect(filterPeers('bob', 'observed', rows)).toEqual([]);
    expect(filterPeers('bob', 'trusted', rows)).toEqual([rows[1]]);
  });

  it('blank/whitespace-only query is a no-op', () => {
    expect(filterPeers('   ', 'all', rows)).toHaveLength(3);
  });

  it('a query matching nothing returns []', () => {
    expect(filterPeers('nonexistent-peer', 'all', rows)).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// peersEmptyStateLabel
// ---------------------------------------------------------------------------

describe('peersEmptyStateLabel', () => {
  it('shows the loading label before the first fetch resolves', () => {
    expect(peersEmptyStateLabel(false, 0, 0)).toBe(PEERS_LOADING_LABEL);
  });

  it('shows "no peers connected" when the swarm truly has zero peers', () => {
    expect(peersEmptyStateLabel(true, 0, 0)).toBe(PEERS_EMPTY_LABEL);
  });

  it('shows "no peers match this filter" when peers exist but the filter yields none', () => {
    expect(peersEmptyStateLabel(true, 5, 0)).toBe(PEERS_FILTERED_EMPTY_LABEL);
  });

  it('returns "" (render the rows) when the filtered list is non-empty', () => {
    expect(peersEmptyStateLabel(true, 5, 2)).toBe('');
  });
});

// ---------------------------------------------------------------------------
// peerFilterTabStyle
// ---------------------------------------------------------------------------

describe('peerFilterTabStyle', () => {
  it('applies the accent style to the active tab', () => {
    const style = peerFilterTabStyle('observed', 'observed');
    expect(style).toEqual({ color: '#9fd4f5', border: 'rgba(120,190,230,0.5)', background: 'rgba(74,166,224,0.1)' });
  });

  it('applies the neutral style to inactive tabs', () => {
    const style = peerFilterTabStyle('providers', 'observed');
    expect(style).toEqual({ color: '#7d929b', border: 'rgba(90,150,180,0.28)', background: 'transparent' });
  });

  it('is correct for every tab id', () => {
    const tabs: PeerFilterTab[] = ['all', 'trusted', 'observed', 'providers'];
    for (const tab of tabs) {
      expect(peerFilterTabStyle(tab, tab).background).toBe('rgba(74,166,224,0.1)');
    }
  });
});

// ---------------------------------------------------------------------------
// buildPeerRowViews
// ---------------------------------------------------------------------------

describe('buildPeerRowViews', () => {
  it('renders a truncated fallback name, a full peer id sub-line, and honest FEEDS', () => {
    const rows: PeerRow[] = [
      { peerId: '12D3KooWAbCdEfGh1234567890AbCdEfGh1234567890Ab', addrs: ['/ip4/1.2.3.4/tcp/4001'], name: null, trust: 'observed', paid: false },
    ];
    const [view] = buildPeerRowViews(rows, null);
    expect(view?.name).toBe('12D3KooW…7890Ab');
    expect(view?.isFallbackName).toBe(true);
    expect(view?.fullPeerId).toBe(rows[0]?.peerId);
    expect(view?.feeds).toBe('—');
    expect(view?.address).toBe('/ip4/1.2.3.4/tcp/4001');
  });

  it('renders "—" for a peer with no known address', () => {
    const rows: PeerRow[] = [{ peerId: '12D3KooWAAA', addrs: [], name: null, trust: 'observed', paid: false }];
    expect(buildPeerRowViews(rows, null)[0]?.address).toBe('—');
  });

  it('marks the currently-selected peer', () => {
    const rows: PeerRow[] = [
      { peerId: '12D3KooWAAA', addrs: [], name: null, trust: 'observed', paid: false },
      { peerId: '12D3KooWBBB', addrs: [], name: null, trust: 'observed', paid: false },
    ];
    const views = buildPeerRowViews(rows, '12D3KooWBBB');
    expect(views[0]?.selected).toBe(false);
    expect(views[1]?.selected).toBe(true);
  });

  it('passes trust label/color through peerTrustColor', () => {
    const rows: PeerRow[] = [{ peerId: '12D3KooWAAA', addrs: [], name: null, trust: 'observed', paid: false }];
    const [view] = buildPeerRowViews(rows, null);
    expect(view?.trustLabel).toBe('OBSERVED');
    expect(view?.trustColor).toBe('#7d929b');
  });

  it('passes the paid flag through unchanged', () => {
    const rows: PeerRow[] = [{ peerId: '12D3KooWAAA', addrs: [], name: null, trust: 'observed', paid: true }];
    expect(buildPeerRowViews(rows, null)[0]?.paid).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// buildPeerDetailView
// ---------------------------------------------------------------------------

describe('buildPeerDetailView', () => {
  const baseRow: PeerRow = {
    peerId: '12D3KooWAbCdEfGh1234567890AbCdEfGh1234567890Ab',
    addrs: ['/ip4/1.2.3.4/tcp/4001'],
    name: null,
    trust: 'observed',
    paid: false,
  };

  it('returns null when no row is selected', () => {
    expect(buildPeerDetailView(null, null, true)).toBeNull();
  });

  it('renders every honest "—"/NOT PUBLISHED field when there is no per-peer surface', () => {
    const view = buildPeerDetailView(baseRow, null, true);
    expect(view?.ownertrust).toBe('—');
    expect(view?.agent).toBe('—');
    expect(view?.feeds).toBe('—');
    expect(view?.epmCid).toBe(EPM_CID_NOT_PUBLISHED);
  });

  it('shows the PAID callout only when the row is paid', () => {
    expect(buildPeerDetailView(baseRow, null, true)?.paid).toBe(false);
    expect(buildPeerDetailView({ ...baseRow, paid: true }, null, true)?.paid).toBe(true);
    expect(buildPeerDetailView({ ...baseRow, paid: true }, null, true)?.paidCalloutText).toBe(PEER_PAID_CALLOUT_TEXT);
  });

  it('prefers the fetched detail addrs over the row addrs for ADDRESS', () => {
    const detail = { peerId: baseRow.peerId, addrs: ['/ip4/9.9.9.9/tcp/4001'], connectionCount: 1 };
    expect(buildPeerDetailView(baseRow, detail, true)?.address).toBe('/ip4/9.9.9.9/tcp/4001');
  });

  it('falls back to the row addrs when detail is null (401/offline)', () => {
    expect(buildPeerDetailView(baseRow, null, true)?.address).toBe('/ip4/1.2.3.4/tcp/4001');
  });

  it('renders "—" for ADDRESS when neither detail nor row has an addr', () => {
    const noAddrRow: PeerRow = { ...baseRow, addrs: [] };
    expect(buildPeerDetailView(noAddrRow, null, true)?.address).toBe('—');
  });

  it('includes the real connection count in the subtitle when detail is present', () => {
    const detail = { peerId: baseRow.peerId, addrs: [], connectionCount: 1 };
    expect(buildPeerDetailView(baseRow, detail, true)?.subtitle).toBe('Connected swarm peer · 1 connection');
    const detailMulti = { peerId: baseRow.peerId, addrs: [], connectionCount: 3 };
    expect(buildPeerDetailView(baseRow, detailMulti, true)?.subtitle).toBe('Connected swarm peer · 3 connections');
  });

  it('renders a plain subtitle when detail is unavailable', () => {
    expect(buildPeerDetailView(baseRow, null, true)?.subtitle).toBe('Connected swarm peer');
  });

  it('enables CONNECT only when canConnect is true AND an address is known', () => {
    expect(buildPeerDetailView(baseRow, null, true)?.connectEnabled).toBe(true);
    expect(buildPeerDetailView(baseRow, null, false)?.connectEnabled).toBe(false);
    expect(buildPeerDetailView({ ...baseRow, addrs: [] }, null, true)?.connectEnabled).toBe(false);
    expect(buildPeerDetailView({ ...baseRow, addrs: [] }, null, false)?.connectEnabled).toBe(false);
  });

  it('gives an honest, specific tooltip for each disabled reason', () => {
    expect(buildPeerDetailView(baseRow, null, false)?.connectTooltip).toBe(PEER_CONNECT_REQUIRES_ADMIN_TOOLTIP);
    expect(buildPeerDetailView({ ...baseRow, addrs: [] }, null, true)?.connectTooltip).toBe(PEER_CONNECT_NO_ADDRESS_TOOLTIP);
    expect(buildPeerDetailView(baseRow, null, true)?.connectTooltip).toBe(PEER_CONNECT_TOOLTIP);
  });

  it('vCARD/QR always carry the honest "no peer EPM" tooltip', () => {
    const view = buildPeerDetailView(baseRow, null, true);
    expect(view?.vcardTooltip).toBe(PEER_EPM_NOT_AVAILABLE_TOOLTIP);
    expect(view?.qrTooltip).toBe(PEER_EPM_NOT_AVAILABLE_TOOLTIP);
  });

  it('derives trustBorderColor from trustColor via hexToRgba(...,0.4)', () => {
    expect(buildPeerDetailView(baseRow, null, true)?.trustBorderColor).toBe('rgba(125,146,155,0.4)');
  });
});

// ---------------------------------------------------------------------------
// canConnectPeers
// ---------------------------------------------------------------------------

describe('canConnectPeers', () => {
  function state(status: AuthSessionState['status'], trustLevel?: AuthSessionState['user']): Pick<AuthSessionState, 'status' | 'user'> {
    return { status, user: trustLevel ?? null };
  }

  it('allows an authenticated admin session', () => {
    expect(canConnectPeers(state('authenticated', { trust_level: 'admin' }))).toBe(true);
  });

  it('allows an authenticated ultimate session (this node\'s own key)', () => {
    expect(canConnectPeers(state('authenticated', { trust_level: 'ultimate' }))).toBe(true);
  });

  it('rejects an authenticated session below admin trust', () => {
    expect(canConnectPeers(state('authenticated', { trust_level: 'standard' }))).toBe(false);
    expect(canConnectPeers(state('authenticated', { trust_level: 'full' }))).toBe(false);
    expect(canConnectPeers(state('authenticated', { trust_level: 'marginal' }))).toBe(false);
    expect(canConnectPeers(state('authenticated', { trust_level: 'unknown' }))).toBe(false);
    expect(canConnectPeers(state('authenticated', { trust_level: 'never' }))).toBe(false);
  });

  it('rejects an anonymous session', () => {
    expect(canConnectPeers(state('anonymous'))).toBe(false);
  });

  it('rejects when authenticated but user is somehow null', () => {
    expect(canConnectPeers(state('authenticated'))).toBe(false);
  });

  it('rejects unknown/authenticating/error statuses', () => {
    expect(canConnectPeers(state('unknown'))).toBe(false);
    expect(canConnectPeers(state('authenticating'))).toBe(false);
    expect(canConnectPeers(state('error'))).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// connectToPeer
// ---------------------------------------------------------------------------

describe('buildConnectAddr', () => {
  it('appends /p2p/<peer-id> to a bare swarm addr (server requires it — AddrInfoFromP2pAddr 400s otherwise)', () => {
    expect(buildConnectAddr('/ip4/84.200.125.104/tcp/4001', '12D3KooWabc')).toBe(
      '/ip4/84.200.125.104/tcp/4001/p2p/12D3KooWabc',
    );
  });

  it('leaves an addr that already carries a /p2p/ component untouched', () => {
    const a = '/ip4/1.2.3.4/tcp/4001/p2p/12D3KooWxyz';
    expect(buildConnectAddr(a, '12D3KooWabc')).toBe(a);
  });

  it('returns the addr unchanged when no peer id is known', () => {
    expect(buildConnectAddr('/ip4/1.2.3.4/tcp/4001', null)).toBe('/ip4/1.2.3.4/tcp/4001');
  });
});

describe('connectToPeer', () => {
  function fakeClient(fn: (path: string, opts?: unknown) => unknown): PeersApiClient {
    return {
      requestJson: async <T,>(path: string, opts?: unknown) => {
        const value = fn(path, opts);
        if (value instanceof Error) throw value;
        return { status: 200, data: value as T, etag: null, notModified: false };
      },
    } as unknown as PeersApiClient;
  }

  it('succeeds and returns the peer id on a real 200 response', async () => {
    const client = fakeClient(() => ({ peer_id: '12D3KooWAAA', connected: true }));
    const result = await connectToPeer(client, '/ip4/1.2.3.4/tcp/4001/p2p/12D3KooWAAA');
    expect(result).toEqual({ ok: true, peerId: '12D3KooWAAA', message: null });
  });

  it('sends the exact {"addr": addr} body contract (not addrs/peer_id)', async () => {
    let capturedBody: unknown;
    const client: PeersApiClient = {
      requestJson: async <T,>(_path: string, opts?: RequestOptions) => {
        capturedBody = opts?.body;
        return { status: 200, data: { peer_id: 'x', connected: true } as T, etag: null, notModified: false };
      },
    };
    await connectToPeer(client, '/ip4/1.2.3.4/tcp/4001');
    expect(capturedBody).toEqual({ addr: '/ip4/1.2.3.4/tcp/4001' });
  });

  it('treats a 200 without connected:true as a soft failure, not a throw', async () => {
    const client = fakeClient(() => ({ peer_id: '12D3KooWAAA', connected: false }));
    const result = await connectToPeer(client, '/ip4/1.2.3.4/tcp/4001');
    expect(result.ok).toBe(false);
    expect(result.message).toBeTruthy();
  });

  it('gives an honest message for 401 (no session)', async () => {
    const client = fakeClient(() => new SdnApiError(401, { code: 'unauthorized', message: 'not authenticated' }, '/peers/connect'));
    const result = await connectToPeer(client, '/ip4/1.2.3.4/tcp/4001');
    expect(result).toEqual({ ok: false, peerId: null, message: 'Not authenticated — sign in with an admin session to connect.' });
  });

  it('gives an honest message for 403 (below admin trust)', async () => {
    const client = fakeClient(() => new SdnApiError(403, { code: 'forbidden', message: 'insufficient permissions' }, '/peers/connect'));
    const result = await connectToPeer(client, '/ip4/1.2.3.4/tcp/4001');
    expect(result).toEqual({ ok: false, peerId: null, message: 'Insufficient trust level — an admin session is required to connect.' });
  });

  it('surfaces the server\'s own message for other API errors (e.g. CONNECT_FAILED)', async () => {
    const client = fakeClient(
      () => new SdnApiError(502, { code: 'CONNECT_FAILED', message: 'failed to connect: dial tcp: timeout' }, '/peers/connect'),
    );
    const result = await connectToPeer(client, '/ip4/1.2.3.4/tcp/4001');
    expect(result.ok).toBe(false);
    expect(result.message).toBe('failed to connect: dial tcp: timeout');
  });

  it('falls back to an HTTP-status message when the error body has none', async () => {
    const client = fakeClient(() => new SdnApiError(503, null, '/peers/connect'));
    const result = await connectToPeer(client, '/ip4/1.2.3.4/tcp/4001');
    expect(result.ok).toBe(false);
    expect(result.message).toContain('503');
  });

  it('handles a generic network throw honestly', async () => {
    const client = fakeClient(() => new TypeError('Failed to fetch'));
    const result = await connectToPeer(client, '/ip4/1.2.3.4/tcp/4001');
    expect(result).toEqual({ ok: false, peerId: null, message: 'Connect failed — network error.' });
  });
});

// ---------------------------------------------------------------------------
// fetchPeerDetail
// ---------------------------------------------------------------------------

describe('fetchPeerDetail', () => {
  it('parses a successful response', async () => {
    const client: PeersApiClient = {
      requestJson: async <T,>() => ({
        status: 200,
        data: { peer_id: '12D3KooWAAA', addrs: ['/ip4/1.2.3.4/tcp/4001'], connection_count: 1 } as T,
        etag: null,
        notModified: false,
      }),
    };
    const detail = await fetchPeerDetail(client, '12D3KooWAAA');
    expect(detail).toEqual({ peerId: '12D3KooWAAA', addrs: ['/ip4/1.2.3.4/tcp/4001'], connectionCount: 1 });
  });

  it('returns null on a 401 (unauthenticated/non-admin session) rather than throwing', async () => {
    const client: PeersApiClient = {
      requestJson: async () => {
        throw new SdnApiError(401, { code: 'unauthorized', message: 'not authenticated' }, '/peers/12D3KooWAAA');
      },
    };
    expect(await fetchPeerDetail(client, '12D3KooWAAA')).toBeNull();
  });

  it('URL-encodes the peer id path segment', async () => {
    let capturedPath = '';
    const client: PeersApiClient = {
      requestJson: async <T,>(path: string) => {
        capturedPath = path;
        return { status: 200, data: { peer_id: 'x' } as T, etag: null, notModified: false };
      },
    };
    await fetchPeerDetail(client, '12D3Koo/weird id');
    expect(capturedPath).toBe('/peers/12D3Koo%2Fweird%20id');
  });
});

// ---------------------------------------------------------------------------
// loadPeersDashboardData
// ---------------------------------------------------------------------------

describe('loadPeersDashboardData', () => {
  function fakeApiClient(handlers: Record<string, unknown>): PeersApiClient {
    return {
      requestJson: async <T,>(path: string) => {
        if (!(path in handlers)) throw new Error(`unexpected path ${path}`);
        const value = handlers[path];
        if (value instanceof Error) throw value;
        return { status: 200, data: value as T, etag: null, notModified: false };
      },
    } as unknown as PeersApiClient;
  }

  it('assembles the peer list and PAID marking on success', async () => {
    const apiClient = fakeApiClient({
      '/peers': { peers: [{ peer_id: '12D3KooWAAA', addrs: [] }] },
      '/api/storefront/listings/search': { listings: [{ provider_peer_id: '12D3KooWAAA' }], total: 1, facets: {} },
    });
    const data = await loadPeersDashboardData(apiClient);
    expect(data.peers).toEqual([{ peerId: '12D3KooWAAA', addrs: [] }]);
    expect(data.paidPeerIds.has('12D3KooWAAA')).toBe(true);
  });

  it('matches this node\'s real listings:null response with an honest empty paid set', async () => {
    const apiClient = fakeApiClient({
      '/peers': { peers: [{ peer_id: '12D3KooWAAA', addrs: [] }] },
      '/api/storefront/listings/search': { listings: null, total: 0, facets: {} },
    });
    const data = await loadPeersDashboardData(apiClient);
    expect(data.paidPeerIds.size).toBe(0);
  });

  it('degrades peers to [] when that endpoint fails, without touching the paid lookup', async () => {
    const apiClient = fakeApiClient({
      '/peers': new Error('offline'),
      '/api/storefront/listings/search': { listings: [], total: 0, facets: {} },
    });
    const data = await loadPeersDashboardData(apiClient);
    expect(data.peers).toEqual([]);
    expect(data.paidPeerIds.size).toBe(0);
  });

  it('degrades the paid set to empty when the storefront endpoint fails, without dropping peers', async () => {
    const apiClient = fakeApiClient({
      '/peers': { peers: [{ peer_id: '12D3KooWAAA', addrs: [] }] },
      '/api/storefront/listings/search': new Error('offline'),
    });
    const data = await loadPeersDashboardData(apiClient);
    expect(data.peers).toEqual([{ peerId: '12D3KooWAAA', addrs: [] }]);
    expect(data.paidPeerIds.size).toBe(0);
  });
});

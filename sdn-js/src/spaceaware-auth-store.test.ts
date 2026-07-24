/**
 * Unit tests for the SpaceAware auth session store + route guard
 * (loop task U0.3 — D1 groundwork).
 *
 * Covers the Phase 1A server-session store. The injected public wallet client
 * is retained as the future typed-auth seam but is not invoked: the separate
 * server-auth-v2 cutover owns capability discovery and login operations.
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { SdnApiClient } from '../ui/src/lib/auth/sdn-api-client';
import {
  createAuthStore,
  guardRoute,
  requiresAuthenticatedSession,
} from '../ui/src/lib/auth/auth-store';
import type { getSdnWalletClient } from '../ui/src/lib/auth/wallet-client';

const SERVER_BASE_URL = 'http://127.0.0.1:9999';

type SdnWalletClient = ReturnType<typeof getSdnWalletClient>;

function fakeWallet(): SdnWalletClient {
  return {
    getSnapshot: vi.fn(() => ({ status: 'dormant', identity: null })),
    subscribe: vi.fn(() => vi.fn()),
    connect: vi.fn(),
    openAccount: vi.fn(),
    disconnect: vi.fn(),
    destroy: vi.fn(),
    requestSdnLoginV1: vi.fn(),
    requestSdnLoginV2: vi.fn(),
  } as unknown as SdnWalletClient;
}

/** Scripts a fetch mock over the exact auth endpoints (method + path routing). */
function scriptedFetch(routes: {
  me?: () => Response;
  logout?: () => Response;
}) {
  return vi.fn(async (url: string, init?: RequestInit) => {
    const path = url.replace(SERVER_BASE_URL, '');
    const method = init?.method ?? 'GET';
    if (path === '/api/auth/me' && method === 'GET') return routes.me?.() ?? new Response('', { status: 404 });
    if (path === '/api/auth/logout' && method === 'POST') return routes.logout?.() ?? new Response('', { status: 404 });
    return new Response('not found', { status: 404 });
  });
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe('auth store hydration', () => {
  it('hydrates to anonymous on a 401 from GET /api/auth/me (never throws, never redirects)', async () => {
    const fetchImpl = scriptedFetch({
      me: () => Response.json({ code: 'unauthorized', message: 'not authenticated' }, { status: 401 }),
    });
    const client = new SdnApiClient({ serverBaseUrl: SERVER_BASE_URL, fetchImpl: fetchImpl as unknown as typeof fetch });
    const store = createAuthStore({ client, wallet: fakeWallet() });

    expect(store.state.status).toBe('unknown');
    await store.hydrate();

    expect(store.state.status).toBe('anonymous');
    expect(store.state.user).toBeNull();
  });

  it('hydrates to authenticated with the user payload on 200', async () => {
    const fetchImpl = scriptedFetch({
      me: () => Response.json({ name: 'Test Operator', trust_level: 'full' }),
    });
    const client = new SdnApiClient({ serverBaseUrl: SERVER_BASE_URL, fetchImpl: fetchImpl as unknown as typeof fetch });
    const store = createAuthStore({ client, wallet: fakeWallet() });

    await store.hydrate();

    expect(store.state.status).toBe('authenticated');
    expect(store.state.stage).toBe('confirmed');
    expect(store.state.user).toEqual({ name: 'Test Operator', trust_level: 'full' });
  });

  it('keeps a non-401 hydration failure indeterminate instead of claiming the cookie is anonymous', async () => {
    const fetchImpl = scriptedFetch({
      me: () => Response.json({ code: 'server_error', message: 'temporarily unavailable' }, { status: 503 }),
    });
    const client = new SdnApiClient({ serverBaseUrl: SERVER_BASE_URL, fetchImpl: fetchImpl as unknown as typeof fetch });
    const store = createAuthStore({ client, wallet: fakeWallet() });

    await store.hydrate();

    expect(store.state.status).toBe('error');
    expect(store.state.user).toBeNull();
    expect(store.state.error).toEqual({ code: 'server_error', message: 'temporarily unavailable' });
  });
});

describe('Phase 1A typed wallet boundary', () => {
  it('retains the injected singleton without exposing or invoking a browser login bridge', async () => {
    const fetchImpl = scriptedFetch({
      me: () => Response.json({ code: 'unauthorized', message: 'not authenticated' }, { status: 401 }),
    });
    const client = new SdnApiClient({ serverBaseUrl: SERVER_BASE_URL, fetchImpl: fetchImpl as unknown as typeof fetch });
    const wallet = fakeWallet();
    const store = createAuthStore({ client, wallet });

    expect(store.wallet).toBe(wallet);
    expect(store).not.toHaveProperty('loginWithWallet');
    await store.hydrate();

    expect(wallet.connect).not.toHaveBeenCalled();
    expect(wallet.requestSdnLoginV1).not.toHaveBeenCalled();
    expect(wallet.requestSdnLoginV2).not.toHaveBeenCalled();
  });
});

describe('auth store logout', () => {
  it('resets to anonymous after a successful logout', async () => {
    const fetchImpl = scriptedFetch({
      logout: () => Response.json({ status: 'logged_out' }),
    });
    const client = new SdnApiClient({ serverBaseUrl: SERVER_BASE_URL, fetchImpl: fetchImpl as unknown as typeof fetch });
    const store = createAuthStore({ client, wallet: fakeWallet() });

    await store.logout();

    expect(store.state.status).toBe('anonymous');
    expect(store.state.user).toBeNull();
  });

  it('keeps a failed logout indeterminate because the httpOnly cookie may still be live', async () => {
    let failLogout = false;
    const fetchImpl = scriptedFetch({
      me: () => Response.json({ name: 'Still Signed In', trust_level: 'full' }),
      logout: () => {
        if (failLogout) throw new TypeError('network error');
        return Response.json({ status: 'logged_out' });
      },
    });
    const client = new SdnApiClient({ serverBaseUrl: SERVER_BASE_URL, fetchImpl: fetchImpl as unknown as typeof fetch });
    const store = createAuthStore({ client, wallet: fakeWallet() });

    await store.hydrate();
    failLogout = true;

    await expect(store.logout()).rejects.toThrow();
    expect(store.state.status).toBe('error');
    expect(store.state.user).toEqual({ name: 'Still Signed In', trust_level: 'full' });
    expect(store.state.error).toEqual({ code: 'client_error', message: 'network error' });
  });
});

describe('route guard (client-side; no-redirect-on-API rule lives here, not in the API client)', () => {
  it('requires an authenticated session only for /console routes', () => {
    expect(requiresAuthenticatedSession({ screen: 'console' })).toBe(true);
    expect(requiresAuthenticatedSession({ screen: 'login' })).toBe(false);
    expect(requiresAuthenticatedSession({ screen: 'orbital' })).toBe(false);
    expect(requiresAuthenticatedSession({ screen: 'gantt' })).toBe(false);
    expect(requiresAuthenticatedSession({ screen: 'bmc2' })).toBe(false);
  });

  it('redirects an anonymous session away from /console to /login', () => {
    const navigate = vi.fn();
    const blocked = guardRoute({ status: 'anonymous' }, { screen: 'console' }, navigate);
    expect(blocked).toBe(true);
    expect(navigate).toHaveBeenCalledWith('/login');
  });

  it('does not redirect an authenticated session away from /console', () => {
    const navigate = vi.fn();
    const blocked = guardRoute({ status: 'authenticated' }, { screen: 'console' }, navigate);
    expect(blocked).toBe(false);
    expect(navigate).not.toHaveBeenCalled();
  });

  it('does not flash-redirect while hydration is still in flight (status "unknown")', () => {
    const navigate = vi.fn();
    const blocked = guardRoute({ status: 'unknown' }, { screen: 'console' }, navigate);
    expect(blocked).toBe(false);
    expect(navigate).not.toHaveBeenCalled();
  });

  it('does not redirect when session hydration is indeterminate after a transport or server error', () => {
    const navigate = vi.fn();
    const blocked = guardRoute({ status: 'error' }, { screen: 'console' }, navigate);
    expect(blocked).toBe(false);
    expect(navigate).not.toHaveBeenCalled();
  });

  it('never redirects non-console routes regardless of session status', () => {
    const navigate = vi.fn();
    expect(guardRoute({ status: 'anonymous' }, { screen: 'orbital' }, navigate)).toBe(false);
    expect(navigate).not.toHaveBeenCalled();
  });
});

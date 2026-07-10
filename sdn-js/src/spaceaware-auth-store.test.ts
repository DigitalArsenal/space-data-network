/**
 * Unit tests for the SpaceAware auth session store + route guard
 * (loop task U0.3 — D1 groundwork).
 *
 * Drives the full challenge → sign → verify → `auth/me` hydration round
 * trip against a mocked `fetch` (scripted per sdn-server/internal/auth's
 * wire shapes) using a fake `UnlockedWallet` — the real hd-wallet-wasm
 * signing path is covered separately in `spaceaware-local-wallet.test.ts`.
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { SdnApiClient, SdnApiError } from '../ui/src/lib/auth/sdn-api-client';
import {
  createAuthStore,
  guardRoute,
  requiresAuthenticatedSession,
  type AuthSessionState,
} from '../ui/src/lib/auth/auth-store';
import type { UnlockedWallet } from '../ui/src/lib/auth/local-wallet';

const SERVER_BASE_URL = 'http://127.0.0.1:9999';

function fakeWallet(overrides: Partial<UnlockedWallet> = {}): UnlockedWallet {
  return {
    label: 'test-operator',
    xpub: 'xpubTESTFIXTUREnotarealkey',
    peerId: '12D3KooWTestFixturePeer',
    signingPublicKeyHex: 'aa'.repeat(32),
    sign: vi.fn(async () => new Uint8Array(64).fill(7)),
    lock: vi.fn(),
    ...overrides,
  };
}

/** Scripts a fetch mock over the exact auth endpoints (method + path routing). */
function scriptedFetch(routes: {
  challenge?: () => Response;
  verify?: () => Response;
  me?: () => Response;
  logout?: () => Response;
}) {
  return vi.fn(async (url: string, init?: RequestInit) => {
    const path = url.replace(SERVER_BASE_URL, '');
    const method = init?.method ?? 'GET';
    if (path === '/api/auth/challenge' && method === 'POST') return routes.challenge?.() ?? new Response('', { status: 404 });
    if (path === '/api/auth/verify' && method === 'POST') return routes.verify?.() ?? new Response('', { status: 404 });
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
    const store = createAuthStore({ client });

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
    const store = createAuthStore({ client });

    await store.hydrate();

    expect(store.state.status).toBe('authenticated');
    expect(store.state.stage).toBe('confirmed');
    expect(store.state.user).toEqual({ name: 'Test Operator', trust_level: 'full' });
  });
});

describe('auth store challenge -> sign -> verify -> me round trip', () => {
  it('walks stage idle -> challenge -> verify -> confirmed and ends authenticated', async () => {
    const fetchImpl = scriptedFetch({
      challenge: () => Response.json({ challenge_id: 'chal-1', challenge: 'YWJjZA', expires_at: 1000 }),
      verify: () => Response.json({ user: { name: 'Test Operator', trust_level: 'full' }, expires_at: 2000 }),
      me: () => Response.json({ name: 'Test Operator', trust_level: 'full' }),
    });
    const client = new SdnApiClient({ serverBaseUrl: SERVER_BASE_URL, fetchImpl: fetchImpl as unknown as typeof fetch });

    const snapshots: AuthSessionState[] = [];
    const store = createAuthStore({ client, onStateChange: (s) => snapshots.push(s) });

    const wallet = fakeWallet();
    await store.loginWithWallet(wallet);

    expect(store.state.status).toBe('authenticated');
    expect(store.state.user).toEqual({ name: 'Test Operator', trust_level: 'full' });
    expect(wallet.sign).toHaveBeenCalledTimes(1);

    const stages = snapshots.map((s) => s.stage);
    expect(stages).toEqual(expect.arrayContaining(['challenge', 'verify', 'confirmed']));
    expect(stages.indexOf('challenge')).toBeLessThan(stages.indexOf('verify'));
    expect(stages.indexOf('verify')).toBeLessThan(stages.indexOf('confirmed'));
  });

  it('signs the exact base64-decoded challenge bytes returned by the server', async () => {
    const fetchImpl = scriptedFetch({
      challenge: () => Response.json({ challenge_id: 'chal-1', challenge: 'YWJjZA', expires_at: 1000 }), // "abcd"
      verify: () => Response.json({ user: { trust_level: 'standard' }, expires_at: 2000 }),
      me: () => Response.json({ trust_level: 'standard' }),
    });
    const client = new SdnApiClient({ serverBaseUrl: SERVER_BASE_URL, fetchImpl: fetchImpl as unknown as typeof fetch });
    const store = createAuthStore({ client });
    const wallet = fakeWallet();

    await store.loginWithWallet(wallet);

    const signed = (wallet.sign as ReturnType<typeof vi.fn>).mock.calls[0][0] as Uint8Array;
    expect(new TextDecoder().decode(signed)).toBe('abcd');
  });

  it('surfaces a real 4xx from /api/auth/verify as a typed error and does not authenticate', async () => {
    const fetchImpl = scriptedFetch({
      challenge: () => Response.json({ challenge_id: 'chal-1', challenge: 'YWJjZA', expires_at: 1000 }),
      verify: () => Response.json({ code: 'authentication_failed', message: 'authentication failed' }, { status: 403 }),
    });
    const client = new SdnApiClient({ serverBaseUrl: SERVER_BASE_URL, fetchImpl: fetchImpl as unknown as typeof fetch });
    const store = createAuthStore({ client });

    await expect(store.loginWithWallet(fakeWallet())).rejects.toBeInstanceOf(SdnApiError);

    expect(store.state.status).toBe('error');
    expect(store.state.error).toEqual({ code: 'authentication_failed', message: 'authentication failed' });
    expect(store.state.user).toBeNull();
  });
});

describe('auth store logout', () => {
  it('resets to anonymous after a successful logout', async () => {
    const fetchImpl = scriptedFetch({
      logout: () => Response.json({ status: 'logged_out' }),
    });
    const client = new SdnApiClient({ serverBaseUrl: SERVER_BASE_URL, fetchImpl: fetchImpl as unknown as typeof fetch });
    const store = createAuthStore({ client });

    await store.logout();

    expect(store.state.status).toBe('anonymous');
    expect(store.state.user).toBeNull();
  });

  it('still clears local state to anonymous even when the logout request itself fails', async () => {
    const fetchImpl = vi.fn(async () => {
      throw new TypeError('network error');
    });
    const client = new SdnApiClient({ serverBaseUrl: SERVER_BASE_URL, fetchImpl: fetchImpl as unknown as typeof fetch });
    const store = createAuthStore({ client });

    await expect(store.logout()).rejects.toThrow();
    expect(store.state.status).toBe('anonymous');
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

  it('never redirects non-console routes regardless of session status', () => {
    const navigate = vi.fn();
    expect(guardRoute({ status: 'anonymous' }, { screen: 'orbital' }, navigate)).toBe(false);
    expect(navigate).not.toHaveBeenCalled();
  });
});

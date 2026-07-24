/**
 * SpaceAware auth session store (loop task U0.3 — D1 groundwork).
 *
 * Tracks the server's httpOnly `sdn_wallet_session` cookie through
 * `GET /api/auth/me` hydration and `POST /api/auth/logout`. It exposes a
 * plain reactive-friendly state object so Svelte components can mirror it
 * into a `$state` rune without this module depending on the Svelte runtime.
 *
 * Phase 1A deliberately retains, but does not invoke, the document's typed
 * public wallet client. The current node endpoint verifies a legacy raw
 * challenge, while modern password-based wallet identities use the reviewed
 * canonical v2 protocol. Those protocols must not be bridged in browser code.
 * The separate server-auth-v2 cutover will add capability discovery and the
 * first typed authentication operation here once the server can verify it.
 *
 * `/api/**` never redirects (loop GROUND TRUTH) — this store only tracks
 * session STATE; whether an unauthenticated visit to a `/console` route
 * navigates to `/login` is a client-route-level decision made by the caller
 * (see `requiresAuthenticatedSession` below and its use in
 * `SpaceAwareApp.svelte`), never a side effect of an API response.
 */

import { SdnApiClient, SdnApiError, type AuthSessionUser, type SdnApiErrorBody } from './sdn-api-client';
import type { getSdnWalletClient } from './wallet-client';
import type { SpaceAwareRoute } from '../../spaceaware/router';

type SdnWalletClient = ReturnType<typeof getSdnWalletClient>;

export type AuthStatus = 'unknown' | 'anonymous' | 'authenticating' | 'authenticated' | 'error';
export type AuthStage = 'idle' | 'challenge' | 'verify' | 'confirmed';

export interface AuthSessionState {
  status: AuthStatus;
  stage: AuthStage;
  user: AuthSessionUser | null;
  error: SdnApiErrorBody | null;
}

export interface AuthStoreOptions {
  client: SdnApiClient;
  wallet: SdnWalletClient;
  onStateChange?: (state: AuthSessionState) => void;
}

export interface AuthStore {
  readonly state: AuthSessionState;
  readonly wallet: SdnWalletClient;
  /** `GET /api/auth/me` → `authenticated` (with `user`) or `anonymous` (401). Call once on boot. */
  hydrate(): Promise<void>;
  /** `POST /api/auth/logout`; only a confirmed response proves the httpOnly cookie was cleared. */
  logout(): Promise<void>;
}

/** `/console` routes require an authenticated session (D2: node-key/anon explore goes to `/orbital`, not `/console`). */
export function requiresAuthenticatedSession(route: Pick<SpaceAwareRoute, 'screen'>): boolean {
  return route.screen === 'console';
}

/**
 * Session guard for client-side routing: returns `true` (and, when
 * `navigate` is provided, redirects) when access to `route` should be
 * blocked pending authentication. Never touches the network — pure
 * decision over already-hydrated store state; callers must `hydrate()`
 * before relying on this for anything other than the loading placeholder.
 */
export function guardRoute(
  state: Pick<AuthSessionState, 'status'>,
  route: Pick<SpaceAwareRoute, 'screen'>,
  navigate?: (path: string) => void,
): boolean {
  if (!requiresAuthenticatedSession(route)) return false;
  if (state.status === 'authenticated') return false;
  if (state.status === 'unknown' || state.status === 'error') return false; // session truth is indeterminate — do not claim anonymous
  navigate?.('/login');
  return true;
}

export function createAuthStore(options: AuthStoreOptions): AuthStore {
  const client = options.client;

  const state: AuthSessionState = {
    status: 'unknown',
    stage: 'idle',
    user: null,
    error: null,
  };

  function publish(patch: Partial<AuthSessionState>): void {
    Object.assign(state, patch);
    options.onStateChange?.({ ...state });
  }

  async function hydrate(): Promise<void> {
    try {
      const user = await client.authMe();
      publish({ status: 'authenticated', stage: 'confirmed', user, error: null });
    } catch (err) {
      if (err instanceof SdnApiError && err.isUnauthorized) {
        publish({ status: 'anonymous', stage: 'idle', user: null, error: null });
        return;
      }
      // A transport/5xx failure does not prove an httpOnly session cookie is
      // absent. Keep the state explicitly indeterminate instead of publishing
      // a false anonymous result.
      publish({ status: 'error', error: toErrorBody(err) });
    }
  }

  async function logout(): Promise<void> {
    try {
      await client.authLogout();
      publish({ status: 'anonymous', stage: 'idle', user: null, error: null });
    } catch (err) {
      // The browser may still hold a live httpOnly cookie when the request did
      // not complete. Preserve the last known user and expose indeterminate
      // session truth to callers.
      publish({ status: 'error', error: toErrorBody(err) });
      throw err;
    }
  }

  return { state, wallet: options.wallet, hydrate, logout };
}

function toErrorBody(err: unknown): SdnApiErrorBody {
  if (err instanceof SdnApiError) {
    return err.body ?? { code: err.code, message: err.message };
  }
  return { code: 'client_error', message: err instanceof Error ? err.message : String(err) };
}

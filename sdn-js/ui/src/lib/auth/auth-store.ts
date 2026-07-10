/**
 * SpaceAware auth session store (loop task U0.3 — D1 groundwork).
 *
 * Drives the passwordless Ed25519 challenge/verify round trip
 * (`POST /api/auth/challenge` → sign locally via an `UnlockedWallet`
 * (local-wallet.ts) → `POST /api/auth/verify` → `sdn_wallet_session`
 * cookie set by the server → `GET /api/auth/me` hydration) and exposes a
 * plain reactive-friendly state object (pub/sub via `onStateChange`, the
 * same shape as the existing `createNodeIdentitySessionController` pattern
 * in `node-identity-session.ts`) so Svelte components can mirror it into a
 * `$state` rune at the call site without this module depending on the
 * Svelte runtime itself — it is plain, framework-agnostic TS and is unit
 * tested directly (see `spaceaware-auth-store.test.ts`).
 *
 * `/api/**` never redirects (loop GROUND TRUTH) — this store only tracks
 * session STATE; whether an unauthenticated visit to a `/console` route
 * navigates to `/login` is a client-route-level decision made by the caller
 * (see `requiresAuthenticatedSession` below and its use in
 * `SpaceAwareApp.svelte`), never a side effect of an API response.
 */

import { SdnApiClient, SdnApiError, type AuthSessionUser, type SdnApiErrorBody } from './sdn-api-client';
import type { UnlockedWallet } from './local-wallet';
import type { SpaceAwareRoute } from '../../spaceaware/router';

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
  onStateChange?: (state: AuthSessionState) => void;
  /** Injectable for tests; default `Date.now`. */
  now?: () => number;
}

export interface AuthStore {
  readonly state: AuthSessionState;
  /** `GET /api/auth/me` → `authenticated` (with `user`) or `anonymous` (401). Call once on boot. */
  hydrate(): Promise<void>;
  /** Full challenge → sign (via `wallet.sign`) → verify → `auth/me` hydration round trip. */
  loginWithWallet(wallet: UnlockedWallet): Promise<void>;
  /** `POST /api/auth/logout`; always resets local state to `anonymous`, even if the request fails. */
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
  if (state.status === 'unknown') return false; // hydration in flight — do not flash-redirect
  navigate?.('/login');
  return true;
}

export function createAuthStore(options: AuthStoreOptions): AuthStore {
  const client = options.client;
  const now = options.now ?? (() => Date.now());

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
      publish({ status: 'anonymous', stage: 'idle', user: null, error: toErrorBody(err) });
    }
  }

  async function loginWithWallet(wallet: UnlockedWallet): Promise<void> {
    publish({ status: 'authenticating', stage: 'challenge', error: null });
    try {
      const ts = Math.floor(now() / 1000);
      const challenge = await client.authChallenge({
        xpub: wallet.xpub,
        client_pubkey_hex: wallet.signingPublicKeyHex,
        ts,
      });

      publish({ stage: 'verify' });
      const challengeBytes = base64ToBytes(challenge.challenge);
      const signature = await wallet.sign(challengeBytes);
      const verifyResp = await client.authVerify({
        challenge_id: challenge.challenge_id,
        xpub: wallet.xpub,
        client_pubkey_hex: wallet.signingPublicKeyHex,
        challenge: challenge.challenge,
        signature_hex: bytesToHex(signature),
      });

      publish({ stage: 'confirmed', user: verifyResp.user });

      // auth/me is the hydration source of truth (acceptance: "me
      // hydrates") — re-fetch rather than trusting the verify response
      // alone, even though it already carries the user.
      await hydrate();
    } catch (err) {
      publish({ status: 'error', stage: 'idle', error: toErrorBody(err) });
      throw err;
    }
  }

  async function logout(): Promise<void> {
    try {
      await client.authLogout();
    } finally {
      publish({ status: 'anonymous', stage: 'idle', user: null, error: null });
    }
  }

  return { state, hydrate, loginWithWallet, logout };
}

function toErrorBody(err: unknown): SdnApiErrorBody {
  if (err instanceof SdnApiError) {
    return err.body ?? { code: err.code, message: err.message };
  }
  return { code: 'client_error', message: err instanceof Error ? err.message : String(err) };
}

function bytesToHex(bytes: Uint8Array): string {
  return Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
}

/** Decodes the server's `base64.RawStdEncoding` (unpadded, standard alphabet) challenge string. */
function base64ToBytes(base64: string): Uint8Array {
  const padded = base64 + '='.repeat((4 - (base64.length % 4)) % 4);
  const binary = atob(padded);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i += 1) bytes[i] = binary.charCodeAt(i);
  return bytes;
}

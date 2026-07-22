<script lang="ts">
  import { onMount } from 'svelte';
  import LoginScreen from './screens/LoginScreen.svelte';
  import ScaffoldScreen from './screens/ScaffoldScreen.svelte';
  import Bmc2Router from './screens/Bmc2Router.svelte';
  import ConsoleShell from './screens/ConsoleShell.svelte';
  import { createRouter, type SpaceAwareRoute } from './router';
  import { SdnApiClient } from '../lib/auth/sdn-api-client';
  import { createAuthStore, guardRoute, type AuthSessionState } from '../lib/auth/auth-store';
  import { getSdnWalletClient } from '../lib/auth/wallet-client';

  // U0.3 (D1 groundwork): one client/store for the whole app lifetime.
  // Session state is hydrated once on mount (`GET /api/auth/me`) and the
  // `/console` route family is guarded client-side per the no-redirect-on-API
  // rule — the guard is a route decision, never a side effect of the auth
  // API itself (see auth-store.ts).
  const apiClient = new SdnApiClient();
  const walletClient = getSdnWalletClient(document);
  let authState = $state<AuthSessionState>({ status: 'unknown', stage: 'idle', user: null, error: null });
  const authStore = createAuthStore({
    client: apiClient,
    wallet: walletClient,
    onStateChange: (next) => {
      authState = next;
    },
  });

  // The router is created at component init, not in onMount: children mount
  // (and run their onMount) before this component's onMount, and ConsoleShell
  // resolves `?route=` deep links via `navigate` during its own mount — a
  // mount-time router would hand every child a dead no-op for that first call.
  const router = createRouter((next) => {
    route = next;
    guardRoute(authState, next, navigate);
  });
  const navigate = router.navigate;
  let route = $state<SpaceAwareRoute>(router.current());

  onMount(() => {
    void authStore.hydrate().then(() => {
      guardRoute(authState, route, navigate);
    });

    return () => router.destroy();
  });
</script>

<div class="sa-root">
  {#if route.screen === 'login'}
    <LoginScreen {navigate} {apiClient} />
  {:else if route.screen === 'bmc2'}
    <Bmc2Router {route} {navigate} {authState} />
  {:else if route.screen === 'console'}
    <ConsoleShell {route} {navigate} {authState} {apiClient} />
  {:else}
    <ScaffoldScreen {route} {navigate} {authState} />
  {/if}
</div>

<script lang="ts">
  import { onMount } from 'svelte';
  import LoginScreen from './screens/LoginScreen.svelte';
  import ScaffoldScreen from './screens/ScaffoldScreen.svelte';
  import Bmc2Router from './screens/Bmc2Router.svelte';
  import { createRouter, routeFromLocation, type SpaceAwareRoute } from './router';
  import { SdnApiClient } from '../lib/auth/sdn-api-client';
  import { createAuthStore, guardRoute, type AuthSessionState } from '../lib/auth/auth-store';

  let route = $state<SpaceAwareRoute>(routeFromLocation(window.location));
  let navigate = $state<(path: string) => void>(() => {});

  // U0.3 (D1 groundwork): one client/store for the whole app lifetime.
  // Session state is hydrated once on mount (`GET /api/auth/me`) and the
  // `/console` route family is guarded client-side per the no-redirect-on-API
  // rule — the guard is a route decision, never a side effect of the auth
  // API itself (see auth-store.ts).
  const apiClient = new SdnApiClient();
  let authState = $state<AuthSessionState>({ status: 'unknown', stage: 'idle', user: null, error: null });
  const authStore = createAuthStore({
    client: apiClient,
    onStateChange: (next) => {
      authState = next;
    },
  });

  onMount(() => {
    const router = createRouter((next) => {
      route = next;
      guardRoute(authState, next, navigate);
    });
    navigate = router.navigate;
    route = router.current();

    void authStore.hydrate().then(() => {
      guardRoute(authState, route, navigate);
    });

    return () => router.destroy();
  });
</script>

<div class="sa-root">
  {#if route.screen === 'login'}
    <LoginScreen {navigate} {authStore} {authState} {apiClient} />
  {:else if route.screen === 'bmc2'}
    <Bmc2Router {route} {navigate} {authState} />
  {:else}
    <ScaffoldScreen {route} {navigate} {authState} />
  {/if}
</div>

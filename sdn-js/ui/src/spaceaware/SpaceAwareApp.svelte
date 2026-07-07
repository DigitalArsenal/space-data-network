<script lang="ts">
  import { onMount } from 'svelte';
  import LoginScreen from './screens/LoginScreen.svelte';
  import ScaffoldScreen from './screens/ScaffoldScreen.svelte';
  import { createRouter, routeFromLocation, type SpaceAwareRoute } from './router';

  let route = $state<SpaceAwareRoute>(routeFromLocation(window.location));
  let navigate = $state<(path: string) => void>(() => {});

  onMount(() => {
    const router = createRouter((next) => {
      route = next;
    });
    navigate = router.navigate;
    route = router.current();
    return () => router.destroy();
  });
</script>

<div class="sa-root">
  {#if route.screen === 'login'}
    <LoginScreen {navigate} />
  {:else}
    <ScaffoldScreen {route} {navigate} />
  {/if}
</div>

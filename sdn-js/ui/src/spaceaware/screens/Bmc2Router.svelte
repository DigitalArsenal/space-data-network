<script lang="ts">
  /**
   * Sub-router for `route.screen === 'bmc2'` (loop U2.1). The index and
   * F1–F3 boards are pixel-ported here; F4–F6 fall back to the existing
   * `ScaffoldScreen` placeholder until loop U2.2 ports them (same pattern
   * SpaceAwareApp.svelte already used for every other unported screen).
   */
  import type { SpaceAwareRoute } from '../router';
  import type { AuthSessionState } from '../../lib/auth/auth-store';
  import ScaffoldScreen from './ScaffoldScreen.svelte';
  import Bmc2Index from './bmc2/Bmc2Index.svelte';
  import Bmc2F1Surveillance from './bmc2/Bmc2F1Surveillance.svelte';
  import Bmc2F2Track from './bmc2/Bmc2F2Track.svelte';
  import Bmc2F3Sensors from './bmc2/Bmc2F3Sensors.svelte';

  let {
    route,
    navigate,
    authState,
  }: {
    route: SpaceAwareRoute;
    navigate: (path: string) => void;
    authState?: AuthSessionState;
  } = $props();
</script>

{#if route.sub === null}
  <Bmc2Index {navigate} />
{:else if route.sub === 'f1'}
  <Bmc2F1Surveillance {navigate} />
{:else if route.sub === 'f2'}
  <Bmc2F2Track {navigate} />
{:else if route.sub === 'f3'}
  <Bmc2F3Sensors {navigate} />
{:else}
  <ScaffoldScreen {route} {navigate} {authState} />
{/if}

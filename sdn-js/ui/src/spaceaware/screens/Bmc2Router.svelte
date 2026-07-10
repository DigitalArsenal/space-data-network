<script lang="ts">
  /**
   * Sub-router for `route.screen === 'bmc2'` (loop U2.1 / U2.2). The index
   * and all six F1–F6 mode boards are pixel-ported here; nothing left on
   * the `ScaffoldScreen` placeholder for this screen (same pattern
   * SpaceAwareApp.svelte uses for every other unported screen).
   */
  import type { SpaceAwareRoute } from '../router';
  import type { AuthSessionState } from '../../lib/auth/auth-store';
  import ScaffoldScreen from './ScaffoldScreen.svelte';
  import Bmc2Index from './bmc2/Bmc2Index.svelte';
  import Bmc2F1Surveillance from './bmc2/Bmc2F1Surveillance.svelte';
  import Bmc2F2Track from './bmc2/Bmc2F2Track.svelte';
  import Bmc2F3Sensors from './bmc2/Bmc2F3Sensors.svelte';
  import Bmc2F4Conjunction from './bmc2/Bmc2F4Conjunction.svelte';
  import Bmc2F5Maneuver from './bmc2/Bmc2F5Maneuver.svelte';
  import Bmc2F6Comms from './bmc2/Bmc2F6Comms.svelte';

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
{:else if route.sub === 'f4'}
  <Bmc2F4Conjunction {navigate} />
{:else if route.sub === 'f5'}
  <Bmc2F5Maneuver {navigate} />
{:else if route.sub === 'f6'}
  <Bmc2F6Comms {navigate} />
{:else}
  <ScaffoldScreen {route} {navigate} {authState} />
{/if}

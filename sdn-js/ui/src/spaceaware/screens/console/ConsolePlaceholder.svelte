<script lang="ts">
  /**
   * "Not yet ported" placeholder content for the 5 console views that
   * aren't NODE (loop U3.1 scope: only NODE gets a real view now — the
   * other 5 render their existing scaffold placeholder INSIDE this shell
   * instead of the old full-page `ScaffoldScreen`).
   *
   * Preserves `ScaffoldScreen.svelte`'s prior behavior for `route.sub ===
   * 'peers'`: the SdnGlobe demo panel (loop U0.2) stays mounted there until
   * the real PEER MAP wiring lands (U3.4).
   */
  import Panel from '../../primitives/Panel.svelte';
  import GlobeDemoPanel from '../GlobeDemoPanel.svelte';
  import { CONSOLE_TITLES } from '../../lib/console';
  import type { ConsoleView } from '../../router';

  let { view }: { view: ConsoleView } = $props();
</script>

<div class="sdn-console-placeholder">
  {#if view === 'peers'}
    <GlobeDemoPanel />
  {/if}

  <Panel title={`${CONSOLE_TITLES[view]} · not yet ported`} variant="well">
    <p class="sdn-console-placeholder-copy">
      This view is intentionally empty: no placeholder data is rendered
      until it is wired to real surfaces per the loop plan.
    </p>
  </Panel>
</div>

<style>
  .sdn-console-placeholder {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }

  .sdn-console-placeholder-copy {
    margin: 0;
    font-size: 11px;
    color: #5d7681;
  }
</style>

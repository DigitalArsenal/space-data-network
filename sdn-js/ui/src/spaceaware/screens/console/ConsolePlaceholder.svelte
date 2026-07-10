<script lang="ts">
  /**
   * "Not yet ported" placeholder content for the console views that aren't
   * NODE or PEERS (loop U3.1 scope: only NODE got a real view then; loop
   * U3.3 added PEERS — see `ConsoleShell.svelte`, which now routes `peers`
   * to `PeersView.svelte` directly and never reaches this component for
   * that view). The remaining views render their existing scaffold
   * placeholder INSIDE this shell instead of the old full-page
   * `ScaffoldScreen`.
   *
   * The SdnGlobe demo panel (loop U0.2) that used to live here for
   * `route.sub === 'peers'` moved out with it — the real PEER MAP widget
   * (NODE view, loop U3.2) and the PEERS directory (loop U3.3) both ship
   * without it now; the panel itself stays unused pending the real PEER MAP
   * canvas wiring (U3.4), at which point it's expected to be deleted rather
   * than re-attached here.
   */
  import Panel from '../../primitives/Panel.svelte';
  import { CONSOLE_TITLES } from '../../lib/console';
  import type { ConsoleView } from '../../router';

  let { view }: { view: ConsoleView } = $props();
</script>

<div class="sdn-console-placeholder">
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

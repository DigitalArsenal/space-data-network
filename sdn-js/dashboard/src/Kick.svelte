<script>
  /**
   * SECTION KICKER, scaled.
   *
   * The design's `Kicker` renders a BARE, CLASSLESS <span> (Kicker.svelte:8-10)
   * with its 10px size in a Svelte-scoped rule. There is no `style` prop to
   * pass and no class to select, and `:global(span)` would be unacceptable —
   * it would restyle every span in the dashboard. So the design component is
   * wrapped in an element we own and reached through THAT.
   *
   * `.k-wrap > :global(span)` is specificity (0,2,1), which beats the design's
   * (0,1,1) without `!important`. The wrapper is named `.k-wrap` and not `.k`
   * because `.k` is already an SDN section-kicker class in AccountAdmin
   * (IRIS ruling 2026-07-30 §4).
   *
   * Both kicker roles — this one and AccountAdmin's `.k` — sit on the SAME
   * rung: side-by-side kickers at different sizes is exactly the
   * inconsistency the owner reports.
   *
   * @type {{ text: string, color?: string|null }}
   */
  import Kicker from 'spaceaware-student-sdn/src/lib/components/Kicker.svelte';
  let { text, color = null } = $props();
</script>

<span class="k-wrap">
  {#if color}<Kicker {text} {color} />{:else}<Kicker {text} />{/if}
</span>

<style>
  .k-wrap { display: contents; }
  .k-wrap > :global(span) {
    font-size: var(--sdn-fs-fine);
    line-height: var(--sdn-lh-fine);
  }
</style>

<script>
  /**
   * NETWORK THROUGHPUT.
   *
   * DESIGN SOURCE (declared, not copied):
   *   SpaceAware-UI @ archive/SpaceAware.io 2/SDN Console.dc.html
   *   sha256 abacdbfc62aeaee1193eccec9087669bfeb2324422fe8223482556fad207f152
   *   widget :207-219 · registry entry :867
   *
   * The export highlights bar index 8, which is fixture styling and means
   * nothing on a live ring — here the NEWEST bar is the highlighted one (IRIS
   * constraint (d)). The axis states the span the bars ACTUALLY cover: a node up
   * for twenty seconds has four samples, and labelling that −60s would be a lie
   * about the window.
   */
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import { formatRateMBs, sparkBars, sparkSpanS } from '../../runtime.js';

  let { runtime } = $props();

  const bars = $derived(sparkBars(runtime?.history));
  const spanS = $derived(sparkSpanS(bars.length));
  const rateIn = $derived(formatRateMBs(runtime?.rateInBps));
  const rateOut = $derived(formatRateMBs(runtime?.rateOutBps));
</script>

<div class="wkick" style="color:{theme.textMuted};">NETWORK THROUGHPUT</div>
{#if rateIn !== '' || rateOut !== ''}
  <div class="thead">
    <span class="thnum mono" style="color:{theme.textBright};">{rateIn || '0.00'}</span>
    <span class="thunit" style="color:{theme.textDim};">MB/s ↓</span>
    <span class="thnum2 mono" style="color:{theme.ice};">{rateOut || '0.00'}</span>
    <span class="thunit" style="color:{theme.textDim};">↑</span>
  </div>
{/if}
<div class="spark">
  {#each bars as bar, i (i)}
    <span
      class="sbar"
      style="height:{bar.pct}%;background:linear-gradient(180deg,{bar.newest ? theme.ice : theme.cyan},transparent);"
    ></span>
  {/each}
</div>
<div class="saxis" style="color:{theme.textMuted};">
  <span>{spanS ? `−${spanS}s` : ''}</span>
  <span>NOW</span>
</div>

<style>
  .wkick {
    font-size: var(--sdn-fs-micro);
    line-height: var(--sdn-lh-micro);
    letter-spacing: 0.2em;
    display: block;
  }

  .thead { display: flex; align-items: baseline; gap: var(--sdn-sp-3); flex-wrap: wrap; }
  /* IRIS §7 — the design's throughput face (26.5) re-snapped to the title rung. */
  .thnum {
    font-weight: 600;
    font-size: var(--sdn-fs-title);
    line-height: var(--sdn-lh-title);
    font-variant-numeric: tabular-nums;
  }
  .thnum2 {
    font-size: var(--sdn-fs-note);
    line-height: var(--sdn-lh-note);
    font-variant-numeric: tabular-nums;
    margin-left: var(--sdn-sp-1);
  }
  .thunit { font-size: var(--sdn-fs-body); line-height: var(--sdn-lh-body); }
  .mono { font-family: 'IBM Plex Mono', ui-monospace, monospace; }

  .spark { display: flex; align-items: flex-end; gap: 2px; margin-top: var(--sdn-sp-6); height: 64px; }
  .sbar { flex: 1; min-width: 0; }
  .saxis {
    display: flex;
    justify-content: space-between;
    font-size: var(--sdn-fs-micro);
    line-height: var(--sdn-lh-micro);
    letter-spacing: 0.08em;
    margin-top: var(--sdn-sp-3);
  }
</style>

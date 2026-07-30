<script>
  /**
   * NODE HEALTH.
   *
   * DESIGN SOURCE (declared, not copied):
   *   SpaceAware-UI @ archive/SpaceAware.io 2/SDN Console.dc.html
   *   sha256 abacdbfc62aeaee1193eccec9087669bfeb2324422fe8223482556fad207f152
   *   widget :122-140 · registry entry :863
   *
   * API is the origin this console is talking to — a fact about this page, not a
   * guess about the host's config. GATEWAY is only claimed once the node's own
   * /ipfs/ mount answers for the empty-block CID (bafkqaaa is identity-inlined,
   * so the probe costs no network fetch). A node without that mount shows no
   * GATEWAY row rather than a plausible-looking URL.
   */
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import { formatBytes, storageFraction } from '../../runtime.js';

  let { node, runtime } = $props();

  const online = $derived(Boolean(node?.online));
  const privileged = $derived(Boolean(runtime?.privileged));
  const storageUsed = $derived(formatBytes(runtime?.storeBytes));
  const storageCapacity = $derived(formatBytes(runtime?.diskCapacityBytes));
  const storageFrac = $derived(storageFraction(runtime?.storeBytes, runtime?.diskCapacityBytes));

  let apiOrigin = $state('');
  let gatewayBase = $state('');
  $effect(() => {
    apiOrigin = globalThis.location?.origin ?? '';
    if (gatewayBase) return;
    fetch('/ipfs/bafkqaaa', { method: 'HEAD' })
      .then((res) => {
        if (res.ok) gatewayBase = `${globalThis.location?.origin ?? ''}/ipfs`;
      })
      .catch(() => {});
  });
</script>

<span class="tick tl" style="border-color:{theme.ice};"></span>
<span class="tick tr" style="border-color:{theme.ice};"></span>
<span class="tick bl" style="border-color:{theme.ice};"></span>
<span class="tick br" style="border-color:{theme.ice};"></span>
<div class="wkick" style="color:{theme.textMuted};">NODE HEALTH</div>
<div class="hero">
  <span class="dot" style="background:{online ? theme.green : theme.textMuted};box-shadow:0 0 9px {online ? theme.green : theme.textMuted};"></span>
  <span class="heroval" style="color:{theme.textBright};">{online ? 'ONLINE' : 'OFFLINE'}</span>
</div>
{#if runtime?.mode}
  <div class="sub" style="color:{theme.textDim};">MODE · {runtime.mode.toUpperCase()}</div>
{/if}
<div class="cells fill">
  <div class="cell">
    <div class="clabel" style="color:{theme.textMuted};">PEER ID</div>
    <div class="cval mono break" style="color:{theme.textBody};">{node?.peerId || ''}</div>
  </div>
  <div class="crow">
    {#if apiOrigin}
      <div class="cell">
        <div class="clabel" style="color:{theme.textMuted};">API</div>
        <div class="cval mono break" style="color:{theme.textBody};">{apiOrigin}</div>
      </div>
    {/if}
    {#if gatewayBase}
      <div class="cell">
        <div class="clabel" style="color:{theme.textMuted};">GATEWAY</div>
        <div class="cval mono break" style="color:{theme.textBody};">{gatewayBase}</div>
      </div>
    {/if}
  </div>
  {#if privileged && storageUsed}
    <!-- STORAGE renders ONLY from the admin snapshot (IRIS condition C1).
         /api/v1/stats looks like the anonymous source for this, but it seeds
         `total_bytes: 0` and serves that on a store-read budget miss — so it can
         report an EMPTY store for a busy one. A measurement or nothing. -->
    <div class="cell">
      <div class="sline">
        <span class="clabel" style="color:{theme.textMuted};">STORAGE</span>
        <span class="cval num" style="color:{theme.textBright};">
          <!-- &nbsp; because Svelte trims the leading space out of the span and
               the design reads "4.8 GB / 32 GB", not "4.8 GB/ 32 GB". -->
          {storageUsed}{#if storageCapacity}<span style="color:{theme.textMuted};">&nbsp;/ {storageCapacity}</span>{/if}
        </span>
      </div>
      {#if storageFrac !== null}
        <!-- The bar renders ONLY with a real capacity. A null `disk` (statfs
             unavailable) must never become a zero-capacity bar — the node
             reports null so this does not happen. -->
        <div class="bar" style="background:{theme.divider};">
          <div class="fill" style="width:{(storageFrac * 100).toFixed(1)}%;background:linear-gradient(90deg,{theme.cyan},{theme.ice});"></div>
        </div>
      {/if}
    </div>
  {/if}
</div>

<style>
  /* Corner ticks — the design's accent brackets. Positioned against the Panel,
     which NodeConsole renders `position:relative`. */
  .tick { position: absolute; width: 9px; height: 9px; }
  .tick.tl { top: -1px; left: -1px; border-top: 1px solid; border-left: 1px solid; }
  .tick.tr { top: -1px; right: -1px; border-top: 1px solid; border-right: 1px solid; }
  .tick.bl { bottom: -1px; left: -1px; border-bottom: 1px solid; border-left: 1px solid; }
  .tick.br { bottom: -1px; right: -1px; border-bottom: 1px solid; border-right: 1px solid; }

  .wkick {
    font-size: var(--sdn-fs-micro);
    line-height: var(--sdn-lh-micro);
    letter-spacing: 0.2em;
    display: block;
  }

  .hero { display: flex; align-items: baseline; gap: var(--sdn-sp-3); }
  .dot { width: 9px; height: 9px; border-radius: 50%; flex: none; }
  /* IRIS §7 — the design's hero face re-snapped to the nearest rung (ONLINE 31
     -> hero). Never re-multiplied. */
  .heroval {
    font-family: 'Chakra Petch', sans-serif;
    font-weight: 700;
    font-size: var(--sdn-fs-hero);
    line-height: var(--sdn-lh-hero);
    letter-spacing: 0.06em;
  }
  .sub {
    font-size: var(--sdn-fs-label);
    line-height: var(--sdn-lh-label);
    letter-spacing: 0.04em;
    margin: var(--sdn-sp-1) 0 var(--sdn-sp-6);
  }

  .cells { display: flex; flex-direction: column; gap: var(--sdn-sp-4); min-width: 0; }
  /* Take the leftover height and SPREAD the cells through it, so the last one
     lands on the panel floor as the export composes it — rather than leaving one
     void beneath a top-packed stack (C4). */
  .cells.fill { flex: 1; justify-content: space-between; }
  .crow { display: flex; gap: var(--sdn-sp-8); flex-wrap: wrap; }
  .crow .cell { flex: 1 1 40%; min-width: 0; }
  .cell { min-width: 0; }
  .clabel {
    font-size: var(--sdn-fs-micro);
    line-height: var(--sdn-lh-micro);
    letter-spacing: 0.14em;
  }
  .cval {
    font-size: var(--sdn-fs-note);
    line-height: var(--sdn-lh-note);
    margin-top: 2px;
  }
  .cval.break { overflow-wrap: anywhere; }
  .cval.num { font-variant-numeric: tabular-nums; }
  .mono { font-family: 'IBM Plex Mono', ui-monospace, monospace; }

  .sline { display: flex; justify-content: space-between; align-items: baseline; gap: var(--sdn-sp-4); }
  .bar { height: 6px; margin-top: var(--sdn-sp-2); }
  .bar .fill { height: 100%; }
</style>

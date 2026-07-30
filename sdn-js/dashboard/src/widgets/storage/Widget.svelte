<script>
  /**
   * STORAGE.
   *
   * DESIGN SOURCE (declared, not copied):
   *   SpaceAware-UI @ archive/SpaceAware.io 2/SDN Console.dc.html
   *   sha256 abacdbfc62aeaee1193eccec9087669bfeb2324422fe8223482556fad207f152
   *   widget :221-228 · registry entry :870
   *
   * IRIS §3: renderable from `store.total_bytes` + `disk.capacity_bytes`, `disk`
   * nullable and NEVER a fabricated 0. The design's hero splits the number from
   * its unit ("4.8" / "/ 32 GB"); `formatBytes` returns them joined, so the split
   * happens here rather than by re-implementing the formatter.
   *
   * The export's two footer lines ("6 STANDARDS SYNCED · FRESH", "SCHEMA v1.0.3 ·
   * synced") are fixtures about a standards-sync surface this node does not
   * publish, so they are replaced by the two facts the same snapshot really
   * carries: how many records are in the store, and how much room is left.
   */
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import { formatBytes, storageFraction } from '../../runtime.js';

  let { runtime } = $props();

  const storageUsed = $derived(formatBytes(runtime?.storeBytes));
  const storageCapacity = $derived(formatBytes(runtime?.diskCapacityBytes));
  const storageFrac = $derived(storageFraction(runtime?.storeBytes, runtime?.diskCapacityBytes));
  const storageNumber = $derived(storageUsed.split(' ')[0] ?? '');
  const storageUnit = $derived(storageUsed.split(' ')[1] ?? '');
  const storageAvailable = $derived(formatBytes(runtime?.diskAvailableBytes));
  const storageRecords = $derived(
    typeof runtime?.storeRecords === 'number' ? runtime.storeRecords.toLocaleString('en-US') : ''
  );
</script>

<div class="wkick" style="color:{theme.textMuted};">STORAGE</div>
{#if storageUsed}
  <div class="thead">
    <span class="storenum mono" style="color:{theme.textBright};">{storageNumber}</span>
    <span class="thunit" style="color:{theme.textDim};">
      {storageUnit}{#if storageCapacity} / {storageCapacity}{/if}
    </span>
  </div>
  {#if storageFrac !== null}
    <div class="bar tall" style="background:{theme.divider};">
      <div class="fill" style="width:{(storageFrac * 100).toFixed(1)}%;background:linear-gradient(90deg,{theme.cyan},{theme.ice});"></div>
    </div>
  {/if}
  <div class="cells fill foot">
    {#if storageRecords}
      <div class="sline">
        <span class="clabel" style="color:{theme.textMuted};">RECORDS</span>
        <span class="cval num" style="color:{theme.textBody};">{storageRecords}</span>
      </div>
    {/if}
    {#if storageAvailable}
      <!-- Free space on the store's own filesystem. Absent whenever the statfs
           probe is (the node reports `disk: null` rather than a fabricated
           zero), which is the same rule the bar follows. -->
      <div class="sline">
        <span class="clabel" style="color:{theme.textMuted};">AVAILABLE</span>
        <span class="cval num" style="color:{theme.textBody};">{storageAvailable}</span>
      </div>
    {/if}
  </div>
{:else}
  <div class="empty" style="color:{theme.textFaint};">Store totals are not readable on this node.</div>
{/if}

<style>
  .wkick {
    font-size: var(--sdn-fs-micro);
    line-height: var(--sdn-lh-micro);
    letter-spacing: 0.2em;
    display: block;
  }

  .thead { display: flex; align-items: baseline; gap: var(--sdn-sp-3); flex-wrap: wrap; }
  /* The design's storage hero is 29 — the same rung as the page title (27), per
     IRIS §3: "no new rung — re-snap STORAGE 29 -> --sdn-fs-title". */
  .storenum {
    font-weight: 600;
    font-size: var(--sdn-fs-title);
    line-height: var(--sdn-lh-title);
    font-variant-numeric: tabular-nums;
  }
  .thunit { font-size: var(--sdn-fs-body); line-height: var(--sdn-lh-body); }
  .mono { font-family: 'IBM Plex Mono', ui-monospace, monospace; }

  .cells { display: flex; flex-direction: column; gap: var(--sdn-sp-4); min-width: 0; }
  .cells.fill { flex: 1; justify-content: space-between; }
  .cells.foot { justify-content: flex-end; }
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
  .cval.num { font-variant-numeric: tabular-nums; }

  .sline { display: flex; justify-content: space-between; align-items: baseline; gap: var(--sdn-sp-4); }
  .bar { height: 6px; margin-top: var(--sdn-sp-2); }
  .bar.tall { height: 7px; margin-top: var(--sdn-sp-5); }
  .bar .fill { height: 100%; }

  .empty {
    font-size: var(--sdn-fs-body);
    line-height: var(--sdn-lh-body);
    letter-spacing: 0.02em;
    margin-top: var(--sdn-sp-4);
  }
</style>

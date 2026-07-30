<script>
  /**
   * ACTIVITY LOG.
   *
   * DESIGN SOURCE (declared, not copied):
   *   SpaceAware-UI @ archive/SpaceAware.io 2/SDN Console.dc.html
   *   sha256 abacdbfc62aeaee1193eccec9087669bfeb2324422fe8223482556fad207f152
   *   widget :231-241 · registry entry :871
   *
   * The ring, NEWEST FIRST, paginated — never scrolled (grammar law L4,
   * iris-dashboard-grammar-law). `activityRead` distinguishes "not read yet"
   * from "read and genuinely empty": an in-memory ring starts empty at boot, and
   * saying so is different from saying nothing (IRIS R4).
   */
  import StatusChip from 'spaceaware-student-sdn/src/lib/components/StatusChip.svelte';
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import Pager from '../../Pager.svelte';
  import { ACTIVITY_ROWS, activityRow } from '../../runtime.js';
  import { shortId } from '../../format.js';

  let { runtime } = $props();

  let activityPage = $state(0);
  const activityRows = $derived((runtime?.activity ?? []).map((ev) => activityRow(ev, shortId)));
  const activityPages = $derived(Math.max(1, Math.ceil(activityRows.length / ACTIVITY_ROWS)));
  const activitySafePage = $derived(Math.min(activityPage, activityPages - 1));
  const activityVisible = $derived(
    activityRows.slice(activitySafePage * ACTIVITY_ROWS, (activitySafePage + 1) * ACTIVITY_ROWS)
  );
</script>

<div class="whead">
  <span class="wkick" style="color:{theme.textMuted};">ACTIVITY LOG</span>
  {#if activityRows.length}
    <span class="hchips">
      <StatusChip label={`${activityRows.length} EVENTS`} color={theme.ice} dot={false} />
    </span>
  {/if}
</div>
{#if activityVisible.length}
  <div class="alist">
    {#each activityVisible as row, i (`${row.clock}:${i}`)}
      <div class="arow">
        <span class="aclock mono" style="color:{theme.textMuted};">{row.clock}</span>
        <span class="adot" style="background:{theme[row.token] ?? theme.textDim};"></span>
        <span class="atext" style="color:{theme.textBody};">{row.text}</span>
      </div>
    {/each}
  </div>
  {#if activityPages > 1}
    <!-- L4: overflow PAGINATES through the one shared pager. -->
    <Pager
      page={activitySafePage}
      pageCount={activityPages}
      total={activityRows.length}
      pageSize={ACTIVITY_ROWS}
      unit="EVENTS"
      compact
      onPage={(p) => (activityPage = p)}
    />
  {/if}
{:else}
  <div class="empty" style="color:{theme.textFaint};">
    {runtime?.activityRead
      ? 'No activity recorded since this node started.'
      : 'Reading the activity ring…'}
  </div>
{/if}

<style>
  .wkick {
    font-size: var(--sdn-fs-micro);
    line-height: var(--sdn-lh-micro);
    letter-spacing: 0.2em;
    display: block;
  }
  .whead {
    display: flex;
    align-items: baseline;
    gap: var(--sdn-sp-4);
    flex-wrap: wrap;
    margin-bottom: var(--sdn-sp-5);
  }
  .whead .wkick { margin-bottom: 0; }
  .hchips { display: inline-flex; align-items: center; gap: var(--sdn-sp-2); margin-left: auto; flex-wrap: wrap; }

  .empty {
    font-size: var(--sdn-fs-body);
    line-height: var(--sdn-lh-body);
    letter-spacing: 0.02em;
    margin-top: var(--sdn-sp-4);
  }

  /* ACTIVITY LOG rows (`:233-239`). No max-height and no scroller: the pager
     below is how this widget handles more than it can show (L4). */
  .alist { display: flex; flex-direction: column; gap: var(--sdn-sp-3); min-width: 0; }
  .arow { display: flex; align-items: baseline; gap: var(--sdn-sp-4); min-width: 0; }
  .aclock {
    font-size: var(--sdn-fs-micro);
    line-height: var(--sdn-lh-micro);
    font-variant-numeric: tabular-nums;
    flex: none;
  }
  .mono { font-family: 'IBM Plex Mono', ui-monospace, monospace; }
  .adot { width: 6px; height: 6px; border-radius: 50%; flex: none; align-self: center; }
  .atext {
    font-size: var(--sdn-fs-body);
    line-height: var(--sdn-lh-body);
    flex: 1;
    min-width: 0;
    overflow-wrap: anywhere;
  }
</style>

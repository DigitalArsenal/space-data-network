<script>
  /**
   * THE PEERS ROUTE (owner directive 2026-07-30). The swarm map and the peer
   * directory it plots, in one column: the SAME PeerMap component the dashboard's
   * netmap widget renders, never a second globe surface (IRIS R7 + the grammar
   * law's companion trap — two mounted globes are two WebGL contexts).
   *
   * L5: no panel here carries a width, a max-width or its own inline padding, so
   * every edge is the column's edge.
   *
   * All of the search / filter / sort / pagination state below used to live in
   * App.svelte, where it was 90 lines that only this page read. It is route-local
   * now, which is what lets a PEERS task and an ACCOUNTS task run at once.
   */
  import Panel from 'spaceaware-student-sdn/src/lib/components/Panel.svelte';
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import NodeTable from '../../NodeTable.svelte';
  import PeerMap from '../../PeerMap.svelte';
  import Pager from '../../Pager.svelte';
  import AccountAdmin from '../../AccountAdmin.svelte';
  import { shortId } from '../../format.js';
  import { TRUST_TIERS } from '../../trust.js';
  import { canManagePermissions } from '../../permissions.js';
  import { applySettings, substringSearch, semanticRank, sortNodes } from '../../filters.js';
  import { onSearchStatus, queryScores, searchStatus } from './search.js';

  let {
    view = null,
    accountNodes = [],
    accountRows = [],
    now = 0,
    session = null,
    rootAdminAvailable = null,
    presence,
    selected = null,
    onSelectNode = () => {},
    onRequestSignIn = () => {},
  } = $props();

  const PAGE_SIZE = 5;
  const HIDE_KEY = 'sdn.dashboard.hideUntrustedOffline';
  const readHidePref = () => {
    try {
      const raw = globalThis.localStorage?.getItem(HIDE_KEY);
      return raw === null || raw === undefined ? true : raw === '1';
    } catch {
      return true;
    }
  };

  let query = $state('');
  let trustTier = $state('all');
  let hideUntrustedOffline = $state(readHidePref());
  let sortKey = $state('trust');
  let sortDir = $state(1);
  let settingsOpen = $state(false);
  let page = $state(0);
  let semStatus = $state(searchStatus());
  /** @type {Map<string, number> | null} */
  let semScores = $state(null);

  $effect(() => onSearchStatus((s) => (semStatus = s)));

  const updatedAgo = $derived(
    view ? Math.max(0, Math.round((now - view.generatedAt) / 1000)) : null
  );
  const visible = $derived(applySettings(accountNodes, { trustTier, hideUntrustedOffline }));
  const searching = $derived(Boolean(query.trim()));
  const semanticActive = $derived(searching && semStatus === 'ready' && semScores !== null);

  /** @type {{node: any, score?: number}[]} */
  const rows = $derived.by(() => {
    if (!searching) return sortNodes(visible, sortKey, sortDir).map((node) => ({ node }));
    const subs = substringSearch(visible, query);
    if (semanticActive) {
      return semanticRank(visible, semScores, new Set(subs.map((n) => n.peerId)));
    }
    return sortNodes(subs, sortKey, sortDir).map((node) => ({ node }));
  });

  const pageCount = $derived(Math.max(1, Math.ceil(rows.length / PAGE_SIZE)));
  const safePage = $derived(Math.min(page, pageCount - 1));
  const pagedRows = $derived(rows.slice(safePage * PAGE_SIZE, (safePage + 1) * PAGE_SIZE));

  // Any change to the filtered set snaps back to the first page.
  $effect(() => {
    void query;
    void trustTier;
    void hideUntrustedOffline;
    void sortKey;
    void sortDir;
    page = 0;
  });

  // Debounced query embedding → semScores drives the semantic ranking.
  $effect(() => {
    const q = query.trim();
    if (!q || semStatus !== 'ready') {
      semScores = null;
      return;
    }
    const t = setTimeout(async () => {
      const scores = await queryScores(q);
      if (query.trim() === q) semScores = scores;
    }, 220);
    return () => clearTimeout(t);
  });

  /*
   * OWNER DIRECTIVE 2026-07-30, twice: "also remove 'semantic' and ALL
   * superfluous descriptions / tags from all the menus here, you are shitting up
   * the interface with this garbage".
   *
   * The search-mode chip is GONE — label, colour and tooltip together. It was a
   * status word bolted onto a search control, which is the exact class the owner
   * named. `semStatus` is still what selects the ranking function above; the
   * search box simply no longer narrates which one won. A control is its label.
   */

  function toggleSort(key) {
    if (sortKey === key) sortDir = -sortDir;
    else {
      sortKey = key;
      sortDir = 1;
    }
  }

  function setHidePref(checked) {
    hideUntrustedOffline = checked;
    try {
      globalThis.localStorage?.setItem(HIDE_KEY, checked ? '1' : '0');
    } catch {
      /* private mode etc. — non-persistent is fine */
    }
  }
</script>

<svelte:window onkeydown={(e) => e.key === 'Escape' && settingsOpen && (settingsOpen = false)} />

<div class="toolbar">
  <div class="search" style="border-color:{theme.hairline};">
    <span class="sglyph" style="color:{theme.textMuted};">⌕</span>
    <input
      type="search"
      placeholder="SEARCH"
      bind:value={query}
      style="color:{theme.textBright};"
      aria-label="Search nodes"
    />
  </div>

  <label class="ctl" style="color:{theme.textMuted};">
    TRUST
    <select bind:value={trustTier} style="color:{theme.textBright};border-color:{theme.hairline};" aria-label="Filter by trust tier">
      <option value="all">ALL</option>
      {#each TRUST_TIERS as tier (tier)}
        <option value={tier}>{tier.toUpperCase()}</option>
      {/each}
    </select>
  </label>

  <div class="settings-wrap">
    <button
      class="settings-btn"
      style="color:{settingsOpen ? theme.cyan : theme.textMuted};border-color:{settingsOpen ? theme.cyan : theme.hairline};"
      onclick={() => (settingsOpen = !settingsOpen)}
      aria-expanded={settingsOpen}
      aria-haspopup="true"
    ><span class="gear">⚙</span> SETTINGS</button>
    {#if settingsOpen}
      <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
      <div class="settings-backdrop" onclick={() => (settingsOpen = false)}></div>
      <div class="settings-menu" style="background:{theme.panelRaised};border-color:{theme.panelBorder};">
        <div class="settings-title" style="color:{theme.textMuted};border-color:{theme.divider};">DISPLAY SETTINGS</div>
        <!-- This menu said the same thing THREE times: the checkbox label, a
             96-character `title` restating it, and a hint paragraph restating it
             again. The label survives; the other two are the "garbage" the owner
             named (2026-07-30, twice). -->
        <label class="ctl check" style="color:{theme.textBody};">
          <input
            type="checkbox"
            checked={hideUntrustedOffline}
            onchange={(e) => setHidePref(e.currentTarget.checked)}
          />
          HIDE UNTRUSTED OFFLINE
        </label>
      </div>
    {/if}
  </div>
</div>

<!-- The table's own arithmetic, stated as a subset rather than as a total: a
     filter is on this toolbar and "12 PEERS" beside a table of three is the same
     class of defect as the header count was. -->
<div class="meta" style="color:{theme.textMuted};">
  <span>{rows.length} OF {accountRows.length} SHOWN</span>
  <span class="dot">·</span>
  <span>{presence.connected} CONNECTED</span>
  {#if presence.pinnedOffline}
    <span class="dot">·</span>
    <span>{presence.pinnedOffline} PINNED, OFFLINE</span>
  {/if}
  <span class="dot">·</span>
  <span>SOURCE {shortId(view?.sourcePeerId ?? '')}</span>
  <span class="dot">·</span>
  <span>UPDATED {updatedAgo === 0 ? 'now' : `${updatedAgo}s ago`}</span>
</div>

<div class="stack">
  <Panel variant="raised">
    <PeerMap
      nodes={accountNodes}
      selectedId={selected?.peerId ?? ''}
      onSelectNode={onSelectNode}
      height="420px"
    />
  </Panel>
  <Panel variant="raised" pad="0">
    <div class="table-panel">
      <NodeTable
        rows={pagedRows}
        {now}
        {sortKey}
        {sortDir}
        onSort={toggleSort}
        onOpen={onSelectNode}
        {semanticActive}
      />
      <!-- L4: the ONE shared pager. This was an inline copy; the ACTIVITY LOG
           widget needed the same control, and two implementations is how
           "widgets paginate" becomes "widgets mostly paginate". -->
      <Pager
        page={safePage}
        pageCount={pageCount}
        total={rows.length}
        pageSize={PAGE_SIZE}
        unit="ROWS"
        onPage={(p) => (page = p)}
      />
    </div>
  </Panel>

  {#if session && canManagePermissions(session.trustLevel)}
    <!-- ADD A PEER lives with the peers (owner directive 2026-07-30). It is the
         SAME component the ACCOUNTS tab uses, rendering only its form section —
         one set of API calls, one piece of state. -->
    <AccountAdmin
      {session}
      nodes={accountNodes}
      {rootAdminAvailable}
      onRequestSignIn={onRequestSignIn}
      view="peer-add"
    />
  {/if}
</div>

<style>
  .toolbar {
    display: flex;
    align-items: center;
    gap: 18px;
    flex-wrap: wrap;
    margin-bottom: 12px;
  }
  .search {
    display: flex;
    align-items: center;
    gap: 8px;
    border: 1px solid;
    padding: 7px 10px;
    flex: 1 1 320px;
    min-width: 260px;
    max-width: 560px;
  }
  .sglyph { font-size: var(--sdn-fs-head); line-height: var(--sdn-lh-head); }
  .search input {
    flex: 1;
    background: transparent;
    border: 0;
    outline: none;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: var(--sdn-fs-value); line-height: var(--sdn-lh-value);
    letter-spacing: 0.06em;
    min-width: 0;
  }
  .search input::placeholder { color: rgba(159, 212, 245, 0.35); }
  /* MENU SCALE. Originally the owner's 2026-07-27 directive, applied here by
     hand. The comment that stood here claimed "body text, tables and panels
     are deliberately untouched: they were already scaled by the earlier
     global directive" — that was FALSE, and it is why the owner had to give
     the same instruction twice: measured on the built page, tables rendered
     at 10-14.5px, i.e. unscaled. Sizes now come from the ladder in
     scale.css, which covers every surface; padding/tracking stay nudged only
     where larger glyphs would clip. */
  .ctl {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    font-size: var(--sdn-fs-body); line-height: var(--sdn-lh-body);
    letter-spacing: 0.14em; /* eased from 0.16em so TRUST/HIDE… stay on one line */
    white-space: nowrap;
  }
  /* L6 (owner directive 2026-07-30: "the arrow in the drop down should have
     margin"): a <select> reserves INSET space for the chevron the platform draws
     inside its box. Without it "ALL" sat against the arrow and the arrow against
     the border. --sdn-sp-8 (20px) is the law's floor for this. */
  .ctl select {
    background: transparent;
    border: 1px solid;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: var(--sdn-fs-note); line-height: var(--sdn-lh-note);
    letter-spacing: 0.08em;
    padding: 6px 10px;
    padding-right: var(--sdn-sp-8);
    outline: none;
  }
  .ctl select option { background: #0a141b; }
  .ctl.check { cursor: pointer; user-select: none; }
  .ctl.check input {
    appearance: none;
    width: 17px; /* the tick keeps pace with its label */
    height: 17px;
    border: 1px solid rgba(110, 170, 190, 0.5);
    background: transparent;
    cursor: pointer;
    display: inline-grid;
    place-content: center;
    margin: 0;
  }
  .ctl.check input::before {
    content: '';
    width: 9px;
    height: 9px;
    transform: scale(0);
    background: #35c9d8;
  }
  .ctl.check input:checked::before { transform: scale(1); }
  /* THE METALINE WRAPS (defect, owner's 390px screenshot 2026-07-30). This was a
     nowrap flex row inside `main`, which is `overflow-x: hidden` — so at phone
     widths it did not scroll and it did not wrap, it was CUT: measured 592px of
     content in a 285px box, losing the last 307px. The words it lost were the
     ones the page was rewritten to say ("1 PINNED, OFFLINE", "UPDATED …"), and
     it is the surface the header's own count chips DELEGATE to below 1180px
     (see the L3 block in App.svelte) — the one place a phone can read them. Two
     rules do it:
       flex-wrap  the LIST of facts breaks onto as many lines as it needs;
       nowrap     a single fact never splits, so "3 OF 3 SHOWN" cannot render as
                  "3 OF" / "3 SHOWN" the way it did at 390px. */
  .meta {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 9px;
    row-gap: 2px;
    font-size: var(--sdn-fs-data); line-height: var(--sdn-lh-data);
    letter-spacing: 0.14em;
    margin-bottom: 12px;
  }
  .meta > span { white-space: nowrap; }
  .meta .dot { opacity: 0.5; }
  /* L1 + L5: a plain column of panels, each at its NATURAL height, all the same
     width. It used to be a flex race (`flex: 1 1 50%` on each panel against a
     `min-height:480px` parent), which is what squeezed a panel to zero height
     while its pager kept painting — the overlap in the owner's screenshot. The
     page scrolls; nothing here competes for height. */
  .stack {
    display: flex;
    flex-direction: column;
    gap: var(--sdn-sp-7);
  }
  /* L1: no `flex: 1` height race and no inner scroller — the table is as tall as
     its five rows and the pager sits under it. */
  .table-panel { display: flex; flex-direction: column; }
  .settings-wrap { position: relative; }
  .settings-backdrop { position: fixed; inset: 0; z-index: 29; }
  .settings-btn {
    background: transparent;
    border: 1px solid;
    cursor: pointer;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: var(--sdn-fs-body); line-height: var(--sdn-lh-body);
    letter-spacing: 0.14em;
    padding: 8px 14px;
    white-space: nowrap;
    display: inline-flex;
    align-items: center;
    gap: var(--sdn-sp-3);
  }
  /* L6 (owner directive 2026-07-30: "the settings icon needs to be full size"): a
     glyph beside a text label renders at the label's rung or ONE ABOVE it, never
     below. The gear was inheriting the button's --sdn-fs-body and reading as a
     rendering failure next to its own word; it now sits one rung up at
     --sdn-fs-data with its own line-height so the button box does not grow. */
  .gear {
    font-size: var(--sdn-fs-data);
    line-height: 1;
  }
  .settings-menu {
    position: absolute;
    top: calc(100% + 6px);
    right: 0;
    z-index: 30;
    border: 1px solid;
    /* Widened for the scaled type so the hint does not re-wrap — but
       min-width beats max-width in CSS, so the cap has to live INSIDE the
       min() or a 390px phone pushes the popover off the left edge. */
    min-width: min(390px, calc(100vw - 32px));
    max-width: calc(100vw - 32px);
    padding: 14px 16px 15px;
    box-shadow: 0 14px 44px rgba(0, 0, 0, 0.5);
  }
  .settings-title {
    font-size: var(--sdn-fs-label); line-height: var(--sdn-lh-label);
    letter-spacing: 0.16em;
    border-bottom: 1px solid;
    padding-bottom: 7px;
    margin-bottom: 10px;
  }

  /* Narrow screens (owner report: taps landing on clipped targets): the desktop
     half-and-half no-scroll layout collapses tap targets into 140px strips. On
     phones the page scrolls naturally instead. */
  @media (max-width: 760px) {
    .toolbar {
      gap: 10px;
    }
    .search {
      flex-basis: 100%;
      max-width: none;
    }
    /* At the +30% menu size the popover is wider than the toolbar row it hangs
       off, and that row has wrapped — anchoring to it pushes the menu off the
       left edge. Pin it to the viewport instead; the existing full-screen
       backdrop already dismisses it. */
    .settings-menu {
      position: fixed;
      inset: auto 12px 12px 12px;
      min-width: 0;
      max-width: none;
    }
  }

  /* Phones — the same 560px tier NodeTable's column ladder uses, so the table
     and the line above it change shape at one width rather than two.
     scale.css assigns the metaline rung to `micro`; at --sdn-fs-data its
     longest single fact ("SOURCE 16Uiu2…1uF5PP") measures 266px, which still
     does not fit a 320px viewport's 215px content box even after wrapping, and
     wrapping alone spends five lines at 390px. At `micro` it fits every phone
     width and costs four. Desktop keeps `data` — this is the one rung the
     narrow tier lowers. */
  @media (max-width: 560px) {
    .meta {
      font-size: var(--sdn-fs-micro);
      line-height: var(--sdn-lh-micro);
    }
  }
</style>

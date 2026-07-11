<script lang="ts">
  /**
   * GROUPS console view (loop task U3.8). Ground truth: the
   * `<!-- ============ GROUPS ============ -->` block in
   * `design_handoff/sdn_console/SDN Console.dc.html` (lines ~321-402) — a
   * PRIMITIVES legend + ALL/MY GROUPS/PEER GROUPS filter strip (span 12), a
   * GROUP DIRECTORY table (left, span 7), and a group detail +
   * CONJUNCTION MONITOR card (right, span 5). The shared `ConsoleHeader`
   * already renders "GROUPS · SHARED ACROSS NODES" (`lib/console.ts`), so
   * this view starts at the PRIMITIVES legend.
   *
   * DECISION D5 (client-local groups, no server surface) — unlike PEERS/
   * DATA/CHANNELS, every group definition here lives ONLY in
   * `localStorage['sdn_shared_groups']`. All data wiring/view-model
   * building lives in `../../lib/groups-data.ts` — see that file's doc
   * comment for the extracted mock schema, the demo-badging rules (decision
   * D4: peer/provider ownership AND conjunction status are both fabricated
   * on this build and carry a DEMO tag styled like `Bmc2TopBar.svelte`'s),
   * and why this view — not `ConsoleShell.svelte` — is what finally
   * consumes the `?group=` deep link `console.ts`'s
   * `parseConsoleDeepLinkQuery` has captured (but left unconsumed) since
   * loop U3.1.
   *
   * CRUD: the mock has NO create/delete UI for groups anywhere in this
   * template (`allGroups()` only ever READS `sdn_shared_groups`). This
   * view adds a minimal "+ NEW GROUP" row-style button under the directory
   * (opens an inline NAME/REGIME/SCOPE form) and a small "✕" remove
   * control on MY-GROUP rows only — both styled with this app's existing
   * ghost-button convention (`PeersView.svelte`'s `.sdn-peers-btn--ghost`:
   * square corners, uppercase, transparent/bordered). Deleting a peer/
   * provider group is impossible by construction — `deleteGroup` (see
   * `groups-data.ts`) no-ops for any `owner!=='self'` id, and the row
   * control itself only renders for `owner:'self'` rows.
   *
   * Selection semantics mirror `PeersView.svelte`/`ChannelsView.svelte`:
   * the detail card tracks a selected group id against the FULL
   * (unfiltered) group list, defaulting to the first group, so switching
   * the ALL/MY GROUPS/PEER GROUPS tabs never clears an existing selection
   * just because it scrolled out of the filtered view.
   */
  import { onMount } from 'svelte';
  import { parseConsoleDeepLinkQuery } from '../../lib/console';
  import {
    GROUPS_CONJUNCTION_CONSOLE_PATH,
    GROUPS_CONJUNCTION_DEMO_TAG_TITLE,
    GROUPS_LEGEND_CAPTION,
    GROUPS_OWNERSHIP_DEMO_TAG_TITLE,
    GROUP_FILTER_TABS,
    GROUP_PRIMITIVES,
    GROUP_REGIME_OPTIONS,
    buildGroupDetailView,
    buildGroupRows,
    createGroup,
    deleteGroup,
    filterGroups,
    groupFilterTabStyle,
    groupsCountCaption,
    loadSharedGroups,
    resolveDeepLinkGroupId,
    resolveSelectedGroup,
    saveSharedGroups,
    validateCreateGroupInput,
    type GroupFilterTab,
    type SharedGroup,
  } from '../../lib/groups-data';

  let { navigate }: { navigate: (path: string) => void } = $props();

  let groups = $state<SharedGroup[]>([]);
  let loaded = $state(false);
  let activeTab = $state<GroupFilterTab>('all');
  let selectedId = $state<string | null>(null);

  let createOpen = $state(false);
  let createName = $state('');
  let createRegime = $state<string>('ALL');
  let createScope = $state('');
  let createError = $state<string | null>(null);

  const filteredGroups = $derived(filterGroups(groups, activeTab));
  const rows = $derived(buildGroupRows(filteredGroups, selectedId));
  const selectedGroup = $derived(resolveSelectedGroup(groups, selectedId));
  const detailView = $derived(selectedGroup ? buildGroupDetailView(selectedGroup) : null);
  const caption = $derived(groupsCountCaption(groups));

  onMount(() => {
    const loadedGroups = loadSharedGroups(window.localStorage);
    groups = loadedGroups;
    loaded = true;
    try {
      const { group: groupParam } = parseConsoleDeepLinkQuery(window.location.search);
      const resolved = resolveDeepLinkGroupId(loadedGroups, groupParam);
      if (resolved) selectedId = resolved;
    } catch {
      // Malformed query string — nothing to select.
    }
  });

  function persist(next: SharedGroup[]) {
    groups = next;
    saveSharedGroups(window.localStorage, next);
  }

  function selectGroup(id: string) {
    selectedId = id;
  }

  function setTab(tab: GroupFilterTab) {
    activeTab = tab;
  }

  function openCreateForm() {
    createOpen = true;
    createName = '';
    createRegime = 'ALL';
    createScope = '';
    createError = null;
  }

  function cancelCreateForm() {
    createOpen = false;
    createError = null;
  }

  function submitCreateForm() {
    const input = { name: createName, regime: createRegime, scope: createScope };
    const validationError = validateCreateGroupInput(input);
    if (validationError) {
      createError = validationError;
      return;
    }
    const next = createGroup(groups, input);
    const created = next[next.length - 1];
    persist(next);
    if (created) selectedId = created.id;
    createOpen = false;
    createError = null;
  }

  function removeGroup(event: MouseEvent, id: string) {
    event.stopPropagation();
    persist(deleteGroup(groups, id));
    if (selectedId === id) selectedId = null;
  }

  function openIn3d() {
    if (!detailView) return;
    navigate(detailView.openIn3dPath);
  }

  function screenForConjunctions() {
    navigate(GROUPS_CONJUNCTION_CONSOLE_PATH);
  }
</script>

<div class="sdn-groups-root">
  <section class="sdn-groups-legend">
    <span class="sdn-groups-legend-kicker">PRIMITIVES</span>
    <div class="sdn-groups-legend-items">
      {#each GROUP_PRIMITIVES as pr (pr.id)}
        <div class="sdn-groups-legend-item">
          <span class="sdn-groups-legend-glyph" style={`color:${pr.color};`}>{pr.glyph}</span>
          <span class="sdn-groups-legend-text">
            <span class="sdn-groups-legend-label-row">
              <span class="sdn-groups-legend-label">{pr.label}</span>
              {#if pr.id === 'peergroup'}
                <span class="sdn-groups-demo-tag" title={GROUPS_OWNERSHIP_DEMO_TAG_TITLE}>DEMO</span>
              {/if}
            </span>
            <span class="sdn-groups-legend-sub">{pr.sub}</span>
          </span>
        </div>
      {/each}
    </div>
    <span class="sdn-groups-legend-spacer"></span>
    <span class="sdn-groups-legend-caption">{GROUPS_LEGEND_CAPTION}</span>
    <div class="sdn-groups-filter-tabs">
      {#each GROUP_FILTER_TABS as tab (tab.id)}
        {@const style = groupFilterTabStyle(tab.id, activeTab)}
        <button
          type="button"
          class="sdn-groups-filter-tab"
          style={`color:${style.color};border-color:${style.border};background:${style.background};`}
          title={`Filter the directory to ${tab.label.toLowerCase()}`}
          onclick={() => setTab(tab.id)}
        >
          {tab.label}
        </button>
      {/each}
    </div>
  </section>

  <section class="sdn-groups-directory">
    <div class="sdn-groups-directory-header">
      <span class="sdn-groups-directory-title">GROUP DIRECTORY</span>
      <span class="sdn-groups-directory-caption">
        <span class="sdn-groups-caption-mine">{caption.mineCount}</span> mine · <span class="sdn-groups-caption-peer"
          >{caption.peerCount}</span
        > peer-defined
      </span>
    </div>
    <div class="sdn-groups-row-header">
      <span></span>
      <span>GROUP / SCOPE</span>
      <span>OWNER</span>
      <span>OBJ</span>
      <span>CONJUNCTION</span>
      <span></span>
    </div>
    <div class="sdn-groups-rows">
      {#if !loaded}
        <div class="sdn-groups-empty">LOADING GROUPS…</div>
      {:else if rows.length === 0}
        <div class="sdn-groups-empty">NO GROUPS IN THIS FILTER</div>
      {:else}
        {#each rows as row (row.id)}
          <div
            class="sdn-groups-row"
            role="button"
            tabindex="0"
            title={`View details for ${row.name}`}
            style={`background:${row.selected ? 'rgba(74,166,224,0.1)' : 'transparent'};border-left-color:${row.glyphColor};`}
            onclick={() => selectGroup(row.id)}
            onkeydown={(event) => {
              if (event.key === 'Enter' || event.key === ' ') selectGroup(row.id);
            }}
          >
            <span class="sdn-groups-row-glyph" style={`color:${row.glyphColor};`}>{row.glyph}</span>
            <span class="sdn-groups-row-name-cell">
              <span class="sdn-groups-row-name">{row.name}</span>
              <br />
              <span class="sdn-groups-row-sub">{row.regimeScope}</span>
            </span>
            <span class="sdn-groups-row-owner" style={`color:${row.ownerColor};`}>{row.ownerName}</span>
            <span class="sdn-groups-row-count">{row.countLabel}</span>
            <span class="sdn-groups-row-conj">
              {#if row.conj.hasData}
                <span
                  class="sdn-groups-row-conj-dot"
                  style={`background:${row.conj.dotColor};box-shadow:0 0 6px ${row.conj.dotColor};`}
                ></span>
                <span class="sdn-groups-row-conj-label" style={`color:${row.conj.dotColor};`}>{row.conj.label}</span>
                <span class="sdn-groups-row-conj-count">{row.conj.countSuffix}</span>
              {:else}
                <span class="sdn-groups-row-conj-label sdn-groups-row-conj-label--dash">{row.conj.label}</span>
              {/if}
            </span>
            <span class="sdn-groups-row-actions">
              {#if row.isMine}
                <button
                  type="button"
                  class="sdn-groups-row-remove"
                  title={`Delete ${row.name} (client-local only — this cannot be undone)`}
                  onclick={(event) => removeGroup(event, row.id)}
                >
                  ✕
                </button>
              {/if}
            </span>
          </div>
        {/each}
      {/if}
    </div>

    <div class="sdn-groups-new-row">
      <button
        type="button"
        class="sdn-groups-btn sdn-groups-btn--ghost sdn-groups-new-btn"
        title="Create a new client-local group"
        onclick={openCreateForm}
      >
        + NEW GROUP
      </button>
    </div>

    {#if createOpen}
      <form
        class="sdn-groups-create-form"
        onsubmit={(event) => {
          event.preventDefault();
          submitCreateForm();
        }}
      >
        <label class="sdn-groups-create-field">
          <span>NAME</span>
          <input type="text" bind:value={createName} placeholder="e.g. LEO Constellation B" title="Group name" />
        </label>
        <label class="sdn-groups-create-field">
          <span>REGIME</span>
          <div class="sdn-groups-regime-pills">
            {#each GROUP_REGIME_OPTIONS as option (option)}
              <button
                type="button"
                class="sdn-groups-regime-pill"
                class:sdn-groups-regime-pill--active={createRegime === option}
                title={`Set regime to ${option}`}
                onclick={() => (createRegime = option)}
              >
                {option}
              </button>
            {/each}
          </div>
        </label>
        <label class="sdn-groups-create-field">
          <span>SCOPE</span>
          <input type="text" bind:value={createScope} placeholder="e.g. custody shell description" title="Free-text scope description" />
        </label>
        <div class="sdn-groups-create-form-buttons">
          <button type="submit" class="sdn-groups-btn sdn-groups-btn--screen" title="Create this group">CREATE GROUP</button>
          <button type="button" class="sdn-groups-btn sdn-groups-btn--ghost" onclick={cancelCreateForm} title="Cancel">CANCEL</button>
        </div>
        {#if createError}
          <div class="sdn-groups-create-error">{createError}</div>
        {/if}
      </form>
    {/if}
  </section>

  <section class="sdn-groups-detail">
    {#if detailView}
      <div class="sdn-groups-detail-head">
        <span class="sdn-groups-detail-glyph" style={`color:${detailView.glyphColor};`}>{detailView.glyph}</span>
        <div class="sdn-groups-detail-head-text">
          <div class="sdn-groups-detail-name">{detailView.name}</div>
          <div class="sdn-groups-detail-scope">{detailView.scope}</div>
        </div>
      </div>
      <div class="sdn-groups-detail-kind" style={`border-color:${detailView.kindBorder};background:${detailView.kindBg};`}>
        <span style={`color:${detailView.kindColor};`}>{detailView.kindLabel}</span>
      </div>
      <div class="sdn-groups-detail-owner">
        <span class="sdn-groups-detail-owner-glyph" style={`color:${detailView.ownerColor};`}>◍</span>
        <div class="sdn-groups-detail-owner-text">
          <div class="sdn-groups-detail-owner-label-row">
            <span class="sdn-groups-field-label">DEFINED BY</span>
            {#if detailView.isOwnershipDemo}
              <span class="sdn-groups-demo-tag" title={GROUPS_OWNERSHIP_DEMO_TAG_TITLE}>DEMO</span>
            {/if}
          </div>
          <div class="sdn-groups-detail-owner-value" style={`color:${detailView.ownerColor};`}>{detailView.ownerName}</div>
        </div>
      </div>
      <div class="sdn-groups-detail-grid">
        <div>
          <div class="sdn-groups-field-label">REGIME</div>
          <div class="sdn-groups-field-value">{detailView.regime}</div>
        </div>
        <div>
          <div class="sdn-groups-field-label">MEMBERS</div>
          <div class="sdn-groups-field-value">{detailView.membersLabel}</div>
        </div>
        <div>
          <div class="sdn-groups-field-label">UPDATED</div>
          <div class="sdn-groups-field-value">{detailView.updatedLabel}</div>
        </div>
      </div>
      <div class="sdn-groups-conj-section">
        <div class="sdn-groups-conj-header">
          <span class="sdn-groups-conj-title-row">
            <span class="sdn-groups-field-label">CONJUNCTION MONITOR</span>
            {#if detailView.conjunction.isDemo}
              <span class="sdn-groups-demo-tag" title={GROUPS_CONJUNCTION_DEMO_TAG_TITLE}>DEMO</span>
            {/if}
          </span>
          <span class="sdn-groups-conj-status">
            <span
              class="sdn-groups-conj-status-dot"
              style={`background:${detailView.conjunction.dotColor};box-shadow:0 0 7px ${detailView.conjunction.dotColor};`}
            ></span>
            <span class="sdn-groups-conj-status-label" style={`color:${detailView.conjunction.dotColor};`}
              >{detailView.conjunction.label}</span
            >
          </span>
        </div>
        <div class="sdn-groups-conj-sub">{detailView.conjunction.subText}</div>
        {#if detailView.conjunction.events.length > 0}
          <div class="sdn-groups-conj-events">
            {#each detailView.conjunction.events as ev (ev.object)}
              <div class="sdn-groups-conj-event" style={`border-left-color:${ev.stateColor};`}>
                <span class="sdn-groups-conj-event-object">{ev.object}</span>
                <span class="sdn-groups-conj-event-tca">{ev.tca}</span>
                <span class="sdn-groups-conj-event-miss" style={`color:${ev.stateColor};`}>{ev.missKm} km</span>
                <span class="sdn-groups-conj-event-pc">{ev.pc}</span>
              </div>
            {/each}
          </div>
        {/if}
      </div>
      <div class="sdn-groups-detail-buttons">
        <button
          type="button"
          class="sdn-groups-btn sdn-groups-btn--screen"
          style={`background:${detailView.screenButton.background};border-color:${detailView.screenButton.border};color:${detailView.screenButton.color};`}
          title="Screen this group for conjunctions (opens the CONJUNCTION console view)"
          onclick={screenForConjunctions}
        >
          {detailView.screenButton.glyph} {detailView.screenButton.label}
        </button>
        <button type="button" class="sdn-groups-btn sdn-groups-btn--open3d" title="Open this group in the 3D Orbital Console" onclick={openIn3d}>
          OPEN IN 3D
        </button>
      </div>
      {#if detailView.readOnlyNote}
        <div class="sdn-groups-readonly-note">{detailView.readOnlyNote}</div>
      {/if}
    {:else}
      <div class="sdn-groups-directory-title">GROUP DETAIL</div>
      <div class="sdn-groups-detail-empty">NO GROUP SELECTED</div>
    {/if}
  </section>
</div>

<style>
  .sdn-groups-root {
    display: grid;
    grid-template-columns: repeat(12, minmax(0, 1fr));
    gap: 14px;
    align-content: start;
  }

  .sdn-groups-legend,
  .sdn-groups-directory,
  .sdn-groups-detail {
    background: linear-gradient(178deg, #16252f, #0a141b);
    border: 1px solid rgba(90, 150, 180, 0.22);
    box-shadow: inset 0 1px 0 rgba(150, 210, 240, 0.14);
  }

  /* -- legend + filter strip -- */

  .sdn-groups-legend {
    grid-column: span 12;
    padding: 13px 16px;
    display: flex;
    align-items: center;
    gap: 18px;
    flex-wrap: wrap;
  }

  .sdn-groups-legend-kicker {
    font-size: 10px;
    letter-spacing: 0.2em;
    color: #5a7a8a;
    flex: none;
  }

  .sdn-groups-legend-items {
    display: flex;
    gap: 16px;
    flex-wrap: wrap;
  }

  .sdn-groups-legend-item {
    display: flex;
    align-items: center;
    gap: 7px;
  }

  .sdn-groups-legend-glyph {
    font-size: 17px;
    line-height: 1;
  }

  .sdn-groups-legend-text {
    display: flex;
    flex-direction: column;
    line-height: 1.15;
  }

  .sdn-groups-legend-label-row {
    display: flex;
    align-items: center;
    gap: 5px;
  }

  .sdn-groups-legend-label {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 600;
    font-size: 12px;
    letter-spacing: 0.06em;
    color: #cfe3ec;
  }

  .sdn-groups-legend-sub {
    font-size: 9.5px;
    letter-spacing: 0.04em;
    color: #6f8693;
  }

  .sdn-groups-legend-spacer {
    flex: 1;
  }

  .sdn-groups-legend-caption {
    font-size: 10px;
    letter-spacing: 0.06em;
    color: #6f8693;
  }

  .sdn-groups-filter-tabs {
    display: flex;
    gap: 6px;
  }

  .sdn-groups-filter-tab {
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 11.5px;
    letter-spacing: 0.06em;
    border: 1px solid;
    padding: 4px 11px;
    cursor: pointer;
    transition:
      border-color 0.14s,
      color 0.14s,
      background 0.14s;
  }

  /* -- DEMO tag (same style as Bmc2TopBar.svelte's .bmc-demo-tag) -- */

  .sdn-groups-demo-tag {
    padding: 1px 5px;
    border: 1px solid rgba(255, 208, 137, 0.5);
    color: #ffd089;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 8px;
    letter-spacing: 0.12em;
    flex: none;
  }

  /* -- group directory -- */

  .sdn-groups-directory {
    grid-column: span 7;
    display: flex;
    flex-direction: column;
    min-width: 0;
    padding: 15px 16px;
  }

  .sdn-groups-directory-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 12px;
  }

  .sdn-groups-directory-title {
    font-size: 10px;
    letter-spacing: 0.2em;
    color: #5a7a8a;
    margin-bottom: 12px;
  }

  .sdn-groups-directory-header .sdn-groups-directory-title {
    margin-bottom: 0;
  }

  .sdn-groups-directory-caption {
    font-size: 10px;
    letter-spacing: 0.06em;
    color: #5d7681;
  }

  .sdn-groups-caption-mine {
    color: #c77dff;
  }

  .sdn-groups-caption-peer {
    color: #ff9e64;
  }

  .sdn-groups-row-header {
    display: grid;
    grid-template-columns: 20px 2fr 1.2fr 0.7fr 1fr 20px;
    gap: 0 12px;
    padding: 0 4px 7px;
    border-bottom: 1px solid rgba(90, 150, 180, 0.14);
    font-size: 9.5px;
    letter-spacing: 0.14em;
    color: #5a7a8a;
    flex: none;
  }

  .sdn-groups-rows {
    overflow-y: auto;
    max-height: calc(100vh - 380px);
  }

  .sdn-groups-empty {
    padding: 24px 4px;
    font-size: 11px;
    letter-spacing: 0.06em;
    color: #5d7681;
    text-align: center;
  }

  .sdn-groups-row {
    display: grid;
    grid-template-columns: 20px 2fr 1.2fr 0.7fr 1fr 20px;
    gap: 0 12px;
    align-items: center;
    padding: 11px 4px;
    border-bottom: 1px solid rgba(90, 150, 180, 0.07);
    border-left: 2px solid transparent;
    cursor: pointer;
  }

  .sdn-groups-row-glyph {
    font-size: 15.5px;
    line-height: 1;
  }

  .sdn-groups-row-name-cell {
    min-width: 0;
  }

  .sdn-groups-row-name {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 600;
    font-size: 14.5px;
    color: #eaf6f8;
  }

  .sdn-groups-row-sub {
    font-size: 10px;
    color: #6f8693;
  }

  .sdn-groups-row-owner {
    font-size: 11.5px;
    overflow-wrap: anywhere;
  }

  .sdn-groups-row-count {
    font-size: 12.5px;
    color: #cfe3ec;
    font-variant-numeric: tabular-nums;
  }

  .sdn-groups-row-conj {
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }

  .sdn-groups-row-conj-dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    flex: none;
  }

  .sdn-groups-row-conj-label {
    font-size: 11px;
    letter-spacing: 0.04em;
  }

  .sdn-groups-row-conj-label--dash {
    color: #5a7a8a;
  }

  .sdn-groups-row-conj-count {
    font-size: 10px;
    color: #6f8693;
  }

  .sdn-groups-row-actions {
    display: flex;
    justify-content: flex-end;
  }

  .sdn-groups-row-remove {
    width: 18px;
    height: 18px;
    display: flex;
    align-items: center;
    justify-content: center;
    background: transparent;
    border: 1px solid rgba(255, 107, 107, 0.35);
    color: #d68a8a;
    font-size: 10px;
    line-height: 1;
    padding: 0;
    cursor: pointer;
  }

  .sdn-groups-row-remove:hover {
    border-color: rgba(255, 107, 107, 0.6);
    color: #ffb3b3;
    background: rgba(255, 107, 107, 0.1);
  }

  .sdn-groups-new-row {
    margin-top: 10px;
    padding-top: 10px;
    border-top: 1px solid rgba(90, 150, 180, 0.12);
  }

  .sdn-groups-new-btn {
    width: 100%;
  }

  /* -- CREATE GROUP inline form -- */

  .sdn-groups-create-form {
    display: flex;
    flex-direction: column;
    gap: 9px;
    margin-top: 12px;
    padding-top: 12px;
    border-top: 1px solid rgba(90, 150, 180, 0.12);
  }

  .sdn-groups-create-field {
    display: flex;
    flex-direction: column;
    gap: 4px;
    font-size: 9.5px;
    letter-spacing: 0.1em;
    color: #5a7a8a;
  }

  .sdn-groups-create-field input {
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 12px;
    letter-spacing: 0.02em;
    color: #eaf6f8;
    background: #090d12;
    border: 1px solid rgba(90, 150, 180, 0.3);
    padding: 6px 8px;
    outline: none;
  }

  .sdn-groups-create-field input::placeholder {
    color: #5a7a8a;
  }

  .sdn-groups-regime-pills {
    display: flex;
    flex-wrap: wrap;
    gap: 5px;
  }

  .sdn-groups-regime-pill {
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 11px;
    letter-spacing: 0.04em;
    background: transparent;
    border: 1px solid rgba(90, 150, 180, 0.3);
    color: #9fb3bc;
    padding: 4px 10px;
    cursor: pointer;
  }

  .sdn-groups-regime-pill--active {
    border-color: rgba(120, 190, 230, 0.55);
    color: #9fd4f5;
    background: rgba(74, 166, 224, 0.14);
  }

  .sdn-groups-create-form-buttons {
    display: flex;
    gap: 6px;
    margin-top: 4px;
  }

  .sdn-groups-create-error {
    font-size: 10.5px;
    line-height: 1.4;
    color: #ff8d8d;
  }

  /* -- group detail + CONJUNCTION MONITOR -- */

  .sdn-groups-detail {
    grid-column: span 5;
    min-width: 0;
    overflow-y: auto;
    max-height: calc(100vh - 230px);
    padding: 15px 16px;
  }

  .sdn-groups-detail-head {
    display: flex;
    align-items: center;
    gap: 9px;
    margin-bottom: 13px;
  }

  .sdn-groups-detail-glyph {
    font-size: 24px;
    line-height: 1;
  }

  .sdn-groups-detail-head-text {
    flex: 1;
    min-width: 0;
  }

  .sdn-groups-detail-name {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 700;
    font-size: 19px;
    color: #eaf6f8;
    line-height: 1.1;
    overflow-wrap: anywhere;
  }

  .sdn-groups-detail-scope {
    font-size: 11px;
    color: #7d929b;
    margin-top: 2px;
  }

  .sdn-groups-detail-kind {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    border: 1px solid;
    padding: 5px 10px;
    margin-bottom: 14px;
    font-size: 11px;
    letter-spacing: 0.08em;
  }

  .sdn-groups-detail-owner {
    display: flex;
    align-items: center;
    gap: 8px;
    background: rgba(255, 255, 255, 0.015);
    border: 1px solid rgba(90, 150, 180, 0.18);
    padding: 8px 11px;
    margin-bottom: 14px;
  }

  .sdn-groups-detail-owner-glyph {
    font-size: 13px;
  }

  .sdn-groups-detail-owner-text {
    flex: 1;
    min-width: 0;
  }

  .sdn-groups-detail-owner-label-row {
    display: flex;
    align-items: center;
    gap: 6px;
  }

  .sdn-groups-detail-owner-value {
    font-size: 13px;
    margin-top: 1px;
  }

  .sdn-groups-detail-grid {
    display: grid;
    grid-template-columns: 1fr 1fr 1fr;
    gap: 11px;
    margin-bottom: 15px;
  }

  .sdn-groups-field-label {
    font-size: 9.5px;
    letter-spacing: 0.14em;
    color: #5a7a8a;
  }

  .sdn-groups-field-value {
    font-size: 13px;
    color: #cfe3ec;
    margin-top: 2px;
  }

  .sdn-groups-conj-section {
    border-top: 1px solid rgba(90, 150, 180, 0.12);
    padding-top: 13px;
  }

  .sdn-groups-conj-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 10px;
    gap: 8px;
  }

  .sdn-groups-conj-title-row {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
  }

  .sdn-groups-conj-status {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    flex: none;
  }

  .sdn-groups-conj-status-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
  }

  .sdn-groups-conj-status-label {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 700;
    font-size: 13px;
    letter-spacing: 0.06em;
  }

  .sdn-groups-conj-sub {
    font-size: 11px;
    color: #9a8a8a;
    line-height: 1.5;
    margin-bottom: 11px;
  }

  .sdn-groups-conj-events {
    display: flex;
    flex-direction: column;
    gap: 0;
    border: 1px solid rgba(90, 150, 180, 0.16);
  }

  .sdn-groups-conj-event {
    display: grid;
    grid-template-columns: 1.1fr 1.3fr 0.8fr 0.9fr;
    gap: 0 10px;
    align-items: center;
    padding: 8px 10px;
    border-bottom: 1px solid rgba(90, 150, 180, 0.08);
    border-left: 2px solid;
  }

  .sdn-groups-conj-event-object {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 600;
    font-size: 13px;
    color: #eaf6f8;
  }

  .sdn-groups-conj-event-tca {
    font-size: 11px;
    color: #9fb3bc;
  }

  .sdn-groups-conj-event-miss {
    font-size: 12px;
    font-variant-numeric: tabular-nums;
  }

  .sdn-groups-conj-event-pc {
    font-size: 11.5px;
    color: #cfe3ec;
    font-variant-numeric: tabular-nums;
  }

  .sdn-groups-detail-buttons {
    display: flex;
    gap: 6px;
    margin-top: 15px;
  }

  .sdn-groups-btn {
    padding: 9px 0;
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 600;
    font-size: 11.5px;
    letter-spacing: 0.08em;
    cursor: pointer;
    transition:
      border-color 0.14s,
      color 0.14s,
      background 0.14s;
  }

  .sdn-groups-btn--screen {
    flex: 1.5;
    border: 1px solid;
  }

  .sdn-groups-btn--open3d {
    flex: 1;
    display: flex;
    align-items: center;
    justify-content: center;
    background: rgba(74, 166, 224, 0.1);
    border: 1px solid rgba(120, 190, 230, 0.45);
    color: #9fd4f5;
  }

  .sdn-groups-btn--open3d:hover {
    border-color: rgba(120, 190, 230, 0.6);
    color: #eaf6f8;
    background: rgba(74, 166, 224, 0.2);
  }

  .sdn-groups-btn--ghost {
    flex: 1;
    background: transparent;
    border: 1px solid rgba(90, 150, 180, 0.3);
    color: #9fb3bc;
  }

  .sdn-groups-btn--ghost:hover {
    border-color: rgba(120, 190, 230, 0.6);
    color: #eaf6f8;
    background: rgba(74, 166, 224, 0.08);
  }

  .sdn-groups-readonly-note {
    font-size: 9.5px;
    color: #6f8693;
    line-height: 1.5;
    margin-top: 10px;
  }

  .sdn-groups-detail-empty {
    font-size: 11px;
    color: #5d7681;
    letter-spacing: 0.06em;
  }
</style>

<script>
  /**
   * Sortable node table (replaces the card grid — owner directive in
   * graph/tasks/nst-dashboard-table.md). Row click opens the detail modal.
   * Styled only with theme.js tokens; column sorters live in filters.js.
   */
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import { normalizeTrust, TRUST_COLOR_TOKEN } from './trust.js';
  import { shortId, formatLastSeen, formatUptime } from './format.js';
  import { accountDisplayName, accountFromNode, isUnnamed, kindLabel } from './accounts.js';

  /**
   * @type {{
   *   rows: {node: any, score?: number}[],
   *   now: number,
   *   sortKey: string, sortDir: number,
   *   onSort: (key: string) => void,
   *   onOpen: (node: any) => void,
   *   semanticActive?: boolean,
   * }}
   */
  let { rows = [], now, sortKey, sortDir, onSort, onOpen, semanticActive = false } = $props();

  // wide: hidden on narrow screens (<=760px) so the remaining columns fit a
  // phone without a nested horizontal scroller swallowing taps.
  const COLS = [
    { key: 'node', label: 'NAME' },
    { key: 'org', label: 'ORGANIZATION', wide: true },
    { key: 'trust', label: 'TRUST' },
    { key: 'status', label: 'STATUS' },
    { key: 'geo', label: 'GEO', wide: true },
    { key: 'agent', label: 'AGENT', wide: true },
    { key: 'seen', label: 'LAST SEEN' },
  ];

  const trustColor = (level) => theme[TRUST_COLOR_TOKEN[normalizeTrust(level)]] ?? theme.textMuted;
</script>

<div class="scroller">
  <table>
    <thead>
      <tr style="color:{theme.textMuted};border-color:{theme.panelBorder};">
        {#each COLS as col (col.key)}
          <th onclick={() => onSort(col.key)} class:active={sortKey === col.key} class:wide={col.wide} style="color:{sortKey === col.key ? theme.ice : theme.textMuted};">
            {col.label}{#if sortKey === col.key}<span class="dir">{sortDir === 1 ? '▲' : '▼'}</span>{/if}
          </th>
        {/each}
        {#if semanticActive}<th class="num" style="color:{theme.textMuted};">MATCH</th>{/if}
      </tr>
    </thead>
    <tbody>
      {#each rows as row (row.node.peerId + (row.node.isSelf ? ':self' : ''))}
        {@const n = row.node}
        <tr
          class="row"
          style="border-color:{theme.divider};"
          onclick={() => onOpen(n)}
          onkeydown={(e) => (e.key === 'Enter' || e.key === ' ') && (e.preventDefault(), onOpen(n))}
          tabindex="0"
        >
          <td>
            <!-- NAME first, always a primary line: an account with no name
                 reads "unknown" rather than promoting an identifier to look
                 like one (§16.4.3). The id lives underneath. -->
            <span
              class="dn"
              class:unnamed={isUnnamed(n.account ?? accountFromNode(n))}
              style="color:{isUnnamed(n.account ?? accountFromNode(n)) ? theme.textMuted : theme.textBright};"
            >{accountDisplayName(n.account ?? accountFromNode(n))}</span>
            {#if (n.account?.kind ?? 'peer') !== 'peer'}
              <span class="kind" style="color:{theme.cyan};border-color:{theme.cyan};">{kindLabel(n.account.kind)}</span>
            {/if}
            <!-- The node you are looking at is a row like any other (owner,
                 2026-07-28) — the badge is what says which one it is, and it
                 reads exactly as the nav item does: THIS NODE. -->
            {#if n.isSelf}<span class="tag" style="color:{theme.cyan};border-color:{theme.cyan};">THIS NODE</span>{/if}
            <div class="pid" style="color:{theme.textFaint};">{shortId(n.peerId)}</div>
          </td>
          <td class="wide" style="color:{theme.textBody};">{n.org?.trim() || '—'}</td>
          <td><span class="trust" style="color:{trustColor(n.trustLevel)};border-color:{trustColor(n.trustLevel)};">{normalizeTrust(n.trustLevel).toUpperCase()}</span></td>
          <td>
            <span class="status" style="color:{n.online ? theme.green : theme.textMuted};">
              <i style="background:{n.online ? theme.green : theme.textMuted};"></i>{n.online ? 'ONLINE' : 'OFFLINE'}
            </span>
          </td>
          <td class="wide" style="color:{theme.textBody};">{n.geoLabel || '—'}</td>
          <td class="mono nowrap wide" style="color:{theme.textDim};">{n.agent || '—'}</td>
          <td class="mono" style="color:{theme.textBody};">{n.isSelf ? `UP ${formatUptime(n.uptimeS)}` : formatLastSeen(n.lastSeen, now)}</td>
          {#if semanticActive}
            <td class="num mono" style="color:{row.score !== undefined && row.score >= 0 ? theme.ice : theme.textFaint};">
              {row.score !== undefined && row.score >= 0 ? `${Math.round(row.score * 100)}%` : '—'}
            </td>
          {/if}
        </tr>
      {:else}
        <tr><td colspan={semanticActive ? 8 : 7} class="empty" style="color:{theme.textDim};">NO NODES MATCH THE CURRENT FILTERS</td></tr>
      {/each}
    </tbody>
  </table>
</div>

<style>
  .scroller {
    overflow: auto;
    min-height: 0;
    flex: 1;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 15.5px;
  }
  thead tr {
    border-bottom: 1px solid;
    position: sticky;
    top: 0;
    background: linear-gradient(178deg, #0b151c, #060d12);
    z-index: 1;
  }
  th {
    text-align: left;
    font-weight: 500;
    font-size: 12.5px;
    letter-spacing: 0.16em;
    padding: 9px 12px;
    cursor: pointer;
    user-select: none;
    white-space: nowrap;
  }
  th .dir { margin-left: 5px; font-size: 10.5px; }
  th.num, td.num { text-align: right; }
  tbody tr.row {
    border-bottom: 1px solid;
    cursor: pointer;
  }
  tbody tr.row:hover td, tbody tr.row:focus-visible td {
    background: rgba(53, 201, 216, 0.06);
  }
  tbody tr.row:focus-visible { outline: 1px solid rgba(53, 201, 216, 0.5); outline-offset: -1px; }
  td {
    padding: 9px 12px;
    vertical-align: top;
    overflow-wrap: anywhere;
  }
  .dn {
    font-family: 'Chakra Petch', sans-serif;
    font-weight: 600;
    font-size: 17px;
    letter-spacing: 0.04em;
  }
  .pid { font-size: 13px; margin-top: 2px; }
  .dn.unnamed { font-style: italic; }
  .kind {
    border: 1px solid;
    font-size: 10px;
    letter-spacing: 0.14em;
    padding: 1px 5px;
    margin-left: 7px;
    white-space: nowrap;
  }
  .tag {
    font-size: 10.5px;
    letter-spacing: 0.16em;
    border: 1px solid;
    padding: 1px 5px;
    margin-left: 7px;
    vertical-align: 2px;
  }
  .trust {
    font-size: 11.5px;
    letter-spacing: 0.14em;
    border: 1px solid;
    padding: 2px 7px;
    white-space: nowrap;
  }
  .status {
    display: inline-flex;
    align-items: center;
    white-space: nowrap;
    gap: 6px;
    font-size: 13.5px;
    letter-spacing: 0.12em;
  }
  .status i { width: 6px; height: 6px; border-radius: 50%; display: inline-block; }
  .empty {
    text-align: center;
    padding: 30px 12px;
    font-size: 14.5px;
    letter-spacing: 0.1em;
  }
  .mono { font-family: 'IBM Plex Mono', ui-monospace, monospace; font-size: 14.5px; }
  .nowrap { white-space: nowrap; }
  /* Narrow screens: drop the wide columns so NODE/TRUST/STATUS/LAST SEEN
     fit without a nested horizontal scroller (tap targets stay whole). */
  @media (max-width: 760px) {
    th.wide, td.wide { display: none; }
    th, td { padding: 9px 7px; }
    .dn { font-size: 14.5px; }
    .pid { font-size: 10.5px; }
    .trust { padding: 2px 5px; font-size: 10px; letter-spacing: 0.08em; }
    .status { font-size: 11.5px; letter-spacing: 0.06em; }
    .mono { font-size: 12px; }
  }
</style>

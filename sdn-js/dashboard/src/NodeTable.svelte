<script>
  /**
   * Sortable node table (replaces the card grid — owner directive in
   * graph/tasks/nst-dashboard-table.md). Row click opens the detail modal.
   * Styled only with theme.js tokens; column sorters live in filters.js.
   */
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import { normalizeTrust, TRUST_COLOR_TOKEN } from './trust.js';
  import { shortId } from './format.js';
  import { accountDisplayName, accountFromNode, isUnnamed, kindLabel } from './accounts.js';
  import { peerSource, lastSeenLabel } from './peers.js';

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

  // THE COLUMN PRIORITY LADDER (grammar L4: "Horizontal overflow is a RESPONSIVE
  // failure: columns drop by declared priority at breakpoints").
  //
  // `drop` is the viewport width at which a column LEAVES, and it is the class
  // name too, so the breakpoint is legible in the markup and in the stylesheet
  // rather than being a number you have to go and look up. It replaces a single
  // `wide: true` flag whose one breakpoint (760px) was measured to be wrong:
  // the table's own minimum is 1054px with the wide columns in, so between 760
  // and 1180 it overflowed its panel and was CLIPPED by `main`'s
  // `overflow-x: hidden` — measured +136px at 900, +12px at 1024.
  //
  // Priorities, in the order a column earns its place:
  //   NAME, SOURCE  never drop. Who it is, and why it is listed.
  //   1180  ORGANIZATION, GEO, AGENT — the three that describe a peer rather
  //         than account for it. Reuses the threshold App.svelte's header
  //         cluster already collapses at; no new number is minted. Measured:
  //         ORGANIZATION's floor is its own nowrap header label (143px) even
  //         when every row holds an em-dash, and handing that 143px to SOURCE
  //         at 900px takes the config path from nine wrapped lines to four.
  //   560   TRUST, STATUS, LAST SEEN. Liveness is NOT lost with STATUS: SOURCE
  //         already says CONNECTED NOW for a live peer, and the dormant amber
  //         rail marks a pinned peer that is not. TRUST is a toolbar filter and
  //         all three are one tap away in the row's detail modal.
  //
  // SOURCE IS NOT DROPPABLE. It is the column that answers the owner's question —
  // "I have no idea what these peers are that are in the table" — and a column
  // that answers the question the page exists to answer does not get dropped
  // first. AGENT and GEO were already the droppable ones.
  const COLS = [
    { key: 'node', label: 'NAME' },
    { key: 'org', label: 'ORGANIZATION', drop: 'd1180' },
    { key: 'source', label: 'SOURCE' },
    { key: 'trust', label: 'TRUST', drop: 'd560' },
    { key: 'status', label: 'STATUS', drop: 'd560' },
    { key: 'geo', label: 'GEO', drop: 'd1180' },
    { key: 'agent', label: 'AGENT', drop: 'd1180' },
    { key: 'seen', label: 'LAST SEEN', drop: 'd560' },
  ];

  const trustColor = (level) => theme[TRUST_COLOR_TOKEN[normalizeTrust(level)]] ?? theme.textMuted;
</script>

<div class="scroller">
  <table>
    <thead>
      <tr style="color:{theme.textMuted};border-color:{theme.panelBorder};">
        {#each COLS as col (col.key)}
          <!-- `class:` directives, not `class={col.drop}`: Svelte's CSS pruner
               tracks the former and would prune a breakpoint class it can only
               see inside an expression. -->
          <th
            onclick={() => onSort(col.key)}
            class:active={sortKey === col.key}
            class:d1180={col.drop === 'd1180'}
            class:d560={col.drop === 'd560'}
            style="color:{sortKey === col.key ? theme.ice : theme.textMuted};"
          >
            {col.label}{#if sortKey === col.key}<span class="dir">{sortDir === 1 ? '▲' : '▼'}</span>{/if}
          </th>
        {/each}
        {#if semanticActive}<th class="num" style="color:{theme.textMuted};">MATCH</th>{/if}
      </tr>
    </thead>
    <tbody>
      {#each rows as row (row.node.peerId + (row.node.isSelf ? ':self' : ''))}
        {@const n = row.node}
        {@const src = peerSource(n)}
        {@const dormant = !n.isSelf && !n.online && src.pinned}
        <tr
          class="row"
          class:dormant
          style="border-color:{theme.divider};"
          onclick={() => onOpen(n)}
          onkeydown={(e) => (e.key === 'Enter' || e.key === ' ') && (e.preventDefault(), onOpen(n))}
          tabindex="0"
        >
          <!-- The dormant rule (owner: "Pinned-not-seen rows must be visibly
               distinct from live ones"): a pinned peer with no live connection
               carries the amber the map legend already spends on OFFLINE ·
               TRUSTED, so the two surfaces agree on what amber means. -->
          <td style={dormant ? `box-shadow:inset 3px 0 0 ${theme.amber};` : ''}>
            <!-- NAME first, always a primary line: an account with no name
                 reads "unknown" rather than promoting an identifier to look
                 like one (§16.4.3). The id lives underneath.

                 THIS IS WHERE THE FAKE NAME WENT. The live feed's one named row
                 read 'Config Trusted Peer' — a hardcoded placeholder describing
                 the row's PROVENANCE, not a name (owner: "what does the first
                 row 'config trusted peer' mean?"). It is removed at the source;
                 provenance has its own column now, and a peer whose EPM supplies
                 no name reads "unknown" over its short peer id, which is an
                 honest identity rather than a manufactured one. -->
            <span
              class="dn"
              class:unnamed={isUnnamed(n.account ?? accountFromNode(n))}
              style="color:{isUnnamed(n.account ?? accountFromNode(n)) ? theme.textMuted : theme.textBright};"
            >{accountDisplayName(n.account ?? accountFromNode(n))}</span>
            {#if (n.account?.kind ?? 'peer') !== 'peer'}
              <span class="kind" style="color:{theme.cyan};border-color:{theme.cyan};">{kindLabel(n.account.kind)}</span>
            {/if}
            <!-- Defensive only: the ACCOUNTS listing never contains this node
                 (accounts.js withoutSelf, owner rule). The table is reused by
                 no other surface today, and the tag stays so a future caller
                 passing a self row cannot render it unlabelled. -->
            {#if n.isSelf}<span class="tag" style="color:{theme.cyan};border-color:{theme.cyan};">SELF</span>{/if}
            <div class="pid" style="color:{theme.textFaint};">{shortId(n.peerId)}</div>
          </td>
          <td class="d1180" style="color:{theme.textBody};">{n.org?.trim() || '—'}</td>
          <!-- SOURCE — the answer to "how did this get here?", on the row, in
               words, without devtools. `src.note` is rendered as VISIBLE TEXT
               (keystate.js's standing rule: never a tooltip only): for a config
               pin it is the file and key an operator actually edits. -->
          <td>
            <span class="src" style="color:{theme[src.tone] ?? theme.textDim};border-color:{theme[src.tone] ?? theme.textDim};" title={src.sentence}>{src.label}</span>
            {#if src.id === 'connected' && src.pinned}
              <span class="tag" style="color:{theme.ice};border-color:{theme.ice};">PINNED</span>
            {/if}
            {#if src.note}
              <div class="note" style="color:{theme.textFaint};">{src.note}</div>
            {/if}
          </td>
          <td class="d560"><span class="trust" style="color:{trustColor(n.trustLevel)};border-color:{trustColor(n.trustLevel)};">{normalizeTrust(n.trustLevel).toUpperCase()}</span></td>
          <td class="d560">
            <span class="status" style="color:{n.online ? theme.green : theme.textMuted};">
              <i style="background:{n.online ? theme.green : theme.textMuted};"></i>{n.online ? 'ONLINE' : 'OFFLINE'}
            </span>
          </td>
          <td class="d1180" style="color:{theme.textBody};">{n.geoLabel || '—'}</td>
          <td class="mono nowrap d1180" style="color:{theme.textDim};">{n.agent || '—'}</td>
          <!-- Never the bare word "never" again (owner directive 2026-07-30).
               A pinned peer this node has not dialled yet says exactly that. -->
          <td class="mono d560" style="color:{n.lastSeen ? theme.textBody : theme.textMuted};">{lastSeenLabel(n, now)}</td>
          {#if semanticActive}
            <td class="num mono" style="color:{row.score !== undefined && row.score >= 0 ? theme.ice : theme.textFaint};">
              {row.score !== undefined && row.score >= 0 ? `${Math.round(row.score * 100)}%` : '—'}
            </td>
          {/if}
        </tr>
      {:else}
        <tr><td colspan={semanticActive ? 9 : 8} class="empty" style="color:{theme.textDim};">NO NODES MATCH THE CURRENT FILTERS</td></tr>
      {/each}
    </tbody>
  </table>
</div>

<style>
  /* GRAMMAR L1 (iris-dashboard-grammar-law): this element used to declare
     `overflow:auto; scrollbar-gutter:stable`, and it is the second scrollbar in
     the owner's screenshot — a scroller inside the page's own scroller, with its
     own reserved track, which is also why this panel's right edge disagreed with
     the panels around it. The table takes its natural height and the PAGE
     scrolls; rows beyond the page size are reached through the pager. */
  .scroller {
    min-height: 0;
    flex: 1;
  }
  /* DENSITY (owner 2026-07-30 "reduce the font size in the tables by 30%", IRIS
     R7's rungs — not a multiplier): the cell base drops value (20) -> label (15),
     the header label (15) -> micro (13), and the metaline stays on micro. */
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: var(--sdn-fs-label); line-height: var(--sdn-lh-label);
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
    font-size: var(--sdn-fs-micro); line-height: var(--sdn-lh-micro);
    letter-spacing: 0.16em;
    padding: var(--sdn-sp-2) var(--sdn-sp-5);
    cursor: pointer;
    user-select: none;
    white-space: nowrap;
  }
  th .dir { margin-left: 5px; font-size: var(--sdn-fs-micro); line-height: var(--sdn-lh-micro); }
  th.num, td.num { text-align: right; }
  tbody tr.row {
    border-bottom: 1px solid;
    cursor: pointer;
  }
  tbody tr.row:hover td, tbody tr.row:focus-visible td {
    background: rgba(53, 201, 216, 0.06);
  }
  tbody tr.row:focus-visible { outline: 1px solid rgba(53, 201, 216, 0.5); outline-offset: -1px; }
  /* `break-word`, NOT `anywhere` (defect, owner's 1440px screenshot 2026-07-30:
     the NAME column rendered the fallback identity as `unkno` / `wn` on two
     lines, with the short peer id broken under it).
     The two differ in ONE way that decides this table: `anywhere` lets a cell's
     MINIMUM content width collapse to a single character, so an auto-layout
     table is free to squeeze any column to nothing — and it did, to 80px,
     because SOURCE's config path (a 137-character absolute path on the live
     node) bid for 400px of the 1311 available. `break-word` wraps at word
     boundaries and only splits a word that genuinely cannot fit its line, so
     every column keeps a floor made of its own longest unbreakable token.
     `.note` opts back into `anywhere` below — a filesystem path is the one
     value here that must be allowed to collapse. */
  td {
    padding: var(--sdn-sp-2) var(--sdn-sp-5);
    vertical-align: top;
    overflow-wrap: break-word;
  }
  /* The OTHER unbounded value in the table (an EPM display name is whatever an
     operator typed), capped on the same principle as `.note`: no value of
     unbounded length may bid for unbounded width. Measured without the cap, a
     41-character single-token name took 422px of NAME and pushed the table 39px
     past its panel at 1440 and 154px past it at 390 — `break-word` does not
     shrink a cell's MINIMUM, it only breaks a word once the line already
     exists, so one unspaced name was enough to overflow the whole table.
     `anywhere` + `max-width` is the pair that fixes it: the name may collapse,
     and `.pid`'s nowrap floor underneath is what still keeps `unknown` on one
     line — that floor is now the id's width, never one character. */
  .dn {
    display: inline-block;
    max-width: 30ch;
    overflow-wrap: anywhere;
    font-family: 'Chakra Petch', sans-serif;
    font-weight: 600;
    font-size: var(--sdn-fs-note); line-height: var(--sdn-lh-note);
    letter-spacing: 0.04em;
  }
  /* A peer id is ONE token: `12D3Ko…7bZBWs` split across two lines is not an
     identifier any more, it is two fragments. This is also the NAME column's
     floor — its min-content becomes the id's width (~111-125px measured), which
     is what makes the column immune to whatever length the SOURCE path is. */
  .pid { font-size: var(--sdn-fs-micro); line-height: var(--sdn-lh-micro); margin-top: 2px; white-space: nowrap; }
  .dn.unnamed { font-style: italic; }
  .kind {
    border: 1px solid;
    font-size: var(--sdn-fs-micro); line-height: var(--sdn-lh-micro);
    letter-spacing: 0.14em;
    padding: 1px 5px;
    margin-left: 7px;
    white-space: nowrap;
  }
  .tag {
    font-size: var(--sdn-fs-micro); line-height: var(--sdn-lh-micro);
    letter-spacing: 0.16em;
    border: 1px solid;
    padding: 1px 5px;
    margin-left: 7px;
    vertical-align: 2px;
  }
  .trust {
    font-size: var(--sdn-fs-micro); line-height: var(--sdn-lh-micro);
    letter-spacing: 0.14em;
    border: 1px solid;
    padding: 2px 7px;
    white-space: nowrap;
  }
  /* SOURCE reads as a badge, like TRUST, because it is the same kind of fact
     about the row — not a value, a classification. It may wrap: "PINNED BY
     OPERATOR" is three words and truncating it would put the answer back
     behind a tooltip. */
  .src {
    display: inline-block;
    font-size: var(--sdn-fs-micro); line-height: var(--sdn-lh-micro);
    letter-spacing: 0.14em;
    border: 1px solid;
    padding: 2px 7px;
  }
  /* The config pin's file+key. Small, dim, and WHOLE — an operator has to be
     able to read the path they are being sent to.
     THE COLUMN BUDGET LIVES HERE. This is the only value in the table with no
     upper bound on its length, and an auto-layout table hands free space out in
     proportion to what each column ASKS for, so an unbounded ask is an
     unbounded column: uncapped, a 137-char dev path took 400px and a 59-char
     prod path took 342px, both at the expense of NAME. `max-width` caps what
     the cell may ask for (a block child's max-width bounds its parent cell's
     max-content), which is the knob — the column it actually receives is
     smaller. Measured at 40ch: SOURCE lands at 249px of 1311 at 1440w (19%) and
     is IDENTICAL for the 59-char and 137-char paths, i.e. the layout no longer
     depends on how long an operator's config path happens to be.
     `overflow-wrap: anywhere` (against the `break-word` td default) is what
     lets it collapse on a phone; the breaks still land on `/` and `-` first, so
     the path reads in segments rather than being sliced mid-word. */
  .note {
    font-size: var(--sdn-fs-micro); line-height: var(--sdn-lh-micro);
    letter-spacing: 0.02em;
    margin-top: 3px;
    overflow-wrap: anywhere;
    max-width: 40ch;
  }
  /* theme.amber (#ffb24d) at low alpha — the same token the inline accent and
     the map legend use, spelled as a tint the way this file already spells
     theme.cyan's hover tint. */
  tbody tr.dormant td { background: rgba(255, 178, 77, 0.05); }
  .status {
    display: inline-flex;
    align-items: center;
    white-space: nowrap;
    gap: 6px;
    font-size: var(--sdn-fs-label); line-height: var(--sdn-lh-label);
    letter-spacing: 0.12em;
  }
  .status i { width: 6px; height: 6px; border-radius: 50%; display: inline-block; }
  .empty {
    text-align: center;
    padding: 30px 12px;
    font-size: var(--sdn-fs-label); line-height: var(--sdn-lh-label);
    letter-spacing: 0.1em;
  }
  .mono { font-family: 'IBM Plex Mono', ui-monospace, monospace; font-size: var(--sdn-fs-label); line-height: var(--sdn-lh-label); }
  .nowrap { white-space: nowrap; }
  /* THE DROP LADDER (L4). Each class is named for the width at which its column
     leaves; the COLS table above assigns them and states the priority argument.
     Measured table width vs. available width, prod-length config path, after:
       1920 1789/1791 · 1440 1309/1311 · 1280 1149/1151 · 1024 893/895
        900  769/771  ·  760  653/655  ·  560  453/455  ·  414  307/309
        390  283/285  ·  320  213/215
     — no width overflows its panel, where BEFORE this ladder 900px overflowed
     by 136px and 390px by 128px, silently clipped by `main`'s overflow-x. */
  @media (max-width: 1180px) {
    /* ORGANIZATION, GEO and AGENT go first: they describe a peer, they do not
       account for it, and AGENT alone is a 222px nowrap column. 1180 is
       App.svelte's existing header-collapse threshold, not a new number. */
    th.d1180, td.d1180 { display: none; }
  }
  /* No column leaves at 760 — this tier is the type/padding tightening the
     remaining five need to keep their labels whole. */
  @media (max-width: 760px) {
    th, td { padding: 9px 7px; }
    .dn { font-size: var(--sdn-fs-fine); line-height: var(--sdn-lh-fine); }
    .pid { font-size: var(--sdn-fs-micro); line-height: var(--sdn-lh-micro); }
    .trust { padding: 2px 5px; font-size: var(--sdn-fs-micro); line-height: var(--sdn-lh-micro); letter-spacing: 0.08em; }
    /* Same tightening TRUST already had, for the same reason: the badge's
       longest word is what floors the SOURCE column, and at phone widths that
       floor is the difference between the label reading `PINNED BY OPERATOR`
       and reading `PINN` / `ED` / `BY` / `OPERAT` / `OR`. */
    .src { padding: 2px 5px; letter-spacing: 0.08em; }
    .status { font-size: var(--sdn-fs-micro); line-height: var(--sdn-lh-micro); letter-spacing: 0.06em; }
    .mono { font-size: var(--sdn-fs-label); line-height: var(--sdn-lh-label); }
  }
  @media (max-width: 560px) {
    /* Phones. NAME + SOURCE only — 285px of table at 390px of viewport does not
       hold five columns, and the alternative measured out as a SOURCE column of
       71px rendering `CONN`/`ECTE`/`D`/`NOW` and a 14-line path. Liveness is not
       lost: SOURCE says CONNECTED NOW for a live peer and the dormant amber rail
       marks a pinned one that is not. TRUST, STATUS and LAST SEEN are in the
       row's detail modal, one tap away. */
    th.d560, td.d560 { display: none; }
    th, td { padding: 9px 5px; }
  }
</style>

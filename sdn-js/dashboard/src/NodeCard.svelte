<script>
  /**
   * One card per NodeStatusSet entry. Rendered ONLY with design theme.js tokens
   * + the design StatusChip; no invented palette.
   * @typedef {import('../../src/status/view-model').NodeStatusView} NodeStatusView
   * @type {{ node: NodeStatusView, now: number }}
   */
  import StatusChip from 'spaceaware-student-sdn/src/lib/components/StatusChip.svelte';
  import Panel from 'spaceaware-student-sdn/src/lib/components/Panel.svelte';
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import { shortId, formatUptime, formatLastSeen, formatCoords } from './format.js';

  let { node, now } = $props();

  let expanded = $state(false);

  const title = $derived(node.dn?.trim() || node.org?.trim() || shortId(node.peerId));
  const hasOrg = $derived(Boolean(node.org?.trim()) && node.org?.trim() !== title);
  const coords = $derived(formatCoords(node.lat, node.lon));
  const addrs = $derived(node.addrs ?? []);
  const shownAddrs = $derived(expanded ? addrs : addrs.slice(0, 1));
</script>

<Panel variant="raised" pad="0">
  <div class="card">
    <div class="head">
      <div class="titles">
        <div class="dn" style="color:{theme.textBright};" title={node.dn || node.peerId}>{title}</div>
        {#if hasOrg}<div class="org" style="color:{theme.textDim};">{node.org}</div>{/if}
      </div>
      <div class="chips">
        {#if node.isSelf}<StatusChip label="SELF" color={theme.cyan} dot={false} />{/if}
        <StatusChip
          label={node.online ? 'ONLINE' : 'OFFLINE'}
          color={node.online ? theme.green : theme.textMuted}
        />
      </div>
    </div>

    <div class="pid" style="color:{theme.textMuted};border-color:{theme.divider};">
      <span class="k">PEER</span>
      <span class="mono" style="color:{theme.ice};" title={node.peerId}>{shortId(node.peerId)}</span>
      {#if node.role}<span class="tag" style="color:{theme.textFaint};">{node.role.toUpperCase()}</span>{/if}
      {#if node.trustLevel}<span class="tag" style="color:{theme.textFaint};">{node.trustLevel.toUpperCase()}</span>{/if}
    </div>

    <dl class="rows">
      {#if node.geoLabel || coords}
        <div class="row"><dt style="color:{theme.textMuted};">GEO</dt>
          <dd style="color:{theme.textBody};">{node.geoLabel || '—'}{#if coords}<span class="coords" style="color:{theme.textFaint};"> · {coords}</span>{/if}</dd>
        </div>
      {/if}
      <div class="row"><dt style="color:{theme.textMuted};">AGENT</dt>
        <dd class="mono" style="color:{theme.textBody};">{node.agent || '—'}</dd>
      </div>
      {#if node.standardsVersion || node.suiteVersion}
        <div class="row"><dt style="color:{theme.textMuted};">SDS · SUITE</dt>
          <dd class="mono" style="color:{theme.textDim};">{node.standardsVersion || '—'} · {node.suiteVersion || '—'}</dd>
        </div>
      {/if}
      {#if node.isSelf}
        <div class="row"><dt style="color:{theme.textMuted};">UPTIME</dt>
          <dd class="mono" style="color:{theme.textBody};">{formatUptime(node.uptimeS)}</dd>
        </div>
      {:else}
        <div class="row"><dt style="color:{theme.textMuted};">LAST SEEN</dt>
          <dd class="mono" style="color:{theme.textBody};">{formatLastSeen(node.lastSeen, now)}</dd>
        </div>
      {/if}
    </dl>

    {#if addrs.length}
      <div class="addrs" style="border-color:{theme.divider};">
        <div class="addr-head">
          <span class="k" style="color:{theme.textMuted};">ADDRS ({addrs.length})</span>
          {#if addrs.length > 1}
            <button
              class="toggle"
              style="color:{theme.ice};border-color:{theme.hairline};"
              onclick={() => (expanded = !expanded)}
            >{expanded ? 'COLLAPSE' : `+${addrs.length - 1} MORE`}</button>
          {/if}
        </div>
        <ul>
          {#each shownAddrs as addr (addr)}
            <li class="mono" style="color:{theme.textDim};" title={addr}>{addr}</li>
          {/each}
        </ul>
      </div>
    {/if}
  </div>
</Panel>

<style>
  .card { display: flex; flex-direction: column; }
  .head {
    display: flex; align-items: flex-start; justify-content: space-between; gap: 12px;
    padding: 14px 15px 11px;
  }
  .titles { min-width: 0; }
  .dn {
    font-family: 'Chakra Petch', sans-serif; font-weight: 600; font-size: 15.5px;
    letter-spacing: 0.04em; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  }
  .org { font-size: 11.5px; letter-spacing: 0.04em; margin-top: 3px;
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .chips { display: flex; gap: 6px; flex: none; flex-wrap: wrap; justify-content: flex-end; }
  .pid {
    display: flex; align-items: center; gap: 9px; flex-wrap: wrap;
    padding: 9px 15px; border-top: 1px solid; font-size: 11.5px; letter-spacing: 0.06em;
  }
  .pid .k { font-size: 9.5px; letter-spacing: 0.18em; }
  .tag { font-size: 9px; letter-spacing: 0.16em; }
  .rows { margin: 0; padding: 11px 15px 4px; }
  .row { display: flex; gap: 12px; padding: 3px 0; align-items: baseline; }
  dt { flex: none; width: 92px; font-size: 9.5px; letter-spacing: 0.16em; }
  dd { margin: 0; font-size: 12px; min-width: 0; overflow-wrap: anywhere; }
  .coords { font-size: 11px; }
  .addrs { padding: 10px 15px 13px; border-top: 1px solid; margin-top: 6px; }
  .addr-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 6px; }
  .addr-head .k { font-size: 9.5px; letter-spacing: 0.18em; }
  .toggle {
    background: transparent; border: 1px solid; cursor: pointer;
    font-family: 'IBM Plex Mono', ui-monospace, monospace; font-size: 9.5px;
    letter-spacing: 0.12em; padding: 3px 8px;
  }
  ul { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 4px; }
  li { font-size: 11px; overflow-wrap: anywhere; line-height: 1.45; }
  .mono { font-family: 'IBM Plex Mono', ui-monospace, monospace; }
</style>

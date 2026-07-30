<script>
  /**
   * PEER SUMMARY.
   *
   * DESIGN SOURCE (declared, not copied):
   *   SpaceAware-UI @ archive/SpaceAware.io 2/SDN Console.dc.html
   *   sha256 abacdbfc62aeaee1193eccec9087669bfeb2324422fe8223482556fad207f152
   *   widget :207-219 · registry entry :869
   *
   * The design lists three peers with a name, a right-aligned detail and a trust
   * label. Its detail column is a "feeds" count — a provider-catalogue concept
   * this node does not publish — so the honest equivalent is the peer's own
   * liveness: ONLINE, or when it was last seen. Ordered most-trusted first, then
   * online, so the rows that matter are the ones on screen.
   */
  import StatusChip from 'spaceaware-student-sdn/src/lib/components/StatusChip.svelte';
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import { normalizeTrust, trustRank, TRUST_COLOR_TOKEN } from '../../trust.js';
  import { shortId, formatLastSeen } from '../../format.js';

  let { nodes = [], now = 0, onSelectNode = () => {} } = $props();

  const peers = $derived((nodes ?? []).filter((n) => !n.isSelf));
  const onlinePeers = $derived(peers.filter((n) => n.online).length);
  const peerRows = $derived(
    [...peers]
      .sort(
        (a, b) =>
          trustRank(b.trustLevel) - trustRank(a.trustLevel) ||
          Number(b.online) - Number(a.online) ||
          (b.lastSeen ?? 0) - (a.lastSeen ?? 0)
      )
      .slice(0, 4)
      .map((n) => ({
        peerId: n.peerId,
        name: n.dn?.trim() || n.org?.trim() || shortId(n.peerId),
        tier: normalizeTrust(n.trustLevel),
        color: theme[TRUST_COLOR_TOKEN[normalizeTrust(n.trustLevel)]] ?? theme.textMuted,
        detail: n.online ? 'ONLINE' : formatLastSeen(n.lastSeen, now || Date.now()),
        node: n,
      }))
  );
</script>

<div class="whead">
  <span class="wkick" style="color:{theme.textMuted};">PEER SUMMARY</span>
  <span class="hchips">
    <StatusChip label={`${onlinePeers}/${peers.length} ONLINE`} color={theme.green} dot={false} />
  </span>
</div>
{#if peerRows.length}
  <div class="cells fill">
    {#each peerRows as row (row.peerId)}
      <button class="prow" onclick={() => onSelectNode(row.node)} title="Open this peer's details">
        <span class="pdot" style="background:{row.color};box-shadow:0 0 6px {row.color};"></span>
        <span class="pname" style="color:{theme.textBright};">{row.name}</span>
        <span class="pdetail" style="color:{theme.textDim};">{row.detail}</span>
        <span class="ptier" style="color:{row.color};">{row.tier.toUpperCase()}</span>
      </button>
    {/each}
  </div>
{:else}
  <div class="empty" style="color:{theme.textFaint};">No peers known to this node yet.</div>
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

  .cells { display: flex; flex-direction: column; gap: var(--sdn-sp-4); min-width: 0; }
  .cells.fill { flex: 1; justify-content: space-between; }

  .empty {
    font-size: var(--sdn-fs-body);
    line-height: var(--sdn-lh-body);
    letter-spacing: 0.02em;
    margin-top: var(--sdn-sp-4);
  }

  /* PEER SUMMARY rows: a button, because clicking one opens that peer. */
  .prow {
    display: flex;
    align-items: center;
    gap: var(--sdn-sp-3);
    background: transparent;
    border: 0;
    padding: 0;
    text-align: left;
    cursor: pointer;
    font: inherit;
    min-width: 0;
  }
  .prow:hover .pname { filter: brightness(1.2); }
  .prow:focus-visible { outline: 1px solid currentColor; outline-offset: 2px; }
  .pdot { width: 7px; height: 7px; border-radius: 50%; flex: none; }
  /* The design's PEER SUMMARY row is ONE line: name, right-aligned detail, trust
     (`SDN Console.dc.html:210-217`). This max-width is TEXT truncation inside that
     row, not a panel dimension — it caps the name so a long operator name cannot
     push the trust label out of the widget (L5 asks for the reason to be stated
     here, and this is it). */
  .pname {
    font-family: 'Chakra Petch', sans-serif;
    font-weight: 600;
    font-size: var(--sdn-fs-note);
    line-height: var(--sdn-lh-note);
    flex: none;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    /* TEXT truncation inside the row, not a panel dimension (L5). */
    max-width: 55%;
  }
  .pdetail {
    font-size: var(--sdn-fs-micro);
    line-height: var(--sdn-lh-micro);
    flex: 1;
    text-align: right;
    white-space: nowrap;
  }
  .ptier {
    font-size: var(--sdn-fs-micro);
    line-height: var(--sdn-lh-micro);
    letter-spacing: 0.04em;
    flex: none;
    text-align: right;
    min-width: 68px;
  }
</style>

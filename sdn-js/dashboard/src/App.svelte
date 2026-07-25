<script>
  /**
   * SDN Node Status $APP view.
   *
   * Composes the DESIGN repo's shell (SdnRail + ConsoleHeader) and primitives
   * (StatusChip) around a single "NETWORK NODES" route: one card per entry in
   * the live NodeStatusSet. Data comes EXCLUSIVELY from globalThis.SDN_NODE_STATUS
   * (published by the sdn-js status runtime); this view never fetches anything.
   *
   * PROVISIONAL (Iris ruling): the next design-tool zip supersedes this view.
   * The data seam (SDN_NODE_STATUS) survives. Styled ONLY with theme.js tokens
   * and the design components — no invented palette.
   */
  import { onMount } from 'svelte';
  import SdnRail from 'spaceaware-student-sdn/src/lib/shell/SdnRail.svelte';
  import ConsoleHeader from 'spaceaware-student-sdn/src/lib/shell/ConsoleHeader.svelte';
  import StatusChip from 'spaceaware-student-sdn/src/lib/components/StatusChip.svelte';
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import NodeCard from './NodeCard.svelte';
  import { shortId } from './format.js';

  const SECTIONS = [
    { label: 'NETWORK', items: [{ id: 'nodes', label: 'NODES', glyph: '◍', fkey: 'N1' }] }
  ];

  /** @type {import('../../src/status/view-model').NodeStatusSetView | null} */
  let view = $state(null);
  let now = $state(Date.now());

  const nodes = $derived(view?.nodes ?? []);
  const onlinePeers = $derived(nodes.filter((n) => !n.isSelf && n.online).length);
  const totalPeers = $derived(nodes.filter((n) => !n.isSelf).length);
  const connected = $derived(view != null);
  const updatedAgo = $derived(
    view ? Math.max(0, Math.round((now - view.generatedAt) / 1000)) : null
  );

  onMount(() => {
    let unsub = () => {};
    let cancelled = false;
    // main.js starts the runtime before mount; poll briefly in case of races.
    const attach = () => {
      const g = globalThis.SDN_NODE_STATUS;
      if (!g) {
        if (!cancelled) setTimeout(attach, 100);
        return;
      }
      const seed = g.current?.();
      if (seed) view = seed;
      unsub = g.subscribe((v) => (view = v));
    };
    attach();
    const clock = setInterval(() => (now = Date.now()), 1000);
    return () => {
      cancelled = true;
      clock && clearInterval(clock);
      unsub();
    };
  });
</script>

<div class="root" style="background:{theme.pageGlow};color:{theme.textBody};">
  <SdnRail sections={SECTIONS} active="nodes" onSelect={() => {}} />
  <main>
    <ConsoleHeader title="NETWORK NODES" sub="· LIVE NODE STATUS" accent={theme.cyan}>
      {#snippet right()}
        {#if connected}
          <StatusChip label="FEED LIVE" color={theme.green} />
          <StatusChip label={`${onlinePeers}/${totalPeers} PEERS ONLINE`} color={theme.ice} dot={false} />
        {:else}
          <StatusChip label="CONNECTING" color={theme.amber} />
        {/if}
      {/snippet}
    </ConsoleHeader>

    <div class="body">
      {#if !connected}
        <div class="empty" style="color:{theme.textDim};border-color:{theme.hairline};">
          <span class="glyph" style="color:{theme.cyan};">◍</span>
          Connecting to the node status feed (/ws/status)…
        </div>
      {:else}
        <div class="meta" style="color:{theme.textMuted};">
          <span>{nodes.length} NODE{nodes.length === 1 ? '' : 'S'}</span>
          <span class="dot">·</span>
          <span>SOURCE {shortId(view?.sourcePeerId ?? '')}</span>
          <span class="dot">·</span>
          <span>UPDATED {updatedAgo === 0 ? 'now' : `${updatedAgo}s ago`}</span>
        </div>
        <div class="grid">
          {#each nodes as node (node.peerId + (node.isSelf ? ':self' : ''))}
            <NodeCard {node} {now} />
          {/each}
        </div>
      {/if}
    </div>
  </main>
</div>

<style>
  .root {
    position: fixed;
    inset: 0;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    -webkit-font-smoothing: antialiased;
    overflow: hidden;
  }
  main {
    position: absolute;
    left: 66px;
    right: 0;
    top: 0;
    bottom: 0;
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }
  .body {
    flex: 1;
    min-height: 0;
    overflow: auto;
    padding: 18px 24px 40px;
  }
  .meta {
    display: flex;
    align-items: center;
    gap: 9px;
    font-size: 11px;
    letter-spacing: 0.14em;
    margin-bottom: 16px;
  }
  .meta .dot { opacity: 0.5; }
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(340px, 1fr));
    gap: 16px;
    align-items: start;
  }
  .empty {
    display: flex;
    align-items: center;
    gap: 12px;
    border: 1px solid;
    padding: 26px 28px;
    font-size: 12.5px;
    letter-spacing: 0.06em;
    max-width: 560px;
  }
  .empty .glyph { font-size: 16px; }
</style>

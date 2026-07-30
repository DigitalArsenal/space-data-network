<script>
  /**
   * PEER MAP · GEOIP — the swarm map, extracted so it exists exactly ONCE.
   *
   * DESIGN SOURCE (declared, not copied):
   *   SpaceAware-UI @ archive/SpaceAware.io 2/SDN Console.dc.html
   *   sha256 abacdbfc62aeaee1193eccec9087669bfeb2324422fe8223482556fad207f152
   *   netmap widget :168-206 · registry :866 · ROUTES :892
   *
   * WHY IT IS A COMPONENT. Two surfaces show this map: the dashboard's `netmap`
   * widget and the PEERS route (owner directive 2026-07-30, "move the peers table
   * with add peer form, along with the globe, to a whole new menu called Peers").
   * IRIS ruled it is the SAME widget, extracted, never forked — "one legend, one
   * 3D/2D switch, one vendored land.json" — and recorded the trap in
   * iris-dashboard-grammar-law: two mounted globes are two WebGL contexts on one
   * page. The routes are mutually exclusive, so only one instance is ever mounted.
   */
  import StatusChip from 'spaceaware-student-sdn/src/lib/components/StatusChip.svelte';
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import Globe from './Globe.svelte';

  /**
   * @type {{
   *   nodes: any[], selectedId?: string, onSelectNode?: (node: any) => void,
   *   interactive?: boolean, height?: string, sub?: string,
   * }}
   */
  let {
    nodes = [],
    selectedId = '',
    onSelectNode = () => {},
    interactive = true,
    /** The widget's row height; the PEERS route passes its own taller box. */
    height = '322px',
    sub = 'GEOIP · LIVE SWARM',
  } = $props();

  let mapMode = $state('3d');

  const peers = $derived((nodes ?? []).filter((n) => !n.isSelf));
  const links = $derived(peers.filter((n) => n.online).length);
  /** Only nodes with a real fix are drawn — 0,0 is "no location", not the Gulf of Guinea. */
  const placed = $derived((nodes ?? []).filter((n) => n.lat !== 0 || n.lon !== 0));
  const countries = $derived(
    new Set(
      placed
        .map((n) => (n.geoLabel || '').split(',').pop()?.trim())
        .filter((c) => Boolean(c))
    ).size
  );
</script>

<div class="whead">
  <span class="wkick" style="color:{theme.textMuted};">PEER MAP</span>
  <span class="wsub" style="color:{theme.textFaint};">{sub}</span>
  <span class="hchips">
    <StatusChip label={`${links} LINK${links === 1 ? '' : 'S'}`} color={theme.green} />
    <span class="tabs" style="border-color:{theme.panelBorder};">
      {#each [['3d', '3D'], ['2d', '2D']] as [id, label] (id)}
        <button
          class="tab"
          style="background:{mapMode === id ? 'rgba(53,201,216,0.18)' : 'transparent'};color:{mapMode === id ? theme.cyan : theme.textDim};border-color:{theme.panelBorder};"
          aria-pressed={mapMode === id}
          onclick={() => (mapMode = id)}
        >{label}</button>
      {/each}
    </span>
  </span>
</div>
<!-- The height arrives as a CUSTOM PROPERTY, not as an inline `height`: an inline
     declaration would beat the narrow-screen media query below on specificity,
     and the 3D globe needs a shorter box on a phone. -->
<div class="map" style="--peermap-h:{height};">
  <Globe nodes={placed} mode={mapMode} legend={false} {selectedId} onSelect={onSelectNode} {interactive} />
  <div class="mapmeta mono" style="color:{theme.textMuted};">
    <div>{peers.length} PEER{peers.length === 1 ? '' : 'S'}</div>
    <div>{countries} {countries === 1 ? 'COUNTRY' : 'COUNTRIES'}</div>
  </div>
  {#if mapMode === '3d' && interactive}
    <div class="maphint mono" style="color:{theme.textFaint};">DRAG TO ROTATE</div>
  {/if}
</div>
<div class="mapfoot">
  <span class="legend" style="color:{theme.textDim};">
    <span><i style="background:{theme.cyan};box-shadow:0 0 6px {theme.cyan};"></i>THIS NODE</span>
    <span><i style="background:{theme.green};box-shadow:0 0 6px {theme.green};"></i>ONLINE</span>
    <span><i style="background:{theme.amber};box-shadow:0 0 6px {theme.amber};"></i>OFFLINE · TRUSTED</span>
    <span><i style="background:{theme.textMuted};"></i>OFFLINE</span>
  </span>
  <!-- The design's "Locations · MaxMind GeoLite2" attribution stays ABSENT: it
       is a claim about a resolver, true only of a node whose resolver really is
       GeoLite2 (IRIS constraint (b), wave 1). -->
</div>

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
  .wsub {
    font-size: var(--sdn-fs-micro);
    line-height: var(--sdn-lh-micro);
    letter-spacing: 0.14em;
  }
  .hchips { display: inline-flex; align-items: center; gap: var(--sdn-sp-2); margin-left: auto; flex-wrap: wrap; }

  .tabs { display: inline-flex; border: 1px solid; }
  .tab {
    border: 0;
    cursor: pointer;
    font-family: 'Chakra Petch', sans-serif;
    font-weight: 600;
    font-size: var(--sdn-fs-label);
    line-height: var(--sdn-lh-label);
    letter-spacing: 0.08em;
    padding: 4px 11px;
  }
  .tab + .tab { border-left: 1px solid; }

  /* The map is aspect-free but height-bound, and the height comes from the
     caller — L5: no width, no max-width, no margin-inline here, so a panel's
     edges stay the column's edges. The -4px insets are the design's own bleed of
     the canvas into the panel padding. */
  .map { position: relative; height: var(--peermap-h, 322px); margin: var(--sdn-sp-3) -4px 0; min-width: 0; }
  .mapmeta {
    position: absolute;
    left: 8px;
    top: 6px;
    pointer-events: none;
    font-size: var(--sdn-fs-micro);
    line-height: var(--sdn-lh-micro);
    letter-spacing: 0.12em;
  }
  .maphint {
    position: absolute;
    right: 8px;
    bottom: 6px;
    pointer-events: none;
    font-size: var(--sdn-fs-micro);
    line-height: var(--sdn-lh-micro);
    letter-spacing: 0.1em;
  }
  .mapfoot {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--sdn-sp-4);
    margin-top: var(--sdn-sp-5);
    flex-wrap: wrap;
  }
  .legend { display: inline-flex; gap: var(--sdn-sp-6); flex-wrap: wrap; }
  .legend span { display: inline-flex; align-items: center; gap: var(--sdn-sp-2); font-size: var(--sdn-fs-micro); line-height: var(--sdn-lh-micro); letter-spacing: 0.06em; }
  .legend i { width: 7px; height: 7px; border-radius: 50%; display: inline-block; flex: none; }

  /* Below the grid's usable width the widget is full-bleed and the globe gets a
     shorter box, so the legend and the table below it stay on screen. */
  @media (max-width: 1180px) {
    .map { height: 300px; }
  }
</style>

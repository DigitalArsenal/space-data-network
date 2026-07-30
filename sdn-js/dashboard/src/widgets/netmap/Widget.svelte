<script>
  /**
   * PEER MAP.
   *
   * DESIGN SOURCE (declared, not copied):
   *   SpaceAware-UI @ archive/SpaceAware.io 2/SDN Console.dc.html
   *   sha256 abacdbfc62aeaee1193eccec9087669bfeb2324422fe8223482556fad207f152
   *   widget :192-205 · registry entry :866 · setInteractive :1020,1049
   *
   * "· GEOIP" is not in the title: which geolocation database backs the dots is
   * not a fact the reader of a PEER MAP header needs (owner directive
   * 2026-07-30, issued twice).
   *
   * PeerMap is the SHARED component the PEERS route also renders, so only ONE
   * globe is ever mounted (IRIS R7 + the grammar law's companion trap — two
   * mounted globes are two WebGL contexts on one page).
   *
   * `interactive` is the export's setInteractive(!editMode): without it,
   * dragging this widget to reorder it just spins the globe.
   */
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import PeerMap from '../../PeerMap.svelte';

  let { nodes = [], editMode = false, onSelectNode = () => {} } = $props();
</script>

<span class="tick tl" style="border-color:{theme.cyan};"></span>
<span class="tick tr" style="border-color:{theme.cyan};"></span>
<span class="tick bl" style="border-color:{theme.cyan};"></span>
<span class="tick br" style="border-color:{theme.cyan};"></span>
<PeerMap {nodes} onSelectNode={onSelectNode} interactive={!editMode} />

<style>
  /* Corner ticks — the design's accent brackets, against the Panel. */
  .tick { position: absolute; width: 9px; height: 9px; }
  .tick.tl { top: -1px; left: -1px; border-top: 1px solid; border-left: 1px solid; }
  .tick.tr { top: -1px; right: -1px; border-top: 1px solid; border-right: 1px solid; }
  .tick.bl { bottom: -1px; left: -1px; border-bottom: 1px solid; border-left: 1px solid; }
  .tick.br { bottom: -1px; right: -1px; border-bottom: 1px solid; border-right: 1px solid; }
</style>

<script lang="ts">
  /**
   * Pixel port of BMC2_F1_Surveillance.dc.html (loop U2.1). Static board —
   * per bmc2/README.md there is no logic class; the marquee/track/table
   * rows below are the ground truth's own fixture numbers, not live data.
   * `Bmc2TopBar` carries the shared 46px mode nav (identical byte-for-byte
   * markup across all six boards in the source).
   */
  import Bmc2TopBar from './Bmc2TopBar.svelte';
  import { BMC2_KICKERS } from '../../lib/bmc2';

  let { navigate }: { navigate: (path: string) => void } = $props();

  // Affiliation-colored track dots scattered over the stylized globe.
  const TRACKS = [
    { left: '39%', top: '33%', size: 7, color: '#35c9d8', glow: 7 },
    { left: '44%', top: '30%', size: 7, color: '#35c9d8', glow: 7 },
    { left: '48%', top: '35%', size: 7, color: '#ff6b6b', glow: 8 },
    { left: '42%', top: '38%', size: 7, color: '#5ad6a0', glow: 7 },
    { left: '64%', top: '46%', size: 6, color: '#9fb3bc', glow: 6 },
    { left: '70%', top: '58%', size: 7, color: '#ff6b6b', glow: 8 },
    { left: '30%', top: '60%', size: 6, color: '#35c9d8', glow: 6 },
    { left: '58%', top: '24%', size: 6, color: '#f0b54a', glow: 7 },
  ];

  const LAYER_ROWS = [
    { label: 'FRIENDLY', dot: '#35c9d8', glow: true, text: '#cde0e6', status: 'ON', statusColor: '#5ad6a0' },
    { label: 'HOSTILE', dot: '#ff6b6b', glow: true, text: '#cde0e6', status: 'ON', statusColor: '#5ad6a0' },
    { label: 'NEUTRAL', dot: '#f0b54a', glow: true, text: '#cde0e6', status: 'ON', statusColor: '#5ad6a0' },
    { label: 'UNKNOWN', dot: '#9fb3bc', glow: false, text: '#7d929b', status: 'DIM', statusColor: '#5d7681' },
    { label: 'DEBRIS', dot: null, text: '#7d929b', status: 'OFF', statusColor: '#5d7681' },
  ];

  const SELECTION_BARS = [
    { label: 'FRIENDLY', width: '50%', color: '#35c9d8', count: 2 },
    { label: 'HOSTILE', width: '25%', color: '#ff6b6b', count: 1 },
    { label: 'NEUTRAL', width: '25%', color: '#5ad6a0', count: 1 },
  ];

  const TABLE_ROWS = [
    {
      dot: '#35c9d8', rowBg: 'rgba(74,166,224,0.1)', opacity: 1,
      designator: 'ORB-10171', designatorColor: '#eaf6f8',
      type: 'PAYLOAD', typeColor: '#bcccd3',
      regime: 'LEO', regimeColor: '#35c9d8',
      affil: 'FRIEND', affilColor: '#7fc4d6',
      alt: '812', altColor: '#bcccd3',
      status: 'OPS', statusColor: '#5ad6a0',
    },
    {
      dot: '#35c9d8', rowBg: 'rgba(74,166,224,0.1)', opacity: 1,
      designator: 'ORB-10240', designatorColor: '#eaf6f8',
      type: 'PAYLOAD', typeColor: '#bcccd3',
      regime: 'LEO', regimeColor: '#35c9d8',
      affil: 'FRIEND', affilColor: '#7fc4d6',
      alt: '847', altColor: '#bcccd3',
      status: 'OPS', statusColor: '#5ad6a0',
    },
    {
      dot: '#ff6b6b', rowBg: 'rgba(255,107,107,0.08)', opacity: 1,
      designator: 'UNK-44219', designatorColor: '#ffd2d2',
      type: 'PAYLOAD', typeColor: '#bcccd3',
      regime: 'LEO', regimeColor: '#35c9d8',
      affil: 'HOSTILE', affilColor: '#ff8d8d',
      alt: '793', altColor: '#bcccd3',
      status: 'MANEUVER', statusColor: '#f0b54a',
    },
    {
      dot: '#5ad6a0', rowBg: 'rgba(90,214,160,0.07)', opacity: 1,
      designator: 'ORB-22008', designatorColor: '#eaf6f8',
      type: 'ROCKET BODY', typeColor: '#bcccd3',
      regime: 'GEO', regimeColor: '#f0b54a',
      affil: 'NEUTRAL', affilColor: '#9fe6c0',
      alt: '35,786', altColor: '#bcccd3',
      status: 'INACTIVE', statusColor: '#6f8693',
    },
    {
      dot: '#9fb3bc', dotSize: 6, rowBg: 'transparent', opacity: 0.7,
      designator: 'ORB-31552', designatorColor: '#bcccd3',
      type: 'DEBRIS', typeColor: '#8ba0a8',
      regime: 'LEO', regimeColor: '#35c9d8',
      affil: 'UNK', affilColor: '#7d929b',
      alt: '621', altColor: '#8ba0a8',
      status: '—', statusColor: '#6f8693',
    },
    {
      dot: '#9fb3bc', dotSize: 6, rowBg: 'transparent', opacity: 0.7,
      designator: 'ORB-31604', designatorColor: '#bcccd3',
      type: 'DEBRIS', typeColor: '#8ba0a8',
      regime: 'LEO', regimeColor: '#35c9d8',
      affil: 'UNK', affilColor: '#7d929b',
      alt: '638', altColor: '#8ba0a8',
      status: '—', statusColor: '#6f8693',
    },
  ];

  const HIGHLIGHTED_CELL_STYLE = {
    border: 'rgba(120,190,230,0.4)',
    background: 'linear-gradient(180deg,rgba(74,166,224,0.12),rgba(0,0,0,0.25))',
  };
  const NEUTRAL_CELL_STYLE = {
    border: 'rgba(90,150,180,0.3)',
    background: 'linear-gradient(180deg,rgba(74,166,224,0.07),rgba(0,0,0,0.25))',
  };
  const CLEAR_CELL_STYLE = {
    border: 'rgba(255,107,107,0.3)',
    background: 'linear-gradient(180deg,rgba(255,107,107,0.08),rgba(0,0,0,0.25))',
  };

  const COMMAND_CELLS = [
    { key: 'Q', glyph: '▣', glyphColor: '#7fd4ff', label: 'GROUP', labelColor: '#bcccd3', style: HIGHLIGHTED_CELL_STYLE },
    { key: 'W', glyph: '☆', glyphColor: '#9fd4f5', label: 'WATCH', labelColor: '#bcccd3', style: NEUTRAL_CELL_STYLE },
    { key: 'E', glyph: '◑', glyphColor: '#9fd4f5', label: 'COLOR', labelColor: '#bcccd3', style: NEUTRAL_CELL_STYLE },
    { key: 'A', glyph: '◎', glyphColor: '#9fd4f5', label: 'ISOLATE', labelColor: '#bcccd3', style: NEUTRAL_CELL_STYLE },
    { key: 'S', glyph: '⊘', glyphColor: '#9fd4f5', label: 'HIDE', labelColor: '#bcccd3', style: NEUTRAL_CELL_STYLE },
    { key: 'D', glyph: '⛶', glyphColor: '#9fd4f5', label: 'FRAME', labelColor: '#bcccd3', style: NEUTRAL_CELL_STYLE },
    { key: 'Z', glyph: '⤓', glyphColor: '#9fd4f5', label: 'EXPORT', labelColor: '#bcccd3', style: NEUTRAL_CELL_STYLE },
    { key: 'X', glyph: '⎘', glyphColor: '#9fd4f5', label: 'COMPARE', labelColor: '#bcccd3', style: NEUTRAL_CELL_STYLE },
    { key: 'C', glyph: '✕', glyphColor: '#ff8d8d', label: 'CLEAR', labelColor: '#d9b3b3', style: CLEAR_CELL_STYLE },
  ];
</script>

<div class="f1-root" data-screen-label="BMC2 F1 Surveillance">
  <div class="f1-globe">
    <div class="f1-globe-stars"></div>
    <div class="f1-sphere"></div>
    <div class="f1-ring f1-ring--a"></div>
    <div class="f1-ring f1-ring--b"></div>
    <div class="f1-ring f1-ring--c"></div>
    {#each TRACKS as t, i (i)}
      <span
        class="f1-track"
        style={`left:${t.left};top:${t.top};width:${t.size}px;height:${t.size}px;background:${t.color};box-shadow:0 0 ${t.glow}px ${t.color};`}
      ></span>
    {/each}
    <div class="f1-marquee"></div>
    <div class="f1-marquee-label">MARQUEE · 4 SELECTED</div>
  </div>
  <div class="f1-vignette"></div>

  <Bmc2TopBar
    activeMode="f1"
    kicker={BMC2_KICKERS.f1}
    statusLabel="LINK NOMINAL"
    statusTextColor="#5ad6a0"
    statusDotColor="#5ad6a0"
    {navigate}
  />

  <div class="f1-catalog-chip">
    <div class="f1-catalog-title">ORBITAL CATALOG</div>
    <div class="f1-catalog-sub">
      31,000 TRACKED · <span style="color:#35c9d8;">2,418</span> IN VIEW ·
      <span style="color:#9fd4f5;">4</span> SELECTED
    </div>
  </div>

  <div class="f1-layers">
    <div class="f1-layers-title">LAYERS</div>
    {#each LAYER_ROWS as row (row.label)}
      <div class="f1-layer-row" style={`color:${row.text};`}>
        <span class="f1-layer-name">
          {#if row.dot}
            <span
              class="f1-layer-dot"
              style={`background:${row.dot};${row.glow ? `box-shadow:0 0 6px ${row.dot};` : ''}`}
            ></span>
          {:else}
            <span class="f1-layer-dot f1-layer-dot--outline"></span>
          {/if}
          {row.label}
        </span>
        <span class="f1-layer-status" style={`color:${row.statusColor};`}>{row.status}</span>
      </div>
    {/each}
  </div>

  <div class="f1-drawer">
    <div class="f1-panel f1-panel--left">
      <div class="f1-sel-title">SELECTION · 4 OBJECTS</div>
      <div class="f1-sel-bars">
        {#each SELECTION_BARS as bar (bar.label)}
          <div class="f1-sel-bar-row">
            <span class="f1-sel-bar-label">{bar.label}</span>
            <div class="f1-sel-bar-track">
              <div class="f1-sel-bar-fill" style={`width:${bar.width};background:${bar.color};`}></div>
            </div>
            <span class="f1-sel-bar-count">{bar.count}</span>
          </div>
        {/each}
      </div>
      <div class="f1-sel-footer">
        <span>REGIME</span><span style="color:#35c9d8;">LEO ×3</span><span style="color:#f0b54a;">GEO ×1</span>
      </div>
    </div>

    <div class="f1-panel f1-panel--middle">
      <div class="f1-filter-strip">
        <span class="f1-filter-label">FILTER</span>
        <span class="f1-filter-chip">REGIME: LEO ×</span>
        <span class="f1-filter-chip">MANEUVERED &lt;24H ×</span>
        <span class="f1-filter-add">+ ADD FILTER</span>
        <span class="f1-filter-spacer"></span>
        <span class="f1-filter-match">2,418 MATCH · SORT: ALT ▼</span>
      </div>
      <div class="f1-table">
        <div class="f1-table-topline"></div>
        <div class="f1-table-head">
          <span></span><span>DESIGNATOR</span><span>TYPE</span><span>REGIME</span><span>AFFIL</span><span>ALT km</span
          ><span>STATUS</span>
        </div>
        <div class="f1-table-body">
          {#each TABLE_ROWS as row (row.designator)}
            <div class="f1-table-row" style={`background:${row.rowBg};opacity:${row.opacity};`}>
              <span
                class="f1-table-dot"
                style={`width:${row.dotSize ?? 7}px;height:${row.dotSize ?? 7}px;background:${row.dot};${row.opacity === 1 ? `box-shadow:0 0 6px ${row.dot};` : ''}`}
              ></span>
              <span class="f1-table-designator" style={`color:${row.designatorColor};`}>{row.designator}</span>
              <span class="f1-table-type" style={`color:${row.typeColor};`}>{row.type}</span>
              <span class="f1-table-regime" style={`color:${row.regimeColor};`}>{row.regime}</span>
              <span class="f1-table-affil" style={`color:${row.affilColor};`}>{row.affil}</span>
              <span class="f1-table-alt" style={`color:${row.altColor};`}>{row.alt}</span>
              <span class="f1-table-status" style={`color:${row.statusColor};`}>{row.status}</span>
            </div>
          {/each}
        </div>
      </div>
    </div>

    <div class="f1-panel f1-panel--right">
      <div class="f1-actions-head">
        <span class="f1-actions-title">SELECTION ACTIONS</span>
        <span class="f1-actions-tag">F1</span>
      </div>
      <div class="f1-actions-grid">
        {#each COMMAND_CELLS as cell (cell.key)}
          <div class="f1-action-cell" style={`border:1px solid ${cell.style.border};background:${cell.style.background};`}>
            <span class="f1-action-key">{cell.key}</span>
            <span class="f1-action-glyph" style={`color:${cell.glyphColor};`}>{cell.glyph}</span>
            <span class="f1-action-label" style={`color:${cell.labelColor};`}>{cell.label}</span>
          </div>
        {/each}
      </div>
    </div>
  </div>
</div>

<style>
  .f1-root {
    position: fixed;
    inset: 0;
    background: #04060a;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    color: #c7d6dd;
    overflow: hidden;
    -webkit-font-smoothing: antialiased;
  }

  /* ---- stylized CSS-only globe (no Cesium) ---- */

  .f1-globe {
    position: absolute;
    inset: 0;
    background: radial-gradient(circle at 50% 128%, #0a1a26, #04060a 62%);
    overflow: hidden;
  }

  .f1-globe-stars {
    position: absolute;
    inset: 0;
    background-image:
      radial-gradient(1px 1px at 14% 22%, rgba(255, 255, 255, 0.5), transparent),
      radial-gradient(1px 1px at 72% 31%, rgba(255, 255, 255, 0.35), transparent),
      radial-gradient(1px 1px at 41% 64%, rgba(255, 255, 255, 0.4), transparent),
      radial-gradient(1px 1px at 88% 71%, rgba(255, 255, 255, 0.3), transparent),
      radial-gradient(1px 1px at 24% 81%, rgba(255, 255, 255, 0.35), transparent),
      radial-gradient(1px 1px at 61% 14%, rgba(255, 255, 255, 0.3), transparent);
  }

  .f1-sphere {
    position: absolute;
    left: 50%;
    top: 50%;
    width: 560px;
    height: 560px;
    transform: translate(-50%, -50%);
    border-radius: 50%;
    background: radial-gradient(circle at 38% 30%, #16384e, #0c2030 52%, #060f16 78%);
    box-shadow:
      inset -50px -36px 130px rgba(0, 0, 0, 0.82),
      inset 30px 20px 80px rgba(53, 201, 216, 0.06),
      0 0 90px rgba(53, 201, 216, 0.14);
  }

  .f1-ring {
    position: absolute;
    left: 50%;
    top: 50%;
    border-radius: 50%;
  }
  .f1-ring--a {
    width: 740px;
    height: 300px;
    transform: translate(-50%, -50%) rotate(-18deg);
    border: 1px solid rgba(120, 190, 230, 0.18);
  }
  .f1-ring--b {
    width: 880px;
    height: 380px;
    transform: translate(-50%, -50%) rotate(12deg);
    border: 1px solid rgba(120, 190, 230, 0.12);
  }
  .f1-ring--c {
    width: 680px;
    height: 680px;
    transform: translate(-50%, -50%);
    border: 1px solid rgba(120, 190, 230, 0.08);
  }

  .f1-track {
    position: absolute;
    border-radius: 50%;
  }

  .f1-marquee {
    position: absolute;
    left: 36.5%;
    top: 27%;
    width: 140px;
    height: 120px;
    border: 1px dashed #9fd4f5;
    background-color: rgba(74, 166, 224, 0.08);
    background-image: repeating-linear-gradient(0deg, transparent 0 13px, rgba(120, 190, 230, 0.04) 13px 14px);
    box-shadow: 0 0 14px rgba(74, 166, 224, 0.4);
    animation: oc-marq 1.6s linear infinite;
  }

  .f1-marquee-label {
    position: absolute;
    left: 36.5%;
    top: 24.2%;
    font-size: 11px;
    letter-spacing: 0.1em;
    color: #9fd4f5;
  }

  .f1-vignette {
    position: absolute;
    inset: 0;
    pointer-events: none;
    box-shadow: inset 0 0 220px 60px rgba(0, 0, 0, 0.78);
  }

  /* ---- catalog chip ---- */

  .f1-catalog-chip {
    position: absolute;
    top: 58px;
    left: 16px;
    z-index: 15;
    background: rgba(7, 12, 18, 0.72);
    border: 1px solid rgba(110, 170, 190, 0.16);
    backdrop-filter: blur(6px);
    -webkit-backdrop-filter: blur(6px);
    padding: 9px 13px;
  }
  .f1-catalog-title {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 600;
    font-size: 14.5px;
    letter-spacing: 0.14em;
    color: #eaf6f8;
  }
  .f1-catalog-sub {
    font-size: 11.5px;
    letter-spacing: 0.08em;
    color: #6f8693;
    margin-top: 3px;
  }

  /* ---- layer manager ---- */

  .f1-layers {
    position: absolute;
    bottom: calc(25vh + 14px);
    left: 16px;
    z-index: 15;
    background: rgba(7, 12, 18, 0.72);
    border: 1px solid rgba(110, 170, 190, 0.16);
    backdrop-filter: blur(6px);
    -webkit-backdrop-filter: blur(6px);
    padding: 9px 12px;
    display: flex;
    flex-direction: column;
    gap: 6px;
    min-width: 150px;
  }
  .f1-layers-title {
    font-size: 9.5px;
    letter-spacing: 0.18em;
    color: #5d7681;
  }
  .f1-layer-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 14px;
    font-size: 12px;
  }
  .f1-layer-name {
    display: flex;
    align-items: center;
    gap: 7px;
  }
  .f1-layer-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
  }
  .f1-layer-dot--outline {
    border: 1px solid #6f8693;
    /* Ground truth: the DEBRIS marker is a SQUARE outline — the .dc.html
       span has no border-radius, unlike the colored circular dots. */
    border-radius: 0;
  }
  .f1-layer-status {
    font-size: 9.5px;
  }

  /* ---- bottom drawer ---- */

  .f1-drawer {
    position: absolute;
    bottom: 0;
    left: 0;
    right: 0;
    height: 25vh;
    z-index: 20;
    display: flex;
    gap: 2px;
    align-items: flex-end;
  }

  .f1-panel {
    background: linear-gradient(178deg, #16252f, #0a141b);
    border: 1px solid rgba(90, 150, 180, 0.22);
    box-shadow: inset 0 1px 0 rgba(150, 210, 240, 0.16);
    min-width: 0;
  }

  .f1-panel--left {
    flex: 1;
    height: 100%;
    display: flex;
    flex-direction: column;
    padding: 9px 11px;
  }
  .f1-sel-title {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-size: 10px;
    letter-spacing: 0.2em;
    color: #5a7a8a;
    margin-bottom: 8px;
  }
  .f1-sel-bars {
    display: flex;
    flex-direction: column;
    gap: 7px;
    flex: 1;
    justify-content: center;
  }
  .f1-sel-bar-row {
    display: flex;
    align-items: center;
    gap: 9px;
  }
  .f1-sel-bar-label {
    font-size: 11.5px;
    color: #9fb3bc;
    width: 74px;
    flex: none;
  }
  .f1-sel-bar-track {
    flex: 1;
    height: 8px;
    background: rgba(110, 170, 190, 0.1);
  }
  .f1-sel-bar-fill {
    height: 8px;
  }
  .f1-sel-bar-count {
    font-size: 12px;
    color: #eaf6f8;
    width: 20px;
    text-align: right;
    flex: none;
  }
  .f1-sel-footer {
    display: flex;
    gap: 6px;
    font-size: 10px;
    color: #6f8693;
    border-top: 1px solid rgba(110, 170, 190, 0.1);
    padding-top: 7px;
  }

  .f1-panel--middle {
    flex: 3;
    height: calc(100% - 26px);
    display: flex;
    flex-direction: column;
    gap: 2px;
    background: #060d12;
  }
  .f1-filter-strip {
    flex: none;
    height: 46px;
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 0 14px;
    background: linear-gradient(178deg, #16252f, #0a141b);
    border: 1px solid rgba(90, 150, 180, 0.22);
    box-shadow: inset 0 1px 0 rgba(150, 210, 240, 0.16);
  }
  .f1-filter-label {
    font-size: 9.5px;
    letter-spacing: 0.16em;
    color: #5a7a8a;
    flex: none;
  }
  .f1-filter-chip {
    font-size: 11px;
    color: #9fd4f5;
    background: rgba(74, 166, 224, 0.14);
    border: 1px solid rgba(120, 190, 230, 0.4);
    padding: 3px 8px;
  }
  .f1-filter-add {
    font-size: 11px;
    color: #6f8693;
    border: 1px dashed rgba(110, 170, 190, 0.3);
    padding: 3px 8px;
  }
  .f1-filter-spacer {
    flex: 1;
  }
  .f1-filter-match {
    font-size: 11px;
    color: #6f8693;
  }

  .f1-table {
    flex: 1;
    background: linear-gradient(178deg, #15232d, #0a141b);
    border: 1px solid rgba(90, 150, 180, 0.22);
    box-shadow: inset 0 1px 0 rgba(150, 210, 240, 0.14);
    min-width: 0;
    position: relative;
    overflow: hidden;
    display: flex;
    flex-direction: column;
  }
  .f1-table-topline {
    position: absolute;
    top: 0;
    left: 0;
    right: 0;
    height: 1px;
    background: linear-gradient(90deg, transparent, #4aa6e0, transparent);
    opacity: 0.6;
  }
  .f1-table-head,
  .f1-table-row {
    display: grid;
    grid-template-columns: 18px 1.4fr 0.9fr 0.7fr 0.8fr 0.9fr 0.7fr;
    gap: 0 12px;
  }
  .f1-table-head {
    padding: 7px 16px 6px;
    border-bottom: 1px solid rgba(110, 170, 190, 0.16);
    font-size: 9.5px;
    letter-spacing: 0.12em;
    color: #5d7681;
  }
  .f1-table-body {
    flex: 1;
    overflow-y: auto;
  }
  .f1-table-row {
    padding: 6px 16px;
    align-items: center;
    border-bottom: 1px solid rgba(110, 170, 190, 0.07);
  }
  .f1-table-dot {
    border-radius: 50%;
  }
  .f1-table-designator {
    font-size: 13px;
  }
  .f1-table-type,
  .f1-table-regime,
  .f1-table-alt {
    font-size: 12px;
  }
  .f1-table-alt {
    font-variant-numeric: tabular-nums;
  }
  .f1-table-affil,
  .f1-table-status {
    font-size: 11px;
  }

  .f1-panel--right {
    flex: 1;
    height: 100%;
    display: flex;
    flex-direction: column;
    padding: 9px 10px;
  }
  .f1-actions-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 7px;
  }
  .f1-actions-title {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-size: 10px;
    letter-spacing: 0.2em;
    color: #5a7a8a;
  }
  .f1-actions-tag {
    font-size: 9.5px;
    color: #9fd4f5;
  }
  .f1-actions-grid {
    flex: 1;
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    grid-template-rows: repeat(3, 1fr);
    gap: 5px;
  }
  .f1-action-cell {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 3px;
    position: relative;
  }
  .f1-action-key {
    position: absolute;
    top: 2px;
    left: 4px;
    font-size: 9.5px;
    color: #5a7a8a;
  }
  .f1-action-glyph {
    font-size: 21.5px;
  }
  .f1-action-label {
    font-size: 9.5px;
    text-align: center;
  }
</style>

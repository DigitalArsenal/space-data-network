<script lang="ts">
  /**
   * Pixel port of BMC2_F3_Sensors.dc.html (loop U2.1). Static sensor
   * volumes / access-coverage board — per bmc2/README.md there is no logic
   * class, so the access-window bars/table rows below are the ground
   * truth's own fixture numbers, not a live schedule.
   *
   * Note: the detail-panel heading "TARGET ACCESS · ORB-10171 → 4 SENSORS"
   * keeps its "→" verbatim — the token hard rule bans directional arrows
   * appended to *button/link/nav labels*, not descriptive headings, and
   * this one isn't an actionable control (unlike the index's "OPEN →",
   * which is a link label and IS stripped in Bmc2Index.svelte).
   */
  import Bmc2TopBar from './Bmc2TopBar.svelte';
  import { BMC2_KICKERS } from '../../lib/bmc2';

  let { navigate }: { navigate: (path: string) => void } = $props();

  const ACCESS_ROWS = [
    {
      label: 'DIEGO GARCIA · EO',
      labelColor: '#bcccd3',
      segments: [
        { left: '12%', width: '26%', color: '#35c9d8' },
        { left: '70%', width: '10%', color: '#35c9d8' },
      ],
      readout: '142° / 38° / 1,210',
      readoutColor: '#8ba0a8',
    },
    {
      label: 'KAENA POINT · RADAR',
      labelColor: '#bcccd3',
      segments: [{ left: '40%', width: '18%', color: '#5ad6a0' }],
      readout: '061° / 22° / 1,880',
      readoutColor: '#8ba0a8',
    },
    {
      label: 'SBSS-1 · SPACE EO',
      labelColor: '#bcccd3',
      segments: [
        { left: '5%', width: '40%', color: '#35c9d8' },
        { left: '82%', width: '12%', color: '#35c9d8' },
      ],
      readout: '— / — / 2,040',
      readoutColor: '#8ba0a8',
    },
    {
      label: 'EGLIN · RADAR',
      labelColor: '#8ba0a8',
      segments: [{ left: '88%', width: '8%', color: 'rgba(240,181,74,0.7)' }],
      readout: 'COVERAGE GAP',
      readoutColor: '#6f8693',
    },
  ];

  const HIGHLIGHTED_CELL_STYLE = {
    border: 'rgba(120,190,230,0.45)',
    background: 'linear-gradient(180deg,rgba(74,166,224,0.12),rgba(0,0,0,0.25))',
  };
  const NEUTRAL_CELL_STYLE = {
    border: 'rgba(90,150,180,0.35)',
    background: 'linear-gradient(180deg,rgba(74,166,224,0.08),rgba(0,0,0,0.25))',
  };

  const COMMAND_CELLS = [
    { glyph: '◣', label: 'ADD CONIC', glyphColor: '#7fd4ff', style: HIGHLIGHTED_CELL_STYLE },
    { glyph: '▭', label: 'RECTANG', glyphColor: '#9fd4f5', style: NEUTRAL_CELL_STYLE },
    { glyph: '⬡', label: 'CUSTOM', glyphColor: '#9fd4f5', style: NEUTRAL_CELL_STYLE },
    { glyph: '⌖', label: 'SLEW-TO', glyphColor: '#9fd4f5', style: NEUTRAL_CELL_STYLE },
    { glyph: '🔒', label: 'LOCK BS', glyphColor: '#9fd4f5', style: NEUTRAL_CELL_STYLE },
    { glyph: '◯', label: 'FOOTPRINT', glyphColor: '#9fd4f5', style: NEUTRAL_CELL_STYLE },
    { glyph: '▦', label: 'HEATMAP', glyphColor: '#9fd4f5', style: NEUTRAL_CELL_STYLE },
    { glyph: '⮃', label: 'TIP-CUE', glyphColor: '#9fd4f5', style: NEUTRAL_CELL_STYLE },
    { glyph: '⤓', label: 'SCHEDULE', glyphColor: '#9fd4f5', style: NEUTRAL_CELL_STYLE },
  ];
</script>

<div class="f3-root" data-screen-label="BMC2 F3 Sensors">
  <div class="f3-globe">
    <div class="f3-globe-stars"></div>
    <div class="f3-sphere"></div>
    <div class="f3-sat-dot"></div>
    <div class="f3-sensor-cone"></div>
    <div class="f3-boresight"></div>
    <div class="f3-footprint"></div>
    <div class="f3-footprint-label">FOV FOOTPRINT · 1,240 km</div>
    <div class="f3-ground-ring f3-ground-ring--a"></div>
    <div class="f3-ground-dot f3-ground-dot--a"></div>
    <div class="f3-ground-ring f3-ground-ring--b"></div>
    <div class="f3-ground-dot f3-ground-dot--b"></div>
  </div>
  <div class="f3-vignette"></div>

  <Bmc2TopBar
    activeMode="f3"
    kicker={BMC2_KICKERS.f3}
    statusLabel="LINK NOMINAL"
    statusTextColor="#5ad6a0"
    statusDotColor="#5ad6a0"
    {navigate}
  />

  <div class="f3-drawer">
    <div class="f3-panel f3-panel--left">
      <div class="f3-sensor-head">
        <span class="f3-sensor-title">SENSOR · EO IMAGER</span>
        <span class="f3-sensor-tag">CONIC</span>
      </div>
      <div class="f3-cone-canvas">
        <div class="f3-cone-model">
          <div class="f3-apex-dot"></div>
          <div class="f3-cone-tri"></div>
          <div class="f3-cone-boresight"></div>
          <div class="f3-cone-footprint"></div>
        </div>
        <div class="f3-cone-caption">HALF-ANGLE 28.5°<br />BORESIGHT NADIR<br />RANGE 0–2,400 km</div>
      </div>
    </div>

    <div class="f3-panel f3-panel--middle">
      <div class="f3-access-strip">
        <span class="f3-access-label">ACCESS WINDOW</span>
        <span class="f3-access-clock">02:14:08</span>
        <div class="f3-access-track">
          <div class="f3-access-window" style="left:18%;width:22%;"></div>
          <div class="f3-access-window" style="left:64%;width:14%;"></div>
          <div class="f3-access-playhead"></div>
        </div>
        <span class="f3-access-next">NEXT ACCESS T+04:12</span>
      </div>

      <div class="f3-detail">
        <div class="f3-detail-topline"></div>
        <div class="f3-detail-head">
          <span class="f3-detail-title">TARGET ACCESS · ORB-10171 → 4 SENSORS</span>
          <div class="f3-detail-spacer"></div>
          <span class="f3-detail-columns">AZ / EL / RANGE</span>
        </div>
        <div class="f3-access-rows">
          {#each ACCESS_ROWS as row (row.label)}
            <div class="f3-access-row">
              <span class="f3-access-row-label" style={`color:${row.labelColor};`}>{row.label}</span>
              <div class="f3-access-row-track">
                {#each row.segments as seg, i (i)}
                  <div
                    class="f3-access-row-segment"
                    style={`left:${seg.left};width:${seg.width};background:${seg.color};`}
                  ></div>
                {/each}
              </div>
              <span class="f3-access-row-readout" style={`color:${row.readoutColor};`}>{row.readout}</span>
            </div>
          {/each}
        </div>
      </div>
    </div>

    <div class="f3-panel f3-panel--right">
      <div class="f3-actions-head">
        <span class="f3-actions-title">SENSOR ACTIONS</span>
        <span class="f3-actions-tag">F3</span>
      </div>
      <div class="f3-actions-grid">
        {#each COMMAND_CELLS as cell (cell.label)}
          <div class="f3-action-cell" style={`border:1px solid ${cell.style.border};background:${cell.style.background};`}>
            <span class="f3-action-glyph" style={`color:${cell.glyphColor};`}>{cell.glyph}</span>
            <span class="f3-action-label">{cell.label}</span>
          </div>
        {/each}
      </div>
    </div>
  </div>
</div>

<style>
  .f3-root {
    position: fixed;
    inset: 0;
    background: #04060a;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    color: #c7d6dd;
    overflow: hidden;
    -webkit-font-smoothing: antialiased;
  }

  /* ---- stylized CSS-only globe (no Cesium) ---- */

  .f3-globe {
    position: absolute;
    inset: 0;
    background: radial-gradient(circle at 50% 128%, #0a1a26, #04060a 62%);
    overflow: hidden;
  }

  .f3-globe-stars {
    position: absolute;
    inset: 0;
    background-image:
      radial-gradient(1px 1px at 14% 22%, rgba(255, 255, 255, 0.5), transparent),
      radial-gradient(1px 1px at 72% 31%, rgba(255, 255, 255, 0.35), transparent),
      radial-gradient(1px 1px at 41% 64%, rgba(255, 255, 255, 0.4), transparent),
      radial-gradient(1px 1px at 88% 71%, rgba(255, 255, 255, 0.3), transparent);
  }

  .f3-sphere {
    position: absolute;
    left: 50%;
    top: 52%;
    width: 560px;
    height: 560px;
    transform: translate(-50%, -50%);
    border-radius: 50%;
    background: radial-gradient(circle at 38% 30%, #16384e, #0c2030 52%, #060f16 78%);
    box-shadow:
      inset -50px -36px 130px rgba(0, 0, 0, 0.82),
      0 0 90px rgba(53, 201, 216, 0.14);
  }

  .f3-sat-dot {
    position: absolute;
    left: 50%;
    top: 24%;
    width: 11px;
    height: 11px;
    transform: translate(-50%, -50%);
    border-radius: 50%;
    background: #eaf6f8;
    box-shadow: 0 0 12px #fff;
  }

  .f3-sensor-cone {
    position: absolute;
    left: 50%;
    top: 24%;
    width: 0;
    height: 0;
    transform: translate(-50%, 0);
    border-left: 96px solid transparent;
    border-right: 96px solid transparent;
    border-top: 230px solid rgba(53, 201, 216, 0.12);
  }

  .f3-boresight {
    position: absolute;
    left: 50%;
    top: 24%;
    width: 1px;
    height: 230px;
    transform: translate(-50%, 0);
    background: linear-gradient(180deg, rgba(120, 190, 230, 0.7), transparent);
  }

  .f3-footprint {
    position: absolute;
    left: 50%;
    top: 53%;
    width: 184px;
    height: 64px;
    transform: translate(-50%, -50%);
    border: 1px solid rgba(53, 201, 216, 0.6);
    border-radius: 50%;
    background: radial-gradient(ellipse, rgba(53, 201, 216, 0.18), transparent 72%);
    box-shadow: 0 0 18px rgba(53, 201, 216, 0.3);
  }

  .f3-footprint-label {
    position: absolute;
    left: calc(50% + 96px);
    top: 55%;
    font-size: 11px;
    color: #7fdce8;
  }

  .f3-ground-ring {
    position: absolute;
    transform: translate(-50%, -50%);
    border: 1px dashed;
    border-radius: 50%;
  }
  .f3-ground-ring--a {
    left: 31%;
    top: 60%;
    width: 120px;
    height: 120px;
    border-color: rgba(240, 181, 74, 0.45);
  }
  .f3-ground-ring--b {
    left: 70%;
    top: 64%;
    width: 96px;
    height: 96px;
    border-color: rgba(240, 181, 74, 0.4);
  }
  .f3-ground-dot {
    position: absolute;
    width: 7px;
    height: 7px;
    transform: translate(-50%, -50%);
    background: #f0b54a;
    box-shadow: 0 0 7px #f0b54a;
  }
  .f3-ground-dot--a {
    left: 31%;
    top: 60%;
  }
  .f3-ground-dot--b {
    left: 70%;
    top: 64%;
  }

  .f3-vignette {
    position: absolute;
    inset: 0;
    pointer-events: none;
    box-shadow: inset 0 0 220px 60px rgba(0, 0, 0, 0.78);
  }

  /* ---- bottom drawer ---- */

  .f3-drawer {
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

  .f3-panel {
    background: linear-gradient(178deg, #16252f, #0a141b);
    border: 1px solid rgba(90, 150, 180, 0.22);
    box-shadow: inset 0 1px 0 rgba(150, 210, 240, 0.16);
    min-width: 0;
  }

  .f3-panel--left {
    flex: 1;
    height: 100%;
    display: flex;
    flex-direction: column;
    padding: 9px 10px;
  }
  .f3-sensor-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 7px;
  }
  .f3-sensor-title {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-size: 10px;
    letter-spacing: 0.2em;
    color: #5a7a8a;
  }
  .f3-sensor-tag {
    font-size: 9.5px;
    color: #7fdce8;
  }
  .f3-cone-canvas {
    flex: 1;
    position: relative;
    border: 1px solid rgba(90, 150, 180, 0.3);
    box-shadow: inset 0 0 34px rgba(0, 0, 0, 0.65);
    background:
      repeating-linear-gradient(0deg, rgba(74, 166, 224, 0.05) 0 1px, transparent 1px 22px),
      repeating-linear-gradient(90deg, rgba(74, 166, 224, 0.05) 0 1px, transparent 1px 22px);
    overflow: hidden;
    display: flex;
    align-items: center;
    justify-content: center;
  }
  .f3-cone-model {
    position: relative;
    width: 120px;
    height: 108px;
  }
  .f3-apex-dot {
    position: absolute;
    left: 50%;
    top: 6px;
    width: 8px;
    height: 8px;
    transform: translate(-50%, -50%);
    border-radius: 50%;
    background: #eaf6f8;
    box-shadow: 0 0 8px #fff;
  }
  .f3-cone-tri {
    position: absolute;
    left: 50%;
    top: 6px;
    width: 0;
    height: 0;
    transform: translate(-50%, 0);
    border-left: 48px solid transparent;
    border-right: 48px solid transparent;
    border-top: 88px solid rgba(53, 201, 216, 0.16);
  }
  .f3-cone-boresight {
    position: absolute;
    left: 50%;
    top: 6px;
    width: 1px;
    height: 88px;
    transform: translate(-50%, 0);
    background: linear-gradient(180deg, #9fd4f5, transparent);
  }
  .f3-cone-footprint {
    position: absolute;
    left: 50%;
    bottom: 6px;
    width: 96px;
    height: 22px;
    transform: translate(-50%, 0);
    border: 1px solid rgba(53, 201, 216, 0.5);
    border-radius: 50%;
  }
  .f3-cone-caption {
    position: absolute;
    left: 6px;
    bottom: 5px;
    font-size: 9px;
    color: #5a7a8a;
    line-height: 1.6;
  }

  .f3-panel--middle {
    flex: 3;
    height: calc(100% - 26px);
    display: flex;
    flex-direction: column;
    gap: 2px;
    background: #060d12;
  }
  .f3-access-strip {
    flex: none;
    height: 46px;
    display: flex;
    align-items: center;
    gap: 13px;
    padding: 0 16px;
    background: linear-gradient(178deg, #16252f, #0a141b);
    border: 1px solid rgba(90, 150, 180, 0.22);
    box-shadow: inset 0 1px 0 rgba(150, 210, 240, 0.16);
  }
  .f3-access-label {
    font-size: 9.5px;
    letter-spacing: 0.16em;
    color: #5a7a8a;
  }
  .f3-access-clock {
    font-weight: 600;
    font-size: 19px;
    color: #eaf6f8;
    font-variant-numeric: tabular-nums;
  }
  .f3-access-track {
    flex: 1;
    height: 18px;
    position: relative;
    background: linear-gradient(178deg, #0b151c, #060d12);
    border: 1px solid rgba(90, 150, 180, 0.22);
  }
  .f3-access-window {
    position: absolute;
    top: 0;
    bottom: 0;
    background: rgba(53, 201, 216, 0.3);
    border-left: 1px solid #35c9d8;
    border-right: 1px solid #35c9d8;
  }
  .f3-access-playhead {
    position: absolute;
    top: -3px;
    bottom: -3px;
    left: 46%;
    width: 2px;
    background: #ffb24d;
    box-shadow: 0 0 8px #ffb24d;
  }
  .f3-access-next {
    font-size: 11px;
    color: #7fdce8;
  }

  .f3-detail {
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
  .f3-detail-topline {
    position: absolute;
    top: 0;
    left: 0;
    right: 0;
    height: 1px;
    background: linear-gradient(90deg, transparent, #4aa6e0, transparent);
    opacity: 0.6;
  }
  .f3-detail-head {
    flex: none;
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 7px 16px;
    border-bottom: 1px solid rgba(110, 170, 190, 0.16);
  }
  .f3-detail-title {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 700;
    font-size: 17px;
    letter-spacing: 0.06em;
    color: #eaf6f8;
  }
  .f3-detail-spacer {
    flex: 1;
  }
  .f3-detail-columns {
    font-size: 9.5px;
    color: #5a7a8a;
    letter-spacing: 0.12em;
  }

  .f3-access-rows {
    flex: 1;
    overflow-y: auto;
    padding: 8px 16px;
    display: flex;
    flex-direction: column;
    gap: 7px;
  }
  .f3-access-row {
    display: flex;
    align-items: center;
    gap: 10px;
  }
  .f3-access-row-label {
    font-size: 12px;
    width: 130px;
    flex: none;
  }
  .f3-access-row-track {
    flex: 1;
    height: 10px;
    background: rgba(110, 170, 190, 0.08);
    position: relative;
  }
  .f3-access-row-segment {
    position: absolute;
    top: 0;
    bottom: 0;
  }
  .f3-access-row-readout {
    font-size: 11px;
    width: 120px;
    text-align: right;
    flex: none;
  }

  .f3-panel--right {
    flex: 1;
    height: 100%;
    display: flex;
    flex-direction: column;
    padding: 9px 10px;
  }
  .f3-actions-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 7px;
  }
  .f3-actions-title {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-size: 10px;
    letter-spacing: 0.2em;
    color: #5a7a8a;
  }
  .f3-actions-tag {
    font-size: 9.5px;
    color: #9fd4f5;
  }
  .f3-actions-grid {
    flex: 1;
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    grid-template-rows: repeat(3, 1fr);
    gap: 5px;
  }
  .f3-action-cell {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 3px;
  }
  .f3-action-glyph {
    font-size: 21.5px;
  }
  .f3-action-label {
    font-size: 9.5px;
    color: #bcccd3;
    text-align: center;
  }
</style>

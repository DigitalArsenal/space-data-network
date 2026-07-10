<script lang="ts">
  /**
   * Pixel port of BMC2_F6_Comms.dc.html (loop U2.2). Static ground-stations
   * / pass-schedule board — per bmc2/README.md there is no logic class, so
   * the pass list / link-budget figures below are the ground truth's own
   * fixture numbers, not a live scheduler. GREEN semantic accent per
   * DESIGN_TOKENS (see lib/bmc2.ts BMC2_MODE_ACCENTS.f6).
   */
  import Bmc2TopBar from './Bmc2TopBar.svelte';
  import { BMC2_KICKERS } from '../../lib/bmc2';

  let { navigate }: { navigate: (path: string) => void } = $props();

  const PASS_ROWS = [
    {
      dot: '#5ad6a0',
      dotSize: 7,
      glow: true,
      rowBg: 'rgba(90,214,160,0.08)',
      opacity: 1,
      station: 'DIEGO GARCIA',
      stationColor: '#eaf6f8',
      band: 'X',
      bandColor: '#9fe6c0',
      fieldColor: '#bcccd3',
      aos: 'NOW',
      dur: '9:40',
      maxEl: '38°',
    },
    {
      dot: '#f0b54a',
      dotSize: 7,
      glow: false,
      rowBg: 'rgba(240,181,74,0.06)',
      opacity: 1,
      station: 'SVALBARD',
      stationColor: '#eaf6f8',
      band: 'S',
      bandColor: '#bcccd3',
      fieldColor: '#bcccd3',
      aos: 'T+04:12',
      dur: '7:10',
      maxEl: '61°',
    },
    {
      dot: '#35c9d8',
      dotSize: 7,
      glow: false,
      rowBg: 'transparent',
      opacity: 1,
      station: 'KAENA POINT',
      stationColor: '#eaf6f8',
      band: 'X',
      bandColor: '#bcccd3',
      fieldColor: '#bcccd3',
      aos: 'T+12:48',
      dur: '11:20',
      maxEl: '44°',
    },
    {
      dot: '#6f8693',
      dotSize: 6,
      glow: false,
      rowBg: 'transparent',
      opacity: 0.7,
      station: 'TROLL',
      stationColor: '#bcccd3',
      band: 'S',
      bandColor: '#8ba0a8',
      fieldColor: '#8ba0a8',
      aos: 'T+22:05',
      dur: '6:30',
      maxEl: '28°',
    },
  ];

  const GREEN_CELL_STYLE = { border: 'rgba(90,214,160,0.45)', background: 'linear-gradient(180deg,rgba(90,214,160,0.12),rgba(0,0,0,0.25))' };
  const SCREEN_CELL_STYLE = { border: 'rgba(120,190,230,0.45)', background: 'linear-gradient(180deg,rgba(74,166,224,0.12),rgba(0,0,0,0.25))' };
  const NEUTRAL_CELL_STYLE = { border: 'rgba(90,150,180,0.35)', background: 'linear-gradient(180deg,rgba(74,166,224,0.08),rgba(0,0,0,0.25))' };
  const RED_CELL_STYLE = { border: 'rgba(255,107,107,0.4)', background: 'linear-gradient(180deg,rgba(255,107,107,0.08),rgba(0,0,0,0.25))' };

  const COMMAND_CELLS = [
    { glyph: '⇩', glyphColor: '#9fe6c0', label: 'DOWNLINK', style: GREEN_CELL_STYLE },
    { glyph: '⇧', glyphColor: '#7fd4ff', label: 'UPLINK CMD', style: SCREEN_CELL_STYLE },
    { glyph: '📡', glyphColor: '#9fd4f5', label: 'ADD STN', style: NEUTRAL_CELL_STYLE },
    { glyph: '⊞', glyphColor: '#9fd4f5', label: 'SCHEDULE', style: NEUTRAL_CELL_STYLE },
    { glyph: '≋', glyphColor: '#9fd4f5', label: 'BAND SEL', style: NEUTRAL_CELL_STYLE },
    { glyph: '⊕', glyphColor: '#9fd4f5', label: '2D TRACK', style: NEUTRAL_CELL_STYLE },
    { glyph: '◷', glyphColor: '#9fd4f5', label: 'PASS PRED', style: NEUTRAL_CELL_STYLE },
    { glyph: '⤓', glyphColor: '#9fd4f5', label: 'EXPORT', style: NEUTRAL_CELL_STYLE },
    { glyph: '⏏', glyphColor: '#ff8d8d', label: 'ABORT DL', style: RED_CELL_STYLE },
  ];
</script>

<div class="f6-root" data-screen-label="BMC2 F6 Comms">
  <div class="f6-globe">
    <div class="f6-globe-stars"></div>
    <div class="f6-sphere"></div>
    <div class="f6-ground-track"></div>
    <div class="f6-sat-dot"></div>
    <div class="f6-downlink-beam"></div>
    <div class="f6-station-ring f6-station-ring--active"></div>
    <div class="f6-station-dot f6-station-dot--active"></div>
    <div class="f6-station-label">DIEGO GARCIA · X-BAND LOCK</div>
    <div class="f6-station-ring f6-station-ring--idle"></div>
    <div class="f6-station-dot f6-station-dot--idle"></div>
    <div class="f6-station-dot f6-station-dot--unknown"></div>
  </div>
  <div class="f6-vignette"></div>

  <Bmc2TopBar
    activeMode="f6"
    kicker={BMC2_KICKERS.f6}
    statusLabel="X-BAND LOCK"
    statusTextColor="#5ad6a0"
    statusDotColor="#5ad6a0"
    statusPulseSeconds={1.6}
    {navigate}
  />

  <div class="f6-drawer">
    <div class="f6-panel f6-panel--left">
      <div class="f6-link-head">
        <span class="f6-link-title">LINK · X-BAND DL</span>
        <span class="f6-link-tag">LOCKED</span>
      </div>
      <div class="f6-link-body">
        <div class="f6-bar-row">
          <div class="f6-bar-label-row"><span>LINK MARGIN</span><span class="f6-bar-value f6-bar-value--green">+6.4 dB</span></div>
          <div class="f6-bar-track"><div class="f6-bar-fill f6-bar-fill--green" style="width:72%;"></div></div>
        </div>
        <div class="f6-bar-row">
          <div class="f6-bar-label-row"><span>DATA RATE</span><span class="f6-bar-value">320 Mbps</span></div>
          <div class="f6-bar-track"><div class="f6-bar-fill f6-bar-fill--cyan" style="width:64%;"></div></div>
        </div>
        <div class="f6-link-caption">EIRP 41.2 dBW · G/T 18 dB/K<br />ELEV 38° · DOPPLER −41 kHz<br />BACKLOG 2.4 GB</div>
      </div>
    </div>

    <div class="f6-panel f6-panel--middle">
      <div class="f6-schedule-strip">
        <span class="f6-schedule-label">CONTACT SCHEDULE</span>
        <span class="f6-schedule-next">NEXT AOS T+04:12</span>
        <div class="f6-schedule-track">
          <div class="f6-schedule-window f6-schedule-window--a"></div>
          <div class="f6-schedule-window f6-schedule-window--b"></div>
          <div class="f6-schedule-window f6-schedule-window--c"></div>
          <div class="f6-schedule-playhead"></div>
        </div>
        <span class="f6-schedule-contention">CONTENTION ×1</span>
      </div>

      <div class="f6-detail">
        <div class="f6-detail-topline"></div>

        <div class="f6-pass-list">
          <div class="f6-pass-head">
            <span></span><span>STATION</span><span>BAND</span><span>AOS</span><span>DUR</span><span>MAX EL</span>
          </div>
          <div class="f6-pass-body">
            {#each PASS_ROWS as row (row.station)}
              <div class="f6-pass-row" style={`background:${row.rowBg};opacity:${row.opacity};`}>
                <span
                  class="f6-pass-dot"
                  style={`width:${row.dotSize}px;height:${row.dotSize}px;background:${row.dot};${row.glow ? `box-shadow:0 0 6px ${row.dot};` : ''}`}
                ></span>
                <span class="f6-pass-station" style={`color:${row.stationColor};`}>{row.station}</span>
                <span class="f6-pass-band" style={`color:${row.bandColor};`}>{row.band}</span>
                <span class="f6-pass-field" style={`color:${row.fieldColor};`}>{row.aos}</span>
                <span class="f6-pass-field" style={`color:${row.fieldColor};`}>{row.dur}</span>
                <span class="f6-pass-field" style={`color:${row.fieldColor};`}>{row.maxEl}</span>
              </div>
            {/each}
          </div>
        </div>

        <div class="f6-track2d">
          <div class="f6-track2d-title">GROUND TRACK · 2D</div>
          <div class="f6-track2d-canvas">
            <div class="f6-track2d-hline"></div>
            <div class="f6-track2d-vline"></div>
            <div class="f6-track2d-orbit"></div>
            <div class="f6-track2d-sat"></div>
            <div class="f6-track2d-station"></div>
          </div>
        </div>
      </div>
    </div>

    <div class="f6-panel f6-panel--right">
      <div class="f6-actions-head">
        <span class="f6-actions-title">COMMS ACTIONS</span>
        <span class="f6-actions-tag">F6</span>
      </div>
      <div class="f6-actions-grid">
        {#each COMMAND_CELLS as cell (cell.label)}
          <div class="f6-action-cell" style={`border:1px solid ${cell.style.border};background:${cell.style.background};`}>
            <span class="f6-action-glyph" style={`color:${cell.glyphColor};`}>{cell.glyph}</span>
            <span class="f6-action-label">{cell.label}</span>
          </div>
        {/each}
      </div>
    </div>
  </div>
</div>

<style>
  .f6-root {
    position: fixed;
    inset: 0;
    background: #04060a;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    color: #c7d6dd;
    overflow: hidden;
    -webkit-font-smoothing: antialiased;
  }

  /* ---- stylized CSS-only globe (no Cesium) ---- */

  .f6-globe {
    position: absolute;
    inset: 0;
    background: radial-gradient(circle at 50% 128%, #0a1a26, #04060a 62%);
    overflow: hidden;
  }

  .f6-globe-stars {
    position: absolute;
    inset: 0;
    background-image:
      radial-gradient(1px 1px at 14% 22%, rgba(255, 255, 255, 0.5), transparent),
      radial-gradient(1px 1px at 72% 31%, rgba(255, 255, 255, 0.35), transparent),
      radial-gradient(1px 1px at 41% 64%, rgba(255, 255, 255, 0.4), transparent),
      radial-gradient(1px 1px at 88% 71%, rgba(255, 255, 255, 0.3), transparent);
  }

  .f6-sphere {
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

  /* ground track (dashed great-circle-ish) */
  .f6-ground-track {
    position: absolute;
    left: 50%;
    top: 52%;
    width: 540px;
    height: 180px;
    transform: translate(-50%, -50%) rotate(-14deg);
    border: 1px dashed rgba(53, 201, 216, 0.4);
    border-radius: 50%;
  }

  /* satellite + downlink beam to active station */
  .f6-sat-dot {
    position: absolute;
    left: 42%;
    top: 30%;
    width: 10px;
    height: 10px;
    transform: translate(-50%, -50%);
    border-radius: 50%;
    background: #eaf6f8;
    box-shadow: 0 0 10px #fff;
  }
  .f6-downlink-beam {
    position: absolute;
    left: 36%;
    top: 58%;
    width: 120px;
    height: 2px;
    transform-origin: left center;
    transform: rotate(-118deg);
    background: linear-gradient(90deg, #5ad6a0, transparent);
    box-shadow: 0 0 8px rgba(90, 214, 160, 0.6);
  }

  /* active station */
  .f6-station-ring {
    position: absolute;
    transform: translate(-50%, -50%);
    border-radius: 50%;
  }
  .f6-station-ring--active {
    left: 36%;
    top: 58%;
    width: 110px;
    height: 110px;
    border: 1px solid rgba(90, 214, 160, 0.5);
    background: radial-gradient(circle, rgba(90, 214, 160, 0.1), transparent 70%);
  }
  .f6-station-dot {
    position: absolute;
    transform: translate(-50%, -50%);
  }
  .f6-station-dot--active {
    left: 36%;
    top: 58%;
    width: 8px;
    height: 8px;
    background: #5ad6a0;
    box-shadow: 0 0 8px #5ad6a0;
  }
  .f6-station-label {
    position: absolute;
    left: calc(36% + 12px);
    top: 58%;
    font-size: 11px;
    color: #9fe6c0;
  }

  /* idle stations */
  .f6-station-ring--idle {
    left: 66%;
    top: 64%;
    width: 90px;
    height: 90px;
    border: 1px dashed rgba(240, 181, 74, 0.4);
  }
  .f6-station-dot--idle {
    left: 66%;
    top: 64%;
    width: 7px;
    height: 7px;
    background: #f0b54a;
    box-shadow: 0 0 7px #f0b54a;
  }
  .f6-station-dot--unknown {
    left: 28%;
    top: 40%;
    width: 7px;
    height: 7px;
    background: #6f8693;
    box-shadow: 0 0 6px #6f8693;
  }

  .f6-vignette {
    position: absolute;
    inset: 0;
    pointer-events: none;
    box-shadow: inset 0 0 220px 60px rgba(0, 0, 0, 0.78);
  }

  /* ---- bottom drawer ---- */

  .f6-drawer {
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

  .f6-panel {
    background: linear-gradient(178deg, #16252f, #0a141b);
    border: 1px solid rgba(90, 150, 180, 0.22);
    box-shadow: inset 0 1px 0 rgba(150, 210, 240, 0.16);
    min-width: 0;
  }

  .f6-panel--left {
    flex: 1;
    height: 100%;
    display: flex;
    flex-direction: column;
    padding: 9px 10px;
  }
  .f6-link-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 7px;
  }
  .f6-link-title {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-size: 10px;
    letter-spacing: 0.2em;
    color: #5a7a8a;
  }
  .f6-link-tag {
    font-size: 9.5px;
    color: #9fe6c0;
  }
  .f6-link-body {
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: 8px;
    justify-content: center;
  }
  .f6-bar-label-row {
    display: flex;
    justify-content: space-between;
    font-size: 10px;
    color: #9fb3bc;
    margin-bottom: 3px;
  }
  .f6-bar-value {
    color: #eaf6f8;
  }
  .f6-bar-value--green {
    color: #5ad6a0;
  }
  .f6-bar-track {
    height: 9px;
    background: rgba(110, 170, 190, 0.1);
    border: 1px solid rgba(110, 170, 190, 0.2);
  }
  .f6-bar-fill {
    height: 100%;
  }
  .f6-bar-fill--green {
    background: linear-gradient(90deg, rgba(90, 214, 160, 0.5), #5ad6a0);
  }
  .f6-bar-fill--cyan {
    background: linear-gradient(90deg, rgba(53, 201, 216, 0.5), #35c9d8);
  }
  .f6-link-caption {
    font-size: 10px;
    color: #6f8693;
    line-height: 1.6;
  }

  .f6-panel--middle {
    flex: 3;
    height: calc(100% - 26px);
    display: flex;
    flex-direction: column;
    gap: 2px;
    background: #060d12;
  }
  .f6-schedule-strip {
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
  .f6-schedule-label {
    font-size: 9.5px;
    letter-spacing: 0.16em;
    color: #5a7a8a;
  }
  .f6-schedule-next {
    font-weight: 600;
    font-size: 17px;
    color: #eaf6f8;
  }
  .f6-schedule-track {
    flex: 1;
    height: 18px;
    position: relative;
    background: linear-gradient(178deg, #0b151c, #060d12);
    border: 1px solid rgba(90, 150, 180, 0.22);
  }
  .f6-schedule-window {
    position: absolute;
    top: 0;
    bottom: 0;
  }
  .f6-schedule-window--a {
    left: 8%;
    width: 16%;
    background: rgba(90, 214, 160, 0.35);
    border-left: 1px solid #5ad6a0;
    border-right: 1px solid #5ad6a0;
  }
  .f6-schedule-window--b {
    left: 38%;
    width: 12%;
    background: rgba(240, 181, 74, 0.3);
    border-left: 1px solid #f0b54a;
    border-right: 1px solid #f0b54a;
  }
  .f6-schedule-window--c {
    left: 70%;
    width: 18%;
    background: rgba(53, 201, 216, 0.3);
    border-left: 1px solid #35c9d8;
    border-right: 1px solid #35c9d8;
  }
  .f6-schedule-playhead {
    position: absolute;
    top: -3px;
    bottom: -3px;
    left: 16%;
    width: 2px;
    background: #ffb24d;
    box-shadow: 0 0 8px #ffb24d;
  }
  .f6-schedule-contention {
    font-size: 11px;
    color: #f0d49a;
  }

  .f6-detail {
    flex: 1;
    background: linear-gradient(178deg, #15232d, #0a141b);
    border: 1px solid rgba(90, 150, 180, 0.22);
    box-shadow: inset 0 1px 0 rgba(150, 210, 240, 0.14);
    min-width: 0;
    position: relative;
    overflow: hidden;
    display: flex;
  }
  .f6-detail-topline {
    position: absolute;
    top: 0;
    left: 0;
    right: 0;
    height: 1px;
    background: linear-gradient(90deg, transparent, #5ad6a0, transparent);
    opacity: 0.6;
  }

  /* pass list */
  .f6-pass-list {
    flex: 1;
    display: flex;
    flex-direction: column;
    min-width: 0;
    border-right: 1px solid rgba(110, 170, 190, 0.14);
  }
  .f6-pass-head,
  .f6-pass-row {
    display: grid;
    grid-template-columns: 14px 1.4fr 0.8fr 0.9fr 0.9fr 0.7fr;
    gap: 0 10px;
  }
  .f6-pass-head {
    flex: none;
    padding: 7px 14px 6px;
    border-bottom: 1px solid rgba(110, 170, 190, 0.16);
    font-size: 9.5px;
    letter-spacing: 0.1em;
    color: #5d7681;
  }
  .f6-pass-body {
    flex: 1;
    overflow-y: auto;
  }
  .f6-pass-row {
    padding: 6px 14px;
    align-items: center;
    border-bottom: 1px solid rgba(110, 170, 190, 0.07);
  }
  .f6-pass-dot {
    border-radius: 50%;
  }
  .f6-pass-station {
    font-size: 12.5px;
  }
  .f6-pass-band,
  .f6-pass-field {
    font-size: 11px;
  }

  /* ground-track 2D inset */
  .f6-track2d {
    flex: none;
    width: 220px;
    padding: 8px 10px;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .f6-track2d-title {
    font-size: 9.5px;
    letter-spacing: 0.14em;
    color: #5d7681;
  }
  .f6-track2d-canvas {
    flex: 1;
    position: relative;
    border: 1px solid rgba(110, 170, 190, 0.2);
    background: linear-gradient(180deg, #0a1822, #07111a);
    overflow: hidden;
  }
  .f6-track2d-hline {
    position: absolute;
    left: 0;
    right: 0;
    top: 50%;
    height: 1px;
    background: rgba(110, 170, 190, 0.15);
  }
  .f6-track2d-vline {
    position: absolute;
    top: 0;
    bottom: 0;
    left: 50%;
    width: 1px;
    background: rgba(110, 170, 190, 0.15);
  }
  .f6-track2d-orbit {
    position: absolute;
    left: 6%;
    top: 60%;
    width: 88%;
    height: 30%;
    border: 1px dashed rgba(53, 201, 216, 0.5);
    border-radius: 50%;
    transform: rotate(-8deg);
  }
  .f6-track2d-sat {
    position: absolute;
    left: 30%;
    top: 54%;
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: #eaf6f8;
    box-shadow: 0 0 7px #fff;
  }
  .f6-track2d-station {
    position: absolute;
    left: 24%;
    top: 70%;
    width: 5px;
    height: 5px;
    border-radius: 50%;
    background: #5ad6a0;
    box-shadow: 0 0 6px #5ad6a0;
  }

  .f6-panel--right {
    flex: 1;
    height: 100%;
    display: flex;
    flex-direction: column;
    padding: 9px 10px;
  }
  .f6-actions-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 7px;
  }
  .f6-actions-title {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-size: 10px;
    letter-spacing: 0.2em;
    color: #5a7a8a;
  }
  .f6-actions-tag {
    font-size: 9.5px;
    color: #9fe6c0;
  }
  .f6-actions-grid {
    flex: 1;
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    grid-template-rows: repeat(3, 1fr);
    gap: 5px;
  }
  .f6-action-cell {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 3px;
  }
  .f6-action-glyph {
    font-size: 21.5px;
  }
  .f6-action-label {
    font-size: 9.5px;
    color: #bcccd3;
    text-align: center;
  }
</style>

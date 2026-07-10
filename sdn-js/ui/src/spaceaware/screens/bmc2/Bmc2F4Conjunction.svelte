<script lang="ts">
  /**
   * Pixel port of BMC2_F4_Conjunction.dc.html (loop U2.2). Static RPO/
   * collision-screening board — per bmc2/README.md there is no logic class,
   * so the screening list rows/threat-card figures below are the ground
   * truth's own fixture numbers, not a live screening feed. RED semantic
   * accent per DESIGN_TOKENS (see lib/bmc2.ts BMC2_MODE_ACCENTS.f4).
   *
   * `oc-blink` (conjunction-point marker + top screening row dot) is new for
   * this board — first BMC2 board to use it; defined globally alongside the
   * existing oc-spin/oc-marq/oc-pulse in styles/spaceaware.css per the loop
   * task's porting note.
   */
  import Bmc2TopBar from './Bmc2TopBar.svelte';
  import { BMC2_KICKERS } from '../../lib/bmc2';

  let { navigate }: { navigate: (path: string) => void } = $props();

  const SCREENING_ROWS = [
    {
      dot: '#ff6b6b',
      blink: true,
      rowBg: 'rgba(255,107,107,0.1)',
      primary: 'ORB-10171',
      primaryColor: '#eaf6f8',
      secondary: 'DEB-44219',
      secondaryColor: '#ffd2d2',
      miss: '240 m',
      missColor: '#ff9d9d',
      pc: '1.4e-3',
      pcColor: '#ff9d9d',
      tca: '08:41',
      vrel: '11.8',
    },
    {
      dot: '#f0b54a',
      blink: false,
      rowBg: 'rgba(240,181,74,0.07)',
      primary: 'ORB-10240',
      primaryColor: '#eaf6f8',
      secondary: 'SL-16 R/B',
      secondaryColor: '#dbe7ec',
      miss: '610 m',
      missColor: '#f0d49a',
      pc: '3.1e-4',
      pcColor: '#f0d49a',
      tca: '02:14:30',
      vrel: '9.2',
    },
    {
      dot: '#f0b54a',
      blink: false,
      rowBg: 'transparent',
      primary: 'ORB-22008',
      primaryColor: '#eaf6f8',
      secondary: 'FENGYUN DEB',
      secondaryColor: '#dbe7ec',
      miss: '880 m',
      missColor: '#f0d49a',
      pc: '1.7e-4',
      pcColor: '#f0d49a',
      tca: '11:52:09',
      vrel: '14.1',
    },
  ];

  const SCREEN_CELL_STYLE = {
    border: 'rgba(120,190,230,0.45)',
    background: 'linear-gradient(180deg,rgba(74,166,224,0.12),rgba(0,0,0,0.25))',
  };
  const NEUTRAL_CELL_STYLE = {
    border: 'rgba(90,150,180,0.35)',
    background: 'linear-gradient(180deg,rgba(74,166,224,0.08),rgba(0,0,0,0.25))',
  };
  const AMBER_CELL_STYLE = {
    border: 'rgba(255,178,77,0.4)',
    background: 'linear-gradient(180deg,rgba(255,178,77,0.1),rgba(0,0,0,0.25))',
  };
  const RED_CELL_STYLE = {
    border: 'rgba(255,107,107,0.4)',
    background: 'linear-gradient(180deg,rgba(255,107,107,0.08),rgba(0,0,0,0.25))',
  };
  const GREEN_CELL_STYLE = {
    border: 'rgba(90,214,160,0.4)',
    background: 'linear-gradient(180deg,rgba(90,214,160,0.09),rgba(0,0,0,0.25))',
  };

  const COMMAND_CELLS = [
    { glyph: '◌', glyphColor: '#7fd4ff', label: 'SCREEN', style: SCREEN_CELL_STYLE },
    { glyph: '⚲', glyphColor: '#9fd4f5', label: 'PIN PAIR', style: NEUTRAL_CELL_STYLE },
    { glyph: '∿', glyphColor: '#ffd089', label: 'COMPUTE COLA', style: AMBER_CELL_STYLE },
    { glyph: '◹', glyphColor: '#ff8d8d', label: 'THREAT FAN', style: RED_CELL_STYLE },
    { glyph: '⬢', glyphColor: '#ff8d8d', label: 'KEEP-OUT', style: RED_CELL_STYLE },
    { glyph: '◐', glyphColor: '#9fd4f5', label: 'REPLAY', style: NEUTRAL_CELL_STYLE },
    { glyph: '⤓', glyphColor: '#9fd4f5', label: 'REPORT', style: NEUTRAL_CELL_STYLE },
    { glyph: '✓', glyphColor: '#9fe6c0', label: 'ACK', style: GREEN_CELL_STYLE },
    { glyph: '⚠', glyphColor: '#ff8d8d', label: 'WARN', style: RED_CELL_STYLE },
  ];
</script>

<div class="f4-root" data-screen-label="BMC2 F4 Conjunction">
  <div class="f4-globe">
    <div class="f4-globe-stars"></div>
    <div class="f4-sphere"></div>
    <div class="f4-orbit f4-orbit--primary"></div>
    <div class="f4-orbit f4-orbit--secondary"></div>
    <div class="f4-conj-ring"></div>
    <div class="f4-conj-dot f4-conj-dot--primary"></div>
    <div class="f4-conj-dot f4-conj-dot--secondary"></div>
    <div class="f4-conj-label">TCA T+00:08:41 · 240 m</div>
  </div>
  <div class="f4-vignette"></div>

  <Bmc2TopBar
    activeMode="f4"
    kicker={BMC2_KICKERS.f4}
    statusLabel="3 WARNINGS"
    statusTextColor="#ff8d8d"
    statusDotColor="#ff6b6b"
    statusPulseSeconds={1}
    {navigate}
  />

  <div class="f4-drawer">
    <div class="f4-panel f4-panel--left">
      <div class="f4-relmo-head">
        <span class="f4-relmo-title">RELATIVE MOTION · RIC</span>
        <span class="f4-relmo-tag">PAIR</span>
      </div>
      <div class="f4-relmo-canvas">
        <div class="f4-relmo-primary"></div>
        <div class="f4-relmo-cola"></div>
        <div class="f4-relmo-track"></div>
        <div class="f4-relmo-secondary"></div>
        <div class="f4-relmo-caption">MISS 240 m · Pc 1.4e-3<br />Vrel 11.8 km/s</div>
      </div>
    </div>

    <div class="f4-panel f4-panel--middle">
      <div class="f4-screening-strip">
        <span class="f4-screening-label">SCREENING</span>
        <span class="f4-screening-count">3 EVENTS &lt; 24H</span>
        <div class="f4-screening-spacer"></div>
        <span class="f4-screening-vol">SCREEN VOL 1.0 km · Pc &gt; 1e-4</span>
      </div>

      <div class="f4-list">
        <div class="f4-list-topline"></div>
        <div class="f4-list-head">
          <span></span><span>PRIMARY</span><span>SECONDARY</span><span>MISS</span><span>Pc</span><span>TCA</span
          ><span>Vrel</span>
        </div>
        <div class="f4-list-body">
          {#each SCREENING_ROWS as row (row.primary)}
            <div class="f4-list-row" style={`background:${row.rowBg};`}>
              <span
                class="f4-list-dot"
                class:f4-list-dot--blink={row.blink}
                style={`background:${row.dot};${row.blink ? `box-shadow:0 0 6px ${row.dot};` : ''}`}
              ></span>
              <span class="f4-list-primary" style={`color:${row.primaryColor};`}>{row.primary}</span>
              <span class="f4-list-secondary" style={`color:${row.secondaryColor};`}>{row.secondary}</span>
              <span class="f4-list-miss" style={`color:${row.missColor};`}>{row.miss}</span>
              <span class="f4-list-pc" style={`color:${row.pcColor};`}>{row.pc}</span>
              <span class="f4-list-tca">{row.tca}</span>
              <span class="f4-list-vrel">{row.vrel}</span>
            </div>
          {/each}
        </div>
      </div>
    </div>

    <div class="f4-panel f4-panel--right">
      <div class="f4-actions-head">
        <span class="f4-actions-title">THREAT ACTIONS</span>
        <span class="f4-actions-tag">F4</span>
      </div>
      <div class="f4-actions-grid">
        {#each COMMAND_CELLS as cell (cell.label)}
          <div class="f4-action-cell" style={`border:1px solid ${cell.style.border};background:${cell.style.background};`}>
            <span class="f4-action-glyph" style={`color:${cell.glyphColor};`}>{cell.glyph}</span>
            <span class="f4-action-label">{cell.label}</span>
          </div>
        {/each}
      </div>
    </div>
  </div>
</div>

<style>
  .f4-root {
    position: fixed;
    inset: 0;
    background: #04060a;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    color: #c7d6dd;
    overflow: hidden;
    -webkit-font-smoothing: antialiased;
  }

  /* ---- stylized CSS-only globe (no Cesium) ---- */

  .f4-globe {
    position: absolute;
    inset: 0;
    background: radial-gradient(circle at 50% 128%, #0a1a26, #04060a 62%);
    overflow: hidden;
  }

  .f4-globe-stars {
    position: absolute;
    inset: 0;
    background-image:
      radial-gradient(1px 1px at 14% 22%, rgba(255, 255, 255, 0.5), transparent),
      radial-gradient(1px 1px at 72% 31%, rgba(255, 255, 255, 0.35), transparent),
      radial-gradient(1px 1px at 41% 64%, rgba(255, 255, 255, 0.4), transparent),
      radial-gradient(1px 1px at 88% 71%, rgba(255, 255, 255, 0.3), transparent);
  }

  .f4-sphere {
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
      0 0 90px rgba(53, 201, 216, 0.14);
  }

  /* two converging orbits */
  .f4-orbit {
    position: absolute;
    left: 50%;
    top: 50%;
    width: 760px;
    height: 320px;
    border-radius: 50%;
  }
  .f4-orbit--primary {
    transform: translate(-50%, -50%) rotate(-20deg);
    border: 1px solid rgba(53, 201, 216, 0.45);
  }
  .f4-orbit--secondary {
    transform: translate(-50%, -50%) rotate(34deg);
    border: 1px solid rgba(255, 107, 107, 0.5);
  }

  /* conjunction point + screening ellipsoid */
  .f4-conj-ring {
    position: absolute;
    left: 62%;
    top: 33%;
    width: 64px;
    height: 64px;
    transform: translate(-50%, -50%);
    border: 1px solid rgba(255, 107, 107, 0.6);
    border-radius: 50%;
    background: radial-gradient(circle, rgba(255, 107, 107, 0.16), transparent 70%);
    box-shadow: 0 0 22px rgba(255, 107, 107, 0.4);
    animation: oc-blink 1.8s infinite;
  }
  .f4-conj-dot {
    position: absolute;
    width: 8px;
    height: 8px;
    border-radius: 50%;
  }
  .f4-conj-dot--primary {
    left: 60.5%;
    top: 35%;
    background: #35c9d8;
    box-shadow: 0 0 8px #35c9d8;
  }
  .f4-conj-dot--secondary {
    left: 63.5%;
    top: 31%;
    background: #ff6b6b;
    box-shadow: 0 0 8px #ff6b6b;
  }
  .f4-conj-label {
    position: absolute;
    left: 62%;
    top: 25%;
    font-size: 11px;
    color: #ff9d9d;
    letter-spacing: 0.06em;
  }

  .f4-vignette {
    position: absolute;
    inset: 0;
    pointer-events: none;
    box-shadow: inset 0 0 220px 60px rgba(0, 0, 0, 0.78);
  }

  /* ---- bottom drawer ---- */

  .f4-drawer {
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

  .f4-panel {
    background: linear-gradient(178deg, #16252f, #0a141b);
    border: 1px solid rgba(90, 150, 180, 0.22);
    box-shadow: inset 0 1px 0 rgba(150, 210, 240, 0.16);
    min-width: 0;
  }

  .f4-panel--left {
    flex: 1;
    height: 100%;
    display: flex;
    flex-direction: column;
    padding: 9px 10px;
  }
  .f4-relmo-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 7px;
  }
  .f4-relmo-title {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-size: 10px;
    letter-spacing: 0.2em;
    color: #5a7a8a;
  }
  .f4-relmo-tag {
    font-size: 9.5px;
    color: #ff9d9d;
  }
  .f4-relmo-canvas {
    flex: 1;
    position: relative;
    border: 1px solid rgba(90, 150, 180, 0.3);
    box-shadow: inset 0 0 34px rgba(0, 0, 0, 0.65);
    background:
      repeating-linear-gradient(0deg, rgba(74, 166, 224, 0.05) 0 1px, transparent 1px 20px),
      repeating-linear-gradient(90deg, rgba(74, 166, 224, 0.05) 0 1px, transparent 1px 20px);
    overflow: hidden;
  }
  /* primary at center */
  .f4-relmo-primary {
    position: absolute;
    left: 50%;
    top: 50%;
    width: 9px;
    height: 9px;
    transform: translate(-50%, -50%);
    border-radius: 50%;
    background: #35c9d8;
    box-shadow: 0 0 8px #35c9d8;
  }
  .f4-relmo-cola {
    position: absolute;
    left: 50%;
    top: 50%;
    width: 46px;
    height: 46px;
    transform: translate(-50%, -50%);
    border: 1px dashed rgba(255, 107, 107, 0.45);
    border-radius: 50%;
  }
  /* secondary relative trajectory */
  .f4-relmo-track {
    position: absolute;
    left: 14%;
    top: 18%;
    width: 78px;
    height: 2px;
    transform: rotate(34deg);
    background: linear-gradient(90deg, transparent, #ff6b6b);
    box-shadow: 0 0 6px rgba(255, 107, 107, 0.5);
  }
  .f4-relmo-secondary {
    position: absolute;
    left: 62%;
    top: 54%;
    width: 7px;
    height: 7px;
    transform: translate(-50%, -50%);
    border-radius: 50%;
    background: #ff6b6b;
    box-shadow: 0 0 8px #ff6b6b;
  }
  .f4-relmo-caption {
    position: absolute;
    left: 6px;
    bottom: 5px;
    font-size: 9px;
    color: #5a7a8a;
    line-height: 1.6;
  }

  .f4-panel--middle {
    flex: 3;
    height: calc(100% - 26px);
    display: flex;
    flex-direction: column;
    gap: 2px;
    background: #060d12;
  }
  .f4-screening-strip {
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
  .f4-screening-label {
    font-size: 9.5px;
    letter-spacing: 0.16em;
    color: #5a7a8a;
  }
  .f4-screening-count {
    font-weight: 600;
    font-size: 17px;
    color: #ff9d9d;
  }
  .f4-screening-spacer {
    flex: 1;
  }
  .f4-screening-vol {
    font-size: 11px;
    color: #6f8693;
  }

  .f4-list {
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
  .f4-list-topline {
    position: absolute;
    top: 0;
    left: 0;
    right: 0;
    height: 1px;
    background: linear-gradient(90deg, transparent, #ff6b6b, transparent);
    opacity: 0.6;
  }
  .f4-list-head,
  .f4-list-row {
    display: grid;
    grid-template-columns: 14px 1.3fr 1.3fr 0.8fr 0.7fr 0.9fr 0.8fr;
    gap: 0 12px;
  }
  .f4-list-head {
    padding: 7px 16px 6px;
    border-bottom: 1px solid rgba(110, 170, 190, 0.16);
    font-size: 9.5px;
    letter-spacing: 0.1em;
    color: #5d7681;
  }
  .f4-list-body {
    flex: 1;
    overflow-y: auto;
  }
  .f4-list-row {
    padding: 7px 16px;
    align-items: center;
    border-bottom: 1px solid rgba(110, 170, 190, 0.07);
  }
  .f4-list-dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
  }
  .f4-list-dot--blink {
    animation: oc-blink 1.5s infinite;
  }
  .f4-list-primary,
  .f4-list-secondary {
    font-size: 13px;
  }
  .f4-list-miss,
  .f4-list-pc {
    font-size: 12px;
  }
  .f4-list-tca {
    font-size: 12px;
    color: #eaf6f8;
  }
  .f4-list-vrel {
    font-size: 12px;
    color: #bcccd3;
  }

  .f4-panel--right {
    flex: 1;
    height: 100%;
    display: flex;
    flex-direction: column;
    padding: 9px 10px;
  }
  .f4-actions-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 7px;
  }
  .f4-actions-title {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-size: 10px;
    letter-spacing: 0.2em;
    color: #5a7a8a;
  }
  .f4-actions-tag {
    font-size: 9.5px;
    color: #ff9d9d;
  }
  .f4-actions-grid {
    flex: 1;
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    grid-template-rows: repeat(3, 1fr);
    gap: 5px;
  }
  .f4-action-cell {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 3px;
  }
  .f4-action-glyph {
    font-size: 21.5px;
  }
  .f4-action-label {
    font-size: 9.5px;
    color: #bcccd3;
    text-align: center;
  }
</style>

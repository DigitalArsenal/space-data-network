<script lang="ts">
  /**
   * Pixel port of BMC2_F2_Track.dc.html (loop U2.1). Static single-object
   * deep-dive board — per bmc2/README.md there is no logic class; only the
   * OVERVIEW subsystem tab has content in the ground truth (POWER/PROP/
   * SENSORS/COMMS are inactive labels with no populated panel), so they're
   * rendered as plain non-interactive tab labels here, matching the source.
   */
  import Bmc2TopBar from './Bmc2TopBar.svelte';
  import { BMC2_KICKERS } from '../../lib/bmc2';

  let { navigate }: { navigate: (path: string) => void } = $props();

  const REF_FRAMES = [
    { id: 'ric', label: 'RIC', active: true },
    { id: 'eci', label: 'ECI', active: false },
    { id: 'ecef', label: 'ECEF', active: false },
  ];

  const SUBSYSTEM_TABS = ['OVERVIEW', 'POWER', 'PROP', 'SENSORS', 'COMMS'];

  const ORBITAL_ELEMENTS = [
    { label: 'SEMI-MAJOR AXIS', value: '7,183 km' },
    { label: 'ALTITUDE', value: '812 km' },
    { label: 'INCLINATION', value: '98.21°' },
    { label: 'ECCENTRICITY', value: '0.0012' },
    { label: 'PERIOD', value: '101.3 min' },
    { label: 'ORBITAL VEL', value: '7.452 km/s' },
    { label: 'RAAN', value: '241.6°' },
    { label: 'MEAN MOTION', value: '14.21 rev/day' },
    { label: 'DRY MASS', value: '1,240 kg' },
  ];

  const IMAGING_CELL_STYLE = {
    border: 'rgba(90,150,180,0.35)',
    background: 'linear-gradient(180deg,rgba(74,166,224,0.09),rgba(0,0,0,0.25))',
  };
  const MANEUVER_CELL_STYLE = {
    border: 'rgba(255,178,77,0.4)',
    background: 'linear-gradient(180deg,rgba(255,178,77,0.1),rgba(0,0,0,0.25))',
  };
  const POINTING_CELL_STYLE = {
    border: 'rgba(90,214,160,0.4)',
    background: 'linear-gradient(180deg,rgba(90,214,160,0.09),rgba(0,0,0,0.25))',
  };
  const SAFE_CELL_STYLE = {
    border: 'rgba(255,107,107,0.4)',
    background: 'linear-gradient(180deg,rgba(255,107,107,0.08),rgba(0,0,0,0.25))',
  };

  const COMMAND_CELLS = [
    { glyph: '◎', glyphColor: '#7fd4ff', label: 'NTM IMG', labelColor: '#9fb6c0', style: IMAGING_CELL_STYLE },
    { glyph: '⊕', glyphColor: '#7fd4ff', label: 'EARTH OBS', labelColor: '#9fb6c0', style: IMAGING_CELL_STYLE },
    { glyph: '◐', glyphColor: '#7fd4ff', label: 'LUNAR CAL', labelColor: '#9fb6c0', style: IMAGING_CELL_STYLE },
    { glyph: '✶', glyphColor: '#7fd4ff', label: 'STELLAR', labelColor: '#9fb6c0', style: IMAGING_CELL_STYLE },
    { glyph: '⊚', glyphColor: '#7fd4ff', label: 'SAR SCAN', labelColor: '#9fb6c0', style: IMAGING_CELL_STYLE },
    { glyph: '▲', glyphColor: '#ffd089', label: 'PROGRADE', labelColor: '#e8c79a', style: MANEUVER_CELL_STYLE },
    { glyph: '▼', glyphColor: '#ffd089', label: 'RETRO', labelColor: '#e8c79a', style: MANEUVER_CELL_STYLE },
    { glyph: '▣', glyphColor: '#ffd089', label: 'STN KEEP', labelColor: '#e8c79a', style: MANEUVER_CELL_STYLE },
    { glyph: '⟳', glyphColor: '#ffd089', label: 'MOM DUMP', labelColor: '#e8c79a', style: MANEUVER_CELL_STYLE },
    { glyph: '⊘', glyphColor: '#ffd089', label: 'COLL AVD', labelColor: '#e8c79a', style: MANEUVER_CELL_STYLE },
    { glyph: '✷', glyphColor: '#9fe6c0', label: 'SUN PT', labelColor: '#9fb6c0', style: POINTING_CELL_STYLE },
    { glyph: '⤓', glyphColor: '#9fe6c0', label: 'NADIR', labelColor: '#9fb6c0', style: POINTING_CELL_STYLE },
    { glyph: '⊙', glyphColor: '#9fe6c0', label: 'INERTIAL', labelColor: '#9fb6c0', style: POINTING_CELL_STYLE },
    { glyph: '⇩', glyphColor: '#9fe6c0', label: 'DOWNLINK', labelColor: '#9fb6c0', style: POINTING_CELL_STYLE },
    { glyph: '⊗', glyphColor: '#ff8d8d', label: 'SAFE', labelColor: '#d9b3b3', style: SAFE_CELL_STYLE },
  ];
</script>

<div class="f2-root" data-screen-label="BMC2 F2 Track">
  <div class="f2-globe">
    <div class="f2-globe-stars"></div>
    <div class="f2-sphere"></div>
    <div class="f2-orbit-ring"></div>
    <div class="f2-object-dot"></div>
    <div class="f2-reticle">
      <div class="f2-reticle-circle"></div>
      <div class="f2-reticle-v"></div>
      <div class="f2-reticle-h"></div>
    </div>
    <div class="f2-object-label">ORB-10171 · TRACKING</div>
  </div>
  <div class="f2-vignette"></div>

  <Bmc2TopBar
    activeMode="f2"
    kicker={BMC2_KICKERS.f2}
    statusLabel="LINK NOMINAL"
    statusTextColor="#5ad6a0"
    statusDotColor="#5ad6a0"
    {navigate}
  />

  <div class="f2-drawer">
    <div class="f2-panel f2-panel--left">
      <div class="f2-frame-head">
        <span class="f2-frame-title">OBJECT FRAME</span>
        <div class="f2-frame-toggle">
          {#each REF_FRAMES as frame (frame.id)}
            <span class="f2-frame-chip" class:is-active={frame.active}>{frame.label}</span>
          {/each}
        </div>
      </div>
      <div class="f2-attitude-canvas">
        <div class="f2-attitude-model">
          <div class="f2-solar-panel"></div>
          <div class="f2-bus"></div>
          <div class="f2-axis-r"></div>
          <div class="f2-axis-r-label">+R</div>
          <div class="f2-axis-i"></div>
          <div class="f2-axis-i-label">+I</div>
          <div class="f2-origin-dot"></div>
        </div>
        <div class="f2-attitude-caption">ATT q [0.92, 0.00, 0.00, 0.39]<br />RATE 0.061 °/s</div>
        <div class="f2-attitude-frame-label">RIC · 3D</div>
      </div>
    </div>

    <div class="f2-panel f2-panel--middle">
      <div class="f2-time-strip">
        <span class="f2-play-btn" title="Playback">▶</span>
        <div class="f2-time-block">
          <span class="f2-time-value">02:14:08</span>
          <span class="f2-time-sub">2018-164D · UTC</span>
        </div>
        <div class="f2-scrub-track">
          <div class="f2-scrub-playhead"></div>
        </div>
        <span class="f2-speed">▶ 60×</span>
      </div>

      <div class="f2-detail">
        <div class="f2-detail-topline"></div>
        <div class="f2-detail-head">
          <span class="f2-detail-dot"></span>
          <div class="f2-detail-identity">
            <div class="f2-detail-title-row">
              <span class="f2-detail-designator">ORB-10171</span>
              <span class="f2-detail-status">OPERATIONAL</span>
            </div>
            <div class="f2-detail-sub">20513 · 2018-164D · PAYLOAD · LEO · FRIENDLY</div>
          </div>
          <div class="f2-detail-spacer"></div>
          <div class="f2-subsystem-tabs">
            {#each SUBSYSTEM_TABS as tab (tab)}
              <span class="f2-subsystem-tab" class:is-active={tab === 'OVERVIEW'}>{tab}</span>
            {/each}
          </div>
        </div>
        <div class="f2-elements">
          {#each ORBITAL_ELEMENTS as el (el.label)}
            <div class="f2-element-row">
              <span class="f2-element-label">{el.label}</span>
              <span class="f2-element-value">{el.value}</span>
            </div>
          {/each}
        </div>
      </div>
    </div>

    <div class="f2-panel f2-panel--right">
      <div class="f2-command-head">
        <span class="f2-command-title">COMMAND CARD</span>
        <span class="f2-command-status">▸ AWAITING TASKING</span>
      </div>
      <div class="f2-command-grid">
        {#each COMMAND_CELLS as cell (cell.label)}
          <div class="f2-command-cell" style={`border:1px solid ${cell.style.border};background:${cell.style.background};`}>
            <span class="f2-command-glyph" style={`color:${cell.glyphColor};`}>{cell.glyph}</span>
            <span class="f2-command-label" style={`color:${cell.labelColor};`}>{cell.label}</span>
          </div>
        {/each}
      </div>
    </div>
  </div>
</div>

<style>
  .f2-root {
    position: fixed;
    inset: 0;
    background: #04060a;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    color: #c7d6dd;
    overflow: hidden;
    -webkit-font-smoothing: antialiased;
  }

  /* ---- stylized CSS-only globe (no Cesium) ---- */

  .f2-globe {
    position: absolute;
    inset: 0;
    background: radial-gradient(circle at 50% 128%, #0a1a26, #04060a 62%);
    overflow: hidden;
  }

  .f2-globe-stars {
    position: absolute;
    inset: 0;
    background-image:
      radial-gradient(1px 1px at 14% 22%, rgba(255, 255, 255, 0.5), transparent),
      radial-gradient(1px 1px at 72% 31%, rgba(255, 255, 255, 0.35), transparent),
      radial-gradient(1px 1px at 41% 64%, rgba(255, 255, 255, 0.4), transparent),
      radial-gradient(1px 1px at 88% 71%, rgba(255, 255, 255, 0.3), transparent),
      radial-gradient(1px 1px at 61% 14%, rgba(255, 255, 255, 0.3), transparent);
  }

  .f2-sphere {
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

  .f2-orbit-ring {
    position: absolute;
    left: 50%;
    top: 50%;
    width: 780px;
    height: 330px;
    transform: translate(-50%, -50%) rotate(-22deg);
    border: 1px solid rgba(53, 201, 216, 0.55);
    border-radius: 50%;
    box-shadow: 0 0 16px rgba(53, 201, 216, 0.25);
  }

  .f2-object-dot {
    position: absolute;
    left: 67%;
    top: 36%;
    width: 10px;
    height: 10px;
    border-radius: 50%;
    background: #eaf6f8;
    box-shadow: 0 0 12px #fff;
  }

  .f2-reticle {
    position: absolute;
    left: 67%;
    top: 36%;
    width: 54px;
    height: 54px;
    transform: translate(-50%, -50%);
  }
  .f2-reticle-circle {
    position: absolute;
    inset: 0;
    border: 1px solid rgba(120, 190, 230, 0.6);
    border-radius: 50%;
  }
  .f2-reticle-v {
    position: absolute;
    left: 50%;
    top: -9px;
    bottom: -9px;
    width: 1px;
    background: rgba(120, 190, 230, 0.6);
  }
  .f2-reticle-h {
    position: absolute;
    top: 50%;
    left: -9px;
    right: -9px;
    height: 1px;
    background: rgba(120, 190, 230, 0.6);
  }

  .f2-object-label {
    position: absolute;
    left: calc(67% + 34px);
    top: 33%;
    font-size: 11px;
    letter-spacing: 0.08em;
    color: #9fd4f5;
  }

  .f2-vignette {
    position: absolute;
    inset: 0;
    pointer-events: none;
    box-shadow: inset 0 0 220px 60px rgba(0, 0, 0, 0.78);
  }

  /* ---- bottom drawer ---- */

  .f2-drawer {
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

  .f2-panel {
    background: linear-gradient(178deg, #16252f, #0a141b);
    border: 1px solid rgba(90, 150, 180, 0.22);
    box-shadow: inset 0 1px 0 rgba(150, 210, 240, 0.16);
    min-width: 0;
  }

  .f2-panel--left {
    flex: 1;
    height: 100%;
    display: flex;
    flex-direction: column;
    padding: 9px 10px;
  }
  .f2-frame-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 7px;
    gap: 6px;
  }
  .f2-frame-title {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-size: 10px;
    letter-spacing: 0.2em;
    color: #5a7a8a;
  }
  .f2-frame-toggle {
    display: flex;
    gap: 2px;
  }
  .f2-frame-chip {
    padding: 2px 6px;
    font-size: 9.5px;
    background: rgba(74, 166, 224, 0.04);
    border: 1px solid rgba(90, 150, 180, 0.22);
    color: #7390a0;
  }
  .f2-frame-chip.is-active {
    background: rgba(74, 166, 224, 0.22);
    border-color: rgba(120, 190, 230, 0.55);
    color: #9fd4f5;
  }

  .f2-attitude-canvas {
    flex: 1;
    position: relative;
    border: 1px solid rgba(90, 150, 180, 0.3);
    box-shadow: inset 0 0 34px rgba(0, 0, 0, 0.65);
    background:
      radial-gradient(ellipse at 50% 45%, rgba(74, 166, 224, 0.07), transparent 70%),
      repeating-linear-gradient(0deg, rgba(74, 166, 224, 0.05) 0 1px, transparent 1px 22px),
      repeating-linear-gradient(90deg, rgba(74, 166, 224, 0.05) 0 1px, transparent 1px 22px);
    overflow: hidden;
    display: flex;
    align-items: center;
    justify-content: center;
  }
  .f2-attitude-model {
    position: relative;
    width: 130px;
    height: 90px;
  }
  .f2-solar-panel {
    position: absolute;
    left: 50%;
    top: 50%;
    width: 96px;
    height: 18px;
    transform: translate(-50%, -50%);
    background: linear-gradient(90deg, #16314a, #1f4d6e, #16314a);
    border: 1px solid #2f6f96;
    background-image: repeating-linear-gradient(90deg, rgba(140, 200, 240, 0.25) 0 1px, transparent 1px 8px);
  }
  .f2-bus {
    position: absolute;
    left: 50%;
    top: 50%;
    width: 22px;
    height: 26px;
    transform: translate(-50%, -50%);
    background: linear-gradient(160deg, #caa765, #7c5e2e);
    border: 1px solid #e6c48f;
  }
  .f2-axis-r {
    position: absolute;
    left: 14px;
    bottom: 12px;
    width: 48px;
    height: 2px;
    background: #ff6b6b;
    box-shadow: 0 0 5px rgba(255, 107, 107, 0.6);
  }
  .f2-axis-r-label {
    position: absolute;
    left: 58px;
    bottom: 7px;
    font-size: 11px;
    color: #ff8d8d;
  }
  .f2-axis-i {
    position: absolute;
    left: 14px;
    bottom: 12px;
    width: 2px;
    height: 48px;
    background: #5ad6a0;
    box-shadow: 0 0 5px rgba(90, 214, 160, 0.6);
  }
  .f2-axis-i-label {
    position: absolute;
    left: 9px;
    top: 24px;
    font-size: 11px;
    color: #7fe6bb;
  }
  .f2-origin-dot {
    position: absolute;
    left: 12px;
    bottom: 10px;
    width: 5px;
    height: 5px;
    border-radius: 50%;
    background: #eaf6f8;
    box-shadow: 0 0 5px #fff;
  }
  .f2-attitude-caption {
    position: absolute;
    left: 6px;
    bottom: 5px;
    font-size: 9px;
    color: #5a7a8a;
    line-height: 1.5;
  }
  .f2-attitude-frame-label {
    position: absolute;
    right: 6px;
    top: 5px;
    font-size: 9px;
    color: #5a7a8a;
  }

  .f2-panel--middle {
    flex: 3;
    height: calc(100% - 26px);
    display: flex;
    flex-direction: column;
    gap: 2px;
    background: #060d12;
  }
  .f2-time-strip {
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
  .f2-play-btn {
    width: 34px;
    height: 34px;
    display: flex;
    align-items: center;
    justify-content: center;
    background: rgba(74, 166, 224, 0.2);
    border: 1px solid rgba(120, 190, 230, 0.55);
    color: #9fd4f5;
    font-size: 15.5px;
  }
  .f2-time-block {
    display: flex;
    flex-direction: column;
    line-height: 1.15;
  }
  .f2-time-value {
    font-weight: 600;
    font-size: 21.5px;
    font-variant-numeric: tabular-nums;
    color: #eaf6f8;
  }
  .f2-time-sub {
    font-size: 9.5px;
    color: #5a7a8a;
    letter-spacing: 0.14em;
  }
  .f2-scrub-track {
    flex: 1;
    height: 2px;
    background: rgba(120, 190, 230, 0.2);
    position: relative;
    margin: 0 8px;
  }
  .f2-scrub-playhead {
    position: absolute;
    left: 46%;
    top: -4px;
    bottom: -4px;
    width: 2px;
    background: #ffb24d;
    box-shadow: 0 0 8px #ffb24d;
  }
  .f2-speed {
    font-size: 13px;
    color: #9fd4f5;
  }

  .f2-detail {
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
  .f2-detail-topline {
    position: absolute;
    top: 0;
    left: 0;
    right: 0;
    height: 1px;
    background: linear-gradient(90deg, transparent, #4aa6e0, transparent);
    opacity: 0.6;
  }
  .f2-detail-head {
    flex: none;
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 7px 16px;
    border-bottom: 1px solid rgba(110, 170, 190, 0.16);
  }
  .f2-detail-dot {
    width: 10px;
    height: 10px;
    border-radius: 50%;
    background: #35c9d8;
    box-shadow: 0 0 8px #35c9d8;
    flex: none;
  }
  .f2-detail-identity {
    flex: none;
  }
  .f2-detail-title-row {
    display: flex;
    align-items: baseline;
    gap: 8px;
  }
  .f2-detail-designator {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 700;
    font-size: 20.5px;
    letter-spacing: 0.05em;
    color: #eaf6f8;
    line-height: 1;
  }
  .f2-detail-status {
    font-size: 10px;
    color: #5ad6a0;
  }
  .f2-detail-sub {
    font-size: 10px;
    color: #6f8693;
    margin-top: 3px;
  }
  .f2-detail-spacer {
    flex: 1;
  }
  .f2-subsystem-tabs {
    display: flex;
    gap: 0;
    flex: none;
  }
  .f2-subsystem-tab {
    border-bottom: 2px solid transparent;
    color: #6f8693;
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 600;
    font-size: 12px;
    letter-spacing: 0.08em;
    padding: 7px 8px 6px;
  }
  .f2-subsystem-tab.is-active {
    border-bottom-color: #4aa6e0;
    color: #eaf6f8;
    background: rgba(74, 166, 224, 0.12);
  }

  .f2-elements {
    flex: 1;
    min-height: 0;
    overflow: hidden;
    padding: 12px 18px;
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: 0 30px;
    align-content: center;
  }
  .f2-element-row {
    display: flex;
    justify-content: space-between;
    padding: 4px 0;
    border-bottom: 1px solid rgba(110, 170, 190, 0.08);
  }
  .f2-element-label {
    font-size: 13px;
    color: #6f8693;
  }
  .f2-element-value {
    font-size: 13px;
    color: #dbe7ec;
  }

  .f2-panel--right {
    flex: 1;
    height: 100%;
    display: flex;
    flex-direction: column;
    padding: 9px 10px;
  }
  .f2-command-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 7px;
  }
  .f2-command-title {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-size: 10px;
    letter-spacing: 0.2em;
    color: #5a7a8a;
  }
  .f2-command-status {
    font-size: 9.5px;
    color: #ffb24d;
  }
  .f2-command-grid {
    flex: 1;
    display: grid;
    grid-template-columns: repeat(5, 1fr);
    grid-template-rows: repeat(3, 1fr);
    gap: 5px;
  }
  .f2-command-cell {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 3px;
  }
  .f2-command-glyph {
    font-size: 21.5px;
  }
  .f2-command-label {
    font-size: 9.5px;
    text-align: center;
  }
</style>

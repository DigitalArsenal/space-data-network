<script lang="ts">
  /**
   * Pixel port of BMC2_F5_Maneuver.dc.html (loop U2.2). Static burn-planning
   * / COA-comparison board — per bmc2/README.md there is no logic class, so
   * the ΔV budget bars and COA cards below are the ground truth's own
   * fixture numbers, not a live planner. AMBER semantic accent per
   * DESIGN_TOKENS (see lib/bmc2.ts BMC2_MODE_ACCENTS.f5).
   *
   * The "PLAN ✓ → REVIEW → AUTHORIZE → EXECUTE" state strip is a
   * non-interactive status readout (plain spans, not links/buttons/nav), so
   * its "→" step separators are kept verbatim — the token hard rule bans
   * directional arrows on *actionable* button/link/nav labels, not
   * descriptive state indicators (same reasoning F3 documented for its
   * "TARGET ACCESS · ORB-10171 → 4 SENSORS" heading).
   */
  import Bmc2TopBar from './Bmc2TopBar.svelte';
  import { BMC2_KICKERS } from '../../lib/bmc2';

  let { navigate }: { navigate: (path: string) => void } = $props();

  const PLAN_STEPS = [
    { label: 'PLAN ✓', state: 'done' as const },
    { label: 'REVIEW', state: 'active' as const },
    { label: 'AUTHORIZE', state: 'pending' as const },
    { label: 'EXECUTE', state: 'pending' as const },
  ];

  const COA_CARDS = [
    {
      title: 'COA-1 PROGRADE',
      titleColor: '#eaf6f8',
      recommended: true,
      border: 'rgba(120,190,230,0.5)',
      background: 'rgba(74,166,224,0.08)',
      dv: '42 m/s',
      dvColor: '#dbe7ec',
      fuel: '7.1 kg',
      fuelColor: '#dbe7ec',
      texec: 'T+12:30',
      texecColor: '#dbe7ec',
      apogee: '+1,420 km',
    },
    {
      title: 'COA-2 BI-ELLIPTIC',
      titleColor: '#cde0e6',
      recommended: false,
      border: 'rgba(90,150,180,0.25)',
      background: 'transparent',
      dv: '38 m/s',
      dvColor: '#dbe7ec',
      fuel: '6.4 kg',
      fuelColor: '#dbe7ec',
      texec: 'T+02:40:00',
      texecColor: '#dbe7ec',
      apogee: '+1,420 km',
    },
    {
      title: 'COA-3 RADIAL',
      titleColor: '#cde0e6',
      recommended: false,
      border: 'rgba(90,150,180,0.25)',
      background: 'transparent',
      dv: '61 m/s',
      dvColor: '#f0d49a',
      fuel: '10.3 kg',
      fuelColor: '#f0d49a',
      texec: 'T+12:30',
      texecColor: '#dbe7ec',
      apogee: '+1,180 km',
    },
  ];

  const AMBER_CELL_STYLE = { border: 'rgba(255,178,77,0.45)', background: 'linear-gradient(180deg,rgba(255,178,77,0.12),rgba(0,0,0,0.25))' };
  const AMBER_CELL_STYLE_DIM = { border: 'rgba(255,178,77,0.4)', background: 'linear-gradient(180deg,rgba(255,178,77,0.1),rgba(0,0,0,0.25))' };
  const SCREEN_CELL_STYLE = { border: 'rgba(120,190,230,0.45)', background: 'linear-gradient(180deg,rgba(74,166,224,0.12),rgba(0,0,0,0.25))' };
  const NEUTRAL_CELL_STYLE = { border: 'rgba(90,150,180,0.35)', background: 'linear-gradient(180deg,rgba(74,166,224,0.08),rgba(0,0,0,0.25))' };
  const GREEN_CELL_STYLE = { border: 'rgba(90,214,160,0.45)', background: 'linear-gradient(180deg,rgba(90,214,160,0.12),rgba(0,0,0,0.25))' };

  const COMMAND_CELLS = [
    { glyph: '▲', glyphColor: '#ffd089', label: 'PROGRADE', labelColor: '#e8c79a', style: AMBER_CELL_STYLE },
    { glyph: '▼', glyphColor: '#ffd089', label: 'RETRO', labelColor: '#e8c79a', style: AMBER_CELL_STYLE_DIM },
    { glyph: '◀', glyphColor: '#ffd089', label: 'RADIAL', labelColor: '#e8c79a', style: AMBER_CELL_STYLE_DIM },
    { glyph: '⬆', glyphColor: '#ffd089', label: 'NORMAL', labelColor: '#e8c79a', style: AMBER_CELL_STYLE_DIM },
    { glyph: '▣', glyphColor: '#ffd089', label: 'STN KEEP', labelColor: '#e8c79a', style: AMBER_CELL_STYLE_DIM },
    { glyph: '∿', glyphColor: '#ffd089', label: 'PHASING', labelColor: '#e8c79a', style: AMBER_CELL_STYLE_DIM },
    { glyph: '◌', glyphColor: '#7fd4ff', label: 'PREVIEW', labelColor: '#bcccd3', style: SCREEN_CELL_STYLE },
    { glyph: '⎙', glyphColor: '#9fd4f5', label: 'AUTHORIZE', labelColor: '#bcccd3', style: NEUTRAL_CELL_STYLE },
    { glyph: '▶', glyphColor: '#9fe6c0', label: 'EXECUTE', labelColor: '#bcccd3', style: GREEN_CELL_STYLE },
  ];
</script>

<div class="f5-root" data-screen-label="BMC2 F5 Maneuver">
  <div class="f5-globe">
    <div class="f5-globe-stars"></div>
    <div class="f5-sphere"></div>
    <div class="f5-orbit-current"></div>
    <div class="f5-orbit-target"></div>
    <div class="f5-burn-dot"></div>
    <div class="f5-burn-vector"></div>
    <div class="f5-burn-label">ΔV 42 m/s · PROGRADE</div>
    <div class="f5-preview-label">PREVIEW: APOGEE +1,420 km</div>
  </div>
  <div class="f5-vignette"></div>

  <Bmc2TopBar
    activeMode="f5"
    kicker={BMC2_KICKERS.f5}
    statusLabel="PLAN PENDING"
    statusTextColor="#ffd089"
    statusDotColor="#ffb24d"
    statusPulseSeconds={1.6}
    {navigate}
  />

  <div class="f5-drawer">
    <div class="f5-panel f5-panel--left">
      <div class="f5-burn-head">
        <span class="f5-burn-title">BURN · ΔV BUDGET</span>
        <span class="f5-burn-tag">BIPROP</span>
      </div>
      <div class="f5-burn-body">
        <div class="f5-burn-bars">
          <div class="f5-bar-row">
            <div class="f5-bar-label-row"><span>ΔV REMAINING</span><span class="f5-bar-value">187 m/s</span></div>
            <div class="f5-bar-track"><div class="f5-bar-fill f5-bar-fill--green" style="width:74%;"></div></div>
          </div>
          <div class="f5-bar-row">
            <div class="f5-bar-label-row"><span>THIS BURN</span><span class="f5-bar-value f5-bar-value--amber">42 m/s</span></div>
            <div class="f5-bar-track"><div class="f5-bar-fill f5-bar-fill--amber" style="width:22%;"></div></div>
          </div>
          <div class="f5-burn-caption">ATTITUDE +X RAM · PROGRADE<br />BURN 28.4 s · 490 N<br />FUEL −7.1 kg</div>
        </div>
      </div>
    </div>

    <div class="f5-panel f5-panel--middle">
      <div class="f5-coa-strip">
        <span class="f5-coa-label">COURSES OF ACTION</span>
        <span class="f5-coa-count">ORB-10171 · 3 COA</span>
        <div class="f5-coa-spacer"></div>
        <div class="f5-plan-steps">
          {#each PLAN_STEPS as step, i (step.label)}
            <span class="f5-plan-step" class:f5-plan-step--done={step.state === 'done'} class:f5-plan-step--active={step.state === 'active'}>{step.label}</span>
            {#if i < PLAN_STEPS.length - 1}
              <span class="f5-plan-sep">→</span>
            {/if}
          {/each}
        </div>
      </div>

      <div class="f5-coa-panel">
        <div class="f5-coa-topline"></div>
        {#each COA_CARDS as coa (coa.title)}
          <div class="f5-coa-card" style={`border:1px solid ${coa.border};background:${coa.background};`}>
            <div class="f5-coa-card-head">
              <span class="f5-coa-card-title" style={`color:${coa.titleColor};`}>{coa.title}</span>
              {#if coa.recommended}
                <span class="f5-coa-recommended">RECOMMENDED</span>
              {/if}
            </div>
            <div class="f5-coa-row"><span class="f5-coa-row-label">ΔV</span><span style={`color:${coa.dvColor};`}>{coa.dv}</span></div>
            <div class="f5-coa-row"><span class="f5-coa-row-label">FUEL</span><span style={`color:${coa.fuelColor};`}>{coa.fuel}</span></div>
            <div class="f5-coa-row"><span class="f5-coa-row-label">T-EXEC</span><span style={`color:${coa.texecColor};`}>{coa.texec}</span></div>
            <div class="f5-coa-row"><span class="f5-coa-row-label">Δ APOGEE</span><span class="f5-coa-apogee">{coa.apogee}</span></div>
          </div>
        {/each}
      </div>
    </div>

    <div class="f5-panel f5-panel--right">
      <div class="f5-actions-head">
        <span class="f5-actions-title">MANEUVER ACTIONS</span>
        <span class="f5-actions-tag">F5</span>
      </div>
      <div class="f5-actions-grid">
        {#each COMMAND_CELLS as cell (cell.label)}
          <div class="f5-action-cell" style={`border:1px solid ${cell.style.border};background:${cell.style.background};`}>
            <span class="f5-action-glyph" style={`color:${cell.glyphColor};`}>{cell.glyph}</span>
            <span class="f5-action-label" style={`color:${cell.labelColor};`}>{cell.label}</span>
          </div>
        {/each}
      </div>
    </div>
  </div>
</div>

<style>
  .f5-root {
    position: fixed;
    inset: 0;
    background: #04060a;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    color: #c7d6dd;
    overflow: hidden;
    -webkit-font-smoothing: antialiased;
  }

  /* ---- stylized CSS-only globe (no Cesium) ---- */

  .f5-globe {
    position: absolute;
    inset: 0;
    background: radial-gradient(circle at 50% 128%, #0a1a26, #04060a 62%);
    overflow: hidden;
  }

  .f5-globe-stars {
    position: absolute;
    inset: 0;
    background-image:
      radial-gradient(1px 1px at 14% 22%, rgba(255, 255, 255, 0.5), transparent),
      radial-gradient(1px 1px at 72% 31%, rgba(255, 255, 255, 0.35), transparent),
      radial-gradient(1px 1px at 41% 64%, rgba(255, 255, 255, 0.4), transparent),
      radial-gradient(1px 1px at 88% 71%, rgba(255, 255, 255, 0.3), transparent);
  }

  .f5-sphere {
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

  /* current orbit */
  .f5-orbit-current {
    position: absolute;
    left: 50%;
    top: 50%;
    width: 720px;
    height: 300px;
    transform: translate(-50%, -50%) rotate(-18deg);
    border: 1px solid rgba(53, 201, 216, 0.5);
    border-radius: 50%;
  }

  /* target/after orbit (ghost dashed, larger) */
  .f5-orbit-target {
    position: absolute;
    left: 50%;
    top: 50%;
    width: 860px;
    height: 360px;
    transform: translate(-50%, -50%) rotate(-18deg);
    border: 1px dashed rgba(255, 178, 77, 0.6);
    border-radius: 50%;
    box-shadow: 0 0 16px rgba(255, 178, 77, 0.2);
  }

  /* burn point + thrust vector */
  .f5-burn-dot {
    position: absolute;
    left: 79%;
    top: 42%;
    width: 9px;
    height: 9px;
    transform: translate(-50%, -50%);
    border-radius: 50%;
    background: #eaf6f8;
    box-shadow: 0 0 10px #fff;
  }
  .f5-burn-vector {
    position: absolute;
    left: 79%;
    top: 42%;
    width: 54px;
    height: 2px;
    transform: translate(0, -50%) rotate(-12deg);
    background: linear-gradient(90deg, #ffd089, #ffb24d);
    box-shadow: 0 0 8px rgba(255, 178, 77, 0.8);
  }
  .f5-burn-label {
    position: absolute;
    left: calc(79% + 54px);
    top: 39%;
    font-size: 11px;
    color: #ffd089;
  }
  .f5-preview-label {
    position: absolute;
    left: 50%;
    top: 18%;
    font-size: 11px;
    color: #7fdce8;
    letter-spacing: 0.06em;
    transform: translateX(-50%);
  }

  .f5-vignette {
    position: absolute;
    inset: 0;
    pointer-events: none;
    box-shadow: inset 0 0 220px 60px rgba(0, 0, 0, 0.78);
  }

  /* ---- bottom drawer ---- */

  .f5-drawer {
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

  .f5-panel {
    background: linear-gradient(178deg, #16252f, #0a141b);
    border: 1px solid rgba(90, 150, 180, 0.22);
    box-shadow: inset 0 1px 0 rgba(150, 210, 240, 0.16);
    min-width: 0;
  }

  .f5-panel--left {
    flex: 1;
    height: 100%;
    display: flex;
    flex-direction: column;
    padding: 9px 10px;
  }
  .f5-burn-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 7px;
  }
  .f5-burn-title {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-size: 10px;
    letter-spacing: 0.2em;
    color: #5a7a8a;
  }
  .f5-burn-tag {
    font-size: 9.5px;
    color: #ffd089;
  }
  .f5-burn-body {
    flex: 1;
    display: flex;
    align-items: center;
    gap: 14px;
  }
  .f5-burn-bars {
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .f5-bar-label-row {
    display: flex;
    justify-content: space-between;
    font-size: 10px;
    color: #9fb3bc;
    margin-bottom: 3px;
  }
  .f5-bar-value {
    color: #eaf6f8;
  }
  .f5-bar-value--amber {
    color: #ffd089;
  }
  .f5-bar-track {
    height: 9px;
    background: rgba(110, 170, 190, 0.1);
    border: 1px solid rgba(110, 170, 190, 0.2);
  }
  .f5-bar-fill {
    height: 100%;
  }
  .f5-bar-fill--green {
    background: linear-gradient(90deg, rgba(90, 214, 160, 0.5), #5ad6a0);
  }
  .f5-bar-fill--amber {
    background: linear-gradient(90deg, rgba(255, 178, 77, 0.5), #ffb24d);
  }
  .f5-burn-caption {
    font-size: 10px;
    color: #6f8693;
    line-height: 1.6;
  }

  .f5-panel--middle {
    flex: 3;
    height: calc(100% - 26px);
    display: flex;
    flex-direction: column;
    gap: 2px;
    background: #060d12;
  }
  .f5-coa-strip {
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
  .f5-coa-label {
    font-size: 9.5px;
    letter-spacing: 0.16em;
    color: #5a7a8a;
  }
  .f5-coa-count {
    font-weight: 600;
    font-size: 17px;
    color: #eaf6f8;
  }
  .f5-coa-spacer {
    flex: 1;
  }
  .f5-plan-steps {
    display: flex;
    align-items: center;
    gap: 0;
    font-size: 9.5px;
    letter-spacing: 0.08em;
  }
  .f5-plan-step {
    padding: 4px 9px;
    border: 1px solid rgba(90, 150, 180, 0.25);
    color: #6f8693;
  }
  .f5-plan-step--done {
    background: rgba(90, 214, 160, 0.16);
    border-color: rgba(90, 214, 160, 0.4);
    color: #9fe6c0;
  }
  .f5-plan-step--active {
    background: rgba(74, 166, 224, 0.16);
    border-color: rgba(120, 190, 230, 0.5);
    color: #9fd4f5;
  }
  .f5-plan-sep {
    color: #5a7a8a;
    padding: 0 4px;
  }

  .f5-coa-panel {
    flex: 1;
    background: linear-gradient(178deg, #15232d, #0a141b);
    border: 1px solid rgba(90, 150, 180, 0.22);
    box-shadow: inset 0 1px 0 rgba(150, 210, 240, 0.14);
    min-width: 0;
    position: relative;
    overflow: hidden;
    padding: 10px 14px;
    display: flex;
    gap: 10px;
    align-items: stretch;
  }
  .f5-coa-topline {
    position: absolute;
    top: 0;
    left: 0;
    right: 0;
    height: 1px;
    background: linear-gradient(90deg, transparent, #ffb24d, transparent);
    opacity: 0.5;
  }
  .f5-coa-card {
    flex: 1;
    padding: 9px 11px;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .f5-coa-card-head {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
  }
  .f5-coa-card-title {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 600;
    font-size: 14.5px;
  }
  .f5-coa-recommended {
    font-size: 9.5px;
    color: #9fd4f5;
  }
  .f5-coa-row {
    display: flex;
    justify-content: space-between;
    font-size: 12px;
  }
  .f5-coa-row-label {
    color: #6f8693;
  }
  .f5-coa-apogee {
    color: #5ad6a0;
  }

  .f5-panel--right {
    flex: 1;
    height: 100%;
    display: flex;
    flex-direction: column;
    padding: 9px 10px;
  }
  .f5-actions-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 7px;
  }
  .f5-actions-title {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-size: 10px;
    letter-spacing: 0.2em;
    color: #5a7a8a;
  }
  .f5-actions-tag {
    font-size: 9.5px;
    color: #ffd089;
  }
  .f5-actions-grid {
    flex: 1;
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    grid-template-rows: repeat(3, 1fr);
    gap: 5px;
  }
  .f5-action-cell {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 3px;
  }
  .f5-action-glyph {
    font-size: 21.5px;
  }
  .f5-action-label {
    font-size: 9.5px;
    text-align: center;
  }
</style>

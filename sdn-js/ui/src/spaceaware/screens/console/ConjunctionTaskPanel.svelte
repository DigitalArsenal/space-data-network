<script lang="ts">
  /**
   * "CONJUNCTION TASK · CONFIGURE" panel (loop task U3.9) — the red-accent
   * board at the top of the CONJUNCTION console view. Ground truth:
   * `SDN Console.dc.html:592-694` — a 🔒 "MANEUVER INTENT NEVER LEAVES THIS
   * NODE" chip, a SCREEN TARGET pill row + "3D" link (gated by `show3dLink`,
   * default true — the standalone conjunction ship passes false since /orbital
   * is descoped there, C3) + status strip, three
   * numbered columns (① DATA SOURCES precedence stack / ② PROPAGATOR radio
   * cards / ③ SCREENING CRITERIA steppers) separated by `→` glyphs, a LIVE
   * STREAM STATUS card + ONE-OFF RUN button, and a corner one-off backfill
   * popover. Split out of `ConjunctionView.svelte` per the loop task's own
   * "split subcomponents if huge" allowance — this is the single largest
   * piece of the CONJUNCTION board.
   *
   * All data wiring/view-model building lives in `../../lib/conjunction-data.ts`
   * — see that file's doc comment for the D4 honesty split (SCREEN TARGET +
   * ① DATA SOURCES are real; ② PROPAGATOR/③ CRITERIA are honest unused
   * client-side state; the LIVE card is fabricated demo data, DEMO-tagged).
   * This component only renders view-model strings and forwards user
   * actions to the parent's pure-function-backed handlers — it holds no
   * state of its own.
   */
  import {
    CONJUNCTION_LIVE_DEMO_TAG_TITLE,
    CONJUNCTION_ONE_OFF_DEMO_TAG_TITLE,
    CONJUNCTION_SOURCES_PRECEDENCE_FOOTNOTE,
    formatMissDistanceLabel,
    formatPcThresholdLabel,
    type ConjunctionCriteria,
    type ConjunctionLiveCardView,
    type ConjunctionPropagatorKey,
    type ConjunctionPropagatorRowView,
    type ConjunctionSourceRowView,
    type ConjunctionTargetPillView,
    type ConjunctionTargetStripView,
  } from '../../lib/conjunction-data';

  let {
    targetPills,
    selectedGroupId,
    onSelectGroup,
    targetStrip,
    show3dLink = true,
    onOpen3d,
    sourceRows,
    onMoveSource,
    onToggleSource,
    propagatorRows,
    onSelectPropagator,
    criteria,
    onMissDown,
    onMissUp,
    onWindowDown,
    onWindowUp,
    onHbrDown,
    onHbrUp,
    onStepDown,
    onStepUp,
    onCyclePc,
    liveCard,
    onToggleLive,
    oneOffOpen,
    oneOffWindow,
    oneOffMessage,
    onToggleOneOff,
    onOneOffDown,
    onOneOffUp,
    onRunOneOff,
  }: {
    targetPills: ConjunctionTargetPillView[];
    selectedGroupId: string | null;
    onSelectGroup: (id: string) => void;
    targetStrip: ConjunctionTargetStripView | null;
    show3dLink?: boolean;
    onOpen3d: () => void;
    sourceRows: ConjunctionSourceRowView[];
    onMoveSource: (id: string, direction: -1 | 1) => void;
    onToggleSource: (id: string) => void;
    propagatorRows: ConjunctionPropagatorRowView[];
    onSelectPropagator: (key: ConjunctionPropagatorKey) => void;
    criteria: ConjunctionCriteria;
    onMissDown: () => void;
    onMissUp: () => void;
    onWindowDown: () => void;
    onWindowUp: () => void;
    onHbrDown: () => void;
    onHbrUp: () => void;
    onStepDown: () => void;
    onStepUp: () => void;
    onCyclePc: () => void;
    liveCard: ConjunctionLiveCardView;
    onToggleLive: () => void;
    oneOffOpen: boolean;
    oneOffWindow: number;
    oneOffMessage: string;
    onToggleOneOff: () => void;
    onOneOffDown: () => void;
    onOneOffUp: () => void;
    onRunOneOff: () => void;
  } = $props();
</script>

<section class="sdn-conj-task">
  <span class="sdn-conj-corner sdn-conj-corner--tl"></span>
  <span class="sdn-conj-corner sdn-conj-corner--tr"></span>
  <span class="sdn-conj-corner sdn-conj-corner--bl"></span>
  <span class="sdn-conj-corner sdn-conj-corner--br"></span>

  <div class="sdn-conj-task-header">
    <span class="sdn-conj-task-title">CONJUNCTION TASK · CONFIGURE</span>
    <span class="sdn-conj-lock-chip">🔒 MANEUVER INTENT NEVER LEAVES THIS NODE</span>
  </div>
  <div class="sdn-conj-task-desc">
    Stack data sources by precedence, choose one propagator, set screening criteria. Screening runs continuously as
    encrypted ephemeris arrives — no manual run needed.
  </div>

  <div class="sdn-conj-target-row">
    <span class="sdn-conj-target-kicker">SCREEN TARGET</span>
    <div class="sdn-conj-target-pills">
      {#each targetPills as pill (pill.id)}
        <button
          type="button"
          class="sdn-conj-target-pill"
          style={`background:${pill.bg};border-color:${pill.border};color:${pill.color};`}
          title={`Screen ${pill.name} for conjunctions`}
          aria-pressed={pill.id === selectedGroupId}
          onclick={() => onSelectGroup(pill.id)}
        >
          <span style={`color:${pill.glyphColor};`}>{pill.glyph}</span>{pill.name}
          <span class="sdn-conj-target-pill-dot" style={`background:${pill.conjColorDot};`}></span>
        </button>
      {/each}
      {#if targetPills.length === 0}
        <span class="sdn-conj-target-empty">NO GROUPS AVAILABLE — create one in GROUPS first</span>
      {/if}
    </div>
    {#if show3dLink}
      <button type="button" class="sdn-conj-3d-btn" disabled={!targetStrip} title="Open this group in the 3D Orbital Console" onclick={onOpen3d}>
        3D
      </button>
    {/if}
  </div>

  {#if targetStrip}
    <div class="sdn-conj-status-strip">
      Screening <span style={`color:${targetStrip.glyphColor};`}>{targetStrip.glyph}</span>
      <span class="sdn-conj-status-name">{targetStrip.name}</span> · {targetStrip.countLabel} ·
      <span style={`color:${targetStrip.kindColor};`}>{targetStrip.kindLabel}</span> defined by {targetStrip.ownerName}
    </div>
  {/if}

  <div class="sdn-conj-columns">
    <div class="sdn-conj-col sdn-conj-col--sources">
      <div class="sdn-conj-col-header">
        <span class="sdn-conj-col-title">① DATA SOURCES</span>
        <span class="sdn-conj-col-sub">PRECEDENCE · STACK</span>
      </div>
      <div class="sdn-conj-source-rows">
        {#each sourceRows as src (src.id)}
          <div class="sdn-conj-source-row" style={`opacity:${src.rowOpacity};`}>
            <span class="sdn-conj-source-prec">{src.precedence}</span>
            <div class="sdn-conj-source-arrows">
              <button
                type="button"
                class="sdn-conj-source-arrow"
                disabled={!src.canMoveUp}
                title="Raise precedence"
                onclick={() => onMoveSource(src.id, -1)}
              >
                ▲
              </button>
              <button
                type="button"
                class="sdn-conj-source-arrow"
                disabled={!src.canMoveDown}
                title="Lower precedence"
                onclick={() => onMoveSource(src.id, 1)}
              >
                ▼
              </button>
            </div>
            <span class="sdn-conj-source-enc" style={`color:${src.tagColorEffective};`}>{src.enc}</span>
            <span class="sdn-conj-source-name" style={`color:${src.nameColor};`} title={src.detail}>{src.name}</span>
            <span class="sdn-conj-source-type">{src.type}</span>
            <span class="sdn-conj-source-tag" style={`color:${src.tagColorEffective};`}>{src.tag}</span>
            <button
              type="button"
              class="sdn-conj-source-toggle"
              style={`background:${src.toggleBg};border-color:${src.toggleBorder};`}
              title="Enable / disable this source"
              aria-pressed={!src.off}
              onclick={() => onToggleSource(src.id)}
            >
              <span class="sdn-conj-source-toggle-knob" style={`left:${src.toggleKnob};background:${src.toggleKnobBg};`}></span>
            </button>
          </div>
        {/each}
      </div>
      <div class="sdn-conj-col-footnote">{CONJUNCTION_SOURCES_PRECEDENCE_FOOTNOTE}</div>
    </div>

    <span class="sdn-conj-col-arrow">→</span>

    <div class="sdn-conj-col sdn-conj-col--propagator">
      <div class="sdn-conj-col-header">
        <span class="sdn-conj-col-title">② PROPAGATOR</span>
        <span class="sdn-conj-col-sub">ONE · PER RUN</span>
      </div>
      <div class="sdn-conj-prop-rows" role="radiogroup" aria-label="Propagator">
        {#each propagatorRows as pr (pr.key)}
          <button
            type="button"
            class="sdn-conj-prop-row"
            role="radio"
            aria-checked={pr.selected}
            disabled={pr.disabled}
            style={`background:${pr.rowBg};border-color:${pr.rowBorder};`}
            title={pr.tooltip}
            onclick={() => onSelectPropagator(pr.key)}
          >
            <span class="sdn-conj-prop-radio" style={`border-color:${pr.radioBorder};`}>
              <span class="sdn-conj-prop-radio-dot" style={`background:${pr.radioDot};`}></span>
            </span>
            <div class="sdn-conj-prop-text">
              <div class="sdn-conj-prop-name" style={`color:${pr.nameColor};`}>
                {pr.name}
                {#if pr.paid}<span class="sdn-conj-prop-paid">PAID</span>{/if}
              </div>
              <div class="sdn-conj-prop-detail">{pr.detail}</div>
            </div>
            <span class="sdn-conj-prop-state" style={`color:${pr.stateColor};`}>{pr.stateLabel}</span>
          </button>
        {/each}
      </div>
      <div class="sdn-conj-col-footnote">
        One propagator per run. Ephemeris sources are used as-is; catalog sources are propagated with it.
      </div>
    </div>

    <span class="sdn-conj-col-arrow">→</span>

    <div class="sdn-conj-col sdn-conj-col--criteria">
      <div class="sdn-conj-col-title">③ SCREENING CRITERIA</div>
      <div class="sdn-conj-crit-rows">
        <div class="sdn-conj-crit-row">
          <span class="sdn-conj-crit-label">MISS DISTANCE ≤</span>
          <button type="button" class="sdn-conj-crit-btn" title="Decrease miss-distance threshold" onclick={onMissDown}>−</button>
          <span class="sdn-conj-crit-value">{formatMissDistanceLabel(criteria)}<span class="sdn-conj-crit-unit"> km</span></span>
          <button type="button" class="sdn-conj-crit-btn" title="Increase miss-distance threshold" onclick={onMissUp}>+</button>
        </div>
        <div class="sdn-conj-crit-row">
          <span class="sdn-conj-crit-label">Pc THRESHOLD ≥</span>
          <button type="button" class="sdn-conj-crit-cycle" title="Cycle the Pc threshold" onclick={onCyclePc}>
            {formatPcThresholdLabel(criteria)}
          </button>
        </div>
        <div class="sdn-conj-crit-row">
          <span class="sdn-conj-crit-label">SCREEN WINDOW</span>
          <button type="button" class="sdn-conj-crit-btn" title="Decrease screen window" onclick={onWindowDown}>−</button>
          <span class="sdn-conj-crit-value">{criteria.window}<span class="sdn-conj-crit-unit"> h</span></span>
          <button type="button" class="sdn-conj-crit-btn" title="Increase screen window" onclick={onWindowUp}>+</button>
        </div>
        <div class="sdn-conj-crit-row">
          <span class="sdn-conj-crit-label">HARD-BODY RADIUS</span>
          <button type="button" class="sdn-conj-crit-btn" title="Decrease hard-body radius" onclick={onHbrDown}>−</button>
          <span class="sdn-conj-crit-value">{criteria.hbr}<span class="sdn-conj-crit-unit"> m</span></span>
          <button type="button" class="sdn-conj-crit-btn" title="Increase hard-body radius" onclick={onHbrUp}>+</button>
        </div>
        <div class="sdn-conj-crit-row">
          <span class="sdn-conj-crit-label">STEP SIZE</span>
          <button type="button" class="sdn-conj-crit-btn" title="Decrease step size" onclick={onStepDown}>−</button>
          <span class="sdn-conj-crit-value">{criteria.step}<span class="sdn-conj-crit-unit"> s</span></span>
          <button type="button" class="sdn-conj-crit-btn" title="Increase step size" onclick={onStepUp}>+</button>
        </div>
      </div>
    </div>

    <span class="sdn-conj-col-arrow">→</span>

    <div class="sdn-conj-live-rail">
      <div class="sdn-conj-live-card" style={`border-color:${liveCard.borderColor};background:${liveCard.bgColor};`}>
        <div class="sdn-conj-live-header">
          <span
            class="sdn-conj-live-dot"
            class:sdn-conj-live-dot--pulse={liveCard.pulseOn}
            style={`background:${liveCard.dotColor};box-shadow:0 0 8px ${liveCard.dotColor};`}
          ></span>
          <span class="sdn-conj-live-label" style={`color:${liveCard.textColor};`}>{liveCard.label}</span>
        </div>
        <span class="sdn-conj-demo-tag sdn-conj-live-demo-tag" title={CONJUNCTION_LIVE_DEMO_TAG_TITLE}>DEMO</span>
        <div class="sdn-conj-live-sub">{liveCard.subText}</div>
        <div class="sdn-conj-live-meta">
          <span class="sdn-conj-live-meta-value">{liveCard.sourceCountLabel}</span> ·
          <span class="sdn-conj-live-meta-value">{liveCard.propagatorLabel}</span><br />
          <span class="sdn-conj-live-meta-delta">{liveCard.lastDeltaLabel}</span>
        </div>
        <button
          type="button"
          class="sdn-conj-live-btn"
          style={`border-color:${liveCard.buttonBorderColor};color:${liveCard.buttonColor};`}
          title="Pause or resume the demo live-screening indicator"
          onclick={onToggleLive}
        >
          {liveCard.buttonLabel}
        </button>
      </div>
      <button
        type="button"
        class="sdn-conj-oneoff-btn"
        title="Open the one-off backfill screening popover (demo)"
        aria-expanded={oneOffOpen}
        onclick={onToggleOneOff}
      >
        ONE-OFF RUN ▸
      </button>
    </div>
  </div>

  {#if oneOffOpen}
    <div class="sdn-conj-oneoff-popover">
      <div class="sdn-conj-oneoff-popover-header">
        <span class="sdn-conj-oneoff-popover-title">ONE-OFF SCREEN</span>
        <span class="sdn-conj-demo-tag" title={CONJUNCTION_ONE_OFF_DEMO_TAG_TITLE}>DEMO</span>
        <button type="button" class="sdn-conj-oneoff-close" title="Close" onclick={onToggleOneOff}>×</button>
      </div>
      <div class="sdn-conj-oneoff-desc">
        Backfill a single screening over a past window using the same profile. Runs once — the live stream keeps
        running.
      </div>
      <div class="sdn-conj-crit-row">
        <span class="sdn-conj-crit-label">LOOK-BACK</span>
        <button type="button" class="sdn-conj-crit-btn" title="Decrease look-back window" onclick={onOneOffDown}>−</button>
        <span class="sdn-conj-crit-value">{oneOffWindow}<span class="sdn-conj-crit-unit"> h</span></span>
        <button type="button" class="sdn-conj-crit-btn" title="Increase look-back window" onclick={onOneOffUp}>+</button>
      </div>
      <button type="button" class="sdn-conj-oneoff-run" title="Run this one-off backfill screening now" onclick={onRunOneOff}>
        RUN ONCE
      </button>
      {#if oneOffMessage}
        <div class="sdn-conj-oneoff-msg">{oneOffMessage}</div>
      {/if}
    </div>
  {/if}
</section>

<style>
  .sdn-conj-task {
    grid-column: span 12;
    position: relative;
    background: linear-gradient(178deg, #1b1518, #0f0a0c);
    border: 1px solid rgba(255, 107, 107, 0.28);
    box-shadow:
      inset 0 1px 0 rgba(255, 150, 150, 0.1),
      inset 0 -10px 30px rgba(0, 0, 0, 0.45);
    padding: 16px 18px;
  }

  .sdn-conj-corner {
    position: absolute;
    width: 9px;
    height: 9px;
  }

  .sdn-conj-corner--tl {
    top: -1px;
    left: -1px;
    border-top: 1px solid #ff6b6b;
    border-left: 1px solid #ff6b6b;
  }

  .sdn-conj-corner--tr {
    top: -1px;
    right: -1px;
    border-top: 1px solid #ff6b6b;
    border-right: 1px solid #ff6b6b;
  }

  .sdn-conj-corner--bl {
    bottom: -1px;
    left: -1px;
    border-bottom: 1px solid #ff6b6b;
    border-left: 1px solid #ff6b6b;
  }

  .sdn-conj-corner--br {
    bottom: -1px;
    right: -1px;
    border-bottom: 1px solid #ff6b6b;
    border-right: 1px solid #ff6b6b;
  }

  .sdn-conj-task-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 4px;
    gap: 12px;
    flex-wrap: wrap;
  }

  .sdn-conj-task-title {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 700;
    font-size: 17px;
    letter-spacing: 0.06em;
    color: #ffd2d2;
  }

  .sdn-conj-lock-chip {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 11px;
    letter-spacing: 0.08em;
    color: #ff9b9b;
    border: 1px solid rgba(255, 107, 107, 0.4);
    padding: 3px 9px;
  }

  .sdn-conj-task-desc {
    font-size: 11.5px;
    color: #b89a9a;
    margin-bottom: 12px;
  }

  .sdn-conj-target-row {
    display: flex;
    align-items: center;
    gap: 11px;
    flex-wrap: wrap;
    background: rgba(20, 12, 14, 0.4);
    border: 1px solid rgba(255, 107, 107, 0.22);
    padding: 9px 11px;
    margin-bottom: 14px;
  }

  .sdn-conj-target-kicker {
    font-size: 9.5px;
    letter-spacing: 0.16em;
    color: #c98a8a;
    flex: none;
  }

  .sdn-conj-target-pills {
    display: flex;
    gap: 6px;
    flex-wrap: wrap;
    flex: 1;
    align-items: center;
  }

  .sdn-conj-target-pill {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    border: 1px solid;
    padding: 5px 10px;
    cursor: pointer;
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 600;
    font-size: 12px;
    letter-spacing: 0.04em;
  }

  .sdn-conj-target-pill-dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    flex: none;
  }

  .sdn-conj-target-empty {
    font-size: 11px;
    color: #7a6060;
  }

  .sdn-conj-3d-btn {
    flex: none;
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 11px;
    letter-spacing: 0.06em;
    color: #9fd4f5;
    background: transparent;
    border: 1px solid rgba(120, 190, 230, 0.4);
    padding: 5px 9px;
    cursor: pointer;
  }

  .sdn-conj-3d-btn:disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }

  .sdn-conj-status-strip {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
    margin-bottom: 13px;
    font-size: 11px;
    color: #b89a9a;
  }

  .sdn-conj-status-name {
    color: #eaf6f8;
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 600;
  }

  .sdn-conj-columns {
    display: flex;
    align-items: stretch;
    gap: 0;
    flex-wrap: wrap;
  }

  .sdn-conj-col {
    min-width: 220px;
    display: flex;
    flex-direction: column;
    background: rgba(20, 12, 14, 0.45);
    border: 1px solid rgba(255, 107, 107, 0.22);
    padding: 10px 11px;
  }

  .sdn-conj-col--sources {
    flex: 1.5;
  }

  .sdn-conj-col--propagator {
    flex: 1.05;
  }

  .sdn-conj-col--criteria {
    flex: 1.15;
  }

  .sdn-conj-col-arrow {
    flex: none;
    width: 22px;
    display: flex;
    align-items: center;
    justify-content: center;
    font-size: 18px;
    color: #7d6b6b;
  }

  .sdn-conj-col-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 9px;
  }

  .sdn-conj-col-title {
    font-size: 9.5px;
    letter-spacing: 0.16em;
    color: #c98a8a;
  }

  .sdn-conj-col-sub {
    font-size: 9px;
    letter-spacing: 0.08em;
    color: #8a6f6f;
  }

  .sdn-conj-col-footnote {
    font-size: 9px;
    color: #7a6060;
    margin-top: 8px;
    line-height: 1.4;
  }

  /* -- ① DATA SOURCES -- */

  .sdn-conj-source-rows {
    display: flex;
    flex-direction: column;
    gap: 5px;
  }

  .sdn-conj-source-row {
    display: flex;
    align-items: center;
    gap: 7px;
    background: rgba(255, 255, 255, 0.02);
    border: 1px solid rgba(90, 150, 180, 0.18);
    padding: 6px 8px;
  }

  .sdn-conj-source-prec {
    flex: none;
    width: 16px;
    height: 16px;
    display: flex;
    align-items: center;
    justify-content: center;
    font-size: 11px;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    color: #ffb3b3;
    border: 1px solid rgba(255, 107, 107, 0.35);
  }

  .sdn-conj-source-arrows {
    flex: none;
    display: flex;
    flex-direction: column;
    gap: 1px;
  }

  .sdn-conj-source-arrow {
    width: 15px;
    height: 9px;
    display: flex;
    align-items: center;
    justify-content: center;
    background: transparent;
    border: 1px solid rgba(110, 170, 190, 0.3);
    color: #9fb3bc;
    cursor: pointer;
    font-size: 8.5px;
    line-height: 1;
    padding: 0;
  }

  .sdn-conj-source-arrow:disabled {
    opacity: 0.3;
    cursor: not-allowed;
  }

  .sdn-conj-source-enc {
    flex: none;
    font-size: 13px;
  }

  .sdn-conj-source-name {
    flex: 1;
    min-width: 0;
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 600;
    font-size: 13px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .sdn-conj-source-type {
    flex: none;
    font-size: 9px;
    letter-spacing: 0.06em;
    color: #6f8693;
  }

  .sdn-conj-source-tag {
    flex: none;
    font-size: 9px;
    letter-spacing: 0.06em;
  }

  .sdn-conj-source-toggle {
    flex: none;
    width: 24px;
    height: 14px;
    border-radius: 7px;
    border: 1px solid;
    position: relative;
    cursor: pointer;
    padding: 0;
  }

  .sdn-conj-source-toggle-knob {
    position: absolute;
    top: 1px;
    width: 10px;
    height: 10px;
    border-radius: 50%;
    transition: left 0.14s;
  }

  /* -- ② PROPAGATOR -- */

  .sdn-conj-prop-rows {
    display: flex;
    flex-direction: column;
    gap: 7px;
  }

  .sdn-conj-prop-row {
    display: flex;
    align-items: center;
    gap: 9px;
    border: 1px solid;
    padding: 7px 9px;
    cursor: pointer;
    text-align: left;
  }

  .sdn-conj-prop-row:disabled {
    cursor: not-allowed;
    opacity: 0.65;
  }

  .sdn-conj-prop-radio {
    flex: none;
    width: 14px;
    height: 14px;
    border-radius: 50%;
    border: 1px solid;
    display: flex;
    align-items: center;
    justify-content: center;
  }

  .sdn-conj-prop-radio-dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
  }

  .sdn-conj-prop-text {
    flex: 1;
    min-width: 0;
  }

  .sdn-conj-prop-name {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 600;
    font-size: 13px;
  }

  .sdn-conj-prop-paid {
    font-size: 8.5px;
    letter-spacing: 0.08em;
    color: #35c9d8;
    border: 1px solid rgba(53, 201, 216, 0.45);
    padding: 0 4px;
    margin-left: 4px;
  }

  .sdn-conj-prop-detail {
    font-size: 9.5px;
    color: #6f8693;
    margin-top: 1px;
  }

  .sdn-conj-prop-state {
    flex: none;
    font-size: 9px;
    letter-spacing: 0.08em;
  }

  /* -- ③ SCREENING CRITERIA -- */

  .sdn-conj-crit-rows {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  .sdn-conj-crit-row {
    display: flex;
    align-items: center;
    gap: 6px;
  }

  .sdn-conj-crit-label {
    flex: 1;
    font-size: 10px;
    letter-spacing: 0.03em;
    color: #b89a9a;
  }

  .sdn-conj-crit-btn {
    width: 16px;
    height: 16px;
    display: flex;
    align-items: center;
    justify-content: center;
    background: rgba(255, 107, 107, 0.1);
    border: 1px solid rgba(255, 107, 107, 0.3);
    color: #ffb3b3;
    cursor: pointer;
    font-size: 13px;
    line-height: 1;
    padding: 0;
  }

  .sdn-conj-crit-value {
    width: 52px;
    text-align: center;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 13px;
    color: #eaf6f8;
    font-variant-numeric: tabular-nums;
  }

  .sdn-conj-crit-unit {
    font-size: 9.5px;
    color: #7d929b;
  }

  .sdn-conj-crit-cycle {
    min-width: 74px;
    text-align: center;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 13px;
    color: #eaf6f8;
    background: rgba(255, 107, 107, 0.1);
    border: 1px solid rgba(255, 107, 107, 0.3);
    cursor: pointer;
    padding: 2px 8px;
  }

  /* -- LIVE STREAM STATUS + ONE-OFF RUN -- */

  .sdn-conj-live-rail {
    flex: none;
    width: 160px;
    display: flex;
    flex-direction: column;
    align-items: stretch;
    justify-content: center;
    gap: 8px;
  }

  .sdn-conj-live-card {
    border: 1px solid;
    padding: 11px 12px;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  .sdn-conj-live-header {
    display: flex;
    align-items: center;
    gap: 7px;
  }

  .sdn-conj-live-dot {
    flex: none;
    width: 8px;
    height: 8px;
    border-radius: 50%;
  }

  .sdn-conj-live-dot--pulse {
    animation: sa-pulse 1.4s ease-in-out infinite;
  }

  .sdn-conj-live-label {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 700;
    font-size: 13px;
    letter-spacing: 0.06em;
  }

  .sdn-conj-live-demo-tag {
    align-self: flex-start;
  }

  .sdn-conj-live-sub {
    font-size: 9.5px;
    color: #9a8a8a;
    line-height: 1.55;
  }

  .sdn-conj-live-meta {
    font-size: 9.5px;
    letter-spacing: 0.03em;
    color: #9a8a8a;
    line-height: 1.85;
    border-top: 1px solid rgba(255, 255, 255, 0.06);
    padding-top: 7px;
  }

  .sdn-conj-live-meta-value {
    color: #cfe3ec;
  }

  .sdn-conj-live-meta-delta {
    color: #7d929b;
  }

  .sdn-conj-live-btn {
    background: transparent;
    border: 1px solid;
    padding: 7px 0;
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 600;
    font-size: 11px;
    letter-spacing: 0.1em;
    cursor: pointer;
  }

  .sdn-conj-oneoff-btn {
    background: rgba(255, 255, 255, 0.02);
    border: 1px solid rgba(90, 150, 180, 0.3);
    color: #9fb3bc;
    padding: 7px 0;
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 600;
    font-size: 11px;
    letter-spacing: 0.08em;
    cursor: pointer;
  }

  .sdn-conj-oneoff-btn:hover {
    border-color: rgba(120, 190, 230, 0.5);
    color: #eaf6f8;
  }

  /* -- one-off backfill popover -- */

  .sdn-conj-oneoff-popover {
    position: absolute;
    right: 18px;
    bottom: 18px;
    z-index: 25;
    width: 244px;
    background: linear-gradient(178deg, #1b1518, #0f0a0c);
    border: 1px solid rgba(255, 107, 107, 0.45);
    box-shadow: 0 18px 44px rgba(0, 0, 0, 0.62);
    padding: 13px 14px;
    display: flex;
    flex-direction: column;
    gap: 10px;
  }

  .sdn-conj-oneoff-popover-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
  }

  .sdn-conj-oneoff-popover-title {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 700;
    font-size: 13px;
    letter-spacing: 0.06em;
    color: #ffd2d2;
    flex: 1;
  }

  .sdn-conj-oneoff-close {
    background: transparent;
    border: none;
    color: #9a8a8a;
    cursor: pointer;
    font-size: 17px;
    line-height: 1;
    padding: 0;
  }

  .sdn-conj-oneoff-desc {
    font-size: 10px;
    color: #b89a9a;
    line-height: 1.55;
  }

  .sdn-conj-oneoff-run {
    background: rgba(255, 107, 107, 0.16);
    border: 1px solid rgba(255, 107, 107, 0.5);
    color: #ffb3b3;
    padding: 9px 0;
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 700;
    font-size: 12px;
    letter-spacing: 0.1em;
    cursor: pointer;
  }

  .sdn-conj-oneoff-msg {
    font-size: 9.5px;
    color: #7d929b;
    text-align: center;
  }

  /* -- DEMO tag (same style as Bmc2TopBar.svelte's .bmc-demo-tag /
     groups-data.ts's .sdn-groups-demo-tag) -- */

  .sdn-conj-demo-tag {
    padding: 1px 5px;
    border: 1px solid rgba(255, 208, 137, 0.5);
    color: #ffd089;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 8px;
    letter-spacing: 0.12em;
    flex: none;
  }
</style>

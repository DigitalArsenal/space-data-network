<script lang="ts">
  /**
   * Shared BMC2 top mode bar (46px), ground truth: the `<!-- TOP MODE BAR
   * -->` block, byte-identical in structure across all six boards
   * (BMC2_F1_Surveillance.dc.html … BMC2_F6_Comms.dc.html) — only the
   * kicker text, active tab, and right-hand status cluster differ per
   * board. F1/F2/F3 are wired here (loop U2.1); F4/F5/F6 will reuse this
   * same component unmodified in loop U2.2.
   */
  import { BMC2_MODE_TABS, bmc2Route, bmc2TabAccent, BMC2_DEMO_TAG_TITLE } from '../../lib/bmc2';
  import type { Bmc2Mode } from '../../router';

  let {
    activeMode,
    kicker,
    statusLabel,
    statusTextColor = '#5ad6a0',
    statusDotColor = '#5ad6a0',
    statusPulseSeconds = 1.6,
    navigate,
  }: {
    activeMode: Bmc2Mode;
    kicker: string;
    statusLabel: string;
    statusTextColor?: string;
    statusDotColor?: string;
    statusPulseSeconds?: number;
    navigate: (path: string) => void;
  } = $props();

  // Static per the ground truth (bmc2/README.md: "no logic class" — these
  // boards are presentational only, so the clock is the literal
  // "02:14:08" baked into every .dc.html, not a live readout).
  const CLOCK_LABEL = '02:14:08';
</script>

<div class="bmc-topbar">
  <div class="bmc-topbar-brand">
    <span class="bmc-topbar-wordmark">ORBITAL BMC2</span>
    <span class="bmc-topbar-kicker">{kicker}</span>
    <span class="bmc-demo-tag" title={BMC2_DEMO_TAG_TITLE}>DEMO</span>
  </div>

  <nav class="bmc-topbar-nav" aria-label="BMC2 mode boards">
    {#each BMC2_MODE_TABS as tab (tab.id)}
      {@const accent = bmc2TabAccent(tab.id, activeMode)}
      <button
        type="button"
        class="bmc-topbar-tab"
        aria-current={tab.id === activeMode ? 'page' : undefined}
        style={`background:${accent.background};border:${accent.border};`}
        title={`${tab.label} — BMC2 ${tab.key} board`}
        onclick={() => navigate(bmc2Route(tab.id))}
      >
        <span class="bmc-topbar-tab-label" style={`color:${accent.label};`}>{tab.label}</span>
        <span class="bmc-topbar-tab-key" style={`color:${accent.sub};`}>{tab.key}</span>
      </button>
    {/each}
  </nav>

  <div class="bmc-topbar-status">
    <span class="bmc-topbar-clock">{CLOCK_LABEL}</span>
    <span class="bmc-topbar-link" style={`color:${statusTextColor};`}>
      <span
        class="bmc-topbar-link-dot"
        style={`background:${statusDotColor};box-shadow:0 0 6px ${statusDotColor};animation-duration:${statusPulseSeconds}s;`}
      ></span>{statusLabel}
    </span>
  </div>
</div>

<style>
  .bmc-topbar {
    position: absolute;
    top: 0;
    left: 0;
    right: 0;
    height: 46px;
    z-index: 30;
    display: flex;
    align-items: center;
    gap: 18px;
    padding: 0 16px;
    background: linear-gradient(180deg, rgba(7, 12, 18, 0.92), rgba(7, 12, 18, 0.55));
    border-bottom: 1px solid rgba(90, 150, 180, 0.22);
    backdrop-filter: blur(6px);
    -webkit-backdrop-filter: blur(6px);
  }

  .bmc-topbar-brand {
    display: flex;
    align-items: baseline;
    gap: 8px;
    flex: none;
  }

  .bmc-topbar-wordmark {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 700;
    font-size: 17px;
    letter-spacing: 0.16em;
    color: #eaf6f8;
  }

  .bmc-topbar-kicker {
    font-size: 10px;
    letter-spacing: 0.18em;
    color: #5d7681;
  }

  .bmc-demo-tag {
    padding: 1px 5px;
    border: 1px solid rgba(255, 208, 137, 0.5);
    color: #ffd089;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 8px;
    letter-spacing: 0.12em;
  }

  .bmc-topbar-nav {
    flex: 1;
    display: flex;
    justify-content: center;
    gap: 2px;
  }

  .bmc-topbar-tab {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 1px;
    padding: 5px 13px;
    cursor: pointer;
    background: transparent;
  }

  .bmc-topbar-tab-label {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 600;
    font-size: 12px;
    letter-spacing: 0.1em;
  }

  .bmc-topbar-tab-key {
    font-size: 8.5px;
    letter-spacing: 0.14em;
  }

  .bmc-topbar-status {
    flex: none;
    display: flex;
    align-items: center;
    gap: 14px;
  }

  .bmc-topbar-clock {
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 15.5px;
    font-variant-numeric: tabular-nums;
    letter-spacing: 0.04em;
    color: #eaf6f8;
  }

  .bmc-topbar-link {
    display: flex;
    align-items: center;
    gap: 5px;
    font-size: 9.5px;
    letter-spacing: 0.16em;
  }

  .bmc-topbar-link-dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    animation-name: oc-pulse;
    animation-iteration-count: infinite;
  }
</style>

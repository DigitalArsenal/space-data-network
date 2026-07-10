<script lang="ts">
  /**
   * Console header bar (loop U3.1). Ground truth: the `<header>` block in
   * `SDN Console.dc.html` — kicker + route title/subtitle on the left,
   * three status chips on the right.
   *
   * Per the loop task's acceptance criteria, only two of the three chips
   * are wired real: the health (ONLINE/DEGRADED/OFFLINE) chip from
   * `GET /api/v1/data/health`, and the session (IDENTITY) chip from the
   * already-hydrated `authState` (itself sourced from `GET /api/auth/me`
   * via the shared `authStore` — see `SpaceAwareApp.svelte`). The middle
   * "N TRUSTED PEERS" chip stays the mock's literal placeholder value
   * (real peer/trust wiring is a later gap — see
   * `SPACEAWARE_UI_WIRING_ANALYSIS.md` gap M4) but its click-through to the
   * PEERS view is real routing, not a placeholder.
   */
  import {
    CONSOLE_SUBTITLES,
    CONSOLE_TITLES,
    consoleHealthChipStyle,
    consoleSessionChipStyle,
    consoleTitleAccent,
    hexToRgba,
    type ConsoleHealthChipState,
  } from '../../lib/console';
  import type { AuthStatus } from '../../../lib/auth/auth-store';
  import type { ConsoleView } from '../../router';

  let {
    view,
    healthState,
    sessionStatus,
    navigate,
  }: {
    view: ConsoleView;
    healthState: ConsoleHealthChipState;
    sessionStatus: AuthStatus;
    navigate: (path: string) => void;
  } = $props();

  const healthChip = $derived(consoleHealthChipStyle(healthState));
  const sessionChip = $derived(consoleSessionChipStyle(sessionStatus));

  // Placeholder count — see doc comment above. Real trust-aware peer count
  // is gap M4 in the wiring analysis, not this loop task.
  const TRUSTED_PEERS_PLACEHOLDER = '2 TRUSTED PEERS';

  function goPeers() {
    navigate('/console/peers');
  }
</script>

<header class="sdn-console-header">
  <div class="sdn-console-header-title-block">
    <div class="sdn-console-header-kicker">SPACE OPERATIONS NETWORK CONSOLE</div>
    <div class="sdn-console-header-title-row">
      <span class="sdn-console-header-title">{CONSOLE_TITLES[view]}</span>
      <span class="sdn-console-header-subtitle" style={`color:${consoleTitleAccent(view)};`}
        >{CONSOLE_SUBTITLES[view]}</span
      >
    </div>
  </div>

  <div class="sdn-console-header-chips">
    <span
      class="sdn-console-chip"
      title={`Node health: ${healthChip.label}`}
      style={`border-color:${hexToRgba(healthChip.color, 0.4)};background:${hexToRgba(healthChip.color, 0.06)};color:${healthChip.color};`}
    >
      <span class="sdn-console-chip-dot" style={`background:${healthChip.color};box-shadow:0 0 7px ${healthChip.color};`}
      ></span>{healthChip.label}
    </span>

    <button
      type="button"
      class="sdn-console-chip sdn-console-chip--peers"
      title="View peers directory"
      onclick={goPeers}
    >
      {TRUSTED_PEERS_PLACEHOLDER}
    </button>

    <span
      class="sdn-console-chip"
      title={`Session: ${sessionChip.label}`}
      style={`border-color:${hexToRgba(sessionChip.color, 0.4)};background:${hexToRgba(sessionChip.color, 0.06)};color:${sessionChip.color};`}
    >
      <span
        class="sdn-console-chip-dot"
        style={`background:${sessionChip.color};box-shadow:0 0 7px ${sessionChip.color};`}
      ></span>{sessionChip.label}
    </span>
  </div>
</header>

<style>
  .sdn-console-header {
    flex: none;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 18px;
    border-bottom: 1px solid rgba(90, 150, 180, 0.18);
    background: rgba(10, 15, 21, 0.92);
    padding: 14px 24px;
  }

  .sdn-console-header-kicker {
    font-size: 11.5px;
    letter-spacing: 0.22em;
    color: #5d7681;
    margin-bottom: 5px;
  }

  .sdn-console-header-title-row {
    display: flex;
    align-items: baseline;
    gap: 13px;
  }

  .sdn-console-header-title {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 700;
    font-size: 29px;
    letter-spacing: 0.1em;
    color: #eaf6f8;
  }

  .sdn-console-header-subtitle {
    font-size: 11.5px;
    letter-spacing: 0.16em;
  }

  .sdn-console-header-chips {
    display: flex;
    gap: 8px;
  }

  .sdn-console-chip {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    border: 1px solid rgba(90, 150, 180, 0.3);
    background: transparent;
    padding: 5px 11px;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 12px;
    letter-spacing: 0.08em;
    color: #9fb3bc;
  }

  button.sdn-console-chip {
    cursor: pointer;
  }

  .sdn-console-chip--peers:hover {
    background: rgba(74, 166, 224, 0.07);
  }

  .sdn-console-chip-dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    flex: none;
  }
</style>

<script>
  /**
   * SERVICE.
   *
   * DESIGN SOURCE (declared, not copied):
   *   SpaceAware-UI @ archive/SpaceAware.io 2/SDN Console.dc.html
   *   sha256 abacdbfc62aeaee1193eccec9087669bfeb2324422fe8223482556fad207f152
   *   widget :168-190 · registry entry :865
   *
   * AUTOSTART IS A MEASUREMENT, from the supervisor probe (GET
   * /api/node/service, systemd's own UnitFileState). Wave 1 rendered a
   * hardcoded "ENABLED" behind a flag that was structurally always false; that
   * literal is GONE (IRIS R8). With no supervisor proven, the cell is ABSENT.
   *
   * RESTART / STOP are rendered ONLY from `runtime.canRestart` / `canStop`,
   * which are fail-closed on the host, and never rendered disabled: "three
   * greyed buttons advertise a capability the node lacks and invite the owner to
   * click them" (IRIS §5). Neither flag is true on any host yet, so neither
   * button is drawn — this widget is where they will land.
   */
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import { formatStartedUTC, formatUptimeClock } from '../../runtime.js';

  let { runtime, now = 0 } = $props();

  const serviceState = $derived((runtime?.serviceState || '').toUpperCase());
  const versionLine = $derived(
    [runtime?.suiteVersion ? `v${runtime.suiteVersion}` : '', runtime?.standardsVersion ? `SDS ${runtime.standardsVersion}` : '']
      .filter(Boolean)
      .join(' · ')
  );
  const uptimeClock = $derived(
    typeof runtime?.uptimeS === 'number' ? formatUptimeClock(runtime.uptimeS) : ''
  );
  /** started_at on the UTC clock, carrying the date when it is not today (C3). */
  const startedClock = $derived(formatStartedUTC(runtime?.startedAt, now || Date.now()));
  /**
   * systemd's own UnitFileState word, upper-cased for the cell. Colour states
   * what it MEANS: enabled is good, disabled/masked is the operator's choice to
   * see, and static/indirect are neither (so they take the neutral tone rather
   * than being coloured as if they were).
   */
  const autostart = $derived((runtime?.autostart || '').trim());
  const autostartColor = $derived(
    autostart === 'enabled' || autostart === 'enabled-runtime'
      ? theme.green
      : autostart === 'disabled' || autostart === 'masked'
        ? theme.amber
        : theme.textBody
  );
</script>

<div class="wkick" style="color:{theme.textMuted};">SERVICE</div>
<div class="hero">
  <span class="dot" style="background:{theme.green};box-shadow:0 0 9px {theme.green};"></span>
  <!-- The daemon can only honestly claim "running" while it is up and answering
       this request — which is the only way this renders. -->
  <span class="svcval" style="color:{theme.textBright};">{serviceState || 'RUNNING'}</span>
</div>
{#if versionLine}
  <div class="sub" style="color:{theme.textDim};">{versionLine}</div>
{/if}
<div class="crow foot">
  {#if uptimeClock}
    <div class="cell">
      <div class="clabel" style="color:{theme.textMuted};">UPTIME</div>
      <div class="cval num" style="color:{theme.textBody};">{uptimeClock}</div>
    </div>
  {/if}
  {#if startedClock}
    <div class="cell">
      <div class="clabel" style="color:{theme.textMuted};">STARTED</div>
      <div class="cval num" style="color:{theme.textBody};">{startedClock}</div>
    </div>
  {/if}
  {#if autostart}
    <!-- A MEASUREMENT now: systemd's UnitFileState, read through
         GET /api/node/service. Absent when no supervisor is proven — which is
         every host that is not systemd, and is the honest answer there. -->
    <div class="cell">
      <div class="clabel" style="color:{theme.textMuted};">AUTOSTART</div>
      <div class="cval" style="color:{autostartColor};">{autostart.toUpperCase()}</div>
    </div>
  {/if}
</div>

<style>
  .wkick {
    font-size: var(--sdn-fs-micro);
    line-height: var(--sdn-lh-micro);
    letter-spacing: 0.2em;
    display: block;
  }

  .hero { display: flex; align-items: baseline; gap: var(--sdn-sp-3); }
  .dot { width: 9px; height: 9px; border-radius: 50%; flex: none; }
  /* IRIS §7 — the design's service state (24) re-snapped to the head rung. */
  .svcval {
    font-family: 'Chakra Petch', sans-serif;
    font-weight: 700;
    font-size: var(--sdn-fs-head);
    line-height: var(--sdn-lh-head);
    letter-spacing: 0.05em;
  }
  .sub {
    font-size: var(--sdn-fs-label);
    line-height: var(--sdn-lh-label);
    letter-spacing: 0.04em;
    margin: var(--sdn-sp-1) 0 var(--sdn-sp-6);
  }

  .crow { display: flex; gap: var(--sdn-sp-8); flex-wrap: wrap; }
  /* SERVICE's terminal row: the export puts its last block at the floor. */
  .crow.foot { margin-top: auto; }
  .crow .cell { flex: 1 1 40%; min-width: 0; }
  .cell { min-width: 0; }
  .clabel {
    font-size: var(--sdn-fs-micro);
    line-height: var(--sdn-lh-micro);
    letter-spacing: 0.14em;
  }
  .cval {
    font-size: var(--sdn-fs-note);
    line-height: var(--sdn-lh-note);
    margin-top: 2px;
  }
  .cval.num { font-variant-numeric: tabular-nums; }
</style>

<script lang="ts">
  import tokens, { regime, status, typeScale } from '../../lib/tokens';
  import Panel from '../primitives/Panel.svelte';
  import SaButton from '../primitives/SaButton.svelte';
  import SaTabs from '../primitives/SaTabs.svelte';
  import StatusDot from '../primitives/StatusDot.svelte';

  let { navigate }: { navigate: (path: string) => void } = $props();

  let specimenTab = $state('type');

  const specimenTabs = [
    { id: 'type', label: 'Type', title: 'Typography specimen' },
    { id: 'color', label: 'Color', title: 'Color token specimen' },
    { id: 'controls', label: 'Controls', title: 'Control primitives specimen' },
  ] as const;

  const textEntries = Object.entries(tokens.text);
  const accentEntries = Object.entries(tokens.accent);
  const regimeEntries = Object.entries(regime);
  const statusEntries = Object.keys(status) as (keyof typeof status)[];
  const chakraWeights = [400, 500, 600, 700];
  const plexWeights = [400, 500, 600];
  const jetbrainsWeights = [400, 500, 600];
</script>

<main>
  <header>
    <span class="sa-kicker">SpaceAware · SDN</span>
    <h1>LOGIN</h1>
    <p class="scaffold-note">
      U0.1 scaffold placeholder — design-token specimen. The pixel port of
      login/Login.dc.html lands in loop task U1.1.
    </p>
  </header>

  <Panel title="Token specimen" variant="raised">
    <SaTabs tabs={specimenTabs} selected={specimenTab} onselect={(id) => (specimenTab = id)} />

    {#if specimenTab === 'type'}
      <div class="specimen-block">
        <span class="sa-kicker">Chakra Petch · display / titles / buttons</span>
        {#each chakraWeights as weight (weight)}
          <div class="type-row" style={`font-family:${tokens.font.display};font-weight:${weight};`}>
            <span class="type-meta">{weight}</span>
            SPACE DATA NETWORK · OPERATOR CONSOLE
          </div>
        {/each}
      </div>
      <div class="specimen-block">
        <span class="sa-kicker">IBM Plex Mono · body / data values</span>
        {#each plexWeights as weight (weight)}
          <div class="type-row" style={`font-family:${tokens.font.mono};font-weight:${weight};`}>
            <span class="type-meta">{weight}</span>
            NORAD_CAT_ID 25544 · MEAN_MOTION 15.49560532 · EPOCH 2026-07-06T12:00:00Z
          </div>
        {/each}
      </div>
      <div class="specimen-block">
        <span class="sa-kicker">JetBrains Mono · dense numeric readouts</span>
        {#each jetbrainsWeights as weight (weight)}
          <div class="type-row" style={`font-family:${tokens.font.numeric};font-weight:${weight};`}>
            <span class="type-meta">{weight}</span>
            +042.7719 −087.9067 415.21 KM 7.6612 KM/S
          </div>
        {/each}
      </div>
      <div class="specimen-block">
        <span class="sa-kicker">Type scale (px)</span>
        <div class="scale-row">
          {#each typeScale as size (size)}
            <span class="scale-item" style={`font-size:${size}px;`} title={`${size}px`}>{size}</span>
          {/each}
        </div>
        <div class="type-row" style="letter-spacing:0.22em;">
          KICKER TRACKING 0.22EM · <span style="letter-spacing:0.28em;">MAX 0.28EM</span>
        </div>
      </div>
    {:else if specimenTab === 'color'}
      <div class="specimen-block">
        <span class="sa-kicker">Text</span>
        <div class="swatch-grid">
          {#each textEntries as [name, value] (name)}
            <div class="swatch" title={`text.${name} ${value}`}>
              <span class="chip" style={`background:${value};`}></span>
              <span class="swatch-name" style={`color:${value};`}>{name}</span>
              <span class="swatch-hex">{value}</span>
            </div>
          {/each}
        </div>
      </div>
      <div class="specimen-block">
        <span class="sa-kicker">Accents</span>
        <div class="swatch-grid">
          {#each accentEntries as [name, value] (name)}
            <div class="swatch" title={`accent.${name} ${value}`}>
              <span class="chip" style={`background:${value};`}></span>
              <span class="swatch-name" style={`color:${value};`}>{name}</span>
              <span class="swatch-hex">{value}</span>
            </div>
          {/each}
        </div>
      </div>
      <div class="specimen-block">
        <span class="sa-kicker">Orbit regimes</span>
        <div class="swatch-grid">
          {#each regimeEntries as [name, value] (name)}
            <div class="swatch" title={`regime.${name} ${value}`}>
              <span class="chip" style={`background:${value};box-shadow:0 0 7px ${value};`}></span>
              <span class="swatch-name" style={`color:${value};`}>{name}</span>
              <span class="swatch-hex">{value}</span>
            </div>
          {/each}
        </div>
      </div>
      <div class="specimen-block">
        <span class="sa-kicker">Status dots</span>
        <div class="dot-row">
          {#each statusEntries as level (level)}
            <span class="dot-item">
              <StatusDot {level} pulse={level !== 'nominal'} label={level.toUpperCase()} />
              <span class="swatch-hex">{level.toUpperCase()} {status[level]}</span>
            </span>
          {/each}
        </div>
      </div>
    {:else}
      <div class="specimen-block">
        <span class="sa-kicker">Buttons</span>
        <div class="btn-row">
          <SaButton variant="primary" title="Primary action specimen">Authenticate</SaButton>
          <SaButton variant="neutral" title="Neutral action specimen">Explore Public Catalog</SaButton>
          <SaButton variant="destructive" title="Destructive action specimen">Revoke Session</SaButton>
          <SaButton variant="neutral" title="Disabled control specimen" disabled>Disabled</SaButton>
        </div>
      </div>
      <div class="specimen-block">
        <span class="sa-kicker">Panels</span>
        <div class="panel-row">
          <Panel title="Default panel">
            <span class="panel-demo-text">rgba(7,12,18,0.85) + blur</span>
          </Panel>
          <Panel title="Raised panel" variant="raised">
            <span class="panel-demo-text">178deg #16252f → #0a141b</span>
          </Panel>
          <Panel title="Well panel" variant="well">
            <span class="panel-demo-text">178deg #0b151c → #060d12</span>
          </Panel>
        </div>
      </div>
      <div class="specimen-block">
        <span class="sa-kicker">Keyframes</span>
        <div class="fx-row">
          <span class="fx-pulse" title="sa-pulse">PULSE</span>
          <span class="fx-spin" title="sa-spin"></span>
          <span title="sa-blink">CARET<span class="fx-blink">▍</span></span>
        </div>
      </div>
      <div class="specimen-block">
        <span class="sa-kicker">Glyph set</span>
        <div class="glyph-row" title="Established glyph set — no other decoration permitted">
          {#each tokens.glyphs as glyph (glyph)}
            <span class="glyph">{glyph}</span>
          {/each}
        </div>
      </div>
    {/if}
  </Panel>

  <nav class="route-nav">
    <span class="sa-kicker">Route skeleton</span>
    <div class="btn-row">
      <SaButton variant="primary" title="SDN console scaffold" onclick={() => navigate('/console/node')}>
        Console
      </SaButton>
      <SaButton variant="neutral" title="Orbital console scaffold" onclick={() => navigate('/orbital')}>
        Orbital
      </SaButton>
      <SaButton variant="neutral" title="Gantt view scaffold" onclick={() => navigate('/gantt')}>
        Gantt
      </SaButton>
      <SaButton variant="neutral" title="BMC2 mode boards scaffold" onclick={() => navigate('/bmc2')}>
        BMC2
      </SaButton>
    </div>
  </nav>
</main>

<style>
  main {
    max-width: 1080px;
    margin: 0 auto;
    padding: 40px 24px 64px 24px;
    display: flex;
    flex-direction: column;
    gap: 22px;
  }
  h1 {
    margin: 6px 0 0 0;
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 700;
    font-size: 26px;
    letter-spacing: 0.16em;
    color: #eaf6f8;
  }
  .scaffold-note {
    margin: 8px 0 0 0;
    max-width: 640px;
    font-size: 11px;
    color: #7d929b;
  }
  .specimen-block {
    margin-top: 16px;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .type-row {
    color: #c7d6dd;
    font-size: 13px;
  }
  .type-meta {
    display: inline-block;
    width: 34px;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 9px;
    color: #5a7a8a;
  }
  .scale-row {
    display: flex;
    align-items: baseline;
    gap: 10px;
    flex-wrap: wrap;
    color: #cfe3ec;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
  }
  .swatch-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
    gap: 6px 14px;
  }
  .swatch {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 10.5px;
  }
  .chip {
    width: 14px;
    height: 14px;
    border: 1px solid rgba(110, 170, 190, 0.28);
    flex: none;
  }
  .swatch-name {
    min-width: 84px;
  }
  .swatch-hex {
    color: #5d7681;
    font-size: 9.5px;
  }
  .dot-row,
  .btn-row,
  .fx-row,
  .glyph-row {
    display: flex;
    align-items: center;
    gap: 14px;
    flex-wrap: wrap;
  }
  .dot-item {
    display: inline-flex;
    align-items: center;
    gap: 7px;
  }
  .panel-row {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
    gap: 12px;
  }
  .panel-demo-text {
    font-size: 10.5px;
    color: #9fb3bc;
  }
  .fx-row {
    color: #9fd4f5;
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-size: 11px;
    letter-spacing: 0.12em;
  }
  .fx-pulse {
    animation: sa-pulse 1.5s ease-in-out infinite;
  }
  .fx-spin {
    width: 12px;
    height: 12px;
    border: 1px solid rgba(53, 201, 216, 0.35);
    border-top-color: #35c9d8;
    border-radius: 50%;
    animation: sa-spin 0.9s linear infinite;
  }
  .fx-blink {
    animation: sa-blink 1.2s step-end infinite;
    color: #35c9d8;
  }
  .glyph-row {
    font-size: 15.5px;
    color: #cfe3ec;
  }
  .route-nav {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
</style>

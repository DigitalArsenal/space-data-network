<script lang="ts">
  /**
   * Pixel port of BMC2_Modes_Index.dc.html (loop U2.1). Static — the only
   * behavior is navigation into a mode board or the live Orbital Console.
   * Unlike the F1–F6 boards this page is a normal scrolling document (the
   * ground truth uses `min-height:100vh`, not `position:fixed;inset:0`).
   */
  import { BMC2_INDEX_CARDS, BMC2_CARD_VARIANT_STYLE, bmc2Route } from '../../lib/bmc2';

  let { navigate }: { navigate: (path: string) => void } = $props();

  function go(path: string) {
    return (event: MouseEvent) => {
      event.preventDefault();
      navigate(path);
    };
  }
</script>

<div class="bmc-index" data-screen-label="BMC2 Modes Index">
  <header class="bmc-index-header">
    <div>
      <div class="bmc-index-title">ORBITAL BMC2</div>
      <div class="bmc-index-subtitle">SPACE BATTLE MANAGEMENT &amp; COMMON OPERATING PICTURE · MODE MOCKUPS</div>
    </div>
    <div class="bmc-index-shell-note">
      SAME BOX SHELL · SIX MODES<br />
      LEFT PORTRAIT · CENTER GLOBE+DETAIL · RIGHT COMMAND CARD<br />
      <span class="bmc-index-shell-note-accent">SC2-STYLE · F1–F6</span>
    </div>
  </header>

  <div class="bmc-index-grid">
    {#each BMC2_INDEX_CARDS as card (card.mode)}
      {@const style = BMC2_CARD_VARIANT_STYLE[card.variant]}
      <a
        href={bmc2Route(card.mode)}
        class={`bmc-card bmc-card--${card.variant}`}
        title={`Open the BMC2 ${card.title} board`}
        onclick={go(bmc2Route(card.mode))}
      >
        <div class="bmc-card-head">
          <span class="bmc-card-title" style={`color:${style.title};`}>{card.title}</span>
          <span class="bmc-card-tag" style={`color:${style.tag};border-color:${style.tagBorder};`}
            >{card.mode.toUpperCase()}</span
          >
        </div>
        <div class="bmc-card-desc" style={`color:${style.description};`}>{card.description}</div>
        <div class="bmc-card-meta" style={`color:${style.meta};`}>
          {#each card.meta as row (row.label)}
            <div><span style={`color:${style.metaLabel};`}>{row.label}</span> · {row.text}</div>
          {/each}
        </div>
      </a>
    {/each}
  </div>

  <div class="bmc-index-live">
    <a
      href="/orbital"
      class="bmc-live-card"
      title="Open the live Orbital Console"
      onclick={go('/orbital')}
    >
      <span class="bmc-live-dot"></span>
      <div class="bmc-live-body">
        <div class="bmc-live-title">LIVE CONSOLE · TRACK (Cesium)</div>
        <div class="bmc-live-desc">The working build — real globe, 31k-object propagation, sim clock, EPS telemetry.</div>
      </div>
      <span class="bmc-live-open">OPEN</span>
    </a>
  </div>

  <div class="bmc-index-footnote">
    MOCKUPS · stylized globes &amp; representative data for layout review. Capabilities shaped from OrbPro demo set
    (multi-select, sensor volumes, access/coverage, reference frames, conjunction, maneuvers, ground stations).
    Confirm any specific demo behavior and I&apos;ll refine the matching mode.
  </div>
</div>

<style>
  .bmc-index {
    min-height: 100vh;
    background: radial-gradient(circle at 50% -10%, #0a1a26, #04060a 60%);
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    color: #c7d6dd;
    -webkit-font-smoothing: antialiased;
    padding: 40px 40px 56px;
  }

  .bmc-index-header {
    max-width: 1200px;
    margin: 0 auto 30px;
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    gap: 20px;
    border-bottom: 1px solid rgba(90, 150, 180, 0.22);
    padding-bottom: 20px;
  }

  .bmc-index-title {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 700;
    font-size: 33.5px;
    letter-spacing: 0.12em;
    color: #eaf6f8;
  }

  .bmc-index-subtitle {
    font-size: 13px;
    letter-spacing: 0.22em;
    color: #5d7681;
    margin-top: 6px;
  }

  .bmc-index-shell-note {
    text-align: right;
    font-size: 11px;
    color: #6f8693;
    line-height: 1.7;
  }

  .bmc-index-shell-note-accent {
    color: #7fb4d6;
  }

  .bmc-index-grid {
    max-width: 1200px;
    margin: 0 auto;
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: 16px;
  }

  .bmc-card {
    display: block;
    padding: 18px 18px 16px;
    text-decoration: none;
    cursor: pointer;
    transition: border-color 0.15s, background 0.15s, transform 0.1s;
  }

  .bmc-card--cyan {
    background: linear-gradient(178deg, #16252f, #0a141b);
    border: 1px solid rgba(90, 150, 180, 0.3);
  }
  .bmc-card--red {
    background: linear-gradient(178deg, #1f1a1c, #120c0e);
    border: 1px solid rgba(255, 107, 107, 0.3);
  }
  .bmc-card--amber {
    background: linear-gradient(178deg, #211d15, #13100a);
    border: 1px solid rgba(255, 178, 77, 0.32);
  }
  .bmc-card--green {
    background: linear-gradient(178deg, #15211b, #0a130e);
    border: 1px solid rgba(90, 214, 160, 0.3);
  }

  /* Higher specificity than `.bmc-card--*` (class + pseudo-class) so hover
     always wins regardless of variant — matches the ground truth's shared
     `.bmc-card:hover{...!important}` rule applying uniformly to all cards. */
  .bmc-card:hover {
    border-color: rgba(120, 190, 230, 0.6);
    background: rgba(74, 166, 224, 0.06);
    transform: translateY(-2px);
  }

  .bmc-card-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
  }

  .bmc-card-title {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 700;
    font-size: 19px;
    letter-spacing: 0.06em;
  }

  .bmc-card-tag {
    font-size: 12px;
    border: 1px solid;
    padding: 2px 7px;
  }

  .bmc-card-desc {
    font-size: 12px;
    margin-top: 8px;
    line-height: 1.5;
  }

  .bmc-card-meta {
    margin-top: 12px;
    display: flex;
    flex-direction: column;
    gap: 4px;
    font-size: 11px;
  }

  .bmc-index-live {
    max-width: 1200px;
    margin: 18px auto 0;
  }

  .bmc-live-card {
    display: flex;
    align-items: center;
    gap: 16px;
    background: linear-gradient(90deg, #16252f, #0a141b);
    border: 1px solid rgba(90, 150, 180, 0.3);
    padding: 16px 20px;
    text-decoration: none;
    cursor: pointer;
    transition: border-color 0.15s, background 0.15s, transform 0.1s;
  }

  .bmc-live-card:hover {
    border-color: rgba(120, 190, 230, 0.6);
    background: rgba(74, 166, 224, 0.06);
    transform: translateY(-2px);
  }

  .bmc-live-dot {
    width: 10px;
    height: 10px;
    border-radius: 50%;
    background: #35c9d8;
    box-shadow: 0 0 8px #35c9d8;
    flex: none;
  }

  .bmc-live-body {
    flex: 1;
  }

  .bmc-live-title {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 600;
    font-size: 17px;
    color: #eaf6f8;
  }

  .bmc-live-desc {
    font-size: 12px;
    color: #7d929b;
    margin-top: 3px;
  }

  /* Token hard rule: no directional arrow glyphs on labels — ground truth
     reads "OPEN →", the arrow is stripped here (see D3 resolution notes). */
  .bmc-live-open {
    font-size: 13px;
    color: #7fb4d6;
  }

  .bmc-index-footnote {
    max-width: 1200px;
    margin: 26px auto 0;
    font-size: 11px;
    color: #48606b;
    letter-spacing: 0.06em;
    line-height: 1.7;
  }
</style>

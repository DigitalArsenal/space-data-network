<script lang="ts">
  /**
   * Collapsible left rail (loop U3.1). Ground truth: the `<!-- ===
   * COLLAPSIBLE RAIL === -->` `<aside>` in `SDN Console.dc.html` — 66px
   * collapsed, hover-expands to 218px (`.sdn-rail:hover`), brand + two nav
   * sections (IDENTITY & GROUPS / OPERATIONS) + bottom SpaceAware.io link.
   *
   * PINNED-STATE AMBIGUITY (resolved): the mock drives `.sdn-rail.pinned`
   * from a `railPinned` DESIGN-TOOL PROP (`this.props.railPinned`, set by
   * whoever embeds the DC component) — there is no click handler in the
   * markup that toggles it from inside the running prototype. A real app
   * has no such prop mechanism, but the loop task explicitly asks for a
   * "PINNED state", so we need a genuine user-facing toggle. Rather than
   * add a new visual element (which would break pixel parity with the
   * `.dc.html` at rest), clicking the EXISTING brand row toggles `pinned`,
   * persisted to `localStorage['sdn_console_rail_pinned']`
   * (`lib/console.ts`) so it survives reloads like the rest of this app's
   * client-local prefs. The brand row's `title` reflects the action.
   */
  import { onMount } from 'svelte';
  import {
    CONSOLE_NAV_ITEMS,
    consoleNavItemStyle,
    loadRailPinned,
    saveRailPinned,
    type ConsoleNavItem,
  } from '../../lib/console';
  import type { ConsoleView } from '../../router';

  let {
    activeView,
    navigate,
  }: {
    activeView: ConsoleView;
    navigate: (path: string) => void;
  } = $props();

  let pinned = $state(false);

  onMount(() => {
    try {
      pinned = loadRailPinned(window.localStorage);
    } catch {
      pinned = false;
    }
  });

  function togglePin() {
    pinned = !pinned;
    try {
      saveRailPinned(window.localStorage, pinned);
    } catch {
      // localStorage unavailable — pin state just won't survive reload.
    }
  }

  function goTo(item: ConsoleNavItem) {
    navigate(`/console/${item.id}`);
  }

  function goOrbital(event: MouseEvent) {
    event.preventDefault();
    navigate('/orbital');
  }

  const identityItems = CONSOLE_NAV_ITEMS.filter((i) => i.group === 'identity');
  const operationsItems = CONSOLE_NAV_ITEMS.filter((i) => i.group === 'operations');
</script>

<aside class="sdn-rail" class:is-pinned={pinned}>
  <button
    type="button"
    class="sdn-rail-brand"
    onclick={togglePin}
    title={pinned ? 'Unpin sidebar (collapse on mouse-out)' : 'Pin sidebar open'}
  >
    <span class="sdn-rail-icon-well">
      <svg width="30" height="30" viewBox="0 0 32 32" style="display:block;">
        <defs>
          <linearGradient id="sdnMark" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0" stop-color="#6fe6f2"></stop>
            <stop offset="1" stop-color="#1d8597"></stop>
          </linearGradient>
        </defs>
        <path
          fill="url(#sdnMark)"
          d="M9.3 4.4 L13.4 4.4 L13.4 11 L18.6 11 L18.6 4.4 L22.7 4.4 L29.4 16 L22.7 27.6 L9.3 27.6 L2.6 16 Z"
        ></path>
        <path
          fill="none"
          stroke="#9ff0fa"
          stroke-width="0.7"
          stroke-opacity="0.5"
          d="M2.6 16 L9.3 4.4 L13.4 4.4 M18.6 4.4 L22.7 4.4 L29.4 16"
        ></path>
      </svg>
    </span>
    <span class="lbl sdn-rail-brand-text">
      <span class="sdn-rail-wordmark">SPACE DATA NET</span>
      <span class="sdn-rail-sub">LOCAL NODE · DESKTOP</span>
    </span>
  </button>

  <span class="lbl sdn-rail-section-label sdn-rail-section-label--first">IDENTITY &amp; GROUPS</span>
  <nav class="sdn-rail-nav">
    {#each identityItems as item (item.id)}
      {@const style = consoleNavItemStyle(item, activeView)}
      <button
        type="button"
        class="sdn-rail-nav-item"
        class:is-active={item.id === activeView}
        title={item.label}
        style={`background:${style.background};border-left-color:${style.barColor};color:${style.labelColor};`}
        onclick={() => goTo(item)}
      >
        <span class="sdn-rail-nav-icon" style={`color:${style.iconColor};`}>{item.icon}</span>
        <span class="lbl sdn-rail-nav-label">{item.label}<span class="sdn-rail-nav-fkey">{item.fkey}</span></span>
      </button>
    {/each}
  </nav>

  <span class="lbl sdn-rail-section-label sdn-rail-section-label--second">OPERATIONS</span>
  <nav class="sdn-rail-nav">
    {#each operationsItems as item (item.id)}
      {@const style = consoleNavItemStyle(item, activeView)}
      <button
        type="button"
        class="sdn-rail-nav-item"
        class:is-active={item.id === activeView}
        title={item.label}
        style={`background:${style.background};border-left-color:${style.barColor};color:${style.labelColor};`}
        onclick={() => goTo(item)}
      >
        <span class="sdn-rail-nav-icon" style={`color:${style.iconColor};`}>{item.icon}</span>
        <span class="lbl sdn-rail-nav-label">{item.label}<span class="sdn-rail-nav-fkey">{item.fkey}</span></span>
      </button>
    {/each}
  </nav>

  <div class="sdn-rail-spacer"></div>

  <a
    href="/orbital"
    class="sdn-rail-orbital-link"
    title="Open SpaceAware dashboard"
    onclick={goOrbital}
  >
    <span class="sdn-rail-icon-well">
      <svg width="19" height="19" viewBox="0 0 24 24" fill="none" stroke="#35c9d8" stroke-width="1.4" class="sdn-rail-orbital-svg">
        <circle cx="12" cy="12" r="9.2"></circle>
        <ellipse cx="12" cy="12" rx="9.2" ry="3.6"></ellipse>
        <ellipse cx="12" cy="12" rx="3.6" ry="9.2"></ellipse>
        <line x1="2.8" y1="12" x2="21.2" y2="12"></line>
      </svg>
    </span>
    <span class="lbl sdn-rail-orbital-label">SpaceAware.io</span>
  </a>
</aside>

<style>
  .sdn-rail {
    width: 66px;
    transition: width 0.2s cubic-bezier(0.4, 0, 0.2, 1);
    overflow: hidden;
    position: absolute;
    left: 0;
    top: 0;
    bottom: 0;
    z-index: 30;
    background: #05080c;
    border-right: 1px solid rgba(90, 150, 180, 0.18);
    display: flex;
    flex-direction: column;
    padding: 14px 0;
  }

  .sdn-rail:hover {
    width: 218px;
    box-shadow: 10px 0 30px rgba(0, 0, 0, 0.55);
  }

  .sdn-rail.is-pinned {
    width: 218px;
  }

  .sdn-rail .lbl {
    opacity: 0;
    white-space: nowrap;
    transition: opacity 0.13s;
    pointer-events: none;
  }

  .sdn-rail.is-pinned .lbl,
  .sdn-rail:hover .lbl {
    opacity: 1;
    pointer-events: auto;
  }

  .sdn-rail-brand {
    height: 44px;
    flex: none;
    display: flex;
    align-items: center;
    margin-bottom: 18px;
    background: transparent;
    border: none;
    padding: 0;
    cursor: pointer;
    width: 100%;
    text-align: left;
  }

  .sdn-rail-icon-well {
    width: 66px;
    flex: none;
    display: grid;
    place-items: center;
  }

  .sdn-rail-brand-text {
    line-height: 1.3;
  }

  .sdn-rail-wordmark {
    display: block;
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 700;
    font-size: 15px;
    letter-spacing: 0.08em;
    color: #eaf6f8;
  }

  .sdn-rail-sub {
    display: block;
    font-size: 9.5px;
    letter-spacing: 0.16em;
    color: #5d7681;
    margin-top: 1px;
  }

  .sdn-rail-section-label {
    display: block;
    font-size: 9.5px;
    letter-spacing: 0.2em;
    color: #48606b;
    padding: 0 0 8px 18px;
  }

  .sdn-rail-section-label--second {
    padding: 16px 0 8px 18px;
  }

  .sdn-rail-nav {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }

  .sdn-rail-nav-item {
    display: flex;
    align-items: center;
    height: 46px;
    width: 100%;
    border: 0;
    border-left: 2px solid transparent;
    cursor: pointer;
    padding: 0;
    text-align: left;
    font: inherit;
  }

  .sdn-rail-nav-item:not(.is-active):hover {
    background: rgba(74, 166, 224, 0.05) !important;
    color: #cfe3ec !important;
  }

  .sdn-rail-nav-item:not(.is-active):hover .sdn-rail-nav-icon {
    color: #cfe3ec;
  }

  .sdn-rail-nav-icon {
    width: 64px;
    flex: none;
    display: grid;
    place-items: center;
    /* Ground truth renders the ◉/◍/⬡ glyph spans without a font-family
       (system Arial), which draws them larger than IBM Plex Mono's
       fallback path. System font, nothing fetched. */
    font-family: Arial, 'Helvetica Neue', sans-serif;
    font-size: 20.5px;
    line-height: 1;
  }

  .sdn-rail-nav-label {
    display: flex;
    align-items: center;
    gap: 8px;
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 600;
    font-size: 15px;
    letter-spacing: 0.05em;
  }

  .sdn-rail-nav-fkey {
    font-size: 9.5px;
    color: #5a7a8a;
    letter-spacing: 0.1em;
  }

  .sdn-rail-spacer {
    flex: 1;
  }

  .sdn-rail-orbital-link {
    display: flex;
    align-items: center;
    height: 44px;
    flex: none;
    border-top: 1px solid rgba(90, 150, 180, 0.12);
    margin-top: 8px;
    text-decoration: none;
    color: #9fd4f5;
  }

  .sdn-rail-orbital-svg {
    display: block;
    filter: drop-shadow(0 0 4px rgba(53, 201, 216, 0.5));
  }

  .sdn-rail-orbital-label {
    display: flex;
    align-items: center;
    gap: 7px;
    font-size: 11.5px;
    letter-spacing: 0.04em;
    color: #9fd4f5;
  }
</style>

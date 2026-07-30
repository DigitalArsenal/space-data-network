<script>
  /**
   * THE modal shell — one overlay/head/body idiom for the whole dashboard
   * (IRIS ruling 2026-07-29 §3: "extract NodeModal's overlay+head+body and
   * re-point NodeModal *and* SignInModal at it — that is what prevents a
   * fourth modal idiom").
   *
   * Behaviour is the union of what the two existing dialogs already did, and
   * nothing more: Esc closes, an overlay click closes, the head carries a
   * title (+ optional sub) and a chips slot ending in ✕, the body scrolls, and
   * on a phone the dialog takes the whole screen so nothing is clipped.
   *
   * `onClose` is called for every dismissal route, so a caller that must tear
   * something down first (SignInModal cancels a live wallet transaction) has
   * exactly ONE place to do it.
   *
   * Styled only with theme.js tokens.
   */
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';

  /**
   * @type {{
   *   title: string, sub?: string, label?: string, width?: string,
   *   unnamed?: boolean, chips?: any, children?: any, onClose: () => void
   * }}
   */
  let {
    title,
    sub = '',
    label = '',
    width = '720px',
    unnamed = false,
    chips,
    children,
    onClose,
  } = $props();

  function onKeydown(e) {
    if (e.key === 'Escape') onClose();
  }
</script>

<svelte:window onkeydown={onKeydown} />

<!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
<div class="overlay" role="presentation" onclick={(e) => e.target === e.currentTarget && onClose()}>
  <div
    class="modal"
    role="dialog"
    aria-modal="true"
    aria-label={label || title}
    style="background:{theme.panelRaised};border-color:{theme.panelBorder};color:{theme.textBody};width:min({width}, 100%);"
  >
    <div class="head" style="border-color:{theme.divider};">
      <div class="titles">
        <div class="dn" class:unnamed style="color:{unnamed ? theme.textMuted : theme.textBright};">{title}</div>
        {#if sub}
          <div class="sub" style="color:{theme.textDim};">{sub}</div>
        {/if}
      </div>
      <div class="chips">
        {#if chips}{@render chips()}{/if}
        <button
          type="button"
          class="close"
          style="color:{theme.textMuted};border-color:{theme.hairline};"
          onclick={onClose}
          aria-label="Close"
        >✕</button>
      </div>
    </div>
    <div class="body">
      {#if children}{@render children()}{/if}
    </div>
  </div>
</div>

<style>
  .overlay {
    position: fixed;
    inset: 0;
    background: rgba(2, 5, 8, 0.72);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 50;
    padding: 28px;
  }
  .modal {
    max-height: min(88vh, 820px);
    border: 1px solid;
    display: flex;
    flex-direction: column;
    box-shadow: 0 18px 60px rgba(0, 0, 0, 0.55);
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
  }
  .head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 14px;
    padding: 16px 18px 13px;
    border-bottom: 1px solid;
  }
  .titles { min-width: 0; }
  .dn {
    font-family: 'Chakra Petch', sans-serif;
    font-weight: 600;
    font-size: var(--sdn-fs-title); line-height: var(--sdn-lh-title);
    letter-spacing: 0.06em;
    overflow-wrap: anywhere;
  }
  .dn.unnamed { font-style: italic; }
  .sub { font-size: var(--sdn-fs-note); line-height: var(--sdn-lh-note); letter-spacing: 0.1em; margin-top: 4px; overflow-wrap: anywhere; }
  .chips { display: flex; gap: 6px; flex: none; flex-wrap: wrap; justify-content: flex-end; align-items: center; }
  .close {
    background: transparent;
    border: 1px solid;
    cursor: pointer;
    font-size: var(--sdn-fs-data);
    line-height: var(--sdn-lh-data);
    padding: 4px 7px;
    margin-left: 4px;
  }
  .body { overflow: auto; padding: 15px 18px 18px; display: flex; flex-direction: column; gap: 15px; }

  /* Phones: the modal takes the whole screen so its content and close
     button are never clipped by the cramped viewport. */
  @media (max-width: 760px) {
    .overlay { padding: 0; }
    .modal { width: 100% !important; max-height: 100dvh; height: 100dvh; }
  }
</style>

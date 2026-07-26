<script>
  /**
   * Node detail modal — thin overlay around the shared NodeDetail view
   * (also used by the THIS NODE sub-page). Esc or overlay click closes.
   * Styled only with theme.js tokens.
   */
  import StatusChip from 'spaceaware-student-sdn/src/lib/components/StatusChip.svelte';
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import NodeDetail from './NodeDetail.svelte';
  import { normalizeTrust, TRUST_COLOR_TOKEN } from './trust.js';
  import { shortId } from './format.js';

  /** @type {{ node: any, now: number, onClose: () => void }} */
  let { node, now, onClose } = $props();

  const tier = $derived(normalizeTrust(node.trustLevel));
  const tierColor = $derived(theme[TRUST_COLOR_TOKEN[tier]] ?? theme.textMuted);
  const title = $derived(node.dn?.trim() || node.org?.trim() || shortId(node.peerId));

  function onKeydown(e) {
    if (e.key === 'Escape') onClose();
  }
</script>

<svelte:window onkeydown={onKeydown} />

<!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
<div class="overlay" onclick={(e) => e.target === e.currentTarget && onClose()}>
  <div class="modal" role="dialog" aria-modal="true" aria-label="Node details"
    style="background:{theme.panelRaised};border-color:{theme.panelBorder};color:{theme.textBody};">
    <div class="head" style="border-color:{theme.divider};">
      <div class="titles">
        <div class="dn" style="color:{theme.textBright};">{title}</div>
        {#if node.org?.trim() && node.org.trim() !== title}
          <div class="org" style="color:{theme.textDim};">{node.org}</div>
        {/if}
      </div>
      <div class="chips">
        {#if node.isSelf}<StatusChip label="SELF" color={theme.cyan} dot={false} />{/if}
        <StatusChip label={tier.toUpperCase()} color={tierColor} dot={false} />
        <StatusChip label={node.online ? 'ONLINE' : 'OFFLINE'} color={node.online ? theme.green : theme.textMuted} />
        <button class="close" style="color:{theme.textMuted};border-color:{theme.hairline};" onclick={onClose} aria-label="Close">✕</button>
      </div>
    </div>
    <div class="body">
      <NodeDetail {node} {now} />
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
    z-index: 40;
    padding: 28px;
  }
  .modal {
    width: min(720px, 100%);
    max-height: min(84vh, 820px);
    border: 1px solid;
    display: flex;
    flex-direction: column;
    box-shadow: 0 18px 60px rgba(0, 0, 0, 0.55);
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
    font-size: 22px;
    letter-spacing: 0.04em;
    overflow-wrap: anywhere;
  }
  .org { font-size: 15px; letter-spacing: 0.04em; margin-top: 3px; }
  .chips { display: flex; gap: 6px; flex: none; flex-wrap: wrap; justify-content: flex-end; align-items: center; }
  .close {
    background: transparent;
    border: 1px solid;
    cursor: pointer;
    font-size: 14.5px;
    line-height: 1;
    padding: 4px 7px;
    margin-left: 4px;
  }
  .body { overflow: auto; padding: 14px 18px 18px; }
  /* Phones: the modal takes the whole screen so its content and close
     button are never clipped by the cramped viewport. */
  @media (max-width: 760px) {
    .overlay { padding: 0; }
    .modal { width: 100%; max-height: 100dvh; height: 100dvh; }
  }
</style>

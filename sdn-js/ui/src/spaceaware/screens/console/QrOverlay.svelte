<script lang="ts">
  /**
   * QR export overlay (loop U3.1 — structure + open/close only). Ground
   * truth: the `<!-- QR overlay -->` block in `SDN Console.dc.html` — a
   * full-bleed backdrop with a centered panel holding an 11x11 cell grid.
   *
   * The mock's `onClick={{ onToggleQr }}` handler lives ONLY on the outer
   * backdrop `<div>`; the inner panel never calls `stopPropagation`, so a
   * click anywhere (backdrop OR panel) bubbles up and closes it — the
   * caption literally says "click anywhere to close". We replicate that by
   * putting `onclick` on the outer element only and letting it bubble.
   *
   * Cell content is `generateQrPlaceholderPattern()` (lib/console.ts) — a
   * deterministic visual placeholder. Encoding the real EPM/vCARD payload
   * (`GET /api/node/epm/qr`) is loop task U3.2.
   */
  import { generateQrPlaceholderPattern } from '../../lib/console';

  let { open, onClose }: { open: boolean; onClose: () => void } = $props();

  const cells = generateQrPlaceholderPattern(11);
</script>

{#if open}
  <div
    class="sdn-qr-backdrop"
    onclick={onClose}
    role="button"
    tabindex="0"
    title="Close"
    onkeydown={(e) => {
      if (e.key === 'Enter' || e.key === ' ') onClose();
    }}
  >
    <div class="sdn-qr-panel">
      <div class="sdn-qr-kicker">EPM / vCARD · QR EXPORT</div>
      <div class="sdn-qr-grid">
        {#each cells as on, i (i)}
          <span class="sdn-qr-cell" class:is-on={on}></span>
        {/each}
      </div>
      <div class="sdn-qr-caption">Scan to import EPM · click anywhere to close</div>
    </div>
  </div>
{/if}

<style>
  .sdn-qr-backdrop {
    position: absolute;
    inset: 0;
    z-index: 60;
    background: rgba(4, 6, 10, 0.78);
    backdrop-filter: blur(3px);
    -webkit-backdrop-filter: blur(3px);
    display: flex;
    align-items: center;
    justify-content: center;
    cursor: pointer;
    border: none;
    padding: 0;
  }

  .sdn-qr-panel {
    background: linear-gradient(178deg, #16252f, #0a141b);
    border: 1px solid rgba(120, 190, 230, 0.4);
    padding: 22px;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 14px;
    box-shadow: 0 24px 60px rgba(0, 0, 0, 0.7);
  }

  .sdn-qr-kicker {
    font-size: 11px;
    letter-spacing: 0.2em;
    color: #5a7a8a;
  }

  .sdn-qr-grid {
    width: 180px;
    height: 180px;
    background: #eaf6f8;
    display: grid;
    grid-template-columns: repeat(11, 1fr);
    grid-template-rows: repeat(11, 1fr);
    padding: 10px;
    gap: 2px;
  }

  .sdn-qr-cell {
    background: transparent;
  }

  .sdn-qr-cell.is-on {
    background: #0a141b;
  }

  .sdn-qr-caption {
    font-size: 11px;
    color: #7d929b;
  }
</style>

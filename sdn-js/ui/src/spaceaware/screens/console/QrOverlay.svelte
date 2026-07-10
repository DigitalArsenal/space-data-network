<script lang="ts">
  /**
   * QR export overlay (loop U3.1 structure, loop U3.2 real encoding).
   * Ground truth: the `<!-- QR overlay -->` block in `SDN Console.dc.html`
   * — a full-bleed backdrop with a centered panel holding an 11x11 cell
   * grid.
   *
   * The mock's `onClick={{ onToggleQr }}` handler lives ONLY on the outer
   * backdrop `<div>`; the inner panel never calls `stopPropagation`, so a
   * click anywhere (backdrop OR panel) bubbles up and closes it — the
   * caption literally says "click anywhere to close". We replicate that by
   * putting `onclick` on the outer element only and letting it bubble.
   *
   * `GET /api/node/epm/qr` 500s ("content too long to encode") on this
   * build — a real server gap, not something this overlay can paper over
   * by retrying. `sdn-js` already depends on the `qrcode` package (see
   * `ui/src/components/IdentityPanel.svelte`'s established lazy-import
   * pattern, reused by `lib/node-data.ts`'s `encodeQrDataUrl`), so instead
   * this fetches the node's own `GET /api/node/epm/vcard` text on first
   * open and encodes a REAL scannable QR client-side from it. A real QR
   * needs a standard module grid (21x21 minimum, larger for a vCard-length
   * payload) which the mock's fixed 11x11 decorative grid was never sized
   * for, so the encoded PNG replaces the cell grid as a single image once
   * it's ready; the original deterministic `generateQrPlaceholderPattern()`
   * grid (`lib/console.ts`) stays as the loading/offline fallback so the
   * panel is never blank while fetching, and never shows a broken image on
   * failure — an honest placeholder, not a fabricated "success" state.
   */
  import { generateQrPlaceholderPattern } from '../../lib/console';
  import { encodeQrDataUrl, fetchVCardText } from '../../lib/node-data';

  let { open, onClose }: { open: boolean; onClose: () => void } = $props();

  const cells = generateQrPlaceholderPattern(11);

  let qrDataUrl = $state<string | null>(null);
  let qrRequested = false;

  // Fetch + encode once per mounted lifetime of this overlay (it stays
  // mounted with `open` toggling internal visibility — see ConsoleShell.svelte
  // — so this only ever runs the first time it's opened).
  $effect(() => {
    if (open && !qrRequested) {
      qrRequested = true;
      void loadRealQr();
    }
  });

  async function loadRealQr(): Promise<void> {
    const vcard = await fetchVCardText();
    if (!vcard) return; // honest: keep the decorative placeholder grid, never fabricate a QR image.
    qrDataUrl = await encodeQrDataUrl(vcard);
  }
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
        {#if qrDataUrl}
          <img class="sdn-qr-image" src={qrDataUrl} alt="EPM vCard QR code" />
        {:else}
          {#each cells as on, i (i)}
            <span class="sdn-qr-cell" class:is-on={on}></span>
          {/each}
        {/if}
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

  /* Real encoded QR (loop U3.2) — replaces the decorative cell grid inside
     the same fixed 180x180 `.sdn-qr-grid` box once `encodeQrDataUrl`
     resolves, so the panel's outer pixel dimensions are unaffected. */
  .sdn-qr-image {
    /* The parent keeps its 11-col grid for the fallback cells — without an
       explicit spanning area the img lands in ONE 15px cell. */
    grid-area: 1 / 1 / -1 / -1;
    width: 100%;
    height: 100%;
    display: block;
  }

  .sdn-qr-caption {
    font-size: 11px;
    color: #7d929b;
  }
</style>

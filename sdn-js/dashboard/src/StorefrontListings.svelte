<script>
  /**
   * STOREFRONT · LISTINGS — what this node offers, as the node itself lists it.
   *
   * Source: `GET /api/storefront/listings` (sdn-server/internal/storefront/api.go:67),
   * which is optional-auth: an anonymous visitor gets the public catalogue and a
   * signed-in operator gets whatever their session additionally admits. Nothing
   * on this surface is synthesized — a listing with no pricing tiers shows no
   * price, and a node with no listings says exactly that.
   *
   * These keys are LOWERCASE snake_case on purpose: they are API-synthesized
   * fields, not SDS record fields (standing capitalization rule — the $PMM read
   * one tab over is the uppercase one, and the two conventions are deliberate).
   *
   * Styled with theme.js tokens only.
   */
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import StatusChip from 'spaceaware-student-sdn/src/lib/components/StatusChip.svelte';
  import { apiFetch, describeApiError } from './api.js';
  import { shortId } from './format.js';

  /** @type {{ fetchNow?: () => Promise<any> }} */
  let { fetchNow = undefined } = $props();

  /** `null` = not answered yet; `[]` = the node really has none (IRIS D2). */
  let listings = $state(null);
  let error = $state('');

  const KIND_LABEL = { data_stream: 'DATA STREAM', wasm_module: 'MODULE' };

  /**
   * The node encodes AccessType as an ORDINAL (types.go:8-15), so the words come
   * from the enum's order, and an unrecognised ordinal renders NOTHING rather
   * than the number: a bare `4` on a listing row is not a fact an operator can
   * use.
   */
  const ACCESS_LABEL = ['ONE-TIME', 'SUBSCRIPTION', 'STREAMING', 'QUERY'];

  /**
   * The cheapest stated tier, in that tier's own currency and units. The node
   * sends an integer `price_amount` with no scale, so it is shown VERBATIM
   * beside its currency — dividing by 100 here would be this page inventing a
   * denomination the API never stated.
   */
  function priceLabel(listing) {
    const tiers = Array.isArray(listing?.pricing) ? listing.pricing : [];
    if (!tiers.length) return '';
    const cheapest = [...tiers].sort((a, b) => Number(a?.price_amount ?? 0) - Number(b?.price_amount ?? 0))[0];
    const amount = Number(cheapest?.price_amount ?? 0);
    if (!Number.isFinite(amount)) return '';
    if (amount === 0) return 'FREE';
    const currency = String(cheapest?.price_currency ?? '').trim().toUpperCase();
    return currency ? `${amount} ${currency}` : String(amount);
  }

  async function read() {
    try {
      const res = fetchNow ? await fetchNow() : await apiFetch('/api/storefront/listings');
      const rows = Array.isArray(res?.listings) ? res.listings : Array.isArray(res) ? res : [];
      listings = rows;
      error = '';
    } catch (err) {
      listings = [];
      error = describeApiError(err);
    }
  }

  $effect(() => {
    read();
  });
</script>

<div class="listings">
  {#if error}
    <div class="err" style="color:{theme.amber};border-color:{theme.amber};">{error}</div>
  {/if}

  {#if listings === null}
    <div class="none" style="color:{theme.textFaint};">Reading the storefront…</div>
  {:else if !listings.length}
    <!-- IRIS 2026-07-31, verbatim. "Yet" is doing real work: this node CAN
         publish listings, and none is not a defect. -->
    <div class="none" style="color:{theme.textFaint};">No storefront listings are published yet.</div>
  {:else}
    <ul class="rows" style="border-color:{theme.hairline};">
      {#each listings as listing (listing.listing_id)}
        <li style="border-color:{theme.divider};">
          <div class="line">
            <span class="title" style="color:{theme.textBright};">{listing.title?.trim() || listing.listing_id}</span>
            {#if KIND_LABEL[listing.listing_kind]}
              <span class="badge" style="color:{theme.cyan};border-color:{theme.cyan};">{KIND_LABEL[listing.listing_kind]}</span>
            {/if}
            {#if ACCESS_LABEL[Number(listing.access_type)]}
              <span class="badge" style="color:{theme.textDim};border-color:{theme.hairline};">{ACCESS_LABEL[Number(listing.access_type)]}</span>
            {/if}
            {#if priceLabel(listing)}
              <span class="price" style="color:{theme.ice};">{priceLabel(listing)}</span>
            {/if}
            {#if listing.active === false}
              <span class="badge" style="color:{theme.amber};border-color:{theme.amber};">INACTIVE</span>
            {/if}
          </div>
          {#if listing.description?.trim()}
            <div class="desc" style="color:{theme.textDim};">{listing.description}</div>
          {/if}
          <div class="meta mono" style="color:{theme.textFaint};">
            {#if listing.provider_peer_id}PROVIDER {shortId(listing.provider_peer_id)}{/if}
            {#if listing.source_peer_id}<span class="sep"> · </span>DISCOVERED FROM {shortId(listing.source_peer_id)}{/if}
            {#if Array.isArray(listing.data_types) && listing.data_types.length}
              <span class="sep"> · </span>{listing.data_types.join(' ')}
            {/if}
          </div>
        </li>
      {/each}
    </ul>
    <div class="count" style="color:{theme.textMuted};">
      <StatusChip label={`${listings.length} LISTED`} color={theme.ice} dot={false} />
    </div>
  {/if}
</div>

<style>
  .listings { min-width: 0; }
  .err { border: 1px solid; padding: 8px 11px; font-size: var(--sdn-fs-label); line-height: var(--sdn-lh-label); margin-bottom: 10px; }
  .none { font-size: var(--sdn-fs-value); line-height: var(--sdn-lh-value); padding: 18px 0; }
  .rows { list-style: none; margin: 0; padding: 0; border: 1px solid; }
  .rows li { padding: 9px 11px; border-bottom: 1px solid; }
  .rows li:last-child { border-bottom: 0; }
  .line { display: flex; align-items: baseline; gap: 8px; flex-wrap: wrap; }
  .title { font-family: 'Chakra Petch', sans-serif; font-weight: 600; font-size: var(--sdn-fs-note); line-height: var(--sdn-lh-note); letter-spacing: 0.04em; }
  .badge { border: 1px solid; font-size: var(--sdn-fs-micro); line-height: var(--sdn-lh-micro); letter-spacing: 0.14em; padding: 1px 5px; }
  .price { font-size: var(--sdn-fs-label); line-height: var(--sdn-lh-label); letter-spacing: 0.08em; }
  .desc { font-size: var(--sdn-fs-label); line-height: var(--sdn-lh-label); margin-top: 3px; }
  .meta { font-size: var(--sdn-fs-micro); line-height: var(--sdn-lh-micro); margin-top: 3px; overflow-wrap: anywhere; }
  .count { margin-top: 10px; }
  .mono { font-family: 'IBM Plex Mono', ui-monospace, monospace; }
</style>

<script>
  /**
   * THE pager — one implementation, for every surface that overflows.
   *
   * GRAMMAR LAW L4 (graph task iris-dashboard-grammar-law, owner directive
   * 2026-07-30: "the widgets should not scroll, they should be responsive and be
   * paginated if there is overflow"): vertical overflow paginates, never scrolls,
   * and it does so THROUGH THIS COMPONENT. IRIS's condition on wave 2 was
   * explicit — extract the ACCOUNTS pager before the ACTIVITY LOG widget uses it,
   * because "two pager implementations is how 'widgets paginate' becomes 'widgets
   * mostly paginate'".
   *
   * It owns no data and no page state: the caller holds `page` and clamps it.
   * That keeps this file free of the "which page am I on after the rows changed"
   * decision, which is different for a filtered table (snap to 0) than for a live
   * ring (hold position).
   */
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';

  /**
   * @type {{
   *   page: number, pageCount: number, total: number,
   *   pageSize: number, unit?: string, onPage: (page: number) => void,
   *   compact?: boolean,
   * }}
   */
  let { page, pageCount, total, pageSize, unit = 'ROWS', onPage, compact = false } = $props();

  const from = $derived(total ? page * pageSize + 1 : 0);
  const to = $derived(Math.min((page + 1) * pageSize, total));
</script>

<div class="pager" class:compact style="border-color:{theme.divider};color:{theme.textMuted};">
  <span>{unit} {from}–{to} OF {total}</span>
  <span class="pager-ctl">
    <button
      type="button"
      style="color:{theme.ice};border-color:{theme.hairline};"
      disabled={page === 0}
      onclick={() => onPage(page - 1)}
    >‹ PREV</button>
    <span>PAGE {page + 1}/{pageCount}</span>
    <button
      type="button"
      style="color:{theme.ice};border-color:{theme.hairline};"
      disabled={page >= pageCount - 1}
      onclick={() => onPage(page + 1)}
    >NEXT ›</button>
  </span>
</div>

<style>
  /* Lifted verbatim from App.svelte's `.pager`, which is the shape the owner has
     already accepted on the ACCOUNTS table — the extraction is not a redesign. */
  .pager {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    flex-wrap: wrap;
    border-top: 1px solid;
    padding: var(--sdn-sp-5) var(--sdn-sp-9);
    font-size: var(--sdn-fs-body);
    line-height: var(--sdn-lh-body);
    letter-spacing: 0.14em;
  }
  /* Inside a 4-of-12 widget there is no room for the table's gutter, and the
     pager sits on the panel's own padding instead. Same control, same rungs. */
  .pager.compact {
    padding: var(--sdn-sp-4) 0 0;
    letter-spacing: 0.08em;
    font-size: var(--sdn-fs-label);
    line-height: var(--sdn-lh-label);
    margin-top: auto;
  }
  .pager-ctl { display: inline-flex; align-items: center; gap: var(--sdn-sp-5); }
  .pager.compact .pager-ctl { gap: var(--sdn-sp-3); }
  .pager button {
    background: transparent;
    border: 1px solid;
    cursor: pointer;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: var(--sdn-fs-body);
    line-height: var(--sdn-lh-body);
    letter-spacing: 0.12em;
    padding: var(--sdn-sp-3) calc(var(--sdn-sp-4) + 0.12em) var(--sdn-sp-3) var(--sdn-sp-4);
    min-height: 40px;
  }
  /* L6: a control never shrinks below its label's rung. In the compact form the
     box gets smaller; the type stays on the ladder. */
  .pager.compact button {
    font-size: var(--sdn-fs-label);
    line-height: var(--sdn-lh-label);
    padding: var(--sdn-sp-1) var(--sdn-sp-4);
    min-height: 28px;
  }
  .pager button:disabled { opacity: 0.35; cursor: default; }
</style>

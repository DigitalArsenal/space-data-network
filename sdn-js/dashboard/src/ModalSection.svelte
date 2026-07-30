<script>
  /**
   * One labelled block inside a ModalShell body. Exists so the three
   * management dialogs (operator, peer, account) cannot drift into three
   * different section grammars — IRIS ruled the section ORDER, which only
   * means anything if the sections look the same.
   *
   * `danger` draws the block in red: the DANGER ZONE is always last and always
   * visibly separated (IRIS §3.6).
   */
  import Kicker from 'spaceaware-student-sdn/src/lib/components/Kicker.svelte';
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';

  /** @type {{ title: string, note?: string, danger?: boolean, children?: any }} */
  let { title, note = '', danger = false, children } = $props();
</script>

<section class="sect" class:danger style="border-color:{danger ? theme.red : theme.divider};">
  <Kicker text={title} />
  {#if note}
    <p class="note" style="color:{theme.textDim};">{note}</p>
  {/if}
  <div class="content">
    {#if children}{@render children()}{/if}
  </div>
</section>

<style>
  .sect {
    border-top: 1px solid;
    padding-top: 12px;
    display: flex;
    flex-direction: column;
    gap: 9px;
  }
  .sect:first-of-type { border-top: 0; padding-top: 0; }
  .sect.danger { margin-top: 6px; padding-top: 14px; }
  .note { margin: 0; font-size: 13px; line-height: 1.55; letter-spacing: 0.02em; }
  .content { display: flex; flex-direction: column; gap: 9px; min-width: 0; }
</style>

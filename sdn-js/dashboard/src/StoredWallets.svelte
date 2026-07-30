<script>
  /**
   * STORED WALLETS — the custody duty §15.1 attaches to declining the
   * quarantine capability at sign-in.
   *
   * The node's removed 2.0.6 `/login` page (the unpkg CDN fallback deleted in
   * §12.1) wrote a REAL encrypted wallet and a REAL passkey credential into
   * localStorage on this origin, and that removal orphaned them: no surface on
   * the node can unlock them any more. Their export/delete affordance must
   * exist SOMEWHERE — just never inside a sign-in transaction — so it lives
   * here, on the signed-in node page.
   *
   * Presence-based exactly like the wallet's own detector: a key that is not
   * in storage produces no row, and this widget renders nothing at all when
   * the origin is clean. Nothing is decrypted or interpreted — the values are
   * opaque and are handed back verbatim.
   */
  import Panel from 'spaceaware-student-sdn/src/lib/components/Panel.svelte';
  import StatusChip from 'spaceaware-student-sdn/src/lib/components/StatusChip.svelte';
  import Kick from './Kick.svelte';
  import GBtn from 'spaceaware-student-sdn/src/lib/components/GBtn.svelte';
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import { readQuarantinedRecords } from './walletui.js';

  let records = $state(readQuarantinedRecords());
  let confirming = $state('');

  function exportAll() {
    const payload = JSON.stringify(
      Object.fromEntries(records.map((r) => [r.key, r.value])),
      null,
      2
    );
    const a = document.createElement('a');
    a.href = URL.createObjectURL(new Blob([payload], { type: 'application/json' }));
    a.download = `sdn-stored-wallets-${location.hostname}.json`;
    a.click();
    setTimeout(() => URL.revokeObjectURL(a.href), 5000);
  }

  function remove(key) {
    if (confirming !== key) {
      confirming = key;
      return;
    }
    try {
      globalThis.localStorage?.removeItem(key);
    } catch {
      /* private mode — nothing to remove */
    }
    confirming = '';
    records = readQuarantinedRecords();
  }
</script>

{#if records.length}
  <div class="wide">
    <Panel variant="raised" pad="0">
      <div class="w">
        <div class="whead" style="border-color:{theme.divider};">
          <Kick text="THIS BROWSER, THIS ORIGIN" />
          <div class="wtitle" style="color:{theme.textBright};">STORED WALLETS</div>
          <div class="wchips">
            <StatusChip label={`${records.length} ORPHANED`} color={theme.amber} dot={false} />
            <GBtn title="Download these records before deleting them" onclick={exportAll}>↓ EXPORT ALL</GBtn>
          </div>
        </div>
        <div class="wbody">
          <p class="prose" style="color:{theme.textDim};">
            A previous version of this node's sign-in page stored an encrypted wallet in this
            browser. Nothing here can unlock it any more. Export it if you still need it, then
            delete it — it is never read during sign-in.
          </p>
          <ul class="list" style="border-color:{theme.hairline};">
            {#each records as r (r.key)}
              <li style="border-color:{theme.divider};">
                <span class="mono key" style="color:{theme.ice};">{r.key}</span>
                <span class="mono bytes" style="color:{theme.textFaint};">{r.bytes} bytes</span>
                <span class="act">
                  <GBtn
                    title={confirming === r.key ? 'Click again to permanently delete' : 'Delete this stored record'}
                    variant="destructive"
                    onclick={() => remove(r.key)}
                  >{confirming === r.key ? 'CONFIRM DELETE' : 'DELETE'}</GBtn>
                </span>
              </li>
            {/each}
          </ul>
        </div>
      </div>
    </Panel>
  </div>
{/if}

<style>
  .wide { grid-column: 1 / -1; min-width: 0; }
  .wide :global(> section) { height: 100%; }
  .w { display: flex; flex-direction: column; min-width: 0; }
  .whead {
    display: flex;
    align-items: baseline;
    gap: 10px;
    flex-wrap: wrap;
    padding: 13px 16px 11px;
    border-bottom: 1px solid;
  }
  .wtitle {
    font-family: 'Chakra Petch', sans-serif;
    font-weight: 600;
    font-size: var(--sdn-fs-lead); line-height: var(--sdn-lh-lead);
    letter-spacing: 0.1em;
    margin-right: auto;
  }
  .wchips { display: flex; gap: 8px; flex-wrap: wrap; align-items: center; }
  .wbody { padding: 12px 16px 15px; min-width: 0; }
  .prose { margin: 0 0 12px; font-size: var(--sdn-fs-data); line-height: var(--sdn-lh-data); }
  .list { list-style: none; margin: 0; padding: 0; border: 1px solid; }
  .list li {
    display: flex;
    align-items: center;
    gap: 12px;
    flex-wrap: wrap;
    padding: 8px 10px;
    border-bottom: 1px solid;
  }
  .list li:last-child { border-bottom: 0; }
  .key { font-size: var(--sdn-fs-data); line-height: var(--sdn-lh-data); overflow-wrap: anywhere; min-width: 0; flex: 1 1 220px; }
  .bytes { font-size: var(--sdn-fs-body); line-height: var(--sdn-lh-body); white-space: nowrap; }
  .act { margin-left: auto; }
  .mono { font-family: 'IBM Plex Mono', ui-monospace, monospace; }

  @media (max-width: 760px) {
    .list li { align-items: stretch; flex-direction: column; gap: 6px; }
    .act { margin-left: 0; }
  }
</style>

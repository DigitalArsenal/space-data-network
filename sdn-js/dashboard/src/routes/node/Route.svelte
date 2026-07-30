<script>
  /**
   * THE NODE DASHBOARD ROUTE — the SDN Console template's view, with EDIT LAYOUT
   * and the full eight-widget registry (IRIS §2/§3 wave 2).
   *
   * This route owns exactly one piece of state: whether the inline profile form
   * is open. Everything it renders comes from the shell context, and the widget
   * grid it hands to NodeConsole is discovered from `widgets/` — so a widget task
   * touches neither this file nor App.svelte.
   */
  import Panel from 'spaceaware-student-sdn/src/lib/components/Panel.svelte';
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import NodeConsole from '../../NodeConsole.svelte';
  import NodeEditForm from '../../NodeEditForm.svelte';

  let {
    nodes = [],
    selfNode = null,
    runtime,
    now = 0,
    canEdit = false,
    onSelectNode = () => {},
    onShowSelf = () => {},
    onProfileSaved = () => {},
  } = $props();

  let editing = $state(false);
</script>

{#if selfNode}
  {#if editing}
    <!-- The IDENTITY card's EDIT opens the profile form INLINE, in a raised panel
         on this page — "less like a modal, more like a page" (owner 2026-07-27;
         IRIS §6). -->
    <Panel variant="raised" pad="0" style="max-width:880px;">
      <div class="self-body">
        <NodeEditForm
          onCancel={() => (editing = false)}
          onSaved={(payload) => {
            onProfileSaved(payload);
            editing = false;
          }}
        />
      </div>
    </Panel>
  {:else}
    <div class="node-page">
      <NodeConsole
        node={selfNode}
        {nodes}
        {runtime}
        {now}
        {canEdit}
        onSelectNode={onSelectNode}
        onEdit={() => (editing = true)}
        onShowDetail={() => onShowSelf('parsed')}
        onShowQr={() => onShowSelf('qr')}
      />
    </div>
  {/if}
{:else}
  <div class="empty" style="color:{theme.textDim};border-color:{theme.hairline};">
    <span class="glyph" style="color:{theme.cyan};">◉</span>
    Waiting for this node's status entry…
  </div>
{/if}

<style>
  .self-body { padding: 14px 18px 18px; }
  /* The NODE dashboard takes its natural height and lets .body scroll — the
     widget grid is the page, so a nested scroller would trap the last row. */
  .node-page { flex: none; min-width: 0; padding-bottom: 8px; }
  .empty {
    display: flex;
    align-items: center;
    gap: 12px;
    border: 1px solid;
    padding: 26px 28px;
    font-size: var(--sdn-fs-lead); line-height: var(--sdn-lh-lead);
    letter-spacing: 0.06em;
    max-width: 560px;
  }
  .empty .glyph { font-size: var(--sdn-fs-title); line-height: var(--sdn-lh-title); }
</style>

<script>
  /**
   * The NODE route's DASHBOARD SHELL — the SDN Console template's grid, its EDIT
   * LAYOUT chrome and its ADD tray. It renders whatever the widget registry
   * discovered; it knows nothing about any individual widget.
   *
   * DESIGN SOURCE (declared, not copied):
   *   SpaceAware-UI @ archive/SpaceAware.io 2/SDN Console.dc.html
   *   sha256 abacdbfc62aeaee1193eccec9087669bfeb2324422fe8223482556fad207f152
   *   layout :113 · registry :862-873 · edit chrome :105-117
   *   · ADD WIDGET :250-258 · mutators :883-889 · setInteractive :1020,1049
   *
   * WHAT THIS FILE USED TO BE (sdn-dashboard-modularize-for-parallelism): 952
   * lines, of which ~285 were an `{#if w.id === 'health'} … {:else if w.id ===
   * …}` chain over EIGHT branches plus the union of all eight widgets' CSS. That
   * chain was the reason two agents could not build two widgets at once — every
   * widget task edited this one file. Each arm now lives in
   * `widgets/<id>/Widget.svelte` with its own styles, and the chain is a lookup.
   *
   * THE WIDGET CONTRACT. Every widget receives the same context object, spread
   * as props, and declares in its own `$props()` which parts of it it reads:
   *
   *   node          this node's status entry (public feed)
   *   nodes         every node the feed knows about
   *   runtime       the folded runtime snapshot (runtime.js); `privileged` is
   *                 true once the ADMIN snapshot has actually been read
   *   now           the page clock, ms
   *   canEdit       this session may edit the node's published profile
   *   editMode      EDIT LAYOUT is on (netmap uses it to stop the globe eating
   *                 the drag; most widgets ignore it)
   *   onSelectNode  open a peer's detail modal
   *   onEdit        open the inline profile form
   *   onShowDetail  open THIS node's detail modal on its fields
   *   onShowQr      … and on its scannable QR
   *
   * Adding a field here is a DATA task and touches this file. Adding a WIDGET is
   * not, and does not.
   *
   * EDIT LAYOUT IS ADMIN-ONLY — not on permissions grounds but on coherence:
   * renderLayout() computes the public layout for an anonymous viewer and
   * ignores what is stored, so an anonymous edit would persist into a view the
   * renderer discards (IRIS R3).
   */
  import Panel from 'spaceaware-student-sdn/src/lib/components/Panel.svelte';
  import GBtn from 'spaceaware-student-sdn/src/lib/components/GBtn.svelte';
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import {
    addWidget,
    availableWidgets,
    cycleSpan,
    moveWidget,
    readLayout,
    removeWidget,
    renderLayout,
    resetLayout,
    writeLayout,
  } from './node-layout.js';
  import { WIDGET_COMPONENTS } from './widgets/components.js';

  /**
   * @type {{
   *   node: any, nodes?: any[], runtime: any, now?: number, canEdit?: boolean,
   *   onSelectNode?: (node: any) => void, onEdit?: () => void,
   *   onShowQr?: () => void, onShowDetail?: () => void,
   * }}
   */
  let {
    node,
    nodes = [],
    runtime,
    now = 0,
    canEdit = false,
    onSelectNode = () => {},
    onEdit = () => {},
    onShowQr = () => {},
    onShowDetail = () => {},
  } = $props();

  // ---- LAYOUT + EDIT LAYOUT ------------------------------------------------
  const privileged = $derived(Boolean(runtime?.privileged));
  /** The operator's saved arrangement. Read once — localStorage is not reactive. */
  let stored = $state(readLayout(globalThis.localStorage));
  let editMode = $state(false);
  const layout = $derived(renderLayout(privileged, stored));
  const available = $derived(availableWidgets(layout));
  /** Signing out ends the edit session with the view it was editing. */
  $effect(() => {
    if (!privileged && editMode) editMode = false;
  });

  /** Persist and re-render in one step, exactly as the design's setLayout does. */
  function commit(next) {
    stored = next;
    writeLayout(globalThis.localStorage, next);
  }

  /** The widget currently being dragged (`_dragId` in the export). */
  let dragId = $state('');
  const onDragEnter = (id) => {
    if (!editMode || !dragId) return;
    commit(moveWidget(layout, dragId, id));
  };

  /** The one seam every widget reads. See THE WIDGET CONTRACT above. */
  const ctx = $derived({
    node,
    nodes,
    runtime,
    now,
    canEdit,
    editMode,
    onSelectNode,
    onEdit,
    onShowQr,
    onShowDetail,
  });
</script>

<div class="dash">
  <!-- The template's page kicker and its EDIT LAYOUT control (`:102-110`). The
       control exists only for an Admin (IRIS R3); an anonymous viewer sees the
       kicker alone, exactly as wave 1 shipped. -->
  <div class="dashhead">
    <span class="kick" style="color:{theme.textMuted};">DASHBOARD</span>
    <span class="spacer"></span>
    <!-- No instruction caption. Every affordance it narrated is VISIBLE in edit
         mode: the ⤢ and ✕ handles are drawn on each widget and the widgets drag.
         Owner directive 2026-07-30 (twice) — a control strip is its controls. -->
    {#if privileged && editMode}
      <GBtn title="Restore the default arrangement" onclick={() => commit(resetLayout())}>RESET</GBtn>
    {/if}
    {#if privileged}
      <GBtn
        title={editMode ? 'Finish editing the layout' : 'Rearrange, resize, add or remove widgets'}
        variant={editMode ? 'primary' : 'neutral'}
        onclick={() => (editMode = !editMode)}
      >{editMode ? 'DONE' : 'EDIT LAYOUT'}</GBtn>
    {/if}
  </div>

  <div class="grid">
    {#each layout as w (w.id)}
      <!-- The registry lookup that replaced the eight-branch chain. It sits here
           rather than beside the render because `{@const}` must be the immediate
           child of a block. -->
      {@const Widget = WIDGET_COMPONENTS[w.id]}
      <Panel
        variant="raised"
        style="position:relative;grid-column:span {w.span};min-width:0;{editMode
          ? `border:1px dashed ${theme.ice};cursor:move;`
          : ''}"
      >
        <!-- Every widget is a full-height flex column so its terminal block can
             compose to the PANEL FLOOR (IRIS condition C4). Grid rows are as tall
             as their tallest member, and without this the shorter widgets in a
             row were top-packed with a 90-180px void underneath, which the
             export never shows. -->
        <!-- svelte-ignore a11y_no_static_element_interactions -->
        <div
          class="w"
          draggable={editMode}
          ondragstart={() => (dragId = w.id)}
          ondragenter={() => onDragEnter(w.id)}
          ondragover={(e) => editMode && e.preventDefault()}
          ondragend={() => (dragId = '')}
        >
          {#if editMode}
            <!-- The export's per-widget edit chrome (`:117-122`): a drag grip, a
                 span cycle showing the span it will take, and a remove. -->
            <div class="editbar" style="background:{theme.panelRaised};border-color:{theme.panelBorder};">
              <span class="grip" style="color:{theme.textMuted};" title="Drag to reorder">⠿</span>
              <button
                class="ebtn"
                style="color:{theme.ice};border-color:{theme.ice};"
                title="Resize"
                onclick={() => commit(cycleSpan(layout, w.id))}
              >⤢ W{w.span}</button>
              <button
                class="ebtn del"
                style="color:{theme.red};border-color:{theme.red};"
                title="Remove"
                onclick={() => commit(removeWidget(layout, w.id))}
              >✕</button>
            </div>
          {/if}

          <!-- A layout entry whose component is missing renders NOTHING rather
               than throwing: sanitizeLayout already rejects unknown ids, so this
               can only be a widget directory with metadata and no component, and
               a half-built widget must not take the whole dashboard down. -->
          {#if Widget}
            <Widget {...ctx} />
          {/if}
        </div>
      </Panel>
    {/each}
  </div>

  {#if privileged && editMode && available.length}
    <!-- ADD WIDGET (`:250-258`): only what is not already placed. -->
    <div class="addtray" style="border-color:{theme.panelBorder};">
      <div class="wkick" style="color:{theme.textMuted};">ADD WIDGET</div>
      <div class="addrow">
        {#each available as w (w.id)}
          <GBtn title={`Add the ${w.title} widget`} onclick={() => commit(addWidget(layout, w.id))}>
            + {w.title}
          </GBtn>
        {/each}
      </div>
    </div>
  {/if}
</div>

<style>
  .dash { min-width: 0; }
  .dashhead {
    display: flex;
    align-items: center;
    gap: var(--sdn-sp-5);
    margin-bottom: var(--sdn-sp-6);
  }
  .dashhead .spacer { flex: 1; }
  .kick {
    font-size: var(--sdn-fs-micro);
    line-height: var(--sdn-lh-micro);
    letter-spacing: 0.2em;
  }

  /* The template's grid (`:113`): 12 columns, dense row flow, min-content rows.
     The gap is the ladder's rung nearest the design's 14px under the
     sub-proportional spacing rule (scale.css). */
  .grid {
    display: grid;
    grid-template-columns: repeat(12, minmax(0, 1fr));
    grid-auto-rows: min-content;
    grid-auto-flow: row dense;
    gap: var(--sdn-sp-7);
    align-content: start;
    min-width: 0;
  }

  /* The full-height widget column (C4). `height:100%` resolves because a grid
     item is stretched to its row, which is the same arrangement each widget
     relies on inside Panel. */
  .w { display: flex; flex-direction: column; height: 100%; min-width: 0; }

  /* EDIT LAYOUT chrome (`:117-122`). Absolutely positioned so turning edit mode
     on cannot change a widget's height — a layout that reflows the moment you
     start editing it is a layout you cannot aim at. */
  .editbar {
    position: absolute;
    top: 6px;
    right: 6px;
    z-index: 6;
    display: flex;
    align-items: center;
    gap: var(--sdn-sp-1);
    border: 1px solid;
    padding: 2px 3px;
  }
  .grip { cursor: move; font-size: var(--sdn-fs-body); line-height: 1; }
  .ebtn {
    background: transparent;
    border: 1px solid;
    cursor: pointer;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    /* L6: control chrome never renders below its own label's rung. */
    font-size: var(--sdn-fs-label);
    line-height: var(--sdn-lh-label);
    letter-spacing: 0.04em;
    padding: 1px var(--sdn-sp-2);
  }

  .addtray {
    margin-top: var(--sdn-sp-7);
    border: 1px dashed;
    padding: var(--sdn-sp-5) var(--sdn-sp-7);
  }
  .addrow { display: flex; flex-wrap: wrap; gap: var(--sdn-sp-3); margin-top: var(--sdn-sp-4); }

  /* The ADD tray's own kicker. Every other `.wkick` on this page now belongs to
     the widget that prints it, in that widget's own directory. */
  .wkick {
    font-size: var(--sdn-fs-micro);
    line-height: var(--sdn-lh-micro);
    letter-spacing: 0.2em;
    display: block;
  }

  /* Below the 12-column grid's usable width every widget takes the full row —
     a 4-of-12 panel at 900px is narrower than the peer id it has to print. */
  @media (max-width: 1180px) {
    .grid > :global(*) { grid-column: span 12 !important; }
  }
</style>

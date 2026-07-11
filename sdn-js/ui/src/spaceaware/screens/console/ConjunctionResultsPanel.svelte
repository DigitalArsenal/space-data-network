<script lang="ts">
  /**
   * "SCREENING RESULTS" panel (loop task U3.9). Ground truth:
   * `SDN Console.dc.html:697-721` — a TABLE/JSON/CSV mode toggle and either
   * a fixed OBJECT/TCA/MISS km/Pc/STATE grid (TABLE) or a scrollable mono
   * code block (JSON/CSV). See `ConjunctionView.svelte`'s doc comment and
   * `../../lib/conjunction-data.ts` for why this whole panel is DEMO data
   * (no conjunction-screening engine exists on this build) — hence the
   * DEMO tag next to the panel kicker, styled like `GroupsView.svelte`'s
   * `.sdn-groups-demo-tag`.
   *
   * TABLE mode renders the mock's pixel-exact fixed-column grid (this
   * fixture always has the same 5 columns, so unlike the U3.6 query
   * workbench's dynamic-column table this doesn't need `buildQueryTableColumns`).
   * JSON/CSV modes render `resultJson`/`resultCsv`, both built in
   * `conjunction-data.ts` by reusing the U3.6 schema-exact passthrough
   * builders (`buildQueryJsonOutput`/`buildQueryCsvOutput`).
   */
  import { CONJUNCTION_RESULTS_DEMO_TAG_TITLE, type ConjunctionResultRowView } from '../../lib/conjunction-data';
  import { QUERY_OUTPUT_MODES, queryOutputModeStyle, type QueryOutputMode } from '../../lib/query-data';

  let {
    resultMode,
    onSetMode,
    resultRows,
    resultJson,
    resultCsv,
  }: {
    resultMode: QueryOutputMode;
    onSetMode: (mode: QueryOutputMode) => void;
    resultRows: ConjunctionResultRowView[];
    resultJson: string;
    resultCsv: string;
  } = $props();
</script>

<section class="sdn-conj-results">
  <div class="sdn-conj-results-header">
    <span class="sdn-conj-results-kicker-row">
      <span class="sdn-conj-results-kicker">SCREENING RESULTS</span>
      <span class="sdn-conj-demo-tag" title={CONJUNCTION_RESULTS_DEMO_TAG_TITLE}>DEMO</span>
    </span>
    <div class="sdn-conj-results-modes">
      {#each QUERY_OUTPUT_MODES as mode (mode.id)}
        {@const style = queryOutputModeStyle(mode.id, resultMode)}
        <button
          type="button"
          class="sdn-conj-results-mode-btn"
          style={`background:${style.background};border-color:${style.border};color:${style.color};`}
          title={`Show screening results as ${mode.label}`}
          onclick={() => onSetMode(mode.id)}
        >
          {mode.label}
        </button>
      {/each}
    </div>
  </div>

  {#if resultMode === 'table'}
    <div class="sdn-conj-results-row-header">
      <span>OBJECT</span>
      <span>TCA (UTC)</span>
      <span>MISS km</span>
      <span>Pc</span>
      <span>STATE</span>
    </div>
    {#each resultRows as row (row.object)}
      <div class="sdn-conj-results-row" style={`border-left-color:${row.stateColor};`}>
        <span class="sdn-conj-results-object">{row.object}</span>
        <span class="sdn-conj-results-tca">{row.tca}</span>
        <span class="sdn-conj-results-miss" style={`color:${row.missColor};`}>{row.missLabel}</span>
        <span class="sdn-conj-results-pc">{row.pc}</span>
        <span class="sdn-conj-results-state" style={`color:${row.stateColor};`}>{row.state}</span>
      </div>
    {/each}
  {:else if resultMode === 'json'}
    <pre class="sdn-conj-results-code">{resultJson}</pre>
  {:else}
    <pre class="sdn-conj-results-code">{resultCsv}</pre>
  {/if}
</section>

<style>
  .sdn-conj-results {
    grid-column: span 7;
    background: linear-gradient(178deg, #16252f, #0a141b);
    border: 1px solid rgba(90, 150, 180, 0.22);
    box-shadow: inset 0 1px 0 rgba(150, 210, 240, 0.14);
    padding: 15px 16px;
  }

  .sdn-conj-results-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
    margin-bottom: 12px;
  }

  .sdn-conj-results-kicker-row {
    display: flex;
    align-items: center;
    gap: 7px;
  }

  .sdn-conj-results-kicker {
    font-size: 10px;
    letter-spacing: 0.2em;
    color: #5a7a8a;
  }

  .sdn-conj-results-modes {
    display: flex;
    gap: 4px;
  }

  .sdn-conj-results-mode-btn {
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 11px;
    letter-spacing: 0.08em;
    border: 1px solid;
    padding: 4px 11px;
    cursor: pointer;
    transition:
      border-color 0.14s,
      color 0.14s,
      background 0.14s;
  }

  .sdn-conj-results-row-header {
    display: grid;
    grid-template-columns: 1.1fr 1.6fr 0.8fr 0.9fr 0.8fr;
    gap: 0 12px;
    padding: 0 4px 7px;
    border-bottom: 1px solid rgba(90, 150, 180, 0.14);
    font-size: 9.5px;
    letter-spacing: 0.14em;
    color: #5a7a8a;
  }

  .sdn-conj-results-row {
    display: grid;
    grid-template-columns: 1.1fr 1.6fr 0.8fr 0.9fr 0.8fr;
    gap: 0 12px;
    align-items: center;
    padding: 11px 4px;
    border-bottom: 1px solid rgba(90, 150, 180, 0.07);
    border-left: 2px solid;
  }

  .sdn-conj-results-object {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 600;
    font-size: 14.5px;
    color: #eaf6f8;
  }

  .sdn-conj-results-tca {
    font-size: 12px;
    color: #9fb3bc;
  }

  .sdn-conj-results-miss {
    font-size: 13px;
    font-variant-numeric: tabular-nums;
  }

  .sdn-conj-results-pc {
    font-size: 12.5px;
    color: #cfe3ec;
    font-variant-numeric: tabular-nums;
  }

  .sdn-conj-results-state {
    font-size: 11.5px;
    letter-spacing: 0.06em;
  }

  .sdn-conj-results-code {
    margin: 0;
    overflow: auto;
    max-height: 260px;
    background: #090d12;
    border: 1px solid rgba(90, 150, 180, 0.18);
    padding: 12px;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 12.5px;
    line-height: 1.55;
    color: #9fd4c8;
    white-space: pre;
  }

  /* -- DEMO tag (same style as GroupsView.svelte's .sdn-groups-demo-tag) -- */

  .sdn-conj-demo-tag {
    padding: 1px 5px;
    border: 1px solid rgba(255, 208, 137, 0.5);
    color: #ffd089;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 8px;
    letter-spacing: 0.12em;
    flex: none;
  }
</style>

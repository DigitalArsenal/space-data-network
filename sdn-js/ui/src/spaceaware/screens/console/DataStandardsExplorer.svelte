<script lang="ts">
  /**
   * DATA view · DATA STANDARDS sub-view (loop task U3.5). Ground truth: the
   * `<!-- STANDARDS VIEW -->` block inside `<!-- ============ DATA ============ -->`
   * in `SDN Console.dc.html` — a 300px STANDARDS left panel + a right panel
   * with the selected-standard header and the EXPLORER/GENERATE/DATA tab
   * strip. Split out of `DataView.svelte` (which owns the banner + the
   * DATA STANDARDS/MODULES toggle) per the loop task's LOCK SCOPE note that
   * this view could get "huge" — see that file's doc comment for the
   * overall split.
   *
   * All data comes from `entries` (already fetched/joined/sorted by
   * `DataView.svelte` via `lib/standards-data.ts`) and the vendored
   * `main.fbs` corpus (`lib/standards-fbs.ts`, `getVendoredSchema`) — this
   * component owns no fetch of its own for the EXPLORER/GENERATE tabs, only
   * their tab selection state and the download buttons.
   *
   * The DATA tab (loop task U3.6 — local FlatSQL query workbench) IS wired
   * here: all fetching/parsing/rendering logic lives in
   * `../../lib/query-data.ts`, which this component only orchestrates
   * (probe `/api/v1/query` ONCE per DATA-tab activation, cache that
   * availability, re-query on every standard switch while the tab stays
   * active — see the `$effect` below) and renders verbatim. Real fallback:
   * `POST /api/v1/data/query` returns RECORD-METADATA rows (`batch_id`,
   * `cid`, `peer_id`, ...), not decoded standard fields — there is no
   * decoded-field surface until G.5 ships (`query-data.ts`'s doc comment has
   * the full endpoint contract). Every row-rendering path (TABLE/JSON/CSV)
   * passes each row's keys through completely unmodified, which is what
   * lets a future G.5 decoded row (`NORAD_CAT_ID`-style keys) render
   * correctly here with no code changes.
   */
  import {
    CODEGEN_LANGUAGES,
    NO_VENDORED_SCHEMA_MESSAGE,
    STANDARD_DETAIL_TABS,
    buildExplorerFieldRows,
    buildSelectedStandardHeader,
    buildStandardsListRows,
    fbsDownloadFilename,
    findCodegenLanguage,
    generateReaderStub,
    generatedCodeFilename,
    standardDetailTabStyle,
    type CodegenLanguageId,
    type StandardDetailTab,
    type StandardEntry,
  } from '../../lib/standards-data';
  import { getVendoredSchema } from '../../lib/standards-fbs';
  import {
    QUERY_DEFAULT_MODE,
    QUERY_OUTPUT_MODES,
    QUERY_ROW_LIMIT,
    buildDecodedQuerySql,
    buildQueryCsvOutput,
    buildQueryJsonOutput,
    buildQueryTableColumns,
    loadQueryTabData,
    queryEmptyStateLabel,
    queryEngineCaption,
    queryOutputModeStyle,
    queryTableCellText,
    resolveQueryEngine,
    type QueryEngineAvailability,
    type QueryOutputMode,
    type QueryTabResult,
  } from '../../lib/query-data';
  import type { SdnApiClient } from '../../../lib/auth/sdn-api-client';

  let {
    entries,
    selectedCode,
    onSelect,
    loaded,
    sdsPackageVersion,
    apiClient,
  }: {
    entries: readonly StandardEntry[];
    selectedCode: string | null;
    onSelect: (code: string) => void;
    loaded: boolean;
    sdsPackageVersion: string | null;
    apiClient: SdnApiClient;
  } = $props();

  let activeTab = $state<StandardDetailTab>('explorer');
  let genLang = $state<CodegenLanguageId>('typescript');

  // DATA tab query state. `queryEngineAvailability` is probed ONCE per
  // DATA-tab activation (see the `$effect` below, and `query-data.ts`'s
  // `resolveQueryEngine` doc comment) — every subsequent standard switch
  // while the tab stays active reuses the cached value rather than
  // re-probing `/api/v1/query` on every keystroke/switch.
  let queryMode = $state<QueryOutputMode>(QUERY_DEFAULT_MODE);
  let queryEngineAvailability = $state<QueryEngineAvailability>('unknown');
  let queryEngineProbed = $state(false);
  let queryResult = $state<QueryTabResult | null>(null);
  let queryLoaded = $state(false);

  // Default selection prefers the first standard that HAS a vendored schema:
  // the rows-first sort can put a non-vendored live channel (e.g. PRR) on
  // top, and greeting the user with "no vendored schema" as the view's
  // first render is honest but useless when EPM sits one row below.
  const selectedEntry = $derived(
    entries.find((e) => e.code === selectedCode) ??
      entries.find((e) => getVendoredSchema(e.code) !== null) ??
      entries[0] ??
      null,
  );
  const listRows = $derived(buildStandardsListRows(entries, selectedEntry?.code ?? null));
  const headerView = $derived(buildSelectedStandardHeader(selectedEntry));
  const selectedSchema = $derived(selectedEntry ? getVendoredSchema(selectedEntry.code) : null);
  const explorerRows = $derived(buildExplorerFieldRows(selectedSchema));
  const activeLang = $derived(findCodegenLanguage(genLang));
  const genCodeText = $derived(
    selectedEntry ? generateReaderStub(selectedEntry.code, selectedSchema, sdsPackageVersion, genLang) : '',
  );
  const genFileName = $derived(selectedEntry ? generatedCodeFilename(selectedEntry.code, activeLang) : '');

  const queryRows = $derived(queryResult?.rows ?? []);
  const queryColumns = $derived(buildQueryTableColumns(queryRows));
  const queryJsonText = $derived(buildQueryJsonOutput(queryRows));
  const queryCsvText = $derived(buildQueryCsvOutput(queryRows));
  const queryCaption = $derived(queryEngineCaption(queryResult?.engine ?? 'metadata'));
  const queryEmptyLabel = $derived(
    selectedEntry ? queryEmptyStateLabel(selectedEntry.code, queryLoaded, queryRows.length) : '',
  );

  // Runs the DATA tab's query on tab activation and on every standard
  // switch while the tab stays active (loop task spec: "run the query on
  // tab activation (limit 50)") — never on EXPLORER/GENERATE. A stale
  // in-flight request from a since-abandoned activation/switch is dropped
  // via the `cancelled` flag, matching `PeersView.svelte`'s
  // selected-peer-detail effect.
  $effect(() => {
    const tab = activeTab;
    const code = selectedEntry?.code ?? null;
    if (tab !== 'data' || !code) return;
    let cancelled = false;
    queryLoaded = false;
    queryResult = null;
    void (async () => {
      if (!queryEngineProbed) {
        queryEngineAvailability = await resolveQueryEngine(apiClient, buildDecodedQuerySql(code, QUERY_ROW_LIMIT, 0));
        queryEngineProbed = true;
      }
      if (cancelled) return;
      const result = await loadQueryTabData(apiClient, code, queryEngineAvailability, QUERY_ROW_LIMIT, 0);
      if (cancelled) return;
      queryResult = result;
      queryLoaded = true;
    })();
    return () => {
      cancelled = true;
    };
  });

  function selectStandard(code: string) {
    onSelect(code);
  }

  function setTab(tab: StandardDetailTab) {
    activeTab = tab;
  }

  function setQueryMode(mode: QueryOutputMode) {
    queryMode = mode;
  }

  function triggerDownload(content: string, filename: string) {
    try {
      const blob = new Blob([content], { type: 'text/plain' });
      const url = URL.createObjectURL(blob);
      const link = document.createElement('a');
      link.href = url;
      link.download = filename;
      link.click();
      URL.revokeObjectURL(url);
    } catch {
      // Browser download APIs unavailable in this context — the button is a no-op rather than a crash.
    }
  }

  function downloadFbs() {
    if (!selectedEntry || !selectedSchema) return;
    triggerDownload(selectedSchema.raw, fbsDownloadFilename(selectedEntry.code));
  }

  function downloadGeneratedCode() {
    if (!selectedEntry) return;
    triggerDownload(genCodeText, genFileName);
  }
</script>

<div class="sdn-data-explorer-root">
  <section class="sdn-data-standards-panel">
    <div class="sdn-data-panel-kicker">STANDARDS</div>
    <div class="sdn-data-standards-list">
      {#each listRows as row (row.code)}
        <div
          class="sdn-data-standard-row"
          role="button"
          tabindex="0"
          title={`View the ${row.code} schema`}
          style={`border-left-color:${row.selected ? '#35c9d8' : 'transparent'};background:${row.selected ? 'rgba(74,166,224,0.12)' : 'transparent'};`}
          onclick={() => selectStandard(row.code)}
          onkeydown={(event) => {
            if (event.key === 'Enter' || event.key === ' ') selectStandard(row.code);
          }}
        >
          <span class="sdn-data-standard-code" style={`color:${row.selected ? '#eaf6f8' : '#cfe3ec'};`}>{row.code}</span>
          <div class="sdn-data-standard-meta">
            <div class="sdn-data-standard-label">{row.label}</div>
            <div class="sdn-data-standard-sub">{row.versionLabel} · {row.rowsLabel}</div>
          </div>
          {#if row.encrypted}
            <span class="sdn-data-standard-status" title="Encrypted channel">🔒</span>
          {:else}
            <span class="sdn-data-standard-status" style={`color:${row.statusColor};`}>{row.statusGlyph}</span>
          {/if}
        </div>
      {:else}
        <div class="sdn-data-standards-empty">{loaded ? 'NO STANDARDS' : 'LOADING STANDARDS…'}</div>
      {/each}
    </div>
  </section>

  <section class="sdn-data-detail-panel">
    {#if headerView}
      <div class="sdn-data-detail-header">
        <span class="sdn-data-detail-code">{headerView.code}</span>
        <span class="sdn-data-detail-name">{headerView.name}</span>
        <span class="sdn-data-detail-version-chip">{headerView.versionChip}</span>
        {#if headerView.encrypted}
          <span class="sdn-data-detail-encrypted">🔒 ENCRYPTED</span>
        {/if}
        <span class="sdn-data-detail-spacer"></span>
        <span class="sdn-data-detail-rows-caption">{headerView.rowsCaption}</span>
      </div>

      <div class="sdn-data-detail-tabs">
        {#each STANDARD_DETAIL_TABS as tab (tab.id)}
          {@const style = standardDetailTabStyle(tab.id, activeTab)}
          <button
            type="button"
            class="sdn-data-detail-tab"
            style={`color:${style.color};border-bottom-color:${style.borderColor};`}
            title={`Show the ${tab.label} tab`}
            onclick={() => setTab(tab.id)}
          >
            {tab.label}
          </button>
        {/each}
      </div>

      <div class="sdn-data-detail-body">
        {#if activeTab === 'explorer'}
          <div class="sdn-data-tab-stack">
            <div class="sdn-data-section-kicker">MESSAGE SCHEMA · {headerView.code}</div>
            <div class="sdn-data-field-table-header">
              <span>FIELD</span><span>TYPE</span><span>NOTES</span>
            </div>
            <div class="sdn-data-field-rows">
              {#each explorerRows as field (field.name)}
                <div class="sdn-data-field-row">
                  <span class="sdn-data-field-name">{field.name}</span>
                  <span class="sdn-data-field-type" style={`color:${field.typeColor};`}>{field.type}</span>
                  <span class="sdn-data-field-note">{field.note}</span>
                </div>
              {:else}
                <div class="sdn-data-field-empty">{NO_VENDORED_SCHEMA_MESSAGE}</div>
              {/each}
            </div>
            <div class="sdn-data-idl-header">
              <span class="sdn-data-section-kicker">FLATBUFFERS IDL · {headerView.code}.fbs</span>
              <button
                type="button"
                class="sdn-data-btn-ghost"
                disabled={!selectedSchema}
                title="Download this standard's FlatBuffers IDL file"
                onclick={downloadFbs}
              >
                ↓ .fbs
              </button>
            </div>
            <pre class="sdn-data-code-block">{selectedSchema?.raw ?? NO_VENDORED_SCHEMA_MESSAGE}</pre>
          </div>
        {:else if activeTab === 'generate'}
          <div class="sdn-data-tab-stack">
            <div class="sdn-data-section-kicker">TARGET LANGUAGE</div>
            <div class="sdn-data-lang-strip">
              {#each CODEGEN_LANGUAGES as lang (lang.id)}
                <button
                  type="button"
                  class="sdn-data-lang-btn"
                  class:is-active={lang.id === genLang}
                  title={`Generate a ${lang.name} stub reader`}
                  onclick={() => (genLang = lang.id)}
                >
                  {lang.name}
                </button>
              {/each}
            </div>
            <div class="sdn-data-generate-toolbar">
              <span class="sdn-data-generate-filename">{genFileName}</span>
              <div class="sdn-data-generate-buttons">
                <button
                  type="button"
                  class="sdn-data-btn-ghost"
                  disabled={!selectedSchema}
                  title="Download this standard's FlatBuffers IDL file"
                  onclick={downloadFbs}
                >
                  ↓ .fbs
                </button>
                <button
                  type="button"
                  class="sdn-data-btn-accent"
                  disabled={!selectedEntry}
                  title="Download the generated stub reader"
                  onclick={downloadGeneratedCode}
                >
                  ↓ DOWNLOAD CODE
                </button>
              </div>
            </div>
            <pre class="sdn-data-code-block sdn-data-code-block--tall">{genCodeText}</pre>
          </div>
        {:else}
          <div class="sdn-data-tab-stack">
            <div class="sdn-data-query-header">
              <span class="sdn-data-section-kicker">LOCAL FLATSQL · QUERY OUTPUT</span>
              <div class="sdn-data-query-modes">
                {#each QUERY_OUTPUT_MODES as mode (mode.id)}
                  {@const modeStyle = queryOutputModeStyle(mode.id, queryMode)}
                  <button
                    type="button"
                    class="sdn-data-query-mode-btn"
                    style={`background:${modeStyle.background};border-color:${modeStyle.border};color:${modeStyle.color};`}
                    title={`Show query output as ${mode.label}`}
                    onclick={() => setQueryMode(mode.id)}
                  >
                    {mode.label}
                  </button>
                {/each}
              </div>
            </div>
            <div class="sdn-data-query-caption">{queryCaption}</div>
            {#if queryResult?.errorKind}
              <div class="sdn-data-query-error">{queryResult.errorMessage}</div>
            {:else if queryEmptyLabel}
              <div class="sdn-data-query-empty">{queryEmptyLabel}</div>
            {:else if queryMode === 'table'}
              <div class="sdn-data-query-table-wrap">
                <table class="sdn-data-query-table">
                  <thead>
                    <tr>
                      {#each queryColumns as col (col)}
                        <th>{col}</th>
                      {/each}
                    </tr>
                  </thead>
                  <tbody>
                    {#each queryRows as row, rowIndex (rowIndex)}
                      <tr>
                        {#each queryColumns as col (col)}
                          <td>{queryTableCellText(row[col])}</td>
                        {/each}
                      </tr>
                    {/each}
                  </tbody>
                </table>
              </div>
            {:else if queryMode === 'json'}
              <pre class="sdn-data-code-block">{queryJsonText}</pre>
            {:else}
              <pre class="sdn-data-code-block">{queryCsvText}</pre>
            {/if}
          </div>
        {/if}
      </div>
    {:else}
      <div class="sdn-data-detail-empty">NO STANDARD SELECTED</div>
    {/if}
  </section>
</div>

<style>
  .sdn-data-explorer-root {
    display: grid;
    grid-template-columns: 300px minmax(0, 1fr);
    gap: 14px;
    align-items: start;
  }

  .sdn-data-standards-panel {
    background: linear-gradient(178deg, #16252f, #0a141b);
    border: 1px solid rgba(90, 150, 180, 0.22);
    box-shadow: inset 0 1px 0 rgba(150, 210, 240, 0.14);
    padding: 13px 14px;
    min-width: 0;
  }

  .sdn-data-panel-kicker,
  .sdn-data-section-kicker {
    font-size: 10px;
    letter-spacing: 0.2em;
    color: #5a7a8a;
    margin-bottom: 10px;
  }

  .sdn-data-section-kicker {
    font-size: 9.5px;
    letter-spacing: 0.16em;
    margin-bottom: 0;
  }

  .sdn-data-standards-list {
    display: flex;
    flex-direction: column;
    max-height: calc(100vh - 260px);
    overflow-y: auto;
  }

  .sdn-data-standard-row {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 9px 8px;
    border-left: 2px solid transparent;
    cursor: pointer;
  }

  .sdn-data-standard-row:hover {
    background: rgba(74, 166, 224, 0.07);
  }

  .sdn-data-standard-code {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 700;
    font-size: 14.5px;
    width: 38px;
    flex: none;
  }

  .sdn-data-standard-meta {
    flex: 1;
    min-width: 0;
  }

  .sdn-data-standard-label {
    font-size: 12px;
    color: #9fb3bc;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .sdn-data-standard-sub {
    font-size: 9.5px;
    color: #5a7a8a;
    margin-top: 1px;
  }

  .sdn-data-standard-status {
    font-size: 13px;
    flex: none;
  }

  .sdn-data-standards-empty {
    padding: 20px 4px;
    font-size: 11px;
    letter-spacing: 0.06em;
    color: #5d7681;
    text-align: center;
  }

  .sdn-data-detail-panel {
    background: linear-gradient(178deg, #16252f, #0a141b);
    border: 1px solid rgba(90, 150, 180, 0.22);
    box-shadow: inset 0 1px 0 rgba(150, 210, 240, 0.14);
    padding: 0;
    display: flex;
    flex-direction: column;
    overflow: hidden;
    min-width: 0;
  }

  .sdn-data-detail-header {
    display: flex;
    align-items: center;
    gap: 11px;
    padding: 13px 16px;
    border-bottom: 1px solid rgba(90, 150, 180, 0.16);
    flex-wrap: wrap;
  }

  .sdn-data-detail-code {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 700;
    font-size: 23px;
    color: #eaf6f8;
  }

  .sdn-data-detail-name {
    font-size: 13px;
    color: #9fb3bc;
  }

  .sdn-data-detail-version-chip {
    font-size: 10px;
    color: #9fd4f5;
    border: 1px solid rgba(120, 190, 230, 0.4);
    padding: 1px 7px;
  }

  .sdn-data-detail-encrypted {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font-size: 10px;
    color: #ffb24d;
    border: 1px solid rgba(255, 178, 77, 0.4);
    padding: 1px 7px;
  }

  .sdn-data-detail-spacer {
    flex: 1;
  }

  .sdn-data-detail-rows-caption {
    font-size: 11px;
    color: #7d929b;
  }

  .sdn-data-detail-tabs {
    display: flex;
    padding: 0 16px;
    border-bottom: 1px solid rgba(90, 150, 180, 0.14);
  }

  .sdn-data-detail-tab {
    background: transparent;
    border: 0;
    border-bottom: 2px solid transparent;
    cursor: pointer;
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 600;
    font-size: 12px;
    letter-spacing: 0.08em;
    padding: 10px 16px;
  }

  .sdn-data-detail-body {
    padding: 14px 16px;
    overflow-y: auto;
  }

  .sdn-data-detail-empty {
    padding: 24px 16px;
    font-size: 11px;
    letter-spacing: 0.06em;
    color: #5d7681;
  }

  .sdn-data-tab-stack {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }

  .sdn-data-field-table-header {
    display: grid;
    grid-template-columns: 1.4fr 1.1fr 1.3fr;
    gap: 0 14px;
    padding: 0 4px 7px;
    border-bottom: 1px solid rgba(90, 150, 180, 0.14);
    font-size: 9.5px;
    letter-spacing: 0.14em;
    color: #5a7a8a;
  }

  .sdn-data-field-rows {
    display: flex;
    flex-direction: column;
  }

  .sdn-data-field-row {
    display: grid;
    grid-template-columns: 1.4fr 1.1fr 1.3fr;
    gap: 0 14px;
    align-items: center;
    padding: 7px 4px;
    border-bottom: 1px solid rgba(90, 150, 180, 0.06);
  }

  .sdn-data-field-name {
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 12.5px;
    color: #cfe3ec;
  }

  .sdn-data-field-type {
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 12px;
  }

  .sdn-data-field-note {
    font-size: 11px;
    color: #7d929b;
  }

  .sdn-data-field-empty {
    padding: 14px 4px;
    font-size: 11px;
    color: #5d7681;
  }

  .sdn-data-idl-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-top: 4px;
  }

  .sdn-data-btn-ghost {
    background: transparent;
    border: 1px solid rgba(90, 150, 180, 0.3);
    color: #9fb3bc;
    padding: 5px 11px;
    font-size: 11px;
    letter-spacing: 0.06em;
    cursor: pointer;
    transition:
      border-color 0.14s,
      color 0.14s,
      background 0.14s;
  }

  .sdn-data-btn-ghost:hover:not(:disabled) {
    border-color: rgba(120, 190, 230, 0.6);
    color: #eaf6f8;
    background: rgba(74, 166, 224, 0.08);
  }

  .sdn-data-btn-ghost:disabled {
    opacity: 0.45;
    cursor: not-allowed;
  }

  .sdn-data-btn-accent {
    background: rgba(53, 201, 216, 0.14);
    border: 1px solid rgba(53, 201, 216, 0.5);
    color: #9fe9f2;
    padding: 6px 13px;
    font-size: 11px;
    letter-spacing: 0.08em;
    cursor: pointer;
    transition:
      border-color 0.14s,
      color 0.14s,
      background 0.14s;
  }

  .sdn-data-btn-accent:hover:not(:disabled) {
    border-color: rgba(53, 201, 216, 0.7);
    color: #eaf6f8;
  }

  .sdn-data-btn-accent:disabled {
    opacity: 0.45;
    cursor: not-allowed;
  }

  .sdn-data-code-block {
    margin: 0;
    overflow: auto;
    max-height: 180px;
    background: #090d12;
    border: 1px solid rgba(90, 150, 180, 0.18);
    padding: 12px;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 12px;
    line-height: 1.5;
    color: #9fd4c8;
    white-space: pre;
  }

  .sdn-data-code-block--tall {
    max-height: 240px;
  }

  .sdn-data-lang-strip {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }

  .sdn-data-lang-btn {
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 12px;
    background: rgba(74, 166, 224, 0.04);
    border: 1px solid rgba(90, 150, 180, 0.28);
    color: #9fb3bc;
    cursor: pointer;
    padding: 6px 12px;
  }

  .sdn-data-lang-btn.is-active {
    background: rgba(53, 201, 216, 0.16);
    border-color: #35c9d8;
    color: #9fe9f2;
  }

  .sdn-data-generate-toolbar {
    display: flex;
    justify-content: space-between;
    align-items: center;
    border-top: 1px solid rgba(90, 150, 180, 0.12);
    padding-top: 11px;
  }

  .sdn-data-generate-filename {
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 11.5px;
    color: #9fb3bc;
  }

  .sdn-data-generate-buttons {
    display: flex;
    gap: 6px;
  }

  .sdn-data-query-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
  }

  .sdn-data-query-modes {
    display: flex;
    gap: 4px;
  }

  .sdn-data-query-mode-btn {
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

  .sdn-data-query-caption {
    font-size: 10.5px;
    color: #5d7681;
    margin-top: -6px;
  }

  .sdn-data-query-error {
    font-size: 11px;
    line-height: 1.4;
    color: #ff8d8d;
    border: 1px solid rgba(255, 107, 107, 0.3);
    background: rgba(255, 107, 107, 0.06);
    padding: 10px 12px;
  }

  .sdn-data-query-empty {
    padding: 20px 4px;
    font-size: 11px;
    letter-spacing: 0.06em;
    color: #5d7681;
    text-align: center;
    background: #090d12;
    border: 1px solid rgba(90, 150, 180, 0.18);
  }

  .sdn-data-query-table-wrap {
    overflow: auto;
    max-height: 240px;
    background: #090d12;
    border: 1px solid rgba(90, 150, 180, 0.18);
  }

  .sdn-data-query-table {
    width: 100%;
    border-collapse: collapse;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 11.5px;
  }

  .sdn-data-query-table th {
    position: sticky;
    top: 0;
    text-align: left;
    font-size: 9.5px;
    letter-spacing: 0.1em;
    color: #5a7a8a;
    background: #0c1017;
    padding: 8px 10px;
    border-bottom: 1px solid rgba(90, 150, 180, 0.18);
    white-space: nowrap;
  }

  .sdn-data-query-table td {
    padding: 6px 10px;
    color: #9fd4c8;
    border-bottom: 1px solid rgba(90, 150, 180, 0.06);
    white-space: nowrap;
  }
</style>

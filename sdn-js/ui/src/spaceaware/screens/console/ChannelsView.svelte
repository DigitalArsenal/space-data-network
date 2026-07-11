<script lang="ts">
  /**
   * CHANNELS console view (loop task U3.7). Ground truth: the
   * `<!-- ============ CHANNELS ============ -->` block in
   * `design_handoff/sdn_console/SDN Console.dc.html` — an "ENCRYPTED DATA
   * CHANNELS" table (left, span 7: status dot / CHANNEL · RECIPIENT / STD /
   * GRANT / ENCRYPTION) and a "CHANNEL MONITOR" card (right, span 5: header
   * + SEALED/OPEN tag, channel name, `→ recipient` line, a 2×2
   * VISIBILITY/SUBSCRIPTION/GRANT STATE/STANDARD field grid, a KEY ENVELOPE
   * box, and an OPEN SEALED STREAM / ISSUE GRANT / ENVELOPE button row).
   * The shared `ConsoleHeader` already renders "CHANNELS · ENCRYPTED
   * EXCHANGE" (`lib/console.ts`), so this view starts at the table.
   *
   * All data wiring lives in `../../lib/channels-data.ts` — this file only
   * renders its view-model strings verbatim, matching `PeersView.svelte`/
   * `DataView.svelte`'s established split. Every honest empty/disabled
   * state below (dim PLAINTEXT instead of the mock's fabricated "✓
   * SIGNED", "{CODE} BROADCAST" instead of a fabricated provider slug, the
   * permanently-disabled OPEN SEALED STREAM/ENVELOPE buttons, the
   * admin-gated ISSUE GRANT) traces back to a real gap documented in
   * `channels-data.ts`'s file doc comment — nothing here fabricates data
   * the daemon doesn't actually expose yet.
   *
   * Selection semantics mirror `PeersView.svelte`: the CHANNEL MONITOR
   * card tracks a selected row key against the full row list, defaulting
   * to the first row. On selection it also refetches
   * `/channels?standardCode={code}` (`fetchChannelDetailRow`) to pick up a
   * richer verified per-provider row when one exists — never blocking or
   * clearing the current selection on that refetch's failure.
   *
   * ISSUE GRANT opens a minimal inline form (recipient/scopes/optional
   * RFC3339 expiry, plus a `sourceId` field defaulting to `"local"` — see
   * `channels-data.ts`'s `buildGrantChannelId` doc comment for why a real
   * channel id is required even for these topic-level rows) rather than a
   * modal, keeping this view dependency-free.
   */
  import { onMount } from 'svelte';
  import {
    buildChannelRows,
    buildMonitorView,
    canIssueChannelGrant,
    channelRowKey,
    channelsEmptyStateLabel,
    fetchChannelDetailRow,
    issueChannelGrant,
    loadChannelsCollection,
    type ChannelCollectionRow,
  } from '../../lib/channels-data';
  import type { AuthSessionState } from '../../../lib/auth/auth-store';
  import type { SdnApiClient } from '../../../lib/auth/sdn-api-client';

  let { apiClient, authState }: { apiClient: SdnApiClient; authState: AuthSessionState } = $props();

  let rows = $state<ChannelCollectionRow[]>([]);
  let loaded = $state(false);
  let selectedKey = $state<string | null>(null);
  let detailRow = $state<ChannelCollectionRow | null>(null);

  let grantFormOpen = $state(false);
  let grantSourceId = $state('local');
  let grantTo = $state('');
  let grantScopes = $state('');
  let grantExpiresAt = $state('');
  let grantSubmitting = $state(false);
  let grantError = $state<string | null>(null);
  let grantSuccessId = $state<string | null>(null);

  const rowViews = $derived(buildChannelRows(rows, selectedKey));
  const selectedBaseRow = $derived(rows.find((r) => channelRowKey(r) === selectedKey) ?? rows[0] ?? null);
  const effectiveRow = $derived(detailRow ?? selectedBaseRow);
  const canIssueGrant = $derived(canIssueChannelGrant(authState));
  const monitorView = $derived(buildMonitorView(effectiveRow, canIssueGrant));
  const emptyStateLabel = $derived(channelsEmptyStateLabel(loaded, rows.length));

  onMount(() => {
    void loadChannelsCollection(apiClient).then((data) => {
      rows = data;
      loaded = true;
    });
  });

  // Re-fetch GET /api/v1/channels?standardCode={code} whenever the resolved
  // selection changes, to pick up a richer verified per-provider row when
  // one exists (see fetchChannelDetailRow's doc comment). Never clears an
  // existing detailRow on a stale/cancelled response.
  $effect(() => {
    const base = selectedBaseRow;
    detailRow = null;
    grantFormOpen = false;
    grantError = null;
    grantSuccessId = null;
    // sourceId is kept across selections (a per-session default the user
    // is likely to reuse); to/scopes/expiresAt are per-request and would
    // otherwise silently carry over to a different channel's grant.
    grantTo = '';
    grantScopes = '';
    grantExpiresAt = '';
    if (!base) return;
    let cancelled = false;
    void fetchChannelDetailRow(apiClient, base.standardCode, base).then((resolved) => {
      if (!cancelled) detailRow = resolved;
    });
    return () => {
      cancelled = true;
    };
  });

  function selectRow(key: string) {
    selectedKey = key;
  }

  function toggleGrantForm() {
    if (!monitorView?.issueGrantEnabled) return;
    grantFormOpen = !grantFormOpen;
    grantError = null;
    grantSuccessId = null;
  }

  async function submitGrant() {
    if (!effectiveRow || grantSubmitting) return;
    grantSubmitting = true;
    grantError = null;
    grantSuccessId = null;
    const result = await issueChannelGrant(apiClient, {
      sourceId: grantSourceId,
      standardCode: effectiveRow.standardCode,
      to: grantTo,
      scopesRaw: grantScopes,
      expiresAtRaw: grantExpiresAt,
    });
    grantSubmitting = false;
    if (!result.ok) {
      grantError = result.error;
      return;
    }
    grantSuccessId = result.grant?.grantId ?? null;
    grantFormOpen = false;
    grantTo = '';
    grantScopes = '';
    grantExpiresAt = '';
  }
</script>

<div class="sdn-channels-root">
  <section class="sdn-channels-directory">
    <div class="sdn-channels-directory-title">ENCRYPTED DATA CHANNELS</div>
    <div class="sdn-channels-row-header">
      <span></span>
      <span>CHANNEL / RECIPIENT</span>
      <span>STD</span>
      <span>GRANT</span>
      <span>ENCRYPTION</span>
    </div>
    <div class="sdn-channels-rows">
      {#if emptyStateLabel}
        <div class="sdn-channels-empty">{emptyStateLabel}</div>
      {:else}
        {#each rowViews as row (row.key)}
          <div
            class="sdn-channels-row"
            role="button"
            tabindex="0"
            title={`View channel details for ${row.name}`}
            style={`background:${row.selected ? 'rgba(74,166,224,0.1)' : 'transparent'};`}
            onclick={() => selectRow(row.key)}
            onkeydown={(event) => {
              if (event.key === 'Enter' || event.key === ' ') selectRow(row.key);
            }}
          >
            <span class="sdn-channels-row-dot" style={`color:${row.visibilityColor};`}>{row.visibilityGlyph}</span>
            <span class="sdn-channels-row-name-cell">
              <span class="sdn-channels-row-name">{row.name}</span>
              <br />
              <span class="sdn-channels-row-recipient">→ {row.recipientLabel}</span>
            </span>
            <span class="sdn-channels-row-std">{row.standardCode}</span>
            <span class="sdn-channels-row-grant" style={`color:${row.grantColor};`}>{row.grantLabel}</span>
            <span class="sdn-channels-row-encryption" style={`color:${row.encryptionColor};`}>
              {#if row.encryptionGlyph}<span class="sdn-channels-row-encryption-glyph">{row.encryptionGlyph}</span>{/if}
              {row.encryptionLabel}
            </span>
          </div>
        {/each}
      {/if}
    </div>
  </section>

  <section class="sdn-channels-monitor">
    {#if monitorView}
      <div class="sdn-channels-monitor-header">
        <span class="sdn-channels-directory-title">CHANNEL MONITOR</span>
        <span class="sdn-channels-monitor-tag" style={`color:${monitorView.headerTagColor};`}>
          {#if monitorView.headerTagGlyph}{monitorView.headerTagGlyph}{/if}
          {monitorView.headerTagLabel}
        </span>
      </div>
      <div class="sdn-channels-monitor-name">{monitorView.name}</div>
      <div class="sdn-channels-monitor-recipient">→ {monitorView.recipientLabel}</div>

      <div class="sdn-channels-monitor-fields">
        <div>
          <div class="sdn-channels-field-label">VISIBILITY</div>
          <div class="sdn-channels-field-value" style={`color:${monitorView.visibilityColor};`}>{monitorView.visibilityLabel}</div>
        </div>
        <div>
          <div class="sdn-channels-field-label">SUBSCRIPTION</div>
          <div class="sdn-channels-field-value" style={`color:${monitorView.subscriptionColor};`}>{monitorView.subscriptionLabel}</div>
        </div>
        <div>
          <div class="sdn-channels-field-label">GRANT STATE</div>
          <div class="sdn-channels-field-value" style={`color:${monitorView.grantColor};`}>{monitorView.grantLabel}</div>
        </div>
        <div>
          <div class="sdn-channels-field-label">STANDARD</div>
          <div class="sdn-channels-field-value">{monitorView.standardCode}</div>
        </div>
      </div>

      <div class="sdn-channels-envelope-block">
        <div class="sdn-channels-field-label">KEY ENVELOPE</div>
        <div class="sdn-channels-envelope-box">
          <span class="sdn-channels-envelope-glyph" style={`color:${monitorView.keyEnvelope.color};`}>{monitorView.keyEnvelope.glyph}</span>
          <div class="sdn-channels-envelope-text">
            <div class="sdn-channels-envelope-title">{monitorView.keyEnvelope.title}</div>
            <div class="sdn-channels-envelope-meta">{monitorView.keyEnvelope.meta}</div>
          </div>
        </div>
      </div>

      <div class="sdn-channels-monitor-buttons">
        <button
          type="button"
          class="sdn-channels-btn sdn-channels-btn--stream"
          disabled={!monitorView.streamButtonEnabled}
          title={monitorView.streamButtonTooltip}
        >
          {monitorView.streamButtonLabel}
        </button>
        <button
          type="button"
          class="sdn-channels-btn sdn-channels-btn--ghost"
          disabled={!monitorView.issueGrantEnabled}
          title={monitorView.issueGrantTooltip}
          onclick={toggleGrantForm}
        >
          ISSUE GRANT
        </button>
        <button type="button" class="sdn-channels-btn sdn-channels-btn--ghost" disabled title={monitorView.envelopeButtonTooltip}>
          ENVELOPE
        </button>
      </div>

      {#if grantFormOpen}
        <form
          class="sdn-channels-grant-form"
          onsubmit={(event) => {
            event.preventDefault();
            void submitGrant();
          }}
        >
          <label class="sdn-channels-grant-field">
            <span>SOURCE ID</span>
            <input
              type="text"
              bind:value={grantSourceId}
              title="Local source id this grant is issued against (defaults to &quot;local&quot; — combined with the standard code to form the real channel id)"
            />
          </label>
          <label class="sdn-channels-grant-field">
            <span>RECIPIENT (TO)</span>
            <input type="text" bind:value={grantTo} placeholder="peer id or alias" title="Subject/recipient identity this grant is issued to" />
          </label>
          <label class="sdn-channels-grant-field">
            <span>SCOPES</span>
            <input
              type="text"
              bind:value={grantScopes}
              placeholder="subscribe, stream_open"
              title="Comma- or space-separated scope list — leave blank for the server's default private-grant scope set"
            />
          </label>
          <label class="sdn-channels-grant-field">
            <span>EXPIRES AT (RFC3339, OPTIONAL)</span>
            <input
              type="text"
              bind:value={grantExpiresAt}
              placeholder="2026-08-01T00:00:00Z"
              title="Optional RFC3339 expiry — leave blank to default to 24 hours from now"
            />
          </label>
          <div class="sdn-channels-grant-form-buttons">
            <button type="submit" class="sdn-channels-btn sdn-channels-btn--stream" disabled={grantSubmitting} title="Submit this grant request">
              {grantSubmitting ? 'ISSUING…' : 'SUBMIT GRANT'}
            </button>
            <button type="button" class="sdn-channels-btn sdn-channels-btn--ghost" onclick={toggleGrantForm} title="Cancel">CANCEL</button>
          </div>
          {#if grantError}
            <div class="sdn-channels-grant-error">{grantError}</div>
          {/if}
        </form>
      {/if}

      {#if grantSuccessId}
        <div class="sdn-channels-grant-success">GRANT ISSUED · {grantSuccessId}</div>
      {/if}
    {:else}
      <div class="sdn-channels-directory-title">CHANNEL MONITOR</div>
      <div class="sdn-channels-monitor-empty">NO CHANNEL SELECTED</div>
    {/if}
  </section>
</div>

<style>
  .sdn-channels-root {
    display: grid;
    grid-template-columns: repeat(12, minmax(0, 1fr));
    gap: 14px;
    align-content: start;
  }

  .sdn-channels-directory,
  .sdn-channels-monitor {
    background: linear-gradient(178deg, #16252f, #0a141b);
    border: 1px solid rgba(90, 150, 180, 0.22);
    box-shadow: inset 0 1px 0 rgba(150, 210, 240, 0.14);
    padding: 15px 16px;
  }

  .sdn-channels-directory {
    grid-column: span 7;
    display: flex;
    flex-direction: column;
    min-width: 0;
  }

  .sdn-channels-monitor {
    grid-column: span 5;
    min-width: 0;
    overflow-y: auto;
    max-height: calc(100vh - 230px);
  }

  .sdn-channels-directory-title {
    font-size: 10px;
    letter-spacing: 0.2em;
    color: #5a7a8a;
    margin-bottom: 12px;
  }

  .sdn-channels-row-header {
    display: grid;
    grid-template-columns: 14px 1.8fr 0.7fr 1fr 1fr;
    gap: 0 12px;
    padding: 0 4px 7px;
    border-bottom: 1px solid rgba(90, 150, 180, 0.14);
    font-size: 9.5px;
    letter-spacing: 0.14em;
    color: #5a7a8a;
    flex: none;
  }

  .sdn-channels-rows {
    overflow-y: auto;
    max-height: calc(100vh - 300px);
  }

  .sdn-channels-empty {
    padding: 24px 4px;
    font-size: 11px;
    letter-spacing: 0.06em;
    color: #5d7681;
    text-align: center;
  }

  .sdn-channels-row {
    display: grid;
    grid-template-columns: 14px 1.8fr 0.7fr 1fr 1fr;
    gap: 0 12px;
    align-items: center;
    padding: 11px 4px;
    border-bottom: 1px solid rgba(90, 150, 180, 0.07);
    cursor: pointer;
  }

  .sdn-channels-row-dot {
    font-size: 13px;
    line-height: 1;
  }

  .sdn-channels-row-name-cell {
    min-width: 0;
  }

  .sdn-channels-row-name {
    font-size: 13px;
    color: #eaf6f8;
    overflow-wrap: anywhere;
  }

  .sdn-channels-row-recipient {
    font-size: 10px;
    color: #6f8693;
  }

  .sdn-channels-row-std {
    font-size: 12px;
    color: #9fb3bc;
  }

  .sdn-channels-row-grant {
    font-size: 11.5px;
    letter-spacing: 0.04em;
  }

  .sdn-channels-row-encryption {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    font-size: 11.5px;
    letter-spacing: 0.04em;
  }

  .sdn-channels-row-encryption-glyph {
    font-size: 12px;
  }

  .sdn-channels-monitor-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 12px;
  }

  .sdn-channels-monitor-header .sdn-channels-directory-title {
    margin-bottom: 0;
  }

  .sdn-channels-monitor-tag {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    font-size: 10px;
    letter-spacing: 0.06em;
  }

  .sdn-channels-monitor-name {
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-weight: 600;
    font-size: 18px;
    color: #eaf6f8;
    overflow-wrap: anywhere;
  }

  .sdn-channels-monitor-recipient {
    font-size: 11.5px;
    color: #7d929b;
    margin: 5px 0 14px;
  }

  .sdn-channels-monitor-fields {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 11px;
  }

  .sdn-channels-field-label {
    font-size: 9.5px;
    letter-spacing: 0.14em;
    color: #5a7a8a;
  }

  .sdn-channels-field-value {
    font-size: 13px;
    color: #cfe3ec;
    margin-top: 2px;
  }

  .sdn-channels-envelope-block {
    border-top: 1px solid rgba(90, 150, 180, 0.12);
    margin-top: 14px;
    padding-top: 12px;
  }

  .sdn-channels-envelope-block .sdn-channels-field-label {
    margin-bottom: 8px;
  }

  .sdn-channels-envelope-box {
    display: flex;
    align-items: center;
    gap: 9px;
    background: #090d12;
    border: 1px solid rgba(90, 150, 180, 0.2);
    padding: 9px 11px;
  }

  .sdn-channels-envelope-glyph {
    font-size: 17px;
  }

  .sdn-channels-envelope-text {
    flex: 1;
    min-width: 0;
  }

  .sdn-channels-envelope-title {
    font-size: 12px;
    color: #cfe3ec;
  }

  .sdn-channels-envelope-meta {
    font-size: 9.5px;
    color: #6f8693;
    margin-top: 1px;
  }

  .sdn-channels-monitor-buttons {
    display: flex;
    gap: 6px;
    margin-top: 14px;
  }

  .sdn-channels-btn {
    padding: 8px 0;
    font-size: 11.5px;
    letter-spacing: 0.08em;
    cursor: pointer;
    transition:
      border-color 0.14s,
      color 0.14s,
      background 0.14s;
  }

  .sdn-channels-btn:disabled {
    opacity: 0.45;
    cursor: not-allowed;
  }

  .sdn-channels-btn--stream {
    flex: 1.3;
    background: rgba(74, 166, 224, 0.12);
    border: 1px solid rgba(120, 190, 230, 0.5);
    color: #9fd4f5;
  }

  .sdn-channels-btn--stream:hover:not(:disabled) {
    border-color: rgba(120, 190, 230, 0.6);
    color: #eaf6f8;
    background: rgba(74, 166, 224, 0.2);
  }

  .sdn-channels-btn--ghost {
    flex: 1;
    background: transparent;
    border: 1px solid rgba(90, 150, 180, 0.3);
    color: #9fb3bc;
  }

  .sdn-channels-btn--ghost:hover:not(:disabled) {
    border-color: rgba(120, 190, 230, 0.6);
    color: #eaf6f8;
    background: rgba(74, 166, 224, 0.08);
  }

  .sdn-channels-grant-form {
    display: flex;
    flex-direction: column;
    gap: 9px;
    margin-top: 14px;
    padding-top: 12px;
    border-top: 1px solid rgba(90, 150, 180, 0.12);
  }

  .sdn-channels-grant-field {
    display: flex;
    flex-direction: column;
    gap: 4px;
    font-size: 9.5px;
    letter-spacing: 0.1em;
    color: #5a7a8a;
  }

  .sdn-channels-grant-field input {
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 12px;
    letter-spacing: 0.02em;
    color: #eaf6f8;
    background: #090d12;
    border: 1px solid rgba(90, 150, 180, 0.3);
    padding: 6px 8px;
    outline: none;
  }

  .sdn-channels-grant-field input::placeholder {
    color: #5a7a8a;
  }

  .sdn-channels-grant-form-buttons {
    display: flex;
    gap: 6px;
    margin-top: 4px;
  }

  .sdn-channels-grant-error {
    font-size: 10.5px;
    line-height: 1.4;
    color: #ff8d8d;
  }

  .sdn-channels-grant-success {
    margin-top: 10px;
    font-size: 10.5px;
    letter-spacing: 0.06em;
    color: #5ad6a0;
  }

  .sdn-channels-monitor-empty {
    font-size: 11px;
    color: #5d7681;
    letter-spacing: 0.06em;
  }
</style>

<script lang="ts">
  import type { ConjunctionEvent, ConjunctionScreenResult, SdnBackend } from '../../../src/ui/runtime/sdn-backend';

  export let backend: SdnBackend | null = null;

  let primarySchema = 'MPE.fbs';
  let secondarySchema = 'OMM.fbs';
  let encrypted = true;
  let grantId = '';
  let channelId = '';
  let assessorPeerId = '';
  let limit = 25;
  let outputMode: 'table' | 'json' | 'csv' = 'table';
  let status = 'Ready';
  let result: ConjunctionScreenResult | null = null;

  $: events = result?.events ?? [];
  $: resultText = outputMode === 'json'
    ? JSON.stringify(result ?? {}, null, 2)
    : outputMode === 'csv'
      ? eventsToCsv(events)
      : '';

  async function runScreen(): Promise<void> {
    if (!backend) {
      status = 'Backend unavailable';
      return;
    }
    status = 'Screening';
    const response = await backend.screenConjunction({
      primarySchema,
      secondarySchema,
      encrypted,
      grantId: grantId.trim() || undefined,
      channelId: channelId.trim() || undefined,
      assessorPeerId: assessorPeerId.trim() || undefined,
      includeProvenance: true,
      limit,
    });
    result = response.data ?? null;
    status = response.ok ? `${response.data?.count ?? 0} events` : response.capability.reason ?? 'Conjunction screening unavailable';
  }

  function eventsToCsv(rows: ConjunctionEvent[]): string {
    const header = ['primary_object', 'secondary_object', 'tca', 'miss_distance_km', 'probability', 'provider_id', 'source_name', 'status'];
    const lines = rows.map((row) => [
      row.primaryObject ?? '',
      row.secondaryObject ?? '',
      row.tca ?? '',
      row.missDistanceKm ?? '',
      row.probability ?? '',
      row.providerId ?? '',
      row.sourceName ?? '',
      row.status ?? '',
    ].map(csvCell).join(','));
    return [header.join(','), ...lines].join('\n');
  }

  function csvCell(value: unknown): string {
    const text = String(value ?? '');
    return /[",\n]/.test(text) ? `"${text.replace(/"/g, '""')}"` : text;
  }
</script>

<section class="sdn-channel-screen" aria-label="Conjunction">
  <div class="sdn-channel-toolbar">
    <label>
      <span>Primary</span>
      <select class="sdn-input sdn-select" bind:value={primarySchema}>
        <option value="MPE.fbs">Maneuver Ephemeris</option>
        <option value="OMM.fbs">OMM.fbs</option>
        <option value="OEM.fbs">OEM.fbs</option>
      </select>
    </label>
    <label>
      <span>Secondary</span>
      <select class="sdn-input sdn-select" bind:value={secondarySchema}>
        <option value="OMM.fbs">OMM.fbs</option>
        <option value="MPE.fbs">MPE.fbs</option>
        <option value="OEM.fbs">OEM.fbs</option>
      </select>
    </label>
    <label>
      <span>Grant</span>
      <input class="sdn-input" bind:value={grantId} placeholder="grant id" />
    </label>
    <label>
      <span>Channel</span>
      <input class="sdn-input" bind:value={channelId} placeholder="private channel" />
    </label>
    <label>
      <span>Assessor</span>
      <input class="sdn-input" bind:value={assessorPeerId} placeholder="peer id" />
    </label>
    <label>
      <span>Limit</span>
      <input class="sdn-input" type="number" min="1" max="500" bind:value={limit} />
    </label>
    <label>
      <span>Encrypted</span>
      <input type="checkbox" bind:checked={encrypted} />
    </label>
    <button class="sdn-button" type="button" on:click={runScreen} disabled={!backend}>Screen</button>
    <span>{status}</span>
  </div>

  <div class="sdn-channel-layout">
    <div class="sdn-channel-list">
      <div class="sdn-table-toolbar">
        <button class="sdn-button sdn-button-compact" class:sdn-button-muted={outputMode !== 'table'} type="button" on:click={() => outputMode = 'table'}>Table</button>
        <button class="sdn-button sdn-button-compact" class:sdn-button-muted={outputMode !== 'json'} type="button" on:click={() => outputMode = 'json'}>JSON</button>
        <button class="sdn-button sdn-button-compact" class:sdn-button-muted={outputMode !== 'csv'} type="button" on:click={() => outputMode = 'csv'}>CSV</button>
      </div>
      {#if outputMode === 'table'}
        <div class="sdn-table-wrap">
          <table class="sdn-table">
            <thead>
              <tr>
                <th>Primary</th>
                <th>Secondary</th>
                <th>TCA</th>
                <th>Miss km</th>
                <th>Pc</th>
                <th>Status</th>
              </tr>
            </thead>
            <tbody>
              {#each events as event}
                <tr>
                  <td>{event.primaryObject ?? 'unknown'}</td>
                  <td>{event.secondaryObject ?? 'unknown'}</td>
                  <td>{event.tca ?? 'pending'}</td>
                  <td>{event.missDistanceKm ?? ''}</td>
                  <td>{event.probability ?? ''}</td>
                  <td>{event.status ?? result?.status ?? 'pending'}</td>
                </tr>
              {:else}
                <tr>
                  <td colspan="6">No conjunction events returned</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {:else}
        <textarea class="sdn-input sdn-conjunction-output" readonly value={resultText}></textarea>
      {/if}
    </div>

    <div class="sdn-channel-detail">
      <div class="sdn-channel-heading">
        <div>
          <h2>{result?.mode ?? 'private-maneuver-ephemeris'}</h2>
          <span>{result?.workflow ?? 'encrypted-conjunction-assessment'}</span>
        </div>
        <span>{result?.status ?? 'ready'}</span>
      </div>
      <dl class="sdn-channel-metrics">
        <div><dt>Primary Schema</dt><dd>{result?.primarySchema ?? primarySchema}</dd></div>
        <div><dt>Secondary Schema</dt><dd>{result?.secondarySchema ?? secondarySchema}</dd></div>
        <div><dt>Grant</dt><dd>{result?.grantId ?? (grantId || 'not set')}</dd></div>
        <div><dt>Channel</dt><dd>{result?.channelId ?? (channelId || 'not set')}</dd></div>
        <div><dt>Assessor</dt><dd>{result?.assessorPeerId ?? (assessorPeerId || 'not set')}</dd></div>
        <div><dt>Events</dt><dd>{result?.count ?? 0}</dd></div>
      </dl>
    </div>
  </div>
</section>

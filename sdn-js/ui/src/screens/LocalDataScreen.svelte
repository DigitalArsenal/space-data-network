<script lang="ts">
  import type { LocalObjectSummary, StorageSummary } from '../../../src/ui/runtime/sdn-backend';
  import MetricCard from '../components/cards/MetricCard.svelte';

  export let storage: StorageSummary | null = null;
  export let objects: LocalObjectSummary[] = [];
  export let inspectionTarget: string | null = null;
  export let inspectionGatewayUrl: string | null = null;
  export let inspectionState = 'not-selected';

  function formatBytes(value: number | null | undefined): string {
    if (!value) return 'pending';
    if (value > 1_000_000_000) return `${(value / 1_000_000_000).toFixed(1)} GB`;
    if (value > 1_000_000) return `${(value / 1_000_000).toFixed(1)} MB`;
    return `${value} B`;
  }
</script>

<div class="sdn-grid sdn-grid-3">
  <MetricCard title="Used" value={formatBytes(storage?.usedBytes)} detail="Local SDN and Kubo usage" />
  <MetricCard title="Pinned" value={formatBytes(storage?.pinnedBytes)} detail="Pinned object total" />
  <MetricCard title="Quota" value={formatBytes(storage?.quotaBytes)} detail="Configured storage ceiling" />
</div>

<article class="sdn-card">
  <h2>Pins And Stored Objects</h2>
  <div class="sdn-table-wrap">
    <table class="sdn-table">
      <thead>
        <tr><th>Object</th><th>Schema</th><th>Source</th><th>State</th></tr>
      </thead>
      <tbody>
        {#each objects as object}
          <tr>
            <td>{object.label}</td>
            <td>{object.schema ?? 'unknown'}</td>
            <td>{object.source ?? 'local'}</td>
            <td>{object.state}</td>
          </tr>
        {:else}
          <tr><td colspan="4">No local objects loaded.</td></tr>
        {/each}
      </tbody>
    </table>
  </div>
</article>

<section class="sdn-panel-grid">
  <article class="sdn-card">
    <h2>Object Inspector</h2>
    {#if inspectionTarget}
      <dl class="sdn-details">
        <div><dt>CID</dt><dd><code>{inspectionTarget}</code></dd></div>
        <div><dt>Gateway</dt><dd>{inspectionState}</dd></div>
      </dl>
      {#if inspectionGatewayUrl}
        <a class="sdn-button" href={inspectionGatewayUrl} target="_blank" rel="noreferrer">Open Gateway</a>
      {:else}
        <p>Gateway access is unavailable for this object.</p>
      {/if}
    {:else}
      <p>Select a stored object to inspect provenance, signatures, and gateway access.</p>
    {/if}
  </article>
  <article class="sdn-card"><h2>Rulesets</h2><p>Retention, aging, and replication policies are represented as degraded until the local policy endpoint is wired.</p></article>
  <article class="sdn-card"><h2>SQL Workbench</h2><p>Local SQL queries use the backend contract and show degraded state when no local index is available.</p></article>
</section>

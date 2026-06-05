<script lang="ts">
  import { onMount } from 'svelte';
  import type { ChannelMonitor, ChannelSummary, SdnBackend } from '../../../src/ui/runtime/sdn-backend';

  export let backend: SdnBackend | null = null;

  let standardCode = 'OMM';
  let channels: ChannelSummary[] = [];
  let selectedChannelId = '';
  let selectedChannel: ChannelSummary | null = null;
  let monitor: ChannelMonitor | null = null;
  let status = 'Loading';

  onMount(() => {
    void refreshChannels();
  });

  $: if (backend && selectedChannelId && selectedChannel?.channelId !== selectedChannelId) {
    void loadChannel(selectedChannelId);
  }

  async function refreshChannels(): Promise<void> {
    if (!backend) {
      status = 'Backend unavailable';
      channels = [];
      return;
    }
    status = 'Loading';
    const result = await backend.channels.list({ standardCode });
    if (!result.ok) {
      status = result.capability.reason ?? 'Channels unavailable';
      channels = [];
      selectedChannel = null;
      monitor = null;
      return;
    }
    channels = result.data ?? [];
    selectedChannelId = channels[0]?.channelId ?? '';
    status = channels.length > 0 ? `${channels.length} channels` : 'No channels';
  }

  async function loadChannel(channelId: string): Promise<void> {
    if (!backend || !channelId) return;
    const detail = await backend.channels.get(channelId);
    selectedChannel = detail.data ?? null;
    const nextMonitor = await backend.channels.monitor(channelId);
    monitor = nextMonitor.data ?? null;
    if (!detail.ok || !nextMonitor.ok) {
      status = detail.capability.reason ?? nextMonitor.capability.reason ?? 'Channel monitor unavailable';
    }
  }

  async function subscribeSelected(): Promise<void> {
    if (!backend || !selectedChannelId) return;
    const result = await backend.channels.subscribe(selectedChannelId);
    status = result.ok ? 'Subscribed' : result.capability.reason ?? 'Subscribe unavailable';
    await loadChannel(selectedChannelId);
  }

  function formatNumber(value: number | null | undefined): string {
    return new Intl.NumberFormat().format(value ?? 0);
  }

  function formatBytes(value: number | null | undefined): string {
    const bytes = value ?? 0;
    if (bytes >= 1_000_000_000) return `${(bytes / 1_000_000_000).toFixed(2)} GB`;
    if (bytes >= 1_000_000) return `${(bytes / 1_000_000).toFixed(2)} MB`;
    if (bytes >= 1_000) return `${(bytes / 1_000).toFixed(1)} KB`;
    return `${bytes} B`;
  }

  function formatRate(value: number | null | undefined): string {
    return `${formatBytes(value)}/s`;
  }

  function formatPercent(value: number | null | undefined): string {
    return typeof value === 'number' ? `${(value * 100).toFixed(1)}%` : 'Unknown';
  }
</script>

<section class="sdn-channel-screen" aria-label="Channels">
  <div class="sdn-channel-toolbar">
    <label>
      <span>standardCode</span>
      <input bind:value={standardCode} maxlength="3" />
    </label>
    <button class="sdn-button" type="button" on:click={refreshChannels}>Refresh</button>
    <button class="sdn-button sdn-button-muted" type="button" on:click={subscribeSelected} disabled={!selectedChannelId}>Subscribe</button>
    <span>{status}</span>
  </div>

  <div class="sdn-channel-layout">
    <section class="sdn-channel-list" aria-label="Channel list">
      <table class="sdn-table">
        <thead>
          <tr>
            <th>Channel</th>
            <th>standardCode</th>
            <th>Visibility</th>
            <th>Subscribed</th>
            <th>Grant</th>
          </tr>
        </thead>
        <tbody>
          {#each channels as channel}
            <tr class:selected={channel.channelId === selectedChannelId} on:click={() => selectedChannelId = channel.channelId}>
              <td>{channel.channelId}</td>
              <td>{channel.standardCode}</td>
              <td>{channel.visibility}</td>
              <td>{channel.subscribed ? 'Yes' : 'No'}</td>
              <td>{channel.grantState}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </section>

    <section class="sdn-channel-detail" aria-label="Channel monitor">
      <div class="sdn-channel-heading">
        <h2>{selectedChannel?.channelId ?? 'Channel'}</h2>
        <span>{selectedChannel?.encryptionState ?? 'unknown'}</span>
      </div>
      <dl class="sdn-channel-metrics">
        <div><dt>Channel Head</dt><dd>{monitor?.channelHead || 'Unknown'}</dd></div>
        <div><dt>Verified PNM</dt><dd>{monitor?.pnmVerified ? 'Verified' : 'Unverified'}</dd></div>
        <div><dt>Provider Peer</dt><dd>{monitor?.providerPeer || 'Unknown'}</dd></div>
        <div><dt>Subscribed</dt><dd>{monitor?.subscribed ? 'Yes' : 'No'}</dd></div>
        <div><dt>Local Rows</dt><dd>{formatNumber(monitor?.localRows)}</dd></div>
        <div><dt>Remote Rows</dt><dd>{formatNumber(monitor?.remoteRows)}</dd></div>
        <div><dt>Synced Rows</dt><dd>{formatNumber(monitor?.syncedRows)}</dd></div>
        <div><dt>Missing Rows</dt><dd>{formatNumber(monitor?.missingRows)}</dd></div>
        <div><dt>Pinned Rows</dt><dd>{formatNumber(monitor?.pinnedRows)}</dd></div>
        <div><dt>Synced Bytes</dt><dd>{formatBytes(monitor?.syncedBytes)}</dd></div>
        <div><dt>Current Throughput</dt><dd>{formatRate(monitor?.throughputBytesPerSecond)}</dd></div>
        <div><dt>Wire-Speed Utilization</dt><dd>{formatPercent(monitor?.wireSpeedUtilization)}</dd></div>
        <div><dt>Grant State</dt><dd>{monitor?.grantState ?? selectedChannel?.grantState ?? 'unknown'}</dd></div>
        <div><dt>Encryption State</dt><dd>{monitor?.encryptionState ?? selectedChannel?.encryptionState ?? 'unknown'}</dd></div>
        <div><dt>Last Verified Update</dt><dd>{monitor?.lastVerifiedUpdate || 'Unknown'}</dd></div>
      </dl>
    </section>
  </div>
</section>

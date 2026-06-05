import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';

describe('SDN channels UI source', () => {
  it('wires the Channels screen into primary navigation without exposing internal schema suffixes', () => {
    const app = readFileSync(new URL('../../ui/src/App.svelte', import.meta.url), 'utf8');
    const nav = readFileSync(new URL('../../ui/src/components/SideNav.svelte', import.meta.url), 'utf8');
    const routes = readFileSync(new URL('../../ui/src/lib/routes.ts', import.meta.url), 'utf8');
    const internalSuffix = String.fromCharCode(46, 102, 98, 115);

    expect(app).toContain('ChannelsScreen');
    expect(nav).toContain('#/channels');
    expect(routes).toContain('/channels');
    expect(`${app}\n${nav}\n${routes}`).not.toContain(internalSuffix);
  });

  it('shows required monitor fields in the Channels screen source', () => {
    const source = readFileSync(new URL('../../ui/src/screens/ChannelsScreen.svelte', import.meta.url), 'utf8');
    for (const label of [
      'Channel Head',
      'Verified PNM',
      'Verified DPM',
      'Provider Peer',
      'Local Rows',
      'Remote Rows',
      'Synced Rows',
      'Missing Rows',
      'Pinned Count',
      'Synced Bytes',
      'Current Throughput',
      'Wire-Speed Utilization',
      'Grant State',
      'Encryption State',
      'Last Verified Update',
    ]) {
      expect(source).toContain(label);
    }
    expect(source).not.toContain(String.fromCharCode(46, 102, 98, 115));
  });

  it('shows monitor phase timings in the Channels screen source', () => {
    const source = readFileSync(new URL('../../ui/src/screens/ChannelsScreen.svelte', import.meta.url), 'utf8');
    for (const expected of [
      'Discovery',
      'Grant Negotiation',
      'PNM/DPM Verification',
      'Transfer',
      'Decrypt',
      'Hash Verification',
      'Durable Import',
      'monitor?.timingsMs.discovery',
      'monitor?.timingsMs.grantNegotiation',
      'monitor?.timingsMs.pnmDpmVerification',
      'monitor?.timingsMs.transfer',
      'monitor?.timingsMs.decrypt',
      'monitor?.timingsMs.hashVerification',
      'monitor?.timingsMs.durableImport',
    ]) {
      expect(source).toContain(expected);
    }
    expect(source).not.toContain(String.fromCharCode(46, 102, 98, 115));
  });

  it('renders channel filters and subscribe controls required by the pub/sub surface', () => {
    const source = readFileSync(new URL('../../ui/src/screens/ChannelsScreen.svelte', import.meta.url), 'utf8');
    for (const expected of [
      'Filter by source',
      'Filter by visibility',
      'Filter by grant state',
      'backend.channels.list({ standardCode, ...channelAccessOptions })',
      'filteredChannels',
      'channelMatchesFilters',
      'unsubscribeSelected',
      'backend.channels.unsubscribe(selectedChannelId, channelAccessOptions)',
      '>Unsubscribe<',
    ]) {
      expect(source).toContain(expected);
    }
    expect(source).not.toContain(String.fromCharCode(46, 102, 98, 115));
  });

  it('passes private grant context from the Channels screen to list and subscription actions', () => {
    const source = readFileSync(new URL('../../ui/src/screens/ChannelsScreen.svelte', import.meta.url), 'utf8');
    for (const expected of [
      'Grant subject',
      'Grant ID',
      'channelAccessOptions',
      'backend.channels.list({ standardCode, ...channelAccessOptions })',
      'backend.channels.get(channelId, channelAccessOptions)',
      'backend.channels.monitor(channelId, channelAccessOptions)',
      'backend.channels.subscribe(selectedChannelId, channelAccessOptions)',
      'backend.channels.unsubscribe(selectedChannelId, channelAccessOptions)',
    ]) {
      expect(source).toContain(expected);
    }
    expect(source).not.toContain(String.fromCharCode(46, 102, 98, 115));
  });

  it('renders a channel grant issue action required by the private pub/sub surface', () => {
    const source = readFileSync(new URL('../../ui/src/screens/ChannelsScreen.svelte', import.meta.url), 'utf8');
    for (const expected of [
      'Issue grant to',
      'Grant scopes',
      'issueGrantSelected',
      'parseGrantScopes',
      'backend.channels.issueGrant(selectedChannelId, { to: grantRecipient.trim(), scopes }, channelAccessOptions)',
      '>Issue Grant<',
    ]) {
      expect(source).toContain(expected);
    }
    expect(source).not.toContain(String.fromCharCode(46, 102, 98, 115));
  });

  it('renders a private channel key-envelope action required by the private pub/sub surface', () => {
    const source = readFileSync(new URL('../../ui/src/screens/ChannelsScreen.svelte', import.meta.url), 'utf8');
    for (const expected of [
      'Content key ID',
      'Recipient key ID',
      'keyUnwrapSelected',
      'backend.channels.keyUnwrap(selectedChannelId',
      'Key Envelope',
      'Key Envelope CID',
    ]) {
      expect(source).toContain(expected);
    }
    expect(source).not.toContain(String.fromCharCode(46, 102, 98, 115));
  });

  it('renders a native channel stream open action required by the pub/sub surface', () => {
    const source = readFileSync(new URL('../../ui/src/screens/ChannelsScreen.svelte', import.meta.url), 'utf8');
    for (const expected of [
      'openStreamSelected',
      'backend.channels.openStream(selectedChannelId, channelAccessOptions)',
      'streamBytesReceived',
      '>Open Stream<',
      'Stream Bytes',
    ]) {
      expect(source).toContain(expected);
    }
    expect(source).not.toContain(String.fromCharCode(46, 102, 98, 115));
  });

  it('renders a native channel stream publish action required by the pub/sub surface', () => {
    const source = readFileSync(new URL('../../ui/src/screens/ChannelsScreen.svelte', import.meta.url), 'utf8');
    for (const expected of [
      'Stream file',
      'onStreamFileSelected',
      'publishStreamSelected',
      'new Uint8Array(await streamFile.arrayBuffer())',
      'backend.channels.publish(selectedChannelId, bytes, channelAccessOptions)',
      '>Publish Stream<',
    ]) {
      expect(source).toContain(expected);
    }
    expect(source).not.toContain('readAsText');
    expect(source).not.toContain('btoa');
    expect(source).not.toContain(String.fromCharCode(46, 102, 98, 115));
  });
});

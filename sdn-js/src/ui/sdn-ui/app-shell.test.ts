import { render } from 'svelte/server';
import { describe, expect, it } from 'vitest';
import AppShell from '../../../ui/src/components/AppShell.svelte';

describe('SDN Svelte app shell', () => {
  it('renders the three SDN product navigation items without upstream WebUI nav', () => {
    const { body } = render(AppShell, {
      props: {
        activeRoute: '/node',
        backendMode: 'desktop-local',
        nodeState: 'online',
        peerCount: 2,
        walletState: 'claimed',
        storageLabel: '1.2 GB',
        title: 'Node',
      },
    });
    const text = body.replace(/<[^>]*>/g, ' ');

    expect(text).toContain('Node');
    expect(text).toContain('Peers');
    expect(text).toContain('Local Data');
    expect(text).not.toContain('Status');
    expect(text).not.toContain('Files');
  });
});

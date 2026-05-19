import { readFileSync } from 'node:fs';
import { render } from 'svelte/server';
import { describe, expect, it } from 'vitest';
import AppShell from '../../../ui/src/components/AppShell.svelte';

const appCss = readFileSync(new URL('../../../ui/src/styles/app.css', import.meta.url), 'utf8');

describe('SDN Svelte app shell', () => {
  it('renders the three SDN product navigation items without upstream WebUI nav', () => {
    const { body } = render(AppShell, {
      props: {
        activeRoute: '/node',
        backendMode: 'desktop-local',
        nodeState: 'online',
        peerCount: 2,
        storageLabel: '1.2 GB',
        title: 'Node',
      },
    });
    const text = body.replace(/<[^>]*>/g, ' ');

    expect(text).toContain('Node');
    expect(text).toContain('Peers');
    expect(text).toContain('Data');
    expect(text).not.toContain('Local Data');
    expect(text).not.toContain('Status');
    expect(text).not.toContain('Files');
  });

  it('keeps the desktop top bar draggable while controls remain interactive', () => {
    expect(appCss).toMatch(/\.sdn-top-bar\s*{[^}]*-webkit-app-region:\s*drag;/s);
    expect(appCss).toMatch(/\.sdn-top-meta\s*{[^}]*-webkit-app-region:\s*drag;/s);
    expect(appCss).toMatch(/button,\s*a,\s*input,\s*select,\s*textarea,[^}]*\.sdn-nav-link,[^}]*\.sdn-content[^}]*{[^}]*-webkit-app-region:\s*no-drag;/s);
    expect(appCss).not.toMatch(/button,\s*a,\s*input,\s*select,\s*textarea,[^}]*\.sdn-top-meta[^}]*{[^}]*-webkit-app-region:\s*no-drag;/s);
  });
});

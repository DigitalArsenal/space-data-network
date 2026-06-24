import fs from 'node:fs/promises';
import path from 'node:path';

import { describe, expect, it } from 'vitest';

const uiSrcPath = path.resolve(__dirname, '../../ui/src');

describe('SDN conjunction UI source contract', () => {
  it('registers an encrypted conjunction screen in the Svelte shell', async () => {
    const [app, sideNav, routes, screen] = await Promise.all([
      fs.readFile(path.join(uiSrcPath, 'App.svelte'), 'utf8'),
      fs.readFile(path.join(uiSrcPath, 'components', 'SideNav.svelte'), 'utf8'),
      fs.readFile(path.join(uiSrcPath, 'lib', 'routes.ts'), 'utf8'),
      fs.readFile(path.join(uiSrcPath, 'screens', 'ConjunctionScreen.svelte'), 'utf8'),
    ]);

    expect(app).toContain("import ConjunctionScreen from './screens/ConjunctionScreen.svelte'");
    expect(app).toContain("'/conjunction': 'Conjunction'");
    expect(app).toContain('<ConjunctionScreen {backend} />');
    expect(sideNav).toContain("{ href: '#/conjunction', route: '/conjunction', label: 'Conjunction' }");
    expect(routes).toContain("'/conjunction'");
    expect(routes).toContain("path.startsWith('/conjunction')");
    expect(screen).toContain('screenConjunction');
    expect(screen).toContain('primarySchema');
    expect(screen).toContain('secondarySchema');
    expect(screen).toContain('encrypted');
    expect(screen).toContain('grantId');
    expect(screen).toContain('channelId');
    expect(screen).toContain('assessorPeerId');
    expect(screen).toContain('Maneuver Ephemeris');
    expect(screen).toContain('OMM.fbs');
  });
});

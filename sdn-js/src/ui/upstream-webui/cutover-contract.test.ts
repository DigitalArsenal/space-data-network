import fs from 'node:fs/promises';
import path from 'node:path';

import { describe, expect, it } from 'vitest';

const uiSrcPath = path.resolve(__dirname, '../../../ui/src');
const mainEntryPath = path.join(uiSrcPath, 'main.ts');

async function listFilesRecursively(rootPath: string, currentPath = rootPath): Promise<string[]> {
  const entries = await fs.readdir(currentPath, { withFileTypes: true });
  const files = await Promise.all(entries.map(async (entry) => {
    const entryPath = path.join(currentPath, entry.name);
    if (entry.isDirectory()) {
      return listFilesRecursively(rootPath, entryPath);
    }
    return path.relative(rootPath, entryPath);
  }));
  return files.flat().sort();
}

describe('sdn upstream webui cutover contract', () => {
  it('keeps the product UI and upstream webui overlay paths explicit', async () => {
    await expect(listFilesRecursively(uiSrcPath)).resolves.toEqual([
      'App.svelte',
      'components/AdvancedDrawer.svelte',
      'components/AppShell.svelte',
      'components/DirectorySearchPanel.svelte',
      'components/IdentityPanel.svelte',
      'components/NodeIdentityGate.svelte',
      'components/SideNav.svelte',
      'components/StatusChip.svelte',
      'components/TopStatusBar.svelte',
      'components/cards/MetricCard.svelte',
      'lib/auth/auth-store.ts',
      'lib/auth/hd-wallet-wasm-vendor.ts',
      'lib/auth/local-wallet.ts',
      'lib/auth/sdn-api-client.ts',
      'lib/backend-context.ts',
      'lib/coi-serviceworker.js',
      'lib/cross-origin-isolation.ts',
      'lib/data-explorer-query.ts',
      'lib/data-loading-labels.ts',
      'lib/globe/SdnGlobe.ts',
      'lib/globe/land-dots-data.ts',
      'lib/globe/land-dots.ts',
      'lib/node-identity-session.ts',
      'lib/routes.ts',
      'lib/schema-sync-activity.ts',
      'lib/schema-sync-labels.ts',
      'lib/schema-sync-scheduler.ts',
      'lib/tokens.ts',
      'main.ts',
      'screens/ChannelsScreen.svelte',
      'screens/ConjunctionScreen.svelte',
      'screens/LocalDataScreen.svelte',
      'screens/NodeScreen.svelte',
      'screens/PeersScreen.svelte',
      'spaceaware/SpaceAwareApp.svelte',
      'spaceaware/fonts/LICENSE.md',
      'spaceaware/fonts/chakra-petch-latin-400.woff2',
      'spaceaware/fonts/chakra-petch-latin-500.woff2',
      'spaceaware/fonts/chakra-petch-latin-600.woff2',
      'spaceaware/fonts/chakra-petch-latin-700.woff2',
      'spaceaware/fonts/ibm-plex-mono-latin-400.woff2',
      'spaceaware/fonts/ibm-plex-mono-latin-500.woff2',
      'spaceaware/fonts/ibm-plex-mono-latin-600.woff2',
      'spaceaware/fonts/jetbrains-mono-latin-var.woff2',
      'spaceaware/lib/bmc2.test.ts',
      'spaceaware/lib/bmc2.ts',
      'spaceaware/lib/console.test.ts',
      'spaceaware/lib/console.ts',
      'spaceaware/lib/login.test.ts',
      'spaceaware/lib/login.ts',
      'spaceaware/lib/netmap-data.test.ts',
      'spaceaware/lib/netmap-data.ts',
      'spaceaware/lib/node-data.test.ts',
      'spaceaware/lib/node-data.ts',
      'spaceaware/lib/peers-data.test.ts',
      'spaceaware/lib/peers-data.ts',
      'spaceaware/main.ts',
      'spaceaware/primitives/Kicker.svelte',
      'spaceaware/primitives/Panel.svelte',
      'spaceaware/primitives/SaButton.svelte',
      'spaceaware/primitives/SaTabs.svelte',
      'spaceaware/primitives/StatusDot.svelte',
      'spaceaware/router.ts',
      'spaceaware/screens/Bmc2Router.svelte',
      'spaceaware/screens/ConsoleShell.svelte',
      'spaceaware/screens/LoginScreen.svelte',
      'spaceaware/screens/ScaffoldScreen.svelte',
      'spaceaware/screens/bmc2/Bmc2F1Surveillance.svelte',
      'spaceaware/screens/bmc2/Bmc2F2Track.svelte',
      'spaceaware/screens/bmc2/Bmc2F3Sensors.svelte',
      'spaceaware/screens/bmc2/Bmc2F4Conjunction.svelte',
      'spaceaware/screens/bmc2/Bmc2F5Maneuver.svelte',
      'spaceaware/screens/bmc2/Bmc2F6Comms.svelte',
      'spaceaware/screens/bmc2/Bmc2Index.svelte',
      'spaceaware/screens/bmc2/Bmc2TopBar.svelte',
      'spaceaware/screens/console/ConsoleHeader.svelte',
      'spaceaware/screens/console/ConsolePlaceholder.svelte',
      'spaceaware/screens/console/ConsoleRail.svelte',
      'spaceaware/screens/console/NodeView.svelte',
      'spaceaware/screens/console/PeersView.svelte',
      'spaceaware/screens/console/QrOverlay.svelte',
      'spaceaware/styles/spaceaware.css',
      'styles/app.css',
      'styles/tokens.css',
      'svelte-check-sentinel.svelte',
      'upstream-webui/branding.js',
      'upstream-webui/bundles/index.js',
      'upstream-webui/bundles/peer-locations.js',
      'upstream-webui/bundles/peers.js',
      'upstream-webui/bundles/redirects.js',
      'upstream-webui/index.js',
      'upstream-webui/overrides/App.js',
      'upstream-webui/overrides/bundles/routes.js',
      'upstream-webui/overrides/components/about-ipfs/AboutIpfs.js',
      'upstream-webui/overrides/components/about-webui/AboutWebUI.js',
      'upstream-webui/overrides/components/connected/Connected.js',
      'upstream-webui/overrides/components/is-connected/IsConnected.js',
      'upstream-webui/overrides/directory/DirectoryPage.js',
      'upstream-webui/overrides/marketplace/MarketplacePage.js',
      'upstream-webui/overrides/modules/ModulesPage.js',
      'upstream-webui/overrides/navigation/NavBar.js',
      'upstream-webui/overrides/navigation/sdn-logo-mark.svg',
      'upstream-webui/overrides/settings/SettingsPage.js',
      'upstream-webui/overrides/status/NodeInfo.js',
      'upstream-webui/overrides/status/NodeInfoAdvanced.js',
      'upstream-webui/overrides/status/StatusConnected.js',
      'upstream-webui/peer-source.js',
      'upstream-webui/vendor/components/about-ipfs/AboutIpfs.js',
      'upstream-webui/vendor/components/about-webui/AboutWebUI.js',
      'upstream-webui/vendor/components/connected/Connected.js',
      'upstream-webui/vendor/components/is-connected/IsConnected.js',
      'upstream-webui/vendor/navigation/NavBar.js',
      'upstream-webui/vendor/status/StatusConnected.js',
      'vite-env.d.ts',
    ]);
  });

  it('boots the root dashboard from the Svelte SDN app entrypoint', async () => {
    const source = await fs.readFile(mainEntryPath, 'utf8');

    expect(source).toContain("import App from './App.svelte'");
    expect(source).toContain('mount(App');
    expect(source).not.toContain('renderUpstreamWebUiBaseline');
    expect(source).not.toContain('bootstrapAdminApp');
    expect(source).not.toContain('renderAppShell');
  });

  it('keeps an exact upstream bootstrap flow while swapping in the SDN bundle entry', async () => {
    const upstreamEntryPath = path.join(uiSrcPath, 'upstream-webui', 'index.js');
    const source = await fs.readFile(upstreamEntryPath, 'utf8');

    expect(source).toContain("import App from './overrides/App.js'");
    expect(source).toContain("import getStore from './bundles/index.js'");
    expect(source).toContain("import { installRootDocumentTitleSync } from './branding.js'");
    expect(source).toContain("import { I18nextProvider } from 'react-i18next'");
    expect(source).toContain('installRootDocumentTitleSync();');
    expect(source).toContain('ReactDOM.render(');
    expect(source).toContain('<DndProvider backend={DndBackend}>');
    expect(source).not.toContain('<RootDocumentTitleSync />');
  });
});

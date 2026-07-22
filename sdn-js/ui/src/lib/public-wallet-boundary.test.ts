import { existsSync, readFileSync } from 'node:fs';

import { describe, expect, it } from 'vitest';

function read(relativeUrl: string): string {
  try {
    return readFileSync(new URL(relativeUrl, import.meta.url), 'utf8');
  } catch {
    return '';
  }
}

describe('SDN public wallet boundary', () => {
  it('has one immutable typed SDN client factory and no generic factory', () => {
    const walletClient = read('./auth/wallet-client.ts');
    const conjunction = read('../spaceaware/ConjunctionApp.svelte');
    const fullApp = read('../spaceaware/SpaceAwareApp.svelte');
    const authStore = read('./auth/auth-store.ts');
    const combined = [walletClient, conjunction, fullApp, authStore].join('\n');

    expect(walletClient, 'missing lib/auth/wallet-client.ts').not.toBe('');
    expect(walletClient).toContain("from 'hd-wallet-ui/client/sdn'");
    expect(walletClient).toContain('new WeakMap<Document, SdnWalletClient>()');
    expect(walletClient).toContain('walletConsumer.clientId !== SDN_WALLET_CLIENT_ID');
    expect(walletClient).toContain('walletConsumer.registryReleaseSha256 !== SDN_WALLET_REGISTRY_SHA256');
    expect((combined.match(/\bcreateSdnWalletClient\s*\(/g) ?? []).length).toBe(1);
    expect(combined).not.toMatch(/\bcreateWalletClient\s*\(/);
    expect(combined).not.toContain('hd-wallet-wasm');
  });

  it('keeps one visible presenter in the shipped shell and one shared seam in the dormant app', () => {
    const presenter = read('./PublicWalletPresenter.svelte');
    const conjunction = read('../spaceaware/ConjunctionApp.svelte');
    const fullApp = read('../spaceaware/SpaceAwareApp.svelte');

    expect(presenter, 'missing lib/PublicWalletPresenter.svelte').not.toBe('');
    expect((conjunction.match(/<PublicWalletPresenter\b/g) ?? []).length).toBe(1);
    expect((conjunction.match(/getSdnWalletClient\s*\(/g) ?? []).length).toBe(1);
    expect(conjunction).toContain('<PublicWalletPresenter client={walletClient} />');
    expect((fullApp.match(/getSdnWalletClient\s*\(/g) ?? []).length).toBe(1);
    expect(fullApp).toContain('wallet: walletClient');
    expect(fullApp).not.toContain('<PublicWalletPresenter');
    expect(presenter).toContain('client.connect()');
    expect(presenter).toContain('client.openAccount()');
    expect(presenter).toContain('client.subscribe');
    expect(presenter).toContain('unsubscribe();');
    expect(presenter).not.toContain('client.destroy()');
    expect(presenter).not.toContain('client.disconnect()');
  });

  it('pins the generated consumer row to the reviewed 2.0.28 release', () => {
    const config = read('./wallet-consumer.generated.ts');

    expect(config, 'missing lib/wallet-consumer.generated.ts').not.toBe('');
    expect(config).toContain('sdn-node-console-v1');
    expect(config).toContain('https://sdn.spaceaware.io/wallet/callback');
    expect(config).toContain('2.0.28');
    expect(config).toContain('e1ce6fe903c9700484a8a87d96581c8cad97063dabf63030b4518a31a3bdaa93');
    expect(config).toContain('sdn.wallet.account.v1');
    expect(config).toContain('sdn.wallet.connect.v1');
    expect(config).toContain('sdn-login:sdn.spaceaware.io');
  });

  it('installs the reviewed wallet package pair at the exact suite version', () => {
    const packageJson = JSON.parse(read('../../../package.json')) as {
      dependencies?: Record<string, string>;
    };
    const suiteVersions = JSON.parse(read('../../../../suite.versions.json')) as {
      dependencies?: Record<string, string>;
    };

    expect(packageJson.dependencies?.['hd-wallet-ui']).toBe('2.0.28');
    expect(packageJson.dependencies?.['hd-wallet-wasm']).toBe('2.0.28');
    expect(suiteVersions.dependencies?.hdWalletUI).toBe('2.0.28');
    expect(suiteVersions.dependencies?.hdWalletWasm).toBe('2.0.28');
  });

  it('does not reference private-key, signing, or credential implementation from the new surface', () => {
    const source = [
      read('./PublicWalletPresenter.svelte'),
      read('./auth/wallet-client.ts'),
      read('./wallet-consumer.generated.ts'),
    ].join('\n');

    for (const forbidden of [
      'createWalletUI',
      'hd-wallet-wasm',
      'local-wallet',
      'mnemonic',
      'privateKey',
      '.sign(',
      'requestSdnLoginV1(',
      'requestSdnLoginV2(',
    ]) {
      expect(source).not.toContain(forbidden);
    }
  });

  it('keeps the dormant SpaceAware graph on the Phase 1A public boundary', () => {
    const authStore = read('./auth/auth-store.ts');
    const loginScreen = read('../spaceaware/screens/LoginScreen.svelte');
    const loginLogic = read('../spaceaware/lib/login.ts');
    const presenter = read('./PublicWalletPresenter.svelte');
    const fullApp = read('../spaceaware/SpaceAwareApp.svelte');
    const productionGraph = [authStore, loginScreen, loginLogic, presenter, fullApp].join('\n');

    expect(existsSync(new URL('./auth/local-wallet.ts', import.meta.url))).toBe(false);
    expect(existsSync(new URL('./auth/hd-wallet-wasm-vendor.ts', import.meta.url))).toBe(false);
    expect(existsSync(new URL('../../../src/spaceaware-local-wallet.test.ts', import.meta.url))).toBe(false);

    for (const forbidden of [
      'UnlockedWallet',
      'createWalletClient',
      'hd-wallet-wasm',
      'local-wallet',
      'mnemonic',
      'privateKey',
      '.sign(',
      'loginWithWallet',
    ]) {
      expect(productionGraph).not.toContain(forbidden);
    }

    expect(loginScreen).not.toContain('OPERATOR ID');
    expect(loginScreen).not.toContain('PASSPHRASE');
    expect(loginScreen).not.toContain('type="password"');
    expect(loginScreen).not.toContain('authStore');
    expect(loginScreen).not.toContain('authState');
    expect(loginScreen).toContain('PEER ID / MULTIADDR');
    expect(loginScreen).toContain("navigate('/orbital')");

    // Phase 1A deliberately does not bridge raw V1 and JCS V2. The separate
    // server-auth-v2 cutover owns the first typed authentication call.
    expect(authStore).not.toContain('requestSdnLoginV1(');
    expect(authStore).not.toContain('requestSdnLoginV2(');
    expect(authStore).toContain('wallet: options.wallet');
  });

  it('does not rely on a protected-module stub to keep the conjunction build safe', () => {
    const conjunctionConfig = read('../../vite.conjunction.config.mts');

    expect(existsSync(new URL('../../shims/hd-wallet-wasm-empty.ts', import.meta.url))).toBe(false);
    expect(conjunctionConfig).not.toContain('hd-wallet-wasm');
    expect(conjunctionConfig).not.toContain('hd-wallet-wasm-empty');
  });

  it('removes the legacy wallet runtime from both shipped UI entry graphs', () => {
    const app = read('../App.svelte');
    const nodeScreen = read('../screens/NodeScreen.svelte');
    const identityPanel = read('../components/IdentityPanel.svelte');
    const topBar = read('../components/TopStatusBar.svelte');
    const upstreamApp = read('../upstream-webui/overrides/App.js');
    const runtimeIndex = read('../../../src/ui/runtime/index.ts');
    const desktopBackend = read('../../../src/ui/runtime/sdn-backend-desktop.ts');
    const viteConfig = read('../../../ui/vite.config.mts');
    const packageJson = read('../../../package.json');
    const productionSources = [
      app,
      nodeScreen,
      identityPanel,
      topBar,
      upstreamApp,
      runtimeIndex,
      desktopBackend,
      viteConfig,
      packageJson,
    ].join('\n');

    for (const relativeUrl of [
      '../../../src/ui/runtime/wallet-ui.ts',
      '../../../src/ui/runtime/wallet-modal.ts',
      '../../../src/ui/runtime/wallet-storage-bridge.ts',
      '../../../src/ui/runtime/account-menu.ts',
      '../../../src/ui/runtime/identity-core.ts',
      '../components/NodeIdentityGate.svelte',
      './node-identity-session.ts',
      '../../../src/types/hd-wallet-ui.d.ts',
      '../../../scripts/patch-hd-wallet-ui.mjs',
    ]) {
      expect(existsSync(new URL(relativeUrl, import.meta.url)), relativeUrl).toBe(false);
    }

    for (const forbidden of [
      'createWalletUI',
      'mountWalletUI',
      'NodeIdentityGate',
      'createNodeIdentitySessionController',
      'applyWalletNodeIdentity',
      'getWalletStorage',
      'saveWalletStorage',
      'login.sign',
      'Deterministic Keygen',
      'Import Passphrase',
      'Encrypted Private Key',
      'postinstall',
      "'/wallet-ui'",
      '/^hd-wallet-wasm$/',
      "from './wallet-ui'",
      "from './wallet-storage-bridge'",
      "from './account-menu'",
    ]) {
      expect(productionSources, `legacy production token: ${forbidden}`).not.toContain(forbidden);
    }

    expect(identityPanel).toContain('Read-only node identity');
    expect(identityPanel).toContain('Hosted EPM status');
    expect(identityPanel).not.toMatch(/<(?:button|input|form|textarea|select)\b/);
    expect(upstreamApp).not.toContain('SessionControls');
  });

  it('keeps the generic public UI graph out of the protected wallet crypto runtime', () => {
    const genericGraphSources = [
      read('../screens/LocalDataScreen.svelte'),
      read('../../../src/ui/runtime/identity.ts'),
      read('../../../src/ui/runtime/peer-identity.ts'),
      read('../../../src/ui/runtime/sdn-backend-adapter-utils.ts'),
      read('../../../src/ui/runtime/published-flatbuffer-shard.ts'),
      read('../../../src/field-stream.ts'),
      read('../../../src/ui/runtime/live-delivery.ts'),
      read('../screens/PeersScreen.svelte'),
    ];

    for (const source of genericGraphSources) {
      expect(source).not.toContain("crypto/hd-wallet");
      expect(source).not.toContain('derivePublicIdentityKeysFromXpub');
    }
    expect(genericGraphSources.join('\n')).not.toContain('deriveHostedEpmRecordKeysFromXpub');
  });

  it('keeps generated web artifacts free of the retired wallet implementation', () => {
    const conjunctionArtifact = read('../../../../sdn-server/cmd/spacedatanetwork/embedded/conjunction_app.html');
    const spaceAwareArtifact = read('../../../../sdn-server/cmd/spacedatanetwork/embedded/spaceaware_app.html');

    expect(conjunctionArtifact).toContain('sdn-node-console-v1');
    expect(conjunctionArtifact).toContain('https://wallet.spacedatanetwork.org');
    for (const [name, artifact] of [
      ['conjunction', conjunctionArtifact],
      ['spaceaware', spaceAwareArtifact],
    ] as const) {
      for (const forbidden of [
        'hd-wallet-wasm',
        'createWalletUI',
        'createWalletClient',
        'mountWalletUI',
        'NodeIdentityGate',
        'applyWalletNodeIdentity',
        'wallet-storage',
        'mnemonic',
        'privateKey',
      ]) {
        expect(artifact, `${name} emitted token: ${forbidden}`).not.toContain(forbidden);
      }
    }
  });
});

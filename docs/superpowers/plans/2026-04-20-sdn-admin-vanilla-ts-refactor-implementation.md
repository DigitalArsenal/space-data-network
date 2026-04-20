# SDN Admin Vanilla TypeScript Refactor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Refactor the `sdn-js` admin UI into smaller vanilla TypeScript controllers, state modules, and view helpers while keeping `/admin` behavior intact and preserving pure JS/CSS build artifacts.

**Architecture:** Keep all runtime/network logic in `sdn-js/src/ui/runtime`, move the current orchestration out of `sdn-js/ui/src/main.ts` into focused `bootstrap`, `state`, `dom`, `controllers`, and `views` modules, and preserve the current Vite entry/build shape. Execute in safe slices so the local Vite `/admin` flow and hosted `sdn-server` `/admin` flow keep working after each task.

**Tech Stack:** TypeScript, Vite, Vitest, Monaco editor, `hd-wallet-ui`, `hd-wallet-wasm`, `sdn-js` runtime modules, browser DOM APIs

---

## File Structure

- `sdn-js/ui/src/main.ts`
  - final thin entrypoint that only imports styles and calls `bootstrapAdminApp()`
- `sdn-js/ui/src/bootstrap.ts`
  - app startup, shell render, state creation, initial adapter/provider refresh, controller wiring
- `sdn-js/ui/src/runtime-modules.ts`
  - lazy loading of crypto/discovery/module-delivery/node/runtime helpers
- `sdn-js/ui/src/state/types.ts`
  - UI-local types currently embedded in `main.ts`
- `sdn-js/ui/src/state/app-state.ts`
  - shared mutable app state with explicit getters/setters
- `sdn-js/ui/src/dom/query.ts`
  - query/queryAll helpers with typed wrappers
- `sdn-js/ui/src/dom/escape.ts`
  - `escapeHtml`, `formatError`, `hexToBytes`, `uniqueStrings`
- `sdn-js/ui/src/dom/workspaces.ts`
  - workspace activation helper
- `sdn-js/ui/src/controllers/admin-shell-controller.ts`
  - local/server mode, connect server, shell meta, wallet trigger, feature carousel
- `sdn-js/ui/src/controllers/network-workspace-controller.ts`
  - provider refresh, runtime ensure, live flow, address lookup, timeline updates
- `sdn-js/ui/src/controllers/store-workspace-controller.ts`
  - marketplace refresh, store selection, feed/detail/spotlight rendering
- `sdn-js/ui/src/controllers/directory-workspace-controller.ts`
  - local/server directory panel rendering and admin user fetch
- `sdn-js/ui/src/controllers/frontend-workspace-controller.ts`
  - frontend tree/editor/upload/save/move/delete orchestration
- `sdn-js/ui/src/controllers/wallet-controller.ts`
  - account button + wallet workspace modal handling
- `sdn-js/ui/src/views/network-view.ts`
  - provider/timeline/observed-peer markup helpers
- `sdn-js/ui/src/views/store-view.ts`
  - store spotlight/feed/detail markup helpers
- `sdn-js/ui/src/views/directory-view.ts`
  - directory summary/user-roster markup helpers
- `sdn-js/ui/src/views/frontend-view.ts`
  - frontend status/tree/placeholder markup helpers
- `sdn-js/src/ui/app-shell.test.ts`
  - stays as shell smoke coverage, updated only if DOM hook names change
- `sdn-js/src/ui/runtime/*.test.ts`
  - reused for runtime behavior
- `sdn-js/src/ui/bootstrap-*.test.ts`
  - new focused tests for extracted state/controller seams

## Task 1: Extract Shared UI State and DOM Helpers

**Files:**
- Create: `sdn-js/ui/src/state/types.ts`
- Create: `sdn-js/ui/src/state/app-state.ts`
- Create: `sdn-js/ui/src/dom/query.ts`
- Create: `sdn-js/ui/src/dom/escape.ts`
- Create: `sdn-js/ui/src/dom/workspaces.ts`
- Test: `sdn-js/src/ui/bootstrap-app-state.test.ts`
- Modify: `sdn-js/ui/src/main.ts`

- [ ] **Step 1: Write the failing test**

```ts
import { describe, expect, it } from 'vitest';

import { createAppState } from '../../ui/src/state/app-state';

describe('createAppState', () => {
  it('tracks provider, delivery events, and store selection through explicit mutators', () => {
    const state = createAppState();

    state.setProvider({
      publicKey: '02abc',
      peerId: '16Uiu2HAmTest',
      relayAddresses: ['/ip4/127.0.0.1/tcp/14080/ws/p2p/16Uiu2HAmTest'],
    });
    state.pushDeliveryEvent({ stage: 'grant-received', detail: 'ok' });
    state.setStoreSelection({ kind: 'plugin', key: 'licensing@0.1.0' });

    expect(state.snapshot().provider?.peerId).toBe('16Uiu2HAmTest');
    expect(state.snapshot().deliveryEvents).toHaveLength(1);
    expect(state.snapshot().storeSelection).toEqual({ kind: 'plugin', key: 'licensing@0.1.0' });
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js && npx vitest run src/ui/bootstrap-app-state.test.ts`

Expected: FAIL with `Failed to load url ../../ui/src/state/app-state` or missing `createAppState`.

- [ ] **Step 3: Write minimal implementation**

```ts
// sdn-js/ui/src/state/types.ts
export interface ProviderDescriptor {
  publicKey: string;
  peerId: string;
  relayAddresses: string[];
  ipns?: string;
  identity?: {
    xpub?: string;
    identityPublicKey?: string;
    signingPublicKey?: string;
    encryptionPublicKey?: string;
    ipnsEntries?: string[];
    ensNames?: string[];
    addresses?: Array<{
      chain: string;
      address: string;
      keyPath?: string;
      publicKey?: string;
    }>;
  };
}

export interface ModuleDeliveryEventLike {
  stage: string;
  detail?: string;
  cid?: string;
  providerPeerId?: string;
}

export type StoreSelection =
  | { kind: 'author'; key: string }
  | { kind: 'plugin'; key: string }
  | { kind: 'data'; key: string };

// sdn-js/ui/src/state/app-state.ts
import { createMarketplaceIndex } from '../../src/ui/runtime/marketplace';
import { ObservedPeerIndex } from '../../src/ui/runtime/observed-peers';
import type { FrontendWorkspace } from '../../src/ui/runtime/frontend-workspace';
import type { AdminState } from '../../src/ui/runtime/admin-state';
import type { WalletModalController } from '../../src/ui/runtime/wallet-modal';
import type { FrontendEditorController } from '../frontend-editor';
import type { ModuleDeliveryEventLike, ProviderDescriptor, StoreSelection } from './types';

export function createAppState() {
  const data = {
    provider: null as ProviderDescriptor | null,
    node: null as unknown,
    identity: null as unknown,
    admin: null as AdminState | null,
    walletModal: null as WalletModalController | null,
    frontendWorkspace: null as FrontendWorkspace | null,
    frontendWorkspaceKey: null as string | null,
    frontendEditor: null as FrontendEditorController | null,
    marketplace: createMarketplaceIndex(),
    observedPeers: new ObservedPeerIndex(),
    deliveryEvents: [] as ModuleDeliveryEventLike[],
    storeSelection: null as StoreSelection | null,
  };

  return {
    snapshot: () => ({ ...data, deliveryEvents: [...data.deliveryEvents] }),
    setProvider(provider: ProviderDescriptor | null) { data.provider = provider; },
    pushDeliveryEvent(event: ModuleDeliveryEventLike) { data.deliveryEvents.push(event); },
    resetDeliveryEvents() { data.deliveryEvents = []; },
    setStoreSelection(selection: StoreSelection | null) { data.storeSelection = selection; },
    setAdmin(admin: AdminState | null) { data.admin = admin; },
    setWalletModal(walletModal: WalletModalController | null) { data.walletModal = walletModal; },
    setFrontendWorkspace(workspace: FrontendWorkspace | null, key: string | null) {
      data.frontendWorkspace = workspace;
      data.frontendWorkspaceKey = key;
    },
    setFrontendEditor(editor: FrontendEditorController | null) { data.frontendEditor = editor; },
  };
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js && npx vitest run src/ui/bootstrap-app-state.test.ts`

Expected: PASS with `1 passed`.

- [ ] **Step 5: Commit**

```bash
cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui
git add sdn-js/ui/src/state/types.ts sdn-js/ui/src/state/app-state.ts sdn-js/ui/src/dom/query.ts sdn-js/ui/src/dom/escape.ts sdn-js/ui/src/dom/workspaces.ts sdn-js/src/ui/bootstrap-app-state.test.ts sdn-js/ui/src/main.ts
git commit -m "refactor: extract admin UI shared state and DOM helpers"
```

## Task 2: Extract Bootstrap and Runtime Module Loading

**Files:**
- Create: `sdn-js/ui/src/runtime-modules.ts`
- Create: `sdn-js/ui/src/bootstrap.ts`
- Test: `sdn-js/src/ui/bootstrap-runtime.test.ts`
- Modify: `sdn-js/ui/src/main.ts`

- [ ] **Step 1: Write the failing test**

```ts
import { describe, expect, it, vi } from 'vitest';

import { createRuntimeModuleLoader } from '../../ui/src/runtime-modules';

describe('createRuntimeModuleLoader', () => {
  it('memoizes the dynamic runtime imports', async () => {
    const loadCrypto = vi.fn(async () => ({ initHDWallet: async () => true }));
    const loadDiscovery = vi.fn(async () => ({ discoverProvider: async () => ({ discoveryCID: 'cid' }) }));

    const loader = createRuntimeModuleLoader({
      loadCrypto,
      loadDiscovery,
      loadModuleDelivery: async () => ({ fetchEncryptedModuleBundle: async () => ({}) }),
      loadNode: async () => ({ SDNNode: { create: async () => ({}) } }),
      loadAddressLookup: async () => ({ normalizeAddressLookupKey: async () => ({ normalizedValue: 'x', discoveryCID: 'cid' }) }),
      loadLiveDelivery: async () => ({
        decryptEncryptedModuleBundle: async () => new Uint8Array(),
        invokeLoadedModule: async () => ({}),
        loadDecryptedModule: async () => ({}),
        unwrapGrantContentKey: async () => new Uint8Array(),
      }),
    });

    await loader.load();
    await loader.load();

    expect(loadCrypto).toHaveBeenCalledTimes(1);
    expect(loadDiscovery).toHaveBeenCalledTimes(1);
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js && npx vitest run src/ui/bootstrap-runtime.test.ts`

Expected: FAIL with missing `../../ui/src/runtime-modules`.

- [ ] **Step 3: Write minimal implementation**

```ts
// sdn-js/ui/src/runtime-modules.ts
export function createRuntimeModuleLoader(deps = {
  loadCrypto: () => import('../../src/crypto'),
  loadDiscovery: () => import('../../src/discovery'),
  loadModuleDelivery: () => import('../../src/module-delivery'),
  loadNode: () => import('../../src/node'),
  loadAddressLookup: () => import('../../src/ui/runtime/address-lookup'),
  loadLiveDelivery: () => import('../../src/ui/runtime/live-delivery'),
}) {
  let promise: Promise<any> | null = null;

  return {
    async load() {
      if (!promise) {
        promise = Promise.all([
          deps.loadCrypto(),
          deps.loadDiscovery(),
          deps.loadModuleDelivery(),
          deps.loadNode(),
          deps.loadAddressLookup(),
          deps.loadLiveDelivery(),
        ]).then(([crypto, discovery, moduleDelivery, node, addressLookup, liveDelivery]) => ({
          initHDWallet: crypto.initHDWallet,
          deriveIdentity: crypto.deriveIdentity,
          randomBytes: crypto.randomBytes,
          discoverProvider: discovery.discoverProvider,
          fetchEncryptedModuleBundle: moduleDelivery.fetchEncryptedModuleBundle,
          SDNNode: node.SDNNode,
          normalizeAddressLookupKey: addressLookup.normalizeAddressLookupKey,
          decryptEncryptedModuleBundle: liveDelivery.decryptEncryptedModuleBundle,
          invokeLoadedModule: liveDelivery.invokeLoadedModule,
          loadDecryptedModule: liveDelivery.loadDecryptedModule,
          unwrapGrantContentKey: liveDelivery.unwrapGrantContentKey,
        }));
      }
      return promise;
    },
  };
}

// sdn-js/ui/src/bootstrap.ts
import { renderAppShell } from './app';
import { createRuntimeModuleLoader } from './runtime-modules';
import { createAppState } from './state/app-state';

export async function bootstrapAdminApp(root: HTMLElement) {
  await renderAppShell(root);
  return {
    state: createAppState(),
    runtimeModules: createRuntimeModuleLoader(),
  };
}

// sdn-js/ui/src/main.ts
import { bootstrapAdminApp } from './bootstrap';
import './styles.css';

const root = document.querySelector('#app');
if (!(root instanceof HTMLElement)) {
  throw new Error('SDN UI root element not found');
}

bootstrapAdminApp(root).catch((error) => {
  root.innerHTML = `<pre class="sdn-error">${String(error)}</pre>`;
});
```

- [ ] **Step 4: Run test to verify it passes**

Run:

`cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js && npx vitest run src/ui/bootstrap-runtime.test.ts src/ui/app-shell.test.ts`

Expected: PASS with both tests green.

- [ ] **Step 5: Commit**

```bash
cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui
git add sdn-js/ui/src/runtime-modules.ts sdn-js/ui/src/bootstrap.ts sdn-js/ui/src/main.ts sdn-js/src/ui/bootstrap-runtime.test.ts
git commit -m "refactor: extract admin UI bootstrap and runtime loader"
```

## Task 3: Extract Shell Controller and Feature Carousel

**Files:**
- Create: `sdn-js/ui/src/controllers/admin-shell-controller.ts`
- Create: `sdn-js/ui/src/controllers/feature-carousel-controller.ts`
- Test: `sdn-js/src/ui/admin-shell-controller.test.ts`
- Modify: `sdn-js/ui/src/main.ts`
- Modify: `sdn-js/ui/src/app.ts`

- [ ] **Step 1: Write the failing test**

```ts
import { describe, expect, it } from 'vitest';

import { setActiveFeatureSlide } from '../../ui/src/controllers/feature-carousel-controller';

describe('setActiveFeatureSlide', () => {
  it('marks the selected slide and indicator active', () => {
    const root = document.createElement('div');
    root.innerHTML = `
      <article data-feature-slide="marketplace" class="sdn-feature-slide"></article>
      <article data-feature-slide="directory" class="sdn-feature-slide"></article>
      <button data-feature-target="marketplace"></button>
      <button data-feature-target="directory"></button>
    `;

    setActiveFeatureSlide(root, 'directory');

    expect(root.querySelector('[data-feature-slide="directory"]')?.classList.contains('sdn-feature-slide--active')).toBe(true);
    expect(root.querySelector('[data-feature-target="directory"]')?.classList.contains('sdn-feature-carousel__indicator--active')).toBe(true);
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js && npx vitest run src/ui/admin-shell-controller.test.ts`

Expected: FAIL with missing `feature-carousel-controller`.

- [ ] **Step 3: Write minimal implementation**

```ts
// sdn-js/ui/src/controllers/feature-carousel-controller.ts
export function setActiveFeatureSlide(root: ParentNode, featureId: string): void {
  root.querySelectorAll('[data-feature-slide]').forEach((slide) => {
    const active = slide.getAttribute('data-feature-slide') === featureId;
    slide.classList.toggle('sdn-feature-slide--active', active);
    slide.setAttribute('aria-hidden', active ? 'false' : 'true');
  });
  root.querySelectorAll('[data-feature-target]').forEach((button) => {
    const active = button.getAttribute('data-feature-target') === featureId;
    button.classList.toggle('sdn-feature-carousel__indicator--active', active);
    button.setAttribute('aria-selected', active ? 'true' : 'false');
  });
}

export function moveFeatureSlide(root: ParentNode, delta: number): void {
  const slides = Array.from(root.querySelectorAll('[data-feature-slide]'));
  const currentIndex = slides.findIndex((slide) => slide.classList.contains('sdn-feature-slide--active'));
  const nextIndex = currentIndex === -1 ? 0 : (currentIndex + delta + slides.length) % slides.length;
  const nextId = slides[nextIndex]?.getAttribute('data-feature-slide');
  if (nextId) setActiveFeatureSlide(root, nextId);
}

// sdn-js/ui/src/controllers/admin-shell-controller.ts
import { moveFeatureSlide, setActiveFeatureSlide } from './feature-carousel-controller';

export function bindAdminShell(root: HTMLElement, deps: {
  onToggleMode(): Promise<void>;
  onPromptConnectServer(): Promise<void>;
  onSelectWorkspace(workspaceId: string): Promise<void>;
  onOpenWallet(): Promise<void>;
}) {
  root.querySelector<HTMLButtonElement>('#sdn-mode-switch')?.addEventListener('click', () => { void deps.onToggleMode(); });
  root.querySelector<HTMLButtonElement>('#sdn-connect-server')?.addEventListener('click', () => { void deps.onPromptConnectServer(); });
  root.querySelector<HTMLButtonElement>('[data-feature-prev]')?.addEventListener('click', () => moveFeatureSlide(root, -1));
  root.querySelector<HTMLButtonElement>('[data-feature-next]')?.addEventListener('click', () => moveFeatureSlide(root, 1));
  root.querySelectorAll('[data-feature-target]').forEach((button) => {
    button.addEventListener('click', () => {
      const featureId = button.getAttribute('data-feature-target');
      if (featureId) setActiveFeatureSlide(root, featureId);
    });
  });
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js && npx vitest run src/ui/admin-shell-controller.test.ts src/ui/app-shell.test.ts`

Expected: PASS with carousel behavior and shell smoke both green.

- [ ] **Step 5: Commit**

```bash
cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui
git add sdn-js/ui/src/controllers/admin-shell-controller.ts sdn-js/ui/src/controllers/feature-carousel-controller.ts sdn-js/ui/src/main.ts sdn-js/ui/src/app.ts sdn-js/src/ui/admin-shell-controller.test.ts
git commit -m "refactor: extract admin shell and carousel controllers"
```

## Task 4: Extract Network Workspace Controller and Views

**Files:**
- Create: `sdn-js/ui/src/views/network-view.ts`
- Create: `sdn-js/ui/src/controllers/network-workspace-controller.ts`
- Test: `sdn-js/src/ui/network-workspace-controller.test.ts`
- Modify: `sdn-js/ui/src/main.ts`

- [ ] **Step 1: Write the failing test**

```ts
import { describe, expect, it } from 'vitest';

import { renderObservedPeerList } from '../../ui/src/views/network-view';

describe('renderObservedPeerList', () => {
  it('renders up to six observed peers with peer id, source list, and detail', () => {
    const html = renderObservedPeerList([
      { peerId: 'peer-a', sources: ['seed', 'dht'], detail: 'bootstrap' },
    ]);

    expect(html).toContain('peer-a');
    expect(html).toContain('seed, dht');
    expect(html).toContain('bootstrap');
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js && npx vitest run src/ui/network-workspace-controller.test.ts`

Expected: FAIL with missing `network-view`.

- [ ] **Step 3: Write minimal implementation**

```ts
// sdn-js/ui/src/views/network-view.ts
import { escapeHtml } from '../dom/escape';

export function renderProviderDescriptorPayload(provider: {
  publicKey: string;
  peerId: string;
  ipns?: string;
  relayAddresses: string[];
  identity?: Record<string, unknown>;
}): string {
  return JSON.stringify({
    publicKey: provider.publicKey,
    peerId: provider.peerId,
    ipns: provider.ipns,
    relayAddresses: provider.relayAddresses,
    identity: provider.identity,
  }, null, 2);
}

export function renderObservedPeerList(items: Array<{ peerId: string; sources: string[]; detail?: string }>): string {
  if (items.length === 0) {
    return 'DHT, provider, and protocol evidence will stream here.';
  }
  return items.slice(0, 6).map((item) => `
    <div class="sdn-sighting">
      <strong>${escapeHtml(item.peerId)}</strong>
      <span>${escapeHtml(item.sources.join(', '))}</span>
      <span>${escapeHtml(item.detail ?? '')}</span>
    </div>
  `).join('');
}

export function renderDeliveryTimeline(events: Array<{ stage: string; detail?: string; cid?: string }>): string {
  if (events.length === 0) {
    return '<div class="sdn-empty">Challenge, grant, fetch, decrypt, load, and invoke events appear in order.</div>';
  }
  return `<ol class="sdn-timeline">${events.map((event) => `
    <li><strong>${escapeHtml(event.stage)}</strong><span>${escapeHtml(event.detail ?? event.cid ?? '')}</span></li>
  `).join('')}</ol>`;
}
```

- [ ] **Step 4: Run test to verify it passes**

Run:

`cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js && npx vitest run src/ui/network-workspace-controller.test.ts src/ui/runtime/live-delivery.test.ts`

Expected: PASS with new network view coverage and existing delivery behavior still green.

- [ ] **Step 5: Commit**

```bash
cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui
git add sdn-js/ui/src/views/network-view.ts sdn-js/ui/src/controllers/network-workspace-controller.ts sdn-js/ui/src/main.ts sdn-js/src/ui/network-workspace-controller.test.ts
git commit -m "refactor: extract network workspace controller"
```

## Task 5: Extract Store and Directory Controllers and Views

**Files:**
- Create: `sdn-js/ui/src/views/store-view.ts`
- Create: `sdn-js/ui/src/views/directory-view.ts`
- Create: `sdn-js/ui/src/controllers/store-workspace-controller.ts`
- Create: `sdn-js/ui/src/controllers/directory-workspace-controller.ts`
- Test: `sdn-js/src/ui/store-workspace-controller.test.ts`
- Modify: `sdn-js/ui/src/main.ts`

- [ ] **Step 1: Write the failing test**

```ts
import { describe, expect, it } from 'vitest';

import { renderStoreChipRow } from '../../ui/src/views/store-view';

describe('renderStoreChipRow', () => {
  it('renders empty text when no standards are referenced', () => {
    expect(renderStoreChipRow([])).toContain('No SDS standards referenced');
  });

  it('renders chips for referenced standards', () => {
    expect(renderStoreChipRow(['OMM', 'OEM'])).toContain('OMM');
    expect(renderStoreChipRow(['OMM', 'OEM'])).toContain('OEM');
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js && npx vitest run src/ui/store-workspace-controller.test.ts`

Expected: FAIL with missing `store-view`.

- [ ] **Step 3: Write minimal implementation**

```ts
// sdn-js/ui/src/views/store-view.ts
import { escapeHtml } from '../dom/escape';

export function renderStoreChipRow(values: string[]): string {
  if (values.length === 0) {
    return '<span class="sdn-store-chip-row__empty">No SDS standards referenced</span>';
  }
  return `<div class="sdn-store-chip-row">${values.map((value) => `<span class="sdn-chip">${escapeHtml(value)}</span>`).join('')}</div>`;
}

// sdn-js/ui/src/views/directory-view.ts
import { escapeHtml } from '../dom/escape';

export function renderDirectorySummary(summary: {
  displayName: string;
  mode: string;
  role: string;
  transport: string;
  peerId: string | null;
  server: string | null;
}): string {
  return `
    <div class="sdn-stack">
      <strong>${escapeHtml(summary.displayName)}</strong>
      <span>Mode: ${escapeHtml(summary.mode)}</span>
      <span>Role: ${escapeHtml(summary.role)}</span>
      <span>Transport: ${escapeHtml(summary.transport)}</span>
      <span>Peer ID: ${escapeHtml(summary.peerId ?? '<unknown>')}</span>
      <span>Server: ${escapeHtml(summary.server ?? '<browser-local>')}</span>
    </div>
  `;
}
```

- [ ] **Step 4: Run test to verify it passes**

Run:

`cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js && npx vitest run src/ui/store-workspace-controller.test.ts src/ui/runtime/marketplace-source.test.ts src/ui/runtime/store-search.test.ts`

Expected: PASS with store rendering helpers and marketplace/search behavior green.

- [ ] **Step 5: Commit**

```bash
cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui
git add sdn-js/ui/src/views/store-view.ts sdn-js/ui/src/views/directory-view.ts sdn-js/ui/src/controllers/store-workspace-controller.ts sdn-js/ui/src/controllers/directory-workspace-controller.ts sdn-js/ui/src/main.ts sdn-js/src/ui/store-workspace-controller.test.ts
git commit -m "refactor: extract store and directory controllers"
```

## Task 6: Extract Frontend Workspace and Wallet Controllers, Then Finalize Entry Point

**Files:**
- Create: `sdn-js/ui/src/views/frontend-view.ts`
- Create: `sdn-js/ui/src/controllers/frontend-workspace-controller.ts`
- Create: `sdn-js/ui/src/controllers/wallet-controller.ts`
- Test: `sdn-js/src/ui/frontend-workspace-controller.test.ts`
- Modify: `sdn-js/ui/src/main.ts`
- Modify: `sdn-js/ui/src/bootstrap.ts`

- [ ] **Step 1: Write the failing test**

```ts
import { describe, expect, it } from 'vitest';

import { renderFrontendPlaceholder } from '../../ui/src/views/frontend-view';

describe('renderFrontendPlaceholder', () => {
  it('returns a consistent empty-state block for disconnected frontend management', () => {
    expect(renderFrontendPlaceholder('Connect as admin')).toContain('Connect as admin');
    expect(renderFrontendPlaceholder('Connect as admin')).toContain('sdn-empty');
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js && npx vitest run src/ui/frontend-workspace-controller.test.ts`

Expected: FAIL with missing `frontend-view`.

- [ ] **Step 3: Write minimal implementation**

```ts
// sdn-js/ui/src/views/frontend-view.ts
import { escapeHtml } from '../dom/escape';

export function renderFrontendPlaceholder(message: string): string {
  return `<div class="sdn-empty">${escapeHtml(message)}</div>`;
}

export function renderFrontendTree(nodes: Array<{ name: string; path: string; isDir: boolean; children?: any[] }>, selectedPath: string | null): string {
  if (nodes.length === 0) {
    return '<div class="sdn-empty">No frontend files available.</div>';
  }
  const renderNodes = (items: Array<{ name: string; path: string; isDir: boolean; children?: any[] }>): string => `
    <ul class="sdn-frontend-tree__list">
      ${items.map((node) => `
        <li class="sdn-frontend-tree__node">
          <button type="button" class="sdn-frontend-tree__item${node.path === selectedPath ? ' sdn-frontend-tree__item--active' : ''}" data-frontend-path="${escapeHtml(node.path)}">
            <span class="sdn-frontend-tree__badge">${node.isDir ? 'DIR' : 'FILE'}</span>
            <span>${escapeHtml(node.name)}</span>
          </button>
          ${node.children?.length ? renderNodes(node.children) : ''}
        </li>
      `).join('')}
    </ul>
  `;
  return renderNodes(nodes);
}

// sdn-js/ui/src/controllers/wallet-controller.ts
export function createWalletController(deps: { openAccount(): Promise<void> }) {
  return {
    async open() {
      await deps.openAccount();
    },
  };
}
```

- [ ] **Step 4: Run test to verify it passes**

Run:

`cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js && npx vitest run src/ui/frontend-workspace-controller.test.ts src/ui/runtime/frontend-workspace.test.ts src/ui/runtime/wallet-modal.test.ts src/ui/runtime/wallet-ui.test.ts src/ui/app-shell.test.ts && npm run build:ui`

Expected: PASS with all frontend/wallet tests green and Vite build succeeding.

- [ ] **Step 5: Commit**

```bash
cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui
git add sdn-js/ui/src/views/frontend-view.ts sdn-js/ui/src/controllers/frontend-workspace-controller.ts sdn-js/ui/src/controllers/wallet-controller.ts sdn-js/ui/src/bootstrap.ts sdn-js/ui/src/main.ts sdn-js/src/ui/frontend-workspace-controller.test.ts
git commit -m "refactor: extract frontend and wallet controllers"
```

## Task 7: Verify Local Dev Flow and Final Cleanup

**Files:**
- Modify: `sdn-js/ui/src/main.ts`
- Modify: `sdn-js/ui/src/app.ts`
- Modify: `sdn-js/ui/src/styles.css`
- Test: `sdn-js/src/ui/app-shell.test.ts`
- Test: `sdn-js/src/ui/styles-contract.test.ts`

- [ ] **Step 1: Write the failing test**

```ts
import { describe, expect, it } from 'vitest';

import { renderAppShell } from '../../ui/src/app';

describe('renderAppShell refactor contract', () => {
  it('still renders the shell entrypoints needed by bootstrap controllers', async () => {
    const root = { innerHTML: '', querySelector: () => null } as unknown as HTMLElement;

    await renderAppShell(root);

    expect(root.innerHTML).toContain('id="sdn-provider-url"');
    expect(root.innerHTML).toContain('id="sdn-store-search"');
    expect(root.innerHTML).toContain('id="sdn-frontend-editor"');
    expect(root.innerHTML).toContain('id="sdn-wallet-modal-host"');
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js && npx vitest run src/ui/app-shell.test.ts src/ui/styles-contract.test.ts`

Expected: FAIL if any renamed shell hooks or CSS contracts were not updated consistently.

- [ ] **Step 3: Write minimal implementation**

```ts
// sdn-js/ui/src/main.ts
import { bootstrapAdminApp } from './bootstrap';
import './styles.css';

const root = document.querySelector('#app');

if (!(root instanceof HTMLElement)) {
  throw new Error('SDN UI root element not found');
}

bootstrapAdminApp(root).catch((error) => {
  root.innerHTML = `<pre class="sdn-error">${String(error)}</pre>`;
  console.error('[sdn-ui] bootstrap failed', error);
});
```

- [ ] **Step 4: Run test to verify it passes**

Run:

`cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js && npx vitest run src/ui/app-shell.test.ts src/ui/styles-contract.test.ts src/ui/runtime/runtime-config.test.ts src/ui/runtime/marketplace-source.test.ts src/ui/runtime/live-delivery.test.ts src/ui/runtime/frontend-workspace.test.ts && npm run build:ui && cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui && npm run admin:dev`

Expected:
- Vitest suite PASS
- `vite build` PASS
- `npm run admin:dev` starts local server and Vite UI
- local server logs show connection to bootstrap peer `16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45`
- Vite UI available at `http://127.0.0.1:5173/admin/`

- [ ] **Step 5: Commit**

```bash
cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui
git add sdn-js/ui/src/main.ts sdn-js/ui/src/app.ts sdn-js/ui/src/styles.css sdn-js/src/ui/app-shell.test.ts sdn-js/src/ui/styles-contract.test.ts
git commit -m "refactor: finalize vanilla TS admin UI split"
```

## Self-Review

### Spec coverage

- Thin `main.ts` entrypoint: covered by Tasks 2 and 7.
- Shared bootstrap/state/dom layers: covered by Tasks 1 and 2.
- Shell controller extraction: covered by Task 3.
- Network workspace extraction: covered by Task 4.
- Store and directory extraction: covered by Task 5.
- Frontend and wallet extraction: covered by Task 6.
- Local `/admin` and `npm run admin:dev` verification: covered by Task 7.

No spec gaps remain.

### Placeholder scan

- No `TBD`, `TODO`, or “implement later” markers remain.
- Every implementation step includes concrete file paths, commands, and code skeletons.
- Every verification step includes an exact command and expected result.

### Type consistency

- Shared provider/event/store-selection shapes are defined first in Task 1.
- Runtime loader naming matches the current code (`initHDWallet`, `discoverProvider`, `fetchEncryptedModuleBundle`, `SDNNode`, `normalizeAddressLookupKey`, `decryptEncryptedModuleBundle`, `invokeLoadedModule`, `loadDecryptedModule`, `unwrapGrantContentKey`).
- Controller/view names are reused consistently across later tasks.

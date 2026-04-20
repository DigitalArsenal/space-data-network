import { ObservedPeerIndex } from '../../src/ui/runtime/observed-peers';
import { mountWalletUI } from '../../src/ui/runtime/wallet-ui';
import {
  createWalletModalController,
  type WalletModalController,
} from '../../src/ui/runtime/wallet-modal';
import {
  createAdminState,
  type AdminState,
  type AdminSnapshot,
} from '../../src/ui/runtime/admin-state';
import {
  createLocalFrontendTransport,
} from '../../src/ui/runtime/frontend-workspace';
import { createLocalAdapter } from '../../src/ui/runtime/local-adapter';
import { createServerAdapter } from '../../src/ui/runtime/server-adapter';
import { parseFirstBrowserBundle } from './browser-bundle';
import { renderAppShell } from './app';
import { readDefaultProviderDescriptorUrl } from '../../src/ui/runtime/runtime-config';
import { createAppState } from './state/app-state';
import type {
  ProviderDescriptor,
  RuntimeModules,
} from './state/types';
import { query, queryAll } from './dom/query';
import { escapeHtml, formatError } from './dom/escape';
import { setActiveWorkspace } from './dom/workspaces';
import { createRuntimeModuleLoader } from './runtime-modules';
import { bindAdminShell, renderShellMeta } from './controllers/admin-shell-controller';
import { createNetworkWorkspaceController } from './controllers/network-workspace-controller';
import { createStoreWorkspaceController } from './controllers/store-workspace-controller';
import { createDirectoryWorkspaceController } from './controllers/directory-workspace-controller';
import { createWalletController } from './controllers/wallet-controller';
import { createFrontendWorkspaceController } from './controllers/frontend-workspace-controller';
const runtimeModuleLoader = createRuntimeModuleLoader();
const defaultProviderDescriptorUrl = readDefaultProviderDescriptorUrl(import.meta.env);
let directoryController: ReturnType<typeof createDirectoryWorkspaceController> | null = null;
let frontendController: ReturnType<typeof createFrontendWorkspaceController> | null = null;

const DEFAULT_PROVIDER_DESCRIPTOR: ProviderDescriptor = {
  publicKey: '0257d9a39fac79d4c36e017b3b6913f60684586605ebb9370cf417ef44bf0f7cd2',
  peerId: '16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45',
  relayAddresses: [
    '/ip4/104.131.11.220/tcp/8080/ws/p2p/16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45',
  ],
  ipns: '/ipns/16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45',
};

const state = createAppState();

export async function bootstrapAdminApp(): Promise<void> {
  const root = document.querySelector('#app');
  if (!(root instanceof HTMLElement)) {
    throw new Error('SDN UI root element not found');
  }

  await renderAppShell(root, { mountWalletUI });
  const walletModalHost = query<HTMLElement>(root, '#sdn-wallet-modal-host');
  if (!walletModalHost) {
    throw new Error('wallet modal host element not found');
  }
  state.walletModal = createWalletModalController({
    mountWalletUI: async () => mountWalletUI(walletModalHost),
  });
  const initialServerTarget = inferInitialServerTarget();
  state.admin = createAdminState({
    localAdapter: () => createLocalAdapter({
      getNodeContext: async () => ({
        displayName: state.identity ? 'Local Helia requester' : 'Local Helia backend',
        xpub: null,
        transport: 'helia',
        descriptorUrl: inferProviderDescriptorUrl(root),
      }),
      getPermissions: async () => ({
        authenticated: Boolean(state.identity),
        role: 'local',
        canManageUsers: true,
        canManageFrontend: true,
        canManageStore: true,
        canOpenWallet: true,
      }),
    }),
    serverAdapter: (target) => createServerAdapter({ target }),
    ...(initialServerTarget
      ? {
        initialMode: 'server' as const,
        initialServerTarget,
      }
      : {}),
  });
  const networkController = createNetworkWorkspaceController({
    root,
    state,
    defaultProviderDescriptor: DEFAULT_PROVIDER_DESCRIPTOR,
    loadRuntimeModules,
    parseFirstBrowserBundle,
    getProviderDescriptorCandidates: () => [
      query<HTMLInputElement>(root, '#sdn-provider-url')?.value,
      inferProviderDescriptorUrl(root),
      `${window.location.origin}/api/module-delivery/provider`,
    ],
    getSelectedPluginListing: () => getSelectedPluginListing(root),
    onRefreshLocalAdmin: async () => {
      if (state.admin) {
        applyAdminSnapshot(root, await state.admin.refresh());
      }
    },
  });
  const storeController = createStoreWorkspaceController({
    root,
    state,
    getCatalogBaseUrl: () => inferCatalogBaseUrl(root),
    onObservedPeer: (peerId, source, detail) => {
      networkController.recordObservedPeer(peerId, source, detail);
    },
  });
  directoryController = createDirectoryWorkspaceController({ root, state });
  const walletController = createWalletController({ state });
  frontendController = createFrontendWorkspaceController({ root, state });

  bindUI(root, networkController, storeController, walletController, frontendController);
  const initialSnapshot = initialServerTarget
    ? await state.admin.connectServer(initialServerTarget)
    : await state.admin.connectLocal();
  applyAdminSnapshot(root, initialSnapshot);
  const providerUrl = query<HTMLInputElement>(root, '#sdn-provider-url');
  const initialProviderUrl = defaultProviderDescriptorUrl
    ?? (initialSnapshot.serverTarget?.baseUrl
      ? `${initialSnapshot.serverTarget.baseUrl}/api/module-delivery/provider`
      : null);
  if (providerUrl && initialProviderUrl) {
    providerUrl.value = initialProviderUrl;
  }
  await networkController.refreshProviderDescriptor();
  await storeController.refreshMarketplace();
}

async function loadRuntimeModules(): Promise<RuntimeModules> {
  return runtimeModuleLoader.load();
}

function bindUI(
  root: HTMLElement,
  networkController: ReturnType<typeof createNetworkWorkspaceController>,
  storeController: ReturnType<typeof createStoreWorkspaceController>,
  walletController: ReturnType<typeof createWalletController>,
  frontendController: ReturnType<typeof createFrontendWorkspaceController>,
): void {
  bindAdminShell(root, {
    onToggleMode: () => toggleAdminMode(root, networkController),
    onConnectServer: () => promptAndConnectServer(root, networkController),
    onOpenWallet: () => walletController.openWalletAccount(),
    onSetWorkspace: (workspaceId) => setWorkspace(root, workspaceId),
  });
  query<HTMLInputElement>(root, '#sdn-provider-url')?.addEventListener('change', () => {
    void networkController.refreshProviderDescriptor();
  });
  query<HTMLButtonElement>(root, '#sdn-refresh-provider')?.addEventListener('click', () => {
    void networkController.refreshProviderDescriptor();
  });
  query<HTMLButtonElement>(root, '#sdn-refresh-marketplace')?.addEventListener('click', () => {
    void storeController.refreshMarketplace();
  });
  query<HTMLInputElement>(root, '#sdn-store-search')?.addEventListener('input', () => {
    storeController.renderMarketplace();
  });
  query<HTMLElement>(root, '#sdn-store-results')?.addEventListener('click', (event) => {
    const target = event.target;
    if (!(target instanceof HTMLElement)) {
      return;
    }
    const button = target.closest<HTMLElement>('[data-store-result-kind][data-store-result-key]');
    if (!button) {
      return;
    }
    const kind = button.getAttribute('data-store-result-kind');
    const key = button.getAttribute('data-store-result-key');
    if (!kind || !key) {
      return;
    }
    state.storeSelection = kind === 'plugin'
      ? { kind: 'plugin', key }
      : kind === 'author'
        ? { kind: 'author', key }
        : { kind: 'data', key };
    storeController.renderMarketplace();
  });
  query<HTMLElement>(root, '#sdn-store-detail')?.addEventListener('click', (event) => {
    const target = event.target;
    if (!(target instanceof HTMLElement)) {
      return;
    }
    if (target.closest('#sdn-run-live-flow')) {
      void setWorkspace(root, 'network').then(() => networkController.runLiveFlow());
      return;
    }
    const workspaceButton = target.closest<HTMLElement>('[data-store-open-workspace]');
    const workspaceId = workspaceButton?.getAttribute('data-store-open-workspace');
    if (workspaceId) {
      void setWorkspace(root, workspaceId);
    }
  });
  query<HTMLButtonElement>(root, '#sdn-address-lookup-run')?.addEventListener('click', () => {
    void networkController.runAddressLookup();
  });
  query<HTMLButtonElement>(root, '#sdn-account-button')?.addEventListener('click', () => {
    void walletController.openWalletAccount();
  });
  query<HTMLButtonElement>(root, '#sdn-frontend-upload')?.addEventListener('click', () => {
    query<HTMLInputElement>(root, '#sdn-frontend-upload-input')?.click();
  });
  query<HTMLInputElement>(root, '#sdn-frontend-upload-input')?.addEventListener('change', (event) => {
    void frontendController.uploadFrontendFiles(event.currentTarget as HTMLInputElement | null);
  });
  query<HTMLButtonElement>(root, '#sdn-frontend-save')?.addEventListener('click', () => {
    void frontendController.saveFrontendFile();
  });
  query<HTMLButtonElement>(root, '#sdn-frontend-move')?.addEventListener('click', () => {
    void frontendController.moveFrontendFile();
  });
  query<HTMLButtonElement>(root, '#sdn-frontend-delete')?.addEventListener('click', () => {
    void frontendController.deleteFrontendFile();
  });
  query<HTMLElement>(root, '#sdn-frontend-tree')?.addEventListener('click', (event) => {
    const target = event.target;
    if (!(target instanceof HTMLElement)) {
      return;
    }
    const button = target.closest('[data-frontend-path]');
    if (!(button instanceof HTMLElement)) {
      return;
    }
    const path = button.getAttribute('data-frontend-path');
    if (path) {
      void frontendController.selectFrontendFile(path);
    }
  });
  const frontendWorkspace = query<HTMLElement>(root, '#sdn-frontend-workspace');
  frontendWorkspace?.addEventListener('dragover', (event) => {
    event.preventDefault();
  });
  frontendWorkspace?.addEventListener('drop', (event) => {
    event.preventDefault();
    const files = [...(event.dataTransfer?.files ?? [])];
    if (files.length > 0) {
      void frontendController.uploadFrontendFileList(files);
    }
  });
}

function inferCatalogBaseUrl(root: HTMLElement): string {
  const providerUrl = query<HTMLInputElement>(root, '#sdn-provider-url')?.value.trim();
  if (providerUrl) {
    try {
      const url = new URL(providerUrl);
      return url.origin;
    } catch {
      // Fall back to same origin below.
    }
  }
  const serverBaseUrl = state.admin?.snapshot().serverTarget?.baseUrl;
  if (serverBaseUrl) {
    return serverBaseUrl;
  }
  return window.location.origin;
}



async function toggleAdminMode(
  root: HTMLElement,
  networkController: ReturnType<typeof createNetworkWorkspaceController>,
): Promise<void> {
  const admin = requireAdminState();
  const snapshot = admin.snapshot();
  if (snapshot.mode === 'local') {
    try {
      applyAdminSnapshot(root, await admin.setMode('server'));
      await networkController.refreshProviderDescriptor();
      return;
    } catch {
      await promptAndConnectServer(root, networkController);
      return;
    }
  }

  applyAdminSnapshot(root, await admin.setMode('local'));
  await networkController.refreshProviderDescriptor();
}

async function promptAndConnectServer(
  root: HTMLElement,
  networkController: ReturnType<typeof createNetworkWorkspaceController>,
): Promise<void> {
  const admin = requireAdminState();
  const currentTarget = admin.snapshot().serverTarget?.baseUrl ?? 'https://sdn.spaceaware.io';
  const candidate = window.prompt(
    'Enter the admin base URL for the remote SDN node:',
    currentTarget,
  );
  if (!candidate) {
    return;
  }

  const snapshot = await admin.connectServer({
    baseUrl: candidate,
  });
  applyAdminSnapshot(root, snapshot);
  const providerUrl = query<HTMLInputElement>(root, '#sdn-provider-url');
  if (providerUrl && snapshot.serverTarget?.baseUrl) {
    providerUrl.value = `${snapshot.serverTarget.baseUrl}/api/module-delivery/provider`;
  }
  await networkController.refreshProviderDescriptor();
}

async function setWorkspace(root: HTMLElement, workspaceId: string): Promise<void> {
  if (workspaceId === 'wallet') {
    await state.walletModal?.openAccount();
    return;
  }
  const admin = requireAdminState();
  applyAdminSnapshot(root, await admin.setWorkspace(workspaceId));
}

function applyAdminSnapshot(root: HTMLElement, snapshot: AdminSnapshot): void {
  renderShellMeta(root, snapshot);
  setActiveWorkspace(root, snapshot.workspace.activeId);
  void directoryController?.refreshDirectoryPanel();
  void frontendController?.refreshFrontendWorkspace();
}

function inferProviderDescriptorUrl(root: HTMLElement): string {
  const providerUrl = query<HTMLInputElement>(root, '#sdn-provider-url')?.value.trim();
  if (providerUrl) {
    return providerUrl;
  }
  if (defaultProviderDescriptorUrl) {
    return defaultProviderDescriptorUrl;
  }
  const serverBaseUrl = state.admin?.snapshot().serverTarget?.baseUrl;
  if (serverBaseUrl) {
    return `${serverBaseUrl}/api/module-delivery/provider`;
  }
  return `${window.location.origin}/api/module-delivery/provider`;
}

function inferInitialServerTarget() {
  if (!window.location.pathname.startsWith('/admin')) {
    return null;
  }
  return {
    baseUrl: window.location.origin,
    label: 'Current server',
  };
}

function requireAdminState(): AdminState {
  if (!state.admin) {
    throw new Error('admin state not initialized');
  }
  return state.admin;
}

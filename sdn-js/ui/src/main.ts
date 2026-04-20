import { createMarketplaceIndex, canonicalListingKey } from '../../src/ui/runtime/marketplace';
import { loadMarketplaceListingsFromServer } from '../../src/ui/runtime/marketplace-source';
import { ObservedPeerIndex } from '../../src/ui/runtime/observed-peers';
import type { CanonicalListing, ObservedPeerSource } from '../../src/ui/runtime/types';
import { mountWalletUI } from '../../src/ui/runtime/wallet-ui';
import {
  createAccountMenuController,
  type AccountMenuController,
} from '../../src/ui/runtime/account-menu';
import {
  createAdminState,
  type AdminState,
  type AdminSnapshot,
} from '../../src/ui/runtime/admin-state';
import {
  createFrontendWorkspace,
  createLocalFrontendTransport,
  createServerFrontendTransport,
  type FrontendUploadFile,
  type FrontendWorkspace,
} from '../../src/ui/runtime/frontend-workspace';
import { createLocalAdapter } from '../../src/ui/runtime/local-adapter';
import { createServerAdapter } from '../../src/ui/runtime/server-adapter';
import {
  createBrowserEditorController,
  type FrontendEditorController,
} from './frontend-editor';
import { parseFirstBrowserBundle } from './browser-bundle';
import { renderAppShell } from './app';
import './styles.css';

interface ProviderDescriptor {
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

interface ModuleDeliveryEventLike {
  stage: string;
  detail?: string;
  cid?: string;
  providerPeerId?: string;
}

interface RuntimeIdentityLike {
  encryptionKey: {
    privateKey: Uint8Array;
  };
}

interface RuntimeNodeLike {
  dial: (address: string) => Promise<void>;
  requestModuleGrant: (options: Record<string, unknown>) => Promise<unknown>;
  discoverProviders: (discoveryCID: string) => Promise<Array<{ peerId: string; multiaddrs: string[] }>>;
}

interface RuntimeModules {
  initHDWallet: () => Promise<boolean>;
  deriveIdentity: (entropy: Uint8Array) => Promise<RuntimeIdentityLike>;
  randomBytes: (length: number) => Uint8Array;
  discoverProvider: (publicKey: Uint8Array) => Promise<{ discoveryCID: string }>;
  fetchEncryptedModuleBundle: (
    node: RuntimeNodeLike,
    grant: unknown,
    options?: {
      onEvent?: (event: ModuleDeliveryEventLike) => void;
    },
  ) => Promise<{
    grant: {
      wrappedContentKey: unknown;
      bundleDescriptor: {
        cid: string;
      };
    };
    encryptedBundleBytes: Uint8Array;
  }>;
  SDNNode: {
    create: (
      config: Record<string, unknown>,
      handlers: {
        onPeerConnected?: (peerId: string) => void;
        onPeerDisconnected?: (peerId: string) => void;
        onModuleDeliveryEvent?: (event: ModuleDeliveryEventLike) => void;
      },
    ) => Promise<RuntimeNodeLike>;
  };
  normalizeAddressLookupKey: (chain: string, value: string) => Promise<{
    normalizedValue: string;
    discoveryCID: string;
  }>;
  decryptEncryptedModuleBundle: (
    encryptedBundleBytes: Uint8Array,
    contentKey: Uint8Array,
    observer?: {
      onEvent?: (event: ModuleDeliveryEventLike) => void;
    },
  ) => Promise<Uint8Array>;
  invokeLoadedModule: <TResult = unknown>(
    harness: unknown,
    request: unknown,
    observer?: {
      onEvent?: (event: ModuleDeliveryEventLike) => void;
    },
  ) => Promise<TResult>;
  loadDecryptedModule: (
    wasmBytes: Uint8Array,
    observer?: {
      onEvent?: (event: ModuleDeliveryEventLike) => void;
    },
  ) => Promise<unknown>;
  unwrapGrantContentKey: (
    wrappedContentKey: unknown,
    recipientPrivateKey: Uint8Array,
    observer?: {
      onEvent?: (event: ModuleDeliveryEventLike) => void;
    },
  ) => Promise<Uint8Array>;
}

interface DirectoryUserLike {
  xpub: string;
  name?: string;
  trust_level?: string;
  source?: string;
  last_login?: string;
}

let runtimeModulesPromise: Promise<RuntimeModules> | null = null;

const DEFAULT_PROVIDER_DESCRIPTOR: ProviderDescriptor = {
  publicKey: '0257d9a39fac79d4c36e017b3b6913f60684586605ebb9370cf417ef44bf0f7cd2',
  peerId: '16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45',
  relayAddresses: [
    '/ip4/104.131.11.220/tcp/8080/ws/p2p/16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45',
  ],
  ipns: '/ipns/16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45',
};

const state = {
  provider: null as ProviderDescriptor | null,
  node: null as RuntimeNodeLike | null,
  identity: null as RuntimeIdentityLike | null,
  admin: null as AdminState | null,
  accountMenu: null as AccountMenuController | null,
  frontendWorkspace: null as FrontendWorkspace | null,
  frontendWorkspaceKey: null as string | null,
  frontendEditor: null as FrontendEditorController | null,
  localFrontendTransport: createLocalFrontendTransport({
    'index.html': [
      '<!doctype html>',
      '<html lang="en">',
      '<head><meta charset="utf-8"><title>SDN Local Frontend</title></head>',
      '<body><h1>Space Data Network</h1><p>Local browser-backed workspace.</p></body>',
      '</html>',
    ].join(''),
    'src/main.ts': 'console.log("Space Data Network local frontend");\n',
    'styles/site.css': 'body { font-family: "IBM Plex Sans", sans-serif; }\n',
  }),
  marketplace: createMarketplaceIndex(),
  observedPeers: new ObservedPeerIndex(),
  deliveryEvents: [] as ModuleDeliveryEventLike[],
};

async function bootstrap(): Promise<void> {
  const root = document.querySelector('#app');
  if (!(root instanceof HTMLElement)) {
    throw new Error('SDN UI root element not found');
  }

  await renderAppShell(root, { mountWalletUI });
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
  state.accountMenu = createAccountMenuController({
    mountWalletUI: async () => {
      const host = query<HTMLElement>(root, '#sdn-account-wallet-panel');
      if (!host) {
        return undefined;
      }
      return mountWalletUI(host);
    },
    onSignOut: async () => {
      const snapshot = requireAdminState().snapshot();
      if (snapshot.mode !== 'server' || !snapshot.serverTarget?.baseUrl) {
        return;
      }
      const response = await fetch(`${snapshot.serverTarget.baseUrl}/api/auth/logout`, {
        method: 'POST',
        credentials: 'include',
      });
      if (!response.ok) {
        throw new Error(`logout failed (${response.status})`);
      }
    },
  });

  bindUI(root);
  const initialSnapshot = initialServerTarget
    ? await state.admin.connectServer(initialServerTarget)
    : await state.admin.connectLocal();
  applyAdminSnapshot(root, initialSnapshot);
  const providerUrl = query<HTMLInputElement>(root, '#sdn-provider-url');
  if (providerUrl && initialSnapshot.serverTarget?.baseUrl) {
    providerUrl.value = `${initialSnapshot.serverTarget.baseUrl}/api/module-delivery/provider`;
  }
  await refreshProviderDescriptor(root);
  await refreshMarketplace(root);
}

async function loadRuntimeModules(): Promise<RuntimeModules> {
  if (!runtimeModulesPromise) {
    runtimeModulesPromise = Promise.all([
      import('../../src/crypto'),
      import('../../src/discovery'),
      import('../../src/module-delivery'),
      import('../../src/node'),
      import('../../src/ui/runtime/address-lookup'),
      import('../../src/ui/runtime/live-delivery'),
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
  return runtimeModulesPromise;
}

function bindUI(root: HTMLElement): void {
  bindShell(root);
  query<HTMLInputElement>(root, '#sdn-provider-url')?.addEventListener('change', () => {
    void refreshProviderDescriptor(root);
  });
  query<HTMLButtonElement>(root, '#sdn-refresh-provider')?.addEventListener('click', () => {
    void refreshProviderDescriptor(root);
  });
  query<HTMLButtonElement>(root, '#sdn-refresh-marketplace')?.addEventListener('click', () => {
    void refreshMarketplace(root);
  });
  query<HTMLSelectElement>(root, '#sdn-marketplace-select')?.addEventListener('change', () => {
    renderMarketplace(root);
    renderModuleMetadata(root);
  });
  query<HTMLButtonElement>(root, '#sdn-run-live-flow')?.addEventListener('click', () => {
    void runLiveFlow(root);
  });
  query<HTMLButtonElement>(root, '#sdn-address-lookup-run')?.addEventListener('click', () => {
    void runAddressLookup(root);
  });
  query<HTMLButtonElement>(root, '#sdn-account-button')?.addEventListener('click', () => {
    void openAccountDialog(root);
  });
  query<HTMLButtonElement>(root, '#sdn-account-close')?.addEventListener('click', () => {
    closeAccountDialog(root);
  });
  query<HTMLElement>(root, '[data-account-dismiss="backdrop"]')?.addEventListener('click', () => {
    closeAccountDialog(root);
  });
  query<HTMLButtonElement>(root, '#sdn-account-open-wallet')?.addEventListener('click', () => {
    void openAccountWallet(root);
  });
  query<HTMLButtonElement>(root, '#sdn-account-signout')?.addEventListener('click', () => {
    void signOutAccount(root);
  });
  query<HTMLButtonElement>(root, '#sdn-frontend-upload')?.addEventListener('click', () => {
    query<HTMLInputElement>(root, '#sdn-frontend-upload-input')?.click();
  });
  query<HTMLInputElement>(root, '#sdn-frontend-upload-input')?.addEventListener('change', (event) => {
    void uploadFrontendFiles(root, event.currentTarget as HTMLInputElement | null);
  });
  query<HTMLButtonElement>(root, '#sdn-frontend-save')?.addEventListener('click', () => {
    void saveFrontendFile(root);
  });
  query<HTMLButtonElement>(root, '#sdn-frontend-move')?.addEventListener('click', () => {
    void moveFrontendFile(root);
  });
  query<HTMLButtonElement>(root, '#sdn-frontend-delete')?.addEventListener('click', () => {
    void deleteFrontendFile(root);
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
      void selectFrontendFile(root, path);
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
      void uploadFrontendFileList(root, files);
    }
  });
}

function bindShell(root: HTMLElement): void {
  query<HTMLButtonElement>(root, '#sdn-mode-switch')?.addEventListener('click', () => {
    void toggleAdminMode(root);
  });
  query<HTMLButtonElement>(root, '#sdn-connect-server')?.addEventListener('click', () => {
    void promptAndConnectServer(root);
  });
  queryAll(root, '[data-nav]').forEach((item) => {
    if (!('addEventListener' in item)) {
      return;
    }
    item.addEventListener('click', () => {
      const target = item.getAttribute('data-nav');
      if (!target || target === 'ipfs-dashboard') {
        return;
      }
      void setWorkspace(root, target);
    });
  });
  queryAll(root, '[data-workspace-link]').forEach((item) => {
    if (!('addEventListener' in item)) {
      return;
    }
    item.addEventListener('click', (event) => {
      event.preventDefault();
      const target = item.getAttribute('data-workspace-link');
      if (!target) {
        return;
      }
      void setWorkspace(root, target);
    });
  });
  query<HTMLButtonElement>(root, '[data-feature-prev]')?.addEventListener('click', () => {
    shiftFeatureSlide(root, -1);
  });
  query<HTMLButtonElement>(root, '[data-feature-next]')?.addEventListener('click', () => {
    shiftFeatureSlide(root, 1);
  });
  queryAll(root, '[data-feature-target]').forEach((item) => {
    if (!('addEventListener' in item)) {
      return;
    }
    item.addEventListener('click', () => {
      const target = item.getAttribute('data-feature-target');
      target && setFeatureSlide(root, target);
    });
  });
  renderShellMeta(root, state.admin?.snapshot());
  initializeFeatureCarousel(root);
}

async function refreshProviderDescriptor(root: HTMLElement): Promise<void> {
  setConnectionStatus(root, 'Loading provider descriptor');

  const providerUrl = query<HTMLInputElement>(root, '#sdn-provider-url');
  const candidates = uniqueStrings([
    providerUrl?.value,
    inferProviderDescriptorUrl(root),
    `${window.location.origin}/api/module-delivery/provider`,
  ]);

  for (const candidate of candidates) {
    try {
      const response = await fetch(candidate);
      if (!response.ok) {
        continue;
      }
      const payload = await response.json() as ProviderDescriptor;
      if (payload.publicKey && payload.peerId && Array.isArray(payload.relayAddresses)) {
        providerUrl && (providerUrl.value = candidate);
        state.provider = payload;
        recordObservedPeer(payload.peerId, 'provider', payload.relayAddresses[0] ?? candidate);
        renderProviderDescriptor(root, payload);
        setConnectionStatus(root, 'Provider descriptor loaded');
        return;
      }
    } catch {
      // Fall through to the next candidate.
    }
  }

  state.provider = DEFAULT_PROVIDER_DESCRIPTOR;
  recordObservedPeer(
    DEFAULT_PROVIDER_DESCRIPTOR.peerId,
    'provider',
    DEFAULT_PROVIDER_DESCRIPTOR.relayAddresses[0],
  );
  renderProviderDescriptor(root, DEFAULT_PROVIDER_DESCRIPTOR);
  setConnectionStatus(root, 'Using seeded live provider descriptor');
}

async function refreshMarketplace(root: HTMLElement): Promise<void> {
  const marketplacePanel = query<HTMLElement>(root, '#sdn-marketplace-panel');
  if (!marketplacePanel) {
    return;
  }

  marketplacePanel.innerHTML = '<div class="sdn-empty">Refreshing live PLG listings…</div>';
  const baseUrl = inferCatalogBaseUrl(root);

  try {
    const listings = await loadMarketplaceListingsFromServer(baseUrl);
    state.marketplace = createMarketplaceIndex(listings);
    for (const listing of state.marketplace.values()) {
      if (listing.publisherPeerId) {
        recordObservedPeer(listing.publisherPeerId, 'identity', `${listing.publisherName ?? listing.pluginId}`);
      }
    }
    renderMarketplace(root);
    renderModuleMetadata(root);
  } catch (error) {
    marketplacePanel.innerHTML = `<div class="sdn-empty">${escapeHtml(formatError(error))}</div>`;
  }
}

async function ensureRuntime(root: HTMLElement): Promise<{
  node: RuntimeNodeLike;
  identity: RuntimeIdentityLike;
  provider: ProviderDescriptor;
}> {
  const runtime = await loadRuntimeModules();

  if (!state.provider) {
    await refreshProviderDescriptor(root);
  }
  if (!state.provider) {
    throw new Error('provider descriptor unavailable');
  }

  if (!state.identity) {
    const walletReady = await runtime.initHDWallet();
    if (!walletReady) {
      throw new Error('hd-wallet-wasm failed to initialize');
    }
    state.identity = await runtime.deriveIdentity(runtime.randomBytes(64));
  }

  if (!state.node) {
    state.node = await runtime.SDNNode.create(
      {
        edgeRelays: state.provider.relayAddresses,
        includeIPFSBootstrap: false,
        enableStorage: false,
        enableRelayProbing: false,
        identity: state.identity,
      },
      {
        onPeerConnected(peerId) {
          recordObservedPeer(peerId, 'protocol', 'libp2p connection');
          renderObservedPeers(root);
          setConnectionStatus(root, `Connected to ${peerId}`);
        },
        onPeerDisconnected(peerId) {
          recordObservedPeer(peerId, 'protocol', 'peer disconnected');
          renderObservedPeers(root);
        },
        onModuleDeliveryEvent(event) {
          handleDeliveryEvent(root, event);
        },
      },
    );

    try {
      await state.node.dial(state.provider.relayAddresses[0]);
      recordObservedPeer(state.provider.peerId, 'seed', state.provider.relayAddresses[0]);
      setConnectionStatus(root, `Seeded from ${state.provider.peerId}`);
    } catch (error) {
      setConnectionStatus(root, `Seed dial failed: ${formatError(error)}`);
    }

    try {
      const discovery = await runtime.discoverProvider(hexToBytes(state.provider.publicKey));
      const providers = await state.node.discoverProviders(discovery.discoveryCID);
      for (const provider of providers) {
        recordObservedPeer(provider.peerId, 'dht', provider.multiaddrs.join(', '));
      }
      renderObservedPeers(root);
    } catch {
      renderObservedPeers(root);
    }

    if (state.admin?.snapshot().mode === 'local') {
      applyAdminSnapshot(root, await state.admin.refresh());
    }
  }

  return {
    node: state.node,
    identity: state.identity,
    provider: state.provider,
  };
}

async function runLiveFlow(root: HTMLElement): Promise<void> {
  resetDelivery(root);

  try {
    const runtime = await loadRuntimeModules();
    const selectedListing = getSelectedListing(root);
    if (!selectedListing) {
      throw new Error('select a live PLG listing first');
    }

    const requesterDomain = query<HTMLInputElement>(root, '#sdn-requester-domain')?.value.trim() || 'app.example.com';
    const requestedTimeoutMs = Number(query<HTMLInputElement>(root, '#sdn-request-timeout')?.value || 300_000);
    const invokeMethod = query<HTMLInputElement>(root, '#sdn-invoke-method')?.value.trim() || 'echo';
    const invokePayload = query<HTMLTextAreaElement>(root, '#sdn-invoke-payload')?.value ?? '';

    const { node, identity, provider } = await ensureRuntime(root);
    renderModuleMetadata(root, {
      pluginId: selectedListing.pluginId,
      version: selectedListing.version,
      providerPeerId: provider.peerId,
      relayAddresses: provider.relayAddresses,
    });

    const grant = await node.requestModuleGrant({
      serverDescriptor: provider,
      moduleId: selectedListing.pluginId,
      moduleVersion: selectedListing.version,
      requesterDomain,
      requestedTimeoutMs,
    });
    const delivery = await runtime.fetchEncryptedModuleBundle(node, grant, {
      onEvent(event) {
        handleDeliveryEvent(root, event);
      },
    });
    const contentKey = await runtime.unwrapGrantContentKey(
      delivery.grant.wrappedContentKey,
      identity.encryptionKey.privateKey,
      {
        onEvent(event) {
          handleDeliveryEvent(root, event);
        },
      },
    );
    const decryptedBundle = await runtime.decryptEncryptedModuleBundle(
      delivery.encryptedBundleBytes,
      contentKey,
      {
        onEvent(event) {
          handleDeliveryEvent(root, event);
        },
      },
    );
    const parsedBundle = await parseFirstBrowserBundle([
      decryptedBundle,
      delivery.encryptedBundleBytes,
    ]);
    renderModuleMetadata(root, {
      pluginId: selectedListing.pluginId,
      version: selectedListing.version,
      providerPeerId: provider.peerId,
      cid: delivery.grant.bundleDescriptor.cid,
      manifest: parsedBundle.manifest,
      canonicalModuleHashHex: parsedBundle.canonicalModuleHashHex,
    });

    const harness = await runtime.loadDecryptedModule(decryptedBundle, {
      onEvent(event) {
        handleDeliveryEvent(root, event);
      },
    });
    const response = await runtime.invokeLoadedModule<{
      statusCode?: number;
      outputs?: Array<{ payload?: Uint8Array }>;
    }>(
      harness,
      {
        methodId: invokeMethod,
        inputs: [
          {
            portId: 'request',
            payload: new TextEncoder().encode(invokePayload),
          },
        ],
      },
      {
        onEvent(event) {
          handleDeliveryEvent(root, event);
        },
      },
    );

    const outputPayload = response.outputs?.[0]?.payload;
    const completionState = query<HTMLElement>(root, '#sdn-completion-state');
    if (completionState) {
      completionState.innerHTML = `
        <div class="sdn-stack">
          <div>Status code: ${escapeHtml(String(response.statusCode ?? 'unknown'))}</div>
          <div>Bundle CID: ${escapeHtml(delivery.grant.bundleDescriptor.cid)}</div>
          <div>Canonical hash: ${escapeHtml(parsedBundle.canonicalModuleHashHex)}</div>
          <div>Invoke output: ${escapeHtml(outputPayload ? new TextDecoder().decode(outputPayload) : '<none>')}</div>
        </div>
      `;
    }
  } catch (error) {
    const completionState = query<HTMLElement>(root, '#sdn-completion-state');
    if (completionState) {
      completionState.innerHTML = `<div class="sdn-empty">${escapeHtml(formatError(error))}</div>`;
    }
    const rawDetail = query<HTMLElement>(root, '#sdn-raw-event-detail');
    if (rawDetail) {
      rawDetail.textContent = JSON.stringify({ error: formatError(error) }, null, 2);
    }
  }
}

async function runAddressLookup(root: HTMLElement): Promise<void> {
  const chain = query<HTMLSelectElement>(root, '#sdn-address-lookup-chain')?.value ?? 'bitcoin';
  const value = query<HTMLInputElement>(root, '#sdn-address-lookup-value')?.value ?? '';
  const rawDetail = query<HTMLElement>(root, '#sdn-raw-event-detail');

  try {
    const runtime = await loadRuntimeModules();
    const { node } = await ensureRuntime(root);
    const lookupKey = await runtime.normalizeAddressLookupKey(chain, value);
    const providers = await node.discoverProviders(lookupKey.discoveryCID);
    for (const provider of providers) {
      recordObservedPeer(provider.peerId, 'identity', `${chain}:${lookupKey.normalizedValue}`);
    }
    renderObservedPeers(root);
    if (rawDetail) {
      rawDetail.textContent = JSON.stringify({
        lookup: lookupKey,
        providers,
      }, null, 2);
    }
  } catch (error) {
    if (rawDetail) {
      rawDetail.textContent = JSON.stringify({ error: formatError(error) }, null, 2);
    }
  }
}

function handleDeliveryEvent(root: HTMLElement, event: ModuleDeliveryEvent): void {
  state.deliveryEvents.push(event);
  if (event.providerPeerId) {
    recordObservedPeer(
      event.providerPeerId,
      event.stage === 'provider-discovery' ? 'provider' : 'protocol',
      event.detail ?? event.cid,
    );
  }
  renderObservedPeers(root);
  renderTimeline(root);
  const rawDetail = query<HTMLElement>(root, '#sdn-raw-event-detail');
  rawDetail && (rawDetail.textContent = JSON.stringify(event, null, 2));
}

function renderProviderDescriptor(root: HTMLElement, provider: ProviderDescriptor): void {
  const node = query<HTMLElement>(root, '#sdn-provider-descriptor');
  if (!node) {
    return;
  }
  const payload: Record<string, unknown> = {
    publicKey: provider.publicKey,
    peerId: provider.peerId,
    ipns: provider.ipns,
    relayAddresses: provider.relayAddresses,
  };
  if (provider.identity) {
    payload.identity = {
      xpub: provider.identity.xpub,
      identityPublicKey: provider.identity.identityPublicKey,
      signingPublicKey: provider.identity.signingPublicKey,
      encryptionPublicKey: provider.identity.encryptionPublicKey,
      ipnsEntries: provider.identity.ipnsEntries,
      ensNames: provider.identity.ensNames,
      addresses: provider.identity.addresses,
    };
  }
  node.textContent = JSON.stringify(payload, null, 2);
  renderObservedPeers(root);
}

function renderObservedPeers(root: HTMLElement): void {
  const count = query<HTMLElement>(root, '#sdn-observed-peer-count');
  const sightings = query<HTMLElement>(root, '#sdn-sightings');
  count && (count.textContent = String(state.observedPeers.count()));
  if (sightings) {
    const items = state.observedPeers.list().slice(0, 6);
    sightings.innerHTML = items.length === 0
      ? 'DHT, provider, and protocol evidence will stream here.'
      : items.map((item) => `
          <div class="sdn-sighting">
            <strong>${escapeHtml(item.peerId)}</strong>
            <span>${escapeHtml(item.sources.join(', '))}</span>
            <span>${escapeHtml(item.detail ?? '')}</span>
          </div>
        `).join('');
  }
}

function renderMarketplace(root: HTMLElement): void {
  const select = query<HTMLSelectElement>(root, '#sdn-marketplace-select');
  const panel = query<HTMLElement>(root, '#sdn-marketplace-panel');
  if (!select || !panel) {
    return;
  }

  const listings = state.marketplace.values();
  if (listings.length === 0) {
    select.innerHTML = '<option value="">No live PLG listings loaded</option>';
    panel.innerHTML = '<div class="sdn-empty">Publisher and module listings will populate from live PLG manifests.</div>';
    return;
  }

  const currentValue = select.value || canonicalListingKey(listings[0]);
  select.innerHTML = listings.map((listing) => `
    <option value="${escapeHtml(canonicalListingKey(listing))}">
      ${escapeHtml(listing.name ?? listing.pluginId)} (${escapeHtml(listing.version)})
    </option>
  `).join('');
  select.value = listings.some((listing) => canonicalListingKey(listing) === currentValue)
    ? currentValue
    : canonicalListingKey(listings[0]);

  const selectedListing = getSelectedListing(root);
  panel.innerHTML = selectedListing ? `
    <div class="sdn-stack">
      <strong>${escapeHtml(selectedListing.name ?? selectedListing.pluginId)}</strong>
      <span>${escapeHtml(selectedListing.tagline ?? selectedListing.description ?? 'Canonical PLG listing')}</span>
      <span>Publisher: ${escapeHtml(selectedListing.publisherName ?? selectedListing.publisherHandle ?? 'Unknown')}</span>
      <span>Status: ${escapeHtml(selectedListing.status ?? 'public')}</span>
      <span>Tags: ${escapeHtml((selectedListing.tags ?? []).join(', ') || '<none>')}</span>
    </div>
  ` : '<div class="sdn-empty">No live PLG listing selected.</div>';
}

function renderModuleMetadata(root: HTMLElement, override?: Record<string, unknown>): void {
  const moduleMetadata = query<HTMLElement>(root, '#sdn-module-metadata');
  if (!moduleMetadata) {
    return;
  }
  const selectedListing = getSelectedListing(root);
  const payload = {
    pluginId: selectedListing?.pluginId,
    version: selectedListing?.version,
    publisher: selectedListing?.publisherName ?? selectedListing?.publisherHandle,
    ...override,
  };
  moduleMetadata.innerHTML = `<pre class="sdn-code">${escapeHtml(JSON.stringify(payload, null, 2))}</pre>`;
}

function renderTimeline(root: HTMLElement): void {
  const timeline = query<HTMLElement>(root, '#sdn-delivery-timeline');
  if (!timeline) {
    return;
  }
  if (state.deliveryEvents.length === 0) {
    timeline.innerHTML = '<div class="sdn-empty">Challenge, grant, fetch, decrypt, load, and invoke events appear in order.</div>';
    return;
  }
  timeline.innerHTML = `
    <ol class="sdn-timeline">
      ${state.deliveryEvents.map((event) => `
        <li>
          <strong>${escapeHtml(event.stage)}</strong>
          <span>${escapeHtml(event.detail ?? event.cid ?? '')}</span>
        </li>
      `).join('')}
    </ol>
  `;
}

function resetDelivery(root: HTMLElement): void {
  state.deliveryEvents = [];
  renderTimeline(root);
  const completionState = query<HTMLElement>(root, '#sdn-completion-state');
  completionState && (completionState.innerHTML = '<div class="sdn-empty">Running live module-delivery flow…</div>');
}

function getSelectedListing(root: HTMLElement): CanonicalListing | undefined {
  const select = query<HTMLSelectElement>(root, '#sdn-marketplace-select');
  if (!select || !select.value) {
    return state.marketplace.values()[0];
  }
  const [pluginId, version] = select.value.split('@');
  return state.marketplace.get(pluginId, version);
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

function setConnectionStatus(root: HTMLElement, status: string): void {
  const node = query<HTMLElement>(root, '#sdn-connection-status');
  node && (node.textContent = status);
  const topbar = query<HTMLElement>(root, '#sdn-connection-status-top');
  topbar && (topbar.textContent = status);
}

function recordObservedPeer(peerId: string, source: ObservedPeerSource, detail?: string): void {
  state.observedPeers.record({ peerId, source, detail });
}

function query<TElement extends HTMLElement>(root: ParentNode, selector: string): TElement | null {
  return root.querySelector(selector) as TElement | null;
}

function queryAll(root: ParentNode, selector: string): Element[] {
  return Array.from(root.querySelectorAll(selector));
}

function setActiveWorkspace(root: HTMLElement, workspaceId: string): void {
  queryAll(root, '[data-nav]').forEach((item) => {
    item.classList.toggle('sdn-admin-nav__item--active', item.getAttribute('data-nav') === workspaceId);
  });
  queryAll(root, '[data-workspace]').forEach((panel) => {
    panel.classList.toggle('sdn-admin-workspace--active', panel.getAttribute('data-workspace') === workspaceId);
  });
}

function initializeFeatureCarousel(root: HTMLElement): void {
  const first = query<HTMLElement>(root, '[data-feature-slide]');
  if (!first) {
    return;
  }
  const firstId = first.getAttribute('data-feature-slide');
  firstId && setFeatureSlide(root, firstId);
}

function shiftFeatureSlide(root: HTMLElement, delta: number): void {
  const slides = queryAll(root, '[data-feature-slide]');
  if (slides.length === 0) {
    return;
  }
  const currentIndex = slides.findIndex((slide) => slide.classList.contains('sdn-feature-slide--active'));
  const nextIndex = currentIndex === -1
    ? 0
    : (currentIndex + delta + slides.length) % slides.length;
  const nextId = slides[nextIndex]?.getAttribute('data-feature-slide');
  nextId && setFeatureSlide(root, nextId);
}

function setFeatureSlide(root: HTMLElement, featureId: string): void {
  queryAll(root, '[data-feature-slide]').forEach((slide) => {
    const active = slide.getAttribute('data-feature-slide') === featureId;
    slide.classList.toggle('sdn-feature-slide--active', active);
    slide.setAttribute('aria-hidden', active ? 'false' : 'true');
  });
  queryAll(root, '[data-feature-target]').forEach((button) => {
    const active = button.getAttribute('data-feature-target') === featureId;
    button.classList.toggle('sdn-feature-carousel__dot--active', active);
    button.setAttribute('aria-selected', active ? 'true' : 'false');
  });
}

function renderShellMeta(root: HTMLElement, snapshot = state.admin?.snapshot()): void {
  const activeTarget = query<HTMLElement>(root, '#sdn-active-target');
  const modeSwitch = query<HTMLElement>(root, '#sdn-mode-switch');
  if (!snapshot) {
    activeTarget && (activeTarget.textContent = 'Local backend');
    modeSwitch && (modeSwitch.textContent = 'Local');
    return;
  }
  activeTarget && (activeTarget.textContent = snapshot.mode === 'local'
    ? 'Local backend'
    : (snapshot.serverTarget?.label
      ? `Server · ${snapshot.serverTarget.label}`
      : (snapshot.serverTarget?.baseUrl ? `Server · ${snapshot.serverTarget.baseUrl}` : 'Server backend')));
  modeSwitch && (modeSwitch.textContent = snapshot.mode === 'local' ? 'Local' : 'Server');
}

async function toggleAdminMode(root: HTMLElement): Promise<void> {
  const admin = requireAdminState();
  const snapshot = admin.snapshot();
  if (snapshot.mode === 'local') {
    try {
      applyAdminSnapshot(root, await admin.setMode('server'));
      await refreshProviderDescriptor(root);
      return;
    } catch {
      await promptAndConnectServer(root);
      return;
    }
  }

  applyAdminSnapshot(root, await admin.setMode('local'));
  await refreshProviderDescriptor(root);
}

async function promptAndConnectServer(root: HTMLElement): Promise<void> {
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
  await refreshProviderDescriptor(root);
}

async function setWorkspace(root: HTMLElement, workspaceId: string): Promise<void> {
  const admin = requireAdminState();
  applyAdminSnapshot(root, await admin.setWorkspace(workspaceId));
}

function applyAdminSnapshot(root: HTMLElement, snapshot: AdminSnapshot): void {
  state.accountMenu?.setAdminSnapshot(snapshot);
  renderShellMeta(root, snapshot);
  renderAccountDialog(root);
  setActiveWorkspace(root, snapshot.workspace.activeId);
  void refreshDirectoryPanel(root);
  void refreshFrontendWorkspace(root);
}

function inferProviderDescriptorUrl(root: HTMLElement): string {
  const serverBaseUrl = state.admin?.snapshot().serverTarget?.baseUrl;
  if (serverBaseUrl) {
    return `${serverBaseUrl}/api/module-delivery/provider`;
  }
  const providerUrl = query<HTMLInputElement>(root, '#sdn-provider-url')?.value.trim();
  if (providerUrl) {
    return providerUrl;
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

async function refreshDirectoryPanel(root: HTMLElement): Promise<void> {
  const panel = query<HTMLElement>(root, '#sdn-directory-panel');
  const admin = state.admin;
  if (!panel || !admin) {
    return;
  }

  const snapshot = admin.snapshot();
  const summary = `
    <div class="sdn-stack">
      <strong>${escapeHtml(snapshot.nodeContext.displayName)}</strong>
      <span>Mode: ${escapeHtml(snapshot.mode)}</span>
      <span>Role: ${escapeHtml(snapshot.permissions.role)}</span>
      <span>Transport: ${escapeHtml(snapshot.nodeContext.transport)}</span>
      <span>Peer ID: ${escapeHtml(snapshot.nodeContext.peerId ?? '<unknown>')}</span>
      <span>Server: ${escapeHtml(snapshot.serverTarget?.baseUrl ?? '<browser-local>')}</span>
    </div>
  `;

  if (snapshot.mode === 'local') {
    panel.innerHTML = `
      ${summary}
      <div class="sdn-empty">
        Local mode uses the browser-owned Helia backend. Switch to Server to inspect the live node directory and user roster.
      </div>
    `;
    return;
  }

  if (!snapshot.permissions.authenticated || !snapshot.serverTarget?.baseUrl) {
    panel.innerHTML = `
      ${summary}
      <div class="sdn-empty">
        Sign in on the selected server to inspect node membership and permissions.
      </div>
    `;
    return;
  }

  if (snapshot.permissions.role !== 'admin') {
    panel.innerHTML = `
      ${summary}
      <div class="sdn-empty">
        Connected as ${escapeHtml(snapshot.permissions.role)}. Admin-only user management data is hidden for this session.
      </div>
    `;
    return;
  }

  panel.innerHTML = `
    ${summary}
    <div class="sdn-empty">Loading live server users…</div>
  `;

  try {
    const response = await fetch(`${snapshot.serverTarget.baseUrl}/api/auth/users`, {
      credentials: 'include',
    });
    if (!response.ok) {
      throw new Error(`user query failed (${response.status})`);
    }
    const users = await response.json() as DirectoryUserLike[];
    panel.innerHTML = `
      ${summary}
      <div class="sdn-stack">
        <strong>Server users (${users.length})</strong>
        ${users.map((user) => `
          <div class="sdn-sighting">
            <strong>${escapeHtml(user.name ?? user.xpub)}</strong>
            <span>${escapeHtml(user.trust_level ?? 'unknown')} · ${escapeHtml(user.source ?? 'server')}</span>
            <span>${escapeHtml(user.xpub)}</span>
          </div>
        `).join('')}
      </div>
    `;
  } catch (error) {
    panel.innerHTML = `
      ${summary}
      <div class="sdn-empty">${escapeHtml(formatError(error))}</div>
    `;
  }
}

async function openAccountDialog(root: HTMLElement): Promise<void> {
  const accountMenu = state.accountMenu;
  if (!accountMenu) {
    return;
  }
  accountMenu.setAdminSnapshot(requireAdminState().snapshot());
  await accountMenu.open();
  renderAccountDialog(root);
}

function closeAccountDialog(root: HTMLElement): void {
  state.accountMenu?.close();
  renderAccountDialog(root);
}

async function openAccountWallet(root: HTMLElement): Promise<void> {
  const accountMenu = state.accountMenu;
  if (!accountMenu) {
    return;
  }
  await accountMenu.openWalletAccount();
  renderAccountDialog(root);
}

async function signOutAccount(root: HTMLElement): Promise<void> {
  const accountMenu = state.accountMenu;
  const admin = state.admin;
  if (!accountMenu || !admin) {
    return;
  }
  await accountMenu.signOut();
  renderAccountDialog(root);
  const snapshot = admin.snapshot();
  if (snapshot.mode === 'server') {
    applyAdminSnapshot(root, await admin.refresh());
    await refreshProviderDescriptor(root);
  }
}

function renderAccountDialog(root: HTMLElement): void {
  const dialog = query<HTMLElement>(root, '#sdn-account-dialog');
  const meta = query<HTMLElement>(root, '#sdn-account-meta');
  const signOutButton = query<HTMLButtonElement>(root, '#sdn-account-signout');
  const accountMenu = state.accountMenu;
  if (!dialog || !meta || !accountMenu) {
    return;
  }

  const snapshot = accountMenu.snapshot();
  dialog.hidden = !snapshot.isOpen;
  dialog.setAttribute('aria-hidden', String(!snapshot.isOpen));
  meta.innerHTML = `
    <div class="sdn-stack">
      <strong>${escapeHtml(snapshot.title)}</strong>
      <span>Mode: ${escapeHtml(snapshot.mode)}</span>
      <span>Role: ${escapeHtml(snapshot.role)}</span>
      <span>${escapeHtml(snapshot.subtitle)}</span>
    </div>
  `;
  if (signOutButton) {
    signOutButton.hidden = !snapshot.canSignOut;
    signOutButton.disabled = !snapshot.canSignOut;
  }
}

async function refreshFrontendWorkspace(root: HTMLElement): Promise<void> {
  const admin = state.admin;
  const statusNode = query<HTMLElement>(root, '#sdn-frontend-status');
  if (!admin || !statusNode) {
    return;
  }

  const adminSnapshot = admin.snapshot();
  if (
    adminSnapshot.mode === 'server'
    && (
      !adminSnapshot.serverTarget?.baseUrl
      || !adminSnapshot.permissions.authenticated
      || adminSnapshot.permissions.role !== 'admin'
    )
  ) {
    state.frontendWorkspace = null;
    state.frontendWorkspaceKey = null;
    renderFrontendPlaceholder(
      root,
      'Connect as an admin on the selected server to manage the public frontend.',
    );
    return;
  }

  const workspaceKey = adminSnapshot.mode === 'server'
    ? `server:${adminSnapshot.serverTarget?.baseUrl ?? ''}`
    : 'local';

  if (!state.frontendWorkspace || state.frontendWorkspaceKey !== workspaceKey) {
    state.frontendWorkspace = createFrontendWorkspace({
      mode: adminSnapshot.mode,
      transport: adminSnapshot.mode === 'server' && adminSnapshot.serverTarget?.baseUrl
        ? createServerFrontendTransport({ baseUrl: adminSnapshot.serverTarget.baseUrl })
        : state.localFrontendTransport,
    });
    state.frontendWorkspaceKey = workspaceKey;
    await state.frontendWorkspace.connect();
  }

  await ensureFrontendEditor(root);
  renderFrontendWorkspace(root, state.frontendWorkspace.snapshot());
}

async function ensureFrontendEditor(root: HTMLElement): Promise<void> {
  if (state.frontendEditor) {
    return;
  }
  const host = query<HTMLElement>(root, '#sdn-frontend-editor');
  if (!host) {
    return;
  }
  state.frontendEditor = await createBrowserEditorController(host, (value) => {
    if (!state.frontendWorkspace) {
      return;
    }
    const snapshot = state.frontendWorkspace.editContent(value);
    renderFrontendStatus(root, snapshot);
  });
}

function renderFrontendWorkspace(root: HTMLElement, snapshot: ReturnType<FrontendWorkspace['snapshot']>): void {
  const pathInput = query<HTMLInputElement>(root, '#sdn-frontend-path');
  const tree = query<HTMLElement>(root, '#sdn-frontend-tree');
  const editor = state.frontendEditor;
  if (pathInput) {
    pathInput.value = snapshot.selectedPath ?? '';
  }
  renderFrontendStatus(root, snapshot);
  if (tree) {
    tree.innerHTML = snapshot.tree.length === 0
      ? '<div class="sdn-empty">No frontend files available.</div>'
      : `<ul class="sdn-frontend-tree__list">${renderFrontendTreeNodes(snapshot.tree, snapshot.selectedPath)}</ul>`;
  }
  editor?.setDocument(snapshot.editor.value, snapshot.editor.language);
}

function renderFrontendStatus(root: HTMLElement, snapshot: ReturnType<FrontendWorkspace['snapshot']>): void {
  const statusNode = query<HTMLElement>(root, '#sdn-frontend-status');
  const saveButton = query<HTMLButtonElement>(root, '#sdn-frontend-save');
  const deleteButton = query<HTMLButtonElement>(root, '#sdn-frontend-delete');
  const moveButton = query<HTMLButtonElement>(root, '#sdn-frontend-move');
  if (statusNode) {
    statusNode.textContent = snapshot.editor.dirty
      ? `${snapshot.status} · unsaved`
      : snapshot.status;
  }
  if (saveButton) {
    saveButton.disabled = !snapshot.selectedPath || !snapshot.editor.dirty;
  }
  if (deleteButton) {
    deleteButton.disabled = !snapshot.selectedPath;
  }
  if (moveButton) {
    moveButton.disabled = !snapshot.selectedPath;
  }
}

function renderFrontendPlaceholder(root: HTMLElement, message: string): void {
  const tree = query<HTMLElement>(root, '#sdn-frontend-tree');
  const status = query<HTMLElement>(root, '#sdn-frontend-status');
  const pathInput = query<HTMLInputElement>(root, '#sdn-frontend-path');
  if (tree) {
    tree.innerHTML = `<div class="sdn-empty">${escapeHtml(message)}</div>`;
  }
  if (status) {
    status.textContent = message;
  }
  if (pathInput) {
    pathInput.value = '';
  }
  state.frontendEditor?.setDocument('', 'plaintext');
}

function renderFrontendTreeNodes(
  nodes: Array<{ name: string; path: string; isDir: boolean; children?: Array<{ name: string; path: string; isDir: boolean; children?: unknown[] }> }>,
  selectedPath: string | null,
): string {
  return nodes.map((node) => `
    <li class="sdn-frontend-tree__node">
      <button
        type="button"
        class="sdn-frontend-tree__item${node.path === selectedPath ? ' sdn-frontend-tree__item--active' : ''}"
        data-frontend-path="${escapeHtml(node.path)}"
      >
        <span class="sdn-frontend-tree__badge">${node.isDir ? 'DIR' : 'FILE'}</span>
        <span>${escapeHtml(node.name)}</span>
      </button>
      ${node.children?.length
        ? `<ul class="sdn-frontend-tree__list">${renderFrontendTreeNodes(node.children as never, selectedPath)}</ul>`
        : ''}
    </li>
  `).join('');
}

async function selectFrontendFile(root: HTMLElement, path: string): Promise<void> {
  if (!state.frontendWorkspace) {
    return;
  }
  renderFrontendWorkspace(root, await state.frontendWorkspace.selectPath(path));
  state.frontendEditor?.focus();
}

async function saveFrontendFile(root: HTMLElement): Promise<void> {
  if (!state.frontendWorkspace) {
    return;
  }
  renderFrontendWorkspace(root, await state.frontendWorkspace.save());
}

async function moveFrontendFile(root: HTMLElement): Promise<void> {
  if (!state.frontendWorkspace) {
    return;
  }
  const targetPath = query<HTMLInputElement>(root, '#sdn-frontend-path')?.value.trim() ?? '';
  if (!targetPath) {
    return;
  }
  renderFrontendWorkspace(root, await state.frontendWorkspace.moveSelection(targetPath));
}

async function deleteFrontendFile(root: HTMLElement): Promise<void> {
  if (!state.frontendWorkspace) {
    return;
  }
  renderFrontendWorkspace(root, await state.frontendWorkspace.deleteSelection());
}

async function uploadFrontendFiles(root: HTMLElement, input: HTMLInputElement | null): Promise<void> {
  const files = [...(input?.files ?? [])];
  if (files.length === 0) {
    return;
  }
  await uploadFrontendFileList(root, files);
  if (input) {
    input.value = '';
  }
}

async function uploadFrontendFileList(root: HTMLElement, files: File[]): Promise<void> {
  if (!state.frontendWorkspace) {
    return;
  }
  const uploads = await Promise.all(files.map(async (file) => ({
    name: file.name,
    text: await file.text(),
  } satisfies FrontendUploadFile)));
  const directory = frontendSelectedDirectory(state.frontendWorkspace.snapshot().selectedPath);
  renderFrontendWorkspace(root, await state.frontendWorkspace.upload(uploads, directory));
}

function frontendSelectedDirectory(selectedPath: string | null): string {
  if (!selectedPath || !selectedPath.includes('/')) {
    return '';
  }
  return selectedPath.split('/').slice(0, -1).join('/');
}

function requireAdminState(): AdminState {
  if (!state.admin) {
    throw new Error('admin state not initialized');
  }
  return state.admin;
}

function uniqueStrings(values: Array<string | null | undefined>): string[] {
  return [...new Set(values.map((value) => String(value ?? '').trim()).filter(Boolean))];
}

function hexToBytes(value: string): Uint8Array {
  const normalized = value.trim();
  const bytes = new Uint8Array(normalized.length / 2);
  for (let index = 0; index < bytes.length; index += 1) {
    bytes[index] = Number.parseInt(normalized.slice(index * 2, index * 2 + 2), 16);
  }
  return bytes;
}

function escapeHtml(value: string): string {
  return value
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#39;');
}

function formatError(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

bootstrap().catch((error) => {
  const root = document.querySelector('#app');
  if (root instanceof HTMLElement) {
    root.innerHTML = `<pre class="sdn-error">${escapeHtml(String(error))}</pre>`;
  }
  console.error('[sdn-ui] bootstrap failed', error);
});

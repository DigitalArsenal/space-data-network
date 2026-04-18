import { createMarketplaceIndex, canonicalListingKey } from '../../src/ui/runtime/marketplace';
import { decodeCanonicalPlgListing } from '../../src/ui/runtime/plg-listings';
import { ObservedPeerIndex } from '../../src/ui/runtime/observed-peers';
import type { CanonicalListing, ObservedPeerSource } from '../../src/ui/runtime/types';
import { mountWalletUI } from '../../src/ui/runtime/wallet-ui';
import { parseFirstBrowserBundle } from './browser-bundle';
import { renderAppShell } from './app';
import './styles.css';

interface ProviderDescriptor {
  publicKey: string;
  peerId: string;
  relayAddresses: string[];
  ipns?: string;
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
  mode: 'local' as 'local' | 'server',
  serverBaseUrl: '' as string,
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

  bindUI(root);
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
}

function bindShell(root: HTMLElement): void {
  query<HTMLButtonElement>(root, '#sdn-mode-switch')?.addEventListener('click', () => {
    state.mode = state.mode === 'local' ? 'server' : 'local';
    renderShellMeta(root);
  });
  query<HTMLButtonElement>(root, '#sdn-connect-server')?.addEventListener('click', () => {
    const candidate = window.prompt(
      'Enter the admin base URL for the remote SDN node:',
      state.serverBaseUrl || 'https://sdn.spaceaware.io',
    );
    if (!candidate) {
      return;
    }
    state.serverBaseUrl = candidate.replace(/\/+$/, '');
    state.mode = 'server';
    const providerUrl = query<HTMLInputElement>(root, '#sdn-provider-url');
    if (providerUrl) {
      providerUrl.value = `${state.serverBaseUrl}/api/module-delivery/provider`;
    }
    renderShellMeta(root);
    void refreshProviderDescriptor(root);
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
      setActiveWorkspace(root, target);
    });
  });
  renderShellMeta(root);
}

async function refreshProviderDescriptor(root: HTMLElement): Promise<void> {
  setConnectionStatus(root, 'Loading provider descriptor');

  const providerUrl = query<HTMLInputElement>(root, '#sdn-provider-url');
  const candidates = uniqueStrings([
    providerUrl?.value,
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
    const response = await fetch(
      `${baseUrl}/api/v1/data/query/PLG?include_data=true&format=json&limit=25`,
    );
    if (!response.ok) {
      throw new Error(`listing query failed (${response.status})`);
    }

    const payload = await response.json() as {
      results?: Array<{ data_base64?: string; timestamp?: string }>;
    };
    const listings = (payload.results ?? [])
      .map((entry) => entry.data_base64 ? decodeCanonicalPlgListing(base64ToBytes(entry.data_base64), {
        observedAt: entry.timestamp ? Date.parse(entry.timestamp) : Date.now(),
      }) : null)
      .filter((listing): listing is CanonicalListing => Boolean(listing));

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
  node.textContent = JSON.stringify({
    publicKey: provider.publicKey,
    peerId: provider.peerId,
    relayAddresses: provider.relayAddresses,
  }, null, 2);
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

function renderShellMeta(root: HTMLElement): void {
  const activeTarget = query<HTMLElement>(root, '#sdn-active-target');
  const modeSwitch = query<HTMLElement>(root, '#sdn-mode-switch');
  activeTarget && (activeTarget.textContent = state.mode === 'local'
    ? 'Local backend'
    : (state.serverBaseUrl ? `Server · ${state.serverBaseUrl}` : 'Server backend'));
  modeSwitch && (modeSwitch.textContent = state.mode === 'local' ? 'Local' : 'Server');
}

function uniqueStrings(values: Array<string | null | undefined>): string[] {
  return [...new Set(values.map((value) => String(value ?? '').trim()).filter(Boolean))];
}

function base64ToBytes(value: string): Uint8Array {
  const binary = atob(value);
  const bytes = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index += 1) {
    bytes[index] = binary.charCodeAt(index);
  }
  return bytes;
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

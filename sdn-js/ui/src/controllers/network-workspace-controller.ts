import type { CanonicalListing, ObservedPeerSource } from '../../../src/ui/runtime/types';
import { query } from '../dom/query';
import { escapeHtml, hexToBytes, uniqueStrings } from '../dom/escape';
import type { AppState } from '../state/app-state';
import type {
  ModuleDeliveryEventLike,
  ProviderDescriptor,
  RuntimeIdentityLike,
  RuntimeModules,
  RuntimeNodeLike,
} from '../state/types';
import {
  buildObservedPeersMarkup,
  buildProviderDescriptorText,
  buildTimelineMarkup,
} from '../views/network-view';

interface NetworkWorkspaceControllerOptions {
  defaultProviderDescriptor: ProviderDescriptor;
  getProviderDescriptorCandidates: () => Array<string | null | undefined>;
  getSelectedPluginListing: () => CanonicalListing | undefined;
  loadRuntimeModules: () => Promise<RuntimeModules>;
  onRefreshLocalAdmin?: () => Promise<void>;
  parseFirstBrowserBundle: (
    candidates: Uint8Array[],
  ) => Promise<{ canonicalModuleHashHex: string }>;
  root: HTMLElement;
  state: AppState;
}

export function createNetworkWorkspaceController(options: NetworkWorkspaceControllerOptions) {
  const {
    defaultProviderDescriptor,
    getProviderDescriptorCandidates,
    getSelectedPluginListing,
    loadRuntimeModules,
    onRefreshLocalAdmin,
    parseFirstBrowserBundle,
    root,
    state,
  } = options;

  function recordObservedPeer(peerId: string, source: ObservedPeerSource, detail?: string): void {
    state.observedPeers.record({ peerId, source, detail });
  }

  function renderObservedPeers(): void {
    const count = query<HTMLElement>(root, '#sdn-observed-peer-count');
    const sightings = query<HTMLElement>(root, '#sdn-sightings');
    if (count) {
      count.textContent = String(state.observedPeers.count());
    }
    if (sightings) {
      sightings.innerHTML = buildObservedPeersMarkup(state.observedPeers.list());
    }
  }

  function renderProviderDescriptor(provider: ProviderDescriptor): void {
    const node = query<HTMLElement>(root, '#sdn-provider-descriptor');
    if (!node) {
      return;
    }
    node.textContent = buildProviderDescriptorText(provider);
    renderObservedPeers();
  }

  function renderTimeline(): void {
    const timeline = query<HTMLElement>(root, '#sdn-delivery-timeline');
    if (!timeline) {
      return;
    }
    timeline.innerHTML = buildTimelineMarkup(state.deliveryEvents);
  }

  function handleDeliveryEvent(event: ModuleDeliveryEventLike): void {
    state.deliveryEvents.push(event);
    if (event.providerPeerId) {
      recordObservedPeer(
        event.providerPeerId,
        event.stage === 'provider-discovery' ? 'provider' : 'protocol',
        event.detail ?? event.cid,
      );
    }
    renderObservedPeers();
    renderTimeline();
    const rawDetail = query<HTMLElement>(root, '#sdn-raw-event-detail');
    if (rawDetail) {
      rawDetail.textContent = JSON.stringify(event, null, 2);
    }
  }

  function resetDelivery(): void {
    state.deliveryEvents = [];
    renderTimeline();
    const completionState = query<HTMLElement>(root, '#sdn-completion-state');
    if (completionState) {
      completionState.innerHTML = '<div class="sdn-empty">Running live module-delivery flow…</div>';
    }
  }

  async function refreshProviderDescriptor(): Promise<void> {
    const providerUrl = query<HTMLInputElement>(root, '#sdn-provider-url');
    const candidates = uniqueStrings(getProviderDescriptorCandidates());

    for (const candidate of candidates) {
      try {
        const response = await fetch(candidate);
        if (!response.ok) {
          continue;
        }
        const payload = await response.json() as ProviderDescriptor;
        if (payload.publicKey && payload.peerId && Array.isArray(payload.relayAddresses)) {
          if (providerUrl) {
            providerUrl.value = candidate;
          }
          state.provider = payload;
          recordObservedPeer(payload.peerId, 'provider', payload.relayAddresses[0] ?? candidate);
          renderProviderDescriptor(payload);
          return;
        }
      } catch {
        // Fall through to the next candidate.
      }
    }

    state.provider = defaultProviderDescriptor;
    recordObservedPeer(
      defaultProviderDescriptor.peerId,
      'provider',
      defaultProviderDescriptor.relayAddresses[0],
    );
    renderProviderDescriptor(defaultProviderDescriptor);
  }

  async function ensureRuntime(): Promise<{
    node: RuntimeNodeLike;
    identity: RuntimeIdentityLike;
    provider: ProviderDescriptor;
  }> {
    const runtime = await loadRuntimeModules();

    if (!state.provider) {
      await refreshProviderDescriptor();
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
            renderObservedPeers();
          },
          onPeerDisconnected(peerId) {
            recordObservedPeer(peerId, 'protocol', 'peer disconnected');
            renderObservedPeers();
          },
          onModuleDeliveryEvent(event) {
            handleDeliveryEvent(event);
          },
        },
      );

      try {
        await state.node.dial(state.provider.relayAddresses[0]);
        recordObservedPeer(state.provider.peerId, 'seed', state.provider.relayAddresses[0]);
      } catch (_error) {
        // Discovery should still proceed from the seeded descriptor even if the first relay dial fails.
      }

      try {
        const discovery = await runtime.discoverProvider(hexToBytes(state.provider.publicKey));
        const providers = await state.node.discoverProviders(discovery.discoveryCID);
        for (const provider of providers) {
          recordObservedPeer(provider.peerId, 'dht', provider.multiaddrs.join(', '));
        }
        renderObservedPeers();
      } catch {
        renderObservedPeers();
      }

      if (state.admin?.snapshot().mode === 'local') {
        await onRefreshLocalAdmin?.();
      }
    }

    return {
      node: state.node,
      identity: state.identity,
      provider: state.provider,
    };
  }

  async function runLiveFlow(): Promise<void> {
    resetDelivery();

    try {
      const runtime = await loadRuntimeModules();
      const selectedListing = getSelectedPluginListing();
      if (!selectedListing) {
        throw new Error('select a live plugin listing first');
      }

      const requesterDomain = query<HTMLInputElement>(root, '#sdn-requester-domain')?.value.trim() || 'app.example.com';
      const requestedTimeoutMs = Number(query<HTMLInputElement>(root, '#sdn-request-timeout')?.value || 300_000);
      const invokeMethod = query<HTMLInputElement>(root, '#sdn-invoke-method')?.value.trim() || 'echo';
      const invokePayload = query<HTMLTextAreaElement>(root, '#sdn-invoke-payload')?.value ?? '';

      const { node, identity, provider } = await ensureRuntime();
      const grant = await node.requestModuleGrant({
        serverDescriptor: provider,
        moduleId: selectedListing.pluginId,
        moduleVersion: selectedListing.version,
        requesterDomain,
        requestedTimeoutMs,
      });
      const delivery = await runtime.fetchEncryptedModuleBundle(node, grant, {
        onEvent(event) {
          handleDeliveryEvent(event);
        },
      });
      const contentKey = await runtime.unwrapGrantContentKey(
        delivery.grant.wrappedContentKey,
        identity.encryptionKey.privateKey,
        {
          onEvent(event) {
            handleDeliveryEvent(event);
          },
        },
      );
      const decryptedBundle = await runtime.decryptEncryptedModuleBundle(
        delivery.encryptedBundleBytes,
        contentKey,
        {
          onEvent(event) {
            handleDeliveryEvent(event);
          },
        },
      );
      const parsedBundle = await parseFirstBrowserBundle([
        decryptedBundle,
        delivery.encryptedBundleBytes,
      ]);

      const harness = await runtime.loadDecryptedModule(decryptedBundle, {
        onEvent(event) {
          handleDeliveryEvent(event);
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
            handleDeliveryEvent(event);
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

  async function runAddressLookup(): Promise<void> {
    const chain = query<HTMLSelectElement>(root, '#sdn-address-lookup-chain')?.value ?? 'bitcoin';
    const value = query<HTMLInputElement>(root, '#sdn-address-lookup-value')?.value ?? '';
    const rawDetail = query<HTMLElement>(root, '#sdn-raw-event-detail');

    try {
      const runtime = await loadRuntimeModules();
      const { node } = await ensureRuntime();
      const lookupKey = await runtime.normalizeAddressLookupKey(chain, value);
      const providers = await node.discoverProviders(lookupKey.discoveryCID);
      for (const provider of providers) {
        recordObservedPeer(provider.peerId, 'identity', `${chain}:${lookupKey.normalizedValue}`);
      }
      renderObservedPeers();
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

  return {
    ensureRuntime,
    handleDeliveryEvent,
    recordObservedPeer,
    refreshProviderDescriptor,
    renderObservedPeers,
    renderProviderDescriptor,
    renderTimeline,
    resetDelivery,
    runAddressLookup,
    runLiveFlow,
  };
}

import { createMarketplaceIndex } from '../../../src/ui/runtime/marketplace';
import { ObservedPeerIndex } from '../../../src/ui/runtime/observed-peers';
import {
  createLocalFrontendTransport,
  type FrontendWorkspace,
} from '../../../src/ui/runtime/frontend-workspace';
import type { AdminState } from '../../../src/ui/runtime/admin-state';
import type { WalletModalController } from '../../../src/ui/runtime/wallet-modal';
import type { FrontendEditorController } from '../frontend-editor';
import type {
  ModuleDeliveryEventLike,
  ProviderDescriptor,
  RuntimeIdentityLike,
  RuntimeNodeLike,
  StoreSelection,
} from './types';

export function createAppState() {
  return {
    provider: null as ProviderDescriptor | null,
    node: null as RuntimeNodeLike | null,
    identity: null as RuntimeIdentityLike | null,
    admin: null as AdminState | null,
    walletModal: null as WalletModalController | null,
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
    storeSelection: null as StoreSelection | null,
    snapshot() {
      return {
        provider: this.provider,
        node: this.node,
        identity: this.identity,
        admin: this.admin,
        walletModal: this.walletModal,
        frontendWorkspace: this.frontendWorkspace,
        frontendWorkspaceKey: this.frontendWorkspaceKey,
        frontendEditor: this.frontendEditor,
        localFrontendTransport: this.localFrontendTransport,
        marketplace: this.marketplace,
        observedPeers: this.observedPeers,
        deliveryEvents: [...this.deliveryEvents],
        storeSelection: this.storeSelection,
      };
    },
    setProvider(provider: ProviderDescriptor | null) {
      this.provider = provider;
    },
    pushDeliveryEvent(event: ModuleDeliveryEventLike) {
      this.deliveryEvents.push(event);
    },
    resetDeliveryEvents() {
      this.deliveryEvents = [];
    },
    setStoreSelection(selection: StoreSelection | null) {
      this.storeSelection = selection;
    },
  };
}

export type AppState = ReturnType<typeof createAppState>;

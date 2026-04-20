import { createMarketplaceIndex } from '../../../src/ui/runtime/marketplace';
import { loadMarketplaceListingsFromServer } from '../../../src/ui/runtime/marketplace-source';
import {
  buildStoreFeed,
  searchStoreListings,
} from '../../../src/ui/runtime/store-search';
import type { CanonicalListing, ObservedPeerSource } from '../../../src/ui/runtime/types';
import { query } from '../dom/query';
import { escapeHtml, formatError } from '../dom/escape';
import type { AppState } from '../state/app-state';
import {
  getSelectedPluginListing,
  renderStoreDetail,
  renderStoreFeed,
  renderStoreSpotlight,
  resolveStoreSelection,
} from '../views/store-view';

interface StoreWorkspaceControllerOptions {
  getCatalogBaseUrl: () => string;
  onObservedPeer?: (peerId: string, source: ObservedPeerSource, detail?: string) => void;
  root: HTMLElement;
  state: AppState;
}

export function createStoreWorkspaceController(options: StoreWorkspaceControllerOptions) {
  const { getCatalogBaseUrl, onObservedPeer, root, state } = options;

  async function refreshMarketplace(): Promise<void> {
    const spotlightPanel = query<HTMLElement>(root, '#sdn-store-spotlight');
    const feedPanel = query<HTMLElement>(root, '#sdn-store-feed');
    const detailPanel = query<HTMLElement>(root, '#sdn-store-detail');
    if (!spotlightPanel || !feedPanel || !detailPanel) {
      return;
    }

    spotlightPanel.innerHTML = '';
    feedPanel.innerHTML = '<div class="sdn-empty">Refreshing live PLG listings…</div>';
    detailPanel.innerHTML = '<div class="sdn-empty">Refreshing live store detail…</div>';

    try {
      const listings = await loadMarketplaceListingsFromServer(getCatalogBaseUrl());
      state.marketplace = createMarketplaceIndex(listings);
      for (const listing of state.marketplace.values()) {
        if (listing.publisherPeerId) {
          onObservedPeer?.(
            listing.publisherPeerId,
            'identity',
            `${listing.publisherName ?? listing.pluginId}`,
          );
        }
      }
      renderMarketplace();
    } catch (error) {
      spotlightPanel.innerHTML = '';
      feedPanel.innerHTML = `<div class="sdn-empty">${escapeHtml(formatError(error))}</div>`;
      detailPanel.innerHTML = `<div class="sdn-empty">${escapeHtml(formatError(error))}</div>`;
    }
  }

  function renderMarketplace(): void {
    const spotlightPanel = query<HTMLElement>(root, '#sdn-store-spotlight');
    const feedPanel = query<HTMLElement>(root, '#sdn-store-feed');
    const detailPanel = query<HTMLElement>(root, '#sdn-store-detail');
    if (!spotlightPanel || !feedPanel || !detailPanel) {
      return;
    }

    const listings = state.marketplace.values();
    if (listings.length === 0) {
      spotlightPanel.innerHTML = '';
      feedPanel.innerHTML = '';
      detailPanel.innerHTML = '';
      state.storeSelection = null;
      return;
    }

    const searchQuery = query<HTMLInputElement>(root, '#sdn-store-search')?.value ?? '';
    const results = searchStoreListings(listings, searchQuery);
    const feed = buildStoreFeed(results, searchQuery);
    const selection = resolveMarketplaceSelection(results);

    spotlightPanel.innerHTML = renderStoreSpotlight(results, selection, searchQuery);
    feedPanel.innerHTML = renderStoreFeed(feed.entries, selection, searchQuery);
    detailPanel.innerHTML = renderStoreDetail(results, selection);
  }

  function resolveMarketplaceSelection(
    results: ReturnType<typeof searchStoreListings>,
  ) {
    const selection = resolveStoreSelection(results, state.storeSelection);
    state.storeSelection = selection;
    return selection;
  }

  function currentSelectedPluginListing(): CanonicalListing | undefined {
    const searchQuery = query<HTMLInputElement>(root, '#sdn-store-search')?.value ?? '';
    const results = searchStoreListings(state.marketplace.values(), searchQuery);
    const selection = resolveMarketplaceSelection(results);
    return getSelectedPluginListing(results, selection)?.listing;
  }

  return {
    getSelectedPluginListing: currentSelectedPluginListing,
    refreshMarketplace,
    renderMarketplace,
  };
}

import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { describe, expect, it } from 'vitest';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const repoRoot = path.resolve(__dirname, '../../..');
const marketplacePagePath = path.join(
  repoRoot,
  'ui/src/upstream-webui/overrides/marketplace/MarketplacePage.js',
);
const routesPath = path.join(
  repoRoot,
  'ui/src/upstream-webui/overrides/bundles/routes.js',
);
const navPath = path.join(
  repoRoot,
  'ui/src/upstream-webui/overrides/navigation/NavBar.js',
);

describe('marketplace page layout', () => {
  it('wires a first-class marketplace route and nav item into the SDN webui', async () => {
    const [routesSource, navSource] = await Promise.all([
      readFile(routesPath, 'utf8'),
      readFile(navPath, 'utf8'),
    ]);

    expect(routesSource).toContain("import MarketplacePage from '../marketplace/MarketplacePage.js'");
    expect(routesSource).toContain("'/marketplace': MarketplacePage");
    expect(navSource).toContain("<NavLink to='/marketplace'");
    expect(navSource).toContain('Marketplace');
  });

  it('loads marketplace listings and provides browse/search/filter controls', async () => {
    const source = await readFile(marketplacePagePath, 'utf8');

    expect(source).toContain('loadMarketplaceListingsFromServer');
    expect(source).toContain('searchStoreListings');
    expect(source).toContain("aria-label='Search marketplace'");
    expect(source).toContain("ariaLabel='Filter by schema'");
    expect(source).toContain("ariaLabel='Filter by provider'");
    expect(source).toContain("ariaLabel='Filter by payment'");
    expect(source).toContain("ariaLabel='Filter by publication status'");
    expect(source).toContain('aria-label={ariaLabel}');
    expect(source).toContain('filteredListings');
  });

  it('renders module listings, STF data listings, and standards-linked discovery cards', async () => {
    const source = await readFile(marketplacePagePath, 'utf8');

    expect(source).toContain('PluginListingCard');
    expect(source).toContain('DataListingCard');
    expect(source).toContain('DataStandardCard');
    expect(source).toContain("listing.listingKind === 'data'");
    expect(source).toContain("listing.listingKind !== 'data'");
    expect(source).toContain('priceUsdCents');
    expect(source).toContain('acceptedPaymentMethods');
    expect(source).toContain('requiredScope');
    expect(source).toContain('standardsUsed');
    expect(source).toContain('protectedDelivery');
    expect(source).toContain('Encrypted CID');
    expect(source).toContain('licensing/core');
  });

  it('renders listing detail views with provider, terms, schema, verification, and CID evidence', async () => {
    const source = await readFile(marketplacePagePath, 'utf8');

    expect(source).toContain('ListingDetailView');
    expect(source).toContain('selectedListingKey');
    expect(source).toContain('View details');
    expect(source).toContain('Provider identity');
    expect(source).toContain('Provider peer ID');
    expect(source).toContain('Terms');
    expect(source).toContain('Supported schemas');
    expect(source).toContain('Verification state');
    expect(source).toContain('Sample CID');
    expect(source).toContain('Manifest CID');
    expect(source).toContain('verificationState');
  });

  it('provides a purchase-to-grant panel using the Go storefront JSON contract', async () => {
    const source = await readFile(marketplacePagePath, 'utf8');

    expect(source).toContain('PurchaseAccessPanel');
    expect(source).toContain('Purchase access');
    expect(source).toContain("`${runtimeBaseUrl()}/api/storefront/purchases`");
    expect(source).toContain('listing_id: listing.pluginId');
    expect(source).toContain('tier_name: tierName.trim()');
    expect(source).toContain('buyer_peer_id: buyerPeerId.trim()');
    expect(source).toContain('buyer_encryption_pubkey: encryptionPubkey.trim()');
    expect(source).toContain("key_algorithm: 'x25519'");
    expect(source).toContain('payment_method: paymentMethodValue(paymentMethod)');
    expect(source).toContain('preferred_delivery_method: preferredDeliveryMethod');
    expect(source).toContain('/api/storefront/purchases/${encodeURIComponent(requestId)}/pay-credits');
    expect(source).toContain('Delivery topic');
    expect(source).toContain('Grant issued');
    expect(source).toContain('Verify encrypted delivery');
    expect(source).toContain('__SDN_MARKETPLACE_CLIENT_DECRYPT__');
    expect(source).toContain('Decrypt/load complete');
  });
});

import { expect, test } from '@playwright/test';

const fixtureListing = {
  listing_id: 'protected-od-browser-e2e',
  listing_kind: 'wasm_module',
  provider_peer_id: 'provider-peer-e2e',
  title: 'Protected OD Browser Fixture',
  description: 'Paid encrypted module fixture for storefront browser verification.',
  data_types: ['OMM', 'CDM'],
  tags: ['browser-e2e', 'protected'],
  version: '2026.05.05',
  active: true,
  sample_cid: 'bafy-sample-browser-e2e',
  access_type: 1,
  encryption_required: true,
  pricing: [
    {
      name: 'Basic',
      price_amount: 500,
      price_currency: 'SDN_CREDITS',
      duration_days: 30,
    },
  ],
  accepted_payments: [3],
  protected_delivery: {
    encrypted_cid: 'bafy-encrypted-browser-e2e',
    manifest_cid: 'bafy-manifest-browser-e2e',
    content_hash: 'sha256-browser-e2e',
    content_key_id: 'content-key-browser-e2e',
    license_module_id: 'licensing/core',
    module_id: 'protected-od-browser-e2e',
    module_version: '2026.05.05',
    grant_scope: 'module:invoke:protected-od-browser-e2e',
    delivery_protocol: '/space-data-network/module-delivery/1.0.0',
  },
};

test('purchases a protected storefront listing and exercises browser decrypt/load harness', async ({ page }) => {
  const requests: Array<{ url: string; body: unknown }> = [];

  await page.addInitScript(() => {
    window.__SDN_CONFIG__ = {
      peerId: 'buyer-peer-e2e',
      encryptionPublicKey: 'AQIDBA==',
      serverBaseUrl: window.location.origin,
    };
    window.__SDN_MARKETPLACE_CLIENT_DECRYPT__ = {
      async fetchEncryptedBundle({ cid }) {
        window.__SDN_MARKETPLACE_E2E_FETCHED_CID__ = cid;
        return new TextEncoder().encode('encrypted module fixture bytes');
      },
      async decryptArtifact({ grant, encryptedBundleBytes }) {
        window.__SDN_MARKETPLACE_E2E_DECRYPTED__ = {
          grantId: grant.grant_id,
          encryptedBytes: encryptedBundleBytes.byteLength,
        };
        return new TextEncoder().encode('decrypted wasm fixture');
      },
      async loadModule({ bytes, listing }) {
        window.__SDN_MARKETPLACE_E2E_LOADED__ = {
          listingId: listing.pluginId,
          bytes: bytes.byteLength,
        };
        return { operation: 'fixture-load' };
      },
    };
  });

  await page.route('**/api/**', async (route) => {
    await route.fulfill({ status: 404, contentType: 'application/json', body: '{}' });
  });
  await page.route('**/api/module-delivery/listings', async (route) => {
    await route.fulfill({ status: 404, contentType: 'application/json', body: '{}' });
  });
  await page.route('**/api/v1/data/query/STF**', async (route) => {
    await route.fulfill({ status: 404, contentType: 'application/json', body: '{}' });
  });
  await page.route('**/api/storefront/listings', async (route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ listings: [fixtureListing] }),
    });
  });
  await page.route('**/api/storefront/purchases', async (route) => {
    const request = route.request();
    requests.push({ url: request.url(), body: JSON.parse(request.postData() ?? '{}') });
    await route.fulfill({
      status: 201,
      contentType: 'application/json',
      body: JSON.stringify({
        request_id: 'purchase-browser-e2e',
        listing_id: fixtureListing.listing_id,
        tier_name: 'Basic',
        buyer_peer_id: 'buyer-peer-e2e',
        buyer_encryption_pubkey: 'AQIDBA==',
        key_algorithm: 'x25519',
        payment_method: 3,
        payment_amount: 500,
        payment_currency: 'SDN_CREDITS',
        status: 0,
        preferred_delivery_method: 'IPFSPin',
      }),
    });
  });
  await page.route('**/api/storefront/purchases/purchase-browser-e2e/pay-credits', async (route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        grant_id: 'grant-browser-e2e',
        listing_id: fixtureListing.listing_id,
        tier_name: 'Basic',
        buyer_peer_id: 'buyer-peer-e2e',
        access_type: 1,
        status: 0,
        payment_method: 3,
        payment_amount: 500,
        payment_currency: 'SDN_CREDITS',
        delivery_topic: '/sdn/data/protected-od-browser-e2e/buyer-peer-e2e',
        provider_peer_id: 'provider-peer-e2e',
        provider_signature: 'CQkJ',
        grant_response_base64: 'AQIDBA==',
      }),
    });
  });
  await page.route('**/api/storefront/purchases/purchase-browser-e2e/manual-dev-paid', async (route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        mode: 'manual-dev',
        purchase: {
          request_id: 'purchase-browser-e2e',
          listing_id: fixtureListing.listing_id,
          tier_name: 'Basic',
          buyer_peer_id: 'buyer-peer-e2e',
          status: 3,
          grant_id: 'grant-browser-e2e',
        },
        grant: {
          grant_id: 'grant-browser-e2e',
          listing_id: fixtureListing.listing_id,
          tier_name: 'Basic',
          buyer_peer_id: 'buyer-peer-e2e',
          access_type: 1,
          status: 0,
          payment_method: 4,
          payment_amount: 500,
          payment_currency: 'SDN_CREDITS',
          delivery_topic: '/sdn/data/protected-od-browser-e2e/buyer-peer-e2e',
          provider_peer_id: 'provider-peer-e2e',
          provider_signature: 'CQkJ',
          grant_response_base64: 'AQIDBA==',
        },
      }),
    });
  });
  await page.goto('/storefront-marketplace-e2e.html');
  await expect(page.getByRole('heading', { name: 'Marketplace' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Protected OD Browser Fixture' }).first()).toBeVisible();

  await page.getByRole('button', { name: 'View details' }).click();
  await expect(page.getByText('bafy-encrypted-browser-e2e').first()).toBeVisible();

  await page.getByRole('button', { name: 'Create purchase' }).click();
  await expect(page.getByText('purchase-browser-e2e')).toBeVisible();
  await page.getByRole('button', { name: 'Mark manual/dev paid' }).click();
  await expect(page.getByText('grant-browser-e2e')).toBeVisible();
  await expect(page.getByText('/sdn/data/protected-od-browser-e2e/buyer-peer-e2e')).toBeVisible();

  await page.getByRole('button', { name: 'Verify encrypted delivery' }).click();
  await expect(page.getByText('Decrypt/load complete')).toBeVisible();
  await expect(page.getByText('Loaded fixture-load')).toBeVisible();

  expect(requests[0]?.body).toMatchObject({
    listing_id: fixtureListing.listing_id,
    tier_name: 'Basic',
    buyer_peer_id: 'buyer-peer-e2e',
    buyer_encryption_pubkey: 'AQIDBA==',
    key_algorithm: 'x25519',
    payment_method: 3,
    preferred_delivery_method: 'IPFSPin',
  });
  await expect.poll(async () => page.evaluate(() => window.__SDN_MARKETPLACE_E2E_FETCHED_CID__)).toBe(
    'bafy-encrypted-browser-e2e',
  );
  await expect.poll(async () => page.evaluate(() => window.__SDN_MARKETPLACE_E2E_LOADED__)).toEqual({
    listingId: fixtureListing.listing_id,
    bytes: 22,
  });
});

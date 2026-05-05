import * as flatbuffers from 'flatbuffers';
import { describe, expect, it, vi } from 'vitest';
import { PLG } from 'spacedatastandards.org/lib/js/REC/PLG.js';
import { publicationState as listingStatus } from 'spacedatastandards.org/lib/js/PLG/publicationState.js';
import { pluginCategory as pluginType } from 'spacedatastandards.org/lib/js/PLG/pluginCategory.js';
import { STF } from 'spacedatastandards.org/lib/js/REC/STF.js';
import { accessCategory } from 'spacedatastandards.org/lib/js/STF/accessCategory.js';
import { paymentMethod } from 'spacedatastandards.org/lib/js/STF/paymentMethod.js';
import { PricingTier } from 'spacedatastandards.org/lib/js/STF/PricingTier.js';

import { loadMarketplaceListingsFromServer } from './marketplace-source';

describe('loadMarketplaceListingsFromServer', () => {
  it('prefers module-delivery PLG listings when that API is available', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce({
        ok: true,
        status: 200,
        async json() {
          return {
            results: [
              {
                data_base64: Buffer.from(createPlgBytes({
                  pluginId: 'com.space-data-network.orbital-demo',
                  version: '1.2.3',
                  name: 'Orbital Demo',
                  description: 'Canonical PLG record',
                })).toString('base64'),
                timestamp: '2026-04-18T15:00:00Z',
              },
            ],
          };
        },
      })
      .mockResolvedValueOnce({
        ok: false,
        status: 404,
        async json() {
          return {};
        },
      });

    await expect(
      loadMarketplaceListingsFromServer('https://sdn.spaceaware.io', fetchMock),
    ).resolves.toEqual([
      expect.objectContaining({
        pluginId: 'com.space-data-network.orbital-demo',
        version: '1.2.3',
        name: 'Orbital Demo',
        description: 'Canonical PLG record',
        observedAt: Date.parse('2026-04-18T15:00:00Z'),
      }),
    ]);

    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it('loads canonical STF data listings alongside PLG module listings', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce({
        ok: true,
        status: 200,
        async json() {
          return {
            results: [
              {
                data_base64: Buffer.from(createPlgBytes({
                  pluginId: 'com.space-data-network.conjunction',
                  version: '3.0.0',
                  name: 'Conjunction Assessment',
                  description: 'Screens OMM data',
                })).toString('base64'),
                timestamp: '2026-04-18T15:00:00Z',
              },
            ],
          };
        },
      })
      .mockResolvedValueOnce({
        ok: true,
        status: 200,
        async json() {
          return {
            results: [
              {
                data_base64: Buffer.from(createStfBytes()).toString('base64'),
                timestamp: '2026-04-18T16:00:00Z',
              },
            ],
          };
        },
      })
      .mockResolvedValueOnce({
        ok: false,
        status: 404,
        async json() {
          return {};
        },
      });

    await expect(
      loadMarketplaceListingsFromServer('https://sdn.spaceaware.io', fetchMock),
    ).resolves.toEqual([
      expect.objectContaining({
        listingKind: 'module',
        pluginId: 'com.space-data-network.conjunction',
        standardsUsed: ['OMM'],
      }),
      expect.objectContaining({
        listingKind: 'data',
        pluginId: 'data.celestrak.omm-full-catalog',
        name: 'CelesTrak OMM Full Catalog',
        publisherPeerId: '16Uiu2HAmCelestrakProvider',
        standardsUsed: ['OMM'],
        paymentModel: 'subscription',
        priceUsdCents: 2500,
        acceptedPaymentMethods: ['Fiat_Stripe', 'Free'],
        requiredScope: 'data:data.celestrak.omm-full-catalog:query',
        status: 'public',
      }),
    ]);

    expect(fetchMock).toHaveBeenCalledWith(
      'https://sdn.spaceaware.io/api/v1/data/query/STF?include_data=true&format=json&limit=25',
      { credentials: 'include' },
    );
  });

  it('falls back to storefront listings when the module-delivery route is unavailable', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce({
        ok: false,
        status: 404,
        async json() {
          return {};
        },
      })
      .mockResolvedValueOnce({
        ok: true,
        status: 200,
        async json() {
          return {
            listings: [
              {
                listing_id: 'com.space-data-network.orbital-demo',
                provider_peer_id: '16Uiu2HAmDemo',
                title: 'Orbital Demo',
                description: 'Storefront record',
                tags: ['orbit', 'OMM', 'demo'],
                version: 7,
                active: true,
                updated_at: '2026-04-18T15:00:00Z',
              },
            ],
          };
        },
      })
      .mockResolvedValueOnce({
        ok: false,
        status: 404,
        async json() {
          return {};
        },
      });

    await expect(
      loadMarketplaceListingsFromServer('https://sdn.spaceaware.io', fetchMock),
    ).resolves.toEqual([
      {
        listingKind: 'data',
        pluginId: 'com.space-data-network.orbital-demo',
        version: '7',
        name: 'Orbital Demo',
        description: 'Storefront record',
        publisherPeerId: '16Uiu2HAmDemo',
        observedAt: Date.parse('2026-04-18T15:00:00Z'),
        status: 'public',
        tags: ['orbit', 'OMM', 'demo'],
        standardsUsed: ['OMM'],
      },
    ]);

    expect(fetchMock).toHaveBeenCalledTimes(3);
  });

  it('returns an empty marketplace from successful empty module-delivery and STF responses without probing legacy fallback routes', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce({
        ok: true,
        status: 200,
        async json() {
          return {
            results: null,
            count: 0,
          };
        },
      })
      .mockResolvedValueOnce({
        ok: false,
        status: 404,
        async json() {
          return {};
        },
      });

    await expect(
      loadMarketplaceListingsFromServer('https://sdn.spaceaware.io', fetchMock),
    ).resolves.toEqual([]);

    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it('keeps the empty storefront state quiet when the storefront route succeeds with no listings', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce({
        ok: false,
        status: 404,
        async json() {
          return {};
        },
      })
      .mockResolvedValueOnce({
        ok: true,
        status: 200,
        async json() {
          return {
            listings: [],
            total: 0,
          };
        },
      })
      .mockResolvedValueOnce({
        ok: false,
        status: 404,
        async json() {
          return {};
        },
      });

    await expect(
      loadMarketplaceListingsFromServer('https://sdn.spaceaware.io', fetchMock),
    ).resolves.toEqual([]);

    expect(fetchMock).toHaveBeenCalledTimes(3);
  });

  it('ignores malformed storefront listing payloads instead of crashing or probing PLG', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce({
        ok: false,
        status: 404,
        async json() {
          return {};
        },
      })
      .mockResolvedValueOnce({
        ok: true,
        status: 200,
        async json() {
          return {
            listings: {
              unexpected: true,
            },
            total: 1,
          };
        },
      })
      .mockResolvedValueOnce({
        ok: false,
        status: 404,
        async json() {
          return {};
        },
      });

    await expect(
      loadMarketplaceListingsFromServer('https://sdn.spaceaware.io', fetchMock),
    ).resolves.toEqual([]);

    expect(fetchMock).toHaveBeenCalledTimes(3);
  });

  it('ignores malformed PLG query payloads instead of crashing', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce({
        ok: false,
        status: 404,
        async json() {
          return {};
        },
      })
      .mockResolvedValueOnce({
        ok: true,
        status: 200,
        async json() {
          return {
            results: {
              unexpected: true,
            },
          };
        },
      })
      .mockResolvedValueOnce({
        ok: false,
        status: 404,
        async json() {
          return {};
        },
      });

    await expect(
      loadMarketplaceListingsFromServer('https://sdn.spaceaware.io', fetchMock),
    ).resolves.toEqual([]);

    expect(fetchMock).toHaveBeenCalledTimes(3);
  });
});

function createPlgBytes(options: {
  pluginId: string;
  version: string;
  name: string;
  description?: string;
}): Uint8Array {
  const builder = new flatbuffers.Builder(256);
  const pluginIdOffset = builder.createString(options.pluginId);
  const nameOffset = builder.createString(options.name);
  const versionOffset = builder.createString(options.version);
  const descriptionOffset = options.description ? builder.createString(options.description) : 0;

  const root = PLG.createPLG(
    builder,
    pluginIdOffset,
    nameOffset,
    versionOffset,
    descriptionOffset,
    0,
    pluginType.Analysis,
    0,
    0,
    0,
    0,
    0,
    0,
    0,
    0,
    1,
    0,
    0n,
    0,
    0,
    0n,
    0,
    0,
    0,
    0,
    0,
    0,
    true,
    0,
    0,
    0,
    0n,
    0,
    0n,
    0n,
    0,
    0,
    0,
    0,
    0,
    0,
    0,
    0,
    listingStatus.Public,
    0,
  );

  PLG.finishPLGBuffer(builder, root);
  return builder.asUint8Array();
}

function createStfBytes(): Uint8Array {
  const builder = new flatbuffers.Builder(256);
  const listingIdOffset = builder.createString('data.celestrak.omm-full-catalog');
  const providerPeerIdOffset = builder.createString('16Uiu2HAmCelestrakProvider');
  const titleOffset = builder.createString('CelesTrak OMM Full Catalog');
  const descriptionOffset = builder.createString('Full-catalog OMM data');
  const dataTypesOffset = STF.createDataTypesVector(builder, [
    builder.createString('OMM'),
  ]);
  const priceTierOffset = PricingTier.createPricingTier(
    builder,
    builder.createString('Monthly'),
    2500n,
    builder.createString('USD'),
    30,
    1000,
    0,
  );
  const pricingOffset = STF.createPricingVector(builder, [priceTierOffset]);
  const acceptedPaymentsOffset = STF.createAcceptedPaymentsVector(builder, [
    paymentMethod.Fiat_Stripe,
    paymentMethod.Free,
  ]);

  STF.startSTF(builder);
  STF.addListingId(builder, listingIdOffset);
  STF.addProviderPeerId(builder, providerPeerIdOffset);
  STF.addTitle(builder, titleOffset);
  STF.addDescription(builder, descriptionOffset);
  STF.addDataTypes(builder, dataTypesOffset);
  STF.addAccessType(builder, accessCategory.Subscription);
  STF.addEncryptionRequired(builder, true);
  STF.addPricing(builder, pricingOffset);
  STF.addAcceptedPayments(builder, acceptedPaymentsOffset);
  STF.addCreatedAt(builder, 1_776_523_200n);
  STF.addUpdatedAt(builder, 1_776_526_800n);
  STF.addActive(builder, true);
  const root = STF.endSTF(builder);

  STF.finishSTFBuffer(builder, root);
  return builder.asUint8Array();
}

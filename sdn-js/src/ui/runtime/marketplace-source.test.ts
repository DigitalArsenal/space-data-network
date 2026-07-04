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
      .mockResolvedValueOnce(flatbufferStreamListingResponse([createStfBytes()]));

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

    // The STF listing fallback is ONE datasync stream request — the retired
    // base64 `data/query/STF?include_data=true&format=json` envelope is gone.
    expect(fetchMock).toHaveBeenCalledWith(
      'https://sdn.spaceaware.io/api/v1/data/query',
      expect.objectContaining({
        method: 'POST',
        credentials: 'include',
        headers: expect.objectContaining({ accept: 'application/vnd.sdn.flatbuffers.stream' }),
        body: JSON.stringify({ schema: 'STF.fbs', limit: 25 }),
      }),
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

  it('serves the PLG listing fallback from ONE FlatBuffer stream request (no base64)', async () => {
    const fetchMock = vi.fn()
      // module-delivery unavailable
      .mockResolvedValueOnce({ ok: false, status: 404, async json() { return {}; } })
      // storefront unavailable
      .mockResolvedValueOnce({ ok: false, status: 404, async json() { return {}; } })
      // PLG datasync stream
      .mockResolvedValueOnce(flatbufferStreamListingResponse([createPlgBytes({
        pluginId: 'com.space-data-network.stream-demo',
        version: '2.0.0',
        name: 'Stream Demo',
        description: 'Served as raw FlatBuffer frames',
      })]))
      // STF datasync stream: empty
      .mockResolvedValueOnce(flatbufferStreamListingResponse([]));

    await expect(
      loadMarketplaceListingsFromServer('https://sdn.spaceaware.io', fetchMock),
    ).resolves.toEqual([
      expect.objectContaining({
        pluginId: 'com.space-data-network.stream-demo',
        version: '2.0.0',
        name: 'Stream Demo',
        description: 'Served as raw FlatBuffer frames',
      }),
    ]);

    expect(fetchMock).toHaveBeenCalledTimes(4);
    expect(fetchMock).toHaveBeenNthCalledWith(
      3,
      'https://sdn.spaceaware.io/api/v1/data/query',
      expect.objectContaining({
        method: 'POST',
        headers: expect.objectContaining({ accept: 'application/vnd.sdn.flatbuffers.stream' }),
        body: JSON.stringify({ schema: 'PLG.fbs', limit: 25 }),
      }),
    );
  });

  it('decodes protected WASM storefront listings into module marketplace entries', async () => {
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
                listing_id: 'protected-wasm-od',
                listing_kind: 'wasm_module',
                provider_peer_id: '16Uiu2HProvider',
                title: 'Protected OD Module',
                description: 'Encrypted WASM OD workflow',
                data_types: ['WASM', 'OMM'],
                tags: ['wasm', 'orbit-determination'],
                sample_cid: 'bafybeisample',
                access_type: 1,
                encryption_required: true,
                pricing: [
                  {
                    name: 'Basic',
                    price_amount: 9900,
                    price_currency: 'USD',
                    duration_days: 30,
                  },
                ],
                accepted_payments: [4],
                protected_delivery: {
                  encrypted_cid: 'bafybeiencryptedwasm',
                  manifest_cid: 'bafybeimanifest',
                  content_hash: 'sha256:artifact',
                  content_key_id: 'ck-1',
                  license_module_id: 'licensing/core',
                  module_id: 'com.space-data-network.protected-od',
                  module_version: '1.2.3',
                  required_scopes: ['module:invoke'],
                  grant_scope: 'module:invoke:protected-od',
                  delivery_protocol: '/space-data-network/module-delivery/1.0.0',
                },
                active: true,
                updated_at: '2026-05-05T12:00:00Z',
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
        pluginId: 'protected-wasm-od',
        name: 'Protected OD Module',
        paymentModel: 'subscription',
        priceUsdCents: 9900,
        subscriptionPeriodDays: 30,
        acceptedPaymentMethods: ['Fiat_Stripe'],
        requiredScope: 'module:invoke:protected-od',
        standardsUsed: ['OMM', 'WASM'],
        sampleCid: 'bafybeisample',
        accessType: 'subscription',
        encryptionRequired: true,
        protectedDelivery: expect.objectContaining({
          encryptedCid: 'bafybeiencryptedwasm',
          licenseModuleId: 'licensing/core',
          moduleId: 'com.space-data-network.protected-od',
        }),
      }),
    ]);
  });

  it('decodes protected field-stream policy metadata for data stream listings', async () => {
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
                listing_id: 'protected-maneuver-ephemeris',
                listing_kind: 'data_stream',
                provider_peer_id: '16Uiu2HProvider',
                title: 'Protected Maneuver Ephemeris',
                description: 'Field-level protected maneuver stream',
                data_types: ['MPE'],
                access_type: 2,
                encryption_required: true,
                protected_delivery: {
                  delivery_protocol: '/space-data-network/field-stream/1.0.0',
                  grant_scope: 'stream:protected-maneuver-ephemeris:maneuver-ephemeris-live',
                  field_stream_policy: {
                    policy_id: 'policy-mpe-alpha',
                    policy_version: 3,
                    stream_id: 'maneuver-ephemeris-live',
                    schema_code: 'MPE',
                    allowed_field_paths: ['object_id', 'timestamp', 'position'],
                    redacted_field_paths: ['maneuver_plan'],
                    key_epoch: 'epoch-7',
                    grant_scope: 'stream:protected-maneuver-ephemeris:maneuver-ephemeris-live',
                    allowed_operations: ['Subscribe', 'Decrypt'],
                  },
                },
                active: true,
                updated_at: '2026-06-25T12:00:00Z',
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
        listingKind: 'data',
        pluginId: 'protected-maneuver-ephemeris',
        accessType: 'streaming',
        encryptionRequired: true,
        protectedDelivery: expect.objectContaining({
          deliveryProtocol: '/space-data-network/field-stream/1.0.0',
          grantScope: 'stream:protected-maneuver-ephemeris:maneuver-ephemeris-live',
          fieldStreamPolicy: {
            policyId: 'policy-mpe-alpha',
            policyVersion: 3,
            streamId: 'maneuver-ephemeris-live',
            schemaCode: 'MPE',
            allowedFieldPaths: ['object_id', 'timestamp', 'position'],
            redactedFieldPaths: ['maneuver_plan'],
            keyEpoch: 'epoch-7',
            grantScope: 'stream:protected-maneuver-ephemeris:maneuver-ephemeris-live',
            allowedOperations: ['Subscribe', 'Decrypt'],
          },
        }),
      }),
    ]);
  });

  it('falls back to storefront listings when module-delivery is available but empty', async () => {
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
        ok: true,
        status: 200,
        async json() {
          return {
            listings: [
              {
                listing_id: 'protected-live-daemon-e2e',
                listing_kind: 'wasm_module',
                provider_peer_id: '16Uiu2HAmLive',
                title: 'Protected Live Daemon Fixture',
                data_types: ['WASM', 'OMM'],
                active: true,
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
        pluginId: 'protected-live-daemon-e2e',
        name: 'Protected Live Daemon Fixture',
        publisherPeerId: '16Uiu2HAmLive',
        standardsUsed: ['OMM', 'WASM'],
      }),
    ]);

    expect(fetchMock).toHaveBeenCalledTimes(3);
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

  // Imperative builder API — positional createPLG args go stale every time
  // the PLG schema gains/loses fields (1.134 ALLOWED_XPUBS, 1.135 domain
  // removal, ...); only set the fields this fixture needs.
  PLG.startPLG(builder);
  PLG.addPluginId(builder, pluginIdOffset);
  PLG.addName(builder, nameOffset);
  PLG.addVersion(builder, versionOffset);
  if (descriptionOffset) {
    PLG.addDescription(builder, descriptionOffset);
  }
  PLG.addPluginType(builder, pluginType.Analysis);
  PLG.addAbiVersion(builder, 1);
  PLG.addEncrypted(builder, true);
  PLG.addListingStatus(builder, listingStatus.Public);
  const root = PLG.endPLG(builder);

  PLG.finishPLGBuffer(builder, root);
  return builder.asUint8Array();
}

function flatbufferStreamListingResponse(records: Uint8Array[]) {
  const size = records.reduce((total, record) => total + 4 + record.byteLength, 0);
  const body = new Uint8Array(size);
  const view = new DataView(body.buffer);
  let offset = 0;
  for (const record of records) {
    view.setUint32(offset, record.byteLength, true); // LE — flatsql-size-prefixed-le-u32
    offset += 4;
    body.set(record, offset);
    offset += record.byteLength;
  }
  return {
    ok: true,
    status: 200,
    async json() {
      throw new Error('stream responses are not JSON');
    },
    async arrayBuffer() {
      return body.buffer as ArrayBuffer;
    },
  };
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

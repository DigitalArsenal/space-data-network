import * as flatbuffers from 'flatbuffers';
import { describe, expect, it, vi } from 'vitest';
import { PLG } from 'spacedatastandards.org/lib/js/REC/PLG.js';
import { publicationState as listingStatus } from 'spacedatastandards.org/lib/js/PLG/publicationState.js';
import { pluginCategory as pluginType } from 'spacedatastandards.org/lib/js/PLG/pluginCategory.js';

import { loadMarketplaceListingsFromServer } from './marketplace-source';

describe('loadMarketplaceListingsFromServer', () => {
  it('prefers module-delivery PLG listings when that API is available', async () => {
    const fetchMock = vi.fn(async () => ({
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
    }));

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

    expect(fetchMock).toHaveBeenCalledTimes(1);
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
      });

    await expect(
      loadMarketplaceListingsFromServer('https://sdn.spaceaware.io', fetchMock),
    ).resolves.toEqual([
      {
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

    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it('returns an empty marketplace from a successful empty module-delivery response without probing fallback routes', async () => {
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
      });

    await expect(
      loadMarketplaceListingsFromServer('https://sdn.spaceaware.io', fetchMock),
    ).resolves.toEqual([]);

    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('keeps the empty storefront state quiet when the storefront route succeeds with no listings', async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce({
      ok: true,
      status: 200,
      async json() {
        return {
          listings: [],
          total: 0,
        };
      },
    });

    await expect(
      loadMarketplaceListingsFromServer('https://sdn.spaceaware.io', fetchMock),
    ).resolves.toEqual([]);

    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('ignores malformed storefront listing payloads instead of crashing or probing PLG', async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce({
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
    });

    await expect(
      loadMarketplaceListingsFromServer('https://sdn.spaceaware.io', fetchMock),
    ).resolves.toEqual([]);

    expect(fetchMock).toHaveBeenCalledTimes(1);
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
      });

    await expect(
      loadMarketplaceListingsFromServer('https://sdn.spaceaware.io', fetchMock),
    ).resolves.toEqual([]);

    expect(fetchMock).toHaveBeenCalledTimes(2);
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

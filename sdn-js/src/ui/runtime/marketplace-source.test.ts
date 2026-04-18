import * as flatbuffers from 'flatbuffers';
import { describe, expect, it, vi } from 'vitest';
import { PLG } from 'spacedatastandards.org/lib/js/REC/PLG.js';
import { listingStatus } from 'spacedatastandards.org/lib/js/PLG/listingStatus.js';
import { pluginType } from 'spacedatastandards.org/lib/js/PLG/pluginType.js';

import { loadMarketplaceListingsFromServer } from './marketplace-source';

describe('loadMarketplaceListingsFromServer', () => {
  it('prefers storefront listings when that API is available', async () => {
    const fetchMock = vi.fn(async () => ({
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
              tags: ['orbit', 'demo'],
              version: 7,
              active: true,
              updated_at: '2026-04-18T15:00:00Z',
            },
          ],
        };
      },
    }));

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
        tags: ['orbit', 'demo'],
      },
    ]);

    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('falls back to canonical PLG listings when the storefront route is unavailable', async () => {
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
            results: [
              {
                data_base64: Buffer.from(createPlgBytes({
                  pluginId: 'com.space-data-network.orbital-demo',
                  version: '1.2.3',
                  name: 'Orbital Demo',
                  description: 'Synthetic orbital demo',
                })).toString('base64'),
                timestamp: '2026-04-18T15:00:00Z',
              },
            ],
          };
        },
      });

    await expect(
      loadMarketplaceListingsFromServer('https://sdn.spaceaware.io', fetchMock),
    ).resolves.toEqual([
      expect.objectContaining({
        pluginId: 'com.space-data-network.orbital-demo',
        version: '1.2.3',
        name: 'Orbital Demo',
        description: 'Synthetic orbital demo',
      }),
    ]);

    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it('returns an empty marketplace instead of throwing when storefront is empty and the PLG route is unavailable', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce({
        ok: true,
        status: 200,
        async json() {
          return {
            listings: null,
            total: 0,
            facets: {
              data_types: null,
              price_ranges: null,
              providers: null,
              access_types: null,
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

    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it('falls back to canonical PLG listings when storefront succeeds but has no listings', async () => {
    const fetchMock = vi.fn()
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
        ok: true,
        status: 200,
        async json() {
          return {
            results: [
              {
                data_base64: Buffer.from(createPlgBytes({
                  pluginId: 'com.space-data-network.orbital-demo',
                  version: '2.0.0',
                  name: 'Orbital Demo From PLG',
                  description: 'PLG fallback record',
                })).toString('base64'),
                timestamp: '2026-04-18T18:45:00Z',
              },
            ],
          };
        },
      });

    await expect(
      loadMarketplaceListingsFromServer('https://sdn.spaceaware.io', fetchMock),
    ).resolves.toEqual([
      expect.objectContaining({
        pluginId: 'com.space-data-network.orbital-demo',
        version: '2.0.0',
        name: 'Orbital Demo From PLG',
        description: 'PLG fallback record',
      }),
    ]);

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

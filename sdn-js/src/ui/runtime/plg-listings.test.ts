import * as flatbuffers from 'flatbuffers';
import { PLG } from 'spacedatastandards.org/lib/js/REC/PLG.js';
import { listingStatus } from 'spacedatastandards.org/lib/js/PLG/listingStatus.js';
import { pluginType } from 'spacedatastandards.org/lib/js/PLG/pluginType.js';
import { describe, expect, it } from 'vitest';

import { createMarketplaceIndex } from './marketplace';
import { decodeCanonicalPlgListing } from './plg-listings';

describe('decodeCanonicalPlgListing', () => {
  it('decodes real PLG bytes into a marketplace-ready listing', () => {
    const bytes = createPlgBytes({
      pluginId: 'com.space-data-network.orbital-demo',
      version: '1.2.3',
      name: 'Orbital Demo',
      description: 'Synthetic orbital demo',
      tagline: 'Live orbital data',
      publisherName: 'Space Ops',
      publisherHandle: '@spaceops',
      tags: ['orbit', 'OMM', 'demo'],
      listingStatus: listingStatus.Unlisted,
    });

    expect(
      decodeCanonicalPlgListing(bytes, { observedAt: 1_700_000_000_000 }),
    ).toEqual({
      pluginId: 'com.space-data-network.orbital-demo',
      version: '1.2.3',
      name: 'Orbital Demo',
      description: 'Synthetic orbital demo',
      tagline: 'Live orbital data',
      publisherName: 'Space Ops',
      publisherHandle: '@spaceops',
      observedAt: 1_700_000_000_000,
      status: 'unlisted',
      tags: ['orbit', 'OMM', 'demo'],
      standardsUsed: ['OMM'],
    });
  });

  it('supports deduping decoded PLG listings by plugin id and version', () => {
    const index = createMarketplaceIndex([
      decodeCanonicalPlgListing(
        createPlgBytes({
          pluginId: 'com.space-data-network.orbital-demo',
          version: '1.2.3',
          name: 'Orbital Demo',
          publisherName: 'First publisher',
          listingStatus: listingStatus.Public,
        }),
        { observedAt: 10 },
      ),
      decodeCanonicalPlgListing(
        createPlgBytes({
          pluginId: 'com.space-data-network.orbital-demo',
          version: '1.2.3',
          name: 'Orbital Demo',
          publisherName: 'Second publisher',
          listingStatus: listingStatus.Public,
        }),
        { observedAt: 20 },
      ),
    ]);

    expect(index.count()).toBe(1);
    expect(index.get('com.space-data-network.orbital-demo', '1.2.3')).toMatchObject({
      publisherName: 'Second publisher',
      observedAt: 20,
    });
  });
});

function createPlgBytes(options: {
  pluginId: string;
  version: string;
  name: string;
  description?: string;
  tagline?: string;
  publisherName?: string;
  publisherHandle?: string;
  tags?: string[];
  listingStatus?: listingStatus;
}): Uint8Array {
  const builder = new flatbuffers.Builder(256);
  const pluginIdOffset = builder.createString(options.pluginId);
  const nameOffset = builder.createString(options.name);
  const versionOffset = builder.createString(options.version);
  const descriptionOffset = options.description ? builder.createString(options.description) : 0;
  const taglineOffset = options.tagline ? builder.createString(options.tagline) : 0;
  const publisherNameOffset = options.publisherName ? builder.createString(options.publisherName) : 0;
  const publisherHandleOffset = options.publisherHandle ? builder.createString(options.publisherHandle) : 0;
  const tagsOffset = options.tags?.length
    ? PLG.createTagsVector(builder, options.tags.map((tag) => builder.createString(tag)))
    : 0;

  const root = PLG.createPLG(
    builder,
    pluginIdOffset,
    nameOffset,
    versionOffset,
    descriptionOffset,
    taglineOffset,
    pluginType.Analysis,
    publisherNameOffset,
    publisherHandleOffset,
    0,
    0,
    tagsOffset,
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
    options.listingStatus ?? listingStatus.Public,
    0,
  );

  PLG.finishPLGBuffer(builder, root);
  return builder.asUint8Array();
}

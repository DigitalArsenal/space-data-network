import * as flatbuffers from 'flatbuffers';
import { PLG } from 'spacedatastandards.org/lib/js/REC/PLG.js';
import { publicationState as listingStatus } from 'spacedatastandards.org/lib/js/PLG/publicationState.js';
import { pluginCategory as pluginType } from 'spacedatastandards.org/lib/js/PLG/pluginCategory.js';
import { purchaseTier as paymentModel } from 'spacedatastandards.org/lib/js/PLG/purchaseTier.js';
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
      screenshotUrls: [
        'https://cdn.example.test/orbital-demo/screen-1.png',
        'https://cdn.example.test/orbital-demo/screen-2.png',
      ],
      paymentModel: paymentModel.Subscription,
      priceUsdCents: 1299,
      subscriptionPeriodDays: 30,
      acceptedPaymentMethods: ['stripe', 'eth'],
      requiredScope: 'module:com.space-data-network.orbital-demo:run',
      listingStatus: listingStatus.Unlisted,
    });

    expect(
      decodeCanonicalPlgListing(bytes, { observedAt: 1_700_000_000_000 }),
    ).toEqual({
      listingKind: 'module',
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
      screenshotUrls: [
        'https://cdn.example.test/orbital-demo/screen-1.png',
        'https://cdn.example.test/orbital-demo/screen-2.png',
      ],
      paymentModel: 'subscription',
      priceUsdCents: 1299,
      subscriptionPeriodDays: 30,
      acceptedPaymentMethods: ['stripe', 'eth'],
      requiredScope: 'module:com.space-data-network.orbital-demo:run',
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
  screenshotUrls?: string[];
  paymentModel?: paymentModel;
  priceUsdCents?: number;
  subscriptionPeriodDays?: number;
  acceptedPaymentMethods?: string[];
  requiredScope?: string;
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
  const screenshotUrlsOffset = options.screenshotUrls?.length
    ? PLG.createScreenshotUrlsVector(builder, options.screenshotUrls.map((url) => builder.createString(url)))
    : 0;
  const acceptedPaymentMethodsOffset = options.acceptedPaymentMethods?.length
    ? PLG.createAcceptedPaymentMethodsVector(
      builder,
      options.acceptedPaymentMethods.map((method) => builder.createString(method)),
    )
    : 0;
  const requiredScopeOffset = options.requiredScope ? builder.createString(options.requiredScope) : 0;

  PLG.startPLG(builder);
  PLG.addPluginId(builder, pluginIdOffset);
  PLG.addName(builder, nameOffset);
  PLG.addVersion(builder, versionOffset);
  PLG.addDescription(builder, descriptionOffset);
  PLG.addTagline(builder, taglineOffset);
  PLG.addPluginType(builder, pluginType.Analysis);
  PLG.addPublisherName(builder, publisherNameOffset);
  PLG.addPublisherHandle(builder, publisherHandleOffset);
  PLG.addTags(builder, tagsOffset);
  PLG.addScreenshotUrls(builder, screenshotUrlsOffset);
  PLG.addAbiVersion(builder, 1);
  PLG.addEncrypted(builder, true);
  PLG.addRequiredScope(builder, requiredScopeOffset);
  PLG.addPaymentModel(builder, options.paymentModel ?? paymentModel.Free);
  PLG.addPriceUsdCents(builder, options.priceUsdCents ?? 0);
  PLG.addSubscriptionPeriodDays(builder, options.subscriptionPeriodDays ?? 0);
  PLG.addAcceptedPaymentMethods(builder, acceptedPaymentMethodsOffset);
  PLG.addListingStatus(builder, options.listingStatus ?? listingStatus.Public);
  const root = PLG.endPLG(builder);

  PLG.finishPLGBuffer(builder, root);
  return builder.asUint8Array();
}

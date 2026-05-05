import { SUPPORTED_SCHEMAS } from '../../schemas';
import * as flatbuffers from 'flatbuffers';
import { PLG } from 'spacedatastandards.org/lib/js/REC/PLG.js';
import { publicationState } from 'spacedatastandards.org/lib/js/PLG/publicationState.js';
import { purchaseTier } from 'spacedatastandards.org/lib/js/PLG/purchaseTier.js';

import type { CanonicalListing, ListingPaymentModel, ListingStatus } from './types';

export interface DecodeCanonicalPlgListingOptions {
  observedAt?: number;
}

const textDecoder = new TextDecoder();

export function decodeCanonicalPlgListing(
  bytes: Uint8Array,
  options: DecodeCanonicalPlgListingOptions = {},
): CanonicalListing {
  const manifest = PLG.getRootAsPLG(new flatbuffers.ByteBuffer(bytes));
  const unpacked = manifest.unpack();
  const listing: CanonicalListing = {
    listingKind: 'module',
    pluginId: normalizeRequiredString(unpacked.PLUGIN_ID, 'pluginId'),
    version: normalizeRequiredString(unpacked.VERSION, 'version'),
    observedAt: options.observedAt ?? 0,
    status: decodeListingStatus(unpacked.LISTING_STATUS),
  };

  const name = normalizeOptionalString(unpacked.NAME);
  if (name) {
    listing.name = name;
  }

  const description = normalizeOptionalString(unpacked.DESCRIPTION);
  if (description) {
    listing.description = description;
  }

  const tagline = normalizeOptionalString(unpacked.TAGLINE);
  if (tagline) {
    listing.tagline = tagline;
  }

  const publisherName = normalizeOptionalString(unpacked.PUBLISHER_NAME);
  if (publisherName) {
    listing.publisherName = publisherName;
  }

  const publisherHandle = normalizeOptionalString(unpacked.PUBLISHER_HANDLE);
  if (publisherHandle) {
    listing.publisherHandle = publisherHandle;
  }

  const publisherPeerId = normalizeOptionalString(unpacked.PROVIDER_PEER_ID);
  if (publisherPeerId) {
    listing.publisherPeerId = publisherPeerId;
  }

  const tags = normalizeStringList(unpacked.TAGS);
  if (tags) {
    listing.tags = tags;
  }

  const screenshotUrls = normalizeStringList(unpacked.SCREENSHOT_URLS);
  if (screenshotUrls) {
    listing.screenshotUrls = screenshotUrls;
  }

  listing.paymentModel = decodePaymentModel(unpacked.PAYMENT_MODEL);

  if (unpacked.PRICE_USD_CENTS > 0) {
    listing.priceUsdCents = unpacked.PRICE_USD_CENTS;
  }

  if (unpacked.SUBSCRIPTION_PERIOD_DAYS > 0) {
    listing.subscriptionPeriodDays = unpacked.SUBSCRIPTION_PERIOD_DAYS;
  }

  const acceptedPaymentMethods = normalizeStringList(unpacked.ACCEPTED_PAYMENT_METHODS);
  if (acceptedPaymentMethods) {
    listing.acceptedPaymentMethods = acceptedPaymentMethods;
  }

  const requiredScope = normalizeOptionalString(unpacked.REQUIRED_SCOPE);
  if (requiredScope) {
    listing.requiredScope = requiredScope;
  }

  const standardsUsed = inferStandardsUsed(
    listing.pluginId,
    name,
    description,
    tagline,
    tags,
  );
  if (standardsUsed) {
    listing.standardsUsed = standardsUsed;
  }

  return listing;
}

export function inferStandardsUsed(
  ...sources: Array<string | string[] | undefined>
): string[] | undefined {
  const values = sources.flatMap((source) => Array.isArray(source) ? source : (source ? [source] : []));
  const matches = SUPPORTED_SCHEMAS
    .map((schemaName) => schemaName.replace(/\.fbs$/i, ''))
    .filter((schema) => values.some((value) => matchesSchemaToken(value, schema)));

  return matches.length > 0 ? matches : undefined;
}

function normalizeRequiredString(
  value: string | Uint8Array | null | undefined,
  fieldName: string,
): string {
  const normalized = normalizeOptionalString(value);
  if (!normalized) {
    throw new Error(`${fieldName} is required`);
  }
  return normalized;
}

function normalizeOptionalString(
  value: string | Uint8Array | null | undefined,
): string | undefined {
  const normalized = (
    typeof value === 'string'
      ? value
      : value instanceof Uint8Array
        ? textDecoder.decode(value)
        : ''
  ).trim();
  return normalized ? normalized : undefined;
}

function normalizeStringList(
  values: (string | Uint8Array | null | undefined)[] | null | undefined,
): string[] | undefined {
  const normalized = values
    ?.map((value) => normalizeOptionalString(value))
    .filter((value): value is string => Boolean(value));

  return normalized && normalized.length > 0 ? normalized : undefined;
}

function decodeListingStatus(status: publicationState): ListingStatus {
  switch (status) {
    case publicationState.Public:
      return 'public';
    case publicationState.Unlisted:
      return 'unlisted';
    case publicationState.Retired:
      return 'retired';
    default:
      throw new Error(`unknown PLG listing status: ${status}`);
  }
}

function decodePaymentModel(model: purchaseTier): ListingPaymentModel {
  switch (model) {
    case purchaseTier.Free:
      return 'free';
    case purchaseTier.OneTime:
      return 'one-time';
    case purchaseTier.Subscription:
      return 'subscription';
    default:
      throw new Error(`unknown PLG payment model: ${model}`);
  }
}

function matchesSchemaToken(value: string, schema: string): boolean {
  const escapedSchema = schema.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  return new RegExp(`(^|[^A-Za-z0-9])${escapedSchema}(?:\\.fbs)?($|[^A-Za-z0-9])`, 'i').test(value);
}

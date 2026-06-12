import { isValidSchema, type SchemaName } from './schemas';

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;
const STANDARD_CODE_RE = /^[A-Z]{3}$/;
const INTERNAL_SCHEMA_SUFFIX = String.fromCharCode(46, 102, 98, 115);

export const CHANNEL_TOPIC_PREFIX = '/spacedatanetwork/channels/';

export interface ParsedChannelId {
  channelId: string;
  sourceId: string;
  standardCode: string;
  feedUuid: string | null;
}

export interface ChannelIdInput {
  sourceId: string;
  standardCode: string;
  feedUuid?: string | null;
}

export function assertStandardCode(value: string): string {
  const code = value.trim();
  if (!STANDARD_CODE_RE.test(code)) {
    throw new Error(`invalid standardCode ${JSON.stringify(value)}`);
  }
  if (!isValidSchema(`${code}${INTERNAL_SCHEMA_SUFFIX}` as SchemaName)) {
    throw new Error(`unknown standardCode ${JSON.stringify(value)}`);
  }
  return code;
}

export function schemaNameFromStandardCode(standardCode: string): SchemaName {
  const code = assertStandardCode(standardCode);
  return `${code}${INTERNAL_SCHEMA_SUFFIX}` as SchemaName;
}

export function standardCodeFromSchemaName(schemaName: string): string {
  const value = schemaName.trim();
  if (!value.endsWith(INTERNAL_SCHEMA_SUFFIX)) {
    throw new Error(`invalid schema name ${JSON.stringify(schemaName)}`);
  }
  return assertStandardCode(value.slice(0, -INTERNAL_SCHEMA_SUFFIX.length));
}

export function parseChannelId(channelId: string): ParsedChannelId {
  const value = channelId.trim();
  if (value === '' || value.includes('--')) {
    throw new Error(`invalid channel ID ${JSON.stringify(channelId)}`);
  }

  const parts = value.split('-');
  if (parts.length < 2 || parts.some((part) => part === '')) {
    throw new Error(`invalid channel ID ${JSON.stringify(channelId)}`);
  }

  let feedUuid: string | null = null;
  let standardIndex = parts.length - 1;
  if (parts.length >= 7) {
    const maybeUuid = parts.slice(-5).join('-');
    if (UUID_RE.test(maybeUuid)) {
      feedUuid = maybeUuid;
      standardIndex = parts.length - 6;
    }
  }

  if (standardIndex <= 0) {
    throw new Error(`channel sourceId is required in ${JSON.stringify(channelId)}`);
  }

  const standardCode = assertStandardCode(parts[standardIndex]);
  const sourceId = parts.slice(0, standardIndex).join('-');
  if (sourceId === '') {
    throw new Error(`channel sourceId is required in ${JSON.stringify(channelId)}`);
  }

  return {
    channelId: formatChannelId({ sourceId, standardCode, feedUuid }),
    sourceId,
    standardCode,
    feedUuid,
  };
}

export function formatChannelId(input: ChannelIdInput): string {
  const sourceId = input.sourceId.trim();
  if (sourceId === '' || sourceId.includes('--')) {
    throw new Error('channel sourceId is required');
  }

  const standardCode = assertStandardCode(input.standardCode);
  const feedUuid = input.feedUuid == null ? null : input.feedUuid.trim();
  if (feedUuid && !UUID_RE.test(feedUuid)) {
    throw new Error(`invalid feedUuid ${JSON.stringify(input.feedUuid)}`);
  }

  return feedUuid ? `${sourceId}-${standardCode}-${feedUuid}` : `${sourceId}-${standardCode}`;
}

export function channelDiscoveryTopic(standardCode: string): string {
  return `${CHANNEL_TOPIC_PREFIX}${assertStandardCode(standardCode)}`;
}

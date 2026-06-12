import { describe, expect, it } from 'vitest';
import {
  channelDiscoveryTopic,
  formatChannelId,
  parseChannelId,
  schemaNameFromStandardCode,
  standardCodeFromSchemaName,
} from './channels';

const internalSchemaSuffix = String.fromCharCode(46, 102, 98, 115);

describe('SDN channel naming', () => {
  it('parses source and three-letter standard code from the right', () => {
    expect(parseChannelId('celestrak-OMM')).toEqual({
      channelId: 'celestrak-OMM',
      sourceId: 'celestrak',
      standardCode: 'OMM',
      feedUuid: null,
    });
    expect(parseChannelId('celestrak-eth-CDM')).toEqual({
      channelId: 'celestrak-eth-CDM',
      sourceId: 'celestrak-eth',
      standardCode: 'CDM',
      feedUuid: null,
    });
  });

  it('parses optional UUID suffix without splitting hyphenated source IDs', () => {
    expect(parseChannelId('spaceaware-live-OMM-550e8400-e29b-41d4-a716-446655440000')).toEqual({
      channelId: 'spaceaware-live-OMM-550e8400-e29b-41d4-a716-446655440000',
      sourceId: 'spaceaware-live',
      standardCode: 'OMM',
      feedUuid: '550e8400-e29b-41d4-a716-446655440000',
    });
  });

  it('rejects lowercase, missing pieces, and schema-file suffixes', () => {
    for (const input of [
      '',
      'OMM',
      '-OMM',
      'celestrak-',
      'celestrak-omm',
      `celestrak-OMM${internalSchemaSuffix}`,
      'celestrak-OMM-not-a-uuid',
    ]) {
      expect(() => parseChannelId(input), input).toThrow();
    }
  });

  it('formats channels and maps standard codes to internal schema names', () => {
    expect(formatChannelId({ sourceId: 'celestrak-eth', standardCode: 'CDM' })).toBe('celestrak-eth-CDM');
    expect(formatChannelId({
      sourceId: 'spaceaware',
      standardCode: 'OMM',
      feedUuid: '550e8400-e29b-41d4-a716-446655440000',
    })).toBe('spaceaware-OMM-550e8400-e29b-41d4-a716-446655440000');
    expect(schemaNameFromStandardCode('OMM')).toBe(`OMM${internalSchemaSuffix}`);
    expect(standardCodeFromSchemaName(`OMM${internalSchemaSuffix}`)).toBe('OMM');
    expect(channelDiscoveryTopic('OMM')).toBe('/spacedatanetwork/channels/OMM');
  });
});

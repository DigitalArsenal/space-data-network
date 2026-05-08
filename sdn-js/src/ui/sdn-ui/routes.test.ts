import { describe, expect, it } from 'vitest';
import { normalizeSdnRoute } from '../../../ui/src/lib/routes';

describe('SDN Svelte UI route compatibility', () => {
  it.each([
    ['/', '/node'],
    ['/status', '/node'],
    ['/settings', '/node?panel=advanced'],
    ['/files', '/data'],
    ['/pins', '/data?tab=pins'],
    ['/modules', '/peers?tab=modules'],
    ['/marketplace', '/peers?tab=marketplace'],
    ['/explore/bafy123', '/data?inspect=bafy123'],
    ['/local-data', '/data'],
    ['/local-data?tab=pins', '/data?tab=pins'],
  ])('maps %s to %s', (input, expected) => {
    expect(normalizeSdnRoute(input)).toBe(expected);
  });
});

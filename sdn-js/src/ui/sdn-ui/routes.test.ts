import { describe, expect, it } from 'vitest';
import { normalizeSdnRoute } from '../../../ui/src/lib/routes';

describe('SDN Svelte UI route compatibility', () => {
  it.each([
    ['/', '/node'],
    ['/status', '/node'],
    ['/settings', '/node?panel=advanced'],
    ['/files', '/local-data'],
    ['/pins', '/local-data?tab=pins'],
    ['/modules', '/peers?tab=modules'],
    ['/marketplace', '/peers?tab=marketplace'],
    ['/explore/bafy123', '/local-data?inspect=bafy123'],
  ])('maps %s to %s', (input, expected) => {
    expect(normalizeSdnRoute(input)).toBe(expected);
  });
});

import { describe, expect, it } from 'vitest';

import { readDefaultProviderDescriptorUrl } from './runtime-config';

describe('readDefaultProviderDescriptorUrl', () => {
  it('returns the configured provider descriptor URL when it is a valid absolute URL', () => {
    expect(readDefaultProviderDescriptorUrl({
      VITE_SDN_DEFAULT_PROVIDER_URL: 'https://sdn.spaceaware.io/api/module-delivery/provider',
    })).toBe('https://sdn.spaceaware.io/api/module-delivery/provider');
  });

  it('ignores blank or non-http URLs', () => {
    expect(readDefaultProviderDescriptorUrl({
      VITE_SDN_DEFAULT_PROVIDER_URL: '   ',
    })).toBeNull();

    expect(readDefaultProviderDescriptorUrl({
      VITE_SDN_DEFAULT_PROVIDER_URL: '/api/module-delivery/provider',
    })).toBeNull();
  });
});

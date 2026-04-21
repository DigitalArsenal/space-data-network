import { describe, expect, it } from 'vitest';

import {
  readDefaultProviderDescriptorUrl,
  readHostedServerBaseUrl,
  readHostedIPFSDashboardUrl,
} from './runtime-config';

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

describe('readHostedServerBaseUrl', () => {
  it('returns the configured hosted server base URL when present', () => {
    expect(readHostedServerBaseUrl({
      __SDN_CONFIG__: {
        serverBaseUrl: 'https://sdn.spaceaware.io',
      },
    })).toBe('https://sdn.spaceaware.io/');
  });

  it('ignores blank or non-http hosted server base URLs', () => {
    expect(readHostedServerBaseUrl({
      __SDN_CONFIG__: {
        serverBaseUrl: '   ',
      },
    })).toBeNull();

    expect(readHostedServerBaseUrl({
      __SDN_CONFIG__: {
        serverBaseUrl: '/login',
      },
    })).toBeNull();
  });
});

describe('readHostedIPFSDashboardUrl', () => {
  it('returns a hosted relative IPFS dashboard path when present', () => {
    expect(readHostedIPFSDashboardUrl({
      __SDN_CONFIG__: {
        ipfsDashboardUrl: '/webui/',
      },
    })).toBe('/webui/');
  });

  it('returns a desktop custom-protocol IPFS dashboard URL when present', () => {
    expect(readHostedIPFSDashboardUrl({
      __SDN_CONFIG__: {
        ipfsDashboardUrl: 'webui://-/#/files',
      },
    })).toBe('webui://-/#/files');
  });

  it('ignores blank or unsafe hosted IPFS dashboard URLs', () => {
    expect(readHostedIPFSDashboardUrl({
      __SDN_CONFIG__: {
        ipfsDashboardUrl: '   ',
      },
    })).toBeNull();

    expect(readHostedIPFSDashboardUrl({
      __SDN_CONFIG__: {
        ipfsDashboardUrl: 'javascript:alert(1)',
      },
    })).toBeNull();
  });
});

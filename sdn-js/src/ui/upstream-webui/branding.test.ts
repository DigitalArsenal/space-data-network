import { describe, expect, it } from 'vitest';

import { brandUpstreamDocumentTitle } from '../../../ui/src/upstream-webui/branding.js';

describe('sdn upstream webui branding helper', () => {
  it('rewrites upstream IPFS page titles for the SDN root shell', () => {
    expect(brandUpstreamDocumentTitle('Status | IPFS')).toBe('Status | Space Data Network');
    expect(brandUpstreamDocumentTitle('Peers | IPFS')).toBe('Peers | Space Data Network');
    expect(brandUpstreamDocumentTitle('IPFS')).toBe('Space Data Network');
  });

  it('leaves already branded titles untouched', () => {
    expect(brandUpstreamDocumentTitle('Space Data Network Dashboard')).toBe('Space Data Network Dashboard');
  });
});

import fs from 'node:fs/promises';
import path from 'node:path';

import { describe, expect, it } from 'vitest';

import { brandUpstreamDocumentTitle } from '../../../ui/src/upstream-webui/branding.js';

const uiSrcPath = path.resolve(__dirname, '../../../ui/src/upstream-webui');

describe('sdn upstream webui branding helper', () => {
  it('rewrites upstream IPFS page titles for the SDN root shell', () => {
    expect(brandUpstreamDocumentTitle('Status | IPFS')).toBe('Status | Space Data Network');
    expect(brandUpstreamDocumentTitle('Peers | IPFS')).toBe('Peers | Space Data Network');
    expect(brandUpstreamDocumentTitle('IPFS')).toBe('Space Data Network');
  });

  it('leaves already branded titles untouched', () => {
    expect(brandUpstreamDocumentTitle('Space Data Network Dashboard')).toBe('Space Data Network Dashboard');
  });

  it('routes the SDN welcome page IPFS callout to the standalone /webui surface', async () => {
    const source = await fs.readFile(
      path.join(uiSrcPath, 'overrides/components/about-webui/AboutWebUI.js'),
      'utf8',
    );

    expect(source).toContain("href='/webui'");
    expect(source).not.toContain("href='/webui/'");
  });

  it('replaces the SDN root diagnostics sidebar item with an IPFS link to /webui', async () => {
    const source = await fs.readFile(
      path.join(uiSrcPath, 'overrides/navigation/NavBar.js'),
      'utf8',
    );

    expect(source).toContain("href='/webui'");
    expect(source).toContain('>IPFS<');
    expect(source).not.toContain("to='/diagnostics'");
    expect(source).not.toContain("href='#/diagnostics'");
  });

  it('uses a standalone centered SDN logo mark asset instead of baked text logo SVGs', async () => {
    const source = await fs.readFile(
      path.join(uiSrcPath, 'overrides/navigation/NavBar.js'),
      'utf8',
    );

    expect(source).toContain("import sdnLogoMark from './sdn-logo-mark.svg'");
    expect(source).not.toContain('sdn-logo-text-vert.svg');
    expect(source).not.toContain('sdn-logo-text-horiz.svg');
  });

  it('adds a root-only account control that opens the wallet UI and supports logout', async () => {
    const source = await fs.readFile(
      path.join(uiSrcPath, 'overrides/App.js'),
      'utf8',
    );

    expect(source).toContain('StrokeUser');
    expect(source).toContain('StrokePower');
    expect(source).toContain('/api/auth/logout');
    expect(source).toContain('mountWalletUI');
  });
});

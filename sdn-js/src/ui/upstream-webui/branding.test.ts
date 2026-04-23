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
    expect(source).toContain("target='_blank'");
    expect(source).toContain('>IPFS<');
    expect(source).not.toContain("to='/diagnostics'");
    expect(source).not.toContain("href='#/diagnostics'");
    expect(source).toContain("import ipfsLogoMark from '../../../../../../webui/src/navigation/ipfs-logo.svg'");
  });

  it('adds root-only directory and identity nav entries without removing the IPFS escape hatch', async () => {
    const source = await fs.readFile(
      path.join(uiSrcPath, 'overrides/navigation/NavBar.js'),
      'utf8',
    );

    expect(source).toContain("to='/directory'");
    expect(source).toContain("to='/identity'");
    expect(source).toContain('>Directory<');
    expect(source).toContain('>Identity<');
    expect(source).toContain("href='/webui'");
  });

  it('leaves the upstream /webui diagnostics sidebar item untouched', async () => {
    const source = await fs.readFile(
      path.resolve(__dirname, '../../../../webui/src/navigation/NavBar.js'),
      'utf8',
    );

    expect(source).toContain("to='/diagnostics'");
    expect(source).not.toContain("href='/webui'");
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

  it('defines root-only directory and identity route pages in the SDN override tree', async () => {
    const routes = await fs.readFile(
      path.join(uiSrcPath, 'overrides/bundles/routes.js'),
      'utf8',
    );

    expect(routes).toContain('../directory/DirectoryPage.js');
    expect(routes).toContain('../identity/IdentityPage.js');
    expect(routes).toContain("'/directory'");
    expect(routes).toContain("'/identity'");
  });

  it('adds a single root-only account control that opens the wallet UI account surface', async () => {
    const source = await fs.readFile(
      path.join(uiSrcPath, 'overrides/App.js'),
      'utf8',
    );

    expect(source).toContain('GlyphUser');
    expect(source).not.toContain('StrokeUser');
    expect(source).not.toContain('GlyphPower');
    expect(source).not.toContain('StrokePower');
    expect(source).toContain('/api/auth/logout');
    expect(source).toContain('mountWalletUI');
    expect(source).toContain('openAccount');
    expect(source).not.toContain('openLogin');
    expect(source.indexOf("<Connected className='joyride-app-status' />")).toBeLessThan(
      source.indexOf("<SessionControls className='ml1' />"),
    );
  });

  it('uses SDN node info for the root status node identity instead of upstream identity context', async () => {
    const source = await fs.readFile(
      path.join(uiSrcPath, 'overrides/status/NodeInfo.js'),
      'utf8',
    );

    expect(source).toContain("fetch('/api/node/info'");
    expect(source).toContain('peer_id');
    expect(source).toContain('spacedatanetwork/');
    expect(source).not.toContain('useIdentity');
  });
});

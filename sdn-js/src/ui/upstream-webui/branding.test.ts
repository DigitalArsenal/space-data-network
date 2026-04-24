import fs from 'node:fs/promises';
import path from 'node:path';

import { describe, expect, it } from 'vitest';

import {
  brandUpstreamDocumentTitle,
  rootOnlyDocumentTitleForHash,
} from '../../../ui/src/upstream-webui/branding.js';

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

  it('sets document titles for root-only SDN routes', () => {
    expect(rootOnlyDocumentTitleForHash('#/directory')).toBe('Directory | Space Data Network');
    expect(rootOnlyDocumentTitleForHash('#/identity')).toBeNull();
    expect(rootOnlyDocumentTitleForHash('#/peers')).toBeNull();
    expect(brandUpstreamDocumentTitle('Peers | Space Data Network', '#/directory')).toBe('Directory | Space Data Network');
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

  it('adds the root-only directory nav entry without a separate identity menu', async () => {
    const source = await fs.readFile(
      path.join(uiSrcPath, 'overrides/navigation/NavBar.js'),
      'utf8',
    );

    expect(source).toContain("to='/directory'");
    expect(source).toContain('>Directory<');
    expect(source).not.toContain("to='/identity'");
    expect(source).not.toContain('>Identity<');
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

  it('defines the root-only directory route without a separate identity route', async () => {
    const routes = await fs.readFile(
      path.join(uiSrcPath, 'overrides/bundles/routes.js'),
      'utf8',
    );

    expect(routes).toContain('../directory/DirectoryPage.js');
    expect(routes).toContain("'/directory'");
    expect(routes).not.toContain('../identity/IdentityPage.js');
    expect(routes).not.toContain("'/identity'");
  });

  it('keeps Directory focused on full-width search, import, and result surfaces', async () => {
    const source = await fs.readFile(
      path.join(uiSrcPath, 'overrides/directory/DirectoryPage.js'),
      'utf8',
    );

    expect(source).toContain("className='sdn-directory-page w-100 ph3 ph4-l pv3'");
    expect(source).toContain('Directory records');
    expect(source).toContain('Type');
    expect(source).toContain('sortableDirectoryHeader');
    expect(source).toContain('pageSize');
    expect(source).toContain('directoryPagerButtonStyle');
    expect(source).toContain('Upload vCard / EPM');
    expect(source).toContain('overflow-auto');
    expect(source).not.toContain('recordTypeFilter');
    expect(source).not.toContain('sdn-directory-record-type-filter');
    expect(source).not.toContain('Matched directory node');
    expect(source).not.toContain('Matched directory user');
    expect(source).not.toContain('<h2 className=\'f4 mt0 mb3\'>Nodes</h2>');
    expect(source).not.toContain('<h2 className=\'f4 mt0 mb3\'>Users</h2>');
    expect(source).not.toContain('measure-wide');
    expect(source).not.toContain('Node profile');
    expect(source).not.toContain('runtimeRef.current.connect');
  });

  it('folds the SDN node profile into the Status advanced disclosure', async () => {
    const source = await fs.readFile(
      path.join(uiSrcPath, 'overrides/status/NodeInfoAdvanced.js'),
      'utf8',
    );

    expect(source).toContain('Node profile');
    expect(source).toContain("fetch('/api/node/info'");
    expect(source).toContain('runtimeRef.current.connect');
    expect(source).toContain('Descriptor URL');
    expect(source).toContain('summaryText={t(\'app:terms.advanced\')}');
  });

  it('lets Directory import vCard or EPM records through the shared adapter', async () => {
    const source = await fs.readFile(
      path.join(uiSrcPath, 'overrides/directory/DirectoryPage.js'),
      'utf8',
    );

    expect(source).toContain('directory.importRecord');
    expect(source).toContain('accept=\'.vcf,.vcard,.json,application/json,text/vcard,text/x-vcard\'');
    expect(source).toContain('Upload vCard / EPM');
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

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
    expect(source).toContain('directoryButtonCenterStyle');
    expect(source).toContain('bitcoinBalanceURL');
    expect(source).toContain('https://mempool.space/address/');
    expect(source).toContain("target='_blank'");
    expect(source).toContain("rel='noreferrer'");
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

  it('captures Settings profile input values before updating state', async () => {
    const source = await fs.readFile(
      path.join(uiSrcPath, 'overrides/settings/SettingsPage.js'),
      'utf8',
    );

    expect(source).toContain('const { value } = event.target');
    expect(source).not.toContain('event.target.value');
  });

  it('generates the Settings profile QR client-side from the loaded EPM profile', async () => {
    const source = await fs.readFile(
      path.join(uiSrcPath, 'overrides/settings/SettingsPage.js'),
      'utf8',
    );

    expect(source).toContain("import QRCode from 'qrcode'");
    expect(source).toContain('QRCode.toDataURL(nodeProfileQRVCard(epmPayload, profile)');
    expect(source).toContain('const [epmPayload, setEPMPayload] = useState({})');
    expect(source).toContain('nodeProfileQRVCard');
    expect(source).toContain('const displayName = firstProfileString(profile.dn, epmPayload.dn');
    expect(source).toContain('givenName = displayName');
    expect(source).toContain('X-SDN-EPM-CID');
    expect(source).toContain('addProfileQRIdentityEmailLines(lines, epmPayload)');
    expect(source).toContain('signing.digitalarsenal.io');
    expect(source).toContain('encryption.digitalarsenal.io');
    expect(source).toContain('bitcoin.digitalarsenal.io');
    expect(source).toContain('ethereum.digitalarsenal.io');
    expect(source).toContain('solana.digitalarsenal.io');
    expect(source).not.toContain('/api/node/epm/qr');
    expect(source).not.toContain('X-SDN-EPM-B64');
    expect(source).not.toContain('X-SDN-EPM-SIGNATURE');
    expect(source).not.toContain('X-SDN-EPM-SIGNATURE-TIMESTAMP');
    expect(source).not.toContain('addProfileEmailAlias');
    expect(source).not.toContain("src={`/api/node/epm/qr?v=${qrVersion}`}");
  });

  it('uses common first and last name labels in the Settings profile editor', async () => {
    const source = await fs.readFile(
      path.join(uiSrcPath, 'overrides/settings/SettingsPage.js'),
      'utf8',
    );

    expect(source).toContain("['given_name', 'First name']");
    expect(source).toContain("['family_name', 'Last Name']");
    expect(source).not.toContain("['given_name', 'Given name']");
    expect(source).not.toContain("['family_name', 'Family name']");
  });

  it('renders Settings as Profile, Server Admin, and Node Settings tabs', async () => {
    const source = await fs.readFile(
      path.join(uiSrcPath, 'overrides/settings/SettingsPage.js'),
      'utf8',
    );

    expect(source).toContain("['profile', 'Profile']");
    expect(source).toContain("['server-admin', 'Server Admin']");
    expect(source).toContain("['node-settings', 'Node Settings']");
    expect(source).toContain("const [activeTab, setActiveTab] = useState('profile')");
    expect(source).toContain("activeTab === 'profile' && <NodeProfileSection />");
    expect(source).toContain("activeTab === 'server-admin' && <ServerAdminSection />");
    expect(source).toContain("activeTab === 'node-settings' && <UpstreamSettingsPage />");
  });

  it('adds Server Admin grants from uploaded vCard or EPM records through the auth user API', async () => {
    const source = await fs.readFile(
      path.join(uiSrcPath, 'overrides/settings/SettingsPage.js'),
      'utf8',
    );

    expect(source).toContain('function ServerAdminSection()');
    expect(source).toContain("fetch('/api/auth/users'");
    expect(source).toContain("fetch(`/api/auth/users/${encodeURIComponent(grant.xpub)}`");
    expect(source).toContain('serverAdminGrantFromText(text, permissionLevel)');
    expect(source).toContain("accept='.vcf,.vcard,.json,application/json,text/vcard,text/x-vcard'");
    expect(source).toContain('Upload vCard / EPM');
    expect(source).toContain('Server backend permission');
    expect(source).toContain("['limited', 'Limited']");
    expect(source).toContain("['standard', 'Standard']");
    expect(source).toContain("['trusted', 'Trusted']");
    expect(source).toContain("['admin', 'Admin']");
    expect(source).toContain('extractServerAdminXPubFromVCard');
    expect(source).toContain('extractServerAdminSigningKeyFromVCard');
    expect(source).toContain('extractServerAdminXPubFromJSON');
    expect(source).toContain('extractServerAdminSigningKeyFromJSON');
  });

  it('centers every Settings profile action control', async () => {
    const source = await fs.readFile(
      path.join(uiSrcPath, 'overrides/settings/SettingsPage.js'),
      'utf8',
    );

    expect(source).toContain('const profileActionButtonStyle =');
    expect(source).toContain('const profilePhotoButtonStyle =');
    expect(source).toContain("flexDirection: 'column'");
    expect(source).toContain("gap: '0.15rem'");
    expect(source).toContain('...profileActionButtonStyle');
    expect(source.match(/style=\{profileActionButtonStyle\}/g)?.length).toBeGreaterThanOrEqual(3);
    expect(source).toContain("className='Button transition-all sans-serif inline-flex items-center justify-center");
    expect(source).toContain('const profileShareButtonStyle =');
    expect(source).toContain("color: '#ffffff'");
  });

  it('renders Settings photo actions in the right rail with short labels, icons, and removal confirmation', async () => {
    const source = await fs.readFile(
      path.join(uiSrcPath, 'overrides/settings/SettingsPage.js'),
      'utf8',
    );

    expect(source).toContain("import PictureIcon from '../../../../../../webui/src/icons/StrokePicture.tsx'");
    expect(source).toContain("import CameraIcon from '../../../../../../webui/src/icons/StrokeCamera.tsx'");
    expect(source).toContain("import TrashIcon from '../../../../../../webui/src/icons/StrokeTrash.tsx'");
    expect(source).toContain('const profilePhotoActionsClassName =');
    expect(source).toContain('<ProfileActionIcon Icon={PictureIcon} />');
    expect(source).toContain('<ProfileActionIcon Icon={CameraIcon} />');
    expect(source).toContain('<ProfileActionIcon Icon={TrashIcon} />');
    expect(source).toContain('<ProfileActionText>Upload</ProfileActionText>');
    expect(source).toContain('<ProfileActionText>Take</ProfileActionText>');
    expect(source).toContain('<ProfileActionText>Remove</ProfileActionText>');
    expect(source).not.toContain('<ProfileActionText>Upload image</ProfileActionText>');
    expect(source).not.toContain('<ProfileActionText>Take picture</ProfileActionText>');
    expect(source).toContain("return <span className='db tc truncate f7'>{children}</span>");
    expect(source).not.toContain('<ProfileActionSpacer />');
    expect(source).not.toContain('function ProfileActionSpacer');
    expect(source).toContain('danger');
    expect(source).not.toContain("disabled={!profile.photo_data_url}");
    expect(source).toContain("window.confirm('Are you sure you want to remove this profile image?')");
    expect(source.indexOf("<aside className='w-100 w-third-l")).toBeLessThan(source.indexOf("<h3 className='f5 mt0 mb2 charcoal'>Photo</h3>"));
    expect(source.indexOf("<h3 className='f5 mt0 mb2 charcoal'>Photo</h3>")).toBeLessThan(source.indexOf("<h3 className='f5 mt4 mb2 charcoal'>Share</h3>"));
  });

  it('renders the Settings QR above the vCard download button', async () => {
    const source = await fs.readFile(
      path.join(uiSrcPath, 'overrides/settings/SettingsPage.js'),
      'utf8',
    );

    expect(source.indexOf("alt='Node profile QR code'")).toBeLessThan(source.indexOf('Download .vcf'));
  });

  it('opens the Settings profile image preview in a modal', async () => {
    const source = await fs.readFile(
      path.join(uiSrcPath, 'overrides/settings/SettingsPage.js'),
      'utf8',
    );

    expect(source).toContain('const [imageModalOpen, setImageModalOpen] = useState(false)');
    expect(source).toContain("aria-label='Open profile image preview'");
    expect(source).toContain('ProfileImageModal');
    expect(source).toContain("role='dialog'");
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

  it('exports Directory vCards with iPhone-visible SDN key and EPM fields', async () => {
    const source = await fs.readFile(
      path.join(uiSrcPath, 'overrides/directory/DirectoryPage.js'),
      'utf8',
    );

    expect(source).toContain('VERSION:3.0');
    expect(source).toContain('-//Apple Inc.//iPhone OS 15.1.1//EN');
    expect(source).toContain('signing.digitalarsenal.io');
    expect(source).toContain('encryption.digitalarsenal.io');
    expect(source).toContain('bitcoin.digitalarsenal.io');
    expect(source).toContain('ethereum.digitalarsenal.io');
    expect(source).toContain('solana.digitalarsenal.io');
    expect(source).toContain('X-ABRELATEDNAMES');
    expect(source).toContain('Binary EPM');
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

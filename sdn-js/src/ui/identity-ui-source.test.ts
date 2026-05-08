import { existsSync, readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

const uiRoot = new URL('../../ui/src/', import.meta.url);

function readUiSource(path: string): string {
  const fileUrl = new URL(path, uiRoot);
  expect(existsSync(fileUrl), `${path} should exist`).toBe(true);
  return readFileSync(fileUrl, 'utf8');
}

function expectSourceToContainAll(source: string, snippets: string[]): void {
  for (const snippet of snippets) {
    expect(source).toContain(snippet);
  }
}

describe('SDN identity Svelte source', () => {
  it('mounts the hosted EPM workspace from the node screen', () => {
    const source = readUiSource('screens/NodeScreen.svelte');

    expect(source).toContain("import IdentityPanel from '../components/IdentityPanel.svelte'");
    expect(source).toContain('<IdentityPanel');
  });

  it('exposes hosted EPM editing, upload, public downloads, QR, wallet, and encrypted Core controls', () => {
    const source = readUiSource('components/IdentityPanel.svelte');

    expectSourceToContainAll(source, [
      'backend.saveHostedEpm',
      'backend.importHostedEpm',
      'backend.downloadHostedEpm',
      'createVCardQrPayload',
      'downloadHostedEpm(record.id,',
      '.json,.epm,.vcf,.vcard',
      'listWalletsAndEpms',
      'beginClaimEpm',
      'exportCore',
      'importCore',
      'Encrypted Core',
      'Public vCard QR',
      'public EPM',
    ]);

    for (const field of [
      'dn',
      'legal_name',
      'given_name',
      'family_name',
      'email',
      'telephone',
      'entity_type',
      'peer_id',
      'epm_cid',
      'public_key',
      'signing_public_key',
      'encryption_public_key',
      'multiformat_address',
    ]) {
      expect(source).toContain(field);
    }
  });

  it('renders EPM actions in peer rows and includes directory search', () => {
    const source = readUiSource('screens/PeersScreen.svelte');

    expect(source).toContain("import DirectorySearchPanel from '../components/DirectorySearchPanel.svelte'");
    expect(source).toContain('<DirectorySearchPanel');
    expect(source).toContain('<th>EPM</th>');
    expect(source).toContain('downloadHostedEpm');
    expect(source).toContain('sdn-actions-nowrap');
  });

  it('queries nodes and people in the directory search panel', () => {
    const source = readUiSource('components/DirectorySearchPanel.svelte');

    expect(source).toContain('backend.searchDirectory');
    expect(source).toContain('directoryKind ===');
    expect(source).toContain('Nodes');
    expect(source).toContain('People');
    expect(source).toContain('downloadHostedEpm');
    expect(source).toContain('Show QR');
  });

  it('does not offer plaintext Core export controls in public identity UI copy', () => {
    const sources = [
      readUiSource('components/IdentityPanel.svelte'),
      readUiSource('components/DirectorySearchPanel.svelte'),
      readUiSource('screens/NodeScreen.svelte'),
      readUiSource('screens/PeersScreen.svelte'),
    ].join('\n');

    expect(sources).not.toMatch(/plaintext\s+core/i);
    expect(sources).not.toMatch(/plain\s+text\s+core/i);
  });
});

describe('SDN identity styling guardrails', () => {
  it('uses darkest black and subtle non-blob texture tokens', () => {
    const tokens = readUiSource('styles/tokens.css');
    const appCss = readUiSource('styles/app.css');

    expect(tokens).toMatch(/--sdn-bg:\s*#000000\b/i);
    expect(appCss).toContain('repeating-linear-gradient');
    expect(appCss).toContain('repeating-radial-gradient');
    expect(appCss).not.toMatch(/\bblob\b|\borb\b/i);
  });

  it('uses liquid glass panels without pill-shaped buttons', () => {
    const appCss = readUiSource('styles/app.css');

    expect(appCss).toMatch(/\.sdn-glass\s*{[^}]*backdrop-filter:\s*blur\([^)]*\)\s*saturate\([^)]*\)/s);
    expect(appCss).toMatch(/\.sdn-button\s*{[^}]*border-radius:\s*var\(--sdn-radius-sm\)/s);
    expect(appCss).not.toMatch(/border-radius:\s*(999|1000|50%)/);
  });

  it('keeps peer actions on one line and centers select arrows', () => {
    const appCss = readUiSource('styles/app.css');

    expect(appCss).toMatch(/\.sdn-actions-nowrap\s*{[^}]*white-space:\s*nowrap/s);
    expect(appCss).toMatch(/\.sdn-select\s*{[^}]*background-position:[^;]*center/s);
  });
});

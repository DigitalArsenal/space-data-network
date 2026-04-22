import fs from 'node:fs/promises';
import path from 'node:path';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';

import { describe, expect, it } from 'vitest';

const execFileAsync = promisify(execFile);
const repoRoot = path.resolve(__dirname, '../../../..');
const uiRoot = path.join(repoRoot, 'sdn-js', 'ui', 'src', 'upstream-webui');

describe('sdn upstream webui branding sync', () => {
  it('keeps the vendored upstream branding slice in sync', async () => {
    await expect(execFileAsync('node', [
      path.join(repoRoot, 'scripts', 'sync-upstream-webui-into-sdn-js.mjs'),
      '--check',
    ], {
      cwd: repoRoot,
    })).resolves.toMatchObject({
      stderr: '',
    });
  });

  it('brands the SDN root status header and navbar through local overrides', async () => {
    const welcomeConnectedOverride = await fs.readFile(path.join(uiRoot, 'overrides', 'components', 'is-connected', 'IsConnected.js'), 'utf8');
    const aboutWebUiOverride = await fs.readFile(path.join(uiRoot, 'overrides', 'components', 'about-webui', 'AboutWebUI.js'), 'utf8');
    const aboutIpfsOverride = await fs.readFile(path.join(uiRoot, 'overrides', 'components', 'about-ipfs', 'AboutIpfs.js'), 'utf8');
    const statusOverride = await fs.readFile(path.join(uiRoot, 'overrides', 'status', 'StatusConnected.js'), 'utf8');
    const navOverride = await fs.readFile(path.join(uiRoot, 'overrides', 'navigation', 'NavBar.js'), 'utf8');
    const connectedOverride = await fs.readFile(path.join(uiRoot, 'overrides', 'components', 'connected', 'Connected.js'), 'utf8');

    expect(welcomeConnectedOverride).toContain('Connected to the Space Data Network');
    expect(aboutWebUiOverride).toContain('In this app, you can');
    expect(aboutWebUiOverride).toContain('Open the full IPFS dashboard');
    expect(aboutIpfsOverride).toContain('What is Space Data Network?');
    expect(aboutIpfsOverride).toContain('space situational awareness');
    expect(statusOverride).toContain('Connected to the Space Data Network');
    expect(navOverride).toContain('sdn-logo-mark.svg');
    expect(navOverride).not.toContain('sdn-logo-text-vert.svg');
    expect(navOverride).not.toContain('sdn-logo-text-horiz.svg');
    expect(connectedOverride).toContain('Connected to the Space Data Network');
  });
});

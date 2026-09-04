import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { existsSync, readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const repoRoot = dirname(dirname(dirname(fileURLToPath(import.meta.url))));
const script = readFileSync(join(repoRoot, 'scripts/install.sh'), 'utf8');
const powershellScriptPath = join(repoRoot, 'scripts/install.ps1');
const pagesInstallPath = join(repoRoot, 'docs/install.sh');
const pagesPowerShellInstallPath = join(repoRoot, 'docs/install.ps1');

test('install script downloads self-contained CLI release archives', () => {
  assert.match(script, /REPO="DigitalArsenal\/space-data-network"/);
  assert.match(script, /PRIMARY_BINARY_NAME="spacedatanetwork"/);
  assert.match(script, /ALIAS_BINARY_NAME="sdn"/);
  assert.match(script, /\[ "\$OS" = "windows" \] && \[ "\$ARCH" = "arm64" \]/);
  assert.match(script, /ARCHIVE_NAME="spacedatanetwork-\$\{ASSET_VERSION\}-\$\{OS\}-\$\{ARCH\}\.zip"/);
  assert.match(script, /ARCHIVE_NAME="spacedatanetwork-\$\{ASSET_VERSION\}-\$\{OS\}-\$\{ARCH\}\.tar\.gz"/);
  assert.match(script, /spacedatanetwork-checksums\.txt/);
  assert.match(script, /BUNDLE_PARENT_DIR="\$\{SDN_BUNDLE_DIR:-\$HOME\/\.spacedatanetwork\/bundles\}"/);
  assert.doesNotMatch(script, /go-space-data-network/);
});

test('install script uses user-owned Unix command links without implicit sudo', () => {
  assert.match(script, /INSTALL_DIR="\$\{SDN_INSTALL_DIR:-\$HOME\/\.spacedatanetwork\/bin\}"/);
  assert.match(script, /ln -sf "\$\{BUNDLE_ROOT\}\/bin\/\$\{PRIMARY_BINARY_NAME\}" "\$\{INSTALL_DIR\}\/\$\{PRIMARY_BINARY_NAME\}"/);
  assert.match(script, /ln -sf "\$\{BUNDLE_ROOT\}\/bin\/\$\{ALIAS_BINARY_NAME\}" "\$\{INSTALL_DIR\}\/\$\{ALIAS_BINARY_NAME\}"/);
  assert.doesNotMatch(script, /\bsudo\b/);
  assert.doesNotMatch(script, /\/usr\/local\/bin/);
});

test('install script verifies both Unix commands are available after linking', () => {
  assert.match(script, /PRIMARY_COMMAND_PATH="\$\{INSTALL_DIR\}\/\$\{PRIMARY_BINARY_NAME\}"/);
  assert.match(script, /"\$PRIMARY_COMMAND_PATH" init/);
  assert.match(script, /command -v "\$PRIMARY_BINARY_NAME"/);
  assert.match(script, /command -v "\$ALIAS_BINARY_NAME"/);
  assert.match(script, /"\$ALIAS_COMMAND_PATH" status/);
});

test('install script initializes the local node identity after Unix install', () => {
  assert.match(script, /SDN_SKIP_INIT/);
  assert.match(script, /"\$PRIMARY_COMMAND_PATH" init/);
  assert.match(script, /"\$PRIMARY_COMMAND_PATH" show-identity >\/dev\/null/);
  assert.match(script, /Run '\$PRIMARY_BINARY_NAME start' to start the node as a persistent background service/);
  assert.match(script, /Run '\$PRIMARY_BINARY_NAME daemon' for foreground\/manual mode/);
});

test('native PowerShell installer installs Windows shims without elevation', () => {
  assert.equal(existsSync(powershellScriptPath), true, 'scripts/install.ps1 must exist');

  const powershellScript = readFileSync(powershellScriptPath, 'utf8');
  assert.match(powershellScript, /\$InstallDir = Join-Path \$HOME '\.spacedatanetwork\\bin'/);
  assert.match(powershellScript, /\$BundleParentDir = Join-Path \$HOME '\.spacedatanetwork\\bundles'/);
  assert.match(powershellScript, /Invoke-RestMethod/);
  assert.match(powershellScript, /Expand-Archive/);
  assert.match(powershellScript, /Get-FileHash/);
  assert.match(powershellScript, /'ARM64'\s+\{\s+return 'amd64'\s+\}/);
  assert.match(powershellScript, /spacedatanetwork\.cmd/);
  assert.match(powershellScript, /sdn\.cmd/);
  assert.match(powershellScript, /\$PrimaryExe\s+init/);
  assert.match(powershellScript, /\$PrimaryExe\s+show-identity\s+2>\$null\s+\|\s+Out-Null/);
  assert.doesNotMatch(powershellScript, /Start-Process[\s\S]*-Verb\s+RunAs/i);
  assert.doesNotMatch(powershellScript, /\bsudo\b/i);
});

test('public installers use spacedatanetwork.org docs and no GitHub CLI', () => {
  assert.equal(existsSync(pagesInstallPath), true, 'docs/install.sh must be published by GitHub Pages');
  assert.equal(existsSync(pagesPowerShellInstallPath), true, 'docs/install.ps1 must be published by GitHub Pages');

  const pagesInstall = readFileSync(pagesInstallPath, 'utf8');
  const pagesPowerShellInstall = readFileSync(pagesPowerShellInstallPath, 'utf8');
  const readme = readFileSync(join(repoRoot, 'README.md'), 'utf8');

  assert.match(readme, /curl -fsSL https:\/\/spacedatanetwork\.org\/install\.sh \| bash/);
  assert.match(readme, /irm https:\/\/spacedatanetwork\.org\/install\.ps1 \| iex/);
  assert.doesNotMatch(readme, /digitalarsenal\.github\.io\/space-data-network/);
  assert.doesNotMatch(readme, /space-data-network\/\/install\.sh/);
  assert.match(pagesInstall, /raw\.githubusercontent\.com\/DigitalArsenal\/space-data-network\/main\/scripts\/install\.sh/);
  assert.match(pagesPowerShellInstall, /raw\.githubusercontent\.com\/DigitalArsenal\/space-data-network\/main\/scripts\/install\.ps1/);
  assert.match(pagesInstall, /curl -fsSL/);
  assert.match(pagesInstall, /wget -qO-/);
  assert.doesNotMatch(pagesInstall, /\bgh\b/);
  assert.match(pagesPowerShellInstall, /Invoke-WebRequest/);
  assert.doesNotMatch(pagesPowerShellInstall, /\bgh\b/);
});

test('README documents CLI search and EPM wizard', () => {
  const readme = readFileSync(join(repoRoot, 'README.md'), 'utf8');

  assert.match(readme, /spacedatanetwork search providers/);
  assert.match(readme, /spacedatanetwork search standards OMM --format json/);
  assert.match(readme, /spacedatanetwork identity wizard/);
  assert.match(readme, /identity export --format flatbuffer/);
});

// The repository publishes library releases (sdn-js-v*) beside the node's own
// v<semver> releases, and GitHub's releases/latest returns whichever stable
// release is newest — today sdn-js-v3.0.0, whose archive URL 404s. The
// selector must pick the newest node release: stable first, else pre-release.
const selectorSource = script.match(/select_node_release_tag\(\) \{[\s\S]*?\n\}\n/)?.[0];
const selectTag = (releases) =>
  execFileSync('sh', ['-c', `${selectorSource}\nselect_node_release_tag`], { input: JSON.stringify(releases, null, 1), encoding: 'utf8' }).trim();
const rel = (tag_name, prerelease) => ({ url: 'x', tag_name, name: tag_name, draft: false, prerelease, created_at: '2026-01-01T00:00:00Z' });

test('install script resolves the newest node release, never an sdn-js library tag', () => {
  assert.ok(selectorSource, 'select_node_release_tag is defined in scripts/install.sh');
  assert.doesNotMatch(script, /releases\/latest/);
  assert.equal(selectTag([rel('sdn-js-v3.0.0', false), rel('sdn-js-v2.9.0', false), rel('v1.0.3-beta.17', true), rel('v1.0.3-beta.16', true)]), 'v1.0.3-beta.17');
  // The newest node release wins whatever its prerelease flag: the June betas
  // were labelled stable, and a new beta must still be what installs.
  assert.equal(selectTag([rel('sdn-js-v3.0.0', false), rel('v1.0.4-beta.18', true), rel('v1.0.3-beta.17', false), rel('v1.0.3-beta.16', false)]), 'v1.0.4-beta.18');
  assert.equal(selectTag([rel('sdn-js-v3.0.0', false)]), '');
});

test('PowerShell installer resolves the newest node release the same way', () => {
  const ps = readFileSync(powershellScriptPath, 'utf8');
  assert.doesNotMatch(ps, /releases\/latest/);
  assert.match(ps, /function Select-NodeReleaseTag/);
  assert.match(ps, /tag_name -match '\^v\\d'/);
  assert.doesNotMatch(ps, /prerelease \}/);
});

import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const repoRoot = dirname(dirname(dirname(fileURLToPath(import.meta.url))));
const script = readFileSync(join(repoRoot, 'scripts/install.sh'), 'utf8');
const pagesInstallPath = join(repoRoot, 'docs/install.sh');

test('install script downloads self-contained CLI release archives', () => {
  assert.match(script, /REPO="DigitalArsenal\/space-data-network"/);
  assert.match(script, /PRIMARY_BINARY_NAME="spacedatanetwork"/);
  assert.match(script, /ALIAS_BINARY_NAME="sdn"/);
  assert.match(script, /ARCHIVE_NAME="spacedatanetwork-\$\{ASSET_VERSION\}-\$\{OS\}-\$\{ARCH\}\.zip"/);
  assert.match(script, /ARCHIVE_NAME="spacedatanetwork-\$\{ASSET_VERSION\}-\$\{OS\}-\$\{ARCH\}\.tar\.gz"/);
  assert.match(script, /spacedatanetwork-checksums\.txt/);
  assert.match(script, /BUNDLE_PARENT_DIR="\$\{SDN_BUNDLE_DIR:-\$HOME\/\.spacedatanetwork\/bundles\}"/);
  assert.doesNotMatch(script, /go-space-data-network/);
});

test('install script links both Unix commands from the extracted bundle', () => {
  assert.match(script, /ln -sf "\$\{BUNDLE_ROOT\}\/bin\/\$\{PRIMARY_BINARY_NAME\}" "\$\{INSTALL_DIR\}\/\$\{PRIMARY_BINARY_NAME\}"/);
  assert.match(script, /ln -sf "\$\{BUNDLE_ROOT\}\/bin\/\$\{ALIAS_BINARY_NAME\}" "\$\{INSTALL_DIR\}\/\$\{ALIAS_BINARY_NAME\}"/);
});

test('install script verifies both Unix commands are available after linking', () => {
  assert.match(script, /command -v "\$PRIMARY_BINARY_NAME"/);
  assert.match(script, /command -v "\$ALIAS_BINARY_NAME"/);
  assert.match(script, /"\$ALIAS_BINARY_NAME" status/);
});

test('install script initializes the local node identity after Unix install', () => {
  assert.match(script, /SDN_SKIP_INIT/);
  assert.match(script, /"\$PRIMARY_BINARY_NAME" init/);
  assert.match(script, /Run '\$PRIMARY_BINARY_NAME daemon' to start the node/);
});

test('install script leaves Windows ZIP usage portable', () => {
  assert.match(script, /\[ "\$OS" = "windows" \]/);
  assert.match(script, /Add .*\$\{BUNDLE_ROOT\}\/bin.* to your PATH/);
  assert.match(script, /\$\{BUNDLE_ROOT\}\/bin\/spacedatanetwork\.exe/);
  assert.match(script, /\$\{BUNDLE_ROOT\}\/bin\/sdn\.exe/);
});

test('public pages installer needs only curl or wget and no GitHub CLI', () => {
  assert.equal(existsSync(pagesInstallPath), true, 'docs/install.sh must be published by GitHub Pages');

  const pagesInstall = readFileSync(pagesInstallPath, 'utf8');
  const readme = readFileSync(join(repoRoot, 'README.md'), 'utf8');

  assert.match(readme, /curl -sSL https:\/\/digitalarsenal\.github\.io\/space-data-network\/install\.sh \| bash/);
  assert.doesNotMatch(readme, /space-data-network\/\/install\.sh/);
  assert.match(pagesInstall, /raw\.githubusercontent\.com\/DigitalArsenal\/space-data-network\/main\/scripts\/install\.sh/);
  assert.match(pagesInstall, /curl -fsSL/);
  assert.match(pagesInstall, /wget -qO-/);
  assert.doesNotMatch(pagesInstall, /\bgh\b/);
});

import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const repoRoot = dirname(dirname(dirname(fileURLToPath(import.meta.url))));
const script = readFileSync(join(repoRoot, 'scripts/install.sh'), 'utf8');

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

test('install script leaves Windows ZIP usage portable', () => {
  assert.match(script, /\[ "\$OS" = "windows" \]/);
  assert.match(script, /Add .*\$\{BUNDLE_ROOT\}\/bin.* to your PATH/);
  assert.match(script, /\$\{BUNDLE_ROOT\}\/bin\/spacedatanetwork\.exe/);
  assert.match(script, /\$\{BUNDLE_ROOT\}\/bin\/sdn\.exe/);
});

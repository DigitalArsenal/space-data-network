// The operator docs say what the update channel trusts and what an existing
// install must do: the demo root, the one manual step, and that the channel
// carries nothing until a release is cut with the signing key.
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { dirname, join, resolve } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..');
const DEMO_ROOT_JSON = '{"12cb25de07c0":"MCowBQYDK2VwAyEAi0cVzY4ICSRzddR7HYHKgEPXmXXFh1uvFizfMb/V5ZM="}';

for (const doc of ['docs/INSTALL.md', 'docs/sdn-signed-updater.md']) {
  test(`${doc} states the demo root, the manual step for existing installs, and that a release is required`, async () => {
    const raw = await readFile(join(repoRoot, doc), 'utf8');
    assert.ok(raw.includes(DEMO_ROOT_JSON), 'the demo root, exactly as the trust file holds it');
    const text = raw.replace(/\s+/g, ' ');
    assert.match(text, /demo (key|root)/i);
    // The SEC-001 root is named as burned, never as a root to install.
    assert.match(text, /d4a971a7e534.{0,200}SEC-001|SEC-001.{0,200}d4a971a7e534/);
    // The one manual step: reinstall, or replace the trust file and install --direct.
    assert.match(text, /re-runs? the installer/i);
    assert.match(text, /trust\/update-roots\.json/);
    assert.match(text, /update install --direct/);
    assert.match(text, /replace[^.]{0,20}not add beside/i);
    // The channel needs a release cut with the signing key.
    assert.match(text, /a release is required for the channel|the channel needs a release/i);
    assert.match(text, /signing key/);
  });
}

import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const workflow = readFileSync(resolve('.github/workflows/live-dht-cross-platform.yml'), 'utf8');

test('live DHT workflow covers Linux Docker, native macOS, and native Windows', () => {
  assert.match(workflow, /linux-docker/);
  assert.match(workflow, /macos-native/);
  assert.match(workflow, /windows-native/);
  assert.match(workflow, /ubuntu-latest/);
  assert.match(workflow, /macos-14/);
  assert.match(workflow, /windows-latest/);
});

test('live DHT workflow uses release CLI artifacts and public Kademlia DHT', () => {
  assert.match(workflow, /gh release download/);
  assert.match(workflow, /spacedatanetwork-\*-\$\{\{\s*matrix\.target_os\s*\}\}-\$\{\{\s*matrix\.target_arch\s*\}\}/);
  assert.match(workflow, /node:24-bookworm/);
  assert.match(workflow, /SDN_LIVE_DHT_EXPECT_ROLES/);
  assert.match(workflow, /300000/);
  assert.doesNotMatch(workflow, /tailscale/i);
});

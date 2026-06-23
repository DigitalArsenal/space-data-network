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

test('live DHT workflow builds current-ref CLI artifacts and uses public Kademlia DHT', () => {
  assert.match(workflow, /build-updater-wasm:/);
  assert.match(workflow, /build-cli:/);
  assert.match(workflow, /needs: build-updater-wasm/);
  assert.match(workflow, /updater-module-wasm/);
  assert.match(workflow, /packages\/sdn-updater-module\/dist\/isomorphic/);
  assert.match(workflow, /Build target SDN binary/);
  assert.match(workflow, /build-self-contained-cli\.mjs/);
  assert.match(workflow, /--hd-wallet-wasm-path "\$\{PWD\}\/sdn-js\/node_modules\/hd-wallet-wasm\/dist\/hd-wallet-wasi\.wasm"/);
  assert.match(workflow, /actions\/download-artifact@v4/);
  assert.match(workflow, /node:24-bookworm/);
  assert.match(workflow, /SDN_LIVE_DHT_EXPECT_ROLES/);
  assert.match(workflow, /300000/);
  assert.doesNotMatch(workflow, /gh release download/);
  assert.doesNotMatch(workflow, /tailscale/i);
});

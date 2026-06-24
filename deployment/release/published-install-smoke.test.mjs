import assert from 'node:assert/strict';
import test from 'node:test';

import {
  SMOKE_COMMANDS,
  buildPublishedInstallSmokePlan,
  resolvePublishedInstallSmokePlatform
} from './published-install-smoke.mjs';

test('published installer smoke uses clean user roots and public Unix one-liner', () => {
  const plan = buildPublishedInstallSmokePlan({
    platform: 'unix',
    homeDir: '/tmp/sdn-smoke-home'
  });

  assert.equal(plan.platform, 'unix');
  assert.equal(plan.install.command, 'bash');
  assert.deepEqual(plan.install.args, ['-lc', 'curl -fsSL https://spacedatanetwork.org/install.sh | bash']);
  assert.equal(plan.env.HOME, '/tmp/sdn-smoke-home');
  assert.equal(plan.env.SDN_INSTALL_DIR, '/tmp/sdn-smoke-home/.spacedatanetwork/bin');
  assert.equal(plan.env.SDN_BUNDLE_DIR, '/tmp/sdn-smoke-home/.spacedatanetwork/bundles');
  assert.equal(plan.env.SDN_SKIP_INIT, undefined);
  assert.deepEqual(plan.commands.map((command) => command.args.join(' ')), SMOKE_COMMANDS);
  assert.equal(plan.commands[0].command, '/tmp/sdn-smoke-home/.spacedatanetwork/bin/spacedatanetwork');
});

test('published installer smoke uses clean user roots and public Windows one-liner', () => {
  const plan = buildPublishedInstallSmokePlan({
    platform: 'windows',
    homeDir: String.raw`C:\sdn-smoke-home`
  });

  assert.equal(plan.platform, 'windows');
  assert.equal(plan.install.command, 'powershell.exe');
  assert.deepEqual(plan.install.args, [
    '-NoLogo',
    '-NoProfile',
    '-ExecutionPolicy',
    'Bypass',
    '-Command',
    'irm https://spacedatanetwork.org/install.ps1 | iex'
  ]);
  assert.equal(plan.env.USERPROFILE, String.raw`C:\sdn-smoke-home`);
  assert.equal(plan.env.SDN_INSTALL_DIR, String.raw`C:\sdn-smoke-home\.spacedatanetwork\bin`);
  assert.equal(plan.env.SDN_BUNDLE_DIR, String.raw`C:\sdn-smoke-home\.spacedatanetwork\bundles`);
  assert.deepEqual(plan.commands.map((command) => command.args.join(' ')), SMOKE_COMMANDS);
  assert.equal(plan.commands[0].command, String.raw`C:\sdn-smoke-home\.spacedatanetwork\bin\spacedatanetwork.cmd`);
});

test('published installer smoke resolves host platform names', () => {
  assert.equal(resolvePublishedInstallSmokePlatform('linux'), 'unix');
  assert.equal(resolvePublishedInstallSmokePlatform('darwin'), 'unix');
  assert.equal(resolvePublishedInstallSmokePlatform('win32'), 'windows');
  assert.throws(() => resolvePublishedInstallSmokePlatform('freebsd'), /Unsupported published installer smoke platform/);
});

import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { test } from 'node:test';
import { publicRecordSchemas, renderNginx } from './render-nginx.mjs';

test('route selection follows the node public policy and remains opt-in', () => {
  assert.ok(publicRecordSchemas().includes('omm'));
  assert.ok(publicRecordSchemas().includes('rfb'));
  assert.ok(!publicRecordSchemas().includes('kmf'));
  const config = renderNginx({ schemas: ['cat', 'omm', 'cat'] });
  assert.equal(config.match(/location = \/api\/v1\/data\/cat\/bulk/g).length, 1);
  assert.ok(!config.includes('location = /api/v1/data/rfb/bulk'));
  for (const schemas of [[], ['kmf'], ['zzz'], ['OMM'], ['omm/bulk'], ['omm; return 200;']]) {
    assert.throws(() => renderNginx({ schemas }), /public-schema policy/);
  }
});

test('config parameters refuse remote origins, secrets, directive injection and unbounded sizes', () => {
  for (const upstream of ['https://127.0.0.1:7173', 'http://user:password@127.0.0.1:7173',
    'http://example.com:80', 'http://127.0.0.1:0', 'http://127.0.0.1:99999',
    'http://127.0.0.1:7173/path', 'http://127.0.0.1:7173; return 200;', 'http://127.0.0.1:7173\n']) {
    assert.throws(() => renderNginx({ upstream }));
  }
  for (const listen of ['8088', 'example.com:8088', '127.0.0.1:0', '127.0.0.1:65536', '127.0.0.1:8088\n']) {
    assert.throws(() => renderNginx({ listen }));
  }
  for (const cacheMiB of [0, 1025, 1.5, NaN, Infinity, '128m']) assert.throws(() => renderNginx({ cacheMiB }));
  for (const ttlSeconds of [0, 61, 1.5, NaN]) assert.throws(() => renderNginx({ ttlSeconds }));
  assert.ok(renderNginx({ upstream: 'http://[::1]:7173', listen: '[::1]:8088' }).includes('listen [::1]:8088;'));
});

test('CLI emits no partial configuration on bad input', () => {
  const command = new URL('./render-nginx.mjs', import.meta.url);
  for (const args of [['--schemas', 'kmf'], ['--schemas'], ['--unknown', '1'], ['--schemas', 'omm', '--schemas', 'cat']]) {
    const result = spawnSync(process.execPath, [command.pathname, ...args], { encoding: 'utf8' });
    assert.equal(result.status, 1);
    assert.equal(result.stdout, '');
  }
});

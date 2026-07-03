import assert from 'node:assert/strict';
import { readFile, access } from 'node:fs/promises';
import { test } from 'node:test';

// Single-writer ingest topology (loop C.6b): the FlatSQL v2 store admits one
// writer process, so the CelesTrak host runs ingest INSIDE the daemon
// (config.yaml `ingest.enabled`) and the separate ingest unit is gone.

test('celestrak host has no separate ingest unit (single-writer topology)', async () => {
  await assert.rejects(
    access(new URL('./spacedatanetwork-ingest.service', import.meta.url)),
    'the separate ingest unit must not exist — in-daemon ingest replaced it',
  );
});

test('celestrak daemon config enables in-daemon ingest', async () => {
  const config = await readFile(new URL('./config.yaml', import.meta.url), 'utf8');

  assert.match(config, /^ingest:$/m);
  assert.match(config, /^\s+enabled: true$/m);
  assert.match(config, /^\s+celestrak_interval: 3h$/m);
  assert.match(config, /dataset_publish_url: http:\/\/127\.0\.0\.1:5001\/api\/v1\/admin\/dataset-updates\/publish/);
  // Credential VALUES must never live in the checked-in config (the comment
  // may name the env vars operators set in private drop-ins).
  assert.doesNotMatch(config, /(identity|username|password)\s*[:=]/i);
});

test('celestrak installer removes the legacy ingest unit and does not enable it', async () => {
  const installer = await readFile(new URL('./install-host.sh', import.meta.url), 'utf8');

  assert.match(installer, /systemctl disable --now spacedatanetwork-ingest\.service/);
  assert.match(installer, /rm -f \/etc\/systemd\/system\/spacedatanetwork-ingest\.service/);
  assert.doesNotMatch(installer, /install .*spacedatanetwork-ingest\.service/);
  assert.doesNotMatch(installer, /systemctl enable .*spacedatanetwork-ingest/);
});

test('standalone ingest unit example is clearly marked and still WasmEdge-capable', async () => {
  const unit = await readFile(
    new URL('../../sdn-server/deploy/spacedatanetwork-ingest.service.standalone-example', import.meta.url),
    'utf8',
  );

  assert.match(unit, /STANDALONE EXAMPLE — DO NOT ENABLE NEXT TO A RUNNING DAEMON/);
  assert.match(unit, /SINGLE-WRITER/);
  assert.match(unit, /Environment=WASMEDGE_DIR=\/opt\/spacedatanetwork\/\.wasmedge/);
  assert.match(unit, /Environment=LD_LIBRARY_PATH=\/opt\/spacedatanetwork\/\.wasmedge\/lib/);
});

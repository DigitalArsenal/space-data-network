import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { test } from 'node:test';

test('CelesTrak ingest unit can load the bundled WasmEdge runtime under systemd', async () => {
  const unit = await readFile(new URL('./spacedatanetwork-ingest.service', import.meta.url), 'utf8');

  assert.match(unit, /Environment=WASMEDGE_DIR=\/opt\/spacedatanetwork\/\.wasmedge/);
  assert.match(unit, /Environment=LD_LIBRARY_PATH=\/opt\/spacedatanetwork\/\.wasmedge\/lib/);
});

test('generic ingest unit can load the bundled WasmEdge runtime under systemd', async () => {
  const unit = await readFile(new URL('../../sdn-server/deploy/spacedatanetwork-ingest.service', import.meta.url), 'utf8');

  assert.match(unit, /Environment=WASMEDGE_DIR=\/opt\/spacedatanetwork\/\.wasmedge/);
  assert.match(unit, /Environment=LD_LIBRARY_PATH=\/opt\/spacedatanetwork\/\.wasmedge\/lib/);
});

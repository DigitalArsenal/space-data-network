import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdtemp, readFile, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { BUNDLE_SECTION_NAME, buildCarrier, extractBundleBytes } from './build-update-carrier.mjs';

const cliPath = fileURLToPath(new URL('./build-update-carrier.mjs', import.meta.url));

test('buildCarrier emits a minimal wasm module with exact header bytes', () => {
  const bundle = Buffer.from('bundle-bytes');
  const carrier = buildCarrier(bundle);

  // WASM magic and version.
  assert.deepEqual([...carrier.subarray(0, 4)], [0x00, 0x61, 0x73, 0x6d]);
  assert.deepEqual([...carrier.subarray(4, 8)], [0x01, 0x00, 0x00, 0x00]);

  // One custom section (id 0) whose payload is name length + name + bundle.
  const name = Buffer.from(BUNDLE_SECTION_NAME, 'utf8');
  assert.equal(name.length, 20);
  const payloadLength = 1 + name.length + bundle.length;
  assert.ok(payloadLength < 0x80, 'fixture payload must fit one LEB128 byte');
  assert.equal(carrier[8], 0x00);
  assert.equal(carrier[9], payloadLength);
  assert.equal(carrier[10], name.length);
  assert.equal(carrier.subarray(11, 11 + name.length).toString('utf8'), BUNDLE_SECTION_NAME);
  assert.deepEqual(carrier.subarray(11 + name.length), bundle);
  assert.equal(carrier.length, 10 + payloadLength);
});

test('buildCarrier/extractBundleBytes round-trips bundle bytes', () => {
  const bundle = Buffer.alloc(300, 0xa5);
  const carrier = buildCarrier(bundle);
  // 300-byte bundle forces a multi-byte LEB128 section size.
  assert.ok(carrier[9] & 0x80, 'section size should use multi-byte LEB128');
  assert.deepEqual(extractBundleBytes(carrier), bundle);

  const empty = buildCarrier(Buffer.alloc(0));
  assert.deepEqual(extractBundleBytes(empty), Buffer.alloc(0));
});

test('extractBundleBytes walks past unrelated sections', () => {
  const bundle = Buffer.from('payload');
  const otherName = Buffer.from('other.section');
  const otherPayload = Buffer.concat([
    Buffer.from([otherName.length]),
    otherName,
    Buffer.from('junk'),
  ]);
  const carrier = buildCarrier(bundle);
  const withExtra = Buffer.concat([
    carrier.subarray(0, 8),
    Buffer.from([0x00, otherPayload.length]),
    otherPayload,
    carrier.subarray(8),
  ]);
  assert.deepEqual(extractBundleBytes(withExtra), bundle);
});

test('extractBundleBytes rejects non-wasm and truncated carriers', () => {
  assert.throws(() => extractBundleBytes(Buffer.from('not wasm at all')), /not a wasm module/);
  const carrier = buildCarrier(Buffer.from('bundle'));
  assert.throws(() => extractBundleBytes(carrier.subarray(0, carrier.length - 2)), /truncated section/);
  const noSection = carrier.subarray(0, 8);
  assert.throws(() => extractBundleBytes(noSection), /does not contain an SDN bundle section/);
});

test('CLI builds a carrier file from a bundle archive', async () => {
  const root = await mkdtemp(join(tmpdir(), 'sdn-update-carrier-'));
  const bundlePath = join(root, 'bundle.tar.gz');
  const outPath = join(root, 'update.wasm');
  const bundle = Buffer.from('cli bundle bytes');
  await writeFile(bundlePath, bundle);

  await runNode([cliPath, '--bundle', bundlePath, '--out', outPath]);
  const carrier = await readFile(outPath);
  assert.deepEqual(carrier, buildCarrier(bundle));
  assert.deepEqual(extractBundleBytes(carrier), bundle);
});

function runNode(args, options) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, args, { ...options, stdio: ['ignore', 'pipe', 'pipe'] });
    let stdout = '';
    let stderr = '';
    child.stdout.on('data', (chunk) => {
      stdout += chunk;
    });
    child.stderr.on('data', (chunk) => {
      stderr += chunk;
    });
    child.on('error', reject);
    child.on('close', (code) => {
      if (code === 0) {
        resolve(stdout);
        return;
      }
      reject(new Error(`node exited ${code}: ${stderr.trim()}`));
    });
  });
}

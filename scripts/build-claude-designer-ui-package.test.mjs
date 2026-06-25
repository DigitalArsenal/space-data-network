import { after, before, describe, it } from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, mkdtempSync, readFileSync, rmSync, statSync } from 'node:fs';
import { readdir } from 'node:fs/promises';
import { join, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';
import { tmpdir } from 'node:os';

const repoRoot = resolve(new URL('..', import.meta.url).pathname);
const scriptPath = join(repoRoot, 'scripts/build-claude-designer-ui-package.mjs');
const tmpRoot = mkdtempSync(join(tmpdir(), 'sdn-designer-package-test-'));
const outputDir = join(tmpRoot, 'artifacts/design');
const packageDir = join(outputDir, 'claude-designer-ui-package');
const zipPath = join(outputDir, 'claude-designer-ui-package.zip');

function runGenerator() {
  return spawnSync(process.execPath, [
    scriptPath,
    '--output-dir',
    outputDir
  ], {
    cwd: repoRoot,
    encoding: 'utf8'
  });
}

function readPackageFile(relativePath) {
  return readFileSync(join(packageDir, relativePath), 'utf8');
}

async function listFiles(root, prefix = '') {
  const entries = await readdir(join(root, prefix), { withFileTypes: true });
  const files = [];
  for (const entry of entries) {
    const relative = prefix ? `${prefix}/${entry.name}` : entry.name;
    if (entry.isDirectory()) {
      files.push(...await listFiles(root, relative));
    } else {
      files.push(relative);
    }
  }
  return files.sort();
}

describe('Claude Designer UI package generator', () => {
  before(() => {
    rmSync(outputDir, { recursive: true, force: true });
  });

  after(() => {
    rmSync(tmpRoot, { recursive: true, force: true });
  });

  it('builds a self-contained handoff package with screenshots and ZIP archive', async () => {
    const result = runGenerator();
    assert.equal(result.status, 0, `${result.stdout}\n${result.stderr}`);

    const requiredFiles = [
      'CLAUDE_DESIGNER_BRIEF.md',
      'SCREEN_INVENTORY.md',
      'SOURCE_MAP.md',
      'DESIGN_CONSTRAINTS.md',
      'IMPLEMENTATION_NOTES.md',
      'prototype/index.html',
      'prototype/styles.css',
      'prototype/app.js',
      'prototype/data/fixtures.json',
      'screenshots/node.png',
      'screenshots/peers.png',
      'screenshots/data.png',
      'screenshots/channels.png',
      'screenshots/conjunction.png'
    ];

    for (const file of requiredFiles) {
      const absolute = join(packageDir, file);
      assert.equal(existsSync(absolute), true, `${file} should exist`);
      assert.ok(statSync(absolute).size > 0, `${file} should be non-empty`);
    }

    assert.equal(existsSync(zipPath), true, 'ZIP archive should exist');
    assert.ok(statSync(zipPath).size > 10_000, 'ZIP archive should contain prototype and screenshots');

    const indexHtml = readPackageFile('prototype/index.html');
    assert.match(indexHtml, /Space Data Network/);
    assert.match(indexHtml, /prototype\/data\/fixtures\.json/);

    const fixtures = JSON.parse(readPackageFile('prototype/data/fixtures.json'));
    const spaceAwarePeer = fixtures.peers.find((peer) => peer.name === 'SpaceAware.io');
    const celesTrakPeer = fixtures.peers.find((peer) => peer.name === 'CelesTrak Provider');
    assert.equal(spaceAwarePeer?.id, '16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45');
    assert.equal(celesTrakPeer?.id, '16Uiu2HAm9oK2jAeVC2RMESFcYfq7BKGp2K2CCDxzoKhB5s9vpbj3');
    assert.deepEqual(fixtures.standards.map((standard) => standard.id), ['CAT', 'EPM', 'MPE', 'OMM', 'PNM', 'SPW']);

    const files = await listFiles(packageDir);
    assert.equal(files.some((file) => file.includes('node_modules')), false);
    assert.equal(files.some((file) => file.includes('.git')), false);

    const combinedText = files
      .filter((file) => /\.(md|html|css|js|json)$/.test(file))
      .map((file) => readPackageFile(file))
      .join('\n');
    assert.doesNotMatch(combinedText, /mnemonic|xpriv|private[_ -]?key|BEGIN [A-Z ]*PRIVATE KEY/i);
    assert.doesNotMatch(combinedText, /(?:"(?:token|secret|password)"\s*:|\b(?:token|secret|password)\s*[=:])/i);
    assert.doesNotMatch(combinedText, /\/Users\/tj(?:\/|$)/i);
  });
});

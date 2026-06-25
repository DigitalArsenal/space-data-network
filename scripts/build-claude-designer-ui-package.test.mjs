import { after, before, describe, it } from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, symlinkSync } from 'node:fs';
import { readdir } from 'node:fs/promises';
import { join, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';
import { tmpdir } from 'node:os';
import { fileURLToPath, pathToFileURL } from 'node:url';

const repoRoot = resolve(fileURLToPath(new URL('..', import.meta.url)));
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
    encoding: 'utf8',
    timeout: 120000
  });
}

async function loadGeneratorModule() {
  return import(`${pathToFileURL(scriptPath).href}?t=${Date.now()}`);
}

function readPackageFile(relativePath) {
  return readFileSync(join(packageDir, relativePath), 'utf8');
}

function lines(text) {
  return text.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
}

function listZipEntries() {
  const zipinfo = spawnSync('zipinfo', ['-1', zipPath], { encoding: 'utf8' });
  if (zipinfo.status === 0) return lines(zipinfo.stdout);
  const unzip = spawnSync('unzip', ['-Z1', zipPath], { encoding: 'utf8' });
  assert.equal(unzip.status, 0, `${zipinfo.stdout}\n${zipinfo.stderr}\n${unzip.stdout}\n${unzip.stderr}`);
  return lines(unzip.stdout);
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
    const zipEntries = listZipEntries();
    const expectedZipEntries = requiredFiles.map((file) => `claude-designer-ui-package/${file}`).sort();
    const actualZipEntries = zipEntries.filter((entry) => !entry.endsWith('/')).sort();
    assert.deepEqual(actualZipEntries, expectedZipEntries);

    const indexHtml = readPackageFile('prototype/index.html');
    const appJs = readPackageFile('prototype/app.js');
    assert.match(indexHtml, /Space Data Network/);
    assert.match(indexHtml, /\.\/app\.js/);
    assert.match(appJs, /\.\/data\/fixtures\.json/);

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
    assert.doesNotMatch(combinedText, /(?:\/Users\/[^/\s]+(?:\/|$)|\/home\/[^/\s]+(?:\/|$)|[A-Z]:\\Users\\[^\\\s]+(?:\\|$))/i);
  });

  it('rejects output paths that overlap the source template or tracked source directories', async () => {
    const { assertSafeOutputPaths } = await loadGeneratorModule();
    const fakeRepoRoot = join(tmpRoot, 'fake-repo');
    const fakeTemplateDir = join(fakeRepoRoot, 'design/claude-designer-ui-package');
    const safeOutputDir = join(tmpRoot, 'safe-artifacts/design');

    assert.doesNotThrow(() => assertSafeOutputPaths({
      repoRoot: fakeRepoRoot,
      templateDir: fakeTemplateDir,
      outputDir: safeOutputDir,
      packageDir: join(safeOutputDir, 'claude-designer-ui-package')
    }));

    assert.throws(() => assertSafeOutputPaths({
      repoRoot: fakeRepoRoot,
      templateDir: fakeTemplateDir,
      outputDir: join(fakeRepoRoot, 'design'),
      packageDir: fakeTemplateDir
    }), /overlap/i);

    assert.throws(() => assertSafeOutputPaths({
      repoRoot: fakeRepoRoot,
      templateDir: fakeTemplateDir,
      outputDir: join(fakeTemplateDir, 'generated'),
      packageDir: join(fakeTemplateDir, 'generated/claude-designer-ui-package')
    }), /overlap/i);

    assert.throws(() => assertSafeOutputPaths({
      repoRoot: fakeRepoRoot,
      templateDir: fakeTemplateDir,
      outputDir: join(fakeRepoRoot, 'docs/generated'),
      packageDir: join(fakeRepoRoot, 'docs/generated/claude-designer-ui-package')
    }), /tracked source/i);

    const symlinkRepoRoot = join(tmpRoot, 'symlink-repo');
    const symlinkTemplateDir = join(symlinkRepoRoot, 'design/claude-designer-ui-package');
    const symlinkArtifactsDir = join(symlinkRepoRoot, 'artifacts');
    mkdirSync(symlinkTemplateDir, { recursive: true });
    mkdirSync(symlinkArtifactsDir, { recursive: true });
    symlinkSync(join(symlinkRepoRoot, 'design'), join(symlinkArtifactsDir, 'design-link'), 'dir');

    assert.throws(() => assertSafeOutputPaths({
      repoRoot: symlinkRepoRoot,
      templateDir: symlinkTemplateDir,
      outputDir: join(symlinkArtifactsDir, 'design-link'),
      packageDir: join(symlinkArtifactsDir, 'design-link/claude-designer-ui-package')
    }), /overlap|tracked source/i);
    assert.equal(existsSync(symlinkTemplateDir), true, 'source template should not be deleted by path validation');

    const artifactsSymlinkRepoRoot = join(tmpRoot, 'artifacts-symlink-repo');
    const artifactsSymlinkTemplateDir = join(artifactsSymlinkRepoRoot, 'design/claude-designer-ui-package');
    mkdirSync(artifactsSymlinkTemplateDir, { recursive: true });
    symlinkSync(join(artifactsSymlinkRepoRoot, 'design'), join(artifactsSymlinkRepoRoot, 'artifacts'), 'dir');

    assert.throws(() => assertSafeOutputPaths({
      repoRoot: artifactsSymlinkRepoRoot,
      templateDir: artifactsSymlinkTemplateDir,
      outputDir: join(artifactsSymlinkRepoRoot, 'artifacts/design'),
      packageDir: join(artifactsSymlinkRepoRoot, 'artifacts/design/claude-designer-ui-package')
    }), /overlap|tracked source/i);
    assert.equal(existsSync(artifactsSymlinkTemplateDir), true, 'source template should survive artifacts symlink validation');
  });

  it('flags common credential-shaped text before packaging', async () => {
    const { findForbiddenPackageText } = await loadGeneratorModule();
    const forbiddenSamples = [
      'session_token=abc123',
      '"apiKey": "abc123"',
      'access_key: abc123',
      'secretKey = abc123',
      'Authorization: Bearer abc123456789',
      'credentials: abc123'
    ];

    for (const sample of forbiddenSamples) {
      assert.notEqual(findForbiddenPackageText(sample), null, sample);
    }

    assert.equal(findForbiddenPackageText('CSS tokens and secret-looking examples are discussed without values.'), null);
  });
});

#!/usr/bin/env node

import { spawnSync } from 'node:child_process';
import { cpSync, existsSync, mkdirSync, readFileSync, readdirSync, realpathSync, rmSync, statSync } from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, isAbsolute, join, relative, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(__dirname, '..');
const templateDir = join(repoRoot, 'design/claude-designer-ui-package');
const args = process.argv.slice(2);
const outputDir = resolve(readOption('--output-dir') ?? join(repoRoot, 'artifacts/design'));
const packageName = 'claude-designer-ui-package';
const packageDir = join(outputDir, packageName);
const zipPath = join(outputDir, `${packageName}.zip`);
const routes = ['node', 'peers', 'data', 'channels', 'conjunction'];

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
  ...routes.map((route) => `screenshots/${route}.png`)
];

const credentialKeyPattern = /(?:"(?:token|secret|password|api[_-]?key|access[_-]?key|secret[_-]?key|session[_-]?token|credential|credentials|authorization)"\s*:|\b(?:token|secret|password|api[_-]?key|access[_-]?key|secret[_-]?key|session[_-]?token|credential|credentials|authorization)\b\s*[=:])/i;
const forbiddenTextPatterns = [
  { name: 'private key or mnemonic text', pattern: /mnemonic|xpriv|private[_ -]?key|BEGIN [A-Z ]*PRIVATE KEY/i },
  { name: 'credential assignment', pattern: credentialKeyPattern },
  { name: 'authorization bearer value', pattern: /\bAuthorization\s*:\s*Bearer\s+[A-Za-z0-9._~+/=-]{8,}/i },
  { name: 'user-home absolute path', pattern: /(?:\/Users\/[^/\s]+(?:\/|$)|\/home\/[^/\s]+(?:\/|$)|[A-Z]:\\Users\\[^\\\s]+(?:\\|$))/i }
];

function readOption(flag) {
  const index = args.indexOf(flag);
  if (index === -1) return null;
  const value = args[index + 1];
  if (!value || value.startsWith('--')) {
    throw new Error(`${flag} requires a value`);
  }
  return value;
}

function log(message) {
  console.log(`[designer-package] ${message}`);
}

function isSameOrInside(parentPath, candidatePath) {
  const normalizedParent = resolve(parentPath);
  const normalizedCandidate = resolve(candidatePath);
  const relativePath = relative(normalizedParent, normalizedCandidate);
  return relativePath === '' || (!relativePath.startsWith('..') && !isAbsolute(relativePath));
}

function pathsOverlap(firstPath, secondPath) {
  return isSameOrInside(firstPath, secondPath) || isSameOrInside(secondPath, firstPath);
}

function resolveExistingPrefix(path) {
  const targetPath = resolve(path);
  let existingPath = targetPath;
  const missingSegments = [];

  while (!existsSync(existingPath)) {
    const parent = dirname(existingPath);
    if (parent === existingPath) {
      return targetPath;
    }
    missingSegments.unshift(relative(parent, existingPath));
    existingPath = parent;
  }

  return resolve(realpathSync.native(existingPath), ...missingSegments);
}

export function assertSafeOutputPaths(paths) {
  const normalizedRepoRoot = resolveExistingPrefix(paths.repoRoot);
  const normalizedTemplateDir = resolveExistingPrefix(paths.templateDir);
  const normalizedOutputDir = resolveExistingPrefix(paths.outputDir);
  const normalizedPackageDir = resolveExistingPrefix(paths.packageDir);
  const artifactsDir = resolveExistingPrefix(join(paths.repoRoot, 'artifacts'));

  if (pathsOverlap(normalizedTemplateDir, artifactsDir)) {
    throw new Error(`artifacts directory overlaps the source template: ${artifactsDir}`);
  }

  if (pathsOverlap(normalizedTemplateDir, normalizedOutputDir) || pathsOverlap(normalizedTemplateDir, normalizedPackageDir)) {
    throw new Error(`output path overlaps the source template: ${normalizedPackageDir}`);
  }

  if (isSameOrInside(normalizedRepoRoot, normalizedOutputDir) && !isSameOrInside(artifactsDir, normalizedOutputDir)) {
    throw new Error(`refusing to write Designer package into tracked source directory: ${normalizedOutputDir}`);
  }
}

export function findForbiddenPackageText(text) {
  for (const { name, pattern } of forbiddenTextPatterns) {
    if (pattern.test(text)) return name;
  }
  return null;
}

function copyTemplate() {
  if (!existsSync(templateDir)) {
    throw new Error(`missing template directory: ${templateDir}`);
  }

  assertSafeOutputPaths({ repoRoot, templateDir, outputDir, packageDir });
  rmSync(packageDir, { recursive: true, force: true });
  rmSync(zipPath, { force: true });
  mkdirSync(outputDir, { recursive: true });
  cpSync(templateDir, packageDir, {
    recursive: true,
    filter: (source) => {
      const basename = source.split(/[\\/]/).pop();
      return basename !== '.DS_Store';
    }
  });
}

async function loadPlaywright() {
  const requireFromSdnJs = createRequire(join(repoRoot, 'sdn-js/package.json'));
  return requireFromSdnJs('playwright');
}

async function captureScreenshots() {
  const { chromium } = await loadPlaywright();
  const screenshotDir = join(packageDir, 'screenshots');
  mkdirSync(screenshotDir, { recursive: true });

  const browser = await chromium.launch();
  try {
    const page = await browser.newPage({
      viewport: { width: 1440, height: 960 },
      deviceScaleFactor: 1
    });
    const prototypeUrl = pathToFileURL(join(packageDir, 'prototype/index.html')).href;

    for (const route of routes) {
      await page.goto(`${prototypeUrl}#/${route}`);
      await page.waitForSelector(`[data-screen="${route}"]`, { timeout: 10_000 });
      await page.screenshot({
        path: join(screenshotDir, `${route}.png`),
        fullPage: true
      });
    }
  } finally {
    await browser.close();
  }
}

function listFiles(root, prefix = '') {
  const entries = readdirSync(join(root, prefix), { withFileTypes: true });
  const files = [];

  for (const entry of entries) {
    const relativePath = prefix ? `${prefix}/${entry.name}` : entry.name;
    if (entry.isDirectory()) {
      files.push(...listFiles(root, relativePath));
    } else {
      files.push(relativePath);
    }
  }

  return files.sort();
}

function validatePackage() {
  const files = listFiles(packageDir);
  const expectedFiles = [...requiredFiles].sort();
  if (JSON.stringify(files) !== JSON.stringify(expectedFiles)) {
    const missing = expectedFiles.filter((file) => !files.includes(file));
    const extra = files.filter((file) => !expectedFiles.includes(file));
    throw new Error(`unexpected package contents\nmissing: ${missing.join(', ') || 'none'}\nextra: ${extra.join(', ') || 'none'}`);
  }

  for (const file of requiredFiles) {
    const absolute = join(packageDir, file);
    if (!existsSync(absolute)) {
      throw new Error(`required package file missing: ${file}`);
    }
    if (statSync(absolute).size === 0) {
      throw new Error(`required package file is empty: ${file}`);
    }
  }

  if (files.some((file) => file.includes('node_modules') || file.includes('.git'))) {
    throw new Error('package must not include node_modules or .git content');
  }

  const combinedText = files
    .filter((file) => /\.(md|html|css|js|json)$/.test(file))
    .map((file) => readFileSync(join(packageDir, file), 'utf8'))
    .join('\n');

  const forbiddenMatch = findForbiddenPackageText(combinedText);
  if (forbiddenMatch) {
    throw new Error(`package contains forbidden content: ${forbiddenMatch}`);
  }
}

function createZip() {
  rmSync(zipPath, { force: true });
  const result = spawnSync('zip', ['-qr', zipPath, packageName], {
    cwd: outputDir,
    encoding: 'utf8'
  });

  if (result.status !== 0) {
    throw new Error(`zip failed:\n${result.stdout}\n${result.stderr}`);
  }
}

async function main() {
  log(`copying template to ${relative(repoRoot, packageDir)}`);
  copyTemplate();
  log('capturing screenshots');
  await captureScreenshots();
  log('scanning package');
  validatePackage();
  log(`writing ${relative(repoRoot, zipPath)}`);
  createZip();
  log('done');
}

if (process.argv[1] && pathToFileURL(resolve(process.argv[1])).href === import.meta.url) {
  main().catch((error) => {
    console.error(error instanceof Error ? error.stack : error);
    process.exit(1);
  });
}

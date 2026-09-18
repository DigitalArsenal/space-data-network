#!/usr/bin/env node
/**
 * A test suite that nothing runs is a comment.
 *
 * On 2026-09-18 a sweep found 35 of the 43 root-level *.test.mjs suites were
 * named by no npm script, no workflow and not by ci-local.sh — including the
 * ones covering what a release CONTAINS, how it INSTALLS, how the update
 * manifest is SIGNED, and how the public host route is rendered. 29 of them
 * passed; they had simply never been run. Two of the bugs fixed that same day
 * lived squarely under suites in that list.
 *
 * So the gate now runs them, and this guard keeps the class from coming back:
 * every root-level *.test.mjs must be named by something that runs it, or
 * appear in QUARANTINE below with the reason it cannot.
 *
 * sdn-js/ is deliberately out of scope — vitest discovers those by glob, so
 * they cannot be orphaned the same way.
 */
import { readFileSync, readdirSync, existsSync, statSync } from 'node:fs';
import { dirname, join, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const SEARCH_ROOTS = ['scripts', 'deployment', 'tests'];

/**
 * Suites that are RED and therefore not wired in. Each entry states why, so
 * the list reads as work rather than as permission. Fixing one means deleting
 * its line here and adding it to ci-local.sh.
 */
export const QUARANTINE = new Map([
  ['deployment/release/docs-parity.test.mjs',
    'asserts install phrases in docs/docs.html that have since changed'],
  ['deployment/release/sdn-parity-contract.test.mjs',
    'references desktop/test/unit/static-http-server-identity.spec.js, which no longer exists'],
  ['deployment/release/oss-preflight.test.mjs',
    'its temp-repo fixture never copies scripts/check-sdn-js-dependency-layering.mjs'],
  ['deployment/release/docs-network-ecosystem.test.mjs',
    'expects an id="network-ecosystem" section that has moved'],
  ['scripts/build-claude-designer-ui-package.test.mjs',
    'the template copy step fails against a temp directory'],
]);

export function findSuites(root = ROOT) {
  const found = [];
  const walk = (dir) => {
    if (!existsSync(dir) || !statSync(dir).isDirectory()) return;
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      if (entry.name === 'node_modules' || entry.name.startsWith('.')) continue;
      const full = join(dir, entry.name);
      if (entry.isDirectory()) walk(full);
      else if (entry.name.endsWith('.test.mjs')) found.push(relative(root, full));
    }
  };
  for (const dir of SEARCH_ROOTS) walk(join(root, dir));
  return found.sort();
}

/** Every file whose text could name a suite that gets run. */
export function runnerText(root = ROOT) {
  const parts = [];
  for (const rel of ['package.json', 'scripts/ci-local.sh']) {
    const path = join(root, rel);
    if (existsSync(path)) parts.push(readFileSync(path, 'utf8'));
  }
  const workflows = join(root, '.github/workflows');
  if (existsSync(workflows)) {
    for (const name of readdirSync(workflows)) {
      if (/\.ya?ml$/.test(name)) parts.push(readFileSync(join(workflows, name), 'utf8'));
    }
  }
  return parts.join('\n');
}

export function orphans(suites, text, quarantine = QUARANTINE) {
  return suites.filter((suite) => !quarantine.has(suite) && !text.includes(suite.split('/').pop()));
}

export function staleQuarantine(suites, quarantine = QUARANTINE) {
  return [...quarantine.keys()].filter((suite) => !suites.includes(suite));
}

function main() {
  const suites = findSuites();
  const text = runnerText();
  const unrun = orphans(suites, text);
  const stale = staleQuarantine(suites);

  if (stale.length > 0) {
    console.error('[orphan-suites] quarantine names files that no longer exist:');
    for (const suite of stale) console.error(`    - ${suite}`);
  }
  if (unrun.length > 0) {
    console.error('[orphan-suites] these suites are run by nothing:');
    for (const suite of unrun) console.error(`    - ${suite}`);
    console.error('Add them to scripts/ci-local.sh, or quarantine them with the reason they cannot run.');
  }
  if (unrun.length > 0 || stale.length > 0) process.exit(1);

  console.log(
    `[orphan-suites] OK: ${suites.length - QUARANTINE.size} of ${suites.length} suites are wired in, ` +
      `${QUARANTINE.size} quarantined with a stated reason.`,
  );
}

if (process.argv[1] && import.meta.url.endsWith(process.argv[1].split('/').pop())) main();

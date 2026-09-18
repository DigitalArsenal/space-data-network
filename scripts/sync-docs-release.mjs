#!/usr/bin/env node
/**
 * sync-docs-release.mjs
 *
 * docs/ IS the published site (GitHub Pages serves main:/docs at
 * spacedatanetwork.org), and it carried hardcoded download URLs pinned to
 * v1.0.5-beta.1 while the project shipped sixty-odd releases past it. Every
 * "Download" button on the front page pointed at an artifact from a different
 * build, and nothing failed — the links resolve, they just hand out an old
 * binary, which is worse than a broken link because nobody reports it.
 *
 * This rewrites every release literal in the site to one version, and in
 * --check mode fails when they disagree with the newest published release. The
 * generated-and-checked-constant pattern: a literal that is verified cannot go
 * stale silently.
 *
 *   node scripts/sync-docs-release.mjs             # sync to the latest release
 *   node scripts/sync-docs-release.mjs --tag v1.2.3
 *   node scripts/sync-docs-release.mjs --check     # fail if out of date
 *
 * The install SCRIPTS are deliberately untouched: install.sh and install.ps1
 * resolve the newest release at run time, so they were never stale and must not
 * be pinned to a literal here.
 */
import { readFileSync, writeFileSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const FILES = ['docs/index.html', 'docs/onboarding.html', 'docs/INSTALL.md'];

// Matches both spellings the site uses: the tag (v1.0.5-beta.1) and the bare
// artifact version (1.0.5-beta.1), in URLs, filenames and prose.
// Three spellings appear in the site and they are NOT interchangeable:
//   v1.0.5-beta.68   the git tag, in /releases/download/<tag>/ and in prose
//   1.0.5-beta.68    most artifact filenames
//   1.0.5.beta.68    the CONTAINER artifacts, which use dots
// The container link was written with dashes and had 404'd since the page was
// authored, so this matches all three and puts each back in its own spelling.
const RELEASE_LITERAL = /v?\d+\.\d+\.\d+[-.]beta[-.]\d+/g;

// `gh` on a GitHub Actions runner is installed but UNAUTHENTICATED unless the
// step passes GH_TOKEN, and it then exits 4 with a hint on stderr. Left raw
// that surfaced as a Node stack trace inside a gofmt-and-links preflight,
// which says nothing about the actual problem.
function latestTag() {
  try {
    const out = execFileSync('gh', [
      'release', 'view', '--repo', 'DigitalArsenal/space-data-network',
      '--json', 'tagName', '--jq', '.tagName',
    ], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
    return out.trim();
  } catch (error) {
    const stderr = (error.stderr || '').trim();
    console.error('[docs-release] could not read the newest release tag from GitHub.');
    if (stderr) console.error(`[docs-release] gh: ${stderr}`);
    console.error('[docs-release] pass --tag <vX.Y.Z-beta.N> to check against a known tag,');
    console.error('[docs-release] or give the step a token (GH_TOKEN: ${{ github.token }}).');
    process.exit(1);
  }
}

export function rewrite(text, tag) {
  const bare = tag.replace(/^v/, '');
  const dotted = bare.replace('-beta.', '.beta.');
  return text.replace(RELEASE_LITERAL, (m) => {
    if (m.startsWith('v')) return tag;
    return m.includes('.beta.') ? dotted : bare;
  });
}

const args = process.argv.slice(2);
const check = args.includes('--check');
const tagFlag = args.indexOf('--tag');
const tag = tagFlag !== -1 ? args[tagFlag + 1] : latestTag();

if (!/^v\d+\.\d+\.\d+-beta\.\d+$/.test(tag)) {
  console.error(`[docs-release] not a release tag: ${tag}`);
  process.exit(1);
}

let stale = [];
for (const rel of FILES) {
  const path = resolve(ROOT, rel);
  const before = readFileSync(path, 'utf8');
  const after = rewrite(before, tag);
  if (before === after) continue;
  if (check) {
    const found = [...new Set(before.match(RELEASE_LITERAL) ?? [])].join(', ');
    stale.push(`${rel} (has ${found}, newest release is ${tag})`);
  } else {
    writeFileSync(path, after);
    console.log(`[docs-release] ${rel} -> ${tag}`);
  }
}

// EVERY DOWNLOAD LINK MUST NAME AN ASSET THAT EXISTS.
// The container link read 1.0.5-beta.N while the asset is 1.0.5.beta.N, so it
// 404'd from the day it was written — a version sync alone would have kept it
//404ing at a newer version. Rewriting a literal is not the same as checking it.
if (check) {
  const names = new Set(JSON.parse(execFileSync('gh', [
    'release', 'view', tag, '--repo', 'DigitalArsenal/space-data-network',
    '--json', 'assets',
  ], { encoding: 'utf8' })).assets.map((a) => a.name));
  const broken = [];
  for (const rel of FILES) {
    const text = readFileSync(resolve(ROOT, rel), 'utf8');
    for (const m of text.matchAll(/releases\/download\/[^/"']+\/([^"')\s]+)/g)) {
      if (!names.has(m[1])) broken.push(`${rel}: ${m[1]}`);
    }
  }
  if (broken.length > 0) {
    console.error(`[docs-release] the site links assets that are not in ${tag}:`);
    for (const b of broken) console.error(`    - ${b}`);
    process.exit(1);
  }
}

if (check && stale.length > 0) {
  console.error('[docs-release] the published site advertises a release that is not the newest:');
  for (const s of stale) console.error(`    - ${s}`);
  console.error('Run: node scripts/sync-docs-release.mjs');
  process.exit(1);
}
console.log(check ? `[docs-release] site matches ${tag}` : `[docs-release] synced to ${tag}`);

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
const DOCKER_IMAGE = 'dockerdigitalarsenal/space-data-network';
// A Docker tag is [A-Za-z0-9_][A-Za-z0-9_.-]{0,127}; \S+ would swallow the
// closing </span> in the HTML and report a tag nobody wrote.
const TAG_CHARS = '[A-Za-z0-9_][A-Za-z0-9_.-]*';

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
    console.error('[docs-release] pass --tag <vX.Y.Z or vX.Y.Z-beta.N> to check against a known tag,');
    console.error('[docs-release] or give the step a token (GH_TOKEN: ${{ github.token }}).');
    process.exit(1);
  }
}

// A release tag is a beta (v1.0.5-beta.70) or, since v1.1.0, a stable vX.Y.Z.
export function isReleaseTag(tag) {
  return /^v\d+\.\d+\.\d+(?:-beta\.\d+)?$/.test(String(tag));
}

// A STABLE release has no "beta" to be found by, and a bare X.Y.Z is not
// necessarily ours: the site also names WasmEdge 0.16.4 and module versions.
// So a stable version counts as advertised only where nothing but a release
// can stand — a release download URL or the Docker image tag — and every
// spelling of exactly that version is then rewritten. Boundaries keep it off
// longer numbers (11.1.0, 1.1.0.2) and off a beta of the same X.Y.Z, which
// RELEASE_LITERAL owns.
const STABLE_ANCHORS = [
  /\/releases\/download\/v(\d+\.\d+\.\d+)\//g,
  new RegExp(`${DOCKER_IMAGE.replace(/[/.]/g, '\\$&')}:v?(\\d+\\.\\d+\\.\\d+)(?![0-9A-Za-z_.-])`, 'g'),
];

export function advertisedStableVersions(text) {
  const versions = new Set();
  for (const anchor of STABLE_ANCHORS) {
    for (const m of text.matchAll(anchor)) versions.add(m[1]);
  }
  return [...versions];
}

function stableLiteral(versions) {
  const alternatives = versions.map((v) => v.replace(/\./g, '\\.')).join('|');
  return new RegExp(`(?<![0-9A-Za-z.])v?(?:${alternatives})(?!\\d|\\.\\d|[-.]beta)`, 'g');
}

// Every release literal the site carries, for the --check report.
export function releaseLiterals(text) {
  const stable = advertisedStableVersions(text);
  return [...new Set([
    ...(text.match(RELEASE_LITERAL) ?? []),
    ...(stable.length ? text.match(stableLiteral(stable)) ?? [] : []),
  ])];
}

export function rewrite(text, tag) {
  const bare = tag.replace(/^v/, '');
  const dotted = bare.replace('-beta.', '.beta.');
  let versioned = text.replace(RELEASE_LITERAL, (m) => {
    if (m.startsWith('v')) return tag;
    return m.includes('.beta.') ? dotted : bare;
  });
  const stable = advertisedStableVersions(text);
  if (stable.length) {
    // A stable version is spelled the same with dashes or dots, so the
    // container asset (the one dotted name the site links) is told apart by
    // its prefix: a beta written there must be dotted again.
    versioned = versioned.replace(stableLiteral(stable), (m, offset, whole) => {
      if (m.startsWith('v')) return tag;
      return whole.slice(Math.max(0, offset - 'container-'.length), offset) === 'container-' ? dotted : bare;
    });
  }
  // A FOURTH spelling, and the only one that is not ours to choose: the Docker
  // image tag is whatever the registry actually holds, and that is the
  // v-prefixed one. index.html and onboarding.html were authored without the
  // v, so `docker run ...:1.0.5-beta.69` on the front page has always failed
  // with "manifest unknown" — the version sync faithfully kept the wrong
  // spelling current. Anchored on the image name so it cannot touch anything
  // else.
  return versioned.replace(
    new RegExp(`(${DOCKER_IMAGE.replace(/[/.]/g, '\\$&')}:)${TAG_CHARS}`, 'g'),
    `$1${tag}`,
  );
}

// AND EVERY DOCKER TAG MUST EXIST IN THE REGISTRY.
// Same defect class as the asset links, different registry: the site's
// `docker run` line is copied and pasted by hand, so a tag that resolves to
// nothing is a broken front page. An anonymous pull token is enough to list
// tags.
async function dockerTags() {
  const auth = await fetch(
    `https://auth.docker.io/token?service=registry.docker.io&scope=repository:${DOCKER_IMAGE}:pull`,
  );
  if (!auth.ok) throw new Error(`docker auth ${auth.status}`);
  const { token } = await auth.json();
  const res = await fetch(`https://registry-1.docker.io/v2/${DOCKER_IMAGE}/tags/list`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!res.ok) throw new Error(`docker tags ${res.status}`);
  return new Set((await res.json()).tags ?? []);
}

async function main() {
  const args = process.argv.slice(2);
  const check = args.includes('--check');
  const tagFlag = args.indexOf('--tag');
  const tag = tagFlag !== -1 ? args[tagFlag + 1] : latestTag();

  if (!isReleaseTag(tag)) {
    console.error(`[docs-release] not a release tag: ${tag}`);
    process.exit(1);
  }

  const stale = [];
  for (const rel of FILES) {
    const path = resolve(ROOT, rel);
    const before = readFileSync(path, 'utf8');
    const after = rewrite(before, tag);
    if (before === after) continue;
    if (check) {
      const found = releaseLiterals(before).join(', ');
      stale.push(`${rel} (has ${found}, newest release is ${tag})`);
    } else {
      writeFileSync(path, after);
      console.log(`[docs-release] ${rel} -> ${tag}`);
    }
  }

  // EVERY DOWNLOAD LINK MUST NAME AN ASSET THAT EXISTS.
  // The container link read 1.0.5-beta.N while the asset is 1.0.5.beta.N, so it
  // 404'd from the day it was written — a version sync alone would have kept it
  // 404ing at a newer version. Rewriting a literal is not the same as checking it.
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

  if (check) {
    let tags;
    try {
      tags = await dockerTags();
    } catch (error) {
      console.error(`[docs-release] could not list Docker Hub tags: ${error.message}`);
      process.exit(1);
    }
    const missing = [];
    for (const rel of FILES) {
      const text = readFileSync(resolve(ROOT, rel), 'utf8');
      for (const m of text.matchAll(new RegExp(`${DOCKER_IMAGE.replace(/[/.]/g, '\\$&')}:(${TAG_CHARS})`, 'g'))) {
        if (!tags.has(m[1])) missing.push(`${rel}: ${DOCKER_IMAGE}:${m[1]}`);
      }
    }
    if (missing.length > 0) {
      console.error('[docs-release] the site names Docker tags that do not exist:');
      for (const m of missing) console.error(`    - ${m}`);
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
}

// Run as a script only: the tests import rewrite() without asking GitHub.
if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  await main();
}
